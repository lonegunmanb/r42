package spec

import (
	"fmt"
	"slices"
	"sort"

	"github.com/zclconf/go-cty/cty"
)

type valueMark string

const sensitive valueMark = "sensitive"

func ValidateType(typeValue cty.Type) error {
	return validateTypeAt(typeValue, "value")
}

func validateTypeAt(typeValue cty.Type, path string) error {
	if typeValue.Equals(cty.NilType) {
		return fmt.Errorf("type is invalid")
	}
	if typeValue.Equals(cty.DynamicPseudoType) {
		return fmt.Errorf("type must not contain dynamic values at %s", path)
	}
	if typeValue.IsCapsuleType() {
		return fmt.Errorf("capsule type is not supported at %s", path)
	}
	if typeValue.IsPrimitiveType() {
		return nil
	}

	switch {
	case typeValue.IsListType(), typeValue.IsSetType():
		return validateTypeAt(typeValue.ElementType(), path+"[]")
	case typeValue.IsMapType():
		return validateTypeAt(typeValue.ElementType(), path+"{}")
	case typeValue.IsTupleType():
		for index, elementType := range typeValue.TupleElementTypes() {
			if err := validateTypeAt(elementType, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		return nil
	case typeValue.IsObjectType():
		attributeTypes := typeValue.AttributeTypes()
		attributeNames := make([]string, 0, len(attributeTypes))
		for name := range attributeTypes {
			attributeNames = append(attributeNames, name)
		}
		sort.Strings(attributeNames)
		for _, name := range attributeNames {
			if err := validateTypeAt(attributeTypes[name], path+"."+name); err != nil {
				return err
			}
		}
		return nil
	default:
		// note: untested because cty.Type is a closed type with all families handled above.
		return fmt.Errorf("unsupported type at %s", path)
	}
}

func MarkSensitive(value cty.Value) cty.Value {
	return value.Mark(sensitive)
}

func IsSensitive(value cty.Value) bool {
	return value.HasMarkDeep(sensitive)
}

func PropagateSensitive(result cty.Value, sources ...cty.Value) cty.Value {
	if slices.ContainsFunc(sources, IsSensitive) {
		return MarkSensitive(result)
	}

	return result
}
