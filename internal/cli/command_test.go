package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type fakeRuntime struct {
	planned          *plan.Plan
	plannedDirectory string
	variables        []golden.CliFlagAssignedVariables
	configOptions    executor.ResearchConfigOptions
	applyDeadline    bool
	applyDeadlineAt  time.Time
	outputs          map[string]cty.Value
	warnings         []error
	planErr          error
	applyErr         error
	applyHook        func()
}

func (f *fakeRuntime) Config(
	directory string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	f.plannedDirectory = directory
	f.variables = options.Variables
	if f.planErr != nil {
		return nil, f.planErr
	}
	return f.config(f.planned, options)
}

func (f *fakeRuntime) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return f.config(planned, options)
}

func (f *fakeRuntime) config(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	f.configOptions = options
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		if f.applyHook != nil {
			f.applyHook()
		}
		f.applyDeadlineAt, f.applyDeadline = options.Context.Deadline()
		return f.outputs, f.warnings, f.applyErr
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}

func TestCommandPlanSavesOptionalOutputAndSeparatesPermissionWarning(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	planned, err := plan.NewWithContextAndLocals(directory, nil, map[string]plan.OutputSpec{
		"answer": {Value: cty.StringVal("42")},
	}, nil, nil)
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
	assert.Equal(t, directory, runtime.plannedDirectory)
	assert.Len(t, runtime.variables, 2)
	assert.Contains(t, stdout, `"answer"`)
	assert.NotContains(t, stdout, "unencrypted")
	assert.Contains(t, stderr, "unencrypted")
	loaded, err := plan.Load(outPath)
	require.NoError(t, err)
	assert.Equal(t, planned.Outputs(), loaded.Outputs())
}

