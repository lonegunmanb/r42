package chokepoint_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

var trackNames = []string{
	"product_structure",
	"manufacturing_testing",
	"equipment",
	"materials_chemicals",
	"qualification_integration",
}

//nolint:paralleltest // Compiles all evidence tools serially to cap process pressure.
func TestEvidenceToolsUseNativeGo(t *testing.T) {
	expected := map[string]bool{
		"register_evidence_source":         false,
		"stage_evidence_claims":            false,
		"stage_claim_freshness_checks":     false,
		"stage_evidence_gaps":              false,
		"finalize_evidence_ledger":         true,
		"prepare_evidence_reconciliation":  false,
		"resolve_evidence_conflict":        false,
		"finalize_evidence_reconciliation": true,
		"stage_report_claims":              false,
		"finalize_report_manifest":         true,
	}
	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("evidence_tools.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	found := map[string]struct{}{}
	for _, block := range body.Blocks {
		if len(block.Labels) != 1 {
			continue
		}
		name := block.Labels[0]
		terminate, exists := expected[name]
		if !exists {
			continue
		}
		assert.Equal(t, "go_tool", block.Type, "%s must be a native go_tool", name)
		if block.Type != "go_tool" {
			continue
		}
		source := block.Body.Attributes["source"]
		value, valueDiagnostics := source.Expr.Value(nil)
		require.False(t, valueDiagnostics.HasErrors(), valueDiagnostics.Error())
		program, compileErr := compiler.Compile(t.Context(), value.AsString())
		require.NoError(t, compileErr, "compile go_tool.%s", name)
		if terminate {
			assert.True(t, program.Analysis().OutputType.Equals(cty.String), "go_tool.%s Output must be string", name)
		}
		found[name] = struct{}{}
	}
	assert.Len(t, found, len(expected), "all evidence tools must be registered as go_tool")
}

func TestSubmitSupplyChainScopeValidatesCoverageInventory(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_supply_chain_scope"))
	require.NoError(t, err)

	t.Run("accepts complete scope", func(t *testing.T) {
		workingDirectory := blockDirectory(t, "scope-valid")
		input := validScopeInput(filepath.Join(workingDirectory, "scope.json"))

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)

		require.NoError(t, invokeErr)
		assert.True(t, response.Accepted)
		assert.FileExists(t, mapValue[string](t, input, "artifact_path"))
	})

	tests := []struct {
		name         string
		mutate       func(*testing.T, map[string]any)
		expectedCode string
	}{
		{
			name: "duplicate coverage id",
			mutate: func(t *testing.T, input map[string]any) {
				t.Helper()
				items := mapValue[[]any](t, input, "coverage_items")
				duplicate := cloneMap(t, valueAs[map[string]any](t, items[0]))
				items[1] = duplicate
			},
			expectedCode: "coverage_id",
		},
		{
			name: "expected stage has no coverage item",
			mutate: func(t *testing.T, input map[string]any) {
				t.Helper()
				input["expected_stages"] = append(mapValue[[]string](t, input, "expected_stages"), "distribution")
			},
			expectedCode: "stage_coverage",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workingDirectory := blockDirectory(t, test.name)
			input := validScopeInput(filepath.Join(workingDirectory, "scope.json"))
			test.mutate(t, input)

			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
			assert.NoFileExists(t, mapValue[string](t, input, "artifact_path"))
		})
	}
}

