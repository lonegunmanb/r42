package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/snapshot"
	corespec "github.com/lonegunmanb/r42/internal/spec"
)

var closedWorldBuiltIns = []string{
	"web_search", "web_fetch", "bash", "powershell", "read_powershell", "list_powershell",
	"shell", "view", "edit", "create", "glob", "task", "ask_user",
}

var collectionBlockedBuiltIns = []string{"task", "powershell", "curl"}

var validSnapshotIDPattern = regexp.MustCompile(`^snapshot-[0-9a-f]{32}$`)

func collectionDisallowedTools(configured []string) []string {
	result := slices.Clone(configured)
	for _, name := range collectionBlockedBuiltIns {
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func closedWorldDisallowedTools(configured []string) []string {
	result := slices.Clone(configured)
	for _, name := range closedWorldBuiltIns {
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func closedWorldAllowedTools(configured, mandatory []string) []string {
	if configured == nil {
		return slices.Clone(mandatory)
	}
	return phaseAllowedTools(configured, mandatory)
}

func collectionBuiltInHooks(quota *toolCallQuota, gate *collection.AcquisitionGate) *sdk.SessionHooks {
	return &sdk.SessionHooks{
		OnPreToolUse: func(input sdk.PreToolUseHookInput, _ sdk.HookInvocation) (*sdk.PreToolUseHookOutput, error) {
			if (input.ToolName == "web_search" || input.ToolName == "web_fetch") && gate != nil {
				if reason := acquisitionGateDenial(gate); reason != "" {
					return &sdk.PreToolUseHookOutput{
						PermissionDecision:       "deny",
						PermissionDecisionReason: reason,
					}, nil
				}
			}
			return toolCallQuotaDecision(quota, input.ToolName), nil
		},
		OnPostToolUseFailure: func(
			input sdk.PostToolUseFailureHookInput,
			_ sdk.HookInvocation,
		) (*sdk.PostToolUseFailureHookOutput, error) {
			quota.rollback(input.ToolName)
			return &sdk.PostToolUseFailureHookOutput{}, nil
		},
	}
}

func acquisitionGateDenial(gate *collection.AcquisitionGate) string {
	if err := gate.Acquire(); err != nil {
		return err.Error()
	}
	return ""
}

func toolNames(tools []sdk.Tool) []string {
	result := make([]string, len(tools))
	for index := range tools {
		result[index] = tools[index].Name
	}
	return result
}

func wrapCollectionAcquisitionTools(tools []sdk.Tool, context *collection.Context) []sdk.Tool {
	result := slices.Clone(tools)
	for index := range result {
		original := result[index].Handler
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			if err := context.BeginWorkflow(); err != nil {
				return rejectedToolResult("context_validation", err.Error())
			}
			if err := context.Gate().Acquire(); err != nil {
				return rejectedToolResult("checkpoint_pending", err.Error())
			}
			toolResult, err := original(invocation)
			if err != nil {
				return toolResult, err
			}
			if invocation.ToolCallID != "" && toolResult.ResultType == "success" && toolResult.TextResultForLLM != "" {
				if err = context.Registry.RetainToolResult(invocation.ToolCallID, toolResult.TextResultForLLM); err != nil {
					return sdk.ToolResult{}, err
				}
			}
			return toolResult, nil
		}
	}
	return result
}

func collectionProtocolTools(context *collection.Context, checkpoints *collection.CheckpointRecorder) []sdk.Tool {
	register := collection.NewRegisterHandler(context)
	checkpoint := collection.NewCheckpointHandler(context)
	return []sdk.Tool{
		{
			Name: "r42_register_snapshot", Description: "Register one snapshot by workspace path or retained source tool call ID",
			Parameters: objectSchema(map[string]any{
				"path": map[string]any{"type": "string"}, "source_tool_call_id": map[string]any{"type": "string"},
			}, nil),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, err := decodeArguments[collection.RegisterArgs](invocation.Arguments)
				if err != nil {
					return rejectedToolResult("invalid_arguments", err.Error())
				}
				return responseToolResult(register.Register(args))
			},
		},
		{
			Name: "r42_collection_checkpoint", Description: "Submit all unreviewed snapshots for Collection QC, or report that Collection is exhausted",
			Parameters: objectSchema(map[string]any{
				"empty_reason":         map[string]any{"type": "string"},
				"collection_exhausted": map[string]any{"type": "boolean"},
			}, nil),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, err := decodeArguments[collection.CheckpointArgs](invocation.Arguments)
				if err != nil {
					return rejectedToolResult("invalid_arguments", err.Error())
				}
				response := checkpoint.Submit(args)
				if response.Accepted && response.Output != nil {
					if err = checkpoints.Record(*response.Output); err != nil {
						return sdk.ToolResult{}, err
					}
				}
				return responseToolResult(response)
			},
		},
	}
}

func collectionQCVerdictTool(verdicts *collectionqc.VerdictRecorder) sdk.Tool {
	return sdk.Tool{
		Name: "r42_collection_qc_verdict", Description: "Report whether the checkpoint evidence is sufficient",
		Parameters: objectSchema(map[string]any{
			"decision": map[string]any{"type": "string", "enum": []string{"sufficient", "needs_more"}},
			"issues":   issueArraySchema(),
		}, []string{"decision"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			verdict, err := decodeArguments[collectionqc.Verdict](invocation.Arguments)
			if err != nil {
				return rejectedToolResult("invalid_arguments", err.Error())
			}
			if err = verdicts.Record(verdict); err != nil {
				return rejectedToolResult("invalid_verdict", err.Error())
			}
			return acceptedToolResult(struct{}{})
		},
	}
}

func evidenceTools(
	registry *snapshot.Registry,
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
) ([]sdk.Tool, error) {
	snapshots, err := evidence.NewSnapshotAccessWithRegistry(registry)
	if err != nil {
		return nil, err
	}
	return evidenceToolsWithAccess(snapshots, workspace, artifacts, write)
}

func evidenceToolsWithUpstream(
	registry *snapshot.Registry,
	upstream map[string]string,
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
) ([]sdk.Tool, *evidence.SnapshotAccess, error) {
	snapshots, err := evidence.NewSnapshotAccessWithRegistryAndUpstream(registry, upstream)
	if err != nil {
		return nil, nil, err
	}
	tools, err := evidenceToolsWithAccess(snapshots, workspace, artifacts, write)
	tools = slices.DeleteFunc(tools, func(tool sdk.Tool) bool {
		return tool.Name == "r42_list_snapshots"
	})
	return tools, snapshots, err
}

func evidenceToolsWithAccess(
	snapshots *evidence.SnapshotAccess,
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
) ([]sdk.Tool, error) {
	artifactAccess, err := evidence.NewArtifactAccess(workspace)
	if err != nil {
		return nil, err
	}
	writer, err := evidence.NewMarkdownWriter(workspace)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]researchspec.Artifact, len(artifacts))
	for _, artifact := range artifacts {
		declared[artifact.Name] = artifact
	}

	tools := []sdk.Tool{
		{
			Name: "r42_list_snapshots", Description: "List registered research snapshots by ID",
			Parameters: objectSchema(map[string]any{}, nil),
			Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
				items, listErr := snapshots.ListSnapshots()
				if listErr != nil {
					return sdk.ToolResult{}, listErr
				}
				return acceptedToolResult(items)
			},
		},
		{
			Name: "r42_read_snapshot", Description: "Read bounded content from a registered snapshot ID",
			Parameters: objectSchema(map[string]any{
				"id": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"id", "max_bytes"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[boundedReadArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				content, readErr := snapshots.ReadSnapshot(args.ID, args.MaxBytes)
				if readErr != nil {
					return rejectedToolResult("snapshot_read_failed", readErr.Error())
				}
				return acceptedToolResult(content)
			},
		},
		{
			Name: "r42_read_artifact", Description: "Read bounded content from a declared candidate artifact by name",
			Parameters: objectSchema(map[string]any{
				"name": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"name", "max_bytes"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactReadArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				artifact, ok := declared[args.Name]
				if !ok {
					return rejectedToolResult("unknown_artifact", fmt.Sprintf("unknown artifact %q", args.Name))
				}
				content, readErr := artifactAccess.ReadArtifact(artifact.Name, artifact.Path, args.MaxBytes)
				if readErr != nil {
					return rejectedToolResult("artifact_read_failed", readErr.Error())
				}
				return acceptedToolResult(content)
			},
		},
	}
	if !write {
		return tools, nil
	}
	tools = append(tools, sdk.Tool{
		Name: "r42_write_markdown", Description: "Write a declared Markdown artifact by name",
		Parameters: objectSchema(map[string]any{
			"name": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
		}, []string{"name", "content"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			args, decodeErr := decodeArguments[markdownWriteArgs](invocation.Arguments)
			if decodeErr != nil {
				return rejectedToolResult("invalid_arguments", decodeErr.Error())
			}
			artifact, ok := declared[args.Name]
			if !ok {
				return rejectedToolResult("unknown_artifact", fmt.Sprintf("unknown artifact %q", args.Name))
			}
			if artifact.Type != researchspec.ArtifactTypeFile {
				return rejectedToolResult("invalid_artifact_type", "markdown writer requires a file artifact")
			}
			path, writeErr := writer.Write(artifact.Path, args.Content)
			if writeErr != nil {
				return rejectedToolResult("artifact_write_failed", writeErr.Error())
			}
			return acceptedToolResult(path)
		},
	})
	return tools, nil
}

func enforceSnapshotIDReferences(
	tools []sdk.Tool,
	access *evidence.SnapshotAccess,
	workspace string,
) []sdk.Tool {
	result := slices.Clone(tools)
	for index := range result {
		original := result[index].Handler
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			invalidIDs := invalidSnapshotIDs(invocation.Arguments)
			if len(invalidIDs) > 0 {
				return rejectedToolResult(
					"invalid_snapshot_id",
					"snapshot_id must use snapshot- plus 32 lowercase hexadecimal characters, not a filesystem path: "+
						strings.Join(invalidIDs, ", "),
				)
			}
			foreignPaths := foreignSnapshotPaths(invocation.Arguments, workspace)
			if len(foreignPaths) > 0 {
				return rejectedToolResult(
					"snapshot_path_not_allowed",
					"use snapshot_id for cross-block evidence references; paths are outside this research task workspace: "+
						strings.Join(foreignPaths, ", "),
				)
			}
			unknown := unknownSnapshotIDs(invocation.Arguments, access)
			if len(unknown) > 0 {
				return rejectedToolResult(
					"unknown_snapshot_id",
					"snapshot IDs are not authorized for this research task: "+strings.Join(unknown, ", "),
				)
			}
			invalidQuotes, validationErr := invalidSnapshotQuotes(invocation.Arguments, access)
			if validationErr != nil {
				return rejectedToolResult("snapshot_read_failed", validationErr.Error())
			}
			if len(invalidQuotes) > 0 {
				return rejectedToolResult(
					"snapshot_quote_not_found",
					"exact_quote is not present in its referenced snapshot_id after whitespace normalization: "+
						strings.Join(invalidQuotes, ", "),
				)
			}
			invalidURLs, validationErr := invalidSnapshotURLs(invocation.Arguments, access)
			if validationErr != nil {
				return rejectedToolResult("snapshot_read_failed", validationErr.Error())
			}
			if len(invalidURLs) > 0 {
				return rejectedToolResult(
					"snapshot_url_mismatch",
					"source URL must match the URL recorded in its referenced snapshot_id: "+
						strings.Join(invalidURLs, ", "),
				)
			}
			return original(invocation)
		}
	}
	return result
}

type snapshotURLReference struct {
	id  string
	url string
}

func invalidSnapshotURLs(arguments any, access *evidence.SnapshotAccess) ([]string, error) {
	references := make([]snapshotURLReference, 0)
	collectSnapshotURLs(arguments, &references)
	invalid := make([]string, 0)
	for _, reference := range references {
		expected, err := access.SnapshotSourceURL(reference.id)
		if err != nil {
			return nil, err
		}
		actualURL, actualOK := normalizeSnapshotURL(reference.url)
		expectedURL, expectedOK := normalizeSnapshotURL(expected)
		if !expectedOK {
			return nil, fmt.Errorf("snapshot %q URL header must be an absolute HTTP or HTTPS URL", reference.id)
		}
		if !actualOK || actualURL != expectedURL {
			invalid = append(invalid, reference.id)
		}
	}
	return invalid, nil
}

func collectSnapshotURLs(value any, result *[]snapshotURLReference) {
	switch typed := value.(type) {
	case map[string]any:
		id, hasID := typed["snapshot_id"].(string)
		if hasID && strings.TrimSpace(id) != "" {
			for _, field := range []string{"url", "source_url"} {
				if sourceURL, ok := typed[field].(string); ok && strings.TrimSpace(sourceURL) != "" {
					*result = append(*result, snapshotURLReference{id: id, url: sourceURL})
				}
			}
		}
		for _, nested := range typed {
			collectSnapshotURLs(nested, result)
		}
	case []any:
		for _, nested := range typed {
			collectSnapshotURLs(nested, result)
		}
	}
}

func normalizeSnapshotURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.Fragment = ""
	return parsed.String(), true
}

func invalidSnapshotIDs(arguments any) []string {
	seen := map[string]struct{}{}
	collectSnapshotIDs(arguments, seen)
	invalid := make([]string, 0, len(seen))
	for id := range seen {
		if !validSnapshotIDPattern.MatchString(id) {
			invalid = append(invalid, id)
		}
	}
	slices.Sort(invalid)
	return invalid
}

type snapshotQuoteReference struct {
	id    string
	quote string
}

func invalidSnapshotQuotes(arguments any, access *evidence.SnapshotAccess) ([]string, error) {
	references := make([]snapshotQuoteReference, 0)
	collectSnapshotQuotes(arguments, &references)
	invalid := make([]string, 0)
	for _, reference := range references {
		contains, err := access.ContainsNormalizedText(reference.id, reference.quote)
		if err != nil {
			return nil, err
		}
		if !contains {
			invalid = append(invalid, reference.id)
		}
	}
	return invalid, nil
}

func collectSnapshotQuotes(value any, result *[]snapshotQuoteReference) {
	switch typed := value.(type) {
	case map[string]any:
		id, hasID := typed["snapshot_id"].(string)
		quote, hasQuote := typed["exact_quote"].(string)
		if hasID && hasQuote && strings.TrimSpace(id) != "" && strings.TrimSpace(quote) != "" {
			*result = append(*result, snapshotQuoteReference{id: id, quote: quote})
		}
		for _, nested := range typed {
			collectSnapshotQuotes(nested, result)
		}
	case []any:
		for _, nested := range typed {
			collectSnapshotQuotes(nested, result)
		}
	}
}

func unknownSnapshotIDs(arguments any, access *evidence.SnapshotAccess) []string {
	seen := map[string]struct{}{}
	collectSnapshotIDs(arguments, seen)
	unknown := make([]string, 0, len(seen))
	for id := range seen {
		if !access.HasSnapshot(id) {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	return unknown
}

func collectSnapshotIDs(value any, result map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			switch key {
			case "snapshot_id":
				if id, ok := nested.(string); ok && strings.TrimSpace(id) != "" {
					result[id] = struct{}{}
				}
			case "snapshot_ids":
				switch ids := nested.(type) {
				case []any:
					for _, item := range ids {
						if id, stringOK := item.(string); stringOK && strings.TrimSpace(id) != "" {
							result[id] = struct{}{}
						}
					}
				case []string:
					for _, id := range ids {
						if strings.TrimSpace(id) != "" {
							result[id] = struct{}{}
						}
					}
				}
			default:
				collectSnapshotIDs(nested, result)
			}
		}
	case []any:
		for _, nested := range typed {
			collectSnapshotIDs(nested, result)
		}
	}
}

func foreignSnapshotPaths(value any, workspace string) []string {
	paths := make([]string, 0)
	collectSnapshotPaths(value, &paths)
	foreign := make([]string, 0, len(paths))
	for _, path := range paths {
		if !pathWithinWorkspace(workspace, path) {
			foreign = append(foreign, path)
		}
	}
	return foreign
}

func collectSnapshotPaths(value any, result *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if key == "snapshot_path" {
				if path, ok := nested.(string); ok && strings.TrimSpace(path) != "" {
					*result = append(*result, path)
				}
				continue
			}
			collectSnapshotPaths(nested, result)
		}
	case []any:
		for _, nested := range typed {
			collectSnapshotPaths(nested, result)
		}
	}
}

