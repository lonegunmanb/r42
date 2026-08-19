package qc_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRunnerPassesCandidateWithIsolatedQCContext(t *testing.T) {
	t.Parallel()

	value := "candidate"
	research := &fakeResearch{results: []researchruntime.Result{{
		Value:     &value,
		Artifacts: map[string]string{"report": "D:/work/report.md"},
	}}}
	verdicts := qc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(_ int, _ string) error {
		return verdicts.Record(qc.Verdict{Decision: qc.DecisionPass})
	}}
	prompt := "investigate demand"
	runner := qc.NewRunner(research, session, verdicts)

	result, err := runner.Run(t.Context(), qc.Config{
		Task: qc.Task{SystemPrompt: "research carefully", Prompt: &prompt},
		Criteria: cty.MapVal(map[string]cty.Value{
			"accuracy": cty.StringVal("cite primary sources"),
		}),
		Artifacts: []researchspec.Artifact{{
			Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Required: true,
		}},
		Research:            researchruntime.Config{InitialPrompt: prompt, MaxProtocolAttempts: 10, Workspace: "D:/work"},
		MaxRounds:           10,
		MaxProtocolAttempts: 10,
		VerdictToolName:     "r42_qc_verdict",
	})

	require.NoError(t, err)
	require.NotNil(t, result.Candidate.Value)
	assert.Equal(t, "candidate", *result.Candidate.Value)
	assert.Equal(t, 1, result.Rounds)
	require.Len(t, session.prompts, 1)
	var contextDocument map[string]any
	require.NoError(t, json.Unmarshal([]byte(session.prompts[0]), &contextDocument))
	assert.ElementsMatch(t, []string{"task", "criteria", "candidate", "artifacts"}, mapKeys(contextDocument))
	assert.NotContains(t, session.prompts[0], "transcript")
	assert.Contains(t, session.prompts[0], "D:/work/report.md")
}

func TestRunnerReturnsQCIssuesToResearchThenPassesRevision(t *testing.T) {
	t.Parallel()

	first := "draft"
	second := "revised"
	research := &fakeResearch{results: []researchruntime.Result{{Value: &first}, {Value: &second}}}
	verdicts := qc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(call int, _ string) error {
		if call == 1 {
			return verdicts.Record(qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{
				Code: "accuracy", Message: "correct the total",
			}}})
		}
		return verdicts.Record(qc.Verdict{Decision: qc.DecisionPass})
	}}
	runner := qc.NewRunner(research, session, verdicts)

	result, err := runner.Run(t.Context(), validConfig())

	require.NoError(t, err)
	assert.Equal(t, 2, result.Rounds)
	require.Len(t, research.configs, 2)
	assert.Equal(t, "initial task", research.configs[0].InitialPrompt)
	assert.Contains(t, research.configs[1].InitialPrompt, "correct the total")
	assert.Equal(t, validConfig().MaxProtocolAttempts, research.configs[1].MaxProtocolAttempts)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[0], "draft")
	assert.Contains(t, session.prompts[1], "revised")
}

func TestRunnerResetsVerdictProtocolBudgetForEachRevision(t *testing.T) {
	t.Parallel()

	research := &fakeResearch{results: []researchruntime.Result{{}, {}}}
	verdicts := qc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(call int, _ string) error {
		switch call {
		case 2:
			return verdicts.Record(qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{Code: "source", Message: "add source"}}})
		case 4:
			return verdicts.Record(qc.Verdict{Decision: qc.DecisionPass})
		default:
			return nil
		}
	}}
	runner := qc.NewRunner(research, session, verdicts)
	config := validConfig()
	config.MaxProtocolAttempts = 2

	result, err := runner.Run(t.Context(), config)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Rounds)
	assert.Len(t, session.prompts, 4)
	assert.Contains(t, session.prompts[1], "r42_qc_verdict")
	assert.Contains(t, session.prompts[3], "r42_qc_verdict")
}

func TestRunnerStopsAtVerdictProtocolLimit(t *testing.T) {
	t.Parallel()

	session := &fakeSession{}
	runner := qc.NewRunner(&fakeResearch{results: []researchruntime.Result{{}}}, session, qc.NewVerdictRecorder())
	config := validConfig()
	config.MaxProtocolAttempts = 2

	_, err := runner.Run(t.Context(), config)

	require.ErrorContains(t, err, "qc verdict protocol attempts exhausted")
	assert.Len(t, session.prompts, 2)
}

func TestRunnerStopsAtQCRoundLimit(t *testing.T) {
	t.Parallel()

	verdicts := qc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(_ int, _ string) error {
		return verdicts.Record(qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{Code: "accuracy", Message: "still wrong"}}})
	}}
	research := &fakeResearch{results: []researchruntime.Result{{}}}
	runner := qc.NewRunner(research, session, verdicts)
	config := validConfig()
	config.MaxRounds = 1

	_, err := runner.Run(t.Context(), config)

	require.ErrorContains(t, err, "qc rounds exhausted")
	assert.Len(t, research.configs, 1)
	assert.Len(t, session.prompts, 1)
}

