package cli

import (
	"encoding/json"
	"fmt"
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
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/snapshot"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
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
	registerProperties, ok := protocol[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, registerProperties, "description")
	saveProperties, ok := protocol[2].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, saveProperties, "description")
	require.NotContains(t, protocol[2].Parameters["required"], "description")

	path := filepath.Join(workspace, "snapshots", "source.md")
	result, err := protocol[2].Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"snapshot_path": path,
		"content":       "\n# Evidence\n\nCollected material.\n",
		"source":        "local-record:42",
		"description":   "Database record used for the baseline",
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
	require.Len(t, context.Registry.Snapshots(), 1)
	assert.Equal(t, "Database record used for the baseline", context.Registry.Snapshots()[0].Description)
}

func TestEvidenceToolsExposeSnapshotPagingAndSearch(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := snapshot.NewRegistry(workspace)
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("- Source: local-record:42\n\none\ntarget phrase\nthree\n"), 0o600))
	registration, err := registry.RegisterPath(path)
	require.NoError(t, err)
	tools, err := evidenceTools(registry, workspace, nil, false)
	require.NoError(t, err)

	read := toolByName(t, tools, "r42_read_snapshot")
	properties, ok := read.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "offset_bytes")
	search := toolByName(t, tools, "r42_search_snapshot")
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
		"snapshot_id": registration.ID, "pattern": "target\\s+phrase", "max_matches": 5,
	}})
	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, `"matched_text":"target phrase"`)
}

func TestEvidenceToolsExposeArtifactPaging(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := snapshot.NewRegistry(workspace)
	artifactPath := filepath.Join(workspace, "claims.json")
	require.NoError(t, os.WriteFile(artifactPath, []byte("0123456789"), 0o600))
	artifacts := []researchspec.Artifact{{
		Name: "claims", Type: researchspec.ArtifactTypeFile, Path: "claims.json", Description: "Claims fixture",
	}}
	runArtifacts := artifactpkg.NewRegistry()
	record, err := runArtifacts.Declare(workspace, artifacts[0])
	require.NoError(t, err)
	require.NoError(t, runArtifacts.MarkReady(record.ID))
	tools, err := evidenceToolsWithArtifactRegistry(registry, workspace, artifacts, false, runArtifacts, []string{record.ID})
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
		Name: "tool_finish", Parameters: objectSchema(map[string]any{"claims": map[string]any{"type": "array"}}, []string{"claims"}),
		Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) { return acceptedToolResult("done") },
	}}
	bound, err := applyToolUseBindings(tools, []researchspec.ToolUse{{
		Name: "finish", ToolID: "tool_finish", InputFromAgent: cty.ObjectVal(map[string]cty.Value{
			"claims": cty.ObjectVal(map[string]cty.Value{
				"desc": cty.StringVal("Claim IDs required for the map."),
				"sources": cty.TupleVal([]cty.Value{
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-file"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "description": cty.StringVal("Claims JSON")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-directory"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("directory"), "description": cty.StringVal("Evidence directory")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("snapshot-0123456789abcdef0123456789abcdef"), "kind": cty.StringVal("snapshot"), "type": cty.StringVal("file"), "description": cty.StringVal("Primary source")}),
				}),
			}),
		}),
	}})
	require.NoError(t, err)
	properties, ok := bound[0].Parameters["properties"].(map[string]any)
	require.True(t, ok)
	claims, ok := properties["claims"].(map[string]any)
	require.True(t, ok)
	description, ok := claims["description"].(string)
	require.True(t, ok)
	assert.Contains(t, description, "Claim IDs required for the map.")
	assert.Contains(t, description, "r42_read_artifact")
	assert.Contains(t, description, "r42_list_artifact_files")
	assert.Contains(t, description, "r42_read_snapshot")
	assert.Contains(t, description, "Current phase snapshots remain available")
}

