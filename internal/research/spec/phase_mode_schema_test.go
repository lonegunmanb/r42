package spec_test

import (
	"testing"

	"github.com/lonegunmanb/golden"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockPlansCollectionOnlyPhaseMode(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "collect" {}
fixture_tool "submit" {}

research "static" "builder" {
  phase_mode    = "collection_only"
  model         = "model"
  system_prompt = "Collect and calculate."

  collection_tool_ids = [fixture_tool.collect.id]
  collection_skills   = ["dcf-model"]

  tool_use "submit" {
    tool_id   = fixture_tool.submit.id
    terminate = true
  }
}
`)

	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.ResearchBlock](config)[0]
	planned := block.ResearchConfig()
	assert.Equal(t, researchspec.PhaseModeCollectionOnly, planned.EffectivePhaseMode())
	assert.False(t, planned.TerminateToolIDSet)
	require.NotNil(t, planned.TerminateToolID)
	assert.Equal(t, "tool_fixture_submit", *planned.TerminateToolID)
	assert.Equal(t, []string{"tool_fixture_collect"}, planned.CollectionToolIDs)
	assert.Equal(t, []string{"dcf-model"}, planned.CollectionSkills)
	assert.Equal(t, "collection_only", block.Values()["phase_mode"].AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsModeFieldCombinations(t *testing.T) {
	registerResearchSchemaBlocks()
	tests := []struct {
		name          string
		source        string
		expectedError string
	}{
		{
			name: "collection only explicit terminal tool",
			source: `
fixture_tool "submit" {}
research "static" "builder" {
  phase_mode        = "collection_only"
  model             = "model"
  system_prompt     = "Collect and calculate."
  terminate_tool_id = fixture_tool.submit.id
  tool_use "submit" {
    tool_id   = fixture_tool.submit.id
    terminate = true
  }
}

`,
			expectedError: "tool_use cannot be combined with tool_ids or terminate_tool_id",
		},
		{
			name: "research only collection settings",
			source: `
fixture_tool "collect" {}
research "static" "juror" {
  phase_mode          = "research_only"
  model               = "model"
  system_prompt       = "Review frozen data."
  collection_tool_ids = [fixture_tool.collect.id]
}

`,
			expectedError: "research_only forbids collection_tool_ids",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseResearchConfig(t, tt.source)
			assert.ErrorContains(t, config.RunPlan(), tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestDeferredStaticResearchKeepsUnknownPhaseMode(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "finish" {}

research "static" "baseline" {
  model             = "model"
  system_prompt     = "baseline"
  terminate_tool_id = fixture_tool.finish.id
}

research "static" "downstream" {
  phase_mode    = research.static.baseline.result
  model         = "model"
  system_prompt = "downstream"
}
`)

	require.NoError(t, config.RunPlan())
	value := config.EvalContext().Variables["research"].GetAttr("static").GetAttr("downstream")
	assert.False(t, value.GetAttr("phase_mode").IsKnown())
}
