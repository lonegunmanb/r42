package spec

import (
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMCPServersKeepsSameNameServersWithDifferentRuntimeNamesSeparate(t *testing.T) {
	t.Parallel()

	firstID := "mcp_resource_market__codes_12345678-1234-8234-9234-123456789abc"
	secondID := "mcp_resource_other__codes_12345678-1234-8234-9234-123456789abc"
	firstToolID := "mcp_tool_market__quote_12345678-1234-8234-9234-123456789abc"
	secondToolID := "mcp_tool_other__quote_12345678-1234-8234-9234-123456789abc"
	firstServer := mcp.Config{
		Name: "market", RuntimeName: "module.first.mcp_server.market", Transport: mcp.TransportHTTP, Timeout: 30 * time.Second,
		HTTP: &mcp.HTTPConfig{URL: "https://first.example.test/mcp"},
	}
	secondServer := mcp.Config{
		Name: "market", RuntimeName: "module.second.mcp_server.market", Transport: mcp.TransportHTTP, Timeout: 30 * time.Second,
		HTTP: &mcp.HTTPConfig{URL: "https://second.example.test/mcp"},
	}
	registry := mcp.ResourceRegistry{
		firstID: {
			ID: firstID, URI: "quote://codes", Server: firstServer,
		},
		secondID: {
			ID: secondID, URI: "quote://codes", Server: secondServer,
		},
	}
	toolRegistry := mcp.ToolRegistry{
		firstToolID:  {ID: firstToolID, Name: "quote", Server: firstServer},
		secondToolID: {ID: secondToolID, Name: "quote", Server: secondServer},
	}

	servers, err := resolveMCPServers(
		[]string{firstToolID, secondToolID}, []string{firstID, secondID}, toolRegistry, registry,
	)

	require.NoError(t, err)
	require.Len(t, servers, 2)
	assert.Equal(t, "module.first.mcp_server.market", servers[0].RuntimeName)
	assert.Equal(t, "module.second.mcp_server.market", servers[1].RuntimeName)
	assert.Equal(t, []string{"quote"}, servers[0].Tools)
	assert.Equal(t, []string{"quote"}, servers[1].Tools)
	assert.Equal(t, []string{"quote://codes"}, servers[0].Resources)
	assert.Equal(t, []string{"quote://codes"}, servers[1].Resources)
}

func TestMCPRegistriesDetectLiteralBearerTokensAsSensitive(t *testing.T) {
	t.Parallel()

	token := "literal-secret"
	server := mcp.Config{
		Name: "jin10", RuntimeName: "mcp_server.jin10", Transport: mcp.TransportHTTP,
		Tools: []string{"get_quote"}, Resources: []string{"quote://codes"}, Timeout: 30 * time.Second,
		HTTP: &mcp.HTTPConfig{URL: "https://mcp.jin10.com/mcp", BearerToken: &token},
	}
	tools := mcp.ToolRegistry{"tool-id": {ID: "tool-id", Name: "get_quote", Server: server}}
	resources := mcp.ResourceRegistry{"resource-id": {ID: "resource-id", URI: "quote://codes", Server: server}}

	assert.True(t, mcpToolRegistrySensitive(tools))
	assert.True(t, mcpResourceRegistrySensitive(resources))
}
