package spec

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

var researchAttributeNames = map[string]struct{}{
	"model_provider": {}, "model": {}, "profile": {}, "reasoning_effort": {}, "system_prompt": {}, "prompt": {},
	"tool_ids": {}, "tool_call_quota": {}, "terminate_tool_id": {}, "allowed_tools": {},
	"disallowed_tools": {}, "skill_directories": {}, "skills": {}, "disabled_skills": {},
	"permission": {}, "max_protocol_attempts": {}, "timeout": {},
	"collection_model_provider": {}, "collection_tool_ids": {}, "collection_skill_directories": {}, "collection_skills": {},
	"collection_disabled_skills": {}, "collection_batch_size": {}, "max_collection_rounds": {},
}

func (b *ResearchBlock) Decode(block *golden.HclBlock, context *hcl.EvalContext) error {
	b.deferredTaskExpression = ""
	b.plannedTaskValue = cty.NilVal
	expanded := block
	var err error
	if researchHasDynamicBlocks(block) {
		expanded, err = block.ExpandDynamicBlocks(context)
		if err != nil {
			return err
		}
	}
	diagnostics := gohcl.DecodeBody(researchDecodeBody(expanded), context, b)
	if !diagnostics.HasErrors() {
		if err := decodeToolUseValidations(expanded, context, b); err != nil {
			return err
		}
		return nil
	}
	expression, err := staticResearchTaskExpression(expanded)
	if err != nil {
		return err
	}
	parsed, parseDiagnostics := hclsyntax.ParseExpression(
		[]byte(expression), block.Range().Filename, block.Range().Start,
	)
	if parseDiagnostics.HasErrors() {
		return parseDiagnostics
	}
	value, valueDiagnostics := parsed.Value(context)
	if valueDiagnostics.HasErrors() {
		return diagnostics
	}
	if value.IsWhollyKnown() {
		return diagnostics
	}
	b.deferredTaskExpression = expression
	b.plannedTaskValue = value
	return nil
}

func researchHasDynamicBlocks(block *golden.HclBlock) bool {
	if block == nil {
		return false
	}
	for _, nested := range block.NestedBlocks() {
		if nested.Type == "dynamic" || researchHasDynamicBlocks(nested) {
			return true
		}
	}
	return false
}

func researchDecodeBody(block *golden.HclBlock) *hclsyntax.Body {
	body := &hclsyntax.Body{Attributes: make(hclsyntax.Attributes)}
	for name, attribute := range block.Body.Attributes {
		if golden.MetaAttributeNames.Contains(name) {
			continue
		}
		body.Attributes[name] = attribute
	}
	for _, nested := range block.Body.Blocks {
		if golden.MetaNestedBlockNames.Contains(nested.Type) {
			continue
		}
		if nested.Type != "tool_use" {
			body.Blocks = append(body.Blocks, nested)
			continue
		}
		copyBlock := *nested
		copyBody := *nested.Body
		copyBody.Blocks = make(hclsyntax.Blocks, 0, len(nested.Body.Blocks))
		for _, child := range nested.Body.Blocks {
			if child.Type != "validation" {
				copyBody.Blocks = append(copyBody.Blocks, child)
			}
		}
		copyBlock.Body = &copyBody
		body.Blocks = append(body.Blocks, &copyBlock)
	}
	return body
}

func decodeToolUseValidations(
	block *golden.HclBlock,
	context *hcl.EvalContext,
	research *ResearchBlock,
) error {
	toolBlocks := nestedBlocks(block, "tool_use")
	if len(toolBlocks) != len(research.ToolUseBlocks) {
		return fmt.Errorf("tool_use validation decoding lost block alignment")
	}
	for index, toolBlock := range toolBlocks {
		validations, err := decodeValidationBlocks(toolBlock, context)
		if err != nil {
			return fmt.Errorf("tool_use %q validation: %w", research.ToolUseBlocks[index].Name, err)
		}
		research.ToolUseBlocks[index].validations = validations
	}
	return nil
}

func decodeValidationBlocks(block *golden.HclBlock, context *hcl.EvalContext) ([]corespec.Condition, error) {
	result := make([]corespec.Condition, 0)
	for _, nested := range block.NestedBlocks() {
		if nested.Type != "validation" {
			return nil, fmt.Errorf("unsupported block type %q", nested.Type)
		}
		for name := range nested.Attributes() {
			if name != "condition" && name != "error_message" {
				return nil, fmt.Errorf("unsupported validation argument %q", name)
			}
		}
		conditionAttribute, ok := nested.Attributes()["condition"]
		if !ok {
			return nil, fmt.Errorf("condition is required")
		}
		errorAttribute, ok := nested.Attributes()["error_message"]
		if !ok {
			return nil, fmt.Errorf("error_message is required")
		}
		errorValue, diagnostics := errorAttribute.Expr.Value(context)
		if diagnostics.HasErrors() {
			return nil, diagnostics
		}
		errorValue, _ = errorValue.UnmarkDeep()
		if !errorValue.IsWhollyKnown() || errorValue.IsNull() || !errorValue.Type().Equals(cty.String) {
			return nil, fmt.Errorf("error_message must be a known string")
		}
		condition := corespec.Condition{
			Expression:   conditionAttribute.ExprString(),
			ErrorMessage: errorValue.AsString(),
		}
		if err := validateConditionRoots(condition.Expression, "input"); err != nil {
			return nil, err
		}
		result = append(result, condition)
	}
	return result, nil
}

