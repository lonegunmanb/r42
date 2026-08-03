package spec_test

import (
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/provider"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestConfigValidateRequiredFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        researchspec.Config
		expectedError string
	}{
		{
			name:          "model is required",
			config:        researchspec.Config{SystemPrompt: "research carefully"},
			expectedError: "research model is required",
		},
		{
			name:          "system prompt is required",
			config:        researchspec.Config{Model: "gpt-5.6-sol"},
			expectedError: "research system prompt is required",
		},
		{
			name: "profile must not be blank",
			config: researchspec.Config{
				Model:        "gpt-5.6-sol",
				Profile:      "  ",
				SystemPrompt: "research carefully",
			},
			expectedError: "research profile must not be empty",
		},
		{
			name: "reasoning effort must not be blank",
			config: researchspec.Config{
				Model:           "gpt-5.6-sol",
				SystemPrompt:    "research carefully",
				ReasoningEffort: stringPointer("  "),
			},
			expectedError: "research reasoning effort must not be empty",
		},
		{
			name: "tool id must not be empty",
			config: researchspec.Config{
				Model:        "gpt-5.6-sol",
				SystemPrompt: "research carefully",
				Policy:       researchspec.SessionPolicy{ToolIDs: []string{""}},
			},
			expectedError: "research tool_ids must not contain empty values",
		},
		{
			name: "typed tool quota must not be negative",
			config: researchspec.Config{
				Model:        "gpt-5.6-sol",
				SystemPrompt: "research carefully",
				Policy: researchspec.SessionPolicy{
					TypedToolCallQuota: map[string]int{"tool_example": -1},
				},
			},
			expectedError: "research typed_tool_call_quota for \"tool_example\" must be non-negative",
		},
		{
			name: "typed tool quota must reference a session tool",
			config: researchspec.Config{
				Model:        "gpt-5.6-sol",
				SystemPrompt: "research carefully",
				Policy: researchspec.SessionPolicy{
					TypedToolCallQuota: map[string]int{"tool_example": 1},
				},
			},
			expectedError: "research typed_tool_call_quota references tool id \"tool_example\" that is not configured for this session",
		},
		{
			name: "reference must contain kind",
			config: researchspec.Config{
				Model:         "gpt-5.6-sol",
				SystemPrompt:  "research carefully",
				ModelProvider: cty.ObjectVal(map[string]cty.Value{"address": cty.StringVal("model_provider.primary")}),
			},
			expectedError: "research model_provider must be a provider reference",
		},
		{
			name: "terminate tool id must not be empty",
			config: researchspec.Config{
				Model:           "gpt-5.6-sol",
				SystemPrompt:    "research carefully",
				TerminateToolID: stringPointer(" "),
			},
			expectedError: "research terminate_tool_id must not be empty",
		},
		{
			name: "provider reference address must be valid",
			config: researchspec.Config{
				Model:        "gpt-5.6-sol",
				SystemPrompt: "research carefully",
				ModelProvider: cty.ObjectVal(map[string]cty.Value{
					"address": cty.StringVal(""),
					"kind":    cty.StringVal("provider"),
				}),
			},
			expectedError: "research model_provider must be a provider reference",
		},
		{
			name: "protocol attempts must be positive",
			config: researchspec.Config{
				Model:               "gpt-5.6-sol",
				SystemPrompt:        "research carefully",
				MaxProtocolAttempts: -1,
			},
			expectedError: "research max protocol attempts must be positive",
		},
		{
			name: "valid minimal config",
			config: researchspec.Config{
				Model:               "gpt-5.6-sol",
				SystemPrompt:        "research carefully",
				Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
				MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
			},
		},
		{
			name: "zero permission receives default",
			config: researchspec.Config{
				Model:        "gpt-5.6-sol",
				SystemPrompt: "research carefully",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestQCConfigRejectsNegativeTypedToolCallQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		quota         map[string]int
		expectedError string
	}{
		{
			name:          "negative",
			quota:         map[string]int{"tool_example": -1},
			expectedError: "qc typed_tool_call_quota for \"tool_example\" must be non-negative",
		},
		{
			name:          "outside session",
			quota:         map[string]int{"tool_example": 1},
			expectedError: "qc typed_tool_call_quota references tool id \"tool_example\" that is not configured for this session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := researchspec.QCConfig{
				Criteria:           cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("accurate")}),
				TypedToolCallQuota: tt.quota,
				MaxRounds:          researchspec.DefaultMaxQCRounds,
			}

			require.EqualError(t, config.Validate(), tt.expectedError)
		})
	}
}

