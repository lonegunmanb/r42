package spec_test

import (
	"testing"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestConfigValidateCollectionFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*researchspec.Config)
		expectedError string
	}{
		{
			name: "empty collection tool id",
			mutate: func(config *researchspec.Config) {
				config.CollectionToolIDs = []string{""}
			},
			expectedError: "research collection_tool_ids must not contain empty values",
		},
		{
			name: "collection tool quota may reference collection tool ids",
			mutate: func(config *researchspec.Config) {
				config.CollectionToolIDs = []string{"tool_go_tool_example_12345678-1234-8234-9234-123456789abc"}
				config.Policy.ToolCallQuota = map[string]int{"tool_go_tool_example_12345678-1234-8234-9234-123456789abc": 1}
			},
			expectedError: "",
		},
		{
			name: "collection tool quota rejects unconfigured tool ids",
			mutate: func(config *researchspec.Config) {
				config.Policy.ToolCallQuota = map[string]int{"tool_go_tool_example_12345678-1234-8234-9234-123456789abc": 1}
			},
			expectedError: `research tool_call_quota references tool id "tool_go_tool_example_12345678-1234-8234-9234-123456789abc" that is not configured for this session`,
		},
		{
			name: "zero collection batch size",
			mutate: func(config *researchspec.Config) {
				config.CollectionBatchSize = 0
			},
			expectedError: "research collection batch size must be positive",
		},
		{
			name: "negative collection batch size",
			mutate: func(config *researchspec.Config) {
				config.CollectionBatchSize = -1
			},
			expectedError: "research collection batch size must be positive",
		},
		{
			name: "zero max collection rounds",
			mutate: func(config *researchspec.Config) {
				config.MaxCollectionRounds = intPointer(0)
			},
			expectedError: "research max collection rounds must be positive",
		},
		{
			name: "negative max collection rounds",
			mutate: func(config *researchspec.Config) {
				config.MaxCollectionRounds = intPointer(-3)
			},
			expectedError: "research max collection rounds must be positive",
		},
		{
			name: "invalid collection qc",
			mutate: func(config *researchspec.Config) {
				config.CollectionQC = &researchspec.CollectionQCConfig{Model: stringPointer(" ")}
			},
			expectedError: "collection qc model must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := researchspec.Config{
				Model:               "model",
				SystemPrompt:        "prompt",
				CollectionBatchSize: researchspec.DefaultCollectionBatchSize,
				Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
			}
			tt.mutate(&config)
			if tt.expectedError == "" {
				require.NoError(t, config.Validate())
				return
			}
			assert.EqualError(t, config.Validate(), tt.expectedError)
		})
	}
}

func TestCollectionQCConfigValidate(t *testing.T) {
	t.Parallel()

	negativeRetries := -1
	tests := []struct {
		name          string
		mutate        func(*researchspec.CollectionQCConfig)
		expectedError string
	}{
		{
			name: "empty model",
			mutate: func(config *researchspec.CollectionQCConfig) {
				config.Model = stringPointer(" ")
			},
			expectedError: "collection qc model must not be empty",
		},
		{
			name: "empty reasoning effort",
			mutate: func(config *researchspec.CollectionQCConfig) {
				config.ReasoningEffort = stringPointer(" ")
			},
			expectedError: "collection qc reasoning effort must not be empty",
		},
		{
			name: "invalid permission",
			mutate: func(config *researchspec.CollectionQCConfig) {
				config.Permission = permissionPointer("prompt")
			},
			expectedError: "collection qc: permission must be approve_all",
		},
		{
			name: "invalid retry",
			mutate: func(config *researchspec.CollectionQCConfig) {
				config.Retry.ModelCallRetries = &negativeRetries
			},
			expectedError: "collection qc retry: model call retries must not be negative",
		},
		{
			name: "empty criteria map",
			mutate: func(config *researchspec.CollectionQCConfig) {
				config.Criteria = cty.MapValEmpty(cty.String)
			},
			expectedError: "collection qc criteria must be a non-empty map of string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := researchspec.CollectionQCConfig{}
			tt.mutate(&config)
			assert.EqualError(t, config.Validate(), tt.expectedError)
		})
	}
}

