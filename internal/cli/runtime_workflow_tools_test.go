package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	"github.com/lonegunmanb/r42/internal/mcp"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestClosedWorldDisallowedToolsIncludesAcquisitionAndArbitraryIO(t *testing.T) {
	t.Parallel()

	tools := closedWorldDisallowedTools(nil, nil)

	for _, name := range []string{
		"web_search", "web_fetch", "bash", "powershell", "read_powershell", "list_powershell",
		"shell", "edit", "create", "glob", "task", "ask_user",
	} {
		assert.Contains(t, tools, name)
	}
	for _, name := range []string{"view", "grep", "head", "tail"} {
		assert.NotContains(t, tools, name)
	}
}

func freezeTestInformationNeeds(t *testing.T, context *collection.Context) {
	t.Helper()
	response := collection.NewInformationNeedsHandler(context).Set(collection.InformationNeedsArgs{InformationNeeds: []collection.InformationNeedInput{{
		Question:       "test fixture need",
		StopConditions: []collection.StopConditionInput{{Condition: "test fixture condition"}},
	}}})
	require.True(t, response.Accepted)
}

func TestClosedWorldAllowedToolsIncludesReadOnlyFileTools(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"view", "grep", "head", "tail", "r42_read_artifact", "submit"}, closedWorldAllowedTools(
		nil,
		[]string{"r42_read_artifact", "submit"},
	))
	assert.Equal(t, []string{"custom_tool", "view", "grep", "head", "tail", "submit"}, closedWorldAllowedTools(
		[]string{"custom_tool", "view"},
		[]string{"submit"},
	))
}

func TestCollectionDisallowedToolsBlocksDelegationAndShellFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured []string
		expected   []string
	}{
		{name: "defaults", expected: []string{"web_search", "web_fetch", "bash", "powershell", "read_powershell", "list_powershell", "shell", "edit", "create", "glob", "task", "ask_user", "curl"}},
		{
			name:       "preserves custom exclusions without duplicating defaults",
			configured: []string{"custom", "curl"},
			expected:   []string{"custom", "curl", "web_search", "web_fetch", "bash", "powershell", "read_powershell", "list_powershell", "shell", "edit", "create", "glob", "task", "ask_user"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := slices.Clone(tt.configured)

			tools := collectionDisallowedTools(input, nil)

			assert.ElementsMatch(t, tt.expected, tools)
			for _, name := range []string{"view", "grep", "head", "tail"} {
				assert.NotContains(t, tools, name)
			}
			assert.Equal(t, tt.configured, input)
		})
	}
}

func TestCollectionDisallowedToolsAllowsExplicitShellTools(t *testing.T) {
	t.Parallel()

	tools := collectionDisallowedTools(nil, []string{"powershell", "shell"})

	assert.NotContains(t, tools, "powershell")
	assert.NotContains(t, tools, "shell")
	for _, name := range []string{
		"web_search", "web_fetch", "bash", "read_powershell", "list_powershell", "edit", "create", "glob", "task", "ask_user", "curl",
	} {
		assert.Contains(t, tools, name)
	}
}

func TestClosedWorldDisallowedToolsAllowsExplicitBuiltIns(t *testing.T) {
	t.Parallel()

	tools := closedWorldDisallowedTools(nil, []string{"web_search", "shell"})

	assert.NotContains(t, tools, "web_search")
	assert.NotContains(t, tools, "shell")
	for _, name := range []string{
		"web_fetch", "bash", "powershell", "read_powershell", "list_powershell",
		"edit", "create", "glob", "task", "ask_user",
	} {
		assert.Contains(t, tools, name)
	}
}

func TestExplicitDisallowedToolsOverrideBuiltinOptIn(t *testing.T) {
	t.Parallel()

	collection := collectionDisallowedTools([]string{"shell"}, []string{"powershell", "shell"})
	assert.NotContains(t, collection, "powershell")
	assert.NotContains(t, collection, "shell")

	closedWorld := closedWorldDisallowedTools([]string{"web_search"}, []string{"web_search", "shell"})
	assert.NotContains(t, closedWorld, "web_search")
	assert.NotContains(t, closedWorld, "shell")
}

func TestAllowedBuiltinToolsAreRemovedFromConfiguredDenials(t *testing.T) {
	t.Parallel()

	for _, build := range []struct {
		name string
		fn   func([]string, []string) []string
	}{
		{name: "collection", fn: collectionDisallowedTools},
		{name: "closed world", fn: closedWorldDisallowedTools},
	} {
		t.Run(build.name, func(t *testing.T) {
			t.Parallel()
			result := build.fn([]string{"bash", "powershell", "shell", "web_search", "custom"}, []string{"bash", "powershell", "shell", "web_search"})
			assert.Contains(t, result, "custom")
			for _, name := range []string{"bash", "powershell", "shell", "web_search"} {
				assert.NotContains(t, result, name)
			}
		})
	}
}

func TestFinalQCDisallowedToolsHonorsBuiltinAllowlist(t *testing.T) {
	t.Parallel()

	effective := researchspec.EffectiveQC{DisallowedTools: []string{"edit"}}
	assert.NotContains(t, finalQCDisallowedTools(effective, false, []string{"edit"}), "edit")
	assert.NotContains(t, finalQCDisallowedTools(effective, true, []string{"edit"}), "edit")
}

func TestCollectionAllowedToolsAddsOnlyMandatoryProtocolTools(t *testing.T) {
	t.Parallel()

	assert.Nil(t, collectionAllowedTools(nil, []string{"r42_collection_checkpoint"}, nil, nil))
	assert.Equal(t,
		[]string{"web_fetch", "r42_collection_checkpoint"},
		collectionAllowedTools(
			[]string{"web_fetch"},
			[]string{"r42_collection_checkpoint"},
			nil,
			nil,
		),
	)
}

func TestCollectionAllowedToolsRequiresExplicitMCPAllowlistEntry(t *testing.T) {
	t.Parallel()

	quoteID := "mcp_tool_market__quote_12345678-1234-8234-9234-123456789abc"
	klineID := "mcp_tool_market__kline_22345678-1234-8234-9234-123456789abc"
	registry := mcp.ToolRegistry{
		quoteID: {ID: quoteID, Name: "quote", Server: mcp.Config{Name: "market"}},
		klineID: {ID: klineID, Name: "kline", Server: mcp.Config{Name: "market"}},
	}
	selected := []string{quoteID, klineID}

	assert.Equal(t,
		[]string{"web_search", "mcp:market-quote", "r42_collection_checkpoint"},
		collectionAllowedTools(
			[]string{"web_search", quoteID},
			[]string{"r42_collection_checkpoint"},
			selected,
			registry,
		),
	)
	assert.NotContains(t,
		collectionAllowedTools([]string{"web_search"}, nil, selected, registry),
		"mcp:market-quote",
	)
	assert.NotContains(t,
		collectionAllowedTools([]string{"mcp:market-kline"}, nil, selected, registry),
		"mcp:market-kline",
	)
	assert.Nil(t, collectionAllowedTools(nil, nil, selected, registry))
	assert.Equal(t,
		[]string{"mcp:market-quote"},
		collectionMCPToolFilters([]string{quoteID}, selected, registry),
	)
}

