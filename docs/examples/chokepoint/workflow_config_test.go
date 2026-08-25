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

func TestSynthesizeReceivesExactFinalizedClaimPathsAndClosedInputQC(t *testing.T) {
	t.Parallel()

	localsPayload, err := os.ReadFile("locals.r42.hcl")
	require.NoError(t, err)
	localsConfiguration := string(localsPayload)
	assert.Contains(t, localsConfiguration, "synthesis_claim_paths")
	assert.Contains(t, localsConfiguration, "research.static.primary_source_baseline.artifact")
	assert.Contains(t, localsConfiguration, "research.dynamic.graph_track.tasks")
	assert.Contains(t, localsConfiguration, "research.dynamic.prioritize_companies.tasks")

	mainPayload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(mainPayload)
	synthesizeStart := strings.Index(configuration, `research "static" "synthesize"`)
	require.NotEqual(t, -1, synthesizeStart)
	synthesize := configuration[synthesizeStart:]
	assert.Contains(t, synthesize, "jsonencode(local.synthesis_claim_paths)")
	assert.Contains(t, synthesize, "finalized claims.json")
	assert.NotContains(t, synthesize, "claim_paths containing every claim file above")
	assert.Contains(t, synthesize, "collection_qc {")
	assert.Contains(t, synthesize, "closed-input synthesis")
	assert.Contains(t, synthesize, "collection_exhausted=true")
}

func TestGraphTracksUseDynamicTasksForDeferredInputs(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload)

	assert.Contains(t, configuration, `research "dynamic" "graph_track"`)
	assert.Contains(t, configuration, "for index, track_key in keys(local.graph_tracks)")
	assert.NotContains(t, configuration, `research "static" "graph_track"`)
	assert.NotContains(t, configuration, "each.key")
	assert.NotContains(t, configuration, "each.value")
	assert.Contains(t, configuration, `Workspace: "${block_wd()}/${index}"`)
	assert.Contains(t, configuration, `path      = "${block_wd()}/${index}/claims.json"`)
	assert.Contains(t, configuration, `path      = "${block_wd()}/${index}/source-registry.json"`)
	assert.GreaterOrEqual(t, strings.Count(configuration, "research.dynamic.graph_track.tasks"), 4)
	assert.NotContains(t, configuration, "research.static.graph_track")
}

func TestClosedResearchPromptsUseAuthorizedArtifactIDs(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload)

	assert.NotContains(t, configuration, "Use r42_list_artifacts")
	assert.GreaterOrEqual(t, strings.Count(configuration, "authorized artifact_id"), 3)
}

func TestTypedToolDescriptionsPublishAllowedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file    string
		tool    string
		field   string
		allowed []string
	}{
		{file: "support_tools.r42.hcl", tool: "register_evidence_source", field: "source_type", allowed: []string{"authoritative_primary", "official_filing", "official_product", "official_statement", "regulator", "qualified_media", "credible_media", "named_media", "peer_reviewed", "industry_research", "other_published", "other", "lead_only", "self_media", "forum", "aggregator"}},
		{file: "support_tools.r42.hcl", tool: "register_evidence_source", field: "reporting_basis", allowed: []string{"public_document", "named_source", "anonymous_sources", "direct_observation", "published_methodology"}},
		{file: "support_tools.r42.hcl", tool: "register_evidence_source", field: "provenance", allowed: []string{"original", "syndication", "aggregation"}},
		{file: "support_tools.r42.hcl", tool: "submit_supply_chain_scope", field: "coverage_items.track", allowed: []string{"product_structure", "manufacturing_testing", "equipment", "materials_chemicals", "qualification_integration"}},
		{file: "decision_tools.r42.hcl", tool: "submit_claim_cards", field: "cards.status", allowed: []string{"confirmed", "reported", "inferred"}},
		{file: "decision_tools.r42.hcl", tool: "submit_node_assessment", field: "risk_scope", allowed: []string{"global", "branch"}},
		{file: "decision_tools.r42.hcl", tool: "submit_node_assessment", field: "conclusion", allowed: []string{"confirmed", "candidate", "not_proven"}},
		{file: "decision_tools.r42.hcl", tool: "submit_node_assessment", field: "scenarios", allowed: []string{"current_production", "expansion_upgrade", "product_branch"}},
		{file: "decision_tools.r42.hcl", tool: "submit_supply_chain_map", field: "nodes.kind", allowed: []string{"product", "component", "material", "process", "equipment", "qualification", "service", "system"}},
		{file: "decision_tools.r42.hcl", tool: "submit_supply_chain_map", field: "edges.relation", allowed: []string{"contains", "supplies", "transformed_into", "assembled_into", "processed_by", "tested_by", "qualified_by", "used_by"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.role", allowed: []string{"existing_supplier", "qualified_alternative", "related_product_only", "unverified"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.priority", allowed: []string{"A", "B", "C", "do_not_research"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.economic_exposure.*.evidence_directness", allowed: []string{"none", "confirmed", "reported", "inferred"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.economic_exposure.customer_validation.status", allowed: []string{"unknown", "evaluation", "qualified", "ordered", "delivering", "production_use"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.economic_exposure.revenue_materiality.status", allowed: []string{"unknown", "exposure_unquantified", "quantified_immaterial", "quantified_material"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.economic_exposure.bottleneck_capture.status", allowed: []string{"unknown", "none", "plausible", "demonstrated"}},
		{file: "decision_tools.r42.hcl", tool: "submit_company_priorities", field: "companies.economic_exposure.commercialization_timing.status", allowed: []string{"unknown", "current", "within_12_months", "beyond_12_months"}},
	}

	for _, tt := range tests {
		t.Run(tt.tool+"/"+tt.field, func(t *testing.T) {
			t.Parallel()

			description := typedToolDescription(t, tt.file, tt.tool)
			assert.Contains(t, description, "`"+tt.field+"`")
			for _, value := range tt.allowed {
				assert.Contains(t, description, "`"+value+"`", "allowed value %q must be published", value)
			}
		})
	}
}

func TestCompanyPriorityToolUseDescribesEconomicExposureInputs(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload)
	start := strings.Index(configuration, "submit_company_priorities = {")
	require.NotEqual(t, -1, start)
	toolUse := configuration[start:]
	end := strings.Index(toolUse, "conclusion = {")
	require.NotEqual(t, -1, end)
	companiesBinding := toolUse[:end]

	for _, required := range []string{
		"economic_exposure",
		"customer_validation",
		"revenue_materiality",
		"bottleneck_capture",
		"commercialization_timing",
		"evidence_directness",
		"claim_ids",
		"current task",
		"assessment.artifact",
		"primary_source_baseline.artifact",
		"graph_track.tasks",
	} {
		assert.Contains(t, companiesBinding, required)
	}
	assert.Contains(t, companiesBinding, "unknown")
	assert.Contains(t, companiesBinding, "submit_claim_cards")
}

func TestFinalizeReportDescriptionPublishesPathContract(t *testing.T) {
	t.Parallel()

	description := typedToolDescription(t, "decision_tools.r42.hcl", "finalize_research_report")
	assert.Contains(t, description, "current research workspace")
	assert.Contains(t, description, "finalized `claims.json`")
	assert.Contains(t, description, "artifact_kind")
	assert.Contains(t, description, "r42_claim_cards")
}

func typedToolDescription(t *testing.T, filename, toolName string) string {
	t.Helper()

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile(filename)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)
	for _, block := range body.Blocks {
		if block.Type != "go_tool" || len(block.Labels) != 1 || block.Labels[0] != toolName {
			continue
		}
		attribute, exists := block.Body.Attributes["description"]
		require.True(t, exists, "go_tool %q must have a description", toolName)
		value, valueDiagnostics := attribute.Expr.Value(nil)
		require.False(t, valueDiagnostics.HasErrors(), valueDiagnostics.Error())
		return value.AsString()
	}
	require.FailNow(t, "go_tool not found", toolName)
	return ""
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
