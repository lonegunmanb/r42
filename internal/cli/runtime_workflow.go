package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

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
	"github.com/lonegunmanb/r42/internal/snapshot"
	"github.com/lonegunmanb/r42/internal/workflow"
	"github.com/zclconf/go-cty/cty"
)

const (
	collectionCheckpointToolName = "r42_collection_checkpoint"
	collectionQCVerdictToolName  = "r42_collection_qc_verdict"
	finalQCVerdictToolName       = "r42_qc_verdict"
)

const researchSnapshotProtocol = "Evidence protocol: snapshots cross research-block boundaries only by snapshot_id. " +
	"Use r42_read_snapshot with an authorized snapshot_id to inspect source material. " +
	"Do not use snapshot paths as cross-block evidence references and do not read or copy snapshot files through the filesystem. " +
	"Every citation carried into a downstream knowledge result must retain its snapshot_id."

var snapshotIDPattern = regexp.MustCompile(`snapshot-[0-9a-f]{32}`)

type runSnapshotCatalog struct {
	mu    sync.RWMutex
	paths map[string]string
}

func newRunSnapshotCatalog() *runSnapshotCatalog {
	return &runSnapshotCatalog{paths: map[string]string{}}
}

func (c *runSnapshotCatalog) add(snapshots []snapshot.Snapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range snapshots {
		if item.Reviewed {
			c.paths[item.ID] = item.Path
		}
	}
}

func (c *runSnapshotCatalog) authorized(text ...string) map[string]string {
	result := map[string]string{}
	if c == nil {
		return result
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, value := range text {
		for _, id := range snapshotIDPattern.FindAllString(value, -1) {
			if path, ok := c.paths[id]; ok {
				result[id] = path
			}
		}
	}
	return result
}

func closedResearchSystemPrompt(configured string) string {
	return "You are the closed Research synthesis phase. Read registered evidence only through r42 typed tools. " +
		"Do not acquire new evidence or use network, shell, generic file, edit, task, or user-input tools.\n\n" +
		researchSnapshotProtocol + "\n\n" + configured
}

func researchEvidencePrompt(configured string, ids []string) string {
	if len(ids) == 0 {
		return configured
	}
	return configured + "\n\nAuthorized evidence snapshot IDs for this Research phase:\n- " + strings.Join(ids, "\n- ")
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
		"You are the Collection phase. Acquire evidence and preserve complete source material. When source content is not already available as a workspace file or retained source-tool result, call r42_save_snapshot with a non-empty source identifier; it saves and registers the snapshot, so use its returned snapshot_id directly and do not call r42_register_snapshot afterward. Use r42_register_snapshot only for an existing workspace snapshot file or retained source-tool result; supply its optional source when that content has no Source or legacy URL header. Call r42_collection_checkpoint before ending the round. If no more evidence can be acquired, submit an empty checkpoint with empty_reason and collection_exhausted=true.\n\n"+planned.Config.SystemPrompt,
		collectionBuiltInQuota,
	)
	collectionSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionCollection, copilot.SessionConfig{
		Provider: collectionProvider,
		Retry:    collectionRetry, Model: planned.Config.Model, Profile: planned.Config.ProfileName(),
		ReasoningEffort: pointerValue(planned.Config.ReasoningEffort), SystemPrompt: collectionPrompt, WorkingDirectory: workspace,
		Tools: collectionTools, AvailableTools: phaseAllowedTools(planned.Config.Policy.AllowedTools, toolNames(collectionTools)),
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
	collectionQCReadTools, err := evidenceTools(collectionContext.Registry, workspace, planned.Config.Artifacts, false)
	if err != nil {
		return cleanupSetup(err)
	}
	collectionVerdicts := collectionqc.NewVerdictRecorder()
	collectionQCReadTools = append(collectionQCReadTools, collectionQCVerdictTool(collectionVerdicts))
	collectionQCSession, err := f.openRecordedWorkflowSession(ctx, executionAddress, debuglog.SessionCollectionQC, copilot.SessionConfig{
		Provider: collectionQCProvider,
		Retry:    effectiveCollectionQC.Retry, Model: effectiveCollectionQC.Model,
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
	upstreamText := planned.Config.SystemPrompt
	if planned.Config.Prompt != nil {
		upstreamText += "\n" + *planned.Config.Prompt
	}
	upstream := f.snapshotCatalog.authorized(upstreamText)
	readWriteTools, snapshotAccess, err := evidenceToolsWithUpstream(
		collectionContext.Registry,
		upstream,
		workspace,
		planned.Config.Artifacts,
		true,
	)
	if err != nil {
		return cleanupSetup(err)
	}
	terminateName := ""
	if planned.Config.TerminateToolID != nil {
		terminateName = *planned.Config.TerminateToolID
	}
	researchTools = enforceSnapshotIDReferences(researchTools, snapshotAccess, workspace, terminateName, terminal)
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
		snapshotIDs: func() []string {
			ids := collectionContext.Registry.ReviewedSnapshotIDs()
			for id := range upstream {
				ids = append(ids, id)
			}
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
		finalEvidenceTools, _, toolsErr := evidenceToolsWithUpstream(
			collectionContext.Registry,
			upstream,
			workspace,
			planned.Config.Artifacts,
			false,
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
				"You are Final QC. Review only authorized snapshots by snapshot_id and candidate artifacts, then submit pass, revise_research, or reopen_collection. "+researchSnapshotProtocol,
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
			return workflowRunner.Run(runContext, workflowConfig)
		},
		afterSuccess: func() {
			f.snapshotCatalog.add(collectionContext.Registry.Snapshots())
		},
	}
	keepContext = true
	return block, nil
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
