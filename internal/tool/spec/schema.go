package spec

import (
	"fmt"
	"strings"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

var (
	_ golden.PlanBlock        = (*GoToolBlock)(nil)
	_ golden.PlanBlock        = (*ExternalToolBlock)(nil)
	_ golden.CustomDecode     = (*ExternalToolBlock)(nil)
	_ golden.SingleValueBlock = (*GoToolBlock)(nil)
	_ golden.SingleValueBlock = (*ExternalToolBlock)(nil)
)

type GoToolBlock struct {
	*golden.BaseBlock
	Description string `hcl:"description"`
	Source      string `hcl:"source"`
}

func (*GoToolBlock) Type() string { return "" }

func (*GoToolBlock) BlockType() string { return "go_tool" }

func (*GoToolBlock) AddressLength() int { return 2 }

func (*GoToolBlock) CanExecutePrePlan() bool { return false }

func (b *GoToolBlock) CanonicalAddress() string { return canonicalAddress(b.BaseBlock) }

func (b *GoToolBlock) Id() string { return typedToolID(b.BaseBlock, b.BlockType()) }

func (b *GoToolBlock) BaseValues() map[string]cty.Value {
	return typedToolBaseValues(b.BaseBlock, b.Id())
}

func (b *GoToolBlock) Value() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"id":          cty.StringVal(b.Id()),
		"address":     cty.StringVal(b.Address()),
		"kind":        cty.StringVal(string(config.AddressKindGo)),
		"description": cty.StringVal(b.Description),
		"source":      cty.StringVal(b.Source),
	})
}

func (b *GoToolBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		if strings.TrimSpace(b.Description) == "" {
			return fmt.Errorf("go tool description is required")
		}
		if strings.TrimSpace(b.Source) == "" {
			return fmt.Errorf("go tool source is required")
		}
		return nil
	})
}

type ExternalToolBlock struct {
	*golden.BaseBlock
	Description string
	Program     []string
	WorkingDir  string

	inputConstraint  Constraint
	outputConstraint Constraint
}

func (*ExternalToolBlock) Type() string { return "" }

func (*ExternalToolBlock) BlockType() string { return "external_tool" }

func (*ExternalToolBlock) AddressLength() int { return 2 }

func (*ExternalToolBlock) CanExecutePrePlan() bool { return false }

func (b *ExternalToolBlock) CanonicalAddress() string { return canonicalAddress(b.BaseBlock) }

func (b *ExternalToolBlock) Id() string { return typedToolID(b.BaseBlock, b.BlockType()) }

func (b *ExternalToolBlock) BaseValues() map[string]cty.Value {
	return typedToolBaseValues(b.BaseBlock, b.Id())
}

func (b *ExternalToolBlock) Value() cty.Value {
	program := cty.ListValEmpty(cty.String)
	if len(b.Program) > 0 {
		values := make([]cty.Value, len(b.Program))
		for index, argument := range b.Program {
			values[index] = cty.StringVal(argument)
		}
		program = cty.ListVal(values)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"id":          cty.StringVal(b.Id()),
		"address":     cty.StringVal(b.Address()),
		"kind":        cty.StringVal(string(config.AddressKindExternal)),
		"description": cty.StringVal(b.Description),
		"program":     program,
		"working_dir": cty.StringVal(b.WorkingDir),
	})
}

func (b *ExternalToolBlock) Decode(block *golden.HclBlock, context *hcl.EvalContext) error {
	if err := validateExternalToolSchema(block); err != nil {
		return err
	}
	description, err := decodeRequiredString(block, context, "description")
	if err != nil {
		return err
	}
	program, err := decodeProgram(block, context)
	if err != nil {
		return err
	}
	workingDir, err := decodeOptionalString(block, context, "working_dir")
	if err != nil {
		return err
	}
	inputConstraint, err := decodeConstraint(block, "input_type")
	if err != nil {
		return fmt.Errorf("external tool %w", err)
	}
	outputConstraint, err := decodeConstraint(block, "output_type")
	if err != nil {
		return fmt.Errorf("external tool %w", err)
	}

	b.Description = description
	b.Program = program
	b.WorkingDir = workingDir
	b.inputConstraint = inputConstraint
	b.outputConstraint = outputConstraint

	return nil
}

