package spec

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/zclconf/go-cty/cty"
)

const (
	DefaultMaxProtocolAttempts = 10
	DefaultMaxQCRounds         = 10
)

type Permission string

const PermissionApproveAll Permission = "approve_all"

type SessionPolicy struct {
	ToolIDs            []string
	TypedToolCallQuota map[string]int
	AllowedTools       []string
	DisallowedTools    []string
	SkillDirectories   []string
	Skills             []string
	DisabledSkills     []string
	Permission         Permission
}

type Config struct {
	ModelProvider       cty.Value
	Model               string
	Profile             string
	ReasoningEffort     *string
	SystemPrompt        string
	Prompt              *string
	TerminateToolID     *string
	MaxProtocolAttempts int
	Timeout             *time.Duration
	Retry               provider.RetryOverride
	Policy              SessionPolicy
	Artifacts           []Artifact
	QC                  *QCConfig
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
	researchToolIDs := slices.Clone(c.Policy.ToolIDs)
	if c.TerminateToolID != nil {
		researchToolIDs = append(researchToolIDs, *c.TerminateToolID)
	}
	if err := validateTypedToolCallQuota(c.Policy.TypedToolCallQuota, researchToolIDs, "research"); err != nil {
		return err
	}
	if err := validateOptionalToolID(c.TerminateToolID, "research terminate_tool_id"); err != nil {
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
	return nil
}

type QCConfig struct {
	Criteria           cty.Value
	ModelProvider      cty.Value
	Model              *string
	ReasoningEffort    *string
	Retry              provider.RetryOverride
	ToolIDs            []string
	TypedToolCallQuota map[string]int
	AllowedTools       []string
	DisallowedTools    []string
	SkillDirectories   []string
	Skills             []string
	DisabledSkills     []string
	Permission         *Permission
	MaxRounds          int
}

func (c QCConfig) Validate() error {
	if err := validateCriteria(c.Criteria); err != nil {
		return err
	}
	if err := validateOptionalProviderReference(c.ModelProvider, "qc model_provider"); err != nil {
		return err
	}
	if err := validateToolIDs(c.ToolIDs, "qc tool_ids"); err != nil {
		return err
	}
	if err := validateTypedToolCallQuota(c.TypedToolCallQuota, c.ToolIDs, "qc"); err != nil {
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

func validateCriteria(criteria cty.Value) error {
	if criteria.Type().Equals(cty.NilType) {
		return errors.New("qc criteria must be a non-empty map of string")
	}
	unmarked, _ := criteria.UnmarkDeep()
	if !unmarked.IsWhollyKnown() {
		return errors.New("qc criteria must be wholly known during plan")
	}
	if !unmarked.Type().Equals(cty.Map(cty.String)) {
		return errors.New("qc criteria must be map of string")
	}
	if unmarked.IsNull() || unmarked.LengthInt() == 0 {
		return errors.New("qc criteria must be a non-empty map of string")
	}
	for _, value := range unmarked.AsValueMap() {
		if value.IsNull() {
			return errors.New("qc criteria values must not be null")
		}
	}
	return nil
}

type EffectiveQC struct {
	Criteria           cty.Value
	ModelProvider      cty.Value
	Model              string
	Profile            string
	ReasoningEffort    *string
	Retry              provider.RetryPolicy
	ToolIDs            []string
	TypedToolCallQuota map[string]int
	AllowedTools       []string
	DisallowedTools    []string
	SkillDirectories   []string
	Skills             []string
	DisabledSkills     []string
	Permission         Permission
	MaxRounds          int
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
		Criteria:           c.QC.Criteria,
		ModelProvider:      modelProvider,
		Model:              model,
		Profile:            profile,
		ReasoningEffort:    reasoningEffort,
		Retry:              qcRetry,
		ToolIDs:            slices.Clone(c.QC.ToolIDs),
		TypedToolCallQuota: maps.Clone(c.QC.TypedToolCallQuota),
		AllowedTools:       slices.Clone(c.QC.AllowedTools),
		DisallowedTools:    disallowedTools,
		SkillDirectories:   slices.Clone(c.QC.SkillDirectories),
		Skills:             slices.Clone(c.QC.Skills),
		DisabledSkills:     slices.Clone(c.QC.DisabledSkills),
		Permission:         permission,
		MaxRounds:          maxRounds,
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

func validateTypedToolCallQuota(quota map[string]int, sessionToolIDs []string, scope string) error {
	for toolID, limit := range quota {
		if limit < 0 {
			return fmt.Errorf("%s typed_tool_call_quota for %q must be non-negative", scope, toolID)
		}
		if !slices.Contains(sessionToolIDs, toolID) {
			return fmt.Errorf("%s typed_tool_call_quota references tool id %q that is not configured for this session", scope, toolID)
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
