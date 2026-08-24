package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lonegunmanb/golden"
	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/coordinator"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/evidence"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
	"github.com/zclconf/go-cty/cty"
)

const (
	collectionCheckpointToolName = "r42_collection_checkpoint"
	collectionQCVerdictToolName  = "r42_collection_qc_verdict"
	finalQCVerdictToolName       = "r42_qc_verdict"
)

const researchArtifactProtocol = "Evidence protocol: evidence crosses research-block boundaries only by artifact_id. " +
	"Use r42_read_artifact with an authorized artifact_id to inspect source material, r42_search_artifact to locate exact evidence text, " +
	"and r42_search_artifacts to find text across all authorized artifacts. " +
	"Read-only view, grep, head, and tail may inspect files by path. Do not use artifact paths as cross-block evidence references. " +
	"Every citation carried into a downstream knowledge result must retain its artifact_id."

func toolUseArtifactIDs(uses []researchspec.ToolUse) []string {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, use := range uses {
		fields, err := toolUseObjectMap(use.InputFromAgent)
		if err != nil {
			continue
		}
		for _, field := range fields {
			unmarked, _ := field.UnmarkDeep()
			if !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("sources") || !unmarked.GetAttr("sources").IsKnown() {
				continue
			}
			for _, source := range unmarked.GetAttr("sources").AsValueSlice() {
				if !source.Type().IsObjectType() || !source.Type().HasAttribute("id") || !source.Type().HasAttribute("kind") {
					continue
				}
				id, kind := source.GetAttr("id"), source.GetAttr("kind")
				if !id.IsKnown() || id.IsNull() || !kind.IsKnown() || kind.IsNull() {
					continue
				}
				if kind.AsString() != "artifact" {
					continue
				}
				if _, exists := seen[id.AsString()]; exists {
					continue
				}
				seen[id.AsString()] = struct{}{}
				ids = append(ids, id.AsString())
			}
		}
	}
	return ids
}

func importedArtifactIDs(imports []researchspec.ImportArtifact) ([]string, error) {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, imported := range imports {
		sources, _ := imported.Sources.UnmarkDeep()
		if !sources.IsKnown() || sources.IsNull() || (!sources.Type().IsListType() && !sources.Type().IsTupleType()) {
			return nil, fmt.Errorf("import_artifact %q sources are not available at apply", imported.Name)
		}
		for _, source := range sources.AsValueSlice() {
			if !source.Type().IsObjectType() || !source.Type().HasAttribute("id") || !source.Type().HasAttribute("kind") {
				return nil, fmt.Errorf("import_artifact %q source is invalid", imported.Name)
			}
			id, kind := source.GetAttr("id"), source.GetAttr("kind")
			if !id.IsKnown() || id.IsNull() || !id.Type().Equals(cty.String) || strings.TrimSpace(id.AsString()) == "" ||
				!kind.IsKnown() || kind.IsNull() || !kind.Type().Equals(cty.String) || kind.AsString() != "artifact" {
				return nil, fmt.Errorf("import_artifact %q source must resolve to an artifact ID", imported.Name)
			}
			if _, exists := seen[id.AsString()]; !exists {
				seen[id.AsString()] = struct{}{}
				result = append(result, id.AsString())
			}
		}
	}
	return result, nil
}

func validateToolUseArtifactAccess(uses []researchspec.ToolUse, authorized []string) error {
	allowed := make(map[string]struct{}, len(authorized))
	for _, id := range authorized {
		allowed[id] = struct{}{}
	}
	for _, id := range toolUseArtifactIDs(uses) {
		if _, exists := allowed[id]; !exists {
			return fmt.Errorf("input_from_agent source artifact %q is not declared by artifact or import_artifact", id)
		}
	}
	return nil
}

