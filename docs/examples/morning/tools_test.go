package morning_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestSubmitBreakfastPacketRejectsMechanicalEvidenceProblems(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "packet")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	input := validPacketInput(artifactPath)
	input["events"] = []any{
		map[string]any{
			"id": "event-1", "headline": "Fed holds rates", "category": "macro",
			"status": "occurred", "as_of": "2026-08-28", "importance": 5,
			"summary": "The decision was announced.", "evidence_ids": []string{"overnight-kb-1"},
		},
		map[string]any{
			"id": "event-2", "headline": "  Fed\t holds   rates ", "category": "macro",
			"status": "occurred", "as_of": "2026-08-29", "importance": 4,
			"summary": "Duplicate wire copy.", "evidence_ids": []string{"overnight-kb-1"},
		},
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.ElementsMatch(t, []string{"duplicate_headline", "future_data"}, issueCodes(response))
	assert.NoFileExists(t, artifactPath)
}

func TestSubmitBreakfastPacketAcceptsUnavailableMarketAsOf(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "packet-unavailable-as-of")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	input := validPacketInput(artifactPath)
	metrics, ok := input["market_snapshot"].([]any)
	require.True(t, ok)
	metrics[0] = map[string]any{
		"key": "sp500", "label": "S&P 500", "value": "unavailable",
		"change": "unavailable", "direction": "unavailable", "as_of": "unavailable",
		"evidence_ids": []string{"overnight-kb-1"},
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, artifactPath)
}

func TestSubmitBreakfastPacketRejectsDatedUnavailableMarket(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "packet-dated-unavailable")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	input := validPacketInput(artifactPath)
	metrics, ok := input["market_snapshot"].([]any)
	require.True(t, ok)
	metrics[0] = map[string]any{
		"key": "sp500", "label": "S&P 500", "value": "unavailable",
		"change": "unavailable", "direction": "unavailable", "as_of": "2026-08-28",
		"evidence_ids": []string{"overnight-kb-1"},
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "as_of")
	assert.NoFileExists(t, artifactPath)
}

func TestSubmitMorningEvidenceRejectsFutureSourcesAndUnusedQuotes(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_evidence")
	workspace := blockDirectory(t, "evidence")
	artifactPath := filepath.Join(workspace, "track", "evidence.json")
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": artifactPath,
		"task_id":            "overnight-market",
		"edition_date":       "2026-08-28",
		"question":           "What changed overnight?",
		"claims": []any{map[string]any{
			"id": "overnight-market-kb-1", "statement": "The index rose.",
			"as_of": "2026-08-28", "confidence": "high", "quote_ids": []string{"quote-1"},
		}},
		"quotes": []any{
			map[string]any{
				"id": "quote-1", "source_title": "Exchange", "url": "https://example.com/market",
				"artifact_id":      "artifact-11111111111111111111111111111111",
				"publication_date": "2026-08-29", "locator": "close table", "exact_quote": "The index rose 1%.",
			},
			map[string]any{
				"id": "quote-2", "source_title": "Exchange", "url": "https://example.com/unused",
				"artifact_id":      "artifact-22222222222222222222222222222222",
				"publication_date": "2026-08-28", "locator": "table", "exact_quote": "An unused quote.",
			},
		},
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "future_source")
	assert.Contains(t, issueCodes(response), "unused_quote")
	assert.NoFileExists(t, artifactPath)
}

func TestSubmitMorningEvidenceAcceptsExplicitEmptyResult(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_evidence")
	workspace := blockDirectory(t, "empty-evidence")
	artifactPath := filepath.Join(workspace, "track", "evidence.json")
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": artifactPath,
		"task_id":            "industry-themes",
		"edition_date":       "2026-08-28",
		"question":           "What changed in industries and companies?",
		"claims":             []any{},
		"quotes":             []any{},
		"empty_reason":       "No verifiable events were found in the assigned time window.",
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, artifactPath)

	payload, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(payload, &document))
	assert.Empty(t, document["claims"])
	assert.Empty(t, document["quotes"])
	assert.Equal(t, "No verifiable events were found in the assigned time window.", document["empty_reason"])
}