func validateExternalToolSchema(block *golden.HclBlock) error {
	allowedAttributes := map[string]struct{}{
		"description": {},
		"program":     {},
		"working_dir": {},
		"input_type":  {},
		"output_type": {},
	}
	for name := range block.Attributes() {
		if _, ok := allowedAttributes[name]; ok || golden.MetaAttributeNames.Contains(name) {
			continue
		}
		return fmt.Errorf("unsupported argument; An argument named %q is not expected here", name)
	}
	for _, nested := range block.NestedBlocks() {
		if golden.MetaNestedBlockNames.Contains(nested.Type) {
			continue
		}
		return fmt.Errorf("unsupported block type; Blocks of type %q are not expected here", nested.Type)
	}

	return nil
}

func (b *ExternalToolBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		if strings.TrimSpace(b.Description) == "" {
			return fmt.Errorf("external tool description is required")
		}
		if len(b.Program) == 0 || strings.TrimSpace(b.Program[0]) == "" {
			return fmt.Errorf("external tool program must contain an executable")
		}
		return nil
	})
}

func (b *ExternalToolBlock) InputConstraint() Constraint {
	return b.inputConstraint
}

func (b *ExternalToolBlock) OutputConstraint() Constraint {
	return b.outputConstraint
}

func decodeRequiredString(block *golden.HclBlock, context *hcl.EvalContext, name string) (string, error) {
	attribute, ok := block.Attributes()[name]
	if !ok {
		return "", fmt.Errorf("external tool %s is required", name)
	}
	value, diagnostics := attribute.Expr.Value(context)
	if diagnostics.HasErrors() {
		return "", diagnostics
	}
	if value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.String) {
		return "", fmt.Errorf("external tool %s must be a known string", name)
	}

	return value.AsString(), nil
}

func decodeOptionalString(block *golden.HclBlock, context *hcl.EvalContext, name string) (string, error) {
	attribute, ok := block.Attributes()[name]
	if !ok {
		return "", nil
	}
	value, diagnostics := attribute.Expr.Value(context)
	if diagnostics.HasErrors() {
		return "", diagnostics
	}
	if value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.String) {
		return "", fmt.Errorf("external tool %s must be a known string", name)
	}

	return value.AsString(), nil
}

func decodeProgram(block *golden.HclBlock, context *hcl.EvalContext) ([]string, error) {
	attribute, ok := block.Attributes()["program"]
	if !ok {
		return nil, fmt.Errorf("external tool program is required")
	}
	value, diagnostics := attribute.Expr.Value(context)
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	if !value.IsWhollyKnown() {
		return nil, fmt.Errorf("external tool program must be wholly known")
	}
	if value.IsNull() {
		return nil, fmt.Errorf("external tool program must be a list of strings")
	}
	value, err := convert.Convert(value, cty.List(cty.String))
	if err != nil {
		return nil, fmt.Errorf("external tool program must be a list of strings: %w", err)
	}

	result := make([]string, 0, value.LengthInt())
	for iterator := value.ElementIterator(); iterator.Next(); {
		_, element := iterator.Element()
		if element.IsNull() {
			return nil, fmt.Errorf("external tool program must not contain null elements")
		}
		result = append(result, element.AsString())
	}

	return result, nil
}

func decodeConstraint(block *golden.HclBlock, name string) (Constraint, error) {
	attribute, ok := block.Attributes()[name]
	if !ok {
		return Constraint{}, fmt.Errorf("%s is required", name)
	}
	typeValue, defaults, diagnostics := typeexpr.TypeConstraintWithDefaults(attribute.Expr)
	if diagnostics.HasErrors() {
		return Constraint{}, fmt.Errorf("%s: %w", name, diagnostics)
	}
	if err := corespec.ValidateType(typeValue); err != nil {
		return Constraint{}, fmt.Errorf("%s: %w", name, err)
	}

	return NewConstraintWithDefaults(typeValue, defaults), nil
}
