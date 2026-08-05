package executor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	modulecache "github.com/lonegunmanb/r42/internal/module"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	internalplan "github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/run"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

type ResearchConfigOptions struct {
	Context              context.Context
	Variables            []golden.CliFlagAssignedVariables
	RunDirectory         string
	ReservedRunDirectory string
	ModuleDirectory      string
	Parallelism          int
	Debug                bool
	Apply                func(*internalplan.Plan) (map[string]cty.Value, []error, error)
}

type variableDeclaration struct {
	defaultValue *cty.Value
	sensitive    bool
}

type assignedValues map[string]cty.Value

func (a assignedValues) Variables(*golden.BaseConfig) (map[string]golden.VariableValueRead, error) {
	result := make(map[string]golden.VariableValueRead, len(a))
	for name, value := range a {
		cloned := value
		result[name] = golden.NewVariableValueRead(name, &cloned, nil)
	}
	return result, nil
}

func NewResearchConfig(directory string, options ResearchConfigOptions) (*ResearchConfig, error) {
	registerResearchBlocks()
	absolute, err := normalizeDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("reading module directory: %w", err)
	}
	childVariableDirectory, err := os.MkdirTemp("", "r42-module-vars-")
	if err != nil {
		return nil, fmt.Errorf("creating isolated child variable directory: %w", err)
	}
	runRoot := options.RunDirectory
	if strings.TrimSpace(runRoot) == "" {
		runRoot = absolute
	}
	var reserved *run.Run
	if options.ReservedRunDirectory != "" {
		reserved, err = run.Open(options.ReservedRunDirectory)
	} else {
		reserved, err = run.NewManager(runRoot).Reserve()
	}
	if err != nil {
		_ = os.RemoveAll(childVariableDirectory)
		return nil, err
	}
	result, err := newSourceResearchConfig(
		absolute, nil, nil, true, childVariableDirectory, reserved, "", options,
	)
	if err != nil {
		_ = os.RemoveAll(childVariableDirectory)
		return nil, err
	}
	result.cleanupSource = func() { _ = os.RemoveAll(childVariableDirectory) }
	return result, nil
}

func NewResearchConfigFromPlan(saved *internalplan.Plan, options ResearchConfigOptions) (*ResearchConfig, error) {
	if saved == nil {
		return nil, fmt.Errorf("saved plan is required")
	}
	if options.Apply == nil {
		return nil, fmt.Errorf("saved plan apply function is required")
	}
	activeRun, err := runFromSavedPlan(saved, options.RunDirectory)
	if err != nil {
		return nil, err
	}
	baseConfig := config.NewBaseConfig(golden.NewBaseConfigArgs{
		Basedir: saved.Directory(),
		Ctx:     options.Context,
	})
	result := &ResearchConfig{
		BaseConfig:  baseConfig,
		directory:   saved.Directory(),
		run:         activeRun,
		parallelism: r42concurrency.DefaultGlobalParallelism,
		applyPlan:   options.Apply,
	}
	if options.Parallelism > 0 {
		result.parallelism = options.Parallelism
	}
	result.plan = &ResearchPlan{
		config: result,
		Plan: modulespec.Plan{
			Directory: saved.Directory(),
			Saved:     saved,
		},
	}
	return result, nil
}

func newSourceResearchConfig(
	directory string,
	inputs map[string]cty.Value,
	stack []string,
	root bool,
	childVariableDirectory string,
	activeRun *run.Run,
	addressPrefix string,
	options ResearchConfigOptions,
) (*ResearchConfig, error) {
	if slices.Contains(stack, directory) {
		return nil, fmt.Errorf("module directory cycle: %v -> %s", stack, directory)
	}
	loaded, diagnostics, err := config.LoadDirectoryContext(options.Context, directory)
	if err != nil {
		return nil, fmt.Errorf("reading module directory: %w", err)
	}
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	variables, err := inspectVariables(loaded.Blocks)
	if err != nil {
		return nil, err
	}
	assignedInputs := inputs
	if root {
		if err = rejectUndeclaredInputs(variables, inputs); err != nil {
			return nil, err
		}
	} else {
		assignedInputs, err = childVariableValues(variables, inputs)
		if err != nil {
			return nil, err
		}
	}
	assignedInputs, inheritedSensitive := unmarkAssignedValues(assignedInputs)
	assigned := []golden.CliFlagAssignedVariables{assignedValues(assignedInputs)}
	if root {
		assigned = append(assigned, options.Variables...)
	}
	baseArguments := golden.NewBaseConfigArgs{
		Basedir: directory, CliFlagAssignedVariables: assigned, Ctx: options.Context,
	}
	if !root {
		baseArguments.VarConfigDir = &childVariableDirectory
	}
	baseConfig := config.NewBaseConfig(baseArguments)
	researchConfig := &ResearchConfig{
		BaseConfig:             baseConfig,
		directory:              directory,
		run:                    activeRun,
		addressPrefix:          addressPrefix,
		moduleDirectory:        options.ModuleDirectory,
		childVariableDirectory: childVariableDirectory,
		stack:                  append(slices.Clone(stack), directory),
		sensitiveVariables:     sensitiveVariableNames(variables, inheritedSensitive),
		source:                 true,
		parallelism:            r42concurrency.DefaultGlobalParallelism,
		applyPlan:              options.Apply,
	}
	if options.Parallelism > 0 {
		researchConfig.parallelism = options.Parallelism
	}
	initStarted := time.Now()
	if err = debuglog.Lifecycle(options.Context, "golden.config.init", debuglog.StatusStarted, debuglog.Event{
		Path: directory, Count: len(loaded.Blocks),
	}); err != nil {
		return nil, err
	}
	initErr := golden.InitConfig(researchConfig, loaded.Blocks)
	initErr = errors.Join(initErr, debuglog.CompleteLifecycle(options.Context, "golden.config.init", initStarted, initErr, debuglog.Event{
		Path: directory, Count: len(loaded.Blocks),
	}))
	if initErr != nil {
		return nil, initErr
	}
	return researchConfig, nil
}

