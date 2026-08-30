package copilot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/mcp"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestFactoryOpenMaterializesProviderAndAssemblesSession(t *testing.T) {
	t.Parallel()

	apiKeyRef := "MODEL_API_KEY"
	wireAPI := provider.WireAPIResponses
	transport := provider.TransportWebSockets
	client := &fakeClient{}
	factory := newFactory(client, func(name string) (string, bool) {
		assert.Equal(t, apiKeyRef, name)
		return "secret-at-apply", true
	}, noDelay, fixedRandom)
	tool := sdk.Tool{Name: "go_tool_finish", Description: "finish"}

	session, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type:      provider.TypeOpenAI,
			Endpoint:  "https://models.example.test",
			WireAPI:   &wireAPI,
			Transport: &transport,
			Headers:   providerHeaders(map[string]string{"X-R42": "test"}),
			APIKeyRef: &apiKeyRef,
		},
		Retry:            retryPolicy(t, 2, 3),
		Model:            "gpt-5.6-sol",
		Profile:          "gpt-5.4",
		ReasoningEffort:  "max",
		SystemPrompt:     "r42 protocol\nauthor instructions",
		WorkingDirectory: "D:/run/research.market",
		Tools:            []sdk.Tool{tool},
		AvailableTools:   []string{"custom:go_tool_finish", "builtin:view"},
		ExcludedTools:    []string{"builtin:ask_user"},
		SkillDirectories: []string{"D:/skills"},
		Skills:           []string{"source-evaluation"},
		DisabledSkills:   []string{"unsafe-skill"},
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Len(t, client.configs, 1)

	config := client.configs[0]
	assert.Equal(t, "gpt-5.6-sol", config.Model)
	assert.Equal(t, "max", config.ReasoningEffort)
	require.NotNil(t, config.Streaming)
	assert.True(t, *config.Streaming)
	require.NotNil(t, config.EnableSessionStore)
	assert.False(t, *config.EnableSessionStore)
	require.NotNil(t, config.SkipEmbeddingRetrieval)
	assert.True(t, *config.SkipEmbeddingRetrieval)
	require.NotNil(t, config.LargeOutput)
	require.NotNil(t, config.LargeOutput.Enabled)
	assert.False(t, *config.LargeOutput.Enabled)
	assert.Equal(t, "D:/run/research.market", config.WorkingDirectory)
	require.NotNil(t, config.SystemMessage)
	assert.Equal(t, "append", config.SystemMessage.Mode)
	assert.Equal(t, "r42 protocol\nauthor instructions", config.SystemMessage.Content)
	assert.Equal(t, []sdk.Tool{tool}, config.Tools)
	assert.Equal(t, []string{"custom:go_tool_finish", "builtin:view"}, config.AvailableTools)
	assert.Equal(t, []string{"builtin:ask_user"}, config.ExcludedTools)
	assert.NotNil(t, config.OnPermissionRequest)
	assert.Equal(t, sdk.Bool(true), config.EnableSkills)
	assert.Equal(t, []string{"D:/skills"}, config.SkillDirectories)
	assert.Equal(t, []string{"unsafe-skill"}, config.DisabledSkills)
	assert.Equal(t, "r42_research", config.Agent)
	require.Len(t, config.CustomAgents, 1)
	assert.Equal(t, sdk.CustomAgentConfig{
		Name:            "r42_research",
		Prompt:          "r42 protocol\nauthor instructions",
		Skills:          []string{"source-evaluation"},
		Model:           "gpt-5.4",
		ReasoningEffort: "max",
	}, config.CustomAgents[0])
	require.NotNil(t, config.Provider)
	assert.Equal(t, "openai", config.Provider.Type)
	assert.Equal(t, "https://models.example.test", config.Provider.BaseURL)
	assert.Equal(t, "responses", config.Provider.WireAPI)
	assert.Equal(t, "websockets", config.Provider.Transport)
	assert.Equal(t, "secret-at-apply", config.Provider.APIKey)
	assert.Equal(t, map[string]string{"X-R42": "test"}, config.Provider.Headers)
	assert.Equal(t, "gpt-5.4", config.Provider.ModelID)
	assert.Equal(t, "gpt-5.6-sol", config.Provider.WireModel)
}

func TestFactoryOpenSupportsDefaultProviderAndNoSelectedSkills(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	assert.Nil(t, client.configs[0].Provider)
	assert.Empty(t, client.configs[0].CustomAgents)
	assert.Empty(t, client.configs[0].Agent)
}

func TestFactoryOpenForwardsSessionHooks(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	hooks := &sdk.SessionHooks{
		OnPreToolUse: func(sdk.PreToolUseHookInput, sdk.HookInvocation) (*sdk.PreToolUseHookOutput, error) {
			return &sdk.PreToolUseHookOutput{PermissionDecision: "allow"}, nil
		},
	}

	_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0), Hooks: hooks})

	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	assert.Same(t, hooks, client.configs[0].Hooks)
}

