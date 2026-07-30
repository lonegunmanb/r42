package plan

import (
	"encoding/json"
	"fmt"
	"time"

	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
)

type wirePlan struct {
	Directory        string                `json:"directory"`
	Nodes            []wireNode            `json:"nodes"`
	Outputs          map[string]wireOutput `json:"outputs"`
	Context          map[string]wireValue  `json:"context,omitempty"`
	LocalExpressions map[string]string     `json:"local_expressions,omitempty"`
}

type wireNode struct {
	Address      string      `json:"address"`
	Kind         string      `json:"kind"`
	Dependencies []string    `json:"dependencies"`
	Config       wireValue   `json:"config"`
	Module       *wireModule `json:"module,omitempty"`
}

type wireModule struct {
	Plan        wirePlan `json:"plan"`
	Parallelism int      `json:"parallelism"`
	Timeout     int64    `json:"timeout_nanoseconds"`
}

type wireOutput struct {
	Value       wireValue `json:"value"`
	Description string    `json:"description"`
	Expression  string    `json:"expression,omitempty"`
}

type wireValue struct {
	Type           json.RawMessage `json:"type"`
	Value          []byte          `json:"value"`
	SensitivePaths []wirePath      `json:"sensitive_paths,omitempty"`
}

type wirePath []wirePathStep

type wirePathStep struct {
	Attribute *string    `json:"attribute,omitempty"`
	Index     *wireIndex `json:"index,omitempty"`
}

type wireIndex struct {
	Type  json.RawMessage `json:"type"`
	Value json.RawMessage `json:"value"`
}

func Marshal(planned *Plan) ([]byte, error) {
	if planned == nil {
		return nil, fmt.Errorf("plan is nil")
	}
	wire, err := encodePlan(planned)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		// note: untested because wirePlan contains only JSON-compatible fields.
		return nil, fmt.Errorf("encode plan: %w", err)
	}
	return encoded, nil
}

func Unmarshal(encoded []byte) (*Plan, error) {
	var wire wirePlan
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, fmt.Errorf("decode plan: %w", err)
	}
	planned, err := decodePlan(wire)
	if err != nil {
		return nil, fmt.Errorf("decode plan: %w", err)
	}
	return planned, nil
}

func encodePlan(planned *Plan) (wirePlan, error) {
	result := wirePlan{
		Directory:        planned.directory,
		Nodes:            make([]wireNode, len(planned.nodes)),
		Outputs:          make(map[string]wireOutput, len(planned.outputs)),
		Context:          make(map[string]wireValue, len(planned.context)),
		LocalExpressions: planned.LocalExpressions(),
	}
	for index, node := range planned.nodes {
		config, err := encodeValue(node.Config)
		if err != nil {
			return wirePlan{}, fmt.Errorf("node %q config: %w", node.Address, err)
		}
		result.Nodes[index] = wireNode{
			Address:      node.Address,
			Kind:         node.Kind,
			Dependencies: node.Dependencies,
			Config:       config,
		}
		if node.Module != nil {
			child, err := encodePlan(node.Module.Plan)
			if err != nil {
				return wirePlan{}, fmt.Errorf("module %q: %w", node.Address, err)
			}
			result.Nodes[index].Module = &wireModule{
				Plan:        child,
				Parallelism: node.Module.Parallelism,
				Timeout:     int64(node.Module.Timeout),
			}
		}
	}
	for name, output := range planned.outputs {
		value, err := encodeValue(output.Value)
		if err != nil {
			return wirePlan{}, fmt.Errorf("output %q: %w", name, err)
		}
		result.Outputs[name] = wireOutput{
			Value: value, Description: output.Description, Expression: output.Expression,
		}
	}
	for name, value := range planned.context {
		encoded, err := encodeValue(value)
		if err != nil {
			return wirePlan{}, fmt.Errorf("context %q: %w", name, err)
		}
		result.Context[name] = encoded
	}
	return result, nil
}

func decodePlan(wire wirePlan) (*Plan, error) {
	nodes := make([]NodeSpec, len(wire.Nodes))
	for index, node := range wire.Nodes {
		config, err := decodeValue(node.Config)
		if err != nil {
			return nil, fmt.Errorf("node %q config: %w", node.Address, err)
		}
		nodes[index] = NodeSpec{
			Address:      node.Address,
			Kind:         node.Kind,
			Dependencies: node.Dependencies,
			Config:       config,
		}
		if node.Module != nil {
			child, err := decodePlan(node.Module.Plan)
			if err != nil {
				return nil, fmt.Errorf("module %q: %w", node.Address, err)
			}
			nodes[index].Module = &ModuleSpec{
				Plan:        child,
				Parallelism: node.Module.Parallelism,
				Timeout:     time.Duration(node.Module.Timeout),
			}
		}
	}
	outputs := make(map[string]OutputSpec, len(wire.Outputs))
	for name, output := range wire.Outputs {
		value, err := decodeValue(output.Value)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		outputs[name] = OutputSpec{
			Value: value, Description: output.Description, Expression: output.Expression,
		}
	}
	contextValues := make(map[string]cty.Value, len(wire.Context))
	for name, encoded := range wire.Context {
		value, err := decodeValue(encoded)
		if err != nil {
			return nil, fmt.Errorf("context %q: %w", name, err)
		}
		contextValues[name] = value
	}
	return NewWithContextAndLocals(wire.Directory, nodes, outputs, contextValues, wire.LocalExpressions)
}

