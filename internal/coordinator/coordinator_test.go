package coordinator_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/coordinator"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCoordinatesNeedsMoreAndResearchRevision(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: needMoreAssessment()},
		{assessments: sufficientAssessment()},
	}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{
		{Decision: qc.DecisionReviseResearch, Issues: issues("accuracy")},
		{Decision: qc.DecisionPass},
	}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)

	result, err := runner.Run(t.Context(), coordinator.Config{
		Research:         researchruntime.Config{InitialPrompt: "original synthesis task"},
		FinalQCEnabled:   true,
		MaxFinalQCRounds: 3,
	})

	require.NoError(t, err)
	assert.Equal(t, workflow.PhaseComplete, state.Phase())
	assert.Equal(t, 2, collector.calls)
	assert.Equal(t, 2, collectionReviewer.calls)
	assert.Equal(t, 1, researcher.calls)
	assert.Equal(t, 2, finalReviewer.calls)
	assert.Contains(t, researcher.prompts[0], "original synthesis task")
	assert.Equal(t, "candidate-1", *result.Value)
}

func TestRunnerPassesWithoutFinalQC(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: sufficientAssessment()},
	}}
	researcher := &fakeResearcher{}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, nil)

	result, err := runner.Run(t.Context(), coordinator.Config{
		Research: researchruntime.Config{InitialPrompt: "original synthesis task"},
	})

	require.NoError(t, err)
	assert.Equal(t, workflow.PhaseComplete, state.Phase())
	assert.Equal(t, 1, collector.calls)
	assert.Equal(t, 1, collectionReviewer.calls)
	assert.Equal(t, 1, researcher.calls)
	assert.Equal(t, "candidate-1", *result.Value)
}

func TestRunnerCarriesOutcomesIntoNextCollectionPrompt(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	reviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: needMoreAssessment(), outcomes: []collection.InformationNeedOutcome{
			{InformationNeedID: "NEED-001", Question: "closed supplier question", Resolution: collection.NeedResolutionUnresolved, TerminationReason: collection.TerminationSearchStalled},
		}, activeStates: []collection.ActiveInformationNeedState{{
			InformationNeed: collection.InformationNeed{
				ID: "NEED-002", Question: "active materiality question",
				StopConditions: []collection.StopCondition{{ID: "NEED-002-SC-001", Condition: "economic exposure"}, {ID: "NEED-002-SC-002", Condition: "revenue share"}},
			},
			UnsatisfiedConditionIDs: []string{"NEED-002-SC-002"},
		}}},
		{assessments: sufficientAssessment()},
	}}
	runner := coordinator.NewRunner(state, collector, reviewer, &fakeResearcher{}, nil)

	_, err := runner.Run(t.Context(), coordinator.Config{})

	require.NoError(t, err)
	require.Len(t, collector.prompts, 2)
	assert.Contains(t, collector.prompts[1], "active materiality question")
	assert.Contains(t, collector.prompts[1], "NEED-002-SC-002")
	assert.Contains(t, collector.prompts[1], "revenue share")
	assert.Contains(t, collector.prompts[1], "NEED-001")
	assert.Contains(t, collector.prompts[1], "search_stalled")
}

func TestRunnerInjectsOutcomesOnlyIntoResearch(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	reviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: needMoreAssessment(), outcomes: []collection.InformationNeedOutcome{
			{InformationNeedID: "NEED-001", Resolution: collection.NeedResolutionUnresolved, TerminationReason: collection.TerminationBudgetExhausted},
		}},
		{assessments: sufficientAssessment(), outcomes: []collection.InformationNeedOutcome{
			{InformationNeedID: "NEED-001", Resolution: collection.NeedResolutionUnresolved, TerminationReason: collection.TerminationBudgetExhausted},
			{InformationNeedID: "NEED-002", Question: "satisfied context", Resolution: collection.NeedResolutionSatisfied},
		}},
	}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{{Decision: qc.DecisionPass}}}
	runner := coordinator.NewRunner(state, collector, reviewer, researcher, finalReviewer)

	_, err := runner.Run(t.Context(), coordinator.Config{FinalQCEnabled: true, MaxFinalQCRounds: 1})

	require.NoError(t, err)
	require.Len(t, researcher.prompts, 1)
	assert.Contains(t, researcher.prompts[0], "NEED-001")
	assert.Contains(t, researcher.prompts[0], "budget_exhausted")
	assert.Contains(t, researcher.prompts[0], "NEED-002")
	assert.Contains(t, researcher.prompts[0], "satisfied context")
	assert.Equal(t, 1, finalReviewer.calls)
}

