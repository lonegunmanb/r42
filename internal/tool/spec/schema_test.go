package spec_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/config"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type toolTestConfig struct {
	*golden.BaseConfig
	canonicalPrefix string
}

func (c *toolTestConfig) CanonicalAddress(address string) string {
	return c.canonicalPrefix + "." + address
}

var registerToolBlocks sync.Once

//nolint:paralleltest // Golden's block registry is process-global.
func TestExternalToolBlockDecodesTypeConstraints(t *testing.T) {
	config := parseToolConfig(t, `
external_tool "search_catalog" {
  description = "Search the catalog"
  program     = ["catalog-search", "--json"]
  working_dir = "./catalog"

  input_type = object({
    query = string
    limit = optional(number, 20)
  })

  output_type = object({
    matches = list(string)
  })
}
`)

	require.NoError(t, config.RunPlan())

	blocks := golden.Blocks[*toolspec.ExternalToolBlock](config)
	require.Len(t, blocks, 1)
	block := blocks[0]
	assert.Equal(t, "Search the catalog", block.Description)
	assert.Equal(t, []string{"catalog-search", "--json"}, block.Program)
	assert.Equal(t, "./catalog", block.WorkingDir)
	assert.True(t, block.InputConstraint().Type().AttributeOptional("limit"))
	assert.True(t, block.OutputConstraint().Type().Equals(cty.Object(map[string]cty.Type{
		"matches": cty.List(cty.String),
	})))

	actual, err := block.InputConstraint().Apply(cty.ObjectVal(map[string]cty.Value{
		"query": cty.StringVal("energy"),
	}))
	require.NoError(t, err)
	assert.True(t, cty.NumberIntVal(20).RawEquals(actual.GetAttr("limit")))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestStarlarkToolBlockDecodesDefaultsAndUsageContract(t *testing.T) {
	configValue := parseToolConfig(t, `
starlark_tool "calculator" {
  description = "Execute isolated numerical Starlark programs."
}
`)

	require.NoError(t, configValue.RunPlan())
	blocks := golden.Blocks[*toolspec.StarlarkToolBlock](configValue)
	require.Len(t, blocks, 1)
	block := blocks[0]
	assert.Equal(t, 1_000_000, block.MaxSteps)
	assert.Equal(t, 5*time.Second, block.Timeout)
	assert.Equal(t, 65_536, block.MaxSourceBytes)
	assert.Equal(t, 1_048_576, block.MaxDataBytes)
	assert.Equal(t, 1_048_576, block.MaxResultBytes)
	assert.Equal(t, 16_384, block.MaxStdoutBytes)
	assert.Equal(t, 134_217_728, block.MemoryLimit)
	assert.Contains(t, block.ModelDescription(), "https://github.com/google/starlark-go/blob/master/doc/spec.md")
	assert.Contains(t, block.ModelDescription(), "https://github.com/google/starlark-go")
	assert.Contains(t, block.ModelDescription(), "https://pkg.go.dev/go.starlark.net/starlark")
	assert.Contains(t, block.ModelDescription(), "https://bazel.build/rules/language")

	address, ok := config.AddressFromValue(block.Value())
	require.True(t, ok)
	assert.Equal(t, config.AddressKindStarlark, address.Kind)
	assert.Equal(t, "starlark_tool.calculator", address.Value)
	assert.Regexp(t, `^tool_starlark_tool_[a-z]+_[0-9a-f-]{36}$`, block.Id())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestStarlarkToolBlockRejectsResourceLimits(t *testing.T) {
	tests := []struct {
		name          string
		attribute     string
		expectedError string
	}{
		{name: "steps", attribute: "max_steps = 10000001", expectedError: "starlark tool max_steps must not exceed 10000000"},
		{name: "timeout", attribute: `timeout = "31s"`, expectedError: "starlark tool timeout must not exceed 30s"},
		{name: "source", attribute: "max_source_bytes = 262145", expectedError: "starlark tool max_source_bytes must not exceed 262144"},
		{name: "data", attribute: "max_data_bytes = 8388609", expectedError: "starlark tool max_data_bytes must not exceed 8388608"},
		{name: "result", attribute: "max_result_bytes = 8388609", expectedError: "starlark tool max_result_bytes must not exceed 8388608"},
		{name: "stdout", attribute: "max_stdout_bytes = 65537", expectedError: "starlark tool max_stdout_bytes must not exceed 65536"},
		{name: "memory", attribute: "memory_limit = 268435457", expectedError: "starlark tool memory_limit must not exceed 268435456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configValue := parseToolConfig(t, "starlark_tool \"calculator\" {\n  description = \"Calculator\"\n  "+tt.attribute+"\n}")
			assert.ErrorContains(t, configValue.RunPlan(), tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestExternalToolBlockValidation(t *testing.T) {
	referenceTests := []struct {
		name          string
		source        string
		expectedError string
	}{
		{
			name: "missing description",
			source: `external_tool "tool" {
  program = ["tool"]
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool description is required",
		},
		{
			name: "description has wrong type",
			source: `external_tool "tool" {
  description = 42
  program = ["tool"]
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool description must be a known string",
		},
		{
			name: "empty description",
			source: `external_tool "tool" {
  description = " "
  program = ["tool"]
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool description is required",
		},
		{
			name: "null description",
			source: `external_tool "tool" {
  description = null
  program = ["tool"]
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool description must be a known string",
		},
		{
			name: "missing program",
			source: `external_tool "tool" {
  description = "Tool"
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool program is required",
		},
		{
			name: "program has wrong type",
			source: `external_tool "tool" {
  description = "Tool"
  program = "tool"
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool program must be a list of strings",
		},
		{
			name: "program contains null",
			source: `external_tool "tool" {
  description = "Tool"
  program = [null]
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool program must not contain null elements",
		},
		{
			name: "null program",
			source: `external_tool "tool" {
  description = "Tool"
  program = null
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool program must be a list of strings",
		},
		{
			name: "empty program",
			source: `external_tool "tool" {
  description = "Tool"
  program = []
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool program must contain an executable",
		},
		{
			name: "blank executable",
			source: `external_tool "tool" {
  description = "Tool"
  program = [" "]
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool program must contain an executable",
		},
		{
			name: "working directory has wrong type",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  working_dir = 42
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool working_dir must be a known string",
		},
		{
			name: "null working directory",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  working_dir = null
  input_type = object({})
  output_type = string
}`,
			expectedError: "external tool working_dir must be a known string",
		},
		{
			name: "unknown attribute",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  working_directory = "."
  input_type = object({})
  output_type = string
}`,
			expectedError: "unsupported argument; An argument named \"working_directory\" is not expected here",
		},
		{
			name: "unknown nested block",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  input_type = object({})
  output_type = string
  retry {}
}`,
			expectedError: "unsupported block type; Blocks of type \"retry\" are not expected here",
		},
		{
			name: "missing input type",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  output_type = string
}`,
			expectedError: "external tool input_type is required",
		},
		{
			name: "missing output type",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  input_type = object({})
}`,
			expectedError: "external tool output_type is required",
		},
		{
			name: "invalid input type expression",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  input_type = optional(string)
  output_type = string
}`,
			expectedError: "external tool input_type",
		},
		{
			name: "any input type",
			source: `external_tool "tool" {
  description = "Tool"
  program = ["tool"]
  input_type = any
  output_type = string
}`,
			expectedError: "external tool input_type: type must not contain dynamic values at value",
		},
	}

	for _, tt := range referenceTests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseToolConfig(t, tt.source)
			assert.ErrorContains(t, config.RunPlan(), tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestExternalToolBlockAllowsGoldenMetaConfiguration(t *testing.T) {
	config := parseToolConfig(t, `
external_tool "dependency" {
  description = "Dependency"
  program = ["dependency"]
  input_type = object({})
  output_type = string
}

external_tool "target" {
  description = "Target"
  program = ["target"]
  input_type = object({})
  output_type = string
  depends_on = [external_tool.dependency]

  precondition {
    condition = true
  }
}
`)

	require.NoError(t, config.RunPlan())
	ancestors, err := config.GetAncestors("external_tool.target")
	require.NoError(t, err)
	assert.Contains(t, ancestors, "external_tool.dependency")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestExternalToolPostconditionUsesInputAndOutput(t *testing.T) {
	config := parseToolConfig(t, `
external_tool "target" {
  description = "Target"
  program = ["target"]
  input_type = object({ name = string })
  output_type = object({ saved = bool })

  postcondition {
    condition     = input.name != "" && output.saved
    error_message = "target must save the named value"
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*toolspec.ExternalToolBlock](config)
	require.Len(t, blocks, 1)
	require.Len(t, blocks[0].Postconditions, 1)
	assert.Contains(t, blocks[0].Postconditions[0].Expression, "output.saved")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestExternalToolPostconditionFailsDuringPlanWhenKnownFalse(t *testing.T) {
	config := parseToolConfig(t, `
external_tool "target" {
  description = "Target"
  program = ["target"]
  input_type = object({})
  output_type = object({})

  postcondition {
    condition     = false
    error_message = "known invalid tool contract"
  }
}
`)

	require.ErrorContains(t, config.RunPlan(), "known invalid tool contract")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestGoToolBlockValidation(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		expectedError string
	}{
		{
			name: "valid",
			source: `go_tool "finish" {
  description = "Finish research"
  source = "type Input struct{}"
}`,
		},
		{
			name: "empty description",
			source: `go_tool "finish" {
  description = " "
  source = "type Input struct{}"
}`,
			expectedError: "go tool description is required",
		},
		{
			name: "empty source",
			source: `go_tool "finish" {
  description = "Finish research"
  source = " "
}`,
			expectedError: "go tool source is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseToolConfig(t, tt.source)
			err := config.RunPlan()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestGoToolPostconditionUsesInputAndOutput(t *testing.T) {
	config := parseToolConfig(t, `
go_tool "target" {
  description = "Target"
  source = <<-GO
    import "context"
    type Input struct { Name string `+"`json:\"name\"`"+` }
    type Output struct { Saved bool `+"`json:\"saved\"`"+` }
    func Invoke(context.Context, Input) (ToolResponse[Output], error) { return ToolResponse[Output]{}, nil }
  GO

  postcondition {
    condition     = input.name != "" && output.saved
    error_message = "target must save the named value"
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*toolspec.GoToolBlock](config)
	require.Len(t, blocks, 1)
	require.Len(t, blocks[0].Postconditions, 1)
	assert.Contains(t, blocks[0].Postconditions[0].Expression, "output.saved")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestToolBlocksExposeTypedAddressValues(t *testing.T) {
	configValue := parseToolConfig(t, `
go_tool "finish" {
  description = "Finish research"
  source = "type Input struct{}"
}

external_tool "lookup" {
  description = "Look up data"
  program = ["lookup"]
  input_type = object({ query = string })
  output_type = string
}

starlark_tool "calculator" {
  description = "Calculate values."
}
`)
	require.NoError(t, configValue.RunPlan())

	tests := []struct {
		name         string
		value        cty.Value
		expectedKind config.AddressKind
		expected     string
	}{
		{
			name:         "go tool",
			value:        golden.Blocks[*toolspec.GoToolBlock](configValue)[0].Value(),
			expectedKind: config.AddressKindGo,
			expected:     "go_tool.finish",
		},
		{
			name:         "external tool",
			value:        golden.Blocks[*toolspec.ExternalToolBlock](configValue)[0].Value(),
			expectedKind: config.AddressKindExternal,
			expected:     "external_tool.lookup",
		},
		{
			name:         "starlark tool",
			value:        golden.Blocks[*toolspec.StarlarkToolBlock](configValue)[0].Value(),
			expectedKind: config.AddressKindStarlark,
			expected:     "starlark_tool.calculator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address, ok := config.AddressFromValue(tt.value)
			require.True(t, ok)
			assert.Equal(t, tt.expectedKind, address.Kind)
			assert.Equal(t, tt.expected, address.Value)
		})
	}

	evaluationContext := configValue.EvalContext()
	goAddress, ok := config.AddressFromValue(evaluationContext.Variables["go_tool"].GetAttr("finish"))
	require.True(t, ok)
	assert.Equal(t, "go_tool.finish", goAddress.Value)
	externalAddress, ok := config.AddressFromValue(evaluationContext.Variables["external_tool"].GetAttr("lookup"))
	require.True(t, ok)
	assert.Equal(t, "external_tool.lookup", externalAddress.Value)
	starlarkAddress, ok := config.AddressFromValue(evaluationContext.Variables["starlark_tool"].GetAttr("calculator"))
	require.True(t, ok)
	assert.Equal(t, "starlark_tool.calculator", starlarkAddress.Value)

	goValue := evaluationContext.Variables["go_tool"].GetAttr("finish")
	require.True(t, goValue.Type().HasAttribute("description"))
	require.True(t, goValue.Type().HasAttribute("source"))
	assert.Equal(t, "Finish research", goValue.GetAttr("description").AsString())
	assert.Equal(t, "type Input struct{}", goValue.GetAttr("source").AsString())

	externalValue := evaluationContext.Variables["external_tool"].GetAttr("lookup")
	require.True(t, externalValue.Type().HasAttribute("description"))
	require.True(t, externalValue.Type().HasAttribute("program"))
	require.True(t, externalValue.Type().HasAttribute("working_dir"))
	assert.Equal(t, "Look up data", externalValue.GetAttr("description").AsString())
	assert.True(t, cty.ListVal([]cty.Value{cty.StringVal("lookup")}).RawEquals(externalValue.GetAttr("program")))
	assert.Empty(t, externalValue.GetAttr("working_dir").AsString())

	referenceTests := []struct {
		name           string
		expression     string
		expectedPrefix string
	}{
		{name: "go tool traversal", expression: "tool_name(go_tool.finish)", expectedPrefix: "tool_go_tool_finish_"},
		{name: "external tool traversal", expression: "tool_name(external_tool.lookup)", expectedPrefix: "tool_external_tool_lookup_"},
		{name: "starlark tool traversal", expression: "tool_name(starlark_tool.calculator)", expectedPrefix: "tool_starlark_tool_calculat_"},
	}
	for _, tt := range referenceTests {
		t.Run(tt.name, func(t *testing.T) {
			expression, diagnostics := hclsyntax.ParseExpression(
				[]byte(tt.expression),
				"reference.r42",
				hcl.InitialPos,
			)
			require.False(t, diagnostics.HasErrors(), diagnostics.Error())
			actual, diagnostics := expression.Value(evaluationContext)
			require.False(t, diagnostics.HasErrors(), diagnostics.Error())
			assert.True(t, strings.HasPrefix(actual.AsString(), tt.expectedPrefix))
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestToolBlocksExposeStableIDsFromCanonicalAddresses(t *testing.T) {
	const source = `
go_tool "finish" {
  description = "Finish research"
  source = "type Input struct{}"
}

external_tool "lookup" {
  description = "Look up data"
  program = ["lookup"]
  input_type = object({ query = string })
  output_type = string
}
`
	first := parseToolConfig(t, source)
	require.NoError(t, first.RunPlan())
	second := parseToolConfig(t, source)
	require.NoError(t, second.RunPlan())
	sibling := parseToolConfigWithPrefix(t, "module.sibling", source)
	require.NoError(t, sibling.RunPlan())

	firstGo := golden.Blocks[*toolspec.GoToolBlock](first)[0]
	secondGo := golden.Blocks[*toolspec.GoToolBlock](second)[0]
	siblingGo := golden.Blocks[*toolspec.GoToolBlock](sibling)[0]
	firstExternal := golden.Blocks[*toolspec.ExternalToolBlock](first)[0]
	secondExternal := golden.Blocks[*toolspec.ExternalToolBlock](second)[0]
	siblingExternal := golden.Blocks[*toolspec.ExternalToolBlock](sibling)[0]

	assert.Equal(t, "module.parent.go_tool.finish", firstGo.CanonicalAddress())
	assert.Equal(t, "module.parent.external_tool.lookup", firstExternal.CanonicalAddress())
	assert.Equal(t, "module.sibling.go_tool.finish", siblingGo.CanonicalAddress())
	assert.Equal(t, "module.sibling.external_tool.lookup", siblingExternal.CanonicalAddress())
	assert.Regexp(t, `^tool_go_tool_finish_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, firstGo.Id())
	assert.Regexp(t, `^tool_external_tool_lookup_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, firstExternal.Id())
	assert.Equal(t, firstGo.Id(), secondGo.Id())
	assert.Equal(t, firstExternal.Id(), secondExternal.Id())
	assert.NotEqual(t, firstGo.Id(), firstExternal.Id())
	assert.NotEqual(t, firstGo.Id(), siblingGo.Id())
	assert.NotEqual(t, firstExternal.Id(), siblingExternal.Id())
	assert.Equal(t, firstGo.Id(), firstGo.BaseValues()["id"].AsString())
	assert.Equal(t, firstExternal.Id(), firstExternal.BaseValues()["id"].AsString())
	goValue := first.EvalContext().Variables["go_tool"].GetAttr("finish")
	externalValue := first.EvalContext().Variables["external_tool"].GetAttr("lookup")
	require.True(t, goValue.Type().HasAttribute("id"))
	require.True(t, externalValue.Type().HasAttribute("id"))
	assert.Equal(t, firstGo.Id(), goValue.GetAttr("id").AsString())
	assert.Equal(t, firstExternal.Id(), externalValue.GetAttr("id").AsString())
}

func parseConstraint(t *testing.T, source string) toolspec.Constraint {
	t.Helper()

	expression, diagnostics := hclsyntax.ParseExpression([]byte(source), "type.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	typeValue, defaults, diagnostics := typeexpr.TypeConstraintWithDefaults(expression)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	return toolspec.NewConstraintWithDefaults(typeValue, defaults)
}

func parseToolConfig(t *testing.T, source string) *toolTestConfig {
	t.Helper()
	return parseToolConfigWithPrefix(t, "module.parent", source)
}

func parseToolConfigWithPrefix(t *testing.T, canonicalPrefix, source string) *toolTestConfig {
	t.Helper()

	registerToolBlocks.Do(func() {
		golden.RegisterBlock(new(toolspec.GoToolBlock))
		golden.RegisterBlock(new(toolspec.ExternalToolBlock))
		golden.RegisterBlock(new(toolspec.StarlarkToolBlock))
	})

	syntaxFile, diagnostics := hclsyntax.ParseConfig([]byte(source), "tool.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	writeFile, diagnostics := hclwrite.ParseConfig([]byte(source), "tool.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := syntaxFile.Body.(*hclsyntax.Body)
	require.True(t, ok)

	config := &toolTestConfig{
		BaseConfig:      config.NewBaseConfig(golden.NewBaseConfigArgs{}),
		canonicalPrefix: canonicalPrefix,
	}
	err := golden.InitConfig(config, golden.AsHclBlocks(body.Blocks, writeFile.Body().Blocks()))
	require.NoError(t, err)
	return config
}
