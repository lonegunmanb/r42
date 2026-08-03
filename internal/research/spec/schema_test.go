package spec_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type researchTestConfig struct {
	*golden.BaseConfig
}

func (*researchTestConfig) BlockWorkingDirectory(address string) (string, error) {
	return "workspace/" + address, nil
}

var registerResearchBlock sync.Once

type fixtureToolBlock struct {
	*golden.BaseBlock
}

func (*fixtureToolBlock) Type() string             { return "" }
func (*fixtureToolBlock) BlockType() string        { return "fixture_tool" }
func (*fixtureToolBlock) AddressLength() int       { return 2 }
func (*fixtureToolBlock) CanExecutePrePlan() bool  { return false }
func (*fixtureToolBlock) ExecuteDuringPlan() error { return nil }
func (b *fixtureToolBlock) Value() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"id":      cty.StringVal("tool_fixture_" + b.Name()),
		"address": cty.StringVal(b.Address()),
		"kind":    cty.StringVal("builtin"),
	})
}

func referenceValue(address, kind string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal(address),
		"kind":    cty.StringVal(kind),
	})
}

func assertReference(t *testing.T, value cty.Value, address, kind string) {
	t.Helper()
	require.True(t, value.Type().IsObjectType())
	assert.Equal(t, address, value.GetAttr("address").AsString())
	assert.Equal(t, kind, value.GetAttr("kind").AsString())
}

