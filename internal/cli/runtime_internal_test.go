package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

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
	assert.ErrorContains(t, quota.reserve("tool_lookup"), `typed tool "tool_lookup" call quota exhausted (limit 1)`)
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
	assert.ErrorContains(t, err, `typed tool "tool_lookup" call quota exhausted (limit 1)`)
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
		assert.ErrorContains(t, err, `typed tool "tool_lookup" call quota exhausted (limit 1)`)
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
	require.Len(t, lines, 7)

	wantTypes := []sdk.SessionEventType{
		sdk.SessionEventTypeAssistantMessage,
		sdk.SessionEventTypeAssistantReasoningDelta,
		sdk.SessionEventTypeAssistantReasoning,
		sdk.SessionEventTypeAssistantMessage,
	}
	for index, wantType := range wantTypes {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(lines[index+2]), &event))
		assert.Equal(t, debuglog.EventMessage, event.Kind)
		assert.Equal(t, debuglog.RoleAssistant, event.Role)
		var sessionEvent sdk.SessionEvent
		require.NoError(t, json.Unmarshal(event.SDKEvent, &sessionEvent))
		assert.Equal(t, wantType, sessionEvent.Type())
	}
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
	require.Len(t, lines, len(toolEvents)+5)
	for index, sdkEvent := range toolEvents {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(lines[index+2]), &event))
		assert.Equal(t, debuglog.EventTool, event.Kind)
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