func TestSubmitMorningEvidenceSupportsClaimCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		category     string
		expectedCode string
		accepted     bool
		expectedJSON any
	}{
		{name: "analysis", category: "analysis", accepted: true, expectedJSON: "analysis"},
		{name: "source fact", category: "source_fact", accepted: true, expectedJSON: "source_fact"},
		{name: "mixed fact and analysis", category: "mixed", accepted: true, expectedJSON: "mixed"},
		{name: "omitted defaults to source fact", accepted: true},
		{name: "invalid category", category: "opinion", expectedCode: "claim_category"},
		{name: "whitespace is not an enum", category: " analysis ", expectedCode: "claim_category"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			program := compileTool(t, "submit_morning_evidence")
			workspace := blockDirectory(t, tt.name)
			artifactPath := filepath.Join(workspace, "track", "evidence.json")
			input := validMorningEvidenceInput(artifactPath)
			input["claims"] = []any{map[string]any{
				"id": "overnight-market-kb-1", "category": tt.category,
				"statement": "The index rose.", "as_of": "2026-08-28",
				"confidence": "high", "quote_ids": []string{"quote-1"},
			}}

			response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

			require.NoError(t, err)
			assert.Equal(t, tt.accepted, response.Accepted)
			if tt.expectedCode != "" {
				assert.Contains(t, issueCodes(response), tt.expectedCode)
				assert.NoFileExists(t, artifactPath)
				return
			}
			payload, readErr := os.ReadFile(artifactPath)
			require.NoError(t, readErr)
			var document map[string]any
			require.NoError(t, json.Unmarshal(payload, &document))
			claims, ok := document["claims"].([]any)
			require.True(t, ok)
			claim, ok := claims[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, tt.expectedJSON, claim["category"])
		})
	}
}

func TestSubmitMorningEvidenceRejectsIncompleteOrContradictoryResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mutate       func(map[string]any)
		expectedCode string
	}{
		{
			name: "empty result without reason",
			mutate: func(input map[string]any) {
				input["claims"] = []any{}
				input["quotes"] = []any{}
			},
			expectedCode: "empty_reason",
		},
		{
			name: "claim without quote",
			mutate: func(input map[string]any) {
				input["quotes"] = []any{}
			},
			expectedCode: "quotes",
		},
		{
			name: "quote without claim",
			mutate: func(input map[string]any) {
				input["claims"] = []any{}
			},
			expectedCode: "claims",
		},
		{
			name: "evidence with empty reason",
			mutate: func(input map[string]any) {
				input["empty_reason"] = "No verifiable evidence was found."
			},
			expectedCode: "empty_reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			program := compileTool(t, "submit_morning_evidence")
			workspace := blockDirectory(t, tt.name)
			artifactPath := filepath.Join(workspace, "track", "evidence.json")
			input := validMorningEvidenceInput(artifactPath)
			tt.mutate(input)

			response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

			require.NoError(t, err)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.expectedCode)
			assert.NoFileExists(t, artifactPath)
		})
	}
}

func TestSubmitBreakfastPacketRequiresOvernightMarketCoverage(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "coverage")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	input := validPacketInput(artifactPath)
	metrics, ok := input["market_snapshot"].([]any)
	require.True(t, ok)
	input["market_snapshot"] = metrics[:len(metrics)-1]

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "market_coverage")
}

func TestSubmitBreakfastPacketRejectsInventedEvidenceCatalogEntries(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "upstream-evidence")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	upstreamPath := filepath.Join(workspace, "gather", "evidence.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(upstreamPath), 0o700))
	require.NoError(t, os.WriteFile(upstreamPath, []byte(`{
  "claims":[{"id":"overnight-kb-1","statement":"Upstream claim.","confidence":"high"}]
}`), 0o600))
	input := validPacketInput(artifactPath)
	input["reviewed_artifacts"] = []string{upstreamPath}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "upstream_evidence_mismatch")
	assert.NoFileExists(t, artifactPath)
}