func (c *ResearchConfig) EvalContext() *hcl.EvalContext {
	context := c.BaseConfig.EvalContext()
	if context.Variables == nil {
		context.Variables = make(map[string]cty.Value)
	}
	context.Variables["path"] = cty.ObjectVal(map[string]cty.Value{
		"module": cty.StringVal(filepath.ToSlash(c.directory)),
	})
	variables, ok := context.Variables["var"]
	if ok && !variables.IsNull() && variables.Type().IsObjectType() {
		values := variables.AsValueMap()
		for name := range c.sensitiveVariables {
			value, exists := values[name]
			if exists {
				values[name] = corespec.MarkSensitive(value)
			}
		}
		context.Variables["var"] = cty.ObjectVal(values)
	}
	return context
}

func (c *ResearchConfig) PlanChildModule(
	address string,
	source string,
	inputs map[string]cty.Value,
	parallelism *int,
	timeoutValue *string,
) (modulespec.ModulePlan, error) {
	if c.execution != nil {
		return c.savedChildModule(address)
	}
	activeRun, err := c.researchRun()
	if err != nil {
		return modulespec.ModulePlan{}, err
	}
	sourceDirectory := source
	if c.moduleDirectory != "" {
		canonicalAddress := c.CanonicalAddress(address)
		sourceDirectory, err = modulecache.Directory(c.moduleDirectory, canonicalAddress)
		if err != nil {
			return modulespec.ModulePlan{}, err
		}
		if _, statErr := os.Stat(sourceDirectory); statErr != nil {
			return modulespec.ModulePlan{}, fmt.Errorf(
				"module %s is not initialized; run r42 init: %w",
				canonicalAddress,
				statErr,
			)
		}
	} else if !filepath.IsAbs(sourceDirectory) {
		sourceDirectory = filepath.Join(c.directory, sourceDirectory)
	}
	directory, err := normalizeDirectory(sourceDirectory)
	if err != nil {
		return modulespec.ModulePlan{}, fmt.Errorf("reading module directory %q: %w", source, err)
	}
	timeout, err := parseModuleTimeout(timeoutValue)
	if err != nil {
		return modulespec.ModulePlan{}, err
	}
	child, err := newSourceResearchConfig(
		directory, inputs, c.stack, false, c.childVariableDirectory,
		activeRun, c.CanonicalAddress(address), ResearchConfigOptions{
			Context: c.Context(), ModuleDirectory: c.moduleDirectory,
		},
	)
	if err != nil {
		return modulespec.ModulePlan{}, err
	}
	planned, err := RunResearchPlan(child)
	if err != nil {
		return modulespec.ModulePlan{}, err
	}
	result := modulespec.ModulePlan{Plan: planned.Plan, Timeout: timeout}
	if parallelism != nil {
		result.Parallelism = *parallelism
	}
	return result, nil
}

func (c *ResearchConfig) savedChildModule(address string) (modulespec.ModulePlan, error) {
	for _, node := range c.execution.nodes {
		if node.Address != address {
			continue
		}
		if node.Module == nil || node.Module.Plan == nil {
			return modulespec.ModulePlan{}, fmt.Errorf("saved module %s has no child plan", address)
		}
		outputs := node.Module.Plan.Outputs()
		plannedOutputs := make(map[string]modulespec.Output, len(outputs))
		for name, output := range outputs {
			plannedOutputs[name] = modulespec.Output{
				Value: output.Value, Type: output.Value.Type(), Description: output.Description,
				Sensitive: corespec.IsSensitive(output.Value), Expression: output.Expression,
			}
		}
		return modulespec.ModulePlan{
			Plan: modulespec.Plan{
				Directory: node.Module.Plan.Directory(), Outputs: plannedOutputs, Saved: node.Module.Plan,
			},
			Parallelism: node.Module.Parallelism,
			Timeout:     node.Module.Timeout,
		}, nil
	}
	return modulespec.ModulePlan{}, fmt.Errorf("saved module %s was not planned", address)
}

