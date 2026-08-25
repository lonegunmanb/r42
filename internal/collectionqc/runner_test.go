package collectionqc_test

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRunnerReviewsCheckpointAndAdvancesWatermark(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{
			{InformationNeedID: "NEED-001", Status: collection.AssessmentSufficient, EvidenceProgress: collection.EvidenceProgressMaterial},
		}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))

	require.NoError(t, err)
	assert.Len(t, result.Verdict.Assessments, 1)
	assert.Equal(t, 1, collectionContext.State.Cursor())
	assert.ElementsMatch(t, checkpoint.ArtifactIDs, collectionContext.ReviewedEvidenceArtifactIDs())
	assert.Equal(t, "research", collectionContext.State.Phase().String())
	require.Len(t, session.prompts, 1)
	var document map[string]any
	require.NoError(t, json.Unmarshal([]byte(session.prompts[0]), &document))
	assert.Contains(t, document, "criteria")
	assert.Contains(t, session.prompts[0], "information_need_outcomes")
}

func TestRunnerNeedsMoreReturnsToCollectionWithActiveNeeds(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{
			{InformationNeedID: "NEED-001", Status: collection.AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: collection.EvidenceProgressNone},
		}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))

	require.NoError(t, err)
	assert.False(t, result.CollectionLimitExhausted)
	assert.Equal(t, "collection", collectionContext.State.Phase().String())
	assert.Equal(t, 2, collectionContext.State.CollectionRoundsUsed())
	require.Len(t, result.ActiveInformationNeeds, 1)
	assert.Equal(t, "NEED-001", result.ActiveInformationNeeds[0].ID)
	assert.Empty(t, result.Outcomes)
}

func TestRunnerCarriesRemainingConditionIDsIntoNextQCContext(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	call := 0
	session := &fakeSession{onSend: func(string) error {
		call++
		status := collection.AssessmentNeedsMore
		conditions := []string{"NEED-001-SC-001"}
		if call == 2 {
			status = collection.AssessmentSufficient
			conditions = nil
		}
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{{
			InformationNeedID: "NEED-001", Status: status,
			UnsatisfiedConditionIDs: conditions, EvidenceProgress: collection.EvidenceProgressNone,
		}}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	_, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))
	require.NoError(t, err)
	secondCheckpoint := collection.NewCheckpointHandler(collectionContext).Submit(collection.CheckpointArgs{
		EmptyReason: "no new source",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	require.True(t, secondCheckpoint.Accepted)
	require.NoError(t, collectionContext.State.Advance("checkpoint"))
	_, err = runner.Review(t.Context(), validConfig(nil))
	require.NoError(t, err)
	require.Len(t, session.prompts, 2)

	var document struct {
		ActiveStates []collection.ActiveInformationNeedState `json:"active_information_need_states"`
	}
	require.NoError(t, json.Unmarshal([]byte(session.prompts[1]), &document))
	require.Len(t, document.ActiveStates, 1)
	assert.Equal(t, []string{"NEED-001-SC-001"}, document.ActiveStates[0].UnsatisfiedConditionIDs)
}

func TestRunnerCarriesBudgetExhaustionToResearch(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, intPointer(1))
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{
			{InformationNeedID: "NEED-001", Status: collection.AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: collection.EvidenceProgressNone},
		}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))

	require.NoError(t, err)
	assert.True(t, result.CollectionLimitExhausted)
	assert.Equal(t, "research", collectionContext.State.Phase().String())
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, collection.NeedResolutionUnresolved, result.Outcomes[0].Resolution)
	assert.Equal(t, collection.TerminationBudgetExhausted, result.Outcomes[0].TerminationReason)
}

func TestRunnerDoesNotReportBudgetExhaustionWhenLastRoundIsSufficient(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, intPointer(1))
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{{
			InformationNeedID: "NEED-001",
			Status:            collection.AssessmentSufficient,
			EvidenceProgress:  collection.EvidenceProgressMaterial,
		}}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))

	require.NoError(t, err)
	assert.False(t, result.CollectionLimitExhausted)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, collection.NeedResolutionSatisfied, result.Outcomes[0].Resolution)
}

func TestRunnerStoresOutcomesForLaterPhases(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, intPointer(1))
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{
			{InformationNeedID: "NEED-001", Status: collection.AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: collection.EvidenceProgressNone},
		}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	_, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))

	require.NoError(t, err)
	stored := collectionContext.State.InformationNeedOutcomes()
	require.NotEmpty(t, stored)
	var outcomes []collection.InformationNeedOutcome
	require.NoError(t, json.Unmarshal(stored, &outcomes))
	require.Len(t, outcomes, 1)
	assert.Equal(t, collection.NeedResolutionUnresolved, outcomes[0].Resolution)
	assert.Equal(t, collection.TerminationBudgetExhausted, outcomes[0].TerminationReason)
}

func TestRunnerMalformedVerdictDoesNotAdvanceWatermark(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	runner := collectionqc.NewRunner(&fakeSession{}, verdicts, collectionContext)
	config := validConfig(checkpoint.ArtifactIDs)
	config.MaxProtocolAttempts = 1

	_, err := runner.Review(t.Context(), config)

	require.ErrorContains(t, err, "collection qc verdict protocol attempts exhausted")
	assert.Zero(t, collectionContext.State.Cursor())
	assert.Empty(t, collectionContext.ReviewedEvidenceArtifactIDs())
	assert.Equal(t, "collection_qc", collectionContext.State.Phase().String())
}

