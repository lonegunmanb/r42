//nolint:paralleltest // Inline Go compiler integration tests run serially to cap process pressure.
package chokepoint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterEvidenceSourceUsesArtifactID(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "register_evidence_source"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "register-evidence")
	artifactID := "artifact-0123456789abcdef0123456789abcdef"
	input := map[string]any{
		"workspace_dir": workspace,
		"url":           "https://example.com/official", "canonical_url": "https://example.com/official",
		"title": "Official filing", "publisher": "Example issuer",
		"publication_date": "2026-01-02", "accessed_at": "2026-08-09",
		"source_type": "official_filing", "reporting_basis": "public_document",
		"provenance": "original", "artifact_id": artifactID,
		"named_entities": []string{"Example issuer"},
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var output map[string]any
	require.NoError(t, json.Unmarshal(*response.Output, &output))
	var source map[string]any
	require.NoError(t, readJSON(valueAs[string](t, output["source_path"]), &source))
	assert.Equal(t, artifactID, source["artifact_id"])
	assert.NotContains(t, source, "artifact_path")
	assert.NotContains(t, source, "artifact_sha256")
	assert.NotContains(t, source, "content_fingerprint")

	input["artifact_id"] = "artifact-123e4567-e89b-12d3-a456-426614174000"
	uuidResponse, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)
	require.NoError(t, err)
	assert.True(t, uuidResponse.Accepted, "issues: %#v", uuidResponse.Issues)

	input["artifact_id"] = filepath.Join(workspace, "sources", "official.md")
	rejected, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "artifact_id")
}

func TestPPLXFetchRejectsArtifactDirectoryOutsideCollectionWorkspace(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "pplx_fetch"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "pplx-fetch")
	foreignDirectory := filepath.Join(filepath.Dir(workspace), "foreign-artifacts")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"url":          "https://example.com/source",
		"artifact_dir": foreignDirectory,
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "invalid_artifact_dir")
}

func TestPPLXFetchRejectsArtifactDirectorySymlinkOutsideCollectionWorkspace(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "pplx_fetch"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "pplx-fetch-symlink")
	foreignDirectory := filepath.Join(filepath.Dir(workspace), "foreign-snapshots")
	require.NoError(t, os.MkdirAll(foreignDirectory, 0o700))
	linkedDirectory := filepath.Join(workspace, "linked-snapshots")
	require.NoError(t, os.Symlink(foreignDirectory, linkedDirectory))

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"url":          "https://example.com/source",
		"artifact_dir": linkedDirectory,
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "invalid_artifact_dir")
}

func TestPPLXFetchClassifiesUnretrievableContentAsFetchFailure(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })

	source := goToolSource(t, "pplx_fetch")
	assert.Contains(t, source, "isFetchFailureText(content)")
	source = strings.Replace(source, "func Invoke(ctx context.Context, input Input)", "func originalInvoke(ctx context.Context, input Input)", 1)
	source += `
func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
  if isFetchFailureText(input.URL) {
    return ToolResponse[Output]{Issues: []Issue{{Code: "fetch_failed", Message: "unretrievable content"}}}, nil
  }
  return ToolResponse[Output]{Accepted: true}, nil
}
`
	program, err := compiler.Compile(t.Context(), source)
	require.NoError(t, err)

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"url": "Unable to retrieve content from the provided URL.",
	}), blockDirectory(t, "pplx-fetch-failure"))

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "fetch_failed")
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
				"source_id": "source-official", "artifact_id": "artifact-0123456789abcdef0123456789abcdef",
				"exact_quote": "The company outsources all chip packaging.",
				"locator":     "page 227, production model", "derived_from": []string{},
			},
			map[string]any{
				"id": "I-001", "statement": "Outsourcing creates external capacity dependency.",
				"status": "inferred", "scope": "DDR5 die packaging", "as_of": "2025-12-31",
				"source_id": "", "artifact_id": "", "exact_quote": "", "locator": "", "derived_from": []string{"C-001"},
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
	assert.Equal(t, "artifact-0123456789abcdef0123456789abcdef", confirmed["artifact_id"])
	assert.NotContains(t, confirmed, "confirmation_basis")
	assert.NotContains(t, confirmed, "dispute_status")
	assert.NotContains(t, confirmed, "freshness_status")

	var registry map[string]any
	require.NoError(t, readJSON(registryPath, &registry))
	sources := mapValue[[]any](t, registry, "sources")
	require.Len(t, sources, 1)
	source := valueAs[map[string]any](t, sources[0])
	assert.Equal(t, "artifact-0123456789abcdef0123456789abcdef", source["artifact_id"])
	assert.NotContains(t, source, "artifact_path")
	assert.NotContains(t, source, "artifact_sha256")
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
				card["artifact_id"] = ""
				card["exact_quote"] = ""
				card["locator"] = ""
			},
			expectedCode: "derived_from",
		},
		{
			name: "artifact must match registered source",
			mutate: func(card map[string]any) {
				card["artifact_id"] = "artifact-ffffffffffffffffffffffffffffffff"
			},
			expectedCode: "artifact_id",
		},
		{
			name: "inference rejects direct artifact evidence",
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
				"source_id": "source-official", "artifact_id": "artifact-0123456789abcdef0123456789abcdef",
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
			"source_id": "", "artifact_id": "", "exact_quote": "", "locator": "",
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
		"workspace_dir": workspace, "_r42_artifact_path": artifactPath,
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
	assert.NotContains(t, toolStringOutput(t, response), artifactPath)
	assert.FileExists(t, artifactPath)
	var assessment map[string]any
	require.NoError(t, readJSON(artifactPath, &assessment))
	assert.Equal(t, "branch", assessment["risk_scope"])
	assert.Equal(t, "candidate", assessment["conclusion"])
}

