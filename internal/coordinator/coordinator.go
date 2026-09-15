// Package coordinator composes the persistent Collection, Collection QC,
// Research, and optional Final QC phases for one research workflow instance.
package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
)

type Collector interface {
	Run(context.Context, collection.RunConfig) (collection.CheckpointOutput, error)
}

type CollectionReviewer interface {
	Review(context.Context, collectionqc.Config) (collectionqc.Result, error)
}

type Researcher interface {
	Run(context.Context, researchruntime.Config) (researchruntime.Result, error)
}

type FinalReviewer interface {
	Review(context.Context, qc.Config, researchruntime.Result) (qc.Verdict, error)
}

type Config struct {
	Collection       collection.RunConfig
	CollectionQC     collectionqc.Config
	Research         researchruntime.Config
	FinalQC          qc.Config
	FinalQCEnabled   bool
	MaxFinalQCRounds int
	Observe          func(Event)
	CheckpointWriter CheckpointWriter
	Resume           *Checkpoint
}

// CheckpointWriter durably publishes a host checkpoint after a completed
// phase handoff. It is deliberately invoked only after the workflow state
// machine accepted the transition.
type CheckpointWriter interface {
	Commit(context.Context, Checkpoint) error
}

// Checkpoint is the coordinator-owned state needed to restart the next phase.
// Runtime-owned state such as artifact versions and SDK session storage is
// persisted by the writer alongside this value.
type Checkpoint struct {
	NextPhase         workflow.Phase                          `json:"next_phase"`
	Workflow          workflow.Snapshot                       `json:"workflow"`
	CollectionState   []collection.ActiveInformationNeedState `json:"collection_state,omitempty"`
	CollectionQC      collection.CheckpointOutput             `json:"collection_qc"`
	Candidate         researchruntime.Result                  `json:"candidate"`
	FinalRounds       int                                     `json:"final_rounds"`
	FinalQCOpenIssues []corespec.Issue                        `json:"final_qc_open_issues,omitempty"`
}

type Action string

const (
	ActionStarted  Action = "started"
	ActionDecision Action = "decision"
)

type Event struct {
	Phase            workflow.Phase
	Action           Action
	Decision         string
	CollectionRounds int
	Round            int
	IsRevision       bool
}

type Runner struct {
	state        *workflow.State
	collection   Collector
	collectionQC CollectionReviewer
	research     Researcher
	finalQC      FinalReviewer
}

func NewRunner(
	state *workflow.State,
	collectionRunner Collector,
	collectionQCRunner CollectionReviewer,
	researchRunner Researcher,
	finalQCRunner FinalReviewer,
) *Runner {
	return &Runner{state: state, collection: collectionRunner, collectionQC: collectionQCRunner, research: researchRunner, finalQC: finalQCRunner}
}

