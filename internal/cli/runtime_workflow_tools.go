package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/itchyny/gojq"
	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/snapshot"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
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
			Name: "r42_register_snapshot", Description: "Register an existing workspace snapshot path or retained source tool call result. " +
				"Optional source may be a URL or any other source identifier; when supplied, it is added as a compatible Source header only if the snapshot has no non-empty Source or legacy URL header. " +
				"Do not call this after r42_save_snapshot because that tool already registers its saved snapshot.",
			Parameters: objectSchema(map[string]any{
				"path":                map[string]any{"type": "string"},
				"source_tool_call_id": map[string]any{"type": "string"},
				"source":              map[string]any{"type": "string", "description": "Optional source identifier; may be a URL or a non-URL value"},
				"description":         map[string]any{"type": "string", "description": "Optional concise semantic summary of what this snapshot contains"},
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
		collectionSaveSnapshotTool(context),
	}
}

type saveSnapshotArgs struct {
	SnapshotPath string `json:"snapshot_path"`
	Content      string `json:"content"`
	Source       string `json:"source"`
	Description  string `json:"description"`
}

type saveSnapshotOutput struct {
	Path       string `json:"path"`
	SnapshotID string `json:"snapshot_id"`
}

func collectionSaveSnapshotTool(context *collection.Context) sdk.Tool {
	return sdk.Tool{
		Name: "r42_save_snapshot",
		Description: "Save and register complete source material as Markdown under the current Collection workspace snapshots directory, " +
			"then return path and snapshot_id. source is required and may be a URL or any other source identifier; it is written to the snapshot header. " +
			"Provide description when possible to summarize the snapshot's semantic contents for downstream research planning. " +
			"After a successful call, use the returned snapshot_id directly. Do not call r42_register_snapshot for the returned path.",
		Parameters: objectSchema(map[string]any{
			"snapshot_path": map[string]any{"type": "string", "description": "Absolute or workspace-relative .md path under the snapshots directory"},
			"content":       map[string]any{"type": "string", "description": "Complete source material in Markdown"},
			"source":        map[string]any{"type": "string", "description": "Non-empty source identifier; may be a URL or a non-URL value"},
			"description":   map[string]any{"type": "string", "description": "Optional concise semantic summary of what the saved source material contains"},
		}, []string{"snapshot_path", "content", "source"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			args, err := decodeArguments[saveSnapshotArgs](invocation.Arguments)
			if err != nil {
				return rejectedToolResult("invalid_arguments", err.Error())
			}
			return saveCollectionSnapshot(context, args)
		},
	}
}

func saveCollectionSnapshot(context *collection.Context, args saveSnapshotArgs) (sdk.ToolResult, error) {
	issues := make([]corespec.Issue, 0, 3)
	workspace := ""
	if context != nil {
		workspace = context.Workspace
	}
	path, validPath := collectionSnapshotPath(workspace, args.SnapshotPath)
	if !validPath {
		issues = append(issues, corespec.Issue{
			Code: "snapshot_path", Message: "snapshot_path must be a .md path under the current Collection workspace snapshots directory",
		})
	}
	source := strings.Join(strings.Fields(args.Source), " ")
	if source == "" {
		issues = append(issues, corespec.Issue{Code: "snapshot_source", Message: "source must not be empty"})
	}
	content := args.Content
	if strings.TrimSpace(content) == "" {
		issues = append(issues, corespec.Issue{Code: "snapshot_content", Message: "content must not be empty"})
	}
	if len(issues) > 0 {
		return responseToolResult(corespec.ToolResponse[string]{Issues: issues})
	}
	writer, err := evidence.NewMarkdownWriter(workspace)
	if err != nil {
		return sdk.ToolResult{}, err
	}
	written, err := writer.WriteNew(path, "- Source: "+source+"\n\n"+content)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return rejectedToolResult("snapshot_write_failed", "snapshot_path already exists; use a new path")
		}
		return rejectedToolResult("snapshot_write_failed", err.Error())
	}
	registration := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{
		Path: written, Description: args.Description,
	})
	if !registration.Accepted {
		return responseToolResult(corespec.ToolResponse[saveSnapshotOutput]{Issues: registration.Issues})
	}
	output := saveSnapshotOutput{
		Path:       registration.Output.Path,
		SnapshotID: registration.Output.ID,
	}
	return acceptedToolResult(output)
}

