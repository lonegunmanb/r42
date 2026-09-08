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

func TestCheckpointRecorderAcceptsExactlyOneCheckpointPerRound(t *testing.T) {
	t.Parallel()

	recorder := collection.NewCheckpointRecorder()
	checkpoint := collection.CheckpointOutput{ArtifactIDs: []string{"artifact-1"}}

	require.NoError(t, recorder.Record(checkpoint))
	require.ErrorContains(t, recorder.Record(checkpoint), "exactly once")
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
	assert.Contains(t, session.prompts[1], `Call "r42_read_information_needs" before choosing a search direction`)
	assert.Contains(t, session.prompts[1], "No configured Collection acquisition tool is available")
}

func TestRunnerUsesTargetedCheckpointRetryPrompt(t *testing.T) {
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
		ActiveInformationNeedStates: []collection.ActiveInformationNeedState{{
			InformationNeed: collection.InformationNeed{
				ID:       "NEED-003",
				Question: "建立事件前资本充足率、盈利、资产规模和监管制度背景基线",
				StopConditions: []collection.StopCondition{{
					ID:        "NEED-003-SC-001",
					Condition: "至少有监管制度或官方文件及主要机构年报/公告数据",
				}},
			},
			UnsatisfiedConditionIDs: []string{"NEED-003-SC-001"},
		}},
		CollectionToolNames: []string{"pplx_pro_search", "pplx_fetch"},
	})

	require.NoError(t, err)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "NEED-003")
	assert.Contains(t, session.prompts[1], "监管制度或官方文件及主要机构年报/公告数据")
	assert.Contains(t, session.prompts[1], collection.ReadInformationNeedsToolName)
	assert.Contains(t, session.prompts[1], "pplx_pro_search")
	assert.Contains(t, session.prompts[1], "r42_save_artifact")
	assert.Contains(t, session.prompts[1], "r42_register_artifact")
	assert.Contains(t, session.prompts[1], "continue")
	assert.Contains(t, session.prompts[1], "stalled")
	assert.Contains(t, session.prompts[1], "empty_reason")
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
