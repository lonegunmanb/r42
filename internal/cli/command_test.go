package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Azure/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/plan"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type fakeRuntime struct {
	planned       *plan.Plan
	planDirectory string
	variables     []golden.CliFlagAssignedVariables
	applyOptions  cli.ApplyOptions
	applyDeadline bool
	applyResult   cli.ApplyResult
	planErr       error
	applyErr      error
}

func (f *fakeRuntime) Plan(
	_ context.Context,
	directory string,
	variables []golden.CliFlagAssignedVariables,
) (*plan.Plan, error) {
	f.planDirectory = directory
	f.variables = variables
	return f.planned, f.planErr
}

func (f *fakeRuntime) Apply(ctx context.Context, _ *plan.Plan, options cli.ApplyOptions) (cli.ApplyResult, error) {
	f.applyOptions = options
	_, f.applyDeadline = ctx.Deadline()
	return f.applyResult, f.applyErr
}

func TestCommandPlanSavesOptionalOutputAndSeparatesPermissionWarning(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	planned, err := plan.New(directory, nil, map[string]plan.OutputSpec{
		"answer": {Value: cty.StringVal("42")},
	})
	require.NoError(t, err)
	runtime := &fakeRuntime{planned: planned}
	outPath := filepath.Join(t.TempDir(), "research.r42plan")

	stdout, stderr, executeErr := execute(t, runtime,
		"plan", "--directory", directory,
		"--out", outPath,
		"--var", "topic=markets",
		"--var-file", "inputs.r42vars",
	)

	require.NoError(t, executeErr)
	assert.Equal(t, directory, runtime.planDirectory)
	assert.Len(t, runtime.variables, 2)
	assert.Contains(t, stdout, `"answer"`)
	assert.NotContains(t, stdout, "unencrypted")
	assert.Contains(t, stderr, "unencrypted")
	loaded, err := plan.Load(outPath)
	require.NoError(t, err)
	assert.Equal(t, planned.Outputs(), loaded.Outputs())
}

func TestCommandPlanDirectoryFlagsAndDefaultPrintWithoutSaving(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		arguments     []string
		wantDirectory string
	}{
		{name: "default", arguments: []string{"plan"}, wantDirectory: "."},
		{name: "long flag", arguments: []string{"plan", "--directory", "research"}, wantDirectory: "research"},
		{name: "short flag", arguments: []string{"plan", "-d", "research"}, wantDirectory: "research"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			planned := mustPlan(t)
			runtime := &fakeRuntime{planned: planned}

			stdout, stderr, err := execute(t, runtime, test.arguments...)

			require.NoError(t, err)
			assert.Equal(t, test.wantDirectory, runtime.planDirectory)
			assert.Contains(t, stdout, `"nodes"`)
			assert.Empty(t, stderr)
		})
	}
}

func TestCommandPlanPrintsBeforeReportingSaveFailure(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	stdout, stderr, err := execute(t, runtime, "plan", "--out", t.TempDir())

	require.Error(t, err)
	require.ErrorContains(t, err, "save plan")
	assert.Contains(t, stdout, `"nodes"`)
	assert.Empty(t, stderr)
}

func TestCommandPlanTreatsWhitespaceOutputAsOmitted(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	stdout, stderr, err := execute(t, runtime, "plan", "--out", " \t ")

	require.NoError(t, err)
	assert.Contains(t, stdout, `"nodes"`)
	assert.Empty(t, stderr)
}

func TestCommandPlanHelpDescribesOptionalDirectoryAndOutput(t *testing.T) {
	t.Parallel()

	stdout, _, err := execute(t, nil, "plan", "--help")

	require.NoError(t, err)
	assert.Contains(t, stdout, "r42 plan [flags]")
	assert.Contains(t, stdout, "-d, --directory string")
	assert.Contains(t, stdout, `(default ".")`)
	assert.NotContains(t, stdout, "plan DIRECTORY")
}

func TestCommandApplySupportsSavedPlanAndDirectory(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	planned, err := plan.New(directory, nil, nil)
	require.NoError(t, err)
	planPath := filepath.Join(t.TempDir(), "saved.r42plan")
	_, err = plan.Save(planPath, planned)
	require.NoError(t, err)

	tests := []struct {
		name          string
		target        string
		wantPlanCalls bool
	}{
		{name: "saved plan", target: planPath},
		{name: "directory", target: directory, wantPlanCalls: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime := &fakeRuntime{
				planned: planned,
				applyResult: cli.ApplyResult{
					Outputs:  map[string]cty.Value{"answer": cty.StringVal("42")},
					Warnings: []error{errors.New("close session failed")},
				},
			}

			stdout, stderr, executeErr := execute(t, runtime,
				"apply", test.target,
				"--parallelism", "3",
				"--timeout", "2s",
				"--debug",
			)

			require.NoError(t, executeErr)
			assert.Equal(t, test.wantPlanCalls, runtime.planDirectory != "")
			assert.Equal(t, 3, runtime.applyOptions.Parallelism)
			assert.True(t, runtime.applyOptions.Debug)
			assert.True(t, runtime.applyDeadline)
			assert.Contains(t, stdout, `"answer":"42"`)
			assert.NotContains(t, stdout, "close session failed")
			assert.Contains(t, stderr, "close session failed")
			assert.Contains(t, stderr, "sensitive")
		})
	}
}

func TestCommandDiagnosticsAndExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		runtime  *fakeRuntime
		wantCode int
		wantErr  string
	}{
		{
			name:     "usage",
			args:     []string{"apply"},
			runtime:  &fakeRuntime{},
			wantCode: cli.ExitUsage,
			wantErr:  "received 0",
		},
		{
			name:     "plan block diagnostic",
			args:     []string{"plan", "--directory", t.TempDir()},
			runtime:  &fakeRuntime{planErr: errors.New("research.market: model is required")},
			wantCode: cli.ExitFailure,
			wantErr:  "research.market",
		},
		{
			name:     "plan positional directory is rejected",
			args:     []string{"plan", t.TempDir()},
			runtime:  &fakeRuntime{planned: mustPlan(t)},
			wantCode: cli.ExitUsage,
			wantErr:  "unknown command",
		},
		{
			name:     "apply failure",
			args:     []string{"apply", t.TempDir()},
			runtime:  &fakeRuntime{planned: mustPlan(t), applyErr: errors.New("apply block research.market: failed")},
			wantCode: cli.ExitFailure,
			wantErr:  "research.market",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := execute(t, test.runtime, test.args...)
			require.Error(t, err)
			assert.Equal(t, test.wantCode, cli.ExitCode(err))
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func execute(t *testing.T, runtime cli.Runtime, args ...string) (string, string, error) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func mustPlan(t *testing.T) *plan.Plan {
	t.Helper()
	planned, err := plan.New(t.TempDir(), nil, nil)
	require.NoError(t, err)
	return planned
}

func TestApplyTimeoutValidation(t *testing.T) {
	t.Parallel()

	_, _, err := execute(t, &fakeRuntime{planned: mustPlan(t)}, "apply", t.TempDir(), "--timeout", "0s")
	require.Error(t, err)
	assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
	assert.ErrorContains(t, err, "timeout must be positive")
}

func TestApplyDefaultParallelism(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	_, _, err := execute(t, runtime, "apply", t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, 10, runtime.applyOptions.Parallelism)
}

func TestApplyRedactsSensitiveOutputFromStdout(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{
		planned: mustPlan(t),
		applyResult: cli.ApplyResult{Outputs: map[string]cty.Value{
			"secret": corespec.MarkSensitive(cty.StringVal("do-not-print")),
		}},
	}
	stdout, _, err := execute(t, runtime, "apply", t.TempDir())
	require.NoError(t, err)
	assert.Contains(t, stdout, "<sensitive>")
	assert.NotContains(t, stdout, "do-not-print")
}

func TestApplyCancelledContextUsesFailureExit(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t), applyErr: context.Canceled}
	_, _, err := execute(t, runtime, "apply", t.TempDir())
	require.Error(t, err)
	assert.Equal(t, cli.ExitFailure, cli.ExitCode(err))
}

func TestTimeoutContextActuallyExpires(t *testing.T) {
	t.Parallel()

	deadlineObserved := make(chan struct{})
	runtime := runtimeFunc{
		plan: mustPlan(t),
		apply: func(ctx context.Context) error {
			<-ctx.Done()
			close(deadlineObserved)
			return ctx.Err()
		},
	}
	_, _, err := execute(t, runtime, "apply", t.TempDir(), "--timeout", "1ms")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-deadlineObserved:
	case <-time.After(time.Second):
		t.Fatal("apply did not observe timeout")
	}
}

func TestApplyTimeoutIncludesDirectDirectoryPlanning(t *testing.T) {
	t.Parallel()

	runtime := &blockingPlanRuntime{}
	_, _, err := execute(t, runtime, "apply", t.TempDir(), "--timeout", "1ms")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, runtime.applyCalled)
}

type blockingPlanRuntime struct{ applyCalled bool }

func (*blockingPlanRuntime) Plan(ctx context.Context, _ string, _ []golden.CliFlagAssignedVariables) (*plan.Plan, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("plan context has no deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *blockingPlanRuntime) Apply(context.Context, *plan.Plan, cli.ApplyOptions) (cli.ApplyResult, error) {
	r.applyCalled = true
	return cli.ApplyResult{}, nil
}

type runtimeFunc struct {
	plan  *plan.Plan
	apply func(context.Context) error
}

func (r runtimeFunc) Plan(context.Context, string, []golden.CliFlagAssignedVariables) (*plan.Plan, error) {
	return r.plan, nil
}

func (r runtimeFunc) Apply(ctx context.Context, _ *plan.Plan, _ cli.ApplyOptions) (cli.ApplyResult, error) {
	return cli.ApplyResult{}, r.apply(ctx)
}
