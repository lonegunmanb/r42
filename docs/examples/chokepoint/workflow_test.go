//nolint:paralleltest // Inline Go compiler integration tests run serially to cap process pressure.
package chokepoint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaimCardsKeepOneEvidenceLayer(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "submit_claim_cards"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_claim_cards"))
	require.NoError(t, err)

	workspace, claimsPath, registryPath := claimCardFixture(t, "claim-cards")
	response, err := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace,
		"claims_path":   claimsPath,
		"cards": []any{
			map[string]any{
				"id": "C-001", "statement": "CXMT outsources chip packaging.",
				"status": "confirmed", "scope": "DDR5 die packaging", "as_of": "2025-12-31",
				"source_id": "source-official", "exact_quote": "The company outsources all chip packaging.",
				"locator": "page 227, production model", "derived_from": []string{},
			},
			map[string]any{
				"id": "I-001", "statement": "Outsourcing creates external capacity dependency.",
				"status": "inferred", "scope": "DDR5 die packaging", "as_of": "2025-12-31",
				"source_id": "", "exact_quote": "", "locator": "", "derived_from": []string{"C-001"},
			},
		},
	}), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)

	finalized, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "claims_path": claimsPath,
		"source_registry_path": registryPath, "as_of_date": "2026-08-09",
	}), workspace)
	require.NoError(t, err)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	assert.FileExists(t, claimsPath)
	assert.FileExists(t, registryPath)

	var document map[string]any
	require.NoError(t, readJSON(claimsPath, &document))
	cards := mapValue[[]any](t, document, "claims")
	require.Len(t, cards, 2)
	confirmed := valueAs[map[string]any](t, cards[0])
	assert.Equal(t, "confirmed", confirmed["status"])
	assert.Equal(t, "https://example.com/official", confirmed["source_url"])
	assert.NotContains(t, confirmed, "confirmation_basis")
	assert.NotContains(t, confirmed, "dispute_status")
	assert.NotContains(t, confirmed, "freshness_status")
}

func TestClaimCardsRejectUnsupportedStatusAndLeadOnlyEvidence(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_claim_cards"))
	require.NoError(t, err)

	tests := []struct {
		name         string
		mutate       func(map[string]any)
		expectedCode string
	}{
		{
			name: "unknown is a gap not a claim card",
			mutate: func(card map[string]any) {
				card["status"] = "unknown"
			},
			expectedCode: "status",
		},
		{
			name: "inference requires premise claim ids",
			mutate: func(card map[string]any) {
				card["status"] = "inferred"
				card["source_id"] = ""
				card["exact_quote"] = ""
				card["locator"] = ""
			},
			expectedCode: "derived_from",
		},
		{
			name: "lead only source cannot support a card",
			mutate: func(card map[string]any) {
				card["source_id"] = "source-lead"
			},
			expectedCode: "source_class",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, claimsPath, _ := claimCardFixture(t, test.name)
			card := map[string]any{
				"id": "C-001", "statement": "CXMT outsources chip packaging.",
				"status": "confirmed", "scope": "DDR5 die packaging", "as_of": "2025-12-31",
				"source_id": "source-official", "exact_quote": "The company outsources all chip packaging.",
				"locator": "page 227", "derived_from": []string{},
			}
			test.mutate(card)

			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": workspace, "claims_path": claimsPath, "cards": []any{card},
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
		})
	}
}

func TestClaimCardsCanFinalizeAnEmptyCompanySearch(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_claim_cards"))
	require.NoError(t, err)
	workspace, claimsPath, registryPath := claimCardFixture(t, "empty-company-search")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "claims_path": claimsPath,
		"source_registry_path": registryPath, "as_of_date": "2026-08-09",
		"allow_empty": true,
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var document map[string]any
	require.NoError(t, readJSON(claimsPath, &document))
	assert.Empty(t, mapValue[[]any](t, document, "claims"))
}

