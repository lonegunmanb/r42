package executor

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/Azure/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/zclconf/go-cty/cty"
)

type Factory interface {
	New(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error)
}

type OutputResolver interface {
	ResolveOutputs(*plan.Plan) (map[string]cty.Value, error)
}

type CleanupBlock interface {
	Cleanup(context.Context) error
}

type Executor struct {
	applyMu  sync.Mutex
	mu       sync.Mutex
	factory  Factory
	debug    io.Closer
	warnings []error
}

func New(factory Factory, debug io.Closer) *Executor {
	return &Executor{factory: factory, debug: debug}
}

func (e *Executor) Apply(
	ctx context.Context,
	planned *plan.Plan,
	parallelism int,
) (map[string]cty.Value, error) {
	scope, err := r42concurrency.NewScope(parallelism)
	if err != nil {
		return nil, err
	}
	return e.apply(ctx, planned, scope, true)
}

func (e *Executor) ApplyInScope(
	ctx context.Context,
	planned *plan.Plan,
	scope *r42concurrency.Scope,
) (map[string]cty.Value, error) {
	return e.apply(ctx, planned, scope, false)
}

func (e *Executor) apply(
	ctx context.Context,
	planned *plan.Plan,
	scope *r42concurrency.Scope,
	closeDebug bool,
) (map[string]cty.Value, error) {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.setWarnings(nil)

	if e.factory == nil {
		return nil, fmt.Errorf("apply block factory is required")
	}
	if planned == nil {
		return nil, fmt.Errorf("saved plan is required")
	}
	if scope == nil {
		return nil, fmt.Errorf("concurrency scope is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	execution := newPlanExecution(runCtx, cancel, e.factory, scope, planned.Nodes())
	if contextErr := ctx.Err(); contextErr != nil {
		execution.fail(contextErr)
	}
	runErr := runSavedPlan(planned.Directory(), execution)
	failure := execution.failureError()
	if failure == nil && runErr != nil {
		failure = runErr
	}

	warnings := execution.cleanupWarnings()
	outputs := plannedOutputs(planned)
	if failure == nil {
		if resolver, ok := e.factory.(OutputResolver); ok {
			resolved, resolveErr := resolver.ResolveOutputs(planned)
			if resolveErr != nil {
				failure = fmt.Errorf("resolve apply outputs: %w", resolveErr)
			} else {
				outputs = resolved
			}
		}
	}
	if closeDebug && e.debug != nil {
		if closeErr := e.debug.Close(); closeErr != nil {
			warnings = append(warnings, fmt.Errorf("close debug log: %w", closeErr))
		}
	}
	e.setWarnings(warnings)
	if failure != nil {
		return nil, &ApplyError{cause: failure, cleanup: warnings}
	}
	return outputs, nil
}

func plannedOutputs(planned *plan.Plan) map[string]cty.Value {
	outputs := planned.Outputs()
	result := make(map[string]cty.Value, len(outputs))
	for name, output := range outputs {
		result[name] = output.Value
	}
	return result
}

func (e *Executor) Warnings() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.warnings...)
}

func (e *Executor) setWarnings(warnings []error) {
	e.mu.Lock()
	e.warnings = append([]error(nil), warnings...)
	e.mu.Unlock()
}

type ApplyError struct {
	cause   error
	cleanup []error
}

func (e *ApplyError) Error() string { return e.cause.Error() }

func (e *ApplyError) Unwrap() error { return e.cause }

func (e *ApplyError) CleanupWarnings() []error {
	return append([]error(nil), e.cleanup...)
}
