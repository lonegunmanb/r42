package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	moduleruntime "github.com/lonegunmanb/r42/internal/module/runtime"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRunnerAppliesSavedPlanAndPublishesOutputsAtomically(t *testing.T) {
	t.Parallel()

	saved := savedPlan(t)
	executor := &fakeExecutor{outputs: map[string]cty.Value{"summary": cty.StringVal("done")}}
	factory := &fakeFactory{executors: []moduleruntime.NestedExecutor{executor}}
	runner := moduleruntime.NewRunner(factory)

	result, err := runner.Run(t.Context(), moduleruntime.Config{Plan: saved, Parallelism: 3})

	require.NoError(t, err)
	assert.Equal(t, "done", result.Outputs["summary"].AsString())
	assert.Same(t, saved, executor.plan)
	assert.Equal(t, 3, executor.parallelism)
	executor.outputs["summary"] = cty.StringVal("changed")
	assert.Equal(t, "done", result.Outputs["summary"].AsString())
}

func TestRunnerCreatesIsolatedExecutorForEveryModule(t *testing.T) {
	t.Parallel()

	first := &fakeExecutor{outputs: map[string]cty.Value{"summary": cty.StringVal("first")}}
	second := &fakeExecutor{outputs: map[string]cty.Value{"summary": cty.StringVal("second")}}
	factory := &fakeFactory{executors: []moduleruntime.NestedExecutor{first, second}}
	runner := moduleruntime.NewRunner(factory)

	firstResult, err := runner.Run(t.Context(), moduleruntime.Config{Plan: savedPlan(t)})
	require.NoError(t, err)
	secondResult, err := runner.Run(t.Context(), moduleruntime.Config{Plan: savedPlan(t)})
	require.NoError(t, err)

	assert.Equal(t, "first", firstResult.Outputs["summary"].AsString())
	assert.Equal(t, "second", secondResult.Outputs["summary"].AsString())
	assert.Equal(t, 2, factory.calls)
}

func TestRunnerDoesNotPublishPartialOutputsOnFailure(t *testing.T) {
	t.Parallel()

	applyErr := errors.New("child node failed")
	executor := &fakeExecutor{outputs: map[string]cty.Value{"summary": cty.StringVal("partial")}, err: applyErr}
	runner := moduleruntime.NewRunner(&fakeFactory{executors: []moduleruntime.NestedExecutor{executor}})

	result, err := runner.Run(t.Context(), moduleruntime.Config{Plan: savedPlan(t)})

	require.ErrorIs(t, err, applyErr)
	assert.Nil(t, result.Outputs)
}

func TestRunnerRejectsOutputsThatDoNotMatchSavedPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outputs map[string]cty.Value
		error   string
	}{
		{name: "missing", outputs: map[string]cty.Value{}, error: `module output "summary" is missing`},
		{name: "extra", outputs: map[string]cty.Value{"summary": cty.StringVal("ok"), "extra": cty.StringVal("bad")}, error: `module output "extra" was not planned`},
		{name: "wrong type", outputs: map[string]cty.Value{"summary": cty.NumberIntVal(1)}, error: `module output "summary" has type number; planned string`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := moduleruntime.NewRunner(&fakeFactory{executors: []moduleruntime.NestedExecutor{&fakeExecutor{outputs: tt.outputs}}})

			result, err := runner.Run(t.Context(), moduleruntime.Config{Plan: savedPlan(t)})

			require.EqualError(t, err, tt.error)
			assert.Nil(t, result.Outputs)
		})
	}
}

func TestRunnerUsesEarliestParentOrModuleDeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		parentTimeout time.Duration
		moduleTimeout time.Duration
	}{
		{name: "parent earlier", parentTimeout: 100 * time.Millisecond, moduleTimeout: time.Hour},
		{name: "module earlier", parentTimeout: time.Hour, moduleTimeout: 100 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), tt.parentTimeout)
			defer cancel()
			executor := &fakeExecutor{outputs: map[string]cty.Value{"summary": cty.StringVal("ok")}}
			runner := moduleruntime.NewRunner(&fakeFactory{executors: []moduleruntime.NestedExecutor{executor}})
			started := time.Now()

			_, err := runner.Run(ctx, moduleruntime.Config{Plan: savedPlan(t), Timeout: tt.moduleTimeout})

			require.NoError(t, err)
			require.NotZero(t, executor.deadline)
			assert.Less(t, executor.deadline.Sub(started), 500*time.Millisecond)
		})
	}
}

func TestRunnerPropagatesModuleTimeoutCancellation(t *testing.T) {
	t.Parallel()

	executor := &fakeExecutor{waitForCancellation: true}
	runner := moduleruntime.NewRunner(&fakeFactory{executors: []moduleruntime.NestedExecutor{executor}})

	_, err := runner.Run(t.Context(), moduleruntime.Config{Plan: savedPlan(t), Timeout: 20 * time.Millisecond})

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRunnerRejectsInvalidConfigurationAndFactoryResults(t *testing.T) {
	t.Parallel()

	factoryErr := errors.New("factory failed")
	tests := []struct {
		name          string
		runner        *moduleruntime.Runner
		config        moduleruntime.Config
		expectedError string
	}{
		{name: "factory required", runner: moduleruntime.NewRunner(nil), config: moduleruntime.Config{Plan: savedPlan(t)}, expectedError: "nested executor factory is required"},
		{name: "plan required", runner: moduleruntime.NewRunner(&fakeFactory{}), expectedError: "saved module plan is required"},
		{name: "parallelism nonnegative", runner: moduleruntime.NewRunner(&fakeFactory{}), config: moduleruntime.Config{Plan: savedPlan(t), Parallelism: -1}, expectedError: "module parallelism must not be negative"},
		{name: "timeout nonnegative", runner: moduleruntime.NewRunner(&fakeFactory{}), config: moduleruntime.Config{Plan: savedPlan(t), Timeout: -time.Second}, expectedError: "module timeout must not be negative"},
		{name: "factory error", runner: moduleruntime.NewRunner(&fakeFactory{err: factoryErr}), config: moduleruntime.Config{Plan: savedPlan(t)}, expectedError: "create nested executor: factory failed"},
		{name: "nil executor", runner: moduleruntime.NewRunner(&fakeFactory{executors: []moduleruntime.NestedExecutor{nil}}), config: moduleruntime.Config{Plan: savedPlan(t)}, expectedError: "nested executor factory returned nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.runner.Run(t.Context(), tt.config)

			require.EqualError(t, err, tt.expectedError)
		})
	}
}

type fakeFactory struct {
	executors []moduleruntime.NestedExecutor
	calls     int
	err       error
}

func (f *fakeFactory) New() (moduleruntime.NestedExecutor, error) {
	if f.err != nil {
		return nil, f.err
	}
	executor := f.executors[f.calls]
	f.calls++
	return executor, nil
}

type fakeExecutor struct {
	outputs             map[string]cty.Value
	err                 error
	plan                *plan.Plan
	parallelism         int
	deadline            time.Time
	waitForCancellation bool
}

func (e *fakeExecutor) Apply(ctx context.Context, saved *plan.Plan, parallelism int) (map[string]cty.Value, error) {
	e.plan = saved
	e.parallelism = parallelism
	e.deadline, _ = ctx.Deadline()
	if e.waitForCancellation {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return e.outputs, e.err
}

func savedPlan(t *testing.T) *plan.Plan {
	t.Helper()
	saved, err := plan.New("D:/child", nil, map[string]plan.OutputSpec{
		"summary": {Value: cty.UnknownVal(cty.String), Description: "summary"},
	})
	require.NoError(t, err)
	return saved
}
