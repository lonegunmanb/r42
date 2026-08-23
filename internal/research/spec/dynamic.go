package spec

import (
	"fmt"
	"maps"
	"math/big"

	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/debuglog"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

var (
	_ golden.PlanBlock  = (*DynamicResearchBlock)(nil)
	_ golden.ApplyBlock = (*DynamicResearchBlock)(nil)
	_ golden.Valuable   = (*DynamicResearchBlock)(nil)
)

type DynamicResearchBlock struct {
	*golden.BaseBlock
	Serial bool      `hcl:"serial,optional"`
	Tasks  cty.Value `hcl:"tasks"`

	plannedTasks cty.Value
}

func (*DynamicResearchBlock) Type() string { return "dynamic" }

func (*DynamicResearchBlock) BlockType() string { return "research" }

func (*DynamicResearchBlock) AddressLength() int { return 3 }

func (*DynamicResearchBlock) CanExecutePrePlan() bool { return false }

func (b *DynamicResearchBlock) EvalContext() *hcl.EvalContext {
	return researchBlockEvalContext(b.BaseBlock)
}

func (b *DynamicResearchBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		planned, err := PlanDynamicTasks(b.Tasks)
		if err != nil {
			return err
		}
		b.plannedTasks = planned
		return nil
	})
}

func (b *DynamicResearchBlock) Apply() error {
	applier, ok := b.Config().(blockApplier)
	if !ok {
		return fmt.Errorf("research %q requires an r42 apply config", b.Name())
	}
	return applier.ApplyBlock(b.Address())
}

func (b *DynamicResearchBlock) Values() map[string]cty.Value {
	value := b.plannedTasks
	if value == cty.NilVal {
		value = cty.DynamicVal
	}
	return map[string]cty.Value{"tasks": value}
}

func (b *DynamicResearchBlock) TasksExpression() string {
	attribute, ok := b.HclBlock().Attributes()["tasks"]
	if !ok {
		return ""
	}
	return attribute.ExprString()
}

func PlanDynamicTasks(value cty.Value) (cty.Value, error) {
	unmarked, marks := value.UnmarkDeepWithPaths()
	if !unmarked.IsKnown() {
		plannedType, err := dynamicTasksOutputType(unmarked.Type())
		if err != nil {
			return cty.NilVal, err
		}
		return cty.UnknownVal(plannedType).MarkWithPaths(marks), nil
	}
	if !unmarked.Type().IsListType() && !unmarked.Type().IsTupleType() {
		return cty.NilVal, fmt.Errorf("dynamic research tasks must be a list")
	}
	if unmarked.LengthInt() == 0 {
		return cty.EmptyTupleVal.MarkWithPaths(marks), nil
	}
	result := make([]cty.Value, 0, unmarked.LengthInt())
	marked, inheritedMarks := unmarked.MarkWithPaths(marks).Unmark()
	iterator := marked.ElementIterator()
	for index := 0; iterator.Next(); index++ {
		_, task := iterator.Element()
		if !task.IsWhollyKnown() {
			result = append(result, cty.UnknownVal(dynamicTaskOutputType(task.Type())))
			continue
		}
		config, err := DecodeDynamicTask(task)
		if err != nil {
			return cty.NilVal, fmt.Errorf("dynamic research task %d: %w", index, err)
		}
		result = append(result, plannedDynamicTaskValue(task, config))
	}
	return cty.TupleVal(result).WithMarks(inheritedMarks), nil
}

