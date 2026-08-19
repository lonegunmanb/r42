package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/coordinator"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
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

func (f *runtimeFactory) newResearchBlock(
	ctx context.Context,
	address string,
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
	workspace, err := f.run.Workspace(executionAddress)
	if err != nil {
		return nil, err
	}
	retry, err := researchRetry(planned.Provider, planned.Config.Retry)
	if err != nil {
		return nil, err
	}
	collectionContext := collection.NewContext(workspace, planned.Config.CollectionBatchSize, planned.Config.MaxCollectionRounds)
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
	checkpoints := collection.NewCheckpointRecorder()
	collectionTools = append(collectionTools, collectionProtocolTools(collectionContext, checkpoints)...)
	collectionPrompt := appendBuiltInToolCallQuotaPrompt(
		"You are the Collection phase. Acquire evidence, register every useful snapshot, and call r42_collection_checkpoint before ending the round.\n\n"+planned.Config.SystemPrompt,
		collectionBuiltInQuota,
	)
	collectionSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionCollection, copilot.SessionConfig{
		Provider: planned.Provider, Retry: retry, Model: planned.Config.Model, Profile: planned.Config.ProfileName(),
		ReasoningEffort: pointerValue(planned.Config.ReasoningEffort), SystemPrompt: collectionPrompt, WorkingDirectory: workspace,
		Tools: collectionTools, AvailableTools: phaseAllowedTools(planned.Config.Policy.AllowedTools, toolNames(collectionTools)),
		ExcludedTools:    slices.Clone(planned.Config.Policy.DisallowedTools),
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
	effectiveCollectionQC, err := planned.Config.EffectiveCollectionQC(provider.DefaultRetryPolicy())
	if err != nil {
		return cleanupSetup(err)
	}
	collectionQCReadTools, err := evidenceTools(collectionContext.Registry, workspace, planned.Config.Artifacts, false)
	if err != nil {
		return cleanupSetup(err)
	}
	collectionVerdicts := collectionqc.NewVerdictRecorder()
	collectionQCReadTools = append(collectionQCReadTools, collectionQCVerdictTool(collectionVerdicts))
	collectionQCSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionCollectionQC, copilot.SessionConfig{
		Provider: planned.CollectionQCProvider, Retry: effectiveCollectionQC.Retry, Model: effectiveCollectionQC.Model,
		Profile: effectiveCollectionQC.Profile, ReasoningEffort: pointerValue(effectiveCollectionQC.ReasoningEffort),
		SystemPrompt:     "You are Collection QC. Semantically assess whether registered snapshots are sufficient. Use only r42 read tools and submit a typed verdict.",
		WorkingDirectory: workspace, Tools: collectionQCReadTools,
		ExcludedTools: closedWorldDisallowedTools(nil),
	})
	if err != nil {
		return cleanupSetup(err)
	}
	opened = append(opened, collectionQCSession)
	collectionQCRunner := collectionqc.NewRunner(collectionQCSession, collectionVerdicts, collectionContext)

	// Research is closed-world synthesis over registered snapshots.
	researchTypedQuota, researchBuiltInQuota := splitToolCallQuota(planned.Config.Policy.ToolCallQuota)
	terminal := researchruntime.NewTerminalRecorder()
	researchTools, terminalType, err := f.buildTools(ctx, executionAddress, debuglog.SessionResearch, workspace,
		planned.Config.Policy.ToolIDs, planned.Config.TerminateToolID, terminal, newToolCallQuota(researchTypedQuota))
	if err != nil {
		return cleanupSetup(err)
	}
	readWriteTools, err := evidenceTools(collectionContext.Registry, workspace, planned.Config.Artifacts, true)
	if err != nil {
		return cleanupSetup(err)
	}
	researchTools = append(researchTools, readWriteTools...)
	resolved := researchspec.ResolvedTools{}
	terminateName := ""
	if planned.Config.TerminateToolID != nil {
		terminateName = *planned.Config.TerminateToolID
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
		"You are the closed Research synthesis phase. Read registered snapshots through r42 tools. Do not acquire new evidence or use network, shell, generic file, edit, task, or user-input tools.\n\n"+planned.Config.SystemPrompt,
		researchBuiltInQuota,
	)
	researchSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionResearch, copilot.SessionConfig{
		Provider: planned.Provider, Retry: retry, Model: planned.Config.Model, Profile: planned.Config.ProfileName(),
		ReasoningEffort: pointerValue(planned.Config.ReasoningEffort), SystemPrompt: researchPrompt, WorkingDirectory: workspace,
		Tools: researchTools, AvailableTools: phaseAllowedTools(planned.Config.Policy.AllowedTools, toolNames(researchTools)),
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
	coordinatedResearch := &phasedResearch{research: researchRunner, session: recordedResearchSession}

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
			Workspace:           workspace, Artifacts: planned.Config.Artifacts,
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
		effectiveFinalQC, effectiveErr := planned.Config.EffectiveQC(provider.DefaultRetryPolicy())
		if effectiveErr != nil {
			return cleanupSetup(effectiveErr)
		}
		finalTypedQuota, finalBuiltInQuota := splitToolCallQuota(effectiveFinalQC.ToolCallQuota)
		finalTools, _, toolsErr := f.buildTools(ctx, executionAddress, debuglog.SessionFinalQC, workspace,
			effectiveFinalQC.ToolIDs, nil, researchruntime.NewTerminalRecorder(), newToolCallQuota(finalTypedQuota))
		if toolsErr != nil {
			return cleanupSetup(toolsErr)
		}
		finalEvidenceTools, toolsErr := evidenceTools(collectionContext.Registry, workspace, planned.Config.Artifacts, false)
		if toolsErr != nil {
			return cleanupSetup(toolsErr)
		}
		finalTools = append(finalTools, finalEvidenceTools...)
		finalVerdicts := qc.NewVerdictRecorder()
		finalTools = append(finalTools, qcVerdictTool(executionAddress, f.recorder, finalVerdicts))
		finalSession, openErr := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionFinalQC, copilot.SessionConfig{
			Provider: planned.QCProvider, Retry: effectiveFinalQC.Retry, Model: effectiveFinalQC.Model, Profile: effectiveFinalQC.Profile,
			ReasoningEffort:  pointerValue(effectiveFinalQC.ReasoningEffort),
			SystemPrompt:     appendBuiltInToolCallQuotaPrompt("You are Final QC. Review only registered snapshots and candidate artifacts, then submit pass, revise_research, or reopen_collection.", finalBuiltInQuota),
			WorkingDirectory: workspace, Tools: finalTools,
			AvailableTools:   phaseAllowedTools(effectiveFinalQC.AllowedTools, toolNames(finalTools)),
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
			return workflowRunner.Run(runContext, workflowConfig)
		},
	}
	keepContext = true
	return block, nil
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
	r.recorder.SetCollectionBudget(qc.CollectionBudget{RoundsUsed: r.state.CollectionRoundsUsed(), MaxRounds: r.state.MaxCollectionRounds()})
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