func TestFactoryOpenMountsAuthorizedMCPResourceReader(t *testing.T) {
	t.Parallel()

	resourceID := "mcp_resource_jin10__quote_codes_12345678-1234-8234-9234-123456789abc"
	underlying := &fakeMCPResourceSession{contents: []mcp.ResourceContent{{
		URI: "quote://codes", MIMEType: sdk.String("application/json"), Text: sdk.String(`{"codes":["USDCNH"]}`),
	}}}
	client := &fakeClient{session: underlying}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Retry:          retryPolicy(t, 0, 0),
		AvailableTools: []string{"web_search"}, ExcludedTools: []string{MCPResourceReadToolName, "ask_user"},
		MCPResources: []mcp.Resource{{
			ID: resourceID, URI: "quote://codes",
			Server: mcp.Config{Name: "jin10", RuntimeName: "module.market.mcp_server.jin10"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	assert.Contains(t, client.configs[0].AvailableTools, MCPResourceReadToolName)
	assert.NotContains(t, client.configs[0].ExcludedTools, MCPResourceReadToolName)
	assert.Contains(t, client.configs[0].ExcludedTools, "ask_user")
	reader := sdkToolByName(t, client.configs[0].Tools, MCPResourceReadToolName)

	result, err := reader.Handler(sdk.ToolInvocation{Arguments: map[string]any{"resource_id": resourceID}})

	require.NoError(t, err)
	assert.Equal(t, "success", result.ResultType)
	assert.JSONEq(t, `{"resource_id":"`+resourceID+`","contents":[{"uri":"quote://codes","mimeType":"application/json","text":"{\"codes\":[\"USDCNH\"]}"}]}`, result.TextResultForLLM)
	assert.Equal(t, []mcp.ResourceReadRequest{{ServerName: "module.market.mcp_server.jin10", URI: "quote://codes"}}, underlying.reads)
}

func TestMCPResourceReaderRejectsResourceOutsideSessionAuthorization(t *testing.T) {
	t.Parallel()

	underlying := &fakeMCPResourceSession{}
	client := &fakeClient{session: underlying}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	_, err := factory.Open(t.Context(), SessionConfig{
		Retry: retryPolicy(t, 0, 0),
		MCPResources: []mcp.Resource{{
			ID:  "mcp_resource_jin10__quote_codes_12345678-1234-8234-9234-123456789abc",
			URI: "quote://codes", Server: mcp.Config{Name: "jin10"},
		}},
	})
	require.NoError(t, err)
	reader := sdkToolByName(t, client.configs[0].Tools, MCPResourceReadToolName)

	result, err := reader.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"resource_id": "mcp_resource_other__secret_12345678-1234-8234-9234-123456789abc",
	}})

	require.NoError(t, err)
	assert.Equal(t, "success", result.ResultType)
	assert.Contains(t, result.TextResultForLLM, "resource_not_authorized")
	assert.Empty(t, underlying.reads)
}

func TestFactoryOpenDoesNotMountMCPResourceReaderWithoutResources(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0), AvailableTools: []string{"web_search"}})

	require.NoError(t, err)
	assert.NotContains(t, client.configs[0].AvailableTools, MCPResourceReadToolName)
	for _, tool := range client.configs[0].Tools {
		assert.NotEqual(t, MCPResourceReadToolName, tool.Name)
	}
}

func TestMCPResourceReaderSchemaEnumeratesAuthorizedResourceIDs(t *testing.T) {
	t.Parallel()

	resourceIDs := []string{"resource-a", "resource-b"}
	tool := mcpResourceReadTool(newMCPResourceReaderHolder(nil), resourceIDs)

	properties, ok := tool.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	resourceID, ok := properties["resource_id"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, resourceIDs, resourceID["enum"])
}

func TestFactoryOpenSupportsResourceOnlyMCPServerSelection(t *testing.T) {
	t.Parallel()

	client := &fakeClient{session: &fakeMCPResourceSession{}}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	resource := mcp.Resource{
		ID:  "mcp_resource_jin10__quote_codes_12345678-1234-8234-9234-123456789abc",
		URI: "quote://codes", Server: mcp.Config{Name: "jin10"},
	}

	_, err := factory.Open(t.Context(), SessionConfig{
		Retry: retryPolicy(t, 0, 0), MCPResources: []mcp.Resource{resource},
		MCPServers: []mcp.Config{{
			Name: "jin10", Transport: mcp.TransportHTTP, Tools: []string{}, Resources: []string{"quote://codes"},
			Timeout: 30 * time.Second, HTTP: &mcp.HTTPConfig{URL: "https://mcp.jin10.com/mcp"},
		}},
	})

	require.NoError(t, err)
	server, ok := client.configs[0].MCPServers["jin10"].(sdk.MCPHTTPServerConfig)
	require.True(t, ok)
	assert.NotNil(t, server.Tools)
	assert.Empty(t, server.Tools)
}

func TestFactoryOpenNormalizesNilToolsForResourceOnlyMCPServer(t *testing.T) {
	t.Parallel()

	client := &fakeClient{session: &fakeMCPResourceSession{}}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Retry: retryPolicy(t, 0, 0),
		MCPServers: []mcp.Config{{
			Name: "jin10", Transport: mcp.TransportHTTP, Resources: []string{"quote://codes"},
			Timeout: 30 * time.Second, HTTP: &mcp.HTTPConfig{URL: "https://mcp.jin10.com/mcp"},
		}},
	})

	require.NoError(t, err)
	server, ok := client.configs[0].MCPServers["jin10"].(sdk.MCPHTTPServerConfig)
	require.True(t, ok)
	assert.NotNil(t, server.Tools)
	assert.Empty(t, server.Tools)
}