func (r *Runner) Run(ctx context.Context, config Config) (researchruntime.Result, error) {
	if err := r.validate(config); err != nil {
		return researchruntime.Result{}, err
	}
	var candidate researchruntime.Result
	var collectionState []collection.ActiveInformationNeedState
	finalRounds := 0
	if config.Resume != nil {
		if err := r.state.Restore(config.Resume.Workflow); err != nil {
			return researchruntime.Result{}, fmt.Errorf("restore workflow checkpoint: %w", err)
		}
		collectionState = cloneActiveInformationNeedStates(config.Resume.CollectionState)
		config.CollectionQC.CheckpointArtifactIDs = append([]string(nil), config.Resume.CollectionQC.ArtifactIDs...)
		config.CollectionQC.CheckpointEmptyReason = config.Resume.CollectionQC.EmptyReason
		config.CollectionQC.NeedDispositions = append([]collection.NeedDisposition(nil), config.Resume.CollectionQC.NeedDispositions...)
		candidate = cloneResult(config.Resume.Candidate)
		finalRounds = config.Resume.FinalRounds
		config.FinalQC.OpenIssues = cloneIssues(config.Resume.FinalQCOpenIssues)
		if r.state.Phase() == "" {
			if err := r.state.Begin(); err != nil {
				return researchruntime.Result{}, err
			}
		}
	} else if err := r.state.Begin(); err != nil {
		return researchruntime.Result{}, err
	}

	for {
		phase := r.state.Phase()
		event := Event{Phase: phase, Action: ActionStarted, CollectionRounds: r.state.CollectionRoundsUsed()}
		switch phase {
		case workflow.PhaseCollection, workflow.PhaseCollectionQC:
			event.Round = r.state.CollectionRoundsUsed()
		case workflow.PhaseResearch:
			event.Round = 1
		case workflow.PhaseFinalQC:
			finalRounds++
			event.Round = finalRounds
		}
		emit(config.Observe, event)
		switch r.state.Phase() {
		case workflow.PhaseCollection:
			collectionConfig := config.Collection
			collectionConfig.ActiveInformationNeedStates = append([]collection.ActiveInformationNeedState(nil), collectionState...)
			collectionConfig.InitialPrompt = collectionRoundPrompt(
				collectionConfig.InitialPrompt,
				collectionState,
				r.state.InformationNeedOutcomes(),
			)
			checkpoint, err := r.collection.Run(ctx, collectionConfig)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run collection: %w", err)
			}
			config.CollectionQC.CheckpointArtifactIDs = append([]string(nil), checkpoint.ArtifactIDs...)
			config.CollectionQC.CheckpointEmptyReason = checkpoint.EmptyReason
			config.CollectionQC.NeedDispositions = append([]collection.NeedDisposition(nil), checkpoint.NeedDispositions...)
			if err = r.state.Advance(workflow.EventCollectionCheckpoint); err != nil {
				return researchruntime.Result{}, err
			}
			if err = r.commit(ctx, config.CheckpointWriter, collectionState, config.CollectionQC, candidate, finalRounds, config.FinalQC.OpenIssues); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseCollectionQC:
			result, err := r.collectionQC.Review(ctx, config.CollectionQC)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run collection qc: %w", err)
			}
			collectionState = append([]collection.ActiveInformationNeedState(nil), result.ActiveInformationNeedStates...)
			emit(config.Observe, Event{
				Phase: workflow.PhaseCollectionQC, Action: ActionDecision,
				Decision: collectionQCDecision(result), CollectionRounds: r.state.CollectionRoundsUsed(),
				Round: r.state.CollectionRoundsUsed(),
			})
			if !result.CollectionLimitExhausted && r.state.Phase() == workflow.PhaseCollection {
				if err = r.commit(ctx, config.CheckpointWriter, collectionState, config.CollectionQC, candidate, finalRounds, config.FinalQC.OpenIssues); err != nil {
					return researchruntime.Result{}, err
				}
				continue
			}
			if err = r.commit(ctx, config.CheckpointWriter, collectionState, config.CollectionQC, candidate, finalRounds, config.FinalQC.OpenIssues); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseResearch:
			researchConfig := config.Research
			researchConfig.InitialPrompt = researchOutcomesPrompt(researchConfig.InitialPrompt, r.state.InformationNeedOutcomes())
			var err error
			candidate, err = r.research.Run(ctx, researchConfig)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run research: %w", err)
			}
			event := workflow.EventResearchComplete
			if !config.FinalQCEnabled {
				event = workflow.EventResearchCompleteWithoutQC
			}
			if err = r.state.Advance(event); err != nil {
				return researchruntime.Result{}, err
			}
			if err = r.commit(ctx, config.CheckpointWriter, collectionState, config.CollectionQC, candidate, finalRounds, config.FinalQC.OpenIssues); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseFinalQC:
			verdict, err := r.finalQC.Review(ctx, config.FinalQC, candidate)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run final qc: %w", err)
			}
			emit(config.Observe, Event{
				Phase: workflow.PhaseFinalQC, Action: ActionDecision,
				Decision: string(verdict.Decision), CollectionRounds: r.state.CollectionRoundsUsed(),
				Round: finalRounds,
			})
			if verdict.Decision == qc.DecisionPass {
				if err = r.state.Advance(workflow.EventPass); err != nil {
					return researchruntime.Result{}, err
				}
				if err = r.commit(ctx, config.CheckpointWriter, collectionState, config.CollectionQC, candidate, finalRounds, config.FinalQC.OpenIssues); err != nil {
					return researchruntime.Result{}, err
				}
				continue
			}
			if verdict.Decision != qc.DecisionReviseResearch {
				return researchruntime.Result{}, fmt.Errorf("unsupported final qc decision %q", verdict.Decision)
			}
			if finalRounds >= config.MaxFinalQCRounds {
				return researchruntime.Result{}, fmt.Errorf("final qc rounds exhausted after %d rounds", finalRounds)
			}
			config.FinalQC.OpenIssues = append([]corespec.Issue(nil), verdict.Issues...)
			if err = r.state.Advance(workflow.EventFinalQCRetry); err != nil {
				return researchruntime.Result{}, err
			}
			if err = r.commit(ctx, config.CheckpointWriter, collectionState, config.CollectionQC, candidate, finalRounds, config.FinalQC.OpenIssues); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseComplete:
			return candidate, nil
		default:
			return researchruntime.Result{}, fmt.Errorf("unsupported workflow phase %q", r.state.Phase())
		}
	}
}

