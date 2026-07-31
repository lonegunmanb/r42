package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

func TestRunMapsErrorsToProcessExitCodesAndStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctx     context.Context
		args    []string
		runtime cli.Runtime
		want    int
	}{
		{name: "usage", ctx: t.Context(), args: []string{"apply"}, runtime: stubRuntime{}, want: cli.ExitUsage},
		{name: "runtime", ctx: t.Context(), args: []string{"apply", t.TempDir()}, runtime: stubRuntime{applyErr: errors.New("failed")}, want: cli.ExitFailure},
		{name: "signal", ctx: canceledContext(), args: []string{"apply", t.TempDir()}, runtime: stubRuntime{applyErr: context.Canceled}, want: 130},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(test.ctx, test.args, &stdout, &stderr, test.runtime)
			assert.Equal(t, test.want, code)
			assert.Empty(t, stdout.String())
			assert.NotEmpty(t, stderr.String())
		})
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
		[]string{"apply", t.TempDir()},
		&stdout,
		&stderr,
		stubRuntime{applyErr: fmt.Errorf("validate configuration: %w", diagnostics)},
	)

	assert.Equal(t, cli.ExitFailure, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "first invalid expression")
	assert.Contains(t, stderr.String(), "second invalid expression")
	assert.NotContains(t, stderr.String(), "other diagnostic(s)")
}

type stubRuntime struct{ applyErr error }

func (stubRuntime) Plan(context.Context, string, []golden.CliFlagAssignedVariables) (*plan.Plan, error) {
	return plan.New("", nil, nil)
}

func (r stubRuntime) Apply(context.Context, *plan.Plan, cli.ApplyOptions) (cli.ApplyResult, error) {
	return cli.ApplyResult{Outputs: map[string]cty.Value{}}, r.applyErr
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
