package spec_test

import (
	"testing"

	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
)

func TestConfigPhaseModeValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*researchspec.Config)
		expectedMode  researchspec.PhaseMode
		expectedError string
	}{
		{
			name:         "default is full",
			expectedMode: researchspec.PhaseModeFull,
		},
		{
			name: "unknown mode",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = "later"
			},
			expectedMode:  researchspec.PhaseMode("later"),
			expectedError: "research phase_mode must be full, collection_only, or research_only",
		},
		{
			name: "collection only accepts one terminating tool use",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeCollectionOnly
				config.CollectionToolIDs = []string{"collect"}
				config.ToolUses = []researchspec.ToolUse{{Name: "submit", ToolID: "submit", Terminate: true}}
				config.Policy.ToolIDs = []string{"submit"}
				config.TerminateToolID = stringPointer("submit")
			},
			expectedMode: researchspec.PhaseModeCollectionOnly,
		},
		{
			name: "collection only requires terminating tool use",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeCollectionOnly
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only requires exactly one terminating tool_use",
		},
		{
			name: "collection only rejects research skills",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeCollectionOnly
				config.Policy.SkillDirectories = []string{"skills/research"}
				config.ToolUses = []researchspec.ToolUse{{Name: "submit", ToolID: "submit", Terminate: true}}
				config.Policy.ToolIDs = []string{"submit"}
				config.TerminateToolID = stringPointer("submit")
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids research skills",
		},
		{
			name: "collection only rejects research tool ids",
			mutate: func(config *researchspec.Config) {
				configureCollectionOnly(config)
				config.Policy.ToolIDs = append(config.Policy.ToolIDs, "research")
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids tool_ids",
		},
		{
			name: "collection only rejects explicit terminal tool id",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeCollectionOnly
				config.ToolUses = []researchspec.ToolUse{{Name: "submit", ToolID: "submit", Terminate: true}}
				config.Policy.ToolIDs = []string{"submit"}
				config.TerminateToolID = stringPointer("submit")
				config.TerminateToolIDSet = true
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids terminate_tool_id",
		},
		{
			name: "collection only rejects batch size",
			mutate: func(config *researchspec.Config) {
				configureCollectionOnly(config)
				config.CollectionBatchSizeSet = true
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids collection_batch_size",
		},
		{
			name: "collection only rejects collection rounds",
			mutate: func(config *researchspec.Config) {
				configureCollectionOnly(config)
				config.MaxCollectionRoundsSet = true
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids max_collection_rounds",
		},
		{
			name: "collection only rejects collection qc",
			mutate: func(config *researchspec.Config) {
				configureCollectionOnly(config)
				config.CollectionQC = &researchspec.CollectionQCConfig{}
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids collection_qc",
		},
		{
			name: "collection only rejects final qc",
			mutate: func(config *researchspec.Config) {
				configureCollectionOnly(config)
				config.QC = &researchspec.QCConfig{Criteria: validCriteria()}
			},
			expectedMode:  researchspec.PhaseModeCollectionOnly,
			expectedError: "collection_only forbids qc",
		},
		{
			name: "research only rejects collection tools",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeResearchOnly
				config.CollectionToolIDs = []string{"collect"}
			},
			expectedMode:  researchspec.PhaseModeResearchOnly,
			expectedError: "research_only forbids collection_tool_ids",
		},
		{
			name: "research only rejects collection provider",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeResearchOnly
				config.CollectionModelProvider = referenceValue("model_provider.collection", "provider")
			},
			expectedMode:  researchspec.PhaseModeResearchOnly,
			expectedError: "research_only forbids collection_model_provider",
		},
		{
			name: "research only rejects collection skills",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeResearchOnly
				config.CollectionSkills = []string{"source-evaluation"}
			},
			expectedMode:  researchspec.PhaseModeResearchOnly,
			expectedError: "research_only forbids collection skills",
		},
		{
			name: "research only rejects collection batch size",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeResearchOnly
				config.CollectionBatchSizeSet = true
			},
			expectedMode:  researchspec.PhaseModeResearchOnly,
			expectedError: "research_only forbids collection_batch_size",
		},
		{
			name: "research only rejects collection rounds",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeResearchOnly
				config.MaxCollectionRoundsSet = true
			},
			expectedMode:  researchspec.PhaseModeResearchOnly,
			expectedError: "research_only forbids max_collection_rounds",
		},
		{
			name: "research only rejects collection qc",
			mutate: func(config *researchspec.Config) {
				config.PhaseMode = researchspec.PhaseModeResearchOnly
				config.CollectionQC = &researchspec.CollectionQCConfig{}
			},
			expectedMode:  researchspec.PhaseModeResearchOnly,
			expectedError: "research_only forbids collection_qc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := phaseModeFixture()
			if tt.mutate != nil {
				tt.mutate(&config)
			}

			assert.Equal(t, tt.expectedMode, config.EffectivePhaseMode())
			if tt.expectedError == "" {
				assert.NoError(t, config.Validate())
				return
			}
			assert.EqualError(t, config.Validate(), tt.expectedError)
		})
	}
}

func phaseModeFixture() researchspec.Config {
	return researchspec.Config{
		Model:               "model",
		SystemPrompt:        "prompt",
		CollectionBatchSize: researchspec.DefaultCollectionBatchSize,
		Policy: researchspec.SessionPolicy{
			Permission: researchspec.PermissionApproveAll,
		},
	}
}

func configureCollectionOnly(config *researchspec.Config) {
	config.PhaseMode = researchspec.PhaseModeCollectionOnly
	config.ToolUses = []researchspec.ToolUse{{Name: "submit", ToolID: "submit", Terminate: true}}
	config.Policy.ToolIDs = []string{"submit"}
	config.TerminateToolID = stringPointer("submit")
}