func TestConfigEffectiveCollectionQC(t *testing.T) {
	t.Parallel()

	t.Run("omitted collection qc keeps mandatory defaults", func(t *testing.T) {
		t.Parallel()

		config := researchspec.Config{
			Model:               "model",
			SystemPrompt:        "prompt",
			CollectionBatchSize: researchspec.DefaultCollectionBatchSize,
			Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
		}
		effective, err := config.EffectiveCollectionQC(provider.DefaultRetryPolicy())
		require.NoError(t, err)
		assert.Equal(t, "model", effective.Model)
		assert.Equal(t, "model", effective.Profile)
		assert.Equal(t, researchspec.PermissionApproveAll, effective.Permission)
		assert.True(t, effective.Criteria.Type().Equals(cty.Map(cty.String)))
		assert.Equal(t, 1, effective.Criteria.LengthInt())
		assert.NotEmpty(t, effective.Criteria.Index(cty.StringVal("sufficiency")).AsString())
	})

	t.Run("collection qc overrides model and criteria", func(t *testing.T) {
		t.Parallel()

		config := researchspec.Config{
			Model:               "model",
			SystemPrompt:        "prompt",
			CollectionBatchSize: researchspec.DefaultCollectionBatchSize,
			Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
			CollectionQC: &researchspec.CollectionQCConfig{
				Model:    stringPointer("qc-model"),
				Criteria: cty.MapVal(map[string]cty.Value{"coverage": cty.StringVal("cover everything")}),
			},
		}
		effective, err := config.EffectiveCollectionQC(provider.DefaultRetryPolicy())
		require.NoError(t, err)
		assert.Equal(t, "qc-model", effective.Model)
		assert.Equal(t, "qc-model", effective.Profile)
		assert.Equal(t, "cover everything", effective.Criteria.Index(cty.StringVal("coverage")).AsString())
	})

	t.Run("invalid collection qc fails", func(t *testing.T) {
		t.Parallel()

		config := researchspec.Config{
			Model:               "model",
			SystemPrompt:        "prompt",
			CollectionBatchSize: researchspec.DefaultCollectionBatchSize,
			Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
			CollectionQC:        &researchspec.CollectionQCConfig{Model: stringPointer(" ")},
		}
		_, err := config.EffectiveCollectionQC(provider.DefaultRetryPolicy())
		assert.EqualError(t, err, "collection qc model must not be empty")
	})
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockPlansCollectionFields(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
model_provider "collection" {
  type     = "openai"
  endpoint = "https://collection.example.test"
}
fixture_tool "source" {}

research "static" "market" {
  model         = "model"
  system_prompt = "Collect and synthesize."

  collection_model_provider = model_provider.collection
  collection_tool_ids = [
    fixture_tool.source.id,
  ]
  collection_skill_directories = ["skills/collection"]
  collection_skills            = ["source-evaluation"]
  collection_disabled_skills   = ["dangerous"]

  collection_batch_size = 5
  max_collection_rounds = 3

  collection_qc {
    criteria = {
      coverage = "The registered snapshots cover the task."
    }
    model            = "qc-model"
    reasoning_effort = "high"
    permission       = "approve_all"
  }
}
`)
	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*researchspec.ResearchBlock](config)[0].ResearchConfig()
	assertReference(t, planned.CollectionModelProvider, "model_provider.collection", "provider")
	assert.Equal(t, []string{"tool_fixture_source"}, planned.CollectionToolIDs)
	require.Len(t, planned.CollectionSkillDirectories, 1)
	assert.NotEmpty(t, planned.CollectionSkillDirectories[0])
	assert.Equal(t, []string{"source-evaluation"}, planned.CollectionSkills)
	assert.Equal(t, []string{"dangerous"}, planned.CollectionDisabledSkills)
	assert.Equal(t, 5, planned.CollectionBatchSize)
	require.NotNil(t, planned.MaxCollectionRounds)
	assert.Equal(t, 3, *planned.MaxCollectionRounds)
	require.NotNil(t, planned.CollectionQC)
	assert.Equal(t, "qc-model", *planned.CollectionQC.Model)
	assert.Equal(t, "high", *planned.CollectionQC.ReasoningEffort)
	require.Equal(t, researchspec.PermissionApproveAll, *planned.CollectionQC.Permission)
	assert.Equal(t, "The registered snapshots cover the task.", planned.CollectionQC.Criteria.Index(cty.StringVal("coverage")).AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockCollectionDefaults(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "market" {
  model         = "model"
  system_prompt = "Collect and synthesize."
}
`)
	require.NoError(t, config.RunPlan())
	planned := golden.Blocks[*researchspec.ResearchBlock](config)[0].ResearchConfig()
	assert.Equal(t, researchspec.PhaseModeFull, planned.EffectivePhaseMode())
	assert.Equal(t, researchspec.DefaultCollectionBatchSize, planned.CollectionBatchSize)
	require.NotNil(t, planned.MaxCollectionRounds)
	assert.Equal(t, researchspec.DefaultMaxCollectionRounds, *planned.MaxCollectionRounds)
	assert.Nil(t, planned.CollectionQC)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockValuesExposeCollectionFields(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
model_provider "collection" {
  type     = "openai"
  endpoint = "https://collection.example.test"
}
research "static" "market" {
  model         = "model"
  system_prompt = "Collect and synthesize."

  collection_model_provider = model_provider.collection
  collection_tool_ids = ["tool_fixture_source"]
  collection_batch_size = 5
  max_collection_rounds = 3

  collection_qc {
    criteria = { coverage = "cover the task" }
  }
}
`)
	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.ResearchBlock](config)[0]
	values := block.Values()
	assert.Equal(t, "full", values["phase_mode"].AsString())
	assertReference(t, values["collection_model_provider"], "model_provider.collection", "provider")
	batchSize, _ := values["collection_batch_size"].AsBigFloat().Int64()
	assert.Equal(t, int64(5), batchSize)
	maxRounds, _ := values["max_collection_rounds"].AsBigFloat().Int64()
	assert.Equal(t, int64(3), maxRounds)
	assert.Equal(t, "tool_fixture_source", values["collection_tool_ids"].Index(cty.NumberIntVal(0)).AsString())
	collectionQC := values["collection_qc"].Index(cty.NumberIntVal(0))
	assert.Equal(t, "cover the task", collectionQC.GetAttr("criteria").Index(cty.StringVal("coverage")).AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigClonesCollectionFields(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "static" "market" {
  model         = "model"
  system_prompt = "Collect and synthesize."

  collection_tool_ids = ["tool_fixture_source"]
  collection_batch_size = 5
  max_collection_rounds = 3

  collection_qc {
    criteria = { coverage = "cover the task" }
    model    = "qc-model"
  }
}
`)
	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.ResearchBlock](config)[0]
	first := block.ResearchConfig()
	first.CollectionToolIDs[0] = "mutated"
	require.NotNil(t, first.CollectionQC)
	first.CollectionQC.Model = stringPointer("mutated-model")
	require.NotNil(t, first.MaxCollectionRounds)
	*first.MaxCollectionRounds = 99

	second := block.ResearchConfig()
	assert.Equal(t, []string{"tool_fixture_source"}, second.CollectionToolIDs)
	require.NotNil(t, second.CollectionQC)
	assert.Equal(t, "qc-model", *second.CollectionQC.Model)
	require.NotNil(t, second.MaxCollectionRounds)
	assert.Equal(t, 3, *second.MaxCollectionRounds)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockSerializesCollectionFieldsInDeferredExpression(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
model_provider "collection" {
  type     = "openai"
  endpoint = "https://collection.example.test"
}
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

  collection_model_provider = model_provider.collection
  collection_tool_ids = ["tool_fixture_collect"]
  collection_batch_size = 4
  max_collection_rounds = 2

  collection_qc {
    criteria = { coverage = "cover the task" }
    model    = "qc-model"
  }
}
`)
	require.NoError(t, config.RunPlan())
	value := config.EvalContext().Variables["research"].GetAttr("static").GetAttr("summary")
	assertReference(t, value.GetAttr("collection_model_provider"), "model_provider.collection", "provider")
	batchSize, _ := value.GetAttr("collection_batch_size").AsBigFloat().Int64()
	assert.Equal(t, int64(4), batchSize)
	maxRounds, _ := value.GetAttr("max_collection_rounds").AsBigFloat().Int64()
	assert.Equal(t, int64(2), maxRounds)
	assert.Equal(t, "tool_fixture_collect", value.GetAttr("collection_tool_ids").Index(cty.NumberIntVal(0)).AsString())
	collectionQC := value.GetAttr("collection_qc").Index(cty.NumberIntVal(0))
	assert.Equal(t, "qc-model", collectionQC.GetAttr("model").AsString())
	assert.Equal(t, "cover the task", collectionQC.GetAttr("criteria").GetAttr("coverage").AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchBlockRejectsMultipleCollectionQCRetryBlocksWithUnknownPrompt(t *testing.T) {
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

  collection_qc {
    criteria = { coverage = "cover" }
    retry {}
    retry {}
  }
}
`)

	err := config.RunPlan()

	require.ErrorContains(t, err, "collection qc supports at most one retry block")
}
