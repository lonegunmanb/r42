package spec

import (
	"encoding/json"
	"fmt"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/mcp"
	internalplan "github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

type DynamicResearchPlan struct {
	Expression   string
	Providers    map[string]*provider.Config
	Serial       bool
	Tasks        cty.Value
	MCPTools     mcp.ToolRegistry
	MCPResources mcp.ResourceRegistry
}

type dynamicResearchSnapshot struct {
	Expression   string                       `json:"expression"`
	Providers    map[string]*providerSnapshot `json:"providers,omitempty"`
	Serial       bool                         `json:"serial,omitempty"`
	Tasks        *dynamicTasksSnapshot        `json:"tasks,omitempty"`
	MCPTools     mcp.ToolRegistry             `json:"mcp_tools,omitempty"`
	MCPResources mcp.ResourceRegistry         `json:"mcp_resources,omitempty"`
}

type dynamicTasksSnapshot struct {
	Type  json.RawMessage `json:"type"`
	Value json.RawMessage `json:"value"`
}

func EncodeDynamicResearchPlan(
	block *researchspec.DynamicResearchBlock,
	planning golden.Config,
	registry map[string]internalplan.ToolSpec,
) (cty.Value, error) {
	if block == nil {
		return cty.NilVal, fmt.Errorf("dynamic research block is required")
	}
	expression := block.TasksExpression()
	if expression == "" {
		return cty.NilVal, fmt.Errorf("dynamic research tasks expression is required")
	}
	providers, sensitive, err := snapshotAllProviders(planning)
	if err != nil {
		return cty.NilVal, err
	}
	mcpTools := BuildMCPToolRegistry(planning)
	if block.Tasks.IsWhollyKnown() {
		configs, _, decodeErr := researchspec.DecodeDynamicTasks(block.Tasks)
		if decodeErr != nil {
			return cty.NilVal, decodeErr
		}
		plan := DynamicResearchPlan{Providers: restoreProviders(providers), MCPTools: mcpTools.Clone(), MCPResources: BuildMCPResourceRegistry(planning)}
		for index, config := range configs {
			resolved, resolveErr := plan.Resolve(config)
			if resolveErr != nil {
				return cty.NilVal, fmt.Errorf("dynamic research task %d: %w", index, resolveErr)
			}
			if validateErr := ValidateResearchToolIDs(resolved.Config, registry); validateErr != nil {
				return cty.NilVal, fmt.Errorf("dynamic research task %d: %w", index, validateErr)
			}
		}
	}
	var taskSnapshot *dynamicTasksSnapshot
	if block.Tasks.IsWhollyKnown() {
		unmarked, _ := block.Tasks.UnmarkDeep()
		typeJSON, typeErr := ctyjson.MarshalType(unmarked.Type())
		valueJSON, valueErr := ctyjson.Marshal(unmarked, unmarked.Type())
		if typeErr == nil && valueErr == nil {
			taskSnapshot = &dynamicTasksSnapshot{Type: typeJSON, Value: valueJSON}
		}
	}
	encoded, err := json.Marshal(dynamicResearchSnapshot{
		Expression: expression, Providers: providers, Serial: block.Serial, Tasks: taskSnapshot, MCPTools: mcpTools, MCPResources: BuildMCPResourceRegistry(planning),
	})
	if err != nil {
		return cty.NilVal, fmt.Errorf("encode dynamic research plan: %w", err)
	}
	payload := cty.StringVal(string(encoded))
	if sensitive {
		payload = corespec.MarkSensitive(payload)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"model":   cty.StringVal("<dynamic>"),
		"payload": payload,
	}), nil
}

