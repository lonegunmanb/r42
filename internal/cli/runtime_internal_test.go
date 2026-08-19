package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestPhasedResearchMarksSubsequentRunsAsRevision(t *testing.T) {
	t.Parallel()

	session := &recordingSession{kind: debuglog.SessionResearch}
	research := &phaseCapturingResearch{session: session}
	runner := &phasedResearch{research: research, session: session}

	_, err := runner.Run(t.Context(), researchruntime.Config{})
	require.NoError(t, err)
	_, err = runner.Run(t.Context(), researchruntime.Config{})
	require.NoError(t, err)
	assert.Equal(t, []debuglog.SessionKind{debuglog.SessionResearch, debuglog.SessionRevision}, research.phases)
}

func TestQCVerdictToolSchemaDescribesIssueFields(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	tool := qcVerdictTool("research.static.test", recorder, qc.NewVerdictRecorder())

	schema, err := json.Marshal(tool.Parameters)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type": "object",
		"properties": {
			"decision": {"type": "string", "enum": ["pass", "revise_research", "reopen_collection"]},
			"issues": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"code": {"type": "string"},
						"message": {"type": "string"},
						"path": {"type": "string"},
						"repair_hint": {"type": "string"}
					},
					"required": ["code", "message"],
					"additionalProperties": false
				}
			}
		},
		"required": ["decision"],
		"additionalProperties": false
	}`, string(schema))
}

func TestQCVerdictToolReturnsRepairableCollectionBudgetRejection(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	verdicts := qc.NewVerdictRecorder()
	maximum := 2
	verdicts.SetCollectionBudget(qc.CollectionBudget{RoundsUsed: 2, MaxRounds: &maximum})
	tool := qcVerdictTool("research.static.test", recorder, verdicts)

	result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"decision": "reopen_collection",
		"issues":   []any{map[string]any{"code": "coverage", "message": "find another source"}},
	}})

	require.NoError(t, err)
	assert.Equal(t, "success", result.ResultType)
	assert.JSONEq(t, `{
		"accepted": false,
		"issues": [{
			"code": "collection_round_budget_exhausted",
			"message": "cannot reopen collection: all 2 collection rounds have been used",
			"repair_hint": "Choose revise_research or pass using existing snapshots."
		}]
	}`, result.TextResultForLLM)
}

type phaseCapturingResearch struct {
	session *recordingSession
	phases  []debuglog.SessionKind
}

func (r *phaseCapturingResearch) Run(context.Context, researchruntime.Config) (researchruntime.Result, error) {
	r.phases = append(r.phases, r.session.currentKind())
	return researchruntime.Result{}, nil
}

func TestToolCallQuotaSuccessfulCallsExhaustLimit(t *testing.T) {
	t.Parallel()

	quota := newToolCallQuota(map[string]int{"tool_lookup": 1})

	require.NoError(t, quota.reserve("tool_lookup"))
	err := quota.reserve("tool_lookup")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `tool "tool_lookup" per-session call quota exhausted (limit 1 successful calls)`)
	assert.Contains(t, err.Error(), "this quota will not reset during this session")
	assert.Contains(t, err.Error(), "do not call this tool again")
	assert.Contains(t, err.Error(), "continue with existing results or another available tool")
}

func TestTypedToolDescriptionExplainsConfiguredQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		quota        map[string]int
		wantExact    string
		wantContains []string
	}{
		{
			name:      "unlimited tool keeps configured description",
			quota:     nil,
			wantExact: "lookup",
		},
		{
			name:  "limited tool explains quota lifecycle",
			quota: map[string]int{"tool_lookup": 1},
			wantContains: []string{
				"lookup",
				"r42 per-session call quota: at most 1 accepted calls",
				"this quota will not reset during this session",
				"do not call this tool again",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tool, closeFactory := buildQuotaTestTool(t, newToolCallQuota(tt.quota))
			t.Cleanup(closeFactory)

			if tt.wantExact != "" {
				assert.Equal(t, tt.wantExact, tool.Description)
			}
			for _, fragment := range tt.wantContains {
				assert.Contains(t, tool.Description, fragment)
			}
		})
	}
}

func TestToolCallQuotaRollbackRestoresReservation(t *testing.T) {
	t.Parallel()

	quota := newToolCallQuota(map[string]int{"tool_lookup": 1})
	require.NoError(t, quota.reserve("tool_lookup"))

	quota.rollback("tool_lookup")

	require.NoError(t, quota.reserve("tool_lookup"))
}

func TestToolCallQuotaConcurrentReservationsDoNotExceedLimit(t *testing.T) {
	t.Parallel()

	const limit = 5
	quota := newToolCallQuota(map[string]int{"tool_lookup": limit})
	var accepted atomic.Int32
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if quota.reserve("tool_lookup") == nil {
				accepted.Add(1)
			}
		})
	}
	group.Wait()

	assert.Equal(t, int32(limit), accepted.Load())
}

func TestBuiltInToolCallQuotaHooksDenyExhaustedCallAndRollbackFailure(t *testing.T) {
	t.Parallel()

	quota := newToolCallQuota(map[string]int{"web_fetch": 1})
	hooks := builtInToolCallQuotaHooks(quota)
	require.NotNil(t, hooks)

	allowed, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision)

	denied, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "deny", denied.PermissionDecision)
	assert.Contains(t, denied.PermissionDecisionReason, "quota exhausted")
	assert.Contains(t, denied.PermissionDecisionReason, "do not call this tool again")

	_, err = hooks.OnPostToolUseFailure(
		sdk.PostToolUseFailureHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{},
	)
	require.NoError(t, err)

	allowed, err = hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision)
}

func TestBuiltInToolCallQuotaPromptDescribesLimits(t *testing.T) {
	t.Parallel()

	prompt := appendBuiltInToolCallQuotaPrompt("research instructions", map[string]int{
		"web_fetch":  2,
		"web_search": 3,
	})

	assert.Contains(t, prompt, "research instructions")
	assert.Contains(t, prompt, "web_fetch: at most 2 successful calls")
	assert.Contains(t, prompt, "web_search: at most 3 successful calls")
	assert.Contains(t, prompt, "failed calls do not consume quota")
	assert.Contains(t, prompt, "do not retry it")
}

func TestToolCallQuotaRoutesToolIDsAndBuiltInNames(t *testing.T) {
	t.Parallel()

	const toolID = "tool_go_tool_lookup_12345678-1234-8234-9234-123456789abc"
	typed, builtIn := splitToolCallQuota(map[string]int{toolID: 1, "web_fetch": 2})

	assert.Equal(t, map[string]int{toolID: 1}, typed)
	assert.Equal(t, map[string]int{"web_fetch": 2}, builtIn)
	assert.Nil(t, builtInToolCallQuotaHooks(newToolCallQuota(nil)))
	assert.Equal(t, "research instructions", appendBuiltInToolCallQuotaPrompt("research instructions", nil))
}

func TestTypedToolHandlerConsumesOnlySuccessfulCalls(t *testing.T) {
	t.Parallel()

	tool, closeFactory := buildQuotaTestTool(t, newToolCallQuota(map[string]int{"tool_lookup": 1}))
	t.Cleanup(closeFactory)

	malformed, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, malformed.TextResultForLLM, `"accepted":false`)

	rejected, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Mode": "reject"}})
	require.NoError(t, err)
	assert.Contains(t, rejected.TextResultForLLM, `"accepted":false`)

	_, err = tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Mode": "error"}})
	require.ErrorContains(t, err, "execution failed")

	accepted, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Mode": "accept"}})
	require.NoError(t, err)
	assert.Contains(t, accepted.TextResultForLLM, `"accepted":true`)

	_, err = tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Mode": "accept"}})
	assert.ErrorContains(t, err, `tool "tool_lookup" per-session call quota exhausted (limit 1 successful calls)`)
}

func TestResearchAndQCTypedToolQuotasAreIndependent(t *testing.T) {
	t.Parallel()

	researchTool, closeResearch := buildQuotaTestTool(t, newToolCallQuota(map[string]int{"tool_lookup": 1}))
	t.Cleanup(closeResearch)
	qcTool, closeQC := buildQuotaTestTool(t, newToolCallQuota(map[string]int{"tool_lookup": 1}))
	t.Cleanup(closeQC)

	for _, tool := range []sdk.Tool{researchTool, qcTool} {
		_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Mode": "accept"}})
		require.NoError(t, err)
	}
	for _, tool := range []sdk.Tool{researchTool, qcTool} {
		_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Mode": "accept"}})
		assert.ErrorContains(t, err, `tool "tool_lookup" per-session call quota exhausted (limit 1 successful calls)`)
	}
}

func buildQuotaTestTool(t *testing.T, quota *toolCallQuota) (sdk.Tool, func()) {
	t.Helper()
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	factory := &runtimeFactory{
		recorder: recorder,
		state:    new(runtimeState),
		tools: map[string]plan.ToolSpec{
			"tool_lookup": {
				ID: "tool_lookup", Address: "go_tool.lookup", Kind: "go", Description: "lookup",
				Source: `
