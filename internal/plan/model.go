package plan

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/zclconf/go-cty/cty"
)

var toolIDPattern = regexp.MustCompile(`^tool_.+_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type NodeSpec struct {
	Address      string
	Kind         string
	Dependencies []string
	Config       cty.Value
	Module       *ModuleSpec
}

type ModuleSpec struct {
	Plan        *Plan
	Parallelism int
	Timeout     time.Duration
}

type OutputSpec struct {
	Value       cty.Value
	Description string
	Expression  string
}

type ToolSpec struct {
	ID                   string   `json:"id"`
	Address              string   `json:"address"`
	Kind                 string   `json:"kind"`
	Description          string   `json:"description"`
	Source               string   `json:"source,omitempty"`
	Program              []string `json:"program,omitempty"`
	WorkingDir           string   `json:"working_dir,omitempty"`
	InputTypeExpression  string   `json:"input_type_expression,omitempty"`
	OutputTypeExpression string   `json:"output_type_expression,omitempty"`
}

type Plan struct {
	directory        string
	runDirectory     string
	nodes            []NodeSpec
	outputs          map[string]OutputSpec
	context          map[string]cty.Value
	localExpressions map[string]string
	tools            map[string]ToolSpec
}

func NewWithContextAndLocals(
	directory string,
	nodes []NodeSpec,
	outputs map[string]OutputSpec,
	contextValues map[string]cty.Value,
	localExpressions map[string]string,
) (*Plan, error) {
	return newPlan(directory, "", nodes, outputs, contextValues, localExpressions, nil)
}

func NewForRun(
	directory string,
	runDirectory string,
	nodes []NodeSpec,
	outputs map[string]OutputSpec,
	contextValues map[string]cty.Value,
	localExpressions map[string]string,
) (*Plan, error) {
	return NewForRunWithTools(
		directory, runDirectory, nodes, outputs, contextValues, localExpressions, nil,
	)
}

func NewForRunWithTools(
	directory string,
	runDirectory string,
	nodes []NodeSpec,
	outputs map[string]OutputSpec,
	contextValues map[string]cty.Value,
	localExpressions map[string]string,
	tools map[string]ToolSpec,
) (*Plan, error) {
	if runDirectory != "" && !filepath.IsAbs(runDirectory) {
		return nil, fmt.Errorf("run directory must be absolute")
	}
	return newPlan(directory, runDirectory, nodes, outputs, contextValues, localExpressions, tools)
}

func newPlan(
	directory string,
	runDirectory string,
	nodes []NodeSpec,
	outputs map[string]OutputSpec,
	contextValues map[string]cty.Value,
	localExpressions map[string]string,
	tools map[string]ToolSpec,
) (*Plan, error) {
	if err := validateNodes(nodes); err != nil {
		return nil, err
	}
	if err := validateTools(tools); err != nil {
		return nil, err
	}
	return &Plan{
		directory:        directory,
		runDirectory:     runDirectory,
		nodes:            cloneNodes(nodes),
		outputs:          cloneOutputs(outputs),
		context:          cloneContext(contextValues),
		localExpressions: maps.Clone(localExpressions),
		tools:            cloneTools(tools),
	}, nil
}

func (p *Plan) Context() map[string]cty.Value {
	return cloneContext(p.context)
}

func (p *Plan) LocalExpressions() map[string]string {
	return maps.Clone(p.localExpressions)
}

func (p *Plan) Directory() string {
	return p.directory
}

func (p *Plan) RunDirectory() string {
	return p.runDirectory
}

func (p *Plan) Nodes() []NodeSpec {
	return cloneNodes(p.nodes)
}

func (p *Plan) Outputs() map[string]OutputSpec {
	return cloneOutputs(p.outputs)
}

func (p *Plan) Tools() map[string]ToolSpec {
	return cloneTools(p.tools)
}

func IsToolID(value string) bool {
	return toolIDPattern.MatchString(value)
}

func validateTools(tools map[string]ToolSpec) error {
	for id, tool := range tools {
		if id == "" || tool.ID == "" {
			return fmt.Errorf("tool id is required")
		}
		if id != tool.ID {
			return fmt.Errorf("tool registry key must match tool id")
		}
		if !IsToolID(id) {
			return fmt.Errorf("tool id %q is invalid", id)
		}
		if tool.Address == "" {
			return fmt.Errorf("tool %s address is required", id)
		}
		if tool.Kind == "" {
			return fmt.Errorf("tool %s kind is required", id)
		}
	}
	return nil
}

func validateNodes(nodes []NodeSpec) error {
	addresses := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if _, exists := addresses[node.Address]; exists {
			return fmt.Errorf("node %q is declared more than once", node.Address)
		}
		addresses[node.Address] = struct{}{}
		if node.Kind == "module" && (node.Module == nil || node.Module.Plan == nil) {
			return fmt.Errorf("module node %q must contain a child plan", node.Address)
		}
	}
	for _, node := range nodes {
		for _, dependency := range node.Dependencies {
			if _, exists := addresses[dependency]; !exists {
				return fmt.Errorf("node %q depends on unknown node %q", node.Address, dependency)
			}
		}
	}
	return validateAcyclic(nodes)
}

func validateAcyclic(nodes []NodeSpec) error {
	dependencies := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		dependencies[node.Address] = node.Dependencies
	}
	state := make(map[string]uint8, len(nodes))
	stack := make([]string, 0, len(nodes))
	var visit func(string) error
	visit = func(address string) error {
		switch state[address] {
		case 1:
			return fmt.Errorf("plan dependency cycle: %v -> %s", stack, address)
		case 2:
			return nil
		}
		state[address] = 1
		stack = append(stack, address)
		for _, dependency := range dependencies[address] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[address] = 2
		return nil
	}
	for _, node := range nodes {
		if err := visit(node.Address); err != nil {
			return err
		}
	}
	return nil
}

func clonePlan(source *Plan) *Plan {
	if source == nil {
		return nil
	}
	return &Plan{
		directory:        source.directory,
		runDirectory:     source.runDirectory,
		nodes:            cloneNodes(source.nodes),
		outputs:          cloneOutputs(source.outputs),
		context:          cloneContext(source.context),
		localExpressions: maps.Clone(source.localExpressions),
		tools:            cloneTools(source.tools),
	}
}

func cloneTools(source map[string]ToolSpec) map[string]ToolSpec {
	result := make(map[string]ToolSpec, len(source))
	for id, tool := range source {
		tool.Program = slices.Clone(tool.Program)
		result[id] = tool
	}
	return result
}

func cloneContext(source map[string]cty.Value) map[string]cty.Value {
	result := make(map[string]cty.Value, len(source))
	maps.Copy(result, source)
	return result
}

func cloneNodes(source []NodeSpec) []NodeSpec {
	result := make([]NodeSpec, len(source))
	for index, node := range source {
		result[index] = node
		result[index].Dependencies = slices.Clone(node.Dependencies)
		if node.Module != nil {
			module := *node.Module
			module.Plan = clonePlan(node.Module.Plan)
			result[index].Module = &module
		}
	}
	return result
}

func cloneOutputs(source map[string]OutputSpec) map[string]OutputSpec {
	result := make(map[string]OutputSpec, len(source))
	maps.Copy(result, source)
	return result
}
