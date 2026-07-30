package plan_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/plan"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestPlanNestedRoundTripPreservesDAGValuesAndSensitivity(t *testing.T) {
	t.Parallel()

	child, err := plan.New("D:/research/child", []plan.NodeSpec{
		{
			Address: "research.detail",
			Kind:    "research",
			Config: cty.ObjectVal(map[string]cty.Value{
				"model":  cty.StringVal("gpt-5.6-sol"),
				"prompt": cty.StringVal("child prompt"),
			}),
		},
	}, map[string]plan.OutputSpec{
		"summary": {
			Value:       cty.StringVal("child result"),
			Description: "Child summary",
		},
	})
	require.NoError(t, err)
	secretConfig := cty.ObjectVal(map[string]cty.Value{
		"endpoint": cty.StringVal("https://models.example.test"),
		"api_key":  corespec.MarkSensitive(cty.StringVal("literal-secret")),
		"tokens": cty.TupleVal([]cty.Value{
			cty.StringVal("public-token"),
			corespec.MarkSensitive(cty.StringVal("nested-secret")),
		}),
	})
	original, err := plan.New("D:/research", []plan.NodeSpec{
		{Address: "model_provider.primary", Kind: "model_provider", Config: secretConfig},
		{
			Address:      "research.market",
			Kind:         "research",
			Dependencies: []string{"model_provider.primary"},
			Config: cty.ObjectVal(map[string]cty.Value{
				"model": cty.StringVal("gpt-5.6-sol"),
			}),
		},
		{
			Address:      "module.details",
			Kind:         "module",
			Dependencies: []string{"research.market"},
			Config: cty.ObjectVal(map[string]cty.Value{
				"source": cty.StringVal("./child"),
			}),
			Module: &plan.ModuleSpec{
				Plan:        child,
				Parallelism: 3,
				Timeout:     45 * time.Minute,
			},
		},
	}, map[string]plan.OutputSpec{
		"token": {
			Value:       corespec.MarkSensitive(cty.StringVal("output-secret")),
			Description: "Sensitive token",
		},
	})
	require.NoError(t, err)

	encoded, err := plan.Marshal(original)
	require.NoError(t, err)
	decoded, err := plan.Unmarshal(encoded)
	require.NoError(t, err)

	assert.Equal(t, "D:/research", decoded.Directory())
	nodes := decoded.Nodes()
	require.Len(t, nodes, 3)
	assert.Equal(t, "model_provider.primary", nodes[0].Address)
	assert.Equal(t, []string{"model_provider.primary"}, nodes[1].Dependencies)
	assert.True(t, corespec.IsSensitive(nodes[0].Config))
	providerConfig, _ := nodes[0].Config.UnmarkDeep()
	assert.Equal(t, "literal-secret", providerConfig.GetAttr("api_key").AsString())
	assert.True(t, corespec.IsSensitive(nodes[0].Config.GetAttr("tokens").Index(cty.NumberIntVal(1))))
	require.NotNil(t, nodes[2].Module)
	assert.Equal(t, 3, nodes[2].Module.Parallelism)
	assert.Equal(t, 45*time.Minute, nodes[2].Module.Timeout)
	childNodes := nodes[2].Module.Plan.Nodes()
	require.Len(t, childNodes, 1)
	assert.Equal(t, "research.detail", childNodes[0].Address)
	assert.Equal(t, "child result", nodes[2].Module.Plan.Outputs()["summary"].Value.AsString())
	output := decoded.Outputs()["token"]
	assert.True(t, corespec.IsSensitive(output.Value))
	unmarkedOutput, _ := output.Value.UnmarkDeep()
	assert.Equal(t, "output-secret", unmarkedOutput.AsString())
}

