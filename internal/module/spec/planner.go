package spec

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/lonegunmanb/r42/internal/config"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

type Output struct {
	Value       cty.Value
	Type        cty.Type
	Description string
	Sensitive   bool
}

type Plan struct {
	Directory string
	Outputs   map[string]Output
	Modules   map[string]ModulePlan
}

type ModulePlan struct {
	Plan
	Parallelism int
	Timeout     time.Duration
}

type planningConfig struct {
	*golden.BaseConfig
	directory              string
	childVariableDirectory string
	stack                  []string
	sensitiveVariables     map[string]struct{}
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

func PlanDirectory(directory string, inputs map[string]cty.Value) (Plan, error) {
	absolute, err := normalizeDirectory(directory)
	if err != nil {
		return Plan{}, fmt.Errorf("reading module directory: %w", err)
	}
	childVariableDirectory, err := os.MkdirTemp("", "r42-module-vars-")
	if err != nil {
		return Plan{}, fmt.Errorf("creating isolated child variable directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(childVariableDirectory) }()
	return planDirectory(absolute, inputs, nil, true, childVariableDirectory)
}

func planDirectory(
	directory string,
	inputs map[string]cty.Value,
	stack []string,
	root bool,
	childVariableDirectory string,
) (Plan, error) {
	if slices.Contains(stack, directory) {
		return Plan{}, fmt.Errorf("module directory cycle: %v -> %s", stack, directory)
	}
	loaded, diagnostics, err := config.LoadDirectory(directory)
	if err != nil {
		return Plan{}, fmt.Errorf("reading module directory: %w", err)
	}
	if diagnostics.HasErrors() {
		return Plan{}, diagnostics
	}
	variables, err := inspectVariables(loaded.Blocks)
	if err != nil {
		return Plan{}, err
	}
	assignedInputs := inputs
	if root {
		if err = rejectUndeclaredInputs(variables, inputs); err != nil {
			return Plan{}, err
		}
	} else {
		assignedInputs, err = childVariableValues(variables, inputs)
		if err != nil {
			return Plan{}, err
		}
	}
	assignedInputs, inheritedSensitive := unmarkAssignedValues(assignedInputs)
	sensitiveVariables := sensitiveVariableNames(variables, inheritedSensitive)

	assigned := []golden.CliFlagAssignedVariables{assignedValues(assignedInputs)}
	baseArguments := golden.NewBaseConfigArgs{
		Basedir:                  directory,
		CliFlagAssignedVariables: assigned,
	}
	if !root {
		baseArguments.VarConfigDir = &childVariableDirectory
	}
	base := config.NewBaseConfig(baseArguments)
	planning := &planningConfig{
		BaseConfig:             base,
		directory:              directory,
		childVariableDirectory: childVariableDirectory,
		stack:                  append(slices.Clone(stack), directory),
		sensitiveVariables:     sensitiveVariables,
	}
	if err = golden.InitConfig(planning, loaded.Blocks); err != nil {
		return Plan{}, err
	}
	if err = planning.RunPlan(); err != nil {
		return Plan{}, err
	}

	plan := Plan{
		Directory: directory,
		Outputs:   make(map[string]Output),
		Modules:   make(map[string]ModulePlan),
	}
	for _, output := range golden.Blocks[*OutputBlock](planning) {
		plan.Outputs[output.Name()] = output.Snapshot()
	}
	for _, module := range golden.Blocks[*ModuleBlock](planning) {
		plan.Modules[module.Name()] = module.PlannedModule()
	}
	return plan, nil
}

func (c *planningConfig) EvalContext() *hcl.EvalContext {
	context := c.BaseConfig.EvalContext()
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

func (c *planningConfig) planChild(
	source string,
	inputs map[string]cty.Value,
	parallelism *int,
	timeoutValue *string,
) (ModulePlan, error) {
	sourceDirectory := source
	if !filepath.IsAbs(sourceDirectory) {
		sourceDirectory = filepath.Join(c.directory, sourceDirectory)
	}
	directory, err := normalizeDirectory(sourceDirectory)
	if err != nil {
		return ModulePlan{}, fmt.Errorf("reading module directory %q: %w", source, err)
	}
	timeout, err := parseModuleTimeout(timeoutValue)
	if err != nil {
		return ModulePlan{}, err
	}
	plan, err := planDirectory(directory, inputs, c.stack, false, c.childVariableDirectory)
	if err != nil {
		return ModulePlan{}, err
	}
	result := ModulePlan{Plan: plan, Timeout: timeout}
	if parallelism != nil {
		result.Parallelism = *parallelism
	}
	return result, nil
}

func inspectVariables(blocks []*golden.HclBlock) (map[string]variableDeclaration, error) {
	variables := make(map[string]variableDeclaration)
	for _, block := range blocks {
		if block.Type != "variable" || len(block.Labels) < 2 {
			continue
		}
		name := block.Labels[1]
		attribute, ok := block.Attributes()["type"]
		if !ok {
			return nil, fmt.Errorf("variable %q must declare type", name)
		}
		if _, diagnostics := typeexpr.TypeConstraint(attribute.Expr); diagnostics.HasErrors() {
			return nil, fmt.Errorf("variable %q type: %w", name, diagnostics)
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

func cloneModulePlan(source ModulePlan) ModulePlan {
	result := ModulePlan{
		Plan:        clonePlan(source.Plan),
		Parallelism: source.Parallelism,
		Timeout:     source.Timeout,
	}
	return result
}

func clonePlan(source Plan) Plan {
	result := Plan{
		Directory: source.Directory,
		Outputs:   make(map[string]Output, len(source.Outputs)),
		Modules:   make(map[string]ModulePlan, len(source.Modules)),
	}
	maps.Copy(result.Outputs, source.Outputs)
	for name, module := range source.Modules {
		result.Modules[name] = cloneModulePlan(module)
	}
	return result
}