func DecodeDynamicTasks(value cty.Value) ([]Config, []cty.Value, error) {
	unmarked, marks := value.UnmarkDeepWithPaths()
	if !unmarked.IsWhollyKnown() {
		return nil, nil, fmt.Errorf("dynamic research tasks must be known before apply")
	}
	if !unmarked.Type().IsListType() && !unmarked.Type().IsTupleType() {
		return nil, nil, fmt.Errorf("dynamic research tasks must be a list")
	}
	configs := make([]Config, 0, unmarked.LengthInt())
	values := make([]cty.Value, 0, unmarked.LengthInt())
	marked, inheritedMarks := unmarked.MarkWithPaths(marks).Unmark()
	iterator := marked.ElementIterator()
	for index := 0; iterator.Next(); index++ {
		_, task := iterator.Element()
		config, err := DecodeDynamicTask(task)
		if err != nil {
			return nil, nil, fmt.Errorf("dynamic research task %d: %w", index, err)
		}
		configs = append(configs, config)
		values = append(values, dynamicTaskValueWithProfile(task).WithMarks(inheritedMarks))
	}
	return configs, values, nil
}

func DecodeDynamicTask(value cty.Value) (Config, error) {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsWhollyKnown() {
		return Config{}, fmt.Errorf("task must be wholly known")
	}
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return Config{}, fmt.Errorf("task must be an object")
	}

	block := &ResearchBlock{ModelProvider: cty.NilVal}
	var err error
	if block.ModelProvider, err = dynamicOptionalValue(unmarked, "model_provider"); err != nil {
		return Config{}, err
	}
	if block.Model, err = dynamicRequiredString(unmarked, "model"); err != nil {
		return Config{}, err
	}
	if block.Profile, err = dynamicOptionalString(unmarked, "profile"); err != nil {
		return Config{}, err
	}
	if block.ReasoningEffort, err = dynamicOptionalString(unmarked, "reasoning_effort"); err != nil {
		return Config{}, err
	}
	if block.SystemPrompt, err = dynamicRequiredString(unmarked, "system_prompt"); err != nil {
		return Config{}, err
	}
	if block.Prompt, err = dynamicOptionalString(unmarked, "prompt"); err != nil {
		return Config{}, err
	}
	if block.ToolIDs, err = dynamicStringList(unmarked, "tool_ids"); err != nil {
		return Config{}, err
	}
	if block.ToolUseBlocks, err = dynamicToolUseBlocks(unmarked); err != nil {
		return Config{}, err
	}
	if block.ToolCallQuota, err = dynamicIntMap(unmarked, "tool_call_quota"); err != nil {
		return Config{}, err
	}
	if block.TerminateToolID, err = dynamicOptionalString(unmarked, "terminate_tool_id"); err != nil {
		return Config{}, err
	}
	if block.AllowedTools, err = dynamicStringList(unmarked, "allowed_tools"); err != nil {
		return Config{}, err
	}
	if block.DisallowedTools, err = dynamicStringList(unmarked, "disallowed_tools"); err != nil {
		return Config{}, err
	}
	if block.SkillDirectories, err = dynamicStringList(unmarked, "skill_directories"); err != nil {
		return Config{}, err
	}
	if block.Skills, err = dynamicStringList(unmarked, "skills"); err != nil {
		return Config{}, err
	}
	if block.DisabledSkills, err = dynamicStringList(unmarked, "disabled_skills"); err != nil {
		return Config{}, err
	}
	permission, err := dynamicOptionalString(unmarked, "permission")
	if err != nil {
		return Config{}, err
	}
	if permission != nil {
		value := Permission(*permission)
		block.Permission = &value
	}
	if block.MaxProtocolAttempts, err = dynamicOptionalInt(unmarked, "max_protocol_attempts"); err != nil {
		return Config{}, err
	}
	if block.Timeout, err = dynamicOptionalString(unmarked, "timeout"); err != nil {
		return Config{}, err
	}
	if block.RetryBlocks, err = dynamicRetryBlocks(unmarked); err != nil {
		return Config{}, err
	}
	if block.ArtifactBlocks, err = dynamicArtifactBlocks(unmarked); err != nil {
		return Config{}, err
	}
	if block.QCBlocks, err = dynamicQCBlocks(unmarked); err != nil {
		return Config{}, err
	}
	if block.CollectionModelProvider, err = dynamicOptionalValue(unmarked, "collection_model_provider"); err != nil {
		return Config{}, err
	}
	if block.CollectionToolIDs, err = dynamicStringList(unmarked, "collection_tool_ids"); err != nil {
		return Config{}, err
	}
	if block.CollectionSkillDirectories, err = dynamicStringList(unmarked, "collection_skill_directories"); err != nil {
		return Config{}, err
	}
	if block.CollectionSkills, err = dynamicStringList(unmarked, "collection_skills"); err != nil {
		return Config{}, err
	}
	if block.CollectionDisabledSkills, err = dynamicStringList(unmarked, "collection_disabled_skills"); err != nil {
		return Config{}, err
	}
	if block.CollectionBatchSize, err = dynamicOptionalInt(unmarked, "collection_batch_size"); err != nil {
		return Config{}, err
	}
	if block.MaxCollectionRounds, err = dynamicOptionalInt(unmarked, "max_collection_rounds"); err != nil {
		return Config{}, err
	}
	if block.CollectionQCBlocks, err = dynamicCollectionQCBlocks(unmarked); err != nil {
		return Config{}, err
	}
	config, err := block.toConfig()
	if err != nil {
		return Config{}, err
	}
	if err = config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func dynamicToolUseBlocks(object cty.Value) ([]ToolUseBlock, error) {
	value, ok := dynamicAttribute(object, "tool_uses")
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsListType() && !unmarked.Type().IsTupleType() {
		return nil, fmt.Errorf("tool_uses must be a list")
	}
	result := make([]ToolUseBlock, 0, unmarked.LengthInt())
	iterator := unmarked.ElementIterator()
	for index := 0; iterator.Next(); index++ {
		_, item := iterator.Element()
		name, err := dynamicRequiredString(item, "name")
		if err != nil {
			return nil, fmt.Errorf("tool_use %d: %w", index, err)
		}
		toolID, err := dynamicRequiredString(item, "tool_id")
		if err != nil {
			return nil, fmt.Errorf("tool_use %d: %w", index, err)
		}
		terminate, err := dynamicOptionalBool(item, "terminate")
		if err != nil {
			return nil, fmt.Errorf("tool_use %d: %w", index, err)
		}
		input, err := dynamicOptionalValue(item, "input")
		if err != nil {
			return nil, fmt.Errorf("tool_use %d: %w", index, err)
		}
		agent, err := dynamicOptionalValue(item, "input_from_agent")
		if err != nil {
			return nil, fmt.Errorf("tool_use %d: %w", index, err)
		}
		validations, err := dynamicValidations(item)
		if err != nil {
			return nil, fmt.Errorf("tool_use %d: %w", index, err)
		}
		result = append(result, ToolUseBlock{
			Name: name, ToolID: toolID, Terminate: terminate, Input: input, InputFromAgent: agent,
			validations: validations,
		})
	}
	return result, nil
}