func TestFinalizeSupplyChainValidatesCoverageGraphAndEvidence(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_supply_chain"))
	require.NoError(t, err)

	t.Run("accepts complete evidence-backed graph", func(t *testing.T) {
		t.Parallel()

		workingDirectory := blockDirectory(t, "chain-valid")
		input := validSupplyChainInput(t, workingDirectory)
		stageSupplyChain(t, stage, workingDirectory, input)

		response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
			"artifact_path": input["artifact_path"],
		}), workingDirectory)

		require.NoError(t, invokeErr)
		assert.True(t, response.Accepted)
		assert.FileExists(t, mapValue[string](t, input, "artifact_path"))
		require.NotNil(t, response.Output)
		var result string
		require.NoError(t, json.Unmarshal(*response.Output, &result))
		assert.JSONEq(t, string(mustReadFile(t, mapValue[string](t, input, "artifact_path"))), result)
	})

	t.Run("accepts explicit unresolved coverage", func(t *testing.T) {
		t.Parallel()

		workingDirectory := blockDirectory(t, "chain-unknown")
		input := validSupplyChainInput(t, workingDirectory)
		coverage := mapValue[[]any](t, input, "coverage")
		coverage[4] = map[string]any{
			"id": "qualification-dimm", "status": "unknown", "node_ids": []string{},
			"evidence_claim_ids": []string{}, "explanation": "Public evidence does not identify the qualification path.",
			"research_attempt": "Reviewed vendor and system qualification documentation.",
			"impact":           "Target-specific system compatibility remains uncertain.",
		}
		input["nodes"] = mapValue[[]any](t, input, "nodes")[:4]
		input["edges"] = mapValue[[]any](t, input, "edges")[:3]
		stageSupplyChain(t, stage, workingDirectory, input)

		response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
			"artifact_path": input["artifact_path"],
		}), workingDirectory)

		require.NoError(t, invokeErr)
		assert.True(t, response.Accepted)
		assert.FileExists(t, mapValue[string](t, input, "artifact_path"))
	})

	t.Run("accepts an explicit empty chokepoint batch", func(t *testing.T) {
		t.Parallel()

		workingDirectory := blockDirectory(t, "chain-no-chokepoints")
		input := validSupplyChainInput(t, workingDirectory)
		input["chokepoints"] = []any{}
		stageSupplyChain(t, stage, workingDirectory, input)

		response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
			"artifact_path": input["artifact_path"],
		}), workingDirectory)

		require.NoError(t, invokeErr)
		assert.True(t, response.Accepted)
		assert.FileExists(t, mapValue[string](t, input, "artifact_path"))
	})

	tests := []struct {
		name         string
		mutate       func(*testing.T, map[string]any)
		expectedCode string
	}{
		{
			name: "coverage item omitted",
			mutate: func(t *testing.T, input map[string]any) {
				t.Helper()
				coverage := mapValue[[]any](t, input, "coverage")
				input["coverage"] = coverage[:len(coverage)-1]
			},
			expectedCode: "coverage_item",
		},
		{
			name: "node cites unknown claim",
			mutate: func(t *testing.T, input map[string]any) {
				t.Helper()
				nodes := mapValue[[]any](t, input, "nodes")
				valueAs[map[string]any](t, nodes[0])["evidence_claim_ids"] = []string{"missing-claim"}
			},
			expectedCode: "evidence_claim_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workingDirectory := blockDirectory(t, test.name)
			input := validSupplyChainInput(t, workingDirectory)
			test.mutate(t, input)
			stageSupplyChain(t, stage, workingDirectory, input)

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": input["artifact_path"],
			}), workingDirectory)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
			assert.NoFileExists(t, mapValue[string](t, input, "artifact_path"))
		})
	}
}

func TestFinalizeSupplyChainRejectsClaimExcludedByReconciliation(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_supply_chain"))
	require.NoError(t, err)

	workingDirectory := blockDirectory(t, "reconciled-claim")
	input := validSupplyChainInput(t, workingDirectory)
	reconciliationPath := mapValue[string](t, input, "reconciled_artifact")
	require.NoError(t, writeJSON(reconciliationPath, map[string]any{
		"claims": []any{
			map[string]any{"id": "equipment-claim-1", "status": "confirmed"},
			map[string]any{"id": "product_structure-claim-1", "status": "confirmed"},
		},
		"conflicts": []any{map[string]any{
			"id": "conflict-equipment", "claim_ids": []string{"equipment-claim-1", "product_structure-claim-1"},
			"resolution": map[string]any{
				"decision": "prefer", "chosen_claim_ids": []string{"product_structure-claim-1"},
				"rationale": "The selected claim supersedes the other value.",
			},
		}},
	}))
	stageSupplyChain(t, stage, workingDirectory, input)

	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": input["artifact_path"],
	}), workingDirectory)

	require.NoError(t, invokeErr)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reconciled_claim")
	assert.NoFileExists(t, mapValue[string](t, input, "artifact_path"))
}

func TestFinalizeSupplyChainRejectsInheritedUnavailableClaim(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_supply_chain"))
	require.NoError(t, err)

	workingDirectory := blockDirectory(t, "inherited-unavailable-claim")
	input := validSupplyChainInput(t, workingDirectory)
	reconciliationPath := mapValue[string](t, input, "reconciled_artifact")
	var reconciliation map[string]any
	require.NoError(t, readJSON(reconciliationPath, &reconciliation))
	for _, raw := range mapValue[[]any](t, reconciliation, "claims") {
		claim := valueAs[map[string]any](t, raw)
		if claim["id"] == "equipment-claim-1" {
			claim["reconciliation_availability"] = "excluded"
		}
	}
	require.NoError(t, writeJSON(reconciliationPath, reconciliation))
	stageSupplyChain(t, stage, workingDirectory, input)

	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": input["artifact_path"],
	}), workingDirectory)

	require.NoError(t, invokeErr)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reconciled_claim")
	assert.NoFileExists(t, mapValue[string](t, input, "artifact_path"))
}

