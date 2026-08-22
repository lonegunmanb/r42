// Package coordinator composes the persistent Collection, Collection QC,
// Research, and optional Final QC phases for one research workflow instance.
package coordinator

import (
	"context"
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
	var researchIssues []corespec.Issue
	finalRounds := 0

	for {
		emit(config.Observe, Event{Phase: r.state.Phase(), Action: ActionStarted, CollectionRounds: r.state.CollectionRoundsUsed()})
		switch r.state.Phase() {
		case workflow.PhaseCollection:
			collectionConfig := config.Collection
			if len(researchIssues) > 0 {
				collectionConfig.InitialPrompt = issuePrompt("Collect additional evidence for these Final QC issues:", researchIssues)
			} else if previous := r.state.LastCollectionQCIssues(); len(previous) > 0 {
				issues := make([]corespec.Issue, len(previous))
				for index, message := range previous {
					issues[index] = corespec.Issue{Code: "collection_qc", Message: message}
				}
				collectionConfig.InitialPrompt = issuePrompt("Collection QC needs more evidence:", issues)
			}
			checkpoint, err := r.collection.Run(ctx, collectionConfig)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run collection: %w", err)
			}
			config.CollectionQC.CheckpointSnapshotIDs = append([]string(nil), checkpoint.SnapshotIDs...)
			config.CollectionQC.CheckpointEmptyReason = checkpoint.EmptyReason
			config.CollectionQC.CollectionExhausted = checkpoint.CollectionExhausted
			if err = r.state.Advance(workflow.EventCollectionCheckpoint); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseCollectionQC:
			result, err := r.collectionQC.Review(ctx, config.CollectionQC)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run collection qc: %w", err)
			}
			emit(config.Observe, Event{
				Phase: workflow.PhaseCollectionQC, Action: ActionDecision,
				Decision: string(result.Verdict.Decision), CollectionRounds: r.state.CollectionRoundsUsed(),
			})
			if result.Verdict.Decision == collectionqc.DecisionNeedsMore && !result.CollectionLimitExhausted {
				continue
			}
			if len(result.Verdict.Issues) > 0 {
				researchIssues = append([]corespec.Issue(nil), result.Verdict.Issues...)
			}
		case workflow.PhaseResearch:
			researchConfig := config.Research
			if len(researchIssues) > 0 {
				researchConfig.InitialPrompt = appendIssuePrompt(
					config.Research.InitialPrompt,
					"Address these review issues while completing the original task:",
					researchIssues,
				)
			}
			var err error
			candidate, err = r.research.Run(ctx, researchConfig)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run research: %w", err)
			}
			researchIssues = nil
			event := workflow.EventResearchComplete
			if !config.FinalQCEnabled {
				event = workflow.EventResearchCompleteWithoutQC
			}
			if err = r.state.Advance(event); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseFinalQC:
			finalRounds++
			verdict, err := r.finalQC.Review(ctx, config.FinalQC, candidate)
			if err != nil {
				return researchruntime.Result{}, fmt.Errorf("run final qc: %w", err)
			}
			emit(config.Observe, Event{
				Phase: workflow.PhaseFinalQC, Action: ActionDecision,
				Decision: string(verdict.Decision), CollectionRounds: r.state.CollectionRoundsUsed(),
			})
			if verdict.Decision == qc.DecisionPass {
				if err = r.state.Advance(workflow.EventPass); err != nil {
					return researchruntime.Result{}, err
				}
				continue
			}
			if finalRounds >= config.MaxFinalQCRounds {
				return researchruntime.Result{}, fmt.Errorf("final qc rounds exhausted after %d rounds", finalRounds)
			}
			researchIssues = append([]corespec.Issue(nil), verdict.Issues...)
			event := workflow.EventReviseResearch
			if verdict.Decision == qc.DecisionReopenCollection {
				event = workflow.EventReopenCollection
			}
			if err = r.state.Advance(event); err != nil {
				return researchruntime.Result{}, err
			}
		case workflow.PhaseComplete:
			return candidate, nil
		default:
			return researchruntime.Result{}, fmt.Errorf("unsupported workflow phase %q", r.state.Phase())
		}
	}
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
		fmt.Fprintf(&result, "\n- [%s] %s", issue.Code, issue.Message)
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
