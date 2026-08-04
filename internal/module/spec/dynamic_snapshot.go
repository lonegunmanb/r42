package spec

import (
	"encoding/json"
	"fmt"

	"github.com/lonegunmanb/golden"
	internalplan "github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

type DynamicResearchPlan struct {
	Expression string
	Providers  map[string]*provider.Config
}

type dynamicResearchSnapshot struct {
	Expression string                       `json:"expression"`
	Providers  map[string]*providerSnapshot `json:"providers,omitempty"`
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
	if block.Tasks.IsWhollyKnown() {
		configs, _, decodeErr := researchspec.DecodeDynamicTasks(block.Tasks)
		if decodeErr != nil {
			return cty.NilVal, decodeErr
		}
		plan := DynamicResearchPlan{Providers: restoreProviders(providers)}
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
	encoded, err := json.Marshal(dynamicResearchSnapshot{Expression: expression, Providers: providers})
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
	return DynamicResearchPlan{
		Expression: snapshot.Expression,
		Providers:  restoreProviders(snapshot.Providers),
	}, nil
}

func (p DynamicResearchPlan) Resolve(config researchspec.Config) (ResearchPlan, error) {
	modelProvider, err := p.resolveProvider(config.ModelProvider)
	if err != nil {
		return ResearchPlan{}, fmt.Errorf("research model_provider: %w", err)
	}
	var qcProvider *provider.Config
	if config.QC != nil {
		qcProvider, err = p.resolveProvider(config.QC.ModelProvider)
		if err != nil {
			return ResearchPlan{}, fmt.Errorf("qc model_provider: %w", err)
		}
	}
	return ResearchPlan{Config: config, Provider: modelProvider, QCProvider: qcProvider}, nil
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