func TestSupplyChainMapSeparatesAssessmentAndCompanyMappingTargets(t *testing.T) {
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
		"workspace_dir": workspace, "_r42_artifact_path": artifactPath,
		"topic": "CXMT DDR5", "scope_path": scopePath, "claim_paths": []string{claimsPath},
		"nodes": []any{
			map[string]any{"id": "product", "name": "DDR5", "kind": "product", "stages": []string{"product"}, "branches": []string{"all"}, "claim_ids": []string{"C-001"}, "unknowns": []string{}},
			map[string]any{"id": "osat", "name": "Packaging services", "kind": "service", "stages": []string{"packaging"}, "branches": []string{"die"}, "claim_ids": []string{"C-001"}, "unknowns": []string{"spare capacity"}},
		},
		"edges":              []any{map[string]any{"from": "osat", "to": "product", "relation": "supplies", "claim_ids": []string{"C-001"}}},
		"assessment_targets": []any{map[string]any{"node_id": "osat", "node_name": "Packaging services", "why_assess": "External capacity dependency", "claim_ids": []string{"C-001"}}},
		"company_mapping_targets": []any{map[string]any{
			"node_id": "osat", "node_name": "Packaging services",
			"why_map":   "A supplier-addressable service where public-company capability can be tested.",
			"claim_ids": []string{"C-001"},
		}},
		"unknowns": []string{"Alternative capacity"},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifactPath, &document))
	assert.Len(t, mapValue[[]any](t, document, "assessment_targets"), 1)
	assert.Len(t, mapValue[[]any](t, document, "company_mapping_targets"), 1)
	assert.NotContains(t, document, "chokepoints")
	assert.NotContains(t, document, "score")
}

func TestSupplyChainMapRejectsInvalidCompanyMappingTargets(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_supply_chain_map"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "supply-chain-map-invalid-target")
	scopePath := filepath.Join(workspace, "scope.json")
	require.NoError(t, writeJSON(scopePath, map[string]any{"topic": "CXMT DDR5"}))

	tests := []struct {
		name   string
		target map[string]any
		code   string
	}{
		{name: "unknown node", target: map[string]any{"node_id": "missing", "node_name": "Missing", "why_map": "Investigate suppliers.", "claim_ids": []string{"C-001"}}, code: "node_id"},
		{name: "wrong node name", target: map[string]any{"node_id": "osat", "node_name": "Wrong", "why_map": "Investigate suppliers.", "claim_ids": []string{"C-001"}}, code: "node_name"},
		{name: "non supplier addressable node", target: map[string]any{"node_id": "product", "node_name": "DDR5", "why_map": "Investigate suppliers.", "claim_ids": []string{"C-001"}}, code: "node_kind"},
		{name: "missing rationale", target: map[string]any{"node_id": "osat", "node_name": "Packaging services", "why_map": "", "claim_ids": []string{"C-001"}}, code: "why_map"},
		{name: "missing evidence", target: map[string]any{"node_id": "osat", "node_name": "Packaging services", "why_map": "Investigate suppliers.", "claim_ids": []string{}}, code: "claim_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": workspace, "_r42_artifact_path": filepath.Join(workspace, "supply-chain.json"),
				"topic": "CXMT DDR5", "scope_path": scopePath, "claim_paths": []string{claimsPath},
				"nodes": []any{
					map[string]any{"id": "product", "name": "DDR5", "kind": "product", "stages": []string{"product"}, "branches": []string{"all"}, "claim_ids": []string{"C-001"}, "unknowns": []string{}},
					map[string]any{"id": "osat", "name": "Packaging services", "kind": "service", "stages": []string{"packaging"}, "branches": []string{"die"}, "claim_ids": []string{"C-001"}, "unknowns": []string{}},
				},
				"edges": []any{}, "assessment_targets": []any{},
				"company_mapping_targets": []any{tt.target, tt.target}, "unknowns": []string{},
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
			assert.Contains(t, issueCodes(response), "node_id", "duplicate mapping targets must also be reported")
		})
	}
}

