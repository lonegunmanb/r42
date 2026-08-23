package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCompletesWithoutTerminalResult(t *testing.T) {
	t.Parallel()

	session := &fakeSession{}
	runner := researchruntime.NewRunner(session, nil)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		InitialPrompt:       "investigate the market",
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.NoError(t, err)
	assert.Nil(t, result.Value)
	assert.Empty(t, result.Artifacts)
	assert.Zero(t, result.ProtocolAttempts)
	assert.Equal(t, []string{"investigate the market"}, session.prompts)
}

func TestRunnerRequiresTerminalCallAndReturnsOptionalString(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(call int, _ context.Context, _ string) error {
		if call == 2 {
			value := "completed research"
			return recorder.Record(corespec.ToolResponse[string]{Accepted: true, Output: &value})
		}
		return nil
	}}
	runner := researchruntime.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		InitialPrompt:       "investigate",
		TerminateToolName:   "go_tool_finish",
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.NoError(t, err)
	require.NotNil(t, result.Value)
	assert.Equal(t, "completed research", *result.Value)
	assert.Equal(t, 1, result.ProtocolAttempts)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "go_tool_finish")
}

func TestRunnerReturnsRejectedTerminalIssuesForRepairInSameSession(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(call int, _ context.Context, _ string) error {
		if call == 1 {
			return recorder.Record(corespec.ToolResponse[string]{Issues: []corespec.Issue{{
				Code:    "missing_source",
				Message: "include a source",
			}}})
		}
		return recorder.Record(corespec.ToolResponse[string]{Accepted: true})
	}}
	runner := researchruntime.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "external_tool_finish",
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.NoError(t, err)
	assert.Nil(t, result.Value)
	assert.Equal(t, 1, result.ProtocolAttempts)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "include a source")
}

func TestRunnerRepairsRequiredArtifactsBeforeCompleting(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(call int, _ context.Context, _ string) error {
		if call == 2 {
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.md"), []byte("result"), 0o600))
		}
		return recorder.Record(corespec.ToolResponse[string]{Accepted: true})
	}}
	runner := researchruntime.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "go_tool_finish",
		MaxProtocolAttempts: 10,
		Workspace:           workspace,
		Artifacts: []researchspec.Artifact{{
			Name:        "report",
			Type:        researchspec.ArtifactTypeFile,
			Path:        "report.md",
			Description: "Report fixture",
			Required:    true,
			NonEmpty:    true,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(workspace, "report.md"), result.Artifacts["report"])
	assert.Equal(t, 1, result.ProtocolAttempts)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "artifact")
	assert.Contains(t, session.prompts[1], "go_tool_finish")
}

func TestRunnerRepairsRequiredArtifactsWithoutTerminalTool(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	session := &fakeSession{onSend: func(call int, _ context.Context, _ string) error {
		if call == 2 {
			return os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("notes"), 0o600)
		}
		return nil
	}}
	runner := researchruntime.NewRunner(session, nil)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		MaxProtocolAttempts: 10,
		Workspace:           workspace,
		Artifacts: []researchspec.Artifact{{
			Name:        "notes",
			Type:        researchspec.ArtifactTypeFile,
			Path:        "notes.md",
			Description: "Notes fixture",
			Required:    true,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(workspace, "notes.md"), result.Artifacts["notes"])
	assert.Equal(t, 1, result.ProtocolAttempts)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "Finish the research response")
}

func TestRunnerStopsAtProtocolAttemptLimit(t *testing.T) {
	t.Parallel()

	session := &fakeSession{}
	runner := researchruntime.NewRunner(session, researchruntime.NewTerminalRecorder())

	_, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "go_tool_finish",
		MaxProtocolAttempts: 2,
		Workspace:           t.TempDir(),
	})

	require.ErrorContains(t, err, "protocol attempts exhausted")
	assert.Len(t, session.prompts, 2)
}

