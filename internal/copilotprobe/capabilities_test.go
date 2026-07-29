package copilotprobe_test

import (
	"reflect"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type finishInput struct {
	Summary string `json:"summary" jsonschema:"completion summary"`
}

type finishOutput struct {
	Accepted bool   `json:"accepted"`
	Summary  string `json:"summary"`
}

func TestOfficialSDKTypedToolAndApproveAll(t *testing.T) {
	t.Parallel()

	var received finishInput
	tool := copilot.DefineTool(
		"go_tool_finish",
		"Finish the research block",
		func(input finishInput, invocation copilot.ToolInvocation) (finishOutput, error) {
			received = input
			assert.Equal(t, "go_tool_finish", invocation.ToolName)
			return finishOutput{Accepted: true, Summary: input.Summary}, nil
		},
	)

	assert.Equal(t, "go_tool_finish", tool.Name)
	assert.Equal(t, "object", tool.Parameters["type"])
	assert.Contains(t, requireMap(t, tool.Parameters["properties"]), "summary")
	result, err := tool.Handler(copilot.ToolInvocation{
		ToolName:  "go_tool_finish",
		Arguments: map[string]any{"summary": "done"},
	})
	require.NoError(t, err)
	assert.Equal(t, finishInput{Summary: "done"}, received)
	assert.JSONEq(t, `{"accepted":true,"summary":"done"}`, result.TextResultForLLM)
	assert.Equal(t, "success", result.ResultType)

	_, err = tool.Handler(copilot.ToolInvocation{
		ToolName:  "go_tool_finish",
		Arguments: map[string]any{"summary": 42},
	})
	require.ErrorContains(t, err, "failed to unmarshal arguments")

	decision, err := copilot.PermissionHandler.ApproveAll(
		copilot.PermissionRequestCustomTool{ToolName: "go_tool_finish"},
		copilot.PermissionInvocation{},
	)
	require.NoError(t, err)
	assert.IsType(t, &rpc.PermissionDecisionApproveOnce{}, decision)
}

func TestOfficialSDKForwardsR42SessionConfiguration(t *testing.T) {
	t.Parallel()

	runtime := newProbeRuntime(t)
	client := newProbeClient(t, runtime)
	tool := copilot.DefineTool(
		"go_tool_finish",
		"Finish the research block",
		func(input finishInput, _ copilot.ToolInvocation) (finishOutput, error) {
			return finishOutput{Accepted: true, Summary: input.Summary}, nil
		},
	)

	session, err := client.CreateSession(t.Context(), &copilot.SessionConfig{
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "max",
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "append",
			Content: "r42 protocol\nauthor instructions",
		},
		Provider: &copilot.ProviderConfig{
			Type:      "openai",
			WireAPI:   "responses",
			Transport: "websockets",
			BaseURL:   "https://models.example.test",
			APIKey:    "test-key",
			Headers:   map[string]string{"X-R42": "probe"},
		},
		Tools:               []copilot.Tool{tool},
		AvailableTools:      []string{"custom:go_tool_finish", "builtin:view"},
		ExcludedTools:       []string{"custom:go_tool_finish", "builtin:ask_user"},
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		EnableSkills:        copilot.Bool(true),
		SkillDirectories:    []string{"C:/skills"},
		DisabledSkills:      []string{"disabled-skill"},
		CustomAgents: []copilot.CustomAgentConfig{
			{
				Name:            "r42_research",
				Prompt:          "r42 protocol\nauthor instructions",
				Skills:          []string{"research-skill"},
				Model:           "gpt-5.6-sol",
				ReasoningEffort: "max",
			},
		},
		Agent: "r42_research",
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Disconnect()) })

	request := runtime.nextRequest(t, "session.create")
	assert.Equal(t, "gpt-5.6-sol", request.Params["model"])
	assert.Equal(t, "max", request.Params["reasoningEffort"])
	assert.Equal(t, true, request.Params["requestPermission"])
	assert.Equal(t, "excluded", request.Params["toolFilterPrecedence"])
	assert.Equal(t, []any{"custom:go_tool_finish", "builtin:view"}, request.Params["availableTools"])
	assert.Equal(t, []any{"custom:go_tool_finish", "builtin:ask_user"}, request.Params["excludedTools"])
	assert.Equal(t, []any{"C:/skills"}, request.Params["skillDirectories"])
	assert.Equal(t, []any{"disabled-skill"}, request.Params["disabledSkills"])
	assert.Equal(t, true, request.Params["enableSkills"])
	assert.Equal(t, "r42_research", request.Params["agent"])
	systemMessage := requireMap(t, request.Params["systemMessage"])
	assert.Equal(t, "append", systemMessage["mode"])
	assert.Equal(t, "r42 protocol\nauthor instructions", systemMessage["content"])

	provider := requireMap(t, request.Params["provider"])
	assert.Equal(t, "openai", provider["type"])
	assert.Equal(t, "responses", provider["wireApi"])
	assert.Equal(t, "websockets", provider["transport"])
	assert.Equal(t, "https://models.example.test", provider["baseUrl"])
	assert.Equal(t, "test-key", provider["apiKey"])
	assert.Equal(t, "probe", requireMap(t, provider["headers"])["X-R42"])

	agents := requireSlice(t, request.Params["customAgents"])
	require.Len(t, agents, 1)
	agent := requireMap(t, agents[0])
	assert.Equal(t, []any{"research-skill"}, agent["skills"])
	assert.Equal(t, "gpt-5.6-sol", agent["model"])
	assert.Equal(t, "max", agent["reasoningEffort"])
	assert.Equal(t, "r42 protocol\nauthor instructions", agent["prompt"])

	tools := requireSlice(t, request.Params["tools"])
	require.Len(t, tools, 1)
	assert.Equal(t, "go_tool_finish", requireMap(t, tools[0])["name"])
}

