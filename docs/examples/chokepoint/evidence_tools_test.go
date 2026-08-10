//nolint:paralleltest // Inline Go compiler integration tests run serially to cap process and CPU pressure.
package chokepoint_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type evidencePrograms struct {
	register    *gotool.Program
	stageClaims *gotool.Program
	stageGaps   *gotool.Program
	finalize    *gotool.Program
}

type evidenceFixture struct {
	workspace string
	scope     string
	ledger    string
	registry  string
	sources   string
}

type evidenceSourceSpec struct {
	suffix         string
	url            string
	canonicalURL   string
	sourceType     string
	reportingBasis string
	provenance     string
	text           string
}

func TestEvidenceToolsCreateExplicitDynamicTaskWorkspace(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	physicalWorkspace := blockDirectory(t, "dynamic-physical")
	logicalWorkspace := filepath.Join(filepath.Dir(physicalWorkspace), "dynamic-parent", "3")
	ledgerPath := filepath.Join(logicalWorkspace, "evidence-ledger.json")

	response, err := programs.stageGaps.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": logicalWorkspace,
		"ledger_path":   ledgerPath,
		"gaps": []any{map[string]any{
			"coverage_item_id": "node-3", "reason": "No public relationship survived review.",
			"research_attempt": "Reviewed the available primary sources.",
			"impact":           "The relationship remains unknown.",
		}},
	}), physicalWorkspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.DirExists(t, logicalWorkspace)
	assert.FileExists(t, filepath.Join(logicalWorkspace, ".evidence-draft", "gaps", "node-3.json"))

	tests := []struct {
		name    string
		program *gotool.Program
		input   func(string) map[string]any
	}{
		{
			name: "register source", program: programs.register,
			input: func(outsideRun string) map[string]any {
				return map[string]any{
					"workspace_dir": outsideRun, "ledger_path": filepath.Join(outsideRun, "evidence-ledger.json"),
					"url": "https://example.com/source", "title": "Source", "publisher": "Publisher",
					"publication_date": "2026-08-01", "accessed_at": "2026-08-09", "source_type": "official_filing",
					"snapshot_path": filepath.Join(outsideRun, "source.md"), "named_entities": []string{},
				}
			},
		},
		{
			name: "stage claims", program: programs.stageClaims,
			input: func(outsideRun string) map[string]any {
				return map[string]any{
					"workspace_dir": outsideRun, "ledger_path": filepath.Join(outsideRun, "evidence-ledger.json"),
					"claims": []any{},
				}
			},
		},
		{
			name: "stage gaps", program: programs.stageGaps,
			input: func(outsideRun string) map[string]any {
				return map[string]any{
					"workspace_dir": outsideRun, "ledger_path": filepath.Join(outsideRun, "evidence-ledger.json"),
					"gaps": []any{},
				}
			},
		},
		{
			name: "finalize ledger", program: programs.finalize,
			input: func(outsideRun string) map[string]any {
				return map[string]any{
					"workspace_dir": outsideRun, "ledger_path": filepath.Join(outsideRun, "evidence-ledger.json"),
					"source_registry_path": filepath.Join(outsideRun, "source-registry.json"),
					"mode":                 "candidate", "topic": "topic", "as_of_date": "2026-08-09",
					"scope_artifact": "", "track": "",
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name+" rejects workspace outside current run", func(t *testing.T) {
			outsideRun := filepath.Join(t.TempDir(), "outside-run")
			rejected, invokeErr := test.program.Invoke(
				t.Context(),
				marshalInput(t, test.input(outsideRun)),
				physicalWorkspace,
			)

			require.NoError(t, invokeErr)
			assert.False(t, rejected.Accepted)
			assert.Contains(t, issueCodes(rejected), "workspace_dir")
			assert.NoDirExists(t, outsideRun)
		})
	}
}

func TestEvidenceToolsHandleUnpairedSurrogates(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "surrogate", []string{"equipment"})
	snapshot := filepath.Join(fixture.sources, "official.md")
	require.NoError(t, os.WriteFile(snapshot, []byte("CXMT confirms Supplier A supplies production equipment."), 0o600))
	registerInput := map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger, "url": "https://example.com/official",
		"title": "SURROGATE", "publisher": "CXMT", "publication_date": "2026-08-01",
		"accessed_at": "2026-08-09", "source_type": "official_filing",
		"snapshot_path": snapshot, "named_entities": []string{"CXMT"},
	}
	registerPayload := bytes.ReplaceAll(marshalInput(t, registerInput), []byte("SURROGATE"), []byte(`\udc81`))
	registered, err := programs.register.Invoke(t.Context(), registerPayload, fixture.workspace)
	require.NoError(t, err)
	require.True(t, registered.Accepted, "issues: %#v", registered.Issues)
	sourceID := valueAs[string](t, outputMap(t, registered)["source_id"])

	claim := validEvidenceClaim(sourceID, "direct", []string{"equipment"})
	claim["inference"] = "SURROGATE"
	claimPayload := bytes.ReplaceAll(marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger, "claims": []any{claim},
	}), []byte("SURROGATE"), []byte(`\udc8f`))
	staged, err := programs.stageClaims.Invoke(t.Context(), claimPayload, fixture.workspace)
	require.NoError(t, err)
	require.True(t, staged.Accepted, "issues: %#v", staged.Issues)

	files, err := filepath.Glob(filepath.Join(fixture.workspace, ".evidence-draft", "claims", "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	payload, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.True(t, utf8.Valid(payload))
	assert.Contains(t, string(payload), "�")
}

func TestEvidenceLedgerComputesEvidenceStatus(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		directness string
		expected   string
	}{
		{name: "official direct", sourceType: "official_filing", directness: "direct", expected: "confirmed"},
		{name: "media direct", sourceType: "credible_media", directness: "direct", expected: "reported"},
		{name: "indirect source", sourceType: "industry_research", directness: "indirect", expected: "inferred"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, programs := compileEvidencePrograms(t)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			fixture := newEvidenceFixture(t, test.name, []string{"equipment"})
			sourceID := registerEvidenceSource(t, programs.register, fixture, test.sourceType, "support", "2026-08-01")
			stageEvidenceClaims(t, programs.stageClaims, fixture, []any{
				validEvidenceClaim(sourceID, test.directness, []string{"equipment"}),
			})

			response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

			require.True(t, response.Accepted, "issues: %#v", response.Issues)
			var ledger map[string]any
			require.NoError(t, readJSON(fixture.ledger, &ledger))
			claims := mapValue[[]any](t, ledger, "claims")
			assert.Equal(t, test.expected, valueAs[map[string]any](t, claims[0])["status"])
		})
	}
}

func TestRegisterEvidenceSourceDerivesCanonicalOriginAndToleratesUnknownClass(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "source-origin", []string{"equipment"})
	canonicalURL := "HTTPS://Example.com/original/#details"

	first := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "mirror-a", url: "https://mirror-a.example/story", canonicalURL: canonicalURL,
		sourceType: "qualified_media", reportingBasis: "named_source", provenance: "syndication",
		text: "Supplier A disclosed a production relationship.",
	})
	second := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "mirror-b", url: "https://mirror-b.example/story", canonicalURL: canonicalURL,
		sourceType: "qualified_media", reportingBasis: "named_source", provenance: "syndication",
		text: "A mirror reports Supplier A's production relationship.",
	})
	unknown := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "unknown", url: "https://unknown.example/story", canonicalURL: "https://unknown.example/story",
		sourceType: "specialist_trade_press", reportingBasis: "unclear", provenance: "unclear",
		text: "An unclassified publication mentions Supplier A.",
	})

	assert.NotEqual(t, first["source_id"], second["source_id"])
	assert.Equal(t, first["origin_id"], second["origin_id"])
	assert.Equal(t, "https://example.com/original", first["canonical_url"])
	assert.Equal(t, "unknown", unknown["source_class"])
}

func TestEvidenceRegistryCountsIndependentGroups(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "independent-group-count", []string{"equipment"})
	text := "CXMT confirms Supplier A supplies production equipment."
	first := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "publisher-a", url: "https://publisher-a.example/story",
		canonicalURL: "https://publisher-a.example/story", sourceType: "qualified_media",
		reportingBasis: "anonymous_sources", provenance: "original", text: text,
	})
	registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "publisher-b", url: "https://publisher-b.example/reprint",
		canonicalURL: "https://publisher-b.example/reprint", sourceType: "qualified_media",
		reportingBasis: "anonymous_sources", provenance: "original", text: text,
	})
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{
		validEvidenceClaim(valueAs[string](t, first["source_id"]), "direct", []string{"equipment"}),
	})

	response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var registry map[string]any
	require.NoError(t, readJSON(fixture.registry, &registry))
	assert.EqualValues(t, 2, registry["source_record_count"])
	assert.EqualValues(t, 2, registry["unique_canonical_url_count"])
	assert.EqualValues(t, 1, registry["independent_origin_count"])
}

