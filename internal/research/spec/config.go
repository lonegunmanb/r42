package spec

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	internalplan "github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

const (
	DefaultMaxProtocolAttempts   = 10
	DefaultMaxQCRounds           = 10
	DefaultCollectionBatchSize   = 10
	DefaultCollectionQCCriterion = "The registered snapshots must provide sufficient evidence to answer the research task."
)

type Permission string

const PermissionApproveAll Permission = "approve_all"

type SessionPolicy struct {
	ToolIDs          []string
	ToolCallQuota    map[string]int
	AllowedTools     []string
	DisallowedTools  []string
	SkillDirectories []string
	Skills           []string
	DisabledSkills   []string
	Permission       Permission
}

type Config struct {
	ModelProvider              cty.Value
	Model                      string
	Profile                    string
	ReasoningEffort            *string
	SystemPrompt               string
	Prompt                     *string
	TerminateToolID            *string
	MaxProtocolAttempts        int
	Timeout                    *time.Duration
	Retry                      provider.RetryOverride
	Policy                     SessionPolicy
	Artifacts                  []Artifact
	ToolUses                   []ToolUse
	QC                         *QCConfig
	CollectionModelProvider    cty.Value
	CollectionToolIDs          []string
	CollectionSkillDirectories []string
	CollectionSkills           []string
	CollectionDisabledSkills   []string
	CollectionBatchSize        int
	MaxCollectionRounds        *int
	CollectionQC               *CollectionQCConfig
}

// ToolUse assigns typed-tool input fields to HCL or to the research agent.
type ToolUse struct {
	Name           string
	ToolID         string
	Terminate      bool
	Input          cty.Value
	InputFromAgent cty.Value
	Validations    []corespec.Condition
}

