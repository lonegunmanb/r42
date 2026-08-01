package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func Display(planned *Plan) (string, error) {
	if planned == nil {
		return "", fmt.Errorf("plan is nil")
	}
	view, err := displayPlan(planned)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(view); err != nil {
		// note: untested because displayPlan returns only JSON-compatible values.
		return "", fmt.Errorf("encode plan display: %w", err)
	}
	return buffer.String(), nil
}

func DisplayValues(values map[string]cty.Value) (string, error) {
	view := make(map[string]any, len(values))
	for name, value := range values {
		displayed, err := displayValue(value)
		if err != nil {
			return "", fmt.Errorf("display value %q: %w", name, err)
		}
		view[name] = displayed
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(view); err != nil {
		// note: untested because displayValue returns only JSON-compatible values.
		return "", fmt.Errorf("encode values display: %w", err)
	}
	return buffer.String(), nil
}

func displayPlan(planned *Plan) (map[string]any, error) {
	nodes := make([]any, len(planned.nodes))
	for index, node := range planned.nodes {
		config, err := displayValue(node.Config)
		if err != nil {
			return nil, fmt.Errorf("display node %q config: %w", node.Address, err)
		}
		nodeView := map[string]any{
			"address":      node.Address,
			"kind":         node.Kind,
			"dependencies": node.Dependencies,
			"config":       config,
		}
		if node.Module != nil {
			child, err := displayPlan(node.Module.Plan)
			if err != nil {
				return nil, fmt.Errorf("display module %q: %w", node.Address, err)
			}
			nodeView["module"] = map[string]any{
				"plan":        child,
				"parallelism": node.Module.Parallelism,
				"timeout":     node.Module.Timeout.String(),
			}
		}
		nodes[index] = nodeView
	}
	outputs := make(map[string]any, len(planned.outputs))
	for name, output := range planned.outputs {
		value, err := displayValue(output.Value)
		if err != nil {
			return nil, fmt.Errorf("display output %q: %w", name, err)
		}
		outputs[name] = map[string]any{"value": value, "description": output.Description}
	}
	return map[string]any{
		"directory":     planned.directory,
		"run_directory": planned.runDirectory,
		"nodes":         nodes,
		"outputs":       outputs,
	}, nil
}

func displayValue(value cty.Value) (any, error) {
	unmarked, marks := value.Unmark()
	if len(marks) > 0 {
		return "<sensitive>", nil
	}
	if !unmarked.IsKnown() {
		return "<unknown>", nil
	}
	if unmarked.IsNull() {
		return nil, nil
	}
	typeValue := unmarked.Type()
	switch {
	case typeValue.IsObjectType(), typeValue.IsMapType():
		result := make(map[string]any, unmarked.LengthInt())
		for name, element := range unmarked.AsValueMap() {
			view, err := displayValue(element)
			if err != nil {
				return nil, err
			}
			result[name] = view
		}
		return result, nil
	case typeValue.IsTupleType(), typeValue.IsListType(), typeValue.IsSetType():
		result := make([]any, 0, unmarked.LengthInt())
		iterator := unmarked.ElementIterator()
		for iterator.Next() {
			_, element := iterator.Element()
			view, err := displayValue(element)
			if err != nil {
				return nil, err
			}
			result = append(result, view)
		}
		if typeValue.IsSetType() {
			sort.Slice(result, func(i, j int) bool {
				return fmt.Sprint(result[i]) < fmt.Sprint(result[j])
			})
		}
		return result, nil
	case typeValue.IsPrimitiveType():
		encoded, err := ctyjson.Marshal(unmarked, typeValue)
		if err != nil {
			return nil, fmt.Errorf("encode primitive display value: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var result any
		if err = decoder.Decode(&result); err != nil {
			return nil, fmt.Errorf("decode primitive display value: %w", err)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported display type %s", typeValue.FriendlyName())
	}
}
