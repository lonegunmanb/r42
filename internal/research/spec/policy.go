package spec

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

type ToolPolicyRef struct {
	ID         string
	Address    string
	OutputType cty.Type
}

type ResolvedTools struct {
	Terminate        *ToolPolicyRef
	TerminateSDKName string
	QCVerdictSDKName string
}

func (c Config) ValidateResolved(tools ResolvedTools) error {
	if err := c.Validate(); err != nil {
		return err
	}
	hasTerminate := c.TerminateToolID != nil
	if hasTerminate && tools.Terminate == nil {
		return errors.New("terminate tool reference was not resolved")
	}
	if !hasTerminate && tools.Terminate != nil {
		return errors.New("resolved terminate tool was not configured")
	}
	if tools.Terminate != nil {
		if strings.TrimSpace(tools.TerminateSDKName) == "" {
			return errors.New("mandatory tool sdk name is required")
		}
		configuredID := *c.TerminateToolID
		if tools.Terminate.ID != configuredID {
			return fmt.Errorf(
				"resolved terminate tool id %s does not match configured %s",
				tools.Terminate.ID,
				configuredID,
			)
		}
		if tools.TerminateSDKName != configuredID {
			return fmt.Errorf(
				"resolved terminate tool sdk name %s does not match configured %s",
				tools.TerminateSDKName,
				configuredID,
			)
		}
		if err := ValidateTerminateOutputType(tools.Terminate.Address, tools.Terminate.OutputType); err != nil {
			return err
		}
		if err := validateMandatoryTool(c.Policy.AllowedTools, c.Policy.DisallowedTools, tools.TerminateSDKName); err != nil {
			return err
		}
	}
	if c.QC != nil {
		disallowed := c.QC.DisallowedTools
		if disallowed == nil {
			disallowed = DefaultQCDisallowedTools()
		}
		if !slices.ContainsFunc(disallowed, isAskUserFilter) {
			return errors.New("qc must disallow ask_user")
		}
		if err := validateMandatoryTool(c.QC.AllowedTools, disallowed, tools.QCVerdictSDKName); err != nil {
			return err
		}
	}
	return nil
}

// ValidateTerminateOutputType enforces the string result contract shared by
// plan-time tool validation and the apply-time terminal recorder.
func ValidateTerminateOutputType(address string, outputType cty.Type) error {
	if err := corespec.ValidateType(outputType); err != nil {
		return fmt.Errorf("terminate tool %s output type: %w", address, err)
	}
	if !outputType.Equals(cty.String) && convert.GetConversion(outputType, cty.String) == nil {
		return fmt.Errorf("terminate tool %s output type must be string-compatible", address)
	}
	return nil
}

func validateMandatoryTool(allowed, disallowed []string, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("mandatory tool sdk name is required")
	}
	if allowed != nil && !slices.ContainsFunc(allowed, func(filter string) bool {
		return matchesCustomTool(filter, name)
	}) {
		return fmt.Errorf("mandatory tool %s must be included in allowed tools", name)
	}
	if slices.ContainsFunc(disallowed, func(filter string) bool {
		return matchesCustomTool(filter, name)
	}) {
		return fmt.Errorf("mandatory tool %s must not be disallowed", name)
	}
	return nil
}

func isAskUserFilter(name string) bool {
	return name == "ask_user" || name == "builtin:ask_user" || name == "builtin:*"
}

func matchesCustomTool(filter, name string) bool {
	return filter == name || filter == "custom:"+name || filter == "custom:*"
}
