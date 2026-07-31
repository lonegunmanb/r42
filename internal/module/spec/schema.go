package spec

import (
	"fmt"
	"math/big"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/r42/internal/debuglog"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

var (
	_ golden.CustomDecode     = (*ModuleBlock)(nil)
	_ golden.PlanBlock        = (*ModuleBlock)(nil)
	_ golden.ApplyBlock       = (*ModuleBlock)(nil)
	_ golden.SingleValueBlock = (*ModuleBlock)(nil)
	_ golden.PlanBlock        = (*OutputBlock)(nil)
	_ golden.SingleValueBlock = (*OutputBlock)(nil)
)

type ModuleBlock struct {
	*golden.BaseBlock
	Source      string
	Parallelism *int
	Timeout     *string
	Inputs      map[string]cty.Value

	planned ModulePlan
}

type blockApplier interface {
	ApplyBlock(string) error
}

func (*ModuleBlock) Type() string { return "" }

func (*ModuleBlock) BlockType() string { return "module" }

func (*ModuleBlock) AddressLength() int { return 2 }

func (*ModuleBlock) CanExecutePrePlan() bool { return false }

func (b *ModuleBlock) Decode(block *golden.HclBlock, context *hcl.EvalContext) error {
	if err := validateModuleSchema(block); err != nil {
		return err
	}
	source, err := decodeKnownString(block, context, "source", true)
	if err != nil {
		return err
	}
	parallelism, err := decodeOptionalPositiveInt(block, context, "parallelism")
	if err != nil {
		return err
	}
	timeout, err := decodeOptionalString(block, context, "timeout")
	if err != nil {
		return err
	}
	inputs := make(map[string]cty.Value)
	for name, attribute := range block.Attributes() {
		if isModuleFixedAttribute(name) || golden.MetaAttributeNames.Contains(name) {
			continue
		}
		value, diagnostics := attribute.Expr.Value(context)
		if diagnostics.HasErrors() {
			return diagnostics
		}
		if !value.IsWhollyKnown() {
			return fmt.Errorf("module input %q must be wholly known during plan", name)
		}
		inputs[name] = value
	}
	b.Source = source
	b.Parallelism = parallelism
	b.Timeout = timeout
	b.Inputs = inputs
	return nil
}

func (b *ModuleBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		planner, ok := b.Config().(ModulePlanner)
		if !ok {
			return fmt.Errorf("module %q requires an r42 module planning config", b.Name())
		}
		planned, err := planner.PlanChildModule(b.Source, b.Inputs, b.Parallelism, b.Timeout)
		if err != nil {
			return fmt.Errorf("planning module %q: %w", b.Name(), err)
		}
		b.planned = planned
		return nil
	})
}

func (b *ModuleBlock) Apply() error {
	applier, ok := b.Config().(blockApplier)
	if !ok {
		return fmt.Errorf("module %q requires an r42 apply config", b.Name())
	}
	return applier.ApplyBlock(b.Address())
}

type ModulePlanner interface {
	PlanChildModule(string, map[string]cty.Value, *int, *string) (ModulePlan, error)
}

func (b *ModuleBlock) Value() cty.Value {
	if len(b.planned.Outputs) == 0 {
		return cty.EmptyObjectVal
	}
	values := make(map[string]cty.Value, len(b.planned.Outputs))
	for name, output := range b.planned.Outputs {
		values[name] = output.Value
	}
	return cty.ObjectVal(values)
}

func (b *ModuleBlock) PlannedModule() ModulePlan {
	return cloneModulePlan(b.planned)
}

type OutputBlock struct {
	*golden.BaseBlock
	Expression  cty.Value `hcl:"value"`
	Description *string   `hcl:"description,optional"`
	Sensitive   bool      `hcl:"sensitive,optional"`

	planned cty.Value
}

func (*OutputBlock) Type() string { return "" }

func (*OutputBlock) BlockType() string { return "output" }

func (*OutputBlock) AddressLength() int { return 2 }

func (*OutputBlock) CanExecutePrePlan() bool { return false }