func TestNodeAssessmentSeparatesScopeFromConclusion(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_node_assessment"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "node-assessment")
	artifactPath := filepath.Join(workspace, "node-assessment.json")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "artifact_path": artifactPath,
		"claim_paths": []string{claimsPath}, "node_id": "node-osat", "node_name": "Packaging services",
		"risk_scope": "branch", "branch": "DDR5 die packaging",
		"scenarios":              []string{"current_production"},
		"actual_dependency":      "CXMT outsources packaging.",
		"qualified_alternatives": "Multiple qualified providers are named, but spare capacity is unknown.",
		"switching_vs_buffer":    "Qualification and buffer durations are not public.",
		"conclusion":             "candidate", "claim_ids": []string{"C-001"},
		"unknowns":                 []string{"Available capacity at alternative OSATs"},
		"falsification_conditions": []string{"CXMT discloses readily available qualified capacity."},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, artifactPath)
	var assessment map[string]any
	require.NoError(t, readJSON(artifactPath, &assessment))
	assert.Equal(t, "branch", assessment["risk_scope"])
	assert.Equal(t, "candidate", assessment["conclusion"])
}

func TestSupplyChainMapKeepsAssessmentTargetsWithoutChokepointScores(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_supply_chain_map"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "supply-chain-map")
	artifactPath := filepath.Join(workspace, "supply-chain.json")
	scopePath := filepath.Join(workspace, "scope.json")
	require.NoError(t, writeJSON(scopePath, map[string]any{"topic": "CXMT DDR5"}))

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "artifact_path": artifactPath,
		"topic": "CXMT DDR5", "scope_path": scopePath, "claim_paths": []string{claimsPath},
		"nodes": []any{
			map[string]any{"id": "product", "name": "DDR5", "kind": "product", "stages": []string{"product"}, "branches": []string{"all"}, "claim_ids": []string{"C-001"}, "unknowns": []string{}},
			map[string]any{"id": "osat", "name": "Packaging services", "kind": "service", "stages": []string{"packaging"}, "branches": []string{"die"}, "claim_ids": []string{"C-001"}, "unknowns": []string{"spare capacity"}},
		},
		"edges":              []any{map[string]any{"from": "osat", "to": "product", "relation": "supplies", "claim_ids": []string{"C-001"}}},
		"assessment_targets": []any{map[string]any{"node_id": "osat", "node_name": "Packaging services", "why_assess": "External capacity dependency", "claim_ids": []string{"C-001"}}},
		"unknowns":           []string{"Alternative capacity"},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifactPath, &document))
	assert.Len(t, mapValue[[]any](t, document, "assessment_targets"), 1)
	assert.NotContains(t, document, "chokepoints")
	assert.NotContains(t, document, "score")
}

func TestCompanyPrioritiesRequireNodeAndRelationshipEvidence(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "company-priority")
	nodePath := filepath.Join(workspace, "node-assessment.json")
	require.NoError(t, writeJSON(nodePath, map[string]any{
		"node_id": "node-osat", "node_name": "Packaging services", "conclusion": "candidate",
	}))
	artifactPath := filepath.Join(workspace, "company-priorities.json")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "artifact_path": artifactPath,
		"node_assessment_path": nodePath, "claim_paths": []string{claimsPath},
		"companies": []any{map[string]any{
			"company": "Supplier A", "ticker": "000001", "market": "a-share",
			"role": "existing_supplier", "priority": "A",
			"relationship_claim_ids": []string{"C-001"}, "economic_impact_claim_ids": []string{},
			"why_research":    "The exact-node relationship is confirmed; economic exposure remains open.",
			"largest_unknown": "Revenue and profit exposure", "next_check": "Verify segment revenue and orders.",
		}},
		"conclusion": "One company merits immediate follow-up research.",
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, artifactPath)

	invalid := map[string]any{
		"workspace_dir": workspace, "artifact_path": filepath.Join(workspace, "invalid-priorities.json"),
		"node_assessment_path": nodePath, "claim_paths": []string{claimsPath},
		"companies": []any{map[string]any{
			"company": "Supplier B", "ticker": "000002", "market": "a-share",
			"role": "related_product_only", "priority": "A",
			"relationship_claim_ids": []string{}, "economic_impact_claim_ids": []string{},
			"why_research": "It sells a related product.", "largest_unknown": "Any target relationship",
			"next_check": "Find direct relationship evidence.",
		}},
		"conclusion": "Invalid A classification.",
	}
	rejected, err := program.Invoke(t.Context(), marshalInput(t, invalid), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "priority")
}

