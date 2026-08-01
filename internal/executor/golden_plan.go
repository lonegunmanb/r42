package executor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/run"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/zclconf/go-cty/cty"
)

var (
	_ golden.Config      = (*ResearchConfig)(nil)
	_ golden.Parallelism = (*ResearchConfig)(nil)
)

type ResearchConfig struct {
	*golden.BaseConfig
	directory              string
	runMu                  sync.Mutex
	run                    *run.Run
	addressPrefix          string
	childVariableDirectory string
	stack                  []string
	sensitiveVariables     map[string]struct{}
	source                 bool
	cleanupSource          func()
	execution              *planExecution
	parallelism            int
	applyPlan              func(*plan.Plan) (map[string]cty.Value, []error, error)
	applyMu                sync.Mutex
	outputs                map[string]cty.Value
	warnings               []error
	plan                   *ResearchPlan
}

func (c *ResearchConfig) Run() *run.Run {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.run
}

func newApplyResearchConfig(
	directory string,
	execution *planExecution,
	parallelism int,
) (*ResearchConfig, error) {
	registerResearchBlocks()
	blocks, err := nativePlanHCLBlocks(execution.nodes)
	if err != nil {
		return nil, err
	}
	researchConfig := &ResearchConfig{
		BaseConfig:  config.NewBaseConfig(golden.NewBaseConfigArgs{Basedir: directory, Ctx: execution.ctx}),
		execution:   execution,
		parallelism: parallelism,
	}
	return researchConfig, golden.InitConfig(researchConfig, blocks)
}

func (c *ResearchConfig) Parallelism() int {
	return c.parallelism
}

func (c *ResearchConfig) Outputs() map[string]cty.Value {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	return maps.Clone(c.outputs)
}

func (c *ResearchConfig) Warnings() []error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	return slices.Clone(c.warnings)
}

func (c *ResearchConfig) Plan() *ResearchPlan {
	return c.plan
}

func (c *ResearchConfig) ApplyBlock(address string) error {
	if c.execution == nil {
		return fmt.Errorf("apply execution is not configured")
	}
	return c.execution.executeAddress(address)
}

var _ golden.Plan = (*ResearchPlan)(nil)

type ResearchPlan struct {
	config *ResearchConfig
	modulespec.Plan
}

func RunResearchPlan(config *ResearchConfig) (*ResearchPlan, error) {
	if config == nil {
		return nil, fmt.Errorf("research config is required")
	}
	if config.cleanupSource != nil {
		defer config.cleanupSource()
		config.cleanupSource = nil
	}
	started := time.Now()
	blocks := golden.Blocks[golden.Block](config)
	if err := debuglog.Lifecycle(config.Context(), "golden.run_plan", debuglog.StatusStarted, debuglog.Event{
		Path: config.directory, Count: len(blocks),
	}); err != nil {
		return nil, err
	}
	planErr := config.RunPlan()
	planErr = errors.Join(planErr, debuglog.CompleteLifecycle(
		config.Context(),
		"golden.run_plan",
		started,
		planErr,
		debuglog.Event{Path: config.directory, Count: len(blocks)},
	))
	if planErr != nil {
		return nil, planErr
	}
	planned := &ResearchPlan{config: config}
	if config.source {
		result, err := config.snapshotPlan()
		if err != nil {
			return nil, err
		}
		planned.Plan = result
	}
	config.plan = planned
	return planned, nil
}

func (p *ResearchPlan) String() string {
	return fmt.Sprintf("ResearchPlan(%d blocks)", len(golden.Blocks[golden.Block](p.config)))
}

func (p *ResearchPlan) SavedPlan() *plan.Plan {
	return p.Saved
}

func (p *ResearchPlan) Apply() error {
	p.config.applyMu.Lock()
	defer p.config.applyMu.Unlock()
	if p.config.applyPlan != nil {
		outputs, warnings, err := p.config.applyPlan(p.SavedPlan())
		p.config.outputs = maps.Clone(outputs)
		p.config.warnings = slices.Clone(warnings)
		return err
	}
	if p.config.source {
		return fmt.Errorf("source research plan must be applied from its saved plan")
	}
	if p.config.execution != nil {
		return p.applySavedBlocks()
	}
	return golden.Traverse[golden.ApplyBlock](p.config.BaseConfig, func(block golden.ApplyBlock) error {
		return block.Apply()
	})
}