func TestPlanSnapshotsAreImmutable(t *testing.T) {
	t.Parallel()

	dependencies := []string{"go_tool.finish"}
	child, err := plan.New("child", []plan.NodeSpec{{
		Address: "research.child",
		Kind:    "research",
		Config:  cty.EmptyObjectVal,
	}}, nil)
	require.NoError(t, err)
	nodes := []plan.NodeSpec{
		{Address: "go_tool.finish", Kind: "go_tool", Config: cty.EmptyObjectVal},
		{
			Address:      "module.child",
			Kind:         "module",
			Dependencies: dependencies,
			Config:       cty.EmptyObjectVal,
			Module:       &plan.ModuleSpec{Plan: child, Parallelism: 2},
		},
	}
	outputs := map[string]plan.OutputSpec{"value": {Value: cty.StringVal("original")}}
	planned, err := plan.New("root", nodes, outputs)
	require.NoError(t, err)

	dependencies[0] = "changed.input"
	nodes[1].Address = "module.changed"
	outputs["value"] = plan.OutputSpec{Value: cty.StringVal("changed")}
	snapshot := planned.Nodes()
	snapshot[1].Dependencies[0] = "changed.snapshot"
	snapshot[1].Module.Parallelism = 99
	snapshotOutputs := planned.Outputs()
	snapshotOutputs["value"] = plan.OutputSpec{Value: cty.StringVal("changed snapshot")}

	fresh := planned.Nodes()
	assert.Equal(t, "module.child", fresh[1].Address)
	assert.Equal(t, []string{"go_tool.finish"}, fresh[1].Dependencies)
	assert.Equal(t, 2, fresh[1].Module.Parallelism)
	assert.Equal(t, "original", planned.Outputs()["value"].Value.AsString())
}

