package spec

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"time"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

type ResearchBlock struct {
	*golden.BaseBlock
	ModelProvider       cty.Value       `hcl:"model_provider,optional"`
	Model               string          `hcl:"model"`
	ReasoningEffort     *string         `hcl:"reasoning_effort,optional"`
	SystemPrompt        string          `hcl:"system_prompt"`
	Prompt              *string         `hcl:"prompt,optional"`
	Tools               cty.Value       `hcl:"tools,optional"`
	TerminateTool       cty.Value       `hcl:"terminate_tool,optional"`
	AllowedTools        []string        `hcl:"allowed_tools,optional"`
	DisallowedTools     []string        `hcl:"disallowed_tools,optional"`
	SkillDirectories    []string        `hcl:"skill_directories,optional"`
	Skills              []string        `hcl:"skills,optional"`
	DisabledSkills      []string        `hcl:"disabled_skills,optional"`
	Permission          *Permission     `hcl:"permission,optional"`
	MaxProtocolAttempts *int            `hcl:"max_protocol_attempts,optional"`
	Timeout             *string         `hcl:"timeout,optional"`
	RetryBlocks         []RetryBlock    `hcl:"retry,block"`
	ArtifactBlocks      []ArtifactBlock `hcl:"artifact,block"`
	QCBlocks            []QCBlock       `hcl:"qc,block"`

	planned Config
}

func (*ResearchBlock) Type() string { return "" }

func (*ResearchBlock) BlockType() string { return "research" }

func (*ResearchBlock) AddressLength() int { return 2 }

func (*ResearchBlock) CanExecutePrePlan() bool { return false }