func TestFinalizeResearchReportUsesClaimURLsWithoutManifest(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "report")
	reportPath := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(reportPath, []byte("# Report\n\nCXMT outsources packaging. [[claim:C-001]]\n"), 0o600))

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath, "claim_paths": []string{claimsPath},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	rewritten := string(mustReadFile(t, reportPath))
	assert.Contains(t, rewritten, "[C-001](https://example.com/official)")
	assert.Contains(t, rewritten, "## Evidence cards")
	assert.Contains(t, rewritten, "The company outsources all chip packaging.")
	assert.NotContains(t, rewritten, "RPT-")
	assert.NoFileExists(t, filepath.Join(workspace, "report-manifest.json"))
}

func TestFinalizeResearchReportLinksEveryInferencePremise(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "inference-report")
	claimsPath := filepath.Join(workspace, "claims.json")
	require.NoError(t, writeJSON(claimsPath, map[string]any{
		"claims": []any{
			map[string]any{"id": "C-001", "statement": "Premise one", "status": "confirmed", "scope": "node", "as_of": "2026-01-01", "source_url": "https://example.com/one", "exact_quote": "one", "locator": "p1", "derived_from": []string{}},
			map[string]any{"id": "C-002", "statement": "Premise two", "status": "confirmed", "scope": "node", "as_of": "2026-01-01", "source_url": "https://example.com/two", "exact_quote": "two", "locator": "p2", "derived_from": []string{}},
			map[string]any{"id": "I-001", "statement": "Combined inference", "status": "inferred", "scope": "node", "as_of": "2026-01-01", "source_url": "", "exact_quote": "", "locator": "", "derived_from": []string{"C-001", "C-002"}},
		},
	}))
	reportPath := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(reportPath, []byte("# Report\n\nCombined inference. [[claim:I-001]]\n"), 0o600))

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath, "claim_paths": []string{claimsPath},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	rewritten := string(mustReadFile(t, reportPath))
	assert.Contains(t, rewritten, "https://example.com/one")
	assert.Contains(t, rewritten, "https://example.com/two")
}

func claimCardFixture(t *testing.T, name string) (string, string, string) {
	t.Helper()
	workspace := blockDirectory(t, name)
	snapshot := filepath.Join(workspace, "sources", "official.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(snapshot), 0o700))
	require.NoError(t, os.WriteFile(snapshot, []byte("The company outsources all chip packaging."), 0o600))
	draft := filepath.Join(workspace, ".evidence-draft", "sources")
	require.NoError(t, os.MkdirAll(draft, 0o700))
	require.NoError(t, writeJSON(filepath.Join(draft, "source-official.json"), map[string]any{
		"id": "source-official", "url": "https://example.com/official",
		"canonical_url": "https://example.com/official", "title": "Official filing",
		"publisher": "Example issuer", "publication_date": "2026-01-02",
		"accessed_at": "2026-08-09", "source_class": "authoritative_primary",
		"snapshot_path": snapshot, "origin_id": "origin-official",
	}))
	require.NoError(t, writeJSON(filepath.Join(draft, "source-lead.json"), map[string]any{
		"id": "source-lead", "url": "https://example.com/lead",
		"canonical_url": "https://example.com/lead", "title": "Forum post",
		"publisher": "Unknown", "publication_date": "2026-01-02",
		"accessed_at": "2026-08-09", "source_class": "lead_only",
		"snapshot_path": snapshot, "origin_id": "origin-lead",
	}))
	return workspace, filepath.Join(workspace, "claims.json"), filepath.Join(workspace, "source-registry.json")
}

func finalizedClaimCardFixture(t *testing.T, name string) (string, string, string) {
	t.Helper()
	workspace, claimsPath, registryPath := claimCardFixture(t, name)
	require.NoError(t, writeJSON(claimsPath, map[string]any{
		"artifact_kind": "r42_claim_cards",
		"claims": []any{map[string]any{
			"id": "C-001", "statement": "CXMT outsources chip packaging.",
			"status": "confirmed", "scope": "DDR5 die packaging", "as_of": "2025-12-31",
			"source_id": "source-official", "source_url": "https://example.com/official",
			"exact_quote": "The company outsources all chip packaging.",
			"locator":     "page 227", "derived_from": []string{},
		}},
	}))
	require.NoError(t, writeJSON(registryPath, map[string]any{
		"sources": []any{map[string]any{
			"id": "source-official", "canonical_url": "https://example.com/official",
			"source_class": "authoritative_primary",
		}},
	}))
	return workspace, claimsPath, registryPath
}