func TestRunnerFinalQCOnlyPassOrRevise(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: sufficientAssessment()},
	}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{
		{Decision: qc.Decision("reopen_collection"), Issues: issues("coverage")},
		{Decision: qc.DecisionPass},
	}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)

	_, err := runner.Run(t.Context(), coordinator.Config{
		Research:         researchruntime.Config{InitialPrompt: "original synthesis task"},
		FinalQCEnabled:   true,
		MaxFinalQCRounds: 3,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported final qc decision")
	assert.Equal(t, 1, researcher.calls)
	assert.Equal(t, 1, finalReviewer.calls)
	assert.Equal(t, workflow.PhaseFinalQC, state.Phase())
}

func TestRunnerDoesNotStartUnreviewableWorkAfterLastFinalQCRound(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: sufficientAssessment()},
	}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{{Decision: qc.DecisionReviseResearch, Issues: issues("accuracy")}}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)

	_, err := runner.Run(t.Context(), coordinator.Config{FinalQCEnabled: true, MaxFinalQCRounds: 1})

	require.ErrorContains(t, err, "final qc rounds exhausted")
	assert.Equal(t, 1, researcher.calls)
	assert.Equal(t, workflow.PhaseFinalQC, state.Phase())
}

func TestRunnerUsesCallerContextAcrossEveryPhase(t *testing.T) {
	t.Parallel()

	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "shared")
	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{checkContext: func(ctx context.Context) { assert.Equal(t, "shared", ctx.Value(key{})) }}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{{assessments: sufficientAssessment()}}, checkContext: collector.checkContext}
	researcher := &fakeResearcher{checkContext: collector.checkContext}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{{Decision: qc.DecisionPass}}, checkContext: collector.checkContext}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)

	_, err := runner.Run(ctx, coordinator.Config{FinalQCEnabled: true, MaxFinalQCRounds: 1})

	require.NoError(t, err)
}

func TestRunnerEmitsPhaseDecisionsWithoutSnapshotContent(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{{assessments: sufficientAssessment()}}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{{Decision: qc.DecisionPass}}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)
	var events []coordinator.Event

	_, err := runner.Run(t.Context(), coordinator.Config{
		FinalQCEnabled: true, MaxFinalQCRounds: 1,
		Observe: func(event coordinator.Event) { events = append(events, event) },
	})

	require.NoError(t, err)
	assert.Contains(t, events, coordinator.Event{Phase: workflow.PhaseCollection, Action: coordinator.ActionStarted, CollectionRounds: 1, Round: 1})
	assert.Contains(t, events, coordinator.Event{Phase: workflow.PhaseCollectionQC, Action: coordinator.ActionDecision, Decision: "sufficient", CollectionRounds: 1, Round: 1})
	assert.Contains(t, events, coordinator.Event{Phase: workflow.PhaseFinalQC, Action: coordinator.ActionDecision, Decision: "pass", CollectionRounds: 1, Round: 1})
}

func TestRunnerEmitsPhaseRoundsAndResearchRevision(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{
		{assessments: sufficientAssessment()},
	}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{
		{Decision: qc.DecisionReviseResearch, Issues: issues("accuracy")},
		{Decision: qc.DecisionPass},
	}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)
	events := make([]coordinator.Event, 0)

	_, err := runner.Run(t.Context(), coordinator.Config{
		FinalQCEnabled:   true,
		MaxFinalQCRounds: 2,
		Observe:          func(event coordinator.Event) { events = append(events, event) },
	})

	require.NoError(t, err)
	require.Len(t, events, 9)
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseCollection, Action: coordinator.ActionStarted, CollectionRounds: 1, Round: 1}, events[0])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseCollectionQC, Action: coordinator.ActionStarted, CollectionRounds: 1, Round: 1}, events[1])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseCollectionQC, Action: coordinator.ActionDecision, Decision: "sufficient", CollectionRounds: 1, Round: 1}, events[2])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseResearch, Action: coordinator.ActionStarted, CollectionRounds: 1, Round: 1}, events[3])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseFinalQC, Action: coordinator.ActionStarted, CollectionRounds: 1, Round: 1}, events[4])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseFinalQC, Action: coordinator.ActionDecision, Decision: "revise_research", CollectionRounds: 1, Round: 1}, events[5])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseFinalQC, Action: coordinator.ActionStarted, CollectionRounds: 1, Round: 2}, events[6])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseFinalQC, Action: coordinator.ActionDecision, Decision: "pass", CollectionRounds: 1, Round: 2}, events[7])
	assert.Equal(t, coordinator.Event{Phase: workflow.PhaseComplete, Action: coordinator.ActionStarted, CollectionRounds: 1}, events[8])
}

func TestRunnerFinalQCRetryDoesNotRerunResearch(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{{assessments: sufficientAssessment()}}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{
		{Decision: qc.DecisionReviseResearch, Issues: issues("accuracy")},
		{Decision: qc.DecisionPass},
	}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)

	result, err := runner.Run(t.Context(), coordinator.Config{
		FinalQCEnabled: true, MaxFinalQCRounds: 2,
	})

	require.NoError(t, err)
	assert.Equal(t, "candidate-1", *result.Value)
	assert.Equal(t, 1, researcher.calls)
	assert.Equal(t, 2, finalReviewer.calls)
}

