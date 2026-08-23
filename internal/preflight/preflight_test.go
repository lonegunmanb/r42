package preflight_test

import (
	"encoding/json"
	"testing"

	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/preflight"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func TestInputValidateRequiresExactlyExpectedChecks(t *testing.T) {
	t.Parallel()

	expected := []preflight.ExpectedCheck{{ID: "research.static.baseline/tool_use/finish/input_from_agent/claims"}}
	tests := []struct {
		name  string
		input preflight.Input
		want  string
	}{
		{
			name: "valid sufficient",
			input: preflight.Input{Checks: []preflight.Check{{
				CheckID: expected[0].ID, Verdict: preflight.VerdictSufficient, Reason: "claims are in reviewed snapshots",
			}}},
		},
		{
			name: "missing check", input: preflight.Input{}, want: "exactly 1 checks",
		},
		{
			name: "insufficient requires issue", input: preflight.Input{Checks: []preflight.Check{{
				CheckID: expected[0].ID, Verdict: preflight.VerdictInsufficient, Reason: "missing",
			}}}, want: "must contain issues",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.input.Validate(expected)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestInputValidateAcceptsAmbiguousContractIssue(t *testing.T) {
	t.Parallel()

	err := (preflight.Input{Checks: []preflight.Check{{
		CheckID: "check", Verdict: preflight.VerdictAmbiguous, Reason: "description is too broad",
		Issues: []corespec.Issue{{Code: "ambiguous_contract", Message: "source semantics are unclear"}},
	}}}).Validate([]preflight.ExpectedCheck{{ID: "check"}})
	require.NoError(t, err)
}

func TestDocumentRepresentsUnknownConfigWithoutReadingFiles(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals(".", []plan.NodeSpec{{
		Address: "research.static.unknown", Kind: "research",
		Config: cty.ObjectVal(map[string]cty.Value{
			"payload": cty.UnknownVal(cty.String),
		}),
		Origin: plan.Origin{Filename: "main.r42.hcl", Source: "research \"static\" \"unknown\" {}"},
	}}, nil, nil, nil)
	require.NoError(t, err)
	document, _, err := preflight.Document(planned)
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, json.Unmarshal(document.Nodes[0].Config, &config))
	assert.Equal(t, true, config["unknown"])
}

func TestDocumentIncludesDynamicTaskToolUseChecks(t *testing.T) {
	t.Parallel()

	toolUse := cty.ObjectVal(map[string]cty.Value{
		"name":      cty.StringVal("finish"),
		"tool_id":   cty.StringVal("tool_finish_12345678-1234-1234-1234-123456789012"),
		"terminate": cty.BoolVal(true),
		"input":     cty.EmptyObjectVal,
		"input_from_agent": cty.ObjectVal(map[string]cty.Value{
			"summary": cty.StringVal("task output"),
		}),
	})
	task := cty.ObjectVal(map[string]cty.Value{
		"model":         cty.StringVal("task-model"),
		"system_prompt": cty.StringVal("research"),
		"tool_uses":     cty.ListVal([]cty.Value{toolUse}),
	})
	tasks := cty.ListVal([]cty.Value{task})
	typeJSON, err := ctyjson.MarshalType(tasks.Type())
	require.NoError(t, err)
	valueJSON, err := ctyjson.Marshal(tasks, tasks.Type())
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"expression": "[{}]",
		"tasks": map[string]json.RawMessage{
			"type":  typeJSON,
			"value": valueJSON,
		},
	})
	require.NoError(t, err)

	planned, err := plan.NewWithContextAndLocals(".", []plan.NodeSpec{{
		Address: "research.dynamic.followups",
		Kind:    "research",
		Config: cty.ObjectVal(map[string]cty.Value{
			"model":   cty.StringVal("<dynamic>"),
			"payload": cty.StringVal(string(payload)),
		}),
		Origin: plan.Origin{Filename: "main.r42.hcl"},
	}}, nil, nil, nil)
	require.NoError(t, err)

	_, expected, err := preflight.Document(planned)
	require.NoError(t, err)
	require.Equal(t, []preflight.ExpectedCheck{{
		ID: "research.dynamic.followups.tasks[0]/tool_use/finish/input_from_agent/summary",
	}}, expected)
}
