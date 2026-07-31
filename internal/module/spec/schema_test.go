package spec_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Azure/golden"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
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
func TestPlanDirectoryPlansModuleInputsAndOutputs(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "modules", "sector")
	require.NoError(t, os.MkdirAll(child, 0o755))
	writeR42(t, child, "main.r42", `
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
	writeR42(t, root, "main.r42", `
module "sector" {
  source      = "./modules/sector"
  parallelism = 4
  timeout     = "1h"
  topic       = "energy"
}

output "sector_report" {
  value = module.sector.report
}
`)

	plan, err := modulespec.PlanDirectory(root, nil)
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
func TestPlanDirectoryUsesVariableDefault(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42", `
variable "topic" {
  type    = string
  default = "energy"
}
output "topic" { value = var.topic }
`)
	writeR42(t, root, "main.r42", `
module "child" { source = "./child" }
output "topic" { value = module.child.topic }
`)

	plan, err := modulespec.PlanDirectory(root, nil)
	require.NoError(t, err)
	assert.Equal(t, "energy", plan.Outputs["topic"].Value.AsString())
}

func TestPlanDirectoryDelegatesRootVariablesToGolden(t *testing.T) {
	registerSchemas()
	t.Setenv("R42_VAR_TOPIC", "from-env")
	root := t.TempDir()
	writeR42(t, root, "main.r42", `
variable "topic" {
  type = string
}
output "topic" { value = var.topic }
`)

	plan, err := modulespec.PlanDirectory(root, nil)
	require.NoError(t, err)
	assert.Equal(t, "from-env", plan.Outputs["topic"].Value.AsString())
}

func TestChildVariableDefaultIsIsolatedFromGoldenRootSources(t *testing.T) {
	registerSchemas()
	t.Setenv("R42_VAR_TOPIC", "from-env")
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42", `
variable "topic" {
  type    = string
  default = "child-default"
}
output "topic" { value = var.topic }
`)
	writeR42(t, root, "main.r42", `
module "child" { source = "./child" }
output "topic" { value = module.child.topic }
`)

	plan, err := modulespec.PlanDirectory(root, nil)
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
	writeR42(t, root, "main.r42", `
variable "topic" { type = string }
module "child" {
  source = "./child"
  topic  = var.topic
}
output "topic" { value = module.child.topic }
`)
	writeR42(t, child, "r42.r42vars", `topic =`)
	writeR42(t, child, "main.r42", `
variable "topic" { type = string }
output "topic" { value = var.topic }
`)

	plan, err := modulespec.PlanDirectory(root, nil)
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
			writeR42(t, child, "main.r42", tt.variable+`
output "secret" { value = var.secret }
`)
			writeR42(t, root, "main.r42", tt.moduleBody+`
output "secret" { value = module.child.secret }
`)

			plan, err := modulespec.PlanDirectory(root, nil)
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
	writeR42(t, child, "main.r42", `
variable "secret" {
  type = string
  validation {
    condition     = var.secret == "42"
    error_message = "secret must be converted"
  }
}
output "secret" { value = var.secret }
`)
	writeR42(t, root, "main.r42", `
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

	plan, err := modulespec.PlanDirectory(root, nil)
	require.NoError(t, err)
	output := plan.Outputs["secret"]
	assert.True(t, corespec.IsSensitive(output.Value))
	value, _ := output.Value.Unmark()
	assert.Equal(t, "42", value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryAcceptsAbsoluteModuleSource(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	child := t.TempDir()
	writeR42(t, child, "main.r42", `output "value" { value = "absolute" }`)
	writeR42(t, root, "main.r42", fmt.Sprintf(`
module "child" { source = %q }
output "value" { value = module.child.value }
`, filepath.ToSlash(child)))

	plan, err := modulespec.PlanDirectory(root, nil)
	require.NoError(t, err)
	assert.Equal(t, "absolute", plan.Outputs["value"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryRejectsInvalidModuleBoundaries(t *testing.T) {
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
				writeR42(t, child, "main.r42", tt.child)
			}
			writeR42(t, root, "main.r42", tt.root)

			_, err := modulespec.PlanDirectory(root, nil)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryAllowsKnownAfterApplyOutput(t *testing.T) {
	registerSchemas()
	directory := t.TempDir()
	writeR42(t, directory, "main.r42", `
planning_fixture "unknown" { value = "unknown" }
output "result" { value = planning_fixture.unknown }
`)

	planned, err := modulespec.PlanDirectory(directory, nil)

	require.NoError(t, err)
	output := planned.Outputs["result"]
	assert.True(t, output.Type.Equals(cty.String))
	assert.False(t, output.Value.IsKnown())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryBuildsSavedResearchDAG(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(toolspec.GoToolBlock))
	golden.RegisterBlock(new(toolspec.ExternalToolBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42", `
research "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."

  artifact "report" {
    type = "file"
    path = "${cwd()}/report.md"
  }
}

research "summary" {
  model         = "test-model"
  system_prompt = "Summarize evidence."
  prompt        = one(research.source.artifact).path
}

output "report_path" {
  value = one(research.source.artifact).path
}
`)

	planned, err := modulespec.PlanDirectoryWithOptions(directory, modulespec.PlanOptions{
		Context: context.Background(),
	})

	require.NoError(t, err)
	require.NotNil(t, planned.Saved)
	nodes := planned.Saved.Nodes()
	require.Len(t, nodes, 2)
	assert.Equal(t, "research.source", nodes[0].Address)
	assert.Equal(t, "research", nodes[0].Kind)
	assert.Empty(t, nodes[0].Dependencies)
	assert.Equal(t, "test-model", nodes[0].Config.GetAttr("model").AsString())
	assert.Equal(t, "research.summary", nodes[1].Address)
	assert.Equal(t, []string{"research.source"}, nodes[1].Dependencies)
	assert.Equal(t,
		"one(research.source.artifact).path",
		planned.Saved.Outputs()["report_path"].Expression,
	)
	assert.Contains(t, planned.Saved.Context(), "research")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryEmbedsSavedChildPlan(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, child, "main.r42", `
research "inside" {
  model         = "test-model"
  system_prompt = "Work inside the module."
}
`)
	writeR42(t, root, "main.r42", `
module "child" {
  source      = "./child"
  parallelism = 2
}
`)

	planned, err := modulespec.PlanDirectoryWithOptions(root, modulespec.PlanOptions{})

	require.NoError(t, err)
	require.NotNil(t, planned.Saved)
	nodes := planned.Saved.Nodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, "module.child", nodes[0].Address)
	require.NotNil(t, nodes[0].Module)
	assert.Equal(t, 2, nodes[0].Module.Parallelism)
	assert.Equal(t, "research.inside", nodes[0].Module.Plan.Nodes()[0].Address)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedResearchConfigCanBeReconstructed(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(toolspec.GoToolBlock))
	golden.RegisterBlock(new(toolspec.ExternalToolBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42", `
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

research "market" {
  model_provider       = model_provider.primary
  model                = "test-model"
  reasoning_effort     = "high"
  system_prompt        = "Research carefully."
  prompt               = "Start now."
  tools                = [external_tool.lookup]
  terminate_tool       = go_tool.finish
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
  }
}
`)

	planned, err := modulespec.PlanDirectoryWithOptions(directory, modulespec.PlanOptions{})
	require.NoError(t, err)
	node := planned.Saved.Nodes()[0]
	assert.True(t, corespec.IsSensitive(node.Config))

	reconstructed, err := modulespec.DecodeResearchPlan(node.Config)
	require.NoError(t, err)
	assert.Equal(t, "test-model", reconstructed.Config.Model)
	assert.Equal(t, "high", *reconstructed.Config.ReasoningEffort)
	assert.Equal(t, "Start now.", *reconstructed.Config.Prompt)
	assert.Equal(t, 7, reconstructed.Config.MaxProtocolAttempts)
	assert.Equal(t, time.Minute, *reconstructed.Config.Timeout)
	require.NotNil(t, reconstructed.Provider)
	assert.Equal(t, "https://example.test", reconstructed.Provider.Endpoint)
	assert.Equal(t, "secret", *reconstructed.Provider.APIKey)
	require.Len(t, reconstructed.Tools, 1)
	assert.Equal(t, "external_tool.lookup", reconstructed.Tools[0].Address)
	assert.Contains(t, reconstructed.Tools[0].InputTypeExpression, "optional(number, 5)")
	require.NotNil(t, reconstructed.TerminateTool)
	assert.Equal(t, "go_tool.finish", reconstructed.TerminateTool.Address)
	require.NotNil(t, reconstructed.Config.QC)
	assert.Equal(t, "qc-model", *reconstructed.Config.QC.Model)
	assert.Equal(t, 3, reconstructed.Config.QC.MaxRounds)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestSavedResearchConfigRestoresExplicitEmptyHeaders(t *testing.T) {
	registerSchemas()
	golden.RegisterBlock(new(provider.ModelProviderBlock))
	golden.RegisterBlock(new(researchspec.ResearchBlock))
	directory := t.TempDir()
	writeR42(t, directory, "main.r42", `
model_provider "primary" {
  type     = "openai"
  endpoint = "https://example.test"
  headers  = {}
}

research "market" {
  model_provider = model_provider.primary
  model          = "test-model"
  system_prompt  = "Research carefully."
}
`)

	planned, err := modulespec.PlanDirectoryWithOptions(directory, modulespec.PlanOptions{})
	require.NoError(t, err)
	reconstructed, err := modulespec.DecodeResearchPlan(planned.Saved.Nodes()[0].Config)

	require.NoError(t, err)
	require.NotNil(t, reconstructed.Provider)
	assert.True(t, reconstructed.Provider.Headers.RawEquals(cty.MapValEmpty(cty.String)))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryRejectsInvalidModuleSchema(t *testing.T) {
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
			writeR42(t, child, "main.r42", `output "value" { value = "ok" }`)
			writeR42(t, root, "main.r42", "module \"child\" {\n"+tt.moduleBody+"\n}")

			_, err := modulespec.PlanDirectory(root, nil)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryRejectsInvalidOutputPrimitiveTypes(t *testing.T) {
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
			writeR42(t, root, "main.r42", "output \"value\" {\nvalue = \"ok\"\n"+tt.attribute+"\n}")

			_, err := modulespec.PlanDirectory(root, nil)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDirectoryDetectsOnlyActiveDirectoryCycles(t *testing.T) {
	registerSchemas()
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	require.NoError(t, os.Mkdir(shared, 0o755))
	writeR42(t, shared, "main.r42", `output "value" { value = "shared" }`)
	writeR42(t, root, "main.r42", `
module "first" { source = "./shared" }
module "second" { source = "./shared" }
output "first" { value = module.first.value }
output "second" { value = module.second.value }
`)

	plan, err := modulespec.PlanDirectory(root, nil)
	require.NoError(t, err)
	assert.Equal(t, "shared", plan.Outputs["first"].Value.AsString())
	assert.Equal(t, "shared", plan.Outputs["second"].Value.AsString())

	cycleRoot := t.TempDir()
	child := filepath.Join(cycleRoot, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	writeR42(t, cycleRoot, "main.r42", `module "child" { source = "./child" }`)
	writeR42(t, child, "main.r42", `module "root" { source = ".." }`)

	_, err = modulespec.PlanDirectory(cycleRoot, nil)
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
	writeR42(t, child, "main.r42", `
planning_fixture "internal" { value = "hidden" }
output "visible" { value = "shown" }
`)
	writeR42(t, root, "main.r42", `
module "child" { source = "./child" }
output "leak" { value = module.child.planning_fixture.internal.value }
`)

	_, err := modulespec.PlanDirectory(root, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "Unsupported attribute")
}

func writeR42(t *testing.T, directory, name, source string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source+"\n"), 0o600))
}
