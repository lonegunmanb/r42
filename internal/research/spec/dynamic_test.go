package spec_test

import (
	"testing"

	"github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestPlanDynamicTasksPreservesTaskShape(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":             cty.StringVal("test-model"),
			"system_prompt":     cty.StringVal("Research the topic."),
			"prompt":            cty.StringVal("alpha"),
			"topic":             cty.StringVal("alpha"),
			"terminate_tool_id": cty.StringVal("tool_finish"),
			"artifacts":         cty.EmptyTupleVal,
			"retry":             cty.NullVal(cty.DynamicPseudoType),
			"qc":                cty.NullVal(cty.DynamicPseudoType),
		}),
	}))
	require.NoError(t, err)

	task := planned.Index(cty.NumberIntVal(0))
	require.True(t, task.Type().HasAttribute("topic"))
	require.True(t, task.Type().HasAttribute("prompt"))
	require.True(t, task.Type().HasAttribute("result"))
	require.True(t, task.Type().HasAttribute("artifacts"))
	require.True(t, task.Type().HasAttribute("snapshots"))
	assert.Equal(t, "alpha", task.GetAttr("topic").AsString())
	assert.Equal(t, "alpha", task.GetAttr("prompt").AsString())
	assert.Equal(t, "test-model", task.GetAttr("profile").AsString())
	assert.False(t, task.GetAttr("result").IsKnown())
	assert.True(t, task.GetAttr("artifacts").Type().IsListType())
	assert.True(t, task.GetAttr("snapshots").Type().IsListType())
	assert.False(t, task.GetAttr("snapshots").IsKnown())
}

func TestPlanDynamicTasksExposesResultForPartialTaskWithTerminatingToolUse(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         cty.StringVal("test-model"),
			"system_prompt": cty.StringVal("Research the topic."),
			"prompt":        cty.UnknownVal(cty.String),
			"artifacts":     cty.EmptyTupleVal,
			"snapshots":     cty.EmptyTupleVal,
			"retry":         cty.NullVal(cty.DynamicPseudoType),
			"qc":            cty.NullVal(cty.DynamicPseudoType),
			"tool_uses": cty.TupleVal([]cty.Value{
				cty.ObjectVal(map[string]cty.Value{
					"name":             cty.StringVal("finish"),
					"tool_id":          cty.StringVal("tool_finish"),
					"terminate":        cty.BoolVal(true),
					"input":            cty.EmptyObjectVal,
					"input_from_agent": cty.EmptyObjectVal,
				}),
			}),
		}),
	}))
	require.NoError(t, err)

	task := planned.Index(cty.NumberIntVal(0))
	require.True(t, task.Type().HasAttribute("result"))
	assert.False(t, task.GetAttr("result").IsKnown())
}

func TestDecodeDynamicTaskResolvesProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		profile  cty.Value
		expected string
	}{
		{name: "defaults to model", profile: cty.NilVal, expected: "wire-model"},
		{name: "uses explicit profile", profile: cty.StringVal("gpt-5.4"), expected: "gpt-5.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values := map[string]cty.Value{
				"model": cty.StringVal("wire-model"), "system_prompt": cty.StringVal("Research."),
				"artifacts": cty.EmptyTupleVal, "retry": cty.NullVal(cty.DynamicPseudoType),
				"qc": cty.NullVal(cty.DynamicPseudoType),
			}
			if tt.profile != cty.NilVal {
				values["profile"] = tt.profile
			}

			config, err := researchspec.DecodeDynamicTask(cty.ObjectVal(values))

			require.NoError(t, err)
			assert.Equal(t, tt.expected, config.Profile)
		})
	}
}