func TestSubmitBreakfastPacketPreservesClaimCategory(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "claim-category")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	upstreamPath := filepath.Join(workspace, "gather", "evidence.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(upstreamPath), 0o700))
	require.NoError(t, os.WriteFile(upstreamPath, []byte(`{
  "claims":[{"id":"overnight-kb-1","statement":"A reasoned implication.","category":"analysis","confidence":"medium"}]
}`), 0o600))
	input := validPacketInput(artifactPath)
	input["reviewed_artifacts"] = []string{upstreamPath}
	input["evidence_catalog"] = []any{map[string]any{
		"id": "overnight-kb-1", "claim": "A reasoned implication.", "category": "analysis", "confidence": "medium",
	}}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	payload, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	var packet map[string]any
	require.NoError(t, json.Unmarshal(payload, &packet))
	catalog, ok := packet["evidence_catalog"].([]any)
	require.True(t, ok)
	require.Len(t, catalog, 1)
	catalogEntry, ok := catalog[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "analysis", catalogEntry["category"])
}

func TestSubmitBreakfastPacketPreservesSourceURLs(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "source-urls")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	sourcePath := filepath.Join(workspace, "sources", "news.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o700))
	require.NoError(t, os.WriteFile(sourcePath, []byte(`{"data":{"url":"https://xnews.jin10.com/details/228637"}}`), 0o600))
	input := validPacketInput(artifactPath)
	eventURL := "https://xnews.jin10.com/details/228637"
	input["source_paths"] = []string{sourcePath}
	input["events"] = []any{map[string]any{
		"id": "event-1", "headline": "Payroll revisions", "category": "macro",
		"status": "announced", "as_of": "2026-08-28", "importance": 4,
		"summary": "A revision estimate was published.", "evidence_ids": []string{"overnight-kb-1"},
		"source_urls": []string{eventURL},
	}}
	input["evidence_catalog"] = []any{map[string]any{
		"id": "overnight-kb-1", "claim": "A sourced claim.", "confidence": "high",
		"source_urls": []string{eventURL},
	}}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	payload, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	var packet map[string]any
	require.NoError(t, json.Unmarshal(payload, &packet))
	events, ok := packet["events"].([]any)
	require.True(t, ok)
	event, ok := events[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{eventURL}, event["source_urls"])
	catalog, ok := packet["evidence_catalog"].([]any)
	require.True(t, ok)
	claim, ok := catalog[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{eventURL}, claim["source_urls"])
	assert.NotContains(t, string(payload), "source_paths")
}

func TestSubmitBreakfastPacketExtractsURLsFromMarkdownSources(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "markdown-source-urls")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	sourcePath := filepath.Join(workspace, "sources", "news.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o700))
	require.NoError(t, os.WriteFile(sourcePath, []byte("- Source: https://example.com/news/123\n\nA saved source."), 0o600))
	input := validPacketInput(artifactPath)
	input["source_paths"] = []string{sourcePath}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	payload, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	var packet map[string]any
	require.NoError(t, json.Unmarshal(payload, &packet))
	assert.Equal(t, []any{"https://example.com/news/123"}, packet["source_urls"])
}

func TestSubmitBreakfastPacketInjectsDroppedSourceURLs(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "source-url-preservation")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	sourcePath := filepath.Join(workspace, "sources", "news.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o700))
	require.NoError(t, os.WriteFile(sourcePath, []byte(`{"data":{"url":"https://xnews.jin10.com/details/228637"}}`), 0o600))
	input := validPacketInput(artifactPath)
	input["source_paths"] = []string{sourcePath}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	payload, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	var packet map[string]any
	require.NoError(t, json.Unmarshal(payload, &packet))
	assert.Equal(t, []any{"https://xnews.jin10.com/details/228637"}, packet["source_urls"])
}

func TestSubmitBreakfastPacketSkipsMissingOptionalSourcePaths(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "missing-source-path")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	input := validPacketInput(artifactPath)
	input["source_paths"] = []string{filepath.Join(workspace, "sources")}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, artifactPath)
}

func TestSubmitBreakfastPacketFillsCoverageIdentityFromRequirements(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "coverage-identity")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	input := validPacketInput(artifactPath)
	input["required_coverage"] = []any{map[string]any{
		"id": "csi300", "name": "沪深300", "kind": "a_share_index",
		"quote_symbols": []string{"000300.SH"}, "search_terms": []string{"沪深300"},
	}}
	input["coverage"] = []any{map[string]any{
		"object_id": "csi300", "name": "错误名称", "kind": "index",
		"quote_status": "observed", "news_status": "no_material_news",
		"checked_until": "2026-08-28 07:30 Asia/Shanghai", "summary": "已检查行情和新闻，没有重大消息。",
	}}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	payload, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	var packet map[string]any
	require.NoError(t, json.Unmarshal(payload, &packet))
	coverage, ok := packet["coverage"].([]any)
	require.True(t, ok)
	require.Len(t, coverage, 1)
	row, ok := coverage[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "沪深300", row["name"])
	assert.Equal(t, "a_share_index", row["kind"])
}

