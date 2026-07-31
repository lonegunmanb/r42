package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/zclconf/go-cty/cty"
)

const savedPlanBlockType = "r42_internal_apply"

type savedPlanBlock struct {
	*golden.BaseBlock
	Index int `hcl:"index"`

	execution *planExecution
}

func (*savedPlanBlock) Type() string            { return "" }
func (*savedPlanBlock) BlockType() string       { return savedPlanBlockType }
func (*savedPlanBlock) AddressLength() int      { return 2 }
func (*savedPlanBlock) CanExecutePrePlan() bool { return false }

func (b *savedPlanBlock) ExecuteDuringPlan() error {
	if b.execution == nil {
		return fmt.Errorf("saved plan execution is required")
	}
	return b.execution.execute(b.Index)
}

type planExecution struct {
	ctx     context.Context
	cancel  context.CancelFunc
	factory Factory
	scope   *r42concurrency.Scope
	nodes   []plan.NodeSpec

	mu       sync.Mutex
	failure  error
	warnings []error
}

func newPlanExecution(
	ctx context.Context,
	cancel context.CancelFunc,
	factory Factory,
	scope *r42concurrency.Scope,
	nodes []plan.NodeSpec,
) *planExecution {
	return &planExecution{ctx: ctx, cancel: cancel, factory: factory, scope: scope, nodes: nodes}
}

func (e *planExecution) execute(index int) error {
	if index < 0 || index >= len(e.nodes) {
		return fmt.Errorf("saved plan node index %d is out of range", index)
	}
	node := e.nodes[index]
	return debuglog.PlanBlock(e.ctx, node.Address, node.Kind, func() error {
		if err := e.ctx.Err(); err != nil {
			return e.recordFailure(fmt.Errorf("apply block %s: %w", node.Address, err))
		}
		work := func(ctx context.Context) error { return e.executeBlock(ctx, node) }
		if node.Kind != "research" {
			return work(e.ctx)
		}
		called := false
		err := e.scope.WithResearch(e.ctx, func(ctx context.Context) error {
			called = true
			return work(ctx)
		})
		if err != nil && !called {
			return e.recordFailure(fmt.Errorf("apply block %s: %w", node.Address, err))
		}
		return err
	})
}

func (e *planExecution) executeBlock(ctx context.Context, node plan.NodeSpec) error {
	event := debuglog.Event{
		BlockAddress: node.Address, BlockType: node.Kind, Dependencies: node.Dependencies,
	}
	factoryStarted := time.Now()
	if err := debuglog.Lifecycle(ctx, "block.factory", debuglog.StatusStarted, event); err != nil {
		return e.recordFailure(err)
	}
	block, err := e.factory.New(ctx, node, e.scope)
	if err != nil {
		err = fmt.Errorf("create apply block %s: %w", node.Address, err)
	} else if block == nil {
		err = fmt.Errorf("create apply block %s: factory returned nil", node.Address)
	}
	factoryLogErr := debuglog.CompleteLifecycle(ctx, "block.factory", factoryStarted, err, event)
	err = errors.Join(err, factoryLogErr)
	if err == nil {
		applyStarted := time.Now()
		if err = debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusStarted, event); err == nil {
			if applyErr := block.Apply(); applyErr != nil {
				err = fmt.Errorf("apply block %s: %w", node.Address, applyErr)
			} else if contextErr := ctx.Err(); contextErr != nil {
				err = fmt.Errorf("apply block %s: %w", node.Address, contextErr)
			}
			err = errors.Join(err, debuglog.CompleteLifecycle(ctx, "block.apply", applyStarted, err, event))
		}
	}
	if err != nil {
		e.fail(err)
	}
	if cleanup, ok := block.(CleanupBlock); ok {
		cleanupStarted := time.Now()
		cleanupErr := debuglog.Lifecycle(ctx, "block.cleanup", debuglog.StatusStarted, event)
		if cleanupErr == nil {
			cleanupErr = cleanup.Cleanup(context.WithoutCancel(ctx))
			cleanupErr = errors.Join(cleanupErr, debuglog.CompleteLifecycle(ctx, "block.cleanup", cleanupStarted, cleanupErr, event))
		}
		if cleanupErr != nil {
			e.addWarning(fmt.Errorf("cleanup block %s: %w", node.Address, cleanupErr))
		}
	}
	if err == nil {
		if contextErr := ctx.Err(); contextErr != nil {
			err = fmt.Errorf("apply block %s: %w", node.Address, contextErr)
			e.fail(err)
		}
	}
	return err
}