func collectionSnapshotPath(workspace, raw string) (string, bool) {
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(raw) == "" {
		return "", false
	}
	path := filepath.Clean(strings.TrimSpace(raw))
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	path, err := filepath.Abs(path)
	if err != nil || !strings.HasSuffix(strings.ToLower(path), ".md") {
		return "", false
	}
	root, err := filepath.Abs(filepath.Join(workspace, "snapshots"))
	return path, err == nil && pathWithinWorkspace(root, path)
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

func applyToolUseBindings(tools []sdk.Tool, toolUses []researchspec.ToolUse) ([]sdk.Tool, error) {
	uses := make(map[string]researchspec.ToolUse, len(toolUses))
	for _, toolUse := range toolUses {
		uses[toolUse.ToolID] = toolUse
	}
	result := slices.Clone(tools)
	for index := range result {
		toolUse, configured := uses[result[index].Name]
		if !configured {
			continue
		}
		input, err := ctyObjectToAnyMap(toolUse.Input)
		if err != nil {
			return nil, fmt.Errorf("tool_use %q input: %w", toolUse.Name, err)
		}
		agent, err := toolUseObjectMap(toolUse.InputFromAgent)
		if err != nil {
			return nil, fmt.Errorf("tool_use %q input_from_agent: %w", toolUse.Name, err)
		}
		parameters := maps.Clone(result[index].Parameters)
		properties, ok := parameters["properties"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool_use %q tool schema has no object properties", toolUse.Name)
		}
		properties = maps.Clone(properties)
		for field := range input {
			delete(properties, field)
		}
		for field, sources := range agent {
			property, exists := properties[field].(map[string]any)
			if !exists {
				continue
			}
			property = maps.Clone(property)
			description := strings.TrimSpace(anyString(property["description"]))
			sourceDescription := describeToolUseSources(sources)
			if description != "" && sourceDescription != "" {
				description += " "
			}
			property["description"] = description + sourceDescription
			properties[field] = property
		}
		parameters["properties"] = properties
		required, _ := parameters["required"].([]string)
		required = slices.DeleteFunc(slices.Clone(required), func(field string) bool {
			_, bound := input[field]
			return bound
		})
		parameters["required"] = required
		original := result[index].Handler
		result[index].Parameters = parameters
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, ok := invocation.Arguments.(map[string]any)
			if !ok && invocation.Arguments != nil {
				return rejectedToolResult("invalid_arguments", "tool arguments must be an object")
			}
			arguments = maps.Clone(arguments)
			if arguments == nil {
				arguments = make(map[string]any, len(input))
			}
			maps.Copy(arguments, input)
			invocation.Arguments = arguments
			if len(toolUse.Validations) > 0 {
				inputValue, conversionErr := anyMapToCTY(arguments)
				if conversionErr != nil {
					return sdk.ToolResult{}, fmt.Errorf("tool_use %q validation input: %w", toolUse.Name, conversionErr)
				}
				for _, validation := range toolUse.Validations {
					if _, validationErr := validation.Evaluate(
						map[string]cty.Value{"input": inputValue}, nil,
					); validationErr != nil {
						return sdk.ToolResult{}, fmt.Errorf("tool_use %q validation failed: %w", toolUse.Name, validationErr)
					}
				}
			}
			return original(invocation)
		}
	}
	return result, nil
}

func anyMapToCTY(values map[string]any) (cty.Value, error) {
	if values == nil {
		return cty.EmptyObjectVal, nil
	}
	result := make(map[string]cty.Value, len(values))
	for name, value := range values {
		converted, err := anyToCTY(value)
		if err != nil {
			return cty.NilVal, fmt.Errorf("field %q: %w", name, err)
		}
		result[name] = converted
	}
	return cty.ObjectVal(result), nil
}

func anyToCTY(value any) (cty.Value, error) {
	switch value := value.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType), nil
	case string:
		return cty.StringVal(value), nil
	case bool:
		return cty.BoolVal(value), nil
	case int:
		return cty.NumberIntVal(int64(value)), nil
	case int64:
		return cty.NumberIntVal(value), nil
	case float64:
		return cty.NumberFloatVal(value), nil
	case json.Number:
		number, err := cty.ParseNumberVal(string(value))
		if err != nil {
			return cty.NilVal, err
		}
		return number, nil
	case map[string]any:
		return anyMapToCTY(value)
	case []any:
		items := make([]cty.Value, len(value))
		for index, item := range value {
			converted, err := anyToCTY(item)
			if err != nil {
				return cty.NilVal, fmt.Errorf("index %d: %w", index, err)
			}
			items[index] = converted
		}
		if len(items) == 0 {
			return cty.EmptyTupleVal, nil
		}
		return cty.TupleVal(items), nil
	default:
		return cty.NilVal, fmt.Errorf("unsupported value type %T", value)
	}
}

