package mcp_test

import (
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        mcp.Config
		expectedError string
	}{
		{name: "http", config: validHTTPConfig()},
		{name: "loopback http", config: mcp.Config{Name: "local", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "http://127.0.0.1:8080/mcp"}}},
		{name: "stdio", config: mcp.Config{Name: "local", Transport: mcp.TransportStdio, Tools: []string{"query"}, Timeout: time.Minute, Stdio: &mcp.StdioConfig{Command: "server"}}},
		{name: "missing name", config: mcp.Config{Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "https://example.test/mcp"}}, expectedError: "mcp server name is required"},
		{name: "empty tool", config: mcp.Config{Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{" "}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "https://example.test/mcp"}}, expectedError: "mcp server tools must not contain empty values"},
		{name: "empty resource", config: mcp.Config{Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Resources: []string{" "}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "https://example.test/mcp"}}, expectedError: "mcp server resources must not contain empty values"},
		{name: "timeout too high", config: mcp.Config{Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: 6 * time.Minute, HTTP: &mcp.HTTPConfig{URL: "https://example.test/mcp"}}, expectedError: "mcp server timeout must be between 1s and 5m"},
		{name: "url user info", config: mcp.Config{Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "https://user@example.test/mcp"}}, expectedError: "mcp http url must not contain user information"},
		{name: "remote plain http", config: mcp.Config{Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "http://example.test/mcp"}}, expectedError: "mcp http url must use https unless it targets loopback"},
		{name: "header newline", config: mcp.Config{Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: time.Second, HTTP: &mcp.HTTPConfig{URL: "https://example.test/mcp", Headers: map[string]string{"X-Test": "bad\nvalue"}}}, expectedError: "mcp http headers must not contain empty names or newlines"},
		{name: "empty env ref", config: mcp.Config{Name: "invalid", Transport: mcp.TransportStdio, Tools: []string{"query"}, Timeout: time.Second, Stdio: &mcp.StdioConfig{Command: "server", EnvRefs: map[string]string{"TOKEN": " "}}}, expectedError: "mcp stdio env_refs must not contain empty names or references"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			if tt.expectedError == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestConfigValidateRejectsBothHTTPBearerTokenForms(t *testing.T) {
	t.Parallel()

	token := "literal-secret"
	ref := "HTTP_TOKEN"
	config := validHTTPConfig()
	config.HTTP.BearerToken = &token
	config.HTTP.BearerTokenRef = &ref

	err := config.Validate()

	require.EqualError(t, err, "mcp http bearer_token and bearer_token_ref are mutually exclusive")
}

func TestConfigMaterializeClonesAndResolvesEnvironmentReferences(t *testing.T) {
	t.Parallel()

	httpTokenRef := "HTTP_TOKEN"
	httpConfig := validHTTPConfig()
	httpConfig.HTTP.BearerTokenRef = &httpTokenRef
	stdioConfig := mcp.Config{
		Name: "local", Transport: mcp.TransportStdio, Tools: []string{"query"}, Timeout: time.Minute,
		Stdio: &mcp.StdioConfig{
			Command: "server", Args: []string{"--stdio"}, Env: map[string]string{"MODE": "test"},
			EnvRefs: map[string]string{"TOKEN": "STDIO_TOKEN"},
		},
	}
	lookup := func(name string) (string, bool) {
		value, ok := map[string]string{"HTTP_TOKEN": "http-secret", "STDIO_TOKEN": "stdio-secret"}[name]
		return value, ok
	}

	materializedHTTP, err := httpConfig.Materialize(lookup)
	require.NoError(t, err)
	materializedStdio, err := stdioConfig.Materialize(lookup)
	require.NoError(t, err)

	assert.Equal(t, "Bearer http-secret", materializedHTTP.HTTP.Headers["Authorization"])
	assert.NotContains(t, httpConfig.HTTP.Headers, "Authorization")
	assert.Equal(t, "stdio-secret", materializedStdio.Stdio.Env["TOKEN"])
	assert.NotContains(t, stdioConfig.Stdio.Env, "TOKEN")
	materializedHTTP.Tools[0] = "changed"
	materializedHTTP.Resources[0] = "changed"
	materializedStdio.Stdio.Args[0] = "changed"
	assert.Equal(t, []string{"get_quote"}, httpConfig.Tools)
	assert.Equal(t, []string{"quote://codes"}, httpConfig.Resources)
	assert.Equal(t, []string{"--stdio"}, stdioConfig.Stdio.Args)
}

func TestConfigMaterializeUsesLiteralBearerToken(t *testing.T) {
	t.Parallel()

	token := "literal-secret"
	config := validHTTPConfig()
	config.HTTP.BearerToken = &token

	materialized, err := config.Materialize(func(string) (string, bool) {
		return "unexpected-environment-token", true
	})

	require.NoError(t, err)
	require.NotNil(t, materialized.HTTP)
	assert.Equal(t, "Bearer literal-secret", materialized.HTTP.Headers["Authorization"])
	assert.NotContains(t, config.HTTP.Headers, "Authorization")
}

func TestConfigMaterializeRejectsMissingStdioEnvironmentReference(t *testing.T) {
	t.Parallel()

	config := mcp.Config{
		Name: "local", Transport: mcp.TransportStdio, Tools: []string{"query"}, Timeout: time.Minute,
		Stdio: &mcp.StdioConfig{Command: "server", EnvRefs: map[string]string{"TOKEN": "MISSING_TOKEN"}},
	}

	_, err := config.Materialize(func(string) (string, bool) { return "", false })

	require.EqualError(t, err, `mcp server "local" environment variable "MISSING_TOKEN" is not set`)
}

func TestConfigValidateSelectionRequiresSelectedCapability(t *testing.T) {
	t.Parallel()

	config := validHTTPConfig()
	config.Tools = []string{}
	config.Resources = []string{}

	err := config.ValidateSelection()

	require.EqualError(t, err, "mcp server selection must contain a tool or resource")
}

func validHTTPConfig() mcp.Config {
	return mcp.Config{
		Name: "jin10", Transport: mcp.TransportHTTP, Tools: []string{"get_quote"}, Resources: []string{"quote://codes"}, Timeout: 30 * time.Second,
		HTTP: &mcp.HTTPConfig{URL: "https://mcp.jin10.com/mcp", Headers: map[string]string{"X-Test": "value"}},
	}
}