func materializeArtifactReferences(
	uses []researchspec.ToolUse,
	artifactIDs map[string]string,
) []researchspec.ToolUse {
	result := slices.Clone(uses)
	for useIndex := range result {
		result[useIndex].Input = materializeArtifactReferenceIDs(result[useIndex].Input, artifactIDs)
		result[useIndex].InputFromAgent = materializeArtifactReferenceIDs(result[useIndex].InputFromAgent, artifactIDs)
	}
	return result
}

func materializeArtifactReferenceIDs(value cty.Value, artifactIDs map[string]string) cty.Value {
	if value == cty.NilVal {
		return value
	}
	unmarked, marks := value.Unmark()
	if !unmarked.IsKnown() || unmarked.IsNull() {
		return value
	}
	switch {
	case unmarked.Type().Equals(cty.String):
		name, reference := researchspec.ArtifactReferenceIDName(unmarked.AsString())
		if !reference {
			return value
		}
		if replacement := artifactIDs[name]; replacement != "" {
			return cty.StringVal(replacement).WithMarks(marks)
		}
		return value
	case unmarked.Type().IsObjectType():
		values := make(map[string]cty.Value, len(unmarked.AsValueMap()))
		for name, item := range unmarked.AsValueMap() {
			values[name] = materializeArtifactReferenceIDs(item, artifactIDs)
		}
		return cty.ObjectVal(values).WithMarks(marks)
	case unmarked.Type().IsMapType():
		values := make(map[string]cty.Value, len(unmarked.AsValueMap()))
		for name, item := range unmarked.AsValueMap() {
			values[name] = materializeArtifactReferenceIDs(item, artifactIDs)
		}
		if len(values) == 0 {
			return cty.MapValEmpty(unmarked.Type().ElementType()).WithMarks(marks)
		}
		return cty.MapVal(values).WithMarks(marks)
	case unmarked.Type().IsListType():
		items := unmarked.AsValueSlice()
		for index := range items {
			items[index] = materializeArtifactReferenceIDs(items[index], artifactIDs)
		}
		if len(items) == 0 {
			return cty.ListValEmpty(unmarked.Type().ElementType()).WithMarks(marks)
		}
		return cty.ListVal(items).WithMarks(marks)
	case unmarked.Type().IsTupleType():
		items := unmarked.AsValueSlice()
		for index := range items {
			items[index] = materializeArtifactReferenceIDs(items[index], artifactIDs)
		}
		return cty.TupleVal(items).WithMarks(marks)
	default:
		return value
	}
}

func closedResearchSystemPrompt(configured string) string {
	return "You are the closed Research synthesis phase. Use r42 typed tools when artifact identity or structured access matters. " +
		"Read-only view, grep, head, and tail are also available for unrestricted file inspection. " +
		"Do not acquire new evidence or use network, shell, write/edit, task, or user-input tools.\n\n" +
		researchArtifactProtocol + "\n\n" + configured
}

func researchEvidencePrompt(configured string, ids []string) string {
	if len(ids) == 0 {
		return configured
	}
	return configured + "\n\nAuthorized evidence artifact IDs for this Research phase:\n- " + strings.Join(ids, "\n- ")
}