func TestConfigRejectsEmptyToolIDs(t *testing.T) {
	t.Parallel()

	research := researchspec.Config{
		Model:               "model",
		SystemPrompt:        "prompt",
		MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
		Policy: researchspec.SessionPolicy{
			ToolIDs:    []string{""},
			Permission: researchspec.PermissionApproveAll,
		},
	}
	require.EqualError(t, research.Validate(), "research tool_ids must not contain empty values")

	qc := researchspec.QCConfig{
		Criteria:  cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("accurate")}),
		ToolIDs:   []string{""},
		MaxRounds: researchspec.DefaultMaxQCRounds,
	}
	require.EqualError(t, qc.Validate(), "qc tool_ids must not contain empty values")
}

func TestConfigEffectiveQCInheritsOnlySessionFields(t *testing.T) {
	t.Parallel()

	providerRef := referenceValue("model_provider.primary", "provider")
	researchToolIDs := []string{"tool_go_tool_research_12345678-1234-8234-9234-123456789abc"}
	qcToolIDs := []string{"tool_go_tool_qc_12345678-1234-8234-9234-123456789abc"}
	qcQuota := map[string]int{qcToolIDs[0]: 2}
	researchLifecycleRetries := 4
	qcModelCallRetries := 2
	config := researchspec.Config{
		ModelProvider:   providerRef,
		Model:           "research-model",
		Profile:         "gpt-5.4",
		SystemPrompt:    "research carefully",
		ReasoningEffort: stringPointer("high"),
		Retry: provider.RetryOverride{
			LifecycleRetries:  &researchLifecycleRetries,
			ErrorMessageRegex: []string{"research transient"},
		},
		Policy: researchspec.SessionPolicy{
			ToolIDs:          researchToolIDs,
			AllowedTools:     []string{"research_allowed"},
			DisallowedTools:  []string{"research_denied"},
			SkillDirectories: []string{"research-skills"},
			Skills:           []string{"research-skill"},
			DisabledSkills:   []string{"research-disabled"},
			Permission:       researchspec.PermissionApproveAll,
		},
		QC: &researchspec.QCConfig{
			Criteria:           cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("cite every claim")}),
			ToolIDs:            qcToolIDs,
			TypedToolCallQuota: qcQuota,
			Retry: provider.RetryOverride{
				ModelCallRetries:  &qcModelCallRetries,
				ErrorMessageRegex: []string{"qc transient"},
			},
		},
	}

	effective, err := config.EffectiveQC(provider.DefaultRetryPolicy())
	require.NoError(t, err)
	assert.True(t, providerRef.RawEquals(effective.ModelProvider))
	assert.Equal(t, "research-model", effective.Model)
	assert.Equal(t, "gpt-5.4", effective.Profile)
	require.NotNil(t, effective.ReasoningEffort)
	assert.Equal(t, "high", *effective.ReasoningEffort)
	assert.Equal(t, researchspec.PermissionApproveAll, effective.Permission)
	assert.Equal(t, 4, effective.Retry.LifecycleRetries)
	assert.Equal(t, 2, effective.Retry.ModelCallRetries)
	assert.Equal(t, []string{"research transient", "qc transient"}, effective.Retry.ErrorMessageRegex)
	assert.Equal(t, qcToolIDs, effective.ToolIDs)
	assert.Equal(t, qcQuota, effective.TypedToolCallQuota)
	effective.TypedToolCallQuota[qcToolIDs[0]] = 9
	assert.Equal(t, 2, config.QC.TypedToolCallQuota[qcToolIDs[0]])
	assert.Nil(t, effective.AllowedTools)
	assert.Equal(t, researchspec.DefaultQCDisallowedTools(), effective.DisallowedTools)
	assert.Nil(t, effective.SkillDirectories)
	assert.Nil(t, effective.Skills)
	assert.Nil(t, effective.DisabledSkills)
}