func TestSelectChokepointsQCOnlyRequestsSemanticReviewWithoutShell(t *testing.T) {
	t.Parallel()

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("main.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)
	var qcBlock *hclsyntax.Block
	for _, block := range body.Blocks {
		if block.Type != "research" || len(block.Labels) != 2 ||
			block.Labels[0] != "static" || block.Labels[1] != "select_chokepoints" {
			continue
		}
		for _, nested := range block.Body.Blocks {
			if nested.Type == "qc" {
				qcBlock = nested
				break
			}
		}
	}
	require.NotNil(t, qcBlock)

	criteria, criteriaDiagnostics := qcBlock.Body.Attributes["criteria"].Expr.Value(nil)
	require.False(t, criteriaDiagnostics.HasErrors(), criteriaDiagnostics.Error())
	assert.ElementsMatch(t, []string{
		"chokepoint_distinction", "evidence_meaning", "uncertainty", "variant_scope",
	}, valueKeys(criteria))

	disallowed := qcBlock.Body.Attributes["disallowed_tools"].Expr.Variables()
	require.Len(t, disallowed, 1)
	assert.Equal(t, "local", disallowed[0].RootName())
	require.Len(t, disallowed[0], 2)
	attribute, ok := disallowed[0][1].(hcl.TraverseAttr)
	require.True(t, ok)
	assert.Equal(t, "semantic_qc_disallowed_tools", attribute.Name)

	toolIDsAttribute, exists := qcBlock.Body.Attributes["tool_ids"]
	require.True(t, exists, "select_chokepoints QC must configure a read-only evidence tool")
	toolIDs := toolIDsAttribute.Expr.Variables()
	require.Len(t, toolIDs, 1)
	assert.Equal(t, "go_tool", toolIDs[0].RootName())
	require.Len(t, toolIDs[0], 3)
	toolName, ok := toolIDs[0][1].(hcl.TraverseAttr)
	require.True(t, ok)
	assert.Equal(t, "read_chokepoint_evidence", toolName.Name)
}

func TestReadChokepointEvidenceReturnsOnlyRequestedSemanticContext(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	reader, err := compiler.Compile(t.Context(), goToolSource(t, "read_chokepoint_evidence"))
	require.NoError(t, err)
	workingDirectory := blockDirectory(t, "read-chokepoint-evidence")
	artifactPath := filepath.Join(workingDirectory, "evidence-resolution.json")
	require.NoError(t, writeJSON(artifactPath, map[string]any{
		"sources": []any{
			map[string]any{"id": "source-1", "url": "https://example.com/one"},
			map[string]any{"id": "source-2", "url": "https://example.com/two"},
		},
		"claims": []any{
			map[string]any{"id": "claim-1", "status": "confirmed", "evidence": []any{map[string]any{"source_id": "source-1"}}},
			map[string]any{"id": "claim-2", "status": "reported", "evidence": []any{map[string]any{"source_id": "source-2"}}},
		},
		"conflicts": []any{map[string]any{
			"id": "conflict-1", "claim_ids": []string{"claim-1", "claim-2"},
			"resolution": map[string]any{"decision": "prefer", "chosen_claim_ids": []string{"claim-1"}},
		}},
	}))

	response, invokeErr := reader.Invoke(t.Context(), marshalInput(t, map[string]any{
		"evidence_resolution_path": artifactPath, "claim_ids": []string{"claim-1"},
	}), workingDirectory)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	require.NotNil(t, response.Output)
	var encoded string
	require.NoError(t, json.Unmarshal(*response.Output, &encoded))
	var context map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &context))
	claims := mapValue[[]any](t, context, "claims")
	sources := mapValue[[]any](t, context, "sources")
	conflicts := mapValue[[]any](t, context, "conflicts")
	require.Len(t, claims, 1)
	require.Len(t, sources, 1)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "claim-1", valueAs[map[string]any](t, claims[0])["id"])
	assert.Equal(t, "source-1", valueAs[map[string]any](t, sources[0])["id"])
}

func TestFinalizeSupplyChainNormalizesStageWhitespace(t *testing.T) {
	t.Parallel()

	// The finalizer should compare human-readable stage labels by content, not formatting.
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_supply_chain"))
	require.NoError(t, err)

	workingDirectory := blockDirectory(t, "chain-stage-whitespace")
	input := validSupplyChainInput(t, workingDirectory)
	scopePath := mapValue[string](t, input, "scope_artifact")
	scope := validScopeInput(scopePath)
	scope["expected_stages"] = []string{
		"product definition", "wafer_fabrication", "packaging", "module_assembly", "system_qualification",
	}
	require.NoError(t, writeJSON(scopePath, scope))
	nodes := mapValue[[]any](t, input, "nodes")
	valueAs[map[string]any](t, nodes[0])["stages"] = []string{"\n\t product\t\ndefinition \r\n"}
	stageSupplyChain(t, stage, workingDirectory, input)

	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": input["artifact_path"],
	}), workingDirectory)

	require.NoError(t, invokeErr)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, mapValue[string](t, input, "artifact_path"))
}