func TestCollectionBuiltInHooksEnforceCheckpointGate(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	collectionContext := collection.NewContext(workspace, 1, nil)
	require.NoError(t, collectionContext.BeginWorkflow())
	hooks := collectionBuiltInHooks(newToolCallQuota(nil), collectionContext)
	require.NotNil(t, hooks)

	deniedBeforePlan, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "deny", deniedBeforePlan.PermissionDecision)
	assert.Contains(t, deniedBeforePlan.PermissionDecisionReason, "r42_set_information_needs")

	freezeTestInformationNeeds(t, collectionContext)
	allowed, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision)
	_, err = hooks.OnPostToolUse(sdk.PostToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)

	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(path, []byte("evidence"), 0o600))
	registered := collection.NewRegisterHandler(collectionContext).Register(collection.RegisterArgs{Path: path})
	require.True(t, registered.Accepted)

	for _, toolName := range []string{"web_search", "web_fetch"} {
		denied, hookErr := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: toolName}, sdk.HookInvocation{})
		require.NoError(t, hookErr)
		assert.Equal(t, "deny", denied.PermissionDecision)
		assert.Contains(t, denied.PermissionDecisionReason, "checkpoint pending")
	}
	checkpoint := collection.NewCheckpointHandler(collectionContext).Submit(collection.CheckpointArgs{
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	require.True(t, checkpoint.Accepted)
	afterCheckpoint, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "deny", afterCheckpoint.PermissionDecision)
	assert.Contains(t, afterCheckpoint.PermissionDecisionReason, "checkpoint already accepted")

	unrelated, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "some_read_only_tool"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", unrelated.PermissionDecision)
}

func TestCollectionMarkdownWriterHonorsPlanAndCheckpointGate(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Description: "report",
	})
	require.NoError(t, err)
	context := collection.NewContextWithArtifactRegistry(workspace, 10, nil, registry)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, true, registry, []string{record.ID}, nil)
	require.NoError(t, err)
	writeTool := toolByName(t, wrapCollectionMutationTools(tools, context), "r42_write_markdown")
	invocation := sdk.ToolInvocation{Arguments: map[string]any{"artifact_id": record.ID, "content": "# report"}}

	beforePlan, err := writeTool.Handler(invocation)
	require.NoError(t, err)
	assert.Contains(t, beforePlan.TextResultForLLM, `"accepted":false`)
	assert.NoFileExists(t, record.Path)

	freezeTestInformationNeeds(t, context)
	written, err := writeTool.Handler(invocation)
	require.NoError(t, err)
	assert.Contains(t, written.TextResultForLLM, `"accepted":true`)

	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	require.True(t, checkpoint.Accepted)
	afterCheckpoint, err := writeTool.Handler(invocation)
	require.NoError(t, err)
	assert.Contains(t, afterCheckpoint.TextResultForLLM, `"accepted":false`)
}

func TestCollectionCheckpointRejectsInFlightMarkdownWriter(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	freezeTestInformationNeeds(t, context)
	started := make(chan struct{})
	release := make(chan struct{})
	writer := sdk.Tool{Name: "r42_write_markdown", Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
		close(started)
		<-release
		return sdk.ToolResult{ResultType: "success"}, nil
	}}
	wrapped := wrapCollectionMutationTools([]sdk.Tool{writer}, context)[0]
	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Handler(sdk.ToolInvocation{})
		done <- err
	}()
	<-started

	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.False(t, checkpoint.Accepted)
	require.NotEmpty(t, checkpoint.Issues)
	assert.Equal(t, "collection_tools_in_flight", checkpoint.Issues[0].Code)

	close(release)
	require.NoError(t, <-done)
	checkpoint = collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.True(t, checkpoint.Accepted)
}

func TestCollectionCheckpointRejectsInFlightAcquisitionTool(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	freezeTestInformationNeeds(t, context)
	started := make(chan struct{})
	release := make(chan struct{})
	acquisition := sdk.Tool{Name: "fetch", Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
		close(started)
		<-release
		return sdk.ToolResult{ResultType: "success"}, nil
	}}
	wrapped := wrapCollectionAcquisitionTools([]sdk.Tool{acquisition}, context)[0]
	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Handler(sdk.ToolInvocation{})
		done <- err
	}()
	<-started

	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.False(t, checkpoint.Accepted)
	require.NotEmpty(t, checkpoint.Issues)
	assert.Equal(t, "collection_tools_in_flight", checkpoint.Issues[0].Code)

	close(release)
	require.NoError(t, <-done)
	checkpoint = collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.True(t, checkpoint.Accepted)
}

func TestCollectionBuiltInAcquisitionLeaseEndsAfterPostHook(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	freezeTestInformationNeeds(t, context)
	hooks := collectionBuiltInHooks(newToolCallQuota(nil), context)

	allowed, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision)
	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.False(t, checkpoint.Accepted)
	require.NotEmpty(t, checkpoint.Issues)
	assert.Equal(t, "collection_tools_in_flight", checkpoint.Issues[0].Code)

	_, err = hooks.OnPostToolUse(sdk.PostToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	checkpoint = collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.True(t, checkpoint.Accepted)
}

func TestCollectionBuiltInAcquisitionLeaseEndsAfterFailureHook(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	freezeTestInformationNeeds(t, context)
	hooks := collectionBuiltInHooks(newToolCallQuota(map[string]int{"web_fetch": 1}), context)

	allowed, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision)
	_, err = hooks.OnPostToolUseFailure(sdk.PostToolUseFailureHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)

	allowed, err = hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision, "failed calls must release their lease and quota")
	_, err = hooks.OnPostToolUse(sdk.PostToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		EmptyReason: "no evidence artifacts were added",
		NeedDispositions: []collection.NeedDisposition{{
			InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue,
		}},
	})
	assert.True(t, checkpoint.Accepted)
}

