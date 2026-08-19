// Package collectionqc implements semantic review of Collection checkpoints.
// Mechanical snapshot validation remains in the Collection protocol tools.
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
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
	"github.com/zclconf/go-cty/cty"
)

// Decision is the semantic outcome of one Collection QC checkpoint review.
type Decision string

const (
	DecisionSufficient Decision = "sufficient"
	DecisionNeedsMore  Decision = "needs_more"
)

// Verdict is submitted through the mandatory Collection QC verdict tool.
type Verdict struct {
	Decision Decision         `json:"decision"`
	Issues   []corespec.Issue `json:"issues,omitempty"`
}

// Validate enforces the Collection QC verdict protocol.
func (v Verdict) Validate() error {
	switch v.Decision {
	case DecisionSufficient:
		if len(v.Issues) != 0 {
			return errors.New("sufficient verdict must not contain issues")
		}
	case DecisionNeedsMore:
		if len(v.Issues) == 0 {
			return errors.New("needs_more verdict must contain at least one issue")
		}
	default:
		return fmt.Errorf("unsupported collection qc decision %q", v.Decision)
	}
	for index, issue := range v.Issues {
		if err := issue.Validate(); err != nil {
			return fmt.Errorf("issue %d: %w", index, err)
		}
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
	CheckpointSnapshotIDs []string
	MaxProtocolAttempts   int
	VerdictToolName       string
}

// Result reports the valid verdict and whether the collection budget forced
// Research to proceed with unresolved gaps.
type Result struct {
	Verdict                  Verdict
	CollectionLimitExhausted bool
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

	for _, id := range config.CheckpointSnapshotIDs {
		r.collection.Registry.MarkReviewed(id)
	}
	issueMessages := make([]string, len(verdict.Issues))
	for index, issue := range verdict.Issues {
		issueMessages[index] = issue.Message
	}
	r.collection.State.SetLastCollectionQCIssues(issueMessages)

	if verdict.Decision == DecisionSufficient {
		if err = r.collection.State.Advance(workflow.EventSufficient); err != nil {
			return Result{}, fmt.Errorf("advance sufficient collection qc verdict: %w", err)
		}
		return Result{Verdict: verdict}, nil
	}
	if r.collection.State.CollectionLimitExhausted() {
		if err = r.collection.State.Advance(workflow.EventCollectionLimitExhausted); err != nil {
			return Result{}, fmt.Errorf("advance exhausted collection qc verdict: %w", err)
		}
		return Result{Verdict: verdict, CollectionLimitExhausted: true}, nil
	}
	if err = r.collection.State.Advance(workflow.EventNeedsMore); err != nil {
		return Result{}, fmt.Errorf("advance needs_more collection qc verdict: %w", err)
	}
	return Result{Verdict: verdict}, nil
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
	Task                  Task              `json:"task"`
	Criteria              map[string]string `json:"criteria"`
	CheckpointSnapshotIDs []string          `json:"checkpoint_snapshot_ids"`
	PreviousIssues        []string          `json:"previous_issues"`
	CollectionRoundsUsed  int               `json:"collection_rounds_used"`
	CollectionCanReopen   bool              `json:"collection_can_reopen"`
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
		Task:                  config.Task,
		Criteria:              criteriaMap,
		CheckpointSnapshotIDs: append([]string{}, config.CheckpointSnapshotIDs...),
		PreviousIssues:        collectionContext.State.LastCollectionQCIssues(),
		CollectionRoundsUsed:  collectionContext.State.CollectionRoundsUsed(),
		CollectionCanReopen:   !collectionContext.State.CollectionLimitExhausted(),
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
	verdict.Issues = append([]corespec.Issue{}, verdict.Issues...)
	r.mu.Lock()
	r.verdicts = append(r.verdicts, verdict)
	r.mu.Unlock()
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