func (p *ResearchPlan) applySavedBlocks() error {
	nodes := p.config.execution.nodes
	blocks := make(map[string]golden.ApplyBlock, len(nodes))
	for _, block := range golden.Blocks[golden.ApplyBlock](p.config) {
		blocks[block.Address()] = block
	}

	remaining := make(map[string]int, len(nodes))
	dependents := make(map[string][]string, len(nodes))
	ready := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := blocks[node.Address]; !ok {
			return fmt.Errorf("saved plan block %q does not implement golden.ApplyBlock", node.Address)
		}
		remaining[node.Address] = len(node.Dependencies)
		if len(node.Dependencies) == 0 {
			ready = append(ready, node.Address)
		}
		for _, dependency := range node.Dependencies {
			dependents[dependency] = append(dependents[dependency], node.Address)
		}
	}

	type result struct {
		address string
		err     error
	}
	results := make(chan result, len(nodes))
	running := 0
	completed := 0
	failed := false
	for completed < len(nodes) {
		for !failed && running < p.config.parallelism && len(ready) > 0 {
			address := ready[0]
			ready = ready[1:]
			block := blocks[address]
			running++
			go func() {
				results <- result{address: address, err: block.Apply()}
			}()
		}
		if running == 0 {
			break
		}
		result := <-results
		running--
		completed++
		if result.err != nil {
			failed = true
			continue
		}
		for _, dependent := range dependents[result.address] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if failure := p.config.execution.failureError(); failure != nil {
		return failure
	}
	if completed != len(nodes) {
		return fmt.Errorf("apply stopped with %d of %d saved plan blocks completed", completed, len(nodes))
	}
	return nil
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

func (e *planExecution) executeAddress(address string) error {
	for _, node := range e.nodes {
		if node.Address == address {
			return e.execute(node)
		}
	}
	return fmt.Errorf("saved plan node %q is not present", address)
}

func (e *planExecution) execute(node plan.NodeSpec) error {
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

var registerNativeResearchBlocks sync.Once

func registerResearchBlocks() {
	registerNativeResearchBlocks.Do(func() {
		golden.RegisterBlock(new(provider.ModelProviderBlock))
		golden.RegisterBlock(new(toolspec.GoToolBlock))
		golden.RegisterBlock(new(toolspec.ExternalToolBlock))
		golden.RegisterBlock(new(researchspec.ResearchBlock))
		golden.RegisterBlock(new(modulespec.ModuleBlock))
		golden.RegisterBlock(new(modulespec.OutputBlock))
	})
}

func runSavedPlan(directory string, execution *planExecution, parallelism int) error {
	initStarted := time.Now()
	if err := debuglog.Lifecycle(execution.ctx, "apply.golden.config.init", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}); err != nil {
		return err
	}
	researchConfig, initErr := newApplyResearchConfig(directory, execution, parallelism)
	initErr = errors.Join(initErr, debuglog.CompleteLifecycle(execution.ctx, "apply.golden.config.init", initStarted, initErr, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}))
	if initErr != nil {
		return fmt.Errorf("initialize saved plan: %w", initErr)
	}
	planStarted := time.Now()
	if err := debuglog.Lifecycle(execution.ctx, "apply.golden.plan", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}); err != nil {
		return err
	}
	planErr := researchConfig.RunPlan()
	planErr = errors.Join(planErr, debuglog.CompleteLifecycle(execution.ctx, "apply.golden.plan", planStarted, planErr, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}))
	if planErr != nil {
		return fmt.Errorf("plan saved nodes: %w", planErr)
	}
	researchPlan := &ResearchPlan{config: researchConfig}
	applyStarted := time.Now()
	if err := debuglog.Lifecycle(execution.ctx, "apply.golden.apply", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}); err != nil {
		return err
	}
	applyErr := researchPlan.Apply()
	applyErr = errors.Join(applyErr, debuglog.CompleteLifecycle(execution.ctx, "apply.golden.apply", applyStarted, applyErr, debuglog.Event{
		Path: directory, Count: len(execution.nodes),
	}))
	if applyErr != nil {
		return fmt.Errorf("apply saved plan: %w", applyErr)
	}
	return nil
}

func nativePlanHCLBlocks(nodes []plan.NodeSpec) ([]*golden.HclBlock, error) {
	file := hclwrite.NewEmptyFile()
	for _, node := range nodes {
		kind, name, ok := strings.Cut(node.Address, ".")
		if !ok || kind != node.Kind || name == "" {
			return nil, fmt.Errorf("saved plan node %q does not match kind %q", node.Address, node.Kind)
		}
		block := hclwrite.NewBlock(kind, []string{name})
		switch kind {
		case "research":
			block.Body().SetAttributeValue("model", cty.StringVal("saved-plan"))
			block.Body().SetAttributeValue("system_prompt", cty.StringVal("saved-plan"))
		case "module":
			block.Body().SetAttributeValue("source", cty.StringVal("."))
		default:
			return nil, fmt.Errorf("saved plan node %q has unsupported kind %q", node.Address, kind)
		}
		if len(node.Dependencies) > 0 {
			dependencies := make([]hclwrite.Tokens, 0, len(node.Dependencies))
			for _, dependency := range node.Dependencies {
				dependencyKind, dependencyName, found := strings.Cut(dependency, ".")
				if !found || dependencyName == "" {
					return nil, fmt.Errorf("saved plan node %s depends on unknown node %s", node.Address, dependency)
				}
				dependencies = append(dependencies, hclwrite.TokensForTraversal(hcl.Traversal{
					hcl.TraverseRoot{Name: dependencyKind},
					hcl.TraverseAttr{Name: dependencyName},
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