func dynamicValidations(object cty.Value) ([]corespec.Condition, error) {
	value, ok := dynamicAttribute(object, "validation")
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsListType() && !unmarked.Type().IsTupleType() {
		return nil, fmt.Errorf("validation must be a list")
	}
	result := make([]corespec.Condition, 0, unmarked.LengthInt())
	iterator := unmarked.ElementIterator()
	for index := 0; iterator.Next(); index++ {
		_, item := iterator.Element()
		condition, err := dynamicRequiredString(item, "condition")
		if err != nil {
			return nil, fmt.Errorf("validation %d: %w", index, err)
		}
		errorMessage, err := dynamicRequiredString(item, "error_message")
		if err != nil {
			return nil, fmt.Errorf("validation %d: %w", index, err)
		}
		if err = validateConditionRoots(condition, "input"); err != nil {
			return nil, fmt.Errorf("validation %d: %w", index, err)
		}
		result = append(result, corespec.Condition{Expression: condition, ErrorMessage: errorMessage})
	}
	return result, nil
}

func dynamicOptionalValue(object cty.Value, name string) (cty.Value, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return cty.NilVal, nil
	}
	return value, nil
}

func dynamicRequiredString(object cty.Value, name string) (string, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return "", fmt.Errorf("%s is required", name)
	}
	unmarked, _ := value.Unmark()
	if !unmarked.Type().Equals(cty.String) {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return unmarked.AsString(), nil
}