func TestStagedSupplyChainFinalizesAfterTargetedBatchRepair(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_supply_chain"))
	require.NoError(t, err)

	workingDirectory := blockDirectory(t, "staged-chain")
	complete := validSupplyChainInput(t, workingDirectory)
	allNodes := mapValue[[]any](t, complete, "nodes")
	stageInputs := []map[string]any{
		{
			"section": "metadata", "batch_id": "main",
			"metadata": map[string]any{
				"scope_artifact": complete["scope_artifact"], "topic": complete["topic"],
				"focal_node_id": complete["focal_node_id"], "reviewed_artifacts": complete["reviewed_artifacts"],
				"reconciled_artifact": complete["reconciled_artifact"],
				"conclusion":          complete["conclusion"],
			},
		},
		{"section": "nodes", "batch_id": "core", "nodes": allNodes[:2]},
		{"section": "nodes", "batch_id": "boundaries", "nodes": allNodes[2:]},
		{"section": "coverage", "batch_id": "main", "coverage": complete["coverage"]},
		{"section": "chokepoints", "batch_id": "main", "chokepoints": complete["chokepoints"]},
	}
	for _, input := range stageInputs {
		response, invokeErr := stage.Invoke(t.Context(), marshalInput(t, input), workingDirectory)
		require.NoError(t, invokeErr)
		require.True(t, response.Accepted, "issues: %#v", response.Issues)
	}

	invalidNodes := cloneSlice(t, allNodes[:2])
	valueAs[map[string]any](t, invalidNodes[0])["status"] = "confirmed"
	rejected, invokeErr := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"section": "nodes", "batch_id": "core", "nodes": invalidNodes,
	}), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "node_status")

	invalidEdges := cloneSlice(t, mapValue[[]any](t, complete, "edges"))
	valueAs[map[string]any](t, invalidEdges[0])["relation"] = "somehow_related"
	rejected, invokeErr = stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"section": "edges", "batch_id": "main", "edges": invalidEdges,
	}), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "relation")

	valueAs[map[string]any](t, invalidEdges[0])["relation"] = "supplies"
	valueAs[map[string]any](t, invalidEdges[0])["consumer_node_id"] = "missing-node"
	staged, invokeErr := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"section": "edges", "batch_id": "main", "edges": invalidEdges,
	}), workingDirectory)
	require.NoError(t, invokeErr)
	require.True(t, staged.Accepted, "issues: %#v", staged.Issues)

	artifactPath := mapValue[string](t, complete, "artifact_path")
	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifactPath,
	}), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "consumer_node_id")
	assert.NoFileExists(t, artifactPath)

	allEdges := mapValue[[]any](t, complete, "edges")
	for batchID, edges := range map[string][]any{
		"main": allEdges[:2], "remaining": allEdges[2:],
	} {
		repaired, repairErr := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
			"section": "edges", "batch_id": batchID, "edges": edges,
		}), workingDirectory)
		require.NoError(t, repairErr)
		require.True(t, repaired.Accepted, "issues: %#v", repaired.Issues)
	}

	response, invokeErr = finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifactPath,
	}), workingDirectory)
	require.NoError(t, invokeErr)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, artifactPath)
	var artifact map[string]any
	require.NoError(t, json.Unmarshal(mustReadFile(t, artifactPath), &artifact))
	nodes := mapValue[[]any](t, artifact, "nodes")
	assert.Equal(t, "supported", valueAs[map[string]any](t, nodes[0])["status"])
}

func TestFinalizeSupplyChainReportsMissingDraftSectionsWithoutCascading(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_supply_chain"))
	require.NoError(t, err)
	workingDirectory := blockDirectory(t, "missing-draft")

	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": filepath.Join(workingDirectory, "supply-chain.json"),
	}), workingDirectory)

	require.NoError(t, invokeErr)
	assert.False(t, response.Accepted)
	assert.Equal(t, []string{
		"draft_metadata", "draft_section", "draft_section", "draft_section", "draft_section",
	}, issueCodes(response))
}

func TestStageSupplyChainRejectsUnsafeOrOversizedBatches(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	fixtureDirectory := blockDirectory(t, "stage-limits-fixture")
	complete := validSupplyChainInput(t, fixtureDirectory)
	nodes := mapValue[[]any](t, complete, "nodes")
	edges := mapValue[[]any](t, complete, "edges")
	coverage := mapValue[[]any](t, complete, "coverage")
	chokepoints := mapValue[[]any](t, complete, "chokepoints")
	tooManyNodes := make([]any, 11)
	for index := range tooManyNodes {
		node := cloneMap(t, valueAs[map[string]any](t, nodes[0]))
		node["id"] = fmt.Sprintf("node-%d", index)
		tooManyNodes[index] = node
	}
	tooManyEdges := make([]any, 16)
	for index := range tooManyEdges {
		tooManyEdges[index] = cloneMap(t, valueAs[map[string]any](t, edges[0]))
	}
	tooManyCoverage := make([]any, 11)
	for index := range tooManyCoverage {
		item := cloneMap(t, valueAs[map[string]any](t, coverage[0]))
		item["id"] = fmt.Sprintf("coverage-%d", index)
		tooManyCoverage[index] = item
	}
	tooManyChokepoints := make([]any, 11)
	for index := range tooManyChokepoints {
		tooManyChokepoints[index] = cloneMap(t, valueAs[map[string]any](t, chokepoints[0]))
	}

	tests := []struct {
		name         string
		input        map[string]any
		expectedCode string
	}{
		{
			name: "unsafe batch id",
			input: map[string]any{
				"section": "nodes", "batch_id": "../escape", "nodes": nodes[:1],
			},
			expectedCode: "batch_id",
		},
		{
			name: "multiple section payloads",
			input: map[string]any{
				"section": "nodes", "batch_id": "main", "nodes": nodes[:1], "edges": []any{},
			},
			expectedCode: "section_payload",
		},
		{
			name: "missing section payload",
			input: map[string]any{
				"section": "nodes", "batch_id": "main",
			},
			expectedCode: "section_payload",
		},
		{
			name: "mismatched section payload",
			input: map[string]any{
				"section": "nodes", "batch_id": "main", "edges": []any{},
			},
			expectedCode: "section_payload",
		},
		{
			name: "too many nodes",
			input: map[string]any{
				"section": "nodes", "batch_id": "main", "nodes": tooManyNodes,
			},
			expectedCode: "batch_size",
		},
		{
			name: "too many edges",
			input: map[string]any{
				"section": "edges", "batch_id": "main", "edges": tooManyEdges,
			},
			expectedCode: "batch_size",
		},
		{
			name: "too many coverage items",
			input: map[string]any{
				"section": "coverage", "batch_id": "main", "coverage": tooManyCoverage,
			},
			expectedCode: "batch_size",
		},
		{
			name: "too many chokepoints",
			input: map[string]any{
				"section": "chokepoints", "batch_id": "main", "chokepoints": tooManyChokepoints,
			},
			expectedCode: "batch_size",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workingDirectory := blockDirectory(t, test.name)

			response, invokeErr := stage.Invoke(t.Context(), marshalInput(t, test.input), workingDirectory)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
			assert.NoDirExists(t, filepath.Join(workingDirectory, ".supply-chain-draft"))
		})
	}
}

