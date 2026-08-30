package morning_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMorningWorkflowHasEvidenceGatesAndReadableOutput(t *testing.T) {
	t.Parallel()

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("main.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)

	stages := map[string]string{}
	for _, block := range body.Blocks {
		if block.Type == "research" && len(block.Labels) == 2 {
			stages[block.Labels[1]] = block.Labels[0]
		}
	}
	assert.Equal(t, map[string]string{
		"scan":             "dynamic",
		"freeze_packet":    "static",
		"packet_editor":    "static",
		"review":           "dynamic",
		"news_digest":      "static",
		"publish":          "static",
		"publisher_editor": "static",
	}, stages)

	payload, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	localsPayload, err := os.ReadFile("locals.r42.hcl")
	require.NoError(t, err)
	variablesPayload, err := os.ReadFile("variables.r42.hcl")
	require.NoError(t, err)
	configuration := string(payload) + string(localsPayload) + string(variablesPayload)
	for _, contract := range []string{
		"overnight-market",
		"macro-policy",
		"industry-themes",
		"submit_breakfast_packet",
		"submit_morning_scan",
		"submit_breakfast_review",
		"submit_morning_draft",
		"publisher_editor",
		"news_digest",
		"research_only",
		"ordinary reader",
		"what happened",
		"why it matters",
		"what to watch",
		"not personalized investment advice",
		"one assigned",
		"Each non-quote task has exactly one",
		"Quote tasks use the bounded Yahoo-first fallback",
		"Do not call the configured Jin10 tool before the Yahoo step",
		"unsupported",
		"retry get_quote once",
		"The typed tool fills name and kind from required_coverage",
		"r42:claim=",
		"morning-draft.annotated.md",
		"morning-provenance.json",
	} {
		assert.Contains(t, configuration, contract)
	}
	assert.Contains(t, configuration, `annotated_artifact_id  = artifact("report_annotated").id`)
	assert.Contains(t, configuration, `provenance_artifact_id = artifact("report_provenance").id`)
	assert.Contains(t, configuration, `markdown_artifact_id   = artifact("report_markdown").id`)
	assert.Contains(t, configuration, `variable "news_items_per_keyword"`)
	assert.Contains(t, configuration, `default     = 6`)
	assert.Contains(t, configuration, `variable "morning_news_limit"`)
	assert.Contains(t, configuration, `default     = 15`)
	assert.Contains(t, configuration, "previous_date")
	assert.Contains(t, configuration, "research.dynamic.scan.tasks")
	assert.Contains(t, configuration, "research.dynamic.review.tasks")
	assert.NotContains(t, configuration, "PowerShell")
	assert.NotContains(t, configuration, "curl")
}

func TestMorningConfigurationParsesAsHCL(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	parsed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".r42.hcl") {
			continue
		}
		parser := hclparse.NewParser()
		_, diagnostics := parser.ParseHCLFile(entry.Name())
		assert.False(t, diagnostics.HasErrors(), "%s: %s", entry.Name(), diagnostics.Error())
		parsed++
	}
	assert.GreaterOrEqual(t, parsed, 4)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestMorningConfigurationPlansWithNativeMCP(t *testing.T) {
	runtime := cli.NewRuntime()
	config, err := runtime.Config(".", executor.ResearchConfigOptions{
		Context: t.Context(),
		Variables: []golden.CliFlagAssignedVariables{
			golden.NewCliFlagAssignedVariable("model_provider", "{}"),
			golden.NewCliFlagAssignedVariable("qc_model_provider", "{}"),
		},
	})
	require.NoError(t, err)

	planned, err := executor.RunResearchPlan(config)

	require.NoError(t, err)
	require.NotNil(t, planned.SavedPlan())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestMorningDefaultEditionDateUsesShanghaiCurrentDate(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	expectedDates := map[string]struct{}{
		time.Now().In(shanghai).Format(time.DateOnly): {},
	}

	runtime := cli.NewRuntime()
	config, err := runtime.Config(".", executor.ResearchConfigOptions{
		Context: t.Context(),
		Variables: []golden.CliFlagAssignedVariables{
			golden.NewCliFlagAssignedVariable("model_provider", "{}"),
			golden.NewCliFlagAssignedVariable("qc_model_provider", "{}"),
		},
	})
	require.NoError(t, err)
	expectedDates[time.Now().In(shanghai).Format(time.DateOnly)] = struct{}{}

	localValue, ok := config.EvalContext().Variables["local"]
	require.True(t, ok)
	require.True(t, localValue.Type().HasAttribute("edition_date"))
	actual := localValue.GetAttr("edition_date").AsString()
	_, matched := expectedDates[actual]
	assert.True(t, matched, "edition_date %q is not the current Asia/Shanghai date", actual)
}

func TestMorningExampleDocumentsAudienceAndRiskBoundary(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("README.md")
	require.NoError(t, err)
	readme := string(payload)
	for _, required := range []string{
		"普通读者",
		"非投资建议",
		"多元化",
		"杠杆",
		"隐性成本",
		"r42 plan",
		"r42 apply",
	} {
		assert.Contains(t, readme, required)
	}
}

func TestMorningWorkflowScopesFinancialDataToolsToCollectionRoles(t *testing.T) {
	t.Parallel()

	configuration := readMorningText(t, "main.r42.hcl") +
		readMorningText(t, "locals.r42.hcl") +
		readMorningText(t, "variables.r42.hcl")
	for _, required := range []string{
		`mcp_server "jin10"`,
		`resources = ["quote://codes"]`,
		`bearer_token_ref = var.jin10_mcp_token_ref`,
		`bearer_token     = var.jin10_mcp_token`,
		`collection_mcp_tool_ids = action.use_jin10 ? [action.tool_id] : []`,
		`collection_mcp_resource_ids = action.use_jin10 ? [mcp_server.jin10.resource_ids["quote://codes"]] : []`,
		`collection_skill_directories = ["${path.module}/skills"]`,
		`collection_skills = ["yahoo-finance"]`,
		`collection_allowed_builtin_tools = ["bash", "powershell", "shell", "web_search", "web_fetch"]`,
		`mcp_server.jin10.tool_ids["list_calendar"]`,
		`Store get_quote/get_kline's complete data object`,
		`Store list_flash/search_flash's complete data.items`,
		`Store list_news/search_news's complete`,
		`data.items and pagination fields as a news-index artifact`,
		`get_news's complete data including content`,
		`Store list_calendar's complete data array`,
		`complete result of r42_read_mcp_resource`,
		`"china-desk"`,
		"one initial call",
		"has_more and next_cursor",
		"do not blindly paginate",
		"select at most ${var.morning_news_limit}",
		"source_urls",
		"copy every source URL",
		"submit_morning_news_digest",
		"collection_allowed_builtin_tools = [\"web_fetch\"]",
		"at most three sentences",
		"Do not search or fetch additional stories",
		"web_search",
	} {
		assert.Contains(t, configuration, required)
	}
	assert.NotContains(t, configuration, `module "jin10_tools"`)
	assert.NotContains(t, configuration, "jin10_tool_call_quota")
	assert.NotContains(t, configuration, `use_yahoo`)
	assert.FileExists(t, "skills/yahoo-finance/SKILL.md")
	assert.FileExists(t, "skills/yahoo-finance/scripts/yf")

	quoteGuidance := readMorningText(t, "locals.r42.hcl")
	initialYahoo := strings.Index(quoteGuidance, "1) 先用 yahoo-finance")
	codeDiscovery := strings.Index(quoteGuidance, "4) 若仍没有可用结果")
	retryYahoo := strings.Index(quoteGuidance, "5) 若 web_search 找到新代码，从第 1 步重新开始：先用 Yahoo")
	require.GreaterOrEqual(t, initialYahoo, 0)
	require.Greater(t, codeDiscovery, initialYahoo)
	require.Greater(t, retryYahoo, codeDiscovery)
}

func TestMorningWorkflowExposesExplicitEmptyEvidenceReason(t *testing.T) {
	t.Parallel()

	workflow := readMorningText(t, "main.r42.hcl")
	assert.Contains(t, workflow, `status = {`)
	assert.Contains(t, workflow, `no_material_news`)
	assert.Contains(t, workflow, `sources = []`)
}

func TestMorningWorkflowUsesDedicatedQCModel(t *testing.T) {
	t.Parallel()

	variables := readMorningText(t, "variables.r42.hcl")
	workflow := readMorningText(t, "main.r42.hcl")

	assert.Contains(t, variables, `variable "qc_model"`)
	assert.Equal(t, 1, strings.Count(workflow, "= var.qc_model"))
}

func TestMorningWorkflowRequiresInstitutionalScanAndConditionalSetups(t *testing.T) {
	t.Parallel()

	configuration := readMorningText(t, "main.r42.hcl") + readMorningText(t, "tools.r42.hcl")
	for _, required := range []string{
		"institutional_scan",
		"calendar_events",
		"confirmation_signals",
		"invalidation_condition",
		"盘前观察清单",
		"机构信息扫描",
		"natural Chinese morning newspaper",
	} {
		assert.Contains(t, configuration, required)
	}
}

func readMorningText(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(payload)
}
