package spec_test

import (
	"testing"

	"github.com/lonegunmanb/golden"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestDecodeDynamicTaskDecodesCollectionFields(t *testing.T) {
	t.Parallel()

	task := cty.ObjectVal(map[string]cty.Value{
		"model":                        cty.StringVal("wire-model"),
		"system_prompt":                cty.StringVal("Collect and synthesize."),
		"collection_tool_ids":          cty.TupleVal([]cty.Value{cty.StringVal("tool_fixture_source")}),
		"collection_skill_directories": cty.TupleVal([]cty.Value{cty.StringVal("skills/collection")}),
		"collection_skills":            cty.TupleVal([]cty.Value{cty.StringVal("source-evaluation")}),
		"collection_disabled_skills":   cty.TupleVal([]cty.Value{cty.StringVal("dangerous")}),
		"collection_batch_size":        cty.NumberIntVal(5),
		"max_collection_rounds":        cty.NumberIntVal(3),
		"collection_qc": cty.ObjectVal(map[string]cty.Value{
			"criteria":         cty.ObjectVal(map[string]cty.Value{"coverage": cty.StringVal("cover the task")}),
			"model":            cty.StringVal("qc-model"),
			"reasoning_effort": cty.StringVal("high"),
			"permission":       cty.StringVal("approve_all"),
		}),
		"artifacts": cty.EmptyTupleVal,
		"retry":     cty.NullVal(cty.DynamicPseudoType),
		"qc":        cty.NullVal(cty.DynamicPseudoType),
	})

	config, err := researchspec.DecodeDynamicTask(task)

	require.NoError(t, err)
	assert.Equal(t, []string{"tool_fixture_source"}, config.CollectionToolIDs)
	assert.Equal(t, []string{"skills/collection"}, config.CollectionSkillDirectories)
	assert.Equal(t, []string{"source-evaluation"}, config.CollectionSkills)
	assert.Equal(t, []string{"dangerous"}, config.CollectionDisabledSkills)
	assert.Equal(t, 5, config.CollectionBatchSize)
	require.NotNil(t, config.MaxCollectionRounds)
	assert.Equal(t, 3, *config.MaxCollectionRounds)
	require.NotNil(t, config.CollectionQC)
	assert.Equal(t, "qc-model", *config.CollectionQC.Model)
	assert.Equal(t, "high", *config.CollectionQC.ReasoningEffort)
	assert.Equal(t, "cover the task", config.CollectionQC.Criteria.Index(cty.StringVal("coverage")).AsString())
}

func TestDecodeDynamicTaskCollectionQCWithoutCriteria(t *testing.T) {
	t.Parallel()

	task := cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("wire-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"collection_qc": cty.ObjectVal(map[string]cty.Value{
			"model": cty.StringVal("qc-model"),
		}),
		"artifacts": cty.EmptyTupleVal,
		"retry":     cty.NullVal(cty.DynamicPseudoType),
		"qc":        cty.NullVal(cty.DynamicPseudoType),
	})

	config, err := researchspec.DecodeDynamicTask(task)

	require.NoError(t, err)
	require.NotNil(t, config.CollectionQC)
	assert.Equal(t, "qc-model", *config.CollectionQC.Model)
	assert.True(t, config.CollectionQC.Criteria.Type().Equals(cty.NilType))
}

func TestDecodeDynamicTaskCollectionDefaults(t *testing.T) {
	t.Parallel()

	task := cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("wire-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifacts":     cty.EmptyTupleVal,
		"retry":         cty.NullVal(cty.DynamicPseudoType),
		"qc":            cty.NullVal(cty.DynamicPseudoType),
	})

	config, err := researchspec.DecodeDynamicTask(task)

	require.NoError(t, err)
	assert.Equal(t, researchspec.DefaultCollectionBatchSize, config.CollectionBatchSize)
	assert.Nil(t, config.MaxCollectionRounds)
	assert.Nil(t, config.CollectionQC)
}

