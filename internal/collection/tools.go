// Package collection implements the mandatory Collection protocol tools:
// evidence-artifact registration, checkpoint submission, and the acquisition gate.
// The tools return structured ToolResponse envelopes so the model can repair
// rejections, while infrastructure failures surface as Go errors.
package collection

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
)

// Context wires the workflow state machine and Artifact Registry for one
// Collection phase. Checkpoint and review state are deliberately transient.
type Context struct {
	Workspace string
	State     *workflow.State
	Artifacts *artifactpkg.Registry
	batchSize int

	mu                    sync.Mutex
	evidence              []string
	reviewed              map[string]struct{}
	checkpointed          map[string]struct{}
	targets               []artifactTarget
	informationNeeds      []InformationNeed
	needStates            []informationNeedState
	activeToolCalls       int
	checkpointAccepted    bool
	lastRoundHadArtifacts bool
}

type informationNeedState struct {
	need                InformationNeed
	previousUnsatisfied map[string]struct{}
	assessed            bool
	stallStreak         int
	outcome             *InformationNeedOutcome
}

type artifactTarget struct {
	path      string
	directory bool
}

// NewContext creates a Collection protocol context with default batch size 10
// and unlimited collection rounds.
func NewContext(workspace string, batchSize int, maxCollectionRounds *int) *Context {
	return NewContextWithArtifactRegistry(workspace, batchSize, maxCollectionRounds, nil)
}

// NewContextWithArtifactRegistry uses the run artifact registry as the only
// evidence metadata store.
func NewContextWithArtifactRegistry(
	workspace string,
	batchSize int,
	maxCollectionRounds *int,
	artifacts *artifactpkg.Registry,
) *Context {
	if batchSize == 0 {
		batchSize = workflow.DefaultBatchSize
	}
	if artifacts == nil {
		artifacts = artifactpkg.NewRegistry()
	}
	return &Context{
		Workspace:    workspace,
		State:        workflow.New(workflow.Config{MaxCollectionRounds: maxCollectionRounds, BatchSize: batchSize}),
		Artifacts:    artifacts,
		batchSize:    batchSize,
		reviewed:     make(map[string]struct{}),
		checkpointed: make(map[string]struct{}),
	}
}

// MarkEvidenceReviewed records temporary Collection-QC protocol state.
func (c *Context) MarkEvidenceReviewed(id string) error {
	if c == nil {
		return errors.New("collection context is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reviewed[id] = struct{}{}
	return nil
}

// EvidenceArtifactIDs returns all registered evidence in registration order.
func (c *Context) EvidenceArtifactIDs() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.evidence...)
}