func TestEvidenceLedgerCollapsesTransitiveIndependenceBridge(t *testing.T) {
	tests := []struct {
		name              string
		registrationOrder []string
		cited             []string
	}{
		{
			name:              "same origin split by content bridge",
			registrationOrder: []string{"a-x", "b-x", "b-y"},
			cited:             []string{"b-x", "b-y"},
		},
		{
			name:              "transitive endpoints share one component",
			registrationOrder: []string{"a-x", "b-x", "b-y"},
			cited:             []string{"a-x", "b-y"},
		},
		{
			name:              "registration order does not change component",
			registrationOrder: []string{"b-y", "b-x", "a-x"},
			cited:             []string{"a-x", "b-y"},
		},
	}
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEvidenceFixture(t, test.name, []string{"equipment"})
			specs := map[string]evidenceSourceSpec{
				"a-x": {
					suffix: "a-x", url: "https://mirror-a.example/story", canonicalURL: "https://publisher-a.example/story",
					sourceType: "qualified_media", reportingBasis: "anonymous_sources", provenance: "original",
					text: "CXMT confirms Supplier A supplies production equipment.",
				},
				"b-x": {
					suffix: "b-x", url: "https://mirror-b.example/reprint", canonicalURL: "https://publisher-b.example/story",
					sourceType: "qualified_media", reportingBasis: "anonymous_sources", provenance: "original",
					text: "CXMT confirms Supplier A supplies production equipment.",
				},
				"b-y": {
					suffix: "b-y", url: "https://publisher-b.example/update", canonicalURL: "https://publisher-b.example/story",
					sourceType: "qualified_media", reportingBasis: "anonymous_sources", provenance: "original",
					text: "A follow-up says Supplier A supplies production equipment.",
				},
			}
			sources := make(map[string]map[string]any, len(specs))
			for _, key := range test.registrationOrder {
				sources[key] = registerClassifiedEvidenceSource(t, programs.register, fixture, specs[key])
			}
			claim := validEvidenceClaim(valueAs[string](t, sources[test.cited[0]]["source_id"]), "direct", []string{"equipment"})
			firstEvidence := valueAs[map[string]any](t, mapValue[[]any](t, claim, "evidence")[0])
			firstEvidence["authority_for_claim"] = false
			firstEvidence["exact_quote"] = specs[test.cited[0]].text
			claim["evidence"] = append(mapValue[[]any](t, claim, "evidence"), map[string]any{
				"source_id": sources[test.cited[1]]["source_id"], "relation": "supports", "directness": "direct",
				"authority_for_claim": false, "locator": "paragraph 1", "exact_quote": specs[test.cited[1]].text,
			})
			stageEvidenceClaims(t, programs.stageClaims, fixture, []any{claim})

			response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

			require.True(t, response.Accepted, "issues: %#v", response.Issues)
			var ledger map[string]any
			require.NoError(t, readJSON(fixture.ledger, &ledger))
			actual := valueAs[map[string]any](t, mapValue[[]any](t, ledger, "claims")[0])
			assert.Equal(t, "reported", actual["evidence_status"])
			assert.Equal(t, "none", actual["confirmation_basis"])
			assert.EqualValues(t, 1, actual["independent_support_origins"])
			groups := map[string]struct{}{}
			for _, raw := range mapValue[[]any](t, ledger, "sources") {
				source := valueAs[map[string]any](t, raw)
				groups[valueAs[string](t, source["independence_group"])] = struct{}{}
			}
			assert.Len(t, groups, 1)
			var registry map[string]any
			require.NoError(t, readJSON(fixture.registry, &registry))
			assert.EqualValues(t, 1, registry["independent_origin_count"])
		})
	}
}

func TestEvidenceLedgerSeparatesEvidenceStrengthFromDisputeState(t *testing.T) {
	tests := []struct {
		name             string
		sourceType       string
		reportingBasis   string
		directness       string
		authority        bool
		inference        string
		expectedEvidence string
		expectedDispute  string
		expectedStatus   string
		expectedBasis    string
	}{
		{
			name: "authoritative primary", sourceType: "authoritative_primary", reportingBasis: "public_document",
			directness: "direct", authority: true, expectedEvidence: "confirmed", expectedDispute: "clean",
			expectedStatus: "confirmed", expectedBasis: "official_primary",
		},
		{
			name: "qualified named reporting", sourceType: "qualified_media", reportingBasis: "named_source",
			directness: "direct", expectedEvidence: "confirmed", expectedDispute: "clean",
			expectedStatus: "confirmed", expectedBasis: "high_quality_media",
		},
		{
			name: "single anonymous report", sourceType: "qualified_media", reportingBasis: "anonymous_sources",
			directness: "direct", expectedEvidence: "reported", expectedDispute: "clean",
			expectedStatus: "reported", expectedBasis: "none",
		},
		{
			name: "explicit analysis remains inferred", sourceType: "authoritative_primary", reportingBasis: "public_document",
			directness: "direct", authority: true, inference: "The accepted facts imply a switching risk.",
			expectedEvidence: "inferred", expectedDispute: "clean", expectedStatus: "inferred", expectedBasis: "none",
		},
	}
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEvidenceFixture(t, test.name, []string{"equipment"})
			source := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
				suffix: "support", url: "https://example.com/" + fmt.Sprintf("%x", test.name),
				canonicalURL: "https://example.com/" + fmt.Sprintf("%x", test.name), sourceType: test.sourceType,
				reportingBasis: test.reportingBasis, provenance: "original",
				text: "CXMT confirms Supplier A supplies production equipment.",
			})
			claim := validEvidenceClaim(valueAs[string](t, source["source_id"]), test.directness, []string{"equipment"})
			claim["inference"] = test.inference
			evidence := valueAs[map[string]any](t, mapValue[[]any](t, claim, "evidence")[0])
			evidence["authority_for_claim"] = test.authority
			stageEvidenceClaims(t, programs.stageClaims, fixture, []any{claim})

			response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

			require.True(t, response.Accepted, "issues: %#v", response.Issues)
			var ledger map[string]any
			require.NoError(t, readJSON(fixture.ledger, &ledger))
			actual := valueAs[map[string]any](t, mapValue[[]any](t, ledger, "claims")[0])
			assert.Equal(t, test.expectedEvidence, actual["evidence_status"])
			assert.Equal(t, test.expectedDispute, actual["dispute_status"])
			assert.Equal(t, test.expectedStatus, actual["status"])
			assert.Equal(t, test.expectedBasis, actual["confirmation_basis"])
		})
	}
}

func TestEvidenceLedgerRequiresIndependentOriginsForAnonymousMediaConfirmation(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "anonymous-corroboration", []string{"equipment"})
	first := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "anonymous-a", url: "https://media-a.example/story", canonicalURL: "https://media-a.example/story",
		sourceType: "qualified_media", reportingBasis: "anonymous_sources", provenance: "original",
		text: "CXMT confirms Supplier A supplies production equipment.",
	})
	second := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "anonymous-b", url: "https://media-b.example/story", canonicalURL: "https://media-b.example/story",
		sourceType: "qualified_media", reportingBasis: "anonymous_sources", provenance: "original",
		text: "A second investigation confirms Supplier A supplies production equipment.",
	})
	claim := validEvidenceClaim(valueAs[string](t, first["source_id"]), "direct", []string{"equipment"})
	claim["evidence"] = append(mapValue[[]any](t, claim, "evidence"), map[string]any{
		"source_id": second["source_id"], "relation": "supports", "directness": "direct",
		"authority_for_claim": false, "locator": "paragraph 1",
		"exact_quote": "A second investigation confirms Supplier A supplies production equipment.",
	})
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{claim})

	response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var ledger map[string]any
	require.NoError(t, readJSON(fixture.ledger, &ledger))
	actual := valueAs[map[string]any](t, mapValue[[]any](t, ledger, "claims")[0])
	assert.Equal(t, "confirmed", actual["evidence_status"])
	assert.Equal(t, "corroborated_media", actual["confirmation_basis"])
	assert.EqualValues(t, 2, actual["independent_support_origins"])
}

func TestEvidenceLedgerTreatsWeakContradictionAsChallenge(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "weak-challenge", []string{"equipment"})
	support := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "support", url: "https://authority.example/filing", canonicalURL: "https://authority.example/filing",
		sourceType: "authoritative_primary", reportingBasis: "public_document", provenance: "original",
		text: "CXMT confirms Supplier A supplies production equipment.",
	})
	challenge := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
		suffix: "contradiction", url: "https://forum.example/post", canonicalURL: "https://forum.example/post",
		sourceType: "lead_only", reportingBasis: "unspecified", provenance: "original",
		text: "CXMT says Supplier A is not a production supplier.",
	})
	claim := validEvidenceClaim(valueAs[string](t, support["source_id"]), "direct", []string{"equipment"})
	valueAs[map[string]any](t, mapValue[[]any](t, claim, "evidence")[0])["authority_for_claim"] = true
	claim["evidence"] = append(mapValue[[]any](t, claim, "evidence"), map[string]any{
		"source_id": challenge["source_id"], "relation": "contradicts", "directness": "direct",
		"authority_for_claim": false, "locator": "paragraph 1",
		"exact_quote": "CXMT says Supplier A is not a production supplier.",
	})
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{claim})

	response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var ledger map[string]any
	require.NoError(t, readJSON(fixture.ledger, &ledger))
	actual := valueAs[map[string]any](t, mapValue[[]any](t, ledger, "claims")[0])
	assert.Equal(t, "confirmed", actual["evidence_status"])
	assert.Equal(t, "challenged", actual["dispute_status"])
	assert.Equal(t, "confirmed", actual["status"])
}