func dynamicOptionalString(object cty.Value, name string) (*string, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.Unmark()
	if !unmarked.Type().Equals(cty.String) {
		return nil, fmt.Errorf("%s must be a string", name)
	}
	result := unmarked.AsString()
	return &result, nil
}

func dynamicStringList(object cty.Value, name string) ([]string, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.CanIterateElements() {
		return nil, fmt.Errorf("%s must be a list of string", name)
	}
	result := make([]string, 0, unmarked.LengthInt())
	iterator := unmarked.ElementIterator()
	for iterator.Next() {
		_, element := iterator.Element()
		if element.IsNull() || !element.Type().Equals(cty.String) {
			return nil, fmt.Errorf("%s must be a list of string", name)
		}
		result = append(result, element.AsString())
	}
	return result, nil
}

func dynamicIntMap(object cty.Value, name string) (map[string]int, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.CanIterateElements() {
		return nil, fmt.Errorf("%s must be a map of number", name)
	}
	result := make(map[string]int, unmarked.LengthInt())
	iterator := unmarked.ElementIterator()
	for iterator.Next() {
		key, element := iterator.Element()
		if !key.Type().Equals(cty.String) || !element.Type().Equals(cty.Number) {
			return nil, fmt.Errorf("%s must be a map of number", name)
		}
		integer, accuracy := element.AsBigFloat().Int64()
		if accuracy != big.Exact {
			return nil, fmt.Errorf("%s values must be whole numbers", name)
		}
		result[key.AsString()] = int(integer)
	}
	return result, nil
}

func dynamicOptionalInt(object cty.Value, name string) (*int, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.Unmark()
	if !unmarked.Type().Equals(cty.Number) {
		return nil, fmt.Errorf("%s must be a number", name)
	}
	integer, accuracy := unmarked.AsBigFloat().Int64()
	if accuracy != big.Exact {
		return nil, fmt.Errorf("%s must be a whole number", name)
	}
	result := int(integer)
	return &result, nil
}

func dynamicRetryBlocks(object cty.Value) ([]RetryBlock, error) {
	value, ok := dynamicAttribute(object, "retry")
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return nil, fmt.Errorf("retry must be an object")
	}
	block := RetryBlock{}
	var err error
	if block.LifecycleRetries, err = dynamicOptionalInt(unmarked, "lifecycle_retries"); err != nil {
		return nil, err
	}
	if block.ModelCallRetries, err = dynamicOptionalInt(unmarked, "model_call_retries"); err != nil {
		return nil, err
	}
	if block.IntervalSeconds, err = dynamicOptionalInt(unmarked, "interval_seconds"); err != nil {
		return nil, err
	}
	if block.MaxIntervalSeconds, err = dynamicOptionalInt(unmarked, "max_interval_seconds"); err != nil {
		return nil, err
	}
	if block.ErrorMessageRegex, err = dynamicStringList(unmarked, "error_message_regex"); err != nil {
		return nil, err
	}
	return []RetryBlock{block}, nil
}

func dynamicArtifactBlocks(object cty.Value) ([]ArtifactBlock, error) {
	value, ok := dynamicAttribute(object, "artifacts")
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsListType() && !unmarked.Type().IsTupleType() {
		return nil, fmt.Errorf("artifacts must be a list")
	}
	result := make([]ArtifactBlock, 0, unmarked.LengthInt())
	iterator := unmarked.ElementIterator()
	for index := 0; iterator.Next(); index++ {
		_, artifact := iterator.Element()
		name, err := dynamicRequiredString(artifact, "name")
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		artifactType, err := dynamicRequiredString(artifact, "type")
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		path, err := dynamicRequiredString(artifact, "path")
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		description, err := dynamicRequiredString(artifact, "description")
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		required, err := dynamicOptionalBool(artifact, "required")
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		nonEmpty, err := dynamicOptionalBool(artifact, "non_empty")
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		result = append(result, ArtifactBlock{
			Name: name, ArtifactType: artifactType, Path: path, Description: description,
			Required: required, NonEmpty: nonEmpty,
		})
	}
	return result, nil
}