func TestSubmitBreakfastPacketTreatsOmittedCategoryAsSourceFact(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_breakfast_packet")
	workspace := blockDirectory(t, "default-claim-category")
	artifactPath := filepath.Join(workspace, "breakfast-packet.json")
	upstreamPath := filepath.Join(workspace, "gather", "evidence.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(upstreamPath), 0o700))
	require.NoError(t, os.WriteFile(upstreamPath, []byte(`{
  "claims":[{"id":"overnight-kb-1","statement":"A sourced fact.","confidence":"high"}]
}`), 0o600))
	input := validPacketInput(artifactPath)
	input["reviewed_artifacts"] = []string{upstreamPath}
	input["evidence_catalog"] = []any{map[string]any{
		"id": "overnight-kb-1", "claim": "A sourced fact.", "category": "source_fact", "confidence": "high",
	}}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)

	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
}

func TestSubmitMorningNewsDigestRequiresFetchedURLSources(t *testing.T) {
	t.Parallel()

	packetProgram := compileTool(t, "submit_breakfast_packet")
	digestProgram := compileTool(t, "submit_morning_news_digest")
	workspace := blockDirectory(t, "news-digest")
	packetPath := filepath.Join(workspace, "packet", "breakfast-packet.json")
	packetInput := validPacketInput(packetPath)
	packetInput["events"] = []any{map[string]any{
		"id": "event-1", "headline": "Payroll revisions", "category": "macro",
		"status": "announced", "as_of": "2026-08-28", "importance": 4,
		"summary": "A revision estimate was published.", "evidence_ids": []string{"overnight-kb-1"},
		"source_urls": []string{"https://example.com/payroll"},
	}}
	packetResponse, err := packetProgram.Invoke(t.Context(), marshalInput(t, packetInput), workspace)
	require.NoError(t, err)
	require.True(t, packetResponse.Accepted, "issues: %#v", packetResponse.Issues)

	digestPath := filepath.Join(workspace, "digest", "news-digest.json")
	response, err := digestProgram.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": digestPath,
		"packet_path":        packetPath,
		"max_items":          15,
		"items": []any{map[string]any{
			"event_id": "event-1", "headline": "Payroll revisions",
			"source_urls": []string{"https://example.com/payroll"},
			"status":      "fetched", "summary": "A revision estimate was published.",
			"fetch_artifact_ids": []string{},
		}},
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "fetch_artifact")
	assert.NoFileExists(t, digestPath)
}

func TestSubmitMorningNewsDigestAcceptsFetchedNews(t *testing.T) {
	t.Parallel()

	packetProgram := compileTool(t, "submit_breakfast_packet")
	digestProgram := compileTool(t, "submit_morning_news_digest")
	workspace := blockDirectory(t, "news-digest-valid")
	packetPath := filepath.Join(workspace, "packet", "breakfast-packet.json")
	packetInput := validPacketInput(packetPath)
	packetInput["events"] = []any{map[string]any{
		"id": "event-1", "headline": "Payroll revisions", "category": "macro",
		"status": "announced", "as_of": "2026-08-28", "importance": 4,
		"summary": "A revision estimate was published.", "evidence_ids": []string{"overnight-kb-1"},
		"source_urls": []string{"https://example.com/payroll"},
	}}
	packetResponse, err := packetProgram.Invoke(t.Context(), marshalInput(t, packetInput), workspace)
	require.NoError(t, err)
	require.True(t, packetResponse.Accepted, "issues: %#v", packetResponse.Issues)

	digestPath := filepath.Join(workspace, "digest", "news-digest.json")
	response, err := digestProgram.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": digestPath,
		"packet_path":        packetPath,
		"max_items":          15,
		"items": []any{map[string]any{
			"event_id": "event-1", "headline": "Payroll revisions",
			"source_urls": []string{"https://example.com/payroll"},
			"status":      "fetched", "summary": "A revision estimate was published.",
			"fetch_artifact_ids": []string{"artifact-abcdef0123456789abcdef0123456789"},
		}},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, digestPath)
}

