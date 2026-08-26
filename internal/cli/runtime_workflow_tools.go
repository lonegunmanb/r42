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
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/itchyny/gojq"
	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/evidence"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

var readOnlyFileBuiltIns = []string{"view", "grep", "head", "tail"}

func collectionDisallowedTools(configured, allowed []string) []string {
	result := slices.Clone(configured)
	for _, name := range researchspec.CollectionBlockedBuiltinTools() {
		if slices.Contains(allowed, name) {
			continue
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func collectionAllowedTools(configured, mandatory []string) []string {
	if configured == nil {
		return nil
	}
	return phaseAllowedTools(configured, append(slices.Clone(readOnlyFileBuiltIns), mandatory...))
}

func closedWorldDisallowedTools(configured, allowed []string) []string {
	result := slices.Clone(configured)
	for _, name := range researchspec.ClosedWorldBuiltinTools() {
		if slices.Contains(allowed, name) {
			continue
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func finalQCDisallowedTools(effective researchspec.EffectiveQC, explicitlyConfigured bool, allowed []string) []string {
	configured := []string(nil)
	if explicitlyConfigured {
		configured = effective.DisallowedTools
	}
	return closedWorldDisallowedTools(configured, allowed)
}

func closedWorldAllowedTools(configured, mandatory []string) []string {
	result := slices.Clone(configured)
	for _, name := range readOnlyFileBuiltIns {
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return phaseAllowedTools(result, mandatory)
}

func collectionBuiltInHooks(quota *toolCallQuota, collectionContext *collection.Context) *sdk.SessionHooks {
	var leaseMu sync.Mutex
	leases := make(map[string][]func())
	releaseLease := func(toolName string) {
		leaseMu.Lock()
		defer leaseMu.Unlock()
		toolLeases := leases[toolName]
		if len(toolLeases) == 0 {
			return
		}
		toolLeases[0]()
		if len(toolLeases) == 1 {
			delete(leases, toolName)
			return
		}
		leases[toolName] = toolLeases[1:]
	}
	return &sdk.SessionHooks{
		OnPreToolUse: func(input sdk.PreToolUseHookInput, _ sdk.HookInvocation) (*sdk.PreToolUseHookOutput, error) {
			if (input.ToolName == "web_search" || input.ToolName == "web_fetch") && collectionContext != nil {
				release, denialReason := beginCollectionAcquisition(collectionContext)
				if denialReason != "" {
					return &sdk.PreToolUseHookOutput{
						PermissionDecision:       "deny",
						PermissionDecisionReason: denialReason,
					}, nil
				}
				decision := toolCallQuotaDecision(quota, input.ToolName)
				if decision.PermissionDecision != "allow" {
					release()
					return decision, nil
				}
				leaseMu.Lock()
				leases[input.ToolName] = append(leases[input.ToolName], release)
				leaseMu.Unlock()
				return decision, nil
			}
			return toolCallQuotaDecision(quota, input.ToolName), nil
		},
		OnPostToolUse: func(input sdk.PostToolUseHookInput, _ sdk.HookInvocation) (*sdk.PostToolUseHookOutput, error) {
			releaseLease(input.ToolName)
			return &sdk.PostToolUseHookOutput{}, nil
		},
		OnPostToolUseFailure: func(
			input sdk.PostToolUseFailureHookInput,
			_ sdk.HookInvocation,
		) (*sdk.PostToolUseFailureHookOutput, error) {
			releaseLease(input.ToolName)
			quota.rollback(input.ToolName)
			return &sdk.PostToolUseFailureHookOutput{}, nil
		},
	}
}

func beginCollectionAcquisition(collectionContext *collection.Context) (func(), string) {
	release, err := collectionContext.BeginCollectionToolCall()
	if err != nil {
		return nil, err.Error()
	}
	if err = collectionContext.Gate().Acquire(); err != nil {
		release()
		return nil, err.Error()
	}
	return release, ""
}

func wrapCollectionMutationTools(tools []sdk.Tool, collectionContext *collection.Context) []sdk.Tool {
	result := slices.Clone(tools)
	for index := range result {
		if result[index].Name != "r42_write_markdown" {
			continue
		}
		original := result[index].Handler
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			release, err := collectionContext.BeginCollectionToolCall()
			if err != nil {
				return rejectedToolResult("collection_tool_gate", err.Error())
			}
			defer release()
			return original(invocation)
		}
	}
	return result
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
			release, err := context.BeginCollectionToolCall()
			if err != nil {
				return rejectedToolResult("collection_tool_gate", err.Error())
			}
			defer release()
			if err := context.Gate().Acquire(); err != nil {
				return rejectedToolResult("checkpoint_pending", err.Error())
			}
			toolResult, err := original(invocation)
			if err != nil {
				return toolResult, err
			}
			if invocation.ToolCallID != "" && toolResult.ResultType == "success" && toolResult.TextResultForLLM != "" {
				if err = context.Artifacts.RetainToolResult(invocation.ToolCallID, toolResult.TextResultForLLM); err != nil {
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
	informationNeeds := collection.NewInformationNeedsHandler(context)
	return []sdk.Tool{
		{
			Name: "r42_set_information_needs", Description: "Before any collection or artifact-writing tool call, freeze this Collection task's complete search plan. " +
				"List every information need and its objective stop conditions. R42 assigns canonical IDs. This successful call is permanent: later rounds may not add, edit, rename, delete, or split needs or conditions.",
			Parameters: objectSchema(map[string]any{
				"information_needs": map[string]any{"type": "array", "items": objectSchema(map[string]any{
					"question": map[string]any{"type": "string", "description": "The fixed question Collection will investigate"},
					"stop_conditions": map[string]any{"type": "array", "items": objectSchema(map[string]any{
						"condition": map[string]any{"type": "string", "description": "Evidence condition that makes this question sufficient"},
					}, []string{"condition"})},
				}, []string{"question", "stop_conditions"})},
			}, []string{"information_needs"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, err := decodeArguments[collection.InformationNeedsArgs](invocation.Arguments)
				if err != nil {
					return rejectedToolResult("invalid_arguments", err.Error())
				}
				return responseToolResult(informationNeeds.Set(args))
			},
		},
		{
			Name: "r42_register_artifact", Description: "Register an existing workspace evidence artifact path or retained source tool call result. " +
				"Optional source may be a URL or any other source identifier; when supplied, it is added as a compatible Source header only if the artifact has no non-empty Source or legacy URL header. " +
				"Do not call this after r42_save_artifact because that tool already registers its saved artifact.",
			Parameters: objectSchema(map[string]any{
				"path":                map[string]any{"type": "string"},
				"source_tool_call_id": map[string]any{"type": "string"},
				"source":              map[string]any{"type": "string", "description": "Optional source identifier; may be a URL or a non-URL value"},
				"description":         map[string]any{"type": "string", "description": "Optional concise semantic summary of what this evidence artifact contains"},
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
			Name: "r42_collection_checkpoint", Description: "Exactly once in each Collection round, submit all unreviewed evidence artifacts and one continue or stalled disposition for every active information need. " +
				"This is the final valid tool call of this round; after acceptance Collection QC starts. stalled means you made a genuine search effort for that need and found no productive next search action.",
			Parameters: objectSchema(map[string]any{
				"empty_reason": map[string]any{"type": "string", "description": "Required only when this round added no evidence artifacts"},
				"need_dispositions": map[string]any{"type": "array", "items": objectSchema(map[string]any{
					"information_need_id": map[string]any{"type": "string"},
					"search_disposition":  map[string]any{"type": "string", "enum": []string{"continue", "stalled"}},
				}, []string{"information_need_id", "search_disposition"})},
			}, []string{"need_dispositions"}),
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
		collectionSaveArtifactTool(context),
	}
}

// collectionOnlyArtifactTools provides explicit persistence without importing
// the information-needs and checkpoint state machines from full workflows.
func collectionOnlyArtifactTools(workspace string, registry *artifactpkg.Registry, targets []artifactpkg.Record) []sdk.Tool {
	allowsPath := func(raw string) (string, bool) {
		path := filepath.Clean(strings.TrimSpace(raw))
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		absolute, err := filepath.Abs(path)
		if err != nil || !strings.HasSuffix(strings.ToLower(absolute), ".md") {
			return "", false
		}
		for _, target := range targets {
			if target.Type == researchspec.ArtifactTypeDirectory && artifactPathWithin(target.Path, absolute) {
				return absolute, true
			}
			if target.Type == researchspec.ArtifactTypeFile && filepath.Clean(target.Path) == filepath.Clean(absolute) {
				return absolute, true
			}
		}
		return "", false
	}
	register := func(args collection.RegisterArgs) corespec.ToolResponse[collection.RegistrationOutput] {
		hasPath := strings.TrimSpace(args.Path) != ""
		hasRetained := strings.TrimSpace(args.SourceToolCallID) != ""
		if hasPath == hasRetained {
			return corespec.ToolResponse[collection.RegistrationOutput]{Issues: []corespec.Issue{{
				Code: "exactly_one_source", Message: "provide exactly one of path or source_tool_call_id",
			}}}
		}
		var (
			record artifactpkg.Record
			err    error
		)
		if hasPath {
			record, _, err = registry.RegisterEvidence(workspace, args.Path, args.Source, args.Description)
		} else {
			record, _, err = registry.RegisterRetainedEvidence(workspace, args.SourceToolCallID, args.Source, args.Description)
		}
		if err != nil {
			return corespec.ToolResponse[collection.RegistrationOutput]{Issues: []corespec.Issue{{
				Code: "invalid_evidence_artifact", Message: err.Error(),
			}}}
		}
		return corespec.ToolResponse[collection.RegistrationOutput]{Accepted: true, Output: &collection.RegistrationOutput{
			ID: record.ID, Path: record.Path, Description: record.Description,
		}}
	}
	return []sdk.Tool{
		{
			Name: "r42_register_artifact", Description: "Register existing workspace source material or a retained acquisition result as an evidence artifact. " +
				"Provide exactly one of path or source_tool_call_id. No checkpoint is required.",
			Parameters: objectSchema(map[string]any{
				"path":                map[string]any{"type": "string"},
				"source_tool_call_id": map[string]any{"type": "string"},
				"source":              map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
			}, nil),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, err := decodeArguments[collection.RegisterArgs](invocation.Arguments)
				if err != nil {
					return rejectedToolResult("invalid_arguments", err.Error())
				}
				return responseToolResult(register(args))
			},
		},
		{
			Name: "r42_save_artifact", Description: "Save and register complete source material as Markdown at a declared artifact target. " +
				"A source identifier is required; no checkpoint is required.",
			Parameters: objectSchema(map[string]any{
				"artifact_path": map[string]any{"type": "string"},
				"content":       map[string]any{"type": "string"},
				"source":        map[string]any{"type": "string"},
				"description":   map[string]any{"type": "string"},
			}, []string{"artifact_path", "content", "source"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, err := decodeArguments[saveArtifactArgs](invocation.Arguments)
				if err != nil {
					return rejectedToolResult("invalid_arguments", err.Error())
				}
				path, allowed := allowsPath(args.ArtifactPath)
				if !allowed {
					return rejectedToolResult("artifact_path", "artifact_path must be a .md path at a declared artifact target")
				}
				source := strings.Join(strings.Fields(args.Source), " ")
				if source == "" || strings.TrimSpace(args.Content) == "" {
					return rejectedToolResult("invalid_artifact", "content and source must not be empty")
				}
				writer, err := evidence.NewMarkdownWriter(workspace)
				if err != nil {
					return sdk.ToolResult{}, err
				}
				written, err := writer.WriteNew(path, "- Source: "+source+"\n\n"+args.Content)
				if err != nil {
					if errors.Is(err, os.ErrExist) {
						return rejectedToolResult("artifact_write_failed", "artifact_path already exists; use a new path")
					}
					return rejectedToolResult("artifact_write_failed", err.Error())
				}
				response := register(collection.RegisterArgs{Path: written, Description: args.Description})
				if !response.Accepted {
					return responseToolResult(corespec.ToolResponse[saveArtifactOutput]{Issues: response.Issues})
				}
				return acceptedToolResult(saveArtifactOutput{Path: response.Output.Path, ArtifactID: response.Output.ID})
			},
		},
	}
}

func artifactPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func retainCollectionToolResults(tools []sdk.Tool, registry *artifactpkg.Registry, allowed map[string]struct{}) []sdk.Tool {
	result := slices.Clone(tools)
	for index := range result {
		if _, retain := allowed[result[index].Name]; !retain {
			continue
		}
		original := result[index].Handler
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			toolResult, err := original(invocation)
			if err != nil || invocation.ToolCallID == "" || toolResult.ResultType != "success" || toolResult.TextResultForLLM == "" {
				return toolResult, err
			}
			if err = registry.RetainToolResult(invocation.ToolCallID, toolResult.TextResultForLLM); err != nil {
				return sdk.ToolResult{}, err
			}
			return toolResult, nil
		}
	}
	return result
}

type saveArtifactArgs struct {
	ArtifactPath string `json:"artifact_path"`
	Content      string `json:"content"`
	Source       string `json:"source"`
	Description  string `json:"description"`
}

type saveArtifactOutput struct {
	Path       string `json:"path"`
	ArtifactID string `json:"artifact_id"`
}

func collectionSaveArtifactTool(context *collection.Context) sdk.Tool {
	return sdk.Tool{
		Name: "r42_save_artifact",
		Description: "Save and register complete source material as Markdown at a declared evidence artifact file target or below a declared evidence artifact directory target, " +
			"then return path and artifact_id. source is required and may be a URL or any other source identifier; it is written to the artifact header. " +
			"Provide description when possible to summarize the artifact's semantic contents for downstream research planning. " +
			"After a successful call, use the returned artifact_id directly. Do not call r42_register_artifact for the returned path.",
		Parameters: objectSchema(map[string]any{
			"artifact_path": map[string]any{"type": "string", "description": "Absolute or workspace-relative .md path at a declared evidence artifact file target or below a declared evidence artifact directory target"},
			"content":       map[string]any{"type": "string", "description": "Complete source material in Markdown"},
			"source":        map[string]any{"type": "string", "description": "Non-empty source identifier; may be a URL or a non-URL value"},
			"description":   map[string]any{"type": "string", "description": "Optional concise semantic summary of what the saved source material contains"},
		}, []string{"artifact_path", "content", "source"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			args, err := decodeArguments[saveArtifactArgs](invocation.Arguments)
			if err != nil {
				return rejectedToolResult("invalid_arguments", err.Error())
			}
			return saveCollectionArtifact(context, args)
		},
	}
}

func saveCollectionArtifact(context *collection.Context, args saveArtifactArgs) (sdk.ToolResult, error) {
	release, err := context.BeginCollectionToolCall()
	if err != nil {
		return rejectedToolResult("collection_tool_gate", err.Error())
	}
	defer release()
	issues := make([]corespec.Issue, 0, 3)
	workspace := ""
	if context != nil {
		workspace = context.Workspace
	}
	path, validPath := collectionArtifactPath(context, args.ArtifactPath)
	if !validPath {
		issues = append(issues, corespec.Issue{
			Code: "artifact_path", Message: "artifact_path must be a .md path at a declared evidence artifact file target or below a declared evidence artifact directory target",
		})
	}
	source := strings.Join(strings.Fields(args.Source), " ")
	if source == "" {
		issues = append(issues, corespec.Issue{Code: "artifact_source", Message: "source must not be empty"})
	}
	content := args.Content
	if strings.TrimSpace(content) == "" {
		issues = append(issues, corespec.Issue{Code: "artifact_content", Message: "content must not be empty"})
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
			return rejectedToolResult("artifact_write_failed", "artifact_path already exists; use a new path")
		}
		return rejectedToolResult("artifact_write_failed", err.Error())
	}
	registration := collection.NewRegisterHandler(context).Register(collection.RegisterArgs{
		Path: written, Description: args.Description,
	})
	if !registration.Accepted {
		return responseToolResult(corespec.ToolResponse[saveArtifactOutput]{Issues: registration.Issues})
	}
	output := saveArtifactOutput{
		Path:       registration.Output.Path,
		ArtifactID: registration.Output.ID,
	}
	return acceptedToolResult(output)
}

func collectionArtifactPath(context *collection.Context, raw string) (string, bool) {
	if context == nil || strings.TrimSpace(context.Workspace) == "" || strings.TrimSpace(raw) == "" {
		return "", false
	}
	workspace := context.Workspace
	path := filepath.Clean(strings.TrimSpace(raw))
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	path, err := filepath.Abs(path)
	if err != nil || !strings.HasSuffix(strings.ToLower(path), ".md") {
		return "", false
	}
	return path, context.AllowsArtifactPath(path)
}

func collectionQCVerdictTool(collectionContext *collection.Context, verdicts *collectionqc.VerdictRecorder) sdk.Tool {
	return sdk.Tool{
		Name: "r42_collection_qc_verdict", Description: "Exactly once in each Collection QC round, assess every active information need against only its frozen stop-condition IDs. " +
			"sufficient lists no unsatisfied conditions; needs_more lists the remaining IDs. After the first QC round, remaining IDs must be a subset of the previous round; never introduce, reopen, rename, or restate conditions. evidence_progress is material only when this checkpoint materially improves the need.",
		Parameters: objectSchema(map[string]any{
			"assessments": map[string]any{"type": "array", "items": objectSchema(map[string]any{
				"information_need_id":       map[string]any{"type": "string"},
				"status":                    map[string]any{"type": "string", "enum": []string{"sufficient", "needs_more"}},
				"unsatisfied_condition_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"evidence_progress":         map[string]any{"type": "string", "enum": []string{"material", "none"}},
			}, []string{"information_need_id", "status", "unsatisfied_condition_ids", "evidence_progress"})},
		}, []string{"assessments"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			verdict, err := decodeArguments[collectionqc.Verdict](invocation.Arguments)
			if err != nil {
				return rejectedToolResult("invalid_arguments", err.Error())
			}
			if issues := collectionContext.ValidateQCAssessments(verdict.Assessments, collectionContext.LastRoundHadArtifacts()); len(issues) > 0 {
				return responseToolResult(corespec.ToolResponse[collectionqc.Verdict]{Issues: issues})
			}
			if err = verdicts.Record(verdict); err != nil {
				return rejectedToolResult("invalid_verdict", err.Error())
			}
			return acceptedToolResult(struct{}{})
		},
	}
}

func applyToolUseBindings(
	tools []sdk.Tool,
	toolUses []researchspec.ToolUse,
	artifactRegistries ...*artifactpkg.Registry,
) ([]sdk.Tool, error) {
	var artifactsRegistry *artifactpkg.Registry
	if len(artifactRegistries) > 0 {
		artifactsRegistry = artifactRegistries[0]
	}
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
			if description := describeToolUseSources(sources); description != "" {
				property["description"] = description
			}
			properties[field] = property
		}
		if sourceGuidance := groupedToolUseSourceGuidance(agent); sourceGuidance != "" {
			result[index].Description = strings.TrimSpace(result[index].Description + "\n\n" + sourceGuidance)
		}
		if hasModelSuppliedFields(properties, input, agent) {
			result[index].Description = strings.TrimSpace(result[index].Description +
				"\n\nFor fields without explicit source guidance, construct values from the current block's declared artifacts. " +
				"Call r42_list_artifacts first; read file artifacts with r42_read_artifact or JSON tools, and list directory artifacts with r42_list_artifact_files before reading child IDs.")
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
			if artifactsRegistry != nil {
				if err := materializeArtifactPaths(arguments, artifactsRegistry); err != nil {
					return rejectedToolResult("artifact_target", err.Error())
				}
			}
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

func hasModelSuppliedFields(properties, input map[string]any, agent map[string]cty.Value) bool {
	for name := range properties {
		if _, fixed := input[name]; fixed {
			continue
		}
		if _, guided := agent[name]; guided {
			continue
		}
		return true
	}
	return false
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

func toolUseFieldDescription(value cty.Value) string {
	unmarked, _ := value.UnmarkDeep()
	if unmarked.Type().IsObjectType() && unmarked.Type().HasAttribute("desc") && unmarked.Type().HasAttribute("sources") {
		if desc := unmarked.GetAttr("desc"); desc.IsKnown() && !desc.IsNull() && desc.Type().Equals(cty.String) {
			return strings.TrimSpace(desc.AsString())
		}
	}
	return ""
}

type toolUseSource struct {
	id           string
	kind         string
	artifactType string
	description  string
}

func (s toolUseSource) key() string {
	return strings.Join([]string{s.id, s.kind, s.artifactType, s.description}, "\x00")
}

func groupedToolUseSourceGuidance(agent map[string]cty.Value) string {
	type sourceGroup struct {
		fields  []string
		sources []toolUseSource
	}
	groups := map[string]*sourceGroup{}
	for field, value := range agent {
		sources := normalizedToolUseSources(value)
		keyParts := make([]string, len(sources))
		for index, source := range sources {
			keyParts[index] = source.key()
		}
		key := strings.Join(keyParts, "\x01")
		group := groups[key]
		if group == nil {
			group = &sourceGroup{fields: []string{}, sources: sources}
			groups[key] = group
		}
		group.fields = append(group.fields, field)
	}
	if len(groups) == 0 {
		return ""
	}
	ordered := make([]*sourceGroup, 0, len(groups))
	for _, group := range groups {
		sort.Strings(group.fields)
		ordered = append(ordered, group)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return strings.Join(ordered[left].fields, "\x00") < strings.Join(ordered[right].fields, "\x00")
	})

	var builder strings.Builder
	builder.WriteString("Agent-provided field sources:")
	for _, group := range ordered {
		builder.WriteString("\n- ")
		builder.WriteString(strings.Join(group.fields, ", "))
		builder.WriteString(": ")
		if len(group.sources) == 0 {
			builder.WriteString("No declared readable source. Construct these fields from task instructions or data returned directly by prior typed-tool calls in this session.")
			continue
		}
		instructions := make([]string, len(group.sources))
		for index, source := range group.sources {
			instructions[index] = describeToolUseSource(source)
		}
		builder.WriteString(strings.Join(instructions, " "))
	}
	return builder.String()
}

func normalizedToolUseSources(value cty.Value) []toolUseSource {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("sources") {
		return []toolUseSource{}
	}
	sources := unmarked.GetAttr("sources")
	if !sources.IsKnown() || sources.IsNull() || (!sources.Type().IsTupleType() && !sources.Type().IsListType()) {
		return []toolUseSource{}
	}
	seen := map[string]struct{}{}
	result := make([]toolUseSource, 0, len(sources.AsValueSlice()))
	for _, sourceValue := range sources.AsValueSlice() {
		if !sourceValue.IsKnown() || !sourceValue.Type().IsObjectType() {
			continue
		}
		id, kind, artifactType, description := toolUseSourceStrings(sourceValue)
		source := toolUseSource{id: id, kind: kind, artifactType: artifactType, description: description}
		if _, duplicate := seen[source.key()]; duplicate {
			continue
		}
		seen[source.key()] = struct{}{}
		result = append(result, source)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].key() < result[right].key() })
	return result
}

func describeToolUseSource(source toolUseSource) string {
	summary := source.description
	if summary == "" {
		summary = "no description"
	}
	if source.artifactType == "directory" {
		return fmt.Sprintf("Directory artifact %s (%s): call r42_list_artifact_files, then read returned child IDs with r42_read_artifact.", source.id, summary)
	}
	return fmt.Sprintf("File artifact %s (%s): use r42_read_artifact; for JSON, r42_read_artifact_json_schema or r42_query_artifact_json.", source.id, summary)
}

func describeToolUseSources(value cty.Value) string {
	if description := toolUseFieldDescription(value); description != "" {
		return description
	}
	unmarked, _ := value.UnmarkDeep()
	encoded, err := ctyjson.Marshal(unmarked, unmarked.Type())
	if err != nil {
		return ""
	}
	return "Construct this field using these authorized artifact sources: " + string(encoded) + "."
}

func toolUseSourceStrings(value cty.Value) (id, kind, artifactType, description string) {
	for _, field := range []struct {
		name   string
		target *string
	}{
		{name: "id", target: &id},
		{name: "kind", target: &kind},
		{name: "type", target: &artifactType},
		{name: "description", target: &description},
	} {
		if value.Type().HasAttribute(field.name) {
			item := value.GetAttr(field.name)
			if item.IsKnown() && !item.IsNull() && item.Type().Equals(cty.String) {
				*field.target = strings.TrimSpace(item.AsString())
			}
		}
	}
	return id, kind, artifactType, description
}

func evidenceToolsWithArtifactRegistry(
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
	artifactsRegistry *artifactpkg.Registry,
	artifactIDs []string,
	additionalIDs func() []string,
) ([]sdk.Tool, error) {
	return evidenceToolsWithAccess(workspace, artifacts, write, artifactsRegistry, artifactIDs, additionalIDs)
}

func evidenceToolsWithDynamicArtifacts(
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
	registry *artifactpkg.Registry,
	artifactIDs []string,
	additionalIDs func() []string,
) ([]sdk.Tool, error) {
	return evidenceToolsWithAccess(workspace, artifacts, write, registry, artifactIDs, additionalIDs)
}

func evidenceToolsWithAccess(
	workspace string,
	artifacts []researchspec.Artifact,
	write bool,
	artifactsRegistry *artifactpkg.Registry,
	artifactIDs []string,
	additionalIDs func() []string,
) ([]sdk.Tool, error) {
	if artifactsRegistry == nil {
		return nil, errors.New("artifact registry is required")
	}
	writer, err := evidence.NewMarkdownWriter(workspace)
	if err != nil {
		return nil, err
	}
	authorizedArtifacts := make(map[string]struct{}, len(artifactIDs))
	var authorizedArtifactsMu sync.RWMutex
	addAuthorizedArtifacts := func() {
		authorizedArtifactsMu.Lock()
		defer authorizedArtifactsMu.Unlock()
		for _, id := range artifactIDs {
			authorizedArtifacts[id] = struct{}{}
		}
		if additionalIDs != nil {
			for _, id := range additionalIDs() {
				authorizedArtifacts[id] = struct{}{}
			}
		}
	}
	addAuthorizedArtifacts()
	isAuthorizedArtifact := func(id string) bool {
		addAuthorizedArtifacts()
		authorizedArtifactsMu.RLock()
		if _, ok := authorizedArtifacts[id]; ok {
			authorizedArtifactsMu.RUnlock()
			return true
		}
		authorizedArtifactsMu.RUnlock()
		return false
	}
	listedArtifacts := func() ([]artifactpkg.Record, error) {
		addAuthorizedArtifacts()
		authorizedArtifactsMu.RLock()
		ids := make([]string, 0, len(authorizedArtifacts))
		for id := range authorizedArtifacts {
			ids = append(ids, id)
		}
		authorizedArtifactsMu.RUnlock()
		slices.Sort(ids)
		records, err := artifactsRegistry.Records(ids)
		if err != nil {
			return nil, err
		}
		return records, nil
	}

	tools := []sdk.Tool{
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
			Name: "r42_list_artifact_files", Description: "List regular files inside an authorized directory artifact by ID. " +
				"Use each returned child artifact ID with r42_read_artifact; paths are never accepted.",
			Parameters: objectSchema(map[string]any{
				"id": map[string]any{"type": "string", "description": "Authorized run-scoped directory artifact ID"},
			}, []string{"id"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactJSONArgs](invocation.Arguments)
				if decodeErr != nil {
					return rejectedToolResult("invalid_arguments", decodeErr.Error())
				}
				if !isAuthorizedArtifact(args.ID) {
					return rejectedToolResult("unknown_artifact", fmt.Sprintf("unknown artifact %q", args.ID))
				}
				files, listErr := artifactsRegistry.ListDirectoryFiles(args.ID)
				if listErr != nil {
					return rejectedToolResult("artifact_directory_read_failed", listErr.Error())
				}
				authorizedArtifactsMu.Lock()
				for _, file := range files {
					authorizedArtifacts[file.ID] = struct{}{}
				}
				authorizedArtifactsMu.Unlock()
				return acceptedToolResult(files)
			},
		},
		{
			Name: "r42_read_artifact", Description: "Read a bounded page from an authorized run-scoped artifact by ID. Evidence artifacts include their source. " +
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
				if !isAuthorizedArtifact(args.ID) {
					return rejectedToolResult("unknown_artifact", fmt.Sprintf("unknown artifact %q", args.ID))
				}
				page, readErr := artifactsRegistry.ReadPage(args.ID, args.OffsetBytes, args.MaxBytes)
				if readErr != nil {
					return rejectedToolResult("artifact_read_failed", readErr.Error())
				}
				record, recordErr := artifactsRegistry.Record(args.ID)
				if recordErr != nil {
					return rejectedToolResult("artifact_read_failed", recordErr.Error())
				}
				return acceptedToolResult(struct {
					artifactpkg.Page
					Source string `json:"source,omitempty"`
				}{Page: page, Source: record.Source})
			},
		},
		{
			Name: "r42_search_artifact", Description: "Search an authorized evidence artifact by ID using a Go RE2 regular expression. Search uses Unicode-whitespace-normalized text and returns reusable exact matched_text plus bounded context.",
			Parameters: objectSchema(map[string]any{
				"artifact_id":    map[string]any{"type": "string", "description": "Authorized evidence artifact ID; filesystem paths are not accepted"},
				"pattern":        map[string]any{"type": "string", "description": "Go RE2 regular expression applied after Unicode whitespace normalization"},
				"case_sensitive": map[string]any{"type": "boolean", "default": false},
				"max_matches":    map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 10},
				"context_lines":  map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "default": 2},
			}, []string{"artifact_id", "pattern"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactSearchArgs](invocation.Arguments)
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
				ids := slices.Clone(artifactIDs)
				if additionalIDs != nil {
					ids = append(ids, additionalIDs()...)
				}
				access, accessErr := evidence.NewArtifactEvidenceAccess(artifactsRegistry, ids)
				if accessErr != nil {
					return rejectedToolResult("artifact_search_failed", accessErr.Error())
				}
				result, searchErr := access.Search(args.ArtifactID, args.Pattern, args.CaseSensitive, args.MaxMatches, args.ContextLines)
				if searchErr != nil {
					return rejectedToolResult("artifact_search_failed", searchErr.Error())
				}
				return acceptedToolResult(result)
			},
		},
		{
			Name: "r42_search_artifacts", Description: "Search all authorized readable artifacts for the current research block or dynamic task, including imported artifacts and files within authorized directory artifacts. " +
				"Use a Go RE2 regular expression; every match includes its artifact_id for r42_read_artifact. Search uses Unicode-whitespace-normalized text and returns bounded context.",
			Parameters: objectSchema(map[string]any{
				"pattern":        map[string]any{"type": "string", "description": "Go RE2 regular expression applied after Unicode whitespace normalization"},
				"case_sensitive": map[string]any{"type": "boolean", "default": false},
				"max_matches":    map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 10},
				"context_lines":  map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "default": 2},
			}, []string{"pattern"}),
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				args, decodeErr := decodeArguments[artifactSearchArgs](invocation.Arguments)
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
				records, listErr := listedArtifacts()
				if listErr != nil {
					return rejectedToolResult("artifact_search_failed", listErr.Error())
				}
				result, discovered, searchErr := searchAuthorizedArtifacts(
					artifactsRegistry, records, args.Pattern, args.CaseSensitive, args.MaxMatches, args.ContextLines,
				)
				if searchErr != nil {
					return rejectedToolResult("artifact_search_failed", searchErr.Error())
				}
				authorizedArtifactsMu.Lock()
				for _, id := range discovered {
					authorizedArtifacts[id] = struct{}{}
				}
				authorizedArtifactsMu.Unlock()
				return acceptedToolResult(result)
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
				value, readErr := readArtifactJSONValue(artifactsRegistry, isAuthorizedArtifact, args.ID)
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
				value, readErr := readArtifactJSONValue(artifactsRegistry, isAuthorizedArtifact, args.ID)
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
		Name: "r42_write_markdown", Description: "Write a declared Markdown artifact by run-scoped artifact_id. Call r42_list_artifacts when the ID is uncertain; filesystem paths and artifact names are not accepted.",
		Parameters: objectSchema(map[string]any{
			"artifact_id": map[string]any{"type": "string", "description": "Declared file artifact ID from r42_list_artifacts"},
			"content":     map[string]any{"type": "string"},
		}, []string{"artifact_id", "content"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			args, decodeErr := decodeArguments[markdownWriteArgs](invocation.Arguments)
			if decodeErr != nil {
				return rejectedToolResult("invalid_arguments", decodeErr.Error())
			}
			if !isAuthorizedArtifact(args.ArtifactID) {
				return rejectedToolResult("unknown_artifact", fmt.Sprintf("unknown artifact %q", args.ArtifactID))
			}
			record, recordErr := artifactsRegistry.Record(args.ArtifactID)
			if recordErr != nil {
				return rejectedToolResult("artifact_write_failed", recordErr.Error())
			}
			if record.Purpose != artifactpkg.PurposeOutput || record.Type != researchspec.ArtifactTypeFile {
				return rejectedToolResult("invalid_artifact_type", "markdown writer requires a file artifact")
			}
			path, writeErr := writer.Write(record.Path, args.Content)
			if writeErr != nil {
				return rejectedToolResult("artifact_write_failed", writeErr.Error())
			}
			return acceptedToolResult(path)
		},
	})
	return tools, nil
}

type artifactSearchAllMatch struct {
	ArtifactID   string `json:"artifact_id"`
	ArtifactName string `json:"artifact_name,omitempty"`
	evidence.ArtifactSearchMatch
}

type artifactSearchAllResult struct {
	Matches             []artifactSearchAllMatch `json:"matches"`
	SearchedArtifactIDs []string                 `json:"searched_artifact_ids"`
	Truncated           bool                     `json:"truncated"`
}

func searchAuthorizedArtifacts(
	registry *artifactpkg.Registry,
	artifacts []artifactpkg.Record,
	pattern string,
	caseSensitive bool,
	maxMatches, contextLines int,
) (artifactSearchAllResult, []string, error) {
	result := artifactSearchAllResult{Matches: make([]artifactSearchAllMatch, 0)}
	if registry == nil {
		return result, nil, errors.New("artifact registry is required")
	}
	if maxMatches <= 0 {
		return result, nil, errors.New("maximum matches must be positive")
	}

	searchable := slices.Clone(artifacts)
	discovered := make([]string, 0)
	for index := 0; index < len(searchable); index++ {
		artifact := searchable[index]
		if artifact.Type != researchspec.ArtifactTypeDirectory {
			continue
		}
		files, err := registry.ListDirectoryFiles(artifact.ID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, discovered, err
		}
		for _, file := range files {
			discovered = append(discovered, file.ID)
			searchable = append(searchable, file)
		}
	}
	sort.Slice(searchable, func(i, j int) bool { return searchable[i].ID < searchable[j].ID })

	seen := make(map[string]struct{}, len(searchable))
	for _, artifact := range searchable {
		if artifact.Type != researchspec.ArtifactTypeFile {
			continue
		}
		if _, exists := seen[artifact.ID]; exists {
			continue
		}
		seen[artifact.ID] = struct{}{}
		remaining := maxMatches - len(result.Matches)
		if remaining == 0 {
			result.Truncated = true
			break
		}
		matches, err := evidence.SearchArtifact(registry, artifact.ID, pattern, caseSensitive, remaining, contextLines)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, discovered, err
		}
		result.SearchedArtifactIDs = append(result.SearchedArtifactIDs, artifact.ID)
		for _, match := range matches.Matches {
			result.Matches = append(result.Matches, artifactSearchAllMatch{
				ArtifactID: artifact.ID, ArtifactName: artifact.Name, ArtifactSearchMatch: match,
			})
		}
		if matches.Truncated {
			result.Truncated = true
			break
		}
	}
	return result, discovered, nil
}

func enforceArtifactIDReferences(
	tools []sdk.Tool,
	access func() (*evidence.ArtifactEvidenceAccess, error),
	workspace string,
	terminalToolName string,
	terminal *researchruntime.TerminalRecorder,
) []sdk.Tool {
	result := slices.Clone(tools)
	for index := range result {
		original := result[index].Handler
		toolName := result[index].Name
		result[index].Handler = func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			currentAccess, accessErr := access()
			if accessErr != nil {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal, "artifact_read_failed", accessErr.Error())
			}
			invalidIDs := invalidArtifactIDs(invocation.Arguments)
			if len(invalidIDs) > 0 {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal,
					"invalid_artifact_id",
					"artifact_id must use a registered artifact- ID, not a filesystem path: "+
						strings.Join(invalidIDs, ", "),
				)
			}
			foreignPaths := foreignArtifactPaths(invocation.Arguments, workspace)
			if len(foreignPaths) > 0 {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal,
					"artifact_path_not_allowed",
					"use artifact_id for cross-block evidence references; paths are outside this research task workspace: "+
						strings.Join(foreignPaths, ", "),
				)
			}
			unknown := unknownArtifactIDs(invocation.Arguments, currentAccess)
			if len(unknown) > 0 {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal,
					"unknown_artifact_id",
					"artifact IDs are not authorized for this research task: "+strings.Join(unknown, ", "),
				)
			}
			invalidQuotes, validationErr := invalidArtifactQuotes(invocation.Arguments, currentAccess)
			if validationErr != nil {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal, "artifact_read_failed", validationErr.Error())
			}
			if len(invalidQuotes) > 0 {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal,
					"artifact_quote_not_found",
					"exact_quote is not present in its referenced artifact_id after whitespace normalization: "+
						strings.Join(invalidQuotes, ", "),
				)
			}
			invalidSources, validationErr := invalidArtifactSources(invocation.Arguments, currentAccess)
			if validationErr != nil {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal, "artifact_read_failed", validationErr.Error())
			}
			if len(invalidSources) > 0 {
				return rejectedArtifactReferenceResult(toolName, terminalToolName, terminal,
					"artifact_source_mismatch",
					"source must match the Source header recorded in its referenced artifact_id: "+
						strings.Join(invalidSources, ", "),
				)
			}
			return original(invocation)
		}
	}
	return result
}

