// Package collectionqc implements semantic review of Collection checkpoints.
// Mechanical evidence validation remains in the Collection protocol tools.
package collectionqc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/collection"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
	"github.com/zclconf/go-cty/cty"
)

// Verdict is submitted through the mandatory Collection QC verdict tool.
type Verdict struct {
	Assessments []collection.QCAssessment `json:"assessments"`
}

// Validate enforces the Collection QC verdict protocol. Mechanical shape
// checks for assessments happen against the frozen plan in the Collection
// context, so the verdict only needs to be structurally valid here.
func (v Verdict) Validate() error {
	if len(v.Assessments) == 0 {
		return errors.New("collection qc verdict must contain at least one assessment")
	}
	seen := make(map[string]struct{}, len(v.Assessments))
	for index, assessment := range v.Assessments {
		if strings.TrimSpace(assessment.InformationNeedID) == "" {
			return fmt.Errorf("assessment %d: information_need_id is required", index)
		}
		if _, duplicate := seen[assessment.InformationNeedID]; duplicate {
			return fmt.Errorf("assessment %d: information need %q appears more than once", index, assessment.InformationNeedID)
		}
		seen[assessment.InformationNeedID] = struct{}{}
	}
	return nil
}

// Task contains the author-provided task instructions visible to QC.
type Task struct {
	SystemPrompt string  `json:"system_prompt"`
	Prompt       *string `json:"prompt,omitempty"`
}

// Config controls one checkpoint review.
type Config struct {
	Task                  Task
	Criteria              cty.Value
	CheckpointArtifactIDs []string
	CheckpointEmptyReason string
	NeedDispositions      []collection.NeedDisposition
	MaxProtocolAttempts   int
	VerdictToolName       string
}

// Result reports the valid verdict, the terminal per-need outcomes, and
// whether the Collection round budget forced Research to proceed with
// unresolved gaps.
type Result struct {
	Verdict                     Verdict
	Outcomes                    []collection.InformationNeedOutcome
	ActiveInformationNeeds      []collection.InformationNeed
	ActiveInformationNeedStates []collection.ActiveInformationNeedState
	CollectionLimitExhausted    bool
}

// Session is the persistent Collection QC model session.
type Session interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
}

// Runner reviews checkpoints and applies valid verdicts to workflow state.
type Runner struct {
	session    Session
	verdicts   *VerdictRecorder
	collection *collection.Context
}

// NewRunner creates a Collection QC runner.
func NewRunner(session Session, verdicts *VerdictRecorder, collectionContext *collection.Context) *Runner {
	return &Runner{session: session, verdicts: verdicts, collection: collectionContext}
}

// Review obtains one valid semantic verdict and advances the review cursor.
func (r *Runner) Review(ctx context.Context, config Config) (Result, error) {
	if err := r.validate(config); err != nil {
		return Result{}, err
	}
	prompt, err := contextPrompt(config, r.collection)
	if err != nil {
		return Result{}, err
	}
	var verdict Verdict
	for attempt := 1; ; attempt++ {
		if _, err = r.session.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt}); err != nil {
			return Result{}, fmt.Errorf("send collection qc prompt: %w", err)
		}
		verdicts, failure := r.verdicts.drain()
		if failure != nil {
			return Result{}, fmt.Errorf("collection qc verdict tool failed: %w", failure)
		}
		if len(verdicts) > 0 {
			verdict = verdicts[0]
			break
		}
		if attempt >= config.MaxProtocolAttempts {
			return Result{}, fmt.Errorf(
				"collection qc verdict protocol attempts exhausted after %d attempts (maximum %d)",
				attempt,
				config.MaxProtocolAttempts,
			)
		}
		prompt = fmt.Sprintf("You must call the %q tool before Collection QC can finish.", config.VerdictToolName)
	}

	for _, id := range config.CheckpointArtifactIDs {
		if err = r.collection.MarkEvidenceReviewed(id); err != nil {
			return Result{}, fmt.Errorf("publish reviewed evidence artifact %q: %w", id, err)
		}
	}
	budgetExhausted := r.collection.State.CollectionLimitExhausted()
	assessment := r.collection.ApplyQCAssessments(config.NeedDispositions, verdict.Assessments, budgetExhausted, len(config.CheckpointArtifactIDs) > 0)
	if !assessment.Accepted {
		return Result{}, fmt.Errorf("apply collection qc assessments: %s", assessment.Issues[0].Message)
	}
	collectionLimitExhausted := false
	for _, outcome := range assessment.Outcomes {
		if outcome.TerminationReason == collection.TerminationBudgetExhausted {
			collectionLimitExhausted = true
			break
		}
	}
	if len(assessment.Outcomes) > 0 {
		encoded, err := json.Marshal(assessment.Outcomes)
		if err != nil {
			return Result{}, fmt.Errorf("encode information need outcomes: %w", err)
		}
		r.collection.State.SetInformationNeedOutcomes(encoded)
	}
	if assessment.AllTerminal {
		if err = r.collection.State.Advance(workflow.EventSufficient); err != nil {
			return Result{}, fmt.Errorf("advance terminal collection qc verdict: %w", err)
		}
		return Result{
			Verdict: verdict, Outcomes: assessment.Outcomes,
			ActiveInformationNeeds:      r.collection.ActiveInformationNeeds(),
			ActiveInformationNeedStates: r.collection.ActiveInformationNeedStates(),
			CollectionLimitExhausted:    collectionLimitExhausted,
		}, nil
	}
	if err = r.collection.State.Advance(workflow.EventNeedsMore); err != nil {
		return Result{}, fmt.Errorf("advance needs_more collection qc verdict: %w", err)
	}
	r.collection.BeginNextCollectionRound()
	return Result{
		Verdict: verdict, Outcomes: assessment.Outcomes,
		ActiveInformationNeeds:      r.collection.ActiveInformationNeeds(),
		ActiveInformationNeedStates: r.collection.ActiveInformationNeedStates(),
	}, nil
}

