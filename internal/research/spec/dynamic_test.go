package spec_test

import (
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

//nolint:paralleltest // Golden's block registry is process-global.
func TestDynamicResearchBlockPlannedValuesExposeParentWorkspacePath(t *testing.T) {
	registerResearchSchemaBlocks()
	config := parseResearchConfig(t, `
research "dynamic" "path" {
  tasks = []
}
`)

	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*researchspec.DynamicResearchBlock](config)[0]
	path := block.Values()["path"]
	require.True(t, path.IsKnown())
	assert.True(t, filepath.IsAbs(path.AsString()))
	assert.Equal(t, filepath.ToSlash(filepath.Clean(path.AsString())), path.AsString())
}

func TestPlanDynamicTasksPreservesTaskShape(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":             cty.StringVal("test-model"),
			"system_prompt":     cty.StringVal("Research the topic."),
			"prompt":            cty.StringVal("alpha"),
			"topic":             cty.StringVal("alpha"),
			"terminate_tool_id": cty.StringVal("tool_finish"),
			"artifact":          cty.EmptyObjectVal,
			"retry":             cty.NullVal(cty.DynamicPseudoType),
			"qc":                cty.NullVal(cty.DynamicPseudoType),
		}),
	}))
	require.NoError(t, err)

	task := planned.Index(cty.NumberIntVal(0))
	require.True(t, task.Type().HasAttribute("topic"))
	require.True(t, task.Type().HasAttribute("prompt"))
	require.True(t, task.Type().HasAttribute("result"))
	require.True(t, task.Type().HasAttribute("artifact"))
	assert.Equal(t, "alpha", task.GetAttr("topic").AsString())
	assert.Equal(t, "alpha", task.GetAttr("prompt").AsString())
	assert.Equal(t, "test-model", task.GetAttr("profile").AsString())
	assert.False(t, task.GetAttr("result").IsKnown())
	assert.True(t, task.GetAttr("artifact").Type().IsMapType())
}

