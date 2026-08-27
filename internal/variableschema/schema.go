package variableschema

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/config"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const Version = 1

type Document struct {
	SchemaVersion int        `json:"schema_version"`
	Variables     []Variable `json:"variables"`
}

type Variable struct {
	Name            string          `json:"name"`
	Description     *string         `json:"description"`
	Type            string          `json:"type"`
	Required        bool            `json:"required"`
	Nullable        bool            `json:"nullable"`
	Sensitive       bool            `json:"sensitive"`
	HasDefault      bool            `json:"has_default"`
	Default         json.RawMessage `json:"default"`
	DefaultRedacted bool            `json:"default_redacted"`
}

func Inspect(ctx context.Context, directory string) (Document, error) {
	loaded, diagnostics, err := config.LoadDirectoryContext(ctx, directory)
	if err != nil {
		return Document{}, fmt.Errorf("load root configuration: %w", err)
	}
	if diagnostics.HasErrors() {
		return Document{}, diagnostics
	}

	variables := make([]Variable, 0)
	seen := make(map[string]struct{})
	for _, block := range loaded.Blocks {
		if block.Type != "variable" || len(block.Labels) < 2 {
			continue
		}
		name := block.Labels[1]
		if _, exists := seen[name]; exists {
			return Document{}, fmt.Errorf("variable %q is declared more than once", name)
		}
		seen[name] = struct{}{}
		variable, inspectErr := inspectVariable(block)
		if inspectErr != nil {
			return Document{}, inspectErr
		}
		variables = append(variables, variable)
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	return Document{SchemaVersion: Version, Variables: variables}, nil
}

func Marshal(document Document) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode variable schema: %w", err)
	}
	return output.Bytes(), nil
}

func inspectVariable(block *golden.HclBlock) (Variable, error) {
	name := block.Labels[1]
	attributes := block.Attributes()
	typeAttribute, exists := attributes["type"]
	if !exists {
		return Variable{}, fmt.Errorf("variable %q must declare type", name)
	}
	variableType, defaults, diagnostics := typeexpr.TypeConstraintWithDefaults(typeAttribute.Expr)
	if diagnostics.HasErrors() {
		return Variable{}, fmt.Errorf("variable %q type: %w", name, diagnostics)
	}
	if err := corespec.ValidateType(variableType); err != nil {
		return Variable{}, fmt.Errorf("variable %q type: %w", name, err)
	}

	description, err := optionalStringAttribute(name, "description", attributes)
	if err != nil {
		return Variable{}, err
	}
	nullable, err := optionalBoolAttribute(name, "nullable", attributes)
	if err != nil {
		return Variable{}, err
	}
	sensitive, err := optionalBoolAttribute(name, "sensitive", attributes)
	if err != nil {
		return Variable{}, err
	}

	defaultJSON := json.RawMessage("null")
	defaultAttribute, hasDefault := attributes["default"]
	defaultRedacted := sensitive && hasDefault
	if hasDefault {
		value, valueDiagnostics := defaultAttribute.Expr.Value(nil)
		if valueDiagnostics.HasErrors() {
			if sensitive {
				return Variable{}, fmt.Errorf("variable %q default is invalid", name)
			}
			return Variable{}, fmt.Errorf("variable %q default: %w", name, valueDiagnostics)
		}
		if defaults != nil {
			value = defaults.Apply(value)
		}
		converted, convertErr := convert.Convert(value, variableType)
		if convertErr != nil {
			return Variable{}, fmt.Errorf(
				"variable %q default is incompatible with %s", name, formatType(variableType),
			)
		}
		if !sensitive {
			defaultJSON, err = ctyjson.Marshal(converted, converted.Type())
			if err != nil {
				// note: untested because converted is a known value conforming to a validated cty type.
				return Variable{}, fmt.Errorf("encode variable %q default: %w", name, err)
			}
		}
	}

	return Variable{
		Name: name, Description: description, Type: formatType(variableType),
		Required: !hasDefault, Nullable: nullable, Sensitive: sensitive,
		HasDefault: hasDefault, Default: defaultJSON, DefaultRedacted: defaultRedacted,
	}, nil
}

func optionalStringAttribute(
	variableName string,
	attributeName string,
	attributes map[string]*golden.HclAttribute,
) (*string, error) {
	attribute, exists := attributes[attributeName]
	if !exists {
		return nil, nil
	}
	value, diagnostics := attribute.Expr.Value(nil)
	if diagnostics.HasErrors() || value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.String) {
		return nil, fmt.Errorf("variable %q %s must be a known string", variableName, attributeName)
	}
	result := value.AsString()
	return &result, nil
}

func optionalBoolAttribute(
	variableName string,
	attributeName string,
	attributes map[string]*golden.HclAttribute,
) (bool, error) {
	attribute, exists := attributes[attributeName]
	if !exists {
		return false, nil
	}
	value, diagnostics := attribute.Expr.Value(nil)
	if diagnostics.HasErrors() || value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.Bool) {
		return false, fmt.Errorf("variable %q %s must be a known bool", variableName, attributeName)
	}
	return value.True(), nil
}

func formatType(valueType cty.Type) string {
	switch valueType {
	case cty.String:
		return "string"
	case cty.Number:
		return "number"
	case cty.Bool:
		return "bool"
	}
	if valueType.IsListType() {
		return "list(" + formatType(valueType.ElementType()) + ")"
	}
	if valueType.IsSetType() {
		return "set(" + formatType(valueType.ElementType()) + ")"
	}
	if valueType.IsMapType() {
		return "map(" + formatType(valueType.ElementType()) + ")"
	}
	if valueType.IsTupleType() {
		elements := valueType.TupleElementTypes()
		formatted := make([]string, len(elements))
		for index, element := range elements {
			formatted[index] = formatType(element)
		}
		return "tuple([" + strings.Join(formatted, ",") + "])"
	}

	attributeTypes := valueType.AttributeTypes()
	attributeNames := make([]string, 0, len(attributeTypes))
	for name := range attributeTypes {
		attributeNames = append(attributeNames, name)
	}
	sort.Strings(attributeNames)
	optional := valueType.OptionalAttributes()
	formatted := make([]string, 0, len(attributeNames))
	for _, name := range attributeNames {
		attributeType := formatType(attributeTypes[name])
		if _, isOptional := optional[name]; isOptional {
			attributeType = "optional(" + attributeType + ")"
		}
		formatted = append(formatted, name+"="+attributeType)
	}
	return "object({" + strings.Join(formatted, ",") + "})"
}
