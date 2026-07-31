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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
