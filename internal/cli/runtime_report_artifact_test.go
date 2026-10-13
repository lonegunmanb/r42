package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionRuntimeImportsModuleReportArtifactFromSavedPlan(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	producer := filepath.Join(directory, "producer")
	consumer := filepath.Join(directory, "consumer")
	require.NoError(t, os.Mkdir(producer, 0o700))
	require.NoError(t, os.Mkdir(consumer, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(producer, "main.r42.hcl"), []byte(`
research "static" "synthesize" {
  model = "test-model"
  system_prompt = "Produce a report."
  artifact "report" {
    type = "file"
    path = "${block_wd()}/report.md"
    description = "Final report fixture."
    required = true
    non_empty = true
  }
}
output "final_report_artifact" {
  value = research.static.synthesize.artifact.report
}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(consumer, "main.r42.hcl"), []byte(`
variable "previous_report" { type = any }
variable "label" {
  type = string
  default = "follow-up"
}
research "static" "follow_up" {
  model = "test-model"
  system_prompt = "Investigate the imported report."
  import_artifact "previous_report" {
    desc = "Report from the previous module."
    sources = [var.previous_report]
  }
}
output "imported_report" { value = var.previous_report }
output "label" { value = var.label }
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
module "previous" { source = "./producer" }
module "next" {
  source = "./consumer"
  previous_report = module.previous.final_report_artifact
}
output "final_report_artifact" { value = module.previous.final_report_artifact }
output "imported_report" { value = module.next.imported_report }
output "label" { value = module.next.label }
`), 0o600))
	opener := &fakeSessionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	report := planned.Outputs()["final_report_artifact"].Value
	path := report.GetAttr("path").AsString()
	activeRun, err := run.Open(planned.RunDirectory())
	require.NoError(t, err)
	_, err = activeRun.Workspace("module.previous.research.static.synthesize")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("# Final report\n"), 0o600))
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	report = result.Outputs["final_report_artifact"]
	id := report.GetAttr("id").AsString()
	assert.Regexp(t, `^artifact-[0-9a-f-]{36}$`, id)
	assert.Equal(t, filepath.Clean(path), filepath.Clean(report.GetAttr("path").AsString()))
	assert.Equal(t, "artifact", report.GetAttr("kind").AsString())
	assert.True(t, report.RawEquals(result.Outputs["imported_report"]))
	assert.Equal(t, "follow-up", result.Outputs["label"].AsString())
	require.Len(t, opener.configs, 6)
	for _, tool := range opener.configs[5].Tools {
		if tool.Name == "r42_read_artifact" {
			read, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": id, "max_bytes": 1024}})
			require.NoError(t, err)
			assert.Contains(t, read.TextResultForLLM, "# Final report",
				"follow-up Research must be authorized to read the producer's artifact")
			return
		}
	}
	require.Fail(t, "follow-up artifact reader is missing")
}