func TestEvidenceLedgerRequiresIndependentAnonymousContradictions(t *testing.T) {
	tests := []struct {
		name            string
		contradictions  int
		expectedDispute string
	}{
		{name: "one anonymous contradiction challenges", contradictions: 1, expectedDispute: "challenged"},
		{name: "two independent anonymous contradictions dispute", contradictions: 2, expectedDispute: "disputed"},
	}
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEvidenceFixture(t, test.name, []string{"equipment"})
			support := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
				suffix: "support", url: "https://authority.example/filing",
				canonicalURL: "https://authority.example/filing", sourceType: "authoritative_primary",
				reportingBasis: "public_document", provenance: "original",
				text: "CXMT confirms Supplier A supplies production equipment.",
			})
			claim := validEvidenceClaim(valueAs[string](t, support["source_id"]), "direct", []string{"equipment"})
			first := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
				suffix: "anonymous-a", url: "https://media-a.example/story",
				canonicalURL: "https://media-a.example/story", sourceType: "qualified_media",
				reportingBasis: "anonymous_sources", provenance: "original",
				text: "An anonymous source says Supplier A is not a production supplier.",
			})
			claim["evidence"] = append(mapValue[[]any](t, claim, "evidence"), map[string]any{
				"source_id": first["source_id"], "relation": "contradicts", "directness": "direct",
				"authority_for_claim": false, "locator": "paragraph 1",
				"exact_quote": "An anonymous source says Supplier A is not a production supplier.",
			})
			if test.contradictions == 2 {
				second := registerClassifiedEvidenceSource(t, programs.register, fixture, evidenceSourceSpec{
					suffix: "anonymous-b", url: "https://media-b.example/story",
					canonicalURL: "https://media-b.example/story", sourceType: "qualified_media",
					reportingBasis: "anonymous_sources", provenance: "original",
					text: "A separate investigation says Supplier A is not a production supplier.",
				})
				claim["evidence"] = append(mapValue[[]any](t, claim, "evidence"), map[string]any{
					"source_id": second["source_id"], "relation": "contradicts", "directness": "direct",
					"authority_for_claim": false, "locator": "paragraph 1",
					"exact_quote": "A separate investigation says Supplier A is not a production supplier.",
				})
			}
			stageEvidenceClaims(t, programs.stageClaims, fixture, []any{claim})

			response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

			require.True(t, response.Accepted, "issues: %#v", response.Issues)
			var ledger map[string]any
			require.NoError(t, readJSON(fixture.ledger, &ledger))
			actual := valueAs[map[string]any](t, mapValue[[]any](t, ledger, "claims")[0])
			assert.Equal(t, test.expectedDispute, actual["dispute_status"])
		})
	}
}

func TestEvidenceLedgerMarksContradictoryEvidenceDisputed(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "disputed", []string{"equipment"})
	supporting := registerEvidenceSource(t, programs.register, fixture, "official_filing", "support", "2026-08-01")
	contradicting := registerEvidenceSource(t, programs.register, fixture, "official_statement", "contradiction", "2026-08-01")
	claim := validEvidenceClaim(supporting, "direct", []string{"equipment"})
	claim["evidence"] = append(mapValue[[]any](t, claim, "evidence"), map[string]any{
		"source_id": contradicting, "relation": "contradicts", "directness": "direct",
		"authority_for_claim": true,
		"locator":             "paragraph 1", "exact_quote": "CXMT says Supplier A is not a production supplier.",
	})
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{claim})

	response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var ledger map[string]any
	require.NoError(t, readJSON(fixture.ledger, &ledger))
	claims := mapValue[[]any](t, ledger, "claims")
	assert.Equal(t, "disputed", valueAs[map[string]any](t, claims[0])["status"])
}

func TestEvidenceLedgerRequiresClaimOrGapForEveryTrackItem(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "coverage", []string{"equipment", "materials"})
	sourceID := registerEvidenceSource(t, programs.register, fixture, "official_filing", "support", "2026-08-01")
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{
		validEvidenceClaim(sourceID, "direct", []string{"equipment"}),
	})

	rejected := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "coverage_item")

	gap, err := programs.stageGaps.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger,
		"gaps": []any{map[string]any{
			"coverage_item_id": "materials", "reason": "No public supplier disclosure was found.",
			"research_attempt": "Reviewed the current official filings.",
			"impact":           "Material-source risk remains unresolved.",
		}},
	}), fixture.workspace)
	require.NoError(t, err)
	require.True(t, gap.Accepted, "issues: %#v", gap.Issues)
	accepted := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")
	assert.True(t, accepted.Accepted, "issues: %#v", accepted.Issues)
}

func TestStageEvidenceClaimsRejectsInvalidBatches(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "claim-validation", []string{"equipment"})
	sourceID := registerEvidenceSource(t, programs.register, fixture, "official_filing", "support", "2026-08-01")
	tests := []struct {
		name         string
		claims       func() []any
		expectedCode string
	}{
		{
			name: "more than five claims",
			claims: func() []any {
				claims := make([]any, 6)
				for index := range claims {
					claim := validEvidenceClaim(sourceID, "direct", []string{"equipment"})
					claim["id"] = fmt.Sprintf("claim-%d", index)
					claims[index] = claim
				}
				return claims
			},
			expectedCode: "batch_size",
		},
		{
			name: "invalid supplier maturity",
			claims: func() []any {
				claim := validEvidenceClaim(sourceID, "direct", []string{"equipment"})
				claim["claim_type"] = "supplier_maturity"
				claim["value"] = "probably_shipping"
				return []any{claim}
			},
			expectedCode: "supplier_maturity",
		},
		{
			name: "quantitative claim without qualifiers",
			claims: func() []any {
				claim := validEvidenceClaim(sourceID, "direct", []string{"equipment"})
				claim["claim_type"] = "quantitative"
				claim["qualifiers"] = map[string]string{}
				return []any{claim}
			},
			expectedCode: "quantitative_qualifiers",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := programs.stageClaims.Invoke(t.Context(), marshalInput(t, map[string]any{
				"workspace_dir": fixture.workspace,
				"ledger_path":   fixture.ledger, "claims": test.claims(),
			}), fixture.workspace)
			require.NoError(t, err)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
		})
	}
}

func TestFinalizeEvidenceLedgerRejectsFutureSource(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "future", []string{"equipment"})
	sourceID := registerEvidenceSource(t, programs.register, fixture, "official_filing", "future", "2026-08-10")
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{
		validEvidenceClaim(sourceID, "direct", []string{"equipment"}),
	})

	response := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "source_after_as_of_date")
}

func TestCandidateEvidenceLedgerAcceptsExplicitGapWithoutSource(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	fixture := newEvidenceFixture(t, "candidate-gap", []string{"equipment"})
	response, err := programs.stageGaps.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger,
		"gaps": []any{map[string]any{
			"coverage_item_id": "node-3", "reason": "No public company relationship survived review.",
			"research_attempt": "Searched official filings and issuer disclosures.",
			"impact":           "No candidate can be confirmed for this chokepoint.",
		}},
	}), fixture.workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)

	finalized := finalizeEvidenceLedger(t, programs.finalize, fixture, "candidate", "")
	assert.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
}

func TestStageClaimFreshnessChecksNormalizesAmbiguousOutcome(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "stage_claim_freshness_checks"))
	require.NoError(t, err)
	fixture := newEvidenceFixture(t, "freshness-check", []string{"equipment"})
	require.NoError(t, os.MkdirAll(filepath.Join(fixture.workspace, ".evidence-draft", "claims"), 0o700))
	require.NoError(t, writeJSON(
		filepath.Join(fixture.workspace, ".evidence-draft", "claims", "claim-001.json"),
		map[string]any{"id": "claim-001"},
	))

	response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger,
		"checks": []any{map[string]any{
			"claim_id": "claim-001", "checked_at": "2026-08-09",
			"official_channels":         []string{"SSE issuer filings"},
			"latest_primary_source_ids": []string{}, "outcome": "unclear",
			"gap": "No authoritative primary source was identified.",
		}},
	}), fixture.workspace)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var check map[string]any
	require.NoError(t, readJSON(
		filepath.Join(fixture.workspace, ".evidence-draft", "freshness", "claim-001.json"),
		&check,
	))
	assert.Equal(t, "not_verified", check["outcome"])
}

func TestStageClaimFreshnessChecksRejectsUnrelatedPrimarySource(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	freshness, err := compiler.Compile(t.Context(), goToolSource(t, "stage_claim_freshness_checks"))
	require.NoError(t, err)
	fixture := newEvidenceFixture(t, "unrelated-freshness-source", []string{"equipment"})
	claimSourceID := registerEvidenceSource(t, programs.register, fixture, "official_filing", "claim-source", "2026-08-01")
	unrelatedSourceID := registerEvidenceSource(t, programs.register, fixture, "official_filing", "other-source", "2026-08-02")
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{
		validEvidenceClaim(claimSourceID, "direct", []string{"equipment"}),
	})

	response, invokeErr := freshness.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger,
		"checks": []any{map[string]any{
			"claim_id": "equipment-claim-001", "checked_at": "2026-08-09",
			"official_channels":         []string{"Issuer filings"},
			"latest_primary_source_ids": []string{unrelatedSourceID},
			"outcome":                   "verified_primary", "gap": "",
		}},
	}), fixture.workspace)

	require.NoError(t, invokeErr)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "primary_source")
}

func TestFinalizeEvidenceLedgerRejectsStaleFreshnessCheck(t *testing.T) {
	compiler, programs := compileEvidencePrograms(t)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	freshness, err := compiler.Compile(t.Context(), goToolSource(t, "stage_claim_freshness_checks"))
	require.NoError(t, err)
	fixture := newEvidenceFixture(t, "stale-freshness", []string{"equipment"})
	sourceID := registerEvidenceSource(t, programs.register, fixture, "official_filing", "support", "2026-08-01")
	stageEvidenceClaims(t, programs.stageClaims, fixture, []any{
		validEvidenceClaim(sourceID, "direct", []string{"equipment"}),
	})
	response, invokeErr := freshness.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger,
		"checks": []any{map[string]any{
			"claim_id": "equipment-claim-001", "checked_at": "2026-08-08",
			"official_channels":         []string{"Issuer filings"},
			"latest_primary_source_ids": []string{}, "outcome": "checked_no_primary", "gap": "",
		}},
	}), fixture.workspace)
	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)

	finalized := finalizeEvidenceLedger(t, programs.finalize, fixture, "track", "equipment")

	assert.False(t, finalized.Accepted)
	assert.Contains(t, issueCodes(finalized), "freshness_date")
}

