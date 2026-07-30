package spec

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Azure/golden"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/zclconf/go-cty/cty"
)

type PlannedTool struct {
	Address              string   `json:"address"`
	Kind                 string   `json:"kind"`
	Description          string   `json:"description"`
	Source               string   `json:"source,omitempty"`
	Program              []string `json:"program,omitempty"`
	WorkingDir           string   `json:"working_dir,omitempty"`
	InputTypeExpression  string   `json:"input_type_expression,omitempty"`
	OutputTypeExpression string   `json:"output_type_expression,omitempty"`
}

type ResearchPlan struct {
	Config        researchspec.Config
	Provider      *provider.Config
	Tools         []PlannedTool
	TerminateTool *PlannedTool
	QCProvider    *provider.Config
	QCTools       []PlannedTool
}

type researchSnapshot struct {
	Model               string                  `json:"model"`
	ReasoningEffort     *string                 `json:"reasoning_effort,omitempty"`
	SystemPrompt        string                  `json:"system_prompt"`
	Prompt              *string                 `json:"prompt,omitempty"`
	MaxProtocolAttempts int                     `json:"max_protocol_attempts"`
	TimeoutNanoseconds  *int64                  `json:"timeout_nanoseconds,omitempty"`
	Retry               provider.RetryOverride  `json:"retry"`
	Policy              policySnapshot          `json:"policy"`
	Artifacts           []researchspec.Artifact `json:"artifacts"`
	QC                  *qcSnapshot             `json:"qc,omitempty"`
	Provider            *providerSnapshot       `json:"provider,omitempty"`
	Tools               []PlannedTool           `json:"tools,omitempty"`
	TerminateTool       *PlannedTool            `json:"terminate_tool,omitempty"`
	QCProvider          *providerSnapshot       `json:"qc_provider,omitempty"`
	QCTools             []PlannedTool           `json:"qc_tools,omitempty"`
}

