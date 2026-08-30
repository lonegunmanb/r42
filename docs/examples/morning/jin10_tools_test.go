package morning_test

import (
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestMorningUsesNativeJin10MCPServer(t *testing.T) {
	t.Parallel()

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("main.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)

	var server *hclsyntax.Block
	for _, block := range body.Blocks {
		if block.Type == "mcp_server" && len(block.Labels) == 1 && block.Labels[0] == "jin10" {
			server = block
			break
		}
	}
	require.NotNil(t, server)

	toolsValue := literalAttributeValue(t, server, "tools")
	var tools []string
	for iterator := toolsValue.ElementIterator(); iterator.Next(); {
		_, value := iterator.Element()
		tools = append(tools, value.AsString())
	}
	assert.Equal(t, []string{
		"get_quote", "get_kline", "list_flash", "search_flash",
		"list_news", "search_news", "get_news", "list_calendar",
	}, tools)
	resources := literalAttributeValue(t, server, "resources")
	assert.Equal(t, cty.StringVal("quote://codes"), resources.Index(cty.NumberIntVal(0)))

	timeout, err := time.ParseDuration(literalAttributeValue(t, server, "timeout").AsString())
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, timeout)

	require.Len(t, server.Body.Blocks, 1)
	httpBlock := server.Body.Blocks[0]
	assert.Equal(t, "http", httpBlock.Type)
	assert.Equal(t, cty.StringVal("https://mcp.jin10.com/mcp"), literalAttributeValue(t, httpBlock, "url"))
	ref, diagnostics := httpBlock.Body.Attributes["bearer_token_ref"].Expr.Value(&hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var": cty.ObjectVal(map[string]cty.Value{
				"jin10_mcp_token_ref": cty.StringVal("J10_API_KEY"),
				"jin10_mcp_token":     cty.NullVal(cty.String),
			}),
		},
	})
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	assert.Equal(t, cty.StringVal("J10_API_KEY"), ref)
}

func literalAttributeValue(t *testing.T, block *hclsyntax.Block, name string) cty.Value {
	t.Helper()
	attribute, exists := block.Body.Attributes[name]
	require.True(t, exists)
	value, diagnostics := attribute.Expr.Value(nil)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	return value
}
