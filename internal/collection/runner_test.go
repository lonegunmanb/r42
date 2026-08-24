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
		return recorder.Record(collection.CheckpointOutput{ArtifactIDs: []string{"artifact-1"}})
	}}
	runner := collection.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), collection.RunConfig{
		InitialPrompt:       "collect evidence",
		MaxProtocolAttempts: 3,
		CheckpointToolName:  "r42_collection_checkpoint",
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"artifact-1"}, result.ArtifactIDs)
	assert.Equal(t, []string{"collect evidence"}, session.prompts)
}

func TestRunnerMergesAllRecordedCheckpoints(t *testing.T) {
	t.Parallel()

	recorder := collection.NewCheckpointRecorder()
	session := &runnerSession{onSend: func(int) error {
		require.NoError(t, recorder.Record(collection.CheckpointOutput{
			ArtifactIDs: []string{"artifact-1", "artifact-2"},
		}))
		require.NoError(t, recorder.Record(collection.CheckpointOutput{
			ArtifactIDs: []string{"artifact-2", "artifact-3"},
		}))
		return recorder.Record(collection.CheckpointOutput{
			EmptyReason:         "supplementary search found no additional sources",
			CollectionExhausted: true,
		})
	}}
	runner := collection.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), collection.RunConfig{
		InitialPrompt:       "collect evidence",
		MaxProtocolAttempts: 3,
		CheckpointToolName:  "r42_collection_checkpoint",
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"artifact-1", "artifact-2", "artifact-3"}, result.ArtifactIDs)
	assert.Equal(t, "supplementary search found no additional sources", result.EmptyReason)
	assert.True(t, result.CollectionExhausted)
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
