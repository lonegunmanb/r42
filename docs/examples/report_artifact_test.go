package examples_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExamplesExportFinalReportArtifact(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		artifactName string
	}{
		{name: "deep-research", artifactName: "report"},
		{name: "chokepoint", artifactName: "report"},
		{name: "secjury", artifactName: "report"},
		{name: "morning", artifactName: "report_markdown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			producer := filepath.Join(directory, "producer")
			require.NoError(t, os.Mkdir(producer, 0o700))
			require.NoError(t, os.CopyFS(producer, os.DirFS(test.name)))
			variables := []golden.CliFlagAssignedVariables{}
			inputs := []string{}
			for key, value := range exampleS3Variables(test.name) {
				variables = append(variables, golden.NewCliFlagAssignedVariable(key, value))
				inputs = append(inputs, fmt.Sprintf("  %s = %s", key, value))
			}
			runtime := cli.NewRuntime()
			config, err := runtime.Config(producer, executor.ResearchConfigOptions{
				Context: t.Context(), Variables: variables,
			})
			require.NoError(t, err)
			planned, err := executor.RunResearchPlan(config)
			require.NoError(t, err)
			outputs := planned.SavedPlan().Outputs()
			report, ok := outputs["final_report_artifact"]
			require.True(t, ok, "final_report_artifact must expose the complete final report artifact")
			require.True(t, report.Value.Type().IsObjectType())
			require.Len(t, report.Value.Type().AttributeTypes(), 8)
			assert.Equal(t, test.artifactName, report.Value.GetAttr("name").AsString())
			assert.Equal(t, "artifact", report.Value.GetAttr("kind").AsString())
			assert.Equal(t, "file", report.Value.GetAttr("type").AsString())
			assert.True(t, report.Value.GetAttr("path").RawEquals(outputs["report_path"].Value))
			assert.NotEmpty(t, report.Value.GetAttr("description").AsString())
			assert.True(t, report.Value.GetAttr("required").True())
			assert.True(t, report.Value.GetAttr("non_empty").True())
			assert.False(t, report.Value.GetAttr("id").IsKnown(), "artifact IDs resolve during Apply")

			consumer := fmt.Sprintf(`
module "previous" {
  source = "./producer"
%s
}
research "static" "follow_up" {
  model = "test-model"
  system_prompt = "Research a follow-up question from the imported final report."
  import_artifact "previous_report" {
    desc = "Final report from the previous module."
    sources = [module.previous.final_report_artifact]
  }
}
`, strings.Join(inputs, "\n"))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(consumer), 0o600))
			config, err = runtime.Config(directory, executor.ResearchConfigOptions{Context: t.Context()})
			require.NoError(t, err)
			planned, err = executor.RunResearchPlan(config)
			require.NoError(t, err)
			for _, node := range planned.SavedPlan().Nodes() {
				if node.Address == "research.static.follow_up" {
					assert.Contains(t, node.Dependencies, "module.previous")
					return
				}
			}
			require.Fail(t, "follow-up research node is missing")
		})
	}
}