func (c Config) ProfileName() string {
	if c.Profile == "" {
		return c.Model
	}
	return c.Profile
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("research model is required")
	}
	if c.Profile != "" && strings.TrimSpace(c.Profile) == "" {
		return errors.New("research profile must not be empty")
	}
	if strings.TrimSpace(c.SystemPrompt) == "" {
		return errors.New("research system prompt is required")
	}
	if err := validateOptionalProviderReference(c.ModelProvider, "research model_provider"); err != nil {
		return err
	}
	if err := validateToolIDs(c.Policy.ToolIDs, "research tool_ids"); err != nil {
		return err
	}
	if err := validateToolIDs(c.CollectionToolIDs, "research collection_tool_ids"); err != nil {
		return err
	}
	if err := validateOptionalProviderReference(c.CollectionModelProvider, "research collection_model_provider"); err != nil {
		return err
	}
	researchToolIDs := slices.Clone(c.Policy.ToolIDs)
	if c.TerminateToolID != nil {
		researchToolIDs = append(researchToolIDs, *c.TerminateToolID)
	}
	researchToolIDs = append(researchToolIDs, c.CollectionToolIDs...)
	if err := validateToolCallQuota(c.Policy.ToolCallQuota, researchToolIDs, "research"); err != nil {
		return err
	}
	if err := validateOptionalToolID(c.TerminateToolID, "research terminate_tool_id"); err != nil {
		return err
	}
	if err := validateToolUses(c.ToolUses); err != nil {
		return err
	}
	if c.ReasoningEffort != nil && strings.TrimSpace(*c.ReasoningEffort) == "" {
		return errors.New("research reasoning effort must not be empty")
	}
	if c.MaxProtocolAttempts < 0 {
		return errors.New("research max protocol attempts must be positive")
	}
	if c.Timeout != nil && *c.Timeout <= 0 {
		return errors.New("research timeout must be positive")
	}
	if c.CollectionBatchSize <= 0 {
		return errors.New("research collection batch size must be positive")
	}
	if c.MaxCollectionRounds != nil && *c.MaxCollectionRounds <= 0 {
		return errors.New("research max collection rounds must be positive")
	}
	if err := validatePermission(defaultPermission(c.Policy.Permission)); err != nil {
		return fmt.Errorf("research: %w", err)
	}
	if err := c.Retry.Validate(); err != nil {
		return fmt.Errorf("research retry: %w", err)
	}
	artifactNames := make(map[string]struct{}, len(c.Artifacts))
	for _, artifact := range c.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
		if _, exists := artifactNames[artifact.Name]; exists {
			return fmt.Errorf("artifact %s is declared more than once", artifact.Name)
		}
		artifactNames[artifact.Name] = struct{}{}
	}
	if c.QC != nil {
		if err := c.QC.Validate(); err != nil {
			return err
		}
	}
	if c.CollectionQC != nil {
		if err := c.CollectionQC.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateToolUses(toolUses []ToolUse) error {
	names := make(map[string]struct{}, len(toolUses))
	tools := make(map[string]struct{}, len(toolUses))
	terminateCount := 0
	for _, toolUse := range toolUses {
		if strings.TrimSpace(toolUse.Name) == "" {
			return errors.New("tool_use name is required")
		}
		if _, exists := names[toolUse.Name]; exists {
			return fmt.Errorf("tool_use %q is declared more than once", toolUse.Name)
		}
		names[toolUse.Name] = struct{}{}
		if strings.TrimSpace(toolUse.ToolID) == "" {
			return fmt.Errorf("tool_use %q tool_id is required", toolUse.Name)
		}
		if _, exists := tools[toolUse.ToolID]; exists {
			return fmt.Errorf("typed tool %q is used more than once", toolUse.ToolID)
		}
		tools[toolUse.ToolID] = struct{}{}
		input, err := toolUseObject(toolUse.Input, "input")
		if err != nil {
			return fmt.Errorf("tool_use %q: %w", toolUse.Name, err)
		}
		agent, err := toolUseObject(toolUse.InputFromAgent, "input_from_agent")
		if err != nil {
			return fmt.Errorf("tool_use %q: %w", toolUse.Name, err)
		}
		if err := validateAgentInputFields(agent); err != nil {
			return fmt.Errorf("tool_use %q %w", toolUse.Name, err)
		}
		for field := range input.AsValueMap() {
			if agent.Type().HasAttribute(field) {
				return fmt.Errorf("tool_use %q input field %q has multiple owners", toolUse.Name, field)
			}
		}
		if toolUse.Terminate {
			terminateCount++
		}
	}
	if terminateCount > 1 {
		return errors.New("research must have at most one terminating tool_use")
	}
	return nil
}

func validateAgentInputFields(fields cty.Value) error {
	for name, field := range fields.AsValueMap() {
		unmarked, _ := field.UnmarkDeep()
		if !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("desc") || !unmarked.Type().HasAttribute("sources") {
			return fmt.Errorf("input_from_agent field %q must be an object with desc and sources", name)
		}
		description := unmarked.GetAttr("desc")
		if !description.Type().Equals(cty.String) || !description.IsKnown() || description.IsNull() || strings.TrimSpace(description.AsString()) == "" {
			return fmt.Errorf("input_from_agent field %q desc must be a non-empty string", name)
		}
		sources := unmarked.GetAttr("sources")
		if !sources.Type().IsListType() && !sources.Type().IsTupleType() {
			return fmt.Errorf("input_from_agent field %q sources must be a list of artifact or snapshot objects", name)
		}
		if !sources.IsKnown() {
			continue
		}
		for _, source := range sources.AsValueSlice() {
			if !source.Type().IsObjectType() {
				return fmt.Errorf("input_from_agent field %q sources must contain artifact or snapshot objects", name)
			}
			for _, attribute := range []string{"id", "name", "kind", "type", "path", "description"} {
				if !source.Type().HasAttribute(attribute) {
					return fmt.Errorf("input_from_agent field %q source is missing %q", name, attribute)
				}
				if !source.GetAttr(attribute).Type().Equals(cty.String) {
					return fmt.Errorf("input_from_agent field %q source field %q must be a string", name, attribute)
				}
			}
			for _, attribute := range []string{"required", "non_empty"} {
				if !source.Type().HasAttribute(attribute) {
					return fmt.Errorf("input_from_agent field %q source is missing %q", name, attribute)
				}
				if !source.GetAttr(attribute).Type().Equals(cty.Bool) {
					return fmt.Errorf("input_from_agent field %q source field %q must be a bool", name, attribute)
				}
			}
			kind := source.GetAttr("kind")
			if kind.IsKnown() && !kind.IsNull() && kind.AsString() != "artifact" && kind.AsString() != "snapshot" {
				return fmt.Errorf("input_from_agent field %q source kind must be artifact or snapshot", name)
			}
			artifactType := source.GetAttr("type")
			if artifactType.IsKnown() && !artifactType.IsNull() && artifactType.AsString() != "file" && artifactType.AsString() != "directory" {
				return fmt.Errorf("input_from_agent field %q source type must be file or directory", name)
			}
		}
	}
	return nil
}

func toolUseObject(value cty.Value, name string) (cty.Value, error) {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) || value.IsNull() {
		return cty.EmptyObjectVal, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return cty.NilVal, fmt.Errorf("%s must be an object", name)
	}
	if unmarked.Type().IsMapType() {
		return cty.ObjectVal(unmarked.AsValueMap()), nil
	}
	return unmarked, nil
}