func TestCompanyPrioritiesUseSupplyChainTargetAndCapabilityEvidence(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "company-priority")
	supplyChainPath := writeCompanyMappingSupplyChain(t, workspace)
	artifactPath := filepath.Join(workspace, "company-priorities.json")
	economicExposure := unknownEconomicExposure()
	economicExposure["customer_validation"] = map[string]any{
		"status": "qualified", "evidence_directness": "confirmed", "claim_ids": []string{"C-001"},
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "_r42_artifact_path": artifactPath,
		"supply_chain_path": supplyChainPath, "target_node_id": "node-osat", "claim_paths": []string{claimsPath},
		"companies": []any{
			map[string]any{
				"company": "Supplier A", "ticker": "000001", "market": "a-share",
				"role": "capability_match", "research_priority": "A",
				"relationship_claim_ids": []string{}, "capability_claim_ids": []string{"C-001"},
				"economic_exposure": economicExposure,
				"exposure_signals": []any{map[string]any{
					"scope": "segment", "subject": "Advanced packaging", "metric": "share_of_revenue",
					"value": "18%", "as_of": "2025", "evidence_directness": "confirmed", "claim_ids": []string{"C-001"},
				}},
				"why_research":    "The exact-node capability is confirmed; economic exposure remains open.",
				"largest_unknown": "Revenue and profit exposure", "next_check": "Verify segment revenue and orders.",
			},
			map[string]any{
				"company": "Supplier B", "ticker": "000002", "market": "a-share",
				"role": "existing_supplier", "research_priority": "A",
				"relationship_claim_ids": []string{"C-001"}, "capability_claim_ids": []string{},
				"economic_exposure": unknownEconomicExposure(), "exposure_signals": []any{},
				"why_research":    "The exact-node relationship is confirmed; economics remain open.",
				"largest_unknown": "Order materiality", "next_check": "Verify disclosed order value.",
			},
		},
		"conclusion": "One company merits immediate follow-up research.",
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var priorityOutput map[string]any
	require.NoError(t, json.Unmarshal([]byte(toolStringOutput(t, response)), &priorityOutput))
	assert.Contains(t, priorityOutput, "companies")
	assert.Contains(t, priorityOutput, "claims")
	assert.Equal(t, "node-osat", priorityOutput["node_id"])
	assert.Equal(t, "Packaging services", priorityOutput["node_name"])
	assert.Equal(t, "service", priorityOutput["node_kind"])
	assert.NotContains(t, priorityOutput, "node_conclusion")
	assert.FileExists(t, artifactPath)
	companies := mapValue[[]any](t, priorityOutput, "companies")
	require.Len(t, companies, 2)
	recordedCompany, ok := companies[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, recordedCompany, "economic_exposure")

	invalid := map[string]any{
		"workspace_dir": workspace, "_r42_artifact_path": filepath.Join(workspace, "invalid-priorities.json"),
		"supply_chain_path": supplyChainPath, "target_node_id": "node-osat", "claim_paths": []string{claimsPath},
		"companies": []any{
			map[string]any{
				"company": "Supplier C", "ticker": "000003", "market": "a-share",
				"role": "capability_match", "research_priority": "A",
				"relationship_claim_ids": []string{}, "capability_claim_ids": []string{},
				"economic_exposure": unknownEconomicExposure(), "exposure_signals": []any{},
				"why_research": "Capability is only a lead.", "largest_unknown": "Exact-node capability",
				"next_check": "Find exact-node capability evidence.",
			},
			map[string]any{
				"company": "Supplier D", "ticker": "000004", "market": "a-share",
				"role": "related_product_only", "research_priority": "A",
				"relationship_claim_ids": []string{}, "capability_claim_ids": []string{},
				"economic_exposure": unknownEconomicExposure(), "exposure_signals": []any{},
				"why_research": "It sells a related product.", "largest_unknown": "Any target relationship",
				"next_check": "Find direct relationship evidence.",
			},
		},
		"conclusion": "Invalid A classification.",
	}
	rejected, err := program.Invoke(t.Context(), marshalInput(t, invalid), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "research_priority")

	missingTarget := invalid
	missingTarget["target_node_id"] = "not-selected"
	rejected, err = program.Invoke(t.Context(), marshalInput(t, missingTarget), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "target_node_id")
}