func ctyObjectToAnyMap(value cty.Value) (map[string]any, error) {
	values, err := toolUseObjectMap(value)
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(values))
	for name, field := range values {
		unmarked, _ := field.UnmarkDeep()
		if !unmarked.IsWhollyKnown() {
			return nil, fmt.Errorf("field %q must be wholly known", name)
		}
		encoded, marshalErr := ctyjson.Marshal(unmarked, unmarked.Type())
		if marshalErr != nil {
			return nil, fmt.Errorf("encode field %q: %w", name, marshalErr)
		}
		var decoded any
		if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
			return nil, fmt.Errorf("decode field %q: %w", name, unmarshalErr)
		}
		result[name] = decoded
	}
	return result, nil
}

func toolUseObjectMap(value cty.Value) (map[string]cty.Value, error) {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() && !unmarked.Type().IsMapType() {
		return nil, errors.New("value must be an object")
	}
	return unmarked.AsValueMap(), nil
}

func describeToolUseSources(value cty.Value) string {
	unmarked, _ := value.UnmarkDeep()
	encoded, err := ctyjson.Marshal(unmarked, unmarked.Type())
	if err != nil {
		return ""
	}
	return "Construct this field using these authorized artifact or snapshot sources: " + string(encoded) + "."
}

func anyString(value any) string {
	result, _ := value.(string)
	return result
}

func evidenceTools(
	registry *snapshot.Registry,
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
) ([]sdk.Tool, error) {
	artifactsRegistry := artifactpkg.NewRegistry()
	ids := make([]string, 0, len(artifacts))
	for _, declared := range artifacts {
		record, declareErr := artifactsRegistry.Declare(workspace, declared)
		if declareErr != nil {
			return nil, declareErr
		}
		ids = append(ids, record.ID)
	}
	return evidenceToolsWithArtifactRegistry(registry, workspace, artifacts, write, artifactsRegistry, ids)
}

func evidenceToolsWithArtifactRegistry(
	registry *snapshot.Registry,
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
	artifactsRegistry *artifactpkg.Registry,
	artifactIDs []string,
) ([]sdk.Tool, error) {
	snapshots, err := evidence.NewSnapshotAccessWithRegistry(registry)
	if err != nil {
		return nil, err
	}
	return evidenceToolsWithAccess(snapshots, workspace, artifacts, write, artifactsRegistry, artifactIDs)
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
	artifactsRegistry := artifactpkg.NewRegistry()
	ids := make([]string, 0, len(artifacts))
	for _, declared := range artifacts {
		record, declareErr := artifactsRegistry.Declare(workspace, declared)
		if declareErr != nil {
			return nil, nil, declareErr
		}
		ids = append(ids, record.ID)
	}
	tools, err := evidenceToolsWithAccess(snapshots, workspace, artifacts, write, artifactsRegistry, ids)
	tools = slices.DeleteFunc(tools, func(tool sdk.Tool) bool {
		return tool.Name == "r42_list_snapshots"
	})
	return tools, snapshots, err
}