func TestSubmitMorningReportRequiresEvidence(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_report")
	workspace := blockDirectory(t, "report")
	packetPath := filepath.Join(workspace, "upstream", "breakfast-packet.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(packetPath), 0o700))
	require.NoError(t, os.WriteFile(packetPath, []byte(`{
  "edition_date":"2026-08-28",
  "market_snapshot":[{"key":"sp500"}],
  "events":[{"id":"event-1"}]
}`), 0o600))
	reportJSONPath := filepath.Join(workspace, "morning-report.json")
	reportMarkdownPath := filepath.Join(workspace, "morning.md")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"_r42_report_json_path":     reportJSONPath,
		"_r42_report_markdown_path": reportMarkdownPath,
		"packet_path":               packetPath,
		"review_paths":              []string{},
		"edition_date":              "2026-08-28",
		"title":                     "财经早餐",
		"lead":                      []string{"隔夜市场出现变化。"},
		"snapshot": []any{map[string]any{
			"metric_key": "sp500", "plain_language": "美股大盘变化。",
		}},
		"stories": []any{
			map[string]any{
				"headline": "利率决议", "what_happened": "决议已发布。",
				"why_it_matters": "可能影响融资成本。", "what_to_watch": "观察后续数据。",
				"evidence_ids": []string{"missing-event"},
			},
			map[string]any{
				"headline": "就业数据", "what_happened": "数据已发布。",
				"why_it_matters": "可能影响利率预期。", "what_to_watch": "观察市场定价。",
				"evidence_ids": []string{"missing-event"},
			},
			map[string]any{
				"headline": "商品市场", "what_happened": "价格出现变化。",
				"why_it_matters": "可能影响通胀预期。", "what_to_watch": "观察后续走势。",
				"evidence_ids": []string{"missing-event"},
			},
		},
		"themes":             []any{},
		"setups":             []any{},
		"institutional_scan": []any{},
		"calendar_events":    []any{},
		"sentiment": map[string]any{
			"label": "mixed", "basis": "不同市场方向不一。", "limitations": "样本有限。",
		},
		"limitations": []string{"公开信息可能不完整。"},
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "stories")
	assert.Contains(t, issueCodes(response), "evidence_reference")
	assert.NoFileExists(t, reportMarkdownPath)
}

func TestSubmitBreakfastReviewRejectsUnknownEvidenceAndMissingCounterpoint(t *testing.T) {
	t.Parallel()

	packetProgram := compileTool(t, "submit_breakfast_packet")
	reviewProgram := compileTool(t, "submit_breakfast_review")
	workspace := blockDirectory(t, "review")
	packetPath := filepath.Join(workspace, "upstream", "breakfast-packet.json")
	packetResponse, err := packetProgram.Invoke(t.Context(), marshalInput(t, validPacketInput(packetPath)), workspace)
	require.NoError(t, err)
	require.True(t, packetResponse.Accepted, "issues: %#v", packetResponse.Issues)

	reviewPath := filepath.Join(workspace, "strategy", "review.json")
	response, err := reviewProgram.Invoke(t.Context(), marshalInput(t, map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": reviewPath,
		"packet_path":        packetPath,
		"role":               "strategy",
		"headline":           "Signals are mixed.",
		"findings": []any{
			map[string]any{
				"id": "strategy-1", "statement": "A conditional mapping.", "plain_language": "The effect is uncertain.",
				"evidence_ids": []string{"missing-id"}, "confidence": "medium", "counterpoint": "",
				"falsification_condition": "The next data release reverses direction.",
			},
			map[string]any{
				"id": "strategy-2", "statement": "A second mapping.", "plain_language": "Watch the next release.",
				"evidence_ids": []string{"event-1"}, "confidence": "low", "counterpoint": "One day is not a trend.",
				"falsification_condition": "The event is withdrawn.",
			},
		},
	}), workspace)

	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "evidence_reference")
	assert.Contains(t, issueCodes(response), "uncertainty")
	assert.NoFileExists(t, reviewPath)
}

