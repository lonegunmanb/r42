package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRunMapsErrorsToProcessExitCodesAndStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ctx      context.Context
		args     []string
		runtime  cli.Runtime
		want     int
		wantPlan bool
	}{
		{name: "usage", ctx: t.Context(), args: []string{"apply", "one.r42plan", "two.r42plan"}, runtime: stubRuntime{}, want: cli.ExitUsage},
		{name: "runtime", ctx: t.Context(), args: []string{"apply"}, runtime: stubRuntime{applyErr: errors.New("failed")}, want: cli.ExitFailure, wantPlan: true},
		{name: "signal", ctx: canceledContext(), args: []string{"apply"}, runtime: stubRuntime{applyErr: context.Canceled}, want: 130, wantPlan: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(test.ctx, test.args, nil, &stdout, &stderr, test.runtime)
			assert.Equal(t, test.want, code)
			assert.Equal(t, test.wantPlan, strings.Contains(stdout.String(), `"nodes"`))
			assert.NotEmpty(t, stderr.String())
		})
	}
}

func TestRunDispatchesInternalStarlarkWorkerBeforeCLI(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(
		t.Context(),
		[]string{"--internal-starlark-worker"},
		bytes.NewBufferString(`{"code":"result = 4","data_json":"null"}`),
		&stdout,
		&stderr,
		stubRuntime{},
	)

	assert.Equal(t, cli.ExitSuccess, code)
	var response struct {
		Result struct {
			ResultJSON string `json:"result_json"`
			Stdout     string `json:"stdout"`
			Steps      uint64 `json:"steps"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
	assert.Equal(t, "4", response.Result.ResultJSON)
	assert.Empty(t, response.Result.Stdout)
	assert.Positive(t, response.Result.Steps)
	assert.Empty(t, stderr.String())
}

func TestRestoreInterruptHandlingAfterFirstCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	restoreInterruptHandling(ctx, func() { close(stopped) })

	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("signal handling was not restored after cancellation")
	}
}

func TestRunDisplaysEveryHCLDiagnostic(t *testing.T) {
	t.Parallel()
	diagnostics := hcl.Diagnostics{
		&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "first invalid expression",
			Detail:   "the first expression is invalid",
		},
		&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "second invalid expression",
			Detail:   "the second expression is invalid",
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(
		t.Context(),
		[]string{"apply"},
		nil,
		&stdout,
		&stderr,
		stubRuntime{applyErr: fmt.Errorf("validate configuration: %w", diagnostics)},
	)

	assert.Equal(t, cli.ExitFailure, code)
	assert.Contains(t, stdout.String(), `"nodes"`)
	assert.Contains(t, stderr.String(), "first invalid expression")
	assert.Contains(t, stderr.String(), "second invalid expression")
	assert.NotContains(t, stderr.String(), "other diagnostic(s)")
}

//nolint:paralleltest // t.Chdir isolates the initialized schema fixture.
func TestRunSchemaDoesNotLeakSensitiveDefaultInDiagnostics(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	configDirectory := filepath.Join(workingDirectory, ".r42", "config")
	require.NoError(t, os.MkdirAll(configDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDirectory, "variables.r42.hcl"), []byte(`
variable "token" {
  type      = number
  sensitive = true
  default   = "never-print-this-secret"
}
`), 0o600))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(t.Context(), []string{"schema", "--json"}, nil, &stdout, &stderr, stubRuntime{})

	assert.Equal(t, cli.ExitFailure, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), `variable "token" default is incompatible with number`)
	assert.NotContains(t, stderr.String(), "never-print-this-secret")
}

func TestRunAcceptsTerraformStyleVariableFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "separate values",
			args: []string{"plan", "-var", "topic=markets", "-var-file", "inputs.r42vars"},
		},
		{
			name: "equals values",
			args: []string{"apply", "-var=topic=markets", "-var-file=inputs.r42vars"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			runtime := new(variableCapturingRuntime)

			code := run(t.Context(), test.args, nil, &stdout, &stderr, runtime)

			assert.Equal(t, cli.ExitSuccess, code)
			assert.Len(t, runtime.variables, 2)
			if test.args[0] == "apply" {
				assert.Contains(t, stderr.String(), "Research tasks: 0")
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}
}

type variableCapturingRuntime struct {
	stubRuntime
	variables []golden.CliFlagAssignedVariables
}

func (r *variableCapturingRuntime) Config(
	_ string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	r.variables = options.Variables
	return r.stubRuntime.Config("", options)
}

type stubRuntime struct{ applyErr error }

func (stubRuntime) OpenProject(stateDirectory string) (string, string, error) {
	return stateDirectory + "/config", stateDirectory + "/modules", nil
}

func (stubRuntime) SaveProjectOutputs(string, string, map[string]cty.Value) error {
	return nil
}

func (stubRuntime) ReadProjectOutputs(string) ([]byte, error) {
	return []byte(`{}`), nil
}

func (r stubRuntime) Config(
	_ string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	planned, err := plan.NewWithContextAndLocals("", nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return r.config(planned, options)
}

func (r stubRuntime) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return r.config(planned, options)
}

func (r stubRuntime) config(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		return map[string]cty.Value{}, nil, r.applyErr
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