func (f *runtimeFactory) newResearchBlock(
	ctx context.Context,
	address string,
	workspace string,
	planned modulespec.ResearchPlan,
	publish func(string, cty.Value),
) (golden.ApplyBlock, error) {
	var blockCancel context.CancelFunc
	if planned.Config.Timeout != nil {
		ctx, blockCancel = context.WithTimeout(ctx, *planned.Config.Timeout)
	}
	keepContext := false
	defer func() {
		if !keepContext && blockCancel != nil {
			blockCancel()
		}
	}()

	executionAddress := f.CanonicalAddress(address)
	artifactsRegistry := f.ensureArtifactRegistry()
	artifactIDs := make(map[string]string, len(planned.Config.Artifacts))
	currentArtifactIDs := make([]string, 0, len(planned.Config.Artifacts))
	for _, declared := range planned.Config.Artifacts {
		record, declareErr := artifactsRegistry.Declare(workspace, declared)
		if declareErr != nil {
			return nil, declareErr
		}
		artifactIDs[declared.Name] = record.ID
		currentArtifactIDs = append(currentArtifactIDs, record.ID)
	}
	collectionContext := collection.NewContextWithArtifactRegistry(
		workspace, planned.Config.CollectionBatchSize, planned.Config.MaxCollectionRounds, artifactsRegistry,
	)
	if targetErr := addCollectionArtifactTargets(collectionContext, artifactsRegistry, planned.Config.Artifacts, artifactIDs); targetErr != nil {
		return nil, targetErr
	}
	planned.Config.ToolUses = materializeArtifactReferences(planned.Config.ToolUses, artifactIDs)
	importedArtifactIDs, importErr := importedArtifactIDs(planned.Config.Imports)
	if importErr != nil {
		return nil, importErr
	}
	researchArtifactIDs := append(slices.Clone(currentArtifactIDs), importedArtifactIDs...)
	if accessErr := validateToolUseArtifactAccess(planned.Config.ToolUses, researchArtifactIDs); accessErr != nil {
		return nil, accessErr
	}
	var err error
	researchPhaseRetry, err := researchRetry(planned.Provider, planned.Config.Retry)
	if err != nil {
		return nil, err
	}
	collectionProvider := phaseProvider(planned.CollectionProvider, planned.Provider)
	collectionRetry, err := researchRetry(collectionProvider, planned.Config.Retry)
	if err != nil {
		return nil, err
	}
	if err = collectionContext.BeginWorkflow(); err != nil {
		return nil, err
	}
	opened := make([]Session, 0, 4)
	cleanupSetup := func(cause error) (golden.ApplyBlock, error) {
		for _, session := range slices.Backward(opened) {
			if closeErr := session.Close(context.WithoutCancel(ctx)); closeErr != nil {
				f.state.addWarning(fmt.Errorf("close workflow session after setup failure: %w", closeErr))
			}
		}
		return nil, cause
	}

	// Collection is the only open-world phase and owns acquisition tools.
	collectionQuota, collectionBuiltInQuota := splitToolCallQuota(planned.Config.Policy.ToolCallQuota)
	collectionTools, _, err := f.buildTools(ctx, executionAddress, debuglog.SessionCollection, workspace,
		planned.Config.CollectionToolIDs, nil, researchruntime.NewTerminalRecorder(), newToolCallQuota(collectionQuota))
	if err != nil {
		return nil, err
	}
	collectionTools = wrapCollectionAcquisitionTools(collectionTools, collectionContext)
	collectionArtifactTools, err := evidenceToolsWithArtifactRegistry(
		workspace, planned.Config.Artifacts, true, artifactsRegistry, currentArtifactIDs, collectionContext.EvidenceArtifactIDs,
	)
	if err != nil {
		return cleanupSetup(err)
	}
	collectionTools = append(collectionTools, collectionArtifactTools...)
	checkpoints := collection.NewCheckpointRecorder()
	collectionTools = append(collectionTools, collectionProtocolTools(collectionContext, checkpoints)...)
	collectionPrompt := appendBuiltInToolCallQuotaPrompt(
		"You are the Collection phase. Acquire evidence and preserve complete source material. When source content is not already available as a workspace file or retained source-tool result, call r42_save_artifact with a non-empty source identifier; it saves and registers the evidence artifact, so use its returned artifact_id directly and do not call r42_register_artifact afterward. Use r42_register_artifact only for an existing workspace evidence file or retained source-tool result; supply its optional source when that content has no Source or legacy URL header. Call r42_collection_checkpoint before ending the round. If no more evidence can be acquired, submit an empty checkpoint with empty_reason and collection_exhausted=true.\n\n"+planned.Config.SystemPrompt,
		collectionBuiltInQuota,
	)
	collectionSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionCollection, copilot.SessionConfig{
		Provider: collectionProvider,
		Retry:    collectionRetry, Model: planned.Config.Model, Profile: planned.Config.ProfileName(),
		ReasoningEffort: pointerValue(planned.Config.ReasoningEffort), SystemPrompt: collectionPrompt, WorkingDirectory: workspace,
		Tools: collectionTools, AvailableTools: collectionAllowedTools(
			planned.Config.Policy.AllowedTools, toolNames(collectionTools),
		),
		ExcludedTools:    collectionDisallowedTools(planned.Config.Policy.DisallowedTools),
		SkillDirectories: slices.Clone(planned.Config.CollectionSkillDirectories), Skills: slices.Clone(planned.Config.CollectionSkills),
		DisabledSkills: slices.Clone(planned.Config.CollectionDisabledSkills),
		Hooks:          collectionBuiltInHooks(newToolCallQuota(collectionBuiltInQuota), collectionContext.Gate()),
	})
	if err != nil {
		return nil, err
	}
	opened = append(opened, collectionSession)
	collectionRunner := collection.NewRunner(collectionSession, checkpoints)

	// Collection QC is always present and has fixed read-only capabilities.
	collectionQCProvider := phaseProvider(planned.CollectionQCProvider, planned.Provider)
	collectionQCProviderRetry, err := researchRetry(collectionQCProvider, provider.RetryOverride{})
	if err != nil {
		return cleanupSetup(err)
	}
	effectiveCollectionQC, err := planned.Config.EffectiveCollectionQC(collectionQCProviderRetry)
	if err != nil {
		return cleanupSetup(err)
	}
	collectionQCReadTools, err := evidenceToolsWithArtifactRegistry(
		workspace, planned.Config.Artifacts, false, artifactsRegistry, currentArtifactIDs, collectionContext.EvidenceArtifactIDs,
	)
	if err != nil {
		return cleanupSetup(err)
	}
	collectionVerdicts := collectionqc.NewVerdictRecorder()
	collectionQCReadTools = append(collectionQCReadTools, collectionQCVerdictTool(collectionVerdicts))
	collectionQCSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionCollectionQC, copilot.SessionConfig{
		Provider: collectionQCProvider,
		Retry:    effectiveCollectionQC.Retry, Model: effectiveCollectionQC.Model,
		Profile: effectiveCollectionQC.Profile, ReasoningEffort: pointerValue(effectiveCollectionQC.ReasoningEffort),
		SystemPrompt:     "You are Collection QC. Semantically assess whether registered evidence artifacts are sufficient. Use r42 read tools or read-only view, grep, head, and tail, then submit a typed verdict.",
		WorkingDirectory: workspace, Tools: collectionQCReadTools,
		ExcludedTools: closedWorldDisallowedTools(nil),
	})
	if err != nil {
		return cleanupSetup(err)
	}
	opened = append(opened, collectionQCSession)
	collectionQCRunner := collectionqc.NewRunner(collectionQCSession, collectionVerdicts, collectionContext)

	// Research is closed-world synthesis over registered evidence artifacts.
	researchTypedQuota, researchBuiltInQuota := splitToolCallQuota(planned.Config.Policy.ToolCallQuota)
	terminal := researchruntime.NewTerminalRecorder()
	researchTools, terminalType, err := f.buildTools(ctx, executionAddress, debuglog.SessionResearch, workspace,
		planned.Config.Policy.ToolIDs, planned.Config.TerminateToolID, terminal, newToolCallQuota(researchTypedQuota))
	if err != nil {
		return cleanupSetup(err)
	}
	readWriteTools, err := evidenceToolsWithDynamicArtifacts(
		workspace, planned.Config.Artifacts, true, artifactsRegistry, researchArtifactIDs, collectionContext.EvidenceArtifactIDs,
	)
	if err != nil {
		return cleanupSetup(err)
	}
	terminateName := ""
	if planned.Config.TerminateToolID != nil {
		terminateName = *planned.Config.TerminateToolID
	}
	researchTools, err = bindResearchToolUses(researchTools, planned.Config.ToolUses, func() (*evidence.ArtifactEvidenceAccess, error) {
		ids := append(slices.Clone(researchArtifactIDs), collectionContext.EvidenceArtifactIDs()...)
		return evidence.NewArtifactEvidenceAccess(artifactsRegistry, ids)
	}, artifactsRegistry, workspace, terminateName, terminal)
	if err != nil {
		return cleanupSetup(err)
	}
	researchTools = append(researchTools, readWriteTools...)
	resolved := researchspec.ResolvedTools{}
	if planned.Config.TerminateToolID != nil {
		definition := f.tools[terminateName]
		resolved.Terminate = &researchspec.ToolPolicyRef{ID: definition.ID, Address: definition.Address, OutputType: terminalType}
		resolved.TerminateSDKName = terminateName
	}
	if planned.Config.QC != nil {
		resolved.QCVerdictSDKName = finalQCVerdictToolName
	}
	if err = planned.Config.ValidateResolved(resolved); err != nil {
		return cleanupSetup(err)
	}
	researchPrompt := appendBuiltInToolCallQuotaPrompt(
		closedResearchSystemPrompt(planned.Config.SystemPrompt),
		researchBuiltInQuota,
	)
	researchSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionResearch, copilot.SessionConfig{
		Provider: planned.Provider, Retry: researchPhaseRetry, Model: planned.Config.Model, Profile: planned.Config.ProfileName(),
		ReasoningEffort: pointerValue(planned.Config.ReasoningEffort), SystemPrompt: researchPrompt, WorkingDirectory: workspace,
		Tools: researchTools, AvailableTools: closedWorldAllowedTools(planned.Config.Policy.AllowedTools, toolNames(researchTools)),
		ExcludedTools:    closedWorldDisallowedTools(planned.Config.Policy.DisallowedTools),
		SkillDirectories: slices.Clone(planned.Config.Policy.SkillDirectories), Skills: slices.Clone(planned.Config.Policy.Skills),
		DisabledSkills: slices.Clone(planned.Config.Policy.DisabledSkills), Hooks: builtInToolCallQuotaHooks(newToolCallQuota(researchBuiltInQuota)),
	})
	if err != nil {
		return cleanupSetup(err)
	}
	opened = append(opened, researchSession)
	researchRunner := researchruntime.NewRunner(researchSession, terminalIfConfigured(planned.Config.TerminateToolID, terminal))
	recordedResearchSession, ok := researchSession.(*recordingSession)
	if !ok {
		return cleanupSetup(fmt.Errorf("research session does not support phase recording"))
	}
	coordinatedResearch := &phasedResearch{
		research: researchRunner,
		session:  recordedResearchSession,
		artifactIDs: func() []string {
			ids := collectionContext.ReviewedEvidenceArtifactIDs()
			slices.Sort(ids)
			return ids
		},
	}

	initialPrompt := "Begin the configured research task."
	if planned.Config.Prompt != nil {
		initialPrompt = *planned.Config.Prompt
	}
	workflowConfig := coordinator.Config{
		Collection: collection.RunConfig{
			InitialPrompt: initialPrompt, MaxProtocolAttempts: planned.Config.MaxProtocolAttempts,
			CheckpointToolName: collectionCheckpointToolName,
		},
		CollectionQC: collectionqc.Config{
			Task: collectionqc.Task{
				SystemPrompt: planned.Config.SystemPrompt,
				Prompt:       planned.Config.Prompt,
			},
			Criteria: effectiveCollectionQC.Criteria, MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
			VerdictToolName: collectionQCVerdictToolName,
		},
		Research: researchruntime.Config{
			InitialPrompt: initialPrompt, TerminateToolName: terminateName,
			MaxProtocolAttempts: planned.Config.MaxProtocolAttempts,
			Workspace:           workspace, Artifacts: planned.Config.Artifacts, ArtifactIDs: artifactIDs,
		},
		Observe: func(event coordinator.Event) {
			_ = f.recorder.Record(debuglog.Event{
				Kind: debuglog.EventLifecycle, Action: "workflow.phase", Status: debuglog.StatusStarted,
				BlockAddress: executionAddress, BlockType: "research", Session: workflowSessionKind(event.Phase),
				Count: event.CollectionRounds, Content: event.Decision,
			})
		},
	}

	var finalReviewer coordinator.FinalReviewer
	if planned.Config.QC != nil {
		finalQCProvider := phaseProvider(planned.QCProvider, planned.Provider)
		finalQCProviderRetry, retryErr := researchRetry(finalQCProvider, provider.RetryOverride{})
		if retryErr != nil {
			return cleanupSetup(retryErr)
		}
		effectiveFinalQC, effectiveErr := planned.Config.EffectiveQC(finalQCProviderRetry)
		if effectiveErr != nil {
			return cleanupSetup(effectiveErr)
		}
		finalTypedQuota, finalBuiltInQuota := splitToolCallQuota(effectiveFinalQC.ToolCallQuota)
		finalTools, _, toolsErr := f.buildTools(ctx, executionAddress, debuglog.SessionFinalQC, workspace,
			effectiveFinalQC.ToolIDs, nil, researchruntime.NewTerminalRecorder(), newToolCallQuota(finalTypedQuota))
		if toolsErr != nil {
			return cleanupSetup(toolsErr)
		}
		finalEvidenceTools, toolsErr := evidenceToolsWithDynamicArtifacts(
			workspace, planned.Config.Artifacts, false, artifactsRegistry, researchArtifactIDs, collectionContext.EvidenceArtifactIDs,
		)
		if toolsErr != nil {
			return cleanupSetup(toolsErr)
		}
		finalTools = append(finalTools, finalEvidenceTools...)
		finalVerdicts := qc.NewVerdictRecorder()
		finalTools = append(finalTools, qcVerdictTool(executionAddress, f.recorder, finalVerdicts))
		finalSession, openErr := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionFinalQC, copilot.SessionConfig{
			Provider: finalQCProvider,
			Retry:    effectiveFinalQC.Retry, Model: effectiveFinalQC.Model, Profile: effectiveFinalQC.Profile,
			ReasoningEffort: pointerValue(effectiveFinalQC.ReasoningEffort),
			SystemPrompt: appendBuiltInToolCallQuotaPrompt(
				"You are Final QC. Review evidence artifacts and candidate files with r42 read tools or read-only view, grep, head, and tail, then submit pass, revise_research, or reopen_collection. "+researchArtifactProtocol,
				finalBuiltInQuota,
			),
			WorkingDirectory: workspace, Tools: finalTools,
			AvailableTools:   closedWorldAllowedTools(effectiveFinalQC.AllowedTools, toolNames(finalTools)),
			ExcludedTools:    closedWorldDisallowedTools(effectiveFinalQC.DisallowedTools),
			SkillDirectories: slices.Clone(effectiveFinalQC.SkillDirectories), Skills: slices.Clone(effectiveFinalQC.Skills),
			DisabledSkills: slices.Clone(effectiveFinalQC.DisabledSkills), Hooks: builtInToolCallQuotaHooks(newToolCallQuota(finalBuiltInQuota)),
		})
		if openErr != nil {
			return cleanupSetup(openErr)
		}
		opened = append(opened, finalSession)
		finalRunner := qc.NewRunner(nil, finalSession, finalVerdicts)
		finalReviewer = &budgetedFinalReviewer{runner: finalRunner, recorder: finalVerdicts, state: collectionContext.State}
		workflowConfig.FinalQCEnabled = true
		workflowConfig.MaxFinalQCRounds = effectiveFinalQC.MaxRounds
		workflowConfig.FinalQC = qc.Config{
			Task: qc.Task{
				SystemPrompt: planned.Config.SystemPrompt,
				Prompt:       planned.Config.Prompt,
			},
			Criteria: effectiveFinalQC.Criteria, Artifacts: planned.Config.Artifacts,
			MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
			VerdictToolName:     finalQCVerdictToolName,
		}
	}

	workflowRunner := coordinator.NewRunner(collectionContext.State, collectionRunner, collectionQCRunner, coordinatedResearch, finalReviewer)
	block := &researchApplyBlock{
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: address,
		config: workflowConfig.Research, publish: publish, cancel: blockCancel, workflowSessions: opened,
		workflowRun: func(runContext context.Context) (researchruntime.Result, error) {
			result, runErr := workflowRunner.Run(runContext, workflowConfig)
			if runErr != nil {
				return researchruntime.Result{}, runErr
			}
			return result, nil
		},
		afterSuccess: func() {},
	}
	keepContext = true
	return block, nil
}