func (e *planExecution) recordFailure(err error) error {
	e.fail(err)
	return err
}

func (e *planExecution) fail(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failure != nil {
		return
	}
	e.failure = err
	e.cancel()
}

func (e *planExecution) failureError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.failure
}

func (e *planExecution) addWarning(warning error) {
	e.mu.Lock()
	e.warnings = append(e.warnings, warning)
	e.mu.Unlock()
}

func (e *planExecution) cleanupWarnings() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.warnings...)
}

var registerSavedPlanBlock sync.Once

func runSavedPlan(directory string, execution *planExecution) error {
	registerSavedPlanBlock.Do(func() { golden.RegisterBlock(new(savedPlanBlock)) })
	buildStarted := time.Now()
	if err := debuglog.Lifecycle(execution.ctx, "apply.saved_hcl.build", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}); err != nil {
		return err
	}
	blocks, err := savedPlanHCLBlocks(execution.nodes)
	err = errors.Join(err, debuglog.CompleteLifecycle(execution.ctx, "apply.saved_hcl.build", buildStarted, err, debuglog.Event{
		Path: directory, Count: len(blocks),
	}))
	if err != nil {
		return err
	}
	base := config.NewBaseConfig(golden.NewBaseConfigArgs{Basedir: directory, Ctx: execution.ctx})
	initStarted := time.Now()
	if err = debuglog.Lifecycle(execution.ctx, "apply.golden.config.init", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(blocks),
	}); err != nil {
		return err
	}
	initErr := golden.InitConfig(base, blocks)
	initErr = errors.Join(initErr, debuglog.CompleteLifecycle(execution.ctx, "apply.golden.config.init", initStarted, initErr, debuglog.Event{
		Path: directory, Count: len(blocks),
	}))
	if initErr != nil {
		err = initErr
		return fmt.Errorf("initialize saved plan: %w", err)
	}
	for _, block := range golden.Blocks[*savedPlanBlock](base) {
		block.execution = execution
	}
	planStarted := time.Now()
	if err = debuglog.Lifecycle(execution.ctx, "apply.golden.run_plan", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(blocks),
	}); err != nil {
		return err
	}
	runPlanErr := base.RunPlan()
	runPlanErr = errors.Join(runPlanErr, debuglog.CompleteLifecycle(execution.ctx, "apply.golden.run_plan", planStarted, runPlanErr, debuglog.Event{
		Path: directory, Count: len(blocks),
	}))
	if runPlanErr != nil {
		err = runPlanErr
		return fmt.Errorf("run saved plan: %w", err)
	}
	return nil
}

func savedPlanHCLBlocks(nodes []plan.NodeSpec) ([]*golden.HclBlock, error) {
	file := hclwrite.NewEmptyFile()
	names := make(map[string]string, len(nodes))
	for index, node := range nodes {
		names[node.Address] = fmt.Sprintf("node_%d", index)
	}
	for index, node := range nodes {
		block := hclwrite.NewBlock(savedPlanBlockType, []string{names[node.Address]})
		block.Body().SetAttributeValue("index", cty.NumberIntVal(int64(index)))
		if len(node.Dependencies) > 0 {
			dependencies := make([]hclwrite.Tokens, 0, len(node.Dependencies))
			for _, dependency := range node.Dependencies {
				name, ok := names[dependency]
				if !ok {
					return nil, fmt.Errorf("saved plan node %s depends on unknown node %s", node.Address, dependency)
				}
				dependencies = append(dependencies, hclwrite.TokensForTraversal(hcl.Traversal{
					hcl.TraverseRoot{Name: savedPlanBlockType},
					hcl.TraverseAttr{Name: name},
				}))
			}
			block.Body().SetAttributeRaw("depends_on", hclwrite.TokensForTuple(dependencies))
		}
		file.Body().AppendBlock(block)
	}
	source := file.Bytes()
	syntaxFile, diagnostics := hclsyntax.ParseConfig(source, "<saved-r42-plan>", hcl.InitialPos)
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	body, ok := syntaxFile.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("saved plan has an unexpected syntax body")
	}
	return golden.AsHclBlocks(body.Blocks, file.Body().Blocks()), nil
}
