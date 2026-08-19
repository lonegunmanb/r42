package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosedWorldDisallowedToolsIncludesAcquisitionAndArbitraryIO(t *testing.T) {
	t.Parallel()

	tools := closedWorldDisallowedTools(nil)

	for _, name := range []string{"web_search", "web_fetch", "bash", "powershell", "shell", "view", "edit", "task", "ask_user"} {
		assert.Contains(t, tools, name)
	}
}

func TestCollectionBuiltInHooksEnforceCheckpointGate(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	collectionContext := collection.NewContext(workspace, 1, nil)
	require.NoError(t, collectionContext.BeginWorkflow())
	hooks := collectionBuiltInHooks(newToolCallQuota(nil), collectionContext.Gate())
	require.NotNil(t, hooks)

	allowed, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "web_fetch"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", allowed.PermissionDecision)

	path := filepath.Join(workspace, "snapshot.md")
	require.NoError(t, os.WriteFile(path, []byte("evidence"), 0o600))
	registered := collection.NewRegisterHandler(collectionContext).Register(collection.RegisterArgs{Path: path})
	require.True(t, registered.Accepted)

	for _, toolName := range []string{"web_search", "web_fetch"} {
		denied, hookErr := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: toolName}, sdk.HookInvocation{})
		require.NoError(t, hookErr)
		assert.Equal(t, "deny", denied.PermissionDecision)
		assert.Contains(t, denied.PermissionDecisionReason, "checkpoint pending")
	}

	unrelated, err := hooks.OnPreToolUse(sdk.PreToolUseHookInput{ToolName: "some_read_only_tool"}, sdk.HookInvocation{})
	require.NoError(t, err)
	assert.Equal(t, "allow", unrelated.PermissionDecision)
}

func TestCollectionProtocolToolsRegisterRetainedResultAndCheckpoint(t *testing.T) {
	t.Parallel()

	context := collection.NewContext(t.TempDir(), 10, nil)
	recorder := collection.NewCheckpointRecorder()
	acquisition := sdk.Tool{Name: "fetch", Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
		return sdk.ToolResult{TextResultForLLM: `{"title":"source"}`, ResultType: "success"}, nil
	}}
	wrapped := wrapCollectionAcquisitionTools([]sdk.Tool{acquisition}, context)
	_, err := wrapped[0].Handler(sdk.ToolInvocation{ToolCallID: "call-1"})
	require.NoError(t, err)
	protocol := collectionProtocolTools(context, recorder)
	registered, err := protocol[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{"source_tool_call_id": "call-1"}})
	require.NoError(t, err)
	assert.Contains(t, registered.TextResultForLLM, `"accepted":true`)
	checkpoint, err := protocol[1].Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, checkpoint.TextResultForLLM, "snapshot-")
}

func TestCollectionQCVerdictToolRecordsTypedDecision(t *testing.T) {
	t.Parallel()

	verdicts := collectionqc.NewVerdictRecorder()
	tool := collectionQCVerdictTool(verdicts)
	result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"decision": "sufficient"}})

	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.Equal(t, true, response["accepted"])
}

func TestEvidenceToolsExposeIDsAndDeclaredArtifactNamesOnly(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	path := filepath.Join(workspace, "source.txt")
	require.NoError(t, os.WriteFile(path, []byte("evidence"), 0o600))
	registered := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{Path: path})
	require.NotNil(t, registered.Output)
	tools, err := evidenceTools(context.Registry, workspace, []researchspec.Artifact{{
		Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md",
	}}, true)
	require.NoError(t, err)

	assert.Equal(t, []string{"r42_list_snapshots", "r42_read_snapshot", "r42_read_artifact", "r42_write_markdown"}, toolNames(tools))
	read, err := tools[1].Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": registered.Output.ID, "max_bytes": float64(100)}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, "evidence")
	write, err := tools[3].Handler(sdk.ToolInvocation{Arguments: map[string]any{"name": "unknown", "content": "# no"}})
	require.NoError(t, err)
	assert.Contains(t, write.TextResultForLLM, `"accepted":false`)
}
