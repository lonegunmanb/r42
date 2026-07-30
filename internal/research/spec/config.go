package spec

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	r42config "github.com/lonegunmanb/r42/internal/config"
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
	Tools            cty.Value
	AllowedTools     []string
	DisallowedTools  []string
	SkillDirectories []string
	Skills           []string
	DisabledSkills   []string
	Permission       Permission
}

type Config struct {
	ModelProvider       cty.Value
	Model               string
	ReasoningEffort     *string
	SystemPrompt        string
	Prompt              *string
	TerminateTool       cty.Value
	MaxProtocolAttempts int
	Timeout             *time.Duration
	Retry               provider.RetryOverride
	Policy              SessionPolicy
	Artifacts           []Artifact
	QC                  *QCConfig
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("research model is required")
	}
	if strings.TrimSpace(c.SystemPrompt) == "" {
		return errors.New("research system prompt is required")
	}
	if err := validateOptionalProviderReference(c.ModelProvider, "research model_provider"); err != nil {
		return err
	}
	if err := validateToolReferences(c.Policy.Tools, "research tools"); err != nil {
		return err
	}
	if err := validateOptionalToolReference(c.TerminateTool, "research terminate_tool"); err != nil {
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
	Criteria         cty.Value
	ModelProvider    cty.Value
	Model            *string
	ReasoningEffort  *string
	Retry            provider.RetryOverride
	Tools            cty.Value
	AllowedTools     []string
	DisallowedTools  []string
	SkillDirectories []string
	Skills           []string
	DisabledSkills   []string
	Permission       *Permission
	MaxRounds        int
}

func (c QCConfig) Validate() error {
	if err := validateCriteria(c.Criteria); err != nil {
		return err
	}
	if err := validateOptionalProviderReference(c.ModelProvider, "qc model_provider"); err != nil {
		return err
	}
	if err := validateToolReferences(c.Tools, "qc tools"); err != nil {
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
	Criteria         cty.Value
	ModelProvider    cty.Value
	Model            string
	ReasoningEffort  *string
	Retry            provider.RetryPolicy
	Tools            cty.Value
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
	if c.QC.Model != nil {
		model = *c.QC.Model
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
	tools := c.QC.Tools
	if tools.Type().Equals(cty.NilType) {
		tools = cty.EmptyTupleVal
	}
	return EffectiveQC{
		Criteria:         c.QC.Criteria,
		ModelProvider:    modelProvider,
		Model:            model,
		ReasoningEffort:  reasoningEffort,
		Retry:            qcRetry,
		Tools:            tools,
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

func validateOptionalToolReference(value cty.Value, name string) error {
	if !hasValue(value) {
		return nil
	}
	if !isToolReference(value) {
		return fmt.Errorf("%s must be a typed tool reference", name)
	}
	return nil
}

func validateToolReferences(values cty.Value, name string) error {
	if values.Type().Equals(cty.NilType) {
		return nil
	}
	unmarked, _ := values.UnmarkDeep()
	if unmarked.IsNull() || !unmarked.IsKnown() || !unmarked.Type().IsTupleType() {
		return fmt.Errorf("%s must be a tuple of typed tool references", name)
	}
	for iterator := unmarked.ElementIterator(); iterator.Next(); {
		_, value := iterator.Element()
		if !isToolReference(value) {
			return fmt.Errorf("%s must contain typed tool references", name)
		}
	}
	return nil
}

func isToolReference(value cty.Value) bool {
	address, kind, ok := referenceIdentity(value)
	if !ok {
		return false
	}
	_, ok = r42config.AddressFromValue(r42config.Address{
		Kind:  r42config.AddressKind(kind),
		Value: address,
	}.CtyValue())
	return ok
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