func TestFactoryOpenMapsNativeMCPServers(t *testing.T) {
	t.Parallel()

	httpTokenRef := "J10_API_KEY"
	stdioTokenRef := "LOCAL_MCP_TOKEN"
	client := &fakeClient{}
	factory := newFactory(client, func(name string) (string, bool) {
		return map[string]string{
			"J10_API_KEY":     "j10-secret",
			"LOCAL_MCP_TOKEN": "local-secret",
		}[name], true
	}, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Retry: retryPolicy(t, 0, 0),
		MCPServers: []mcp.Config{
			{
				Name: "jin10", Transport: mcp.TransportHTTP, Tools: []string{"get_quote", "get_kline"}, Timeout: 30 * time.Second,
				HTTP: &mcp.HTTPConfig{
					URL: "https://mcp.jin10.com/mcp", Headers: map[string]string{"X-Tenant": "research"}, BearerTokenRef: &httpTokenRef,
				},
			},
			{
				Name: "local", Transport: mcp.TransportStdio, Tools: []string{"query"}, Timeout: 2 * time.Minute,
				Stdio: &mcp.StdioConfig{
					Command: "uvx", Args: []string{"example-mcp"}, Env: map[string]string{"LOG_LEVEL": "warning"},
					EnvRefs: map[string]string{"TOKEN": stdioTokenRef}, WorkingDirectory: "D:/mcp",
				},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	httpConfig, ok := client.configs[0].MCPServers["jin10"].(sdk.MCPHTTPServerConfig)
	require.True(t, ok)
	assert.Equal(t, "https://mcp.jin10.com/mcp", httpConfig.URL)
	assert.Equal(t, map[string]string{"X-Tenant": "research", "Authorization": "Bearer j10-secret"}, httpConfig.Headers)
	assert.Equal(t, []string{"get_quote", "get_kline"}, httpConfig.Tools)
	assert.Equal(t, 30_000, httpConfig.Timeout)
	stdioConfig, ok := client.configs[0].MCPServers["local"].(sdk.MCPStdioServerConfig)
	require.True(t, ok)
	assert.Equal(t, "uvx", stdioConfig.Command)
	assert.Equal(t, []string{"example-mcp"}, stdioConfig.Args)
	assert.Equal(t, map[string]string{"LOG_LEVEL": "warning", "TOKEN": "local-secret"}, stdioConfig.Env)
	assert.Equal(t, "D:/mcp", stdioConfig.WorkingDirectory)
	assert.Equal(t, []string{"query"}, stdioConfig.Tools)
	assert.Equal(t, 120_000, stdioConfig.Timeout)
}

func TestFactoryOpenRejectsMissingMCPEnvironmentValueBeforeSDKCall(t *testing.T) {
	t.Parallel()

	tokenRef := "MISSING_MCP_TOKEN"
	client := &fakeClient{}
	factory := newFactory(client, func(string) (string, bool) { return "", false }, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Retry: retryPolicy(t, 0, 0),
		MCPServers: []mcp.Config{{
			Name: "jin10", Transport: mcp.TransportHTTP, Tools: []string{"get_quote"}, Timeout: 30 * time.Second,
			HTTP: &mcp.HTTPConfig{URL: "https://mcp.jin10.com/mcp", BearerTokenRef: &tokenRef},
		}},
	})

	require.EqualError(t, err, `materialize mcp server: mcp server "jin10" bearer token environment variable "MISSING_MCP_TOKEN" is not set`)
	assert.Empty(t, client.configs)
}

func TestFactoryOpenRejectsInvalidMCPConfigBeforeSDKCall(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Retry: retryPolicy(t, 0, 0),
		MCPServers: []mcp.Config{{
			Name: "invalid", Transport: mcp.TransportHTTP, Tools: []string{"query"}, Timeout: 30 * time.Second,
		}},
	})

	require.EqualError(t, err, "validate mcp server: mcp server must have exactly one http or stdio block")
	assert.Empty(t, client.configs)
}

