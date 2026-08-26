package secjury_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecJuryHCLParses(t *testing.T) {
	t.Parallel()

	for _, filename := range []string{
		"main.r42.hcl",
		"provider.r42.hcl",
		"variables.r42.hcl",
		"tools.r42.hcl",
		"modules/pplx_tools/main.r42.hcl",
	} {
		t.Run(filename, func(t *testing.T) {
			t.Parallel()
			parser := hclparse.NewParser()
			_, diagnostics := parser.ParseHCLFile(filename)
			require.False(t, diagnostics.HasErrors(), diagnostics.Error())
		})
	}
}

func TestSecJuryPlansWithBuiltInAndPPLXCollectionTools(t *testing.T) {
	t.Parallel()

	for _, usePPLX := range []string{"false", "true"} {
		t.Run("use_pplx="+usePPLX, func(t *testing.T) {
			t.Parallel()

			runtime := cli.NewRuntime()
			stateDirectory := filepath.Join(t.TempDir(), "state")
			require.NoError(t, runtime.InitProject(t.Context(), ".", stateDirectory, false))
			configurationDirectory, modulesDirectory, err := runtime.OpenProject(stateDirectory)
			require.NoError(t, err)
			config, err := runtime.Config(configurationDirectory, executor.ResearchConfigOptions{
				Context:         t.Context(),
				ModuleDirectory: modulesDirectory,
				Variables: []golden.CliFlagAssignedVariables{
					golden.NewCliFlagAssignedVariable("target", `"Microsoft MSFT NASDAQ"`),
					golden.NewCliFlagAssignedVariable("valuation_date", `"2026-08-26"`),
					golden.NewCliFlagAssignedVariable("model_provider", `{ api_key_ref = "SECJURY_TEST_API_KEY" }`),
					golden.NewCliFlagAssignedVariable("use_pplx", usePPLX),
				},
			})
			require.NoError(t, err)
			_, err = executor.RunResearchPlan(config)
			require.NoError(t, err)
		})
	}
}

func TestSecJuryUsesConfigurablePrimaryModelProvider(t *testing.T) {
	t.Parallel()

	provider := readText(t, "provider.r42.hcl")
	for _, required := range []string{
		`model_provider "primary"`,
		"var.model_provider.endpoint",
		"var.model_provider.api_key_ref",
		"var.model_provider.retry.model_call_retries",
	} {
		assert.Contains(t, provider, required)
	}

	variables := readText(t, "variables.r42.hcl")
	assert.Contains(t, variables, `variable "model_provider"`)
	assert.Contains(t, variables, "BYOK model provider")

	configuration := readText(t, "main.r42.hcl")
	assert.Equal(t, 4, strings.Count(configuration, "model_provider.primary"))
}

func TestDCFSkillMatchesCopiedSource(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("skills/dcf-model/SKILL.md")
	require.NoError(t, err)
	normalized := strings.ReplaceAll(string(payload), "\r\n", "\n")
	digest := sha256.Sum256([]byte(normalized))
	assert.Equal(t, "ddee513c690e6f3f762ab143ce5d8d839824ea985914e17468a3d9ad43d6eb74", hex.EncodeToString(digest[:]))
}

func TestYahooFinanceSkillMatchesCopiedSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{name: "skill", path: "skills/yahoo-finance/SKILL.md", expected: "786df6f7834d1ec17343b572c1d3a097a6ccf61034acbdbcb33a24f44df879ba"},
		{name: "script", path: "skills/yahoo-finance/scripts/yf", expected: "d9e4438979eda4faffdc6847862092270babdda6a681241cac2519e4952072ba"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := os.ReadFile(tt.path)
			require.NoError(t, err)
			normalized := strings.ReplaceAll(string(payload), "\r\n", "\n")
			digest := sha256.Sum256([]byte(normalized))
			assert.Equal(t, tt.expected, hex.EncodeToString(digest[:]))
		})
	}
}

