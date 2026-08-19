package spec

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/config"
	internalplan "github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/zclconf/go-cty/cty"
)

type ResearchPlan struct {
	Config               researchspec.Config
	Provider             *provider.Config
	QCProvider           *provider.Config
	CollectionQCProvider *provider.Config
	Expression           string
	Providers            map[string]*provider.Config
}

type researchSnapshot struct {
	Expression                 string                       `json:"expression,omitempty"`
	Providers                  map[string]*providerSnapshot `json:"providers,omitempty"`
	Model                      string                       `json:"model"`
	Profile                    string                       `json:"profile,omitempty"`
	ReasoningEffort            *string                      `json:"reasoning_effort,omitempty"`
	SystemPrompt               string                       `json:"system_prompt"`
	Prompt                     *string                      `json:"prompt,omitempty"`
	MaxProtocolAttempts        int                          `json:"max_protocol_attempts"`
	TimeoutNanoseconds         *int64                       `json:"timeout_nanoseconds,omitempty"`
	Retry                      provider.RetryOverride       `json:"retry"`
	Policy                     policySnapshot               `json:"policy"`
	Artifacts                  []researchspec.Artifact      `json:"artifacts"`
	QC                         *qcSnapshot                  `json:"qc,omitempty"`
	Provider                   *providerSnapshot            `json:"provider,omitempty"`
	TerminateToolID            *string                      `json:"terminate_tool_id,omitempty"`
	QCProvider                 *providerSnapshot            `json:"qc_provider,omitempty"`
	CollectionToolIDs          []string                     `json:"collection_tool_ids,omitempty"`
	CollectionSkillDirectories []string                     `json:"collection_skill_directories,omitempty"`
	CollectionSkills           []string                     `json:"collection_skills,omitempty"`
	CollectionDisabledSkills   []string                     `json:"collection_disabled_skills,omitempty"`
	CollectionBatchSize        int                          `json:"collection_batch_size,omitempty"`
	MaxCollectionRounds        *int                         `json:"max_collection_rounds,omitempty"`
	CollectionQC               *collectionQCSnapshot        `json:"collection_qc,omitempty"`
	CollectionQCProvider       *providerSnapshot            `json:"collection_qc_provider,omitempty"`
}

type collectionQCSnapshot struct {
	Criteria        map[string]string        `json:"criteria,omitempty"`
	Model           *string                  `json:"model,omitempty"`
	ReasoningEffort *string                  `json:"reasoning_effort,omitempty"`
	Retry           provider.RetryOverride   `json:"retry"`
	Permission      *researchspec.Permission `json:"permission,omitempty"`
}

type policySnapshot struct {
	ToolIDs          []string                `json:"tool_ids,omitempty"`
	ToolCallQuota    map[string]int          `json:"tool_call_quota,omitempty"`
	AllowedTools     []string                `json:"allowed_tools,omitempty"`
	DisallowedTools  []string                `json:"disallowed_tools,omitempty"`
	SkillDirectories []string                `json:"skill_directories,omitempty"`
	Skills           []string                `json:"skills,omitempty"`
	DisabledSkills   []string                `json:"disabled_skills,omitempty"`
	Permission       researchspec.Permission `json:"permission"`
}

type qcSnapshot struct {
	Criteria         map[string]string        `json:"criteria"`
	Model            *string                  `json:"model,omitempty"`
	ReasoningEffort  *string                  `json:"reasoning_effort,omitempty"`
	Retry            provider.RetryOverride   `json:"retry"`
	ToolIDs          []string                 `json:"tool_ids,omitempty"`
	ToolCallQuota    map[string]int           `json:"tool_call_quota,omitempty"`
	AllowedTools     []string                 `json:"allowed_tools,omitempty"`
	DisallowedTools  []string                 `json:"disallowed_tools,omitempty"`
	SkillDirectories []string                 `json:"skill_directories,omitempty"`
	Skills           []string                 `json:"skills,omitempty"`
	DisabledSkills   []string                 `json:"disabled_skills,omitempty"`
	Permission       *researchspec.Permission `json:"permission,omitempty"`
	MaxRounds        int                      `json:"max_rounds"`
}

