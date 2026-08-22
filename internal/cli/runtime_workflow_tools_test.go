package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
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
	registerProperties, ok := protocol[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, registerProperties, "source")
	assert.Nil(t, protocol[0].Parameters["required"])
	registered, err := protocol[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
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
	checkpoint, err := protocol[1].Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, checkpoint.TextResultForLLM, "snapshot-")
}

func TestCollectionProtocolToolsSaveSnapshotWithRequiredSource(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	context := collection.NewContext(workspace, 10, nil)
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
	assert.Equal(t, []string{"r42_register_snapshot", "r42_collection_checkpoint", "r42_save_snapshot"}, toolNames(protocol))
	assert.Contains(t, phaseAllowedTools([]string{"web_fetch"}, toolNames(protocol)), "r42_save_snapshot")
	assert.ElementsMatch(t, []string{"snapshot_path", "content", "source"}, protocol[2].Parameters["required"])
	assert.Contains(t, protocol[2].Description, "Do not call r42_register_snapshot")
	assert.Contains(t, protocol[2].Description, "snapshot_id")

	path := filepath.Join(workspace, "snapshots", "source.md")
	result, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_path": path,
		"content":       "\n# Evidence\n\nCollected material.\n",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	var response struct {
		Accepted bool `json:"accepted"`
		Output   struct {
			Path       string `json:"path"`
			SnapshotID string `json:"snapshot_id"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.TextResultForLLM), &response))
	assert.True(t, response.Accepted)
	assert.Equal(t, path, response.Output.Path)
	assert.Regexp(t, `^snapshot-[0-9a-f]{32}$`, response.Output.SnapshotID)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: local-record:42\n\n\n# Evidence\n\nCollected material.\n", string(content))

	checkpoint, err := protocol[1].Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, checkpoint.TextResultForLLM, response.Output.SnapshotID)
}

func TestCollectionProtocolToolsRejectSnapshotWithoutSource(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "snapshots", "source.md")
	protocol := collectionProtocolTools(
		collection.NewContext(workspace, 10, nil),
		collection.NewCheckpointRecorder(),
	)

	result, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_path": path,
		"content":       "Collected material.",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"code":"snapshot_source"`)
	assert.NoFileExists(t, path)
}

func TestCollectionProtocolToolsRejectInvalidSnapshotPaths(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	protocol := collectionProtocolTools(
		collection.NewContext(workspace, 10, nil),
		collection.NewCheckpointRecorder(),
	)
	tests := []struct {
		name string
		path string
	}{
		{name: "outside workspace", path: filepath.Join(t.TempDir(), "source.md")},
		{name: "outside snapshots directory", path: filepath.Join(workspace, "source.md")},
		{name: "non markdown file", path: filepath.Join(workspace, "snapshots", "source.txt")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"snapshot_path": tt.path,
				"content":       "Collected material.",
				"source":        "local-record:42",
			}})

			require.NoError(t, err)
			assert.Contains(t, result.TextResultForLLM, `"code":"snapshot_path"`)
			assert.NoFileExists(t, tt.path)
		})
	}
}