type policySnapshot struct {
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

func encodeResearchPlan(config researchspec.Config, planning *planningConfig) (cty.Value, error) {
	tools, err := resolveTools(config.Policy.Tools, planning)
	if err != nil {
		return cty.NilVal, err
	}
	terminate, err := resolveOptionalTool(config.TerminateTool, planning)
	if err != nil {
		return cty.NilVal, err
	}
	providerConfig, sensitive, err := resolveProvider(config.ModelProvider, planning)
	if err != nil {
		return cty.NilVal, err
	}
	snapshot := researchSnapshot{
		Model:               config.Model,
		ReasoningEffort:     clonePointer(config.ReasoningEffort),
		SystemPrompt:        config.SystemPrompt,
		Prompt:              clonePointer(config.Prompt),
		MaxProtocolAttempts: config.MaxProtocolAttempts,
		TimeoutNanoseconds:  durationNanoseconds(config.Timeout),
		Retry:               config.Retry,
		Policy:              snapshotPolicy(config.Policy),
		Artifacts:           slices.Clone(config.Artifacts),
		Provider:            providerConfig,
		Tools:               tools,
		TerminateTool:       terminate,
	}
	if config.QC != nil {
		criteria, criteriaErr := stringMap(config.QC.Criteria)
		if criteriaErr != nil {
			return cty.NilVal, criteriaErr
		}
		snapshot.QC = &qcSnapshot{
			Criteria: criteria, Model: clonePointer(config.QC.Model),
			ReasoningEffort: clonePointer(config.QC.ReasoningEffort), Retry: config.QC.Retry,
			AllowedTools: slices.Clone(config.QC.AllowedTools), DisallowedTools: slices.Clone(config.QC.DisallowedTools),
			SkillDirectories: slices.Clone(config.QC.SkillDirectories), Skills: slices.Clone(config.QC.Skills),
			DisabledSkills: slices.Clone(config.QC.DisabledSkills), Permission: clonePointer(config.QC.Permission),
			MaxRounds: config.QC.MaxRounds,
		}
		snapshot.QCTools, err = resolveTools(config.QC.Tools, planning)
		if err != nil {
			return cty.NilVal, err
		}
		var qcSensitive bool
		snapshot.QCProvider, qcSensitive, err = resolveProvider(config.QC.ModelProvider, planning)
		if err != nil {
			return cty.NilVal, err
		}
		sensitive = sensitive || qcSensitive
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
	configValue := researchspec.Config{
		Model: snapshot.Model, ReasoningEffort: clonePointer(snapshot.ReasoningEffort),
		SystemPrompt: snapshot.SystemPrompt, Prompt: clonePointer(snapshot.Prompt),
		MaxProtocolAttempts: snapshot.MaxProtocolAttempts, Timeout: nanosecondsDuration(snapshot.TimeoutNanoseconds),
		Retry: snapshot.Retry, Policy: restorePolicy(snapshot.Policy, snapshot.Tools),
		Artifacts: slices.Clone(snapshot.Artifacts),
	}
	if snapshot.TerminateTool != nil {
		configValue.TerminateTool = toolReference(*snapshot.TerminateTool)
	}
	if snapshot.QC != nil {
		configValue.QC = &researchspec.QCConfig{
			Criteria: cty.MapVal(stringValues(snapshot.QC.Criteria)), Model: clonePointer(snapshot.QC.Model),
			ReasoningEffort: clonePointer(snapshot.QC.ReasoningEffort), Retry: snapshot.QC.Retry,
			Tools: toolReferences(snapshot.QCTools), AllowedTools: slices.Clone(snapshot.QC.AllowedTools),
			DisallowedTools: slices.Clone(snapshot.QC.DisallowedTools), SkillDirectories: slices.Clone(snapshot.QC.SkillDirectories),
			Skills: slices.Clone(snapshot.QC.Skills), DisabledSkills: slices.Clone(snapshot.QC.DisabledSkills),
			Permission: clonePointer(snapshot.QC.Permission), MaxRounds: snapshot.QC.MaxRounds,
		}
	}
	return ResearchPlan{
		Config: configValue, Provider: restoreProvider(snapshot.Provider), Tools: slices.Clone(snapshot.Tools),
		TerminateTool: cloneTool(snapshot.TerminateTool), QCProvider: restoreProvider(snapshot.QCProvider),
		QCTools: slices.Clone(snapshot.QCTools),
	}, nil
}

func resolveProvider(value cty.Value, planning *planningConfig) (*providerSnapshot, bool, error) {
	address, ok, err := referenceAddress(value)
	if err != nil || !ok {
		return nil, false, err
	}
	for _, block := range golden.Blocks[*provider.ModelProviderBlock](planning) {
		if block.Address() != address {
			continue
		}
		configValue := block.ProviderConfig()
		headers, headerErr := provider.MaterializeHeaders(configValue.Headers)
		if headerErr != nil {
			return nil, false, headerErr
		}
		return &providerSnapshot{
			Type: configValue.Type, Endpoint: configValue.Endpoint, WireAPI: clonePointer(configValue.WireAPI),
			Transport: clonePointer(configValue.Transport), Headers: headers,
			HasHeaders: !configValue.Headers.Type().Equals(cty.NilType), APIKey: clonePointer(configValue.APIKey),
			APIKeyRef: clonePointer(configValue.APIKeyRef), BearerToken: clonePointer(configValue.BearerToken),
			BearerTokenRef: clonePointer(configValue.BearerTokenRef), Retry: configValue.Retry,
		}, configValue.APIKey != nil || configValue.BearerToken != nil || provider.HeadersSensitive(configValue.Headers), nil
	}
	return nil, false, fmt.Errorf("model provider %q was not planned", address)
}

func resolveTools(value cty.Value, planning *planningConfig) ([]PlannedTool, error) {
	if value.Type().Equals(cty.NilType) || value.IsNull() || value.LengthInt() == 0 {
		return nil, nil
	}
	result := make([]PlannedTool, 0, value.LengthInt())
	iterator := value.ElementIterator()
	for iterator.Next() {
		_, element := iterator.Element()
		tool, err := resolveTool(element, planning)
		if err != nil {
			return nil, err
		}
		result = append(result, tool)
	}
	return result, nil
}

func resolveOptionalTool(value cty.Value, planning *planningConfig) (*PlannedTool, error) {
	address, ok, err := referenceAddress(value)
	if err != nil || !ok {
		return nil, err
	}
	tool, err := resolveToolByAddress(address, planning)
	return &tool, err
}

func resolveTool(value cty.Value, planning *planningConfig) (PlannedTool, error) {
	address, ok, err := referenceAddress(value)
	if err != nil {
		return PlannedTool{}, err
	}
	if !ok {
		return PlannedTool{}, fmt.Errorf("typed tool reference is required")
	}
	return resolveToolByAddress(address, planning)
}

func resolveToolByAddress(address string, planning *planningConfig) (PlannedTool, error) {
	for _, block := range golden.Blocks[*toolspec.GoToolBlock](planning) {
		if block.Address() == address {
			return PlannedTool{Address: address, Kind: string(config.AddressKindGo), Description: block.Description, Source: block.Source}, nil
		}
	}
	for _, block := range golden.Blocks[*toolspec.ExternalToolBlock](planning) {
		if block.Address() == address {
			return PlannedTool{
				Address: address, Kind: string(config.AddressKindExternal), Description: block.Description,
				Program: slices.Clone(block.Program), WorkingDir: block.WorkingDir,
				InputTypeExpression:  block.HclBlock().Attributes()["input_type"].ExprString(),
				OutputTypeExpression: block.HclBlock().Attributes()["output_type"].ExprString(),
			}, nil
		}
	}
	return PlannedTool{}, fmt.Errorf("typed tool %q was not planned", address)
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
		AllowedTools: slices.Clone(policy.AllowedTools), DisallowedTools: slices.Clone(policy.DisallowedTools),
		SkillDirectories: slices.Clone(policy.SkillDirectories), Skills: slices.Clone(policy.Skills),
		DisabledSkills: slices.Clone(policy.DisabledSkills), Permission: policy.Permission,
	}
}

func restorePolicy(policy policySnapshot, tools []PlannedTool) researchspec.SessionPolicy {
	return researchspec.SessionPolicy{
		Tools: toolReferences(tools), AllowedTools: slices.Clone(policy.AllowedTools),
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

func toolReferences(tools []PlannedTool) cty.Value {
	if len(tools) == 0 {
		return cty.EmptyTupleVal
	}
	values := make([]cty.Value, len(tools))
	for index, tool := range tools {
		values[index] = toolReference(tool)
	}
	return cty.TupleVal(values)
}

func toolReference(tool PlannedTool) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal(tool.Address), "kind": cty.StringVal(tool.Kind),
	})
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

func cloneTool(tool *PlannedTool) *PlannedTool {
	if tool == nil {
		return nil
	}
	result := *tool
	result.Program = slices.Clone(tool.Program)
	return &result
}