func (r *Runner) commit(
	ctx context.Context,
	writer CheckpointWriter,
	collectionState []collection.ActiveInformationNeedState,
	collectionQC collectionqc.Config,
	candidate researchruntime.Result,
	finalRounds int,
	finalQCOpenIssues []corespec.Issue,
) error {
	if writer == nil {
		return nil
	}
	checkpoint := Checkpoint{
		NextPhase: r.state.Phase(), Workflow: r.state.Snapshot(),
		CollectionState: cloneActiveInformationNeedStates(collectionState),
		CollectionQC: collection.CheckpointOutput{
			ArtifactIDs:      append([]string(nil), collectionQC.CheckpointArtifactIDs...),
			EmptyReason:      collectionQC.CheckpointEmptyReason,
			NeedDispositions: append([]collection.NeedDisposition(nil), collectionQC.NeedDispositions...),
		},
		Candidate: cloneResult(candidate), FinalRounds: finalRounds, FinalQCOpenIssues: cloneIssues(finalQCOpenIssues),
	}
	if err := writer.Commit(ctx, checkpoint); err != nil {
		return fmt.Errorf("commit workflow checkpoint for next phase %s: %w", checkpoint.NextPhase, err)
	}
	return nil
}

func cloneActiveInformationNeedStates(source []collection.ActiveInformationNeedState) []collection.ActiveInformationNeedState {
	result := make([]collection.ActiveInformationNeedState, len(source))
	for index, state := range source {
		result[index] = state
		result[index].InformationNeed.StopConditions = append([]collection.StopCondition(nil), state.InformationNeed.StopConditions...)
		result[index].UnsatisfiedConditionIDs = append([]string(nil), state.UnsatisfiedConditionIDs...)
	}
	return result
}

func cloneResult(source researchruntime.Result) researchruntime.Result {
	result := source
	if source.Value != nil {
		value := *source.Value
		result.Value = &value
	}
	if source.Artifacts != nil {
		result.Artifacts = make(map[string]string, len(source.Artifacts))
		maps.Copy(result.Artifacts, source.Artifacts)
	}
	return result
}

func cloneIssues(source []corespec.Issue) []corespec.Issue {
	result := make([]corespec.Issue, len(source))
	for i, issue := range source {
		result[i] = issue
		if issue.Path != nil {
			value := *issue.Path
			result[i].Path = &value
		}
		if issue.RepairHint != nil {
			value := *issue.RepairHint
			result[i].RepairHint = &value
		}
	}
	return result
}

func collectionQCDecision(result collectionqc.Result) string {
	if result.CollectionLimitExhausted {
		return "budget_exhausted"
	}
	for _, outcome := range result.Outcomes {
		if outcome.Resolution == collection.NeedResolutionUnresolved {
			return "needs_more"
		}
	}
	return "sufficient"
}

// collectionRoundPrompt drives the next Collection round from the frozen
// per-need outcomes instead of a global issue list. Unresolved needs must stay
// explicit so Collection continues genuine search only where the plan still
// demands it. Satisfied or otherwise terminal outcomes add no next-round work.
func collectionRoundPrompt(initialPrompt string, active []collection.ActiveInformationNeedState, outcomes []byte) string {
	if len(active) == 0 && len(outcomes) == 0 {
		return initialPrompt
	}
	document := struct {
		ActiveInformationNeeds []collection.ActiveInformationNeedState `json:"active_information_needs"`
		TerminalOutcomes       json.RawMessage                         `json:"information_need_outcomes,omitempty"`
	}{ActiveInformationNeeds: active}
	if len(outcomes) > 0 {
		document.TerminalOutcomes = json.RawMessage(outcomes)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return initialPrompt
	}
	return initialPrompt + "\n\nCollection QC state for the next round. Search only active needs and their remaining condition IDs; terminal outcomes are frozen and must not be reopened:\n" + string(encoded)
}

// researchOutcomesPrompt makes unresolved information needs visible to closed
// Research so it must represent them as uncertainty, never as absent facts.
// Fully satisfied plans add no uncertainty and keep the original prompt intact.
func researchOutcomesPrompt(initialPrompt string, outcomes []byte) string {
	if len(outcomes) == 0 {
		return initialPrompt
	}
	if !json.Valid(outcomes) {
		return initialPrompt
	}
	return initialPrompt + "\n\nComplete information_need_outcomes from Collection QC (represent unresolved needs as uncertainty, never as proven absence):\n" + string(outcomes)
}

func emit(observer func(Event), event Event) {
	if observer != nil {
		observer(event)
	}
}

func (r *Runner) validate(config Config) error {
	if r.state == nil {
		return fmt.Errorf("workflow state is required")
	}
	if r.collection == nil {
		return fmt.Errorf("collection runner is required")
	}
	if r.collectionQC == nil {
		return fmt.Errorf("collection qc runner is required")
	}
	if r.research == nil {
		return fmt.Errorf("research runner is required")
	}
	if config.FinalQCEnabled {
		if r.finalQC == nil {
			return fmt.Errorf("final qc runner is required")
		}
		if config.MaxFinalQCRounds <= 0 {
			return fmt.Errorf("final qc rounds exhausted before review")
		}
	}
	return nil
}