func TestMorningTypedToolsCompile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "submit_morning_evidence"},
		{name: "submit_breakfast_packet"},
		{name: "submit_morning_news_digest"},
		{name: "submit_breakfast_review"},
		{name: "submit_morning_report"},
		{name: "submit_morning_draft"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			program := compileTool(t, tt.name)
			assert.NotNil(t, program)
		})
	}
}

func TestSubmitMorningDraftStripsMarkersAndKeepsProvenance(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_draft")
	workspace := blockDirectory(t, "draft")
	annotated := filepath.Join(workspace, "morning-draft.annotated.md")
	provenance := filepath.Join(workspace, "morning-provenance.json")
	public := filepath.Join(workspace, "morning.md")
	input := map[string]any{
		"annotated_artifact_id":  "artifact-0123456789abcdef0123456789abcdef",
		"provenance_artifact_id": "artifact-abcdef0123456789abcdef0123456789",
		"markdown_artifact_id":   "artifact-123456789abcdef0123456789abcdef",
		"_r42_annotated_path":    annotated,
		"_r42_provenance_path":   provenance,
		"_r42_markdown_path":     public,
		"edition_date":           "2026-08-29",
		"markdown":               "# 财经早餐\n\n美股上涨，风险偏好回暖。<!-- r42:claim=event-1 evidence=quote-1 -->\n",
	}

	response, err := program.Invoke(t.Context(), marshalInput(t, input), workspace)
	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	clean, err := os.ReadFile(public)
	require.NoError(t, err)
	assert.Equal(t, "# 财经早餐\n\n美股上涨，风险偏好回暖。\n", string(clean))
	index, err := os.ReadFile(provenance)
	require.NoError(t, err)
	assert.Contains(t, string(index), "event-1")
	assert.Contains(t, string(index), "quote-1")
}

func TestSubmitMorningDraftRejectsUnannotatedProse(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_draft")
	workspace := blockDirectory(t, "draft-missing-source")
	public := filepath.Join(workspace, "morning.md")
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"annotated_artifact_id":  "artifact-0123456789abcdef0123456789abcdef",
		"provenance_artifact_id": "artifact-abcdef0123456789abcdef0123456789",
		"markdown_artifact_id":   "artifact-123456789abcdef0123456789abcdef",
		"_r42_annotated_path":    filepath.Join(workspace, "morning-draft.annotated.md"),
		"_r42_provenance_path":   filepath.Join(workspace, "morning-provenance.json"),
		"_r42_markdown_path":     public,
		"edition_date":           "2026-08-29",
		"markdown":               "# 财经早餐\n\n这句话没有出处。\n",
	}), workspace)
	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "missing_provenance")
	assert.NoFileExists(t, public)
}

func TestSubmitMorningDraftRejectsTrailingTextAfterMarker(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_draft")
	workspace := blockDirectory(t, "draft-trailing-text")
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"annotated_artifact_id":  "artifact-0123456789abcdef0123456789abcdef",
		"provenance_artifact_id": "artifact-abcdef0123456789abcdef0123456789",
		"markdown_artifact_id":   "artifact-123456789abcdef0123456789abcdef",
		"_r42_annotated_path":    filepath.Join(workspace, "morning-draft.annotated.md"),
		"_r42_provenance_path":   filepath.Join(workspace, "morning-provenance.json"),
		"_r42_markdown_path":     filepath.Join(workspace, "morning.md"),
		"edition_date":           "2026-08-29",
		"markdown":               "一句话。<!-- r42:evidence=quote-1 --> 尾部没有出处。\n",
	}), workspace)
	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "provenance_marker")
}

func TestSubmitMorningReportHasStringCompatibleOutput(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_morning_report")
	assert.True(t, program.Analysis().OutputType.Equals(cty.String))
}

