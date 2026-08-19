package cli

import (
	"encoding/json"
	"fmt"
	"slices"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/snapshot"
	corespec "github.com/lonegunmanb/r42/internal/spec"
)

var closedWorldBuiltIns = []string{
	"web_search", "web_fetch", "bash", "powershell", "shell", "view", "edit", "task", "ask_user",
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
			Name: "r42_collection_checkpoint", Description: "Submit all unreviewed snapshots for Collection QC",
			Parameters: objectSchema(map[string]any{"empty_reason": map[string]any{"type": "string"}}, nil),
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
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
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