func TestCommandPlanFlagsAndDefaultPrintWithoutSaving(t *testing.T) {
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
			assert.Equal(t, test.wantDirectory, runtime.plannedDirectory)
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

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandPlanDebugRecordsDetailedGoldenPlanningLifecycle(t *testing.T) {
	workingDirectory := t.TempDir()
	directory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
output "answer" { value = "42" }
`), 0o600))

	planPath := filepath.Join(t.TempDir(), "saved.r42plan")
	_, stderr, err := execute(t, cli.NewRuntime(), "plan", "--directory", directory, "--out", planPath, "--debug")

	require.NoError(t, err)
	assert.Contains(t, stderr, "sensitive")
	events := readDebugEvents(t, workingDirectory)
	for _, action := range []string{
		"plan",
		"config.directory.scan",
		"config.file.collect",
		"hcl.syntax.parse",
		"hcl.write.parse",
		"hcl.block.extract",
		"golden.config.init",
		"golden.run_plan",
		"block.decode",
		"block.plan",
		"plan.snapshot",
		"plan.display",
		"plan.save",
	} {
		assert.Contains(t, events, `"action":"`+action+`"`)
	}
	encodedPath, err := json.Marshal(filepath.Join(directory, "main.r42"))
	require.NoError(t, err)
	assert.Contains(t, events, string(encodedPath))
	assert.NoDirExists(t, filepath.Join(directory, ".r42"))
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandPlanStoresDebugRunInWorkingDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	configurationDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(configurationDirectory, "main.r42"), []byte(`
output "answer" { value = "42" }
`), 0o600))

	_, _, err := execute(t, cli.NewRuntime(), "plan", "--directory", configurationDirectory, "--debug")

	require.NoError(t, err)
	assert.Contains(t, readDebugEvents(t, workingDirectory), `"action":"plan"`)
	assert.NoDirExists(t, filepath.Join(configurationDirectory, ".r42"))
}

//nolint:paralleltest // t.Chdir isolates debug output from the repository.
func TestCommandPlanDebugDoesNotCreateMissingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	directory := filepath.Join(t.TempDir(), "missing")

	_, _, err := execute(t, cli.NewRuntime(), "plan", "--directory", directory, "--debug")

	require.Error(t, err)
	_, statErr := os.Stat(directory)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

//nolint:paralleltest // t.Chdir isolates debug output from the repository.
func TestCommandPlanDebugRecordsParseFailure(t *testing.T) {
	workingDirectory := t.TempDir()
	directory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "broken.r42"), []byte(`research "broken" {`), 0o600))

	_, _, err := execute(t, cli.NewRuntime(), "plan", "--directory", directory, "--debug")

	require.Error(t, err)
	events := readDebugEvents(t, workingDirectory)
	assert.Contains(t, events, `"action":"hcl.syntax.parse","status":"failed"`)
	assert.Contains(t, events, `"action":"plan","status":"failed"`)
	assert.Contains(t, events, `"error":`)
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandApplyDebugRecordsDetailedPlanAndApplyLifecycle(t *testing.T) {
	workingDirectory := t.TempDir()
	directory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
research "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."
}
output "answer" { value = "42" }
`), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &fakeSessionOpener{}})

	_, _, err := execute(t, runtime, "apply", directory, "--debug", "--parallelism", "1")

	require.NoError(t, err)
	events := readDebugEvents(t, workingDirectory)
	for _, action := range []string{
		"plan",
		"config.file.collect",
		"hcl.block.extract",
		"golden.config.init",
		"golden.run_plan",
		"plan.snapshot",
		"plan.display",
		"apply",
		"apply.golden.config.init",
		"apply.golden.plan",
		"apply.golden.apply",
		"block.decode",
		"block.plan",
		"block.factory",
		"block.apply",
		"block.cleanup",
		"apply.outputs.resolve",
		"session.open",
		"session.send",
		"session.close",
	} {
		assert.Contains(t, events, `"action":"`+action+`"`)
	}
	assert.Contains(t, events, `"block_address":"research.source"`)
	assert.NoDirExists(t, filepath.Join(directory, ".r42"))
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandApplyStoresRunInWorkingDirectory(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		debug     bool
	}{
		{name: "default", arguments: nil},
		{name: "debug", arguments: []string{"--debug"}, debug: true},
	}
	//nolint:paralleltest // Each subtest changes the process working directory.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDirectory := t.TempDir()
			configurationDirectory := t.TempDir()
			t.Chdir(workingDirectory)
			require.NoError(t, os.WriteFile(filepath.Join(configurationDirectory, "main.r42"), []byte(`
output "answer" { value = "42" }
`), 0o600))

			arguments := append([]string{"apply", configurationDirectory}, test.arguments...)
			_, _, err := execute(t, cli.NewRuntime(), arguments...)

			require.NoError(t, err)
			runs, err := os.ReadDir(filepath.Join(workingDirectory, ".r42", "runs"))
			require.NoError(t, err)
			require.Len(t, runs, 1)
			if test.debug {
				assert.Contains(t, readDebugEvents(t, workingDirectory), `"action":"apply"`)
			}
			assert.NoDirExists(t, filepath.Join(configurationDirectory, ".r42"))
		})
	}
}

func readDebugEvents(t *testing.T, directory string) string {
	t.Helper()
	runs, err := os.ReadDir(filepath.Join(directory, ".r42", "runs"))
	require.NoError(t, err)
	require.Len(t, runs, 1)
	content, err := os.ReadFile(filepath.Join(directory, ".r42", "runs", runs[0].Name(), "events.jsonl"))
	require.NoError(t, err)
	return string(content)
}