func validateConditionRoots(source, allowedRoot string) error {
	expression, diagnostics := hclsyntax.ParseExpression([]byte(source), "validation", hcl.InitialPos)
	if diagnostics.HasErrors() {
		return fmt.Errorf("parse condition: %w", diagnostics)
	}
	for _, traversal := range expression.Variables() {
		if traversal.RootName() != allowedRoot {
			return fmt.Errorf("condition may only reference %s; found %q", allowedRoot, traversal.RootName())
		}
	}
	return nil
}

func staticResearchTaskExpression(block *golden.HclBlock) (string, error) {
	for name := range block.Attributes() {
		if golden.MetaAttributeNames.Contains(name) {
			continue
		}
		if _, ok := researchAttributeNames[name]; !ok {
			return "", fmt.Errorf("unsupported argument %q in research block", name)
		}
	}
	for _, required := range []string{"model", "system_prompt"} {
		if _, ok := block.Attributes()[required]; !ok {
			return "", fmt.Errorf("research %s is required", required)
		}
	}

	retryBlocks := nestedBlocks(block, "retry")
	qcBlocks := nestedBlocks(block, "qc")
	collectionQCBlocks := nestedBlocks(block, "collection_qc")
	if len(retryBlocks) > 1 {
		return "", fmt.Errorf("research supports at most one retry block")
	}
	if len(qcBlocks) > 1 {
		return "", fmt.Errorf("research supports at most one qc block")
	}
	if len(collectionQCBlocks) > 1 {
		return "", fmt.Errorf("research supports at most one collection_qc block")
	}
	for _, nested := range block.NestedBlocks() {
		if nested.Type == "retry" || nested.Type == "artifact" || nested.Type == "qc" ||
			nested.Type == "collection_qc" || nested.Type == "tool_use" || golden.MetaNestedBlockNames.Contains(nested.Type) {
			continue
		}
		return "", fmt.Errorf("unsupported block type %q in research block", nested.Type)
	}

	var result strings.Builder
	result.WriteString("{\n")
	writeExpressionAttributes(&result, block.Attributes(), researchAttributeNames)
	result.WriteString("retry = ")
	if len(retryBlocks) == 0 {
		result.WriteString("null\n")
	} else {
		writeObject(&result, retryBlocks[0], nil)
	}
	result.WriteString("artifacts = [\n")
	for _, artifact := range nestedBlocks(block, "artifact") {
		name := ""
		if len(artifact.Labels) > 0 {
			name = artifact.Labels[len(artifact.Labels)-1]
		}
		writeObject(&result, artifact, map[string]string{
			"name": strconv.Quote(name), "required": "false", "non_empty": "false",
		})
		result.WriteString(",\n")
	}
	result.WriteString("]\n")
	result.WriteString("tool_uses = [\n")
	for _, toolUse := range nestedBlocks(block, "tool_use") {
		name := ""
		if len(toolUse.Labels) > 0 {
			name = toolUse.Labels[len(toolUse.Labels)-1]
		}
		writeToolUseObject(&result, toolUse, map[string]string{
			"name": strconv.Quote(name), "terminate": "false", "input": "{}", "input_from_agent": "{}",
		})
		result.WriteString(",\n")
	}
	result.WriteString("]\nqc = ")
	if len(qcBlocks) == 0 {
		result.WriteString("null\n")
	} else {
		if err := writeQCObject(&result, qcBlocks[0]); err != nil {
			return "", err
		}
	}
	result.WriteString("collection_qc = ")
	if len(collectionQCBlocks) == 0 {
		result.WriteString("null\n")
	} else {
		if err := writeCollectionQCObject(&result, collectionQCBlocks[0]); err != nil {
			return "", err
		}
	}
	result.WriteString("}\n")
	return result.String(), nil
}

func writeToolUseObject(result *strings.Builder, block *golden.HclBlock, defaults map[string]string) {
	result.WriteString("{\n")
	for _, name := range sortedKeys(defaults) {
		if _, exists := block.Attributes()[name]; !exists {
			fmt.Fprintf(result, "%s = %s\n", name, defaults[name])
		}
	}
	writeExpressionAttributes(result, block.Attributes(), nil)
	validations := nestedBlocks(block, "validation")
	if len(validations) > 0 {
		result.WriteString("validation = [\n")
		for _, validation := range validations {
			condition, conditionOK := validation.Attributes()["condition"]
			errorMessage, errorOK := validation.Attributes()["error_message"]
			if !conditionOK || !errorOK {
				continue
			}
			fmt.Fprintf(result, "{ condition = %s, error_message = %s },\n",
				strconv.Quote(condition.ExprString()), errorMessage.ExprString())
		}
		result.WriteString("]\n")
	}
	result.WriteString("}\n")
}

