package s3_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadFilesStreamsFilesWithNormalizedObjectKeysAndVersions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := writeSourceFile(t, root, "a.txt", "alpha")
	second := writeSourceFile(t, root, "nested/b.txt", "beta")
	client := &fakeClient{put: func(input *awss3.PutObjectInput) (*awss3.PutObjectOutput, error) {
		file, ok := input.Body.(*os.File)
		require.True(t, ok, "uploads must stream an open file")
		contents, err := io.ReadAll(file)
		require.NoError(t, err)
		return &awss3.PutObjectOutput{VersionId: aws.String("version-" + string(contents))}, nil
	}}
	uploaded, err := internals3.UploadFiles(t.Context(), client, "bucket", "result/2026", []s3spec.SourceFile{first, second}, internals3.RetryPolicy{})
	require.NoError(t, err)
	assert.Equal(t, []string{"result/2026/a.txt", "result/2026/nested/b.txt"}, client.keys)
	assert.Equal(t, []internals3.UploadedObject{{Key: "result/2026/a.txt", VersionID: "version-alpha"}, {Key: "result/2026/nested/b.txt", VersionID: "version-beta"}}, uploaded)
}

func TestUploadFilesStopsAtFirstFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := writeSourceFile(t, root, "first.txt", "first")
	second := writeSourceFile(t, root, "second.txt", "second")
	client := &fakeClient{put: func(input *awss3.PutObjectInput) (*awss3.PutObjectOutput, error) {
		if aws.StringValue(input.Key) == "first.txt" {
			return nil, errors.New("put failed")
		}
		return &awss3.PutObjectOutput{}, nil
	}}
	_, err := internals3.UploadFiles(t.Context(), client, "bucket", "", []s3spec.SourceFile{first, second}, internals3.RetryPolicy{})
	require.Error(t, err)
	require.ErrorContains(t, err, "first.txt")
	assert.Equal(t, []string{"first.txt"}, client.keys)
}

func TestUploadFilesRejectsEmptySuccessResponse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := writeSourceFile(t, root, "file.txt", "contents")
	client := &fakeClient{put: func(*awss3.PutObjectInput) (*awss3.PutObjectOutput, error) { return nil, nil }}
	_, err := internals3.UploadFiles(t.Context(), client, "bucket", "", []s3spec.SourceFile{file}, internals3.RetryPolicy{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "empty response")
}

func TestUploadFilesWithOptionsCompletesMultipartUpload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := writeSourceFile(t, root, "large.bin", "abcdef")
	client := &fakeClient{
		create: func(*awss3.CreateMultipartUploadInput) (*awss3.CreateMultipartUploadOutput, error) {
			return &awss3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPart: func(input *awss3.UploadPartInput) (*awss3.UploadPartOutput, error) {
			return &awss3.UploadPartOutput{ETag: aws.String(fmt.Sprintf("etag-%d", aws.Int64Value(input.PartNumber)))}, nil
		},
		complete: func(input *awss3.CompleteMultipartUploadInput) (*awss3.CompleteMultipartUploadOutput, error) {
			require.Len(t, input.MultipartUpload.Parts, 3)
			return &awss3.CompleteMultipartUploadOutput{VersionId: aws.String("version")}, nil
		},
	}
	uploaded, err := internals3.UploadFilesWithOptions(t.Context(), client, "bucket", "prefix", []s3spec.SourceFile{file}, internals3.RetryPolicy{}, internals3.UploadOptions{MultipartThreshold: 1, PartSize: 2})
	require.NoError(t, err)
	assert.Equal(t, []internals3.UploadedObject{{Key: "prefix/large.bin", VersionID: "version"}}, uploaded)
	assert.Equal(t, 3, client.partUploads)
	assert.Zero(t, client.aborts)
}

func TestUploadFilesWithOptionsAbortsMultipartAndPreservesAbortError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := writeSourceFile(t, root, "large.bin", "abcdef")
	client := &fakeClient{
		create: func(*awss3.CreateMultipartUploadInput) (*awss3.CreateMultipartUploadOutput, error) {
			return &awss3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPart: func(*awss3.UploadPartInput) (*awss3.UploadPartOutput, error) { return nil, errors.New("part failed") },
		abort: func(*awss3.AbortMultipartUploadInput) (*awss3.AbortMultipartUploadOutput, error) {
			return nil, errors.New("abort failed")
		},
	}
	_, err := internals3.UploadFilesWithOptions(t.Context(), client, "bucket", "", []s3spec.SourceFile{file}, internals3.RetryPolicy{}, internals3.UploadOptions{MultipartThreshold: 1, PartSize: 2})
	require.Error(t, err)
	require.ErrorContains(t, err, "part failed")
	require.ErrorContains(t, err, "abort failed")
	assert.Equal(t, 1, client.aborts)
}

func TestUploadFilesWithOptionsAbortsMultipartAfterCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := writeSourceFile(t, root, "large.bin", "abcdef")
	ctx, cancel := context.WithCancel(t.Context())
	client := &fakeClient{
		create: func(*awss3.CreateMultipartUploadInput) (*awss3.CreateMultipartUploadOutput, error) {
			cancel()
			return &awss3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPart: func(*awss3.UploadPartInput) (*awss3.UploadPartOutput, error) {
			t.Fatal("canceled upload must not start a new part")
			return nil, nil
		},
		abort: func(*awss3.AbortMultipartUploadInput) (*awss3.AbortMultipartUploadOutput, error) {
			return &awss3.AbortMultipartUploadOutput{}, nil
		},
	}
	_, err := internals3.UploadFilesWithOptions(ctx, client, "bucket", "", []s3spec.SourceFile{file}, internals3.RetryPolicy{}, internals3.UploadOptions{MultipartThreshold: 1, PartSize: 2})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, client.aborts)
}