func TestOfficialSDKReusesSessionAndDisconnects(t *testing.T) {
	t.Parallel()

	runtime := newProbeRuntime(t)
	client := newProbeClient(t, runtime)
	session, err := client.CreateSession(t.Context(), &copilot.SessionConfig{
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
	})
	require.NoError(t, err)
	runtime.nextRequest(t, "session.create")

	firstID, err := session.SendPrompt(t.Context(), "first turn")
	require.NoError(t, err)
	secondID, err := session.SendPrompt(t.Context(), "second turn")
	require.NoError(t, err)
	assert.Equal(t, "message-1", firstID)
	assert.Equal(t, "message-2", secondID)

	first := runtime.nextRequest(t, "session.send")
	second := runtime.nextRequest(t, "session.send")
	assert.Equal(t, session.SessionID, first.Params["sessionId"])
	assert.Equal(t, session.SessionID, second.Params["sessionId"])
	assert.Equal(t, "first turn", first.Params["prompt"])
	assert.Equal(t, "second turn", second.Params["prompt"])

	require.NoError(t, session.Disconnect())
	destroy := runtime.nextRequest(t, "session.destroy")
	assert.Equal(t, session.SessionID, destroy.Params["sessionId"])

	require.NoError(t, client.Stop())
	repeatedDestroy := runtime.nextRequest(t, "session.destroy")
	assert.Equal(t, session.SessionID, repeatedDestroy.Params["sessionId"])

	sessionType := reflect.TypeFor[*copilot.Session]()
	_, hasDisconnect := sessionType.MethodByName("Disconnect")
	_, hasCloseSession := sessionType.MethodByName("CloseSession")
	assert.True(t, hasDisconnect)
	assert.False(t, hasCloseSession)
}

func requireMap(t *testing.T, value any) map[string]any {
	t.Helper()

	result, ok := value.(map[string]any)
	require.True(t, ok, "expected map, got %T", value)
	return result
}

func requireSlice(t *testing.T, value any) []any {
	t.Helper()

	result, ok := value.([]any)
	require.True(t, ok, "expected slice, got %T", value)
	return result
}