func (c *ResearchConfig) BlockWorkingDirectory(address string) (string, error) {
	activeRun, err := c.researchRun()
	if err != nil {
		return "", err
	}
	return activeRun.WorkspacePath(c.CanonicalAddress(address))
}

func (c *ResearchConfig) CanonicalAddress(address string) string {
	if c.addressPrefix == "" {
		return address
	}
	return c.addressPrefix + "." + address
}

func (c *ResearchConfig) snapshotPlan() (modulespec.Plan, error) {
	planned := modulespec.Plan{
		Directory: c.directory,
		Outputs:   make(map[string]modulespec.Output),
		Modules:   make(map[string]modulespec.ModulePlan),
	}
	for _, output := range golden.Blocks[*modulespec.OutputBlock](c) {
		planned.Outputs[output.Name()] = output.Snapshot()
	}
	for _, module := range golden.Blocks[*modulespec.ModuleBlock](c) {
		planned.Modules[module.Name()] = module.PlannedModule()
	}
	started := time.Now()
	if err := debuglog.Lifecycle(c.Context(), "plan.snapshot", debuglog.StatusStarted, debuglog.Event{Path: c.directory}); err != nil {
		return modulespec.Plan{}, err
	}
	saved, err := c.savedPlan(planned)
	count := 0
	if saved != nil {
		count = len(saved.Nodes())
	}
	err = errors.Join(err, debuglog.CompleteLifecycle(c.Context(), "plan.snapshot", started, err, debuglog.Event{
		Path: c.directory, Count: count,
	}))
	if err != nil {
		return modulespec.Plan{}, err
	}
	planned.Saved = saved
	return planned, nil
}

func (c *ResearchConfig) savedPlan(planned modulespec.Plan) (*internalplan.Plan, error) {
	activeRun, err := c.researchRun()
	if err != nil {
		return nil, err
	}
	executable := make(map[string]struct{})
	for _, block := range golden.Blocks[*researchspec.ResearchBlock](c) {
		executable[block.Address()] = struct{}{}
	}
	for _, block := range golden.Blocks[*researchspec.DynamicResearchBlock](c) {
		executable[block.Address()] = struct{}{}
	}
	for _, block := range golden.Blocks[*modulespec.ModuleBlock](c) {
		executable[block.Address()] = struct{}{}
	}
	dependencies := make(map[string][]string, len(executable))
	for address := range executable {
		children, err := c.GetChildren(address)
		if err != nil {
			return nil, fmt.Errorf("read dependencies for %s: %w", address, err)
		}
		for child := range children {
			if _, ok := executable[child]; ok {
				dependencies[child] = append(dependencies[child], address)
			}
		}
	}
	toolRegistry := modulespec.BuildToolRegistry(c, planned.Modules)
	nodes := make([]internalplan.NodeSpec, 0, len(executable))
	for _, block := range golden.Blocks[*researchspec.ResearchBlock](c) {
		snapshot, err := modulespec.EncodeResearchPlan(block, c, toolRegistry)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", block.Address(), err)
		}
		nodes = append(nodes, internalplan.NodeSpec{
			Address: block.Address(), Kind: "research", Dependencies: dependencies[block.Address()], Config: snapshot,
		})
	}
	for _, block := range golden.Blocks[*researchspec.DynamicResearchBlock](c) {
		snapshot, err := modulespec.EncodeDynamicResearchPlan(block, c, toolRegistry)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", block.Address(), err)
		}
		nodes = append(nodes, internalplan.NodeSpec{
			Address: block.Address(), Kind: "research", Dependencies: dependencies[block.Address()], Config: snapshot,
		})
	}
	for _, block := range golden.Blocks[*modulespec.ModuleBlock](c) {
		module := planned.Modules[block.Name()]
		nodes = append(nodes, internalplan.NodeSpec{
			Address: block.Address(), Kind: "module", Dependencies: dependencies[block.Address()], Config: cty.EmptyObjectVal,
			Module: &internalplan.ModuleSpec{Plan: module.Saved, Parallelism: module.Parallelism, Timeout: module.Timeout},
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Address < nodes[j].Address })
	for index := range nodes {
		sort.Strings(nodes[index].Dependencies)
	}
	outputs := make(map[string]internalplan.OutputSpec, len(planned.Outputs))
	for name, output := range planned.Outputs {
		outputs[name] = internalplan.OutputSpec{
			Value: output.Value, Description: output.Description, Expression: output.Expression,
		}
	}
	contextValues := make(map[string]cty.Value)
	maps.Copy(contextValues, c.EvalContext().Variables)
	localExpressions := make(map[string]string)
	for _, block := range golden.Blocks[*golden.LocalBlock](c) {
		if attribute, ok := block.HclBlock().Attributes()["value"]; ok {
			localExpressions[block.Name()] = savedExpressionSource(attribute)
		}
	}
	result, err := internalplan.NewForRunWithTools(
		planned.Directory, activeRun.Directory(), nodes, outputs, contextValues, localExpressions, toolRegistry,
	)
	if err != nil {
		return nil, fmt.Errorf("build saved plan: %w", err)
	}
	return result, nil
}

