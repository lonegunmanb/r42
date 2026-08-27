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
	nullable, err := optionalBoolAttribute(name, "nullable", true, attributes)
	if err != nil {
		return Variable{}, err
	}
	sensitive, err := optionalBoolAttribute(name, "sensitive", false, attributes)
	if err != nil {
		return Variable{}, err
	}
	typeDefaults := defaults
	if sensitive {
		typeDefaults = nil
	}
	formattedType, err := formatType(variableType, typeDefaults)
	if err != nil {
		return Variable{}, fmt.Errorf("variable %q type: %w", name, err)
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
				"variable %q default is incompatible with %s", name, formattedType,
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
		Name: name, Description: description, Type: formattedType,
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
	defaultValue bool,
	attributes map[string]*golden.HclAttribute,
) (bool, error) {
	attribute, exists := attributes[attributeName]
	if !exists {
		return defaultValue, nil
	}
	value, diagnostics := attribute.Expr.Value(nil)
	if diagnostics.HasErrors() || value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.Bool) {
		return false, fmt.Errorf("variable %q %s must be a known bool", variableName, attributeName)
	}
	return value.True(), nil
}

func formatType(valueType cty.Type, defaults *typeexpr.Defaults) (string, error) {
	switch valueType {
	case cty.String:
		return "string", nil
	case cty.Number:
		return "number", nil
	case cty.Bool:
		return "bool", nil
	}
	if valueType.IsListType() {
		element, err := formatType(valueType.ElementType(), childDefaults(defaults, ""))
		return "list(" + element + ")", err
	}
	if valueType.IsSetType() {
		element, err := formatType(valueType.ElementType(), childDefaults(defaults, ""))
		return "set(" + element + ")", err
	}
	if valueType.IsMapType() {
		element, err := formatType(valueType.ElementType(), childDefaults(defaults, ""))
		return "map(" + element + ")", err
	}
	if valueType.IsTupleType() {
		elements := valueType.TupleElementTypes()
		formatted := make([]string, len(elements))
		for index, element := range elements {
			var err error
			formatted[index], err = formatType(element, childDefaults(defaults, fmt.Sprint(index)))
			if err != nil {
				return "", err
			}
		}
		return "tuple([" + strings.Join(formatted, ",") + "])", nil
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
		attributeType, err := formatType(attributeTypes[name], childDefaults(defaults, name))
		if err != nil {
			return "", err
		}
		if _, isOptional := optional[name]; isOptional {
			if defaultValue, exists := lookupDefaultValue(defaults, name); exists {
				encoded, err := ctyjson.Marshal(defaultValue, defaultValue.Type())
				if err != nil {
					// note: untested because defaults are known values conforming to validated cty types.
					return "", fmt.Errorf("encode optional attribute %q default: %w", name, err)
				}
				attributeType = "optional(" + attributeType + "," + string(encoded) + ")"
			} else {
				attributeType = "optional(" + attributeType + ")"
			}
		}
		formatted = append(formatted, name+"="+attributeType)
	}
	return "object({" + strings.Join(formatted, ",") + "})", nil
}

func childDefaults(defaults *typeexpr.Defaults, name string) *typeexpr.Defaults {
	if defaults == nil {
		return nil
	}
	return defaults.Children[name]
}

func lookupDefaultValue(defaults *typeexpr.Defaults, name string) (cty.Value, bool) {
	if defaults == nil {
		return cty.NilVal, false
	}
	value, exists := defaults.DefaultValues[name]
	return value, exists
}