func TestWorkflowKeepsOneBuilderParallelJuryAndSynthesis(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload)

	assert.Equal(t, 1, strings.Count(configuration, `research "static" "build_dcf"`))
	assert.Equal(t, 1, strings.Count(configuration, `research "static" "audit_dcf"`))
	assert.Contains(t, configuration, `research "dynamic" "review_dcf"`)
	assert.Contains(t, configuration, "serial = false")
	assert.Equal(t, 20, strings.Count(configuration, "lens_name ="))
	assert.Contains(t, configuration, "${research.static.audit_dcf.result}")
	assert.Contains(t, configuration, `research "static" "synthesize"`)
	assert.Contains(t, configuration, `path        = "${block_wd()}/report.md"`)
	assert.Contains(t, configuration, "[for task in research.dynamic.review_dcf.tasks : task.result]")
	assert.Contains(t, configuration, `starlark_tool "calculator"`)
	assert.Contains(t, configuration, "starlark_tool.calculator.id")
	assert.Contains(t, configuration, `collection_skills            = ["dcf-model", "yahoo-finance"]`)
	assert.Contains(t, configuration, "Use the registered yahoo-finance skill when it can efficiently provide market prices, share data, capital structure, fundamentals, or price history. Prefer its machine-readable JSON output.")
}

func TestJurorsUseDistinctStructuredReviewLenses(t *testing.T) {
	t.Parallel()

	configuration := readText(t, "main.r42.hcl")
	for _, field := range []string{
		"lens_name =",
		"plain_question =",
		"mandate =",
		"required_tests =",
		"out_of_scope =",
		"decision_rule =",
	} {
		assert.Equal(t, 20, strings.Count(configuration, field), "field %q must be defined for every juror", field)
	}

	for _, required := range []string{
		`lens_name = "Failure-mode inversion"`,
		`plain_question = "What must go wrong for this valuation to fail?"`,
		`lens_name = "Accounting and equity-bridge forensics"`,
		`plain_question = "Do accounting choices, cash, debt, or share count make the per-share value misleading?"`,
		`lens_name = "Upside optionality and reinvestment"`,
		`plain_question = "Could the model miss a credible upside path, and what must be reinvested to reach it?"`,
		`lens_name = "Macro regimes and reflexivity"`,
		`plain_question = "How could rates, financing conditions, or market expectations feed back into this valuation?"`,
	} {
		assert.Contains(t, configuration, required)
	}

	for _, required := range []string{
		"Lens: ${juror.lens_name}",
		"Question this role represents: ${juror.plain_question}",
		"Mandate: ${juror.mandate}",
		`${join("\n- ", juror.required_tests)}`,
		`${join("\n- ", juror.out_of_scope)}`,
		"Decision rule: ${juror.decision_rule}",
		"Stay within the assigned lens",
		"Celebrity names are explanatory mnemonics only",
		"do not claim that the real person participated, endorsed, or supplied facts",
		"Write the summary in plain language for a non-specialist reader",
	} {
		assert.Contains(t, configuration, required)
	}
}

func TestWorkflowAuditsAndRevisesCandidateBeforeJuryReview(t *testing.T) {
	t.Parallel()

	configuration := readText(t, "main.r42.hcl")
	buildStart := strings.Index(configuration, `research "static" "build_dcf"`)
	auditStart := strings.Index(configuration, `research "static" "audit_dcf"`)
	reviewStart := strings.Index(configuration, `research "dynamic" "review_dcf"`)
	require.NotEqual(t, -1, buildStart)
	require.NotEqual(t, -1, auditStart)
	require.NotEqual(t, -1, reviewStart)
	assert.Less(t, buildStart, auditStart)
	assert.Less(t, auditStart, reviewStart)

	auditDCF := configuration[auditStart:reviewStart]
	for _, required := range []string{
		`phase_mode       = "collection_only"`,
		`${research.static.build_dcf.result}`,
		"Independently retrieve and register every material raw source",
		`collection_skills                = ["dcf-model", "yahoo-finance"]`,
		`collection_allowed_builtin_tools = ["bash", "powershell", "shell"]`,
		`tool_use "calculate"`,
		`tool_use "pplx_finance_search"`,
		`tool_use "pplx_pro_search"`,
		`tool_use "pplx_fetch"`,
		`tool_use "submit_model"`,
		"point-in-time source completeness",
		"unsupported smooth forecast paths",
		"immediate tax benefits in loss years",
		"terminal value before a demonstrated steady state",
		"working-capital balance versus period change",
		"WACC component build-up",
		"restricted cash, debt-like items, and diluted shares",
		"operating-driver stress tests",
	} {
		assert.Contains(t, auditDCF, required)
	}

	reviewAndSynthesis := configuration[reviewStart:]
	assert.Contains(t, reviewAndSynthesis, `${research.static.audit_dcf.result}`)
	assert.Contains(t, reviewAndSynthesis, `research.static.audit_dcf.artifact.model`)
	assert.Contains(t, reviewAndSynthesis, `research.static.audit_dcf.artifact.sources`)
	assert.NotContains(t, reviewAndSynthesis, `${research.static.build_dcf.result}`)
	assert.NotContains(t, reviewAndSynthesis, `research.static.build_dcf.artifact`)
}

