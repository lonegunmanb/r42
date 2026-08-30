package cli_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRuntimePlansAndAppliesS3FoldersWithoutExecutingProvider(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
s3_provider "oss" {
  endpoint = "https://oss.example.test"
  region = "cn-hangzhou"
}

s3_folder "first" {
  provider = s3_provider.oss
  bucket = "bucket"
  source = "first"
  prefix = "reports/first"
}

s3_folder "second" {
  provider = s3_provider.oss
  bucket = "bucket"
  source = "second"
  prefix = "reports/second"
  depends_on = [s3_folder.first]
}

output "uploaded" { value = s3_folder.second.result }
`), 0o600))
	client := &runtimeS3Client{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{S3ServiceFactory: func(*aws.Config) (internals3.Client, error) { return client, nil }})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	require.Len(t, planned.Nodes(), 2)
	assert.Equal(t, []string{"s3_folder.first", "s3_folder.second"}, []string{planned.Nodes()[0].Address, planned.Nodes()[1].Address})
	assert.Equal(t, []string{"s3_folder.first"}, planned.Nodes()[1].Dependencies)

	savedPath := filepath.Join(t.TempDir(), "saved.r42plan")
	_, err = plan.Save(savedPath, planned)
	require.NoError(t, err)
	planned, err = plan.Load(savedPath)
	require.NoError(t, err)
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(planned.RunDirectory(), name)
		require.NoError(t, os.MkdirAll(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "result.txt"), []byte(name), 0o600))
	}

	result, err := applyRuntime(runtime, context.Background(), planned, executor.ResearchConfigOptions{Parallelism: 2})
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)
	assert.Equal(t, cty.ObjectVal(map[string]cty.Value{
		"bucket": cty.StringVal("bucket"), "prefix": cty.StringVal("reports/second"),
		"root": cty.StringVal("s3://bucket/reports/second"), "object_count": cty.NumberIntVal(1),
	}), result.Outputs["uploaded"])
	assert.Equal(t, []string{"reports/first/result.txt", "reports/second/result.txt"}, client.Keys())
}

func TestRuntimePlansS3FolderImplicitSourceDependency(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
s3_provider "oss" { region = "cn" }
research "static" "result" {
  model = "model"
  system_prompt = "prompt"
}
s3_folder "upload" {
  provider = s3_provider.oss
  bucket = "bucket"
  source = research.static.result.path
}
`), 0o600))
	planned, err := planRuntime(cli.NewRuntime(), t.Context(), directory, nil)
	require.NoError(t, err)
	dependencies := make(map[string][]string, len(planned.Nodes()))
	for _, node := range planned.Nodes() {
		dependencies[node.Address] = node.Dependencies
	}
	assert.Equal(t, []string{"research.static.result"}, dependencies["s3_folder.upload"])
}

type runtimeS3Client struct {
	mu   sync.Mutex
	keys []string
}

func (c *runtimeS3Client) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.keys...)
}

func (*runtimeS3Client) GetBucketVersioningWithContext(aws.Context, *awss3.GetBucketVersioningInput, ...request.Option) (*awss3.GetBucketVersioningOutput, error) {
	return &awss3.GetBucketVersioningOutput{Status: aws.String(internals3.VersioningEnabled)}, nil
}

func (c *runtimeS3Client) PutObjectWithContext(_ aws.Context, input *awss3.PutObjectInput, _ ...request.Option) (*awss3.PutObjectOutput, error) {
	_, _ = io.ReadAll(input.Body)
	c.mu.Lock()
	c.keys = append(c.keys, aws.StringValue(input.Key))
	c.mu.Unlock()
	return &awss3.PutObjectOutput{VersionId: aws.String("version")}, nil
}

func (*runtimeS3Client) CreateMultipartUploadWithContext(aws.Context, *awss3.CreateMultipartUploadInput, ...request.Option) (*awss3.CreateMultipartUploadOutput, error) {
	return nil, nil
}

func (*runtimeS3Client) UploadPartWithContext(aws.Context, *awss3.UploadPartInput, ...request.Option) (*awss3.UploadPartOutput, error) {
	return nil, nil
}

func (*runtimeS3Client) CompleteMultipartUploadWithContext(aws.Context, *awss3.CompleteMultipartUploadInput, ...request.Option) (*awss3.CompleteMultipartUploadOutput, error) {
	return nil, nil
}

func (*runtimeS3Client) AbortMultipartUploadWithContext(aws.Context, *awss3.AbortMultipartUploadInput, ...request.Option) (*awss3.AbortMultipartUploadOutput, error) {
	return nil, nil
}

func (*runtimeS3Client) DeleteObjectWithContext(aws.Context, *awss3.DeleteObjectInput, ...request.Option) (*awss3.DeleteObjectOutput, error) {
	return nil, nil
}

var _ internals3.Client = (*runtimeS3Client)(nil)