func TestStageSupplyChainUsesTypedRiskDimensionsWithoutCompositeRank(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_supply_chain"))
	require.NoError(t, err)
	workingDirectory := blockDirectory(t, "typed-risk")
	input := validSupplyChainInput(t, workingDirectory)
	chokepoint := valueAs[map[string]any](t, mapValue[[]any](t, input, "chokepoints")[0])

	accepted, invokeErr := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"section": "chokepoints", "batch_id": "main", "chokepoints": []any{chokepoint},
	}), workingDirectory)
	require.NoError(t, invokeErr)
	assert.True(t, accepted.Accepted, "issues: %#v", accepted.Issues)

	chokepoint["delivery_impact"] = "sounds_bad"
	rejected, invokeErr := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"section": "chokepoints", "batch_id": "main", "chokepoints": []any{chokepoint},
	}), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "delivery_impact")
}

func TestSubmitCandidatesRequiresEvidenceLedgerClaimIDs(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_candidates"))
	require.NoError(t, err)
	workingDirectory := blockDirectory(t, "candidate-ledger")
	workspace := filepath.Join(workingDirectory, "001")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	ledgerPath := writeEvidenceLedger(t, workspace, map[string]string{"candidate-claim-001": "confirmed"})
	artifactPath := filepath.Join(workspace, "candidates.json")
	input := map[string]any{
		"workspace_dir":  workspace,
		"artifact_path":  artifactPath,
		"ledger_path":    ledgerPath,
		"node_id":        "node-3",
		"max_candidates": 3,
		"candidates": []any{map[string]any{
			"name": "Supplier A", "ticker": "000001", "market": "SSE",
			"node_id": "node-3", "relationship": "critical_supplier",
			"selection_reason":   "The accepted claim directly binds the supplier to this node.",
			"evidence_claim_ids": []string{"candidate-claim-001"},
		}},
		"conclusion": "One evidence-backed candidate remains.",
	}

	response, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)
	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var encoded string
	require.NoError(t, json.Unmarshal(*response.Output, &encoded))
	var artifact map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &artifact))
	candidates := mapValue[[]any](t, artifact, "candidates")
	assert.Equal(t, "confirmed", valueAs[map[string]any](t, candidates[0])["evidence_status"])

	valueAs[map[string]any](t, mapValue[[]any](t, input, "candidates")[0])["evidence_claim_ids"] = []string{"missing"}
	rejected, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "evidence_claim_id")
}

func TestSubmitCandidateAssessmentUsesControlledMaturityAndNoInvestmentScore(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_candidate_assessment"))
	require.NoError(t, err)
	workingDirectory := blockDirectory(t, "candidate-assessment")
	workspace := filepath.Join(workingDirectory, "001")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	ledgerPath := writeEvidenceLedger(t, workspace, map[string]string{"assessment-claim-001": "reported"})
	artifactPath := filepath.Join(workspace, "assessment.json")
	input := map[string]any{
		"workspace_dir": workspace,
		"artifact_path": artifactPath,
		"ledger_path":   ledgerPath,
		"candidate": map[string]any{
			"name": "Supplier A", "ticker": "000001", "market": "SSE",
			"node_id": "node-3", "relationship": "critical_supplier",
		},
		"relationship_maturity":  "batch_delivery",
		"control_mechanism":      "Qualified production equipment delivery.",
		"evidence_claim_ids":     []string{"assessment-claim-001"},
		"peer_alternatives":      []string{"Supplier B"},
		"switching_constraints":  []string{"Qualification lead time"},
		"falsification":          []string{"Official filings deny production delivery"},
		"what_could_weaken_view": []string{"A qualified second source is disclosed"},
		"conclusion":             "The relationship is reported but not officially confirmed.",
	}

	response, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)
	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var encoded string
	require.NoError(t, json.Unmarshal(*response.Output, &encoded))
	assert.Contains(t, encoded, `"evidence_status": "reported"`)
	assert.NotContains(t, encoded, "demand_inflection")

	input["relationship_maturity"] = "probably_shipping"
	rejected, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "relationship_maturity")
}