func TestSubmitMorningReportRendersReaderFriendlyMarkdown(t *testing.T) {
	t.Parallel()

	packetProgram := compileTool(t, "submit_breakfast_packet")
	reportProgram := compileTool(t, "submit_morning_report")
	workspace := blockDirectory(t, "publish")
	packetPath := filepath.Join(workspace, "upstream", "breakfast-packet.json")
	packetInput := validPacketInput(packetPath)
	packetResponse, err := packetProgram.Invoke(t.Context(), marshalInput(t, packetInput), workspace)
	require.NoError(t, err)
	require.True(t, packetResponse.Accepted, "issues: %#v", packetResponse.Issues)

	reviewPaths := make([]string, 0, 3)
	for _, role := range []string{"macro", "sentiment", "strategy"} {
		path := filepath.Join(workspace, role, "review.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(`{"role":"`+role+`"}`), 0o600))
		reviewPaths = append(reviewPaths, path)
	}

	keys := []string{"sp500", "nasdaq", "china_adr", "a50", "usdcnh", "gold", "crude"}
	snapshot := make([]any, 0, len(keys))
	for _, key := range keys {
		snapshot = append(snapshot, map[string]any{
			"metric_key": key, "plain_language": "这个指标帮助理解隔夜风险偏好。",
		})
	}
	stories := make([]any, 0, 5)
	for index := 1; index <= 5; index++ {
		stories = append(stories, map[string]any{
			"headline": "今晨大事", "what_happened": "已确认的事件发生了。",
			"why_it_matters": "它可能影响融资成本和风险偏好。", "what_to_watch": "继续观察后续数据。",
			"evidence_ids": []string{"event-1"},
		})
	}
	reportJSONPath := filepath.Join(workspace, "morning-report.json")
	reportMarkdownPath := filepath.Join(workspace, "morning.md")
	response, err := reportProgram.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_json_artifact_id":     "artifact-0123456789abcdef0123456789abcdef",
		"report_markdown_artifact_id": "artifact-abcdef0123456789abcdef0123456789",
		"_r42_report_json_path":       reportJSONPath,
		"_r42_report_markdown_path":   reportMarkdownPath,
		"packet_path":                 packetPath,
		"review_paths":                reviewPaths,
		"edition_date":                "2026-08-28",
		"title":                       "2026-08-28 财经早餐",
		"lead":                        []string{"隔夜市场信号混合。", "宏观事件仍是主线。", "普通投资者应先看风险。"},
		"snapshot":                    snapshot,
		"stories":                     stories,
		"themes": []any{map[string]any{
			"name": "融资成本观察", "logic_chain": "利率变化可能影响融资成本，再影响估值情绪。",
			"possible_beneficiaries": []string{"现金流稳健的行业"}, "pressure_points": []string{"高负债行业"},
			"counterpoint": "单日波动未必形成趋势。", "falsification_condition": "后续利率和信用数据反向变化。",
			"confidence": "medium", "evidence_ids": []string{"event-1"},
		}},
		"setups": []any{map[string]any{
			"name": "利率敏感方向", "trigger": "市场利率继续回落。",
			"transmission": "融资成本下降可能改善估值折现。", "affected_areas": []string{"成长板块"},
			"confirmation_signals":   []string{"人民币没有同步走弱", "成交额温和放大"},
			"invalidation_condition": "市场利率反向上行。", "horizon": "开盘至午盘",
			"confidence": "medium", "evidence_ids": []string{"event-1"},
		}},
		"institutional_scan": []any{map[string]any{
			"section": "bonds_rates", "headline": "利率仍是估值锚",
			"summary": "债券与汇率信号需要交叉确认。", "evidence_ids": []string{"event-1"},
		}},
		"calendar_events": []any{map[string]any{
			"time": "待公布", "event": "后续宏观数据", "what_to_watch": "实际值与预期的差异。",
		}},
		"sentiment": map[string]any{
			"label": "mixed", "basis": "股票、汇率和商品没有给出一致方向。", "limitations": "只代表信息截点前的公开数据。",
		},
		"limitations": []string{"部分市场数据可能延迟。"},
	}), workspace)

	require.NoError(t, err)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, reportJSONPath)
	markdown, err := os.ReadFile(reportMarkdownPath)
	require.NoError(t, err)
	reportJSON, err := os.ReadFile(reportJSONPath)
	require.NoError(t, err)
	assert.NotContains(t, string(reportJSON), "artifact_id")
	for _, section := range []string{
		"## 今早先看三件事",
		"## 最新已收盘行情（数据日期：2026-08-28）",
		"## 今晨大事",
		"## 今日主线与市场影响",
		"## 盘前观察清单",
		"## 机构信息扫描",
		"## 今日事件表",
		"## 上一交易时段的市场信号",
		"## 本期局限",
	} {
		assert.Contains(t, string(markdown), section)
	}
	assert.Contains(t, string(markdown), "以上判断基于截至 2026-08-28 的最新已收盘行情，不是晨报发布日期的盘中走势。")
	assert.NotContains(t, string(markdown), "证据：")
	assert.NotContains(t, string(markdown), "artifact-")
	assert.NotContains(t, string(markdown), "## 今天可能怎么映射")
	assert.NotContains(t, string(markdown), "## 给普通投资者的防守提醒")
	for _, rigidLabel := range []string{"**发生了什么：**", "**为什么与你有关：**", "**接下来观察：**"} {
		assert.NotContains(t, string(markdown), rigidLabel)
	}
}