func TestDecodeDynamicTaskDecodesDeclaredSnapshots(t *testing.T) {
	t.Parallel()

	config, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("test-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifacts":     cty.EmptyTupleVal,
		"snapshots": cty.TupleVal([]cty.Value{
			cty.ObjectVal(map[string]cty.Value{
				"name":        cty.StringVal("sources"),
				"type":        cty.StringVal("directory"),
				"path":        cty.StringVal("snapshots/sources"),
				"description": cty.StringVal("Collected source material"),
			}),
		}),
		"retry": cty.NullVal(cty.DynamicPseudoType),
		"qc":    cty.NullVal(cty.DynamicPseudoType),
	}))

	require.NoError(t, err)
	require.Len(t, config.Snapshots, 1)
	assert.Equal(t, researchspec.Artifact{
		Name: "sources", Type: researchspec.ArtifactTypeDirectory,
		Path: "snapshots/sources", Description: "Collected source material",
	}, config.Snapshots[0])
}

func TestDecodeDynamicTaskRejectsEmptyProfile(t *testing.T) {
	t.Parallel()

	_, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model": cty.StringVal("wire-model"), "profile": cty.StringVal(" "),
		"system_prompt": cty.StringVal("Research."), "artifacts": cty.EmptyTupleVal,
		"retry": cty.NullVal(cty.DynamicPseudoType), "qc": cty.NullVal(cty.DynamicPseudoType),
	}))

	require.ErrorContains(t, err, "research profile must not be empty")
}

func TestPlanDynamicTasksPreservesSensitiveFields(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         corespec.MarkSensitive(cty.StringVal("test-model")),
			"system_prompt": cty.StringVal("Research the topic."),
			"prompt":        corespec.MarkSensitive(cty.StringVal("secret topic")),
			"artifacts":     cty.EmptyTupleVal,
			"retry":         cty.NullVal(cty.DynamicPseudoType),
			"qc":            cty.NullVal(cty.DynamicPseudoType),
		}),
	}))
	require.NoError(t, err)

	task := planned.Index(cty.NumberIntVal(0))
	assert.True(t, corespec.IsSensitive(task.GetAttr("prompt")))
	assert.True(t, corespec.IsSensitive(task.GetAttr("profile")))
	display, err := plan.DisplayValues(map[string]cty.Value{"tasks": planned})
	require.NoError(t, err)
	assert.Contains(t, display, "<sensitive>")
	assert.NotContains(t, display, "secret topic")
}

func TestPlanDynamicTasksPreservesUnknownShape(t *testing.T) {
	t.Parallel()

	taskType := cty.Object(map[string]cty.Type{
		"model":             cty.String,
		"system_prompt":     cty.String,
		"prompt":            cty.String,
		"topic":             cty.String,
		"terminate_tool_id": cty.String,
		"artifacts":         cty.List(cty.DynamicPseudoType),
	})
	planned, err := researchspec.PlanDynamicTasks(cty.UnknownVal(cty.List(taskType)))
	require.NoError(t, err)

	require.True(t, planned.Type().IsListType())
	elementType := planned.Type().ElementType()
	assert.True(t, elementType.HasAttribute("topic"))
	assert.True(t, elementType.HasAttribute("prompt"))
	assert.True(t, elementType.HasAttribute("result"))
	assert.True(t, elementType.HasAttribute("artifacts"))
	assert.True(t, elementType.HasAttribute("snapshots"))
}

func TestPlanDynamicTasksKeepsUnconstrainedUnknownDynamic(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(corespec.MarkSensitive(cty.DynamicVal))

	require.NoError(t, err)
	assert.True(t, corespec.IsSensitive(planned))
	unmarked, _ := planned.Unmark()
	assert.True(t, unmarked.RawEquals(cty.DynamicVal))
}

