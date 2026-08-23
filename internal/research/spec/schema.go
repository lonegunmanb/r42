package spec

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/provider"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

var (
	_ golden.CustomDecode = (*ResearchBlock)(nil)
	_ golden.PlanBlock    = (*ResearchBlock)(nil)
	_ golden.ApplyBlock   = (*ResearchBlock)(nil)
	_ golden.Valuable     = (*ResearchBlock)(nil)
)

type ResearchBlock struct {
	*golden.BaseBlock
	ModelProvider              cty.Value           `hcl:"model_provider,optional"`
	Model                      string              `hcl:"model"`
	Profile                    *string             `hcl:"profile,optional"`
	ReasoningEffort            *string             `hcl:"reasoning_effort,optional"`
	SystemPrompt               string              `hcl:"system_prompt"`
	Prompt                     *string             `hcl:"prompt,optional"`
	ToolIDs                    []string            `hcl:"tool_ids,optional"`
	ToolCallQuota              map[string]int      `hcl:"tool_call_quota,optional"`
	TerminateToolID            *string             `hcl:"terminate_tool_id,optional"`
	AllowedTools               []string            `hcl:"allowed_tools,optional"`
	DisallowedTools            []string            `hcl:"disallowed_tools,optional"`
	SkillDirectories           []string            `hcl:"skill_directories,optional"`
	Skills                     []string            `hcl:"skills,optional"`
	DisabledSkills             []string            `hcl:"disabled_skills,optional"`
	Permission                 *Permission         `hcl:"permission,optional"`
	MaxProtocolAttempts        *int                `hcl:"max_protocol_attempts,optional"`
	Timeout                    *string             `hcl:"timeout,optional"`
	RetryBlocks                []RetryBlock        `hcl:"retry,block"`
	ArtifactBlocks             []ArtifactBlock     `hcl:"artifact,block"`
	ToolUseBlocks              []ToolUseBlock      `hcl:"tool_use,block"`
	QCBlocks                   []QCBlock           `hcl:"qc,block"`
	CollectionModelProvider    cty.Value           `hcl:"collection_model_provider,optional"`
	CollectionToolIDs          []string            `hcl:"collection_tool_ids,optional"`
	CollectionSkillDirectories []string            `hcl:"collection_skill_directories,optional"`
	CollectionSkills           []string            `hcl:"collection_skills,optional"`
	CollectionDisabledSkills   []string            `hcl:"collection_disabled_skills,optional"`
	CollectionBatchSize        *int                `hcl:"collection_batch_size,optional"`
	MaxCollectionRounds        *int                `hcl:"max_collection_rounds,optional"`
	CollectionQCBlocks         []CollectionQCBlock `hcl:"collection_qc,block"`

	planned                Config
	deferredTaskExpression string
	plannedTaskValue       cty.Value
}

type blockApplier interface {
	ApplyBlock(string) error
}

type blockWorkingDirectoryProvider interface {
	BlockWorkingDirectory(string) (string, error)
}

func (*ResearchBlock) Type() string { return "static" }

func (*ResearchBlock) BlockType() string { return "research" }

func (*ResearchBlock) AddressLength() int { return 3 }

func (*ResearchBlock) CanExecutePrePlan() bool { return false }

func (b *ResearchBlock) EvalContext() *hcl.EvalContext {
	return researchBlockEvalContext(b.BaseBlock)
}

func researchBlockEvalContext(block *golden.BaseBlock) *hcl.EvalContext {
	context := block.EvalContext()
	context.Variables = maps.Clone(context.Variables)
	if _, exists := context.Variables["input"]; !exists {
		context.Variables["input"] = cty.DynamicVal
	}
	context.Functions = maps.Clone(context.Functions)
	if context.Functions == nil {
		context.Functions = make(map[string]function.Function)
	}
	context.Functions["block_wd"] = function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) {
			provider, ok := block.Config().(blockWorkingDirectoryProvider)
			if !ok {
				return cty.NilVal, fmt.Errorf("research %q requires an r42 workspace config", block.Name())
			}
			directory, err := provider.BlockWorkingDirectory(block.Address())
			if err != nil {
				return cty.NilVal, err
			}
			return cty.StringVal(directory), nil
		},
	})
	return context
}

func (b *ResearchBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		if b.deferredTaskExpression != "" {
			return nil
		}
		if err := b.validateNativeStringFields(); err != nil {
			return err
		}
		config, err := b.toConfig()
		if err != nil {
			return err
		}
		if err = config.Validate(); err != nil {
			return err
		}
		b.planned = config
		return nil
	})
}

func (b *ResearchBlock) Apply() error {
	applier, ok := b.Config().(blockApplier)
	if !ok {
		return fmt.Errorf("research %q requires an r42 apply config", b.Name())
	}
	return applier.ApplyBlock(b.Address())
}

