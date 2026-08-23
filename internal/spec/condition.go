package spec

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Condition is a saved HCL assertion evaluated against explicit runtime values.
type Condition struct {
	Expression   string `json:"expression"`
	ErrorMessage string `json:"error_message"`
}

// Evaluate checks a condition. A false known result returns its configured error;
// an unknown result is deferred to a later evaluation.
func (c Condition) Evaluate(
	variables map[string]cty.Value,
	functions map[string]function.Function,
) (bool, error) {
	if strings.TrimSpace(c.Expression) == "" {
		return false, fmt.Errorf("condition expression is required")
	}
	if strings.TrimSpace(c.ErrorMessage) == "" {
		return false, fmt.Errorf("condition error_message is required")
	}
	expression, diagnostics := hclsyntax.ParseExpression(
		[]byte(c.Expression), "saved-condition", hcl.InitialPos,
	)
	if diagnostics.HasErrors() {
		return false, fmt.Errorf("parse condition: %w", diagnostics)
	}
	for _, traversal := range expression.Variables() {
		root := traversal.RootName()
		if _, allowed := variables[root]; !allowed {
			return false, fmt.Errorf(
				"condition may only reference %s; found %q",
				strings.Join(sortedKeys(variables), ", "), root,
			)
		}
	}
	value, diagnostics := expression.Value(&hcl.EvalContext{
		Variables: variables,
		Functions: functions,
	})
	if diagnostics.HasErrors() {
		return false, fmt.Errorf("evaluate condition: %w", diagnostics)
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().Equals(cty.Bool) {
		return false, fmt.Errorf("condition must return bool, got %s", unmarked.Type().FriendlyName())
	}
	if !unmarked.IsWhollyKnown() {
		return false, nil
	}
	if unmarked.IsNull() {
		return false, fmt.Errorf("condition must not return null")
	}
	if !unmarked.True() {
		return true, fmt.Errorf("%s", strings.TrimSpace(c.ErrorMessage))
	}
	return true, nil
}

func sortedKeys(values map[string]cty.Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