func (b *ResearchBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
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

func (b *ResearchBlock) validateNativeStringFields() error {
	if b.BaseBlock == nil {
		return nil
	}
	root, err := b.HclBlock().ExpandDynamicBlocks(b.EvalContext())
	if err != nil {
		return err
	}
	if err := validateStringAttributes(root, b.EvalContext(), "research", []string{
		"model", "reasoning_effort", "system_prompt", "prompt", "permission", "timeout",
	}); err != nil {
		return err
	}
	if err := validateStringCollections(root, b.EvalContext(), "research", []string{
		"allowed_tools", "disallowed_tools", "skill_directories", "skills", "disabled_skills",
	}); err != nil {
		return err
	}
	if err := validateNumberAttributes(root, b.EvalContext(), "research", []string{"max_protocol_attempts"}); err != nil {
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
			if err := validateStringAttributes(nested, b.EvalContext(), "artifact", []string{"type", "path"}); err != nil {
				return err
			}
			if err := validateBoolAttributes(nested, b.EvalContext(), "artifact", []string{"required", "non_empty"}); err != nil {
				return err
			}
		case "qc":
			if err := validateQCStringFields(nested, b.EvalContext()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateQCStringFields(block *golden.HclBlock, context *hcl.EvalContext) error {
	if err := validateStringAttributes(block, context, "qc", []string{"model", "reasoning_effort", "permission"}); err != nil {
		return err
	}
	if err := validateStringCollections(block, context, "qc", []string{
		"allowed_tools", "disallowed_tools", "skill_directories", "skills", "disabled_skills",
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

func (b *ResearchBlock) Value() cty.Value {
	values := map[string]cty.Value{
		"retry":    retryBlockValues(b.RetryBlocks),
		"artifact": ArtifactsValue(b.planned.Artifacts, nil),
		"qc":       qcBlockValues(b.QCBlocks),
	}
	if hasValue(b.planned.TerminateTool) {
		values["result"] = cty.UnknownVal(cty.String)
	}
	return cty.ObjectVal(values)
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
		"tools":             optionalToolsValue(block.Tools),
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
	criteria, err := normalizeCriteria(value)
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
		"tools":             cty.EmptyTuple,
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

func optionalObjectValue(value cty.Value) cty.Value {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) {
		return cty.NullVal(cty.EmptyObject)
	}
	return value
}

func optionalToolsValue(value cty.Value) cty.Value {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) {
		return cty.EmptyTupleVal
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

type ArtifactBlock struct {
	Name         string `hcl:"name,label"`
	ArtifactType string `hcl:"type"`
	Path         string `hcl:"path"`
	Required     bool   `hcl:"required,optional"`
	NonEmpty     bool   `hcl:"non_empty,optional"`
}

type QCBlock struct {
	Criteria         cty.Value    `hcl:"criteria"`
	ModelProvider    cty.Value    `hcl:"model_provider,optional"`
	Model            *string      `hcl:"model,optional"`
	ReasoningEffort  *string      `hcl:"reasoning_effort,optional"`
	Tools            cty.Value    `hcl:"tools,optional"`
	AllowedTools     []string     `hcl:"allowed_tools,optional"`
	DisallowedTools  []string     `hcl:"disallowed_tools,optional"`
	SkillDirectories []string     `hcl:"skill_directories,optional"`
	Skills           []string     `hcl:"skills,optional"`
	DisabledSkills   []string     `hcl:"disabled_skills,optional"`
	Permission       *Permission  `hcl:"permission,optional"`
	MaxQCRounds      *int         `hcl:"max_qc_rounds,optional"`
	RetryBlocks      []RetryBlock `hcl:"retry,block"`
}

func (b *ResearchBlock) toConfig() (Config, error) {
	if len(b.RetryBlocks) > 1 {
		return Config{}, errors.New("research must have at most one retry block")
	}
	if len(b.QCBlocks) > 1 {
		return Config{}, errors.New("research must have at most one qc block")
	}
	timeout, err := optionalDuration(b.Timeout, "timeout")
	if err != nil {
		return Config{}, err
	}
	config := Config{
		ModelProvider:       b.ModelProvider,
		Model:               b.Model,
		ReasoningEffort:     clonePointer(b.ReasoningEffort),
		SystemPrompt:        b.SystemPrompt,
		Prompt:              clonePointer(b.Prompt),
		TerminateTool:       b.TerminateTool,
		MaxProtocolAttempts: DefaultMaxProtocolAttempts,
		Timeout:             timeout,
		Policy: SessionPolicy{
			Tools:            defaultTools(b.Tools),
			AllowedTools:     slices.Clone(b.AllowedTools),
			DisallowedTools:  slices.Clone(b.DisallowedTools),
			SkillDirectories: slices.Clone(b.SkillDirectories),
			Skills:           slices.Clone(b.Skills),
			DisabledSkills:   slices.Clone(b.DisabledSkills),
			Permission:       PermissionApproveAll,
		},
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
	if len(b.QCBlocks) == 1 {
		config.QC, err = b.QCBlocks[0].toConfig()
		if err != nil {
			return Config{}, err
		}
	}
	return config, nil
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
		Name:     b.Name,
		Type:     ArtifactType(b.ArtifactType),
		Path:     b.Path,
		Required: b.Required,
		NonEmpty: b.NonEmpty,
	}
}

func (b QCBlock) toConfig() (*QCConfig, error) {
	if len(b.RetryBlocks) > 1 {
		return nil, errors.New("qc must have at most one retry block")
	}
	criteria, err := normalizeCriteria(b.Criteria)
	if err != nil {
		return nil, err
	}
	config := &QCConfig{
		Criteria:         criteria,
		ModelProvider:    b.ModelProvider,
		Model:            clonePointer(b.Model),
		ReasoningEffort:  clonePointer(b.ReasoningEffort),
		Tools:            defaultTools(b.Tools),
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

func normalizeCriteria(value cty.Value) (cty.Value, error) {
	unmarked, marks := value.UnmarkDeepWithPaths()
	if !unmarked.IsWhollyKnown() {
		return cty.NilVal, fmt.Errorf("qc criteria must be wholly known during plan")
	}
	if unmarked.IsNull() || (!unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType()) {
		return cty.NilVal, fmt.Errorf("qc criteria must be map of string")
	}
	for _, criterion := range unmarked.AsValueMap() {
		if criterion.IsNull() || !criterion.Type().Equals(cty.String) {
			return cty.NilVal, fmt.Errorf("qc criteria must be map of string")
		}
	}
	converted, err := convert.Convert(unmarked, cty.Map(cty.String))
	if err != nil {
		return cty.NilVal, fmt.Errorf("qc criteria must be map of string: %w", err)
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

func defaultTools(value cty.Value) cty.Value {
	if value.Type().Equals(cty.NilType) || value.IsNull() {
		return cty.EmptyTupleVal
	}
	return value
}

func ArtifactsValue(artifacts []Artifact, resolvedPaths map[string]string) cty.Value {
	if len(artifacts) == 0 {
		return cty.ListValEmpty(cty.Object(map[string]cty.Type{
			"name": cty.String, "type": cty.String, "path": cty.String,
			"required": cty.Bool, "non_empty": cty.Bool,
		}))
	}
	values := make([]cty.Value, len(artifacts))
	for index, artifact := range artifacts {
		path := cty.UnknownVal(cty.String)
		if filepath.IsAbs(artifact.Path) {
			path = cty.StringVal(artifact.Path)
		}
		if resolved, ok := resolvedPaths[artifact.Name]; ok {
			path = cty.StringVal(resolved)
		}
		values[index] = cty.ObjectVal(map[string]cty.Value{
			"name":      cty.StringVal(artifact.Name),
			"type":      cty.StringVal(string(artifact.Type)),
			"path":      path,
			"required":  cty.BoolVal(artifact.Required),
			"non_empty": cty.BoolVal(artifact.NonEmpty),
		})
	}
	return cty.ListVal(values)
}

func cloneConfig(config Config) Config {
	result := config
	result.ReasoningEffort = cloneStringPointer(config.ReasoningEffort)
	result.Prompt = cloneStringPointer(config.Prompt)
	if config.Timeout != nil {
		timeout := *config.Timeout
		result.Timeout = &timeout
	}
	result.Retry = cloneRetryOverride(config.Retry)
	result.Policy.AllowedTools = slices.Clone(config.Policy.AllowedTools)
	result.Policy.DisallowedTools = slices.Clone(config.Policy.DisallowedTools)
	result.Policy.SkillDirectories = slices.Clone(config.Policy.SkillDirectories)
	result.Policy.Skills = slices.Clone(config.Policy.Skills)
	result.Policy.DisabledSkills = slices.Clone(config.Policy.DisabledSkills)
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
		qc.AllowedTools = slices.Clone(config.QC.AllowedTools)
		qc.DisallowedTools = slices.Clone(config.QC.DisallowedTools)
		qc.SkillDirectories = slices.Clone(config.QC.SkillDirectories)
		qc.Skills = slices.Clone(config.QC.Skills)
		qc.DisabledSkills = slices.Clone(config.QC.DisabledSkills)
		result.QC = &qc
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