func DecodeDynamicResearchPlan(value cty.Value) (DynamicResearchPlan, error) {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || unmarked.IsNull() || !unmarked.Type().IsObjectType() ||
		!unmarked.Type().HasAttribute("payload") {
		return DynamicResearchPlan{}, fmt.Errorf("dynamic research plan snapshot is invalid")
	}
	payload := unmarked.GetAttr("payload")
	if payload.IsNull() || !payload.IsKnown() || !payload.Type().Equals(cty.String) {
		return DynamicResearchPlan{}, fmt.Errorf("dynamic research plan payload is invalid")
	}
	var snapshot dynamicResearchSnapshot
	if err := json.Unmarshal([]byte(payload.AsString()), &snapshot); err != nil {
		return DynamicResearchPlan{}, fmt.Errorf("decode dynamic research plan: %w", err)
	}
	if snapshot.Expression == "" {
		return DynamicResearchPlan{}, fmt.Errorf("dynamic research plan expression is required")
	}
	result := DynamicResearchPlan{
		Expression:   snapshot.Expression,
		Providers:    restoreProviders(snapshot.Providers),
		Serial:       snapshot.Serial,
		MCPTools:     snapshot.MCPTools.Clone(),
		MCPResources: snapshot.MCPResources.Clone(),
	}
	if snapshot.Tasks != nil {
		taskType, err := ctyjson.UnmarshalType(snapshot.Tasks.Type)
		if err != nil {
			return DynamicResearchPlan{}, fmt.Errorf("decode dynamic research task type: %w", err)
		}
		tasks, err := ctyjson.Unmarshal(snapshot.Tasks.Value, taskType)
		if err != nil {
			return DynamicResearchPlan{}, fmt.Errorf("decode dynamic research tasks: %w", err)
		}
		result.Tasks = tasks
	}
	return result, nil
}

func (p DynamicResearchPlan) Resolve(config researchspec.Config) (ResearchPlan, error) {
	modelProvider, err := p.resolveProvider(config.ModelProvider)
	if err != nil {
		return ResearchPlan{}, fmt.Errorf("research model_provider: %w", err)
	}
	collectionProvider, err := p.resolveProvider(config.CollectionModelProvider)
	if err != nil {
		return ResearchPlan{}, fmt.Errorf("collection model_provider: %w", err)
	}
	var qcProvider *provider.Config
	if config.QC != nil {
		qcProvider, err = p.resolveProvider(config.QC.ModelProvider)
		if err != nil {
			return ResearchPlan{}, fmt.Errorf("qc model_provider: %w", err)
		}
	}
	var collectionQCProvider *provider.Config
	if config.CollectionQC != nil {
		collectionQCProvider, err = p.resolveProvider(config.CollectionQC.ModelProvider)
		if err != nil {
			return ResearchPlan{}, fmt.Errorf("collection qc model_provider: %w", err)
		}
	}
	if err := validateMCPToolFilters(config.Policy.AllowedTools, p.MCPTools, "research allowed_tools"); err != nil {
		return ResearchPlan{}, err
	}
	if err := validateMCPToolFilters(config.Policy.DisallowedTools, p.MCPTools, "research disallowed_tools"); err != nil {
		return ResearchPlan{}, err
	}
	if config.QC != nil {
		if err := validateNoMCPToolFilters(config.QC.AllowedTools, "qc allowed_tools"); err != nil {
			return ResearchPlan{}, err
		}
		if err := validateNoMCPToolFilters(config.QC.DisallowedTools, "qc disallowed_tools"); err != nil {
			return ResearchPlan{}, err
		}
	}
	mcpServers, err := resolveMCPServers(config.CollectionMCPToolIDs, config.CollectionMCPResourceIDs, p.MCPTools, p.MCPResources)
	if err != nil {
		return ResearchPlan{}, err
	}
	return ResearchPlan{
		Config: config, Provider: modelProvider, CollectionProvider: collectionProvider,
		QCProvider: qcProvider, CollectionQCProvider: collectionQCProvider,
		MCPServers: mcpServers, MCPTools: p.MCPTools.Clone(),
		MCPResources: p.MCPResources.Clone(),
	}, nil
}

func (p DynamicResearchPlan) resolveProvider(value cty.Value) (*provider.Config, error) {
	address, ok, err := referenceAddress(value)
	if err != nil || !ok {
		return nil, err
	}
	config, exists := p.Providers[address]
	if !exists {
		return nil, fmt.Errorf("provider %q was not planned", address)
	}
	return config, nil
}

func snapshotAllProviders(planning golden.Config) (map[string]*providerSnapshot, bool, error) {
	result := make(map[string]*providerSnapshot)
	sensitive := false
	for _, block := range golden.Blocks[*provider.ModelProviderBlock](planning) {
		snapshot, blockSensitive, err := snapshotProviderConfig(block.ProviderConfig())
		if err != nil {
			return nil, false, err
		}
		result[block.Address()] = snapshot
		sensitive = sensitive || blockSensitive
	}
	return result, sensitive, nil
}

func restoreProviders(snapshots map[string]*providerSnapshot) map[string]*provider.Config {
	result := make(map[string]*provider.Config, len(snapshots))
	for address, snapshot := range snapshots {
		result[address] = restoreProvider(snapshot)
	}
	return result
}