func savedExpressionSource(attribute *golden.HclAttribute) string {
	source := attribute.ExprString()
	for _, token := range attribute.ExprTokens() {
		if token.Type == hclsyntax.TokenOHeredoc {
			return source + "\n"
		}
	}
	return source
}

func (c *ResearchConfig) researchRun() (*run.Run, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.run != nil {
		return c.run, nil
	}
	reserved, err := run.NewManager(c.directory).Reserve()
	if err != nil {
		return nil, fmt.Errorf("reserve research run: %w", err)
	}
	c.run = reserved
	return c.run, nil
}

func runFromSavedPlan(saved *internalplan.Plan, requestedRoot string) (*run.Run, error) {
	if saved.RunDirectory() != "" {
		return run.Open(saved.RunDirectory())
	}
	root := requestedRoot
	if strings.TrimSpace(root) == "" {
		root = saved.Directory()
	}
	return run.NewManager(root).Reserve()
}

func inspectVariables(blocks []*golden.HclBlock) (map[string]variableDeclaration, error) {
	variables := make(map[string]variableDeclaration)
	for _, block := range blocks {
		if block.Type != "variable" || len(block.Labels) < 2 {
			continue
		}
		name := block.Labels[1]
		_, ok := block.Attributes()["type"]
		if !ok {
			return nil, fmt.Errorf("variable %q must declare type", name)
		}
		declaration := variableDeclaration{}
		if defaultAttribute, hasDefault := block.Attributes()["default"]; hasDefault {
			value, diagnostics := defaultAttribute.Expr.Value(nil)
			if diagnostics.HasErrors() {
				return nil, fmt.Errorf("variable %q default: %w", name, diagnostics)
			}
			declaration.defaultValue = &value
		}
		if sensitiveAttribute, ok := block.Attributes()["sensitive"]; ok {
			value, diagnostics := sensitiveAttribute.Expr.Value(nil)
			if diagnostics.HasErrors() {
				return nil, fmt.Errorf("variable %q sensitive: %w", name, diagnostics)
			}
			if value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.Bool) {
				return nil, fmt.Errorf("variable %q sensitive must be a known bool", name)
			}
			declaration.sensitive = value.True()
		}
		variables[name] = declaration
	}
	return variables, nil
}

func childVariableValues(
	variables map[string]variableDeclaration,
	inputs map[string]cty.Value,
) (map[string]cty.Value, error) {
	if err := rejectUndeclaredInputs(variables, inputs); err != nil {
		return nil, err
	}
	result := make(map[string]cty.Value, len(variables))
	for name, variable := range variables {
		if value, ok := inputs[name]; ok {
			result[name] = value
			continue
		}
		if variable.defaultValue == nil {
			return nil, fmt.Errorf("module input %q is required", name)
		}
		result[name] = *variable.defaultValue
	}
	return result, nil
}

func rejectUndeclaredInputs(variables map[string]variableDeclaration, inputs map[string]cty.Value) error {
	for name := range inputs {
		if _, ok := variables[name]; !ok {
			return fmt.Errorf("module input %q is not declared", name)
		}
	}
	return nil
}

func unmarkAssignedValues(values map[string]cty.Value) (map[string]cty.Value, map[string]struct{}) {
	unmarked := make(map[string]cty.Value, len(values))
	sensitive := make(map[string]struct{})
	for name, value := range values {
		if corespec.IsSensitive(value) {
			sensitive[name] = struct{}{}
		}
		unmarked[name], _ = value.UnmarkDeep()
	}
	return unmarked, sensitive
}

func sensitiveVariableNames(
	variables map[string]variableDeclaration,
	inherited map[string]struct{},
) map[string]struct{} {
	result := make(map[string]struct{}, len(inherited))
	maps.Copy(result, inherited)
	for name, variable := range variables {
		if variable.sensitive {
			result[name] = struct{}{}
		}
	}
	return result
}

func normalizeDirectory(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", absolute)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func parseModuleTimeout(raw *string) (time.Duration, error) {
	if raw == nil {
		return 0, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(*raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("module timeout must be a positive duration")
	}
	return value, nil
}