func TestSubmitCandidateAssessmentDowngradesUnverifiedKeyClaims(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_candidate_assessment"))
	require.NoError(t, err)
	workingDirectory := blockDirectory(t, "candidate-freshness")
	workspace := filepath.Join(workingDirectory, "001")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	ledgerPath := filepath.Join(workspace, "evidence-ledger.json")
	require.NoError(t, writeJSON(ledgerPath, map[string]any{
		"claims": []any{map[string]any{
			"id": "relationship-claim", "status": "confirmed",
			"evidence_status": "confirmed", "dispute_status": "clean",
		}},
		"freshness_checks": []any{},
	}))
	input := map[string]any{
		"workspace_dir": workspace,
		"artifact_path": filepath.Join(workspace, "assessment.json"),
		"ledger_path":   ledgerPath,
		"candidate": map[string]any{
			"name": "Supplier A", "ticker": "000001", "market": "SSE",
			"node_id": "node-3", "relationship": "critical_supplier",
		},
		"relationship_maturity":  "batch_delivery",
		"control_mechanism":      "Qualified production equipment delivery.",
		"evidence_claim_ids":     []string{"relationship-claim"},
		"key_claim_ids":          []string{"relationship-claim"},
		"peer_alternatives":      []string{"Supplier B"},
		"switching_constraints":  []string{"Qualification lead time"},
		"falsification":          []string{"Official filings deny production delivery"},
		"what_could_weaken_view": []string{"A qualified second source is disclosed"},
		"conclusion":             "The relationship requires a current-source check.",
	}

	response, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var encoded string
	require.NoError(t, json.Unmarshal(*response.Output, &encoded))
	var artifact map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &artifact))
	assert.Equal(t, "pending", artifact["verification_status"])
	assert.NotEmpty(t, artifact["verification_gaps"])
	reviews := mapValue[[]any](t, artifact, "key_claim_reviews")
	assert.Equal(t, "reported", valueAs[map[string]any](t, reviews[0])["effective_evidence_status"])

	input["key_claim_ids"] = []string{"missing-key-claim"}
	rejected, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), workingDirectory)
	require.NoError(t, invokeErr)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "key_claim_id")
}

func TestDynamicSubmissionToolsRejectArtifactsOutsideExplicitWorkspace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tool      string
		claimID   string
		status    string
		artifact  string
		configure func(map[string]any, string)
	}{
		{
			name: "candidate discovery", tool: "submit_candidates",
			claimID: "candidate-claim-001", status: "confirmed", artifact: "candidates.json",
			configure: func(input map[string]any, claimID string) {
				input["node_id"] = "node-3"
				input["max_candidates"] = 1
				input["candidates"] = []any{map[string]any{
					"name": "Supplier A", "ticker": "000001", "market": "SSE",
					"node_id": "node-3", "relationship": "critical_supplier",
					"selection_reason":   "The accepted claim directly supports this relationship.",
					"evidence_claim_ids": []string{claimID},
				}}
				input["conclusion"] = "One candidate remains."
			},
		},
		{
			name: "candidate assessment", tool: "submit_candidate_assessment",
			claimID: "assessment-claim-001", status: "reported", artifact: "assessment.json",
			configure: func(input map[string]any, claimID string) {
				input["candidate"] = map[string]any{
					"name": "Supplier A", "ticker": "000001", "market": "SSE",
					"node_id": "node-3", "relationship": "critical_supplier",
				}
				input["relationship_maturity"] = "batch_delivery"
				input["control_mechanism"] = "Qualified production delivery."
				input["evidence_claim_ids"] = []string{claimID}
				input["peer_alternatives"] = []string{"Supplier B"}
				input["switching_constraints"] = []string{"Qualification lead time"}
				input["falsification"] = []string{"Official filings deny production delivery"}
				input["what_could_weaken_view"] = []string{"A qualified second source is disclosed"}
				input["conclusion"] = "The relationship is reported."
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			program, err := compiler.Compile(t.Context(), goToolSource(t, test.tool))
			require.NoError(t, err)
			physicalWorkspace := blockDirectory(t, test.name+"-physical")
			logicalWorkspace := filepath.Join(filepath.Dir(physicalWorkspace), test.name+"-logical", "3")
			require.NoError(t, os.MkdirAll(logicalWorkspace, 0o700))
			ledgerPath := writeEvidenceLedger(t, logicalWorkspace, map[string]string{test.claimID: test.status})
			outsideWorkspace := filepath.Join(filepath.Dir(physicalWorkspace), test.name+"-outside")
			artifactPath := filepath.Join(outsideWorkspace, test.artifact)
			input := map[string]any{
				"workspace_dir": logicalWorkspace,
				"artifact_path": artifactPath,
				"ledger_path":   ledgerPath,
			}
			test.configure(input, test.claimID)

			response, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), physicalWorkspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), "artifact_path")
			assert.NoFileExists(t, artifactPath)

			outsideRun := filepath.Join(t.TempDir(), "outside-run")
			input["workspace_dir"] = outsideRun
			input["artifact_path"] = filepath.Join(outsideRun, test.artifact)
			input["ledger_path"] = filepath.Join(outsideRun, "evidence-ledger.json")
			rejected, invokeErr := program.Invoke(t.Context(), marshalInput(t, input), physicalWorkspace)

			require.NoError(t, invokeErr)
			assert.False(t, rejected.Accepted)
			assert.Contains(t, issueCodes(rejected), "workspace_dir")
			assert.NoDirExists(t, outsideRun)
		})
	}
}