// bindResearchToolUses applies fixed HCL fields before checking artifact
// references, so model-supplied values cannot affect bound fields.
func bindResearchToolUses(
	tools []sdk.Tool,
	toolUses []researchspec.ToolUse,
	access func() (*evidence.ArtifactEvidenceAccess, error),
	artifactsRegistry *artifactpkg.Registry,
	workspace, terminalToolName string,
	terminal *researchruntime.TerminalRecorder,
) ([]sdk.Tool, error) {
	guarded := enforceArtifactIDReferences(tools, access, workspace, terminalToolName, terminal)
	return applyToolUseBindings(guarded, toolUses, artifactsRegistry)
}

func rejectedArtifactReferenceResult(
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

type artifactSourceReference struct {
	id     string
	source string
}

func invalidArtifactSources(arguments any, access *evidence.ArtifactEvidenceAccess) ([]string, error) {
	references := make([]artifactSourceReference, 0)
	collectArtifactSources(arguments, &references)
	invalid := make([]string, 0)
	for _, reference := range references {
		expected, err := access.Source(reference.id)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(reference.source) != strings.TrimSpace(expected) {
			invalid = append(invalid, reference.id)
		}
	}
	return invalid, nil
}

func collectArtifactSources(value any, result *[]artifactSourceReference) {
	switch typed := value.(type) {
	case map[string]any:
		id, hasID := typed["artifact_id"].(string)
		if hasID && strings.TrimSpace(id) != "" {
			for _, field := range []string{"source", "url", "source_url"} {
				if source, ok := typed[field].(string); ok && strings.TrimSpace(source) != "" {
					*result = append(*result, artifactSourceReference{id: id, source: source})
				}
			}
		}
		for _, nested := range typed {
			collectArtifactSources(nested, result)
		}
	case []any:
		for _, nested := range typed {
			collectArtifactSources(nested, result)
		}
	}
}

func invalidArtifactIDs(arguments any) []string {
	return nil
}

type artifactQuoteReference struct {
	id            string
	quote         string
	recordID      string
	recordIDLabel string
	field         string
}

func invalidArtifactQuotes(arguments any, access *evidence.ArtifactEvidenceAccess) ([]string, error) {
	references := make([]artifactQuoteReference, 0)
	collectArtifactQuotes(arguments, &references)
	invalid := make([]string, 0)
	for _, reference := range references {
		contains, err := access.ContainsNormalizedText(reference.id, reference.quote)
		if err != nil {
			return nil, err
		}
		if !contains {
			detail := fmt.Sprintf(
				"%s=%s artifact_id=%s field=%s",
				reference.recordIDLabel,
				reference.recordID,
				reference.id,
				reference.field,
			)
			if nearby := nearbyArtifactText(access, reference); nearby != "" {
				detail += fmt.Sprintf(" nearby_text=%q", nearby)
			}
			invalid = append(invalid, detail)
		}
	}
	return invalid, nil
}

func collectArtifactQuotes(value any, result *[]artifactQuoteReference) {
	collectArtifactQuotesAt(value, "", "record_id", result)
}

func collectArtifactQuotesAt(value any, path, recordIDLabel string, result *[]artifactQuoteReference) {
	switch typed := value.(type) {
	case map[string]any:
		id, hasID := typed["artifact_id"].(string)
		quote, hasQuote := typed["exact_quote"].(string)
		if hasID && hasQuote && strings.TrimSpace(id) != "" && strings.TrimSpace(quote) != "" {
			recordID, _ := typed["id"].(string)
			field := "exact_quote"
			if path != "" {
				field = path + ".exact_quote"
			}
			*result = append(*result, artifactQuoteReference{
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
			collectArtifactQuotesAt(nested, appendJSONPath(path, key), label, result)
		}
	case []any:
		for index, nested := range typed {
			collectArtifactQuotesAt(nested, fmt.Sprintf("%s[%d]", path, index), recordIDLabel, result)
		}
	}
}

func appendJSONPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

func nearbyArtifactText(access *evidence.ArtifactEvidenceAccess, reference artifactQuoteReference) string {
	words := strings.Fields(reference.quote)
	patterns := make([]string, 0, min(32, len(words)))
	for start := 0; start+3 <= len(words) && len(patterns) < 32; start++ {
		patterns = append(patterns, regexp.QuoteMeta(strings.Join(words[start:start+3], " ")))
	}
	result := evidence.ArtifactSearchResult{}
	if len(patterns) > 0 {
		result, _ = access.Search(reference.id, strings.Join(patterns, "|"), false, 1, 1)
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
			result, _ = access.Search(reference.id, strings.Join(patterns, "|"), false, 1, 1)
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

func unknownArtifactIDs(arguments any, access *evidence.ArtifactEvidenceAccess) []string {
	seen := map[string]struct{}{}
	collectArtifactIDs(arguments, seen)
	delete(seen, outputArtifactID(arguments))
	unknown := make([]string, 0, len(seen))
	for id := range seen {
		if !access.HasArtifact(id) {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	return unknown
}

func outputArtifactID(arguments any) string {
	values, ok := arguments.(map[string]any)
	if !ok {
		return ""
	}
	_, hasInternalTarget := values["_r42_artifact_path"]
	id, hasArtifactID := values["artifact_id"].(string)
	if !hasInternalTarget || !hasArtifactID {
		return ""
	}
	return strings.TrimSpace(id)
}

// materializeArtifactTargetPath preserves the output-target helper used by
// callers and tests. All private artifact path bindings are resolved below.
func materializeArtifactTargetPath(arguments map[string]any, registry *artifactpkg.Registry) error {
	return materializeArtifactPaths(arguments, registry)
}

// materializeArtifactPaths resolves fixed artifact IDs into their private path
// fields immediately before a typed tool runs. Filesystem paths are a runtime
// detail and are never supplied by the model.
func materializeArtifactPaths(arguments map[string]any, registry *artifactpkg.Registry) error {
	fields := make([]string, 0)
	for field := range arguments {
		if artifactPathIDField(field) != "" {
			fields = append(fields, field)
		}
	}
	slices.Sort(fields)
	for _, pathField := range fields {
		idField := artifactPathIDField(pathField)
		if registry == nil {
			return errors.New("artifact registry is required")
		}
		if strings.HasSuffix(pathField, "_paths") {
			paths, err := materializeArtifactPathList(arguments[idField], registry)
			if err != nil {
				return fmt.Errorf("%s: %w", idField, err)
			}
			arguments[pathField] = paths
			continue
		}
		id, ok := arguments[idField].(string)
		if !ok || strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s is required", idField)
		}
		record, err := registry.Record(id)
		if err != nil {
			return err
		}
		if record.Type != researchspec.ArtifactTypeFile {
			return fmt.Errorf("%s %q must name a file artifact", idField, id)
		}
		if pathField == "_r42_artifact_path" && record.Purpose != artifactpkg.PurposeOutput {
			return fmt.Errorf("artifact_id %q must name a declared file output artifact", id)
		}
		arguments[pathField] = record.Path
	}
	return nil
}

func artifactPathIDField(pathField string) string {
	if !strings.HasPrefix(pathField, "_r42_") {
		return ""
	}
	name := strings.TrimPrefix(pathField, "_r42_")
	if name == "artifact_path" {
		return "artifact_id"
	}
	if prefix, ok := strings.CutSuffix(name, "_paths"); ok {
		return prefix + "_artifact_ids"
	}
	if prefix, ok := strings.CutSuffix(name, "_path"); ok {
		return prefix + "_artifact_id"
	}
	return ""
}

func materializeArtifactPathList(value any, registry *artifactpkg.Registry) ([]any, error) {
	ids, ok := value.([]any)
	if !ok {
		if strings, stringsOK := value.([]string); stringsOK {
			ids = make([]any, len(strings))
			for index, id := range strings {
				ids[index] = id
			}
		} else {
			return nil, errors.New("must be a list of artifact IDs")
		}
	}
	paths := make([]any, 0, len(ids))
	for index, value := range ids {
		id, ok := value.(string)
		if !ok || strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("item %d must be an artifact ID", index)
		}
		record, err := registry.Record(id)
		if err != nil {
			return nil, err
		}
		if record.Type != researchspec.ArtifactTypeFile {
			return nil, fmt.Errorf("artifact ID %q must name a file artifact", id)
		}
		paths = append(paths, record.Path)
	}
	return paths, nil
}

func collectArtifactIDs(value any, result map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			switch key {
			case "artifact_id":
				if id, ok := nested.(string); ok && strings.TrimSpace(id) != "" {
					result[id] = struct{}{}
				}
			case "artifact_ids":
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
				collectArtifactIDs(nested, result)
			}
		}
	case []any:
		for _, nested := range typed {
			collectArtifactIDs(nested, result)
		}
	}
}

func foreignArtifactPaths(value any, workspace string) []string {
	paths := make([]string, 0)
	collectArtifactPaths(value, &paths)
	foreign := make([]string, 0, len(paths))
	for _, path := range paths {
		if !pathWithinWorkspace(workspace, path) {
			foreign = append(foreign, path)
		}
	}
	return foreign
}

func collectArtifactPaths(value any, result *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if key == "artifact_path" {
				if path, ok := nested.(string); ok && strings.TrimSpace(path) != "" {
					*result = append(*result, path)
				}
				continue
			}
			collectArtifactPaths(nested, result)
		}
	case []any:
		for _, nested := range typed {
			collectArtifactPaths(nested, result)
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

type artifactSearchArgs struct {
	ArtifactID    string `json:"artifact_id"`
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
	ArtifactID string `json:"artifact_id"`
	Content    string `json:"content"`
}

const maxJSONArtifactBytes = 4 * 1024 * 1024

func readArtifactJSONValue(
	registry *artifactpkg.Registry,
	authorized func(string) bool,
	id string,
) (any, error) {
	if registry == nil {
		return nil, errors.New("artifact registry is required")
	}
	if !authorized(id) {
		return nil, fmt.Errorf("unknown artifact %q", id)
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