func TestWorkflowPublishesReverseDCFToJuryAndReport(t *testing.T) {
	t.Parallel()

	configuration := readText(t, "main.r42.hcl")
	auditStart := strings.Index(configuration, `research "static" "audit_dcf"`)
	reviewStart := strings.Index(configuration, `research "dynamic" "review_dcf"`)
	synthesisStart := strings.Index(configuration, `research "static" "synthesize"`)
	require.NotEqual(t, -1, auditStart)
	require.NotEqual(t, -1, reviewStart)
	require.NotEqual(t, -1, synthesisStart)

	auditDCF := configuration[auditStart:reviewStart]
	for _, required := range []string{
		`artifact "reverse_dcf"`,
		`path        = "${block_wd()}/modeling/reverse-dcf.json"`,
		`tool_use "submit_reverse_dcf"`,
		"tool_id = go_tool.submit_reverse_dcf.id",
		"market-implied enterprise value",
		"PV of explicit cash flows",
		"implied terminal FCF",
		"sustainable FCF-margin scenarios",
		"optionality gap",
		"Do not change the evidence-supported base case merely to match the market price",
	} {
		assert.Contains(t, auditDCF, required)
	}
	assert.Equal(t, 2, strings.Count(auditDCF, `reverse_dcf_path = artifact("reverse_dcf").path`))

	reviewDCF := configuration[reviewStart:synthesisStart]
	assert.Contains(t, reviewDCF, "research.static.audit_dcf.artifact.reverse_dcf")
	assert.Contains(t, reviewDCF, "Reverse DCF")
	assert.Contains(t, reviewDCF, "$.reverse_dcf")

	synthesis := configuration[synthesisStart:]
	assert.Contains(t, synthesis, `import_artifact "reverse_dcf"`)
	assert.Contains(t, synthesis, "research.static.audit_dcf.artifact.reverse_dcf")
	assert.Contains(t, synthesis, "reverse_dcf_path")
}

func TestWorkflowUsesSinglePhaseSessionsWithoutQC(t *testing.T) {
	t.Parallel()

	configuration := readText(t, "main.r42.hcl")
	buildStart := strings.Index(configuration, `research "static" "build_dcf"`)
	reviewStart := strings.Index(configuration, `research "dynamic" "review_dcf"`)
	synthesisStart := strings.Index(configuration, `research "static" "synthesize"`)
	require.NotEqual(t, -1, buildStart)
	require.NotEqual(t, -1, reviewStart)
	require.NotEqual(t, -1, synthesisStart)

	buildDCF := configuration[buildStart:reviewStart]
	reviewDCF := configuration[reviewStart:synthesisStart]
	synthesize := configuration[synthesisStart:]
	assert.Contains(t, buildDCF, `phase_mode       = "collection_only"`)
	assert.NotContains(t, buildDCF, "collection_tool_ids")
	assert.Contains(t, buildDCF, `tool_use "calculate"`)
	assert.Contains(t, buildDCF, "tool_id = starlark_tool.calculator.id")
	assert.Contains(t, buildDCF, "data_json")
	assert.Contains(t, buildDCF, `collection_allowed_builtin_tools = ["bash", "powershell", "shell"]`)
	assert.Contains(t, buildDCF, "All derived numeric values must be calculated by calling")
	assert.NotContains(t, buildDCF, "During closed Research")
	assert.NotContains(t, buildDCF, "collection_qc")
	assert.NotContains(t, buildDCF, "  skill_directories            =")
	assert.NotContains(t, buildDCF, "skills                       =")

	assert.Contains(t, reviewDCF, `phase_mode       = "research_only"`)
	assert.NotContains(t, reviewDCF, "During Collection")
	assert.NotContains(t, reviewDCF, "collection_qc")
	assert.NotContains(t, reviewDCF, "qc            =")

	assert.Contains(t, synthesize, `phase_mode       = "research_only"`)
	assert.NotContains(t, synthesize, "During Collection")
	assert.NotContains(t, synthesize, "collection_qc")
	assert.NotContains(t, synthesize, "qc {")
}