func (b *ResearchBlock) validateNativeStringFields() error {
	if b.BaseBlock == nil {
		return nil
	}
	root, err := b.HclBlock().ExpandDynamicBlocks(b.EvalContext())
	if err != nil {
		return err
	}
	if err := validateStringAttributes(root, b.EvalContext(), "research", []string{
		"model", "profile", "reasoning_effort", "system_prompt", "prompt", "terminate_tool_id", "permission", "timeout",
	}); err != nil {
		return err
	}
	if err := validateStringCollections(root, b.EvalContext(), "research", []string{
		"tool_ids", "allowed_tools", "disallowed_tools", "skill_directories", "skills", "disabled_skills",
		"collection_tool_ids", "collection_skill_directories", "collection_skills", "collection_disabled_skills",
	}); err != nil {
		return err
	}
	if err := validateNumberAttributes(root, b.EvalContext(), "research", []string{
		"max_protocol_attempts", "collection_batch_size", "max_collection_rounds",
	}); err != nil {
		return err
	}
	for _, nested := range root.NestedBlocks() {
		switch nested.Type {
		case "retry":
			if err := validateNumberAttributes(nested, b.EvalContext(), "research retry", retryNumberAttributeNames()); err != nil {
				return err
			}
			if err := validateStringCollections(nested, b.EvalContext(), "research retry", []string{"error_message_regex"}); err != nil {
				return err
			}
		case "artifact":
			if err := validateStringAttributes(nested, b.EvalContext(), "artifact", []string{"type", "path", "description"}); err != nil {
				return err
			}
			if err := validateBoolAttributes(nested, b.EvalContext(), "artifact", []string{"required", "non_empty"}); err != nil {
				return err
			}
		case "qc":
			if err := validateQCStringFields(nested, b.EvalContext()); err != nil {
				return err
			}
		case "collection_qc":
			if err := validateCollectionQCStringFields(nested, b.EvalContext()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCollectionQCStringFields(block *golden.HclBlock, context *hcl.EvalContext) error {
	if err := validateStringAttributes(block, context, "collection qc", []string{"model", "reasoning_effort", "permission"}); err != nil {
		return err
	}
	for _, nested := range block.NestedBlocks() {
		if nested.Type != "retry" {
			continue
		}
		if err := validateNumberAttributes(nested, context, "collection qc retry", retryNumberAttributeNames()); err != nil {
			return err
		}
		return validateStringCollections(nested, context, "collection qc retry", []string{"error_message_regex"})
	}
	return nil
}

func validateQCStringFields(block *golden.HclBlock, context *hcl.EvalContext) error {
	if err := validateStringAttributes(block, context, "qc", []string{"model", "reasoning_effort", "permission"}); err != nil {
		return err
	}
	if err := validateStringCollections(block, context, "qc", []string{
		"tool_ids", "allowed_tools", "disallowed_tools", "skill_directories", "skills", "disabled_skills",
	}); err != nil {
		return err
	}
	if err := validateNumberAttributes(block, context, "qc", []string{"max_qc_rounds"}); err != nil {
		return err
	}
	for _, nested := range block.NestedBlocks() {
		if nested.Type == "retry" {
			if err := validateNumberAttributes(nested, context, "qc retry", retryNumberAttributeNames()); err != nil {
				return err
			}
			return validateStringCollections(nested, context, "qc retry", []string{"error_message_regex"})
		}
	}
	return nil
}

func validateStringAttributes(block *golden.HclBlock, context *hcl.EvalContext, scope string, names []string) error {
	return validateScalarAttributes(block, context, scope, names, cty.String, "string")
}

func validateNumberAttributes(block *golden.HclBlock, context *hcl.EvalContext, scope string, names []string) error {
	return validateScalarAttributes(block, context, scope, names, cty.Number, "number")
}

func validateBoolAttributes(block *golden.HclBlock, context *hcl.EvalContext, scope string, names []string) error {
	return validateScalarAttributes(block, context, scope, names, cty.Bool, "bool")
}

func validateScalarAttributes(
	block *golden.HclBlock,
	context *hcl.EvalContext,
	scope string,
	names []string,
	expectedType cty.Type,
	typeName string,
) error {
	for _, name := range names {
		attribute, ok := block.Attributes()[name]
		if !ok {
			continue
		}
		value, diagnostics := attribute.Expr.Value(context)
		if diagnostics.HasErrors() {
			// note: untested because Golden evaluates native fields before plan execution.
			return fmt.Errorf("evaluate %s %s: %w", scope, name, diagnostics)
		}
		unmarked, _ := value.UnmarkDeep()
		if unmarked.IsNull() {
			continue
		}
		if !unmarked.IsWhollyKnown() {
			// note: untested because Golden rejects unknown native strings before plan execution.
			return fmt.Errorf("%s %s must be known during plan", scope, name)
		}
		if !unmarked.Type().Equals(expectedType) {
			return fmt.Errorf("%s %s must be a %s", scope, name, typeName)
		}
	}
	return nil
}

func retryNumberAttributeNames() []string {
	return []string{"lifecycle_retries", "model_call_retries", "interval_seconds", "max_interval_seconds"}
}

func validateStringCollections(block *golden.HclBlock, context *hcl.EvalContext, scope string, names []string) error {
	for _, name := range names {
		attribute, ok := block.Attributes()[name]
		if !ok {
			continue
		}
		value, diagnostics := attribute.Expr.Value(context)
		if diagnostics.HasErrors() {
			// note: untested because Golden evaluates native fields before plan execution.
			return fmt.Errorf("evaluate %s %s: %w", scope, name, diagnostics)
		}
		unmarked, _ := value.UnmarkDeep()
		if unmarked.IsNull() {
			continue
		}
		if !unmarked.IsWhollyKnown() {
			// note: untested because Golden rejects unknown native collections before plan execution.
			return fmt.Errorf("%s %s must be known during plan", scope, name)
		}
		if !unmarked.CanIterateElements() {
			return fmt.Errorf("%s %s must be a collection of strings", scope, name)
		}
		for iterator := unmarked.ElementIterator(); iterator.Next(); {
			_, element := iterator.Element()
			if element.IsNull() || !element.Type().Equals(cty.String) {
				return fmt.Errorf("%s %s must be a collection of strings", scope, name)
			}
		}
	}
	return nil
}

func (b *ResearchBlock) ResearchConfig() Config {
	return cloneConfig(b.planned)
}

func (b *ResearchBlock) DeferredTaskExpression() string {
	return b.deferredTaskExpression
}

func (b *ResearchBlock) Values() map[string]cty.Value {
	if b.deferredTaskExpression != "" {
		return deferredStaticResearchValues(b.plannedTaskValue)
	}
	values := map[string]cty.Value{
		"model_provider":               optionalObjectValue(b.ModelProvider),
		"model":                        cty.StringVal(b.Model),
		"profile":                      cty.StringVal(b.planned.ProfileName()),
		"reasoning_effort":             optionalStringValue(b.ReasoningEffort),
		"system_prompt":                cty.StringVal(b.SystemPrompt),
		"prompt":                       optionalStringValue(b.Prompt),
		"tool_ids":                     stringListValue(b.ToolIDs),
		"tool_call_quota":              intMapValue(b.ToolCallQuota),
		"terminate_tool_id":            optionalStringValue(b.TerminateToolID),
		"allowed_tools":                stringListValue(b.AllowedTools),
		"disallowed_tools":             stringListValue(b.DisallowedTools),
		"skill_directories":            stringListValue(b.SkillDirectories),
		"skills":                       stringListValue(b.Skills),
		"disabled_skills":              stringListValue(b.DisabledSkills),
		"permission":                   optionalPermissionValue(b.Permission),
		"max_protocol_attempts":        optionalIntValue(b.MaxProtocolAttempts),
		"timeout":                      optionalStringValue(b.Timeout),
		"retry":                        retryBlockValues(b.RetryBlocks),
		"artifact":                     ArtifactsValue(b.planned.Artifacts, nil),
		"tool_use":                     toolUseValues(b.planned.ToolUses),
		"snapshots":                    cty.UnknownVal(cty.List(snapshotValueType)),
		"qc":                           qcBlockValues(b.QCBlocks),
		"collection_model_provider":    optionalObjectValue(b.CollectionModelProvider),
		"collection_tool_ids":          stringListValue(b.CollectionToolIDs),
		"collection_skill_directories": stringListValue(b.CollectionSkillDirectories),
		"collection_skills":            stringListValue(b.CollectionSkills),
		"collection_disabled_skills":   stringListValue(b.CollectionDisabledSkills),
		"collection_batch_size":        optionalIntValue(b.CollectionBatchSize),
		"max_collection_rounds":        optionalIntValue(b.MaxCollectionRounds),
		"collection_qc":                collectionQCBlockValues(b.CollectionQCBlocks),
	}
	if b.planned.TerminateToolID != nil {
		values["result"] = cty.UnknownVal(cty.String)
	}
	return values
}

type RetryBlock struct {
	LifecycleRetries   *int     `hcl:"lifecycle_retries,optional"`
	ModelCallRetries   *int     `hcl:"model_call_retries,optional"`
	IntervalSeconds    *int     `hcl:"interval_seconds,optional"`
	MaxIntervalSeconds *int     `hcl:"max_interval_seconds,optional"`
	ErrorMessageRegex  []string `hcl:"error_message_regex,optional"`
}

var retryBlockType = cty.Object(map[string]cty.Type{
	"lifecycle_retries":    cty.Number,
	"model_call_retries":   cty.Number,
	"interval_seconds":     cty.Number,
	"max_interval_seconds": cty.Number,
	"error_message_regex":  cty.List(cty.String),
})

func retryBlockValues(blocks []RetryBlock) cty.Value {
	if len(blocks) == 0 {
		return cty.ListValEmpty(retryBlockType)
	}
	values := make([]cty.Value, len(blocks))
	for index, block := range blocks {
		values[index] = cty.ObjectVal(map[string]cty.Value{
			"lifecycle_retries":    optionalIntValue(block.LifecycleRetries),
			"model_call_retries":   optionalIntValue(block.ModelCallRetries),
			"interval_seconds":     optionalIntValue(block.IntervalSeconds),
			"max_interval_seconds": optionalIntValue(block.MaxIntervalSeconds),
			"error_message_regex":  stringListValue(block.ErrorMessageRegex),
		})
	}
	return cty.ListVal(values)
}

func qcBlockValues(blocks []QCBlock) cty.Value {
	if len(blocks) != 1 {
		return cty.ListValEmpty(qcBlockType())
	}
	block := blocks[0]
	value := cty.ObjectVal(map[string]cty.Value{
		"criteria":          qcCriteriaValue(block.Criteria),
		"model_provider":    optionalObjectValue(block.ModelProvider),
		"model":             optionalStringValue(block.Model),
		"reasoning_effort":  optionalStringValue(block.ReasoningEffort),
		"tool_ids":          stringListValue(block.ToolIDs),
		"tool_call_quota":   intMapValue(block.ToolCallQuota),
		"allowed_tools":     stringListValue(block.AllowedTools),
		"disallowed_tools":  stringListValue(block.DisallowedTools),
		"skill_directories": stringListValue(block.SkillDirectories),
		"skills":            stringListValue(block.Skills),
		"disabled_skills":   stringListValue(block.DisabledSkills),
		"permission":        optionalPermissionValue(block.Permission),
		"max_qc_rounds":     optionalIntValue(block.MaxQCRounds),
		"retry":             retryBlockValues(block.RetryBlocks),
	})
	return cty.ListVal([]cty.Value{value})
}

func qcCriteriaValue(value cty.Value) cty.Value {
	criteria, err := normalizeCriteria(value, "qc")
	if err != nil {
		return cty.UnknownVal(cty.Map(cty.String))
	}
	return criteria
}

func qcBlockType() cty.Type {
	return cty.Object(map[string]cty.Type{
		"criteria":          cty.Map(cty.String),
		"model_provider":    cty.EmptyObject,
		"model":             cty.String,
		"reasoning_effort":  cty.String,
		"tool_ids":          cty.List(cty.String),
		"tool_call_quota":   cty.Map(cty.Number),
		"allowed_tools":     cty.List(cty.String),
		"disallowed_tools":  cty.List(cty.String),
		"skill_directories": cty.List(cty.String),
		"skills":            cty.List(cty.String),
		"disabled_skills":   cty.List(cty.String),
		"permission":        cty.String,
		"max_qc_rounds":     cty.Number,
		"retry":             cty.List(retryBlockType),
	})
}

func collectionQCBlockValues(blocks []CollectionQCBlock) cty.Value {
	if len(blocks) != 1 {
		return cty.ListValEmpty(collectionQCBlockType())
	}
	block := blocks[0]
	criteria := cty.NullVal(cty.Map(cty.String))
	if hasValue(block.Criteria) {
		criteria = qcCriteriaValue(block.Criteria)
	}
	value := cty.ObjectVal(map[string]cty.Value{
		"criteria":         criteria,
		"model_provider":   optionalObjectValue(block.ModelProvider),
		"model":            optionalStringValue(block.Model),
		"reasoning_effort": optionalStringValue(block.ReasoningEffort),
		"permission":       optionalPermissionValue(block.Permission),
		"retry":            retryBlockValues(block.RetryBlocks),
	})
	return cty.ListVal([]cty.Value{value})
}

func collectionQCBlockType() cty.Type {
	return cty.Object(map[string]cty.Type{
		"criteria":         cty.Map(cty.String),
		"model_provider":   cty.EmptyObject,
		"model":            cty.String,
		"reasoning_effort": cty.String,
		"permission":       cty.String,
		"retry":            cty.List(retryBlockType),
	})
}

func optionalObjectValue(value cty.Value) cty.Value {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) {
		return cty.NullVal(cty.EmptyObject)
	}
	return value
}

func optionalStringValue(value *string) cty.Value {
	if value == nil {
		return cty.NullVal(cty.String)
	}
	return cty.StringVal(*value)
}

func optionalPermissionValue(value *Permission) cty.Value {
	if value == nil {
		return cty.NullVal(cty.String)
	}
	return cty.StringVal(string(*value))
}

func optionalIntValue(value *int) cty.Value {
	if value == nil {
		return cty.NullVal(cty.Number)
	}
	return cty.NumberIntVal(int64(*value))
}

func stringListValue(values []string) cty.Value {
	if len(values) == 0 {
		return cty.ListValEmpty(cty.String)
	}
	result := make([]cty.Value, len(values))
	for index, value := range values {
		result[index] = cty.StringVal(value)
	}
	return cty.ListVal(result)
}

func intMapValue(values map[string]int) cty.Value {
	if len(values) == 0 {
		return cty.MapValEmpty(cty.Number)
	}
	result := make(map[string]cty.Value, len(values))
	for key, value := range values {
		result[key] = cty.NumberIntVal(int64(value))
	}
	return cty.MapVal(result)
}

type ArtifactBlock struct {
	Name         string `hcl:"name,label"`
	ArtifactType string `hcl:"type"`
	Path         string `hcl:"path"`
	Description  string `hcl:"description"`
	Required     bool   `hcl:"required,optional"`
	NonEmpty     bool   `hcl:"non_empty,optional"`
}

type ToolUseBlock struct {
	Name           string    `hcl:"name,label"`
	ToolID         string    `hcl:"tool_id"`
	Terminate      bool      `hcl:"terminate,optional"`
	Input          cty.Value `hcl:"input,optional"`
	InputFromAgent cty.Value `hcl:"input_from_agent,optional"`
	validations    []corespec.Condition
}

type QCBlock struct {
	Criteria         cty.Value      `hcl:"criteria"`
	ModelProvider    cty.Value      `hcl:"model_provider,optional"`
	Model            *string        `hcl:"model,optional"`
	ReasoningEffort  *string        `hcl:"reasoning_effort,optional"`
	ToolIDs          []string       `hcl:"tool_ids,optional"`
	ToolCallQuota    map[string]int `hcl:"tool_call_quota,optional"`
	AllowedTools     []string       `hcl:"allowed_tools,optional"`
	DisallowedTools  []string       `hcl:"disallowed_tools,optional"`
	SkillDirectories []string       `hcl:"skill_directories,optional"`
	Skills           []string       `hcl:"skills,optional"`
	DisabledSkills   []string       `hcl:"disabled_skills,optional"`
	Permission       *Permission    `hcl:"permission,optional"`
	MaxQCRounds      *int           `hcl:"max_qc_rounds,optional"`
	RetryBlocks      []RetryBlock   `hcl:"retry,block"`
}

type CollectionQCBlock struct {
	Criteria        cty.Value    `hcl:"criteria,optional"`
	ModelProvider   cty.Value    `hcl:"model_provider,optional"`
	Model           *string      `hcl:"model,optional"`
	ReasoningEffort *string      `hcl:"reasoning_effort,optional"`
	Permission      *Permission  `hcl:"permission,optional"`
	RetryBlocks     []RetryBlock `hcl:"retry,block"`
}

func (b *ResearchBlock) toConfig() (Config, error) {
	if len(b.RetryBlocks) > 1 {
		return Config{}, errors.New("research must have at most one retry block")
	}
	if len(b.QCBlocks) > 1 {
		return Config{}, errors.New("research must have at most one qc block")
	}
	if len(b.CollectionQCBlocks) > 1 {
		return Config{}, errors.New("research must have at most one collection_qc block")
	}
	timeout, err := optionalDuration(b.Timeout, "timeout")
	if err != nil {
		return Config{}, err
	}
	profile, err := resolveProfile(b.Model, b.Profile)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		ModelProvider:       b.ModelProvider,
		Model:               b.Model,
		Profile:             profile,
		ReasoningEffort:     clonePointer(b.ReasoningEffort),
		SystemPrompt:        b.SystemPrompt,
		Prompt:              clonePointer(b.Prompt),
		TerminateToolID:     cloneStringPointer(b.TerminateToolID),
		MaxProtocolAttempts: DefaultMaxProtocolAttempts,
		Timeout:             timeout,
		Policy: SessionPolicy{
			ToolIDs:          slices.Clone(b.ToolIDs),
			ToolCallQuota:    maps.Clone(b.ToolCallQuota),
			AllowedTools:     slices.Clone(b.AllowedTools),
			DisallowedTools:  slices.Clone(b.DisallowedTools),
			SkillDirectories: slices.Clone(b.SkillDirectories),
			Skills:           slices.Clone(b.Skills),
			DisabledSkills:   slices.Clone(b.DisabledSkills),
			Permission:       PermissionApproveAll,
		},
		CollectionToolIDs:          slices.Clone(b.CollectionToolIDs),
		CollectionModelProvider:    b.CollectionModelProvider,
		CollectionSkillDirectories: slices.Clone(b.CollectionSkillDirectories),
		CollectionSkills:           slices.Clone(b.CollectionSkills),
		CollectionDisabledSkills:   slices.Clone(b.CollectionDisabledSkills),
		CollectionBatchSize:        DefaultCollectionBatchSize,
		MaxCollectionRounds:        clonePointer(b.MaxCollectionRounds),
	}
	if config.Policy.DisallowedTools == nil {
		config.Policy.DisallowedTools = []string{"ask_user"}
	}
	if b.Permission != nil {
		config.Policy.Permission = *b.Permission
	}
	if b.MaxProtocolAttempts != nil {
		config.MaxProtocolAttempts = *b.MaxProtocolAttempts
	}
	if b.CollectionBatchSize != nil {
		config.CollectionBatchSize = *b.CollectionBatchSize
	}
	if len(b.RetryBlocks) == 1 {
		config.Retry, err = b.RetryBlocks[0].override()
		if err != nil {
			return Config{}, err
		}
	}
	config.Artifacts = make([]Artifact, len(b.ArtifactBlocks))
	for index, block := range b.ArtifactBlocks {
		config.Artifacts[index] = block.toArtifact()
	}
	if len(b.ToolUseBlocks) > 0 {
		if len(b.ToolIDs) > 0 || b.TerminateToolID != nil {
			return Config{}, errors.New("tool_use cannot be combined with tool_ids or terminate_tool_id")
		}
		config.ToolUses = make([]ToolUse, len(b.ToolUseBlocks))
		for index, block := range b.ToolUseBlocks {
			config.ToolUses[index] = block.toToolUse()
			config.Policy.ToolIDs = append(config.Policy.ToolIDs, block.ToolID)
			if block.Terminate {
				toolID := block.ToolID
				config.TerminateToolID = &toolID
			}
		}
	}
	if len(b.QCBlocks) == 1 {
		config.QC, err = b.QCBlocks[0].toConfig()
		if err != nil {
			return Config{}, err
		}
	}
	if len(b.CollectionQCBlocks) == 1 {
		config.CollectionQC, err = b.CollectionQCBlocks[0].toConfig()
		if err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

func (b ToolUseBlock) toToolUse() ToolUse {
	return ToolUse{
		Name: b.Name, ToolID: b.ToolID, Terminate: b.Terminate,
		Input: b.Input, InputFromAgent: b.InputFromAgent,
		Validations: slices.Clone(b.validations),
	}
}

func toolUseValues(toolUses []ToolUse) cty.Value {
	if len(toolUses) == 0 {
		return cty.EmptyTupleVal
	}
	values := make([]cty.Value, len(toolUses))
	for index, toolUse := range toolUses {
		input, _ := toolUseObject(toolUse.Input, "input")
		agent, _ := toolUseObject(toolUse.InputFromAgent, "input_from_agent")
		values[index] = cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal(toolUse.Name), "tool_id": cty.StringVal(toolUse.ToolID),
			"terminate": cty.BoolVal(toolUse.Terminate), "input": input, "input_from_agent": agent,
		})
	}
	return cty.TupleVal(values)
}

func resolveProfile(model string, profile *string) (string, error) {
	if profile == nil {
		return model, nil
	}
	if strings.TrimSpace(*profile) == "" {
		return "", errors.New("research profile must not be empty")
	}
	return *profile, nil
}

func (b RetryBlock) override() (provider.RetryOverride, error) {
	interval, err := optionalSeconds(b.IntervalSeconds, "interval_seconds")
	if err != nil {
		return provider.RetryOverride{}, err
	}
	maxInterval, err := optionalSeconds(b.MaxIntervalSeconds, "max_interval_seconds")
	if err != nil {
		return provider.RetryOverride{}, err
	}
	return provider.RetryOverride{
		LifecycleRetries:  clonePointer(b.LifecycleRetries),
		ModelCallRetries:  clonePointer(b.ModelCallRetries),
		Interval:          interval,
		MaxInterval:       maxInterval,
		ErrorMessageRegex: slices.Clone(b.ErrorMessageRegex),
	}, nil
}

func (b ArtifactBlock) toArtifact() Artifact {
	return Artifact{
		Name:        b.Name,
		Type:        ArtifactType(b.ArtifactType),
		Path:        b.Path,
		Description: b.Description,
		Required:    b.Required,
		NonEmpty:    b.NonEmpty,
	}
}

func (b QCBlock) toConfig() (*QCConfig, error) {
	if len(b.RetryBlocks) > 1 {
		return nil, errors.New("qc must have at most one retry block")
	}
	criteria, err := normalizeCriteria(b.Criteria, "qc")
	if err != nil {
		return nil, err
	}
	config := &QCConfig{
		Criteria:         criteria,
		ModelProvider:    b.ModelProvider,
		Model:            clonePointer(b.Model),
		ReasoningEffort:  clonePointer(b.ReasoningEffort),
		ToolIDs:          slices.Clone(b.ToolIDs),
		ToolCallQuota:    maps.Clone(b.ToolCallQuota),
		AllowedTools:     slices.Clone(b.AllowedTools),
		DisallowedTools:  slices.Clone(b.DisallowedTools),
		SkillDirectories: slices.Clone(b.SkillDirectories),
		Skills:           slices.Clone(b.Skills),
		DisabledSkills:   slices.Clone(b.DisabledSkills),
		Permission:       clonePointer(b.Permission),
		MaxRounds:        DefaultMaxQCRounds,
	}
	if config.DisallowedTools == nil {
		config.DisallowedTools = DefaultQCDisallowedTools()
	}
	if b.MaxQCRounds != nil {
		config.MaxRounds = *b.MaxQCRounds
	}
	if len(b.RetryBlocks) == 1 {
		config.Retry, err = b.RetryBlocks[0].override()
		if err != nil {
			return nil, err
		}
	}
	return config, nil
}

func (b CollectionQCBlock) toConfig() (*CollectionQCConfig, error) {
	if len(b.RetryBlocks) > 1 {
		return nil, errors.New("collection qc must have at most one retry block")
	}
	config := &CollectionQCConfig{
		ModelProvider:   b.ModelProvider,
		Model:           clonePointer(b.Model),
		ReasoningEffort: clonePointer(b.ReasoningEffort),
		Permission:      clonePointer(b.Permission),
	}
	if hasValue(b.Criteria) {
		criteria, err := normalizeCriteria(b.Criteria, "collection qc")
		if err != nil {
			return nil, err
		}
		config.Criteria = criteria
	}
	if len(b.RetryBlocks) == 1 {
		var err error
		config.Retry, err = b.RetryBlocks[0].override()
		if err != nil {
			return nil, err
		}
	}
	return config, nil
}

func normalizeCriteria(value cty.Value, scope string) (cty.Value, error) {
	unmarked, marks := value.UnmarkDeepWithPaths()
	if !unmarked.IsWhollyKnown() {
		return cty.NilVal, fmt.Errorf("%s criteria must be wholly known during plan", scope)
	}
	if unmarked.IsNull() || (!unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType()) {
		return cty.NilVal, fmt.Errorf("%s criteria must be map of string", scope)
	}
	for _, criterion := range unmarked.AsValueMap() {
		if criterion.IsNull() || !criterion.Type().Equals(cty.String) {
			return cty.NilVal, fmt.Errorf("%s criteria must be map of string", scope)
		}
	}
	converted, err := convert.Convert(unmarked, cty.Map(cty.String))
	if err != nil {
		return cty.NilVal, fmt.Errorf("%s criteria must be map of string: %w", scope, err)
	}
	return converted.MarkWithPaths(objectMarksToMapPaths(marks)), nil
}

func objectMarksToMapPaths(marks []cty.PathValueMarks) []cty.PathValueMarks {
	result := make([]cty.PathValueMarks, len(marks))
	for index, pathMarks := range marks {
		path := make(cty.Path, len(pathMarks.Path))
		for stepIndex, step := range pathMarks.Path {
			if attribute, ok := step.(cty.GetAttrStep); ok {
				step = cty.IndexStep{Key: cty.StringVal(attribute.Name)}
			}
			path[stepIndex] = step
		}
		result[index] = cty.PathValueMarks{Path: path, Marks: pathMarks.Marks}
	}
	return result
}

func optionalDuration(value *string, name string) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	result, err := time.ParseDuration(*value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a go duration: %w", name, err)
	}
	return &result, nil
}

func optionalSeconds(value *int, name string) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	if *value < math.MinInt64/int(time.Second) || *value > math.MaxInt64/int(time.Second) {
		return nil, fmt.Errorf("%s is too large", name)
	}
	result := time.Duration(*value) * time.Second
	return &result, nil
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

var artifactValueType = cty.Object(map[string]cty.Type{
	"id": cty.String, "name": cty.String, "kind": cty.String, "type": cty.String, "path": cty.String,
	"description": cty.String, "required": cty.Bool, "non_empty": cty.Bool,
})

func ArtifactsValue(artifacts []Artifact, resolvedPaths map[string]string) cty.Value {
	return ArtifactsValueWithIDs(artifacts, resolvedPaths, nil)
}

func ArtifactsValueWithIDs(
	artifacts []Artifact,
	resolvedPaths map[string]string,
	ids map[string]string,
) cty.Value {
	if len(artifacts) == 0 {
		return cty.ListValEmpty(artifactValueType)
	}
	values := make([]cty.Value, len(artifacts))
	for index, artifact := range artifacts {
		id := cty.UnknownVal(cty.String)
		if resolved, ok := ids[artifact.Name]; ok {
			id = cty.StringVal(resolved)
		}
		path := cty.UnknownVal(cty.String)
		if filepath.IsAbs(artifact.Path) {
			path = cty.StringVal(artifact.Path)
		}
		if resolved, ok := resolvedPaths[artifact.Name]; ok {
			path = cty.StringVal(resolved)
		}
		values[index] = cty.ObjectVal(map[string]cty.Value{
			"id":          id,
			"name":        cty.StringVal(artifact.Name),
			"kind":        cty.StringVal("artifact"),
			"type":        cty.StringVal(string(artifact.Type)),
			"path":        path,
			"description": cty.StringVal(artifact.Description),
			"required":    cty.BoolVal(artifact.Required),
			"non_empty":   cty.BoolVal(artifact.NonEmpty),
		})
	}
	return cty.ListVal(values)
}

// Snapshot describes one Collection snapshot published by a research result.
type Snapshot struct {
	ID          string
	Path        string
	Description string
}

var snapshotValueType = artifactValueType

// SnapshotsValue converts snapshot outputs into their HCL representation.
func SnapshotsValue(snapshots []Snapshot) cty.Value {
	if len(snapshots) == 0 {
		return cty.ListValEmpty(snapshotValueType)
	}
	values := make([]cty.Value, len(snapshots))
	for index, item := range snapshots {
		values[index] = cty.ObjectVal(map[string]cty.Value{
			"id":          cty.StringVal(item.ID),
			"name":        cty.StringVal(item.ID),
			"kind":        cty.StringVal("snapshot"),
			"type":        cty.StringVal("file"),
			"path":        cty.StringVal(item.Path),
			"description": cty.StringVal(item.Description),
			"required":    cty.BoolVal(false),
			"non_empty":   cty.BoolVal(true),
		})
	}
	return cty.ListVal(values)
}

func cloneConfig(config Config) Config {
	result := config
	result.ReasoningEffort = cloneStringPointer(config.ReasoningEffort)
	result.Prompt = cloneStringPointer(config.Prompt)
	result.TerminateToolID = cloneStringPointer(config.TerminateToolID)
	if config.Timeout != nil {
		timeout := *config.Timeout
		result.Timeout = &timeout
	}
	result.Retry = cloneRetryOverride(config.Retry)
	result.Policy.AllowedTools = slices.Clone(config.Policy.AllowedTools)
	result.Policy.ToolIDs = slices.Clone(config.Policy.ToolIDs)
	result.Policy.ToolCallQuota = maps.Clone(config.Policy.ToolCallQuota)
	result.Policy.DisallowedTools = slices.Clone(config.Policy.DisallowedTools)
	result.Policy.SkillDirectories = slices.Clone(config.Policy.SkillDirectories)
	result.Policy.Skills = slices.Clone(config.Policy.Skills)
	result.Policy.DisabledSkills = slices.Clone(config.Policy.DisabledSkills)
	result.CollectionToolIDs = slices.Clone(config.CollectionToolIDs)
	result.CollectionModelProvider = config.CollectionModelProvider
	result.CollectionSkillDirectories = slices.Clone(config.CollectionSkillDirectories)
	result.CollectionSkills = slices.Clone(config.CollectionSkills)
	result.CollectionDisabledSkills = slices.Clone(config.CollectionDisabledSkills)
	result.MaxCollectionRounds = clonePointer(config.MaxCollectionRounds)
	result.Artifacts = slices.Clone(config.Artifacts)
	if config.QC != nil {
		qc := *config.QC
		qc.Model = cloneStringPointer(config.QC.Model)
		qc.ReasoningEffort = cloneStringPointer(config.QC.ReasoningEffort)
		if config.QC.Permission != nil {
			permission := *config.QC.Permission
			qc.Permission = &permission
		}
		qc.Retry = cloneRetryOverride(config.QC.Retry)
		qc.ToolIDs = slices.Clone(config.QC.ToolIDs)
		qc.ToolCallQuota = maps.Clone(config.QC.ToolCallQuota)
		qc.AllowedTools = slices.Clone(config.QC.AllowedTools)
		qc.DisallowedTools = slices.Clone(config.QC.DisallowedTools)
		qc.SkillDirectories = slices.Clone(config.QC.SkillDirectories)
		qc.Skills = slices.Clone(config.QC.Skills)
		qc.DisabledSkills = slices.Clone(config.QC.DisabledSkills)
		result.QC = &qc
	}
	if config.CollectionQC != nil {
		collectionQC := *config.CollectionQC
		collectionQC.Model = cloneStringPointer(config.CollectionQC.Model)
		collectionQC.ReasoningEffort = cloneStringPointer(config.CollectionQC.ReasoningEffort)
		if config.CollectionQC.Permission != nil {
			permission := *config.CollectionQC.Permission
			collectionQC.Permission = &permission
		}
		collectionQC.Retry = cloneRetryOverride(config.CollectionQC.Retry)
		result.CollectionQC = &collectionQC
	}
	return result
}

func cloneRetryOverride(retry provider.RetryOverride) provider.RetryOverride {
	result := retry
	result.LifecycleRetries = clonePointer(retry.LifecycleRetries)
	result.ModelCallRetries = clonePointer(retry.ModelCallRetries)
	result.Interval = clonePointer(retry.Interval)
	result.MaxInterval = clonePointer(retry.MaxInterval)
	result.ErrorMessageRegex = slices.Clone(retry.ErrorMessageRegex)
	return result
}