func TestCloneResumeSessionConfigDeepCopiesMCPServers(t *testing.T) {
	t.Parallel()

	source := &sdk.ResumeSessionConfig{MCPServers: map[string]sdk.MCPServerConfig{
		"http":  sdk.MCPHTTPServerConfig{Headers: map[string]string{"X-Test": "original"}, Tools: []string{"quote"}},
		"stdio": sdk.MCPStdioServerConfig{Args: []string{"original"}, Env: map[string]string{"TOKEN": "original"}, Tools: []string{"query"}},
	}}

	cloned := cloneResumeSessionConfig(source)
	httpConfig, ok := cloned.MCPServers["http"].(sdk.MCPHTTPServerConfig)
	require.True(t, ok)
	httpConfig.Headers["X-Test"] = "changed"
	httpConfig.Tools[0] = "changed"
	stdioConfig, ok := cloned.MCPServers["stdio"].(sdk.MCPStdioServerConfig)
	require.True(t, ok)
	stdioConfig.Args[0] = "changed"
	stdioConfig.Env["TOKEN"] = "changed"
	stdioConfig.Tools[0] = "changed"

	sourceHTTP, ok := source.MCPServers["http"].(sdk.MCPHTTPServerConfig)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"X-Test": "original"}, sourceHTTP.Headers)
	assert.Equal(t, []string{"quote"}, sourceHTTP.Tools)
	sourceStdio, ok := source.MCPServers["stdio"].(sdk.MCPStdioServerConfig)
	require.True(t, ok)
	assert.Equal(t, []string{"original"}, sourceStdio.Args)
	assert.Equal(t, map[string]string{"TOKEN": "original"}, sourceStdio.Env)
	assert.Equal(t, []string{"query"}, sourceStdio.Tools)
}

func TestFactoryOpenRejectsMissingProviderEnvironmentValueBeforeSDKCall(t *testing.T) {
	t.Parallel()

	apiKeyRef := "MISSING_KEY"
	client := &fakeClient{}
	factory := newFactory(client, func(string) (string, bool) { return "", false }, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type:      provider.TypeOpenAI,
			Endpoint:  "https://models.example.test",
			APIKeyRef: &apiKeyRef,
		},
		Retry: retryPolicy(t, 2, 0),
	})

	require.EqualError(t, err, `materialize provider: environment variable "MISSING_KEY" is not set or empty`)
	assert.Empty(t, client.configs)
}

func TestFactoryOpenMapsBearerToken(t *testing.T) {
	t.Parallel()

	bearerToken := "bearer-secret"
	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type:        provider.TypeAnthropic,
			Endpoint:    "https://anthropic.example.test",
			Headers:     cty.NilVal,
			BearerToken: &bearerToken,
		},
		Retry: retryPolicy(t, 0, 0),
	})

	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	require.NotNil(t, client.configs[0].Provider)
	assert.Equal(t, "anthropic", client.configs[0].Provider.Type)
	assert.Equal(t, "bearer-secret", client.configs[0].Provider.BearerToken)
	assert.Empty(t, client.configs[0].Provider.WireAPI)
	assert.Empty(t, client.configs[0].Provider.Transport)
}

func TestFactoryOpenRetriesTransientLifecycleFailure(t *testing.T) {
	t.Parallel()

	created := &fakeSession{}
	client := &fakeClient{
		createErrors: []error{
			transientError{Err: errors.New("runtime unavailable")},
			transientError{Err: errors.New("runtime still unavailable")},
		},
		session: created,
	}
	var delays []time.Duration
	factory := newFactory(client, nil, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}, fixedRandom)

	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 2, 0)})

	require.NoError(t, err)
	assert.Same(t, created, session.sdk)
	assert.Len(t, client.configs, 3)
	assert.Len(t, delays, 2)
	require.NotEmpty(t, client.configs[0].SessionID)
	for _, config := range client.configs[1:] {
		assert.Equal(t, client.configs[0].SessionID, config.SessionID)
	}
}

func TestFactoryOpenFailsPermanentLifecycleErrorImmediately(t *testing.T) {
	t.Parallel()

	client := &fakeClient{createErrors: []error{
		httpError{StatusCode: 400, Err: errors.New("invalid model")},
	}}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 10, 0)})

	require.ErrorContains(t, err, "create copilot session: http status 400")
	assert.Len(t, client.configs, 1)
}

func TestSessionSendAndWaitRetriesSameSession(t *testing.T) {
	t.Parallel()

	want := &sdk.SessionEvent{ID: "completed"}
	underlying := &fakeSession{
		sendErrors: []error{
			transientError{Err: errors.New("model overloaded")},
			transientError{Err: errors.New("model overloaded again")},
		},
		event: want,
	}
	client := &fakeClient{session: underlying}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 2)})
	require.NoError(t, err)

	got, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Len(t, underlying.messages, 3)
	assert.Len(t, client.configs, 1, "model retry must not replace the session")
}

func TestSessionAbortForwardsToCurrentSDKSession(t *testing.T) {
	t.Parallel()

	underlying := &fakeSession{}
	factory := newFactory(&fakeClient{session: underlying}, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)

	err = session.Abort(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, underlying.abortCalls)
}