func TestRunnerPropagatesResearchRevisionFailure(t *testing.T) {
	t.Parallel()

	revisionErr := errors.New("revision failed")
	research := &fakeResearch{results: []researchruntime.Result{{}}, errAfterResults: revisionErr}
	verdicts := qc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(_ int, _ string) error {
		return verdicts.Record(qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{Code: "accuracy", Message: "revise"}}})
	}}
	runner := qc.NewRunner(research, session, verdicts)

	_, err := runner.Run(t.Context(), validConfig())

	require.ErrorIs(t, err, revisionErr)
}

func TestRunnerRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		runner        *qc.Runner
		mutate        func(*qc.Config)
		expectedError string
	}{
		{name: "research required", runner: qc.NewRunner(nil, &fakeSession{}, qc.NewVerdictRecorder()), expectedError: "research runner is required"},
		{name: "session required", runner: qc.NewRunner(&fakeResearch{}, nil, qc.NewVerdictRecorder()), expectedError: "qc session is required"},
		{name: "recorder required", runner: qc.NewRunner(&fakeResearch{}, &fakeSession{}, nil), expectedError: "qc verdict recorder is required"},
		{name: "rounds required", runner: validRunner(), mutate: func(config *qc.Config) { config.MaxRounds = 0 }, expectedError: "qc rounds exhausted before review"},
		{name: "attempts nonnegative", runner: validRunner(), mutate: func(config *qc.Config) { config.MaxProtocolAttempts = -1 }, expectedError: "qc maximum protocol attempts must not be negative"},
		{name: "tool name required", runner: validRunner(), mutate: func(config *qc.Config) { config.VerdictToolName = " " }, expectedError: "qc verdict tool name is required"},
		{name: "criteria required", runner: validRunner(), mutate: func(config *qc.Config) { config.Criteria = cty.NilVal }, expectedError: "qc criteria must be a non-empty map of string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := validConfig()
			if tt.mutate != nil {
				tt.mutate(&config)
			}

			_, err := tt.runner.Run(t.Context(), config)

			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestRunnerPropagatesInfrastructureFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		research      *fakeResearch
		session       *fakeSession
		configure     func(*qc.VerdictRecorder)
		expectedError error
	}{
		{
			name:          "research",
			research:      &fakeResearch{err: errors.New("research failed")},
			session:       &fakeSession{},
			expectedError: errors.New("research failed"),
		},
		{
			name:          "qc session",
			research:      &fakeResearch{results: []researchruntime.Result{{}}},
			session:       &fakeSession{err: errors.New("qc transport failed")},
			expectedError: errors.New("qc transport failed"),
		},
		{
			name:     "verdict tool",
			research: &fakeResearch{results: []researchruntime.Result{{}}},
			session:  &fakeSession{},
			configure: func(recorder *qc.VerdictRecorder) {
				recorder.RecordError(errors.New("verdict handler failed"))
			},
			expectedError: errors.New("verdict handler failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := qc.NewVerdictRecorder()
			if tt.configure != nil {
				tt.configure(recorder)
			}
			runner := qc.NewRunner(tt.research, tt.session, recorder)

			_, err := runner.Run(t.Context(), validConfig())

			require.ErrorContains(t, err, tt.expectedError.Error())
		})
	}
}

func TestVerdictValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		verdict       qc.Verdict
		expectedError string
	}{
		{name: "pass", verdict: qc.Verdict{Decision: qc.DecisionPass}},
		{name: "issues", verdict: qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{Code: "accuracy", Message: "wrong"}}}},
		{name: "pass with issues", verdict: qc.Verdict{Decision: qc.DecisionPass, Issues: []corespec.Issue{{Code: "accuracy", Message: "wrong"}}}, expectedError: "pass verdict must not contain issues"},
		{name: "failure without issues", verdict: qc.Verdict{Decision: qc.DecisionReviseResearch}, expectedError: "revise_research verdict must contain at least one issue"},
		{name: "invalid issue", verdict: qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{{Message: "missing code"}}}, expectedError: "issue 0: issue code is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.verdict.Validate()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestVerdictRecorderPreservesExplicitHandlerFailure(t *testing.T) {
	t.Parallel()

	recorder := qc.NewVerdictRecorder()
	recorder.RecordError(nil)
	handlerErr := errors.New("verdict handler failed")
	recorder.RecordError(handlerErr)
	recorder.RecordError(errors.New("later failure"))
	runner := qc.NewRunner(&fakeResearch{results: []researchruntime.Result{{}}}, &fakeSession{}, recorder)

	_, err := runner.Run(t.Context(), validConfig())

	require.ErrorIs(t, err, handlerErr)
	assert.NotContains(t, err.Error(), "later failure")
}