import (
  "context"
  "errors"
)
type Input struct { Mode string }
type Output string
func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
  if input.Mode == "error" {
    return ToolResponse[Output]{}, errors.New("execution failed")
  }
  if input.Mode == "reject" {
    return ToolResponse[Output]{Issues: []Issue{{Code: "retry", Message: "try again"}}}, nil
  }
  output := Output("done")
  return ToolResponse[Output]{Accepted: true, Output: &output}, nil
}`,
			},
		},
	}
	tools, _, err := factory.buildTools(
		t.Context(), "research.static.test", debuglog.SessionResearch, t.TempDir(),
		[]string{"tool_lookup"}, nil, nil, quota,
	)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	return tools[0], func() {
		require.NoError(t, factory.Close())
		require.NoError(t, recorder.Close())
	}
}

func TestResearchApplyBlockMatchesStaticResearchAddressShape(t *testing.T) {
	t.Parallel()

	block := new(researchApplyBlock)

	assert.Equal(t, "static", block.Type())
	assert.Equal(t, 3, block.AddressLength())
}

func TestSetBlockResultMergesStaticResearchForEachInstance(t *testing.T) {
	t.Parallel()

	contextValues := map[string]cty.Value{
		"research": cty.ObjectVal(map[string]cty.Value{
			"static": cty.ObjectVal(map[string]cty.Value{
				"deep_dive": cty.ObjectVal(map[string]cty.Value{
					"001": cty.ObjectVal(map[string]cty.Value{
						"artifact": cty.StringVal("knowledge.json"),
					}),
				}),
			}),
		}),
	}

	setBlockResult(contextValues, "research.static.deep_dive[001]", cty.ObjectVal(map[string]cty.Value{
		"result": cty.StringVal("done"),
	}))

	instance := contextValues["research"].
		GetAttr("static").
		GetAttr("deep_dive").
		GetAttr("001")
	assert.Equal(t, "knowledge.json", instance.GetAttr("artifact").AsString())
	assert.Equal(t, "done", instance.GetAttr("result").AsString())
}

func TestRuntimeAppliesLegacyPlanWithReservedDebugRun(t *testing.T) {
	t.Parallel()

	runRoot := t.TempDir()
	legacy, err := plan.NewWithContextAndLocals(
		t.TempDir(), nil,
		map[string]plan.OutputSpec{"summary": {Value: cty.StringVal("legacy")}},
		nil, nil,
	)
	require.NoError(t, err)
	state := &debugRun{enabled: true}
	t.Cleanup(func() { require.NoError(t, state.close()) })
	ctx := withDebugRun(t.Context(), state)
	runtime := NewRuntime()
	config, err := runtime.ConfigFromPlan(legacy, executor.ResearchConfigOptions{
		Context: ctx, RunDirectory: runRoot, Debug: true,
	})
	require.NoError(t, err)

	err = config.Plan().Apply()

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("legacy"), config.Outputs()["summary"])
}

func TestRuntimeFactoryClosesOpenedSessionWhenLifecycleCompletionCannotBeRecorded(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	session := &fakeInternalSession{}
	opener := &closingRecorderOpener{recorder: recorder, session: session}
	factory := &runtimeFactory{sessions: opener}
	ctx := debuglog.WithRecorder(t.Context(), recorder)

	opened, err := factory.openSession(ctx, "research.source", debuglog.SessionResearch, copilot.SessionConfig{
		Model: "test-model", WorkingDirectory: filepath.Clean(t.TempDir()),
	})

	require.ErrorContains(t, err, "debug recorder is closed")
	assert.Nil(t, opened)
	assert.Equal(t, 1, session.closeCalls)
}

func TestRuntimeFactoryResolvesToolDefinitionsByID(t *testing.T) {
	t.Parallel()

	const toolID = "tool_go_tool_finish_12345678-1234-8234-9234-123456789abc"
	factory := &runtimeFactory{tools: map[string]plan.ToolSpec{
		toolID: {ID: toolID, Address: "go_tool.finish", Kind: "go"},
	}}

	definitions, err := factory.resolveToolDefinitions([]string{toolID}, stringPointer(toolID))

	require.NoError(t, err)
	require.Len(t, definitions, 1)
	assert.Equal(t, toolID, definitions[0].ID)

	_, err = factory.resolveToolDefinitions([]string{"missing"}, nil)
	assert.EqualError(t, err, `typed tool id "missing" was not planned`)
}

func stringPointer(value string) *string { return &value }

func TestRecordingSessionClosesSDKSessionWhenLifecycleStartCannotBeRecorded(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	require.NoError(t, recorder.Close())
	session := &fakeInternalSession{}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	err = recorded.Close(t.Context())

	require.ErrorContains(t, err, "debug recorder is closed")
	assert.Equal(t, 1, session.closeCalls)
}

func TestRecordingSessionRecordsReasoningEventsDuringSend(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recorder.Close() })
	session := &fakeInternalSession{
		events: []sdk.SessionEvent{
			{
				ID:   "intermediate-assistant-message",
				Data: &sdk.AssistantMessageData{Content: "not final"},
			},
			{
				ID: "reasoning-delta",
				Data: &sdk.AssistantReasoningDeltaData{
					DeltaContent: "inspect ", ReasoningID: "reasoning-1",
				},
			},
			{
				ID: "reasoning-final",
				Data: &sdk.AssistantReasoningData{
					Content: "inspect sources", ReasoningID: "reasoning-1",
				},
			},
		},
		result: &sdk.SessionEvent{
			ID:   "assistant-final",
			Data: &sdk.AssistantMessageData{Content: "research complete"},
		},
	}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
	require.NoError(t, err)
	assert.Same(t, session.result, result)
	require.NoError(t, recorder.Close())
	assert.Equal(t, 1, session.unsubscribeCalls)

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.Len(t, lines, 9)
	events := make([]debuglog.Event, 0, len(lines))
	for _, line := range lines {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		events = append(events, event)
	}

	wantTypes := []sdk.SessionEventType{
		sdk.SessionEventTypeAssistantMessage,
		sdk.SessionEventTypeAssistantReasoningDelta,
		sdk.SessionEventTypeAssistantReasoning,
		sdk.SessionEventTypeAssistantMessage,
	}
	assistantEvents := make([]debuglog.Event, 0, len(wantTypes))
	for _, event := range events {
		if event.Kind == debuglog.EventMessage && event.Role == debuglog.RoleAssistant {
			assistantEvents = append(assistantEvents, event)
		}
	}
	require.Len(t, assistantEvents, len(wantTypes))
	for index, wantType := range wantTypes {
		event := assistantEvents[index]
		var sessionEvent sdk.SessionEvent
		require.NoError(t, json.Unmarshal(event.SDKEvent, &sessionEvent))
		assert.Equal(t, wantType, sessionEvent.Type())
	}
}

func TestRecordingSessionRecordsSessionLifecycleAndWaitState(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)

	session := &fakeInternalSession{
		events: []sdk.SessionEvent{
			{
				ID:   "session-error",
				Data: &sdk.SessionErrorData{ErrorType: "query", Message: "provider unavailable"},
			},
			{
				ID:   "session-idle",
				Data: &sdk.SessionIdleData{},
			},
		},
		result: &sdk.SessionEvent{
			ID:   "assistant-final",
			Data: &sdk.AssistantMessageData{Content: "complete"},
		},
	}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
	require.NoError(t, err)
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	var events []debuglog.Event
	for _, line := range lines {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		events = append(events, event)
	}

	waitStarted := findEvent(t, events, "session.wait", debuglog.StatusStarted)
	assert.Equal(t, "session.idle", waitStarted.WaitFor)
	waitCompleted := findEvent(t, events, "session.wait", debuglog.StatusCompleted)
	assert.Equal(t, "session.idle", waitCompleted.LastEvent)
	lifecycleError := findEvent(t, events, "session.error", debuglog.StatusFailed)
	assert.Equal(t, "provider unavailable", lifecycleError.Error)
	assert.Equal(t, "provider unavailable", lifecycleError.Content)
}

func TestRecordingSessionWaitStateIsScopedToEachSend(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)

	calls := 0
	session := &fakeInternalSession{}
	session.events = []sdk.SessionEvent{{
		ID:   "warning-on-first-send",
		Data: &sdk.SessionWarningData{Message: "first send"},
	}}
	session.beforeEvents = func() {
		calls++
		if calls == 2 {
			session.events = nil
		}
	}
	session.result = &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "complete"}}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "first"})
	require.NoError(t, err)
	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "second"})
	require.NoError(t, err)
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	var waits []debuglog.Event
	for line := range strings.SplitSeq(strings.TrimSpace(string(content)), "\n") {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event.Action == "session.wait" && event.Status == debuglog.StatusCompleted {
			waits = append(waits, event)
		}
	}
	require.Len(t, waits, 2)
	assert.Equal(t, "session.warning", waits[0].LastEvent)
	assert.Empty(t, waits[1].LastEvent)
}

func TestRecordingSessionContinuesAfterAbortRestoresIdle(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
		handler(sdk.SessionEvent{ID: "tool-start", Data: &sdk.ToolExecutionStartData{
			ToolCallID: "finish-1", ToolName: "finish",
		}})
		handler(sdk.SessionEvent{ID: "tool-complete", Data: &sdk.ToolExecutionCompleteData{
			ToolCallID: "finish-1", Success: true,
		}})
		handler(sdk.SessionEvent{ID: "subagent-start", Data: &sdk.SubagentStartedData{
			ToolCallID: "agent-1", AgentName: "reviewer",
		}})
		handler(sdk.SessionEvent{ID: "subagent-complete", Data: &sdk.SubagentCompletedData{
			ToolCallID: "agent-1", AgentName: "reviewer",
		}})
		handler(sdk.SessionEvent{ID: "assistant-idle", Data: &sdk.AssistantIdleData{}})
		return nil
	})
	session.idleOnAbort = true
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "previous model turn stalled")
}

func TestRecordingSessionRecoversAllKindsOfSessionInactivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events []sdk.SessionEvent
	}{
		{name: "no response"},
		{
			name: "partial assistant message",
			events: []sdk.SessionEvent{{
				ID: "partial-message",
				Data: &sdk.AssistantMessageDeltaData{
					MessageID: "message-1", DeltaContent: "Let me try",
				},
			}},
		},
		{
			name: "tool completes without idle",
			events: []sdk.SessionEvent{
				{ID: "tool-start", Data: &sdk.ToolExecutionStartData{ToolCallID: "fetch-1", ToolName: "fetch"}},
				{ID: "tool-complete", Data: &sdk.ToolExecutionCompleteData{ToolCallID: "fetch-1", Success: true}},
			},
		},
		{
			name: "duplicate subagent event",
			events: []sdk.SessionEvent{
				{ID: "agent-start", Data: &sdk.SubagentStartedData{ToolCallID: "agent-1", AgentName: "reviewer"}},
				{ID: "agent-start", Data: &sdk.SubagentStartedData{ToolCallID: "agent-1", AgentName: "reviewer"}},
				{ID: "agent-complete", Data: &sdk.SubagentCompletedData{ToolCallID: "agent-1", AgentName: "reviewer"}},
			},
		},
		{
			name: "protocol completes without idle",
			events: []sdk.SessionEvent{
				{ID: "assistant-message", Data: &sdk.AssistantMessageData{Content: "complete"}},
				{ID: "assistant-idle", Data: &sdk.AssistantIdleData{}},
			},
		},
		{
			name:   "stale session idle",
			events: []sdk.SessionEvent{{ID: "session-idle", Data: &sdk.SessionIdleData{}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
				for _, event := range tt.events {
					handler(event)
				}
				return nil
			})
			recorder, err := debuglog.NewRecorder(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, recorder.Close()) })
			recorded := &recordingSession{
				Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
				stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
			}
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()

			result, err := recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, 1, session.abortCalls)
			assert.Equal(t, 1, session.resumeCalls)
			assert.Equal(t, 2, session.subscribeCalls)
			require.Len(t, session.prompts, 2)
			assert.Equal(t, "research", session.prompts[0])
			assert.Contains(t, session.prompts[1], "previous model turn stalled")
		})
	}
}

func TestRecordingSessionActivityExtendsStallDeadline(t *testing.T) {
	t.Parallel()

	var session *recoveringInternalSession
	session = newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
		time.Sleep(80 * time.Millisecond)
		event := sdk.SessionEvent{ID: "still-working", Data: &sdk.AssistantReasoningDeltaData{
			ReasoningID: "reasoning-1", DeltaContent: "still working",
		}}
		handler(event)
		time.Sleep(160 * time.Millisecond)
		handler(event)
		time.Sleep(80 * time.Millisecond)
		session.releaseSend()
		return nil
	})
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 200 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
}

func TestRecordingSessionBoundsCanceledWaiterTermination(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	session := newRecoveringInternalSession(func(sdk.SessionEventHandler) error {
		close(started)
		return nil
	})
	session.ignoreCancellation = true
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: time.Hour, terminationTimeout: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, sendErr := recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})
		done <- sendErr
	}()
	<-started
	cancel()

	select {
	case sendErr := <-done:
		require.ErrorIs(t, sendErr, context.Canceled)
		require.ErrorContains(t, sendErr, "timed out waiting for canceled session work to stop")
	case <-time.After(250 * time.Millisecond):
		session.releaseSend()
		require.FailNow(t, "canceled session termination did not time out")
	}
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
	require.NoError(t, recorded.Close(t.Context()))
	assert.Zero(t, session.closeCalls)
	session.releaseSend()
}

func TestRecordingSessionAbortsNativeWorkWhenParentContextIsCanceled(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	session := newRecoveringInternalSession(func(sdk.SessionEventHandler) error {
		close(started)
		return nil
	})
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: time.Hour, terminationTimeout: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, sendErr := recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})
		done <- sendErr
	}()
	<-started
	cancel()

	sendErr := <-done

	require.ErrorIs(t, sendErr, context.Canceled)
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
}

func TestRecordingSessionAbortsWhenSendCompletionRacesParentCancellation(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(nil)
	session.immediateResult = true
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: time.Hour, terminationTimeout: 20 * time.Millisecond,
	}
	ctx := completionRaceContext{Context: t.Context(), done: make(chan struct{})}

	_, err = recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
}

func TestRecordingSessionBoundsAbortWhenParentContextIsCanceled(t *testing.T) {
	t.Parallel()

	abortRelease := make(chan struct{})
	session := newRecoveringInternalSession(nil)
	session.abortBlock = abortRelease
	t.Cleanup(func() {
		close(abortRelease)
		session.releaseSend()
	})
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: time.Hour, terminationTimeout: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, sendErr := recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})
		done <- sendErr
	}()
	cancel()

	select {
	case sendErr := <-done:
		require.ErrorIs(t, sendErr, context.Canceled)
		require.ErrorContains(t, sendErr, "timed out aborting canceled session")
	case <-time.After(250 * time.Millisecond):
		require.FailNow(t, "canceled session abort did not stop at the termination timeout")
	}
	select {
	case <-session.abortStarted:
	default:
		require.Fail(t, "abort was not attempted")
	}
	assert.Zero(t, session.resumeCalls)
}

func TestRecordingSessionCloseIsBounded(t *testing.T) {
	t.Parallel()

	session := &blockingCloseInternalSession{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		terminationTimeout: 20 * time.Millisecond,
	}
	done := make(chan error, 1)
	go func() {
		done <- recorded.Close(t.Context())
	}()
	<-session.started

	select {
	case closeErr := <-done:
		require.ErrorContains(t, closeErr, "timed out closing session")
		close(session.release)
		<-session.finished
	case <-time.After(250 * time.Millisecond):
		close(session.release)
		<-done
		require.FailNow(t, "session close did not stop at the termination timeout")
	}
}

func TestStopCopilotClientReturnsGracefulResult(t *testing.T) {
	t.Parallel()

	stopErr := errors.New("stop failed")
	client := &fakeStoppableClient{stopErr: stopErr}

	err := stopCopilotClient(client, 20*time.Millisecond)

	require.ErrorIs(t, err, stopErr)
	assert.Zero(t, client.forceCalls.Load())
}

func TestStopCopilotClientForcesStopAfterTimeout(t *testing.T) {
	t.Parallel()

	client := newBlockingStoppableClient()

	err := stopCopilotClient(client, 20*time.Millisecond)

	require.ErrorContains(t, err, "timed out stopping copilot client")
	select {
	case <-client.forceCalled:
	case <-time.After(250 * time.Millisecond):
		require.FailNow(t, "ForceStop was not called")
	}
	select {
	case <-client.stopFinished:
	case <-time.After(250 * time.Millisecond):
		require.FailNow(t, "ForceStop did not release Stop")
	}
}

func TestRunBoundedSessionOperationDoesNotStartWithCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := make(chan struct{}, 1)

	err := runBoundedSessionOperation(ctx, func(context.Context) error {
		started <- struct{}{}
		return nil
	})

	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-started:
		require.Fail(t, "operation started after its context was canceled")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRecordingSessionFailsWhenStalledSessionCannotBeResumed(t *testing.T) {
	t.Parallel()

	resumeErr := errors.New("resume rejected")
	session := newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
		handler(sdk.SessionEvent{ID: "subagent-start", Data: &sdk.SubagentStartedData{
			ToolCallID: "agent-1", AgentName: "reviewer",
		}})
		handler(sdk.SessionEvent{ID: "subagent-failed", Data: &sdk.SubagentFailedData{
			ToolCallID: "agent-1", AgentName: "reviewer", Error: "review failed",
		}})
		handler(sdk.SessionEvent{ID: "assistant-idle", Data: &sdk.AssistantIdleData{}})
		return nil
	})
	session.resumeErr = resumeErr
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.ErrorIs(t, err, resumeErr)
	require.ErrorContains(t, err, "resume stalled session")
	assert.Equal(t, 1, session.abortCalls)
	assert.Equal(t, 1, session.resumeCalls)
}

func TestRecordingSessionFailsWhenAbortIsRejectedWithoutIdle(t *testing.T) {
	t.Parallel()

	abortErr := errors.New("abort rejected")
	session := newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
		handler(sdk.SessionEvent{ID: "assistant-idle", Data: &sdk.AssistantIdleData{}})
		return nil
	})
	session.abortErr = abortErr
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.ErrorIs(t, err, abortErr)
	require.ErrorContains(t, err, "abort stalled session")
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
	assert.Equal(t, []string{"research"}, session.prompts)
}

func TestRecordingSessionDoesNotResumeWhenParentCancelsDuringRecovery(t *testing.T) {
	t.Parallel()

	abortRelease := make(chan struct{})
	session := newRecoveringInternalSession(nil)
	session.abortBlock = abortRelease
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, sendErr := recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})
		done <- sendErr
	}()
	<-session.abortStarted
	cancel()
	close(abortRelease)

	sendErr := <-done

	require.ErrorIs(t, sendErr, context.Canceled)
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
	assert.Equal(t, []string{"research"}, session.prompts)
}

func TestRecordingSessionCancelsResumeWhenParentContextIsCanceled(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(nil)
	session.resumeBlock = true
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 500 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, sendErr := recorded.SendAndWait(ctx, sdk.MessageOptions{Prompt: "research"})
		done <- sendErr
	}()
	<-session.resumeStarted
	cancel()

	select {
	case sendErr := <-done:
		require.ErrorIs(t, sendErr, context.Canceled)
	case <-time.After(100 * time.Millisecond):
		sendErr := <-done
		require.FailNow(t, "Resume ignored parent cancellation", "eventual error: %v", sendErr)
	}
	assert.Equal(t, 1, session.abortCalls)
	assert.Equal(t, 1, session.resumeCalls)
	assert.Equal(t, []string{"research"}, session.prompts)
}

func TestWaitForSessionAttemptStopIncludesInFlightEventCallback(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(nil)
	recorded := &recordingSession{Session: session}
	progress := newSessionProgress()
	unsubscribe := recorded.subscribeToAttempt(progress, func(sdk.SessionEvent) {})
	defer unsubscribe()

	recorded.eventMu.Lock()
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		session.handler(sdk.SessionEvent{
			ID: "tool-start",
			Data: &sdk.ToolExecutionStartData{
				ToolCallID: "fetch-1",
				ToolName:   "fetch",
			},
		})
	}()
	defer func() {
		recorded.eventMu.Unlock()
		<-callbackDone
	}()
	select {
	case <-progress.activitySignal:
	case <-time.After(time.Second):
		require.FailNow(t, "event callback did not start")
	}

	ctx, cancel := context.WithCancel(t.Context())
	completed := make(chan sessionSendResult)
	waitDone := make(chan error, 1)
	go func() {
		_, err := waitForSessionAttemptStop(ctx, completed, progress, nil)
		waitDone <- err
	}()
	completed <- sessionSendResult{}
	cancel()

	require.ErrorIs(t, <-waitDone, context.Canceled)
}

func TestRecordingSessionRejectsLateCallbackAfterRecoveryBarrier(t *testing.T) {
	t.Parallel()

	session := newLateCallbackInternalSession()
	t.Cleanup(session.releaseLate)
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	bus := debuglog.NewEventBus()
	recorder.SetEventBus(bus)
	var observedMu sync.Mutex
	var observed []debuglog.Event
	unsubscribe := bus.Subscribe(func(event debuglog.Event) {
		observedMu.Lock()
		observed = append(observed, event)
		observedMu.Unlock()
	})
	t.Cleanup(unsubscribe)
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 100 * time.Millisecond,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
	require.NoError(t, err)
	require.NotNil(t, result)
	session.releaseLate()
	<-session.lateCallbackDone

	observedMu.Lock()
	defer observedMu.Unlock()
	for _, event := range observed {
		assert.NotEqual(t, "late-tool", event.ToolCallID)
	}
}

func TestRecordingSessionFailsWhenStalledWaiterIgnoresCancellation(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
		handler(sdk.SessionEvent{ID: "assistant-message", Data: &sdk.AssistantMessageData{Content: "complete"}})
		handler(sdk.SessionEvent{ID: "assistant-idle", Data: &sdk.AssistantIdleData{}})
		return nil
	})
	session.ignoreCancellation = true
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		_, sendErr := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
		done <- sendErr
	}()

	select {
	case sendErr := <-done:
		require.Error(t, sendErr)
		require.ErrorContains(t, sendErr, "timed out waiting for stalled session work to stop")
	case <-time.After(250 * time.Millisecond):
		session.releaseSend()
		require.FailNow(t, "stalled session cancellation did not time out")
	}
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
	session.releaseSend()
}

func TestRecordingSessionFailsWhenAbortExceedsTerminationTimeout(t *testing.T) {
	t.Parallel()

	abortRelease := make(chan struct{})
	session := newRecoveringInternalSession(nil)
	session.abortBlock = abortRelease
	t.Cleanup(func() {
		close(abortRelease)
		session.releaseSend()
	})
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.Error(t, err)
	require.ErrorContains(t, err, "timed out aborting stalled session")
	select {
	case <-session.abortStarted:
	default:
		require.Fail(t, "abort was not attempted")
	}
	assert.Zero(t, session.resumeCalls)
	require.NoError(t, recorded.Close(t.Context()))
	assert.Zero(t, session.closeCalls)
}

func TestRecordingSessionBoundsWaiterTerminationWhenStallLoggingFails(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	session := newRecoveringInternalSession(func(sdk.SessionEventHandler) error {
		return recorder.Close()
	})
	session.ignoreCancellation = true
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.Error(t, err)
	require.ErrorContains(t, err, "debug recorder is closed")
	require.ErrorContains(t, err, "timed out waiting for canceled session work to stop")
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
	session.releaseSend()
}

func TestRecordingSessionFailsWhenActiveToolOutlivesTerminationTimeout(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(func(handler sdk.SessionEventHandler) error {
		handler(sdk.SessionEvent{ID: "tool-start", Data: &sdk.ToolExecutionStartData{
			ToolCallID: "fetch-1", ToolName: "fetch",
		}})
		return nil
	})
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.Error(t, err)
	require.ErrorContains(t, err, "timed out waiting for stalled session work to stop")
	assert.Equal(t, 1, session.abortCalls)
	assert.Zero(t, session.resumeCalls)
}

func TestRecordingSessionWaitsForActiveToolToFinishBeforeResume(t *testing.T) {
	t.Parallel()

	session := &toolCompletingInternalSession{}
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 100 * time.Millisecond,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	require.NotNil(t, result)
	abortCalls, resumeCalls := session.calls()
	assert.Equal(t, 1, abortCalls)
	assert.Equal(t, 1, resumeCalls)
	assert.False(t, session.resumedBeforeToolFinished.Load())
}

func TestRecordingSessionWaitsForTypedToolHandlerWithoutSDKEventsBeforeResume(t *testing.T) {
	t.Parallel()

	activity := newTypedToolActivity()
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	tools := []sdk.Tool{{
		Name: "typed_fetch",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			close(toolStarted)
			<-releaseTool
			return sdk.ToolResult{TextResultForLLM: "done", ResultType: "success"}, nil
		},
	}}
	trackTypedToolActivity(tools, activity)
	session := newDirectToolInternalSession(tools[0], toolStarted, releaseTool)
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 100 * time.Millisecond,
		typedToolActivity: activity,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	require.NotNil(t, result)
	abortCalls, resumeCalls := session.calls()
	assert.Equal(t, 1, abortCalls)
	assert.Equal(t, 1, resumeCalls)
	assert.False(t, session.resumedBeforeToolFinished.Load())
}

func TestRecordingSessionRejectsTypedToolStartingDuringRecovery(t *testing.T) {
	t.Parallel()

	activity := newTypedToolActivity()
	var handlerCalls atomic.Int32
	tools := []sdk.Tool{{
		Name: "typed_fetch",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			handlerCalls.Add(1)
			return sdk.ToolResult{TextResultForLLM: "done", ResultType: "success"}, nil
		},
	}}
	trackTypedToolActivity(tools, activity)
	session := newRecoveryStartingToolSession(tools[0])
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 100 * time.Millisecond,
		typedToolActivity: activity,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.ErrorContains(t, session.recoveryToolError(), "session recovery is in progress")
	require.NoError(t, session.continuationToolError())
	assert.Equal(t, int32(1), handlerCalls.Load())
}

func TestRecordingSessionFailsWhenContinuationStalls(t *testing.T) {
	t.Parallel()

	session := newRecoveringInternalSession(nil)
	session.stallAfterResume = true
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
		stallTimeout: 5 * time.Millisecond, terminationTimeout: 20 * time.Millisecond,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.Error(t, err)
	require.ErrorContains(t, err, "session stalled again after recovery")
	assert.Equal(t, 2, session.abortCalls)
	assert.Equal(t, 1, session.resumeCalls)
}

func TestRecordingSessionPublishesNormalizedStreamingAndUsageEvents(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	bus := debuglog.NewEventBus()
	recorder.SetEventBus(bus)
	var observed []debuglog.Event
	var observedMu sync.Mutex
	unsubscribe := bus.Subscribe(func(event debuglog.Event) {
		observedMu.Lock()
		observed = append(observed, event)
		observedMu.Unlock()
	})
	t.Cleanup(unsubscribe)
	inputTokens := int64(100)
	outputTokens := int64(25)
	reasoningTokens := int64(7)
	cacheReadTokens := int64(40)
	apiCallID := "api-call-1"
	session := &fakeInternalSession{
		events: []sdk.SessionEvent{
			{
				ID:   "reasoning-delta",
				Data: &sdk.AssistantReasoningDeltaData{DeltaContent: "inspect ", ReasoningID: "reasoning-1"},
			},
			{
				ID:   "message-delta",
				Data: &sdk.AssistantMessageDeltaData{DeltaContent: "drafting", MessageID: "message-1"},
			},
			{
				ID:   "tool-start",
				Data: &sdk.ToolExecutionStartData{ToolCallID: "tool-1", ToolName: "pplx_fetch", Arguments: map[string]any{"url": "https://example.test"}},
			},
			{
				ID: "usage",
				Data: &sdk.AssistantUsageData{
					APICallID: &apiCallID, InputTokens: &inputTokens, OutputTokens: &outputTokens,
					ReasoningTokens: &reasoningTokens, CacheReadTokens: &cacheReadTokens,
				},
			},
		},
		result: &sdk.SessionEvent{
			ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "complete"},
		},
	}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.static.source", kind: debuglog.SessionResearch,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
	require.NoError(t, err)
	observedMu.Lock()
	events := append([]debuglog.Event(nil), observed...)
	observedMu.Unlock()

	reasoning := findObservedEvent(t, events, "assistant.reasoning_delta")
	assert.Equal(t, "inspect ", reasoning.Content)
	message := findObservedEvent(t, events, "assistant.message_delta")
	assert.Equal(t, "drafting", message.Content)
	tool := findObservedEvent(t, events, "tool.execution_start")
	assert.Equal(t, "pplx_fetch", tool.ToolName)
	assert.JSONEq(t, `{"url":"https://example.test"}`, string(tool.Arguments))
	usage := findObservedEvent(t, events, "assistant.usage")
	require.NotNil(t, usage.Usage)
	assert.Equal(t, debuglog.Usage{
		APICallID: "api-call-1", InputTokens: 100, OutputTokens: 25,
		ReasoningTokens: 7, CacheReadTokens: 40,
	}, *usage.Usage)
	final := findObservedEvent(t, events, "assistant.message")
	assert.Equal(t, "complete", final.Content)
}

func findObservedEvent(t *testing.T, events []debuglog.Event, action string) debuglog.Event {
	t.Helper()
	for _, event := range events {
		if event.Action == action {
			return event
		}
	}
	require.FailNow(t, "event not observed", "action=%s events=%v", action, events)
	return debuglog.Event{}
}

func findEvent(t *testing.T, events []debuglog.Event, action string, status debuglog.EventStatus) debuglog.Event {
	t.Helper()
	for _, event := range events {
		if event.Action == action && event.Status == status {
			return event
		}
	}
	require.FailNow(t, "event not observed", "action=%s status=%s events=%v", action, status, events)
	return debuglog.Event{}
}

func TestRecordingSessionRecordsToolEventsDuringSend(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recorder.Close() })
	toolName := "apply_patch"
	turnID := "5"
	toolEvents := []sdk.SessionEvent{
		{
			ID: "tool-call-delta",
			Data: &sdk.AssistantToolCallDeltaData{
				InputDelta: "*** Begin Patch", ToolCallID: "call-1", ToolName: &toolName,
			},
		},
		{
			ID: "tool-search",
			Data: &sdk.ToolSearchActivatedData{
				Strategy: "search", ToolNames: []string{"apply_patch"},
			},
		},
		{
			ID: "tool-user-requested",
			Data: &sdk.ToolUserRequestedData{
				Arguments: "*** Begin Patch", ToolCallID: "call-1", ToolName: toolName,
			},
		},
		{
			ID: "tool-start",
			Data: &sdk.ToolExecutionStartData{
				Arguments: "", ToolCallID: "call-1", ToolName: toolName, TurnID: &turnID,
			},
		},
		{
			ID: "tool-progress",
			Data: &sdk.ToolExecutionProgressData{
				ProgressMessage: "applying patch", ToolCallID: "call-1",
			},
		},
		{
			ID: "tool-partial-result",
			Data: &sdk.ToolExecutionPartialResultData{
				PartialOutput: "updated report.md", ToolCallID: "call-1",
			},
		},
		{
			ID: "tool-complete",
			Data: &sdk.ToolExecutionCompleteData{
				Error: &sdk.ToolExecutionCompleteError{
					Message: "apply_patch requires a non-empty string input",
				},
				Success: false, ToolCallID: "call-1", TurnID: &turnID,
			},
		},
	}
	session := &fakeInternalSession{
		events: append(toolEvents, sdk.SessionEvent{
			ID:   "message-delta",
			Data: &sdk.AssistantMessageDeltaData{DeltaContent: "not logged", MessageID: "message-1"},
		}),
		result: &sdk.SessionEvent{
			ID:   "assistant-final",
			Data: &sdk.AssistantMessageData{Content: "research complete"},
		},
	}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	_, err = recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
	require.NoError(t, err)
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.Len(t, lines, len(toolEvents)+7)
	events := make([]debuglog.Event, 0, len(lines))
	for _, line := range lines {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		events = append(events, event)
	}
	toolEventsRecorded := make([]debuglog.Event, 0, len(toolEvents))
	for _, event := range events {
		if event.Kind == debuglog.EventTool {
			toolEventsRecorded = append(toolEventsRecorded, event)
		}
	}
	require.Len(t, toolEventsRecorded, len(toolEvents))
	for index, sdkEvent := range toolEvents {
		event := toolEventsRecorded[index]
		assert.Equal(t, string(sdkEvent.Type()), event.Action)
		expected, marshalErr := json.Marshal(sdkEvent)
		require.NoError(t, marshalErr)
		assert.JSONEq(t, string(expected), string(event.SDKEvent))
	}
}

func TestRecordingSessionReturnsReasoningLogFailure(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recorder.Close() })
	session := &fakeInternalSession{
		beforeEvents: func() { require.NoError(t, recorder.Close()) },
		events: []sdk.SessionEvent{{
			ID: "reasoning-final",
			Data: &sdk.AssistantReasoningData{
				Content: "inspect sources", ReasoningID: "reasoning-1",
			},
		}},
		result: &sdk.SessionEvent{ID: "assistant-final"},
	}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.ErrorContains(t, err, "debug recorder is closed")
	assert.Nil(t, result)
	assert.Equal(t, 1, session.unsubscribeCalls)
}

func TestRecordingSessionReturnsToolEventEncodingFailure(t *testing.T) {
	t.Parallel()

	recorder, err := debuglog.NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recorder.Close() })
	session := &fakeInternalSession{
		events: []sdk.SessionEvent{{
			ID: "tool-start",
			Data: &sdk.ToolExecutionStartData{
				Arguments: make(chan int), ToolCallID: "call-1", ToolName: "apply_patch",
			},
		}},
		result: &sdk.SessionEvent{ID: "assistant-final"},
	}
	recorded := &recordingSession{
		Session: session, recorder: recorder, address: "research.source", kind: debuglog.SessionResearch,
	}

	result, err := recorded.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.ErrorContains(t, err, "encode tool event")
	require.ErrorContains(t, err, "unsupported type: chan int")
	assert.Nil(t, result)
	assert.Equal(t, 1, session.unsubscribeCalls)
}

type closingRecorderOpener struct {
	recorder *debuglog.Recorder
	session  Session
}

func (o *closingRecorderOpener) Open(context.Context, copilot.SessionConfig) (Session, error) {
	if err := o.recorder.Close(); err != nil {
		return nil, err
	}
	return o.session, nil
}

type fakeInternalSession struct {
	closeCalls       int
	events           []sdk.SessionEvent
	result           *sdk.SessionEvent
	handler          sdk.SessionEventHandler
	beforeEvents     func()
	unsubscribeCalls int
}

type recoveringInternalSession struct {
	mu                 sync.Mutex
	onSend             func(sdk.SessionEventHandler) error
	handler            sdk.SessionEventHandler
	release            chan struct{}
	releaseOnce        sync.Once
	resumed            chan struct{}
	resumeOnce         sync.Once
	abortCalls         int
	resumeCalls        int
	idleOnAbort        bool
	ignoreCancellation bool
	immediateResult    bool
	abortErr           error
	resumeErr          error
	abortBlock         <-chan struct{}
	abortStarted       chan struct{}
	abortStartedOnce   sync.Once
	resumeBlock        bool
	resumeStarted      chan struct{}
	resumeStartedOnce  sync.Once
	stallAfterResume   bool
	closeCalls         int
	subscribeCalls     int
	sendCalls          int
	prompts            []string
}

type toolCompletingInternalSession struct {
	mu                        sync.Mutex
	handler                   sdk.SessionEventHandler
	sendCalls                 int
	abortCalls                int
	resumeCalls               int
	toolFinished              atomic.Bool
	resumedBeforeToolFinished atomic.Bool
}

type blockingCloseInternalSession struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

type directToolInternalSession struct {
	mu                        sync.Mutex
	tool                      sdk.Tool
	toolStarted               <-chan struct{}
	releaseTool               chan struct{}
	releaseOnce               sync.Once
	handlerDone               chan struct{}
	sendCalls                 int
	abortCalls                int
	resumeCalls               int
	resumedBeforeToolFinished atomic.Bool
}

type recoveryStartingToolSession struct {
	mu                  sync.Mutex
	tool                sdk.Tool
	sendCalls           int
	abortCalls          int
	resumeCalls         int
	recoveryToolErr     error
	continuationToolErr error
}

type lateCallbackInternalSession struct {
	mu                  sync.Mutex
	handlers            []sdk.SessionEventHandler
	sendCalls           int
	releaseLateCallback chan struct{}
	releaseLateOnce     sync.Once
	lateCallbackDone    chan struct{}
}

type fakeStoppableClient struct {
	stopErr    error
	forceCalls atomic.Int32
}

type blockingStoppableClient struct {
	releaseStop  chan struct{}
	releaseOnce  sync.Once
	forceCalled  chan struct{}
	forceOnce    sync.Once
	stopFinished chan struct{}
}

// completionRaceContext models cancellation after select commits to a completed send.
type completionRaceContext struct {
	context.Context
	done <-chan struct{}
}

func (c completionRaceContext) Done() <-chan struct{} { return c.done }

func (completionRaceContext) Err() error { return context.Canceled }

func newRecoveringInternalSession(onSend func(sdk.SessionEventHandler) error) *recoveringInternalSession {
	return &recoveringInternalSession{
		onSend: onSend, release: make(chan struct{}), resumed: make(chan struct{}),
		abortStarted: make(chan struct{}), resumeStarted: make(chan struct{}),
	}
}

func newDirectToolInternalSession(
	tool sdk.Tool,
	toolStarted <-chan struct{},
	releaseTool chan struct{},
) *directToolInternalSession {
	return &directToolInternalSession{
		tool: tool, toolStarted: toolStarted, releaseTool: releaseTool, handlerDone: make(chan struct{}),
	}
}

func newRecoveryStartingToolSession(tool sdk.Tool) *recoveryStartingToolSession {
	return &recoveryStartingToolSession{tool: tool}
}

func newLateCallbackInternalSession() *lateCallbackInternalSession {
	return &lateCallbackInternalSession{
		releaseLateCallback: make(chan struct{}),
		lateCallbackDone:    make(chan struct{}),
	}
}

func newBlockingStoppableClient() *blockingStoppableClient {
	return &blockingStoppableClient{
		releaseStop: make(chan struct{}), forceCalled: make(chan struct{}), stopFinished: make(chan struct{}),
	}
}

func (s *recoveringInternalSession) SendAndWait(
	ctx context.Context,
	options sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.prompts = append(s.prompts, options.Prompt)
	immediateResult := s.immediateResult
	s.mu.Unlock()
	if immediateResult {
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "complete"}}, nil
	}
	if call > 1 && !s.stallAfterResume {
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "complete"}}, nil
	}
	if call == 1 && s.onSend != nil {
		if err := s.onSend(s.handler); err != nil {
			return nil, err
		}
	}
	select {
	case <-s.release:
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "complete"}}, nil
	case <-ctx.Done():
		if s.ignoreCancellation {
			<-s.release
			return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "complete"}}, nil
		}
		return nil, fmt.Errorf("waiting for session.idle: %w", ctx.Err())
	}
}

func (s *recoveringInternalSession) On(handler sdk.SessionEventHandler) func() {
	s.subscribeCalls++
	s.handler = handler
	return func() {}
}

func (s *recoveringInternalSession) Abort(context.Context) error {
	if s.abortBlock != nil {
		s.abortStartedOnce.Do(func() { close(s.abortStarted) })
		<-s.abortBlock
	}
	s.abortCalls++
	if s.abortErr != nil {
		return s.abortErr
	}
	if s.idleOnAbort {
		s.handler(sdk.SessionEvent{ID: "session-idle", Data: &sdk.SessionIdleData{}})
		s.releaseSend()
	}
	return nil
}

func (s *recoveringInternalSession) Resume(ctx context.Context) error {
	s.resumeCalls++
	s.resumeOnce.Do(func() { close(s.resumed) })
	if s.resumeBlock {
		s.resumeStartedOnce.Do(func() { close(s.resumeStarted) })
		<-ctx.Done()
		return ctx.Err()
	}
	return s.resumeErr
}

func (s *recoveringInternalSession) releaseSend() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func (s *recoveringInternalSession) Close(context.Context) error {
	s.closeCalls++
	return nil
}

func (s *toolCompletingInternalSession) SendAndWait(
	ctx context.Context,
	_ sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	handler := s.handler
	s.mu.Unlock()
	if call > 1 {
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "done"}}, nil
	}
	handler(sdk.SessionEvent{ID: "tool-start", Data: &sdk.ToolExecutionStartData{
		ToolCallID: "fetch-1", ToolName: "fetch",
	}})
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *toolCompletingInternalSession) On(handler sdk.SessionEventHandler) func() {
	s.mu.Lock()
	s.handler = handler
	s.mu.Unlock()
	return func() {}
}

func (s *toolCompletingInternalSession) Abort(context.Context) error {
	s.mu.Lock()
	s.abortCalls++
	handler := s.handler
	s.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		s.toolFinished.Store(true)
		handler(sdk.SessionEvent{ID: "tool-complete", Data: &sdk.ToolExecutionCompleteData{
			ToolCallID: "fetch-1", Success: true,
		}})
	}()
	return nil
}

func (s *toolCompletingInternalSession) Resume(context.Context) error {
	if !s.toolFinished.Load() {
		s.resumedBeforeToolFinished.Store(true)
	}
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return nil
}

func (*toolCompletingInternalSession) Close(context.Context) error { return nil }

func (s *toolCompletingInternalSession) calls() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortCalls, s.resumeCalls
}

func (*blockingCloseInternalSession) SendAndWait(
	context.Context,
	sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	return nil, nil
}

func (s *blockingCloseInternalSession) Close(context.Context) error {
	close(s.started)
	<-s.release
	close(s.finished)
	return nil
}

func (s *directToolInternalSession) SendAndWait(
	ctx context.Context,
	_ sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.mu.Unlock()
	if call > 1 {
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "done"}}, nil
	}
	go func() {
		defer close(s.handlerDone)
		_, _ = s.tool.Handler(sdk.ToolInvocation{})
	}()
	<-s.toolStarted
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*directToolInternalSession) On(sdk.SessionEventHandler) func() { return func() {} }

func (s *directToolInternalSession) Abort(context.Context) error {
	s.mu.Lock()
	s.abortCalls++
	s.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		s.releaseOnce.Do(func() { close(s.releaseTool) })
	}()
	return nil
}

func (s *directToolInternalSession) Resume(context.Context) error {
	select {
	case <-s.handlerDone:
	default:
		s.resumedBeforeToolFinished.Store(true)
	}
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return nil
}

func (*directToolInternalSession) Close(context.Context) error { return nil }

func (s *directToolInternalSession) calls() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortCalls, s.resumeCalls
}

func (s *recoveryStartingToolSession) SendAndWait(
	ctx context.Context,
	_ sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.mu.Unlock()
	if call == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, toolErr := s.tool.Handler(sdk.ToolInvocation{})
	s.mu.Lock()
	s.continuationToolErr = toolErr
	s.mu.Unlock()
	return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "done"}}, nil
}

func (*recoveryStartingToolSession) On(sdk.SessionEventHandler) func() { return func() {} }

func (s *recoveryStartingToolSession) Abort(context.Context) error {
	_, toolErr := s.tool.Handler(sdk.ToolInvocation{})
	s.mu.Lock()
	s.abortCalls++
	s.recoveryToolErr = toolErr
	s.mu.Unlock()
	return nil
}

func (s *recoveryStartingToolSession) Resume(context.Context) error {
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return nil
}

func (*recoveryStartingToolSession) Close(context.Context) error { return nil }

func (s *recoveryStartingToolSession) recoveryToolError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoveryToolErr
}

func (s *recoveryStartingToolSession) continuationToolError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.continuationToolErr
}

func (s *lateCallbackInternalSession) SendAndWait(
	ctx context.Context,
	_ sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.mu.Unlock()
	if call > 1 {
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "done"}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *lateCallbackInternalSession) On(handler sdk.SessionEventHandler) func() {
	s.mu.Lock()
	s.handlers = append(s.handlers, handler)
	s.mu.Unlock()
	return func() {}
}

func (s *lateCallbackInternalSession) Abort(context.Context) error {
	s.mu.Lock()
	handler := s.handlers[0]
	s.mu.Unlock()
	go func() {
		defer close(s.lateCallbackDone)
		<-s.releaseLateCallback
		handler(sdk.SessionEvent{ID: "late-tool-start", Data: &sdk.ToolExecutionStartData{
			ToolCallID: "late-tool", ToolName: "fetch",
		}})
	}()
	return nil
}

func (s *lateCallbackInternalSession) releaseLate() {
	s.releaseLateOnce.Do(func() { close(s.releaseLateCallback) })
}

func (*lateCallbackInternalSession) Resume(context.Context) error { return nil }

func (*lateCallbackInternalSession) Close(context.Context) error { return nil }

func (c *fakeStoppableClient) Stop() error { return c.stopErr }

func (c *fakeStoppableClient) ForceStop() { c.forceCalls.Add(1) }

func (c *blockingStoppableClient) Stop() error {
	defer close(c.stopFinished)
	<-c.releaseStop
	return nil
}

func (c *blockingStoppableClient) ForceStop() {
	c.forceOnce.Do(func() { close(c.forceCalled) })
	c.releaseOnce.Do(func() { close(c.releaseStop) })
}

func (s *fakeInternalSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if s.beforeEvents != nil {
		s.beforeEvents()
	}
	for _, event := range s.events {
		if s.handler != nil {
			s.handler(event)
		}
	}
	if s.result != nil {
		return s.result, nil
	}
	return &sdk.SessionEvent{}, nil
}

func (s *fakeInternalSession) On(handler sdk.SessionEventHandler) func() {
	s.handler = handler
	return func() { s.unsubscribeCalls++ }
}

func (s *fakeInternalSession) Close(context.Context) error {
	s.closeCalls++
	return nil
}