func encodeValue(value cty.Value) (wireValue, error) {
	unmarked, pathMarks := value.UnmarkDeepWithPaths()
	if err := corespec.ValidateType(unmarked.Type()); err != nil {
		return wireValue{}, err
	}
	typeJSON, err := ctyjson.MarshalType(unmarked.Type())
	if err != nil {
		return wireValue{}, fmt.Errorf("encode type: %w", err)
	}
	valueMessagePack, err := ctymsgpack.Marshal(unmarked, unmarked.Type())
	if err != nil {
		return wireValue{}, fmt.Errorf("encode value: %w", err)
	}
	result := wireValue{Type: typeJSON, Value: valueMessagePack}
	for _, pathMarks := range pathMarks {
		if len(pathMarks.Marks) == 0 {
			continue
		}
		path, err := encodePath(pathMarks.Path)
		if err != nil {
			return wireValue{}, err
		}
		result.SensitivePaths = append(result.SensitivePaths, path)
	}
	return result, nil
}

func decodeValue(wire wireValue) (cty.Value, error) {
	typeValue, err := ctyjson.UnmarshalType(wire.Type)
	if err != nil {
		return cty.NilVal, fmt.Errorf("decode type: %w", err)
	}
	if err := corespec.ValidateType(typeValue); err != nil {
		return cty.NilVal, err
	}
	value, err := ctymsgpack.Unmarshal(wire.Value, typeValue)
	if err != nil {
		return cty.NilVal, fmt.Errorf("decode value: %w", err)
	}
	marks := make([]cty.PathValueMarks, len(wire.SensitivePaths))
	for index, wirePath := range wire.SensitivePaths {
		path, err := decodePath(wirePath)
		if err != nil {
			return cty.NilVal, err
		}
		if _, err = path.Apply(value); err != nil {
			return cty.NilVal, fmt.Errorf("sensitive path does not apply: %w", err)
		}
		marks[index] = cty.PathValueMarks{Path: path, Marks: sensitiveMarks()}
	}
	return value.MarkWithPaths(marks), nil
}

func encodePath(path cty.Path) (wirePath, error) {
	result := make(wirePath, len(path))
	for index, step := range path {
		switch step := step.(type) {
		case cty.GetAttrStep:
			name := step.Name
			result[index].Attribute = &name
		case cty.IndexStep:
			key := step.Key
			typeJSON, err := ctyjson.MarshalType(key.Type())
			if err != nil {
				return nil, fmt.Errorf("encode sensitive path index type: %w", err)
			}
			valueJSON, err := ctyjson.Marshal(key, key.Type())
			if err != nil {
				return nil, fmt.Errorf("encode sensitive path index: %w", err)
			}
			result[index].Index = &wireIndex{Type: typeJSON, Value: valueJSON}
		default:
			// note: untested because cty.PathStep is a closed interface implemented by attribute and index steps.
			return nil, fmt.Errorf("unsupported sensitive path step %T", step)
		}
	}
	return result, nil
}

func decodePath(wire wirePath) (cty.Path, error) {
	result := make(cty.Path, len(wire))
	for index, step := range wire {
		switch {
		case step.Attribute != nil && step.Index == nil:
			result[index] = cty.GetAttrStep{Name: *step.Attribute}
		case step.Attribute == nil && step.Index != nil:
			typeValue, err := ctyjson.UnmarshalType(step.Index.Type)
			if err != nil {
				return nil, fmt.Errorf("decode sensitive path index type: %w", err)
			}
			key, err := ctyjson.Unmarshal(step.Index.Value, typeValue)
			if err != nil {
				return nil, fmt.Errorf("decode sensitive path index: %w", err)
			}
			result[index] = cty.IndexStep{Key: key}
		default:
			return nil, fmt.Errorf("decode sensitive path: each step must contain one selector")
		}
	}
	return result, nil
}

func sensitiveMarks() cty.ValueMarks {
	_, marks := corespec.MarkSensitive(cty.StringVal("")).Unmark()
	return marks
}