func TestEvidenceReconciliationRequiresConflictResolution(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	resolve, err := compiler.Compile(t.Context(), goToolSource(t, "resolve_evidence_conflict"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	workspace, artifact, ledgers := reconciliationFixture(t, "conflict", true)
	prepared, err := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "ledger_paths": ledgers,
	}), workspace)
	require.NoError(t, err)
	require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
	preparedOutput := outputMap(t, prepared)
	assert.EqualValues(t, 1, preparedOutput["conflict_count"])

	rejected, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{"artifact_path": artifact}), workspace)
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Contains(t, issueCodes(rejected), "unresolved_conflict")

	conflictID := valueAs[string](t, valueAs[[]any](t, preparedOutput["conflict_ids"])[0])
	resolved, err := resolve.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "conflict_id": conflictID, "decision": "prefer",
		"chosen_claim_ids": []string{"claim-new"},
		"rationale":        "The newer official filing supersedes the media estimate.",
	}), workspace)
	require.NoError(t, err)
	require.True(t, resolved.Accepted, "issues: %#v", resolved.Issues)
	finalized, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{"artifact_path": artifact}), workspace)
	require.NoError(t, err)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifact, &document))
	conflicts := mapValue[[]any](t, document, "conflicts")
	resolution := mapValue[map[string]any](t, valueAs[map[string]any](t, conflicts[0]), "resolution")
	assert.Equal(t, "prefer", resolution["decision"])
	assert.Equal(t, "r42_evidence_resolution", document["artifact_kind"])
	assert.Equal(t, "finalized", document["reconciliation_status"])
	availability := map[string]string{}
	for _, raw := range mapValue[[]any](t, document, "claims") {
		claim := valueAs[map[string]any](t, raw)
		availability[valueAs[string](t, claim["id"])] = valueAs[string](t, claim["reconciliation_availability"])
	}
	assert.Equal(t, "available", availability["claim-new"])
	assert.Equal(t, "excluded", availability["claim-old"])
}

func TestEvidenceReconciliationIgnoresEqualValues(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	workspace, artifact, ledgers := reconciliationFixture(t, "equal", false)
	prepared, err := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "ledger_paths": ledgers,
	}), workspace)
	require.NoError(t, err)
	require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
	assert.EqualValues(t, 0, outputMap(t, prepared)["conflict_count"])
	finalized, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{"artifact_path": artifact}), workspace)
	require.NoError(t, err)
	assert.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
}

func TestResolveEvidenceConflictEnforcesDecisionSelections(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		chosen   []string
		accepted bool
	}{
		{name: "prefer strict subset", decision: "prefer", chosen: []string{"claim-new"}, accepted: true},
		{name: "prefer rejects empty selection", decision: "prefer", chosen: []string{}},
		{name: "prefer cannot retain every claim", decision: "prefer", chosen: []string{"claim-old", "claim-new"}},
		{name: "prefer rejects duplicate selection", decision: "prefer", chosen: []string{"claim-new", "claim-new"}},
		{name: "preserve both retains every claim", decision: "preserve_both", chosen: []string{"claim-old", "claim-new"}, accepted: true},
		{name: "preserve both rejects subset", decision: "preserve_both", chosen: []string{"claim-new"}},
		{name: "unresolved chooses nothing", decision: "unresolved", chosen: []string{}, accepted: true},
		{name: "unresolved rejects chosen claim", decision: "unresolved", chosen: []string{"claim-new"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
			require.NoError(t, err)
			resolve, err := compiler.Compile(t.Context(), goToolSource(t, "resolve_evidence_conflict"))
			require.NoError(t, err)
			workspace, artifact, ledgers := reconciliationFixture(t, test.name, true)
			prepared, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": artifact, "ledger_paths": ledgers,
			}), workspace)
			require.NoError(t, invokeErr)
			require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
			conflictID := valueAs[string](t, valueAs[[]any](t, outputMap(t, prepared)["conflict_ids"])[0])

			response, invokeErr := resolve.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": artifact, "conflict_id": conflictID, "decision": test.decision,
				"chosen_claim_ids": test.chosen, "rationale": "The retained evidence requires this decision.",
			}), workspace)

			require.NoError(t, invokeErr)
			assert.Equal(t, test.accepted, response.Accepted, "issues: %#v", response.Issues)
			if !test.accepted {
				assert.Contains(t, issueCodes(response), "chosen_claim_ids")
			}
		})
	}
}

func TestResolveEvidenceConflictAggregatesIndependentInputErrors(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	resolve, err := compiler.Compile(t.Context(), goToolSource(t, "resolve_evidence_conflict"))
	require.NoError(t, err)
	tests := []struct {
		name     string
		decision string
		chosen   []string
		want     []string
	}{
		{
			name: "empty prefer selection", decision: "prefer", chosen: []string{},
			want: []string{"invalid_path", "chosen_claim_ids", "rationale"},
		},
		{
			name: "invalid decision", decision: "invalid", chosen: []string{},
			want: []string{"invalid_path", "decision", "rationale"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := blockDirectory(t, test.name+"-resolve-errors")
			outsideArtifact := filepath.Join(filepath.Dir(workspace), "other-reconciliation", "evidence-resolution.json")

			response, invokeErr := resolve.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": outsideArtifact, "conflict_id": "conflict-1", "decision": test.decision,
				"chosen_claim_ids": test.chosen, "rationale": "",
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.ElementsMatch(t, test.want, issueCodes(response))
		})
	}
}

func TestEvidenceReconciliationFinalizesDecisionAvailability(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	resolve, err := compiler.Compile(t.Context(), goToolSource(t, "resolve_evidence_conflict"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	tests := []struct {
		name     string
		decision string
		chosen   []string
		want     map[string]string
	}{
		{
			name: "preserve both keeps every claim available", decision: "preserve_both",
			chosen: []string{"claim-old", "claim-new"},
			want:   map[string]string{"claim-old": "available", "claim-new": "available"},
		},
		{
			name: "unresolved marks every claim unresolved", decision: "unresolved",
			chosen: []string{},
			want:   map[string]string{"claim-old": "unresolved", "claim-new": "unresolved"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, artifact, ledgers := reconciliationFixture(t, test.name, true)
			prepared, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": artifact, "ledger_paths": ledgers,
			}), workspace)
			require.NoError(t, invokeErr)
			require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
			conflictID := valueAs[string](t, valueAs[[]any](t, outputMap(t, prepared)["conflict_ids"])[0])
			resolved, invokeErr := resolve.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": artifact, "conflict_id": conflictID, "decision": test.decision,
				"chosen_claim_ids": test.chosen, "rationale": "Retain the declared conflict outcome.",
			}), workspace)
			require.NoError(t, invokeErr)
			require.True(t, resolved.Accepted, "issues: %#v", resolved.Issues)
			finalized, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"artifact_path": artifact,
			}), workspace)
			require.NoError(t, invokeErr)
			require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)

			var document map[string]any
			require.NoError(t, readJSON(artifact, &document))
			got := map[string]string{}
			for _, raw := range mapValue[[]any](t, document, "claims") {
				claim := valueAs[map[string]any](t, raw)
				got[valueAs[string](t, claim["id"])] = valueAs[string](t, claim["reconciliation_availability"])
			}
			assert.Equal(t, test.want, got)
		})
	}
}

func TestPrepareEvidenceReconciliationRejectsConflictingSourceIdentity(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "source-identity-conflict")
	blocks := filepath.Dir(workspace)
	ledgers := []string{
		filepath.Join(blocks, "source-ledger-a", "evidence-ledger.json"),
		filepath.Join(blocks, "source-ledger-b", "evidence-ledger.json"),
	}
	for index, path := range ledgers {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, writeJSON(path, map[string]any{
			"sources": []any{map[string]any{
				"id": "source-shared", "url": "https://mirror.example/story",
				"canonical_url": fmt.Sprintf("https://publisher-%d.example/story", index),
				"origin_id":     fmt.Sprintf("origin-%d", index),
			}},
			"claims": []any{}, "gaps": []any{},
		}))
	}

	response, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": filepath.Join(workspace, "evidence-resolution.json"),
		"ledger_paths":  ledgers,
	}), workspace)

	require.NoError(t, invokeErr)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "source_id_conflict")
}

func TestPrepareEvidenceReconciliationAcceptsEquivalentSourceCopies(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "equivalent-source-copies")
	blocks := filepath.Dir(workspace)
	ledgers := []string{
		filepath.Join(blocks, "copy-a", "evidence-ledger.json"),
		filepath.Join(blocks, "copy-b", "evidence-ledger.json"),
	}
	for index, path := range ledgers {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, writeJSON(path, map[string]any{
			"sources": []any{map[string]any{
				"id": "source-shared", "url": "https://mirror.example/story",
				"normalized_url": "https://mirror.example/story", "canonical_url": "https://publisher.example/story",
				"origin_id": "origin-story", "content_fingerprint": "content-story", "snapshot_sha256": "snapshot-story",
				"title": "Story", "publisher": "Publisher", "publication_date": "2026-08-01",
				"source_type": "qualified_media", "source_class": "qualified_media",
				"reporting_basis": "named_source", "provenance": "original",
				"snapshot_path":      fmt.Sprintf("D:/different/block-%d/snapshot.md", index),
				"accessed_at":        fmt.Sprintf("2026-08-%02d", index+1),
				"independence_group": fmt.Sprintf("provisional-%d", index),
				"named_entities":     []string{fmt.Sprintf("Entity %d", index)},
			}},
			"claims": []any{}, "gaps": []any{},
		}))
	}

	response, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": filepath.Join(workspace, "evidence-resolution.json"),
		"ledger_paths":  ledgers, "assessment_paths": []string{},
	}), workspace)

	require.NoError(t, invokeErr)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
}