//nolint:paralleltest // t.Chdir isolates CLI run artifacts from the source tree.
func TestCommandApplySupportsSavedPlanAndDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	directory := t.TempDir()
	planned, err := plan.NewWithContextAndLocals(directory, nil, nil, nil, nil)
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
			runtime := &fakeRuntime{
				planned:  planned,
				outputs:  map[string]cty.Value{"answer": cty.StringVal("42")},
				warnings: []error{errors.New("close session failed")},
			}

			stdout, stderr, executeErr := execute(t, runtime,
				"apply", test.target,
				"--parallelism", "3",
				"--timeout", "2s",
				"--debug",
			)

			require.NoError(t, executeErr)
			assert.Equal(t, test.wantPlanCalls, runtime.plannedDirectory != "")
			assert.Equal(t, 3, runtime.configOptions.Parallelism)
			assert.True(t, runtime.configOptions.Debug)
			assert.True(t, runtime.applyDeadline)
			assert.Contains(t, stdout, `"nodes"`)
			assert.Contains(t, stdout, `"answer":"42"`)
			assert.Less(t, strings.Index(stdout, `"nodes"`), strings.Index(stdout, `"answer":"42"`))
			assert.NotContains(t, stdout, "close session failed")
			assert.Contains(t, stderr, "close session failed")
			assert.Contains(t, stderr, "sensitive")
		})
	}
}

func TestCommandApplyPrintsPlanBeforeExecution(t *testing.T) {
	t.Parallel()
	planned := mustPlan(t)
	wantPlan, err := plan.Display(planned)
	require.NoError(t, err)
	wantOutputs, err := plan.DisplayValues(map[string]cty.Value{"answer": cty.StringVal("42")})
	require.NoError(t, err)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runtime := &fakeRuntime{planned: planned, outputs: map[string]cty.Value{"answer": cty.StringVal("42")}}
	runtime.applyHook = func() {
		assert.Equal(t, wantPlan, stdout.String())
	}
	command := cli.NewCommand(runtime)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply", t.TempDir()})

	err = command.ExecuteContext(t.Context())

	require.NoError(t, err)
	assert.Equal(t, wantPlan+wantOutputs, stdout.String())
	assert.Empty(t, stderr.String())
}

func TestCommandApplyPrintsPlanWhenExecutionFails(t *testing.T) {
	t.Parallel()
	planned := mustPlan(t)
	wantPlan, err := plan.Display(planned)
	require.NoError(t, err)
	runtime := &fakeRuntime{planned: planned, applyErr: assert.AnError}

	stdout, _, err := execute(t, runtime, "apply", t.TempDir())

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, wantPlan, stdout)
}

func TestCommandApplyFailsFastWhenPlanCannotBeWritten(t *testing.T) {
	t.Parallel()
	applyCalled := false
	runtime := &fakeRuntime{planned: mustPlan(t), applyHook: func() { applyCalled = true }}
	command := cli.NewCommand(runtime)
	command.SetOut(failingWriter{})
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"apply", t.TempDir()})

	err := command.ExecuteContext(t.Context())

	require.ErrorContains(t, err, "write plan")
	assert.False(t, applyCalled)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, assert.AnError
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

func TestCommandApplyFailsFastWhenTargetCannotBeInspected(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "missing.r42plan")

	_, _, err := execute(t, &fakeRuntime{}, "apply", target)

	require.Error(t, err)
	require.ErrorContains(t, err, "inspect apply target")
	assert.ErrorIs(t, err, os.ErrNotExist)
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
	planned, err := plan.NewWithContextAndLocals(t.TempDir(), nil, nil, nil, nil)
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
	assert.Equal(t, 10, runtime.configOptions.Parallelism)
}

func TestApplyDefaultTimeout(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	_, _, err := execute(t, runtime, "apply", t.TempDir())
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Hour), runtime.applyDeadlineAt, time.Second)
}

func TestApplyRedactsSensitiveOutputFromStdout(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{
		planned: mustPlan(t),
		outputs: map[string]cty.Value{
			"secret": corespec.MarkSensitive(cty.StringVal("do-not-print")),
		},
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

func (*blockingPlanRuntime) Config(
	_ string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	ctx := options.Context
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("plan context has no deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *blockingPlanRuntime) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		r.applyCalled = true
		return nil, nil, nil
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}

type runtimeFunc struct {
	plan  *plan.Plan
	apply func(context.Context) error
}

func (r runtimeFunc) Config(
	_ string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return r.config(r.plan, options)
}

func (r runtimeFunc) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return r.config(planned, options)
}

func (r runtimeFunc) config(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		return nil, nil, r.apply(options.Context)
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}