func TestRunnerPassesDispositionsToCollectionQC(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{checkpoint: collection.CheckpointOutput{
		EmptyReason: "configured source tools are exhausted",
		NeedDispositions: []collection.NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionStalled},
		},
	}}
	reviewer := &fakeCollectionReviewer{state: state, rounds: []collectionQCRound{{assessments: sufficientAssessment()}}}
	runner := coordinator.NewRunner(state, collector, reviewer, &fakeResearcher{}, nil)

	_, err := runner.Run(t.Context(), coordinator.Config{})

	require.NoError(t, err)
	require.Len(t, reviewer.configs, 1)
	assert.Equal(t, "configured source tools are exhausted", reviewer.configs[0].CheckpointEmptyReason)
	require.Len(t, reviewer.configs[0].NeedDispositions, 1)
	assert.Equal(t, "NEED-001", reviewer.configs[0].NeedDispositions[0].InformationNeedID)
}

type collectionQCRound struct {
	assessments  []collection.QCAssessment
	outcomes     []collection.InformationNeedOutcome
	activeStates []collection.ActiveInformationNeedState
}

func needMoreAssessment() []collection.QCAssessment {
	return []collection.QCAssessment{{
		InformationNeedID: "NEED-001", Status: collection.AssessmentNeedsMore,
		UnsatisfiedConditionIDs: []string{"NEED-001-SC-001"}, EvidenceProgress: collection.EvidenceProgressNone,
	}}
}

func sufficientAssessment() []collection.QCAssessment {
	return []collection.QCAssessment{{
		InformationNeedID: "NEED-001", Status: collection.AssessmentSufficient,
		EvidenceProgress: collection.EvidenceProgressMaterial,
	}}
}

type fakeCollector struct {
	calls        int
	prompts      []string
	checkContext func(context.Context)
	checkpoint   collection.CheckpointOutput
}

func (f *fakeCollector) Run(ctx context.Context, config collection.RunConfig) (collection.CheckpointOutput, error) {
	f.calls++
	f.prompts = append(f.prompts, config.InitialPrompt)
	if f.checkContext != nil {
		f.checkContext(ctx)
	}
	return f.checkpoint, nil
}

type fakeCollectionReviewer struct {
	state        *workflow.State
	rounds       []collectionQCRound
	configs      []collectionqc.Config
	calls        int
	checkContext func(context.Context)
}

func (f *fakeCollectionReviewer) Review(ctx context.Context, config collectionqc.Config) (collectionqc.Result, error) {
	f.calls++
	f.configs = append(f.configs, config)
	if f.checkContext != nil {
		f.checkContext(ctx)
	}
	round := f.rounds[0]
	f.rounds = f.rounds[1:]
	event := workflow.EventSufficient
	if round.assessments[0].Status == collection.AssessmentNeedsMore {
		event = workflow.EventNeedsMore
	}
	if err := f.state.Advance(event); err != nil {
		return collectionqc.Result{}, err
	}
	if len(round.outcomes) > 0 {
		encoded, err := json.Marshal(round.outcomes)
		if err != nil {
			return collectionqc.Result{}, err
		}
		f.state.SetInformationNeedOutcomes(encoded)
	}
	result := collectionqc.Result{
		Verdict:                     collectionqc.Verdict{Assessments: round.assessments},
		Outcomes:                    round.outcomes,
		ActiveInformationNeedStates: round.activeStates,
	}
	return result, nil
}

type fakeResearcher struct {
	calls        int
	prompts      []string
	checkContext func(context.Context)
}

func (f *fakeResearcher) Run(ctx context.Context, config researchruntime.Config) (researchruntime.Result, error) {
	f.calls++
	f.prompts = append(f.prompts, config.InitialPrompt)
	if f.checkContext != nil {
		f.checkContext(ctx)
	}
	value := "candidate-" + string(rune('0'+f.calls))
	return researchruntime.Result{Value: &value}, nil
}

type fakeFinalReviewer struct {
	verdicts     []qc.Verdict
	configs      []qc.Config
	calls        int
	checkContext func(context.Context)
}

func (f *fakeFinalReviewer) Review(ctx context.Context, config qc.Config, _ researchruntime.Result) (qc.Verdict, error) {
	f.calls++
	f.configs = append(f.configs, config)
	if f.checkContext != nil {
		f.checkContext(ctx)
	}
	verdict := f.verdicts[0]
	f.verdicts = f.verdicts[1:]
	return verdict, nil
}

func issues(code string) []corespec.Issue {
	return []corespec.Issue{{Code: code, Message: code}}
}
