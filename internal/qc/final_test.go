package qc_test

import (
	"testing"

	"github.com/lonegunmanb/r42/internal/qc"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinalVerdictValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		verdict qc.Verdict
		error   string
	}{
		{name: "pass", verdict: qc.Verdict{Decision: qc.DecisionPass}},
		{name: "revise", verdict: qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{Code: "accuracy", Message: "correct the total"}}}},
		{name: "reopen", verdict: qc.Verdict{Decision: qc.DecisionReopenCollection, Issues: []corespec.Issue{{Code: "coverage", Message: "find source evidence"}}}},
		{name: "pass with issues", verdict: qc.Verdict{Decision: qc.DecisionPass, Issues: []corespec.Issue{{Code: "accuracy", Message: "wrong"}}}, error: "pass verdict must not contain issues"},
		{name: "revision without issues", verdict: qc.Verdict{Decision: qc.DecisionReviseResearch}, error: "revise_research verdict must contain at least one issue"},
		{name: "reopen without issues", verdict: qc.Verdict{Decision: qc.DecisionReopenCollection}, error: "reopen_collection verdict must contain at least one issue"},
		{name: "unknown", verdict: qc.Verdict{Decision: "later"}, error: "unsupported final qc decision"},
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

func TestVerdictRecorderRejectsReopenWhenCollectionBudgetExhausted(t *testing.T) {
	t.Parallel()

	recorder := qc.NewVerdictRecorder()
	recorder.SetCollectionBudget(qc.CollectionBudget{RoundsUsed: 3, MaxRounds: intPointer(3)})

	err := recorder.Record(qc.Verdict{
		Decision: qc.DecisionReopenCollection,
		Issues:   []corespec.Issue{{Code: "coverage", Message: "find more evidence"}},
	})

	require.Error(t, err)
	var rejection *qc.CollectionRoundBudgetExhaustedError
	require.ErrorAs(t, err, &rejection)
	assert.Contains(t, rejection.Error(), "all 3 collection rounds")
	assert.False(t, recorder.CollectionCanReopen())
}

func TestVerdictRecorderAllowsUnlimitedReopen(t *testing.T) {
	t.Parallel()

	recorder := qc.NewVerdictRecorder()
	recorder.SetCollectionBudget(qc.CollectionBudget{RoundsUsed: 100})

	err := recorder.Record(qc.Verdict{
		Decision: qc.DecisionReopenCollection,
		Issues:   []corespec.Issue{{Code: "coverage", Message: "find more evidence"}},
	})

	require.NoError(t, err)
	assert.True(t, recorder.CollectionCanReopen())
}

func TestVerdictRecorderRejectsReopenAfterCollectorExhaustion(t *testing.T) {
	t.Parallel()

	recorder := qc.NewVerdictRecorder()
	recorder.SetCollectionBudget(qc.CollectionBudget{RoundsUsed: 1, Exhausted: true})

	err := recorder.Record(qc.Verdict{
		Decision: qc.DecisionReopenCollection,
		Issues:   []corespec.Issue{{Code: "coverage", Message: "find more evidence"}},
	})

	require.Error(t, err)
	var rejection *qc.CollectionRoundBudgetExhaustedError
	require.ErrorAs(t, err, &rejection)
	assert.True(t, rejection.CollectorExhausted)
	assert.Contains(t, rejection.Error(), "Collection session reported")
	assert.False(t, recorder.CollectionCanReopen())
}

func intPointer(value int) *int { return &value }