func TestEvidenceReconciliationPreservesInheritedAvailability(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "inherited-availability")
	ledger := filepath.Join(filepath.Dir(workspace), "prior-resolution", "evidence-resolution.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(ledger), 0o700))
	require.NoError(t, writeJSON(ledger, map[string]any{
		"sources": []any{}, "gaps": []any{}, "freshness_checks": []any{},
		"claims": []any{
			map[string]any{
				"id": "claim-excluded", "claim_type": "supplier_maturity", "subject": "Supplier A",
				"predicate": "maturity_at_CXMT", "value": "validation", "qualifiers": map[string]string{"as_of": "2026-08-09"},
				"reconciliation_availability": "excluded", "evidence": []any{},
			},
			map[string]any{
				"id": "claim-available", "claim_type": "supplier_maturity", "subject": "Supplier A",
				"predicate": "maturity_at_CXMT", "value": "mass_production", "qualifiers": map[string]string{"as_of": "2026-08-09"},
				"reconciliation_availability": "available", "evidence": []any{},
			},
			map[string]any{
				"id": "claim-unresolved", "claim_type": "supplier_maturity", "subject": "Supplier B",
				"predicate": "maturity_at_CXMT", "value": "unknown", "qualifiers": map[string]string{"as_of": "2026-08-09"},
				"reconciliation_availability": "unresolved", "evidence": []any{},
			},
		},
	}))
	artifact := filepath.Join(workspace, "evidence-resolution.json")
	prepared, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "ledger_paths": []string{ledger}, "assessment_paths": []string{},
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
	assert.EqualValues(t, 0, outputMap(t, prepared)["conflict_count"])
	finalized, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{"artifact_path": artifact}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifact, &document))
	availability := map[string]string{}
	for _, raw := range mapValue[[]any](t, document, "claims") {
		claim := valueAs[map[string]any](t, raw)
		availability[valueAs[string](t, claim["id"])] = valueAs[string](t, claim["reconciliation_availability"])
	}
	assert.Equal(t, "excluded", availability["claim-excluded"])
	assert.Equal(t, "available", availability["claim-available"])
	assert.Equal(t, "unresolved", availability["claim-unresolved"])
}

func TestEvidenceReconciliationCollapsesCrossLedgerIndependenceBridge(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "cross-ledger-independence")
	blocks := filepath.Dir(workspace)
	sources := []map[string]any{
		{"id": "source-a-x", "origin_id": "origin-a", "content_fingerprint": "content-x", "independence_group": "origin-a"},
		{"id": "source-b-x", "origin_id": "origin-b", "content_fingerprint": "content-x", "independence_group": "origin-b"},
		{"id": "source-b-y", "origin_id": "origin-b", "content_fingerprint": "content-y", "independence_group": "origin-b"},
	}
	ledgers := make([]string, len(sources))
	for index, source := range sources {
		path := filepath.Join(blocks, fmt.Sprintf("independence-%d", index), "evidence-ledger.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, writeJSON(path, map[string]any{
			"sources": []any{source}, "claims": []any{}, "gaps": []any{}, "freshness_checks": []any{},
		}))
		ledgers[index] = path
	}
	artifact := filepath.Join(workspace, "evidence-resolution.json")
	prepared, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "ledger_paths": ledgers, "assessment_paths": []string{},
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
	finalized, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{"artifact_path": artifact}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifact, &document))
	groups := map[string]struct{}{}
	for _, raw := range mapValue[[]any](t, document, "sources") {
		source := valueAs[map[string]any](t, raw)
		groups[valueAs[string](t, source["independence_group"])] = struct{}{}
	}
	assert.Equal(t, map[string]struct{}{"origin-a": {}}, groups)
}

func TestEvidenceReconciliationCarriesFreshnessAndAssessmentReviews(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "reconciliation-freshness")
	blocks := filepath.Dir(workspace)
	ledger := filepath.Join(blocks, "assessment-ledger", "evidence-ledger.json")
	assessment := filepath.Join(blocks, "assessment-ledger", "assessment.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(ledger), 0o700))
	require.NoError(t, writeJSON(ledger, map[string]any{
		"sources": []any{}, "gaps": []any{},
		"claims": []any{map[string]any{
			"id": "claim-key", "claim_type": "organization_relationship", "subject": "Supplier A",
			"predicate": "supplies", "value": "CXMT", "qualifiers": map[string]string{"as_of": "2026-08-09"},
			"status": "confirmed", "evidence_status": "confirmed", "dispute_status": "clean", "evidence": []any{},
		}},
		"freshness_checks": []any{map[string]any{
			"claim_id": "claim-key", "checked_at": "2026-08-09", "outcome": "not_verified",
			"gap": "No current primary disclosure was retained.",
		}},
	}))
	require.NoError(t, writeJSON(assessment, map[string]any{
		"verification_status": "pending",
		"key_claim_reviews": []any{map[string]any{
			"claim_id": "claim-key", "evidence_status": "confirmed", "dispute_status": "clean",
			"freshness_status": "not_verified", "effective_evidence_status": "reported",
			"gap": "No current primary disclosure was retained.",
		}},
	}))
	artifact := filepath.Join(workspace, "evidence-resolution.json")

	prepared, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "ledger_paths": []string{ledger}, "assessment_paths": []string{assessment},
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
	finalized, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact,
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifact, &document))
	assert.Len(t, mapValue[[]any](t, document, "freshness_checks"), 1)
	reviews := mapValue[[]any](t, document, "claim_reviews")
	require.Len(t, reviews, 1)
	assert.Equal(t, "reported", valueAs[map[string]any](t, reviews[0])["effective_evidence_status"])
}

func TestEvidenceReconciliationDowngradesReviewForUnavailableClaim(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	prepare, err := compiler.Compile(t.Context(), goToolSource(t, "prepare_evidence_reconciliation"))
	require.NoError(t, err)
	resolve, err := compiler.Compile(t.Context(), goToolSource(t, "resolve_evidence_conflict"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_evidence_reconciliation"))
	require.NoError(t, err)
	workspace, artifact, ledgers := reconciliationFixture(t, "unavailable-review", true)
	assessment := filepath.Join(filepath.Dir(workspace), "candidate-assessment", "assessment.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(assessment), 0o700))
	require.NoError(t, writeJSON(assessment, map[string]any{
		"key_claim_reviews": []any{map[string]any{
			"claim_id": "claim-old", "evidence_status": "confirmed", "dispute_status": "clean",
			"freshness_status": "verified_primary", "effective_evidence_status": "confirmed", "gap": "",
		}},
	}))
	prepared, invokeErr := prepare.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "ledger_paths": ledgers, "assessment_paths": []string{assessment},
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, prepared.Accepted, "issues: %#v", prepared.Issues)
	conflictID := valueAs[string](t, valueAs[[]any](t, outputMap(t, prepared)["conflict_ids"])[0])
	resolved, invokeErr := resolve.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_path": artifact, "conflict_id": conflictID, "decision": "prefer",
		"chosen_claim_ids": []string{"claim-new"}, "rationale": "The newer claim supersedes the old claim.",
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, resolved.Accepted, "issues: %#v", resolved.Issues)
	finalized, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{"artifact_path": artifact}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)
	var document map[string]any
	require.NoError(t, readJSON(artifact, &document))
	reviews := mapValue[[]any](t, document, "claim_reviews")
	require.Len(t, reviews, 1)
	review := valueAs[map[string]any](t, reviews[0])
	assert.Equal(t, "unknown", review["effective_evidence_status"])
	assert.Contains(t, review["gap"], "excluded")
}

func TestReportManifestTracesClaimsToSourceURLsIdempotently(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
	require.NoError(t, err)
	workspace, report, manifest, evidence := reportFixture(t, "valid")
	stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
	require.NoError(t, os.WriteFile(report, []byte("# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n"), 0o600))

	first, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
	}), workspace)
	require.NoError(t, err)
	require.True(t, first.Accepted, "issues: %#v", first.Issues)
	firstReport := string(mustReadFile(t, report))
	assert.Contains(t, firstReport, "<!-- r42:claim-sources:start -->")
	assert.Contains(t, firstReport, "[Official filing](<https://example.com/official-filing>)")
	var document map[string]any
	require.NoError(t, readJSON(manifest, &document))
	claims := mapValue[[]any](t, document, "claims")
	sources := mapValue[[]any](t, valueAs[map[string]any](t, claims[0]), "sources")
	assert.Equal(t, "https://example.com/official-filing", valueAs[map[string]any](t, sources[0])["url"])

	second, err := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
	}), workspace)
	require.NoError(t, err)
	assert.True(t, second.Accepted, "issues: %#v", second.Issues)
	assert.Equal(t, firstReport, string(mustReadFile(t, report)))
}

