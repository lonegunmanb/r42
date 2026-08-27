package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestCommandApplyJSONLPublishesProgressAndSuppressesPrettyPlanAndOutputs(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewForRun(t.TempDir(), filepath.Join(t.TempDir(), "run-42"), []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	runtime := &fakeRuntime{
		planned: planned,
		outputs: map[string]cty.Value{"answer": cty.StringVal("42")},
	}
	runtime.applyHook = func() {
		ctx := runtime.configOptions.Context
		require.NoError(t, debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusStarted, debuglog.Event{
			BlockAddress: "research.static.collect", BlockType: "research",
		}))
		require.NoError(t, debuglog.Record(ctx, debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.message_delta",
			BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
			Content: "checking evidence",
		}))
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl"})

	err = command.ExecuteContext(t.Context())

	require.NoError(t, err)
	// stdout must be exclusively protocol frames: no pretty Plan/output docs.
	assert.NotContains(t, stdout.String(), `"answer": "42"`)
	lines := splitLines(t, stdout.String())
	require.GreaterOrEqual(t, len(lines), 5)
	hello, err := decodeLine(lines[0])
	require.NoError(t, err)
	require.Equal(t, "hello", hello["type"])
	ready, err := decodeLine(lines[1])
	require.NoError(t, err)
	require.Equal(t, "ready", ready["type"])

	var sawSnapshot, sawNodeUpsert, sawTimeline, sawCompleted bool
	for _, line := range lines[2:] {
		frame, decodeErr := decodeLine(line)
		require.NoError(t, decodeErr)
		switch frame["type"] {
		case "run_snapshot":
			sawSnapshot = true
			assert.NotEmpty(t, frame["run_id"])
		case "node_upsert":
			sawNodeUpsert = true
		case "timeline_append":
			sawTimeline = true
		case "run_completed":
			sawCompleted = true
		default:
			t.Fatalf("unexpected post-ready frame type %q", frame["type"])
		}
	}
	assert.True(t, sawSnapshot, "expected the initial progress snapshot after ready")
	assert.True(t, sawNodeUpsert, "expected event-bus node progress")
	assert.True(t, sawTimeline, "expected event-bus timeline progress")
	assert.True(t, sawCompleted, "expected successful terminal progress")
}

func TestCommandApplyJSONLEmitsTerminalRecord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		applyErr  error
		wantType  string
		wantError bool
	}{
		{name: "success", wantType: "run_completed"},
		{name: "failure", applyErr: errors.New("apply failed\x1b[31m"), wantType: "run_failed", wantError: true},
		{name: "canceled", applyErr: context.Canceled, wantType: "run_canceled", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtime := &fakeRuntime{planned: mustPlan(t), applyErr: test.applyErr}
			var stdout bytes.Buffer
			command := cli.NewCommand(runtime)
			command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
			command.SetOut(&stdout)
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{"apply", "--ui=jsonl"})

			err := command.ExecuteContext(t.Context())
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			var terminal map[string]any
			for _, line := range splitLines(t, stdout.String()) {
				frame, decodeErr := decodeLine(line)
				require.NoError(t, decodeErr)
				if frame["type"] == test.wantType {
					terminal = frame
				}
			}
			require.NotNil(t, terminal)
			assert.Equal(t, true, terminal["critical"])
			if test.wantType != "run_completed" {
				assert.NotContains(t, terminal["summary"], "\x1b")
			}
			assert.NotContains(t, stdout.String(), `"answer": "42"`)
		})
	}
}

func TestCommandApplyJSONLPostReadyWriteFailureDoesNotChangeApplyResult(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	output := &failAfterWriter{remaining: 2}
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	command.SetOut(output)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl"})

	err := command.ExecuteContext(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(stderr.String(), "JSONL progress publication stopped:"))
}

