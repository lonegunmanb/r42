package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

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
	require.Len(t, lines, 6)

	wantTypes := []sdk.SessionEventType{
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
		require.NoError(t, json.Unmarshal([]byte(event.Content), &sessionEvent))
		assert.Equal(t, wantType, sessionEvent.Type())
	}
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
	require.Len(t, lines, len(toolEvents)+4)
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
