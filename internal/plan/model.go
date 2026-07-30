package plan

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/zclconf/go-cty/cty"
)

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

type Plan struct {
	directory        string
	nodes            []NodeSpec
	outputs          map[string]OutputSpec
	context          map[string]cty.Value
	localExpressions map[string]string
}

func New(directory string, nodes []NodeSpec, outputs map[string]OutputSpec) (*Plan, error) {
	return NewWithContext(directory, nodes, outputs, nil)
}

func NewWithContext(
	directory string,
	nodes []NodeSpec,
	outputs map[string]OutputSpec,
	contextValues map[string]cty.Value,
) (*Plan, error) {
	return NewWithContextAndLocals(directory, nodes, outputs, contextValues, nil)
}

func NewWithContextAndLocals(
	directory string,
	nodes []NodeSpec,
	outputs map[string]OutputSpec,
	contextValues map[string]cty.Value,
	localExpressions map[string]string,
) (*Plan, error) {
	if err := validateNodes(nodes); err != nil {
		return nil, err
	}
	return &Plan{
		directory:        directory,
		nodes:            cloneNodes(nodes),
		outputs:          cloneOutputs(outputs),
		context:          cloneContext(contextValues),
		localExpressions: maps.Clone(localExpressions),
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

func (p *Plan) Nodes() []NodeSpec {
	return cloneNodes(p.nodes)
}

func (p *Plan) Outputs() map[string]OutputSpec {
	return cloneOutputs(p.outputs)
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
		nodes:            cloneNodes(source.nodes),
		outputs:          cloneOutputs(source.outputs),
		context:          cloneContext(source.context),
		localExpressions: maps.Clone(source.localExpressions),
	}
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
