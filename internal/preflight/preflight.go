// Package preflight validates the plan-only contract used by the preflight model.
package preflight

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const (
	VerdictSufficient   = "sufficient"
	VerdictInsufficient = "insufficient"
	VerdictAmbiguous    = "ambiguous_contract"
)

type Input struct {
	Checks []Check `json:"checks"`
}

type Check struct {
	CheckID string           `json:"check_id"`
	Verdict string           `json:"verdict"`
	Reason  string           `json:"reason"`
	Issues  []corespec.Issue `json:"issues,omitempty"`
}

type Output struct{}

type ExpectedCheck struct{ ID string }

func (i Input) Validate(expected []ExpectedCheck) error {
	want := make(map[string]struct{}, len(expected))
	for _, item := range expected {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("expected preflight check id is empty")
		}
		want[item.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(i.Checks))
	for index, check := range i.Checks {
		if _, ok := want[check.CheckID]; !ok {
			return fmt.Errorf("checks[%d] has unknown check_id %q", index, check.CheckID)
		}
		if _, duplicate := seen[check.CheckID]; duplicate {
			return fmt.Errorf("check_id %q is duplicated", check.CheckID)
		}
		seen[check.CheckID] = struct{}{}
		if strings.TrimSpace(check.Reason) == "" {
			return fmt.Errorf("check %q reason is required", check.CheckID)
		}
		switch check.Verdict {
		case VerdictSufficient:
			if len(check.Issues) != 0 {
				return fmt.Errorf("sufficient check %q must not contain issues", check.CheckID)
			}
		case VerdictInsufficient, VerdictAmbiguous:
			if len(check.Issues) == 0 {
				return fmt.Errorf("%s check %q must contain issues", check.Verdict, check.CheckID)
			}
		default:
			return fmt.Errorf("check %q verdict must be one of sufficient, insufficient, or ambiguous_contract", check.CheckID)
		}
		for issueIndex, issue := range check.Issues {
			if err := issue.Validate(); err != nil {
				return fmt.Errorf("check %q issue %d: %w", check.CheckID, issueIndex, err)
			}
		}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("preflight must submit exactly %d checks; got %d", len(want), len(seen))
	}
	return nil
}

type PlanDocument struct {
	Nodes []NodeDocument `json:"nodes"`
	Tools []ToolDocument `json:"tools"`
}

type NodeDocument struct {
	Address string          `json:"address"`
	Kind    string          `json:"kind"`
	Origin  plan.Origin     `json:"origin"`
	Config  json.RawMessage `json:"config,omitempty"`
}

type ToolDocument struct {
	ID             string               `json:"id"`
	Address        string               `json:"address"`
	Description    string               `json:"description"`
	Origin         plan.Origin          `json:"origin"`
	Postconditions []corespec.Condition `json:"postconditions,omitempty"`
}

func Document(planned *plan.Plan) (PlanDocument, []ExpectedCheck, error) {
	if planned == nil {
		return PlanDocument{}, nil, fmt.Errorf("plan is required")
	}
	nodes := planned.Nodes()
	document := PlanDocument{Nodes: make([]NodeDocument, 0, len(nodes))}
	expected := make([]ExpectedCheck, 0)
	for _, node := range nodes {
		encoded, err := encodeConfig(node.Config)
		if err != nil {
			return PlanDocument{}, nil, fmt.Errorf("encode %s config: %w", node.Address, err)
		}
		document.Nodes = append(document.Nodes, NodeDocument{Address: node.Address, Kind: node.Kind, Origin: node.Origin, Config: encoded})
		if node.Kind != "research" {
			continue
		}
		decoded, err := modulespec.DecodeResearchPlan(node.Config)
		if err == nil && decoded.Expression == "" {
			expected = appendToolUseChecks(expected, node.Address, decoded.Config.ToolUses)
			continue
		}
		dynamic, dynamicErr := modulespec.DecodeDynamicResearchPlan(node.Config)
		if dynamicErr != nil || dynamic.Tasks == cty.NilVal || !dynamic.Tasks.IsWhollyKnown() {
			continue
		}
		configs, _, decodeErr := researchspec.DecodeDynamicTasks(dynamic.Tasks)
		if decodeErr != nil {
			return PlanDocument{}, nil, fmt.Errorf("decode %s dynamic tasks: %w", node.Address, decodeErr)
		}
		for index, config := range configs {
			taskAddress := fmt.Sprintf("%s.tasks[%d]", node.Address, index)
			expected = appendToolUseChecks(expected, taskAddress, config.ToolUses)
		}
	}
	for id, tool := range planned.Tools() {
		document.Tools = append(document.Tools, ToolDocument{ID: id, Address: tool.Address, Description: tool.Description, Origin: tool.Origin, Postconditions: tool.Postconditions})
	}
	sort.Slice(document.Tools, func(i, j int) bool { return document.Tools[i].ID < document.Tools[j].ID })
	return document, expected, nil
}

func encodeConfig(value cty.Value) (json.RawMessage, error) {
	unmarked, _ := value.UnmarkDeep()
	if unmarked.IsWhollyKnown() {
		return ctyjson.Marshal(unmarked, unmarked.Type())
	}
	return json.Marshal(map[string]any{
		"unknown": true,
		"type":    unmarked.Type().FriendlyName(),
	})
}

func appendToolUseChecks(expected []ExpectedCheck, address string, uses []researchspec.ToolUse) []ExpectedCheck {
	for _, use := range uses {
		for field := range valueFields(use.InputFromAgent) {
			expected = append(expected, ExpectedCheck{ID: address + "/tool_use/" + use.Name + "/input_from_agent/" + field})
		}
	}
	return expected
}

func (d PlanDocument) JSON() ([]byte, error) { return json.Marshal(d) }

func valueFields(value cty.Value) map[string]cty.Value {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) || value.IsNull() {
		return nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return nil
	}
	return unmarked.AsValueMap()
}
