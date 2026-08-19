package collection_test

import (
	"context"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerReturnsRecordedCheckpoint(t *testing.T) {
	t.Parallel()

	recorder := collection.NewCheckpointRecorder()
	session := &runnerSession{onSend: func(int) error {
		return recorder.Record(collection.CheckpointOutput{SnapshotIDs: []string{"snapshot-1"}})
	}}
	runner := collection.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), collection.RunConfig{
		InitialPrompt:       "collect evidence",
		MaxProtocolAttempts: 3,
		CheckpointToolName:  "r42_collection_checkpoint",
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"snapshot-1"}, result.SnapshotIDs)
	assert.Equal(t, []string{"collect evidence"}, session.prompts)
}

func TestRunnerRequiresCheckpointAndReusesSession(t *testing.T) {
	t.Parallel()

	recorder := collection.NewCheckpointRecorder()
	session := &runnerSession{onSend: func(call int) error {
		if call == 2 {
			return recorder.Record(collection.CheckpointOutput{})
		}
		return nil
	}}
	runner := collection.NewRunner(session, recorder)

	_, err := runner.Run(t.Context(), collection.RunConfig{
		InitialPrompt:       "collect evidence",
		MaxProtocolAttempts: 2,
		CheckpointToolName:  "r42_collection_checkpoint",
	})

	require.NoError(t, err)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "r42_collection_checkpoint")
}

func TestRunnerStopsAtCheckpointProtocolLimit(t *testing.T) {
	t.Parallel()

	session := &runnerSession{}
	runner := collection.NewRunner(session, collection.NewCheckpointRecorder())

	_, err := runner.Run(t.Context(), collection.RunConfig{
		InitialPrompt:       "collect evidence",
		MaxProtocolAttempts: 2,
		CheckpointToolName:  "r42_collection_checkpoint",
	})

	require.ErrorContains(t, err, "collection checkpoint protocol attempts exhausted")
	assert.Len(t, session.prompts, 2)
}

type runnerSession struct {
	prompts []string
	onSend  func(int) error
}

func (s *runnerSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.prompts = append(s.prompts, options.Prompt)
	if s.onSend != nil {
		return nil, s.onSend(len(s.prompts))
	}
	return nil, nil
}
