package collectionqc_test

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRunnerReviewsCheckpointAndAdvancesWatermark(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithSnapshot(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Decision: collectionqc.DecisionSufficient})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.SnapshotIDs))

	require.NoError(t, err)
	assert.Equal(t, collectionqc.DecisionSufficient, result.Verdict.Decision)
	assert.Equal(t, 1, collectionContext.State.Cursor())
	assert.ElementsMatch(t, checkpoint.SnapshotIDs, collectionContext.Registry.ReviewedSnapshotIDs())
	assert.Equal(t, "research", collectionContext.State.Phase().String())
	require.Len(t, session.prompts, 1)
	var document map[string]any
	require.NoError(t, json.Unmarshal([]byte(session.prompts[0]), &document))
	assert.Contains(t, document, "criteria")
	assert.Contains(t, session.prompts[0], "sufficiency")
}

func TestRunnerNeedsMoreReturnsToCollectionAndCarriesIssues(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithSnapshot(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	issue := corespec.Issue{
		Code: "coverage", Message: "current snapshots do not establish demand",
		RepairHint: stringPointer("find regional demand evidence"),
	}
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{
			Decision: collectionqc.DecisionNeedsMore,
			Issues:   []corespec.Issue{issue},
		})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.SnapshotIDs))

	require.NoError(t, err)
	assert.False(t, result.CollectionLimitExhausted)
	assert.Equal(t, "collection", collectionContext.State.Phase().String())
	assert.Equal(t, 2, collectionContext.State.CollectionRoundsUsed())
	assert.Equal(t, []string{issue.Message}, collectionContext.State.LastCollectionQCIssues())
}

func TestRunnerCarriesIssuesToResearchWhenCollectionBudgetIsExhausted(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithSnapshot(t, intPointer(1))
	verdicts := collectionqc.NewVerdictRecorder()
	issue := corespec.Issue{Code: "coverage", Message: "evidence remains incomplete"}
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{
			Decision: collectionqc.DecisionNeedsMore,
			Issues:   []corespec.Issue{issue},
		})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.SnapshotIDs))

	require.NoError(t, err)
	assert.True(t, result.CollectionLimitExhausted)
	assert.Equal(t, "research", collectionContext.State.Phase().String())
	assert.Equal(t, []string{issue.Message}, collectionContext.State.LastCollectionQCIssues())
}

func TestRunnerMalformedVerdictDoesNotAdvanceWatermark(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithSnapshot(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	runner := collectionqc.NewRunner(&fakeSession{}, verdicts, collectionContext)
	config := validConfig(checkpoint.SnapshotIDs)
	config.MaxProtocolAttempts = 1

	_, err := runner.Review(t.Context(), config)

	require.ErrorContains(t, err, "collection qc verdict protocol attempts exhausted")
	assert.Zero(t, collectionContext.State.Cursor())
	assert.Empty(t, collectionContext.Registry.ReviewedSnapshotIDs())
	assert.Equal(t, "collection_qc", collectionContext.State.Phase().String())
}

func TestRunnerAcceptsRepairAfterInvalidVerdictInSameTurn(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithSnapshot(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	require.Error(t, verdicts.Record(collectionqc.Verdict{Decision: collectionqc.DecisionNeedsMore}))
	require.NoError(t, verdicts.Record(collectionqc.Verdict{Decision: collectionqc.DecisionSufficient}))
	runner := collectionqc.NewRunner(&fakeSession{}, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.SnapshotIDs))

	require.NoError(t, err)
	assert.Equal(t, collectionqc.DecisionSufficient, result.Verdict.Decision)
	assert.Equal(t, 1, collectionContext.State.Cursor())
}

func TestVerdictValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		verdict collectionqc.Verdict
		error   string
	}{
		{name: "sufficient", verdict: collectionqc.Verdict{Decision: collectionqc.DecisionSufficient}},
		{name: "needs more", verdict: collectionqc.Verdict{Decision: collectionqc.DecisionNeedsMore, Issues: []corespec.Issue{{Code: "gap", Message: "missing evidence"}}}},
		{name: "unknown decision", verdict: collectionqc.Verdict{Decision: "later"}, error: "unsupported collection qc decision"},
		{name: "sufficient with issues", verdict: collectionqc.Verdict{Decision: collectionqc.DecisionSufficient, Issues: []corespec.Issue{{Code: "gap", Message: "missing"}}}, error: "sufficient verdict must not contain issues"},
		{name: "needs more without issues", verdict: collectionqc.Verdict{Decision: collectionqc.DecisionNeedsMore}, error: "needs_more verdict must contain at least one issue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.verdict.Validate()
			if tt.error == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.error)
		})
	}
}

func checkpointWithSnapshot(t *testing.T, maxRounds *int) (*collection.Context, collection.CheckpointOutput) {
	t.Helper()
	context := collection.NewContext(t.TempDir(), 10, maxRounds)
	require.NoError(t, context.Registry.RetainToolResult("call-1", "source evidence"))
	registered := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{SourceToolCallID: "call-1"})
	require.True(t, registered.Accepted)
	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{})
	require.True(t, checkpoint.Accepted)
	require.NotNil(t, checkpoint.Output)
	require.NoError(t, context.State.Advance("checkpoint"))
	return context, *checkpoint.Output
}

func validConfig(snapshotIDs []string) collectionqc.Config {
	return collectionqc.Config{
		Task:                  collectionqc.Task{SystemPrompt: "research carefully", Prompt: stringPointer("investigate demand")},
		Criteria:              cty.NilVal,
		CheckpointSnapshotIDs: snapshotIDs,
		MaxProtocolAttempts:   2,
		VerdictToolName:       "r42_collection_qc_verdict",
	}
}

type fakeSession struct {
	prompts []string
	onSend  func(string) error
}

func (s *fakeSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.prompts = append(s.prompts, options.Prompt)
	if s.onSend != nil {
		return nil, s.onSend(options.Prompt)
	}
	return nil, nil
}

func intPointer(value int) *int          { return &value }
func stringPointer(value string) *string { return &value }