func TestCompanyPrioritiesRejectMissingSecurityIdentifier(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "company-missing-security")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": workspace, "_r42_artifact_path": filepath.Join(workspace, "company-priorities.json"),
		"supply_chain_path": writeCompanyMappingSupplyChain(t, workspace),
		"target_node_id":    "node-osat", "claim_paths": []string{claimsPath},
		"companies": []any{map[string]any{
			"company": "Supplier A", "ticker": "", "market": "a-share",
			"role": "existing_supplier", "research_priority": "B",
			"relationship_claim_ids": []string{"C-001"}, "capability_claim_ids": []string{},
			"economic_exposure": unknownEconomicExposure(), "exposure_signals": []any{},
			"why_research": "The relationship merits follow-up.", "largest_unknown": "Economic impact",
			"next_check": "Verify economic exposure.",
		}},
		"conclusion": "The company needs more research.",
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "required")
}

func TestCompanyPrioritiesRejectInvalidAuthoritativeSupplyChain(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "company-invalid-supply-chain")

	tests := []struct {
		name       string
		writeValue any
		missing    bool
		code       string
	}{
		{name: "wrong artifact kind", writeValue: map[string]any{"artifact_kind": "not_supply_chain"}, code: "supply_chain_path"},
		{name: "malformed JSON", writeValue: []byte("{"), code: "supply_chain_path"},
		{name: "missing file", missing: true, code: "supply_chain_path"},
		{name: "target node is missing", writeValue: map[string]any{
			"artifact_kind": "r42_supply_chain", "nodes": []any{},
			"company_mapping_targets": []any{map[string]any{"node_id": "node-osat", "node_name": "Packaging services"}},
		}, code: "target_node_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			supplyChainPath := filepath.Join(workspace, strings.ReplaceAll(tt.name, " ", "-"), "supply-chain.json")
			if !tt.missing {
				require.NoError(t, os.MkdirAll(filepath.Dir(supplyChainPath), 0o700))
				switch value := tt.writeValue.(type) {
				case []byte:
					require.NoError(t, os.WriteFile(supplyChainPath, value, 0o600))
				default:
					require.NoError(t, writeJSON(supplyChainPath, value))
				}
			}
			artifactPath := filepath.Join(workspace, strings.ReplaceAll(tt.name, " ", "-"), "company-priorities.json")
			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": workspace, "_r42_artifact_path": artifactPath,
				"supply_chain_path": supplyChainPath, "target_node_id": "node-osat", "claim_paths": []string{claimsPath},
				"companies": []any{map[string]any{
					"company": "", "ticker": "", "market": "", "role": "unverified", "research_priority": "C",
					"relationship_claim_ids": []string{}, "capability_claim_ids": []string{},
					"economic_exposure": unknownEconomicExposure(), "exposure_signals": []any{},
					"why_research": "", "largest_unknown": "", "next_check": "",
				}},
				"conclusion": "",
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
			assert.Contains(t, issueCodes(response), "required", "authority and company issues must be aggregated")
			assert.Contains(t, issueCodes(response), "conclusion", "authority and conclusion issues must be aggregated")
			assert.NoFileExists(t, artifactPath)
		})
	}
}