func TestRunnerAcceptsRepairAfterInvalidVerdictInSameTurn(t *testing.T) {
	t.Parallel()

	collectionContext, checkpoint := checkpointWithEvidenceArtifact(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	require.Error(t, verdicts.Record(collectionqc.Verdict{}))
	require.NoError(t, verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{
		{InformationNeedID: "NEED-001", Status: collection.AssessmentSufficient, EvidenceProgress: collection.EvidenceProgressMaterial},
	}}))
	runner := collectionqc.NewRunner(&fakeSession{}, verdicts, collectionContext)

	result, err := runner.Review(t.Context(), validConfig(checkpoint.ArtifactIDs))

	require.NoError(t, err)
	assert.Len(t, result.Verdict.Assessments, 1)
	assert.Equal(t, 1, collectionContext.State.Cursor())
}

func TestRunnerRejectsMaterialProgressWithoutNewArtifacts(t *testing.T) {
	t.Parallel()

	// An empty checkpoint (no artifact IDs) cannot claim material progress.
	collectionContext, _ := checkpointWithEmptyRound(t, nil)
	verdicts := collectionqc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(string) error {
		return verdicts.Record(collectionqc.Verdict{Assessments: []collection.QCAssessment{
			{InformationNeedID: "NEED-001", Status: collection.AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: collection.EvidenceProgressMaterial},
		}})
	}}
	runner := collectionqc.NewRunner(session, verdicts, collectionContext)

	_, err := runner.Review(t.Context(), validConfig(nil))

	require.ErrorContains(t, err, "evidence_progress")
	assert.Equal(t, "collection_qc", collectionContext.State.Phase().String())
}

func checkpointWithEmptyRound(t *testing.T, maxRounds *int) (*collection.Context, collection.CheckpointOutput) {
	t.Helper()
	context := collection.NewContext(t.TempDir(), 10, maxRounds)
	plan := collection.NewInformationNeedsHandler(context).Set(collection.InformationNeedsArgs{InformationNeeds: []collection.InformationNeedInput{{
		Question:       "Which qualified suppliers could serve the product?",
		StopConditions: []collection.StopConditionInput{{Condition: "A primary source identifies a supplier relationship."}},
	}}})
	require.True(t, plan.Accepted)
	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no sources found",
		NeedDispositions: []collection.NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionStalled},
		},
	})
	require.True(t, checkpoint.Accepted)
	require.NotNil(t, checkpoint.Output)
	require.NoError(t, context.State.Advance("checkpoint"))
	return context, *checkpoint.Output
}

func TestVerdictValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		verdict collectionqc.Verdict
		error   string
	}{
		{name: "valid assessment", verdict: collectionqc.Verdict{Assessments: []collection.QCAssessment{{InformationNeedID: "NEED-001"}}}},
		{name: "empty", verdict: collectionqc.Verdict{}, error: "must contain at least one assessment"},
		{name: "blank need id", verdict: collectionqc.Verdict{Assessments: []collection.QCAssessment{{InformationNeedID: " "}}}, error: "information_need_id is required"},
		{name: "duplicate need id", verdict: collectionqc.Verdict{Assessments: []collection.QCAssessment{
			{InformationNeedID: "NEED-001"}, {InformationNeedID: "NEED-001"},
		}}, error: "appears more than once"},
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

func TestVerdictRecorderAcceptsExactlyOneVerdictPerRound(t *testing.T) {
	t.Parallel()

	recorder := collectionqc.NewVerdictRecorder()
	verdict := collectionqc.Verdict{Assessments: []collection.QCAssessment{{
		InformationNeedID: "NEED-001",
		Status:            collection.AssessmentSufficient,
		EvidenceProgress:  collection.EvidenceProgressMaterial,
	}}}

	require.NoError(t, recorder.Record(verdict))
	require.ErrorContains(t, recorder.Record(verdict), "exactly once")
}

func checkpointWithEvidenceArtifact(t *testing.T, maxRounds *int) (*collection.Context, collection.CheckpointOutput) {
	t.Helper()
	context := collection.NewContext(t.TempDir(), 10, maxRounds)
	require.NoError(t, context.Artifacts.RetainToolResult("call-1", "source evidence"))
	plan := collection.NewInformationNeedsHandler(context).Set(collection.InformationNeedsArgs{InformationNeeds: []collection.InformationNeedInput{{
		Question:       "Which qualified suppliers could serve the product?",
		StopConditions: []collection.StopConditionInput{{Condition: "A primary source identifies a supplier relationship."}},
	}}})
	require.True(t, plan.Accepted)
	registered := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{SourceToolCallID: "call-1"})
	require.True(t, registered.Accepted)
	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		NeedDispositions: []collection.NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue},
		},
	})
	require.True(t, checkpoint.Accepted)
	require.NotNil(t, checkpoint.Output)
	require.NoError(t, context.State.Advance("checkpoint"))
	return context, *checkpoint.Output
}

func validConfig(artifactIDs []string) collectionqc.Config {
	return collectionqc.Config{
		Task:                  collectionqc.Task{SystemPrompt: "research carefully", Prompt: stringPointer("investigate demand")},
		Criteria:              cty.NilVal,
		CheckpointArtifactIDs: artifactIDs,
		MaxProtocolAttempts:   2,
		VerdictToolName:       "r42_collection_qc_verdict",
		NeedDispositions: []collection.NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue},
		},
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