func TestPlanDynamicTasksExposesResultForPartialTaskWithTerminatingToolUse(t *testing.T) {
	t.Parallel()

	planned, err := researchspec.PlanDynamicTasks(cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         cty.StringVal("test-model"),
			"system_prompt": cty.StringVal("Research the topic."),
			"prompt":        cty.UnknownVal(cty.String),
			"artifact":      cty.EmptyObjectVal,
			"retry":         cty.NullVal(cty.DynamicPseudoType),
			"qc":            cty.NullVal(cty.DynamicPseudoType),
			"tool_use": cty.MapVal(map[string]cty.Value{
				"finish": cty.ObjectVal(map[string]cty.Value{
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
				"artifact": cty.EmptyObjectVal, "retry": cty.NullVal(cty.DynamicPseudoType),
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

func TestDecodeDynamicTaskDecodesDeclaredArtifactsFromSingularAttribute(t *testing.T) {
	t.Parallel()

	config, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("test-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifact": cty.MapVal(map[string]cty.Value{
			"sources": cty.ObjectVal(map[string]cty.Value{
				"type":        cty.StringVal("directory"),
				"path":        cty.StringVal("artifacts/sources"),
				"description": cty.StringVal("Collected source material"),
			}),
		}),
		"retry": cty.NullVal(cty.DynamicPseudoType),
		"qc":    cty.NullVal(cty.DynamicPseudoType),
	}))

	require.NoError(t, err)
	require.Len(t, config.Artifacts, 1)
	assert.Equal(t, researchspec.Artifact{
		Name: "sources", Type: researchspec.ArtifactTypeDirectory,
		Path: "artifacts/sources", Description: "Collected source material",
	}, config.Artifacts[0])
}

func TestDecodeDynamicTaskResolvesDeclaredArtifactSources(t *testing.T) {
	t.Parallel()

	reference, err := researchspec.ArtifactReferenceFunction(nil).Call([]cty.Value{cty.StringVal("sources")})
	require.NoError(t, err)
	config, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("test-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifact": cty.MapVal(map[string]cty.Value{
			"sources": cty.ObjectVal(map[string]cty.Value{
				"type":        cty.StringVal("directory"),
				"path":        cty.StringVal("artifacts/sources"),
				"description": cty.StringVal("Collected source material"),
				"required":    cty.True,
				"non_empty":   cty.True,
			}),
		}),
		"tool_use": cty.MapVal(map[string]cty.Value{
			"submit": cty.ObjectVal(map[string]cty.Value{
				"tool_id": cty.StringVal("tool_submit"),
				"input_from_agent": cty.ObjectVal(map[string]cty.Value{
					"claims": cty.ObjectVal(map[string]cty.Value{
						"desc":    cty.StringVal("Atomic claims"),
						"sources": cty.TupleVal([]cty.Value{reference}),
					}),
				}),
			}),
		}),
		"retry": cty.NullVal(cty.DynamicPseudoType),
		"qc":    cty.NullVal(cty.DynamicPseudoType),
	}))

	require.NoError(t, err)
	source := config.ToolUses[0].InputFromAgent.GetAttr("claims").GetAttr("sources").Index(cty.NumberIntVal(0))
	assert.Equal(t, "directory", source.GetAttr("type").AsString())
	assert.True(t, source.GetAttr("required").True())
	assert.True(t, source.GetAttr("non_empty").True())
}

func TestDecodeDynamicTaskDecodesMapImportsAndToolUses(t *testing.T) {
	t.Parallel()

	config, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("test-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifact":      cty.EmptyObjectVal,
		"import_artifact": cty.MapVal(map[string]cty.Value{
			"baseline": cty.ObjectVal(map[string]cty.Value{
				"desc":    cty.StringVal("Validated baseline evidence"),
				"sources": cty.EmptyTupleVal,
			}),
		}),
		"tool_use": cty.MapVal(map[string]cty.Value{
			"submit": cty.ObjectVal(map[string]cty.Value{
				"tool_id":          cty.StringVal("tool_submit"),
				"input":            cty.EmptyObjectVal,
				"input_from_agent": cty.EmptyObjectVal,
			}),
		}),
		"retry": cty.NullVal(cty.DynamicPseudoType),
		"qc":    cty.NullVal(cty.DynamicPseudoType),
	}))

	require.NoError(t, err)
	require.Len(t, config.Imports, 1)
	assert.Equal(t, "baseline", config.Imports[0].Name)
	require.Len(t, config.ToolUses, 1)
	assert.Equal(t, "submit", config.ToolUses[0].Name)
}

func TestDecodeDynamicTaskRejectsListImportsAndToolUses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		attribute string
		value     cty.Value
		want      string
	}{
		{
			name:      "imports",
			attribute: "import_artifact",
			value: cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("baseline"), "desc": cty.StringVal("Baseline"), "sources": cty.EmptyTupleVal,
			})}),
			want: "import_artifact must be an object or map",
		},
		{
			name:      "tool uses",
			attribute: "tool_use",
			value: cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("submit"), "tool_id": cty.StringVal("tool_submit"),
			})}),
			want: "tool_use must be an object or map",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values := map[string]cty.Value{
				"model":         cty.StringVal("test-model"),
				"system_prompt": cty.StringVal("Collect and synthesize."),
				"artifact":      cty.EmptyObjectVal,
				"retry":         cty.NullVal(cty.DynamicPseudoType),
				"qc":            cty.NullVal(cty.DynamicPseudoType),
			}
			values[tt.attribute] = tt.value

			_, err := researchspec.DecodeDynamicTask(cty.ObjectVal(values))

			require.EqualError(t, err, tt.want)
		})
	}
}

func TestDecodeDynamicTaskRejectsPluralToolUses(t *testing.T) {
	t.Parallel()

	_, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("test-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifact":      cty.EmptyObjectVal,
		"tool_uses":     cty.EmptyObjectVal,
		"retry":         cty.NullVal(cty.DynamicPseudoType),
		"qc":            cty.NullVal(cty.DynamicPseudoType),
	}))

	require.EqualError(t, err, "tool_uses has been renamed to tool_use")
}

func TestDecodeDynamicTaskRejectsListArtifacts(t *testing.T) {
	t.Parallel()

	_, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("test-model"),
		"system_prompt": cty.StringVal("Collect and synthesize."),
		"artifact":      cty.EmptyTupleVal,
		"retry":         cty.NullVal(cty.DynamicPseudoType),
		"qc":            cty.NullVal(cty.DynamicPseudoType),
	}))

	require.EqualError(t, err, "artifact must be an object or map")
}