func TestConfigEffectiveQCAppliesPerFieldOverrides(t *testing.T) {
	t.Parallel()

	qcProvider := referenceValue("model_provider.qc", "provider")
	config := researchspec.Config{
		ModelProvider:   referenceValue("model_provider.research", "provider"),
		Model:           "research-model",
		SystemPrompt:    "research carefully",
		ReasoningEffort: stringPointer("medium"),
		Policy: researchspec.SessionPolicy{
			Permission: researchspec.PermissionApproveAll,
		},
		QC: &researchspec.QCConfig{
			Criteria:        cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
			ModelProvider:   qcProvider,
			Model:           stringPointer("qc-model"),
			ReasoningEffort: stringPointer("max"),
			Permission:      permissionPointer(researchspec.PermissionApproveAll),
			MaxRounds:       3,
		},
	}

	effective, err := config.EffectiveQC(provider.DefaultRetryPolicy())
	require.NoError(t, err)
	assert.True(t, qcProvider.RawEquals(effective.ModelProvider))
	assert.Equal(t, "qc-model", effective.Model)
	assert.Equal(t, "qc-model", effective.Profile)
	require.NotNil(t, effective.ReasoningEffort)
	assert.Equal(t, "max", *effective.ReasoningEffort)
	assert.Equal(t, 3, effective.MaxRounds)
}

func TestConfigEffectiveQCRequiresQC(t *testing.T) {
	t.Parallel()

	_, err := (researchspec.Config{}).EffectiveQC(provider.DefaultRetryPolicy())
	assert.EqualError(t, err, "research qc is not configured")
}

func TestConfigEffectiveQCRejectsInvalidResearch(t *testing.T) {
	t.Parallel()

	config := effectiveQCFixture()
	config.Model = ""
	_, err := config.EffectiveQC(provider.DefaultRetryPolicy())
	assert.EqualError(t, err, "research model is required")
}

func TestConfigValidateTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		timeout       *time.Duration
		expectedError string
	}{
		{name: "unset"},
		{name: "positive", timeout: durationPointer(2 * time.Hour)},
		{name: "zero", timeout: durationPointer(0), expectedError: "research timeout must be positive"},
		{name: "negative", timeout: durationPointer(-time.Second), expectedError: "research timeout must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := researchspec.Config{
				Model:               "model",
				SystemPrompt:        "prompt",
				Policy:              researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
				MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
				Timeout:             tt.timeout,
			}
			err := config.Validate()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestConfigValidateNestedPolicy(t *testing.T) {
	t.Parallel()

	negativeRetries := -1
	tests := []struct {
		name          string
		mutate        func(*researchspec.Config)
		expectedError string
	}{
		{
			name: "invalid permission",
			mutate: func(config *researchspec.Config) {
				config.Policy.Permission = "prompt"
			},
			expectedError: "research: permission must be approve_all",
		},
		{
			name: "invalid retry",
			mutate: func(config *researchspec.Config) {
				config.Retry.LifecycleRetries = &negativeRetries
			},
			expectedError: "research retry: lifecycle retries must not be negative",
		},
		{
			name: "invalid artifact",
			mutate: func(config *researchspec.Config) {
				config.Artifacts = []researchspec.Artifact{{Name: "report", Type: "archive", Path: "report.zip"}}
			},
			expectedError: "artifact report type must be file or directory",
		},
		{
			name: "duplicate artifact",
			mutate: func(config *researchspec.Config) {
				config.Artifacts = []researchspec.Artifact{
					{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "a.md"},
					{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "b.md"},
				}
			},
			expectedError: "artifact report is declared more than once",
		},
		{
			name: "invalid qc",
			mutate: func(config *researchspec.Config) {
				config.QC = &researchspec.QCConfig{Criteria: cty.MapValEmpty(cty.String)}
			},
			expectedError: "qc criteria must be a non-empty map of string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := researchspec.Config{
				Model:        "model",
				SystemPrompt: "prompt",
				Policy:       researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
			}
			tt.mutate(&config)
			assert.EqualError(t, config.Validate(), tt.expectedError)
		})
	}
}

