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

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	defaults "github.com/lonegunmanb/go-defaults"
	"github.com/lonegunmanb/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	mcpspec "github.com/lonegunmanb/r42/internal/mcp/spec"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/run"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
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
	moduleDirectory        string
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
		directory:   directory,
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
	return p.config.RunApply(func(block golden.Block) error {
		applyBlock, ok := block.(golden.ApplyBlock)
		if !ok {
			return nil
		}
		return applyBlock.Apply()
	})
}

func (p *ResearchPlan) applySavedBlocks() error {
	runErr := p.config.RunApply(func(block golden.Block) error {
		applyBlock, ok := block.(golden.ApplyBlock)
		if !ok {
			return fmt.Errorf("saved plan block %q does not implement golden.ApplyBlock", block.Address())
		}
		return applyBlock.Apply()
	})
	if failure := p.config.execution.failureError(); failure != nil {
		return failure
	}
	return runErr
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
	address := e.canonicalAddress(node.Address)
	if err := e.ctx.Err(); err != nil {
		return e.recordFailure(fmt.Errorf("apply block %s: %w", address, err))
	}
	work := func(ctx context.Context) error { return e.executeBlock(ctx, node) }
	if node.Kind != "research" || strings.HasPrefix(node.Address, "research.dynamic.") {
		return work(e.ctx)
	}
	called := false
	err := e.scope.WithResearch(e.ctx, func(ctx context.Context) error {
		called = true
		return work(ctx)
	})
	if err != nil && !called {
		return e.recordFailure(fmt.Errorf("apply block %s: %w", address, err))
	}
	return err
}

func (e *planExecution) executeBlock(ctx context.Context, node plan.NodeSpec) error {
	address := e.canonicalAddress(node.Address)
	event := debuglog.Event{
		BlockAddress: address, BlockType: node.Kind, Dependencies: e.canonicalAddresses(node.Dependencies),
	}
	factoryStarted := time.Now()
	if err := debuglog.Lifecycle(ctx, "block.factory", debuglog.StatusStarted, event); err != nil {
		return e.recordFailure(err)
	}
	block, err := e.factory.New(ctx, node, e.scope)
	if err != nil {
		err = fmt.Errorf("create apply block %s: %w", address, err)
	} else if block == nil {
		err = fmt.Errorf("create apply block %s: factory returned nil", address)
	}
	factoryLogErr := debuglog.CompleteLifecycle(ctx, "block.factory", factoryStarted, err, event)
	err = errors.Join(err, factoryLogErr)
	if err == nil {
		applyStarted := time.Now()
		if err = debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusStarted, event); err == nil {
			if applyErr := block.Apply(); applyErr != nil {
				err = fmt.Errorf("apply block %s: %w", address, applyErr)
			} else if contextErr := ctx.Err(); contextErr != nil {
				err = fmt.Errorf("apply block %s: %w", address, contextErr)
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
			e.addWarning(fmt.Errorf("cleanup block %s: %w", address, cleanupErr))
		}
	}
	if err == nil {
		if contextErr := ctx.Err(); contextErr != nil {
			err = fmt.Errorf("apply block %s: %w", address, contextErr)
			e.fail(err)
		}
	}
	return err
}

func (e *planExecution) canonicalAddress(address string) string {
	if canonicalizer, ok := e.factory.(AddressCanonicalizer); ok {
		return canonicalizer.CanonicalAddress(address)
	}
	return address
}