type providerSnapshot struct {
	Type           provider.Type          `json:"type"`
	Endpoint       string                 `json:"endpoint"`
	WireAPI        *provider.WireAPI      `json:"wire_api,omitempty"`
	Transport      *provider.Transport    `json:"transport,omitempty"`
	Headers        map[string]string      `json:"headers,omitempty"`
	HasHeaders     bool                   `json:"has_headers,omitempty"`
	APIKey         *string                `json:"api_key,omitempty"`
	APIKeyRef      *string                `json:"api_key_ref,omitempty"`
	BearerToken    *string                `json:"bearer_token,omitempty"`
	BearerTokenRef *string                `json:"bearer_token_ref,omitempty"`
	Retry          provider.RetryOverride `json:"retry"`
}

func EncodeResearchPlan(
	block *researchspec.ResearchBlock,
	planning golden.Config,
	registry map[string]internalplan.ToolSpec,
) (cty.Value, error) {
	if block == nil {
		return cty.NilVal, fmt.Errorf("research block is required")
	}
	if expression := block.DeferredTaskExpression(); expression != "" {
		providers, sensitive, err := snapshotAllProviders(planning)
		if err != nil {
			return cty.NilVal, err
		}
		encoded, err := json.Marshal(researchSnapshot{Expression: expression, Providers: providers})
		if err != nil {
			return cty.NilVal, fmt.Errorf("encode deferred research plan: %w", err)
		}
		payload := cty.StringVal(string(encoded))
		if sensitive {
			payload = corespec.MarkSensitive(payload)
		}
		return cty.ObjectVal(map[string]cty.Value{
			"model": cty.StringVal("<dynamic>"), "payload": payload,
		}), nil
	}
	config := block.ResearchConfig()
	if err := ValidateResearchToolIDs(config, registry); err != nil {
		return cty.NilVal, err
	}
	providerConfig, sensitive, err := resolveProvider(config.ModelProvider, planning)
	if err != nil {
		return cty.NilVal, err
	}
	snapshot := researchSnapshot{
		Model:                      config.Model,
		Profile:                    config.ProfileName(),
		ReasoningEffort:            clonePointer(config.ReasoningEffort),
		SystemPrompt:               config.SystemPrompt,
		Prompt:                     clonePointer(config.Prompt),
		MaxProtocolAttempts:        config.MaxProtocolAttempts,
		TimeoutNanoseconds:         durationNanoseconds(config.Timeout),
		Retry:                      config.Retry,
		Policy:                     snapshotPolicy(config.Policy),
		Artifacts:                  slices.Clone(config.Artifacts),
		Provider:                   providerConfig,
		TerminateToolID:            clonePointer(config.TerminateToolID),
		CollectionToolIDs:          slices.Clone(config.CollectionToolIDs),
		CollectionSkillDirectories: slices.Clone(config.CollectionSkillDirectories),
		CollectionSkills:           slices.Clone(config.CollectionSkills),
		CollectionDisabledSkills:   slices.Clone(config.CollectionDisabledSkills),
		CollectionBatchSize:        config.CollectionBatchSize,
		MaxCollectionRounds:        clonePointer(config.MaxCollectionRounds),
	}
	if config.QC != nil {
		criteria, criteriaErr := stringMap(config.QC.Criteria)
		if criteriaErr != nil {
			return cty.NilVal, criteriaErr
		}
		snapshot.QC = &qcSnapshot{
			Criteria: criteria, Model: clonePointer(config.QC.Model),
			ReasoningEffort: clonePointer(config.QC.ReasoningEffort), Retry: config.QC.Retry,
			ToolIDs:       slices.Clone(config.QC.ToolIDs),
			ToolCallQuota: maps.Clone(config.QC.ToolCallQuota),
			AllowedTools:  slices.Clone(config.QC.AllowedTools), DisallowedTools: slices.Clone(config.QC.DisallowedTools),
			SkillDirectories: slices.Clone(config.QC.SkillDirectories), Skills: slices.Clone(config.QC.Skills),
			DisabledSkills: slices.Clone(config.QC.DisabledSkills), Permission: clonePointer(config.QC.Permission),
			MaxRounds: config.QC.MaxRounds,
		}
		var qcSensitive bool
		snapshot.QCProvider, qcSensitive, err = resolveProvider(config.QC.ModelProvider, planning)
		if err != nil {
			return cty.NilVal, err
		}
		sensitive = sensitive || qcSensitive
	}
	if config.CollectionQC != nil {
		snapshot.CollectionQC = &collectionQCSnapshot{
			Model:           clonePointer(config.CollectionQC.Model),
			ReasoningEffort: clonePointer(config.CollectionQC.ReasoningEffort),
			Retry:           config.CollectionQC.Retry,
			Permission:      clonePointer(config.CollectionQC.Permission),
		}
		if !config.CollectionQC.Criteria.Type().Equals(cty.NilType) && !config.CollectionQC.Criteria.IsNull() {
			criteria, criteriaErr := stringMap(config.CollectionQC.Criteria)
			if criteriaErr != nil {
				return cty.NilVal, criteriaErr
			}
			snapshot.CollectionQC.Criteria = criteria
		}
		var collectionQCSensitive bool
		snapshot.CollectionQCProvider, collectionQCSensitive, err = resolveProvider(config.CollectionQC.ModelProvider, planning)
		if err != nil {
			return cty.NilVal, err
		}
		sensitive = sensitive || collectionQCSensitive
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return cty.NilVal, fmt.Errorf("encode research plan: %w", err)
	}
	value := cty.StringVal(string(encoded))
	if sensitive {
		value = corespec.MarkSensitive(value)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"model":   cty.StringVal(config.Model),
		"payload": value,
	}), nil
}