func TestSessionResumeDisconnectsAndRestoresSameConversation(t *testing.T) {
	t.Parallel()

	original := &fakeSession{}
	resumed := &fakeSession{event: &sdk.SessionEvent{ID: "resumed-result"}}
	client := &fakeClient{session: original, resumedSession: resumed}
	factory := newFactory(client, func(name string) (string, bool) {
		if name == "J10_API_KEY" {
			return "j10-secret", true
		}
		return "", false
	}, noDelay, fixedRandom)
	hooks := &sdk.SessionHooks{}
	bearerToken := "provider-secret"
	mcpBearerTokenRef := "J10_API_KEY"
	session, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type: provider.TypeAnthropic, Endpoint: "https://models.example.test", BearerToken: &bearerToken,
		},
		Retry:            retryPolicy(t, 0, 0),
		Model:            "research-model",
		Profile:          "research-profile",
		ReasoningEffort:  "high",
		SystemPrompt:     "research carefully",
		WorkingDirectory: "D:/work",
		Tools:            []sdk.Tool{{Name: "finish"}},
		AvailableTools:   []string{"finish"},
		SkillDirectories: []string{"D:/skills"},
		Skills:           []string{"citation"},
		DisabledSkills:   []string{"unused"},
		Hooks:            hooks,
		MCPServers: []mcp.Config{{
			Name: "jin10", Transport: mcp.TransportHTTP, Tools: []string{"get_quote"}, Timeout: 30 * time.Second,
			HTTP: &mcp.HTTPConfig{URL: "https://mcp.jin10.com/mcp", BearerTokenRef: &mcpBearerTokenRef},
		}},
	})
	require.NoError(t, err)
	require.Len(t, client.configs, 1)

	err = session.Resume(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, original.disconnectCalls)
	require.Len(t, client.resumeIDs, 1)
	assert.Equal(t, client.configs[0].SessionID, client.resumeIDs[0])
	require.Len(t, client.resumeConfigs, 1)
	resume := client.resumeConfigs[0]
	require.NotNil(t, resume.ContinuePendingWork)
	assert.False(t, *resume.ContinuePendingWork)
	require.NotNil(t, resume.EnableSessionStore)
	assert.False(t, *resume.EnableSessionStore)
	require.NotNil(t, resume.SkipEmbeddingRetrieval)
	assert.True(t, *resume.SkipEmbeddingRetrieval)
	assert.Equal(t, "research-model", resume.Model)
	assert.Equal(t, "high", resume.ReasoningEffort)
	require.NotNil(t, resume.Provider)
	assert.Equal(t, "research-profile", resume.Provider.ModelID)
	assert.Equal(t, "research-model", resume.Provider.WireModel)
	assert.Equal(t, "provider-secret", resume.Provider.BearerToken)
	assert.Equal(t, "D:/work", resume.WorkingDirectory)
	assert.Equal(t, []string{"finish"}, resume.AvailableTools)
	assert.Equal(t, []string{"D:/skills"}, resume.SkillDirectories)
	assert.Equal(t, []string{"unused"}, resume.DisabledSkills)
	assert.Equal(t, researchAgentName, resume.Agent)
	require.Len(t, resume.CustomAgents, 1)
	assert.Equal(t, []string{"citation"}, resume.CustomAgents[0].Skills)
	assert.Same(t, hooks, resume.Hooks)
	require.NotNil(t, resume.LargeOutput)
	require.NotNil(t, resume.LargeOutput.Enabled)
	assert.False(t, *resume.LargeOutput.Enabled)
	require.Contains(t, resume.MCPServers, "jin10")
	resumeMCP, ok := resume.MCPServers["jin10"].(sdk.MCPHTTPServerConfig)
	require.True(t, ok)
	assert.Equal(t, "Bearer j10-secret", resumeMCP.Headers["Authorization"])
	assert.Equal(t, []string{"get_quote"}, resumeMCP.Tools)
	assert.Equal(t, 30_000, resumeMCP.Timeout)
	require.Len(t, resume.Tools, 1)
	assert.Equal(t, "finish", resume.Tools[0].Name)

	result, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "continue"})
	require.NoError(t, err)
	assert.Equal(t, "resumed-result", result.ID)
	assert.Empty(t, original.messages)
	assert.Len(t, resumed.messages, 1)
}

func TestSessionResumeRebindsMCPResourceReader(t *testing.T) {
	t.Parallel()

	resourceID := "mcp_resource_jin10__quote_codes_12345678-1234-8234-9234-123456789abc"
	original := &fakeMCPResourceSession{contents: []mcp.ResourceContent{{URI: "quote://codes", Text: sdk.String("original")}}}
	resumed := &fakeMCPResourceSession{contents: []mcp.ResourceContent{{URI: "quote://codes", Text: sdk.String("resumed")}}}
	client := &fakeClient{session: original, resumedSession: resumed}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	resource := mcp.Resource{ID: resourceID, URI: "quote://codes", Server: mcp.Config{Name: "jin10"}}
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0), MCPResources: []mcp.Resource{resource}})
	require.NoError(t, err)
	reader := sdkToolByName(t, client.configs[0].Tools, MCPResourceReadToolName)

	first, err := reader.Handler(sdk.ToolInvocation{Arguments: map[string]any{"resource_id": resourceID}})
	require.NoError(t, err)
	require.NoError(t, session.Resume(t.Context()))
	second, err := reader.Handler(sdk.ToolInvocation{Arguments: map[string]any{"resource_id": resourceID}})

	require.NoError(t, err)
	assert.Contains(t, first.TextResultForLLM, "original")
	assert.Contains(t, second.TextResultForLLM, "resumed")
	assert.Len(t, original.reads, 1)
	assert.Len(t, resumed.reads, 1)
}

