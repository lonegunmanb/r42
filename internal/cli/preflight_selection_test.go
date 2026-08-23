package cli

import (
	"encoding/json"
	"testing"

	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func TestSelectPreflightSelectionResolvesDynamicResearchProvider(t *testing.T) {
	t.Parallel()

	providerAddress := cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal("model_provider.primary"),
		"kind":    cty.StringVal("provider"),
	})
	toolUse := cty.ObjectVal(map[string]cty.Value{
		"name":      cty.StringVal("finish"),
		"tool_id":   cty.StringVal("tool_finish_12345678-1234-1234-1234-123456789012"),
		"terminate": cty.BoolVal(true),
		"input":     cty.EmptyObjectVal,
		"input_from_agent": cty.ObjectVal(map[string]cty.Value{
			"summary": cty.ObjectVal(map[string]cty.Value{
				"desc":    cty.StringVal("the task result"),
				"sources": cty.EmptyTupleVal,
			}),
		}),
	})
	task := cty.ObjectVal(map[string]cty.Value{
		"model_provider": providerAddress,
		"model":          cty.StringVal("task-model"),
		"system_prompt":  cty.StringVal("research"),
		"tool_uses":      cty.ListVal([]cty.Value{toolUse}),
	})
	tasks := cty.ListVal([]cty.Value{task})
	typeJSON, err := ctyjson.MarshalType(tasks.Type())
	require.NoError(t, err)
	valueJSON, err := ctyjson.Marshal(tasks, tasks.Type())
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"expression": "[{}]",
		"providers": map[string]any{
			"model_provider.primary": map[string]any{
				"type":     "openai",
				"endpoint": "https://example.test",
			},
		},
		"tasks": map[string]json.RawMessage{
			"type":  typeJSON,
			"value": valueJSON,
		},
	})
	require.NoError(t, err)

	planned, err := plan.NewWithContextAndLocals(
		t.TempDir(),
		[]plan.NodeSpec{{
			Address: "research.dynamic.followups",
			Kind:    "research",
			Config: cty.ObjectVal(map[string]cty.Value{
				"model":   cty.StringVal("<dynamic>"),
				"payload": cty.StringVal(string(payload)),
			}),
		}},
		nil, nil, nil,
	)
	require.NoError(t, err)

	selection, err := selectPreflightSelection(planned, "")
	require.NoError(t, err)
	require.NotNil(t, selection.Provider)
	require.Equal(t, provider.Type("openai"), selection.Provider.Type)
	require.Equal(t, "task-model", selection.Model)
}

func TestSelectPreflightSelectionUsesSingleProviderPreflightModel(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(map[string]any{
		"model": "configured-model",
		"provider": map[string]any{
			"type":            "openai",
			"endpoint":        "https://example.test",
			"preflight_model": "configured-preflight-model",
		},
	})
	require.NoError(t, err)
	planned, err := plan.NewWithContextAndLocals(
		t.TempDir(),
		[]plan.NodeSpec{{
			Address: "research.static.source",
			Kind:    "research",
			Config: cty.ObjectVal(map[string]cty.Value{
				"model":   cty.StringVal("configured-model"),
				"payload": cty.StringVal(string(payload)),
			}),
		}},
		nil, nil, nil,
	)
	require.NoError(t, err)

	selection, err := selectPreflightSelection(planned, "")
	require.NoError(t, err)
	require.NotNil(t, selection.Provider)
	require.Equal(t, "configured-preflight-model", selection.Model)
}