func TestCompanyPrioritiesRejectInvalidEconomicExposure(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "company-exposure-validation")
	supplyChainPath := writeCompanyMappingSupplyChain(t, workspace)

	tests := []struct {
		name       string
		dimension  string
		assessment map[string]any
		empty      bool
		code       string
	}{
		{
			name: "all dimensions are required", empty: true, code: "economic_exposure",
		},
		{
			name: "non-unknown status requires a claim", dimension: "customer_validation",
			assessment: map[string]any{"status": "qualified", "evidence_directness": "confirmed", "claim_ids": []string{}}, code: "economic_exposure",
		},
		{
			name: "unknown status cannot claim direct evidence", dimension: "customer_validation",
			assessment: map[string]any{"status": "unknown", "evidence_directness": "confirmed", "claim_ids": []string{"C-001"}}, code: "economic_exposure",
		},
		{
			name: "unsupported materiality status is rejected", dimension: "revenue_materiality",
			assessment: map[string]any{"status": "very_large", "evidence_directness": "confirmed", "claim_ids": []string{"C-001"}}, code: "economic_exposure",
		},
		{
			name: "unsupported evidence directness is rejected", dimension: "customer_validation",
			assessment: map[string]any{"status": "qualified", "evidence_directness": "secondary_guess", "claim_ids": []string{"C-001"}}, code: "economic_exposure",
		},
		{
			name: "evidence directness must match claim status", dimension: "customer_validation",
			assessment: map[string]any{"status": "qualified", "evidence_directness": "inferred", "claim_ids": []string{"C-001"}}, code: "economic_exposure",
		},
		{
			name: "economic exposure claim must exist", dimension: "customer_validation",
			assessment: map[string]any{"status": "qualified", "evidence_directness": "confirmed", "claim_ids": []string{"MISSING-001"}}, code: "claim_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exposure := unknownEconomicExposure()
			if tt.empty {
				exposure = map[string]any{}
			} else {
				exposure[tt.dimension] = tt.assessment
			}
			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": workspace, "_r42_artifact_path": filepath.Join(workspace, "company-priorities.json"),
				"supply_chain_path": supplyChainPath, "target_node_id": "node-osat", "claim_paths": []string{claimsPath},
				"companies": []any{map[string]any{
					"company": "Supplier A", "ticker": "000001", "market": "a-share",
					"role": "existing_supplier", "research_priority": "B",
					"relationship_claim_ids": []string{"C-001"}, "capability_claim_ids": []string{},
					"economic_exposure": exposure, "why_research": "Economic exposure needs verification.",
					"exposure_signals": []any{},
					"largest_unknown":  "Economic impact", "next_check": "Verify commercial evidence.",
				}},
				"conclusion": "The company needs more research.",
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
		})
	}
}

func TestCompanyPrioritiesRejectInvalidExposureSignals(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_company_priorities"))
	require.NoError(t, err)
	workspace, claimsPath, _ := finalizedClaimCardFixture(t, "company-exposure-signals")
	supplyChainPath := writeCompanyMappingSupplyChain(t, workspace)

	tests := []struct {
		name   string
		signal map[string]any
		code   string
	}{
		{name: "unsupported scope", signal: map[string]any{"scope": "factory", "subject": "Plant", "metric": "capacity", "value": "10", "as_of": "2025", "evidence_directness": "confirmed", "claim_ids": []string{"C-001"}}, code: "exposure_signal"},
		{name: "required field", signal: map[string]any{"scope": "company", "subject": "", "metric": "revenue", "value": "10", "as_of": "2025", "evidence_directness": "confirmed", "claim_ids": []string{"C-001"}}, code: "required"},
		{name: "claim required", signal: map[string]any{"scope": "modality", "subject": "RNA vaccines", "metric": "share_of_revenue", "value": "11%", "as_of": "2025", "evidence_directness": "confirmed", "claim_ids": []string{}}, code: "exposure_signal"},
		{name: "claim status must match", signal: map[string]any{"scope": "named_program", "subject": "Program X", "metric": "order_value", "value": "$10m", "as_of": "2025", "evidence_directness": "inferred", "claim_ids": []string{"C-001"}}, code: "exposure_signal"},
		{name: "claim must exist", signal: map[string]any{"scope": "target_branch", "subject": "DDR5", "metric": "revenue", "value": "$10m", "as_of": "2025", "evidence_directness": "confirmed", "claim_ids": []string{"MISSING"}}, code: "claim_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": workspace, "_r42_artifact_path": filepath.Join(workspace, "company-priorities.json"),
				"supply_chain_path": supplyChainPath, "target_node_id": "node-osat", "claim_paths": []string{claimsPath},
				"companies": []any{map[string]any{
					"company": "Supplier A", "ticker": "000001", "market": "a-share",
					"role": "existing_supplier", "research_priority": "B",
					"relationship_claim_ids": []string{"C-001"}, "capability_claim_ids": []string{},
					"economic_exposure": unknownEconomicExposure(), "exposure_signals": []any{tt.signal},
					"why_research": "Economic exposure needs verification.", "largest_unknown": "Economic impact",
					"next_check": "Verify commercial evidence.",
				}},
				"conclusion": "The company needs more research.",
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
		})
	}
}