func TestFinalizeReportManifestEmitsEveryCanonicalURLOnce(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
	require.NoError(t, err)
	workspace, report, manifest, evidence := reportFixture(t, "canonical-sources")
	require.NoError(t, writeJSON(evidence, map[string]any{
		"artifact_kind": "r42_evidence_resolution", "reconciliation_status": "finalized",
		"sources": []any{
			map[string]any{
				"id": "source-mirror-a", "url": "https://mirror-a.example/story",
				"canonical_url": "https://publisher.example/story", "origin_id": "origin-story",
				"title": "Original story", "publisher": "Publisher", "publication_date": "2026-01-02",
			},
			map[string]any{
				"id": "source-mirror-b", "url": "https://mirror-b.example/story",
				"canonical_url": "https://publisher.example/story", "origin_id": "origin-story",
				"title": "Syndicated story", "publisher": "Publisher", "publication_date": "2026-01-02",
			},
			map[string]any{
				"id": "source-filing", "url": "https://authority.example/filing",
				"canonical_url": "https://authority.example/filing", "origin_id": "origin-filing",
				"title": "Official filing", "publisher": "Authority", "publication_date": "2026-01-03",
			},
			map[string]any{
				"id": "source-response", "url": "https://other.example/response",
				"canonical_url": "https://other.example/response", "origin_id": "origin-response",
				"title": "Contrary response", "publisher": "Other", "publication_date": "2026-01-04",
			},
		},
		"conflicts": []any{},
		"claims": []any{map[string]any{
			"id": "upstream-claim-001", "status": "disputed", "evidence_status": "confirmed",
			"dispute_status": "disputed", "confirmation_basis": "official_primary",
			"evidence": []any{
				map[string]any{"source_id": "source-mirror-a", "relation": "supports"},
				map[string]any{"source_id": "source-mirror-b", "relation": "supports"},
				map[string]any{"source_id": "source-filing", "relation": "supports"},
				map[string]any{"source_id": "source-response", "relation": "contradicts"},
			},
		}},
	}))
	stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
	require.NoError(t, os.WriteFile(
		report,
		[]byte("# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n"),
		0o600,
	))

	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
	}), workspace)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	reportText := string(mustReadFile(t, report))
	assert.Equal(t, 1, strings.Count(reportText, "https://publisher.example/story"))
	assert.Equal(t, 1, strings.Count(reportText, "https://authority.example/filing"))
	assert.Equal(t, 1, strings.Count(reportText, "https://other.example/response"))
	assert.NotContains(t, reportText, "https://mirror-a.example/story")
	assert.NotContains(t, reportText, "https://mirror-b.example/story")

	var document map[string]any
	require.NoError(t, readJSON(manifest, &document))
	claims := mapValue[[]any](t, document, "claims")
	claim := valueAs[map[string]any](t, claims[0])
	assert.Equal(t, "confirmed", claim["evidence_status"])
	assert.Equal(t, "disputed", claim["dispute_status"])
	sources := mapValue[[]any](t, claim, "sources")
	require.Len(t, sources, 3)
	for _, raw := range sources {
		source := valueAs[map[string]any](t, raw)
		if source["url"] == "https://publisher.example/story" {
			assert.ElementsMatch(t, []any{"source-mirror-a", "source-mirror-b"}, source["source_record_ids"])
		}
	}
}

func TestReadReportClaimEvidenceReturnsOnlyRequestedSemanticContext(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	reader, err := compiler.Compile(t.Context(), goToolSource(t, "read_report_claim_evidence"))
	require.NoError(t, err)
	workspace := blockDirectory(t, "read-report-evidence")
	manifest := filepath.Join(workspace, "report-manifest.json")
	require.NoError(t, writeJSON(manifest, map[string]any{
		"report_path": filepath.Join(workspace, "report.md"),
		"claims": []any{
			map[string]any{
				"id": "report-1", "statement": "Supplier A has a production relationship.",
				"claim_kind": "fact", "supporting_claim_ids": []string{"upstream-1"},
				"evidence_status": "confirmed", "dispute_status": "clean",
				"sources": []any{map[string]any{"relation": "supports", "url": "https://example.com/one"}},
			},
			map[string]any{
				"id": "report-2", "statement": "Supplier B remains unverified.",
				"claim_kind": "fact", "supporting_claim_ids": []string{"upstream-2"},
				"evidence_status": "unknown", "dispute_status": "clean",
				"sources": []any{map[string]any{"relation": "supports", "url": "https://example.com/two"}},
			},
		},
	}))

	response, invokeErr := reader.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_manifest_path": manifest, "claim_ids": []string{"report-1"},
	}), workspace)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	require.NotNil(t, response.Output)
	var encoded string
	require.NoError(t, json.Unmarshal(*response.Output, &encoded))
	var context map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &context))
	claims := mapValue[[]any](t, context, "claims")
	require.Len(t, claims, 1)
	assert.Equal(t, "report-1", valueAs[map[string]any](t, claims[0])["id"])
}

func TestReadReportClaimEvidenceReturnsOnlyRequestedCompleteSemanticContext(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
	require.NoError(t, err)
	reader, err := compiler.Compile(t.Context(), goToolSource(t, "read_report_claim_evidence"))
	require.NoError(t, err)
	workspace, report, manifest, evidence := reportFixture(t, "complete-semantic-context")
	require.NoError(t, writeJSON(evidence, map[string]any{
		"artifact_kind": "r42_evidence_resolution", "reconciliation_status": "finalized",
		"conflicts": []any{},
		"claim_reviews": []any{map[string]any{
			"claim_id": "upstream-1", "effective_evidence_status": "confirmed",
			"freshness_status": "verified_primary", "gap": "",
		}},
		"freshness_checks": []any{map[string]any{
			"claim_id": "upstream-1", "checked_at": "2026-08-09",
			"official_channels":         []string{"Issuer filings", "Exchange disclosures"},
			"latest_primary_source_ids": []string{"source-1"}, "outcome": "verified_primary", "gap": "",
		}},
		"sources": []any{
			map[string]any{
				"id": "source-1", "url": "https://mirror.example/filing", "normalized_url": "https://mirror.example/filing",
				"canonical_url": "https://authority.example/filing", "origin_id": "origin-1", "independence_group": "origin-1",
				"title": "Official filing", "publisher": "Authority", "publication_date": "2026-08-01",
				"source_type": "authoritative_primary", "source_class": "authoritative_primary",
				"reporting_basis": "public_document", "provenance": "original",
			},
			map[string]any{
				"id": "source-2", "url": "https://other.example/story", "canonical_url": "https://other.example/story",
				"origin_id": "origin-2", "independence_group": "origin-2", "title": "Unrelated story",
				"publisher": "Other", "publication_date": "2026-08-02", "source_class": "qualified_media",
				"reporting_basis": "named_source", "provenance": "original",
			},
		},
		"claims": []any{
			map[string]any{
				"id": "upstream-1", "claim_type": "organization_relationship", "subject": "Supplier A",
				"predicate": "supplies", "value": "CXMT DDR5", "qualifiers": map[string]string{"as_of": "2026-08-09"},
				"inference": "", "status": "confirmed", "evidence_status": "confirmed", "dispute_status": "clean",
				"confirmation_basis": "official_primary", "reconciliation_availability": "available",
				"evidence": []any{map[string]any{
					"source_id": "source-1", "relation": "supports", "directness": "direct",
					"authority_for_claim": true, "locator": "page 7", "exact_quote": "Supplier A supplies CXMT DDR5.",
				}},
			},
			map[string]any{
				"id": "upstream-2", "claim_type": "other", "subject": "Supplier B", "predicate": "mentions",
				"value": "Other", "qualifiers": map[string]string{}, "status": "reported",
				"reconciliation_availability": "available",
				"evidence":                    []any{map[string]any{"source_id": "source-2", "relation": "supports"}},
			},
		},
	}))
	stageReportClaims(t, stage, workspace, manifest, []any{
		map[string]any{
			"id": "report-1", "section": "Relationship", "statement": "Supplier A supplies CXMT DDR5.",
			"supporting_claim_ids": []string{"upstream-1"},
		},
		map[string]any{
			"id": "report-2", "section": "Other", "statement": "Supplier B appears elsewhere.",
			"supporting_claim_ids": []string{"upstream-2"},
		},
	})
	require.NoError(t, os.WriteFile(
		report,
		[]byte("# Report\n\nSupplier A supplies CXMT DDR5.[^report-1]\n\nSupplier B appears elsewhere.[^report-2]\n"),
		0o600,
	))
	finalized, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
	}), workspace)
	require.NoError(t, invokeErr)
	require.True(t, finalized.Accepted, "issues: %#v", finalized.Issues)

	response, invokeErr := reader.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_manifest_path": manifest, "claim_ids": []string{"report-1"},
	}), workspace)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	require.NotNil(t, response.Output)
	var encoded string
	require.NoError(t, json.Unmarshal(*response.Output, &encoded))
	assert.NotContains(t, encoded, "report-2")
	assert.NotContains(t, encoded, "source-2")
	var context map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &context))
	reportClaims := mapValue[[]any](t, context, "claims")
	require.Len(t, reportClaims, 1)
	reportClaim := valueAs[map[string]any](t, reportClaims[0])
	upstreamClaims := mapValue[[]any](t, reportClaim, "upstream_claims")
	require.Len(t, upstreamClaims, 1)
	upstream := valueAs[map[string]any](t, upstreamClaims[0])
	assert.Equal(t, "Supplier A", upstream["subject"])
	assert.Equal(t, "supplies", upstream["predicate"])
	assert.Equal(t, "CXMT DDR5", upstream["value"])
	assert.Equal(t, "verified_primary", upstream["freshness_status"])
	freshnessCheck := mapValue[map[string]any](t, upstream, "freshness_check")
	assert.Equal(t, "2026-08-09", freshnessCheck["checked_at"])
	assert.ElementsMatch(t, []any{"Issuer filings", "Exchange disclosures"}, freshnessCheck["official_channels"])
	assert.Equal(t, "verified_primary", freshnessCheck["outcome"])
	evidenceEdges := mapValue[[]any](t, upstream, "evidence")
	require.Len(t, evidenceEdges, 1)
	edge := valueAs[map[string]any](t, evidenceEdges[0])
	assert.Equal(t, "Supplier A supplies CXMT DDR5.", edge["exact_quote"])
	assert.Equal(t, true, edge["authority_for_claim"])
	source := mapValue[map[string]any](t, edge, "source")
	assert.Equal(t, "authoritative_primary", source["source_class"])
	assert.Equal(t, "public_document", source["reporting_basis"])
	assert.Equal(t, "original", source["provenance"])
	assert.Equal(t, "origin-1", source["independence_group"])
	assert.Equal(t, "https://authority.example/filing", source["canonical_url"])
}

