package examples_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestExamplesOptionalS3RunUpload(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(entry.Name(), "main.r42.hcl")); os.IsNotExist(err) {
			continue
		} else {
			require.NoError(t, err)
		}
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()
			_, err := os.Stat(filepath.Join(entry.Name(), "s3.r42.hcl"))
			require.NoError(t, err)
			for _, test := range []struct {
				name      string
				config    string
				endpoint  string
				wantError bool
			}{
				{name: "default disabled"},
				{name: "explicit null", config: "null"},
				{name: "aws", config: `{ region = "us-east-1", bucket = "reports", prefix = "runs/test" }`},
				{name: "local http", config: `{ region = "us-east-1", bucket = "reports", prefix = "runs/test", endpoint = "http://127.0.0.1:9000", force_path_style = true }`, endpoint: "http://127.0.0.1:9000"},
				{name: "oss", config: `{ region = "cn-hangzhou", bucket = "reports", prefix = "runs/test", endpoint = "https://oss-cn-hangzhou.aliyuncs.com", access_key_ref = "S3_TEST_ACCESS", secret_key_ref = "S3_TEST_SECRET" }`, endpoint: "https://oss-cn-hangzhou.aliyuncs.com"},
				{name: "invalid enabled config", config: `{ region = "", bucket = "reports", prefix = "runs/test" }`, wantError: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					t.Parallel()
					directory := t.TempDir()
					require.NoError(t, os.CopyFS(directory, os.DirFS(entry.Name())))
					variables := []golden.CliFlagAssignedVariables{}
					for key, value := range exampleS3Variables(entry.Name()) {
						variables = append(variables, golden.NewCliFlagAssignedVariable(key, value))
					}
					if test.config != "" {
						variables = append(variables, golden.NewCliFlagAssignedVariable("s3", test.config))
					}
					config, err := cli.NewRuntime().Config(directory, executor.ResearchConfigOptions{
						Context: t.Context(), Variables: variables,
					})
					require.NoError(t, err)
					planned, err := executor.RunResearchPlan(config)
					if test.wantError {
						require.ErrorContains(t, err, "region is required")
						return
					}
					require.NoError(t, err)
					_, hasS3ReportPrefix := planned.SavedPlan().Outputs()["report_s3_prefix"]
					assert.True(t, hasS3ReportPrefix, "root output report_s3_prefix is required for all examples")
					var upload *plan.NodeSpec
					work := []string{}
					for _, node := range planned.SavedPlan().Nodes() {
						if node.Kind == "s3_folder" {
							require.Nil(t, upload, "each example uploads its run once")
							upload = &node
						} else {
							work = append(work, node.Address)
						}
					}
					providers := planned.SavedPlan().Context()["s3_provider"].GetAttr("runs")
					if test.config == "" || test.config == "null" {
						assert.Nil(t, upload)
						assert.Zero(t, providers.LengthInt())
						return
					}
					require.NotNil(t, upload)
					assert.Equal(t, "s3_folder.runs[current]", upload.Address)
					assert.Equal(t, 1, providers.LengthInt())
					assert.ElementsMatch(t, work, upload.Dependencies)
					provider, folder, err := s3spec.DecodeFolderPlan(upload.Config)
					require.NoError(t, err)
					assert.Equal(t, test.endpoint, provider.Endpoint)
					assert.Equal(t, "reports", folder.Bucket)
					assert.Equal(t, "runs/test", folder.Prefix)
					assert.Equal(t, filepath.ToSlash(planned.SavedPlan().RunDirectory()), folder.Source)
					assert.Empty(t, folder.Exclude)
				})
			}
		})
	}
}

func exampleS3Variables(name string) map[string]string {
	variables := map[string]string{}
	switch name {
	case "deep-research", "chokepoint":
		variables["topic"] = `"test topic"`
		variables["model_provider"] = "{}"
		variables["qc_model_provider"] = "{}"
		if name == "chokepoint" {
			variables["as_of_date"] = `"2026-10-04"`
		}
	case "secjury":
		variables["target"] = `"MSFT"`
		variables["valuation_date"] = `"2026-10-04"`
		variables["model_provider"] = "{}"
	case "morning":
		variables["model_provider"] = "{}"
		variables["qc_model_provider"] = "{}"
	}
	return variables
}
