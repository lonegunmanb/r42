package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
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
}

func (e *planExecution) executeBlock(ctx context.Context, node plan.NodeSpec) error {
	block, err := e.factory.New(ctx, node, e.scope)
	if err != nil {
		err = fmt.Errorf("create apply block %s: %w", node.Address, err)
	} else if block == nil {
		err = fmt.Errorf("create apply block %s: factory returned nil", node.Address)
	} else if applyErr := block.Apply(); applyErr != nil {
		err = fmt.Errorf("apply block %s: %w", node.Address, applyErr)
	} else if contextErr := ctx.Err(); contextErr != nil {
		err = fmt.Errorf("apply block %s: %w", node.Address, contextErr)
	}
	if err != nil {
		e.fail(err)
	}
	if cleanup, ok := block.(CleanupBlock); ok {
		if cleanupErr := cleanup.Cleanup(context.WithoutCancel(ctx)); cleanupErr != nil {
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
	blocks, err := savedPlanHCLBlocks(execution.nodes)
	if err != nil {
		return err
	}
	base := config.NewBaseConfig(golden.NewBaseConfigArgs{Basedir: directory, Ctx: execution.ctx})
	if err = golden.InitConfig(base, blocks); err != nil {
		return fmt.Errorf("initialize saved plan: %w", err)
	}
	for _, block := range golden.Blocks[*savedPlanBlock](base) {
		block.execution = execution
	}
	if err = base.RunPlan(); err != nil {
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