func TestFinalizeReportManifestRequiresAdjacentFootnoteMarker(t *testing.T) {
	tests := []struct {
		name         string
		report       string
		accepted     bool
		expectedCode string
	}{
		{
			name:     "adjacent marker",
			report:   "# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n",
			accepted: true,
		},
		{
			name:     "adjacent across whitespace",
			report:   "# Report\n\nOfficial filings confirm the production relationship.\n\t[^report-claim-001]\n",
			accepted: true,
		},
		{
			name:         "marker in another paragraph",
			report:       "# Report\n\nOfficial filings confirm the production relationship.\n\nOther prose. [^report-claim-001]\n",
			expectedCode: "claim_marker_not_adjacent",
		},
		{
			name:         "duplicate marker",
			report:       "# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n\nAgain.[^report-claim-001]\n",
			expectedCode: "duplicate_report_claim_tag",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
			require.NoError(t, err)
			finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
			require.NoError(t, err)
			workspace, report, manifest, evidence := reportFixture(t, test.name)
			stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
			require.NoError(t, os.WriteFile(report, []byte(test.report), 0o600))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
			}), workspace)

			require.NoError(t, invokeErr)
			assert.Equal(t, test.accepted, response.Accepted, "issues: %#v", response.Issues)
			if !test.accepted {
				assert.Contains(t, issueCodes(response), test.expectedCode)
			}
		})
	}
}

func TestFinalizeReportManifestRejectsInconsistentClaims(t *testing.T) {
	tests := []struct {
		name          string
		report        string
		supportingIDs []string
		expectedCode  string
	}{
		{name: "unknown report tag", report: "Supported statement.[^missing-report-claim]", supportingIDs: []string{"upstream-claim-001"}, expectedCode: "unknown_report_claim"},
		{name: "manifest claim unused", report: "Supported statement without a claim tag.", supportingIDs: []string{"upstream-claim-001"}, expectedCode: "unused_report_claim"},
		{name: "unknown upstream claim", report: "Supported statement.[^report-claim-001]", supportingIDs: []string{"missing-upstream-claim"}, expectedCode: "unknown_evidence_claim"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
			require.NoError(t, err)
			finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
			require.NoError(t, err)
			workspace, report, manifest, evidence := reportFixture(t, test.name)
			claim := validReportClaim()
			claim["statement"] = "Supported statement."
			claim["supporting_claim_ids"] = test.supportingIDs
			stageReportClaims(t, stage, workspace, manifest, []any{claim})
			require.NoError(t, os.WriteFile(report, []byte("# Report\n\n"+test.report), 0o600))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
			}), workspace)
			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
		})
	}
}

func TestFinalizeReportManifestRequiresFinalizedReconciliation(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*testing.T, string) string
		expectedCode string
	}{
		{
			name: "wrong artifact filename",
			mutate: func(t *testing.T, evidence string) string {
				t.Helper()
				path := filepath.Join(filepath.Dir(evidence), "evidence-ledger.json")
				require.NoError(t, os.Rename(evidence, path))
				return path
			},
			expectedCode: "evidence_path",
		},
		{
			name: "missing finalized marker",
			mutate: func(t *testing.T, evidence string) string {
				t.Helper()
				var document map[string]any
				require.NoError(t, readJSON(evidence, &document))
				delete(document, "artifact_kind")
				delete(document, "reconciliation_status")
				require.NoError(t, writeJSON(evidence, document))
				return evidence
			},
			expectedCode: "evidence_not_finalized",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
			require.NoError(t, err)
			finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
			require.NoError(t, err)
			workspace, report, manifest, evidence := reportFixture(t, test.name)
			evidence = test.mutate(t, evidence)
			stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
			require.NoError(t, os.WriteFile(
				report,
				[]byte("# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n"),
				0o600,
			))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
		})
	}
}

func TestFinalizeReportManifestRequiresExactlyOneEvidencePath(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
	require.NoError(t, err)
	tests := []struct {
		name          string
		evidencePaths func(*testing.T, string, string) []string
	}{
		{
			name: "zero evidence paths",
			evidencePaths: func(_ *testing.T, _, _ string) []string {
				return []string{}
			},
		},
		{
			name: "multiple evidence paths",
			evidencePaths: func(t *testing.T, workspace, evidence string) []string {
				t.Helper()
				second := filepath.Join(filepath.Dir(workspace), "second-evidence", "evidence-resolution.json")
				require.NoError(t, os.MkdirAll(filepath.Dir(second), 0o700))
				payload, readErr := os.ReadFile(evidence)
				require.NoError(t, readErr)
				require.NoError(t, os.WriteFile(second, payload, 0o600))
				return []string{evidence, second}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, report, manifest, evidence := reportFixture(t, test.name)
			stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
			reportPayload := []byte("# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n")
			require.NoError(t, os.WriteFile(report, reportPayload, 0o600))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest,
				"evidence_paths": test.evidencePaths(t, workspace, evidence),
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), "evidence_paths")
			assert.NoFileExists(t, manifest)
			actualReport, readErr := os.ReadFile(report)
			require.NoError(t, readErr)
			assert.Equal(t, reportPayload, actualReport)
		})
	}
}

func TestFinalizeReportManifestAggregatesEvidenceCardinalityWithInvalidManifestPath(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
	require.NoError(t, err)
	tests := []struct {
		name          string
		evidencePaths func(string) []string
	}{
		{name: "zero evidence paths", evidencePaths: func(_ string) []string { return []string{} }},
		{name: "multiple evidence paths", evidencePaths: func(path string) []string { return []string{path, path} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, _, _, evidence := reportFixture(t, test.name+"-aggregate")
			outsideWorkspace := filepath.Join(filepath.Dir(workspace), "other-report-block")
			require.NoError(t, os.MkdirAll(outsideWorkspace, 0o700))
			report := filepath.Join(outsideWorkspace, "report.md")
			manifest := filepath.Join(outsideWorkspace, "report-manifest.json")
			reportPayload := []byte("sentinel report must not change")
			require.NoError(t, os.WriteFile(report, reportPayload, 0o600))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest,
				"evidence_paths": test.evidencePaths(evidence),
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.ElementsMatch(t, []string{"invalid_path", "evidence_paths"}, issueCodes(response))
			assert.NoFileExists(t, manifest)
			actualReport, readErr := os.ReadFile(report)
			require.NoError(t, readErr)
			assert.Equal(t, reportPayload, actualReport)
		})
	}
}

func TestFinalizeReportManifestRejectsUnavailableReconciledClaims(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		chosen   []string
	}{
		{name: "claim excluded by prefer", decision: "prefer", chosen: []string{"upstream-claim-new"}},
		{name: "claim remains unresolved", decision: "unresolved", chosen: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
			require.NoError(t, err)
			finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
			require.NoError(t, err)
			workspace, report, manifest, evidence := reportFixture(t, test.name)
			var document map[string]any
			require.NoError(t, readJSON(evidence, &document))
			document["artifact_kind"] = "r42_evidence_resolution"
			document["reconciliation_status"] = "finalized"
			claims := mapValue[[]any](t, document, "claims")
			claims = append(claims, map[string]any{
				"id": "upstream-claim-new", "status": "confirmed", "subject": "Supplier A",
				"predicate": "supplies", "value": "another product", "evidence": []any{},
			})
			document["claims"] = claims
			document["conflicts"] = []any{map[string]any{
				"id": "conflict-001", "claim_ids": []string{"upstream-claim-001", "upstream-claim-new"},
				"resolution": map[string]any{
					"conflict_id": "conflict-001", "decision": test.decision,
					"chosen_claim_ids": test.chosen, "rationale": "Recorded decision.",
				},
			}}
			require.NoError(t, writeJSON(evidence, document))
			stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
			require.NoError(t, os.WriteFile(
				report,
				[]byte("# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n"),
				0o600,
			))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
			}), workspace)

			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), "unavailable_evidence_claim")
		})
	}
}

func TestFinalizeReportManifestCarriesPendingFreshnessDowngrade(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
	require.NoError(t, err)
	finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
	require.NoError(t, err)
	workspace, report, manifest, evidence := reportFixture(t, "pending-freshness")
	var document map[string]any
	require.NoError(t, readJSON(evidence, &document))
	document["claim_reviews"] = []any{map[string]any{
		"claim_id": "upstream-claim-001", "evidence_status": "confirmed", "dispute_status": "clean",
		"freshness_status": "not_verified", "effective_evidence_status": "reported",
		"gap": "No completed current-source check was recorded.",
	}}
	require.NoError(t, writeJSON(evidence, document))
	stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
	require.NoError(t, os.WriteFile(
		report,
		[]byte("# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n"),
		0o600,
	))

	response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
	}), workspace)

	require.NoError(t, invokeErr)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	var result map[string]any
	require.NoError(t, readJSON(manifest, &result))
	claim := valueAs[map[string]any](t, mapValue[[]any](t, result, "claims")[0])
	assert.Equal(t, "reported", claim["evidence_status"])
	assert.Equal(t, "pending", claim["freshness_status"])
	assert.Equal(t, []any{"No completed current-source check was recorded."}, claim["freshness_gaps"])
}

