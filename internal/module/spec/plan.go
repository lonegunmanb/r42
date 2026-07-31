package spec

import (
	"maps"
	"time"

	internalplan "github.com/lonegunmanb/r42/internal/plan"
	"github.com/zclconf/go-cty/cty"
)

type Output struct {
	Value       cty.Value
	Type        cty.Type
	Description string
	Sensitive   bool
	Expression  string
}

type Plan struct {
	Directory string
	Outputs   map[string]Output
	Modules   map[string]ModulePlan
	Saved     *internalplan.Plan
}

type ModulePlan struct {
	Plan
	Parallelism int
	Timeout     time.Duration
}

func cloneModulePlan(source ModulePlan) ModulePlan {
	return ModulePlan{
		Plan:        clonePlan(source.Plan),
		Parallelism: source.Parallelism,
		Timeout:     source.Timeout,
	}
}

func clonePlan(source Plan) Plan {
	result := Plan{
		Directory: source.Directory,
		Outputs:   make(map[string]Output, len(source.Outputs)),
		Modules:   make(map[string]ModulePlan, len(source.Modules)),
		Saved:     source.Saved,
	}
	maps.Copy(result.Outputs, source.Outputs)
	for name, module := range source.Modules {
		result.Modules[name] = cloneModulePlan(module)
	}
	return result
}