func (e *planExecution) canonicalAddresses(addresses []string) []string {
	result := make([]string, len(addresses))
	for index, address := range addresses {
		result[index] = e.canonicalAddress(address)
	}
	return result
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
		// go-defaults v1.4.0 lazily initializes its shared filler without synchronization.
		defaults.SetDefaults(&struct{}{})
		golden.RegisterBlock(new(provider.ModelProviderBlock))
		golden.RegisterBlock(new(mcpspec.ServerBlock))
		golden.RegisterBlock(new(toolspec.GoToolBlock))
		golden.RegisterBlock(new(toolspec.ExternalToolBlock))
		golden.RegisterBlock(new(toolspec.StarlarkToolBlock))
		golden.RegisterBlock(new(researchspec.ResearchBlock))
		golden.RegisterBlock(new(researchspec.DynamicResearchBlock))
		golden.RegisterBlock(new(s3spec.ProviderBlock))
		golden.RegisterBlock(new(s3spec.FolderBlock))
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
	type nativePlanBlock struct {
		address      nativePlanAddress
		forEach      map[string]cty.Value
		dependencies map[string]nativePlanAddress
	}

	blocksByAddress := make(map[string]*nativePlanBlock, len(nodes))
	orderedBlocks := make([]*nativePlanBlock, 0, len(nodes))
	for _, node := range nodes {
		address, ok := parseNativePlanAddress(node.Address)
		if !ok || address.kind != node.Kind {
			return nil, fmt.Errorf("saved plan node %q does not match kind %q", node.Address, node.Kind)
		}
		baseAddress := address.baseAddress()
		block, exists := blocksByAddress[baseAddress]
		if !exists {
			block = &nativePlanBlock{
				address:      address,
				forEach:      make(map[string]cty.Value),
				dependencies: make(map[string]nativePlanAddress),
			}
			blocksByAddress[baseAddress] = block
			orderedBlocks = append(orderedBlocks, block)
		}
		if address.forEachKey != nil {
			block.forEach[*address.forEachKey] = cty.True
		}
		for _, dependency := range node.Dependencies {
			dependencyAddress, found := parseNativePlanAddress(dependency)
			if !found {
				return nil, fmt.Errorf("saved plan node %s depends on unknown node %s", node.Address, dependency)
			}
			dependencyAddress.forEachKey = nil
			block.dependencies[dependencyAddress.baseAddress()] = dependencyAddress
		}
	}

	file := hclwrite.NewEmptyFile()
	for _, savedBlock := range orderedBlocks {
		address := savedBlock.address
		block := hclwrite.NewBlock(address.kind, address.labels)
		if len(savedBlock.forEach) > 0 {
			block.Body().SetAttributeValue("for_each", cty.ObjectVal(savedBlock.forEach))
		}
		switch address.kind {
		case "research":
			if len(address.labels) > 0 && address.labels[0] == "dynamic" {
				block.Body().SetAttributeValue("tasks", cty.EmptyTupleVal)
			} else {
				block.Body().SetAttributeValue("model", cty.StringVal("saved-plan"))
				block.Body().SetAttributeValue("system_prompt", cty.StringVal("saved-plan"))
			}
		case "module":
			block.Body().SetAttributeValue("source", cty.StringVal("."))
		case "s3_folder":
			block.Body().SetAttributeValue("provider", cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("s3_provider.saved"), "kind": cty.StringVal("s3_provider"),
			}))
			block.Body().SetAttributeValue("bucket", cty.StringVal("saved-plan"))
			block.Body().SetAttributeValue("source", cty.StringVal("."))
		default:
			return nil, fmt.Errorf(
				"saved plan node %q has unsupported kind %q",
				address.baseAddress(),
				address.kind,
			)
		}
		if len(savedBlock.dependencies) > 0 {
			dependencies := make([]hclwrite.Tokens, 0, len(savedBlock.dependencies))
			for _, dependency := range slices.Sorted(maps.Keys(savedBlock.dependencies)) {
				dependencyAddress := savedBlock.dependencies[dependency]
				traversal := hcl.Traversal{hcl.TraverseRoot{Name: dependencyAddress.kind}}
				for _, label := range dependencyAddress.labels {
					traversal = append(traversal, hcl.TraverseAttr{Name: label})
				}
				dependencies = append(dependencies, hclwrite.TokensForTraversal(traversal))
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

type nativePlanAddress struct {
	kind       string
	labels     []string
	forEachKey *string
}

func (a nativePlanAddress) baseAddress() string {
	return a.kind + "." + strings.Join(a.labels, ".")
}

func parseNativePlanAddress(address string) (nativePlanAddress, bool) {
	kind, remainder, found := strings.Cut(address, ".")
	if !found || kind == "" || remainder == "" {
		return nativePlanAddress{}, false
	}
	if kind != "research" {
		if strings.Contains(remainder, ".") {
			return nativePlanAddress{}, false
		}
		return nativePlanAddress{kind: kind, labels: []string{remainder}}, true
	}

	researchType, name, found := strings.Cut(remainder, ".")
	if !found || researchType == "" || name == "" {
		return nativePlanAddress{}, false
	}
	name, key, valid := splitNativePlanInstance(name)
	if !valid {
		return nativePlanAddress{}, false
	}
	return nativePlanAddress{
		kind: kind, labels: []string{researchType, name}, forEachKey: key,
	}, true
}

func splitNativePlanInstance(name string) (string, *string, bool) {
	open := strings.LastIndexByte(name, '[')
	if open < 0 {
		return name, nil, !strings.Contains(name, "]")
	}
	if open == 0 || !strings.HasSuffix(name, "]") || open == len(name)-2 {
		return "", nil, false
	}
	key := name[open+1 : len(name)-1]
	return name[:open], &key, true
}