func (b *OutputBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		if err := b.validatePrimitiveFields(); err != nil {
			return err
		}
		unmarked, _ := b.Expression.UnmarkDeep()
		if err := corespec.ValidateType(unmarked.Type()); err != nil {
			return fmt.Errorf("output %q type: %w", b.Name(), err)
		}
		b.planned = b.Expression
		if b.Sensitive {
			b.planned = corespec.MarkSensitive(b.planned)
		}
		return nil
	})
}

func (b *OutputBlock) Value() cty.Value {
	if b.planned == cty.NilVal {
		return cty.DynamicVal
	}
	return b.planned
}

func (b *OutputBlock) Snapshot() Output {
	description := ""
	if b.Description != nil {
		description = *b.Description
	}
	expression := ""
	if attribute, ok := b.HclBlock().Attributes()["value"]; ok {
		expression = attribute.ExprString()
	}
	return Output{
		Value:       b.planned,
		Type:        b.planned.Type(),
		Description: description,
		Sensitive:   corespec.IsSensitive(b.planned),
		Expression:  expression,
	}
}

func (b *OutputBlock) validatePrimitiveFields() error {
	for name, expected := range map[string]cty.Type{
		"description": cty.String,
		"sensitive":   cty.Bool,
	} {
		attribute, ok := b.HclBlock().Attributes()[name]
		if !ok {
			continue
		}
		value, diagnostics := attribute.Expr.Value(b.EvalContext())
		if diagnostics.HasErrors() {
			return diagnostics
		}
		unmarked, _ := value.UnmarkDeep()
		if unmarked.IsNull() || !unmarked.IsWhollyKnown() || !unmarked.Type().Equals(expected) {
			return fmt.Errorf("output %s must be a known %s", name, expected.FriendlyName())
		}
	}
	return nil
}

func validateModuleSchema(block *golden.HclBlock) error {
	for _, nested := range block.NestedBlocks() {
		if golden.MetaNestedBlockNames.Contains(nested.Type) {
			continue
		}
		return fmt.Errorf("unsupported block type; Blocks of type %q are not expected here", nested.Type)
	}
	return nil
}

func decodeKnownString(block *golden.HclBlock, context *hcl.EvalContext, name string, required bool) (string, error) {
	attribute, ok := block.Attributes()[name]
	if !ok {
		if required {
			return "", fmt.Errorf("module %s is required", name)
		}
		return "", nil
	}
	value, diagnostics := attribute.Expr.Value(context)
	if diagnostics.HasErrors() {
		return "", diagnostics
	}
	unmarked, _ := value.UnmarkDeep()
	if unmarked.IsNull() || !unmarked.IsWhollyKnown() || !unmarked.Type().Equals(cty.String) {
		return "", fmt.Errorf("module %s must be a known string", name)
	}
	return unmarked.AsString(), nil
}

func decodeOptionalString(block *golden.HclBlock, context *hcl.EvalContext, name string) (*string, error) {
	if _, ok := block.Attributes()[name]; !ok {
		return nil, nil
	}
	value, err := decodeKnownString(block, context, name, false)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func decodeOptionalPositiveInt(block *golden.HclBlock, context *hcl.EvalContext, name string) (*int, error) {
	attribute, ok := block.Attributes()[name]
	if !ok {
		return nil, nil
	}
	value, diagnostics := attribute.Expr.Value(context)
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	unmarked, _ := value.UnmarkDeep()
	if unmarked.IsNull() || !unmarked.IsWhollyKnown() || !unmarked.Type().Equals(cty.Number) {
		return nil, fmt.Errorf("module %s must be a positive integer", name)
	}
	integer, accuracy := unmarked.AsBigFloat().Int(nil)
	if accuracy != big.Exact || !integer.IsInt64() || integer.Sign() <= 0 {
		return nil, fmt.Errorf("module %s must be a positive integer", name)
	}
	valueInt := int(integer.Int64())
	if int64(valueInt) != integer.Int64() {
		return nil, fmt.Errorf("module %s must be a positive integer", name)
	}
	return &valueInt, nil
}

func isModuleFixedAttribute(name string) bool {
	return name == "source" || name == "parallelism" || name == "timeout"
}