func registerResearchSchemaBlocks() {
	registerResearchBlock.Do(func() {
		golden.RegisterBlock(new(researchspec.ResearchBlock))
		golden.RegisterBlock(new(provider.ModelProviderBlock))
		golden.RegisterBlock(new(fixtureToolBlock))
	})
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestStaticResearchBlockPlansForEachReferenceMap(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "fact" {
  for_each     = toset(["001", "002"])
  model         = "model"
  system_prompt = each.value

  artifact "answer" {
    type = "file"
    path = "${block_wd()}/answer.md"
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*researchspec.ResearchBlock](config)
	require.Len(t, blocks, 2)
	_, isSingleValue := any(blocks[0]).(golden.SingleValueBlock)
	assert.False(t, isSingleValue)
	_, isValuable := any(blocks[0]).(golden.Valuable)
	assert.True(t, isValuable)

	research := config.EvalContext().Variables["research"]
	instances := research.GetAttr("static").GetAttr("fact").AsValueMap()
	require.Len(t, instances, 2)
	for _, key := range []string{"001", "002"} {
		instance, ok := instances[key]
		require.True(t, ok)
		assert.Equal(t, key, instance.GetAttr("system_prompt").AsString())
		artifacts := instance.GetAttr("artifact")
		require.Equal(t, 1, artifacts.LengthInt())
		assert.Equal(t, "answer", artifacts.Index(cty.NumberIntVal(0)).GetAttr("name").AsString())
		assert.False(t, artifacts.Index(cty.NumberIntVal(0)).GetAttr("path").IsKnown())
	}

	plannedPaths := make(map[string]string, len(blocks))
	for _, block := range blocks {
		require.Len(t, block.ResearchConfig().Artifacts, 1)
		plannedPaths[block.Address()] = block.ResearchConfig().Artifacts[0].Path
	}
	assert.Equal(t, map[string]string{
		"research.static.fact[001]": "workspace/research.static.fact[001]/answer.md",
		"research.static.fact[002]": "workspace/research.static.fact[002]/answer.md",
	}, plannedPaths)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockPlansTypedToolIDsAsStrings(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "lookup" {}
fixture_tool "finish" {}
fixture_tool "read_only" {}

research "static" "market" {
  model             = "model"
  system_prompt     = "prompt"
  tool_ids          = [fixture_tool.lookup.id]
  terminate_tool_id = fixture_tool.finish.id
  typed_tool_call_quota = {
    (fixture_tool.lookup.id) = 2
    (fixture_tool.finish.id) = 1
  }

  qc {
    criteria = { accuracy = "Check the report." }
    tool_ids = [fixture_tool.read_only.id]
    typed_tool_call_quota = {
      (fixture_tool.read_only.id) = 3
    }
  }
}
`)

	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*researchspec.ResearchBlock](config)[0].ResearchConfig()
	assert.Equal(t, []string{"tool_fixture_lookup"}, planned.Policy.ToolIDs)
	assert.Equal(t, map[string]int{"tool_fixture_lookup": 2, "tool_fixture_finish": 1}, planned.Policy.TypedToolCallQuota)
	require.NotNil(t, planned.TerminateToolID)
	assert.Equal(t, "tool_fixture_finish", *planned.TerminateToolID)
	require.NotNil(t, planned.QC)
	assert.Equal(t, []string{"tool_fixture_read_only"}, planned.QC.ToolIDs)
	assert.Equal(t, map[string]int{"tool_fixture_read_only": 3}, planned.QC.TypedToolCallQuota)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestStaticResearchBlockPlansUnknownPromptWithMultipleArtifacts(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "finish" {}

research "static" "source" {
  model             = "model"
  system_prompt     = "source"
  terminate_tool_id = fixture_tool.finish.id
}

research "static" "summary" {
  model             = "model"
  profile           = null
  system_prompt     = "summary"
  prompt            = research.static.source.result
  terminate_tool_id = fixture_tool.finish.id

  artifact "report" {
    type = "file"
    path = "${block_wd()}/report.md"
  }

  artifact "evidence" {
    type      = "file"
    path      = "${block_wd()}/evidence.json"
    required  = true
    non_empty = true
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*researchspec.ResearchBlock](config)
	require.Len(t, blocks, 2)
	var summary *researchspec.ResearchBlock
	for _, block := range blocks {
		if block.Name() == "summary" {
			summary = block
		}
	}
	require.NotNil(t, summary)
	assert.NotEmpty(t, summary.DeferredTaskExpression())
	value := config.EvalContext().Variables["research"].GetAttr("static").GetAttr("summary")
	assert.Equal(t, "model", value.GetAttr("profile").AsString())
	assert.False(t, value.GetAttr("prompt").IsKnown())
	assert.False(t, value.GetAttr("result").IsKnown())
	artifacts := value.GetAttr("artifact")
	require.Equal(t, 2, artifacts.LengthInt())
	assert.Equal(t, "report", artifacts.Index(cty.NumberIntVal(0)).GetAttr("name").AsString())
	assert.Equal(t, "evidence", artifacts.Index(cty.NumberIntVal(1)).GetAttr("name").AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestStaticResearchBlockRejectsMultipleQCRetryBlocksWithUnknownPrompt(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "finish" {}

research "static" "source" {
  model             = "model"
  system_prompt     = "source"
  terminate_tool_id = fixture_tool.finish.id
}

research "static" "summary" {
  model         = "model"
  system_prompt = "summary"
  prompt        = research.static.source.result

  qc {
    criteria = { accuracy = "accurate" }
    retry {}
    retry {}
  }
}
`)

	err := config.RunPlan()

	require.ErrorContains(t, err, "qc supports at most one retry block")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsFractionalTypedToolCallQuota(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "lookup" {}
research "static" "market" {
  model         = "model"
  system_prompt = "prompt"
  tool_ids      = [fixture_tool.lookup.id]
  typed_tool_call_quota = {
    (fixture_tool.lookup.id) = 1.5
  }
}
`)

	err := config.RunPlan()

	require.Error(t, err)
	assert.ErrorContains(t, err, "whole number")
}

func TestResearchBlockReturnsIndependentTypedToolCallQuota(t *testing.T) {
	t.Parallel()

	block := researchspec.ResearchBlock{
		Model:              "model",
		SystemPrompt:       "prompt",
		ToolIDs:            []string{"research_tool"},
		TypedToolCallQuota: map[string]int{"research_tool": 2},
		QCBlocks: []researchspec.QCBlock{{
			Criteria:           validCriteria(),
			ToolIDs:            []string{"qc_tool"},
			TypedToolCallQuota: map[string]int{"qc_tool": 3},
		}},
	}
	require.NoError(t, block.ExecuteDuringPlan())

	first := block.ResearchConfig()
	first.Policy.TypedToolCallQuota["research_tool"] = 20
	require.NotNil(t, first.QC)
	first.QC.TypedToolCallQuota["qc_tool"] = 30

	second := block.ResearchConfig()
	assert.Equal(t, 2, second.Policy.TypedToolCallQuota["research_tool"])
	require.NotNil(t, second.QC)
	assert.Equal(t, 3, second.QC.TypedToolCallQuota["qc_tool"])
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockAcceptsProductionModelProviderReference(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
model_provider "primary" {
  type     = "openai"
  endpoint = "https://models.example.test"
}

research "static" "with_provider" {
  model_provider = model_provider.primary
  model           = "model"
  system_prompt   = "prompt"
}
`)

	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*researchspec.ResearchBlock](config)[0].ResearchConfig()
	assertReference(t, planned.ModelProvider, "model_provider.primary", "provider")
	ancestors, err := config.GetAncestors("research.static.with_provider")
	require.NoError(t, err)
	assert.Contains(t, ancestors, "model_provider.primary")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockPlansForEachInstances(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "fact" {
  for_each     = {
    arithmetic = "What is 17 plus 25?"
    geography  = "What is the capital of Japan?"
    science    = "At what temperature in Celsius does water freeze?"
  }
  model         = "model"
  system_prompt = "${block_wd()}: ${each.value}"

  artifact "answer" {
    type = "file"
    path = "${block_wd()}/answer.md"
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*researchspec.ResearchBlock](config)
	require.Len(t, blocks, 3)

	type plannedResearch struct {
		systemPrompt string
		artifactPath string
	}
	planned := make(map[string]plannedResearch, len(blocks))
	for _, block := range blocks {
		config := block.ResearchConfig()
		require.Len(t, config.Artifacts, 1)
		planned[block.Address()] = plannedResearch{
			systemPrompt: config.SystemPrompt,
			artifactPath: config.Artifacts[0].Path,
		}
	}
	assert.Equal(t, map[string]plannedResearch{
		"research.static.fact[arithmetic]": {
			systemPrompt: "workspace/research.static.fact[arithmetic]: What is 17 plus 25?",
			artifactPath: "workspace/research.static.fact[arithmetic]/answer.md",
		},
		"research.static.fact[geography]": {
			systemPrompt: "workspace/research.static.fact[geography]: What is the capital of Japan?",
			artifactPath: "workspace/research.static.fact[geography]/answer.md",
		},
		"research.static.fact[science]": {
			systemPrompt: "workspace/research.static.fact[science]: At what temperature in Celsius does water freeze?",
			artifactPath: "workspace/research.static.fact[science]/answer.md",
		},
	}, planned)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockPlansPublicShape(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
model_provider "primary" {
  type     = "openai"
  endpoint = "https://models.example.test"
}
model_provider "quality" {
  type     = "openai"
  endpoint = "https://quality.example.test"
}
fixture_tool "lookup" {}
fixture_tool "finish" {}
fixture_tool "read_only" {}

research "static" "market" {
	model_provider       = model_provider.primary
  model                = "gpt-5.6-sol"
  profile              = "gpt-5.4"
  reasoning_effort     = "max"
  system_prompt        = "Act as a rigorous researcher."
  prompt               = "Research the market."
  timeout              = "2h"
	tool_ids             = [fixture_tool.lookup.id]
	terminate_tool_id    = fixture_tool.finish.id
  allowed_tools        = ["web_search", fixture_tool.finish.id]
  disallowed_tools     = ["ask_user"]
  skill_directories    = ["./skills"]
  skills               = ["source-evaluation"]
  disabled_skills      = ["unsafe-skill"]
  permission           = "approve_all"
  max_protocol_attempts = 7

  retry {
    lifecycle_retries   = 4
    error_message_regex = ["research transient"]
  }

  artifact "report" {
    type      = "file"
    path      = "report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      accuracy = "Cite every claim."
    }
	model_provider      = model_provider.quality
    model              = "qc-model"
    reasoning_effort   = "high"
	tool_ids            = [fixture_tool.read_only.id]
    allowed_tools      = ["web_search", "r42_qc_verdict"]
    disallowed_tools   = ["bash", "edit", "task", "ask_user"]
    skill_directories  = ["./qc-skills"]
    skills             = ["qc-skill"]
    disabled_skills    = ["qc-disabled"]
    permission         = "approve_all"
    max_qc_rounds      = 3

    retry {
      model_call_retries = 2
    }
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*researchspec.ResearchBlock](config)
	require.Len(t, blocks, 1)
	block := blocks[0]
	assert.Equal(t, "market", block.Name())
	assert.Equal(t, "research.static.market", block.Address())
	assert.False(t, block.CanExecutePrePlan())

	planned := block.ResearchConfig()
	assertReference(t, planned.ModelProvider, "model_provider.primary", "provider")
	assert.Equal(t, "gpt-5.6-sol", planned.Model)
	assert.Equal(t, "gpt-5.4", planned.Profile)
	require.NotNil(t, planned.ReasoningEffort)
	assert.Equal(t, "max", *planned.ReasoningEffort)
	assert.Equal(t, "Act as a rigorous researcher.", planned.SystemPrompt)
	require.NotNil(t, planned.Prompt)
	assert.Equal(t, "Research the market.", *planned.Prompt)
	require.NotNil(t, planned.Timeout)
	assert.Equal(t, 2*time.Hour, *planned.Timeout)
	assert.Equal(t, researchspec.PermissionApproveAll, planned.Policy.Permission)
	assert.Equal(t, 7, planned.MaxProtocolAttempts)
	assert.Equal(t, []string{"web_search", "tool_fixture_finish"}, planned.Policy.AllowedTools)
	assert.Equal(t, []string{"ask_user"}, planned.Policy.DisallowedTools)
	assert.Equal(t, []string{"./skills"}, planned.Policy.SkillDirectories)
	assert.Equal(t, []string{"source-evaluation"}, planned.Policy.Skills)
	assert.Equal(t, []string{"unsafe-skill"}, planned.Policy.DisabledSkills)
	assert.Equal(t, []string{"tool_fixture_lookup"}, planned.Policy.ToolIDs)
	require.NotNil(t, planned.TerminateToolID)
	assert.Equal(t, "tool_fixture_finish", *planned.TerminateToolID)
	require.Len(t, planned.Artifacts, 1)
	assert.Equal(t, researchspec.Artifact{
		Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Required: true, NonEmpty: true,
	}, planned.Artifacts[0])
	require.NotNil(t, planned.QC)
	assertReference(t, planned.QC.ModelProvider, "model_provider.quality", "provider")
	assert.Equal(t, []string{"tool_fixture_read_only"}, planned.QC.ToolIDs)
	assert.Equal(t, 3, planned.QC.MaxRounds)
	assert.Equal(t, []string{"./qc-skills"}, planned.QC.SkillDirectories)
	assert.Equal(t, []string{"qc-skill"}, planned.QC.Skills)
	assert.Equal(t, []string{"qc-disabled"}, planned.QC.DisabledSkills)

	effective, err := planned.EffectiveQC(provider.DefaultRetryPolicy())
	require.NoError(t, err)
	assert.Equal(t, "qc-model", effective.Model)
	assert.Equal(t, 4, effective.Retry.LifecycleRetries)
	assert.Equal(t, 2, effective.Retry.ModelCallRetries)
	assertReference(t, effective.ModelProvider, "model_provider.quality", "provider")
	assert.Equal(t, []string{"tool_fixture_read_only"}, effective.ToolIDs)

	ancestors, err := config.GetAncestors("research.static.market")
	require.NoError(t, err)
	for _, address := range []string{
		"model_provider.primary",
		"model_provider.quality",
		"fixture_tool.lookup",
		"fixture_tool.finish",
		"fixture_tool.read_only",
	} {
		assert.Contains(t, ancestors, address)
	}

	value := cty.ObjectVal(block.Values())
	for _, attribute := range []string{
		"model_provider",
		"model",
		"profile",
		"reasoning_effort",
		"system_prompt",
		"prompt",
		"tool_ids",
		"terminate_tool_id",
		"allowed_tools",
		"disallowed_tools",
		"skill_directories",
		"skills",
		"disabled_skills",
		"permission",
		"max_protocol_attempts",
		"timeout",
		"retry",
		"artifact",
		"qc",
		"result",
	} {
		assert.True(t, value.Type().HasAttribute(attribute), attribute)
	}
	assert.True(t, value.Type().HasAttribute("result"))
	artifacts := value.GetAttr("artifact")
	assert.True(t, artifacts.Type().IsListType())
	require.Equal(t, 1, artifacts.LengthInt())
	assert.False(t, artifacts.Index(cty.NumberIntVal(0)).GetAttr("path").IsKnown())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockDefaultsProfileToModel(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "market" {
  model         = "wire-model"
  system_prompt = "Research carefully."
}
`)

	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.ResearchBlock](config)[0]

	assert.Equal(t, "wire-model", block.ResearchConfig().Profile)
	assert.Equal(t, "wire-model", block.Values()["profile"].AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsEmptyProfile(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "market" {
  model         = "wire-model"
  profile       = " "
  system_prompt = "Research carefully."
}
`)

	err := config.RunPlan()

	require.ErrorContains(t, err, "research profile must not be empty")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockExposesEveryNestedBlockAsListOfObjects(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "market" {
  model         = "gpt-5.6-sol"
  system_prompt = "Research carefully."

  retry {
    lifecycle_retries = 4
  }

  artifact "report" {
    type = "file"
    path = "report.md"
  }

  artifact "evidence" {
    type = "directory"
    path = "evidence"
  }

  qc {
    criteria = { accuracy = "Cite every claim." }

    retry {
      model_call_retries = 2
    }
  }
}
`)

	require.NoError(t, config.RunPlan())
	value := cty.ObjectVal(golden.Blocks[*researchspec.ResearchBlock](config)[0].Values())
	assert.False(t, value.Type().HasAttribute("artifacts"))

	for _, name := range []string{"retry", "artifact", "qc"} {
		require.True(t, value.Type().HasAttribute(name), name)
		assert.True(t, value.GetAttr(name).Type().IsListType(), name)
	}

	artifacts := value.GetAttr("artifact")
	require.Equal(t, 2, artifacts.LengthInt())
	assert.Equal(t, "report", artifacts.Index(cty.NumberIntVal(0)).GetAttr("name").AsString())
	assert.Equal(t, "evidence", artifacts.Index(cty.NumberIntVal(1)).GetAttr("name").AsString())

	qc := value.GetAttr("qc").Index(cty.NumberIntVal(0))
	assert.True(t, qc.Type().IsObjectType())
	require.True(t, qc.Type().HasAttribute("retry"))
	qcRetries := qc.GetAttr("retry")
	assert.True(t, qcRetries.Type().IsListType())
	require.Equal(t, 1, qcRetries.LengthInt())
	assert.True(t, qcRetries.Index(cty.NumberIntVal(0)).Type().IsObjectType())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockAppliesDefaultsAndOmitsResultWithoutTerminateTool(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "minimal" {
  model         = "gpt-5.6-sol"
  system_prompt = "Research carefully."
  disallowed_tools = []

  qc {
    criteria = {
      accuracy = "Be accurate."
    }
  }
}
`)

	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.ResearchBlock](config)[0]
	planned := block.ResearchConfig()
	require.NoError(t, corespec.ValidateType(cty.ObjectVal(block.Values()).Type()))
	assert.Nil(t, block.Prompt)
	assert.Nil(t, block.MaxProtocolAttempts)
	assert.Equal(t, researchspec.PermissionApproveAll, planned.Policy.Permission)
	assert.Equal(t, researchspec.DefaultMaxProtocolAttempts, planned.MaxProtocolAttempts)
	assert.NotNil(t, planned.Policy.DisallowedTools)
	assert.Empty(t, planned.Policy.DisallowedTools)
	require.NotNil(t, planned.QC)
	assert.Equal(t, researchspec.DefaultMaxQCRounds, planned.QC.MaxRounds)
	assert.Equal(t, researchspec.DefaultQCDisallowedTools(), planned.QC.DisallowedTools)
	assert.False(t, cty.ObjectVal(block.Values()).Type().HasAttribute("result"))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsStringReferencePlaceholders(t *testing.T) {
	registerResearchSchemaBlocks()
	tests := []struct {
		name          string
		attribute     string
		expectedError string
	}{
		{name: "model provider", attribute: `model_provider = "fixture_provider.primary"`, expectedError: "research model_provider must be a provider reference"},
		{name: "tool ids", attribute: `tool_ids = [""]`, expectedError: "research tool_ids must not contain empty values"},
		{name: "terminate tool id", attribute: `terminate_tool_id = ""`, expectedError: "research terminate_tool_id must not be empty"},
		{name: "qc model provider", attribute: "qc {\ncriteria = { accuracy = \"accurate\" }\nmodel_provider = \"fixture_provider.quality\"\n}", expectedError: "qc model_provider must be a provider reference"},
		{name: "qc tool ids", attribute: "qc {\ncriteria = { accuracy = \"accurate\" }\ntool_ids = [\"\"]\n}", expectedError: "qc tool_ids must not contain empty values"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseResearchConfig(t, "research \"static\" \"invalid\" {\nmodel = \"model\"\nsystem_prompt = \"prompt\"\n"+tt.attribute+"\n}")
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsCrossCategoryReferences(t *testing.T) {
	registerResearchSchemaBlocks()
	tests := []struct {
		name          string
		attribute     string
		expectedError string
	}{
		{name: "research provider is tool", attribute: "model_provider = fixture_tool.lookup", expectedError: "research model_provider must be a provider reference"},
		{name: "research tool ids contain provider", attribute: "tool_ids = [model_provider.primary]", expectedError: "string required, but have object"},
		{name: "terminate tool id is provider", attribute: "terminate_tool_id = model_provider.primary", expectedError: "string required, but have object"},
		{name: "qc provider is tool", attribute: "qc {\ncriteria = { accuracy = \"accurate\" }\nmodel_provider = fixture_tool.lookup\n}", expectedError: "qc model_provider must be a provider reference"},
		{name: "qc tool ids contain provider", attribute: "qc {\ncriteria = { accuracy = \"accurate\" }\ntool_ids = [model_provider.primary]\n}", expectedError: "string required, but have object"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseResearchConfig(t, `
model_provider "primary" {
  type     = "openai"
  endpoint = "https://models.example.test"
}
fixture_tool "lookup" {}
research "static" "invalid" {
  model         = "model"
  system_prompt = "prompt"
`+tt.attribute+`
}`)
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsCoercedStringFields(t *testing.T) {
	registerResearchSchemaBlocks()
	tests := []struct {
		name          string
		body          string
		expectedError string
	}{
		{
			name:          "required model",
			body:          "model = 42\nsystem_prompt = \"prompt\"",
			expectedError: "research model must be a string",
		},
		{
			name:          "optional prompt",
			body:          "model = \"model\"\nsystem_prompt = \"prompt\"\nprompt = true",
			expectedError: "research prompt must be a string",
		},
		{
			name:          "tool filter list",
			body:          "model = \"model\"\nsystem_prompt = \"prompt\"\nallowed_tools = [1]",
			expectedError: "research allowed_tools must be a collection of strings",
		},
		{
			name:          "protocol attempts number",
			body:          "model = \"model\"\nsystem_prompt = \"prompt\"\nmax_protocol_attempts = \"7\"",
			expectedError: "research max_protocol_attempts must be a number",
		},
		{
			name: "artifact path",
			body: `model = "model"
system_prompt = "prompt"
artifact "report" {
  type = "file"
  path = 42
}`,
			expectedError: "artifact path must be a string",
		},
		{
			name: "retry regex list",
			body: `model = "model"
system_prompt = "prompt"
retry { error_message_regex = [1] }`,
			expectedError: "research retry error_message_regex must be a collection of strings",
		},
		{
			name: "retry number",
			body: `model = "model"
system_prompt = "prompt"
retry { lifecycle_retries = "7" }`,
			expectedError: "research retry lifecycle_retries must be a number",
		},
		{
			name: "artifact bool",
			body: `model = "model"
system_prompt = "prompt"
artifact "report" {
  type = "file"
  path = "report.md"
  required = "true"
}`,
			expectedError: "artifact required must be a bool",
		},
		{
			name: "qc model",
			body: `model = "model"
system_prompt = "prompt"
qc {
  criteria = { accuracy = "accurate" }
  model = 42
}`,
			expectedError: "qc model must be a string",
		},
		{
			name: "qc rounds number",
			body: `model = "model"
system_prompt = "prompt"
qc {
  criteria = { accuracy = "accurate" }
  max_qc_rounds = "7"
}`,
			expectedError: "qc max_qc_rounds must be a number",
		},
		{
			name: "qc retry number",
			body: `model = "model"
system_prompt = "prompt"
qc {
  criteria = { accuracy = "accurate" }
  retry { interval_seconds = "7" }
}`,
			expectedError: "qc retry interval_seconds must be a number",
		},
		{
			name: "numeric qc criteria",
			body: `model = "model"
system_prompt = "prompt"
qc { criteria = { accuracy = 42 } }`,
			expectedError: "qc criteria must be map of string",
		},
		{
			name: "boolean qc criteria",
			body: `model = "model"
system_prompt = "prompt"
qc { criteria = { accuracy = true } }`,
			expectedError: "qc criteria must be map of string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseResearchConfig(t, "research \"static\" \"invalid\" {\n"+tt.body+"\n}")
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsCoercedFieldsInDynamicBlocks(t *testing.T) {
	registerResearchSchemaBlocks()
	tests := []struct {
		name          string
		dynamicBlock  string
		expectedError string
	}{
		{
			name: "retry",
			dynamicBlock: `dynamic "retry" {
  for_each = [1]
  content { lifecycle_retries = "7" }
}`,
			expectedError: "research retry lifecycle_retries must be a number",
		},
		{
			name: "artifact",
			dynamicBlock: `dynamic "artifact" {
  for_each = [1]
  content {
    labels   = ["report"]
    type     = "file"
    path     = "report.md"
    required = "true"
  }
}`,
			expectedError: "Missing name for artifact",
		},
		{
			name: "qc",
			dynamicBlock: `dynamic "qc" {
  for_each = [1]
  content {
    criteria      = { accuracy = "accurate" }
    max_qc_rounds = "7"
  }
}`,
			expectedError: "qc max_qc_rounds must be a number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseResearchConfig(t, `research "static" "dynamic" {
  model         = "model"
  system_prompt = "prompt"
`+tt.dynamicBlock+`
}`)
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestResearchBlockPreservesCriteriaMarks(t *testing.T) {
	t.Parallel()

	block := researchspec.ResearchBlock{
		Model:        "model",
		SystemPrompt: "prompt",
		QCBlocks: []researchspec.QCBlock{{
			Criteria: cty.ObjectVal(map[string]cty.Value{
				"accuracy": cty.StringVal("accurate").Mark("sensitive"),
			}),
		}},
	}
	require.NoError(t, block.ExecuteDuringPlan())
	planned := block.ResearchConfig()
	require.NotNil(t, planned.QC)
	assert.True(t, planned.QC.Criteria.Index(cty.StringVal("accuracy")).HasMark("sensitive"))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockNormalizesExplicitNullTools(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "null_tools" {
  model         = "model"
  system_prompt = "prompt"
  tool_ids      = null

  qc {
    criteria = { accuracy = "accurate" }
    tool_ids = null
  }
}
`)

	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*researchspec.ResearchBlock](config)[0].ResearchConfig()
	assert.Empty(t, planned.Policy.ToolIDs)
	require.NotNil(t, planned.QC)
	assert.Empty(t, planned.QC.ToolIDs)
}

func TestResearchBlockReturnsIndependentRetryConfig(t *testing.T) {
	t.Parallel()

	block := researchspec.ResearchBlock{
		Model:        "model",
		SystemPrompt: "prompt",
		RetryBlocks: []researchspec.RetryBlock{{
			LifecycleRetries:   intPointer(1),
			ModelCallRetries:   intPointer(2),
			IntervalSeconds:    intPointer(3),
			MaxIntervalSeconds: intPointer(4),
		}},
		QCBlocks: []researchspec.QCBlock{{
			Criteria: validCriteria(),
			RetryBlocks: []researchspec.RetryBlock{{
				LifecycleRetries:   intPointer(5),
				ModelCallRetries:   intPointer(6),
				IntervalSeconds:    intPointer(7),
				MaxIntervalSeconds: intPointer(8),
			}},
		}},
	}
	require.NoError(t, block.ExecuteDuringPlan())

	first := block.ResearchConfig()
	*first.Retry.LifecycleRetries = 11
	*first.Retry.ModelCallRetries = 12
	*first.Retry.Interval = 13 * time.Second
	*first.Retry.MaxInterval = 14 * time.Second
	require.NotNil(t, first.QC)
	*first.QC.Retry.LifecycleRetries = 15
	*first.QC.Retry.ModelCallRetries = 16
	*first.QC.Retry.Interval = 17 * time.Second
	*first.QC.Retry.MaxInterval = 18 * time.Second

	second := block.ResearchConfig()
	assert.Equal(t, 1, *second.Retry.LifecycleRetries)
	assert.Equal(t, 2, *second.Retry.ModelCallRetries)
	assert.Equal(t, 3*time.Second, *second.Retry.Interval)
	assert.Equal(t, 4*time.Second, *second.Retry.MaxInterval)
	require.NotNil(t, second.QC)
	assert.Equal(t, 5, *second.QC.Retry.LifecycleRetries)
	assert.Equal(t, 6, *second.QC.Retry.ModelCallRetries)
	assert.Equal(t, 7*time.Second, *second.QC.Retry.Interval)
	assert.Equal(t, 8*time.Second, *second.QC.Retry.MaxInterval)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockNativeFieldsPreserveExplicitZeroAndFalse(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "explicit_zero" {
  model                 = "gpt-5.6-sol"
  system_prompt         = "Research carefully."
  max_protocol_attempts = 0

  retry {
    lifecycle_retries = 0
  }

  artifact "report" {
    type      = "file"
    path      = "report.md"
    required  = false
    non_empty = false
  }

  qc {
    criteria = { accuracy = "Be accurate." }
    max_qc_rounds = 0
  }
}
`)

	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.ResearchBlock](config)[0]
	planned := block.ResearchConfig()
	require.NotNil(t, block.MaxProtocolAttempts)
	assert.Equal(t, 0, *block.MaxProtocolAttempts)
	require.Len(t, block.ArtifactBlocks, 1)
	assert.False(t, block.ArtifactBlocks[0].Required)
	assert.False(t, block.ArtifactBlocks[0].NonEmpty)
	assert.Equal(t, 0, planned.MaxProtocolAttempts)
	require.NotNil(t, planned.Retry.LifecycleRetries)
	assert.Equal(t, 0, *planned.Retry.LifecycleRetries)
	require.Len(t, planned.Artifacts, 1)
	assert.False(t, planned.Artifacts[0].Required)
	assert.False(t, planned.Artifacts[0].NonEmpty)
	require.NotNil(t, planned.QC)
	assert.Equal(t, 0, planned.QC.MaxRounds)
	effective, err := planned.EffectiveQC(provider.DefaultRetryPolicy())
	require.NoError(t, err)
	assert.Equal(t, 0, effective.MaxRounds)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsMultipleNestedBlocks(t *testing.T) {
	registerResearchSchemaBlocks()
	tests := []struct {
		name          string
		nested        string
		expectedError string
	}{
		{name: "two qc blocks", nested: "qc { criteria = { a = \"a\" } }\nqc { criteria = { b = \"b\" } }", expectedError: "research must have at most one qc block"},
		{name: "two retry blocks", nested: "retry {}\nretry {}", expectedError: "research must have at most one retry block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseResearchConfig(t, "research \"static\" \"invalid\" {\nmodel = \"model\"\nsystem_prompt = \"prompt\"\n"+tt.nested+"\n}")
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestResearchBlockRejectsInvalidAttributeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*researchspec.ResearchBlock)
		expectedError string
	}{
		{
			name: "permission must be supported",
			mutate: func(block *researchspec.ResearchBlock) {
				block.Permission = permissionPointer(researchspec.Permission("prompt"))
			},
			expectedError: "research: permission must be approve_all",
		},
		{
			name: "timeout must parse",
			mutate: func(block *researchspec.ResearchBlock) {
				block.Timeout = stringPointer("later")
			},
			expectedError: "timeout must be a go duration",
		},
		{
			name: "retry interval must fit duration",
			mutate: func(block *researchspec.ResearchBlock) {
				block.RetryBlocks = []researchspec.RetryBlock{{IntervalSeconds: intPointer(math.MaxInt64/int(time.Second) + 1)}}
			},
			expectedError: "interval_seconds is too large",
		},
		{
			name: "retry max interval must fit duration",
			mutate: func(block *researchspec.ResearchBlock) {
				block.RetryBlocks = []researchspec.RetryBlock{{MaxIntervalSeconds: intPointer(math.MaxInt64/int(time.Second) + 1)}}
			},
			expectedError: "max_interval_seconds is too large",
		},
		{
			name: "retry negative interval must fit duration",
			mutate: func(block *researchspec.ResearchBlock) {
				block.RetryBlocks = []researchspec.RetryBlock{{
					IntervalSeconds: intPointer(math.MinInt64/int(time.Second) - 1),
				}}
			},
			expectedError: "interval_seconds is too large",
		},
		{
			name: "qc criteria must convert",
			mutate: func(block *researchspec.ResearchBlock) {
				block.QCBlocks = []researchspec.QCBlock{{Criteria: cty.EmptyTupleVal}}
			},
			expectedError: "qc criteria must be map of string",
		},
		{
			name: "qc retry block is singular",
			mutate: func(block *researchspec.ResearchBlock) {
				block.QCBlocks = []researchspec.QCBlock{{Criteria: validCriteria(), RetryBlocks: []researchspec.RetryBlock{{}, {}}}}
			},
			expectedError: "qc must have at most one retry block",
		},
		{
			name: "qc retry must be valid",
			mutate: func(block *researchspec.ResearchBlock) {
				block.QCBlocks = []researchspec.QCBlock{{Criteria: validCriteria(), RetryBlocks: []researchspec.RetryBlock{{ModelCallRetries: intPointer(-1)}}}}
			},
			expectedError: "qc retry: model call retries must not be negative",
		},
		{
			name: "qc retry max interval must fit duration",
			mutate: func(block *researchspec.ResearchBlock) {
				block.QCBlocks = []researchspec.QCBlock{{
					Criteria: validCriteria(),
					RetryBlocks: []researchspec.RetryBlock{{
						MaxIntervalSeconds: intPointer(math.MaxInt64/int(time.Second) + 1),
					}},
				}}
			},
			expectedError: "max_interval_seconds is too large",
		},
		{
			name: "qc retry negative max interval must fit duration",
			mutate: func(block *researchspec.ResearchBlock) {
				block.QCBlocks = []researchspec.QCBlock{{
					Criteria: validCriteria(),
					RetryBlocks: []researchspec.RetryBlock{{
						MaxIntervalSeconds: intPointer(math.MinInt64/int(time.Second) - 1),
					}},
				}}
			},
			expectedError: "max_interval_seconds is too large",
		},
		{
			name: "complete retry override",
			mutate: func(block *researchspec.ResearchBlock) {
				block.RetryBlocks = []researchspec.RetryBlock{{
					LifecycleRetries: intPointer(1), ModelCallRetries: intPointer(2),
					IntervalSeconds: intPointer(3), MaxIntervalSeconds: intPointer(4),
				}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			block := &researchspec.ResearchBlock{Model: "model", SystemPrompt: "prompt"}
			tt.mutate(block)
			err := block.ExecuteDuringPlan()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func intPointer(value int) *int { return &value }

func validCriteria() cty.Value {
	return cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")})
}

func parseResearchConfig(t *testing.T, source string) *researchTestConfig {
	t.Helper()

	syntaxFile, diagnostics := hclsyntax.ParseConfig([]byte(source), "research.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	writeFile, diagnostics := hclwrite.ParseConfig([]byte(source), "research.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := syntaxFile.Body.(*hclsyntax.Body)
	require.True(t, ok)

	config := &researchTestConfig{BaseConfig: golden.NewBasicConfig("", "r42", "r42", nil, nil, nil)}
	err := golden.InitConfig(config, golden.AsHclBlocks(body.Blocks, writeFile.Body().Blocks()))
	require.NoError(t, err)
	return config
}
