package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Azure/golden"
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