type QCConfig struct {
	Criteria         cty.Value
	ModelProvider    cty.Value
	Model            *string
	ReasoningEffort  *string
	Retry            provider.RetryOverride
	ToolIDs          []string
	ToolCallQuota    map[string]int
	AllowedTools     []string
	DisallowedTools  []string
	SkillDirectories []string
	Skills           []string
	DisabledSkills   []string
	Permission       *Permission
	MaxRounds        int
}

func (c QCConfig) Validate() error {
	if err := validateCriteria(c.Criteria, "qc"); err != nil {
		return err
	}
	if err := validateToolIDs(c.ToolIDs, "qc tool_ids"); err != nil {
		return err
	}
	if err := validateOptionalProviderReference(c.ModelProvider, "qc model_provider"); err != nil {
		return err
	}
	if err := validateToolCallQuota(c.ToolCallQuota, c.ToolIDs, "qc"); err != nil {
		return err
	}
	if c.Model != nil && strings.TrimSpace(*c.Model) == "" {
		return errors.New("qc model must not be empty")
	}
	if c.ReasoningEffort != nil && strings.TrimSpace(*c.ReasoningEffort) == "" {
		return errors.New("qc reasoning effort must not be empty")
	}
	if c.Permission != nil {
		if err := validatePermission(*c.Permission); err != nil {
			return fmt.Errorf("qc: %w", err)
		}
	}
	if c.MaxRounds < 0 {
		return errors.New("qc max rounds must be positive")
	}
	if err := c.Retry.Validate(); err != nil {
		return fmt.Errorf("qc retry: %w", err)
	}
	return nil
}

func validateCriteria(criteria cty.Value, scope string) error {
	if criteria.Type().Equals(cty.NilType) {
		return fmt.Errorf("%s criteria must be a non-empty map of string", scope)
	}
	unmarked, _ := criteria.UnmarkDeep()
	if !unmarked.IsWhollyKnown() {
		return fmt.Errorf("%s criteria must be wholly known during plan", scope)
	}
	if !unmarked.Type().Equals(cty.Map(cty.String)) {
		return fmt.Errorf("%s criteria must be map of string", scope)
	}
	if unmarked.IsNull() || unmarked.LengthInt() == 0 {
		return fmt.Errorf("%s criteria must be a non-empty map of string", scope)
	}
	for _, value := range unmarked.AsValueMap() {
		if value.IsNull() {
			return fmt.Errorf("%s criteria values must not be null", scope)
		}
	}
	return nil
}

// CollectionQCConfig configures the mandatory Collection QC phase. A nil
// CollectionQC on Config represents the default mandatory Collection QC
// behavior: the research model and default semantic sufficiency criteria.
type CollectionQCConfig struct {
	Criteria        cty.Value
	ModelProvider   cty.Value
	Model           *string
	ReasoningEffort *string
	Retry           provider.RetryOverride
	Permission      *Permission
}