func nestedBlocks(block *golden.HclBlock, blockType string) []*golden.HclBlock {
	var result []*golden.HclBlock
	for _, nested := range block.NestedBlocks() {
		if nested.Type == blockType {
			result = append(result, nested)
		}
	}
	return result
}

func writeQCObject(result *strings.Builder, block *golden.HclBlock) error {
	for _, nested := range block.NestedBlocks() {
		if nested.Type == "retry" || golden.MetaNestedBlockNames.Contains(nested.Type) {
			continue
		}
		return fmt.Errorf("unsupported block type %q in qc block", nested.Type)
	}
	retryBlocks := nestedBlocks(block, "retry")
	if len(retryBlocks) > 1 {
		return fmt.Errorf("qc supports at most one retry block")
	}
	result.WriteString("{\n")
	writeExpressionAttributes(result, block.Attributes(), nil)
	result.WriteString("retry = ")
	if len(retryBlocks) == 0 {
		result.WriteString("null\n")
	} else {
		writeObject(result, retryBlocks[0], nil)
	}
	result.WriteString("}\n")
	return nil
}

func writeCollectionQCObject(result *strings.Builder, block *golden.HclBlock) error {
	for _, nested := range block.NestedBlocks() {
		if nested.Type == "retry" || golden.MetaNestedBlockNames.Contains(nested.Type) {
			continue
		}
		return fmt.Errorf("unsupported block type %q in collection_qc block", nested.Type)
	}
	retryBlocks := nestedBlocks(block, "retry")
	if len(retryBlocks) > 1 {
		return fmt.Errorf("collection qc supports at most one retry block")
	}
	result.WriteString("{\n")
	writeExpressionAttributes(result, block.Attributes(), nil)
	result.WriteString("retry = ")
	if len(retryBlocks) == 0 {
		result.WriteString("null\n")
	} else {
		writeObject(result, retryBlocks[0], nil)
	}
	result.WriteString("}\n")
	return nil
}

func writeObject(result *strings.Builder, block *golden.HclBlock, defaults map[string]string) {
	result.WriteString("{\n")
	for _, name := range sortedKeys(defaults) {
		if _, exists := block.Attributes()[name]; !exists {
			fmt.Fprintf(result, "%s = %s\n", name, defaults[name])
		}
	}
	writeExpressionAttributes(result, block.Attributes(), nil)
	result.WriteString("}\n")
}

func writeExpressionAttributes(
	result *strings.Builder,
	attributes map[string]*golden.HclAttribute,
	allowed map[string]struct{},
) {
	for _, name := range sortedKeys(attributes) {
		if golden.MetaAttributeNames.Contains(name) {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		fmt.Fprintf(result, "%s = %s\n", name, attributes[name].ExprString())
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func deferredStaticResearchValues(task cty.Value) map[string]cty.Value {
	values := map[string]cty.Value{
		"model_provider": cty.NullVal(cty.EmptyObject), "profile": cty.UnknownVal(cty.String),
		"reasoning_effort": cty.NullVal(cty.String),
		"prompt":           cty.NullVal(cty.String), "tool_ids": cty.EmptyTupleVal,
		"tool_call_quota": cty.EmptyObjectVal, "terminate_tool_id": cty.NullVal(cty.String),
		"allowed_tools": cty.EmptyTupleVal, "disallowed_tools": cty.EmptyTupleVal,
		"skill_directories": cty.EmptyTupleVal, "skills": cty.EmptyTupleVal,
		"disabled_skills": cty.EmptyTupleVal, "permission": cty.NullVal(cty.String),
		"max_protocol_attempts": cty.NullVal(cty.Number), "timeout": cty.NullVal(cty.String),
		"retry": cty.EmptyTupleVal, "artifact": cty.EmptyTupleVal, "tool_use": cty.EmptyTupleVal, "qc": cty.EmptyTupleVal,
		"collection_model_provider":    cty.NullVal(cty.EmptyObject),
		"collection_tool_ids":          cty.EmptyTupleVal,
		"collection_skill_directories": cty.EmptyTupleVal,
		"collection_skills":            cty.EmptyTupleVal,
		"collection_disabled_skills":   cty.EmptyTupleVal,
		"collection_batch_size":        cty.NumberIntVal(DefaultCollectionBatchSize),
		"max_collection_rounds":        cty.NullVal(cty.Number),
		"collection_qc":                cty.EmptyTupleVal,
	}
	if task.IsKnown() && task.Type().IsObjectType() {
		for name, value := range task.AsValueMap() {
			switch name {
			case "artifacts":
				values["artifact"] = value
			case "tool_uses":
				values["tool_use"] = value
			case "retry", "qc", "collection_qc":
				if !value.IsNull() {
					values[name] = cty.TupleVal([]cty.Value{value})
				}
			default:
				values[name] = value
			}
		}
		if _, exists := task.Type().AttributeTypes()["profile"]; !exists || task.GetAttr("profile").IsNull() {
			values["profile"] = values["model"]
		}
	}
	if _, exists := task.Type().AttributeTypes()["terminate_tool_id"]; exists {
		values["result"] = cty.UnknownVal(cty.String)
	}
	return values
}
