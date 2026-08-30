package collection

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInformationNeedsPlanFreezesBeforeCollectionTools(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	checkpoint := NewCheckpointHandler(context)
	blocked := checkpoint.Submit(CheckpointArgs{EmptyReason: "not planned"})
	assert.False(t, blocked.Accepted)
	require.NotEmpty(t, blocked.Issues)
	assert.Equal(t, "information_needs_required", blocked.Issues[0].Code)

	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{
		InformationNeeds: []InformationNeedInput{{
			Question: "Which qualified suppliers could serve the product?",
			StopConditions: []StopConditionInput{{
				Condition: "A primary source identifies a supplier relationship.",
			}},
		}},
	})
	require.True(t, plan.Accepted)
	require.NotNil(t, plan.Output)
	assert.Equal(t, "NEED-001", plan.Output.InformationNeeds[0].ID)
	assert.Equal(t, "NEED-001-SC-001", plan.Output.InformationNeeds[0].StopConditions[0].ID)

	repeated := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question: "A different question", StopConditions: []StopConditionInput{{Condition: "Different condition"}},
	}}})
	assert.False(t, repeated.Accepted)
	require.NotEmpty(t, repeated.Issues)
	assert.Equal(t, "information_needs_frozen", repeated.Issues[0].Code)
}

func TestCheckpointRequiresEachActiveNeedExactlyOnce(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{
		{Question: "supplier", StopConditions: []StopConditionInput{{Condition: "relationship"}}},
		{Question: "materiality", StopConditions: []StopConditionInput{{Condition: "economic exposure"}}},
	}})
	require.True(t, plan.Accepted)

	response := NewCheckpointHandler(context).Submit(CheckpointArgs{
		EmptyReason: "no sources found",
		NeedDispositions: []NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled,
		}},
	})
	assert.False(t, response.Accepted)
	require.NotEmpty(t, response.Issues)
	assert.Equal(t, "need_dispositions", response.Issues[0].Code)

	response = NewCheckpointHandler(context).Submit(CheckpointArgs{
		EmptyReason: "no sources found",
		NeedDispositions: []NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled},
			{InformationNeedID: "NEED-002", SearchDisposition: SearchDispositionContinue},
		},
	})
	require.True(t, response.Accepted)
	require.NotNil(t, response.Output)
	assert.Len(t, response.Output.NeedDispositions, 2)
}

func TestCheckpointUnknownInformationNeedSuggestsReadingActiveNeeds(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question:       "supplier",
		StopConditions: []StopConditionInput{{Condition: "relationship"}},
	}}})
	require.True(t, plan.Accepted)

	response := NewCheckpointHandler(context).Submit(CheckpointArgs{
		EmptyReason: "no sources found",
		NeedDispositions: []NeedDisposition{{
			InformationNeedID: "supplier", SearchDisposition: SearchDispositionStalled,
		}},
	})

	assert.False(t, response.Accepted)
	require.NotEmpty(t, response.Issues)
	assert.Contains(t, response.Issues[0].Message, "not an active frozen canonical information-need ID")
	assert.Contains(t, response.Issues[0].Message, "r42_read_information_needs")
	assert.Contains(t, response.Issues[0].Message, "active_information_need_states")
}

func TestQCAssessmentUnknownInformationNeedSuggestsReadingActiveNeeds(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question:       "supplier",
		StopConditions: []StopConditionInput{{Condition: "relationship"}},
	}}})
	require.True(t, plan.Accepted)

	issues := context.ValidateQCAssessments([]QCAssessment{{
		InformationNeedID: "supplier",
		Status:            AssessmentSufficient,
		EvidenceProgress:  EvidenceProgressMaterial,
	}}, true)

	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "not an active frozen canonical information-need ID")
	assert.Contains(t, issues[0].Message, "r42_read_information_needs")
	assert.Contains(t, issues[0].Message, "active_information_need_states")
}

func TestAssessmentsCanOnlyShrinkUnsatisfiedConditionsAndCloseStalledNeed(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question: "supplier", StopConditions: []StopConditionInput{{Condition: "relationship"}, {Condition: "qualification"}},
	}}})
	require.True(t, plan.Accepted)

	first := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001", "NEED-001-SC-002"}, EvidenceProgress: EvidenceProgressNone}},
		false,
		true,
	)
	require.True(t, first.Accepted)
	assert.False(t, first.AllTerminal)

	widened := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001", "NEED-001-SC-002", "NEED-001-SC-003"}, EvidenceProgress: EvidenceProgressNone}},
		false,
		true,
	)
	assert.False(t, widened.Accepted)
	require.NotEmpty(t, widened.Issues)
	assert.Equal(t, "unsatisfied_condition_ids", widened.Issues[0].Code)

	second := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001", "NEED-001-SC-002"}, EvidenceProgress: EvidenceProgressNone}},
		false,
		true,
	)
	require.True(t, second.Accepted)
	require.True(t, second.AllTerminal)
	require.Len(t, second.Outcomes, 1)
	assert.Equal(t, NeedResolutionUnresolved, second.Outcomes[0].Resolution)
	assert.Equal(t, TerminationSearchStalled, second.Outcomes[0].TerminationReason)
}