func TestCommandApplyJSONLInvalidUIValueRemainsUsageError(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	runtime := &fakeRuntime{planned: planned}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=bogus"})

	err := command.ExecuteContext(t.Context())

	require.Error(t, err)
	assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
	require.ErrorContains(t, err, "invalid ui mode")
	assert.Empty(t, stdout.String())
}

func TestCommandApplyJSONLRequiresSuccessfulNegotiationBeforeApply(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	runtime := &fakeRuntime{
		planned:   planned,
		applyHook: func() { t.Errorf("apply must not start before ready") },
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput("")) // EOF before select
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl"})

	err := command.ExecuteContext(t.Context())

	require.Error(t, err)
	require.ErrorContains(t, err, "select")
	require.Contains(t, stdout.String(), "hello")
	assert.Empty(t, runtime.plannedDirectory, "Plan must not start before ready")
}

func TestCommandApplyJSONLUnsupportedSchemaFailsBeforeApply(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	runtime := &fakeRuntime{
		planned:   planned,
		applyHook: func() { t.Errorf("apply must not start with unsupported schema") },
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":99}` + "\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl"})

	err := command.ExecuteContext(t.Context())

	require.Error(t, err)
	require.ErrorContains(t, err, "schema_version")
	assert.Equal(t, cli.ExitFailure, cli.ExitCode(err))
}

func TestCommandApplyJSONLRejectsUnclosableStdinBeforePlan(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	runtime := &fakeRuntime{planned: planned}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl"})

	err := command.ExecuteContext(t.Context())

	require.ErrorContains(t, err, "closable stdin")
	assert.Empty(t, runtime.plannedDirectory)
}

func TestCommandApplyJSONLStderrRetainsDiagnostics(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	runtime := &fakeRuntime{
		planned:  planned,
		warnings: []error{assert.AnError},
		outputs:  map[string]cty.Value{"answer": cty.StringVal("42")},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl"})

	err := command.ExecuteContext(t.Context())

	require.NoError(t, err)
	// stderr remains the human-readable diagnostic stream.
	assert.Contains(t, stderr.String(), "warning: "+assert.AnError.Error())
	assert.NotContains(t, stdout.String(), "warning:")
}

//nolint:paralleltest // t.Chdir isolates the --debug run directory from the repository.
func TestCommandApplyJSONLDebugDoesNotAlterStdoutPrivacy(t *testing.T) {
	t.Chdir(t.TempDir())

	planned := mustPlan(t)
	runtime := &fakeRuntime{
		planned: planned,
		outputs: map[string]cty.Value{"answer": cty.StringVal("42")},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetIn(jsonlInput(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", "--ui=jsonl", "--debug"})

	err := command.ExecuteContext(t.Context())

	require.NoError(t, err)
	// --debug only controls the sensitive debug file; it never makes stdout
	// less restrictive. stdout still contains only protocol frames.
	assert.NotContains(t, stdout.String(), `"answer": "42"`)
	assert.Contains(t, stderr.String(), "sensitive")
	lines := splitLines(t, stdout.String())
	for _, line := range lines {
		decoded, err := decodeLine(line)
		require.NoError(t, err)
		assert.Contains(t, []any{"hello", "ready", "run_snapshot", "run_completed"}, decoded["type"])
	}
}

func TestCommandApplyJSONLHelpDescribesJSONLUIMode(t *testing.T) {
	t.Parallel()

	stdout, _, err := execute(t, nil, "apply", "--help")

	require.NoError(t, err)
	assert.Contains(t, stdout, "auto, tui, repl, or jsonl")
}

func splitLines(t *testing.T, content string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func decodeLine(line string) (map[string]any, error) {
	var decoded map[string]any
	err := json.Unmarshal([]byte(line), &decoded)
	return decoded, err
}

func jsonlInput(content string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(content))
}

type failAfterWriter struct {
	remaining int
}

func (w *failAfterWriter) Write(payload []byte) (int, error) {
	if w.remaining == 0 {
		return 0, assert.AnError
	}
	w.remaining--
	return len(payload), nil
}