func evidenceToolsWithUpstreamAndArtifacts(
	registry *snapshot.Registry,
	upstream map[string]string,
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
	artifactsRegistry *artifactpkg.Registry,
	artifactIDs []string,
) ([]sdk.Tool, *evidence.SnapshotAccess, error) {
	snapshots, err := evidence.NewSnapshotAccessWithRegistryAndUpstream(registry, upstream)
	if err != nil {
		return nil, nil, err
	}
	tools, err := evidenceToolsWithAccess(
		snapshots, workspace, artifacts, write, artifactsRegistry, artifactIDs,
	)
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
	artifactsRegistry *artifactpkg.Registry,
	artifactIDs []string,
) ([]sdk.Tool, error) {
	if artifactsRegistry == nil {
		return nil, errors.New("artifact registry is required")
	}
	writer, err := evidence.NewMarkdownWriter(workspace)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]researchspec.Artifact, len(artifacts))
	for _, artifact := range artifacts {
		declared[artifact.Name] = artifact
	}
	authorizedArtifacts := make(map[string]struct{}, len(artifactIDs))
	for _, id := range artifactIDs {
		authorizedArtifacts[id] = struct{}{}
	}
	listedArtifacts := func() ([]artifactpkg.Record, error) {
		fixed, listErr := artifactsRegistry.Records(artifactIDs)
		if listErr != nil {
			return nil, listErr
		}
		seen := maps.Clone(authorizedArtifacts)
		for _, record := range artifactsRegistry.ReadyRecords() {
			if _, exists := seen[record.ID]; exists {
				continue
			}
			fixed = append(fixed, record)
			seen[record.ID] = struct{}{}
		}
		return fixed, nil
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
			Name: "r42_read_snapshot", Description: "Read a bounded page and its source identifier from a registered snapshot ID. " +
				"Use offset_bytes from 0 and continue with next_offset_bytes while truncated is true.",
			Parameters: objectSchema(map[string]any{
				"id":           map[string]any{"type": "string"},
				"offset_bytes": map[string]any{"type": "integer", "minimum": 0, "default": 0},
				"max_bytes":    map[string]any{"type": "integer", "minimum": 1},
			}, []string{"id", "max_bytes"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[boundedReadArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				page, readErr := snapshots.ReadSnapshotPage(args.ID, args.OffsetBytes, args.MaxBytes)
				if readErr != nil {
					return rejectedToolResult("snapshot_read_failed", readErr.Error())
				}
				source, readErr := snapshots.SnapshotSource(args.ID)
				if readErr != nil {
					return rejectedToolResult("snapshot_read_failed", readErr.Error())
				}
				return acceptedToolResult(snapshotReadOutput{
					Content: page.Content, Source: source, OffsetBytes: page.OffsetBytes,
					NextOffsetBytes: page.NextOffsetBytes, TotalBytes: page.TotalBytes, Truncated: page.Truncated,
				})
			},
		},
		{
			Name: "r42_search_snapshot", Description: "Search a registered snapshot by snapshot_id using a Go RE2 regular expression. " +
				"Search runs over Unicode-whitespace-normalized text and returns exact matched_text that can be reused as exact_quote, plus bounded source context. " +
				"Each match is limited to 2000 Unicode characters and each excerpt to 4000; snapshots larger than 32 MiB must be inspected with paged r42_read_snapshot calls.",
			Parameters: objectSchema(map[string]any{
				"snapshot_id":    map[string]any{"type": "string", "description": "Registered snapshot ID; filesystem paths are not accepted"},
				"pattern":        map[string]any{"type": "string", "description": "Go RE2 regular expression applied after Unicode whitespace normalization"},
				"case_sensitive": map[string]any{"type": "boolean", "default": false},
				"max_matches":    map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 10},
				"context_lines":  map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "default": 2},
			}, []string{"snapshot_id", "pattern"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[snapshotSearchArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				if args.MaxMatches == 0 {
					args.MaxMatches = 10
				}
				rawArguments, _ := invocation.Arguments.(map[string]any)
				if _, supplied := rawArguments["context_lines"]; !supplied {
					args.ContextLines = 2
				}
				result, searchErr := snapshots.SearchSnapshot(
					args.SnapshotID,
					args.Pattern,
					args.CaseSensitive,
					args.MaxMatches,
					args.ContextLines,
				)
				if searchErr != nil {
					return rejectedToolResult("snapshot_search_failed", searchErr.Error())
				}
				return acceptedToolResult(result)
			},
		},
		{
			Name: "r42_list_artifacts", Description: "List run-scoped artifacts authorized for the current research block or dynamic task by ID",
			Parameters: objectSchema(map[string]any{}, nil),
			Handler: func(sdk.ToolInvocation) (sdk.ToolResult, error) {
				items, listErr := listedArtifacts()
				if listErr != nil {
					return sdk.ToolResult{}, listErr
				}
				return acceptedToolResult(items)
			},
		},
		{
			Name: "r42_read_artifact", Description: "Read a bounded page from an authorized run-scoped artifact by ID. " +
				"Use offset_bytes=0 for the first page, then continue with next_offset_bytes while truncated is true. " +
				"If the artifact ID is uncertain, call r42_list_artifacts to list valid IDs for the current block or dynamic task.",
			Parameters: objectSchema(map[string]any{
				"id":           map[string]any{"type": "string", "description": "Authorized run-scoped artifact ID"},
				"offset_bytes": map[string]any{"type": "integer", "minimum": 0, "default": 0},
				"max_bytes":    map[string]any{"type": "integer", "minimum": 1},
			}, []string{"id", "max_bytes"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactReadArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				if _, ok := authorizedArtifacts[args.ID]; !ok {
					record, recordErr := artifactsRegistry.Record(args.ID)
					if recordErr != nil || !record.Ready {
						return rejectedToolResult("unknown_artifact", fmt.Sprintf("unknown artifact %q", args.ID))
					}
				}
				page, readErr := artifactsRegistry.ReadPage(args.ID, args.OffsetBytes, args.MaxBytes)
				if readErr != nil {
					return rejectedToolResult("artifact_read_failed", readErr.Error())
				}
				return acceptedToolResult(page)
			},
		},
		{
			Name:        "r42_read_artifact_json_schema",
			Description: "Read the inferred JSON shape of an authorized artifact. The artifact must contain one complete JSON document.",
			Parameters: objectSchema(map[string]any{
				"id": map[string]any{"type": "string", "description": "Authorized run-scoped artifact ID"},
			}, []string{"id"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactJSONArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				value, readErr := readArtifactJSONValue(artifactsRegistry, authorizedArtifacts, args.ID)
				if readErr != nil {
					return rejectedToolResult("artifact_json_read_failed", readErr.Error())
				}
				return acceptedToolResult(inferJSONSchema(value))
			},
		},
		{
			Name:        "r42_query_artifact_json",
			Description: "Run a read-only jq query against an authorized JSON artifact. The query must be jq syntax such as .claims[0].id or .claims[].",
			Parameters: objectSchema(map[string]any{
				"id":    map[string]any{"type": "string", "description": "Authorized run-scoped artifact ID"},
				"query": map[string]any{"type": "string", "description": "jq query expression"},
			}, []string{"id", "query"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactJSONQueryArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				value, readErr := readArtifactJSONValue(artifactsRegistry, authorizedArtifacts, args.ID)
				if readErr != nil {
					return rejectedToolResult("artifact_json_read_failed", readErr.Error())
				}
				query, parseErr := gojq.Parse(args.Query)
				if parseErr != nil {
					return rejectedToolResult("invalid_jq", parseErr.Error())
				}
				iterator := query.Run(value)
				results := make([]any, 0)
				for {
					item, ok := iterator.Next()
					if !ok {
						break
					}
					if queryErr, ok := item.(error); ok {
						return rejectedToolResult("jq_failed", queryErr.Error())
					}
					results = append(results, item)
				}
				return acceptedToolResult(map[string]any{"results": results, "count": len(results)})
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
	terminalToolName string,
	terminal *researchruntime.TerminalRecorder,
) []sdk.Tool {
	result := slices.Clone(tools)
	for index := range result {
		original := result[index].Handler
		toolName := result[index].Name
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			invalidIDs := invalidSnapshotIDs(invocation.Arguments)
			if len(invalidIDs) > 0 {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal,
					"invalid_snapshot_id",
					"snapshot_id must use snapshot- plus 32 lowercase hexadecimal characters, not a filesystem path: "+
						strings.Join(invalidIDs, ", "),
				)
			}
			foreignPaths := foreignSnapshotPaths(invocation.Arguments, workspace)
			if len(foreignPaths) > 0 {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal,
					"snapshot_path_not_allowed",
					"use snapshot_id for cross-block evidence references; paths are outside this research task workspace: "+
						strings.Join(foreignPaths, ", "),
				)
			}
			unknown := unknownSnapshotIDs(invocation.Arguments, access)
			if len(unknown) > 0 {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal,
					"unknown_snapshot_id",
					"snapshot IDs are not authorized for this research task: "+strings.Join(unknown, ", "),
				)
			}
			invalidQuotes, validationErr := invalidSnapshotQuotes(invocation.Arguments, access)
			if validationErr != nil {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal, "snapshot_read_failed", validationErr.Error())
			}
			if len(invalidQuotes) > 0 {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal,
					"snapshot_quote_not_found",
					"exact_quote is not present in its referenced snapshot_id after whitespace normalization: "+
						strings.Join(invalidQuotes, ", "),
				)
			}
			invalidSources, validationErr := invalidSnapshotSources(invocation.Arguments, access)
			if validationErr != nil {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal, "snapshot_read_failed", validationErr.Error())
			}
			if len(invalidSources) > 0 {
				return rejectedSnapshotReferenceResult(toolName, terminalToolName, terminal,
					"snapshot_source_mismatch",
					"source must match the Source header recorded in its referenced snapshot_id: "+
						strings.Join(invalidSources, ", "),
				)
			}
			return original(invocation)
		}
	}
	return result
}

