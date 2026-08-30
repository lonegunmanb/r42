package spec_test

import (
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/mcp"
	mcpspec "github.com/lonegunmanb/r42/internal/mcp/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type mcpTestConfig struct {
	*golden.BaseConfig
}

var registerMCPServerBlock sync.Once

//nolint:paralleltest // Golden's block registry is process-global.
func TestServerBlockPlansHTTPServerAndMCPToolIDs(t *testing.T) {
	registerMCPServerBlock.Do(func() { golden.RegisterBlock(new(mcpspec.ServerBlock)) })
	config := parseMCPConfig(t, `
mcp_server "jin10" {
  tools   = ["get_quote", "get_kline"]
  resources = ["quote://codes"]
  timeout = "30s"

  http {
    url = "https://mcp.jin10.com/mcp"
    headers = {
      "X-Tenant" = "research"
    }
    bearer_token_ref = "J10_API_KEY"
  }
}

`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*mcpspec.ServerBlock](config)
	require.Len(t, blocks, 1)
	block := blocks[0]
	planned := block.ServerConfig()

	assert.Equal(t, "jin10", planned.Name)
	assert.Equal(t, "mcp_server.jin10", planned.RuntimeName)
	assert.Equal(t, mcp.TransportHTTP, planned.Transport)
	assert.Equal(t, []string{"get_quote", "get_kline"}, planned.Tools)
	assert.Equal(t, []string{"quote://codes"}, planned.Resources)
	assert.Equal(t, 30*time.Second, planned.Timeout)
	require.NotNil(t, planned.HTTP)
	assert.Equal(t, "https://mcp.jin10.com/mcp", planned.HTTP.URL)
	assert.Equal(t, map[string]string{"X-Tenant": "research"}, planned.HTTP.Headers)
	require.NotNil(t, planned.HTTP.BearerTokenRef)
	assert.Equal(t, "J10_API_KEY", *planned.HTTP.BearerTokenRef)

	value := block.Value()
	assert.Equal(t, "mcp_server.jin10", value.GetAttr("address").AsString())
	toolIDs := value.GetAttr("tool_ids")
	quoteID := toolIDs.Index(cty.StringVal("get_quote")).AsString()
	klineID := toolIDs.Index(cty.StringVal("get_kline")).AsString()
	assert.True(t, plan.IsMCPToolID(quoteID))
	assert.True(t, plan.IsMCPToolID(klineID))
	assert.False(t, plan.IsToolID(quoteID))
	assert.NotEqual(t, quoteID, klineID)
	assert.Equal(t, quoteID, block.ToolID("get_quote"), "mcp tool ids must be deterministic")
	resourceIDs := value.GetAttr("resource_ids")
	codesID := resourceIDs.Index(cty.StringVal("quote://codes")).AsString()
	assert.True(t, plan.IsMCPResourceID(codesID))
	assert.False(t, plan.IsMCPToolID(codesID))
	assert.Equal(t, codesID, block.ResourceID("quote://codes"), "mcp resource ids must be deterministic")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestServerBlockPlansLiteralBearerToken(t *testing.T) {
	registerMCPServerBlock.Do(func() { golden.RegisterBlock(new(mcpspec.ServerBlock)) })
	config := parseMCPConfig(t, `
mcp_server "jin10" {
  tools = ["get_quote"]
  http {
    url = "https://mcp.jin10.com/mcp"
    bearer_token = "literal-secret"
  }
}
`)

	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*mcpspec.ServerBlock](config)[0].ServerConfig()
	require.NotNil(t, planned.HTTP)
	require.NotNil(t, planned.HTTP.BearerToken)
	assert.Equal(t, "literal-secret", *planned.HTTP.BearerToken)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestServerBlockPlansStdioServer(t *testing.T) {
	registerMCPServerBlock.Do(func() { golden.RegisterBlock(new(mcpspec.ServerBlock)) })
	config := parseMCPConfig(t, `
mcp_server "local" {
  tools = ["query"]

  stdio {
    command           = "uvx"
    args              = ["example-mcp"]
    env               = { LOG_LEVEL = "warning" }
    env_refs          = { DATABASE_TOKEN = "DATABASE_TOKEN" }
    working_directory = "./mcp"
  }
}
`)

	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*mcpspec.ServerBlock](config)[0].ServerConfig()
	assert.Equal(t, mcp.TransportStdio, planned.Transport)
	require.NotNil(t, planned.Stdio)
	assert.Equal(t, "uvx", planned.Stdio.Command)
	assert.Equal(t, []string{"example-mcp"}, planned.Stdio.Args)
	assert.Equal(t, map[string]string{"LOG_LEVEL": "warning"}, planned.Stdio.Env)
	assert.Equal(t, map[string]string{"DATABASE_TOKEN": "DATABASE_TOKEN"}, planned.Stdio.EnvRefs)
	assert.Equal(t, "./mcp", planned.Stdio.WorkingDirectory)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestServerBlockRejectsInvalidConfiguration(t *testing.T) {
	registerMCPServerBlock.Do(func() { golden.RegisterBlock(new(mcpspec.ServerBlock)) })
	tests := []struct {
		name          string
		body          string
		expectedError string
	}{
		{name: "missing tools", body: `http { url = "https://example.test/mcp" }`, expectedError: "mcp server tools must be a non-empty explicit list"},
		{name: "wildcard tool", body: `tools = ["*"]
http { url = "https://example.test/mcp" }`, expectedError: "mcp server tools must not contain wildcard"},
		{name: "duplicate tool", body: `tools = ["query", "query"]
http { url = "https://example.test/mcp" }`, expectedError: `mcp server tool "query" is declared more than once`},
		{name: "empty resource", body: `tools = ["query"]
resources = [" "]
http { url = "https://example.test/mcp" }`, expectedError: "mcp server resources must not contain empty values"},
		{name: "duplicate resource", body: `tools = ["query"]
resources = ["quote://codes", "quote://codes"]
http { url = "https://example.test/mcp" }`, expectedError: `mcp server resource "quote://codes" is declared more than once`},
		{name: "both transports", body: `tools = ["query"]
http { url = "https://example.test/mcp" }
stdio { command = "server" }`, expectedError: "mcp server must have exactly one http or stdio block"},
		{name: "insecure remote http", body: `tools = ["query"]
http { url = "http://example.test/mcp" }`, expectedError: "mcp http url must use https unless it targets loopback"},
		{name: "literal authorization header", body: `tools = ["query"]
http {
  url = "https://example.test/mcp"
  headers = { Authorization = "secret" }
}`, expectedError: "mcp http headers must not contain authorization"},
		{name: "empty bearer ref", body: `tools = ["query"]
http {
  url = "https://example.test/mcp"
  bearer_token_ref = " "
}`, expectedError: "mcp http bearer_token_ref must not be empty"},
		{name: "zero timeout", body: `tools = ["query"]
timeout = "0s"
http { url = "https://example.test/mcp" }`, expectedError: "mcp server timeout must be between 1s and 5m"},
		{name: "stdio missing command", body: `tools = ["query"]
stdio {}`, expectedError: "mcp stdio command is required"},
		{name: "stdio duplicate env destination", body: `tools = ["query"]
stdio {
  command = "server"
  env = { TOKEN = "value" }
  env_refs = { TOKEN = "TOKEN" }
}`, expectedError: `mcp stdio environment variable "TOKEN" is configured by both env and env_refs`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseMCPConfig(t, "mcp_server \"invalid\" {\n"+tt.body+"\n}\n")
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func parseMCPConfig(t *testing.T, source string) *mcpTestConfig {
	t.Helper()

	syntaxFile, diagnostics := hclsyntax.ParseConfig([]byte(source), "mcp.r42.hcl", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	writeFile, diagnostics := hclwrite.ParseConfig([]byte(source), "mcp.r42.hcl", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := syntaxFile.Body.(*hclsyntax.Body)
	require.True(t, ok)

	config := &mcpTestConfig{BaseConfig: golden.NewBasicConfig("", "r42", "r42", nil, nil, nil)}
	require.NoError(t, golden.InitConfig(config, golden.AsHclBlocks(body.Blocks, writeFile.Body().Blocks())))
	return config
}
