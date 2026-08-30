package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

type AddressKind string

const (
	AddressKindUnknown  AddressKind = ""
	AddressKindGo       AddressKind = "go"
	AddressKindExternal AddressKind = "external"
	AddressKindStarlark AddressKind = "starlark"
	AddressKindBuiltin  AddressKind = "builtin"
)

type Address struct {
	Kind  AddressKind
	Value string
}

func (a Address) CtyValue() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal(a.Value),
		"kind":    cty.StringVal(string(a.Kind)),
	})
}

func AddressFromValue(value cty.Value) (Address, bool) {
	unmarked, _ := value.Unmark()
	valueType := unmarked.Type()
	if !valueType.IsObjectType() || !unmarked.IsWhollyKnown() || unmarked.IsNull() || unmarked.ContainsMarked() {
		return Address{}, false
	}
	if !valueType.HasAttribute("address") || !valueType.HasAttribute("kind") {
		return Address{}, false
	}
	if !valueType.AttributeType("address").Equals(cty.String) || !valueType.AttributeType("kind").Equals(cty.String) {
		return Address{}, false
	}

	addressValue := unmarked.GetAttr("address")
	kindValue := unmarked.GetAttr("kind")
	if addressValue.IsNull() || kindValue.IsNull() {
		return Address{}, false
	}
	result := Address{
		Kind:  AddressKind(kindValue.AsString()),
		Value: addressValue.AsString(),
	}

	return result, validAddress(result)
}

func Functions() map[string]function.Function {
	return map[string]function.Function{
		"cwd":                cwdFunction(),
		"one":                oneFunction(),
		"local_timestamp":    localTimestampFunction(),
		"tool_name":          toolNameFunction(),
		"jsondecodewithtype": jsonDecodeWithTypeFunction(),
	}
}

func localTimestampFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) {
			return cty.StringVal(time.Now().Format(time.RFC3339Nano)), nil
		},
	})
}

func jsonDecodeWithTypeFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "json", Type: cty.String},
			{Name: "type", Type: typeexpr.TypeConstraintType},
		},
		Type: func(arguments []cty.Value) (cty.Type, error) {
			if len(arguments) != 2 || !arguments[1].IsKnown() || arguments[1].IsNull() {
				return cty.NilType, function.NewArgError(1, errors.New("type must be known"))
			}
			return typeexpr.TypeConstraintFromVal(arguments[1]), nil
		},
		Impl: func(arguments []cty.Value, returnType cty.Type) (cty.Value, error) {
			if !json.Valid([]byte(arguments[0].AsString())) {
				return cty.NilVal, function.NewArgError(0, errors.New("json must be valid"))
			}
			decoded, err := ctyjson.Unmarshal([]byte(arguments[0].AsString()), returnType)
			if err != nil {
				return cty.NilVal, function.NewArgError(0, err)
			}
			return decoded, nil
		},
	})
}

func cwdFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) {
			workingDirectory, err := os.Getwd()
			if err != nil {
				// note: untested because os.Getwd failure requires invalidating process-wide filesystem state.
				return cty.NilVal, fmt.Errorf("get current working directory: %w", err)
			}
			return cty.StringVal(filepath.ToSlash(workingDirectory)), nil
		},
	})
}

func oneFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{
			Name:             "collection",
			Type:             cty.DynamicPseudoType,
			AllowDynamicType: true,
		}},
		Type: func(arguments []cty.Value) (cty.Type, error) {
			collectionType := arguments[0].Type()
			switch {
			case collectionType.Equals(cty.DynamicPseudoType):
				return cty.DynamicPseudoType, nil
			case collectionType.IsListType(), collectionType.IsSetType():
				return collectionType.ElementType(), nil
			case collectionType.IsTupleType():
				elementTypes := collectionType.TupleElementTypes()
				if len(elementTypes) == 1 {
					return elementTypes[0], nil
				}
				return cty.DynamicPseudoType, nil
			default:
				return cty.NilType, function.NewArgError(0, errors.New("one requires a list, set, or tuple"))
			}
		},
		Impl: func(arguments []cty.Value, returnType cty.Type) (cty.Value, error) {
			collection := arguments[0]
			if collection.IsNull() {
				return cty.NilVal, function.NewArgError(0, errors.New("one collection must not be null"))
			}
			if collection.LengthInt() > 1 {
				return cty.NilVal, function.NewArgError(0, errors.New("one collection must contain zero or one elements"))
			}
			iterator := collection.ElementIterator()
			if !iterator.Next() {
				return cty.NullVal(returnType), nil
			}
			_, value := iterator.Element()
			return value, nil
		},
	})
}

func toolNameFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name:             "address",
				Description:      "A typed tool address.",
				Type:             cty.DynamicPseudoType,
				AllowMarked:      true,
				AllowUnknown:     true,
				AllowDynamicType: true,
			},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(arguments []cty.Value, _ cty.Type) (cty.Value, error) {
			address, ok := AddressFromValue(arguments[0])
			if !ok {
				return cty.NilVal, function.NewArgError(0, errors.New("tool_name requires a typed tool address"))
			}

			_, marks := arguments[0].Unmark()
			unmarked, _ := arguments[0].UnmarkDeep()
			if unmarked.Type().HasAttribute("id") {
				id := unmarked.GetAttr("id")
				if !id.IsNull() && id.Type().Equals(cty.String) {
					return cty.StringVal(id.AsString()).WithMarks(marks), nil
				}
			}
			return cty.StringVal(strings.ReplaceAll(address.Value, ".", "_")).WithMarks(marks), nil
		},
	})
}

func validAddress(address Address) bool {
	switch address.Kind {
	case AddressKindGo:
		return hasNamedPrefix(address.Value, "go_tool.")
	case AddressKindExternal:
		return hasNamedPrefix(address.Value, "external_tool.")
	case AddressKindStarlark:
		return hasNamedPrefix(address.Value, "starlark_tool.")
	case AddressKindBuiltin:
		return address.Value != ""
	default:
		return false
	}
}

func hasNamedPrefix(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix)
}
