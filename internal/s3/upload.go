package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
)

const (
	defaultMultipartThreshold int64 = 8 * 1024 * 1024
	defaultMultipartPartSize  int64 = 8 * 1024 * 1024
	multipartCleanupTimeout         = 30 * time.Second
)

type UploadOptions struct {
	MultipartThreshold int64
	PartSize           int64
}

func DefaultUploadOptions() UploadOptions {
	return UploadOptions{MultipartThreshold: defaultMultipartThreshold, PartSize: defaultMultipartPartSize}
}

func (o UploadOptions) validate() error {
	if o.MultipartThreshold <= 0 || o.PartSize <= 0 {
		return errors.New("S3 multipart threshold and part size must be positive")
	}
	return nil
}

type UploadedObject struct {
	Key       string
	VersionID string
}

func UploadFiles(
	ctx context.Context,
	client Client,
	bucket string,
	prefix string,
	files []s3spec.SourceFile,
	retry RetryPolicy,
) ([]UploadedObject, error) {
	return UploadFilesWithOptions(ctx, client, bucket, prefix, files, retry, DefaultUploadOptions())
}

func UploadFilesWithOptions(
	ctx context.Context,
	client Client,
	bucket string,
	prefix string,
	files []s3spec.SourceFile,
	retry RetryPolicy,
	options UploadOptions,
) ([]UploadedObject, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	uploaded := make([]UploadedObject, 0, len(files))
	for _, file := range files {
		key, err := ObjectKey(prefix, file.RelativePath)
		if err != nil {
			return uploaded, err
		}
		versionID, err := uploadFile(ctx, client, bucket, key, file.AbsolutePath, retry, options)
		if err != nil {
			return uploaded, fmt.Errorf("upload object %q: %w", key, err)
		}
		uploaded = append(uploaded, UploadedObject{Key: key, VersionID: versionID})
	}
	return uploaded, nil
}

func ObjectKey(prefix, relativePath string) (string, error) {
	relativePath = strings.TrimPrefix(path.Clean(strings.ReplaceAll(relativePath, `\`, "/")), "./")
	if relativePath == "." || relativePath == "" || strings.HasPrefix(relativePath, "../") || strings.HasPrefix(relativePath, "/") {
		return "", fmt.Errorf("invalid source relative path %q", relativePath)
	}
	if prefix == "" {
		return relativePath, nil
	}
	return prefix + "/" + relativePath, nil
}

func uploadFile(ctx context.Context, client Client, bucket, key, filename string, retry RetryPolicy, options UploadOptions) (string, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return "", fmt.Errorf("stat source file: %w", err)
	}
	if info.Size() >= options.MultipartThreshold {
		return uploadMultipart(ctx, client, bucket, key, filename, info.Size(), retry, options.PartSize)
	}
	return uploadSinglePut(ctx, client, bucket, key, filename, retry)
}

func uploadSinglePut(ctx context.Context, client Client, bucket, key, filename string, retry RetryPolicy) (string, error) {
	var versionID string
	err := Retry(ctx, retry, func(ctx context.Context) error {
		file, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("open source file: %w", err)
		}
		defer func() { _ = file.Close() }()
		result, err := client.PutObjectWithContext(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: file,
		})
		if err != nil {
			return err
		}
		if result == nil {
			return fmt.Errorf("S3 put object returned an empty response")
		}
		versionID = aws.StringValue(result.VersionId)
		return nil
	})
	return versionID, err
}

func uploadMultipart(ctx context.Context, client Client, bucket, key, filename string, size int64, retry RetryPolicy, partSize int64) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("open source file: %w", err)
	}
	defer func() { _ = file.Close() }()
	var uploadID string
	err = Retry(ctx, retry, func(ctx context.Context) error {
		result, err := client.CreateMultipartUploadWithContext(ctx, &awss3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if err != nil {
			return err
		}
		if result == nil || aws.StringValue(result.UploadId) == "" {
			return errors.New("S3 create multipart upload returned an empty response")
		}
		uploadID = aws.StringValue(result.UploadId)
		return nil
	})
	if err != nil {
		return "", err
	}
	parts := make([]*awss3.CompletedPart, 0, (size+partSize-1)/partSize)
	for partNumber, offset := int64(1), int64(0); offset < size; partNumber, offset = partNumber+1, offset+partSize {
		length := min(partSize, size-offset)
		var eTag string
		err = Retry(ctx, retry, func(ctx context.Context) error {
			body := io.NewSectionReader(file, offset, length)
			result, err := client.UploadPartWithContext(ctx, &awss3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int64(partNumber), Body: body, ContentLength: aws.Int64(length)})
			if err != nil {
				return err
			}
			if result == nil || aws.StringValue(result.ETag) == "" {
				return errors.New("S3 upload part returned an empty response")
			}
			eTag = aws.StringValue(result.ETag)
			return nil
		})
		if err != nil {
			return "", abortMultipart(ctx, client, bucket, key, uploadID, retry, err)
		}
		parts = append(parts, &awss3.CompletedPart{ETag: aws.String(eTag), PartNumber: aws.Int64(partNumber)})
	}
	var versionID string
	err = Retry(ctx, retry, func(ctx context.Context) error {
		result, err := client.CompleteMultipartUploadWithContext(ctx, &awss3.CompleteMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: &awss3.CompletedMultipartUpload{Parts: parts}})
		if err != nil {
			return err
		}
		if result == nil {
			return errors.New("S3 complete multipart upload returned an empty response")
		}
		versionID = aws.StringValue(result.VersionId)
		return nil
	})
	if err != nil {
		return "", abortMultipart(ctx, client, bucket, key, uploadID, retry, err)
	}
	return versionID, nil
}

func abortMultipart(ctx context.Context, client Client, bucket, key, uploadID string, retry RetryPolicy, primary error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), multipartCleanupTimeout)
	defer cancel()
	cleanupErr := Retry(cleanup, retry, func(ctx context.Context) error {
		_, err := client.AbortMultipartUploadWithContext(ctx, &awss3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
		return err
	})
	return errors.Join(primary, cleanupErr)
}