func TestQCConfigValidateOverrides(t *testing.T) {
	t.Parallel()

	negativeRetries := -1
	tests := []struct {
		name          string
		mutate        func(*researchspec.QCConfig)
		expectedError string
	}{
		{
			name: "empty model",
			mutate: func(config *researchspec.QCConfig) {
				config.Model = stringPointer(" ")
			},
			expectedError: "qc model must not be empty",
		},
		{
			name: "empty reasoning effort",
			mutate: func(config *researchspec.QCConfig) {
				config.ReasoningEffort = stringPointer(" ")
			},
			expectedError: "qc reasoning effort must not be empty",
		},
		{
			name: "invalid permission",
			mutate: func(config *researchspec.QCConfig) {
				config.Permission = permissionPointer("prompt")
			},
			expectedError: "qc: permission must be approve_all",
		},
		{
			name: "negative max rounds",
			mutate: func(config *researchspec.QCConfig) {
				config.MaxRounds = -1
			},
			expectedError: "qc max rounds must be positive",
		},
		{
			name: "invalid retry",
			mutate: func(config *researchspec.QCConfig) {
				config.Retry.ModelCallRetries = &negativeRetries
			},
			expectedError: "qc retry: model call retries must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := researchspec.QCConfig{
				Criteria:  cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
				MaxRounds: researchspec.DefaultMaxQCRounds,
			}
			tt.mutate(&config)
			assert.EqualError(t, config.Validate(), tt.expectedError)
		})
	}
}

func TestConfigEffectiveQCRetryCompositionErrors(t *testing.T) {
	t.Parallel()

	baseInvalid := provider.DefaultRetryPolicy()
	baseInvalid.MaxInterval = time.Second
	_, err := effectiveQCFixture().EffectiveQC(baseInvalid)
	require.EqualError(t, err, "research retry: maximum retry interval must not be less than retry interval")

	researchInterval := 20 * time.Second
	researchMax := 30 * time.Second
	qcMax := 15 * time.Second
	config := effectiveQCFixture()
	config.Retry.Interval = &researchInterval
	config.Retry.MaxInterval = &researchMax
	config.QC.Retry.MaxInterval = &qcMax
	_, err = config.EffectiveQC(provider.DefaultRetryPolicy())
	require.EqualError(t, err, "qc retry: maximum retry interval must not be less than retry interval")
}

func TestConfigEffectiveQCRetryUsesSelectedProviderPolicy(t *testing.T) {
	t.Parallel()

	researchInterval := 300 * time.Second
	config := effectiveQCFixture()
	config.Retry.Interval = &researchInterval
	providerPolicy := provider.DefaultRetryPolicy()
	providerPolicy.MaxInterval = 600 * time.Second

	require.NoError(t, config.Validate())
	effective, err := config.EffectiveQC(providerPolicy)
	require.NoError(t, err)
	assert.Equal(t, 300*time.Second, effective.Retry.Interval)
	assert.Equal(t, 600*time.Second, effective.Retry.MaxInterval)
}

func TestQCConfigAllowsPartialRetryUntilEffectivePolicyIsKnown(t *testing.T) {
	t.Parallel()

	interval := 300 * time.Second
	config := researchspec.QCConfig{
		Criteria:  cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("accurate")}),
		Retry:     provider.RetryOverride{Interval: &interval},
		MaxRounds: researchspec.DefaultMaxQCRounds,
	}
	assert.NoError(t, config.Validate())
}

func effectiveQCFixture() researchspec.Config {
	return researchspec.Config{
		Model:        "model",
		SystemPrompt: "prompt",
		Policy:       researchspec.SessionPolicy{Permission: researchspec.PermissionApproveAll},
		QC: &researchspec.QCConfig{
			Criteria: cty.MapVal(map[string]cty.Value{"accuracy": cty.StringVal("be accurate")}),
		},
	}
}

func stringPointer(value string) *string { return &value }

func durationPointer(value time.Duration) *time.Duration { return &value }

func permissionPointer(value researchspec.Permission) *researchspec.Permission { return &value }
