package cli_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRuntimeRunWorkingDirectoryIsSharedAndPersisted(t *testing.T) {
	t.Parallel()
	for _, customRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom root %t", customRoot), func(t *testing.T) {
			t.Parallel()
			directory := writeTerminalToolFixture(t)
			child := filepath.Join(directory, "child")
			require.NoError(t, os.Mkdir(child, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(child, "main.r42.hcl"), []byte(`
output "run" { value = run_wd() }
`), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "paths.r42.hcl"), []byte(`
module "child" { source = "./child" }
locals { final_path = "${run_wd()}/${research.static.source.result}" }
output "run" { value = run_wd() }
output "child_run" { value = module.child.run }
output "final_path" { value = local.final_path }
research "dynamic" "followup" {
  tasks = [for value in [research.static.source.result] : {
    model = "test-model"
    system_prompt = run_wd()
    prompt = value
    terminate_tool_id = go_tool.finish.id
    artifact = {}
    retry = null
    qc = null
  }]
}
output "dynamic_runs" { value = [for task in research.dynamic.followup.tasks : task.system_prompt] }
`), 0o600))
			runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &toolCallingOpener{}})
			options := executor.ResearchConfigOptions{Context: t.Context()}
			if customRoot {
				options.RunDirectory = t.TempDir()
			}
			config, err := runtime.Config(directory, options)
			require.NoError(t, err)
			result, err := executor.RunResearchPlan(config)
			require.NoError(t, err)
			planned := result.SavedPlan()
			expected := filepath.ToSlash(planned.RunDirectory())
			assert.True(t, filepath.IsAbs(filepath.FromSlash(expected)))
			assert.NotContains(t, expected, `\`)
			assert.NoDirExists(t, planned.RunDirectory())
			assert.Equal(t, expected, planned.Outputs()["run"].Value.AsString())
			assert.Equal(t, expected, planned.Outputs()["child_run"].Value.AsString())
			savedPath := filepath.Join(t.TempDir(), "saved.r42plan")
			_, err = plan.Save(savedPath, planned)
			require.NoError(t, err)
			planned, err = plan.Load(savedPath)
			require.NoError(t, err)
			applied, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})
			require.NoError(t, err)
			assert.Equal(t, expected, applied.Outputs["run"].AsString())
			assert.Equal(t, expected, applied.Outputs["child_run"].AsString())
			assert.Equal(t, expected+"/done", applied.Outputs["final_path"].AsString())
			assert.True(t, applied.Outputs["dynamic_runs"].RawEquals(cty.TupleVal([]cty.Value{cty.StringVal(expected)})))
		})
	}
}

func TestRuntimeRunWorkingDirectoryRejectsArguments(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
output "run" { value = run_wd("unexpected") }
`), 0o600))
	_, err := planRuntime(cli.NewRuntime(), t.Context(), directory, nil)
	require.ErrorContains(t, err, "Too many function arguments")
}

func TestBasicExampleOptionalS3UploadAfterSuccess(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		enabled      bool
		failResearch bool
	}{
		{name: "disabled"},
		{name: "enabled", enabled: true},
		{name: "failed research", enabled: true, failResearch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			require.NoError(t, os.CopyFS(directory, os.DirFS("../../docs/examples/basic")))
			opener := &resumeWorkflowOpener{failResearch: test.failResearch}
			client := &runtimeS3Client{}
			var clientCalls atomic.Int64
			runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{
				Sessions: opener,
				S3ServiceFactory: func(*aws.Config) (internals3.Client, error) {
					clientCalls.Add(1)
					return client, nil
				},
			})
			variables := []golden.CliFlagAssignedVariables{}
			if test.enabled {
				variables = append(variables, golden.NewCliFlagAssignedVariable("s3",
					`{ region = "us-east-1", bucket = "reports", prefix = "runs/test" }`))
			}
			planned, err := planRuntime(runtime, t.Context(), directory, variables)
			require.NoError(t, err)
			nested := filepath.Join(planned.RunDirectory(), "nested")
			require.NoError(t, os.MkdirAll(nested, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(nested, "report.md"), []byte("report"), 0o600))
			_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})
			if test.failResearch {
				require.ErrorContains(t, err, "forced research failure")
			} else {
				require.NoError(t, err)
			}
			if !test.enabled || test.failResearch {
				assert.Zero(t, clientCalls.Load())
				assert.Empty(t, client.Keys())
				return
			}
			assert.Equal(t, int64(1), clientCalls.Load())
			assert.Equal(t, 1, opener.calls("research"))
			assert.Contains(t, client.Keys(), "runs/test/nested/report.md")
			assert.Contains(t, client.Keys(), "runs/test/saved-plan.r42plan")
			manifests, err := filepath.Glob(filepath.Join(planned.RunDirectory(), "unit-checkpoints", "*", "checkpoints", "*", "manifest.json"))
			require.NoError(t, err)
			require.NotEmpty(t, manifests)
			for _, manifest := range manifests {
				relative, err := filepath.Rel(planned.RunDirectory(), manifest)
				require.NoError(t, err)
				assert.Contains(t, client.Keys(), "runs/test/"+filepath.ToSlash(relative))
			}
		})
	}
}

func TestRuntimeS3ProviderForEachReferences(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		items string
		keys  []string
	}{
		{name: "disabled", items: "{}", keys: []string{}},
		{name: "single", items: `{ west = "us-west-2" }`, keys: []string{"west"}},
		{name: "multiple", items: `{ west = "us-west-2", east = "us-east-1" }`, keys: []string{"east", "west"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			source := fmt.Sprintf(`
variable "destinations" {
  type = map(string)
  default = %s
}
s3_provider "upload" {
  for_each = var.destinations
  region = each.value
}
s3_folder "upload" {
  for_each = var.destinations
  provider = s3_provider.upload[each.key]
  bucket = "bucket"
  source = "."
  prefix = each.key
}
output "providers" { value = s3_provider.upload }
`, test.items)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(source), 0o600))
			planned, err := planRuntime(cli.NewRuntime(), t.Context(), directory, nil)
			require.NoError(t, err)
			assert.Len(t, planned.Nodes(), len(test.keys))
			assert.Len(t, planned.Outputs()["providers"].Value.AsValueMap(), len(test.keys))
			for _, key := range test.keys {
				value := planned.Outputs()["providers"].Value.GetAttr(key)
				assert.Equal(t, "s3_provider.upload["+key+"]", value.GetAttr("address").AsString())
			}
			for _, node := range planned.Nodes() {
				provider, folder, err := s3spec.DecodeFolderPlan(node.Config)
				require.NoError(t, err)
				assert.Equal(t, planned.Outputs()["providers"].Value.GetAttr(folder.Prefix).GetAttr("region").AsString(), provider.Region)
			}
		})
	}
}

func TestRuntimeAppliesSavedS3FolderForEach(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
s3_provider "upload" { region = "us-east-1" }
s3_folder "upload" {
  for_each = toset(["first", "second"])
  provider = s3_provider.upload
  bucket = "bucket"
  source = each.key
  prefix = each.key
}
output "uploaded" { value = { for key, folder in s3_folder.upload : key => folder.result } }
`), 0o600))
	client := &runtimeS3Client{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{
		S3ServiceFactory: func(*aws.Config) (internals3.Client, error) { return client, nil },
	})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
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
	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"first/result.txt", "second/result.txt"}, client.Keys())
	for _, name := range []string{"first", "second"} {
		assert.Equal(t, "s3://bucket/"+name, result.Outputs["uploaded"].GetAttr(name).GetAttr("root").AsString())
	}
}

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