func TestAssessmentsRejectMaterialProgressWithoutNewArtifacts(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question: "supplier", StopConditions: []StopConditionInput{{Condition: "relationship"}},
	}}})
	require.True(t, plan.Accepted)

	// A round that added no artifacts cannot claim material evidence progress.
	rejected := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: EvidenceProgressMaterial}},
		false,
		false,
	)
	assert.False(t, rejected.Accepted)
	require.NotEmpty(t, rejected.Issues)
	assert.Equal(t, "evidence_progress", rejected.Issues[0].Code)

	// The same verdict with none progress is accepted.
	accepted := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionStalled}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: EvidenceProgressNone}},
		false,
		false,
	)
	require.True(t, accepted.Accepted)
}

func TestBudgetExhaustionMarksRemainingActiveNeeds(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{
		{Question: "supplier", StopConditions: []StopConditionInput{{Condition: "relationship"}}},
		{Question: "materiality", StopConditions: []StopConditionInput{{Condition: "economic exposure"}}},
	}})
	require.True(t, plan.Accepted)

	// Satisfy NEED-001, keep NEED-002 active, and exhaust the budget.
	result := context.ApplyQCAssessments(
		[]NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionContinue},
			{InformationNeedID: "NEED-002", SearchDisposition: SearchDispositionContinue},
		},
		[]QCAssessment{
			{InformationNeedID: "NEED-001", Status: AssessmentSufficient, EvidenceProgress: EvidenceProgressMaterial},
			{InformationNeedID: "NEED-002", Status: AssessmentNeedsMore, UnsatisfiedConditionIDs: []string{"NEED-002-SC-001"}, EvidenceProgress: EvidenceProgressMaterial},
		},
		true,
		true,
	)
	require.True(t, result.Accepted)
	require.True(t, result.AllTerminal)
	require.Len(t, result.Outcomes, 2)
	assert.Equal(t, NeedResolutionSatisfied, result.Outcomes[0].Resolution)
	assert.Equal(t, "supplier", result.Outcomes[0].Question)
	assert.Equal(t, []StopCondition{{ID: "NEED-001-SC-001", Condition: "relationship"}}, result.Outcomes[0].StopConditions)
	assert.Equal(t, NeedResolutionUnresolved, result.Outcomes[1].Resolution)
	assert.Equal(t, "materiality", result.Outcomes[1].Question)
	assert.Equal(t, []StopCondition{{ID: "NEED-002-SC-001", Condition: "economic exposure"}}, result.Outcomes[1].StopConditions)
	assert.Equal(t, TerminationBudgetExhausted, result.Outcomes[1].TerminationReason)
}

func TestInformationNeedOutcomesReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question:       "supplier",
		StopConditions: []StopConditionInput{{Condition: "relationship"}},
	}}})
	require.True(t, plan.Accepted)

	result := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionContinue}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentSufficient, EvidenceProgress: EvidenceProgressMaterial}},
		false,
		true,
	)
	require.True(t, result.Accepted)

	outcomes := context.InformationNeedOutcomes()
	require.Len(t, outcomes, 1)
	require.Len(t, outcomes[0].StopConditions, 1)
	outcomes[0].StopConditions[0].Condition = "mutated by caller"

	fresh := context.InformationNeedOutcomes()
	require.Len(t, fresh, 1)
	assert.Equal(t, "relationship", fresh[0].StopConditions[0].Condition)
}

func TestClosedNeedsCannotReappearInLaterCheckpointOrVerdict(t *testing.T) {
	t.Parallel()

	context := NewContext(t.TempDir(), 10, nil)
	plan := NewInformationNeedsHandler(context).Set(InformationNeedsArgs{InformationNeeds: []InformationNeedInput{{
		Question: "supplier", StopConditions: []StopConditionInput{{Condition: "relationship"}},
	}}})
	require.True(t, plan.Accepted)

	// Close NEED-001 as sufficient in the first round.
	first := context.ApplyQCAssessments(
		[]NeedDisposition{{InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionContinue}},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentSufficient, EvidenceProgress: EvidenceProgressMaterial}},
		false,
		true,
	)
	require.True(t, first.Accepted)

	// The next checkpoint cannot reference the closed need.
	checkpoint := NewCheckpointHandler(context).Submit(CheckpointArgs{
		EmptyReason: "closed need still active",
		NeedDispositions: []NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: SearchDispositionContinue,
		}},
	})
	assert.False(t, checkpoint.Accepted)
	require.NotEmpty(t, checkpoint.Issues)
	assert.Equal(t, "need_dispositions", checkpoint.Issues[0].Code)

	// The next verdict cannot reference the closed need either.
	verdict := context.ApplyQCAssessments(
		[]NeedDisposition{},
		[]QCAssessment{{InformationNeedID: "NEED-001", Status: AssessmentSufficient, EvidenceProgress: EvidenceProgressMaterial}},
		false,
		true,
	)
	assert.False(t, verdict.Accepted)
	require.NotEmpty(t, verdict.Issues)
	assert.Equal(t, "assessments", verdict.Issues[0].Code)
}
