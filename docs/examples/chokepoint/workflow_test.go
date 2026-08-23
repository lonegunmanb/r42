//nolint:paralleltest // Inline Go compiler integration tests run serially to cap process pressure.
package chokepoint_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterEvidenceSourceUsesSnapshotID(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "register_evidence_source"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "register-evidence")
	ledgerPath := filepath.Join(workspace, "evidence-ledger.json")
	snapshotID := "snapshot-0123456789abcdef0123456789abcdef"
	input := map[string]any{
		"workspace_dir": workspace, "ledger_path": ledgerPath,
		"url": "https://example.com/official", "canonical_url": "https://example.com/official",
		"title": "Official filing", "publisher": "Example issuer",
		"publication_date": "2026-01-02", "accessed_at": "2026-08-09",
		"source_type": "official_filing", "reporting_basis": "public_document",
		"provenance": "original", "snapshot_id": snapshotID,
		"named_entities": []string{"Example issuer"},
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var output map[string]any
	require.NoError(t, json.Unmarshal(*response.Output, &output))
	var source map[string]any
	require.NoError(t, readJSON(valueAs[string](t, output["source_path"]), &source))
	assert.Equal(t, snapshotID, source["snapshot_id"])
	assert.NotContains(t, source, "snapshot_path")
	assert.NotContains(t, source, "snapshot_sha256")
	assert.NotContains(t, source, "content_fingerprint")

	input["snapshot_id"] = "snapshot-123e4567-e89b-12d3-a456-426614174000"
	uuidResponse, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)
	require.NoError(t, err)
	assert.True(t, uuidResponse.Accepted, "issues: %#v", uuidResponse.Issues)

	input["snapshot_id"] = filepath.Join(workspace, "sources", "official.md")
	rejected, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "snapshot_id")
}

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
				"source_id": "source-official", "snapshot_id": "snapshot-0123456789abcdef0123456789abcdef",
				"exact_quote": "The company outsources all chip packaging.",
				"locator":     "page 227, production model", "derived_from": []string{},
			},
			map[string]any{
				"id": "I-001", "statement": "Outsourcing creates external capacity dependency.",
				"status": "inferred", "scope": "DDR5 die packaging", "as_of": "2025-12-31",
				"source_id": "", "snapshot_id": "", "exact_quote": "", "locator": "", "derived_from": []string{"C-001"},
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
	require.NotNil(t, finalized.Output)
	var finalizedOutput map[string]any
	require.NoError(t, json.Unmarshal([]byte(toolStringOutput(t, finalized)), &finalizedOutput))
	assert.Equal(t, claimsPath, finalizedOutput["claims_path"])
	assert.Equal(t, registryPath, finalizedOutput["source_registry_path"])
	assert.Equal(t, 2, int(valueAs[float64](t, finalizedOutput["claim_count"])))
	assert.Equal(t, 1, int(valueAs[float64](t, finalizedOutput["source_count"])))
	assert.NotContains(t, finalizedOutput, "claims")
	assert.NotContains(t, finalizedOutput, "source_registry")
	assert.FileExists(t, claimsPath)
	assert.FileExists(t, registryPath)

	var document map[string]any
	require.NoError(t, readJSON(claimsPath, &document))
	cards := mapValue[[]any](t, document, "claims")
	require.Len(t, cards, 2)
	confirmed := valueAs[map[string]any](t, cards[0])
	assert.Equal(t, "confirmed", confirmed["status"])
	assert.Equal(t, "https://example.com/official", confirmed["source_url"])
	assert.Equal(t, "snapshot-0123456789abcdef0123456789abcdef", confirmed["snapshot_id"])
	assert.NotContains(t, confirmed, "confirmation_basis")
	assert.NotContains(t, confirmed, "dispute_status")
	assert.NotContains(t, confirmed, "freshness_status")

	var registry map[string]any
	require.NoError(t, readJSON(registryPath, &registry))
	sources := mapValue[[]any](t, registry, "sources")
	require.Len(t, sources, 1)
	source := valueAs[map[string]any](t, sources[0])
	assert.Equal(t, "snapshot-0123456789abcdef0123456789abcdef", source["snapshot_id"])
	assert.NotContains(t, source, "snapshot_path")
	assert.NotContains(t, source, "snapshot_sha256")
	assert.NotContains(t, source, "content_fingerprint")
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
				card["snapshot_id"] = ""
				card["exact_quote"] = ""
				card["locator"] = ""
			},
			expectedCode: "derived_from",
		},
		{
			name: "snapshot must match registered source",
			mutate: func(card map[string]any) {
				card["snapshot_id"] = "snapshot-ffffffffffffffffffffffffffffffff"
			},
			expectedCode: "snapshot_id",
		},
		{
			name: "inference rejects direct snapshot evidence",
			mutate: func(card map[string]any) {
				card["status"] = "inferred"
				card["source_id"] = ""
				card["exact_quote"] = ""
				card["locator"] = ""
				card["derived_from"] = []string{"C-000"}
			},
			expectedCode: "inference",
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
				"source_id": "source-official", "snapshot_id": "snapshot-0123456789abcdef0123456789abcdef",
				"exact_quote": "The company outsources all chip packaging.",
				"locator":     "page 227", "derived_from": []string{},
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

func TestClaimCardsCanRemoveAStagedClaim(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "submit_claim_cards"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_claim_cards"))
	require.NoError(t, err)
	workspace, claimsPath, registryPath := claimCardFixture(t, "remove-staged-claim")

	response, err := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace,
		"claims_path":   claimsPath,
		"cards":         []any{directClaimCard("C-001", "The facility cost $91 million.", "The facility cost $91 million.")},
	}), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)

	response, err = stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir":    workspace,
		"claims_path":      claimsPath,
		"cards":            []any{directClaimCard("C-001", "The facility cost $92 million.", "The facility cost $92 million.")},
		"remove_claim_ids": []string{"C-001", "C-missing"},
	}), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var removalOutput map[string]any
	require.NoError(t, json.Unmarshal(*response.Output, &removalOutput))
	assert.InEpsilon(t, float64(1), removalOutput["removed"], 0)
	response, err = stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir":    workspace,
		"claims_path":      claimsPath,
		"remove_claim_ids": []string{"C-001"},
	}), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)

	finalized, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "claims_path": claimsPath,
		"source_registry_path": registryPath, "as_of_date": "2026-08-09",
		"allow_empty": true,
	}), workspace)
	require.NoError(t, err)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	var document map[string]any
	require.NoError(t, readJSON(claimsPath, &document))
	assert.Empty(t, mapValue[[]any](t, document, "claims"))
}

