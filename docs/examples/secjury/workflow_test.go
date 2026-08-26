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
	assert.Equal(t, 3, strings.Count(configuration, "model_provider.primary"))
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
	assert.Contains(t, configuration, `research "dynamic" "review_dcf"`)
	assert.Contains(t, configuration, "serial = false")
	assert.Equal(t, 20, strings.Count(configuration, "persona_line ="))
	assert.Contains(t, configuration, "${research.static.build_dcf.result}")
	assert.Contains(t, configuration, `research "static" "synthesize"`)
	assert.Contains(t, configuration, `path        = "${block_wd()}/report.md"`)
	assert.Contains(t, configuration, "[for task in research.dynamic.review_dcf.tasks : task.result]")
	assert.Contains(t, configuration, `starlark_tool "calculator"`)
	assert.Contains(t, configuration, "starlark_tool.calculator.id")
	assert.Contains(t, configuration, `collection_skills            = ["dcf-model", "yahoo-finance"]`)
	assert.Contains(t, configuration, "Use the registered yahoo-finance skill when it can efficiently provide market prices, share data, capital structure, fundamentals, or price history. Prefer its machine-readable JSON output.")
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
	assert.Contains(t, buildDCF, `collection_tool_ids          = local.dcf_collection_tool_ids`)
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