func TestApplyToolUseBindingsDescribesCurrentSnapshotsWithoutUpstreamSources(t *testing.T) {
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
	assert.Contains(t, description, "No upstream artifact is declared")
	assert.Contains(t, description, "r42_read_snapshot")
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

func TestReadArtifactDiscoversSnapshotApprovedAfterToolCreation(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	snapshots := snapshot.NewRegistry(workspace)
	artifacts := artifactpkg.NewRegistry()
	tools, err := evidenceToolsWithArtifactRegistry(
		snapshots, workspace, nil, false, artifacts, nil,
	)
	require.NoError(t, err)

	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("approved source"), 0o600))
	registered, err := snapshots.RegisterPathWithMetadata(path, "", "Approved source")
	require.NoError(t, err)
	_, err = artifacts.RegisterSnapshot(workspace, registered.ID, registered.Path, registered.Description)
	require.NoError(t, err)
	snapshots.MarkReviewed(registered.ID)
	require.NoError(t, artifacts.MarkReady(registered.ID))

	listed, err := toolByName(t, tools, "r42_list_artifacts").Handler(sdk.ToolInvocation{})
	require.NoError(t, err)
	assert.Contains(t, listed.TextResultForLLM, registered.ID)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": registered.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, "approved source")
}

func TestReadArtifactRejectsUnreviewedSnapshot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	snapshots := snapshot.NewRegistry(workspace)
	artifacts := artifactpkg.NewRegistry()
	tools, err := evidenceToolsWithArtifactRegistry(snapshots, workspace, nil, false, artifacts, nil)
	require.NoError(t, err)

	path := filepath.Join(workspace, "pending.md")
	require.NoError(t, os.WriteFile(path, []byte("pending source"), 0o600))
	registered, err := snapshots.RegisterPath(path)
	require.NoError(t, err)
	_, err = artifacts.RegisterSnapshot(workspace, registered.ID, registered.Path, registered.Description)
	require.NoError(t, err)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": registered.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, `"accepted":false`)
	assert.Contains(t, read.TextResultForLLM, "unknown_artifact")
}

func TestSnapshotQuoteValidationIdentifiesClaimAndField(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := snapshot.NewRegistry(workspace)
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("actual evidence\n"), 0o600))
	registration, err := registry.RegisterPath(path)
	require.NoError(t, err)
	access, err := evidence.NewSnapshotAccessWithRegistry(registry)
	require.NoError(t, err)
	invalid, err := invalidSnapshotQuotes(map[string]any{
		"cards": []any{map[string]any{
			"id": "C-007", "snapshot_id": registration.ID, "exact_quote": "missing evidence",
		}},
	}, access)
	require.NoError(t, err)
	require.Len(t, invalid, 1)
	assert.Contains(t, invalid[0], "claim_id=C-007")
	assert.Contains(t, invalid[0], "snapshot_id="+registration.ID)
	assert.Contains(t, invalid[0], "field=cards[0].exact_quote")
	assert.Contains(t, invalid[0], `nearby_text="actual evidence"`)
}

func TestSnapshotQuoteValidationNearbyText(t *testing.T) {
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
			registry := snapshot.NewRegistry(workspace)
			path := filepath.Join(workspace, "source.md")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			registration, err := registry.RegisterPath(path)
			require.NoError(t, err)
			access, err := evidence.NewSnapshotAccessWithRegistry(registry)
			require.NoError(t, err)
			invalid, err := invalidSnapshotQuotes(map[string]any{
				tt.container: []any{map[string]any{
					"id": tt.recordID, "snapshot_id": registration.ID, "exact_quote": tt.quote,
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
			Description: "Report fixture", Required: true, NonEmpty: true,
		},
		{
			Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence",
			Description: "Evidence fixture",
		},
	}
	tools, err := evidenceTools(context.Registry, workspace, artifacts, true)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"r42_list_snapshots", "r42_read_snapshot", "r42_search_snapshot", "r42_list_artifacts", "r42_list_artifact_files", "r42_read_artifact",
		"r42_read_artifact_json_schema", "r42_query_artifact_json",
		"r42_write_markdown",
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
	listed, err := tools[3].Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	var listedResponse struct {
		Accepted bool                 `json:"accepted"`
		Output   []artifactpkg.Record `json:"output"`
	}
	require.NoError(t, json.Unmarshal([]byte(listed.TextResultForLLM), &listedResponse))
	assert.True(t, listedResponse.Accepted)
	assert.Empty(t, listedResponse.Output)
	readArtifactTool := toolByName(t, tools, "r42_read_artifact")
	assert.Contains(t, readArtifactTool.Description, "r42_list_artifacts")
	assert.Contains(t, readArtifactTool.Description, "ID")
	writeTool := toolByName(t, tools, "r42_write_markdown")
	write, err := writeTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"name": "unknown", "content": "# no"}})
	require.NoError(t, err)
	assert.Contains(t, write.TextResultForLLM, `"accepted":false`)
}

