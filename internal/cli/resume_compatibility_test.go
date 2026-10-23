package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestValidateResumeCompatibilityRejectsChangedDependencies(t *testing.T) {
	t.Parallel()

	saved, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.sources", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.other", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.report", Kind: "research", Dependencies: []string{"research.static.sources"}, Config: cty.EmptyObjectVal},
	}, nil, nil, nil)
	require.NoError(t, err)
	current, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.sources", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.other", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.report", Kind: "research", Dependencies: []string{"research.static.other"}, Config: cty.EmptyObjectVal},
	}, nil, nil, nil)
	require.NoError(t, err)

	err = validateResumeCompatibility(current, saved)

	require.ErrorContains(t, err, "dependencies changed")
}

func TestValidateResumeCompatibilityRejectsChangedArtifactSchema(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeResumeCompatibilityResearch(t, directory, "report.md")
	original := planResearchForResumeCompatibility(t, directory)
	writeResumeCompatibilityResearch(t, directory, "changed.md")
	current := planResearchForResumeCompatibility(t, directory)

	err := validateResumeCompatibility(current, original)

	require.ErrorContains(t, err, "artifact schema changed")
}

func TestDebugResumePlansInOriginalRun(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	runRoot := t.TempDir()
	reservedRun := filepath.Join(runRoot, ".r42", "runs", "run-original")
	writeResumeCompatibilityResearch(t, directory, "${block_wd()}/report.md")
	runtime := NewRuntime()
	originalConfig, err := runtime.Config(directory, executor.ResearchConfigOptions{
		Context: t.Context(), RunDirectory: runRoot, ReservedRunDirectory: reservedRun, Resume: true,
	})
	require.NoError(t, err)
	original, err := executor.RunResearchPlan(originalConfig)
	require.NoError(t, err)

	state := &debugRun{enabled: true}
	t.Cleanup(func() { require.NoError(t, state.close()) })
	debugConfig, err := runtime.Config(directory, executor.ResearchConfigOptions{
		Context: withDebugRun(t.Context(), state), RunDirectory: runRoot,
		ReservedRunDirectory: reservedRun, Resume: true, Debug: true,
	})
	require.NoError(t, err)
	current, err := executor.RunResearchPlan(debugConfig)
	require.NoError(t, err)

	assert.Equal(t, reservedRun, debugConfig.Run().Directory())
	assert.Equal(t, filepath.Join(reservedRun, "events.jsonl"), state.path())
	assert.NoError(t, validateResumeCompatibility(current.SavedPlan(), original.SavedPlan()))
}

func writeResumeCompatibilityResearch(t *testing.T, directory, artifactPath string) {
	t.Helper()
	source := "research \"static\" \"report\" {\n" +
		"  model = \"test-model\"\n" +
		"  system_prompt = \"Current operational prompt.\"\n" +
		"  artifact \"report\" {\n" +
		"    type = \"file\"\n" +
		"    path = \"" + artifactPath + "\"\n" +
		"    description = \"Report\"\n" +
		"  }\n" +
		"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(source), 0o600))
}

func planResearchForResumeCompatibility(t *testing.T, directory string) *plan.Plan {
	t.Helper()
	runtime := NewRuntime()
	config, err := runtime.Config(directory, executor.ResearchConfigOptions{Context: t.Context()})
	require.NoError(t, err)
	planned, err := executor.RunResearchPlan(config)
	require.NoError(t, err)
	return planned.SavedPlan()
}

func TestValidateResumeCompatibilityAcceptsOperationalResearchChanges(t *testing.T) {
	t.Parallel()

	planA, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{{Address: "output.report", Kind: "output", Config: cty.EmptyObjectVal}}, nil, nil, nil)
	require.NoError(t, err)
	planB, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{{Address: "output.report", Kind: "output", Config: cty.StringVal("current prompt and timeout")}}, nil, nil, nil)
	require.NoError(t, err)

	require.NoError(t, validateResumeCompatibility(planB, planA))
}
