package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/hclfuncs"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"golang.org/x/sync/errgroup"
)

func (f *runtimeFactory) newDynamicResearchBlock(
	ctx context.Context,
	node plan.NodeSpec,
	scope *r42concurrency.Scope,
) (golden.ApplyBlock, error) {
	planned, err := modulespec.DecodeDynamicResearchPlan(node.Config)
	if err != nil {
		return nil, err
	}
	tasks, err := f.evaluateDynamicTasks(node.Address, planned.Expression)
	if err != nil {
		return nil, err
	}
	configs, taskValues, err := researchspec.DecodeDynamicTasks(tasks)
	if err != nil {
		return nil, err
	}
	resolved := make([]modulespec.ResearchPlan, len(configs))
	for index, taskConfig := range configs {
		resolved[index], err = planned.Resolve(taskConfig)
		if err != nil {
			return nil, fmt.Errorf("dynamic research task %d: %w", index, err)
		}
		if err = modulespec.ValidateResearchToolIDs(taskConfig, f.tools); err != nil {
			return nil, fmt.Errorf("dynamic research task %d: %w", index, err)
		}
	}
	return &dynamicResearchApplyBlock{
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: node.Address,
		factory: f, scope: scope, plans: resolved, tasks: taskValues,
	}, nil
}

func (f *runtimeFactory) evaluateDynamicTasks(address, source string) (cty.Value, error) {
	value, err := f.evaluateResearchExpression(address, source, "dynamic research tasks")
	if err != nil {
		return cty.NilVal, err
	}
	if !value.IsWhollyKnown() {
		return cty.NilVal, fmt.Errorf("dynamic research tasks must be known before apply")
	}
	return value, nil
}

func (f *runtimeFactory) evaluateResearchExpression(address, source, description string) (cty.Value, error) {
	expression, diagnostics := hclsyntax.ParseExpression(
		[]byte(source), "saved-research-expression", hcl.InitialPos,
	)
	if diagnostics.HasErrors() {
		return cty.NilVal, fmt.Errorf("parse %s: %w", description, diagnostics)
	}
	contextValues := maps.Clone(f.contextValues)
	f.mu.Lock()
	for resultAddress, result := range f.results {
		setBlockResult(contextValues, resultAddress, result)
	}
	f.mu.Unlock()
	functions := hclfuncs.Functions(f.directory)
	maps.Copy(functions, config.Functions())
	workspace, err := f.run.WorkspacePath(f.CanonicalAddress(address))
	if err != nil {
		return cty.NilVal, err
	}
	functions["block_wd"] = function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) {
			return cty.StringVal(workspace), nil
		},
	})
	evaluationContext := &hcl.EvalContext{Variables: contextValues, Functions: functions}
	if err = evaluateLocals(evaluationContext, f.localExpressions); err != nil {
		return cty.NilVal, err
	}
	value, diagnostics := expression.Value(evaluationContext)
	if diagnostics.HasErrors() {
		return cty.NilVal, fmt.Errorf("evaluate %s: %w", description, diagnostics)
	}
	return value, nil
}

type dynamicResearchApplyBlock struct {
	*golden.BaseBlock
	ctx     context.Context
	address string
	factory *runtimeFactory
	scope   *r42concurrency.Scope
	plans   []modulespec.ResearchPlan
	tasks   []cty.Value

	mu       sync.Mutex
	warnings []error
}

func (*dynamicResearchApplyBlock) Type() string { return "dynamic" }

func (*dynamicResearchApplyBlock) BlockType() string { return "research" }

func (*dynamicResearchApplyBlock) AddressLength() int { return 3 }

func (*dynamicResearchApplyBlock) CanExecutePrePlan() bool { return false }

func (b *dynamicResearchApplyBlock) Address() string { return b.address }