func TestPlanRejectsInvalidDAG(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		nodes         []plan.NodeSpec
		expectedError string
	}{
		{
			name: "unknown dependency",
			nodes: []plan.NodeSpec{{
				Address:      "research.market",
				Kind:         "research",
				Dependencies: []string{"go_tool.missing"},
				Config:       cty.EmptyObjectVal,
			}},
			expectedError: `node "research.market" depends on unknown node "go_tool.missing"`,
		},
		{
			name: "cycle",
			nodes: []plan.NodeSpec{
				{Address: "research.first", Kind: "research", Dependencies: []string{"research.second"}, Config: cty.EmptyObjectVal},
				{Address: "research.second", Kind: "research", Dependencies: []string{"research.first"}, Config: cty.EmptyObjectVal},
			},
			expectedError: "plan dependency cycle",
		},
		{
			name: "duplicate address",
			nodes: []plan.NodeSpec{
				{Address: "research.same", Kind: "research", Config: cty.EmptyObjectVal},
				{Address: "research.same", Kind: "research", Config: cty.EmptyObjectVal},
			},
			expectedError: `node "research.same" is declared more than once`,
		},
		{
			name: "module child required",
			nodes: []plan.NodeSpec{{
				Address: "module.child",
				Kind:    "module",
				Config:  cty.EmptyObjectVal,
			}},
			expectedError: `module node "module.child" must contain a child plan`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := plan.New("root", tt.nodes, nil)
			if tt.expectedError == "plan dependency cycle" {
				require.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestPlanUnknownValuesRoundTripAsKnownAfterApply(t *testing.T) {
	t.Parallel()

	refinedUnknown := cty.UnknownVal(cty.String).Refine().NotNull().StringPrefix("planned-").NewValue()
	planned, err := plan.New("root", []plan.NodeSpec{{
		Address: "research.market",
		Kind:    "research",
		Config: cty.ObjectVal(map[string]cty.Value{
			"result": cty.UnknownVal(cty.String),
		}),
	}}, map[string]plan.OutputSpec{
		"summary": {Value: refinedUnknown},
		"secret":  {Value: corespec.MarkSensitive(cty.UnknownVal(cty.String))},
	})
	require.NoError(t, err)

	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	decoded, err := plan.Unmarshal(encoded)
	require.NoError(t, err)

	result := decoded.Nodes()[0].Config.GetAttr("result")
	assert.False(t, result.IsKnown())
	assert.True(t, result.Type().Equals(cty.String))
	assert.True(t, refinedUnknown.RawEquals(decoded.Outputs()["summary"].Value))
	assert.True(t, corespec.IsSensitive(decoded.Outputs()["secret"].Value))
	display, err := plan.Display(decoded)
	require.NoError(t, err)
	assert.Contains(t, display, "<unknown>")
	assert.Contains(t, display, "<sensitive>")
}

func TestPlanApplyEvaluationRecipeRoundTrip(t *testing.T) {
	t.Parallel()

	contextValues := map[string]cty.Value{
		"var": cty.ObjectVal(map[string]cty.Value{"prefix": cty.StringVal("market")}),
		"research": cty.ObjectVal(map[string]cty.Value{
			"source": cty.ObjectVal(map[string]cty.Value{"result": cty.UnknownVal(cty.String)}),
		}),
	}
	planned, err := plan.NewWithContext("D:/research", nil, map[string]plan.OutputSpec{
		"summary": {
			Value:      cty.UnknownVal(cty.String),
			Expression: `"${var.prefix}: ${research.source.result}"`,
		},
	}, contextValues)
	require.NoError(t, err)

	contextValues["var"] = cty.EmptyObjectVal
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	decoded, err := plan.Unmarshal(encoded)
	require.NoError(t, err)

	assert.Equal(t, `"${var.prefix}: ${research.source.result}"`, decoded.Outputs()["summary"].Expression)
	assert.Equal(t, "market", decoded.Context()["var"].GetAttr("prefix").AsString())
	snapshot := decoded.Context()
	snapshot["var"] = cty.EmptyObjectVal
	assert.Equal(t, "market", decoded.Context()["var"].GetAttr("prefix").AsString())
}

func TestPlanLocalEvaluationRecipesRoundTrip(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals(
		"D:/research",
		nil,
		map[string]plan.OutputSpec{
			"summary": {Value: cty.UnknownVal(cty.String), Expression: "local.summary"},
		},
		map[string]cty.Value{
			"local": cty.ObjectVal(map[string]cty.Value{"summary": cty.UnknownVal(cty.String)}),
		},
		map[string]string{"summary": "upper(research.source.result)"},
	)
	require.NoError(t, err)

	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	decoded, err := plan.Unmarshal(encoded)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"summary": "upper(research.source.result)"}, decoded.LocalExpressions())
}

func TestPlanSaveKeepsSecretsUnencryptedButRedactsDisplay(t *testing.T) {
	t.Parallel()

	planned, err := plan.New("root", []plan.NodeSpec{{
		Address: "model_provider.primary",
		Kind:    "model_provider",
		Config: cty.ObjectVal(map[string]cty.Value{
			"endpoint": cty.StringVal("https://public.example.test"),
			"api_key":  corespec.MarkSensitive(cty.StringVal("literal-secret")),
		}),
	}}, map[string]plan.OutputSpec{
		"password": {Value: corespec.MarkSensitive(cty.StringVal("output-secret"))},
	})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "research.r42plan")

	warning, err := plan.Save(path, planned)
	require.NoError(t, err)
	assert.Contains(t, warning, "unencrypted")
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(contents, &document))
	encodedConfig, ok := configDocument(t, document)["value"].(string)
	require.True(t, ok)
	configBytes, err := base64.StdEncoding.DecodeString(encodedConfig)
	require.NoError(t, err)
	assert.Contains(t, string(configBytes), "literal-secret")
	display, err := plan.Display(planned)
	require.NoError(t, err)
	assert.Contains(t, display, "https://public.example.test")
	assert.Contains(t, display, "<sensitive>")
	assert.NotContains(t, display, "literal-secret")
	assert.NotContains(t, display, "output-secret")

	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestPlanLoadReturnsNormalDecodeErrors(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "broken.r42plan")
	require.NoError(t, os.WriteFile(path, []byte(`{"directory":`), 0o600))

	_, err := plan.Load(path)

	require.ErrorContains(t, err, "decode plan file")
}