func TestUploadFilesWithOptionsRejectsEmptyMultipartResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configure  func(*fakeClient)
		want       string
		abortCount int
	}{
		{name: "create", want: "create multipart upload returned an empty response", abortCount: 0, configure: func(client *fakeClient) {
			client.create = func(*awss3.CreateMultipartUploadInput) (*awss3.CreateMultipartUploadOutput, error) { return nil, nil }
		}},
		{name: "part", want: "upload part returned an empty response", abortCount: 1, configure: func(client *fakeClient) {
			client.uploadPart = func(*awss3.UploadPartInput) (*awss3.UploadPartOutput, error) { return nil, nil }
		}},
		{name: "complete", want: "complete multipart upload returned an empty response", abortCount: 1, configure: func(client *fakeClient) {
			client.complete = func(*awss3.CompleteMultipartUploadInput) (*awss3.CompleteMultipartUploadOutput, error) {
				return nil, nil
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			file := writeSourceFile(t, root, "large.bin", "ab")
			client := multipartSuccessClient()
			tt.configure(client)
			_, err := internals3.UploadFilesWithOptions(t.Context(), client, "bucket", "", []s3spec.SourceFile{file}, internals3.RetryPolicy{}, internals3.UploadOptions{MultipartThreshold: 1, PartSize: 2})
			require.Error(t, err)
			require.ErrorContains(t, err, tt.want)
			assert.Equal(t, tt.abortCount, client.aborts)
		})
	}
}

func multipartSuccessClient() *fakeClient {
	return &fakeClient{
		create: func(*awss3.CreateMultipartUploadInput) (*awss3.CreateMultipartUploadOutput, error) {
			return &awss3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPart: func(*awss3.UploadPartInput) (*awss3.UploadPartOutput, error) {
			return &awss3.UploadPartOutput{ETag: aws.String("etag")}, nil
		},
		complete: func(*awss3.CompleteMultipartUploadInput) (*awss3.CompleteMultipartUploadOutput, error) {
			return &awss3.CompleteMultipartUploadOutput{}, nil
		},
		abort: func(*awss3.AbortMultipartUploadInput) (*awss3.AbortMultipartUploadOutput, error) {
			return &awss3.AbortMultipartUploadOutput{}, nil
		},
	}
}

func writeSourceFile(t *testing.T, root, relative, contents string) s3spec.SourceFile {
	t.Helper()
	path := filepath.Join(root, relative)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return s3spec.SourceFile{AbsolutePath: path, RelativePath: filepath.ToSlash(relative)}
}

type fakeClient struct {
	keys        []string
	versioning  func(*awss3.GetBucketVersioningInput) (*awss3.GetBucketVersioningOutput, error)
	put         func(*awss3.PutObjectInput) (*awss3.PutObjectOutput, error)
	create      func(*awss3.CreateMultipartUploadInput) (*awss3.CreateMultipartUploadOutput, error)
	uploadPart  func(*awss3.UploadPartInput) (*awss3.UploadPartOutput, error)
	complete    func(*awss3.CompleteMultipartUploadInput) (*awss3.CompleteMultipartUploadOutput, error)
	abort       func(*awss3.AbortMultipartUploadInput) (*awss3.AbortMultipartUploadOutput, error)
	delete      func(*awss3.DeleteObjectInput) (*awss3.DeleteObjectOutput, error)
	partUploads int
	aborts      int
	deleted     []string
}

func (f *fakeClient) GetBucketVersioningWithContext(_ aws.Context, input *awss3.GetBucketVersioningInput, _ ...request.Option) (*awss3.GetBucketVersioningOutput, error) {
	return f.versioning(input)
}

func (f *fakeClient) PutObjectWithContext(_ aws.Context, input *awss3.PutObjectInput, _ ...request.Option) (*awss3.PutObjectOutput, error) {
	f.keys = append(f.keys, aws.StringValue(input.Key))
	return f.put(input)
}

func (f *fakeClient) CreateMultipartUploadWithContext(_ aws.Context, input *awss3.CreateMultipartUploadInput, _ ...request.Option) (*awss3.CreateMultipartUploadOutput, error) {
	return f.create(input)
}

func (f *fakeClient) UploadPartWithContext(_ aws.Context, input *awss3.UploadPartInput, _ ...request.Option) (*awss3.UploadPartOutput, error) {
	f.partUploads++
	return f.uploadPart(input)
}

func (f *fakeClient) CompleteMultipartUploadWithContext(_ aws.Context, input *awss3.CompleteMultipartUploadInput, _ ...request.Option) (*awss3.CompleteMultipartUploadOutput, error) {
	return f.complete(input)
}

func (f *fakeClient) AbortMultipartUploadWithContext(_ aws.Context, input *awss3.AbortMultipartUploadInput, _ ...request.Option) (*awss3.AbortMultipartUploadOutput, error) {
	f.aborts++
	return f.abort(input)
}

func (f *fakeClient) DeleteObjectWithContext(_ aws.Context, input *awss3.DeleteObjectInput, _ ...request.Option) (*awss3.DeleteObjectOutput, error) {
	f.deleted = append(f.deleted, aws.StringValue(input.Key)+"@"+aws.StringValue(input.VersionId))
	return f.delete(input)
}

var _ internals3.Client = (*fakeClient)(nil)
