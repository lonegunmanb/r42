package qc_test

import (
	"testing"

	"github.com/lonegunmanb/r42/internal/qc"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
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
		{name: "pass with issues", verdict: qc.Verdict{Decision: qc.DecisionPass, Issues: []corespec.Issue{{Code: "accuracy", Message: "wrong"}}}, error: "pass verdict must not contain issues"},
		{name: "revision without issues", verdict: qc.Verdict{Decision: qc.DecisionReviseResearch}, error: "revise_research verdict must contain at least one issue"},
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

func TestFinalVerdictRejectsReopenDecision(t *testing.T) {
	t.Parallel()

	err := qc.Verdict{
		Decision: qc.Decision("reopen_collection"),
		Issues:   []corespec.Issue{{Code: "coverage", Message: "find source evidence"}},
	}.Validate()

	assert.ErrorContains(t, err, "unsupported final qc decision")
}