func TestDCFPromptRetainsOriginalGenerationContract(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload)

	for _, required := range []string{
		`schema_version "dcf-model.v2"`,
		"3-5 historical periods",
		"5-10 projection periods",
		"odd square WACC/terminal-growth sensitivity grid",
		"model and sources",
		"progress.json",
		"do not create any spreadsheet or xlsx artifact",
	} {
		assert.Contains(t, configuration, required)
	}
}

func TestSecJuryGoToolsCompile(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })

	for _, name := range []string{
		"update_dcf_progress",
		"submit_dcf_model",
		"submit_reverse_dcf",
		"submit_dcf_juror_opinion",
		"submit_dcf_report",
		"pplx_finance_search",
		"pplx_pro_search",
		"pplx_fetch",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, compileErr := compiler.Compile(t.Context(), goToolSource(t, name))
			require.NoError(t, compileErr)
		})
	}
}

func TestSubmitDCFModelRejectsInvalidSensitivityGrid(t *testing.T) {
	t.Parallel()

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_dcf_model"))
	require.NoError(t, err)

	tests := []struct {
		name        string
		assumptions map[string]any
		sensitivity []map[string]any
	}{
		{
			name:        "empty",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: nil,
		},
		{
			name:        "duplicate point",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: append(sensitivityGrid(), map[string]any{"wacc": 0.10, "terminal_growth": 0.03, "implied_value_per_share": 90.0}),
		},
		{
			name:        "terminal growth reaches WACC",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: []map[string]any{{"wacc": 0.03, "terminal_growth": 0.03, "implied_value_per_share": 90.0}},
		},
		{
			name:        "one by one is not sensitivity analysis",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: []map[string]any{{"wacc": 0.10, "terminal_growth": 0.03, "implied_value_per_share": 90.0}},
		},
		{
			name:        "not square",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: []map[string]any{
				{"wacc": 0.09, "terminal_growth": 0.02, "implied_value_per_share": 100.0},
				{"wacc": 0.10, "terminal_growth": 0.03, "implied_value_per_share": 90.0},
				{"wacc": 0.11, "terminal_growth": 0.04, "implied_value_per_share": 80.0},
			},
		},
		{
			name:        "even square",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: makeSensitivityGrid([]float64{0.09, 0.10}, []float64{0.02, 0.03}, 90.0),
		},
		{
			name:        "rectangular",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: makeSensitivityGrid([]float64{0.09, 0.10}, []float64{0.01, 0.02, 0.03}, 90.0),
		},
		{
			name:        "base case missing",
			assumptions: map[string]any{"wacc": 0.105, "terminal_growth": 0.03},
			sensitivity: sensitivityGrid(),
		},
		{
			name:        "base value mismatch",
			assumptions: map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			sensitivity: makeSensitivityGrid([]float64{0.09, 0.10, 0.11}, []float64{0.02, 0.03, 0.04}, 80.0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			writeCompletedProgress(t, workspace)
			input := validDCFSubmissionInput(workspace)
			model, ok := input["model"].(map[string]any)
			require.True(t, ok)
			model["assumptions"] = tt.assumptions
			model["sensitivity"] = tt.sensitivity

			payload, marshalErr := json.Marshal(input)
			require.NoError(t, marshalErr)
			response, invokeErr := program.Invoke(t.Context(), payload, workspace)
			require.NoError(t, invokeErr)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), "sensitivity_grid")
		})
	}
}

func TestSubmitDCFModelAcceptsCompleteSensitivityGrid(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeCompletedProgress(t, workspace)
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_dcf_model"))
	require.NoError(t, err)
	payload, err := json.Marshal(validDCFSubmissionInput(workspace))
	require.NoError(t, err)

	response, err := program.Invoke(t.Context(), payload, workspace)
	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	assert.FileExists(t, filepath.Join(workspace, "dcf-output.json"))
	assert.FileExists(t, filepath.Join(workspace, "dcf-model.json"))
	assert.FileExists(t, filepath.Join(workspace, "dcf-sources.json"))
}

