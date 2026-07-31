package spec_test

import (
	"reflect"
	"testing"

	"github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestValidateType(t *testing.T) {
	t.Parallel()

	capsule := cty.Capsule("reader", reflect.TypeFor[interface{ Read([]byte) (int, error) }]())
	tests := []struct {
		name          string
		typeValue     cty.Type
		expectedError string
	}{
		{name: "string", typeValue: cty.String},
		{name: "number", typeValue: cty.Number},
		{name: "bool", typeValue: cty.Bool},
		{name: "list", typeValue: cty.List(cty.String)},
		{name: "set", typeValue: cty.Set(cty.Number)},
		{name: "map", typeValue: cty.Map(cty.Bool)},
		{name: "tuple", typeValue: cty.Tuple([]cty.Type{cty.String, cty.Number})},
		{name: "object", typeValue: cty.Object(map[string]cty.Type{"enabled": cty.Bool})},
		{
			name: "optional object attribute",
			typeValue: cty.ObjectWithOptionalAttrs(
				map[string]cty.Type{"query": cty.String, "limit": cty.Number},
				[]string{"limit"},
			),
		},
		{name: "nil", typeValue: cty.NilType, expectedError: "type is invalid"},
		{name: "dynamic", typeValue: cty.DynamicPseudoType, expectedError: "type must not contain dynamic values at value"},
		{name: "any", typeValue: cty.List(cty.DynamicPseudoType), expectedError: "type must not contain dynamic values at value[]"},
		{name: "capsule", typeValue: capsule, expectedError: "capsule type is not supported at value"},
		{
			name:          "nested dynamic",
			typeValue:     cty.Object(map[string]cty.Type{"items": cty.Tuple([]cty.Type{cty.String, cty.DynamicPseudoType})}),
			expectedError: "type must not contain dynamic values at value.items[1]",
		},
		{
			name:          "nested capsule",
			typeValue:     cty.Map(capsule),
			expectedError: "capsule type is not supported at value{}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := spec.ValidateType(tt.typeValue)
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestValidateTypePreservesOptionalAttributes(t *testing.T) {
	t.Parallel()

	typeValue := cty.ObjectWithOptionalAttrs(
		map[string]cty.Type{"query": cty.String, "limit": cty.Number},
		[]string{"limit"},
	)

	require.NoError(t, spec.ValidateType(typeValue))
	assert.True(t, typeValue.AttributeOptional("limit"))
}