func (b *dynamicResearchApplyBlock) Apply() error {
	addresses := make([]string, len(b.plans))
	for index := range b.plans {
		addresses[index] = fmt.Sprintf("%s.tasks[%d]", b.address, index)
	}
	if err := b.factory.recorder.Record(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized", Status: debuglog.StatusCompleted,
		BlockAddress: b.factory.CanonicalAddress(b.address), BlockType: "research", Paths: b.canonicalAddresses(addresses),
		Count: len(addresses),
	}); err != nil {
		return err
	}
	if len(b.plans) == 0 {
		b.factory.publish(b.address, cty.ObjectVal(map[string]cty.Value{"tasks": cty.EmptyTupleVal}))
		return nil
	}

	results := make([]cty.Value, len(b.plans))
	group, ctx := errgroup.WithContext(b.ctx)
	group.SetLimit(b.scope.Limit())
	for index := range b.plans {
		group.Go(func() error {
			return b.scope.WithResearch(ctx, func(taskContext context.Context) error {
				return b.applyTask(taskContext, addresses[index], index, &results[index])
			})
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for index := range results {
		results[index] = researchspec.AppliedDynamicTaskValue(b.tasks[index], results[index])
	}
	b.factory.publish(b.address, cty.ObjectVal(map[string]cty.Value{"tasks": cty.TupleVal(results)}))
	return nil
}

func (b *dynamicResearchApplyBlock) applyTask(
	ctx context.Context,
	address string,
	index int,
	result *cty.Value,
) error {
	event := debuglog.Event{BlockAddress: b.factory.CanonicalAddress(address), BlockType: "research"}
	started := time.Now()
	if err := b.recordLifecycle("block.factory", debuglog.StatusStarted, event, started, nil); err != nil {
		return err
	}
	block, err := b.factory.newResearchBlock(ctx, address, b.plans[index], func(_ string, value cty.Value) {
		*result = value
	})
	if block == nil && err == nil {
		err = fmt.Errorf("create dynamic research task %s: factory returned nil", address)
	}
	if recordErr := b.recordLifecycle("block.factory", lifecycleStatus(err), event, started, err); recordErr != nil {
		err = errors.Join(err, recordErr)
	}
	if err != nil {
		return err
	}

	started = time.Now()
	if err = b.recordLifecycle("block.apply", debuglog.StatusStarted, event, started, nil); err == nil {
		err = block.Apply()
		if err == nil {
			err = ctx.Err()
		}
		err = errors.Join(err, b.recordLifecycle("block.apply", lifecycleStatus(err), event, started, err))
	}
	if cleanup, ok := block.(executorCleanupBlock); ok {
		cleanupStarted := time.Now()
		cleanupErr := b.recordLifecycle("block.cleanup", debuglog.StatusStarted, event, cleanupStarted, nil)
		if cleanupErr == nil {
			cleanupErr = cleanup.Cleanup(context.WithoutCancel(ctx))
			cleanupErr = errors.Join(cleanupErr, b.recordLifecycle(
				"block.cleanup", lifecycleStatus(cleanupErr), event, cleanupStarted, cleanupErr,
			))
		}
		if cleanupErr != nil {
			b.addWarning(fmt.Errorf("cleanup dynamic research task %s: %w", address, cleanupErr))
		}
	}
	if err != nil {
		return fmt.Errorf("apply dynamic research task %s: %w", address, err)
	}
	return nil
}

type executorCleanupBlock interface {
	Cleanup(context.Context) error
}

func (b *dynamicResearchApplyBlock) Cleanup(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return errors.Join(b.warnings...)
}

func (b *dynamicResearchApplyBlock) addWarning(err error) {
	b.mu.Lock()
	b.warnings = append(b.warnings, err)
	b.mu.Unlock()
}

func (b *dynamicResearchApplyBlock) canonicalAddresses(addresses []string) []string {
	result := slices.Clone(addresses)
	for index := range result {
		result[index] = b.factory.CanonicalAddress(result[index])
	}
	return result
}

func (b *dynamicResearchApplyBlock) recordLifecycle(
	action string,
	status debuglog.EventStatus,
	event debuglog.Event,
	started time.Time,
	cause error,
) error {
	event.Kind = debuglog.EventLifecycle
	event.Action = action
	event.Status = status
	if status != debuglog.StatusStarted {
		duration := time.Since(started).Milliseconds()
		event.DurationMS = &duration
	}
	if cause != nil {
		event.Error = cause.Error()
	}
	return b.factory.recorder.Record(event)
}

func lifecycleStatus(err error) debuglog.EventStatus {
	if err != nil {
		return debuglog.StatusFailed
	}
	return debuglog.StatusCompleted
}