func unknownEconomicExposure() map[string]any {
	unknown := func() map[string]any {
		return map[string]any{"status": "unknown", "evidence_directness": "none", "claim_ids": []string{}}
	}
	return map[string]any{
		"customer_validation":      unknown(),
		"revenue_materiality":      unknown(),
		"bottleneck_capture":       unknown(),
		"commercialization_timing": unknown(),
	}
}

func writeCompanyMappingSupplyChain(t *testing.T, workspace string) string {
	t.Helper()

	path := filepath.Join(workspace, "supply-chain.json")
	require.NoError(t, writeJSON(path, map[string]any{
		"artifact_kind": "r42_supply_chain",
		"nodes": []any{map[string]any{
			"id": "node-osat", "name": "Packaging services", "kind": "service",
		}},
		"company_mapping_targets": []any{map[string]any{
			"node_id": "node-osat", "node_name": "Packaging services", "why_map": "Investigate public suppliers.",
		}},
	}))
	return path
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
	supplyChainPath := writeCompanyMappingSupplyChain(t, workspace)

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir":      workspace,
		"_r42_artifact_path": filepath.Join(workspace, "company-priorities.json"),
		"supply_chain_path":  supplyChainPath,
		"target_node_id":     "node-osat",
		"claim_paths":        []string{upstreamClaimsPath, taskClaimsPath},
		"companies":          []any{},
		"conclusion":         "No public company merits follow-up research.",
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
		"workspace_dir":      workspace,
		"_r42_artifact_path": filepath.Join(workspace, "company-priorities.json"),
		"supply_chain_path":  supplyChainPath,
		"target_node_id":     "node-osat",
		"claim_paths":        []string{upstreamClaimsPath},
		"companies":          []any{},
		"conclusion":         "No public company merits follow-up research.",
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

func TestFinalizeResearchReportIgnoresUnsubmittedClaimArtifacts(t *testing.T) {
	workspace := blockDirectory(t, "report-configured-inputs")
	claimsPath := filepath.Join(filepath.Dir(workspace), "upstream", "claims.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(claimsPath), 0o700))
	require.NoError(t, writeJSON(claimsPath, map[string]any{
		"artifact_kind": "r42_claim_cards",
		"claims": []any{map[string]any{
			"id": "C-001", "statement": "Supported statement", "status": "confirmed", "scope": "scope",
			"as_of": "2026-01-01", "source_url": "https://example.com/source",
			"exact_quote": "quote", "locator": "body", "derived_from": []string{},
		}},
	}))
	unrelatedPath := filepath.Join(filepath.Dir(workspace), "unrelated", "claims.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(unrelatedPath), 0o700))
	require.NoError(t, writeJSON(unrelatedPath, map[string]any{
		"artifact_kind": "r42_claim_cards", "claims": []any{},
	}))
	reportPath := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(reportPath, []byte("# Report\n\nSupported statement. [[claim:C-001]]\n"), 0o600))
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_research_report"))
	require.NoError(t, err)

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": reportPath, "claim_paths": []string{claimsPath},
	}), workspace)

	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
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
		"artifact_id": "artifact-0123456789abcdef0123456789abcdef", "origin_id": "origin-official",
	}))
	require.NoError(t, writeJSON(filepath.Join(draft, "source-lead.json"), map[string]any{
		"id": "source-lead", "url": "https://example.com/lead",
		"canonical_url": "https://example.com/lead", "title": "Forum post",
		"publisher": "Unknown", "publication_date": "2026-01-02",
		"accessed_at": "2026-08-09", "source_class": "lead_only",
		"artifact_id": "artifact-abcdef0123456789abcdef0123456789", "origin_id": "origin-lead",
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
			"artifact_id": "artifact-0123456789abcdef0123456789abcdef",
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
		"source_id": "source-official", "artifact_id": "artifact-0123456789abcdef0123456789abcdef",
		"exact_quote": quote, "locator": "article body", "derived_from": []string{},
	}
}