func (c CollectionQCConfig) Validate() error {
	if hasValue(c.Criteria) {
		if err := validateCriteria(c.Criteria, "collection qc"); err != nil {
			return err
		}
	}
	if err := validateOptionalProviderReference(c.ModelProvider, "collection qc model_provider"); err != nil {
		return err
	}
	if c.Model != nil && strings.TrimSpace(*c.Model) == "" {
		return errors.New("collection qc model must not be empty")
	}
	if c.ReasoningEffort != nil && strings.TrimSpace(*c.ReasoningEffort) == "" {
		return errors.New("collection qc reasoning effort must not be empty")
	}
	if c.Permission != nil {
		if err := validatePermission(*c.Permission); err != nil {
			return fmt.Errorf("collection qc: %w", err)
		}
	}
	if err := c.Retry.Validate(); err != nil {
		return fmt.Errorf("collection qc retry: %w", err)
	}
	return nil
}

// EffectiveCollectionQC resolves the Collection QC phase configuration from
// the research defaults and the optional collection_qc overrides.
type EffectiveCollectionQC struct {
	Criteria        cty.Value
	ModelProvider   cty.Value
	Model           string
	Profile         string
	ReasoningEffort *string
	Retry           provider.RetryPolicy
	Permission      Permission
}

func DefaultCollectionQCCriteria() cty.Value {
	return cty.MapVal(map[string]cty.Value{
		"sufficiency": cty.StringVal(DefaultCollectionQCCriterion),
	})
}

func (c Config) EffectiveCollectionQC(providerRetry provider.RetryPolicy) (EffectiveCollectionQC, error) {
	if err := c.Validate(); err != nil {
		return EffectiveCollectionQC{}, err
	}
	researchRetry, err := provider.MergeRetry(providerRetry, c.Retry)
	if err != nil {
		return EffectiveCollectionQC{}, fmt.Errorf("research retry: %w", err)
	}
	effective := EffectiveCollectionQC{
		ModelProvider:   c.ModelProvider,
		Model:           c.Model,
		Profile:         c.ProfileName(),
		ReasoningEffort: cloneStringPointer(c.ReasoningEffort),
		Retry:           researchRetry,
		Permission:      defaultPermission(c.Policy.Permission),
		Criteria:        DefaultCollectionQCCriteria(),
	}
	if c.CollectionQC == nil {
		return effective, nil
	}
	effective.Retry, err = provider.MergeRetry(researchRetry, c.CollectionQC.Retry)
	if err != nil {
		return EffectiveCollectionQC{}, fmt.Errorf("collection qc retry: %w", err)
	}
	if hasValue(c.CollectionQC.ModelProvider) {
		effective.ModelProvider = c.CollectionQC.ModelProvider
	}
	if c.CollectionQC.Model != nil {
		effective.Model = *c.CollectionQC.Model
		effective.Profile = effective.Model
	}
	if c.CollectionQC.ReasoningEffort != nil {
		effective.ReasoningEffort = cloneStringPointer(c.CollectionQC.ReasoningEffort)
	}
	if c.CollectionQC.Permission != nil {
		effective.Permission = *c.CollectionQC.Permission
	}
	if hasValue(c.CollectionQC.Criteria) {
		effective.Criteria = c.CollectionQC.Criteria
	}
	return effective, nil
}

type EffectiveQC struct {
	Criteria         cty.Value
	ModelProvider    cty.Value
	Model            string
	Profile          string
	ReasoningEffort  *string
	Retry            provider.RetryPolicy
	ToolIDs          []string
	ToolCallQuota    map[string]int
	AllowedTools     []string
	DisallowedTools  []string
	SkillDirectories []string
	Skills           []string
	DisabledSkills   []string
	Permission       Permission
	MaxRounds        int
}