func TestResolveArtifactReferencesReplacesDeferredPaths(t *testing.T) {
	t.Parallel()

	reference, err := researchspec.ArtifactReferenceFunction(nil).Call([]cty.Value{cty.StringVal("knowledge")})
	require.NoError(t, err)
	config, err := researchspec.ResolveArtifactReferences(researchspec.Config{
		SystemPrompt: reference.GetAttr("path").AsString(),
		Artifacts: []researchspec.Artifact{{
			Name: "knowledge", Type: researchspec.ArtifactTypeFile,
			Path: "C:/run/task/knowledge.json", Description: "Knowledge artifact",
		}},
		ToolUses: []researchspec.ToolUse{{
			Name: "submit_knowledge",
			Input: cty.ObjectVal(map[string]cty.Value{
				"artifact_path": reference.GetAttr("path"),
			}),
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, "C:/run/task/knowledge.json", config.SystemPrompt)
	assert.Equal(t, "C:/run/task/knowledge.json", config.ToolUses[0].Input.GetAttr("artifact_path").AsString())
}

func TestArtifactReferenceFunctionProducesDeferredArtifactIDs(t *testing.T) {
	t.Parallel()

	function := researchspec.ArtifactReferenceFunction(nil)
	scope, err := function.Call([]cty.Value{cty.StringVal("scope")})
	require.NoError(t, err)
	backup, err := function.Call([]cty.Value{cty.StringVal("backup")})
	require.NoError(t, err)

	scopeName, scopeReference := researchspec.ArtifactReferenceIDName(scope.GetAttr("id").AsString())
	backupName, backupReference := researchspec.ArtifactReferenceIDName(backup.GetAttr("id").AsString())

	assert.True(t, scopeReference)
	assert.Equal(t, "scope", scopeName)
	assert.True(t, backupReference)
	assert.Equal(t, "backup", backupName)
}

func TestArtifactReferenceFunctionDefaultsValidationFlagsToFalse(t *testing.T) {
	t.Parallel()

	reference, err := researchspec.ArtifactReferenceFunction(nil).Call([]cty.Value{cty.StringVal("sources")})

	require.NoError(t, err)
	require.True(t, reference.IsWhollyKnown())
	assert.False(t, reference.GetAttr("required").True())
	assert.False(t, reference.GetAttr("non_empty").True())
}

func TestResolveArtifactReferencesUsesDeclaredValidationFlags(t *testing.T) {
	t.Parallel()

	function := researchspec.ArtifactReferenceFunction(nil)
	optional, err := function.Call([]cty.Value{cty.StringVal("optional")})
	require.NoError(t, err)
	required, err := function.Call([]cty.Value{cty.StringVal("required")})
	require.NoError(t, err)
	config, err := researchspec.ResolveArtifactReferences(researchspec.Config{
		Artifacts: []researchspec.Artifact{
			{Name: "optional", Type: researchspec.ArtifactTypeDirectory, Path: "optional", Description: "Optional sources"},
			{
				Name: "required", Type: researchspec.ArtifactTypeDirectory, Path: "required",
				Description: "Required sources", Required: true, NonEmpty: true,
			},
		},
		ToolUses: []researchspec.ToolUse{{
			Name: "submit",
			InputFromAgent: cty.ObjectVal(map[string]cty.Value{
				"sources": cty.TupleVal([]cty.Value{optional, required}),
			}),
		}},
	})

	require.NoError(t, err)
	sources := config.ToolUses[0].InputFromAgent.GetAttr("sources").AsValueSlice()
	require.Len(t, sources, 2)
	assert.False(t, sources[0].GetAttr("required").True())
	assert.False(t, sources[0].GetAttr("non_empty").True())
	assert.True(t, sources[1].GetAttr("required").True())
	assert.True(t, sources[1].GetAttr("non_empty").True())
}

func TestResolveArtifactReferencesRejectsUndeclaredArtifact(t *testing.T) {
	t.Parallel()

	reference, err := researchspec.ArtifactReferenceFunction(nil).Call([]cty.Value{cty.StringVal("missing")})
	require.NoError(t, err)
	_, err = researchspec.ResolveArtifactReferences(researchspec.Config{
		SystemPrompt: reference.GetAttr("path").AsString(),
	})

	require.ErrorContains(t, err, `artifact("missing") references an undeclared artifact`)
}

func TestDecodeDynamicTaskRejectsEmptyProfile(t *testing.T) {
	t.Parallel()

	_, err := researchspec.DecodeDynamicTask(cty.ObjectVal(map[string]cty.Value{
		"model": cty.StringVal("wire-model"), "profile": cty.StringVal(" "),
		"system_prompt": cty.StringVal("Research."), "artifact": cty.EmptyObjectVal,
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
			"artifact":      cty.EmptyObjectVal,
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
		"artifact":          cty.Map(cty.DynamicPseudoType),
	})
	planned, err := researchspec.PlanDynamicTasks(cty.UnknownVal(cty.List(taskType)))
	require.NoError(t, err)

	require.True(t, planned.Type().IsListType())
	elementType := planned.Type().ElementType()
	assert.True(t, elementType.HasAttribute("topic"))
	assert.True(t, elementType.HasAttribute("prompt"))
	assert.True(t, elementType.HasAttribute("result"))
	assert.True(t, elementType.HasAttribute("artifact"))
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
				"artifact": cty.EmptyObjectVal, "retry": cty.NullVal(cty.DynamicPseudoType),
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
		"artifact": cty.EmptyObjectVal, "retry": cty.NullVal(cty.DynamicPseudoType),
		"qc": cty.NullVal(cty.DynamicPseudoType),
	})
	applied := researchspec.AppliedDynamicTaskValue(task, cty.ObjectVal(map[string]cty.Value{
		"artifact": cty.EmptyObjectVal,
	}))

	assert.False(t, applied.Type().HasAttribute("result"))
	assert.Equal(t, researchspec.FinalQCStrictnessStrict, applied.GetAttr("final_qc_strictness").AsString())
}

func TestAppliedDynamicTaskPublishesDeclaredArtifacts(t *testing.T) {
	t.Parallel()

	task := cty.ObjectVal(map[string]cty.Value{
		"model": cty.StringVal("test-model"), "system_prompt": cty.StringVal("Research."),
		"artifact": cty.EmptyObjectVal, "retry": cty.NullVal(cty.DynamicPseudoType),
		"qc": cty.NullVal(cty.DynamicPseudoType),
	})
	artifacts := researchspec.ArtifactsValueWithIDs([]researchspec.Artifact{{
		Name: "sources", Type: researchspec.ArtifactTypeDirectory, Path: "C:/run/sources", Description: "Primary source",
	}}, nil, map[string]string{"sources": "artifact-sources"})
	applied := researchspec.AppliedDynamicTaskValue(task, cty.ObjectVal(map[string]cty.Value{
		"artifact": artifacts,
	}))

	require.True(t, applied.Type().HasAttribute("artifact"))
	items := applied.GetAttr("artifact")
	require.True(t, items.Type().IsMapType())
	assert.Equal(t, "artifact-sources", items.Index(cty.StringVal("sources")).GetAttr("id").AsString())
	assert.Equal(t, "Primary source", items.Index(cty.StringVal("sources")).GetAttr("description").AsString())
}

func TestAppliedDynamicTaskPreservesSensitiveFields(t *testing.T) {
	t.Parallel()

	input := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"model":         corespec.MarkSensitive(cty.StringVal("test-model")),
			"system_prompt": cty.StringVal("Research."),
			"prompt":        corespec.MarkSensitive(cty.StringVal("secret topic")),
			"artifact":      cty.EmptyObjectVal,
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
		"artifact": cty.EmptyObjectVal,
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
			"artifact":      cty.EmptyObjectVal,
			"retry":         cty.NullVal(cty.DynamicPseudoType),
			"qc":            cty.NullVal(cty.DynamicPseudoType),
		}),
	}))
	_, tasks, err := researchspec.DecodeDynamicTasks(input)
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	assert.True(t, corespec.IsSensitive(tasks[0]))
}