func (r *Runner) validate(config Config) error {
	if r.session == nil {
		return errors.New("collection qc session is required")
	}
	if r.verdicts == nil {
		return errors.New("collection qc verdict recorder is required")
	}
	if err := r.collection.Validate(); err != nil {
		return err
	}
	if r.collection.State.Phase() != workflow.PhaseCollectionQC {
		return fmt.Errorf("collection qc requires phase %s", workflow.PhaseCollectionQC)
	}
	if config.MaxProtocolAttempts <= 0 {
		return errors.New("collection qc maximum protocol attempts must be positive")
	}
	if strings.TrimSpace(config.VerdictToolName) == "" {
		return errors.New("collection qc verdict tool name is required")
	}
	return nil
}

type contextDocument struct {
	Task                        Task                                    `json:"task"`
	Criteria                    map[string]string                       `json:"criteria"`
	CheckpointArtifactIDs       []string                                `json:"checkpoint_artifact_ids"`
	CheckpointEmptyReason       string                                  `json:"checkpoint_empty_reason,omitempty"`
	InformationNeeds            []collection.InformationNeed            `json:"information_needs"`
	ActiveInformationNeeds      []collection.InformationNeed            `json:"active_information_needs"`
	ActiveInformationNeedStates []collection.ActiveInformationNeedState `json:"active_information_need_states"`
	PreviousOutcomes            []collection.InformationNeedOutcome     `json:"information_need_outcomes"`
	CollectionRoundsUsed        int                                     `json:"collection_rounds_used"`
}

func contextPrompt(config Config, collectionContext *collection.Context) (string, error) {
	criteria := researchspec.DefaultCollectionQCCriteria()
	if config.Criteria.Type() != cty.NilType && !config.Criteria.IsNull() {
		criteria = config.Criteria
	}
	criteriaMap := make(map[string]string, criteria.LengthInt())
	for name, value := range criteria.AsValueMap() {
		criteriaMap[name] = value.AsString()
	}
	document := contextDocument{
		Task:                        config.Task,
		Criteria:                    criteriaMap,
		CheckpointArtifactIDs:       append([]string{}, config.CheckpointArtifactIDs...),
		CheckpointEmptyReason:       config.CheckpointEmptyReason,
		InformationNeeds:            collectionContext.InformationNeeds(),
		ActiveInformationNeeds:      collectionContext.ActiveInformationNeeds(),
		ActiveInformationNeedStates: collectionContext.ActiveInformationNeedStates(),
		PreviousOutcomes:            collectionContext.InformationNeedOutcomes(),
		CollectionRoundsUsed:        collectionContext.State.CollectionRoundsUsed(),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode collection qc context: %w", err)
	}
	return string(encoded), nil
}

// VerdictRecorder captures valid verdict tool calls from one persistent QC
// session. Invalid calls are retained as protocol failures.
type VerdictRecorder struct {
	mu       sync.Mutex
	verdicts []Verdict
	failure  error
}

// NewVerdictRecorder creates an empty recorder.
func NewVerdictRecorder() *VerdictRecorder { return &VerdictRecorder{} }

// Record validates and records one verdict.
func (r *VerdictRecorder) Record(verdict Verdict) error {
	if err := verdict.Validate(); err != nil {
		return err
	}
	verdict.Assessments = append([]collection.QCAssessment{}, verdict.Assessments...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.verdicts) > 0 {
		return errors.New("r42_collection_qc_verdict may be accepted exactly once in each Collection QC round")
	}
	r.verdicts = append(r.verdicts, verdict)
	return nil
}

// RecordError preserves the first verdict handler failure.
func (r *VerdictRecorder) RecordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.failure == nil {
		r.failure = err
	}
	r.mu.Unlock()
}

func (r *VerdictRecorder) drain() ([]Verdict, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	verdicts := r.verdicts
	failure := r.failure
	r.verdicts = nil
	r.failure = nil
	return verdicts, failure
}