func (c Config) EffectiveQC(providerRetry provider.RetryPolicy) (EffectiveQC, error) {
	if c.QC == nil {
		return EffectiveQC{}, errors.New("research qc is not configured")
	}
	if err := c.Validate(); err != nil {
		return EffectiveQC{}, err
	}
	researchRetry, err := provider.MergeRetry(providerRetry, c.Retry)
	if err != nil {
		return EffectiveQC{}, fmt.Errorf("research retry: %w", err)
	}
	qcRetry, err := provider.MergeRetry(researchRetry, c.QC.Retry)
	if err != nil {
		return EffectiveQC{}, fmt.Errorf("qc retry: %w", err)
	}

	modelProvider := c.ModelProvider
	if hasValue(c.QC.ModelProvider) {
		modelProvider = c.QC.ModelProvider
	}
	model := c.Model
	profile := c.ProfileName()
	if c.QC.Model != nil {
		model = *c.QC.Model
		profile = model
	}
	reasoningEffort := cloneStringPointer(c.ReasoningEffort)
	if c.QC.ReasoningEffort != nil {
		reasoningEffort = cloneStringPointer(c.QC.ReasoningEffort)
	}
	permission := defaultPermission(c.Policy.Permission)
	if c.QC.Permission != nil {
		permission = *c.QC.Permission
	}
	maxRounds := c.QC.MaxRounds
	disallowedTools := slices.Clone(c.QC.DisallowedTools)
	if disallowedTools == nil {
		disallowedTools = DefaultQCDisallowedTools()
	}
	return EffectiveQC{
		Criteria:         c.QC.Criteria,
		ModelProvider:    modelProvider,
		Model:            model,
		Profile:          profile,
		ReasoningEffort:  reasoningEffort,
		Retry:            qcRetry,
		ToolIDs:          slices.Clone(c.QC.ToolIDs),
		ToolCallQuota:    maps.Clone(c.QC.ToolCallQuota),
		AllowedTools:     slices.Clone(c.QC.AllowedTools),
		DisallowedTools:  disallowedTools,
		SkillDirectories: slices.Clone(c.QC.SkillDirectories),
		Skills:           slices.Clone(c.QC.Skills),
		DisabledSkills:   slices.Clone(c.QC.DisabledSkills),
		Permission:       permission,
		MaxRounds:        maxRounds,
	}, nil
}

func DefaultQCDisallowedTools() []string {
	return []string{"bash", "powershell", "edit", "task", "ask_user"}
}

func validatePermission(permission Permission) error {
	if permission != PermissionApproveAll {
		return fmt.Errorf("permission must be %s", PermissionApproveAll)
	}
	return nil
}

func defaultPermission(permission Permission) Permission {
	if permission == "" {
		return PermissionApproveAll
	}
	return permission
}

func hasValue(value cty.Value) bool {
	return !value.Type().Equals(cty.NilType) && !value.IsNull()
}

func validateOptionalProviderReference(value cty.Value, name string) error {
	if !hasValue(value) {
		return nil
	}
	address, kind, ok := referenceIdentity(value)
	if !ok || kind != "provider" || !hasNamedPrefix(address, "model_provider.") {
		return fmt.Errorf("%s must be a provider reference", name)
	}
	return nil
}

func validateOptionalToolID(value *string, name string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

func validateToolIDs(values []string, name string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not contain empty values", name)
		}
	}
	return nil
}

func validateToolCallQuota(quota map[string]int, sessionToolIDs []string, scope string) error {
	for toolName, limit := range quota {
		if strings.TrimSpace(toolName) == "" {
			return fmt.Errorf("%s tool_call_quota must not contain an empty tool name", scope)
		}
		if limit < 0 {
			return fmt.Errorf("%s tool_call_quota for %q must be non-negative", scope, toolName)
		}
		if internalplan.IsToolID(toolName) && !slices.Contains(sessionToolIDs, toolName) {
			return fmt.Errorf("%s tool_call_quota references tool id %q that is not configured for this session", scope, toolName)
		}
	}
	return nil
}

func referenceIdentity(value cty.Value) (string, string, bool) {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || unmarked.IsNull() || !unmarked.Type().IsObjectType() {
		return "", "", false
	}
	fields := make(map[string]string, 2)
	for _, attribute := range []string{"address", "kind"} {
		if !unmarked.Type().HasAttribute(attribute) {
			return "", "", false
		}
		field := unmarked.GetAttr(attribute)
		if !field.IsKnown() || field.IsNull() || !field.Type().Equals(cty.String) {
			return "", "", false
		}
		fields[attribute] = field.AsString()
	}
	return fields["address"], fields["kind"], true
}

func hasNamedPrefix(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