func TestJSONArtifactToolsReturnSchemaAndJQProjection(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	snapshots := snapshot.NewRegistry(workspace)
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "claims", Type: researchspec.ArtifactTypeFile, Path: "claims.json", Description: "Claims",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record.Path, []byte(`{"claims":[{"id":"C-1","text":"one"}]}`), 0o600))
	require.NoError(t, registry.MarkReady(record.ID))
	tools, err := evidenceToolsWithArtifactRegistry(snapshots, workspace, nil, false, registry, []string{record.ID})
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
	require.NoError(t, registry.MarkReady(parent.ID))
	tools, err := evidenceToolsWithArtifactRegistry(snapshot.NewRegistry(workspace), workspace, nil, false, registry, []string{parent.ID})
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
	require.NoError(t, registry.MarkReady(parent.ID))
	tools, err := evidenceToolsWithArtifactRegistry(snapshot.NewRegistry(workspace), workspace, nil, false, registry, []string{parent.ID})
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
		require.NoError(t, registry.MarkReady(record.ID))
	}
	tools, err := evidenceToolsWithArtifactRegistry(snapshot.NewRegistry(workspace), workspace, nil, false, registry, []string{allowed.ID})
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

func TestArtifactToolsRejectDeclaredArtifactsUntilReady(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "pending", Type: researchspec.ArtifactTypeFile, Path: "pending.json", Description: "Pending output",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record.Path, []byte(`{"id":"pending"}`), 0o600))
	tools, err := evidenceToolsWithArtifactRegistry(snapshot.NewRegistry(workspace), workspace, nil, false, registry, []string{record.ID})
	require.NoError(t, err)

	read, err := toolByName(t, tools, "r42_read_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{
		"id": record.ID, "max_bytes": 100,
	}})
	require.NoError(t, err)
	assert.Contains(t, read.TextResultForLLM, `"accepted":false`)
	assert.Contains(t, read.TextResultForLLM, "is not ready")
	listed, err := toolByName(t, tools, "r42_list_artifacts").Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.NotContains(t, listed.TextResultForLLM, record.ID)
}

func TestResearchSnapshotProtocolUsesIDsInsteadOfPaths(t *testing.T) {
	t.Parallel()

	prompt := closedResearchSystemPrompt("Configured instructions.")

	assert.Contains(t, prompt, "snapshot_id")
	assert.Contains(t, prompt, "r42_read_snapshot")
	assert.Contains(t, prompt, "r42_search_snapshot")
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
	assert.Contains(t, toolNames(tools), "r42_search_snapshot")
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

func TestRunSnapshotCatalogAuthorizesDescriptorSnapshotSources(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := snapshot.NewRegistry(workspace)
	path := filepath.Join(workspace, "reviewed.md")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o600))
	registered, err := registry.RegisterPath(path)
	require.NoError(t, err)
	registry.MarkReviewed(registered.ID)
	catalog := newRunSnapshotCatalog()
	catalog.add(registry.Snapshots())
	uses := []researchspec.ToolUse{{InputFromAgent: cty.ObjectVal(map[string]cty.Value{
		"quote": cty.ObjectVal(map[string]cty.Value{
			"desc":    cty.StringVal("Evidence quote"),
			"sources": researchspec.SnapshotsValue([]researchspec.Snapshot{{ID: registered.ID, Path: path, Description: "Reviewed source"}}),
		}),
	})}}

	assert.Equal(t, map[string]string{registered.ID: path}, catalog.authorizedToolUseSnapshots(uses))
}

func TestToolUseArtifactIDsIncludesOnlyArtifactSources(t *testing.T) {
	t.Parallel()

	uses := []researchspec.ToolUse{
		{InputFromAgent: cty.ObjectVal(map[string]cty.Value{
			"claims": cty.ObjectVal(map[string]cty.Value{
				"desc": cty.StringVal("Claims"),
				"sources": cty.TupleVal([]cty.Value{
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("artifact-claims"), "kind": cty.StringVal("artifact"), "type": cty.StringVal("file"), "path": cty.StringVal("claims.json"), "description": cty.StringVal("Claims")}),
					cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("snapshot-0123456789abcdef0123456789abcdef"), "kind": cty.StringVal("snapshot"), "type": cty.StringVal("file"), "path": cty.StringVal("source.md"), "description": cty.StringVal("Source")}),
				}),
			}),
		})},
	}

	assert.Equal(t, []string{"artifact-claims"}, toolUseArtifactIDs(uses))
}
