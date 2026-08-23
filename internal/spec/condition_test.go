package spec_test

import (
	"testing"

	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestConditionEvaluateUsesOnlyDeclaredSpecialVariables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		condition     corespec.Condition
		variables     map[string]cty.Value
		expectedKnown bool
		expectedError string
	}{
		{
			name: "passes", condition: corespec.Condition{
				Expression: "input.name != \"\"", ErrorMessage: "name is required",
			},
			variables: map[string]cty.Value{"input": cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("r42"),
			})},
			expectedKnown: true,
		},
		{
			name: "known failure", condition: corespec.Condition{
				Expression: "input.name != \"\"", ErrorMessage: "name is required",
			},
			variables: map[string]cty.Value{"input": cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal(""),
			})},
			expectedKnown: true, expectedError: "name is required",
		},
		{
			name: "unknown defers", condition: corespec.Condition{
				Expression: "input.name != \"\"", ErrorMessage: "name is required",
			},
			variables: map[string]cty.Value{"input": cty.UnknownVal(cty.Object(map[string]cty.Type{
				"name": cty.String,
			}))},
		},
		{
			name: "undeclared root", condition: corespec.Condition{
				Expression: "var.enabled", ErrorMessage: "must be enabled",
			},
			variables:     map[string]cty.Value{"input": cty.EmptyObjectVal},
			expectedError: "may only reference input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			known, err := tt.condition.Evaluate(tt.variables, nil)
			assert.Equal(t, tt.expectedKnown, known)
			if tt.expectedError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}