func TestSessionResumeFailsBeforeReplacementWhenDisconnectFails(t *testing.T) {
	t.Parallel()

	original := &fakeSession{disconnectErrors: []error{permanentError{Err: errors.New("destroy rejected")}}}
	client := &fakeClient{session: original, resumedSession: &fakeSession{}}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)

	err = session.Resume(t.Context())

	require.ErrorContains(t, err, "disconnect copilot session before resume")
	assert.Empty(t, client.resumeIDs)
}

func TestSessionResumeFailsFastWhenSDKResumeFails(t *testing.T) {
	t.Parallel()

	resumeErr := errors.New("saved session unavailable")
	client := &fakeClient{
		session:      &fakeSession{},
		resumeErrors: []error{resumeErr},
	}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)

	err = session.Resume(t.Context())

	require.ErrorIs(t, err, resumeErr)
	require.ErrorContains(t, err, "resume copilot session")
}

func TestSessionResumeRetriesTransientLifecycleFailure(t *testing.T) {
	t.Parallel()

	original := &fakeSession{}
	resumed := &fakeSession{}
	client := &fakeClient{
		session:        original,
		resumedSession: resumed,
		resumeErrors:   []error{transientError{Err: errors.New("runtime unavailable")}},
	}
	var delays []time.Duration
	factory := newFactory(client, nil, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 1, 0)})
	require.NoError(t, err)

	err = session.Resume(t.Context())

	require.NoError(t, err)
	require.Len(t, client.resumeIDs, 2)
	assert.Equal(t, client.resumeIDs[0], client.resumeIDs[1])
	assert.Len(t, delays, 1)
}

func TestSessionResumeDoesNotDisconnectWithCanceledContext(t *testing.T) {
	t.Parallel()

	original := &fakeSession{}
	client := &fakeClient{session: original, resumedSession: &fakeSession{}}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = session.Resume(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, original.disconnectCalls)
	assert.Empty(t, client.resumeIDs)
}

func TestSessionResumeDiscardsHandleReturnedAfterContextCancellation(t *testing.T) {
	t.Parallel()

	original := &fakeSession{}
	resumed := &fakeSession{disconnectDone: make(chan struct{})}
	client := &blockingResumeClient{
		original: original,
		resumed:  resumed,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- session.Resume(ctx)
	}()
	<-client.started
	cancel()
	close(client.release)

	resumeErr := <-done
	<-resumed.disconnectDone

	require.ErrorIs(t, resumeErr, context.Canceled)
	assert.Equal(t, 1, original.disconnectCalls)
	assert.Equal(t, 1, resumed.disconnectCalls)
	_, sendErr := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "must not use late handle"})
	require.ErrorContains(t, sendErr, "session is unavailable")
}

func TestSessionOnForwardsSDKEventsAndUnsubscribe(t *testing.T) {
	t.Parallel()

	want := sdk.SessionEvent{
		ID:   "reasoning-event",
		Data: &sdk.AssistantReasoningData{Content: "inspect sources", ReasoningID: "reasoning-1"},
	}
	underlying := &fakeEventSession{event: want, result: &sdk.SessionEvent{ID: "completed"}}
	factory := newFactory(&fakeClient{session: underlying}, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)
	var got sdk.SessionEvent

	unsubscribe := session.On(func(event sdk.SessionEvent) {
		got = event
	})
	_, err = session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
	require.NoError(t, err)
	unsubscribe()

	assert.Equal(t, want, got)
	assert.Equal(t, 1, underlying.unsubscribeCalls)
}

func TestSessionSendAndWaitFailsUnrecoverableSessionLossWithoutReplacement(t *testing.T) {
	t.Parallel()

	underlying := &fakeSession{sendErrors: []error{errors.New("session no longer exists")}}
	client := &fakeClient{session: underlying}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 4, 4)})
	require.NoError(t, err)

	_, err = session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.EqualError(t, err, "send copilot message: session no longer exists")
	assert.Len(t, underlying.messages, 1)
	assert.Len(t, client.configs, 1)
}

func TestOfficialSessionPreservesSDKModelErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      *sdk.SessionErrorData
		transient bool
		permanent bool
	}{
		{
			name: "rate limit is transient",
			data: &sdk.SessionErrorData{
				ErrorType: "rate_limit",
				Message:   "too many requests",
			},
			transient: true,
		},
		{
			name: "authentication is permanent",
			data: &sdk.SessionErrorData{
				ErrorType: "authentication",
				Message:   "invalid credential",
			},
			permanent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			underlying := &fakeEventSession{
				event: sdk.SessionEvent{Data: tt.data},
				err:   errors.New("session error: " + tt.data.Message),
			}
			session := newOfficialSession(underlying)

			_, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

			require.Error(t, err)
			policy := retryPolicy(t, 0, 1)
			assert.Equal(t, tt.transient, policy.IsTransient(err))
			var permanent interface{ IsPermanent() bool }
			require.ErrorAs(t, err, &permanent)
			assert.Equal(t, tt.permanent, permanent.IsPermanent())
			assert.Equal(t, 1, underlying.unsubscribeCalls)
		})
	}
}

func TestOfficialSessionPassesThroughNonModelResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    *sdk.SessionEvent
		err       error
		wantError error
	}{
		{
			name:   "success",
			result: &sdk.SessionEvent{ID: "assistant-result"},
		},
		{
			name:      "transport error",
			err:       transientError{Err: errors.New("connection reset")},
			wantError: transientError{Err: errors.New("connection reset")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			underlying := &fakeEventSession{result: tt.result, err: tt.err}
			session := newOfficialSession(underlying)

			result, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

			if tt.wantError == nil {
				require.NoError(t, err)
				assert.Same(t, tt.result, result)
			} else {
				require.EqualError(t, err, tt.wantError.Error())
			}
			assert.Equal(t, 1, underlying.unsubscribeCalls)
			require.NoError(t, session.Disconnect())
			assert.Equal(t, 1, underlying.disconnectCalls)
		})
	}
}

func TestOfficialSessionForwardsAbort(t *testing.T) {
	t.Parallel()

	underlying := &fakeEventSession{}
	session := newOfficialSession(underlying)

	err := session.Abort(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, underlying.abortCalls)
}

func TestRetryDelayFailureStopsCurrentOperation(t *testing.T) {
	t.Parallel()

	delayErr := context.Canceled
	tests := []struct {
		name          string
		run           func(*testing.T, *Factory) error
		expectedError string
	}{
		{
			name: "create",
			run: func(t *testing.T, factory *Factory) error {
				t.Helper()

				factory.client = &fakeClient{createErrors: []error{
					transientError{Err: errors.New("runtime unavailable")},
				}}
				_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 1, 0)})
				return err
			},
			expectedError: "create copilot session retry: context canceled",
		},
		{
			name: "send",
			run: func(t *testing.T, factory *Factory) error {
				t.Helper()

				underlying := &fakeSession{sendErrors: []error{
					transientError{Err: errors.New("model unavailable")},
				}}
				factory.client = &fakeClient{session: underlying}
				session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 1)})
				require.NoError(t, err)
				_, err = session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
				return err
			},
			expectedError: "send copilot message retry: context canceled",
		},
		{
			name: "close",
			run: func(t *testing.T, factory *Factory) error {
				t.Helper()

				underlying := &fakeSession{disconnectErrors: []error{
					transientError{Err: errors.New("runtime unavailable")},
				}}
				factory.client = &fakeClient{session: underlying}
				session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 1, 0)})
				require.NoError(t, err)
				return session.Close(t.Context())
			},
			expectedError: "close copilot session after 1 attempt: context canceled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory := newFactory(nil, nil, func(context.Context, time.Duration) error {
				return delayErr
			}, fixedRandom)
			err := tt.run(t, factory)
			require.EqualError(t, err, tt.expectedError)
			assert.ErrorIs(t, err, delayErr)
		})
	}
}

func TestSessionCloseRetriesAndReturnsOnlyExhaustedCleanupWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		disconnectError []error
		lifecycleRetry  int
		wantCalls       int
		wantWarning     string
	}{
		{
			name:            "transient close recovers",
			disconnectError: []error{transientError{Err: errors.New("busy")}},
			lifecycleRetry:  1,
			wantCalls:       2,
		},
		{
			name: "exhausted transient close becomes warning",
			disconnectError: []error{
				transientError{Err: errors.New("busy")},
				transientError{Err: errors.New("still busy")},
			},
			lifecycleRetry: 1,
			wantCalls:      2,
			wantWarning:    "close copilot session after 2 attempts: still busy",
		},
		{
			name:            "permanent close immediately becomes warning",
			disconnectError: []error{permanentError{Err: errors.New("destroy rejected")}},
			lifecycleRetry:  5,
			wantCalls:       1,
			wantWarning:     "close copilot session after 1 attempt: destroy rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			underlying := &fakeSession{disconnectErrors: tt.disconnectError}
			factory := newFactory(&fakeClient{session: underlying}, nil, noDelay, fixedRandom)
			session, err := factory.Open(t.Context(), SessionConfig{
				Retry: retryPolicy(t, tt.lifecycleRetry, 0),
			})
			require.NoError(t, err)

			warning := session.Close(t.Context())

			assert.Equal(t, tt.wantCalls, underlying.disconnectCalls)
			if tt.wantWarning == "" {
				assert.NoError(t, warning)
				return
			}
			require.Error(t, warning)
			assert.Equal(t, tt.wantWarning, warning.Error())
		})
	}
}

func TestSessionCloseSuccessReturnsNilError(t *testing.T) {
	t.Parallel()
	underlying := &fakeSession{}
	factory := newFactory(&fakeClient{session: underlying}, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)

	closeErr := session.Close(t.Context())

	require.NoError(t, closeErr)
	assert.Equal(t, 1, underlying.disconnectCalls)
}

type fakeClient struct {
	configs        []*sdk.SessionConfig
	createErrors   []error
	session        sdkSession
	resumeIDs      []string
	resumeConfigs  []*sdk.ResumeSessionConfig
	resumeErrors   []error
	resumedSession sdkSession
}

type fakeMCPResourceSession struct {
	fakeSession
	contents []mcp.ResourceContent
	reads    []mcp.ResourceReadRequest
	err      error
}