func TestCollectionProtocolToolsRegisterRetainedResultAndCheckpoint(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	freezeTestInformationNeeds(t, context)
	recorder := collection.NewCheckpointRecorder()
	acquisition := sdk.Tool{Name: "fetch", Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
		return sdk.ToolResult{TextResultForLLM: `{"title":"source"}`, ResultType: "success"}, nil
	}}
	wrapped := wrapCollectionAcquisitionTools([]sdk.Tool{acquisition}, context)
	_, err := wrapped[0].Handler(sdk.ToolInvocation{ToolCallID: "call-1"})
	require.NoError(t, err)
	protocol := collectionProtocolTools(context, recorder)
	registerTool := toolByName(t, protocol, "r42_register_artifact")
	checkpointTool := toolByName(t, protocol, "r42_collection_checkpoint")
	registerProperties, ok := registerTool.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, registerProperties, "source")
	assert.Nil(t, registerTool.Parameters["required"])
	registered, err := registerTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"source_tool_call_id": "call-1",
		"source":              "retained-result:call-1",
	}})
	require.NoError(t, err)
	assert.Contains(t, registered.TextResultForLLM, `"accepted":true`)
	var registrationResponse struct {
		Output struct {
			Path string `json:"path"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(registered.TextResultForLLM), &registrationResponse))
	content, err := os.ReadFile(registrationResponse.Output.Path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: retained-result:call-1\n\n"+`{"title":"source"}`, string(content))
	checkpoint, err := checkpointTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"need_dispositions": []any{map[string]any{
			"information_need_id": "NEED-001", "search_disposition": "continue",
		}},
	}})
	require.NoError(t, err)
	assert.Contains(t, checkpoint.TextResultForLLM, "artifact-")
}

func TestReadInformationNeedsToolReturnsOnlyActiveStates(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	plan := collection.NewInformationNeedsHandler(context).Set(collection.InformationNeedsArgs{
		InformationNeeds: []collection.InformationNeedInput{
			{
				Question:       "supplier",
				StopConditions: []collection.StopConditionInput{{Condition: "relationship"}},
			},
			{
				Question:       "materiality",
				StopConditions: []collection.StopConditionInput{{Condition: "economic exposure"}},
			},
		},
	})
	require.True(t, plan.Accepted)
	assessment := context.ApplyQCAssessments(
		[]collection.NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue},
			{InformationNeedID: "NEED-002", SearchDisposition: collection.SearchDispositionContinue},
		},
		[]collection.QCAssessment{
			{
				InformationNeedID: "NEED-001",
				Status:            collection.AssessmentSufficient,
				EvidenceProgress:  collection.EvidenceProgressMaterial,
			},
			{
				InformationNeedID:       "NEED-002",
				Status:                  collection.AssessmentNeedsMore,
				UnsatisfiedConditionIDs: []string{"NEED-002-SC-001"},
				EvidenceProgress:        collection.EvidenceProgressMaterial,
			},
		},
		false,
		true,
	)
	require.True(t, assessment.Accepted)

	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	readTool := toolByName(t, protocol, "r42_read_information_needs")
	result, err := readTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{}})

	require.NoError(t, err)
	var response struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			ActiveInformationNeedStates []collection.ActiveInformationNeedState `json:"active_information_need_states"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.True(t, response.Accepted)
	require.Len(t, response.Output.ActiveInformationNeedStates, 1)
	state := response.Output.ActiveInformationNeedStates[0]
	assert.Equal(t, "NEED-002", state.InformationNeed.ID)
	assert.Equal(t, []string{"NEED-002-SC-001"}, state.UnsatisfiedConditionIDs)
	assert.Empty(t, readTool.Parameters["required"])
}

func TestCollectionProtocolToolsSaveArtifactWithRequiredSource(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	require.NoError(t, context.AddArtifactTarget(filepath.Join(workspace, "evidence"), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	assert.Equal(t, []string{"r42_set_information_needs", "r42_read_information_needs", "r42_register_artifact", "r42_collection_checkpoint", "r42_save_artifact"}, toolNames(protocol))
	assert.Contains(t, phaseAllowedTools([]string{"web_fetch"}, toolNames(protocol)), "r42_save_artifact")
	registerTool := toolByName(t, protocol, "r42_register_artifact")
	checkpointTool := toolByName(t, protocol, "r42_collection_checkpoint")
	saveTool := toolByName(t, protocol, "r42_save_artifact")
	assert.ElementsMatch(t, []string{"artifact_path", "content", "source"}, saveTool.Parameters["required"])
	assert.Contains(t, saveTool.Description, "Do not call r42_register_artifact")
	assert.Contains(t, saveTool.Description, "artifact_id")
	assert.NotContains(t, saveTool.Description, "default Collection evidence directory")
	registerProperties, ok := registerTool.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, registerProperties, "description")
	saveProperties, ok := saveTool.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, saveProperties, "description")
	artifactPath, ok := saveProperties["artifact_path"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, artifactPath["description"], "default evidence directory")
	require.NotContains(t, saveTool.Parameters["required"], "description")

	path := filepath.Join(workspace, "evidence", "source.md")
	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "\n# Evidence\n\nCollected material.\n",
		"source":        "local-record:42",
		"description":   "Database record used for the baseline",
	}})

	require.NoError(t, err)
	var response struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			Path       string `json:"path"`
			ArtifactID string `json:"artifact_id"`
			NextAction string `json:"next_action"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.True(t, response.Accepted)
	assert.Equal(t, path, response.Output.Path)
	assert.Regexp(t, `^artifact-[0-9a-f-]{36}$`, response.Output.ArtifactID)
	assert.Empty(t, response.Output.NextAction)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: local-record:42\n\n\n# Evidence\n\nCollected material.\n", string(content))

	checkpoint, err := checkpointTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"need_dispositions": []any{map[string]any{
			"information_need_id": "NEED-001", "search_disposition": "continue",
		}},
	}})
	require.NoError(t, err)
	assert.Contains(t, checkpoint.TextResultForLLM, response.Output.ArtifactID)
	require.Len(t, context.EvidenceArtifactIDs(), 1)
	record, err := context.Artifacts.Record(context.EvidenceArtifactIDs()[0])
	require.NoError(t, err)
	assert.Equal(t, "Database record used for the baseline", record.Description)
}

func TestCollectionProtocolToolsSaveArtifactSignalsCheckpointAtBatchGate(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 1, nil)
	freezeTestInformationNeeds(t, context)
	path := filepath.Join(workspace, "evidence", "source.md")
	require.NoError(t, context.AddArtifactTarget(filepath.Dir(path), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")

	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "# Evidence",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	var response struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			NextAction string `json:"next_action"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.True(t, response.Accepted)
	assert.Equal(t, "r42_collection_checkpoint", response.Output.NextAction)
}

func TestCollectionProtocolToolsRejectsUndeclaredArtifactPath(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")

	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": filepath.Join(workspace, "evidence", "source.md"),
		"content":       "# Evidence",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"code":"artifact_path"`)
}

func TestCollectionProtocolToolsSaveArtifactBelowDeclaredDirectoryTarget(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	directory := filepath.Join(workspace, "collected")
	require.NoError(t, context.AddArtifactTarget(directory, true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")

	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": filepath.Join(directory, "source.md"),
		"content":       "# Evidence",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"accepted":true`)
}

func TestCollectionProtocolToolsSaveArtifactToDeclaredFileTarget(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	path := filepath.Join(workspace, "collected", "primary.md")
	require.NoError(t, context.AddArtifactTarget(path, false))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")

	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "# Evidence\n\nCollected material.",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	var response struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.True(t, response.Accepted)
	assert.Regexp(t, `^artifact-`, response.Output.ArtifactID)
}