func pathWithinWorkspace(workspace, path string) bool {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type boundedReadArgs struct {
	ID       string `json:"id"`
	MaxBytes int    `json:"max_bytes"`
}
type artifactReadArgs struct {
	Name     string `json:"name"`
	MaxBytes int    `json:"max_bytes"`
}
type markdownWriteArgs struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func issueArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": objectSchema(map[string]any{
		"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
		"path": map[string]any{"type": "string"}, "repair_hint": map[string]any{"type": "string"},
	}, []string{"code", "message"})}
}

func decodeArguments[T any](arguments any) (T, error) {
	var result T
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(encoded, &result); err != nil {
		return result, err
	}
	return result, nil
}

func acceptedToolResult[T any](output T) (sdk.ToolResult, error) {
	response := corespec.ToolResponse[T]{Accepted: true, Output: &output}
	encoded, err := json.Marshal(response)
	if err != nil {
		return sdk.ToolResult{}, err
	}
	return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
}

func rejectedToolResult(code, message string) (sdk.ToolResult, error) {
	encoded, err := json.Marshal(corespec.ToolResponse[any]{Issues: []corespec.Issue{{Code: code, Message: message}}})
	if err != nil {
		return sdk.ToolResult{}, err
	}
	return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
}

func responseToolResult[T any](response corespec.ToolResponse[T]) (sdk.ToolResult, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return sdk.ToolResult{}, err
	}
	return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
}