func (s *fakeMCPResourceSession) ReadMCPResource(
	_ context.Context,
	request mcp.ResourceReadRequest,
) ([]mcp.ResourceContent, error) {
	s.reads = append(s.reads, request)
	return s.contents, s.err
}

func sdkToolByName(t *testing.T, tools []sdk.Tool, name string) sdk.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	require.FailNow(t, "SDK tool not found", name)
	return sdk.Tool{}
}

type blockingResumeClient struct {
	original sdkSession
	resumed  sdkSession
	started  chan struct{}
	release  chan struct{}
}

func (c *blockingResumeClient) CreateSession(context.Context, *sdk.SessionConfig) (sdkSession, error) {
	return c.original, nil
}

func (c *blockingResumeClient) ResumeSession(
	context.Context,
	string,
	*sdk.ResumeSessionConfig,
) (sdkSession, error) {
	close(c.started)
	<-c.release
	return c.resumed, nil
}

func (c *fakeClient) CreateSession(ctx context.Context, config *sdk.SessionConfig) (sdkSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.configs = append(c.configs, config)
	index := len(c.configs) - 1
	if index < len(c.createErrors) {
		return nil, c.createErrors[index]
	}
	if c.session == nil {
		c.session = &fakeSession{}
	}
	return c.session, nil
}

func (c *fakeClient) ResumeSession(
	ctx context.Context,
	sessionID string,
	config *sdk.ResumeSessionConfig,
) (sdkSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.resumeIDs = append(c.resumeIDs, sessionID)
	c.resumeConfigs = append(c.resumeConfigs, config)
	index := len(c.resumeIDs) - 1
	if index < len(c.resumeErrors) {
		return nil, c.resumeErrors[index]
	}
	if c.resumedSession == nil {
		c.resumedSession = &fakeSession{}
	}
	return c.resumedSession, nil
}

type fakeSession struct {
	messages         []sdk.MessageOptions
	sendErrors       []error
	event            *sdk.SessionEvent
	abortErrors      []error
	abortCalls       int
	disconnectErrors []error
	disconnectCalls  int
	disconnectDone   chan struct{}
}

func (*fakeSession) On(sdk.SessionEventHandler) func() {
	return func() {}
}

type fakeEventSession struct {
	handler          sdk.SessionEventHandler
	event            sdk.SessionEvent
	result           *sdk.SessionEvent
	err              error
	unsubscribeCalls int
	disconnectCalls  int
	abortCalls       int
}

func (s *fakeEventSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if s.event.Data != nil {
		s.handler(s.event)
	}
	return s.result, s.err
}

func (s *fakeEventSession) On(handler sdk.SessionEventHandler) func() {
	s.handler = handler
	return func() { s.unsubscribeCalls++ }
}

func (s *fakeEventSession) Disconnect() error {
	s.disconnectCalls++
	return nil
}

func (s *fakeEventSession) Abort(context.Context) error {
	s.abortCalls++
	return nil
}

func (s *fakeSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.messages = append(s.messages, options)
	index := len(s.messages) - 1
	if index < len(s.sendErrors) {
		return nil, s.sendErrors[index]
	}
	return s.event, nil
}

func (s *fakeSession) Disconnect() error {
	s.disconnectCalls++
	if s.disconnectDone != nil {
		close(s.disconnectDone)
	}
	index := s.disconnectCalls - 1
	if index < len(s.disconnectErrors) {
		return s.disconnectErrors[index]
	}
	return nil
}

func (s *fakeSession) Abort(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.abortCalls++
	index := s.abortCalls - 1
	if index < len(s.abortErrors) {
		return s.abortErrors[index]
	}
	return nil
}

func retryPolicy(t *testing.T, lifecycle, model int) provider.RetryPolicy {
	t.Helper()

	interval := time.Duration(0)
	policy, err := provider.MergeRetry(provider.DefaultRetryPolicy(), provider.RetryOverride{
		LifecycleRetries: &lifecycle,
		ModelCallRetries: &model,
		Interval:         &interval,
	})
	require.NoError(t, err)
	return policy
}

func providerHeaders(headers map[string]string) cty.Value {
	values := make(map[string]cty.Value, len(headers))
	for name, value := range headers {
		values[name] = cty.StringVal(value)
	}
	return cty.MapVal(values)
}

type httpError struct {
	StatusCode int
	Err        error
}

func (e httpError) Error() string       { return fmt.Sprintf("http status %d: %v", e.StatusCode, e.Err) }
func (e httpError) Unwrap() error       { return e.Err }
func (e httpError) HTTPStatusCode() int { return e.StatusCode }

type transientError struct{ Err error }

func (e transientError) Error() string   { return e.Err.Error() }
func (e transientError) Unwrap() error   { return e.Err }
func (transientError) IsTransient() bool { return true }

type permanentError struct{ Err error }

func (e permanentError) Error() string   { return e.Err.Error() }
func (e permanentError) Unwrap() error   { return e.Err }
func (permanentError) IsPermanent() bool { return true }

func noDelay(context.Context, time.Duration) error { return nil }

func fixedRandom() float64 { return 0.5 }
