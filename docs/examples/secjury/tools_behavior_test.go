package secjury_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateDCFProgressPreservesCompletedWork(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	progressPath := filepath.Join(workspace, "progress.json")
	program := compileTool(t, "update_dcf_progress")
	base := map[string]any{
		"progress_path": progressPath,
		"target":        "Example EXM", "valuation_date": "2026-08-26",
	}
	pending := []map[string]any{{"id": "history", "task": "Normalize history", "status": "pending"}}
	completed := []map[string]any{{
		"id": "history", "task": "Normalize history", "status": "completed",
		"results": []string{"Revenue reconciled"}, "source_ids": []string{"src-1"},
	}}

	response := invokeTool(t, program, mergeInput(base, "steps", pending), workspace)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	response = invokeTool(t, program, mergeInput(base, "steps", completed), workspace)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)

	regressed := []map[string]any{{
		"id": "history", "task": "Normalize history", "status": "pending",
		"results": []string{"Revenue reconciled"}, "source_ids": []string{"src-1"},
	}}
	response = invokeTool(t, program, mergeInput(base, "steps", regressed), workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "progress_status")

	removedEvidence := []map[string]any{{
		"id": "history", "task": "Normalize history", "status": "completed",
		"results": []string{"Revenue reconciled"},
	}}
	response = invokeTool(t, program, mergeInput(base, "steps", removedEvidence), workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "progress_evidence")
}

func TestSubmitDCFJurorOpinionValidatesAndWritesOpinion(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_dcf_juror_opinion")
	tests := []struct {
		name   string
		change func(map[string]any)
		code   string
	}{
		{name: "missing juror", change: func(input map[string]any) { input["juror_id"] = " " }, code: "juror_id"},
		{name: "invalid verdict", change: func(input map[string]any) { input["verdict"] = "maybe" }, code: "juror_verdict"},
		{name: "confidence below zero", change: func(input map[string]any) { input["confidence"] = -0.1 }, code: "juror_confidence"},
		{name: "confidence above one", change: func(input map[string]any) { input["confidence"] = 1.1 }, code: "juror_confidence"},
		{name: "missing summary", change: func(input map[string]any) { input["summary"] = " " }, code: "juror_summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			input := validOpinionInput(filepath.Join(workspace, "opinion.json"), "buffett")
			tt.change(input)
			response := invokeTool(t, program, input, workspace)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
			assert.NoFileExists(t, filepath.Join(workspace, "opinion.json"))
		})
	}

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		opinionPath := filepath.Join(workspace, "opinion.json")
		response := invokeTool(t, program, validOpinionInput(opinionPath, "buffett"), workspace)
		assert.True(t, response.Accepted, "issues: %#v", response.Issues)
		payload, err := os.ReadFile(opinionPath)
		require.NoError(t, err)
		assert.JSONEq(t, `{"juror_id":"buffett","verdict":"revise","confidence":0.8,"summary":"Terminal value is optimistic.","findings":[{"severity":"major","category":"terminal","message":"Growth is aggressive.","model_paths":["$.assumptions.terminal_growth"]}]}`, string(payload))
	})
}

func TestSubmitDCFReportRendersFrozenModelAndOrderedOpinions(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	modelPath := filepath.Join(workspace, "model.json")
	sourcesPath := filepath.Join(workspace, "sources.json")
	opinionOnePath := filepath.Join(workspace, "buffett.json")
	opinionTwoPath := filepath.Join(workspace, "munger.json")
	model := validDCFSubmissionInput(workspace)["model"]
	writeJSONFixture(t, modelPath, model)
	writeJSONFixture(t, sourcesPath, []map[string]any{{
		"id": "src-1", "title": "Filing | annual", "url": "https://example.com/filing",
		"published_date": "2026-08-01", "accessed_date": "2026-08-26",
	}})
	writeJSONFixture(t, opinionOnePath, map[string]any{
		"juror_id": "buffett", "verdict": "revise", "confidence": 0.8,
		"summary": "Demand a margin of safety.", "findings": []map[string]any{},
	})
	writeJSONFixture(t, opinionTwoPath, map[string]any{
		"juror_id": "munger", "verdict": "accept", "confidence": 0.6,
		"summary": "The inversion case survives.", "findings": []map[string]any{},
	})
	reportPath := filepath.Join(workspace, "report.md")
	reportJSONPath := filepath.Join(workspace, "report.json")
	input := map[string]any{
		"report_json_path": reportJSONPath, "report_path": reportPath,
		"model_path": modelPath, "sources_path": sourcesPath,
		"opinion_paths": []string{opinionOnePath, opinionTwoPath},
		"jurors": []map[string]any{
			{"id": "buffett", "name": "Warren Buffett", "group": "value_quality", "persona_line": "Value", "method_rules": []string{"margin of safety"}},
			{"id": "munger", "name": "Charlie Munger", "group": "value_quality", "persona_line": "Invert", "method_rules": []string{"invert"}},
		},
		"decision": "revise", "headline": "Value | needs work", "summary": "Model output\nand jury judgment differ.",
		"key_findings": []string{"Terminal | dependence"}, "limitations": []string{"Frozen evidence only"},
	}

	response := invokeTool(t, compileTool(t, "submit_dcf_report"), input, workspace)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, reportJSONPath)
	payload, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	report := string(payload)
	assert.Contains(t, report, "# Value \\| needs work")
	assert.Contains(t, report, "Model output and jury judgment differ.")
	assert.Contains(t, report, "- Terminal \\| dependence")
	assert.Contains(t, report, "## DCF Model")
	assert.Contains(t, report, "### Sensitivity Analysis")
	assert.Contains(t, report, "Filing \\| annual")
	buffett := "### Warren Buffett (buffett)"
	munger := "### Charlie Munger (munger)"
	assert.Contains(t, report, buffett)
	assert.Contains(t, report, munger)
	assert.Less(t, indexOf(t, report, buffett), indexOf(t, report, munger))
}

func TestSubmitDCFReportRejectsInvalidSynthesis(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_dcf_report")
	tests := []struct {
		name  string
		input map[string]any
		code  string
	}{
		{name: "invalid decision", input: map[string]any{"decision": "abstain", "headline": "Headline", "summary": "Summary"}, code: "report_decision"},
		{name: "missing text", input: map[string]any{"decision": "revise", "headline": " ", "summary": "Summary"}, code: "report_text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := invokeTool(t, program, tt.input, t.TempDir())
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
		})
	}
}

func validOpinionInput(path, jurorID string) map[string]any {
	return map[string]any{
		"opinion_path": path, "juror_id": jurorID, "verdict": "revise", "confidence": 0.8,
		"summary": "Terminal value is optimistic.",
		"findings": []map[string]any{{
			"severity": "major", "category": "terminal", "message": "Growth is aggressive.",
			"model_paths": []string{"$.assumptions.terminal_growth"},
		}},
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

func invokeTool(t *testing.T, program *gotool.Program, input map[string]any, workspace string) gotool.Response {
	t.Helper()
	payload, err := json.Marshal(input)
	require.NoError(t, err)
	response, err := program.Invoke(t.Context(), payload, workspace)
	require.NoError(t, err)
	return response
}

func mergeInput(base map[string]any, key string, value any) map[string]any {
	merged := make(map[string]any, len(base)+1)
	maps.Copy(merged, base)
	merged[key] = value
	return merged
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, payload, 0o600))
}

func indexOf(t *testing.T, value, substring string) int {
	t.Helper()
	index := strings.Index(value, substring)
	require.NotEqual(t, -1, index)
	return index
}
