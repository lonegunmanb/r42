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

func TestSubmitReverseDCFValidatesCalculationsAndWritesArtifact(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_reverse_dcf")
	t.Run("valid", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		path := filepath.Join(workspace, "reverse-dcf.json")
		response := invokeTool(t, program, validReverseDCFInput(path), workspace)
		assert.True(t, response.Accepted, "issues: %#v", response.Issues)
		assert.FileExists(t, path)
	})

	tests := []struct {
		name   string
		change func(*testing.T, map[string]any)
		code   string
	}{
		{
			name: "missing workflow path",
			change: func(_ *testing.T, input map[string]any) {
				input["reverse_dcf_path"] = " "
			},
			code: "reverse_path",
		},
		{
			name: "invalid schema",
			change: func(_ *testing.T, input map[string]any) {
				input["schema_version"] = "reverse-dcf.v2"
			},
			code: "reverse_schema",
		},
		{
			name: "missing identity",
			change: func(_ *testing.T, input map[string]any) {
				input["currency"] = " "
			},
			code: "reverse_identity",
		},
		{
			name: "invalid market snapshot",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				market := requireFixtureValue[map[string]any](t, input, "market_snapshot")
				market["price"] = 0.0
			},
			code: "reverse_market_snapshot",
		},
		{
			name: "market cap does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				market := requireFixtureValue[map[string]any](t, input, "market_snapshot")
				market["market_cap"] = 100.0
			},
			code: "reverse_market_cap",
		},
		{
			name: "market EV does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				market := requireFixtureValue[map[string]any](t, input, "market_snapshot")
				market["market_implied_enterprise_value"] = 100.0
			},
			code: "reverse_market_ev",
		},
		{
			name: "invalid fixed assumptions",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				assumptions := requireFixtureValue[map[string]any](t, input, "fixed_assumptions")
				assumptions["wacc"] = 0.03
			},
			code: "reverse_assumptions",
		},
		{
			name: "EV gap does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				implied := requireFixtureValue[map[string]any](t, input, "implied_expectations")
				implied["enterprise_value_gap"] = 100.0
			},
			code: "reverse_ev_gap",
		},
		{
			name: "terminal FCF does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				implied := requireFixtureValue[map[string]any](t, input, "implied_expectations")
				implied["terminal_fcf"] = 100.0
			},
			code: "reverse_terminal_fcf",
		},
		{
			name: "terminal FCF does not explain market EV",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				base := requireFixtureValue[map[string]any](t, input, "base_case")
				base["pv_explicit_fcf"] = 0.0
			},
			code: "reverse_implied_fcf",
		},
		{
			name: "implied FCF margin does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				implied := requireFixtureValue[map[string]any](t, input, "implied_expectations")
				implied["fcf_to_modeled_revenue"] = 0.2
			},
			code: "reverse_implied_margin",
		},
		{
			name: "too few revenue scenarios",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				scenarios := requireFixtureValue[[]map[string]any](t, input, "revenue_scenarios")
				input["revenue_scenarios"] = scenarios[:2]
			},
			code: "reverse_revenue_scenarios",
		},
		{
			name: "invalid revenue scenario",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				scenarios := requireFixtureValue[[]map[string]any](t, input, "revenue_scenarios")
				scenarios[0]["fcf_margin"] = 0.0
			},
			code: "reverse_revenue_scenario",
		},
		{
			name: "revenue scenario does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				scenarios := requireFixtureValue[[]map[string]any](t, input, "revenue_scenarios")
				scenarios[0]["required_revenue"] = 100.0
			},
			code: "reverse_revenue_scenario",
		},
		{
			name: "optionality gap does not reconcile",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				optionality := requireFixtureValue[map[string]any](t, input, "optionality")
				optionality["unexplained_enterprise_value"] = 100.0
			},
			code: "reverse_optionality_gap",
		},
		{
			name: "positive gap has no unproven requirements",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				optionality := requireFixtureValue[map[string]any](t, input, "optionality")
				optionality["unproven_requirements"] = []string{}
			},
			code: "reverse_optionality",
		},
		{
			name: "optionality driver has no source",
			change: func(t *testing.T, input map[string]any) {
				t.Helper()

				optionality := requireFixtureValue[map[string]any](t, input, "optionality")
				drivers := requireFixtureValue[[]map[string]any](t, optionality, "drivers")
				drivers[0]["supporting_source_ids"] = []string{}
			},
			code: "reverse_optionality_driver",
		},
		{
			name: "missing conclusion",
			change: func(_ *testing.T, input map[string]any) {
				input["conclusion"] = " "
			},
			code: "reverse_conclusion",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			path := filepath.Join(workspace, "reverse-dcf.json")
			input := validReverseDCFInput(path)
			tt.change(t, input)
			response := invokeTool(t, program, input, workspace)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
			assert.NoFileExists(t, path)
		})
	}
}

