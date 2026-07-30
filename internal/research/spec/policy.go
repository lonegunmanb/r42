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
	hasTerminate := hasValue(c.TerminateTool)
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
		configuredAddress, _, ok := referenceIdentity(c.TerminateTool)
		if !ok {
			return errors.New("configured terminate tool identity is invalid")
		}
		if tools.Terminate.Address != configuredAddress {
			return fmt.Errorf(
				"resolved terminate tool address %s does not match configured %s",
				tools.Terminate.Address,
				configuredAddress,
			)
		}
		expectedSDKName := strings.ReplaceAll(configuredAddress, ".", "_")
		if tools.TerminateSDKName != expectedSDKName {
			return fmt.Errorf(
				"resolved terminate tool sdk name %s does not match configured %s",
				tools.TerminateSDKName,
				expectedSDKName,
			)
		}
		if err := corespec.ValidateType(tools.Terminate.OutputType); err != nil {
			return fmt.Errorf("terminate tool %s output type: %w", tools.Terminate.Address, err)
		}
		if !tools.Terminate.OutputType.Equals(cty.String) && convert.GetConversion(tools.Terminate.OutputType, cty.String) == nil {
			return fmt.Errorf("terminate tool %s output type must be string-compatible", tools.Terminate.Address)
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
