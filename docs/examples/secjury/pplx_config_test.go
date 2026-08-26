package secjury_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPPLXIsOptionalAndFinanceFirst(t *testing.T) {
	t.Parallel()

	variables := readText(t, "variables.r42.hcl")
	assert.Contains(t, variables, `variable "use_pplx"`)
	assert.Contains(t, variables, "default     = false")
	assert.Contains(t, variables, `variable "pplx_tool_call_quota"`)

	configuration := readText(t, "main.r42.hcl")
	assert.Contains(t, configuration, `module "pplx_tools"`)
	finance := strings.Index(configuration, "module.pplx_tools.pplx_finance_search_tool_id")
	pro := strings.Index(configuration, "module.pplx_tools.pplx_pro_search_tool_id")
	fetch := strings.Index(configuration, "module.pplx_tools.pplx_fetch_tool_id")
	require.NotEqual(t, -1, finance)
	require.NotEqual(t, -1, pro)
	require.NotEqual(t, -1, fetch)
	assert.Less(t, finance, pro)
	assert.Less(t, pro, fetch)
}

func TestPPLXSwitchesOnlyDCFCollectionAwayFromBuiltInWebTools(t *testing.T) {
	t.Parallel()

	configuration := readText(t, "main.r42.hcl")
	buildStart := strings.Index(configuration, `research "static" "build_dcf"`)
	reviewStart := strings.Index(configuration, `research "dynamic" "review_dcf"`)
	require.NotEqual(t, -1, buildStart)
	require.NotEqual(t, -1, reviewStart)
	buildDCF := configuration[buildStart:reviewStart]

	assert.Contains(t, buildDCF, "collection_tool_ids          = local.dcf_collection_tool_ids")
	assert.Contains(t, buildDCF, "tool_call_quota")
	assert.Contains(t, buildDCF, "disallowed_tools")
	assert.Contains(t, buildDCF, "local.source_tool_guidance")
	assert.Contains(t, configuration, `var.use_pplx ? ["web_search", "web_fetch"] : []`)
	assert.Contains(t, configuration, "dcf_collection_tool_ids = concat([starlark_tool.calculator.id], local.pplx_tool_ids)")
	assert.Contains(t, configuration, "Use pplx_finance_search first")
	assert.Contains(t, configuration, "r42_register_artifact")
	assert.Contains(t, configuration, "built-in web_search")
	assert.Contains(t, configuration, "r42_save_artifact; that call registers it")
	assert.NotContains(t, configuration, "then register it")
}

func TestPPLXModuleExportsAllSecJuryTools(t *testing.T) {
	t.Parallel()

	module := readText(t, "modules/pplx_tools/main.r42.hcl")
	for _, required := range []string{
		`go_tool "pplx_finance_search"`,
		`go_tool "pplx_pro_search"`,
		`go_tool "pplx_fetch"`,
		`output "pplx_finance_search_tool_id"`,
		`output "pplx_pro_search_tool_id"`,
		`output "pplx_fetch_tool_id"`,
		`[]map[string]any{{"type": "finance_search"}}`,
		`"max_steps": 3`,
		`"max_output_tokens": 2048`,
	} {
		assert.Contains(t, module, required)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(payload)
}