func TestFinalizeClaimCardsDeduplicatesOnlyIdenticalClaims(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_claim_cards"))
	require.NoError(t, err)

	tests := []struct {
		name          string
		cards         []map[string]any
		expectedCount int
	}{
		{
			name: "identical claim with different ids",
			cards: []map[string]any{
				directClaimCard("C-001", "The facility cost $91 million.", "The facility cost $91 million."),
				directClaimCard("C-002", "The facility cost $91 million.", "The facility cost $91 million."),
			},
			expectedCount: 1,
		},
		{
			name: "same evidence supports distinct claims",
			cards: []map[string]any{
				directClaimCard("C-001", "The facility cost $91 million and will expand.", "The facility cost $91 million and will expand."),
				directClaimCard("C-002", "The facility cost $91 million.", "The facility cost $91 million and will expand."),
			},
			expectedCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace, claimsPath, registryPath := claimCardFixture(t, tt.name)
			directory := filepath.Join(workspace, ".decision-draft", "claims")
			require.NoError(t, os.MkdirAll(directory, 0o700))
			for _, card := range tt.cards {
				require.NoError(t, writeJSON(filepath.Join(directory, valueAs[string](t, card["id"])+".json"), card))
			}

			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": workspace, "claims_path": claimsPath,
				"source_registry_path": registryPath, "as_of_date": "2026-08-09",
			}), workspace)

			require.NoError(t, invokeErr)
			require.True(t, response.Accepted, "issues: %#v", response.Issues)
			require.NotNil(t, response.Output)
			var output map[string]any
			require.NoError(t, json.Unmarshal([]byte(toolStringOutput(t, response)), &output))
			assert.InEpsilon(t, float64(tt.expectedCount), output["claim_count"], 0)
		})
	}
}