func TestEvidenceToolsExposeArtifactPagingAndSearch(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("- Source: local-record:42\n\none\ntarget phrase\nthree\n"), 0o600))
	registration, _, err := registry.RegisterEvidence(workspace, path, "fixture", "Fixture evidence")
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, []string{registration.ID}, nil)
	require.NoError(t, err)

	read := toolByName(t, tools, "r42_read_artifact")
	properties, ok := read.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "offset_bytes")
	search := toolByName(t, tools, "r42_search_artifact")
	searchProperties, ok := search.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, searchProperties, "pattern")
	assert.Contains(t, searchProperties, "context_lines")

	page, err := read.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": registration.ID, "max_bytes": 5, "offset_bytes": 4,
	}})
	require.NoError(t, err)
	assert.Contains(t, page.TextResultForLLM, `"offset_bytes":4`)
	assert.Contains(t, page.TextResultForLLM, `"next_offset_bytes":9`)

	result, err := search.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_id": registration.ID, "pattern": "target\\s+phrase", "max_matches": 5,
	}})
	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"matched_text":"target phrase"`)
}

func TestEvidenceToolsSearchAllAuthorizedArtifacts(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	importedWorkspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	current := declareSearchArtifact(t, registry, workspace, "current", "current.json", "current scope needle")
	imported := declareSearchArtifact(t, registry, importedWorkspace, "imported", "claims.json", "imported claim needle")
	directory := filepath.Join(workspace, "sources")
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "nested"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "nested", "source.md"), []byte("directory source needle"), 0o600))
	directoryRecord, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "sources", Type: researchspec.ArtifactTypeDirectory, Path: "sources", Description: "Collected sources",
	})
	require.NoError(t, err)
	missing, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Description: "Future report output",
	})
	require.NoError(t, err)

	tools, err := evidenceToolsWithArtifactRegistry(
		workspace, nil, false, registry, []string{current.ID, imported.ID, directoryRecord.ID, missing.ID}, nil,
	)
	require.NoError(t, err)

	search := toolByName(t, tools, "r42_search_artifacts")
	result, err := search.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"pattern": "needle", "max_matches": 10,
	}})
	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"accepted":true`)
	var response struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			Matches []struct {
				ArtifactID   string `json:"artifact_id"`
				ArtifactName string `json:"artifact_name"`
			} `json:"matches"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	require.True(t, response.Accepted)
	assert.Contains(t, response.Output.Matches, struct {
		ArtifactID   string `json:"artifact_id"`
		ArtifactName string `json:"artifact_name"`
	}{ArtifactID: current.ID, ArtifactName: "current"})
	assert.Contains(t, response.Output.Matches, struct {
		ArtifactID   string `json:"artifact_id"`
		ArtifactName string `json:"artifact_name"`
	}{ArtifactID: imported.ID, ArtifactName: "imported"})
	var directoryMatch struct {
		ArtifactID   string `json:"artifact_id"`
		ArtifactName string `json:"artifact_name"`
	}
	for _, match := range response.Output.Matches {
		if match.ArtifactName == "nested/source.md" {
			directoryMatch = match
			break
		}
	}
	require.NotEmpty(t, directoryMatch.ArtifactID)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": directoryMatch.ArtifactID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, "directory source needle")
}

func declareSearchArtifact(t *testing.T, registry *artifactpkg.Registry, workspace, name, relativePath, content string) artifactpkg.Record {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, relativePath), []byte(content), 0o600))
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: name, Type: researchspec.ArtifactTypeFile, Path: relativePath, Description: name + " artifact",
	})
	require.NoError(t, err)
	return record
}

func TestEvidenceToolsExposeArtifactPaging(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "claims.json")
	require.NoError(t, os.WriteFile(artifactPath, []byte("0123456789"), 0o600))
	artifacts := []researchspec.Artifact{{
		Name: "claims", Type: researchspec.ArtifactTypeFile, Path: "claims.json", Description: "Claims fixture",
	}}
	runArtifacts := artifactpkg.NewRegistry()
	record, err := runArtifacts.Declare(workspace, artifacts[0])
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, artifacts, false, runArtifacts, []string{record.ID}, nil)
	require.NoError(t, err)

	read := toolByName(t, tools, "r42_read_artifact")
	properties, ok := read.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "offset_bytes")
	assert.Contains(t, properties, "id")
	assert.NotContains(t, properties, "name")
	require.NotContains(t, read.Parameters["required"], "offset_bytes")

	result, err := read.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": record.ID, "max_bytes": 3, "offset_bytes": 4,
	}})
	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"content":"456"`)
	assert.Contains(t, result.TextResultForLLM, `"next_offset_bytes":7`)

	firstPage, err := read.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": record.ID, "max_bytes": 3,
	}})
	require.NoError(t, err)
	assert.Contains(t, firstPage.TextResultForLLM, `"content":"012"`)
	assert.Contains(t, firstPage.TextResultForLLM, `"offset_bytes":0`)
}

func TestApplyToolUseBindingsInjectsHCLInputAndRestrictsModelSchema(t *testing.T) {
	t.Parallel()

	var received map[string]any
	tools := []sdk.Tool{{
		Name: "tool_finish",
		Parameters: objectSchema(map[string]any{
			"Workspace": map[string]any{"type": "string"},
			"Claims":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"Note":      map[string]any{"type": "string"},
		}, []string{"Workspace", "Claims"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			received, _ = invocation.Arguments.(map[string]any)
			return acceptedToolResult("done")
		},
	}}
	bound, err := applyToolUseBindings(tools, []researchspec.ToolUse{{
		Name: "finish", ToolID: "tool_finish", Terminate: true,
		Input: cty.ObjectVal(map[string]cty.Value{"Workspace": cty.StringVal("D:/run/task")}),
		InputFromAgent: cty.ObjectVal(map[string]cty.Value{
			"Claims": cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
				"id": cty.StringVal("artifact-1"), "path": cty.StringVal("claims.json"),
				"description": cty.StringVal("Validated claims"),
			})}),
		}),
	}})
	require.NoError(t, err)
	require.Len(t, bound, 1)
	properties, ok := bound[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, properties, "Workspace")
	assert.Contains(t, properties, "Claims")
	claimsProperties, ok := properties["Claims"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, claimsProperties["description"], "Validated claims")
	assert.ElementsMatch(t, []string{"Claims"}, bound[0].Parameters["required"])

	_, err = bound[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"Claims": []any{"C-1"}, "Note": "optional",
	}})
	require.NoError(t, err)
	assert.Equal(t, "D:/run/task", received["Workspace"])
	assert.Equal(t, []any{"C-1"}, received["Claims"])
}