func addCollectionArtifactTargets(
	context *collection.Context,
	registry *artifactpkg.Registry,
	artifacts []researchspec.Artifact,
	artifactIDs map[string]string,
) error {
	for _, declared := range artifacts {
		if declared.Type != researchspec.ArtifactTypeDirectory {
			continue
		}
		record, err := registry.Record(artifactIDs[declared.Name])
		if err != nil {
			return err
		}
		if err = context.AddArtifactTarget(record.Path, true); err != nil {
			return err
		}
	}
	return nil
}

func phaseProvider(override, fallback *provider.Config) *provider.Config {
	if override != nil {
		return override
	}
	return fallback
}

func (f *runtimeFactory) openRecordedWorkflowSession(ctx context.Context, address string, kind debuglog.SessionKind, config copilot.SessionConfig) (Session, error) {
	_ = f.recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, BlockAddress: address, Session: kind,
		Role: debuglog.RoleSystem, Content: config.SystemPrompt,
	})
	session, err := f.openSession(ctx, address, kind, config)
	if err != nil {
		return nil, err
	}
	activity := newTypedToolActivity()
	trackTypedToolActivity(config.Tools, activity)
	return &recordingSession{
		Session: session, recorder: f.recorder, address: address, kind: kind,
		stallTimeout: f.sessionStallTimeout, typedToolActivity: activity,
	}, nil
}