func TestFinalizeClaimCardsRemapsReferencesToDeduplicatedClaims(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_claim_cards"))
	require.NoError(t, err)
	workspace, claimsPath, registryPath := claimCardFixture(t, "deduplicated-reference")
	directory := filepath.Join(workspace, ".decision-draft", "claims")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	cards := []map[string]any{
		directClaimCard("C-001", "The facility cost $91 million.", "The facility cost $91 million."),
		directClaimCard("C-002", "The facility cost $91 million.", "The facility cost $91 million."),
		{
			"id": "I-001", "statement": "The facility has a material capital requirement.",
			"status": "inferred", "scope": "facility", "as_of": "2026-01-01",
			"source_id": "", "snapshot_id": "", "exact_quote": "", "locator": "",
			"derived_from": []string{"C-002"},
		},
	}
	for _, card := range cards {
		require.NoError(t, writeJSON(filepath.Join(directory, valueAs[string](t, card["id"])+".json"), card))
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "claims_path": claimsPath,
		"source_registry_path": registryPath, "as_of_date": "2026-08-09",
	}), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)

	var document map[string]any
	require.NoError(t, readJSON(claimsPath, &document))
	claims := mapValue[[]any](t, document, "claims")
	require.Len(t, claims, 2)
	inferred := valueAs[map[string]any](t, claims[1])
	assert.Equal(t, []any{"C-001"}, inferred["derived_from"])
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
	var priorityOutput map[string]any
	require.NoError(t, json.Unmarshal([]byte(toolStringOutput(t, response)), &priorityOutput))
	assert.Contains(t, priorityOutput, "companies")
	assert.Contains(t, priorityOutput, "claims")
	assert.Equal(t, "node-osat", priorityOutput["node_id"])
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

