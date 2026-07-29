package goldenprobe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

//nolint:paralleltest // Golden's block registry is process-global.
func TestGolden_CustomBlocksAndDependencies(t *testing.T) {
	result, err := runDependencyProbe()
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"probe.value.first":  "HELLO",
		"probe.value.second": "HELLO",
	}, result.values)
	assert.Contains(t, result.implicitAncestors, "probe.value.first")
	assert.Contains(t, result.explicitAncestors, "probe.value.first")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestGolden_PlanAndApplySurface(t *testing.T) {
	result, err := runPlanApplyProbe()
	require.NoError(t, err)

	assert.Equal(t, []string{"Apply", "String"}, result.planInterfaceMethods)
	assert.False(t, result.appliedAfterPlan)
	assert.True(t, result.appliedAfterTraverse)
}

//nolint:paralleltest // Golden's variable loader reads process-global environment state.
func TestGolden_VariablePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		sources  variableSources
		expected string
	}{
		{name: "block default", sources: variableSources{}, expected: "block"},
		{name: "environment over block default", sources: variableSources{environment: "environment"}, expected: "environment"},
		{name: "default file over environment", sources: variableSources{environment: "environment", defaultFile: "default-file"}, expected: "default-file"},
		{name: "default json over default hcl", sources: variableSources{defaultFile: "default-hcl", defaultJSON: "default-json"}, expected: "default-json"},
		{name: "automatic file over default file", sources: variableSources{defaultFile: "default-file", autoFile: "auto-file"}, expected: "auto-file"},
		{
			name: "lexically later automatic file across formats",
			sources: variableSources{autoFiles: []namedVariableFile{
				{name: "a.auto.r42vars.json", value: "auto-json"},
				{name: "z.auto.r42vars", value: "auto-hcl"},
			}},
			expected: "auto-hcl",
		},
		{name: "cli over automatic file", sources: variableSources{autoFile: "auto-file", cli: "cli"}, expected: "cli"},
		{
			name: "later explicit cli file over direct value",
			sources: variableSources{
				additionalFiles: []namedVariableFile{{name: "explicit.r42vars", value: "explicit-file"}},
				cliSources: []cliVariableSource{
					{value: "direct"},
					{fileName: "explicit.r42vars"},
				},
			},
			expected: "explicit-file",
		},
		{
			name: "later direct value over explicit cli file",
			sources: variableSources{
				additionalFiles: []namedVariableFile{{name: "explicit.r42vars.json", value: "explicit-file"}},
				cliSources: []cliVariableSource{
					{fileName: "explicit.r42vars.json"},
					{value: "direct"},
				},
			},
			expected: "direct",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := readVariable(t, test.sources)
			require.NoError(t, err)
			assert.Equal(t, test.expected, actual)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestGolden_DynamicModuleInputsNeedCustomDecode(t *testing.T) {
	result, err := runDynamicInputProbe()
	require.NoError(t, err)

	require.ErrorContains(t, result.staticDecodeError, "Unsupported argument")
	assert.Equal(t, "./child", result.customSource)
	assert.Len(t, result.customInputs, 2)
	assert.True(t, cty.StringVal("energy").RawEquals(result.customInputs["topic"]))
	assert.True(t, cty.NumberIntVal(2030).RawEquals(result.customInputs["year"]))
	assert.Contains(t, result.explicitAncestors, "custom_module.upstream")
	assert.ElementsMatch(t, []string{"./a", "./b"}, result.expandedSources)
}

//nolint:paralleltest // Golden's custom type registry is process-global.
func TestGolden_CustomTypeMapping(t *testing.T) {
	assert.Equal(t, cty.Object(map[string]cty.Type{
		"payload": cty.String,
	}), mappedProbeType())
}
