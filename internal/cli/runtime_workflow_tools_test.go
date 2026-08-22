package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosedWorldDisallowedToolsIncludesAcquisitionAndArbitraryIO(t *testing.T) {
	t.Parallel()

	tools := closedWorldDisallowedTools(nil)

	for _, name := range []string{
		"web_search", "web_fetch", "bash", "powershell", "read_powershell", "list_powershell",
		"shell", "view", "edit", "create", "glob", "task", "ask_user",
	} {
		assert.Contains(t, tools, name)
	}
}

func TestClosedWorldAllowedToolsDefaultsToMountedTypedTools(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"r42_read_snapshot", "submit"}, closedWorldAllowedTools(
		nil,
		[]string{"r42_read_snapshot", "submit"},
	))
}

func TestCollectionDisallowedToolsBlocksDelegationAndShellFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured []string
		expected   []string
	}{
		{name: "defaults", expected: []string{"task", "powershell", "curl"}},
		{
			name:       "preserves custom exclusions without duplicating defaults",
			configured: []string{"custom", "curl"},
			expected:   []string{"custom", "curl", "task", "powershell"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := slices.Clone(tt.configured)

			tools := collectionDisallowedTools(input)

			assert.ElementsMatch(t, tt.expected, tools)
			assert.Equal(t, tt.configured, input)
		})
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

func TestResearchSnapshotProtocolUsesIDsInsteadOfPaths(t *testing.T) {
	t.Parallel()

	prompt := closedResearchSystemPrompt("Configured instructions.")

	assert.Contains(t, prompt, "snapshot_id")
	assert.Contains(t, prompt, "r42_read_snapshot")
	assert.Contains(t, prompt, "Do not use snapshot paths as cross-block evidence references")
	assert.True(t, strings.HasSuffix(prompt, "Configured instructions."))
}

func TestResearchEvidenceToolsDoNotExposeSnapshotListing(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	tools, _, err := evidenceToolsWithUpstream(context.Registry, nil, workspace, nil, true)

	require.NoError(t, err)
	assert.NotContains(t, toolNames(tools), "r42_list_snapshots")
	assert.Contains(t, toolNames(tools), "r42_read_snapshot")
}

func TestResearchTypedToolsRejectUnknownSnapshotIDs(t *testing.T) {
	t.Parallel()

	access, err := evidence.NewSnapshotAccess(t.TempDir())
	require.NoError(t, err)
	called := false
	tools := enforceSnapshotIDReferences([]sdk.Tool{{
		Name: "submit",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			called = true
			return sdk.ToolResult{ResultType: "success"}, nil
		},
	}}, access, t.TempDir())

	result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"quotes": []any{map[string]any{"snapshot_id": "snapshot-00000000000000000000000000000000"}},
	}})

	require.NoError(t, err)
	assert.False(t, called)
	assert.Contains(t, result.TextResultForLLM, "unknown_snapshot_id")
}

func TestResearchTypedToolsRejectPathsUsedAsSnapshotIDs(t *testing.T) {
	t.Parallel()

	access, err := evidence.NewSnapshotAccess(t.TempDir())
	require.NoError(t, err)
	called := false
	tools := enforceSnapshotIDReferences([]sdk.Tool{{
		Name: "submit",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			called = true
			return sdk.ToolResult{ResultType: "success"}, nil
		},
	}}, access, t.TempDir())

	result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_id": `C:\runs\blocks\source.md`,
	}})

	require.NoError(t, err)
	assert.False(t, called)
	assert.Contains(t, result.TextResultForLLM, "invalid_snapshot_id")
	assert.Contains(t, result.TextResultForLLM, "not a filesystem path")
}