func TestCompanyPrioritiesReturnOnlyCurrentTaskClaims(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)

	workspace := blockDirectory(t, "company-priority-compact")
	upstreamWorkspace := filepath.Join(filepath.Dir(workspace), "upstream")
	require.NoError(t, os.MkdirAll(upstreamWorkspace, 0o700))
	upstreamClaimsPath := filepath.Join(upstreamWorkspace, "claims.json")
	taskClaimsPath := filepath.Join(workspace, "claims.json")
	require.NoError(t, writeJSON(upstreamClaimsPath, map[string]any{
		"artifact_kind": "r42_claim_cards",
		"claims":        []any{map[string]any{"id": "BASE-001", "status": "confirmed"}},
	}))
	require.NoError(t, writeJSON(taskClaimsPath, map[string]any{
		"artifact_kind": "r42_claim_cards",
		"claims":        []any{map[string]any{"id": "TASK-001", "status": "reported"}},
	}))
	nodePath := filepath.Join(workspace, "node-assessment.json")
	require.NoError(t, writeJSON(nodePath, map[string]any{
		"node_id": "node-osat", "node_name": "Packaging services", "conclusion": "candidate",
	}))

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir":        workspace,
		"artifact_path":        filepath.Join(workspace, "company-priorities.json"),
		"node_assessment_path": nodePath,
		"claim_paths":          []string{upstreamClaimsPath, taskClaimsPath},
		"companies":            []any{},
		"conclusion":           "No public company merits follow-up research.",
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var output struct {
		Claims []struct {
			ID string `json:"id"`
		} `json:"claims"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolStringOutput(t, response)), &output))
	require.Len(t, output.Claims, 1)
	assert.Equal(t, "TASK-001", output.Claims[0].ID)

	rejected, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir":        workspace,
		"artifact_path":        filepath.Join(workspace, "company-priorities.json"),
		"node_assessment_path": nodePath,
		"claim_paths":          []string{upstreamClaimsPath},
		"companies":            []any{},
		"conclusion":           "No public company merits follow-up research.",
	}), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "claim_paths")
}

func TestFinalizeResearchReportUsesClaimURLsWithoutManifest(t *testing.T) {
	claimWorkspace, claimsPath, _ := finalizedClaimCardFixture(t, "report-claims")
	workspace := filepath.Join(filepath.Dir(claimWorkspace), "report")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)
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
	claimWorkspace := blockDirectory(t, "inference-claims")
	workspace := filepath.Join(filepath.Dir(claimWorkspace), "inference-report")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	claimsPath := filepath.Join(claimWorkspace, "claims.json")
	require.NoError(t, writeJSON(claimsPath, map[string]any{
		"artifact_kind": "r42_claim_cards",
		"claims": []any{
			map[string]any{"id": "C-001", "statement": "Premise one", "status": "confirmed", "scope": "node", "as_of": "2026-01-01", "source_url": "https://example.com/one", "exact_quote": "one", "locator": "p1", "derived_from": []string{}},
			map[string]any{"id": "C-002", "statement": "Premise two", "status": "confirmed", "scope": "node", "as_of": "2026-01-01", "source_url": "https://example.com/two", "exact_quote": "two", "locator": "p2", "derived_from": []string{}},
			map[string]any{"id": "I-001", "statement": "Combined inference", "status": "inferred", "scope": "node", "as_of": "2026-01-01", "source_url": "", "exact_quote": "", "locator": "", "derived_from": []string{"C-001", "C-002"}},
		},
	}))
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)
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

func TestFinalizeResearchReportRejectsAllMechanicalClaimPathIssuesWithoutRewriting(t *testing.T) {
	workspace := blockDirectory(t, "invalid-report-inputs")
	reportPath := filepath.Join(workspace, "report.md")
	original := []byte("# Report\n\nSupported statement. [[claim:C-001]]\n")
	require.NoError(t, os.WriteFile(reportPath, original, 0o600))
	wrongKindPath := filepath.Join(workspace, "claims.json")
	require.NoError(t, writeJSON(wrongKindPath, map[string]any{
		"artifact_kind": "draft_claims", "claims": []any{},
	}))
	malformedWorkspace := filepath.Join(filepath.Dir(workspace), "malformed")
	require.NoError(t, os.MkdirAll(malformedWorkspace, 0o700))
	malformedPath := filepath.Join(malformedWorkspace, "claims.json")
	require.NoError(t, os.WriteFile(malformedPath, []byte("{"), 0o600))
	directoryWorkspace := filepath.Join(filepath.Dir(workspace), "directory")
	directoryPath := filepath.Join(directoryWorkspace, "claims.json")
	require.NoError(t, os.MkdirAll(directoryPath, 0o700))
	claimPaths := []string{
		wrongKindPath,
		malformedPath,
		filepath.Join(workspace, "missing", "claims.json"),
		filepath.Join(workspace, "claims-*.json"),
		workspace,
		directoryPath,
	}
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath,
		"claim_paths": claimPaths,
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	codes := issueCodes(response)
	assert.Contains(t, codes, "artifact_kind")
	assert.Contains(t, codes, "invalid_json")
	assert.Contains(t, codes, "claim_path_missing")
	assert.Contains(t, codes, "claim_path")
	assert.Equal(t, original, mustReadFile(t, reportPath))
}

func TestFinalizeResearchReportRejectsDuplicateClaimIDsAcrossFiles(t *testing.T) {
	workspace := blockDirectory(t, "duplicate-report-claims")
	reportPath := filepath.Join(workspace, "report.md")
	original := []byte("# Report\n\nSupported statement. [[claim:C-001]]\n")
	require.NoError(t, os.WriteFile(reportPath, original, 0o600))
	claimPaths := make([]string, 0, 2)
	for _, directory := range []string{"first", "second"} {
		claimWorkspace := filepath.Join(filepath.Dir(workspace), directory)
		require.NoError(t, os.MkdirAll(claimWorkspace, 0o700))
		claimPath := filepath.Join(claimWorkspace, "claims.json")
		require.NoError(t, writeJSON(claimPath, map[string]any{
			"artifact_kind": "r42_claim_cards",
			"claims": []any{map[string]any{
				"id": "C-001", "statement": "Statement", "status": "confirmed",
				"scope": "scope", "as_of": "2026-01-01",
				"source_url": "https://example.com/source", "exact_quote": "quote",
				"locator": "body", "derived_from": []string{},
			}},
		}))
		claimPaths = append(claimPaths, claimPath)
	}
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath, "claim_paths": claimPaths,
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "duplicate_claim_id")
	assert.Equal(t, original, mustReadFile(t, reportPath))
}

func TestFinalizeResearchReportRejectsReportOutsideCurrentWorkspace(t *testing.T) {
	claimWorkspace, claimsPath, _ := finalizedClaimCardFixture(t, "report-claims")
	workspace := filepath.Join(filepath.Dir(claimWorkspace), "report-workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)

	otherWorkspace := filepath.Join(filepath.Dir(workspace), "other-report-workspace")
	require.NoError(t, os.MkdirAll(otherWorkspace, 0o700))
	reportPath := filepath.Join(otherWorkspace, "report.md")
	original := []byte("# Report\n\nSupported statement. [[claim:C-001]]\n")
	require.NoError(t, os.WriteFile(reportPath, original, 0o600))

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath, "claim_paths": []string{claimsPath},
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "report_path")
	assert.Equal(t, original, mustReadFile(t, reportPath))
}

func TestFinalizeResearchReportRequiresExactClaimPathSet(t *testing.T) {
	tests := []struct {
		name       string
		claimPaths func(expected, unexpected []string) []string
		issueCode  string
	}{
		{
			name: "missing configured path",
			claimPaths: func(expected, _ []string) []string {
				return expected[:1]
			},
			issueCode: "claim_path_missing_expected",
		},
		{
			name: "unexpected path",
			claimPaths: func(expected, unexpected []string) []string {
				return append(append([]string{}, expected...), unexpected[0])
			},
			issueCode: "claim_path_unexpected",
		},
		{
			name: "duplicate configured path",
			claimPaths: func(expected, _ []string) []string {
				return append(append([]string{}, expected...), expected[0])
			},
			issueCode: "claim_path_duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := blockDirectory(t, tt.name)
			blocks := filepath.Dir(workspace)
			expected := []string{
				filepath.Join(blocks, "first", "claims.json"),
				filepath.Join(blocks, "second", "claims.json"),
			}
			unexpected := []string{filepath.Join(workspace, "claims.json")}
			provided := tt.claimPaths(expected, unexpected)
			pathsToCreate := append([]string{}, expected...)
			if len(provided) > len(expected) && provided[len(provided)-1] == unexpected[0] {
				pathsToCreate = append(pathsToCreate, unexpected[0])
			}
			for index, path := range pathsToCreate {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, writeJSON(path, map[string]any{
					"artifact_kind": "r42_claim_cards",
					"claims": []any{map[string]any{
						"id":        fmt.Sprintf("C-%03d", index+1),
						"statement": "Statement", "status": "confirmed", "scope": "scope",
						"as_of": "2026-01-01", "source_url": "https://example.com/source",
						"exact_quote": "quote", "locator": "body", "derived_from": []string{},
					}},
				}))
			}
			reportPath := filepath.Join(workspace, "report.md")
			original := []byte("# Report\n\nSupported statement. [[claim:C-001]]\n")
			require.NoError(t, os.WriteFile(reportPath, original, 0o600))
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
			require.NoError(t, err)

			response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": reportPath,
				"claim_paths": provided,
			}), workspace)

			require.NoError(t, err)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.issueCode)
			assert.Equal(t, original, mustReadFile(t, reportPath))
		})
	}
}

func TestFinalizeResearchReportReturnsIndependentReportAndClaimIssuesTogether(t *testing.T) {
	workspace := blockDirectory(t, "combined-report-issues")
	claimsPath := filepath.Join(filepath.Dir(workspace), "malformed", "claims.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(claimsPath), 0o700))
	require.NoError(t, os.WriteFile(claimsPath, []byte("{"), 0o600))
	reportPath := filepath.Join(workspace, "report.md")
	original := []byte("# Report without a claim marker\n")
	require.NoError(t, os.WriteFile(reportPath, original, 0o600))
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath, "claim_paths": []string{claimsPath},
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "invalid_json")
	assert.Contains(t, issueCodes(response), "claim_marker")
	assert.Equal(t, original, mustReadFile(t, reportPath))
}

func claimCardFixture(t *testing.T, name string) (string, string, string) {
	t.Helper()
	workspace := blockDirectory(t, name)
	draft := filepath.Join(workspace, ".evidence-draft", "sources")
	require.NoError(t, os.MkdirAll(draft, 0o700))
	require.NoError(t, writeJSON(filepath.Join(draft, "source-official.json"), map[string]any{
		"id": "source-official", "url": "https://example.com/official",
		"canonical_url": "https://example.com/canonical", "title": "Official filing",
		"publisher": "Example issuer", "publication_date": "2026-01-02",
		"accessed_at": "2026-08-09", "source_class": "authoritative_primary",
		"snapshot_id": "snapshot-0123456789abcdef0123456789abcdef", "origin_id": "origin-official",
	}))
	require.NoError(t, writeJSON(filepath.Join(draft, "source-lead.json"), map[string]any{
		"id": "source-lead", "url": "https://example.com/lead",
		"canonical_url": "https://example.com/lead", "title": "Forum post",
		"publisher": "Unknown", "publication_date": "2026-01-02",
		"accessed_at": "2026-08-09", "source_class": "lead_only",
		"snapshot_id": "snapshot-abcdef0123456789abcdef0123456789", "origin_id": "origin-lead",
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
			"snapshot_id": "snapshot-0123456789abcdef0123456789abcdef",
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

func directClaimCard(id, statement, quote string) map[string]any {
	return map[string]any{
		"id": id, "statement": statement,
		"status": "confirmed", "scope": "facility investment", "as_of": "2026-01-02",
		"source_id": "source-official", "snapshot_id": "snapshot-0123456789abcdef0123456789abcdef",
		"exact_quote": quote, "locator": "article body", "derived_from": []string{},
	}
}
