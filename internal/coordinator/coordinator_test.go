package coordinator_test

import (
	"context"
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

func TestRunnerCoordinatesReopenAndResearchRevision(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, decisions: []collectionqc.Decision{
		collectionqc.DecisionSufficient, collectionqc.DecisionSufficient,
	}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{
		{Decision: qc.DecisionReopenCollection, Issues: issues("coverage")},
		{Decision: qc.DecisionReviseResearch, Issues: issues("accuracy")},
		{Decision: qc.DecisionPass},
	}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)

	result, err := runner.Run(t.Context(), coordinator.Config{FinalQCEnabled: true, MaxFinalQCRounds: 3})

	require.NoError(t, err)
	assert.Equal(t, workflow.PhaseComplete, state.Phase())
	assert.Equal(t, 2, collector.calls)
	assert.Equal(t, 2, collectionReviewer.calls)
	assert.Equal(t, 3, researcher.calls)
	assert.Equal(t, 3, finalReviewer.calls)
	assert.Contains(t, researcher.prompts[1], "coverage")
	assert.Contains(t, researcher.prompts[2], "accuracy")
	assert.Equal(t, "candidate-3", *result.Value)
}

func TestRunnerDoesNotStartUnreviewableWorkAfterLastFinalQCRound(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	collectionReviewer := &fakeCollectionReviewer{state: state, decisions: []collectionqc.Decision{collectionqc.DecisionSufficient}}
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
	collectionReviewer := &fakeCollectionReviewer{state: state, decisions: []collectionqc.Decision{collectionqc.DecisionSufficient}, checkContext: collector.checkContext}
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
	collectionReviewer := &fakeCollectionReviewer{state: state, decisions: []collectionqc.Decision{collectionqc.DecisionSufficient}}
	researcher := &fakeResearcher{}
	finalReviewer := &fakeFinalReviewer{verdicts: []qc.Verdict{{Decision: qc.DecisionPass}}}
	runner := coordinator.NewRunner(state, collector, collectionReviewer, researcher, finalReviewer)
	var events []coordinator.Event

	_, err := runner.Run(t.Context(), coordinator.Config{
		FinalQCEnabled: true, MaxFinalQCRounds: 1,
		Observe: func(event coordinator.Event) { events = append(events, event) },
	})

	require.NoError(t, err)
	assert.Contains(t, events, coordinator.Event{Phase: workflow.PhaseCollection, Action: coordinator.ActionStarted, CollectionRounds: 1})
	assert.Contains(t, events, coordinator.Event{Phase: workflow.PhaseCollectionQC, Action: coordinator.ActionDecision, Decision: "sufficient", CollectionRounds: 1})
	assert.Contains(t, events, coordinator.Event{Phase: workflow.PhaseFinalQC, Action: coordinator.ActionDecision, Decision: "pass", CollectionRounds: 1})
}

func TestRunnerCarriesCollectionQCIssuesIntoNextCollectionPrompt(t *testing.T) {
	t.Parallel()

	state := workflow.New(workflow.Config{})
	collector := &fakeCollector{}
	reviewer := &fakeCollectionReviewer{state: state, decisions: []collectionqc.Decision{
		collectionqc.DecisionNeedsMore, collectionqc.DecisionSufficient,
	}, issues: [][]corespec.Issue{issues("missing_primary_source"), nil}}
	runner := coordinator.NewRunner(state, collector, reviewer, &fakeResearcher{}, nil)

	_, err := runner.Run(t.Context(), coordinator.Config{})

	require.NoError(t, err)
	require.Len(t, collector.prompts, 2)
	assert.Contains(t, collector.prompts[1], "missing_primary_source")
}

type fakeCollector struct {
	calls        int
	prompts      []string
	checkContext func(context.Context)
}

func (f *fakeCollector) Run(ctx context.Context, config collection.RunConfig) (collection.CheckpointOutput, error) {
	f.calls++
	f.prompts = append(f.prompts, config.InitialPrompt)
	if f.checkContext != nil {
		f.checkContext(ctx)
	}
	return collection.CheckpointOutput{}, nil
}

type fakeCollectionReviewer struct {
	state        *workflow.State
	decisions    []collectionqc.Decision
	issues       [][]corespec.Issue
	calls        int
	checkContext func(context.Context)
}

func (f *fakeCollectionReviewer) Review(ctx context.Context, _ collectionqc.Config) (collectionqc.Result, error) {
	f.calls++
	if f.checkContext != nil {
		f.checkContext(ctx)
	}
	decision := f.decisions[0]
	f.decisions = f.decisions[1:]
	var verdictIssues []corespec.Issue
	if len(f.issues) > 0 {
		verdictIssues, f.issues = f.issues[0], f.issues[1:]
	}
	event := workflow.EventSufficient
	if decision == collectionqc.DecisionNeedsMore {
		event = workflow.EventNeedsMore
	}
	if err := f.state.Advance(event); err != nil {
		return collectionqc.Result{}, err
	}
	if decision == collectionqc.DecisionNeedsMore {
		messages := make([]string, len(verdictIssues))
		for index := range verdictIssues {
			messages[index] = verdictIssues[index].Message
		}
		f.state.SetLastCollectionQCIssues(messages)
	}
	return collectionqc.Result{Verdict: collectionqc.Verdict{Decision: decision, Issues: verdictIssues}}, nil
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
	calls        int
	checkContext func(context.Context)
}

func (f *fakeFinalReviewer) Review(ctx context.Context, _ qc.Config, _ researchruntime.Result) (qc.Verdict, error) {
	f.calls++
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