func TestRunnerAcceptsRepairAfterInvalidFinalVerdictInSameTurn(t *testing.T) {
	t.Parallel()

	recorder := qc.NewVerdictRecorder()
	require.Error(t, recorder.Record(qc.Verdict{Decision: qc.DecisionReviseResearch}))
	require.NoError(t, recorder.Record(qc.Verdict{Decision: qc.DecisionPass}))
	runner := qc.NewRunner(nil, &fakeSession{}, recorder)
	config := validConfig()

	verdict, err := runner.Review(t.Context(), config, researchruntime.Result{})

	require.NoError(t, err)
	assert.Equal(t, qc.DecisionPass, verdict.Decision)
}

func TestRunnerRejectsInvalidCriteriaShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		criteria cty.Value
		error    string
	}{
		{name: "unknown", criteria: cty.UnknownVal(cty.Map(cty.String)), error: "non-empty map"},
		{name: "wrong type", criteria: cty.MapVal(map[string]cty.Value{"a": cty.NumberIntVal(1)}), error: "non-empty map"},
		{name: "empty", criteria: cty.MapValEmpty(cty.String), error: "non-empty map"},
		{name: "null value", criteria: cty.MapVal(map[string]cty.Value{"a": cty.NullVal(cty.String)}), error: "values must not be null"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := validConfig()
			config.Criteria = tt.criteria

			_, err := validRunner().Run(t.Context(), config)

			require.ErrorContains(t, err, tt.error)
		})
	}
}

func TestRunnerIncludesCompleteIssueDetailsInRevision(t *testing.T) {
	t.Parallel()

	path := "report.md"
	hint := "add a citation"
	research := &fakeResearch{results: []researchruntime.Result{{}, {}}}
	verdicts := qc.NewVerdictRecorder()
	session := &fakeSession{onSend: func(call int, _ string) error {
		if call == 1 {
			return verdicts.Record(qc.Verdict{Decision: qc.DecisionReviseResearch, Issues: []corespec.Issue{
				{Code: "source", Message: "missing source", Path: &path, RepairHint: &hint},
				{Code: "accuracy", Message: "wrong total"},
			}})
		}
		return verdicts.Record(qc.Verdict{Decision: qc.DecisionPass})
	}}
	runner := qc.NewRunner(research, session, verdicts)

	_, err := runner.Run(t.Context(), validConfig())

	require.NoError(t, err)
	require.Len(t, research.configs, 2)
	assert.Contains(t, research.configs[1].InitialPrompt, "(path: report.md) Repair: add a citation\n- [accuracy]")
}

func TestVerdictRecorderCompletionVersionOnlyAdvancesForNewOutcomes(t *testing.T) {
	t.Parallel()

	recorder := qc.NewVerdictRecorder()
	assert.Zero(t, recorder.CompletionVersion())
	require.NoError(t, recorder.Record(qc.Verdict{Decision: qc.DecisionPass}))
	assert.Equal(t, uint64(1), recorder.CompletionVersion())
	recorder.RecordError(errors.New("handler failed"))
	assert.Equal(t, uint64(2), recorder.CompletionVersion())
	recorder.RecordError(errors.New("duplicate failure"))
	assert.Equal(t, uint64(2), recorder.CompletionVersion())
}

type fakeResearch struct {
	results         []researchruntime.Result
	configs         []researchruntime.Config
	err             error
	errAfterResults error
}

func (r *fakeResearch) Run(_ context.Context, config researchruntime.Config) (researchruntime.Result, error) {
	r.configs = append(r.configs, config)
	if r.err != nil {
		return researchruntime.Result{}, r.err
	}
	if len(r.results) == 0 {
		return researchruntime.Result{}, r.errAfterResults
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

type fakeSession struct {
	prompts []string
	onSend  func(int, string) error
	err     error
}

func (s *fakeSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.prompts = append(s.prompts, options.Prompt)
	if s.err != nil {
		return nil, s.err
	}
	if s.onSend != nil {
		return nil, s.onSend(len(s.prompts), options.Prompt)
	}
	return nil, nil
}

func validConfig() qc.Config {
	return qc.Config{
		Task:                qc.Task{SystemPrompt: "research carefully", Prompt: stringPointer("initial task")},
		Criteria:            cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
		Research:            researchruntime.Config{InitialPrompt: "initial task", MaxProtocolAttempts: 10, Workspace: "D:/work"},
		MaxRounds:           10,
		MaxProtocolAttempts: 10,
		VerdictToolName:     "r42_qc_verdict",
	}
}

func validRunner() *qc.Runner {
	return qc.NewRunner(
		&fakeResearch{results: []researchruntime.Result{{}}},
		&fakeSession{},
		qc.NewVerdictRecorder(),
	)
}

func mapKeys(value map[string]any) []string {
	result := make([]string, 0, len(value))
	for key := range value {
		result = append(result, key)
	}
	return result
}

func stringPointer(value string) *string {
	return &value
}