type budgetedFinalReviewer struct {
	runner   *qc.Runner
	recorder *qc.VerdictRecorder
	state    *workflow.State
}

func (r *budgetedFinalReviewer) Review(ctx context.Context, config qc.Config, candidate researchruntime.Result) (qc.Verdict, error) {
	r.recorder.SetCollectionBudget(qc.CollectionBudget{
		RoundsUsed: r.state.CollectionRoundsUsed(), MaxRounds: r.state.MaxCollectionRounds(),
		Exhausted: r.state.CollectionExhausted(),
	})
	return r.runner.Review(ctx, config, candidate)
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func terminalIfConfigured(id *string, recorder *researchruntime.TerminalRecorder) *researchruntime.TerminalRecorder {
	if id == nil {
		return nil
	}
	return recorder
}

func phaseAllowedTools(configured, mandatory []string) []string {
	if configured == nil {
		return nil
	}
	result := slices.Clone(configured)
	for _, name := range mandatory {
		if !slices.Contains(result, name) && !slices.Contains(result, "custom:"+name) && !slices.Contains(result, "custom:*") {
			result = append(result, name)
		}
	}
	return result
}

func workflowSessionKind(phase workflow.Phase) debuglog.SessionKind {
	switch phase {
	case workflow.PhaseCollection:
		return debuglog.SessionCollection
	case workflow.PhaseCollectionQC:
		return debuglog.SessionCollectionQC
	case workflow.PhaseResearch:
		return debuglog.SessionResearch
	case workflow.PhaseFinalQC:
		return debuglog.SessionFinalQC
	default:
		return ""
	}
}
