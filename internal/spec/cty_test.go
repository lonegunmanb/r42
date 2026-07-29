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

func TestPropagateSensitive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		result            cty.Value
		sources           []cty.Value
		expectedKnown     bool
		expectedNull      bool
		expectedSensitive bool
		expectedMark      any
	}{
		{
			name:              "ordinary source",
			result:            cty.StringVal("public"),
			sources:           []cty.Value{cty.StringVal("source")},
			expectedKnown:     true,
			expectedSensitive: false,
		},
		{
			name:              "sensitive known source",
			result:            cty.StringVal("derived"),
			sources:           []cty.Value{spec.MarkSensitive(cty.StringVal("secret"))},
			expectedKnown:     true,
			expectedSensitive: true,
		},
		{
			name:              "unrelated result mark",
			result:            cty.StringVal("derived").Mark("audit"),
			sources:           []cty.Value{spec.MarkSensitive(cty.StringVal("secret"))},
			expectedKnown:     true,
			expectedSensitive: true,
			expectedMark:      "audit",
		},
		{
			name:              "existing result mark",
			result:            spec.MarkSensitive(cty.StringVal("secret")),
			expectedKnown:     true,
			expectedSensitive: true,
		},
		{
			name:              "nested sensitive source",
			result:            cty.StringVal("derived"),
			sources:           []cty.Value{cty.ObjectVal(map[string]cty.Value{"token": spec.MarkSensitive(cty.StringVal("secret"))})},
			expectedKnown:     true,
			expectedSensitive: true,
		},
		{
			name:              "unknown sensitive source",
			result:            cty.StringVal("derived"),
			sources:           []cty.Value{spec.MarkSensitive(cty.UnknownVal(cty.String))},
			expectedKnown:     true,
			expectedSensitive: true,
		},
		{
			name:              "null sensitive source",
			result:            cty.StringVal("derived"),
			sources:           []cty.Value{spec.MarkSensitive(cty.NullVal(cty.String))},
			expectedKnown:     true,
			expectedSensitive: true,
		},
		{
			name:              "unknown result",
			result:            cty.UnknownVal(cty.String),
			sources:           []cty.Value{spec.MarkSensitive(cty.StringVal("secret"))},
			expectedKnown:     false,
			expectedSensitive: true,
		},
		{
			name:              "null result",
			result:            cty.NullVal(cty.String),
			sources:           []cty.Value{spec.MarkSensitive(cty.StringVal("secret"))},
			expectedKnown:     true,
			expectedNull:      true,
			expectedSensitive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := spec.PropagateSensitive(tt.result, tt.sources...)
			actualUnmarked, _ := actual.UnmarkDeep()
			resultUnmarked, _ := tt.result.UnmarkDeep()
			assert.True(t, actual.Type().Equals(tt.result.Type()))
			assert.True(t, actualUnmarked.RawEquals(resultUnmarked))
			assert.Equal(t, tt.expectedKnown, actual.IsKnown())
			assert.Equal(t, tt.expectedNull, actual.IsNull())
			assert.Equal(t, tt.expectedSensitive, spec.IsSensitive(actual))
			if tt.expectedMark != nil {
				assert.True(t, actual.HasMark(tt.expectedMark))
			}
		})
	}
}
