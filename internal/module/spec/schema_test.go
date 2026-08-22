package spec_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/executor"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	internalplan "github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	runpkg "github.com/lonegunmanb/r42/internal/run"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

var registerModuleBlocks sync.Once

type planningFixtureBlock struct {
	*golden.BaseBlock
	ValueText string `hcl:"value"`
}

func (*planningFixtureBlock) Type() string            { return "" }
func (*planningFixtureBlock) BlockType() string       { return "planning_fixture" }
func (*planningFixtureBlock) AddressLength() int      { return 2 }
func (*planningFixtureBlock) CanExecutePrePlan() bool { return false }
func (b *planningFixtureBlock) ExecuteDuringPlan() error {
	if b.ValueText == "fail" {
		return fmt.Errorf("fixture plan failed")
	}
	return nil
}

func (b *planningFixtureBlock) Value() cty.Value {
	if b.ValueText == "unknown" {
		return cty.UnknownVal(cty.String)
	}
	return cty.StringVal(b.ValueText)
}

func registerSchemas() {
	registerModuleBlocks.Do(func() {
		golden.RegisterBlock(new(modulespec.ModuleBlock))
		golden.RegisterBlock(new(modulespec.OutputBlock))
		golden.RegisterBlock(new(planningFixtureBlock))
	})
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigPlansModuleInputsAndOutputs(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "modules", "sector")
	require.NoError(t, os.MkdirAll(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
variable "topic" {
  type = string
}

planning_fixture "child" {
  value = "planned"
}

output "report" {
  description = "Child report"
  value       = "report for ${var.topic}"
  sensitive   = true
}
`)
	writeR42(t, root, "main.r42.hcl", `
module "sector" {
  source      = "./modules/sector"
  parallelism = 4
  timeout     = " 1h "
  topic       = "energy"
}

output "sector_report" {
  value = module.sector.report
}
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)

	modulePlan, ok := plan.Modules["sector"]
	require.True(t, ok)
	assert.Equal(t, child, modulePlan.Directory)
	assert.Equal(t, 4, modulePlan.Parallelism)
	assert.Equal(t, "1h0m0s", modulePlan.Timeout.String())
	require.Contains(t, modulePlan.Outputs, "report")
	assert.True(t, modulePlan.Outputs["report"].Type.Equals(cty.String))
	assert.Equal(t, "Child report", modulePlan.Outputs["report"].Description)
	assert.True(t, modulePlan.Outputs["report"].Sensitive)
	childOutputValue, _ := modulePlan.Outputs["report"].Value.Unmark()
	assert.Equal(t, "report for energy", childOutputValue.AsString())

	rootOutput := plan.Outputs["sector_report"]
	assert.True(t, rootOutput.Sensitive)
	assert.True(t, rootOutput.Value.IsMarked())
	rootOutputValue, _ := rootOutput.Value.Unmark()
	assert.Equal(t, "report for energy", rootOutputValue.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigPlansModulesFromInitializedCanonicalDirectory(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	installed := filepath.Join(modules, "sector")
	original := filepath.Join(root, "original")
	require.NoError(t, os.MkdirAll(installed, 0o700))
	require.NoError(t, os.MkdirAll(original, 0o700))
	writeR42(t, installed, "main.r42.hcl", `output "origin" { value = "initialized" }`)
	writeR42(t, original, "main.r42.hcl", `output "origin" { value = "source" }`)
	writeR42(t, root, "main.r42.hcl", `
module "sector" { source = "./original" }
output "origin" { value = module.sector.origin }
`)

	planned, err := planSource(root, executor.ResearchConfigOptions{ModuleDirectory: modules})

	require.NoError(t, err)
	assert.Equal(t, installed, planned.Modules["sector"].Directory)
	assert.Equal(t, "initialized", planned.Outputs["origin"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigUsesNestedInitializedDirectoryAsPathModule(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	child := filepath.Join(modules, "a")
	grandchild := filepath.Join(child, "b")
	require.NoError(t, os.MkdirAll(grandchild, 0o700))
	writeR42(t, grandchild, "main.r42.hcl", `output "module_path" { value = path.module }`)
	writeR42(t, child, "main.r42.hcl", `
module "b" { source = "unused" }
output "nested_path" { value = module.b.module_path }
`)
	writeR42(t, root, "main.r42.hcl", `
module "a" { source = "unused" }
output "nested_path" { value = module.a.nested_path }
`)

	planned, err := planSource(root, executor.ResearchConfigOptions{ModuleDirectory: modules})

	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(grandchild), planned.Outputs["nested_path"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRequiresInitializedModuleWhenModuleDirectoryIsConfigured(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	original := filepath.Join(root, "original")
	require.NoError(t, os.Mkdir(original, 0o700))
	writeR42(t, original, "main.r42.hcl", `output "answer" { value = "42" }`)
	writeR42(t, root, "main.r42.hcl", `module "child" { source = "./original" }`)

	_, err := planSource(root, executor.ResearchConfigOptions{
		ModuleDirectory: filepath.Join(t.TempDir(), "modules"),
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "module module.child is not initialized; run r42 init")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigUsesVariableDefault(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
variable "topic" {
  type    = string
  default = "energy"
}
output "topic" { value = var.topic }
`)
	writeR42(t, root, "main.r42.hcl", `
module "child" { source = "./child" }
output "topic" { value = module.child.topic }
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	assert.Equal(t, "energy", plan.Outputs["topic"].Value.AsString())
}

func TestResearchConfigDelegatesRootVariablesToGolden(t *testing.T) {
	registerSchemas()
	t.Setenv("R42_VAR_topic", "from-env")
	root := t.TempDir()
	writeR42(t, root, "main.r42.hcl", `
variable "topic" {
  type = string
}
output "topic" { value = var.topic }
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	assert.Equal(t, "from-env", plan.Outputs["topic"].Value.AsString())
}

func TestChildVariableDefaultIsIsolatedFromGoldenRootSources(t *testing.T) {
	registerSchemas()
	t.Setenv("R42_VAR_topic", "from-env")
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
variable "topic" {
  type    = string
  default = "child-default"
}
output "topic" { value = var.topic }
`)
	writeR42(t, root, "main.r42.hcl", `
module "child" { source = "./child" }
output "topic" { value = module.child.topic }
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	assert.Equal(t, "child-default", plan.Outputs["topic"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry and variable files are process-global.
func TestChildVariablesIgnoreChildVarFilesWhileRootVarFilesRemainActive(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, root, "r42.r42vars", `topic = "from-root-var-file"`)
	writeR42(t, root, "main.r42.hcl", `
variable "topic" { type = string }
module "child" {
  source = "./child"
  topic  = var.topic
}
output "topic" { value = module.child.topic }
`)
	writeR42(t, child, "r42.r42vars", `topic =`)
	writeR42(t, child, "main.r42.hcl", `
variable "topic" { type = string }
output "topic" { value = var.topic }
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	assert.Equal(t, "from-root-var-file", plan.Outputs["topic"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry and environment are process-global.
func TestVariableSensitivePropagatesAcrossModuleBoundary(t *testing.T) {
	registerSchemas()
	tests := []struct {
		name       string
		variable   string
		moduleBody string
	}{
		{
			name: "caller value",
			variable: `variable "secret" {
  type      = string
  sensitive = true
}`,
			moduleBody: `module "child" {
  source = "./child"
  secret = "caller-secret"
}`,
		},
		{
			name: "default value",
			variable: `variable "secret" {
  type      = string
  default   = "default-secret"
  sensitive = true
}`,
			moduleBody: `module "child" {
  source = "./child"
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			child := filepath.Join(root, "child")
			require.NoError(t, os.Mkdir(child, 0o755))
			writeR42(t, child, "main.r42.hcl", tt.variable+`
output "secret" { value = var.secret }
`)
			writeR42(t, root, "main.r42.hcl", tt.moduleBody+`
output "secret" { value = module.child.secret }
`)

			plan, err := planSource(root, executor.ResearchConfigOptions{})
			require.NoError(t, err)
			assert.True(t, corespec.IsSensitive(plan.Outputs["secret"].Value))
			assert.True(t, plan.Outputs["secret"].Sensitive)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSensitiveCallerIsUnmarkedForChildConversionAndValidation(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
variable "secret" {
  type = string
  validation {
    condition     = var.secret == "42"
    error_message = "secret must be converted"
  }
}
output "secret" { value = var.secret }
`)
	writeR42(t, root, "main.r42.hcl", `
variable "secret" {
  type      = number
  default   = 42
  sensitive = true
}
module "child" {
  source = "./child"
  secret = var.secret
}
output "secret" { value = module.child.secret }
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	output := plan.Outputs["secret"]
	assert.True(t, corespec.IsSensitive(output.Value))
	value, _ := output.Value.Unmark()
	assert.Equal(t, "42", value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigAcceptsAbsoluteModuleSource(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := t.TempDir()
	writeR42(t, child, "main.r42.hcl", `output "value" { value = "absolute" }`)
	writeR42(t, root, "main.r42.hcl", fmt.Sprintf(`
module "child" { source = %q }
output "value" { value = module.child.value }
`, filepath.ToSlash(child)))

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	assert.Equal(t, "absolute", plan.Outputs["value"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRejectsInvalidModuleBoundaries(t *testing.T) {
	registerSchemas()
	tests := []struct {
		name          string
		child         string
		root          string
		expectedError string
	}{
		{
			name:          "source directory does not exist",
			root:          `module "child" { source = "./missing" }`,
			expectedError: "reading module directory",
		},
		{
			name: "variable type is required",
			child: `variable "topic" {
  default = "energy"
}`,
			root:          `module "child" { source = "./child" }`,
			expectedError: `variable "topic" must declare type`,
		},
		{
			name:          "required variable is missing",
			child:         `variable "topic" { type = string }`,
			root:          `module "child" { source = "./child" }`,
			expectedError: `module input "topic" is required`,
		},
		{
			name: "variable sensitive is a bool",
			child: `variable "topic" {
  type      = string
  default   = "energy"
  sensitive = "true"
}`,
			root:          `module "child" { source = "./child" }`,
			expectedError: `variable "topic" sensitive must be a known bool`,
		},
		{
			name:  "undeclared input is rejected",
			child: `output "constant" { value = "ok" }`,
			root: `module "child" {
  source = "./child"
  topic  = "energy"
}`,
			expectedError: `module input "topic" is not declared`,
		},
		{
			name:          "child plan must complete",
			child:         `planning_fixture "child" { value = "fail" }`,
			root:          `module "child" { source = "./child" }`,
			expectedError: "fixture plan failed",
		},
		{
			name:          "output value is required",
			child:         `output "result" {}`,
			root:          `module "child" { source = "./child" }`,
			expectedError: "Missing required argument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.child != "" {
				child := filepath.Join(root, "child")
				require.NoError(t, os.Mkdir(child, 0o755))
				writeR42(t, child, "main.r42.hcl", tt.child)
			}
			writeR42(t, root, "main.r42.hcl", tt.root)

			_, err := planSource(root, executor.ResearchConfigOptions{})
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigAllowsKnownAfterApplyOutput(t *testing.T) {
	registerSchemas()
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
planning_fixture "unknown" { value = "unknown" }
output "result" { value = planning_fixture.unknown }
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{})

	require.NoError(t, err)
	output := planned.Outputs["result"]
	assert.True(t, output.Type.Equals(cty.String))
	assert.False(t, output.Value.IsKnown())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigBuildsSavedResearchDAG(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(toolspec.GoToolBlock))
	golden.RegisterBlock(new(toolspec.ExternalToolBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
research "static" "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."

  artifact "report" {
    type = "file"
    path = "${cwd()}/report.md"
  }
}

research "static" "summary" {
  model         = "test-model"
  system_prompt = "Summarize evidence."
  prompt        = one(research.static.source.artifact).path
}

output "report_path" {
  value = one(research.static.source.artifact).path
}
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{
		Context: context.Background(),
	})

	require.NoError(t, err)
	require.NotNil(t, planned.Saved)
	nodes := planned.Saved.Nodes()
	require.Len(t, nodes, 2)
	assert.Equal(t, "research.static.source", nodes[0].Address)
	assert.Equal(t, "research", nodes[0].Kind)
	assert.Empty(t, nodes[0].Dependencies)
	assert.Equal(t, "test-model", nodes[0].Config.GetAttr("model").AsString())
	assert.Equal(t, "research.static.summary", nodes[1].Address)
	assert.Equal(t, []string{"research.static.source"}, nodes[1].Dependencies)
	assert.Equal(t,
		"one(research.static.source.artifact).path",
		planned.Saved.Outputs()["report_path"].Expression,
	)
	assert.Contains(t, planned.Saved.Context(), "research")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigEmbedsSavedChildPlan(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
go_tool "inside" {
  description = "Work inside the module"
  source      = "type Input struct{}"
}

research "static" "inside" {
  model         = "test-model"
  system_prompt = "Work inside the module."
  tool_ids      = [go_tool.inside.id]
}
`)
	writeR42(t, root, "main.r42.hcl", `
module "child" {
  source      = "./child"
  parallelism = 2
}
`)

	planned, err := planSource(root, executor.ResearchConfigOptions{})

	require.NoError(t, err)
	require.NotNil(t, planned.Saved)
	nodes := planned.Saved.Nodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, "module.child", nodes[0].Address)
	require.NotNil(t, nodes[0].Module)
	assert.Equal(t, 2, nodes[0].Module.Parallelism)
	assert.Equal(t, "research.static.inside", nodes[0].Module.Plan.Nodes()[0].Address)
	childRegistry := nodes[0].Module.Plan.Tools()
	require.Len(t, childRegistry, 1)
	for _, definition := range childRegistry {
		assert.Equal(t, "module.child.go_tool.inside", definition.Address)
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigImportsOnlyChildToolsExportedAsOutputs(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
external_tool "exported" {
  description = "Exported child tool"
  program     = ["exported-helper"]
  input_type  = object({ query = string })
  output_type = string
}

external_tool "private" {
  description = "Private child tool"
  program     = ["private-helper"]
  input_type  = object({ query = string })
  output_type = string
}

research "static" "pending" {
  model             = "test-model"
  system_prompt     = "Produce an apply-time result."
  terminate_tool_id = external_tool.private.id
}

output "exported_tool_id" {
  value = external_tool.exported.id
}

output "plain_string" {
  value = "not a tool id"
}

output "metadata" {
  value = { private_id = external_tool.private.id }
}

output "nothing" {
  value = tostring(null)
}

output "pending_result" {
  value = research.static.pending.result
}
`)
	writeR42(t, root, "main.r42.hcl", `
module "tools" {
  source = "./child"
}

research "static" "consumer" {
  model         = "test-model"
  system_prompt = "Use the exported tool."
  tool_ids      = [module.tools.exported_tool_id]
}
`)

	planned, err := planSource(root, executor.ResearchConfigOptions{})

	require.NoError(t, err)
	require.NotNil(t, planned.Saved)
	nodes := planned.Saved.Nodes()
	require.Len(t, nodes, 2)
	var consumer internalplan.NodeSpec
	for _, node := range nodes {
		if node.Address == "research.static.consumer" {
			consumer = node
		}
	}
	require.Equal(t, "research.static.consumer", consumer.Address)
	decoded, err := modulespec.DecodeResearchPlan(consumer.Config)
	require.NoError(t, err)
	require.Len(t, decoded.Config.Policy.ToolIDs, 1)
	exportedID := decoded.Config.Policy.ToolIDs[0]
	registry := planned.Saved.Tools()
	require.Len(t, registry, 1)
	require.Contains(t, registry, exportedID)
	assert.Equal(t, "module.tools.external_tool.exported", registry[exportedID].Address)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRejectsUnknownTypedToolIDsDuringPlan(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	const unknownID = "tool_go_tool_missing_12345678-1234-8234-9234-123456789abc"
	tests := []struct {
		name     string
		fragment string
	}{
		{name: "tool ids", fragment: `tool_ids = ["` + unknownID + `"]`},
		{name: "collection tool ids", fragment: `collection_tool_ids = ["` + unknownID + `"]`},
		{name: "terminate tool id", fragment: `terminate_tool_id = "` + unknownID + `"`},
		{name: "allowed tools", fragment: `allowed_tools = ["` + unknownID + `"]`},
		{name: "disallowed tools", fragment: `disallowed_tools = ["` + unknownID + `"]`},
		{name: "tool call quota", fragment: `tool_ids = ["` + unknownID + `"]
  tool_call_quota = { "` + unknownID + `" = 1 }`},
		{
			name: "qc tool ids",
			fragment: `qc {
    criteria = { accuracy = "Check accuracy." }
    tool_ids = ["` + unknownID + `"]
  }`,
		},
		{
			name: "qc allowed tools",
			fragment: `qc {
    criteria = { accuracy = "Check accuracy." }
    allowed_tools = ["` + unknownID + `"]
  }`,
		},
		{
			name: "qc typed tool call quota",
			fragment: `qc {
    criteria = { accuracy = "Check accuracy." }
    tool_ids = ["` + unknownID + `"]
    tool_call_quota = { "` + unknownID + `" = 1 }
  }`,
		},
		{
			name: "qc disallowed tools",
			fragment: `qc {
    criteria = { accuracy = "Check accuracy." }
    disallowed_tools = ["ask_user", "` + unknownID + `"]
  }`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			writeR42(t, directory, "main.r42.hcl", `
research "static" "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."
			  `+tt.fragment+`
}
`)

			_, err := planSource(directory, executor.ResearchConfigOptions{})

			assert.ErrorContains(t, err, `references tool id "`+unknownID+`" that was not planned`)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRejectsQuotaForToolOutsideSession(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(toolspec.GoToolBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	tests := []struct {
		name     string
		fragment string
		scope    string
	}{
		{
			name:     "research",
			fragment: `tool_call_quota = { (go_tool.lookup.id) = 1 }`,
			scope:    "research",
		},
		{
			name: "qc",
			fragment: `qc {
    criteria = { accuracy = "Check accuracy." }
    tool_call_quota = { (go_tool.lookup.id) = 1 }
  }`,
			scope: "qc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			writeR42(t, directory, "main.r42.hcl", `
go_tool "lookup" {
  description = "Look up evidence"
  source = <<-GO
    import "context"
    type Input struct{}
    type Output string
    func Invoke(context.Context, Input) (ToolResponse[Output], error) {
      return ToolResponse[Output]{Accepted: true}, nil
    }
  GO
}
research "static" "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."
  `+tt.fragment+`
}
`)

			_, err := planSource(directory, executor.ResearchConfigOptions{})

			require.ErrorContains(t, err, tt.scope+" tool_call_quota references tool id")
			assert.ErrorContains(t, err, "that is not configured for this session")
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedResearchConfigCanBeReconstructed(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(toolspec.GoToolBlock))
	golden.RegisterBlock(new(toolspec.ExternalToolBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
model_provider "primary" {
  type        = "openai"
  endpoint    = "https://example.test"
  api_key     = "secret"
  wire_api    = "responses"
  transport   = "http"
  headers     = { "x-project" = "r42" }
}

external_tool "lookup" {
  description = "Look up evidence"
  program     = ["lookup", "--json"]
  working_dir = "data"
  input_type  = object({ query = string, limit = optional(number, 5) })
  output_type = object({ answer = string })
}

go_tool "finish" {
  description = "Finish research"
  source = <<-GO
    import "context"
    type Input struct { Summary string `+"`json:\"summary\"`"+` }
    type Output string
    func Invoke(context.Context, Input) (ToolResponse[Output], error) {
      return ToolResponse[Output]{Accepted: true}, nil
    }
  GO
}

research "static" "market" {
  model_provider       = model_provider.primary
  model                = "test-model"
  profile              = "gpt-5.4"
  reasoning_effort     = "high"
  system_prompt        = "Research carefully."
  prompt               = "Start now."
  tool_ids             = [external_tool.lookup.id]
  terminate_tool_id    = go_tool.finish.id
  tool_call_quota = {
    (external_tool.lookup.id) = 4
    (go_tool.finish.id)       = 1
    web_fetch                 = 6
  }
  allowed_tools        = [tool_name(external_tool.lookup)]
  disallowed_tools     = ["ask_user"]
  skill_directories    = ["."]
  skills               = ["evidence"]
  disabled_skills      = ["unsafe"]
  max_protocol_attempts = 7
  timeout              = "1m"

  artifact "report" {
    type      = "file"
    path      = "report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria     = { accuracy = "Must be accurate" }
    model        = "qc-model"
    max_qc_rounds = 3
    tool_ids = [external_tool.lookup.id]
    tool_call_quota = {
      (external_tool.lookup.id) = 2
      web_search                = 3
    }
  }
}
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	node := planned.Saved.Nodes()[0]
	assert.True(t, corespec.IsSensitive(node.Config))

	reconstructed, err := modulespec.DecodeResearchPlan(node.Config)
	require.NoError(t, err)
	assert.Equal(t, "test-model", reconstructed.Config.Model)
	assert.Equal(t, "gpt-5.4", reconstructed.Config.Profile)
	assert.Equal(t, "high", *reconstructed.Config.ReasoningEffort)
	assert.Equal(t, "Start now.", *reconstructed.Config.Prompt)
	assert.Equal(t, 7, reconstructed.Config.MaxProtocolAttempts)
	assert.Equal(t, time.Minute, *reconstructed.Config.Timeout)
	require.NotNil(t, reconstructed.Provider)
	assert.Equal(t, "https://example.test", reconstructed.Provider.Endpoint)
	assert.Equal(t, "secret", *reconstructed.Provider.APIKey)
	registry := planned.Saved.Tools()
	require.Len(t, registry, 2)
	require.Contains(t, registry, reconstructed.Config.Policy.ToolIDs[0])
	assert.Equal(t, "external_tool.lookup", registry[reconstructed.Config.Policy.ToolIDs[0]].Address)
	assert.Equal(t, map[string]int{
		reconstructed.Config.Policy.ToolIDs[0]: 4,
		*reconstructed.Config.TerminateToolID:  1,
		"web_fetch":                            6,
	}, reconstructed.Config.Policy.ToolCallQuota)
	assert.Contains(t, registry[reconstructed.Config.Policy.ToolIDs[0]].InputTypeExpression, "optional(number, 5)")
	require.NotNil(t, reconstructed.Config.TerminateToolID)
	require.Contains(t, registry, *reconstructed.Config.TerminateToolID)
	assert.Equal(t, "go_tool.finish", registry[*reconstructed.Config.TerminateToolID].Address)
	require.NotNil(t, reconstructed.Config.QC)
	assert.Equal(t, "qc-model", *reconstructed.Config.QC.Model)
	assert.Equal(t, 3, reconstructed.Config.QC.MaxRounds)
	assert.Equal(t, map[string]int{
		reconstructed.Config.QC.ToolIDs[0]: 2,
		"web_search":                       3,
	}, reconstructed.Config.QC.ToolCallQuota)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedResearchConfigRestoresCollectionFields(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
model_provider "collection" {
  type     = "openai"
  endpoint = "https://collection.example.test"
}

external_tool "collect" {
  description = "Collect sources"
  program     = ["collect", "--json"]
  input_type  = object({ query = string })
  output_type = string
}

research "static" "market" {
  model         = "test-model"
  system_prompt = "Collect and synthesize."

  collection_model_provider = model_provider.collection
  collection_tool_ids = [external_tool.collect.id]
  collection_skill_directories = ["skills/collection"]
  collection_skills            = ["source-evaluation"]
  collection_disabled_skills   = ["dangerous"]
  collection_batch_size        = 5
  max_collection_rounds        = 3

  collection_qc {
    criteria         = { coverage = "Cover the task." }
    model            = "qc-model"
    reasoning_effort = "high"
    permission       = "approve_all"
  }
}
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	node := planned.Saved.Nodes()[0]

	reconstructed, err := modulespec.DecodeResearchPlan(node.Config)
	require.NoError(t, err)
	require.NotNil(t, reconstructed.CollectionProvider)
	assert.Equal(t, "https://collection.example.test", reconstructed.CollectionProvider.Endpoint)
	assert.Equal(t, []string(nil), reconstructed.Config.Policy.ToolIDs)
	require.Len(t, reconstructed.Config.CollectionToolIDs, 1)
	registry := planned.Saved.Tools()
	require.Contains(t, registry, reconstructed.Config.CollectionToolIDs[0])
	assert.Equal(t, "external_tool.collect", registry[reconstructed.Config.CollectionToolIDs[0]].Address)
	assert.Equal(t, []string{"skills/collection"}, reconstructed.Config.CollectionSkillDirectories)
	assert.Equal(t, []string{"source-evaluation"}, reconstructed.Config.CollectionSkills)
	assert.Equal(t, []string{"dangerous"}, reconstructed.Config.CollectionDisabledSkills)
	assert.Equal(t, 5, reconstructed.Config.CollectionBatchSize)
	require.NotNil(t, reconstructed.Config.MaxCollectionRounds)
	assert.Equal(t, 3, *reconstructed.Config.MaxCollectionRounds)
	require.NotNil(t, reconstructed.Config.CollectionQC)
	assert.Equal(t, "qc-model", *reconstructed.Config.CollectionQC.Model)
	assert.Equal(t, "high", *reconstructed.Config.CollectionQC.ReasoningEffort)
	require.Equal(t, researchspec.PermissionApproveAll, *reconstructed.Config.CollectionQC.Permission)
	assert.Equal(t, "Cover the task.", reconstructed.Config.CollectionQC.Criteria.Index(cty.StringVal("coverage")).AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedResearchConfigRestoresCollectionDefaultsForLegacySnapshots(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
research "static" "market" {
  model         = "test-model"
  system_prompt = "Collect and synthesize."
}
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	reconstructed, err := modulespec.DecodeResearchPlan(planned.Saved.Nodes()[0].Config)

	require.NoError(t, err)
	assert.Equal(t, researchspec.DefaultCollectionBatchSize, reconstructed.Config.CollectionBatchSize)
	assert.Nil(t, reconstructed.Config.MaxCollectionRounds)
	assert.Nil(t, reconstructed.Config.CollectionQC)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedDynamicResearchPlanRoundTripsExpressionAndSerial(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	golden.RegisterBlock(new(researchspec.DynamicResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
external_tool "collect" {
  description = "Collect sources"
  program     = ["collect", "--json"]
  input_type  = object({ query = string })
  output_type = string
}

research "dynamic" "portfolio" {
  tasks = [
    {
      model         = "test-model"
      system_prompt = "Collect and synthesize."
      collection_tool_ids = [external_tool.collect.id]
      collection_batch_size = 5
      max_collection_rounds = 3
      collection_qc = {
        criteria = { coverage = "Cover the task." }
      }
    },
  ]
}
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	node := planned.Saved.Nodes()[0]

	dynamic, err := modulespec.DecodeDynamicResearchPlan(node.Config)
	require.NoError(t, err)
	assert.NotEmpty(t, dynamic.Expression)
	assert.False(t, dynamic.Serial)
}

func TestDynamicResearchPlanResolvePropagatesCollectionQCProvider(t *testing.T) {
	t.Parallel()

	planned, err := modulespec.DecodeDynamicResearchPlan(cty.ObjectVal(map[string]cty.Value{
		"payload": cty.StringVal(`{"expression":"[1]","providers":{"model_provider.qc":{"type":"openai","endpoint":"https://example.test"}}}`),
	}))
	require.NoError(t, err)

	resolved, err := planned.Resolve(researchspec.Config{
		Model: "model", SystemPrompt: "prompt",
		CollectionQC: &researchspec.CollectionQCConfig{
			ModelProvider: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("model_provider.qc"),
				"kind":    cty.StringVal("provider"),
			}),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resolved.CollectionQCProvider)
	assert.Equal(t, "https://example.test", resolved.CollectionQCProvider.Endpoint)
}

func TestDynamicResearchPlanResolvePropagatesCollectionProvider(t *testing.T) {
	t.Parallel()

	planned, err := modulespec.DecodeDynamicResearchPlan(cty.ObjectVal(map[string]cty.Value{
		"payload": cty.StringVal(`{"expression":"[1]","providers":{"model_provider.collection":{"type":"openai","endpoint":"https://collection.example.test"}}}`),
	}))
	require.NoError(t, err)

	resolved, err := planned.Resolve(researchspec.Config{
		Model: "model", SystemPrompt: "prompt",
		CollectionModelProvider: cty.ObjectVal(map[string]cty.Value{
			"address": cty.StringVal("model_provider.collection"),
			"kind":    cty.StringVal("provider"),
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, resolved.CollectionProvider)
	assert.Equal(t, "https://collection.example.test", resolved.CollectionProvider.Endpoint)
}

func TestDynamicResearchPlanResolveRejectsUnplannedCollectionProvider(t *testing.T) {
	t.Parallel()

	planned, err := modulespec.DecodeDynamicResearchPlan(cty.ObjectVal(map[string]cty.Value{
		"payload": cty.StringVal(`{"expression":"[1]"}`),
	}))
	require.NoError(t, err)

	_, err = planned.Resolve(researchspec.Config{
		Model: "model", SystemPrompt: "prompt",
		CollectionModelProvider: cty.ObjectVal(map[string]cty.Value{
			"address": cty.StringVal("model_provider.missing"),
			"kind":    cty.StringVal("provider"),
		}),
	})
	require.ErrorContains(t, err, "collection model_provider")
	require.ErrorContains(t, err, "was not planned")
}

func TestDynamicResearchPlanResolveRejectsUnplannedCollectionQCProvider(t *testing.T) {
	t.Parallel()

	planned, err := modulespec.DecodeDynamicResearchPlan(cty.ObjectVal(map[string]cty.Value{
		"payload": cty.StringVal(`{"expression":"[1]"}`),
	}))
	require.NoError(t, err)

	_, err = planned.Resolve(researchspec.Config{
		Model: "model", SystemPrompt: "prompt",
		CollectionQC: &researchspec.CollectionQCConfig{
			ModelProvider: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("model_provider.missing"),
				"kind":    cty.StringVal("provider"),
			}),
		},
	})
	require.ErrorContains(t, err, "collection qc model_provider")
	require.ErrorContains(t, err, "was not planned")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedResearchConfigRestoresExplicitEmptyHeaders(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42.hcl", `
model_provider "primary" {
  type     = "openai"
  endpoint = "https://example.test"
  headers  = {}
}

research "static" "market" {
  model_provider = model_provider.primary
  model          = "test-model"
  system_prompt  = "Research carefully."
}
`)

	planned, err := planSource(directory, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	reconstructed, err := modulespec.DecodeResearchPlan(planned.Saved.Nodes()[0].Config)

	require.NoError(t, err)
	require.NotNil(t, reconstructed.Provider)
	assert.True(t, reconstructed.Provider.Headers.RawEquals(cty.MapValEmpty(cty.String)))
}

func TestDecodeResearchPlanDefaultsLegacyProfileToModel(t *testing.T) {
	t.Parallel()

	planned, err := modulespec.DecodeResearchPlan(cty.ObjectVal(map[string]cty.Value{
		"payload": cty.StringVal(`{"model":"legacy-model","system_prompt":"Research."}`),
	}))

	require.NoError(t, err)
	assert.Equal(t, "legacy-model", planned.Config.Profile)
	assert.Equal(t, researchspec.DefaultCollectionBatchSize, planned.Config.CollectionBatchSize)
	assert.Nil(t, planned.Config.MaxCollectionRounds)
	assert.Nil(t, planned.Config.CollectionQC)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRejectsInvalidModuleSchema(t *testing.T) {
	registerSchemas()
	tests := []struct {
		name          string
		moduleBody    string
		expectedError string
	}{
		{
			name:          "source is required",
			moduleBody:    "",
			expectedError: "module source is required",
		},
		{
			name:          "source is a string",
			moduleBody:    "source = 42",
			expectedError: "module source must be a known string",
		},
		{
			name:          "parallelism is positive",
			moduleBody:    "source = \"./child\"\nparallelism = 0",
			expectedError: "module parallelism must be a positive integer",
		},
		{
			name:          "parallelism is integral",
			moduleBody:    "source = \"./child\"\nparallelism = 1.5",
			expectedError: "module parallelism must be a positive integer",
		},
		{
			name:          "timeout is a duration",
			moduleBody:    "source = \"./child\"\ntimeout = \"forever\"",
			expectedError: "module timeout must be a positive duration",
		},
		{
			name:          "nested blocks are closed",
			moduleBody:    "source = \"./child\"\nretry {}",
			expectedError: `Blocks of type "retry" are not expected here`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			child := filepath.Join(root, "child")
			require.NoError(t, os.Mkdir(child, 0o755))
			writeR42(t, child, "main.r42.hcl", `output "value" { value = "ok" }`)
			writeR42(t, root, "main.r42.hcl", "module \"child\" {\n"+tt.moduleBody+"\n}")

			_, err := planSource(root, executor.ResearchConfigOptions{})
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRejectsInvalidOutputPrimitiveTypes(t *testing.T) {
	registerSchemas()
	tests := []struct {
		name          string
		attribute     string
		expectedError string
	}{
		{name: "description", attribute: "description = 42", expectedError: "output description must be a known string"},
		{name: "sensitive", attribute: `sensitive = "true"`, expectedError: "output sensitive must be a known bool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeR42(t, root, "main.r42.hcl", "output \"value\" {\nvalue = \"ok\"\n"+tt.attribute+"\n}")

			_, err := planSource(root, executor.ResearchConfigOptions{})
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigDetectsOnlyActiveDirectoryCycles(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	require.NoError(t, os.Mkdir(shared, 0o755))
	writeR42(t, shared, "main.r42.hcl", `output "value" { value = "shared" }`)
	writeR42(t, root, "main.r42.hcl", `
module "first" { source = "./shared" }
module "second" { source = "./shared" }
output "first" { value = module.first.value }
output "second" { value = module.second.value }
`)

	plan, err := planSource(root, executor.ResearchConfigOptions{})
	require.NoError(t, err)
	assert.Equal(t, "shared", plan.Outputs["first"].Value.AsString())
	assert.Equal(t, "shared", plan.Outputs["second"].Value.AsString())

	cycleRoot := t.TempDir()
	child := filepath.Join(cycleRoot, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, cycleRoot, "main.r42.hcl", `module "child" { source = "./child" }`)
	writeR42(t, child, "main.r42.hcl", `module "root" { source = ".." }`)

	_, err = planSource(cycleRoot, executor.ResearchConfigOptions{})
	require.Error(t, err)
	require.ErrorContains(t, err, "module directory cycle")
	assert.ErrorContains(t, err, filepath.Clean(cycleRoot))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestModuleValueExposesOutputsButNotChildBlocks(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42.hcl", `
planning_fixture "internal" { value = "hidden" }
output "visible" { value = "shown" }
`)
	writeR42(t, root, "main.r42.hcl", `
module "child" { source = "./child" }
output "leak" { value = module.child.planning_fixture.internal.value }
`)

	_, err := planSource(root, executor.ResearchConfigOptions{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "Unsupported attribute")
}

func writeR42(t *testing.T, directory, name, source string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source+"\n"), 0o600))
}

func planSource(directory string, options executor.ResearchConfigOptions) (modulespec.Plan, error) {
	config, err := executor.NewResearchConfig(directory, options)
	if err != nil {
		return modulespec.Plan{}, err
	}
	planned, err := executor.RunResearchPlan(config)
	if err != nil {
		return modulespec.Plan{}, err
	}
	return planned.Plan, nil
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestNestedResearchBlockWorkingDirectoryUsesFullModuleAddress(t *testing.T) {
	runRoot := t.TempDir()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	writeR42(t, child, "main.r42.hcl", `
research "static" "detail" {
  model         = "test-model"
  system_prompt = block_wd()
}
`)
	writeR42(t, root, "main.r42.hcl", `module "child" { source = "./child" }`)

	planned, err := planSource(root, executor.ResearchConfigOptions{RunDirectory: runRoot})
	require.NoError(t, err)
	rootNodes := planned.Saved.Nodes()
	require.Len(t, rootNodes, 1)
	require.NotNil(t, rootNodes[0].Module)
	childNodes := rootNodes[0].Module.Plan.Nodes()
	require.Len(t, childNodes, 1)
	decoded, err := modulespec.DecodeResearchPlan(childNodes[0].Config)
	require.NoError(t, err)
	reserved, err := runpkg.Open(planned.Saved.RunDirectory())
	require.NoError(t, err)
	want, err := reserved.WorkspacePath("module.child.research.static.detail")
	require.NoError(t, err)

	assert.Equal(t, want, decoded.Config.SystemPrompt)
	assert.NoDirExists(t, filepath.Join(runRoot, ".r42"))
}