func rejectedSnapshotReferenceResult(
	toolName, terminalToolName string,
	terminal *researchruntime.TerminalRecorder,
	code, message string,
) (sdk.ToolResult, error) {
	response := corespec.ToolResponse[string]{Issues: []corespec.Issue{{Code: code, Message: message}}}
	if terminal != nil && toolName == terminalToolName {
		if err := terminal.Record(response); err != nil {
			return sdk.ToolResult{}, err
		}
	}
	return responseToolResult(response)
}

type snapshotSourceReference struct {
	id     string
	source string
}

func invalidSnapshotSources(arguments any, access *evidence.SnapshotAccess) ([]string, error) {
	references := make([]snapshotSourceReference, 0)
	collectSnapshotSources(arguments, &references)
	invalid := make([]string, 0)
	for _, reference := range references {
		expected, err := access.SnapshotSource(reference.id)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(reference.source) != strings.TrimSpace(expected) {
			invalid = append(invalid, reference.id)
		}
	}
	return invalid, nil
}

func collectSnapshotSources(value any, result *[]snapshotSourceReference) {
	switch typed := value.(type) {
	case map[string]any:
		id, hasID := typed["snapshot_id"].(string)
		if hasID && strings.TrimSpace(id) != "" {
			for _, field := range []string{"source", "url", "source_url"} {
				if source, ok := typed[field].(string); ok && strings.TrimSpace(source) != "" {
					*result = append(*result, snapshotSourceReference{id: id, source: source})
				}
			}
		}
		for _, nested := range typed {
			collectSnapshotSources(nested, result)
		}
	case []any:
		for _, nested := range typed {
			collectSnapshotSources(nested, result)
		}
	}
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
	id            string
	quote         string
	recordID      string
	recordIDLabel string
	field         string
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
			detail := fmt.Sprintf(
				"%s=%s snapshot_id=%s field=%s",
				reference.recordIDLabel,
				reference.recordID,
				reference.id,
				reference.field,
			)
			if nearby := nearbySnapshotText(access, reference); nearby != "" {
				detail += fmt.Sprintf(" nearby_text=%q", nearby)
			}
			invalid = append(invalid, detail)
		}
	}
	return invalid, nil
}