func validScopeInput(artifactPath string) map[string]any {
	coverage := make([]any, 0, len(trackNames))
	components := make([]string, 0, len(trackNames))
	coverageIDs := []string{
		"product-ddr-die",
		"process-packaging",
		"equipment-lithography",
		"material-silicon-wafer",
		"qualification-dimm",
	}
	stages := []string{"product_definition", "wafer_fabrication", "packaging", "module_assembly", "system_qualification"}
	for index, track := range trackNames {
		component := []string{"DDR die", "packaged memory", "lithography equipment", "silicon wafer", "qualified DIMM"}[index]
		components = append(components, component)
		coverage = append(coverage, map[string]any{
			"id":          coverageIDs[index],
			"description": "Research " + component + ".",
			"track":       track,
			"components":  []string{component},
			"stages":      []string{stages[index]},
		})
	}
	return map[string]any{
		"artifact_path":       artifactPath,
		"topic":               "CXMT DDR memory",
		"focal_product":       "DDR memory centered on CXMT DRAM dies",
		"product_variants":    []string{"DDR4", "DDR5"},
		"expected_components": components,
		"expected_stages":     stages,
		"upstream_boundaries": []string{"production equipment", "input materials"},
		"downstream_boundary": "system qualification",
		"coverage_items":      coverage,
		"open_questions":      []string{"Which product details are publicly confirmed?"},
	}
}

func writeEvidenceLedger(t *testing.T, workingDirectory string, statuses map[string]string) string {
	t.Helper()
	path := filepath.Join(workingDirectory, "evidence-ledger.json")
	claims := make([]any, 0, len(statuses))
	for id, status := range statuses {
		claims = append(claims, map[string]any{"id": id, "status": status})
	}
	require.NoError(t, writeJSON(path, map[string]any{"claims": claims, "sources": []any{}, "gaps": []any{}}))
	return path
}

func validSupplyChainInput(t *testing.T, workingDirectory string) map[string]any {
	t.Helper()
	scopePath := filepath.Join(workingDirectory, "scope.json")
	scope := validScopeInput(scopePath)
	require.NoError(t, writeJSON(scopePath, scope))

	artifacts := make([]string, 0, len(trackNames))
	claimIDs := make([]string, 0, len(trackNames))
	for _, track := range trackNames {
		claimID := fmt.Sprintf("%s-claim-1", track)
		claimIDs = append(claimIDs, claimID)
		path := filepath.Join(workingDirectory, track, "evidence-ledger.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, writeJSON(path, map[string]any{
			"track":  track,
			"claims": []any{map[string]any{"id": claimID, "status": "confirmed"}},
		}))
		artifacts = append(artifacts, path)
	}
	reconciledArtifact := filepath.Join(workingDirectory, "evidence-resolution.json")
	reconciledClaims := make([]any, 0, len(claimIDs))
	reconciledClaims = append(reconciledClaims, map[string]any{
		"id": "baseline-claim-1", "status": "confirmed",
	})
	for _, claimID := range claimIDs {
		reconciledClaims = append(reconciledClaims, map[string]any{"id": claimID, "status": "confirmed"})
	}
	require.NoError(t, writeJSON(reconciledArtifact, map[string]any{
		"claims": reconciledClaims, "conflicts": []any{},
	}))

	coverageItems := mapValue[[]any](t, scope, "coverage_items")
	nodes := make([]any, 0, len(coverageItems))
	coverage := make([]any, 0, len(coverageItems))
	for index, raw := range coverageItems {
		item := valueAs[map[string]any](t, raw)
		nodeID := fmt.Sprintf("node-%d", index+1)
		nodeType := []string{"product", "process", "equipment", "material", "qualification"}[index]
		nodes = append(nodes, map[string]any{
			"id": nodeID, "name": item["description"], "type": nodeType,
			"function": "Represent one declared scope item.", "stages": item["stages"],
			"status": "supported", "evidence_claim_ids": []string{claimIDs[index]},
			"unknown_reason": "", "terminal": index == 2 || index == 3 || index == 4,
			"stop_reason": map[bool]string{true: "Declared research boundary.", false: ""}[index >= 2],
		})
		coverage = append(coverage, map[string]any{
			"id": item["id"], "status": "covered", "node_ids": []string{nodeID},
			"evidence_claim_ids": []string{claimIDs[index]}, "explanation": "Covered by an evidence-backed node.",
			"research_attempt": "", "impact": "",
		})
	}
	edges := make([]any, 0, len(nodes)-1)
	for index := 1; index < len(nodes); index++ {
		edges = append(edges, map[string]any{
			"supplier_node_id": fmt.Sprintf("node-%d", index+1), "consumer_node_id": "node-1",
			"relation": "supplies", "status": "supported",
			"evidence_claim_ids": []string{claimIDs[index]}, "unknown_reason": "",
		})
	}
	return map[string]any{
		"artifact_path":       filepath.Join(workingDirectory, "supply-chain.json"),
		"scope_artifact":      scopePath,
		"topic":               "CXMT DDR memory",
		"focal_node_id":       "node-1",
		"reviewed_artifacts":  artifacts,
		"reconciled_artifact": reconciledArtifact,
		"coverage":            coverage,
		"nodes":               nodes,
		"edges":               edges,
		"chokepoints": []any{map[string]any{
			"node_id": "node-3", "mechanisms": []string{"specialized equipment"},
			"why_selected": "Capacity depends on scarce tools.", "delivery_impact": "production_stop",
			"substitutability": "no_known_substitute", "supplier_concentration": "concentrated",
			"switching_time_min_days": 365, "switching_time_max_days": 1095,
			"recovery_time_min_days": 730, "recovery_time_max_days": 1825,
			"evidence_claim_ids": []string{claimIDs[2]},
		}},
		"conclusion": "The declared chain is covered, with explicit research boundaries.",
	}
}