// ReviewedEvidenceArtifactIDs returns reviewed evidence IDs in registration order.
func (c *Context) ReviewedEvidenceArtifactIDs() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]string, 0)
	for _, id := range c.evidence {
		if _, ok := c.reviewed[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

// Validate verifies the context configuration without mutating workflow
// state.
func (c *Context) Validate() error {
	if c == nil {
		return errors.New("collection context is required")
	}
	if c.Workspace == "" {
		return errors.New("collection workspace is required")
	}
	if c.batchSize <= 0 {
		return errors.New("collection batch size must be positive")
	}
	return nil
}

// BeginWorkflow starts the workflow state machine once.
func (c *Context) BeginWorkflow() error {
	if err := c.Validate(); err != nil {
		return err
	}
	return c.State.Begin()
}

// InformationNeedInput is the model-authored, immutable search direction.
type InformationNeedInput struct {
	Question       string               `json:"question"`
	StopConditions []StopConditionInput `json:"stop_conditions"`
}

// StopConditionInput describes evidence that makes one information need sufficient.
type StopConditionInput struct {
	Condition string `json:"condition"`
}

// InformationNeed is the frozen, R42-identified search direction.
type InformationNeed struct {
	ID             string          `json:"id"`
	Question       string          `json:"question"`
	StopConditions []StopCondition `json:"stop_conditions"`
}

// StopCondition is the frozen, R42-identified stop condition.
type StopCondition struct {
	ID        string `json:"id"`
	Condition string `json:"condition"`
}

// InformationNeedsArgs is the input of r42_set_information_needs.
type InformationNeedsArgs struct {
	InformationNeeds []InformationNeedInput `json:"information_needs"`
}

// InformationNeedsOutput returns the canonical frozen plan.
type InformationNeedsOutput struct {
	InformationNeeds []InformationNeed `json:"information_needs"`
}

// InformationNeedsHandler owns the one-time information-needs plan tool.
type InformationNeedsHandler struct{ context *Context }

// NewInformationNeedsHandler creates the plan tool handler.
func NewInformationNeedsHandler(context *Context) *InformationNeedsHandler {
	return &InformationNeedsHandler{context: context}
}

// Set validates and permanently freezes the initial collection plan.
func (h *InformationNeedsHandler) Set(args InformationNeedsArgs) corespec.ToolResponse[InformationNeedsOutput] {
	if h.context == nil {
		return infrastructureRejection[InformationNeedsOutput]("context_validation", errors.New("collection context is required"))
	}
	h.context.mu.Lock()
	defer h.context.mu.Unlock()
	if err := h.context.BeginWorkflow(); err != nil {
		return infrastructureRejection[InformationNeedsOutput]("context_validation", err)
	}
	if len(h.context.informationNeeds) > 0 {
		return rejection[InformationNeedsOutput]("information_needs_frozen", "information needs are already frozen and cannot be changed")
	}
	issues := validateInformationNeeds(args.InformationNeeds)
	if len(issues) > 0 {
		return corespec.ToolResponse[InformationNeedsOutput]{Issues: issues}
	}
	needs := make([]InformationNeed, 0, len(args.InformationNeeds))
	for needIndex, input := range args.InformationNeeds {
		needID := fmt.Sprintf("NEED-%03d", needIndex+1)
		conditions := make([]StopCondition, 0, len(input.StopConditions))
		for conditionIndex, condition := range input.StopConditions {
			conditions = append(conditions, StopCondition{
				ID: fmt.Sprintf("%s-SC-%03d", needID, conditionIndex+1), Condition: strings.TrimSpace(condition.Condition),
			})
		}
		needs = append(needs, InformationNeed{ID: needID, Question: strings.TrimSpace(input.Question), StopConditions: conditions})
	}
	h.context.informationNeeds = needs
	h.context.needStates = make([]informationNeedState, 0, len(needs))
	for _, need := range needs {
		h.context.needStates = append(h.context.needStates, informationNeedState{need: need})
	}
	return corespec.ToolResponse[InformationNeedsOutput]{Accepted: true, Output: &InformationNeedsOutput{InformationNeeds: cloneInformationNeeds(needs)}}
}

func validateInformationNeeds(needs []InformationNeedInput) []corespec.Issue {
	issues := make([]corespec.Issue, 0)
	if len(needs) == 0 || len(needs) > 10 {
		issues = append(issues, corespec.Issue{Code: "information_needs", Message: "information_needs must contain between 1 and 10 items"})
	}
	for needIndex, need := range needs {
		if strings.TrimSpace(need.Question) == "" {
			issues = append(issues, corespec.Issue{Code: "information_need_question", Message: fmt.Sprintf("information_needs[%d].question is required", needIndex)})
		}
		if len(need.StopConditions) == 0 || len(need.StopConditions) > 5 {
			issues = append(issues, corespec.Issue{Code: "stop_conditions", Message: fmt.Sprintf("information_needs[%d].stop_conditions must contain between 1 and 5 items", needIndex)})
		}
		for conditionIndex, condition := range need.StopConditions {
			if strings.TrimSpace(condition.Condition) == "" {
				issues = append(issues, corespec.Issue{Code: "stop_condition", Message: fmt.Sprintf("information_needs[%d].stop_conditions[%d].condition is required", needIndex, conditionIndex)})
			}
		}
	}
	return issues
}

func cloneInformationNeeds(needs []InformationNeed) []InformationNeed {
	result := make([]InformationNeed, len(needs))
	for index, need := range needs {
		result[index] = need
		result[index].StopConditions = append([]StopCondition{}, need.StopConditions...)
	}
	return result
}

// InformationNeeds returns the frozen plan, if Collection has submitted one.
func (c *Context) InformationNeeds() []InformationNeed {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneInformationNeeds(c.informationNeeds)
}

func (c *Context) informationNeedsFrozen() bool {
	return len(c.informationNeeds) > 0
}

// CollectionToolGate permits a non-read-only Collection tool call only after
// the plan is frozen and before this round's accepted checkpoint. This is a
// formal protocol invariant, not an optional switch.
func (c *Context) CollectionToolGate() error {
	if c == nil {
		return errors.New("collection context is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.collectionToolGateLocked()
}

func (c *Context) collectionToolGateLocked() error {
	if !c.informationNeedsFrozen() {
		return errors.New("call r42_set_information_needs before any non-read-only Collection tool")
	}
	if c.checkpointAccepted {
		return errors.New("collection checkpoint already accepted for this round; wait for Collection QC")
	}
	return nil
}

// BeginCollectionToolCall registers one non-read-only tool call for its full
// execution lifetime. The returned release function is safe to call once via
// defer. Checkpoint submission is rejected while any such call is in flight.
func (c *Context) BeginCollectionToolCall() (func(), error) {
	if c == nil {
		return nil, errors.New("collection context is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.collectionToolGateLocked(); err != nil {
		return nil, err
	}
	c.activeToolCalls++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.activeToolCalls--
			c.mu.Unlock()
		})
	}, nil
}

// BeginNextCollectionRound reopens non-read-only Collection tools after a
// valid QC verdict returns the workflow to Collection.
func (c *Context) BeginNextCollectionRound() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checkpointAccepted = false
}

// AssessmentStatus is Collection QC's semantic assessment for one active need.
type AssessmentStatus string

const (
	AssessmentSufficient AssessmentStatus = "sufficient"
	AssessmentNeedsMore  AssessmentStatus = "needs_more"
)

// EvidenceProgress describes whether the checkpoint materially reduced uncertainty.
type EvidenceProgress string

const (
	EvidenceProgressMaterial EvidenceProgress = "material"
	EvidenceProgressNone     EvidenceProgress = "none"
)

// QCAssessment is the machine-checkable part of a Collection QC verdict.
type QCAssessment struct {
	InformationNeedID       string           `json:"information_need_id"`
	Status                  AssessmentStatus `json:"status"`
	UnsatisfiedConditionIDs []string         `json:"unsatisfied_condition_ids"`
	EvidenceProgress        EvidenceProgress `json:"evidence_progress"`
}

// NeedResolution records whether a need was satisfied or remains unresolved.
type NeedResolution string

const (
	NeedResolutionSatisfied  NeedResolution = "satisfied"
	NeedResolutionUnresolved NeedResolution = "unresolved"
)

// TerminationReason explains an unresolved terminal outcome.
type TerminationReason string

const (
	TerminationSearchStalled   TerminationReason = "search_stalled"
	TerminationBudgetExhausted TerminationReason = "budget_exhausted"
)

// InformationNeedOutcome is the immutable result passed to later phases.
type InformationNeedOutcome struct {
	InformationNeedID string            `json:"information_need_id"`
	Question          string            `json:"question"`
	StopConditions    []StopCondition   `json:"stop_conditions"`
	Resolution        NeedResolution    `json:"resolution"`
	TerminationReason TerminationReason `json:"termination_reason,omitempty"`
}

// AssessmentResult is the result of applying one complete QC round.
type AssessmentResult struct {
	Accepted    bool                     `json:"accepted"`
	Issues      []corespec.Issue         `json:"issues,omitempty"`
	Outcomes    []InformationNeedOutcome `json:"outcomes"`
	AllTerminal bool                     `json:"all_terminal"`
}

// ValidateQCAssessments checks a verdict without changing lifecycle state.
func (c *Context) ValidateQCAssessments(assessments []QCAssessment, hasNewArtifacts bool) []corespec.Issue {
	if c == nil {
		return []corespec.Issue{{Code: "context_validation", Message: "collection context is required"}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return validateAssessments(c.activeNeedStates(), assessments, hasNewArtifacts)
}

// ApplyQCAssessments validates one complete QC verdict and updates per-need
// lifecycle state. It intentionally accepts no free-text issue channel.
// hasNewArtifacts reports whether this checkpoint added evidence artifacts; a
// round without new artifacts cannot claim material evidence progress.
func (c *Context) ApplyQCAssessments(
	dispositions []NeedDisposition,
	assessments []QCAssessment,
	budgetExhausted bool,
	hasNewArtifacts bool,
) AssessmentResult {
	if c == nil {
		return AssessmentResult{Issues: []corespec.Issue{{Code: "context_validation", Message: "collection context is required"}}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	active := c.activeNeedStates()
	if issues := validateNeedDispositions(activeInformationNeeds(active), dispositions); len(issues) > 0 {
		return AssessmentResult{Issues: issues}
	}
	if issues := validateAssessments(active, assessments, hasNewArtifacts); len(issues) > 0 {
		return AssessmentResult{Issues: issues}
	}

	dispositionByID := make(map[string]NeedDisposition, len(dispositions))
	for _, disposition := range dispositions {
		dispositionByID[disposition.InformationNeedID] = disposition
	}
	assessmentByID := make(map[string]QCAssessment, len(assessments))
	for _, assessment := range assessments {
		assessmentByID[assessment.InformationNeedID] = assessment
	}
	for index := range c.needStates {
		state := &c.needStates[index]
		if state.outcome != nil {
			continue
		}
		assessment := assessmentByID[state.need.ID]
		if assessment.Status == AssessmentSufficient {
			state.outcome = newInformationNeedOutcome(state.need, NeedResolutionSatisfied, "")
			continue
		}
		current := conditionSet(assessment.UnsatisfiedConditionIDs)
		shrank := state.assessed && len(current) < len(state.previousUnsatisfied)
		state.previousUnsatisfied = current
		state.assessed = true
		disposition := dispositionByID[state.need.ID]
		if disposition.SearchDisposition == SearchDispositionStalled && assessment.EvidenceProgress == EvidenceProgressNone && !shrank {
			state.stallStreak++
		} else {
			state.stallStreak = 0
		}
		if state.stallStreak >= 2 {
			state.outcome = newInformationNeedOutcome(state.need, NeedResolutionUnresolved, TerminationSearchStalled)
		}
	}
	if budgetExhausted {
		for index := range c.needStates {
			state := &c.needStates[index]
			if state.outcome == nil {
				state.outcome = newInformationNeedOutcome(state.need, NeedResolutionUnresolved, TerminationBudgetExhausted)
			}
		}
	}
	outcomes := c.informationNeedOutcomesLocked()
	return AssessmentResult{Accepted: true, Outcomes: outcomes, AllTerminal: len(outcomes) == len(c.needStates)}
}

func newInformationNeedOutcome(need InformationNeed, resolution NeedResolution, reason TerminationReason) *InformationNeedOutcome {
	return &InformationNeedOutcome{
		InformationNeedID: need.ID,
		Question:          need.Question,
		StopConditions:    append([]StopCondition{}, need.StopConditions...),
		Resolution:        resolution,
		TerminationReason: reason,
	}
}

func (c *Context) activeNeedStates() []informationNeedState {
	active := make([]informationNeedState, 0, len(c.needStates))
	for _, state := range c.needStates {
		if state.outcome == nil {
			active = append(active, state)
		}
	}
	return active
}

func activeInformationNeeds(states []informationNeedState) []InformationNeed {
	needs := make([]InformationNeed, 0, len(states))
	for _, state := range states {
		needs = append(needs, state.need)
	}
	return needs
}

func validateAssessments(states []informationNeedState, assessments []QCAssessment, hasNewArtifacts bool) []corespec.Issue {
	if len(assessments) != len(states) {
		return []corespec.Issue{{Code: "assessments", Message: "assessments must contain every active information need exactly once"}}
	}
	stateByID := make(map[string]informationNeedState, len(states))
	for _, state := range states {
		stateByID[state.need.ID] = state
	}
	seen := make(map[string]struct{}, len(assessments))
	issues := make([]corespec.Issue, 0)
	for index, assessment := range assessments {
		state, found := stateByID[assessment.InformationNeedID]
		if !found {
			issues = append(issues, corespec.Issue{Code: "assessments", Message: fmt.Sprintf("assessments[%d] names unknown or closed information need %q", index, assessment.InformationNeedID)})
			continue
		}
		if _, duplicate := seen[assessment.InformationNeedID]; duplicate {
			issues = append(issues, corespec.Issue{Code: "assessments", Message: fmt.Sprintf("information need %q appears more than once", assessment.InformationNeedID)})
		}
		seen[assessment.InformationNeedID] = struct{}{}
		if assessment.Status != AssessmentSufficient && assessment.Status != AssessmentNeedsMore {
			issues = append(issues, corespec.Issue{Code: "assessment_status", Message: fmt.Sprintf("assessments[%d].status must be sufficient or needs_more", index)})
		}
		if assessment.EvidenceProgress != EvidenceProgressMaterial && assessment.EvidenceProgress != EvidenceProgressNone {
			issues = append(issues, corespec.Issue{Code: "evidence_progress", Message: fmt.Sprintf("assessments[%d].evidence_progress must be material or none", index)})
		}
		if !hasNewArtifacts && assessment.EvidenceProgress == EvidenceProgressMaterial {
			issues = append(issues, corespec.Issue{Code: "evidence_progress", Message: fmt.Sprintf("assessments[%d].evidence_progress must be none when the round added no evidence artifacts", index)})
		}
		if assessment.Status == AssessmentSufficient && len(assessment.UnsatisfiedConditionIDs) > 0 {
			issues = append(issues, corespec.Issue{Code: "unsatisfied_condition_ids", Message: fmt.Sprintf("assessments[%d] is sufficient and must not list unsatisfied conditions", index)})
		}
		if assessment.Status == AssessmentNeedsMore && len(assessment.UnsatisfiedConditionIDs) == 0 {
			issues = append(issues, corespec.Issue{Code: "unsatisfied_condition_ids", Message: fmt.Sprintf("assessments[%d] needs_more and must list unsatisfied conditions", index)})
		}
		allowed := make(map[string]struct{}, len(state.need.StopConditions))
		for _, condition := range state.need.StopConditions {
			allowed[condition.ID] = struct{}{}
		}
		current := conditionSet(assessment.UnsatisfiedConditionIDs)
		if len(current) != len(assessment.UnsatisfiedConditionIDs) {
			issues = append(issues, corespec.Issue{Code: "unsatisfied_condition_ids", Message: fmt.Sprintf("assessments[%d] contains duplicate condition IDs", index)})
		}
		for conditionID := range current {
			if _, allowedCondition := allowed[conditionID]; !allowedCondition {
				issues = append(issues, corespec.Issue{Code: "unsatisfied_condition_ids", Message: fmt.Sprintf("assessments[%d] names unknown condition %q", index, conditionID)})
			}
			if state.assessed {
				if _, wasUnsatisfied := state.previousUnsatisfied[conditionID]; !wasUnsatisfied {
					issues = append(issues, corespec.Issue{Code: "unsatisfied_condition_ids", Message: fmt.Sprintf("assessments[%d] reopens or adds condition %q", index, conditionID)})
				}
			}
		}
	}
	if len(issues) > 0 {
		return issues
	}
	for _, state := range states {
		if _, found := seen[state.need.ID]; !found {
			return []corespec.Issue{{Code: "assessments", Message: "assessments must contain every active information need exactly once"}}
		}
	}
	return nil
}

func conditionSet(ids []string) map[string]struct{} {
	result := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result
}

// ActiveInformationNeeds returns only needs that are still open for Collection.
func (c *Context) ActiveInformationNeeds() []InformationNeed {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneInformationNeeds(activeInformationNeeds(c.activeNeedStates()))
}

// ActiveInformationNeedState is one active need plus its remaining unsatisfied
// stop-condition IDs after the most recent Collection QC round.
type ActiveInformationNeedState struct {
	InformationNeed         InformationNeed `json:"information_need"`
	UnsatisfiedConditionIDs []string        `json:"unsatisfied_condition_ids"`
}

// ActiveInformationNeedStates returns every active need with its remaining
// unsatisfied condition IDs, for driving the next Collection round prompt.
func (c *Context) ActiveInformationNeedStates() []ActiveInformationNeedState {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]ActiveInformationNeedState, 0, len(c.needStates))
	for _, state := range c.needStates {
		if state.outcome != nil {
			continue
		}
		ids := make([]string, 0, len(state.previousUnsatisfied))
		for id := range state.previousUnsatisfied {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		need := state.need
		need.StopConditions = append([]StopCondition{}, state.need.StopConditions...)
		result = append(result, ActiveInformationNeedState{
			InformationNeed:         need,
			UnsatisfiedConditionIDs: ids,
		})
	}
	return result
}

// InformationNeedOutcomes returns immutable terminal results in plan order.
func (c *Context) InformationNeedOutcomes() []InformationNeedOutcome {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.informationNeedOutcomesLocked()
}

func (c *Context) informationNeedOutcomesLocked() []InformationNeedOutcome {
	result := make([]InformationNeedOutcome, 0, len(c.needStates))
	for _, state := range c.needStates {
		if state.outcome != nil {
			outcome := *state.outcome
			outcome.StopConditions = append([]StopCondition{}, state.outcome.StopConditions...)
			result = append(result, outcome)
		}
	}
	return result
}

// Gate exposes the acquisition gate for the Collection session's acquisition
// tools.
func (c *Context) Gate() *AcquisitionGate {
	return &AcquisitionGate{state: c.State}
}

// AddArtifactTarget permits r42_save_artifact to write to a declared file or
// to a new Markdown file below a declared directory.
func (c *Context) AddArtifactTarget(path string, directory bool) error {
	if c == nil {
		return errors.New("collection context is required")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve artifact target: %w", err)
	}
	workspace, err := filepath.Abs(c.Workspace)
	if err != nil {
		return fmt.Errorf("resolve collection workspace: %w", err)
	}
	if !pathWithin(workspace, resolved) {
		return fmt.Errorf("artifact target %q is outside the collection workspace", path)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targets = append(c.targets, artifactTarget{path: resolved, directory: directory})
	return nil
}

// AllowsArtifactPath reports whether a path is inside a configured directory
// target or exactly matches a configured file target.
func (c *Context) AllowsArtifactPath(path string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, target := range c.targets {
		if target.directory && pathWithin(target.path, path) {
			return true
		}
		if !target.directory && filepath.Clean(target.path) == filepath.Clean(path) {
			return true
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// AcquisitionGate rejects new acquisition calls while a checkpoint is pending.
type AcquisitionGate struct {
	state *workflow.State
}

// Acquire checks the checkpoint_pending gate before a new acquisition call.
func (g *AcquisitionGate) Acquire() error {
	return g.state.AcquireGate()
}

// RegisterArgs is the model-facing input of the evidence-artifact registration tool.
type RegisterArgs struct {
	Path             string `json:"path"`
	SourceToolCallID string `json:"source_tool_call_id"`
	Source           string `json:"source"`
	Description      string `json:"description"`
}

// RegistrationOutput is the model-facing output of a successful registration.
type RegistrationOutput struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

// RegisterHandler owns the mandatory evidence-artifact registration tool.
type RegisterHandler struct {
	context *Context
}

// NewRegisterHandler creates a registration tool handler.
func NewRegisterHandler(context *Context) *RegisterHandler {
	return &RegisterHandler{context: context}
}

// Context exposes the handler's protocol context.
func (h *RegisterHandler) Context() *Context { return h.context }

// Register performs one evidence-artifact registration, returning a structured
// ToolResponse so rejections are repairable.
func (h *RegisterHandler) Register(args RegisterArgs) corespec.ToolResponse[RegistrationOutput] {
	if h.context == nil {
		return infrastructureRejection[RegistrationOutput]("context_validation", errors.New("collection context is required"))
	}
	h.context.mu.Lock()
	defer h.context.mu.Unlock()

	if err := h.context.BeginWorkflow(); err != nil {
		return infrastructureRejection[RegistrationOutput]("context_validation", err)
	}
	if !h.context.informationNeedsFrozen() {
		return rejection[RegistrationOutput]("information_needs_required", "call r42_set_information_needs before collecting evidence")
	}
	if h.context.checkpointAccepted {
		return rejection[RegistrationOutput]("collection_round_complete", "collection checkpoint already accepted for this round; wait for Collection QC")
	}
	hasPath := args.Path != ""
	hasToolCall := args.SourceToolCallID != ""
	if hasPath == hasToolCall {
		return rejection[RegistrationOutput]("exactly_one_source", "provide exactly one of path or source_tool_call_id")
	}
	var record artifactpkg.Record
	var created bool
	var err error
	if hasPath {
		record, created, err = h.context.Artifacts.RegisterEvidence(
			h.context.Workspace, args.Path, args.Source, args.Description,
		)
	} else {
		record, created, err = h.context.Artifacts.RegisterRetainedEvidence(
			h.context.Workspace, args.SourceToolCallID, args.Source, args.Description,
		)
	}
	if err != nil {
		return rejection[RegistrationOutput]("invalid_evidence_artifact", err.Error())
	}
	if created {
		if err = h.context.State.RegisterEvidenceArtifact(); err != nil {
			return infrastructureRejection[RegistrationOutput]("state_update", err)
		}
		h.context.evidence = append(h.context.evidence, record.ID)
	}
	output := RegistrationOutput{
		ID: record.ID, Path: record.Path, Description: record.Description,
	}
	return corespec.ToolResponse[RegistrationOutput]{Accepted: true, Output: &output}
}

// CheckpointArgs is the model-facing input of the checkpoint tool.
type CheckpointArgs struct {
	EmptyReason      string            `json:"empty_reason"`
	NeedDispositions []NeedDisposition `json:"need_dispositions"`
}

// SearchDisposition describes whether one active need has another productive search action.
type SearchDisposition string

const (
	SearchDispositionContinue SearchDisposition = "continue"
	SearchDispositionStalled  SearchDisposition = "stalled"
)

// NeedDisposition is Collection's per-need end-of-round declaration.
type NeedDisposition struct {
	InformationNeedID string            `json:"information_need_id"`
	SearchDisposition SearchDisposition `json:"search_disposition"`
}

// CheckpointOutput lists the evidence artifact IDs submitted for review.
type CheckpointOutput struct {
	ArtifactIDs      []string          `json:"artifact_ids"`
	EmptyReason      string            `json:"empty_reason,omitempty"`
	NeedDispositions []NeedDisposition `json:"need_dispositions"`
}

// CheckpointHandler owns the mandatory checkpoint tool.
type CheckpointHandler struct {
	context *Context
}

// NewCheckpointHandler creates a checkpoint tool handler.
func NewCheckpointHandler(context *Context) *CheckpointHandler {
	return &CheckpointHandler{context: context}
}

// Submit submits every unreviewed evidence artifact. An empty checkpoint requires a
// non-empty reason.
func (h *CheckpointHandler) Submit(args CheckpointArgs) corespec.ToolResponse[CheckpointOutput] {
	if h.context == nil {
		return infrastructureRejection[CheckpointOutput]("context_validation", errors.New("collection context is required"))
	}
	h.context.mu.Lock()
	defer h.context.mu.Unlock()

	if err := h.context.BeginWorkflow(); err != nil {
		return infrastructureRejection[CheckpointOutput]("context_validation", err)
	}
	if !h.context.informationNeedsFrozen() {
		return rejection[CheckpointOutput]("information_needs_required", "call r42_set_information_needs before submitting a collection checkpoint")
	}
	if h.context.checkpointAccepted {
		return rejection[CheckpointOutput]("collection_round_complete", "r42_collection_checkpoint may be accepted exactly once in each Collection round")
	}
	if h.context.activeToolCalls > 0 {
		return rejection[CheckpointOutput]("collection_tools_in_flight", "wait for active Collection tool calls to finish before submitting the checkpoint")
	}
	if issues := validateNeedDispositions(activeInformationNeeds(h.context.activeNeedStates()), args.NeedDispositions); len(issues) > 0 {
		return corespec.ToolResponse[CheckpointOutput]{Issues: issues}
	}
	pending := make([]string, 0)
	for _, id := range h.context.evidence {
		if _, submitted := h.context.checkpointed[id]; !submitted {
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 && strings.TrimSpace(args.EmptyReason) == "" {
		return rejection[CheckpointOutput]("empty_checkpoint", "no evidence artifacts are pending; provide an empty_reason explaining why")
	}
	if len(pending) > 0 && args.EmptyReason != "" {
		return rejection[CheckpointOutput]("empty_checkpoint", "evidence artifacts are pending; empty_reason is only allowed for an empty checkpoint")
	}
	if len(pending) > 0 {
		if err := h.context.State.Checkpoint(); err != nil {
			return rejection[CheckpointOutput]("checkpoint_failed", err.Error())
		}
		for _, id := range pending {
			h.context.checkpointed[id] = struct{}{}
		}
	}
	output := CheckpointOutput{
		ArtifactIDs: pending, EmptyReason: args.EmptyReason, NeedDispositions: append([]NeedDisposition{}, args.NeedDispositions...),
	}
	h.context.checkpointAccepted = true
	h.context.lastRoundHadArtifacts = len(pending) > 0
	return corespec.ToolResponse[CheckpointOutput]{Accepted: true, Output: &output}
}

// LastRoundHadArtifacts reports whether the most recently accepted checkpoint
// added evidence artifacts. Collection QC uses it to enforce that a round
// without new artifacts cannot claim material evidence progress.
func (c *Context) LastRoundHadArtifacts() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRoundHadArtifacts
}

func validateNeedDispositions(needs []InformationNeed, dispositions []NeedDisposition) []corespec.Issue {
	if len(dispositions) != len(needs) {
		return []corespec.Issue{{Code: "need_dispositions", Message: "need_dispositions must contain every active information need exactly once"}}
	}
	expected := make(map[string]struct{}, len(needs))
	for _, need := range needs {
		expected[need.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(dispositions))
	issues := make([]corespec.Issue, 0)
	for index, disposition := range dispositions {
		if _, found := expected[disposition.InformationNeedID]; !found {
			issues = append(issues, corespec.Issue{Code: "need_dispositions", Message: fmt.Sprintf("need_dispositions[%d] names unknown or closed information need %q", index, disposition.InformationNeedID)})
			continue
		}
		if _, duplicate := seen[disposition.InformationNeedID]; duplicate {
			issues = append(issues, corespec.Issue{Code: "need_dispositions", Message: fmt.Sprintf("information need %q appears more than once", disposition.InformationNeedID)})
		}
		seen[disposition.InformationNeedID] = struct{}{}
		if disposition.SearchDisposition != SearchDispositionContinue && disposition.SearchDisposition != SearchDispositionStalled {
			issues = append(issues, corespec.Issue{Code: "search_disposition", Message: fmt.Sprintf("need_dispositions[%d].search_disposition must be continue or stalled", index)})
		}
	}
	if len(issues) > 0 {
		return issues
	}
	for _, need := range needs {
		if _, found := seen[need.ID]; !found {
			return []corespec.Issue{{Code: "need_dispositions", Message: "need_dispositions must contain every active information need exactly once"}}
		}
	}
	return nil
}

func rejection[T any](code, message string) corespec.ToolResponse[T] {
	return corespec.ToolResponse[T]{
		Issues: []corespec.Issue{{Code: code, Message: message}},
	}
}

func infrastructureRejection[T any](code string, cause error) corespec.ToolResponse[T] {
	return corespec.ToolResponse[T]{
		Issues: []corespec.Issue{{Code: code, Message: cause.Error()}},
	}
}