func TestSubmitDCFReportRendersFrozenModelAndOrderedOpinions(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	modelPath := filepath.Join(workspace, "model.json")
	sourcesPath := filepath.Join(workspace, "sources.json")
	reverseDCFPath := filepath.Join(workspace, "reverse-dcf.json")
	opinionOnePath := filepath.Join(workspace, "buffett.json")
	opinionTwoPath := filepath.Join(workspace, "munger.json")
	model := validDCFSubmissionInput(workspace)["model"]
	writeJSONFixture(t, modelPath, model)
	writeJSONFixture(t, sourcesPath, []map[string]any{{
		"id": "src-1", "title": "Filing | annual", "url": "https://example.com/filing",
		"published_date": "2026-08-01", "accessed_date": "2026-08-26",
	}})
	reverseDCF := validReverseDCFInput(reverseDCFPath)
	delete(reverseDCF, "reverse_dcf_path")
	writeJSONFixture(t, reverseDCFPath, reverseDCF)
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
		"model_path": modelPath, "sources_path": sourcesPath, "reverse_dcf_path": reverseDCFPath,
		"opinion_paths": []string{opinionOnePath, opinionTwoPath},
		"jurors": []map[string]any{
			{
				"id": "buffett", "name": "Warren Buffett", "group": "value_quality",
				"lens_name": "Durable cash generation", "plain_question": "Can this business reliably turn its advantages into cash?",
				"mandate": "Test cash durability.", "required_tests": []string{"cash conversion"},
				"out_of_scope": []string{"accounting forensics"}, "decision_rule": "Require a margin of safety.",
			},
			{
				"id": "munger", "name": "Charlie Munger", "group": "value_quality",
				"lens_name": "Failure-mode inversion", "plain_question": "What must go wrong for this valuation to fail?",
				"mandate": "Invert the thesis.", "required_tests": []string{"failure conditions"},
				"out_of_scope": []string{"source traceability"}, "decision_rule": "Reject fragile cases.",
			},
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
	reverseHeading := "## Market-Implied Expectations (Reverse DCF)"
	assert.Contains(t, report, reverseHeading)
	assert.Less(t, indexOf(t, report, reverseHeading), indexOf(t, report, "### Sensitivity Analysis"))
	assert.Contains(t, report, "Market-implied enterprise value")
	assert.Contains(t, report, "Implied terminal FCF")
	assert.Contains(t, report, "20% sustainable FCF margin")
	assert.Contains(t, report, "mRNA pipeline")
	assert.Contains(t, report, "Current contribution")
	assert.Contains(t, report, "11.38% of product revenue")
	assert.Contains(t, report, "Filing \\| annual")
	assert.Contains(t, report, "Celebrity names are familiar analytical mnemonics")
	assert.Contains(t, report, "The real people did not participate in or endorse this report.")
	buffett := "### Warren Buffett · Durable cash generation"
	munger := "### Charlie Munger · Failure-mode inversion"
	assert.Contains(t, report, buffett)
	assert.Contains(t, report, munger)
	assert.Less(t, indexOf(t, report, buffett), indexOf(t, report, munger))
	assert.Contains(t, report, "**Question this role represents:** Can this business reliably turn its advantages into cash?")
	assert.Contains(t, report, "**Review result:** revise")
	assert.Contains(t, report, "**Plain-language takeaway:** Demand a margin of safety.")
}

func validReverseDCFInput(path string) map[string]any {
	return map[string]any{
		"reverse_dcf_path": path,
		"schema_version":   "reverse-dcf.v1",
		"valuation_date":   "2026-08-26",
		"currency":         "CNY",
		"monetary_unit":    "millions",
		"market_snapshot": map[string]any{
			"price": 83.99, "diluted_shares": 70.18, "market_cap": 5894.4182,
			"net_debt": -656.0, "market_implied_enterprise_value": 5238.4182,
			"price_source_ids": []string{"src-price"},
		},
		"base_case": map[string]any{
			"enterprise_value": -16.0, "pv_explicit_fcf": 165.0027964274, "implied_value_per_share": 9.13,
			"final_projection_period": "2030", "final_revenue": 288.0, "final_ufcf": -5.0,
		},
		"fixed_assumptions": map[string]any{
			"wacc": 0.11, "terminal_growth": 0.03, "terminal_discount_period": 5.0,
		},
		"implied_expectations": map[string]any{
			"terminal_fcf": 683.92, "final_year_fcf": 664.0,
			"fcf_to_modeled_revenue": 2.3055555556, "enterprise_value_gap": 5254.4182,
		},
		"revenue_scenarios": []map[string]any{
			{"name": "10% sustainable FCF margin", "fcf_margin": 0.10, "required_revenue": 6640.0, "revenue_multiple_vs_modeled": 23.0555555556, "interpretation": "Requires a radically larger business."},
			{"name": "20% sustainable FCF margin", "fcf_margin": 0.20, "required_revenue": 3320.0, "revenue_multiple_vs_modeled": 11.5277777778, "interpretation": "Still far above the base forecast."},
			{"name": "30% sustainable FCF margin", "fcf_margin": 0.30, "required_revenue": 2213.3333333333, "revenue_multiple_vs_modeled": 7.6851851852, "interpretation": "Needs exceptional scale and cash conversion."},
		},
		"optionality": map[string]any{
			"unexplained_enterprise_value": 5254.4182,
			"drivers": []map[string]any{{
				"name": "mRNA pipeline", "current_evidence": "The company reports RNA vaccine product activity.",
				"current_contribution": "11.38% of product revenue", "stage": "Early commercial contribution",
				"supporting_source_ids":        []string{"src-rna"},
				"required_scale_or_milestones": []string{"commercial scale and durable margins"},
				"assessment":                   "Existence is supported, but the scale required by market EV is not yet demonstrated.",
			}},
			"unproven_requirements": []string{"The combined drivers must close the enterprise-value gap."},
		},
		"conclusion":  "The current price implies a business scale absent from the base forecast.",
		"limitations": []string{"Reverse DCF identifies required expectations; it does not prove they will occur."},
	}
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

func requireFixtureValue[T any](t *testing.T, values map[string]any, key string) T {
	t.Helper()

	value, ok := values[key].(T)
	require.True(t, ok, "fixture key %q has unexpected type", key)
	return value
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