func stageSupplyChain(t *testing.T, stage *gotool.Program, workingDirectory string, input map[string]any) {
	t.Helper()
	stages := []map[string]any{
		{
			"section": "metadata", "batch_id": "main",
			"metadata": map[string]any{
				"scope_artifact": input["scope_artifact"], "topic": input["topic"],
				"focal_node_id": input["focal_node_id"], "reviewed_artifacts": input["reviewed_artifacts"],
				"reconciled_artifact": input["reconciled_artifact"],
				"conclusion":          input["conclusion"],
			},
		},
		{"section": "nodes", "batch_id": "main", "nodes": input["nodes"]},
		{"section": "edges", "batch_id": "main", "edges": input["edges"]},
		{"section": "coverage", "batch_id": "main", "coverage": input["coverage"]},
		{"section": "chokepoints", "batch_id": "main", "chokepoints": input["chokepoints"]},
	}
	for _, stageInput := range stages {
		response, err := stage.Invoke(t.Context(), marshalInput(t, stageInput), workingDirectory)
		require.NoError(t, err)
		require.True(t, response.Accepted, "section %s issues: %#v", stageInput["section"], response.Issues)
	}
}

func blockDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), ".r42", "runs", "run", "blocks", name)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	return directory
}

func goToolSource(t *testing.T, name string) string {
	t.Helper()
	for _, filename := range []string{"tools.r42.hcl", "evidence_tools.r42.hcl"} {
		parser := hclparse.NewParser()
		file, diagnostics := parser.ParseHCLFile(filename)
		require.False(t, diagnostics.HasErrors(), diagnostics.Error())
		body, ok := file.Body.(*hclsyntax.Body)
		require.True(t, ok)
		for _, block := range body.Blocks {
			if block.Type != "go_tool" || len(block.Labels) != 1 || block.Labels[0] != name {
				continue
			}
			value, valueDiagnostics := block.Body.Attributes["source"].Expr.Value(nil)
			require.False(t, valueDiagnostics.HasErrors(), valueDiagnostics.Error())
			return value.AsString()
		}
	}
	require.FailNow(t, "go tool not found", "name=%s", name)
	return ""
}

func marshalInput(t *testing.T, input any) json.RawMessage {
	t.Helper()
	value, err := json.Marshal(input)
	require.NoError(t, err)
	return value
}

func writeJSON(path string, input any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

func cloneMap(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	var output map[string]any
	require.NoError(t, json.Unmarshal(marshalInput(t, input), &output))
	return output
}

func cloneSlice(t *testing.T, input []any) []any {
	t.Helper()
	var output []any
	require.NoError(t, json.Unmarshal(marshalInput(t, input), &output))
	return output
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	return payload
}

func mapValue[T any](t *testing.T, input map[string]any, key string) T {
	t.Helper()
	return valueAs[T](t, input[key])
}

func valueAs[T any](t *testing.T, input any) T {
	t.Helper()
	value, ok := input.(T)
	require.True(t, ok, "unexpected value type %T", input)
	return value
}

func issueCodes(response gotool.Response) []string {
	codes := make([]string, 0, len(response.Issues))
	for _, issue := range response.Issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func valueKeys(value cty.Value) []string {
	values := value.AsValueMap()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
