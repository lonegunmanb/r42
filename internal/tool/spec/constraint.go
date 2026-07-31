package spec

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

type Constraint struct {
	typeValue cty.Type
	defaults  *typeexpr.Defaults
}

func NewConstraint(typeValue cty.Type) Constraint {
	return Constraint{typeValue: typeValue}
}

func NewConstraintWithDefaults(typeValue cty.Type, defaults *typeexpr.Defaults) Constraint {
	return Constraint{typeValue: typeValue, defaults: defaults}
}

func (c Constraint) Type() cty.Type {
	return c.typeValue
}

func (c Constraint) Apply(value cty.Value) (cty.Value, error) {
	if err := corespec.ValidateType(c.typeValue); err != nil {
		return cty.NilVal, err
	}
	if c.defaults != nil {
		value = c.defaults.Apply(value)
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsWhollyKnown() {
		return cty.NilVal, fmt.Errorf("value must be wholly known")
	}
	if err := validateValueShape(unmarked, c.typeValue, "value", false); err != nil {
		return cty.NilVal, err
	}
	converted, err := convert.Convert(value, c.typeValue)
	if err != nil {
		return cty.NilVal, fmt.Errorf("value does not match type constraint: %w", err)
	}

	return converted, nil
}

func validateValueShape(value cty.Value, target cty.Type, path string, nullable bool) error {
	if value.IsNull() {
		if nullable {
			return nil
		}
		return fmt.Errorf("null is not allowed at %s", path)
	}

	sourceType := value.Type()
	switch {
	case target.IsObjectType() && (sourceType.IsObjectType() || sourceType.IsMapType()):
		return validateObjectShape(value, target, path)
	case target.IsMapType() && (sourceType.IsObjectType() || sourceType.IsMapType()):
		for name, element := range value.AsValueMap() {
			if err := validateValueShape(element, target.ElementType(), path+"."+name, false); err != nil {
				return err
			}
		}
	case (target.IsListType() || target.IsSetType()) &&
		(sourceType.IsTupleType() || sourceType.IsListType() || sourceType.IsSetType()):
		return validateCollectionShape(value, target.ElementType(), path)
	case target.IsTupleType() && (sourceType.IsTupleType() || sourceType.IsListType()):
		elements := target.TupleElementTypes()
		iterator := value.ElementIterator()
		for index := 0; iterator.Next() && index < len(elements); index++ {
			_, element := iterator.Element()
			if err := validateValueShape(element, elements[index], fmt.Sprintf("%s[%d]", path, index), false); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateObjectShape(value cty.Value, target cty.Type, path string) error {
	attributes := value.AsValueMap()
	targetAttributes := target.AttributeTypes()
	for name := range attributes {
		if _, ok := targetAttributes[name]; !ok {
			return fmt.Errorf("undeclared attribute at %s.%s", path, name)
		}
	}
	for name, attributeType := range targetAttributes {
		attribute, ok := attributes[name]
		if !ok {
			continue
		}
		if err := validateValueShape(attribute, attributeType, path+"."+name, target.AttributeOptional(name)); err != nil {
			return err
		}
	}

	return nil
}

func validateCollectionShape(value cty.Value, elementType cty.Type, path string) error {
	iterator := value.ElementIterator()
	for index := 0; iterator.Next(); index++ {
		_, element := iterator.Element()
		if err := validateValueShape(element, elementType, fmt.Sprintf("%s[%d]", path, index), false); err != nil {
			return err
		}
	}

	return nil
}

func (c Constraint) JSONSchema() (map[string]any, error) {
	return JSONSchema(c.typeValue)
}

func JSONSchema(typeValue cty.Type) (map[string]any, error) {
	if err := corespec.ValidateType(typeValue); err != nil {
		return nil, err
	}

	return schemaForType(typeValue), nil
}

func schemaForType(typeValue cty.Type) map[string]any {
	switch {
	case typeValue.Equals(cty.String):
		return map[string]any{"type": "string"}
	case typeValue.Equals(cty.Number):
		return map[string]any{"type": "number"}
	case typeValue.Equals(cty.Bool):
		return map[string]any{"type": "boolean"}
	case typeValue.IsListType():
		return map[string]any{
			"type":  "array",
			"items": schemaForType(typeValue.ElementType()),
		}
	case typeValue.IsSetType():
		return map[string]any{
			"type":        "array",
			"items":       schemaForType(typeValue.ElementType()),
			"uniqueItems": true,
		}
	case typeValue.IsMapType():
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaForType(typeValue.ElementType()),
		}
	case typeValue.IsTupleType():
		elementTypes := typeValue.TupleElementTypes()
		prefixItems := make([]any, 0, len(elementTypes))
		for _, elementType := range elementTypes {
			prefixItems = append(prefixItems, schemaForType(elementType))
		}
		return map[string]any{
			"type":        "array",
			"prefixItems": prefixItems,
			"items":       false,
			"minItems":    len(elementTypes),
			"maxItems":    len(elementTypes),
		}
	case typeValue.IsObjectType():
		return schemaForObject(typeValue)
	default:
		// note: untested because ValidateType rejects every other cty type family.
		return nil
	}
}

func schemaForObject(typeValue cty.Type) map[string]any {
	attributeTypes := typeValue.AttributeTypes()
	names := make([]string, 0, len(attributeTypes))
	for name := range attributeTypes {
		names = append(names, name)
	}
	sort.Strings(names)

	properties := make(map[string]any, len(names))
	required := make([]string, 0, len(names))
	for _, name := range names {
		properties[name] = schemaForType(attributeTypes[name])
		if !typeValue.AttributeOptional(name) {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}