func TestBindResearchToolUsesResolvesBoundOutputArtifactID(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "scope", Type: researchspec.ArtifactTypeFile,
		Path: "scope.json", Description: "Validated scope",
	})
	require.NoError(t, err)
	var received map[string]any
	tools := []sdk.Tool{{
		Name: "submit_scope",
		Parameters: objectSchema(map[string]any{
			"artifact_id":        map[string]any{"type": "string"},
			"_r42_artifact_path": map[string]any{"type": "string"},
			"scope":              map[string]any{"type": "string"},
		}, []string{"artifact_id", "_r42_artifact_path", "scope"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			received, _ = invocation.Arguments.(map[string]any)
			return acceptedToolResult("done")
		},
	}}

	bound, err := bindResearchToolUses(
		tools,
		[]researchspec.ToolUse{{
			Name: "submit_scope", ToolID: "submit_scope",
			Input: cty.ObjectVal(map[string]cty.Value{
				"artifact_id": cty.StringVal(record.ID), "_r42_artifact_path": cty.StringVal(""),
			}),
		}},
		func() (*evidence.ArtifactEvidenceAccess, error) {
			return evidence.NewArtifactEvidenceAccess(registry, nil)
		},
		registry,
		workspace,
		"submit_scope",
		nil,
	)
	require.NoError(t, err)
	properties, ok := bound[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, properties, "artifact_id")
	assert.NotContains(t, properties, "_r42_artifact_path")

	result, err := bound[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"scope": "complete",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"accepted":true`)
	assert.Equal(t, record.ID, received["artifact_id"])
	assert.Equal(t, record.Path, received["_r42_artifact_path"])
}

func TestMaterializeArtifactReferencesResolvesBoundInputArtifactID(t *testing.T) {
	t.Parallel()

	reference, err := researchspec.ArtifactReferenceFunction(nil).Call([]cty.Value{cty.StringVal("scope")})
	require.NoError(t, err)
	uses := materializeArtifactReferences([]researchspec.ToolUse{{
		Name: "submit_scope",
		Input: cty.ObjectVal(map[string]cty.Value{
			"artifact_id":    reference.GetAttr("id"),
			"artifact_ids":   cty.ListVal([]cty.Value{reference.GetAttr("id")}),
			"artifact_map":   cty.MapVal(map[string]cty.Value{"scope": reference.GetAttr("id")}),
			"artifact_tuple": cty.TupleVal([]cty.Value{reference.GetAttr("id")}),
			"unchanged":      cty.NumberIntVal(1),
		}),
	}}, map[string]string{"scope": "artifact-scope"})

	require.Len(t, uses, 1)
	assert.Equal(t, "artifact-scope", uses[0].Input.GetAttr("artifact_id").AsString())
	assert.Equal(t, "artifact-scope", uses[0].Input.GetAttr("artifact_ids").Index(cty.NumberIntVal(0)).AsString())
	assert.Equal(t, "artifact-scope", uses[0].Input.GetAttr("artifact_map").Index(cty.StringVal("scope")).AsString())
	assert.Equal(t, "artifact-scope", uses[0].Input.GetAttr("artifact_tuple").Index(cty.NumberIntVal(0)).AsString())
	assert.Equal(t, "1", uses[0].Input.GetAttr("unchanged").AsBigFloat().Text('f', 0))
}

func TestMaterializeArtifactTargetPathResolvesBoundArtifactID(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "scope", Type: researchspec.ArtifactTypeFile,
		Path: "scope.json", Description: "Validated scope",
	})
	require.NoError(t, err)
	arguments := map[string]any{
		"artifact_id":        record.ID,
		"_r42_artifact_path": "untrusted-path",
		"scope":              "complete",
	}

	err = materializeArtifactTargetPath(arguments, registry)

	require.NoError(t, err)
	assert.Equal(t, record.Path, arguments["_r42_artifact_path"])
}

func TestMaterializeArtifactPathsResolvesBoundArtifactIDs(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	scope, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "scope", Type: researchspec.ArtifactTypeFile,
		Path: "scope.json", Description: "Validated scope",
	})
	require.NoError(t, err)
	claims, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "claims", Type: researchspec.ArtifactTypeFile,
		Path: "claims.json", Description: "Validated claims",
	})
	require.NoError(t, err)
	arguments := map[string]any{
		"scope_artifact_id":  scope.ID,
		"_r42_scope_path":    "untrusted-scope",
		"claim_artifact_ids": []any{claims.ID},
		"_r42_claim_paths":   []any{"untrusted-claims"},
	}

	err = materializeArtifactPaths(arguments, registry)

	require.NoError(t, err)
	assert.Equal(t, scope.Path, arguments["_r42_scope_path"])
	assert.Equal(t, []any{claims.Path}, arguments["_r42_claim_paths"])
}

func TestApplyToolUseBindingsRejectsValidationFailureBeforeHandler(t *testing.T) {
	t.Parallel()

	called := false
	tools := []sdk.Tool{{
		Name: "tool_finish",
		Parameters: objectSchema(map[string]any{
			"Claims": map[string]any{"type": "array"},
		}, []string{"Claims"}),
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			called = true
			return acceptedToolResult("done")
		},
	}}
	bound, err := applyToolUseBindings(tools, []researchspec.ToolUse{{
		Name: "finish", ToolID: "tool_finish",
		InputFromAgent: cty.ObjectVal(map[string]cty.Value{
			"Claims": cty.EmptyTupleVal,
		}),
		Validations: []corespec.Condition{{
			Expression: "input.Claims == null", ErrorMessage: "claims are required",
		}},
	}})
	require.NoError(t, err)
	_, err = bound[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{"Claims": []any{}}})
	require.ErrorContains(t, err, "claims are required")
	assert.False(t, called)
}

func TestApplyToolUseBindingsDescribesTypedSources(t *testing.T) {
	t.Parallel()

	tools := []sdk.Tool{{
		Name: "tool_finish", Description: "Submit validated fields.", Parameters: objectSchema(map[string]any{
			"canonical_url": map[string]any{"type": "string"},
			"title":         map[string]any{"type": "string"},
			"url":           map[string]any{"type": "string"},
		}, []string{"canonical_url", "title", "url"}),
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) { return acceptedToolResult("done") },
	}}
	bound, err := applyToolUseBindings(tools, []researchspec.ToolUse{{
		Name: "finish", ToolID: "tool_finish", InputFromAgent: cty.ObjectVal(map[string]cty.Value{
			"url": cty.ObjectVal(map[string]cty.Value{
				"desc": cty.StringVal("The URL recorded in the authorized evidence artifact."),
				"sources": cty.TupleVal([]cty.Value{
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-directory"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("directory"), "description": cty.StringVal("Evidence directory")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-file"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Claims JSON")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-0123456789abcdef0123456789abcdef"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Primary source")}),
				}),
			}),
			"canonical_url": cty.ObjectVal(map[string]cty.Value{
				"desc": cty.StringVal("Optional publication identity URL."),
				"sources": cty.TupleVal([]cty.Value{
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-0123456789abcdef0123456789abcdef"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Primary source")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-file"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Claims JSON")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-directory"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("directory"), "description": cty.StringVal("Evidence directory")}),
				}),
			}),
			"title": cty.ObjectVal(map[string]cty.Value{
				"desc": cty.StringVal("The retained source title."),
				"sources": cty.TupleVal([]cty.Value{
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-file"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Claims JSON")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-directory"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("directory"), "description": cty.StringVal("Evidence directory")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-directory"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("directory"), "description": cty.StringVal("Evidence directory")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-0123456789abcdef0123456789abcdef"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Primary source")}),
				}),
			}),
		}),
	}})
	require.NoError(t, err)
	properties, ok := bound[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	url, ok := properties["url"].(map[string]any)
	require.True(t, ok)
	description, ok := url["description"].(string)
	require.True(t, ok)
	assert.Equal(t, "The URL recorded in the authorized evidence artifact.", description)
	assert.NotContains(t, description, "r42_read_artifact")
	assert.Contains(t, bound[0].Description, "Agent-provided field sources:")
	assert.Contains(t, bound[0].Description, "canonical_url, title, url:")
	assert.Equal(t, 1, strings.Count(bound[0].Description, "Evidence directory"))
	assert.Contains(t, bound[0].Description, "artifact-directory")
	assert.Contains(t, bound[0].Description, "artifact-file")
	assert.Contains(t, bound[0].Description, "artifact-0123456789abcdef0123456789abcdef")
}

func TestApplyToolUseBindingsDescribesEmptySourcesAccurately(t *testing.T) {
	t.Parallel()

	tools := []sdk.Tool{{
		Name: "tool_finish", Parameters: objectSchema(map[string]any{"quote": map[string]any{"type": "string"}}, []string{"quote"}),
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) { return acceptedToolResult("done") },
	}}
	bound, err := applyToolUseBindings(tools, []researchspec.ToolUse{{
		Name: "finish", ToolID: "tool_finish", InputFromAgent: cty.ObjectVal(map[string]cty.Value{
			"quote": cty.ObjectVal(map[string]cty.Value{"desc": cty.StringVal("Exact evidence quote."), "sources": cty.EmptyTupleVal}),
		}),
	}})
	require.NoError(t, err)
	properties, ok := bound[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	quote, ok := properties["quote"].(map[string]any)
	require.True(t, ok)
	description, ok := quote["description"].(string)
	require.True(t, ok)
	assert.Equal(t, "Exact evidence quote.", description)
	assert.Contains(t, bound[0].Description, "No declared readable source")
	assert.Contains(t, bound[0].Description, "task instructions or data returned directly by prior typed-tool calls")
	assert.NotContains(t, bound[0].Description, "current phase")
}

func TestEvaluateToolPostconditionsUsesTypedInputAndOutput(t *testing.T) {
	t.Parallel()

	condition := corespec.Condition{
		Expression:   "input.name != \"\" && output.saved",
		ErrorMessage: "tool output was not saved",
	}
	err := evaluateToolPostconditions(
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("claim")}),
		cty.ObjectVal(map[string]cty.Value{"saved": cty.BoolVal(false)}),
		[]corespec.Condition{condition},
	)
	assert.ErrorContains(t, err, "tool output was not saved")
}

func TestReadArtifactAcceptsRegisteredEvidenceAfterToolCreation(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	artifacts := artifactpkg.NewRegistry()

	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("approved source"), 0o600))
	registered, _, err := artifacts.RegisterEvidence(workspace, path, "local-record:42", "Approved source")
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, artifacts, []string{registered.ID}, nil)
	require.NoError(t, err)
	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": registered.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, "approved source")
}

func TestReadArtifactAcceptsUnreviewedEvidenceArtifact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	artifacts := artifactpkg.NewRegistry()

	path := filepath.Join(workspace, "pending.md")
	require.NoError(t, os.WriteFile(path, []byte("pending source"), 0o600))
	registered, _, err := artifacts.RegisterEvidence(workspace, path, "local-record:42", "Pending source")
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, artifacts, []string{registered.ID}, nil)
	require.NoError(t, err)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": registered.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, `"accepted":true`)
	assert.Contains(t, read.TextResultForLLM, "pending source")
}

func TestArtifactQuoteValidationIdentifiesClaimAndField(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("actual evidence\n"), 0o600))
	registration, _, err := registry.RegisterEvidence(workspace, path, "fixture", "Fixture evidence")
	require.NoError(t, err)
	access, err := evidence.NewArtifactEvidenceAccess(registry, []string{registration.ID})
	require.NoError(t, err)
	invalid, err := invalidArtifactQuotes(map[string]any{
		"cards": []any{map[string]any{
			"id": "C-007", "artifact_id": registration.ID, "exact_quote": "missing evidence",
		}},
	}, access)
	require.NoError(t, err)
	require.Len(t, invalid, 1)
	assert.Contains(t, invalid[0], "claim_id=C-007")
	assert.Contains(t, invalid[0], "artifact_id="+registration.ID)
	assert.Contains(t, invalid[0], "field=cards[0].exact_quote")
	assert.Contains(t, invalid[0], `nearby_text="actual evidence"`)
}

func TestArtifactQuoteValidationNearbyText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		content           string
		quote             string
		container         string
		recordID          string
		expectedReference string
		expectedNearby    string
		expectsNearby     bool
	}{
		{
			name: "three word phrase for claim", content: "prefix alpha beta gamma source suffix",
			quote: "alpha beta gamma altered", container: "cards", recordID: "C-001",
			expectedReference: "claim_id=C-001", expectedNearby: "alpha beta gamma", expectsNearby: true,
		},
		{
			name: "single word fallback for quote", content: "prefix distinctive source suffix",
			quote: "distinctive missing words", container: "quotes", recordID: "Q-001",
			expectedReference: "quote_id=Q-001", expectedNearby: "distinctive", expectsNearby: true,
		},
		{
			name: "nearby text is bounded", content: strings.Repeat("context ", 100) + "distinctive source suffix",
			quote: "distinctive missing words", container: "cards", recordID: "C-003",
			expectedReference: "claim_id=C-003", expectedNearby: "distinctive", expectsNearby: true,
		},
		{
			name: "no nearby candidate", content: "actual evidence",
			quote: "completely absent terms", container: "cards", recordID: "C-002",
			expectedReference: "claim_id=C-002", expectsNearby: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			registry := artifactpkg.NewRegistry()
			path := filepath.Join(workspace, "source.md")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			registration, _, err := registry.RegisterEvidence(workspace, path, "fixture", "Fixture evidence")
			require.NoError(t, err)
			access, err := evidence.NewArtifactEvidenceAccess(registry, []string{registration.ID})
			require.NoError(t, err)
			invalid, err := invalidArtifactQuotes(map[string]any{
				tt.container: []any{map[string]any{
					"id": tt.recordID, "artifact_id": registration.ID, "exact_quote": tt.quote,
				}},
			}, access)
			require.NoError(t, err)
			require.Len(t, invalid, 1)
			assert.Contains(t, invalid[0], tt.expectedReference)
			assert.Contains(t, invalid[0], "field="+tt.container+"[0].exact_quote")
			if tt.expectsNearby {
				assert.Contains(t, invalid[0], tt.expectedNearby)
				assert.Contains(t, invalid[0], "nearby_text=")
				assert.LessOrEqual(t, len([]rune(nearbyTextFromFailure(t, invalid[0]))), 300)
			} else {
				assert.NotContains(t, invalid[0], "nearby_text=")
			}
		})
	}
}

func toolByName(t *testing.T, tools []sdk.Tool, name string) sdk.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	require.FailNow(t, "tool not found", name)
	return sdk.Tool{}
}

func nearbyTextFromFailure(t *testing.T, failure string) string {
	t.Helper()
	_, encoded, found := strings.Cut(failure, "nearby_text=")
	require.True(t, found)
	value, err := strconv.Unquote(encoded)
	require.NoError(t, err)
	return value
}

func TestCollectionProtocolToolsRejectArtifactWithoutSource(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence", "source.md")
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	require.NoError(t, context.AddArtifactTarget(filepath.Join(workspace, "evidence"), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")

	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "Collected material.",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"code":"artifact_source"`)
	assert.NoFileExists(t, path)
}

