package cli

import (
	"context"
	"path/filepath"
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

type fakeInternalSession struct{ closeCalls int }

func (*fakeInternalSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	return &sdk.SessionEvent{}, nil
}

func (s *fakeInternalSession) Close(context.Context) error {
	s.closeCalls++
	return nil
}
