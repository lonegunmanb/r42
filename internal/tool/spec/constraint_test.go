package spec_test

import (
	"reflect"
	"testing"

	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestJSONSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typeValue cty.Type
		expected  map[string]any
	}{
		{name: "string", typeValue: cty.String, expected: map[string]any{"type": "string"}},
		{name: "number", typeValue: cty.Number, expected: map[string]any{"type": "number"}},
		{name: "bool", typeValue: cty.Bool, expected: map[string]any{"type": "boolean"}},
		{
			name:      "list",
			typeValue: cty.List(cty.String),
			expected:  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		{
			name:      "set",
			typeValue: cty.Set(cty.Number),
			expected: map[string]any{
				"type": "array", "items": map[string]any{"type": "number"}, "uniqueItems": true,
			},
		},
		{
			name:      "map",
			typeValue: cty.Map(cty.Bool),
			expected: map[string]any{
				"type": "object", "additionalProperties": map[string]any{"type": "boolean"},
			},
		},
		{
			name:      "tuple",
			typeValue: cty.Tuple([]cty.Type{cty.String, cty.Number}),
			expected: map[string]any{
				"type": "array",
				"prefixItems": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
				},
				"items": false, "minItems": 2, "maxItems": 2,
			},
		},
		{
			name: "object with optional attribute",
			typeValue: cty.ObjectWithOptionalAttrs(
				map[string]cty.Type{"query": cty.String, "limit": cty.Number},
				[]string{"limit"},
			),
			expected: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "number"},
					"query": map[string]any{"type": "string"},
				},
				"required": []string{"query"}, "additionalProperties": false,
			},
		},
		{
			name:      "empty object",
			typeValue: cty.EmptyObject,
			expected: map[string]any{
				"type": "object", "properties": map[string]any{}, "additionalProperties": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := toolspec.JSONSchema(tt.typeValue)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestJSONSchemaRejectsUnsupportedTypes(t *testing.T) {
	t.Parallel()

	capsule := cty.Capsule("reader", reflect.TypeFor[interface{ Read([]byte) (int, error) }]())
	tests := []struct {
		name          string
		typeValue     cty.Type
		expectedError string
	}{
		{name: "nil", typeValue: cty.NilType, expectedError: "type is invalid"},
		{
			name: "dynamic", typeValue: cty.DynamicPseudoType,
			expectedError: "type must not contain dynamic values at value",
		},
		{
			name: "nested any", typeValue: cty.List(cty.DynamicPseudoType),
			expectedError: "type must not contain dynamic values at value[]",
		},
		{
			name: "capsule", typeValue: capsule,
			expectedError: "capsule type is not supported at value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := toolspec.JSONSchema(tt.typeValue)
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestConstraintApply(t *testing.T) {
	t.Parallel()

	constraint := parseConstraint(t, `object({
  query = string
  limit = optional(number, 20)
  note  = optional(string)
})`)

	actual, err := constraint.Apply(cty.ObjectVal(map[string]cty.Value{
		"query": cty.StringVal("energy"),
	}))
	require.NoError(t, err)
	assert.Equal(t, "energy", actual.GetAttr("query").AsString())
	assert.True(t, cty.NumberIntVal(20).RawEquals(actual.GetAttr("limit")))
	assert.True(t, actual.GetAttr("note").IsNull())

	explicit, err := constraint.Apply(cty.ObjectVal(map[string]cty.Value{
		"query": cty.StringVal("energy"),
		"limit": cty.NumberIntVal(5),
	}))
	require.NoError(t, err)
	assert.True(t, cty.NumberIntVal(5).RawEquals(explicit.GetAttr("limit")))
}

func TestConstraintApplyNestedDefaults(t *testing.T) {
	t.Parallel()

	constraint := parseConstraint(t, `object({
  query = string
  filters = optional(object({
    limit = optional(number, 20)
  }), {})
})`)

	actual, err := constraint.Apply(cty.ObjectVal(map[string]cty.Value{
		"query": cty.StringVal("energy"),
	}))
	require.NoError(t, err)
	assert.True(t, cty.NumberIntVal(20).RawEquals(actual.GetAttr("filters").GetAttr("limit")))
}

func TestConstraintApplyRejectsFinallyUnknownValue(t *testing.T) {
	t.Parallel()

	constraint := toolspec.NewConstraint(cty.Object(map[string]cty.Type{"query": cty.String}))
	_, err := constraint.Apply(cty.ObjectVal(map[string]cty.Value{
		"query": cty.UnknownVal(cty.String),
	}))
	assert.EqualError(t, err, "value must be wholly known")
}

func TestConstraintApplyRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		constraint    toolspec.Constraint
		value         cty.Value
		expectedError string
	}{
		{
			name:          "unsupported constraint",
			constraint:    toolspec.NewConstraint(cty.DynamicPseudoType),
			value:         cty.StringVal("value"),
			expectedError: "type must not contain dynamic values at value",
		},
		{
			name:          "value cannot convert",
			constraint:    toolspec.NewConstraint(cty.Object(map[string]cty.Type{"query": cty.String})),
			value:         cty.NumberIntVal(42),
			expectedError: "value does not match type constraint",
		},
		{
			name:       "required attribute is null",
			constraint: toolspec.NewConstraint(cty.Object(map[string]cty.Type{"query": cty.String})),
			value: cty.ObjectVal(map[string]cty.Value{
				"query": cty.NullVal(cty.String),
			}),
			expectedError: "null is not allowed at value.query",
		},
		{
			name:       "object has undeclared attribute",
			constraint: toolspec.NewConstraint(cty.Object(map[string]cty.Type{"query": cty.String})),
			value: cty.ObjectVal(map[string]cty.Value{
				"query": cty.StringVal("energy"),
				"extra": cty.True,
			}),
			expectedError: "undeclared attribute at value.extra",
		},
		{
			name: "nested object has undeclared attribute",
			constraint: toolspec.NewConstraint(cty.Object(map[string]cty.Type{
				"filter": cty.Object(map[string]cty.Type{"query": cty.String}),
			})),
			value: cty.ObjectVal(map[string]cty.Value{
				"filter": cty.ObjectVal(map[string]cty.Value{
					"query": cty.StringVal("energy"),
					"extra": cty.True,
				}),
			}),
			expectedError: "undeclared attribute at value.filter.extra",
		},
		{
			name:          "collection element is null",
			constraint:    toolspec.NewConstraint(cty.List(cty.String)),
			value:         cty.TupleVal([]cty.Value{cty.NullVal(cty.String)}),
			expectedError: "null is not allowed at value[0]",
		},
		{
			name:          "set element is null",
			constraint:    toolspec.NewConstraint(cty.Set(cty.String)),
			value:         cty.TupleVal([]cty.Value{cty.NullVal(cty.String)}),
			expectedError: "null is not allowed at value[0]",
		},
		{
			name:       "map element is null",
			constraint: toolspec.NewConstraint(cty.Map(cty.String)),
			value: cty.ObjectVal(map[string]cty.Value{
				"query": cty.NullVal(cty.String),
			}),
			expectedError: "null is not allowed at value.query",
		},
		{
			name:          "tuple element is null",
			constraint:    toolspec.NewConstraint(cty.Tuple([]cty.Type{cty.String})),
			value:         cty.TupleVal([]cty.Value{cty.NullVal(cty.String)}),
			expectedError: "null is not allowed at value[0]",
		},
		{
			name:          "top-level value is null",
			constraint:    toolspec.NewConstraint(cty.String),
			value:         cty.NullVal(cty.String),
			expectedError: "null is not allowed at value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.constraint.Apply(tt.value)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestConstraintJSONSchema(t *testing.T) {
	t.Parallel()

	constraint := toolspec.NewConstraint(cty.String)
	actual, err := constraint.JSONSchema()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"type": "string"}, actual)
}