func TestCollectionProtocolToolsRejectInvalidArtifactPaths(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	require.NoError(t, context.AddArtifactTarget(filepath.Join(workspace, "evidence"), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")
	tests := []struct {
		name string
		path string
	}{
		{name: "outside workspace", path: filepath.Join(t.TempDir(), "source.md")},
		{name: "outside evidence directory", path: filepath.Join(workspace, "source.md")},
		{name: "non markdown file", path: filepath.Join(workspace, "evidence", "source.txt")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"artifact_path": tt.path,
				"content":       "Collected material.",
				"source":        "local-record:42",
			}})

			require.NoError(t, err)
			assert.Contains(t, result.TextResultForLLM, `"code":"artifact_path"`)
			assert.NoFileExists(t, tt.path)
		})
	}
}

func TestCollectionProtocolToolsRejectArtifactSymlinkOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "source.md")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	path := filepath.Join(workspace, "evidence", "source.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	require.NoError(t, context.AddArtifactTarget(filepath.Join(workspace, "evidence"), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")

	result, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "replacement",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"code":"artifact_write_failed"`)
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "original", string(content))
}

func TestCollectionProtocolToolsDoNotOverwriteSavedArtifact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence", "source.md")
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	require.NoError(t, context.AddArtifactTarget(filepath.Join(workspace, "evidence"), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")
	first, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "original evidence",
		"source":        "source:original",
	}})
	require.NoError(t, err)
	assert.Contains(t, first.TextResultForLLM, `"accepted":true`)

	second, err := saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"artifact_path": path,
		"content":       "replacement evidence",
		"source":        "source:replacement",
	}})

	require.NoError(t, err)
	assert.Contains(t, second.TextResultForLLM, `"code":"artifact_write_failed"`)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: source:original\n\noriginal evidence", string(content))
}

func TestCollectionProtocolToolsConcurrentSaveDoesNotOverwriteArtifact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence", "source.md")
	context := collection.NewContext(workspace, 10, nil)
	freezeTestInformationNeeds(t, context)
	require.NoError(t, context.AddArtifactTarget(filepath.Join(workspace, "evidence"), true))
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	saveTool := toolByName(t, protocol, "r42_save_artifact")
	contents := []string{"first evidence", "second evidence"}
	sources := []string{"source:first", "source:second"}
	results := make([]sdk.ToolResult, len(contents))
	errors := make([]error, len(contents))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range contents {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errors[index] = saveTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"artifact_path": path,
				"content":       contents[index],
				"source":        sources[index],
			}})
		}(index)
	}
	close(start)
	wg.Wait()

	acceptedIndex := -1
	for index := range results {
		require.NoError(t, errors[index])
		if strings.Contains(results[index].TextResultForLLM, `"accepted":true`) {
			require.Equal(t, -1, acceptedIndex, "only one concurrent save may succeed")
			acceptedIndex = index
			continue
		}
		assert.Contains(t, results[index].TextResultForLLM, `"code":"artifact_write_failed"`)
	}
	require.NotEqual(t, -1, acceptedIndex)

	var response struct {
		Output saveArtifactOutput `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(results[acceptedIndex].TextResultForLLM), &response))
	registeredPath, err := context.Artifacts.Record(response.Output.ArtifactID)
	require.NoError(t, err)
	content, err := os.ReadFile(registeredPath.Path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: "+sources[acceptedIndex]+"\n\n"+contents[acceptedIndex], string(content))
}