func dynamicQCBlocks(object cty.Value) ([]QCBlock, error) {
	value, ok := dynamicAttribute(object, "qc")
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return nil, fmt.Errorf("qc must be an object")
	}
	criteria, ok := dynamicAttribute(unmarked, "criteria")
	if !ok {
		return nil, fmt.Errorf("qc criteria is required")
	}
	block := QCBlock{Criteria: criteria, ModelProvider: cty.NilVal}
	var err error
	if block.ModelProvider, err = dynamicOptionalValue(unmarked, "model_provider"); err != nil {
		return nil, err
	}
	if block.Model, err = dynamicOptionalString(unmarked, "model"); err != nil {
		return nil, err
	}
	if block.ReasoningEffort, err = dynamicOptionalString(unmarked, "reasoning_effort"); err != nil {
		return nil, err
	}
	if block.ToolIDs, err = dynamicStringList(unmarked, "tool_ids"); err != nil {
		return nil, err
	}
	if block.ToolCallQuota, err = dynamicIntMap(unmarked, "tool_call_quota"); err != nil {
		return nil, err
	}
	if block.AllowedTools, err = dynamicStringList(unmarked, "allowed_tools"); err != nil {
		return nil, err
	}
	if block.DisallowedTools, err = dynamicStringList(unmarked, "disallowed_tools"); err != nil {
		return nil, err
	}
	if block.SkillDirectories, err = dynamicStringList(unmarked, "skill_directories"); err != nil {
		return nil, err
	}
	if block.Skills, err = dynamicStringList(unmarked, "skills"); err != nil {
		return nil, err
	}
	if block.DisabledSkills, err = dynamicStringList(unmarked, "disabled_skills"); err != nil {
		return nil, err
	}
	permission, err := dynamicOptionalString(unmarked, "permission")
	if err != nil {
		return nil, err
	}
	if permission != nil {
		parsed := Permission(*permission)
		block.Permission = &parsed
	}
	if block.MaxQCRounds, err = dynamicOptionalInt(unmarked, "max_qc_rounds"); err != nil {
		return nil, err
	}
	if block.RetryBlocks, err = dynamicRetryBlocks(unmarked); err != nil {
		return nil, err
	}
	return []QCBlock{block}, nil
}

func dynamicCollectionQCBlocks(object cty.Value) ([]CollectionQCBlock, error) {
	value, ok := dynamicAttribute(object, "collection_qc")
	if !ok || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return nil, fmt.Errorf("collection_qc must be an object")
	}
	block := CollectionQCBlock{ModelProvider: cty.NilVal}
	var err error
	if block.ModelProvider, err = dynamicOptionalValue(unmarked, "model_provider"); err != nil {
		return nil, err
	}
	if block.Model, err = dynamicOptionalString(unmarked, "model"); err != nil {
		return nil, err
	}
	if block.ReasoningEffort, err = dynamicOptionalString(unmarked, "reasoning_effort"); err != nil {
		return nil, err
	}
	permission, err := dynamicOptionalString(unmarked, "permission")
	if err != nil {
		return nil, err
	}
	if permission != nil {
		parsed := Permission(*permission)
		block.Permission = &parsed
	}
	if criteria, ok := dynamicAttribute(unmarked, "criteria"); ok && !criteria.IsNull() {
		block.Criteria = criteria
	}
	if block.RetryBlocks, err = dynamicRetryBlocks(unmarked); err != nil {
		return nil, err
	}
	return []CollectionQCBlock{block}, nil
}

