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
	assert.Equal(t, "alpha", task.GetAttr("topic").AsString())
	assert.Equal(t, "alpha", task.GetAttr("prompt").AsString())
	assert.False(t, task.GetAttr("result").IsKnown())
	assert.True(t, task.GetAttr("artifacts").Type().IsListType())
}

func TestPlanDynamicTasksPreservesSensitiveFields(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         cty.StringVal("test-model"),
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

func TestAppliedDynamicTaskPreservesSensitiveFields(t *testing.T) {
	t.Parallel()

	input := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         cty.StringVal("test-model"),
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

	applied := researchspec.AppliedDynamicTaskValue(tasks[0], cty.ObjectVal(map[string]cty.Value{
		"artifact": cty.EmptyTupleVal,
	}))

	assert.True(t, corespec.IsSensitive(applied.GetAttr("prompt")))
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