func TestCollectionQCVerdictToolRecordsTypedDecision(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	plan := collection.NewInformationNeedsHandler(context).Set(collection.InformationNeedsArgs{InformationNeeds: []collection.InformationNeedInput{{
		Question: "supplier", StopConditions: []collection.StopConditionInput{{Condition: "relationship"}},
	}}})
	require.True(t, plan.Accepted)
	// Register and checkpoint one artifact so the round reports material progress.
	require.NoError(t, context.Artifacts.RetainToolResult("call-qc", "source evidence"))
	registered := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{SourceToolCallID: "call-qc"})
	require.True(t, registered.Accepted)
	checkpoint := collection.NewCheckpointHandler(context).Submit(collection.CheckpointArgs{
		NeedDispositions: []collection.NeedDisposition{
			{InformationNeedID: "NEED-001", SearchDisposition: collection.SearchDispositionContinue},
		},
	})
	require.True(t, checkpoint.Accepted)
	verdicts := collectionqc.NewVerdictRecorder()
	tool := collectionQCVerdictTool(context, verdicts)
	result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"assessments": []any{map[string]any{
		"information_need_id": "NEED-001", "status": "sufficient", "unsatisfied_condition_ids": []any{}, "evidence_progress": "material",
	}}}})

	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.Equal(t, true, response["accepted"])
}

func TestEvidenceToolsExposeIDsAndDeclaredArtifactNamesOnly(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	require.NoError(t, os.WriteFile(path, []byte("- Source: local-record:42\n\nevidence"), 0o600))
	artifacts := []researchspec.Artifact{
		{
			Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md",
			Description: "Report fixture", Required: true, NonEmpty: true,
		},
		{
			Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence",
			Description: "Evidence fixture",
		},
	}
	registry := artifactpkg.NewRegistry()
	for _, declared := range artifacts {
		_, err := registry.Declare(workspace, declared)
		require.NoError(t, err)
	}
	registered, _, err := registry.RegisterEvidence(workspace, path, "local-record:42", "Evidence")
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, artifacts, true, registry, []string{registered.ID}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"r42_list_artifacts", "r42_list_artifact_files", "r42_read_artifact", "r42_search_artifact",
		"r42_search_artifacts",
		"r42_read_artifact_json_schema", "r42_query_artifact_json",
		"r42_write_markdown",
	}, toolNames(tools))
	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": registered.ID, "max_bytes": float64(100)}})
	require.NoError(t, err)
	var readResponse struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			Content string `json:"content"`
			Source  string `json:"source"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(read.TextResultForLLM), &readResponse))
	assert.True(t, readResponse.Accepted)
	assert.Equal(t, "- Source: local-record:42\n\nevidence", readResponse.Output.Content)
	assert.Equal(t, "local-record:42", readResponse.Output.Source)
	listed, err := toolByName(t, tools, "r42_list_artifacts").Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	var listedResponse struct {
		Accepted bool                 `json:"accepted"`
		Output   []artifactpkg.Record `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(listed.TextResultForLLM), &listedResponse))
	assert.True(t, listedResponse.Accepted)
	require.Len(t, listedResponse.Output, 1)
	readArtifactTool := toolByName(t, tools, "r42_read_artifact")
	assert.Contains(t, readArtifactTool.Description, "r42_list_artifacts")
	assert.Contains(t, readArtifactTool.Description, "ID")
	writeTool := toolByName(t, tools, "r42_write_markdown")
	write, err := writeTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"artifact_id": "unknown", "content": "# no"}})
	require.NoError(t, err)
	assert.Contains(t, write.TextResultForLLM, `"accepted":false`)
}