func TestSubmitDCFModelRejectsInvalidSources(t *testing.T) {
	t.Parallel()

	program := compileTool(t, "submit_dcf_model")
	tests := []struct {
		name    string
		sources []map[string]any
		code    string
	}{
		{
			name:    "missing required fields",
			sources: []map[string]any{{"id": " ", "title": " ", "url": " ", "accessed_date": " "}},
			code:    "source_fields",
		},
		{
			name: "duplicate ID",
			sources: []map[string]any{
				{"id": "src-1", "title": "One", "url": "https://example.com/one", "accessed_date": "2026-08-26"},
				{"id": "src-1", "title": "Two", "url": "https://example.com/two", "accessed_date": "2026-08-26"},
			},
			code: "source_id",
		},
		{
			name:    "unsupported URL scheme",
			sources: []map[string]any{{"id": "src-1", "title": "One", "url": "file:///tmp/source", "accessed_date": "2026-08-26"}},
			code:    "source_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			writeCompletedProgress(t, workspace)
			input := validDCFSubmissionInput(workspace)
			input["sources"] = tt.sources
			response := invokeTool(t, program, input, workspace)
			assert.False(t, response.Accepted)
			assert.Contains(t, issueCodes(response), tt.code)
		})
	}
}

func TestSubmitDCFModelValidatesReverseDCFModelAndSourceReferences(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeCompletedProgress(t, workspace)
	reversePath := filepath.Join(workspace, "reverse-dcf.json")
	reverseResponse := invokeTool(t, compileTool(t, "submit_reverse_dcf"), validReverseDCFInput(reversePath), workspace)
	require.True(t, reverseResponse.Accepted, "issues: %#v", reverseResponse.Issues)

	input := validDCFSubmissionInput(workspace)
	input["reverse_dcf_path"] = reversePath
	model := requireFixtureValue[map[string]any](t, input, "model")
	company := requireFixtureValue[map[string]any](t, model, "company")
	company["currency"] = "CNY"
	model["assumptions"] = map[string]any{"wacc": 0.11, "terminal_growth": 0.03}
	model["valuation"] = map[string]any{
		"enterprise_value": -16.0, "pv_explicit_fcf": 165.0027964274,
		"net_debt": -656.0, "diluted_shares": 70.18,
		"implied_value_per_share": 9.13, "current_price": 83.99,
	}
	projections := requireFixtureValue[[]map[string]any](t, model, "projections")
	projections[len(projections)-1] = map[string]any{
		"period": "2030", "revenue": 288.0, "ebit": 1.0, "da": 0.0,
		"capex": 0.0, "change_nwc": 0.0, "ufcf": -5.0, "discount_period": 5.0,
	}
	model["sensitivity"] = makeSensitivityGrid([]float64{0.10, 0.11, 0.12}, []float64{0.02, 0.03, 0.04}, 9.13)
	input["sources"] = []map[string]any{
		{"id": "src-price", "title": "Price", "url": "https://example.com/price", "accessed_date": "2026-08-26"},
		{"id": "src-rna", "title": "RNA", "url": "https://example.com/rna", "accessed_date": "2026-08-26"},
	}

	program := compileTool(t, "submit_dcf_model")
	response := invokeTool(t, program, input, workspace)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)

	incompletePath := filepath.Join(workspace, "reverse-incomplete.json")
	incomplete := validReverseDCFInput(incompletePath)
	delete(incomplete, "reverse_dcf_path")
	delete(incomplete, "implied_expectations")
	delete(incomplete, "revenue_scenarios")
	delete(incomplete, "conclusion")
	delete(incomplete, "limitations")
	writeJSONFixture(t, incompletePath, incomplete)
	input["reverse_dcf_path"] = incompletePath
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_contract")

	invalidFormulaPath := filepath.Join(workspace, "reverse-invalid-formula.json")
	invalidFormula := validReverseDCFInput(invalidFormulaPath)
	delete(invalidFormula, "reverse_dcf_path")
	invalidExpectations := requireFixtureValue[map[string]any](t, invalidFormula, "implied_expectations")
	invalidExpectations["terminal_fcf"] = 100.0
	writeJSONFixture(t, invalidFormulaPath, invalidFormula)
	input["reverse_dcf_path"] = invalidFormulaPath
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_contract")
	input["reverse_dcf_path"] = reversePath

	input["sources"] = []map[string]any{
		{"id": "src-price", "title": "Price", "url": "https://example.com/price", "accessed_date": "2026-08-26"},
	}
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_source_id")

	input["sources"] = []map[string]any{
		{"id": "src-price", "title": "Price", "url": "https://example.com/price", "accessed_date": "2026-08-26"},
		{"id": "src-rna", "title": "RNA", "url": "https://example.com/rna", "accessed_date": "2026-08-26"},
	}
	valuation := requireFixtureValue[map[string]any](t, model, "valuation")
	valuation["current_price"] = 80.0
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_model_match")
	valuation["current_price"] = 83.99

	identityPath := filepath.Join(workspace, "reverse-identity.json")
	identity := validReverseDCFInput(identityPath)
	delete(identity, "reverse_dcf_path")
	identity["valuation_date"] = "2026-08-25"
	writeJSONFixture(t, identityPath, identity)
	input["reverse_dcf_path"] = identityPath
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_model_match")

	input["reverse_dcf_path"] = filepath.Join(workspace, "missing-reverse.json")
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_artifact")

	invalidPath := filepath.Join(workspace, "invalid-reverse.json")
	require.NoError(t, os.WriteFile(invalidPath, []byte("not json"), 0o600))
	input["reverse_dcf_path"] = invalidPath
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "reverse_artifact")

	input["reverse_dcf_path"] = reversePath
	model["projections"] = []map[string]any{}
	response = invokeTool(t, program, input, workspace)
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "projection_periods")
	assert.Contains(t, issueCodes(response), "reverse_model_match")
}