func TestDecodeDynamicTaskRejectsListRetryAndQC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		attribute     string
		expectedError string
	}{
		{name: "retry", attribute: "retry", expectedError: "retry must be an object"},
		{name: "qc", attribute: "qc", expectedError: "qc must be an object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			values := map[string]cty.Value{
				"model": cty.StringVal("test-model"), "system_prompt": cty.StringVal("Research."),
				"artifacts": cty.EmptyTupleVal, "retry": cty.NullVal(cty.DynamicPseudoType),
				"qc": cty.NullVal(cty.DynamicPseudoType),
			}
			values[tt.attribute] = cty.EmptyTupleVal

			_, err := researchspec.DecodeDynamicTask(cty.ObjectVal(values))

			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestPlanDynamicTasksAcceptsEmptyList(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(corespec.MarkSensitive(cty.EmptyTupleVal))

	require.NoError(t, err)
	assert.True(t, corespec.IsSensitive(planned))
	unmarked, _ := planned.Unmark()
	assert.True(t, unmarked.RawEquals(cty.EmptyTupleVal))
}

func TestAppliedDynamicTaskWithoutTerminateToolDoesNotGainResult(t *testing.T) {
	t.Parallel()

	task := cty.ObjectVal(map[string]cty.Value{
		"model": cty.StringVal("test-model"), "system_prompt": cty.StringVal("Research."),
		"artifacts": cty.EmptyTupleVal, "retry": cty.NullVal(cty.DynamicPseudoType),
		"qc": cty.NullVal(cty.DynamicPseudoType),
	})
	applied := researchspec.AppliedDynamicTaskValue(task, cty.ObjectVal(map[string]cty.Value{
		"artifact": cty.EmptyTupleVal,
	}))

	assert.False(t, applied.Type().HasAttribute("result"))
}

func TestAppliedDynamicTaskPublishesSnapshots(t *testing.T) {
	t.Parallel()

	task := cty.ObjectVal(map[string]cty.Value{
		"model": cty.StringVal("test-model"), "system_prompt": cty.StringVal("Research."),
		"artifacts": cty.EmptyTupleVal, "retry": cty.NullVal(cty.DynamicPseudoType),
		"qc": cty.NullVal(cty.DynamicPseudoType),
	})
	snapshots := researchspec.SnapshotsValue([]researchspec.Snapshot{{
		ID: "snapshot-1", Path: "C:/run/source.md", Description: "Primary source",
	}})
	applied := researchspec.AppliedDynamicTaskValue(task, cty.ObjectVal(map[string]cty.Value{
		"artifact": cty.EmptyTupleVal, "snapshots": snapshots,
	}))

	require.True(t, applied.Type().HasAttribute("snapshots"))
	items := applied.GetAttr("snapshots").AsValueSlice()
	require.Len(t, items, 1)
	assert.Equal(t, "snapshot-1", items[0].GetAttr("id").AsString())
	assert.Equal(t, "Primary source", items[0].GetAttr("description").AsString())
}

func TestAppliedDynamicTaskPreservesSensitiveFields(t *testing.T) {
	t.Parallel()

	input := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         corespec.MarkSensitive(cty.StringVal("test-model")),
			"system_prompt": cty.StringVal("Research."),
			"prompt":        corespec.MarkSensitive(cty.StringVal("secret topic")),
			"artifacts":     cty.EmptyTupleVal,
			"retry":         cty.NullVal(cty.DynamicPseudoType),
			"qc":            cty.NullVal(cty.DynamicPseudoType),
		}),
	})
	_, tasks, err := researchspec.DecodeDynamicTasks(input)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.True(t, corespec.IsSensitive(tasks[0].GetAttr("prompt")))
	assert.True(t, corespec.IsSensitive(tasks[0].GetAttr("profile")))

	applied := researchspec.AppliedDynamicTaskValue(tasks[0], cty.ObjectVal(map[string]cty.Value{
		"artifact": cty.EmptyTupleVal,
	}))

	assert.True(t, corespec.IsSensitive(applied.GetAttr("prompt")))
	assert.True(t, corespec.IsSensitive(applied.GetAttr("profile")))
}

func TestDecodeDynamicTasksPropagatesCollectionSensitivity(t *testing.T) {
	t.Parallel()

	input := corespec.MarkSensitive(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         cty.StringVal("test-model"),
			"system_prompt": cty.StringVal("Research."),
			"artifacts":     cty.EmptyTupleVal,
			"retry":         cty.NullVal(cty.DynamicPseudoType),
			"qc":            cty.NullVal(cty.DynamicPseudoType),
		}),
	}))
	_, tasks, err := researchspec.DecodeDynamicTasks(input)
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	assert.True(t, corespec.IsSensitive(tasks[0]))
}