func TestResearchTypedToolsRejectSnapshotPaths(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	access, err := evidence.NewSnapshotAccess(workspace)
	require.NoError(t, err)
	called := false
	tools := enforceSnapshotIDReferences([]sdk.Tool{{
		Name: "submit",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			called = true
			return sdk.ToolResult{ResultType: "success"}, nil
		},
	}}, access, workspace)

	result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"quotes": []any{map[string]any{"snapshot_path": "C:/snapshots/source.md"}},
	}})

	require.NoError(t, err)
	assert.False(t, called)
	assert.Contains(t, result.TextResultForLLM, "snapshot_path_not_allowed")

	_, err = tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"quotes": []any{map[string]any{"snapshot_path": filepath.Join(workspace, "snapshots", "source.md")}},
	}})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestResearchTypedToolsValidateExactQuotesBySnapshotID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		exactQuote   string
		accepted     bool
		expectedCode string
	}{
		{name: "accepts unicode whitespace differences", exactQuote: "alpha beta", accepted: true},
		{name: "rejects text absent from snapshot", exactQuote: "alpha gamma", expectedCode: "snapshot_quote_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.md")
			require.NoError(t, os.WriteFile(path, []byte("alpha\n\tbeta"), 0o600))
			registry := snapshot.NewRegistry(workspace)
			registered, err := registry.RegisterPath(path)
			require.NoError(t, err)
			registry.MarkReviewed(registered.ID)
			access, err := evidence.NewSnapshotAccessWithRegistryAndUpstream(registry, nil)
			require.NoError(t, err)
			called := false
			tools := enforceSnapshotIDReferences([]sdk.Tool{{
				Name: "submit",
				Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
					called = true
					return sdk.ToolResult{ResultType: "success"}, nil
				},
			}}, access, workspace)

			result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"quotes": []any{map[string]any{
					"snapshot_id": registered.ID,
					"exact_quote": tt.exactQuote,
				}},
			}})

			require.NoError(t, err)
			assert.Equal(t, tt.accepted, called)
			if tt.expectedCode != "" {
				assert.Contains(t, result.TextResultForLLM, tt.expectedCode)
			}
		})
	}
}

func TestResearchTypedToolsValidateSourceURLBySnapshotID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		field        string
		sourceURL    string
		snapshotURL  string
		accepted     bool
		expectedCode string
	}{
		{name: "accepts snapshot source URL", field: "url", sourceURL: "https://example.com/source", snapshotURL: "https://example.com/source", accepted: true},
		{name: "accepts normalized trailing slash", field: "url", sourceURL: "https://example.com/source/", snapshotURL: "https://example.com/source", accepted: true},
		{name: "rejects a different source URL", field: "url", sourceURL: "https://example.com/other", snapshotURL: "https://example.com/source", expectedCode: "snapshot_url_mismatch"},
		{name: "rejects invalid source URL scheme", field: "url", sourceURL: "ftp://example.com/source", snapshotURL: "https://example.com/source", expectedCode: "snapshot_url_mismatch"},
		{name: "rejects source URL without host", field: "url", sourceURL: "https:///source", snapshotURL: "https://example.com/source", expectedCode: "snapshot_url_mismatch"},
		{name: "rejects mismatched claim source URL", field: "source_url", sourceURL: "https://example.com/other", snapshotURL: "https://example.com/source", expectedCode: "snapshot_url_mismatch"},
		{name: "rejects invalid snapshot URL header", field: "url", sourceURL: "https://example.com/source", snapshotURL: "ftp://example.com/source", expectedCode: "snapshot_read_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.md")
			content := "# Source\n\n- URL: " + tt.snapshotURL + "\n- Fetched at: 2026-08-21T00:00:00Z\n\nEvidence."
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			registry := snapshot.NewRegistry(workspace)
			registered, err := registry.RegisterPath(path)
			require.NoError(t, err)
			registry.MarkReviewed(registered.ID)
			access, err := evidence.NewSnapshotAccessWithRegistryAndUpstream(registry, nil)
			require.NoError(t, err)
			called := false
			tools := enforceSnapshotIDReferences([]sdk.Tool{{
				Name: "register_evidence_source",
				Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
					called = true
					return sdk.ToolResult{ResultType: "success"}, nil
				},
			}}, access, workspace)

			result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"snapshot_id": registered.ID,
				tt.field:      tt.sourceURL,
			}})

			require.NoError(t, err)
			assert.Equal(t, tt.accepted, called)
			if tt.expectedCode != "" {
				assert.Contains(t, result.TextResultForLLM, tt.expectedCode)
			}
		})
	}
}

func TestRunSnapshotCatalogPublishesOnlyReviewedSnapshots(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := snapshot.NewRegistry(workspace)
	paths := []string{
		filepath.Join(workspace, "reviewed.md"),
		filepath.Join(workspace, "pending.md"),
	}
	for index, path := range paths {
		require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "content-%d", index), 0o600))
		_, err := registry.RegisterPath(path)
		require.NoError(t, err)
	}
	items := registry.Snapshots()
	registry.MarkReviewed(items[0].ID)

	catalog := newRunSnapshotCatalog()
	catalog.add(registry.Snapshots())
	authorized := catalog.authorized(items[0].ID + " " + items[1].ID)

	assert.Equal(t, map[string]string{items[0].ID: items[0].Path}, authorized)
}