func validDCFSubmissionInput(workspace string) map[string]any {
	period := map[string]any{"period": "FY", "revenue": 1.0, "ebit": 1.0, "da": 0.0, "capex": 0.0, "change_nwc": 0.0}
	return map[string]any{
		"combined_path":  filepath.Join(workspace, "dcf-output.json"),
		"model_path":     filepath.Join(workspace, "dcf-model.json"),
		"sources_path":   filepath.Join(workspace, "dcf-sources.json"),
		"progress_path":  filepath.Join(workspace, "progress.json"),
		"target":         "Example EXM",
		"valuation_date": "2026-08-26",
		"model": map[string]any{
			"schema_version": "dcf-model.v2",
			"company":        map[string]any{"name": "Example", "ticker": "EXM", "exchange": "NASDAQ", "currency": "USD"},
			"valuation_date": "2026-08-26",
			"assumptions":    map[string]any{"wacc": 0.10, "terminal_growth": 0.03},
			"historical":     []map[string]any{period, period, period},
			"projections":    []map[string]any{period, period, period, period, period},
			"valuation":      map[string]any{"implied_value_per_share": 90.0},
			"sensitivity":    sensitivityGrid(),
		},
		"sources": []map[string]any{{"id": "src-1", "title": "Source", "url": "https://example.com", "accessed_date": "2026-08-26"}},
	}
}

func sensitivityGrid() []map[string]any {
	return makeSensitivityGrid([]float64{0.09, 0.10, 0.11}, []float64{0.02, 0.03, 0.04}, 90.0)
}

func makeSensitivityGrid(waccs, growths []float64, value float64) []map[string]any {
	points := make([]map[string]any, 0, len(waccs)*len(growths))
	for _, wacc := range waccs {
		for _, growth := range growths {
			points = append(points, map[string]any{"wacc": wacc, "terminal_growth": growth, "implied_value_per_share": value})
		}
	}
	return points
}

func writeCompletedProgress(t *testing.T, workspace string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"schema_version": "dcf-progress.v1",
		"target":         "Example EXM",
		"valuation_date": "2026-08-26",
		"steps":          []map[string]any{{"status": "completed"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "progress.json"), payload, 0o600))
}

func issueCodes(response gotool.Response) []string {
	codes := make([]string, 0, len(response.Issues))
	for _, issue := range response.Issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func goToolSource(t *testing.T, name string) string {
	t.Helper()
	for _, filename := range []string{"tools.r42.hcl", "modules/pplx_tools/main.r42.hcl"} {
		if source, ok := goToolSourceFromFile(t, filename, name); ok {
			return source
		}
	}
	require.FailNow(t, "go tool not found", "name=%s", name)
	return ""
}

func goToolSourceFromFile(t *testing.T, filename, name string) (string, bool) {
	t.Helper()
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
		return value.AsString(), true
	}
	return "", false
}
