package spec_test

import (
	"reflect"
	"testing"

	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

func TestConfigValidateResolvedTools(t *testing.T) {
	t.Parallel()

	capsule := cty.Capsule("opaque", reflect.TypeFor[struct{}]())
	tests := []struct {
		name          string
		config        researchspec.Config
		resolved      researchspec.ResolvedTools
		expectedError string
	}{
		{
			name: "no terminal tool has no result",
			config: researchspec.Config{
				Model:               "model",
				SystemPrompt:        "prompt",
				Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
				MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
			},
		},
		{
			name:          "invalid research is rejected first",
			config:        researchspec.Config{},
			expectedError: "research model is required",
		},
		{
			name:   "terminal output converts to string",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
		},
		{
			name:   "terminal output is not string compatible",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.List(cty.String)},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "terminate tool go_tool.finish output type must be string-compatible",
		},
		{
			name:   "resolved terminal address must match configuration",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.other", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "resolved terminate tool address go_tool.other does not match configured go_tool.finish",
		},
		{
			name:   "resolved terminal sdk name must match address",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_other",
			},
			expectedError: "resolved terminate tool sdk name go_tool_other does not match configured go_tool_finish",
		},
		{
			name:   "terminal output type is invalid",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: capsule},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "terminate tool go_tool.finish output type: capsule type is not supported at value",
		},
		{
			name: "terminal tool cannot be denied",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), func(policy *researchspec.SessionPolicy) {
				policy.DisallowedTools = []string{"go_tool_finish"}
			}),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "mandatory tool go_tool_finish must not be disallowed",
		},
		{
			name: "terminal tool cannot be denied by custom wildcard",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), func(policy *researchspec.SessionPolicy) {
				policy.DisallowedTools = []string{"custom:*"}
			}),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "mandatory tool go_tool_finish must not be disallowed",
		},
		{
			name: "terminal tool must be in explicit allowlist",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), func(policy *researchspec.SessionPolicy) {
				policy.AllowedTools = []string{"web_search"}
			}),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "mandatory tool go_tool_finish must be included in allowed tools",
		},
		{
			name: "terminal tool may be in explicit allowlist",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), func(policy *researchspec.SessionPolicy) {
				policy.AllowedTools = []string{"go_tool_finish"}
			}),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
		},
		{
			name: "terminal tool may be in custom wildcard allowlist",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), func(policy *researchspec.SessionPolicy) {
				policy.AllowedTools = []string{"custom:*"}
			}),
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
		},
		{
			name:          "configured terminal tool must resolve",
			config:        validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			expectedError: "terminate tool reference was not resolved",
		},
		{
			name: "unconfigured terminal tool must not resolve",
			config: researchspec.Config{
				Model:        "model",
				SystemPrompt: "prompt",
				Policy:       researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
			},
			resolved: researchspec.ResolvedTools{
				Terminate:        &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
				TerminateSDKName: "go_tool_finish",
			},
			expectedError: "resolved terminate tool was not configured",
		},
		{
			name:   "mandatory terminal sdk name is required",
			config: validResolvedConfig(referenceValue("go_tool.finish", "go"), nil),
			resolved: researchspec.ResolvedTools{
				Terminate: &researchspec.ToolPolicyRef{Address: "go_tool.finish", OutputType: cty.String},
			},
			expectedError: "mandatory tool sdk name is required",
		},
		{
			name: "qc must keep ask user denied",
			config: researchspec.Config{
				Model:        "model",
				SystemPrompt: "prompt",
				Policy:       researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
				QC: &researchspec.QCConfig{
					Criteria:        cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
					DisallowedTools: []string{"bash"},
				},
			},
			resolved:      researchspec.ResolvedTools{QCVerdictSDKName: "r42_qc_verdict"},
			expectedError: "qc must disallow ask_user",
		},
		{
			name: "qc verdict is mandatory under defaults",
			config: researchspec.Config{
				Model:        "model",
				SystemPrompt: "prompt",
				Policy:       researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
				QC: &researchspec.QCConfig{
					Criteria: cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
				},
			},
			resolved: researchspec.ResolvedTools{QCVerdictSDKName: "r42_qc_verdict"},
		},
		{
			name: "builtin wildcard keeps ask user denied",
			config: researchspec.Config{
				Model:        "model",
				SystemPrompt: "prompt",
				Policy:       researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
				QC: &researchspec.QCConfig{
					Criteria:        cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
					AllowedTools:    []string{"custom:r42_qc_verdict"},
					DisallowedTools: []string{"builtin:*"},
				},
			},
			resolved: researchspec.ResolvedTools{QCVerdictSDKName: "r42_qc_verdict"},
		},
		{
			name: "qc verdict cannot be denied",
			config: validResolvedConfig(cty.NilVal, func(policy *researchspec.SessionPolicy) {
				_ = policy
			}),
			resolved:      researchspec.ResolvedTools{QCVerdictSDKName: "r42_qc_verdict"},
			expectedError: "mandatory tool r42_qc_verdict must not be disallowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.ValidateResolved(tt.resolved)
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestDefaultQCDisallowedToolsBlocksPlatformShells(t *testing.T) {
	t.Parallel()

	disallowed := researchspec.DefaultQCDisallowedTools()
	assert.Contains(t, disallowed, "bash")
	assert.Contains(t, disallowed, "powershell")
}

func TestQCConfigValidateCriteria(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		criteria      cty.Value
		expectedError string
	}{
		{name: "non-empty map", criteria: cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("cite sources")})},
		{name: "nil", criteria: cty.NilVal, expectedError: "qc criteria must be a non-empty map of string"},
		{name: "null", criteria: cty.NullVal(cty.Map(cty.String)), expectedError: "qc criteria must be a non-empty map of string"},
		{name: "empty", criteria: cty.MapValEmpty(cty.String), expectedError: "qc criteria must be a non-empty map of string"},
		{name: "object", criteria: cty.ObjectVal(map[string]cty.Value{"accuracy": cty.StringVal("cite sources")}), expectedError: "qc criteria must be map of string"},
		{name: "wrong element", criteria: cty.MapVal(map[string]cty.Value{"accuracy": cty.NumberIntVal(1)}), expectedError: "qc criteria must be map of string"},
		{name: "unknown", criteria: cty.UnknownVal(cty.Map(cty.String)), expectedError: "qc criteria must be wholly known during plan"},
		{name: "null value", criteria: cty.MapVal(map[string]cty.Value{"accuracy": cty.NullVal(cty.String)}), expectedError: "qc criteria values must not be null"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := researchspec.QCConfig{Criteria: tt.criteria, MaxRounds: researchspec.DefaultMaxQCRounds}
			err := config.Validate()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func validResolvedConfig(terminate cty.Value, mutate func(*researchspec.SessionPolicy)) researchspec.Config {
	policy := researchspec.SessionPolicy{
		DisallowedTools: []string{"ask_user"},
		Permission:      researchspec.PermissionApproveAll,
	}
	if mutate != nil {
		mutate(&policy)
	}
	config := researchspec.Config{
		Model:               "model",
		SystemPrompt:        "prompt",
		TerminateTool:       terminate,
		MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
		Policy:              policy,
	}
	if terminate.Type().Equals(cty.NilType) {
		config.QC = &researchspec.QCConfig{
			Criteria:        cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
			MaxRounds:       researchspec.DefaultMaxQCRounds,
			DisallowedTools: []string{"bash", "edit", "task", "ask_user", "r42_qc_verdict"},
		}
	}
	return config
}