func DecodeResearchPlan(value cty.Value) (ResearchPlan, error) {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || unmarked.IsNull() || !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("payload") {
		return ResearchPlan{}, fmt.Errorf("research plan snapshot is invalid")
	}
	payload := unmarked.GetAttr("payload")
	if payload.IsNull() || !payload.IsKnown() || !payload.Type().Equals(cty.String) {
		return ResearchPlan{}, fmt.Errorf("research plan payload is invalid")
	}
	var snapshot researchSnapshot
	if err := json.Unmarshal([]byte(payload.AsString()), &snapshot); err != nil {
		return ResearchPlan{}, fmt.Errorf("decode research plan: %w", err)
	}
	if snapshot.Expression != "" {
		return ResearchPlan{
			Expression: snapshot.Expression,
			Providers:  restoreProviders(snapshot.Providers),
		}, nil
	}
	configValue := researchspec.Config{
		Model: snapshot.Model, Profile: snapshot.Profile, ReasoningEffort: clonePointer(snapshot.ReasoningEffort),
		SystemPrompt: snapshot.SystemPrompt, Prompt: clonePointer(snapshot.Prompt),
		MaxProtocolAttempts: snapshot.MaxProtocolAttempts, Timeout: nanosecondsDuration(snapshot.TimeoutNanoseconds),
		Retry: snapshot.Retry, Policy: restorePolicy(snapshot.Policy),
		Artifacts:                  slices.Clone(snapshot.Artifacts),
		TerminateToolID:            clonePointer(snapshot.TerminateToolID),
		CollectionBatchSize:        researchspec.DefaultCollectionBatchSize,
		MaxCollectionRounds:        clonePointer(snapshot.MaxCollectionRounds),
		CollectionToolIDs:          slices.Clone(snapshot.CollectionToolIDs),
		CollectionSkillDirectories: slices.Clone(snapshot.CollectionSkillDirectories),
		CollectionSkills:           slices.Clone(snapshot.CollectionSkills),
		CollectionDisabledSkills:   slices.Clone(snapshot.CollectionDisabledSkills),
	}
	if snapshot.CollectionBatchSize > 0 {
		configValue.CollectionBatchSize = snapshot.CollectionBatchSize
	}
	if configValue.Profile == "" {
		configValue.Profile = configValue.Model
	}
	if snapshot.QC != nil {
		configValue.QC = &researchspec.QCConfig{
			Criteria: cty.MapVal(stringValues(snapshot.QC.Criteria)), Model: clonePointer(snapshot.QC.Model),
			ReasoningEffort: clonePointer(snapshot.QC.ReasoningEffort), Retry: snapshot.QC.Retry,
			ToolIDs: slices.Clone(snapshot.QC.ToolIDs), AllowedTools: slices.Clone(snapshot.QC.AllowedTools),
			ToolCallQuota:   maps.Clone(snapshot.QC.ToolCallQuota),
			DisallowedTools: slices.Clone(snapshot.QC.DisallowedTools), SkillDirectories: slices.Clone(snapshot.QC.SkillDirectories),
			Skills: slices.Clone(snapshot.QC.Skills), DisabledSkills: slices.Clone(snapshot.QC.DisabledSkills),
			Permission: clonePointer(snapshot.QC.Permission), MaxRounds: snapshot.QC.MaxRounds,
		}
	}
	if snapshot.CollectionQC != nil {
		criteria := cty.NilVal
		if len(snapshot.CollectionQC.Criteria) > 0 {
			criteria = cty.MapVal(stringValues(snapshot.CollectionQC.Criteria))
		}
		configValue.CollectionQC = &researchspec.CollectionQCConfig{
			Criteria: criteria, Model: clonePointer(snapshot.CollectionQC.Model),
			ReasoningEffort: clonePointer(snapshot.CollectionQC.ReasoningEffort),
			Retry:           snapshot.CollectionQC.Retry, Permission: clonePointer(snapshot.CollectionQC.Permission),
		}
	}
	return ResearchPlan{
		Config: configValue, Provider: restoreProvider(snapshot.Provider),
		QCProvider:           restoreProvider(snapshot.QCProvider),
		CollectionQCProvider: restoreProvider(snapshot.CollectionQCProvider),
	}, nil
}