func dynamicOptionalBool(object cty.Value, name string) (bool, error) {
	value, ok := dynamicAttribute(object, name)
	if !ok || value.IsNull() {
		return false, nil
	}
	unmarked, _ := value.Unmark()
	if !unmarked.Type().Equals(cty.Bool) {
		return false, fmt.Errorf("%s must be a bool", name)
	}
	return unmarked.True(), nil
}

func dynamicAttribute(object cty.Value, name string) (cty.Value, bool) {
	if object.Type().IsObjectType() {
		if !object.Type().HasAttribute(name) {
			return cty.NilVal, false
		}
		return object.GetAttr(name), true
	}
	if object.Type().IsMapType() {
		return object.AsValueMap()[name], object.HasIndex(cty.StringVal(name)).True()
	}
	return cty.NilVal, false
}

func plannedDynamicTaskValue(task cty.Value, config Config) cty.Value {
	values, marks := dynamicTaskValuesWithProfile(task)
	values["artifacts"] = ArtifactsValue(config.Artifacts, nil)
	values["snapshots"] = cty.UnknownVal(cty.List(snapshotValueType))
	if config.TerminateToolID != nil {
		values["result"] = cty.UnknownVal(cty.String)
	}
	return cty.ObjectVal(values).WithMarks(marks)
}

func dynamicTaskValueWithProfile(task cty.Value) cty.Value {
	values, marks := dynamicTaskValuesWithProfile(task)
	return cty.ObjectVal(values).WithMarks(marks)
}

func dynamicTaskValuesWithProfile(task cty.Value) (map[string]cty.Value, cty.ValueMarks) {
	unmarked, marks := task.Unmark()
	values := maps.Clone(unmarked.AsValueMap())
	profile, exists := values["profile"]
	if exists {
		profile, _ = profile.Unmark()
	}
	if !exists || profile.IsNull() {
		values["profile"] = values["model"]
	}
	return values, marks
}

func AppliedDynamicTaskValue(task, result cty.Value) cty.Value {
	unmarked, marks := task.UnmarkDeepWithPaths()
	values := maps.Clone(unmarked.AsValueMap())
	resultValues := result.AsValueMap()
	if artifacts, ok := resultValues["artifact"]; ok {
		values["artifacts"] = artifacts
	}
	if snapshots, ok := resultValues["snapshots"]; ok {
		values["snapshots"] = snapshots
	}
	if value, ok := resultValues["result"]; ok {
		values["result"] = value
	}
	return cty.ObjectVal(values).MarkWithPaths(marks)
}

func dynamicTasksOutputType(tasksType cty.Type) (cty.Type, error) {
	switch {
	case tasksType.Equals(cty.DynamicPseudoType):
		return cty.DynamicPseudoType, nil
	case tasksType.IsListType():
		return cty.List(dynamicTaskOutputType(tasksType.ElementType())), nil
	case tasksType.IsTupleType():
		elementTypes := tasksType.TupleElementTypes()
		for index := range elementTypes {
			elementTypes[index] = dynamicTaskOutputType(elementTypes[index])
		}
		return cty.Tuple(elementTypes), nil
	default:
		return cty.NilType, fmt.Errorf("dynamic research tasks must be a list")
	}
}

func dynamicTaskOutputType(taskType cty.Type) cty.Type {
	if !taskType.IsObjectType() {
		return cty.DynamicPseudoType
	}
	attributes := taskType.AttributeTypes()
	attributes["profile"] = cty.String
	attributes["artifacts"] = cty.List(artifactValueType)
	attributes["snapshots"] = cty.List(snapshotValueType)
	if taskType.HasAttribute("terminate_tool_id") {
		attributes["result"] = cty.String
	}
	attributes["collection_batch_size"] = cty.Number
	attributes["max_collection_rounds"] = cty.Number
	if _, exists := attributes["collection_qc"]; exists {
		attributes["collection_qc"] = collectionQCBlockType()
	}
	return cty.Object(attributes)
}
