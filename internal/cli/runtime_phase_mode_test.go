package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionRuntimeResearchOnlyOpensOnlyResearchSession(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "juror" {
  phase_mode    = "research_only"
  model         = "test-model"
  system_prompt = "Review the frozen DCF."
  prompt        = "Return a verdict."
}
`), 0o600))

	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 1)
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_set_information_needs")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_checkpoint")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_qc_verdict")
	assert.Contains(t, opener.configs[0].SystemPrompt, "closed Research synthesis phase")
	assert.NotContains(t, opener.configs[0].SystemPrompt, "Collection phase")
	assert.Equal(t, 1, opener.research.sendCalls)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Zero(t, opener.collection.sendCalls)
	assert.Zero(t, opener.collectionQC.sendCalls)
}

func TestProductionRuntimeResearchOnlyPreservesFinalQCRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "juror" {
  phase_mode    = "research_only"
  model         = "test-model"
  system_prompt = "Review the frozen DCF."
  prompt        = "Return a verdict."

  qc { criteria = { accuracy = "Be accurate." } }
}
`), 0o600))

	opener := &revisionQCOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.research.sendCalls)
	assert.Equal(t, 2, opener.qc.sendCalls)
	assert.Len(t, opener.research.prompts, 2)
	assert.Contains(t, opener.research.prompts[1], "add a citation")
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
	assert.Zero(t, opener.collection.sendCalls)
	assert.Zero(t, opener.collectionQC.sendCalls)
}

func TestProductionRuntimeDynamicResearchOnlyOpensOnlyResearchSessions(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "jurors" {
  tasks = [for prompt in ["alpha", "beta"] : {
    phase_mode    = "research_only"
    model         = "test-model"
    system_prompt = "Review the frozen DCF."
    prompt        = prompt
    artifact      = {}
    retry         = null
    qc            = null
  }]
}
`), 0o600))

	opener := &dynamicTestOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	configs := opener.Configs()
	require.Len(t, configs, 2)
	for _, config := range configs {
		assert.NotContains(t, toolNamesFromConfig(config), "r42_set_information_needs")
		assert.NotContains(t, toolNamesFromConfig(config), "r42_collection_checkpoint")
		assert.NotContains(t, toolNamesFromConfig(config), "r42_collection_qc_verdict")
	}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, opener.Prompts())
}

func TestProductionRuntimeDynamicResearchOnlyPreservesFinalQCRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "jurors" {
  serial = true
  tasks = [for prompt in ["alpha", "beta"] : {
    phase_mode    = "research_only"
    model         = "test-model"
    system_prompt = "Review the frozen DCF."
    prompt        = prompt
    artifact      = {}
    retry         = null
    qc            = { criteria = { accuracy = "Be accurate." } }
  }]
}
`), 0o600))

	opener := &dynamicQCIssueOpener{finalRounds: map[string]int{}, handoffPrompts: map[string]string{}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.finalRounds, 2)
	for workspace, rounds := range opener.finalRounds {
		assert.Equal(t, 2, rounds)
		assert.Contains(t, opener.handoffPrompts[workspace], "correct the task result")
	}
}
