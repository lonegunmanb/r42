package runtime

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/zclconf/go-cty/cty"
)

type NestedExecutor interface {
	Apply(context.Context, *plan.Plan, int) (map[string]cty.Value, error)
}

type Factory interface {
	New() (NestedExecutor, error)
}

type Config struct {
	Plan        *plan.Plan
	Parallelism int
	Timeout     time.Duration
}

type Result struct {
	Outputs map[string]cty.Value
}

type Runner struct {
	factory Factory
}

func NewRunner(factory Factory) *Runner {
	return &Runner{factory: factory}
}

func (r *Runner) Run(ctx context.Context, config Config) (Result, error) {
	if r.factory == nil {
		return Result{}, fmt.Errorf("nested executor factory is required")
	}
	if config.Plan == nil {
		return Result{}, fmt.Errorf("saved module plan is required")
	}
	if config.Parallelism < 0 {
		return Result{}, fmt.Errorf("module parallelism must not be negative")
	}
	if config.Timeout < 0 {
		return Result{}, fmt.Errorf("module timeout must not be negative")
	}
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}
	executor, err := r.factory.New()
	if err != nil {
		return Result{}, fmt.Errorf("create nested executor: %w", err)
	}
	if executor == nil {
		return Result{}, fmt.Errorf("nested executor factory returned nil")
	}
	outputs, err := executor.Apply(ctx, config.Plan, config.Parallelism)
	if err != nil {
		return Result{}, fmt.Errorf("apply saved module plan: %w", err)
	}
	if err = validateOutputs(config.Plan.Outputs(), outputs); err != nil {
		return Result{}, err
	}
	result := make(map[string]cty.Value, len(outputs))
	maps.Copy(result, outputs)
	return Result{Outputs: result}, nil
}

func validateOutputs(planned map[string]plan.OutputSpec, actual map[string]cty.Value) error {
	for name, output := range planned {
		value, exists := actual[name]
		if !exists {
			return fmt.Errorf("module output %q is missing", name)
		}
		if !value.Type().Equals(output.Value.Type()) {
			return fmt.Errorf(
				"module output %q has type %s; planned %s",
				name,
				value.Type().FriendlyName(),
				output.Value.Type().FriendlyName(),
			)
		}
	}
	for name := range actual {
		if _, exists := planned[name]; !exists {
			return fmt.Errorf("module output %q was not planned", name)
		}
	}
	return nil
}