func TestJSONArtifactToolsReturnSchemaAndJQProjection(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "claims", Type: researchspec.ArtifactTypeFile, Path: "claims.json", Description: "Claims",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record.Path, []byte(`{"claims":[{"id":"C-1","text":"one"}]}`), 0o600))
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, []string{record.ID}, nil)
	require.NoError(t, err)
	schemaTool := toolByName(t, tools, "r42_read_artifact_json_schema")
	schema, err := schemaTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": record.ID}})
	require.NoError(t, err)
	assert.Contains(t, schema.TextResultForLLM, `"claims"`)

	queryTool := toolByName(t, tools, "r42_query_artifact_json")
	projection, err := queryTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": record.ID, "query": ".claims[0].id",
	}})
	require.NoError(t, err)
	assert.Contains(t, projection.TextResultForLLM, `"C-1"`)
}

func TestArtifactDirectoryToolListsReadableChildIDs(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	directory := filepath.Join(workspace, "evidence")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "source.json"), []byte(`{"source":true}`), 0o600))
	registry := artifactpkg.NewRegistry()
	parent, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence", Description: "Source documents",
	})
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, []string{parent.ID}, nil)
	require.NoError(t, err)

	listed, err := toolByName(t, tools, "r42_list_artifact_files").Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": parent.ID}})
	require.NoError(t, err)
	var listedResponse struct {
		Output []artifactpkg.Record `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(listed.TextResultForLLM), &listedResponse))
	require.Len(t, listedResponse.Output, 1)
	assert.Equal(t, "source.json", listedResponse.Output[0].Name)
	child, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": listedResponse.Output[0].ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, child.TextResultForLLM, `\"source\":true`)
}

func TestArtifactDirectoryToolSynchronizesListedChildCapabilities(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	directory := filepath.Join(workspace, "evidence")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "source.json"), []byte(`{"source":true}`), 0o600))
	registry := artifactpkg.NewRegistry()
	parent, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence", Description: "Source documents",
	})
	require.NoError(t, err)
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, []string{parent.ID}, nil)
	require.NoError(t, err)

	listTool := toolByName(t, tools, "r42_list_artifact_files")
	initial, err := listTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": parent.ID}})
	require.NoError(t, err)
	var listedResponse struct {
		Output []artifactpkg.Record `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(initial.TextResultForLLM), &listedResponse))
	require.Len(t, listedResponse.Output, 1)
	childID := listedResponse.Output[0].ID
	readTool := toolByName(t, tools, "r42_read_artifact")

	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers*2)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			<-start
			_, listErr := listTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": parent.ID}})
			if listErr != nil {
				errs <- listErr
				return
			}
			_, readErr := readTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": childID, "max_bytes": 100}})
			if readErr != nil {
				errs <- readErr
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for handlerErr := range errs {
		require.NoError(t, handlerErr)
	}
}

func TestArtifactToolsRejectReadyArtifactsOutsideCurrentCapability(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	allowed, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "allowed", Type: researchspec.ArtifactTypeFile, Path: "allowed.json", Description: "Allowed data",
	})
	require.NoError(t, err)
	forbidden, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "forbidden", Type: researchspec.ArtifactTypeFile, Path: "forbidden.json", Description: "Other task data",
	})
	require.NoError(t, err)
	for _, record := range []artifactpkg.Record{allowed, forbidden} {
		require.NoError(t, os.WriteFile(record.Path, []byte(`{"id":"`+record.Name+`"}`), 0o600))
	}
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, []string{allowed.ID}, nil)
	require.NoError(t, err)

	listed, err := toolByName(t, tools, "r42_list_artifacts").Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, listed.TextResultForLLM, allowed.ID)
	assert.NotContains(t, listed.TextResultForLLM, forbidden.ID)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": forbidden.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, `"accepted":false`)
	assert.Contains(t, read.TextResultForLLM, "unknown_artifact")
}

func TestArtifactToolsExposeDeclaredSelfArtifactsWithoutReadyState(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "pending", Type: researchspec.ArtifactTypeFile, Path: "pending.json", Description: "Pending output",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record.Path, []byte(`{"id":"pending"}`), 0o600))
	tools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, []string{record.ID}, nil)
	require.NoError(t, err)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": record.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, `"accepted":true`)
	assert.Contains(t, read.TextResultForLLM, "pending")
	listed, err := toolByName(t, tools, "r42_list_artifacts").Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, listed.TextResultForLLM, record.ID)
}

func TestCollectionArtifactTargetsOnlyIncludeDeclaredDirectories(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	artifacts := []researchspec.Artifact{
		{Name: "sources", Type: researchspec.ArtifactTypeDirectory, Path: "sources", Description: "Collected source material"},
		{Name: "claims", Type: researchspec.ArtifactTypeFile, Path: "claims.json", Description: "Research output"},
	}
	ids := make(map[string]string, len(artifacts))
	for _, declared := range artifacts {
		record, err := registry.Declare(workspace, declared)
		require.NoError(t, err)
		ids[declared.Name] = record.ID
	}

	context := collection.NewContext(workspace, 10, nil)
	require.NoError(t, addCollectionArtifactTargets(context, registry, artifacts, ids))
	assert.True(t, context.AllowsArtifactPath(filepath.Join(workspace, "sources", "source.md")))
	assert.False(t, context.AllowsArtifactPath(filepath.Join(workspace, "claims.json")))
}

func TestResearchArtifactProtocolUsesIDsInsteadOfPaths(t *testing.T) {
	t.Parallel()

	prompt := closedResearchSystemPrompt("Configured instructions.")

	for _, name := range []string{"view", "grep", "head", "tail"} {
		assert.Contains(t, prompt, name)
	}
	assert.NotContains(t, prompt, "only through r42 typed tools")
	assert.Contains(t, prompt, "artifact_id")
	assert.Contains(t, prompt, "r42_read_artifact")
	assert.Contains(t, prompt, "r42_search_artifact")
	assert.Contains(t, prompt, "r42_search_artifacts")
	assert.Contains(t, prompt, "Do not use artifact paths as cross-block evidence references")
	assert.True(t, strings.HasSuffix(prompt, "Configured instructions."))
}
