package chokepoint_test

import (
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosedResearchReceivesValidatedUpstreamResults(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload)

	for _, staleInstruction := range []string{
		"Read both baseline artifacts",
		"Read every artifact",
		"Read the map and relevant cards",
	} {
		assert.NotContains(t, configuration, staleInstruction)
	}
	assert.GreaterOrEqual(t, strings.Count(configuration, ".result"), 10,
		"closed Research prompts must receive validated upstream data, not only artifact paths")
}

func TestDecisionWorkflowReplacesReconciliationAndReportManifest(t *testing.T) {
	t.Parallel()
	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("main.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)

	stages := map[string]bool{}
	for _, block := range body.Blocks {
		if block.Type == "research" && len(block.Labels) == 2 {
			stages[block.Labels[1]] = true
		}
	}
	for _, required := range []string{
		"primary_source_baseline", "brainstorm", "graph_track", "build_supply_chain",
		"assess_nodes", "prioritize_companies", "synthesize",
	} {
		assert.True(t, stages[required], "required stage %q is missing", required)
	}
	for _, removed := range []string{
		"reconcile_chain_evidence", "select_chokepoints", "discover_candidates",
		"assess_candidates", "reconcile_report_evidence",
	} {
		assert.False(t, stages[removed], "obsolete stage %q must stay removed", removed)
	}
}