func TestStageReportClaimsRejectsMoreThanFiveClaims(t *testing.T) {
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
	require.NoError(t, err)
	workspace, _, manifest, _ := reportFixture(t, "batch")
	claims := make([]any, 6)
	for index := range claims {
		claim := validReportClaim()
		claim["id"] = fmt.Sprintf("report-claim-%d", index)
		claims[index] = claim
	}

	response, err := stage.Invoke(t.Context(), marshalInput(t, map[string]any{
		"manifest_path": manifest, "claims": claims,
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "batch_size")
}

func TestFinalizeReportManifestRejectsInvalidSourcesWithoutRewriting(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*testing.T, map[string]any)
		expectedCode string
	}{
		{
			name: "unknown source",
			mutate: func(t *testing.T, ledger map[string]any) {
				t.Helper()
				claims := mapValue[[]any](t, ledger, "claims")
				evidence := mapValue[[]any](t, valueAs[map[string]any](t, claims[0]), "evidence")
				valueAs[map[string]any](t, evidence[0])["source_id"] = "missing-source"
			},
			expectedCode: "unknown_source_id",
		},
		{
			name: "invalid source URL",
			mutate: func(t *testing.T, ledger map[string]any) {
				t.Helper()
				sources := mapValue[[]any](t, ledger, "sources")
				valueAs[map[string]any](t, sources[0])["url"] = "not-a-url"
			},
			expectedCode: "invalid_source_url",
		},
		{
			name: "invalid canonical URL",
			mutate: func(t *testing.T, ledger map[string]any) {
				t.Helper()
				sources := mapValue[[]any](t, ledger, "sources")
				valueAs[map[string]any](t, sources[0])["canonical_url"] = "javascript:alert(1)"
			},
			expectedCode: "invalid_canonical_url",
		},
		{
			name: "claim without source evidence",
			mutate: func(t *testing.T, ledger map[string]any) {
				t.Helper()
				claims := mapValue[[]any](t, ledger, "claims")
				valueAs[map[string]any](t, claims[0])["evidence"] = []any{}
			},
			expectedCode: "missing_claim_sources",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler, err := gotool.NewCompiler()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, compiler.Close()) })
			stage, err := compiler.Compile(t.Context(), goToolSource(t, "stage_report_claims"))
			require.NoError(t, err)
			finalize, err := compiler.Compile(t.Context(), goToolSource(t, "finalize_report_manifest"))
			require.NoError(t, err)
			workspace, report, manifest, evidence := reportFixture(t, test.name)
			var ledger map[string]any
			require.NoError(t, readJSON(evidence, &ledger))
			test.mutate(t, ledger)
			require.NoError(t, writeJSON(evidence, ledger))
			stageReportClaims(t, stage, workspace, manifest, []any{validReportClaim()})
			original := "# Report\n\nOfficial filings confirm the production relationship.[^report-claim-001]\n"
			require.NoError(t, os.WriteFile(report, []byte(original), 0o600))

			response, invokeErr := finalize.Invoke(t.Context(), marshalInput(t, map[string]any{
				"report_path": report, "manifest_path": manifest, "evidence_paths": []string{evidence},
			}), workspace)
			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), test.expectedCode)
			assert.Equal(t, original, string(mustReadFile(t, report)))
			assert.NoFileExists(t, manifest)
		})
	}
}

func compileEvidencePrograms(t *testing.T) (*gotool.Compiler, evidencePrograms) {
	t.Helper()
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	compile := func(name string) *gotool.Program {
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, name))
		require.NoError(t, compileErr)
		return program
	}
	return compiler, evidencePrograms{
		register: compile("register_evidence_source"), stageClaims: compile("stage_evidence_claims"),
		stageGaps: compile("stage_evidence_gaps"), finalize: compile("finalize_evidence_ledger"),
	}
}

func newEvidenceFixture(t *testing.T, name string, coverageIDs []string) evidenceFixture {
	t.Helper()
	workspace := blockDirectory(t, name)
	blocks := filepath.Dir(workspace)
	scopeDirectory := filepath.Join(blocks, "scope")
	sources := filepath.Join(workspace, "sources")
	require.NoError(t, os.MkdirAll(scopeDirectory, 0o700))
	require.NoError(t, os.MkdirAll(sources, 0o700))
	scope := filepath.Join(scopeDirectory, "scope.json")
	coverage := make([]any, len(coverageIDs))
	for index, id := range coverageIDs {
		coverage[index] = map[string]any{"id": id, "track": "equipment"}
	}
	require.NoError(t, writeJSON(scope, map[string]any{"coverage_items": coverage}))
	return evidenceFixture{
		workspace: workspace, scope: scope, sources: sources,
		ledger:   filepath.Join(workspace, "evidence-ledger.json"),
		registry: filepath.Join(workspace, "source-registry.json"),
	}
}

func registerEvidenceSource(
	t *testing.T,
	program *gotool.Program,
	fixture evidenceFixture,
	sourceType, suffix, publicationDate string,
) string {
	t.Helper()
	text := "CXMT confirms Supplier A supplies production equipment."
	if suffix == "contradiction" {
		text = "CXMT says Supplier A is not a production supplier."
	}
	snapshot := filepath.Join(fixture.sources, suffix+".md")
	require.NoError(t, os.WriteFile(snapshot, []byte(text), 0o600))
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger, "url": "https://example.com/" + suffix,
		"title": "Official disclosure", "publisher": "CXMT", "publication_date": publicationDate,
		"accessed_at": "2026-08-09", "source_type": sourceType, "snapshot_path": snapshot,
		"named_entities": []string{"CXMT", "Supplier A"},
	}), fixture.workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	return valueAs[string](t, outputMap(t, response)["source_id"])
}

func registerClassifiedEvidenceSource(
	t *testing.T,
	program *gotool.Program,
	fixture evidenceFixture,
	spec evidenceSourceSpec,
) map[string]any {
	t.Helper()
	snapshot := filepath.Join(fixture.sources, spec.suffix+".md")
	require.NoError(t, os.WriteFile(snapshot, []byte(spec.text), 0o600))
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir":    fixture.workspace,
		"ledger_path":      fixture.ledger,
		"url":              spec.url,
		"canonical_url":    spec.canonicalURL,
		"title":            "Retained evidence",
		"publisher":        "Example publisher",
		"publication_date": "2026-08-01",
		"accessed_at":      "2026-08-09",
		"source_type":      spec.sourceType,
		"reporting_basis":  spec.reportingBasis,
		"provenance":       spec.provenance,
		"snapshot_path":    snapshot,
		"named_entities":   []string{"CXMT", "Supplier A"},
	}), fixture.workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	return outputMap(t, response)
}

func validEvidenceClaim(sourceID, directness string, coverageIDs []string) map[string]any {
	return map[string]any{
		"id": "equipment-claim-001", "claim_type": "organization_relationship",
		"subject": "Supplier A", "predicate": "supplies", "value": "CXMT production equipment",
		"qualifiers": map[string]string{"product": "DDR5"}, "coverage_item_ids": coverageIDs,
		"inference": "",
		"evidence": []any{map[string]any{
			"source_id": sourceID, "relation": "supports", "directness": directness,
			"authority_for_claim": true,
			"locator":             "paragraph 1", "exact_quote": "CXMT confirms Supplier A supplies production equipment.",
		}},
	}
}

func stageEvidenceClaims(t *testing.T, program *gotool.Program, fixture evidenceFixture, claims []any) {
	t.Helper()
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger, "claims": claims,
	}), fixture.workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
}

func finalizeEvidenceLedger(
	t *testing.T,
	program *gotool.Program,
	fixture evidenceFixture,
	mode, track string,
) gotool.Response {
	t.Helper()
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"workspace_dir": fixture.workspace,
		"ledger_path":   fixture.ledger, "source_registry_path": fixture.registry,
		"mode": mode, "topic": "CXMT DDR5", "as_of_date": "2026-08-09",
		"scope_artifact": map[bool]string{true: fixture.scope, false: ""}[mode == "track"], "track": track,
	}), fixture.workspace)
	require.NoError(t, err)
	return response
}

func reconciliationFixture(t *testing.T, name string, conflicting bool) (string, string, []string) {
	t.Helper()
	workspace := blockDirectory(t, name)
	blocks := filepath.Dir(workspace)
	values := []string{"validation", "validation"}
	if conflicting {
		values[1] = "mass_production"
	}
	ledgers := make([]string, len(values))
	for index, value := range values {
		path := filepath.Join(blocks, fmt.Sprintf("track-%d", index), "evidence-ledger.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, writeJSON(path, map[string]any{
			"sources": []any{}, "gaps": []any{},
			"claims": []any{map[string]any{
				"id":         map[bool]string{true: "claim-new", false: "claim-old"}[index == 1],
				"claim_type": "supplier_maturity", "subject": "Supplier A", "predicate": "maturity_at_CXMT",
				"value": value, "qualifiers": map[string]string{"as_of": "2026-08-09"},
				"status": map[bool]string{true: "confirmed", false: "reported"}[index == 1], "evidence": []any{},
			}},
		}))
		ledgers[index] = path
	}
	return workspace, filepath.Join(workspace, "evidence-resolution.json"), ledgers
}

func reportFixture(t *testing.T, name string) (string, string, string, string) {
	t.Helper()
	workspace := blockDirectory(t, name)
	evidenceDirectory := filepath.Join(filepath.Dir(workspace), "evidence")
	require.NoError(t, os.MkdirAll(evidenceDirectory, 0o700))
	evidence := filepath.Join(evidenceDirectory, "evidence-resolution.json")
	require.NoError(t, writeJSON(evidence, map[string]any{
		"artifact_kind": "r42_evidence_resolution", "reconciliation_status": "finalized",
		"sources": []any{map[string]any{
			"id": "source-official-001", "url": "https://example.com/official-filing",
			"title": "Official filing", "publisher": "Example Authority", "publication_date": "2026-01-02",
		}},
		"conflicts": []any{},
		"claims": []any{map[string]any{
			"id": "upstream-claim-001", "status": "confirmed", "subject": "Supplier A",
			"predicate": "supplies", "value": "CXMT",
			"evidence": []any{map[string]any{"source_id": "source-official-001", "relation": "supports"}},
		}},
	}))
	return workspace, filepath.Join(workspace, "report.md"), filepath.Join(workspace, "report-manifest.json"), evidence
}

func validReportClaim() map[string]any {
	return map[string]any{
		"id": "report-claim-001", "section": "Supplier relationship",
		"statement":            "Official filings confirm the production relationship.",
		"supporting_claim_ids": []string{"upstream-claim-001"},
	}
}

func stageReportClaims(
	t *testing.T,
	program *gotool.Program,
	workspace, manifest string,
	claims []any,
) {
	t.Helper()
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"manifest_path": manifest, "claims": claims,
	}), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
}

func outputMap(t *testing.T, response gotool.Response) map[string]any {
	t.Helper()
	require.NotNil(t, response.Output)
	var output map[string]any
	require.NoError(t, json.Unmarshal(*response.Output, &output))
	return output
}

func readJSON(path string, output any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), output)
}