func TestPlanUnmarshalRejectsSemanticValueAndPathErrors(t *testing.T) {
	t.Parallel()

	planned, err := plan.New("root", []plan.NodeSpec{{
		Address: "research.market",
		Kind:    "research",
		Config: cty.ObjectVal(map[string]cty.Value{
			"secret": corespec.MarkSensitive(cty.StringVal("value")),
		}),
	}}, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)

	tests := []struct {
		name          string
		mutate        func(*testing.T, map[string]any)
		expectedError string
	}{
		{
			name: "value does not match type",
			mutate: func(t *testing.T, document map[string]any) {
				t.Helper()
				configDocument(t, document)["type"] = "string"
			},
			expectedError: "decode value",
		},
		{
			name: "sensitive path has no selector",
			mutate: func(t *testing.T, document map[string]any) {
				t.Helper()
				configDocument(t, document)["sensitive_paths"] = []any{[]any{map[string]any{}}}
			},
			expectedError: "each step must contain one selector",
		},
		{
			name: "sensitive path does not exist",
			mutate: func(t *testing.T, document map[string]any) {
				t.Helper()
				configDocument(t, document)["sensitive_paths"] = []any{[]any{map[string]any{
					"attribute": "missing",
				}}}
			},
			expectedError: "sensitive path does not apply",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var document map[string]any
			require.NoError(t, json.Unmarshal(encoded, &document))
			tt.mutate(t, document)
			broken, marshalErr := json.Marshal(document)
			require.NoError(t, marshalErr)

			_, err := plan.Unmarshal(broken)

			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestPlanDisplayRendersSupportedCollectionValues(t *testing.T) {
	t.Parallel()

	planned, err := plan.New("root", []plan.NodeSpec{{
		Address: "research.values",
		Kind:    "research",
		Config: cty.ObjectVal(map[string]cty.Value{
			"bool":   cty.BoolVal(true),
			"list":   cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
			"null":   cty.NullVal(cty.String),
			"number": cty.NumberIntVal(42),
			"set":    cty.SetVal([]cty.Value{cty.StringVal("z"), cty.StringVal("a")}),
			"tuple":  cty.TupleVal([]cty.Value{cty.StringVal("x"), cty.NumberIntVal(1)}),
		}),
	}}, nil)
	require.NoError(t, err)

	display, err := plan.Display(planned)

	require.NoError(t, err)
	assert.Contains(t, display, `"bool": true`)
	assert.Contains(t, display, `"number": 42`)
	assert.Contains(t, display, `"null": null`)
	assert.Less(t, strings.Index(display, `"a"`), strings.Index(display, `"z"`))
}

func TestPlanFileOperationsReportOrdinaryErrors(t *testing.T) {
	t.Parallel()

	_, err := plan.Marshal(nil)
	require.EqualError(t, err, "plan is nil")
	_, err = plan.Display(nil)
	require.EqualError(t, err, "plan is nil")
	_, err = plan.Save(t.TempDir(), validPlan(t))
	require.ErrorContains(t, err, "open plan file")
	_, err = plan.Load(filepath.Join(t.TempDir(), "missing.r42plan"))
	require.ErrorContains(t, err, "read plan file")
}

func TestPlanObservesChangedUnhashedExternalContent(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	externalPath := filepath.Join(directory, "tool.sh")
	require.NoError(t, os.WriteFile(externalPath, []byte("first"), 0o600))
	planned, err := plan.New(directory, []plan.NodeSpec{{
		Address: "external_tool.lookup",
		Kind:    "external_tool",
		Config: cty.ObjectVal(map[string]cty.Value{
			"program_path": cty.StringVal(externalPath),
		}),
	}}, nil)
	require.NoError(t, err)
	planPath := filepath.Join(directory, "saved.r42plan")
	_, err = plan.Save(planPath, planned)
	require.NoError(t, err)
	loaded, err := plan.Load(planPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(externalPath, []byte("second"), 0o600))

	pathFromPlan := loaded.Nodes()[0].Config.GetAttr("program_path").AsString()
	observed, err := os.ReadFile(pathFromPlan)

	require.NoError(t, err)
	assert.Equal(t, "second", string(observed))
}

func configDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()

	nodes, ok := document["nodes"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, nodes)
	node, ok := nodes[0].(map[string]any)
	require.True(t, ok)
	config, ok := node["config"].(map[string]any)
	require.True(t, ok)
	return config
}

func validPlan(t *testing.T) *plan.Plan {
	t.Helper()

	planned, err := plan.New("root", nil, nil)
	require.NoError(t, err)
	return planned
}