func TestRunnerCountsEveryRejectedCallBeforeAcceptedCall(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(_ int, _ context.Context, _ string) error {
		for _, code := range []string{"first", "second"} {
			require.NoError(t, recorder.Record(corespec.ToolResponse[string]{Issues: []corespec.Issue{{
				Code:    code,
				Message: "repair " + code,
			}}}))
		}
		return recorder.Record(corespec.ToolResponse[string]{Accepted: true})
	}}
	runner := researchruntime.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "go_tool_finish",
		MaxProtocolAttempts: 2,
		Workspace:           t.TempDir(),
	})

	require.NoError(t, err)
	assert.Equal(t, 2, result.ProtocolAttempts)
	assert.Len(t, session.prompts, 1)
}

func TestRunnerCombinesMultipleRejectedCallsInOneRepairPrompt(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(call int, _ context.Context, _ string) error {
		if call == 1 {
			for _, code := range []string{"first", "second"} {
				require.NoError(t, recorder.Record(corespec.ToolResponse[string]{Issues: []corespec.Issue{{
					Code:    code,
					Message: "repair " + code,
				}}}))
			}
			return nil
		}
		return recorder.Record(corespec.ToolResponse[string]{Accepted: true})
	}}
	runner := researchruntime.NewRunner(session, recorder)

	result, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "go_tool_finish",
		MaxProtocolAttempts: 3,
		Workspace:           t.TempDir(),
	})

	require.NoError(t, err)
	assert.Equal(t, 2, result.ProtocolAttempts)
	require.Len(t, session.prompts, 2)
	assert.Contains(t, session.prompts[1], "repair first\n- [second] repair second")
}

func TestRunnerRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		runner        *researchruntime.Runner
		config        researchruntime.Config
		expectedError string
	}{
		{
			name:          "session is required",
			runner:        researchruntime.NewRunner(nil, nil),
			expectedError: "research session is required",
		},
		{
			name:   "attempts must not be negative",
			runner: researchruntime.NewRunner(&fakeSession{}, nil),
			config: researchruntime.Config{
				MaxProtocolAttempts: -1,
			},
			expectedError: "maximum protocol attempts must not be negative",
		},
		{
			name:   "terminal recorder is required",
			runner: researchruntime.NewRunner(&fakeSession{}, nil),
			config: researchruntime.Config{
				TerminateToolName: "go_tool_finish",
			},
			expectedError: "terminal recorder is required",
		},
		{
			name:   "timeout must be positive",
			runner: researchruntime.NewRunner(&fakeSession{}, nil),
			config: researchruntime.Config{
				Timeout: durationPointer(0),
			},
			expectedError: "research timeout must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.runner.Run(t.Context(), tt.config)

			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestRunnerReportsArtifactValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		terminal   bool
		maximum    int
		artifacts  []researchspec.Artifact
		errorMatch string
	}{
		{
			name:       "no terminal invalid declaration",
			maximum:    10,
			artifacts:  []researchspec.Artifact{{Name: "bad", Type: "unknown", Path: "bad", Description: "Bad fixture"}},
			errorMatch: "validate research artifacts",
		},
		{
			name:       "terminal invalid declaration",
			terminal:   true,
			maximum:    10,
			artifacts:  []researchspec.Artifact{{Name: "bad", Type: "unknown", Path: "bad", Description: "Bad fixture"}},
			errorMatch: "validate research artifacts",
		},
		{
			name:       "no terminal repair budget exhausted",
			maximum:    1,
			artifacts:  []researchspec.Artifact{{Name: "missing", Type: researchspec.ArtifactTypeFile, Path: "missing", Description: "Missing fixture", Required: true}},
			errorMatch: "protocol attempts exhausted",
		},
		{
			name:       "terminal repair budget exhausted",
			terminal:   true,
			maximum:    1,
			artifacts:  []researchspec.Artifact{{Name: "missing", Type: researchspec.ArtifactTypeFile, Path: "missing", Description: "Missing fixture", Required: true}},
			errorMatch: "protocol attempts exhausted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := &fakeSession{}
			var recorder *researchruntime.TerminalRecorder
			toolName := ""
			if tt.terminal {
				recorder = researchruntime.NewTerminalRecorder()
				toolName = "go_tool_finish"
				session.onSend = func(_ int, _ context.Context, _ string) error {
					return recorder.Record(corespec.ToolResponse[string]{Accepted: true})
				}
			}
			runner := researchruntime.NewRunner(session, recorder)

			_, err := runner.Run(t.Context(), researchruntime.Config{
				TerminateToolName:   toolName,
				MaxProtocolAttempts: tt.maximum,
				Workspace:           t.TempDir(),
				Artifacts:           tt.artifacts,
			})

			require.ErrorContains(t, err, tt.errorMatch)
		})
	}
}

