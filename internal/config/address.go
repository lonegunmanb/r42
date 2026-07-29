package config

import (
	"errors"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

type AddressKind string

const (
	AddressKindUnknown  AddressKind = ""
	AddressKindGo       AddressKind = "go"
	AddressKindExternal AddressKind = "external"
	AddressKindBuiltin  AddressKind = "builtin"
)

type Address struct {
	Kind  AddressKind
	Value string
}

type addressMark struct {
	Address Address
}

func (a Address) CtyValue() cty.Value {
	return cty.StringVal(a.Value).Mark(addressMark{Address: a})
}

func AddressFromValue(value cty.Value) (Address, bool) {
	if !value.Type().Equals(cty.String) || !value.IsKnown() || value.IsNull() {
		return Address{}, false
	}

	unmarked, marks := value.Unmark()
	var result Address
	found := false
	for mark := range marks {
		addressValue, ok := mark.(addressMark)
		if !ok {
			continue
		}
		address := addressValue.Address
		if found || !validAddressKind(address.Kind) || unmarked.AsString() != address.Value {
			return Address{}, false
		}
		result = address
		found = true
	}

	return result, found
}

func Functions() map[string]function.Function {
	return map[string]function.Function{
		"tool_name": toolNameFunction(),
	}
}

func toolNameFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name:             "address",
				Description:      "A typed tool address.",
				Type:             cty.String,
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
			for mark := range marks {
				if _, ok := mark.(addressMark); ok {
					delete(marks, mark)
				}
			}
			return cty.StringVal(strings.ReplaceAll(address.Value, ".", "_")).WithMarks(marks), nil
		},
	})
}

func validAddressKind(kind AddressKind) bool {
	switch kind {
	case AddressKindGo, AddressKindExternal, AddressKindBuiltin:
		return true
	default:
		return false
	}
}