func collectSnapshotQuotes(value any, result *[]snapshotQuoteReference) {
	collectSnapshotQuotesAt(value, "", "record_id", result)
}

func collectSnapshotQuotesAt(value any, path, recordIDLabel string, result *[]snapshotQuoteReference) {
	switch typed := value.(type) {
	case map[string]any:
		id, hasID := typed["snapshot_id"].(string)
		quote, hasQuote := typed["exact_quote"].(string)
		if hasID && hasQuote && strings.TrimSpace(id) != "" && strings.TrimSpace(quote) != "" {
			recordID, _ := typed["id"].(string)
			field := "exact_quote"
			if path != "" {
				field = path + ".exact_quote"
			}
			*result = append(*result, snapshotQuoteReference{
				id: id, quote: quote, recordID: recordID, recordIDLabel: recordIDLabel, field: field,
			})
		}
		for key, nested := range typed {
			label := recordIDLabel
			switch key {
			case "cards", "claims":
				label = "claim_id"
			case "quotes":
				label = "quote_id"
			}
			collectSnapshotQuotesAt(nested, appendJSONPath(path, key), label, result)
		}
	case []any:
		for index, nested := range typed {
			collectSnapshotQuotesAt(nested, fmt.Sprintf("%s[%d]", path, index), recordIDLabel, result)
		}
	}
}

func appendJSONPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