func TestRunnerPropagatesSessionInfrastructureFailure(t *testing.T) {
	t.Parallel()

	infrastructureErr := errors.New("copilot transport failed")
	session := &fakeSession{onSend: func(_ int, _ context.Context, _ string) error {
		return infrastructureErr
	}}
	runner := researchruntime.NewRunner(session, nil)

	_, err := runner.Run(t.Context(), researchruntime.Config{
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.ErrorIs(t, err, infrastructureErr)
	assert.Len(t, session.prompts, 1)
}

func TestRunnerPropagatesTerminalInfrastructureFailureAfterSessionIdle(t *testing.T) {
	t.Parallel()

	infrastructureErr := errors.New("tool process failed")
	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(_ int, _ context.Context, _ string) error {
		recorder.RecordError(infrastructureErr)
		return nil
	}}
	runner := researchruntime.NewRunner(session, recorder)

	_, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "external_tool_finish",
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.ErrorIs(t, err, infrastructureErr)
	assert.Len(t, session.prompts, 1)
}

func TestRunnerPreservesFirstTerminalInfrastructureFailure(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first tool failure")
	secondErr := errors.New("second tool failure")
	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(_ int, _ context.Context, _ string) error {
		recorder.RecordError(nil)
		recorder.RecordError(firstErr)
		recorder.RecordError(secondErr)
		return nil
	}}
	runner := researchruntime.NewRunner(session, recorder)

	_, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "external_tool_finish",
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.ErrorIs(t, err, firstErr)
	assert.NotErrorIs(t, err, secondErr)
}

func TestRunnerPropagatesInvalidTerminalEnvelopeAfterSessionIdle(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	session := &fakeSession{onSend: func(_ int, _ context.Context, _ string) error {
		value := "partial"
		_ = recorder.Record(corespec.ToolResponse[string]{Output: &value})
		return nil
	}}
	runner := researchruntime.NewRunner(session, recorder)

	_, err := runner.Run(t.Context(), researchruntime.Config{
		TerminateToolName:   "go_tool_finish",
		MaxProtocolAttempts: 10,
		Workspace:           t.TempDir(),
	})

	require.ErrorContains(t, err, "record terminal response")
	assert.Len(t, session.prompts, 1)
}

func TestRunnerAppliesBlockTimeout(t *testing.T) {
	t.Parallel()

	timeout := 20 * time.Millisecond
	session := &fakeSession{onSend: func(_ int, ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	runner := researchruntime.NewRunner(session, nil)

	_, err := runner.Run(t.Context(), researchruntime.Config{
		MaxProtocolAttempts: 10,
		Timeout:             &timeout,
		Workspace:           t.TempDir(),
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestTerminalRecorderRejectsInvalidResponse(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	value := "partial"

	err := recorder.Record(corespec.ToolResponse[string]{Output: &value})

	require.ErrorContains(t, err, "record terminal response")
}

func TestTerminalRecorderCompletionVersionOnlyAdvancesForNewOutcomes(t *testing.T) {
	t.Parallel()

	recorder := researchruntime.NewTerminalRecorder()
	assert.Zero(t, recorder.CompletionVersion())
	require.NoError(t, recorder.Record(corespec.ToolResponse[string]{Accepted: true}))
	assert.Equal(t, uint64(1), recorder.CompletionVersion())
	recorder.RecordError(errors.New("handler failed"))
	assert.Equal(t, uint64(2), recorder.CompletionVersion())
	recorder.RecordError(errors.New("duplicate failure"))
	assert.Equal(t, uint64(2), recorder.CompletionVersion())
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}

type fakeSession struct {
	prompts []string
	onSend  func(int, context.Context, string) error
}

func (s *fakeSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.prompts = append(s.prompts, options.Prompt)
	if s.onSend != nil {
		if err := s.onSend(len(s.prompts), ctx, options.Prompt); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