func TestCollectionProtocolToolsRejectSnapshotSymlinkOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "source.md")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	path := filepath.Join(workspace, "snapshots", "source.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	protocol := collectionProtocolTools(
		collection.NewContext(workspace, 10, nil),
		collection.NewCheckpointRecorder(),
	)

	result, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_path": path,
		"content":       "replacement",
		"source":        "local-record:42",
	}})

	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"code":"snapshot_write_failed"`)
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "original", string(content))
}

func TestCollectionProtocolToolsDoNotOverwriteSavedSnapshot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "snapshots", "source.md")
	protocol := collectionProtocolTools(
		collection.NewContext(workspace, 10, nil),
		collection.NewCheckpointRecorder(),
	)
	first, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_path": path,
		"content":       "original evidence",
		"source":        "source:original",
	}})
	require.NoError(t, err)
	assert.Contains(t, first.TextResultForLLM, `"accepted":true`)

	second, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_path": path,
		"content":       "replacement evidence",
		"source":        "source:replacement",
	}})

	require.NoError(t, err)
	assert.Contains(t, second.TextResultForLLM, `"code":"snapshot_write_failed"`)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: source:original\n\noriginal evidence", string(content))
}

func TestCollectionProtocolToolsConcurrentSaveDoesNotOverwriteSnapshot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "snapshots", "source.md")
	context := collection.NewContext(workspace, 10, nil)
	protocol := collectionProtocolTools(context, collection.NewCheckpointRecorder())
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
			results[index], errors[index] = protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"snapshot_path": path,
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
		assert.Contains(t, results[index].TextResultForLLM, `"code":"snapshot_write_failed"`)
	}
	require.NotEqual(t, -1, acceptedIndex)

	var response struct {
		Output saveSnapshotOutput `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(results[acceptedIndex].TextResultForLLM), &response))
	registeredPath, err := context.Registry.Snapshot(response.Output.SnapshotID)
	require.NoError(t, err)
	content, err := os.ReadFile(registeredPath)
	require.NoError(t, err)
	assert.Equal(t, "- Source: "+sources[acceptedIndex]+"\n\n"+contents[acceptedIndex], string(content))
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
	require.NoError(t, os.WriteFile(path, []byte("- Source: local-record:42\n\nevidence"), 0o600))
	registered := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{Path: path})
	require.NotNil(t, registered.Output)
	artifacts := []researchspec.Artifact{
		{
			Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md",
			Required: true, NonEmpty: true,
		},
		{
			Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence",
		},
	}
	tools, err := evidenceTools(context.Registry, workspace, artifacts, true)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"r42_list_snapshots", "r42_read_snapshot", "r42_list_artifacts", "r42_read_artifact", "r42_write_markdown",
	}, toolNames(tools))
	read, err := tools[1].Handler(sdk.ToolInvocation{Arguments: map[string]any{"id": registered.Output.ID, "max_bytes": float64(100)}})
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
	listed, err := tools[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"accepted": true,
		"output": [
			{"name":"report","type":"file","path":"report.md","required":true,"non_empty":true},
			{"name":"evidence","type":"directory","path":"evidence","required":false,"non_empty":false}
		]
	}`, listed.TextResultForLLM)
	assert.Contains(t, tools[3].Description, "r42_list_artifacts")
	assert.Contains(t, tools[3].Description, "name")
	write, err := tools[4].Handler(sdk.ToolInvocation{Arguments: map[string]any{"name": "unknown", "content": "# no"}})
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
	}}, access, t.TempDir(), "", nil)

	result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"quotes": []any{map[string]any{"snapshot_id": "snapshot-00000000000000000000000000000000"}},
	}})

	require.NoError(t, err)
	assert.False(t, called)
	assert.Contains(t, result.TextResultForLLM, "unknown_snapshot_id")
}

func TestResearchTerminateToolRejectionsAdvanceTerminalRecorder(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("# Source\n\nEvidence without URL header."), 0o600))
	registry := snapshot.NewRegistry(workspace)
	registered, err := registry.RegisterPath(path)
	require.NoError(t, err)
	registry.MarkReviewed(registered.ID)
	access, err := evidence.NewSnapshotAccessWithRegistryAndUpstream(registry, nil)
	require.NoError(t, err)
	terminal := researchruntime.NewTerminalRecorder()
	tools := enforceSnapshotIDReferences([]sdk.Tool{{
		Name: "inspect",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			return sdk.ToolResult{ResultType: "success"}, nil
		},
	}, {
		Name: "submit",
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
			return sdk.ToolResult{ResultType: "success"}, nil
		},
	}}, access, workspace, "submit", terminal)

	arguments := map[string]any{
		"quotes": []any{map[string]any{
			"snapshot_id": registered.ID,
			"url":         "https://example.com/source",
		}},
	}
	_, invokeErr := tools[0].Handler(sdk.ToolInvocation{Arguments: arguments})
	require.NoError(t, invokeErr)
	assert.Zero(t, terminal.CompletionVersion())

	result, invokeErr := tools[1].Handler(sdk.ToolInvocation{Arguments: arguments})

	require.NoError(t, invokeErr)
	assert.Contains(t, result.TextResultForLLM, "snapshot_read_failed")
	assert.Equal(t, uint64(1), terminal.CompletionVersion())
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
	}}, access, t.TempDir(), "", nil)

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
	}}, access, workspace, "", nil)

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
			}}, access, workspace, "", nil)

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

func TestResearchTypedToolsValidateSourceBySnapshotID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		field          string
		source         string
		snapshotSource string
		accepted       bool
		expectedCode   string
	}{
		{name: "accepts non URL source", field: "source", source: "local-record:42", snapshotSource: "local-record:42", accepted: true},
		{name: "accepts URL source without scheme restrictions", field: "url", source: "ftp://example.com/source", snapshotSource: "ftp://example.com/source", accepted: true},
		{name: "rejects a different source", field: "source", source: "local-record:43", snapshotSource: "local-record:42", expectedCode: "snapshot_source_mismatch"},
		{name: "rejects mismatched claim source URL", field: "source_url", source: "https://example.com/other", snapshotSource: "https://example.com/source", expectedCode: "snapshot_source_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.md")
			content := "- Source: " + tt.snapshotSource + "\n\nEvidence."
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
			}}, access, workspace, "", nil)

			result, err := tools[0].Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"snapshot_id": registered.ID,
				tt.field:      tt.source,
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
