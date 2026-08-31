// Package coordinator composes the persistent Collection, Collection QC,
// Research, and optional Final QC phases for one research workflow instance.
package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	if err := r.state.Begin(); err != nil {
		return researchruntime.Result{}, err
	}
	var candidate researchruntime.Result
	var finalQCIssues []corespec.Issue
	var collectionState []collection.ActiveInformationNeedState
	finalRounds := 0
	revisionRounds := 0

	for {
		phase := r.state.Phase()
		event := Event{Phase: phase, Action: ActionStarted, CollectionRounds: r.state.CollectionRoundsUsed()}
		switch phase {
		case workflow.PhaseCollection, workflow.PhaseCollectionQC:
			event.Round = r.state.CollectionRoundsUsed()
		case workflow.PhaseResearch:
			if len(finalQCIssues) > 0 {
				revisionRounds++
				event.Round = revisionRounds
				event.IsRevision = true
			}
		case workflow.PhaseFinalQC:
			finalRounds++
			event.Round = finalRounds
		}
		emit(config.Observe, event)
		switch r.state.Phase() {
		case workflow.PhaseCollection:
			collectionConfig := config.Collection
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
				continue
			}
		case workflow.PhaseResearch:
			researchConfig := config.Research
			researchConfig.InitialPrompt = researchOutcomesPrompt(researchConfig.InitialPrompt, r.state.InformationNeedOutcomes())
			if len(finalQCIssues) > 0 {
				researchConfig.InitialPrompt = appendIssuePrompt(
					researchConfig.InitialPrompt,
					"Address these Final QC issues while completing the original task:",
					finalQCIssues,
				)
			}
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
		case workflow.PhaseFinalQC:
			config.FinalQC.OpenIssues = append([]corespec.Issue(nil), finalQCIssues...)
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
				continue
			}
			if verdict.Decision != qc.DecisionReviseResearch {
				return researchruntime.Result{}, fmt.Errorf("unsupported final qc decision %q", verdict.Decision)
			}
			if finalRounds >= config.MaxFinalQCRounds {
				return researchruntime.Result{}, fmt.Errorf("final qc rounds exhausted after %d rounds", finalRounds)
			}
			finalQCIssues = append([]corespec.Issue(nil), verdict.Issues...)
			if err = r.state.Advance(workflow.EventReviseResearch); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseComplete:
			return candidate, nil
		default:
			return researchruntime.Result{}, fmt.Errorf("unsupported workflow phase %q", r.state.Phase())
		}
	}
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

func issuePrompt(header string, issues []corespec.Issue) string {
	var result strings.Builder
	result.WriteString(header)
	for _, issue := range issues {
		fmt.Fprintf(&result, "\n- [%s] [%s] %s", issue.ID, issue.Code, issue.Message)
	}
	return result.String()
}

func appendIssuePrompt(initialPrompt, header string, issues []corespec.Issue) string {
	revisionPrompt := issuePrompt(header, issues)
	if strings.TrimSpace(initialPrompt) == "" {
		return revisionPrompt
	}
	return strings.TrimRight(initialPrompt, "\r\n") + "\n\n" + revisionPrompt
}