func TestStaticAndDynamicMembersProduceEquivalentConfigs(t *testing.T) {
	t.Parallel()

	static := decodeStaticCollectionConfig(t)
	dynamic, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":                        cty.StringVal("model"),
		"system_prompt":                cty.StringVal("Collect and synthesize."),
		"collection_tool_ids":          cty.TupleVal([]cty.Value{cty.StringVal("tool_fixture_source")}),
		"collection_skill_directories": cty.TupleVal([]cty.Value{cty.StringVal("skills/collection")}),
		"collection_skills":            cty.TupleVal([]cty.Value{cty.StringVal("source-evaluation")}),
		"collection_disabled_skills":   cty.TupleVal([]cty.Value{cty.StringVal("dangerous")}),
		"collection_batch_size":        cty.NumberIntVal(5),
		"max_collection_rounds":        cty.NumberIntVal(3),
		"collection_qc": cty.ObjectVal(map[string]cty.Value{
			"criteria": cty.ObjectVal(map[string]cty.Value{"coverage": cty.StringVal("cover the task")}),
			"model":    cty.StringVal("qc-model"),
		}),
		"artifacts": cty.EmptyTupleVal,
		"retry":     cty.NullVal(cty.DynamicPseudoType),
		"qc":        cty.NullVal(cty.DynamicPseudoType),
	}))
	require.NoError(t, err)

	assert.Equal(t, static.CollectionToolIDs, dynamic.CollectionToolIDs)
	assert.Equal(t, static.CollectionSkillDirectories, dynamic.CollectionSkillDirectories)
	assert.Equal(t, static.CollectionSkills, dynamic.CollectionSkills)
	assert.Equal(t, static.CollectionDisabledSkills, dynamic.CollectionDisabledSkills)
	assert.Equal(t, static.CollectionBatchSize, dynamic.CollectionBatchSize)
	require.NotNil(t, static.MaxCollectionRounds)
	require.NotNil(t, dynamic.MaxCollectionRounds)
	assert.Equal(t, *static.MaxCollectionRounds, *dynamic.MaxCollectionRounds)
	require.NotNil(t, static.CollectionQC)
	require.NotNil(t, dynamic.CollectionQC)
	assert.Equal(t, *static.CollectionQC.Model, *dynamic.CollectionQC.Model)
	assert.True(t, static.CollectionQC.Criteria.RawEquals(dynamic.CollectionQC.Criteria))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestPlanDynamicTasksPreservesCollectionShape(t *testing.T) {
	registerResearchSchemaBlocks()
	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":                 cty.StringVal("model"),
			"system_prompt":         cty.StringVal("Collect and synthesize."),
			"collection_tool_ids":   cty.TupleVal([]cty.Value{cty.StringVal("tool_fixture_source")}),
			"collection_batch_size": cty.NumberIntVal(5),
			"max_collection_rounds": cty.NumberIntVal(3),
			"collection_qc":         cty.ObjectVal(map[string]cty.Value{"model": cty.StringVal("qc-model")}),
			"artifacts":             cty.EmptyTupleVal,
			"retry":                 cty.NullVal(cty.DynamicPseudoType),
			"qc":                    cty.NullVal(cty.DynamicPseudoType),
		}),
	}))
	require.NoError(t, err)

	task := planned.Index(cty.NumberIntVal(0))
	require.True(t, task.Type().HasAttribute("collection_tool_ids"))
	require.True(t, task.Type().HasAttribute("collection_batch_size"))
	require.True(t, task.Type().HasAttribute("max_collection_rounds"))
	require.True(t, task.Type().HasAttribute("collection_qc"))
	batchSize, _ := task.GetAttr("collection_batch_size").AsBigFloat().Int64()
	assert.Equal(t, int64(5), batchSize)
	maxRounds, _ := task.GetAttr("max_collection_rounds").AsBigFloat().Int64()
	assert.Equal(t, int64(3), maxRounds)
	assert.Equal(t, "qc-model", task.GetAttr("collection_qc").GetAttr("model").AsString())
	assert.Equal(t, "tool_fixture_source", task.GetAttr("collection_tool_ids").Index(cty.NumberIntVal(0)).AsString())
}

func decodeStaticCollectionConfig(t *testing.T) researchspec.Config {
	t.Helper()
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
fixture_tool "source" {}

research "static" "market" {
  model         = "model"
  system_prompt = "Collect and synthesize."

  collection_tool_ids = [
    fixture_tool.source.id,
  ]
  collection_skill_directories = ["skills/collection"]
  collection_skills            = ["source-evaluation"]
  collection_disabled_skills   = ["dangerous"]

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
	return block.ResearchConfig()
}