func compileTool(t *testing.T, name string) *gotool.Program {
	t.Helper()
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, name))
	require.NoError(t, err)
	return program
}

func goToolSource(t *testing.T, name string) string {
	t.Helper()
	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("tools.r42.hcl")
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
	require.FailNow(t, "go tool not found", "name=%s", name)
	return ""
}

func blockDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), ".r42", "runs", "run", "blocks", name)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	return directory
}

func marshalInput(t *testing.T, input any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(input)
	require.NoError(t, err)
	return payload
}

func issueCodes(response gotool.Response) []string {
	codes := make([]string, 0, len(response.Issues))
	for _, issue := range response.Issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func validMorningEvidenceInput(artifactPath string) map[string]any {
	return map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": artifactPath,
		"task_id":            "overnight-market",
		"edition_date":       "2026-08-28",
		"question":           "What changed overnight?",
		"claims": []any{map[string]any{
			"id": "overnight-market-kb-1", "statement": "The index rose.",
			"as_of": "2026-08-28", "confidence": "high", "quote_ids": []string{"quote-1"},
		}},
		"quotes": []any{map[string]any{
			"id": "quote-1", "source_title": "Exchange", "url": "https://example.com/market",
			"artifact_id":      "artifact-11111111111111111111111111111111",
			"publication_date": "2026-08-28", "locator": "close table", "exact_quote": "The index rose 1%.",
		}},
	}
}

func validPacketInput(artifactPath string) map[string]any {
	keys := []string{"sp500", "nasdaq", "china_adr", "a50", "usdcnh", "gold", "crude"}
	metrics := make([]any, 0, len(keys))
	for _, key := range keys {
		metrics = append(metrics, map[string]any{
			"key": key, "label": key, "value": "100", "change": "+1%",
			"direction": "up", "as_of": "2026-08-28", "evidence_ids": []string{"overnight-kb-1"},
		})
	}
	return map[string]any{
		"artifact_id":        "artifact-0123456789abcdef0123456789abcdef",
		"_r42_artifact_path": artifactPath,
		"edition_date":       "2026-08-28",
		"cutoff_time":        "07:30 Asia/Shanghai",
		"reviewed_artifacts": []string{},
		"market_snapshot":    metrics,
		"events": []any{map[string]any{
			"id": "event-1", "headline": "Fed holds rates", "category": "macro",
			"status": "occurred", "as_of": "2026-08-28", "importance": 5,
			"summary": "The decision was announced.", "evidence_ids": []string{"overnight-kb-1"},
		}},
		"noise_notes": []string{"Removed repeated wire copies."},
		"institutional_scan": []any{map[string]any{
			"id": "scan-1", "section": "market_liquidity", "headline": "Liquidity is stable",
			"summary": "Funding conditions were little changed.", "evidence_ids": []string{"overnight-kb-1"},
		}},
		"calendar_events": []any{map[string]any{
			"id": "calendar-1", "pub_time": "2026-08-28 20:30", "importance": 4,
			"title": "Macro release", "previous": "1.0", "consensus": "1.1", "actual": "",
			"affect": "Watch the gap between actual and consensus.", "evidence_ids": []string{"overnight-kb-1"},
		}},
		"evidence_catalog": []any{map[string]any{
			"id": "overnight-kb-1", "claim": "A sourced claim.", "confidence": "high",
		}},
	}
}