func (p ResearchPlan) Resolve(config researchspec.Config) (ResearchPlan, error) {
	if p.Expression == "" {
		return p, nil
	}
	resolved, err := (DynamicResearchPlan{Providers: p.Providers}).Resolve(config)
	if err != nil {
		return ResearchPlan{}, err
	}
	return ResearchPlan{
		Config: resolved.Config, Provider: resolved.Provider, QCProvider: resolved.QCProvider,
		CollectionQCProvider: resolved.CollectionQCProvider,
	}, nil
}

func resolveProvider(value cty.Value, planning golden.Config) (*providerSnapshot, bool, error) {
	address, ok, err := referenceAddress(value)
	if err != nil || !ok {
		return nil, false, err
	}
	for _, block := range golden.Blocks[*provider.ModelProviderBlock](planning) {
		if block.Address() != address {
			continue
		}
		return snapshotProviderConfig(block.ProviderConfig())
	}
	return nil, false, fmt.Errorf("model provider %q was not planned", address)
}

func snapshotProviderConfig(configValue provider.Config) (*providerSnapshot, bool, error) {
	headers, err := provider.MaterializeHeaders(configValue.Headers)
	if err != nil {
		return nil, false, err
	}
	return &providerSnapshot{
		Type: configValue.Type, Endpoint: configValue.Endpoint, WireAPI: clonePointer(configValue.WireAPI),
		Transport: clonePointer(configValue.Transport), Headers: headers,
		HasHeaders: !configValue.Headers.Type().Equals(cty.NilType), APIKey: clonePointer(configValue.APIKey),
		APIKeyRef: clonePointer(configValue.APIKeyRef), BearerToken: clonePointer(configValue.BearerToken),
		BearerTokenRef: clonePointer(configValue.BearerTokenRef), Retry: configValue.Retry,
	}, configValue.APIKey != nil || configValue.BearerToken != nil || provider.HeadersSensitive(configValue.Headers), nil
}