func nearbySnapshotText(access *evidence.SnapshotAccess, reference snapshotQuoteReference) string {
	words := strings.Fields(reference.quote)
	patterns := make([]string, 0, min(32, len(words)))
	for start := 0; start+3 <= len(words) && len(patterns) < 32; start++ {
		patterns = append(patterns, regexp.QuoteMeta(strings.Join(words[start:start+3], " ")))
	}
	result := evidence.SnapshotSearchResult{}
	if len(patterns) > 0 {
		result, _ = access.SearchSnapshot(reference.id, strings.Join(patterns, "|"), false, 1, 1)
	}
	if len(result.Matches) == 0 {
		patterns = patterns[:0]
		seen := make(map[string]struct{}, min(32, len(words)))
		for _, word := range words {
			normalized := strings.ToLower(word)
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			patterns = append(patterns, regexp.QuoteMeta(word))
			if len(patterns) == 32 {
				break
			}
		}
		if len(patterns) > 0 {
			result, _ = access.SearchSnapshot(reference.id, strings.Join(patterns, "|"), false, 1, 1)
		}
	}
	if len(result.Matches) == 0 {
		return ""
	}
	nearby := strings.Join(strings.Fields(result.Matches[0].Excerpt), " ")
	return boundedNearbyText(nearby, result.Matches[0].MatchedText, 300)
}

func boundedNearbyText(text, matched string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	matchedRunes := []rune(matched)
	matchStart := max(0, strings.Index(text, matched))
	runeStart := utf8.RuneCountInString(text[:matchStart])
	start := max(0, runeStart-(maxRunes-len(matchedRunes))/2)
	if start+maxRunes > len(runes) {
		start = len(runes) - maxRunes
	}
	return string(runes[start : start+maxRunes])
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
	ID          string `json:"id"`
	OffsetBytes int    `json:"offset_bytes"`
	MaxBytes    int    `json:"max_bytes"`
}
type snapshotReadOutput struct {
	Content         string `json:"content"`
	Source          string `json:"source"`
	OffsetBytes     int    `json:"offset_bytes"`
	NextOffsetBytes int    `json:"next_offset_bytes"`
	TotalBytes      int    `json:"total_bytes"`
	Truncated       bool   `json:"truncated"`
}
type snapshotSearchArgs struct {
	SnapshotID    string `json:"snapshot_id"`
	Pattern       string `json:"pattern"`
	CaseSensitive bool   `json:"case_sensitive"`
	MaxMatches    int    `json:"max_matches"`
	ContextLines  int    `json:"context_lines"`
}
type artifactReadArgs struct {
	ID          string `json:"id"`
	OffsetBytes int    `json:"offset_bytes"`
	MaxBytes    int    `json:"max_bytes"`
}
type artifactJSONArgs struct {
	ID string `json:"id"`
}
type artifactJSONQueryArgs struct {
	ID    string `json:"id"`
	Query string `json:"query"`
}
type markdownWriteArgs struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

const maxJSONArtifactBytes = 4 * 1024 * 1024

func readArtifactJSONValue(
	registry *artifactpkg.Registry,
	authorized map[string]struct{},
	id string,
) (any, error) {
	if registry == nil {
		return nil, errors.New("artifact registry is required")
	}
	if _, ok := authorized[id]; !ok {
		record, err := registry.Record(id)
		if err != nil || !record.Ready {
			return nil, fmt.Errorf("unknown artifact %q", id)
		}
	}
	page, err := registry.ReadPage(id, 0, maxJSONArtifactBytes)
	if err != nil {
		return nil, err
	}
	if page.Truncated {
		return nil, fmt.Errorf("JSON artifact %q exceeds %d bytes", id, maxJSONArtifactBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(page.Content))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON artifact %q: %w", id, err)
	}
	return value, nil
}

func inferJSONSchema(value any) map[string]any {
	switch value := value.(type) {
	case nil:
		return map[string]any{"type": "null"}
	case map[string]any:
		properties := make(map[string]any, len(value))
		for key, item := range value {
			properties[key] = inferJSONSchema(item)
		}
		return map[string]any{"type": "object", "properties": properties}
	case []any:
		result := map[string]any{"type": "array"}
		if len(value) > 0 {
			result["items"] = inferJSONSchema(value[0])
		}
		return result
	case string:
		return map[string]any{"type": "string"}
	case bool:
		return map[string]any{"type": "boolean"}
	case json.Number:
		if strings.ContainsAny(string(value), ".eE") {
			return map[string]any{"type": "number"}
		}
		return map[string]any{"type": "integer"}
	default:
		return map[string]any{"type": "number"}
	}
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