func BuildToolRegistry(
	planning golden.Config,
	modules map[string]ModulePlan,
) map[string]internalplan.ToolSpec {
	registry := make(map[string]internalplan.ToolSpec)
	for _, block := range golden.Blocks[*toolspec.GoToolBlock](planning) {
		definition := internalplan.ToolSpec{
			ID: block.Id(), Address: block.CanonicalAddress(), Kind: string(config.AddressKindGo),
			Description: block.Description, Source: block.Source,
		}
		registry[definition.ID] = definition
	}
	for _, block := range golden.Blocks[*toolspec.ExternalToolBlock](planning) {
		definition := internalplan.ToolSpec{
			ID: block.Id(), Address: block.CanonicalAddress(), Kind: string(config.AddressKindExternal),
			Description: block.Description, Program: slices.Clone(block.Program), WorkingDir: block.WorkingDir,
			InputTypeExpression:  block.HclBlock().Attributes()["input_type"].ExprString(),
			OutputTypeExpression: block.HclBlock().Attributes()["output_type"].ExprString(),
		}
		registry[definition.ID] = definition
	}
	for _, module := range modules {
		childRegistry := module.Saved.Tools()
		for _, output := range module.Outputs {
			value, _ := output.Value.UnmarkDeep()
			if !value.IsKnown() || value.IsNull() || !value.Type().Equals(cty.String) {
				continue
			}
			definition, ok := childRegistry[value.AsString()]
			if ok {
				registry[definition.ID] = definition
			}
		}
	}
	return registry
}

func ValidateResearchToolIDs(config researchspec.Config, registry map[string]internalplan.ToolSpec) error {
	if err := validateConfiguredToolIDs(config.Policy.ToolIDs, registry, "research tool_ids"); err != nil {
		return err
	}
	if config.TerminateToolID != nil {
		if err := validateConfiguredToolIDs([]string{*config.TerminateToolID}, registry, "research terminate_tool_id"); err != nil {
			return err
		}
	}
	if err := validateConfiguredToolIDs(config.CollectionToolIDs, registry, "research collection_tool_ids"); err != nil {
		return err
	}
	if err := validatePlannedToolCallQuota(config.Policy.ToolCallQuota, registry, "research"); err != nil {
		return err
	}
	if err := validateToolFilters(config.Policy.AllowedTools, registry, "research allowed_tools"); err != nil {
		return err
	}
	if err := validateToolFilters(config.Policy.DisallowedTools, registry, "research disallowed_tools"); err != nil {
		return err
	}
	if config.QC == nil {
		return nil
	}
	if err := validateConfiguredToolIDs(config.QC.ToolIDs, registry, "qc tool_ids"); err != nil {
		return err
	}
	if err := validatePlannedToolCallQuota(config.QC.ToolCallQuota, registry, "qc"); err != nil {
		return err
	}
	if err := validateToolFilters(config.QC.AllowedTools, registry, "qc allowed_tools"); err != nil {
		return err
	}
	return validateToolFilters(config.QC.DisallowedTools, registry, "qc disallowed_tools")
}

func validatePlannedToolCallQuota(
	quota map[string]int,
	registry map[string]internalplan.ToolSpec,
	scope string,
) error {
	for toolID := range quota {
		if !internalplan.IsToolID(toolID) {
			continue
		}
		if _, ok := registry[toolID]; !ok {
			return fmt.Errorf("%s tool_call_quota references tool id %q that was not planned", scope, toolID)
		}
	}
	return nil
}

func validateConfiguredToolIDs(
	ids []string,
	registry map[string]internalplan.ToolSpec,
	attribute string,
) error {
	for _, id := range ids {
		if _, ok := registry[id]; !ok {
			return fmt.Errorf("%s references tool id %q that was not planned", attribute, id)
		}
	}
	return nil
}

func validateToolFilters(
	filters []string,
	registry map[string]internalplan.ToolSpec,
	attribute string,
) error {
	for _, filter := range filters {
		if _, ok := registry[filter]; ok {
			continue
		}
		if internalplan.IsToolID(filter) {
			return fmt.Errorf("%s references tool id %q that was not planned", attribute, filter)
		}
	}
	return nil
}

func referenceAddress(value cty.Value) (string, bool, error) {
	if value.Type().Equals(cty.NilType) || value.IsNull() {
		return "", false, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("address") {
		return "", false, fmt.Errorf("planned reference does not contain an address")
	}
	return unmarked.GetAttr("address").AsString(), true, nil
}

func snapshotPolicy(policy researchspec.SessionPolicy) policySnapshot {
	return policySnapshot{
		ToolIDs:       slices.Clone(policy.ToolIDs),
		ToolCallQuota: maps.Clone(policy.ToolCallQuota),
		AllowedTools:  slices.Clone(policy.AllowedTools), DisallowedTools: slices.Clone(policy.DisallowedTools),
		SkillDirectories: slices.Clone(policy.SkillDirectories), Skills: slices.Clone(policy.Skills),
		DisabledSkills: slices.Clone(policy.DisabledSkills), Permission: policy.Permission,
	}
}

func restorePolicy(policy policySnapshot) researchspec.SessionPolicy {
	return researchspec.SessionPolicy{
		ToolIDs: slices.Clone(policy.ToolIDs), AllowedTools: slices.Clone(policy.AllowedTools),
		ToolCallQuota:   maps.Clone(policy.ToolCallQuota),
		DisallowedTools: slices.Clone(policy.DisallowedTools), SkillDirectories: slices.Clone(policy.SkillDirectories),
		Skills: slices.Clone(policy.Skills), DisabledSkills: slices.Clone(policy.DisabledSkills), Permission: policy.Permission,
	}
}

func restoreProvider(snapshot *providerSnapshot) *provider.Config {
	if snapshot == nil {
		return nil
	}
	headers := cty.NilVal
	if snapshot.HasHeaders {
		values := stringValues(snapshot.Headers)
		if len(values) == 0 {
			headers = cty.MapValEmpty(cty.String)
		} else {
			headers = cty.MapVal(values)
		}
	}
	return &provider.Config{
		Type: snapshot.Type, Endpoint: snapshot.Endpoint, WireAPI: clonePointer(snapshot.WireAPI),
		Transport: clonePointer(snapshot.Transport), Headers: headers, APIKey: clonePointer(snapshot.APIKey),
		APIKeyRef: clonePointer(snapshot.APIKeyRef), BearerToken: clonePointer(snapshot.BearerToken),
		BearerTokenRef: clonePointer(snapshot.BearerTokenRef), Retry: snapshot.Retry,
	}
}

func stringMap(value cty.Value) (map[string]string, error) {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || unmarked.IsNull() || !unmarked.Type().Equals(cty.Map(cty.String)) {
		return nil, fmt.Errorf("planned string map is invalid")
	}
	result := make(map[string]string, unmarked.LengthInt())
	for name, element := range unmarked.AsValueMap() {
		result[name] = element.AsString()
	}
	return result, nil
}

func stringValues(values map[string]string) map[string]cty.Value {
	result := make(map[string]cty.Value, len(values))
	for name, value := range values {
		result[name] = cty.StringVal(value)
	}
	return result
}

func durationNanoseconds(value *time.Duration) *int64 {
	if value == nil {
		return nil
	}
	nanoseconds := int64(*value)
	return &nanoseconds
}

func nanosecondsDuration(value *int64) *time.Duration {
	if value == nil {
		return nil
	}
	duration := time.Duration(*value)
	return &duration
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
