package qc

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	sdk "github.com/github/copilot-sdk/go"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

type Research interface {
	Run(context.Context, researchruntime.Config) (researchruntime.Result, error)
}

type Session interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
}

type Task struct {
	SystemPrompt string  `json:"system_prompt"`
	Prompt       *string `json:"prompt,omitempty"`
}

type Config struct {
	Task                Task
	Criteria            cty.Value
	Artifacts           []researchspec.Artifact
	Research            researchruntime.Config
	MaxRounds           int
	MaxProtocolAttempts int
	VerdictToolName     string
	IssueToolName       string
	CompleteToolName    string
	OpenIssues          []corespec.Issue
}

type Result struct {
	Candidate researchruntime.Result
	Rounds    int
}

type Decision string

const (
	DecisionPass           Decision = "pass"
	DecisionReviseResearch Decision = "revise_research"
)

type Verdict struct {
	Decision Decision         `json:"decision"`
	Issues   []corespec.Issue `json:"issues,omitempty"`
}

func (v Verdict) Validate() error {
	switch v.Decision {
	case DecisionPass:
		if len(v.Issues) != 0 {
			return fmt.Errorf("pass verdict must not contain issues")
		}
	case DecisionReviseResearch:
		if len(v.Issues) == 0 {
			return fmt.Errorf("revise_research verdict must contain at least one issue")
		}
	default:
		return fmt.Errorf("unsupported final qc decision %q", v.Decision)
	}
	for index, issue := range v.Issues {
		if err := issue.Validate(); err != nil {
			return fmt.Errorf("issue %d: %w", index, err)
		}
	}
	return nil
}

type Runner struct {
	research Research
	session  Session
	verdicts *VerdictRecorder
}

func NewRunner(research Research, session Session, verdicts *VerdictRecorder) *Runner {
	return &Runner{research: research, session: session, verdicts: verdicts}
}

// Review obtains one valid Final-QC verdict for an existing candidate without
// starting follow-up work. The workflow coordinator owns phase transitions.
func (r *Runner) Review(ctx context.Context, config Config, candidate researchruntime.Result) (Verdict, error) {
	if r.session == nil {
		return Verdict{}, fmt.Errorf("qc session is required")
	}
	if r.verdicts == nil {
		return Verdict{}, fmt.Errorf("qc verdict recorder is required")
	}
	if config.MaxProtocolAttempts < 0 {
		return Verdict{}, fmt.Errorf("qc maximum protocol attempts must not be negative")
	}
	if strings.TrimSpace(config.CompleteToolName) != "" {
		if strings.TrimSpace(config.IssueToolName) == "" {
			return Verdict{}, fmt.Errorf("qc issue tool name is required")
		}
		return r.reviewCompletion(ctx, config, candidate)
	}
	if strings.TrimSpace(config.VerdictToolName) == "" {
		return Verdict{}, fmt.Errorf("qc verdict tool name is required")
	}
	if _, err := criteriaMap(config.Criteria); err != nil {
		return Verdict{}, err
	}
	return r.review(ctx, config, candidate)
}

func (r *Runner) Run(ctx context.Context, config Config) (Result, error) {
	if err := r.validate(config); err != nil {
		return Result{}, err
	}
	candidate, err := r.research.Run(ctx, config.Research)
	if err != nil {
		return Result{}, fmt.Errorf("run research candidate: %w", err)
	}
	for round := 1; ; round++ {
		verdict, err := r.Review(ctx, config, candidate)
		if err != nil {
			return Result{}, err
		}
		if verdict.Decision == DecisionPass {
			return Result{Candidate: candidate, Rounds: round}, nil
		}
		if round == config.MaxRounds {
			return Result{}, fmt.Errorf("qc rounds exhausted after %d rounds", round)
		}
		// Final QC repairs the existing candidate through its typed patch tools.
		// Keep reviewing the same artifact set instead of starting a new
		// researcher session and rewriting the whole candidate.
		config.OpenIssues = cloneIssues(verdict.Issues)
	}
}

func (r *Runner) validate(config Config) error {
	if r.research == nil {
		return fmt.Errorf("research runner is required")
	}
	if r.session == nil {
		return fmt.Errorf("qc session is required")
	}
	if r.verdicts == nil {
		return fmt.Errorf("qc verdict recorder is required")
	}
	if config.MaxRounds <= 0 {
		return fmt.Errorf("qc rounds exhausted before review")
	}
	if config.MaxProtocolAttempts < 0 {
		return fmt.Errorf("qc maximum protocol attempts must not be negative")
	}
	if strings.TrimSpace(config.CompleteToolName) != "" {
		if strings.TrimSpace(config.IssueToolName) == "" {
			return fmt.Errorf("qc issue tool name is required")
		}
	} else if strings.TrimSpace(config.VerdictToolName) == "" {
		return fmt.Errorf("qc verdict tool name is required")
	}
	_, err := criteriaMap(config.Criteria)
	return err
}

func (r *Runner) reviewCompletion(
	ctx context.Context,
	config Config,
	candidate researchruntime.Result,
) (Verdict, error) {
	prompt, err := contextPrompt(config, candidate)
	if err != nil {
		return Verdict{}, err
	}
	attempts := 0
	for {
		if _, err = r.session.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt}); err != nil {
			return Verdict{}, fmt.Errorf("send qc prompt: %w", err)
		}
		completed, failure := r.verdicts.drainCompletion()
		if failure != nil {
			return Verdict{}, fmt.Errorf("qc completion tool failed: %w", failure)
		}
		if completed {
			return Verdict{Decision: DecisionPass}, nil
		}
		if config.MaxRounds > 0 && r.verdicts.FinalCompletionAttempts() >= config.MaxRounds {
			return Verdict{}, fmt.Errorf("qc rounds exhausted after %d rounds", config.MaxRounds)
		}
		attempts++
		if attempts >= config.MaxProtocolAttempts {
			return Verdict{}, fmt.Errorf(
				"qc completion protocol attempts exhausted after %d attempts (maximum %d)",
				attempts,
				config.MaxProtocolAttempts,
			)
		}
		active, _ := json.Marshal(struct {
			OpenIssues []corespec.Issue `json:"open_issues"`
		}{OpenIssues: r.verdicts.FinalIssues()})
		prompt = fmt.Sprintf("You must call %q after resolving every active Final-QC issue; call %q to register or resolve issues. Current issue ledger: %s", config.CompleteToolName, config.IssueToolName, active)
	}
}

func (r *Runner) review(
	ctx context.Context,
	config Config,
	candidate researchruntime.Result,
) (Verdict, error) {
	prompt, err := contextPrompt(config, candidate)
	if err != nil {
		// note: untested because validate has already accepted the same immutable criteria value.
		return Verdict{}, err
	}
	attempts := 0
	for {
		if _, err = r.session.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt}); err != nil {
			return Verdict{}, fmt.Errorf("send qc prompt: %w", err)
		}
		verdicts, failure := r.verdicts.drain()
		if failure != nil {
			return Verdict{}, fmt.Errorf("qc verdict tool failed: %w", failure)
		}
		if len(verdicts) > 0 {
			return verdicts[0], nil
		}
		attempts++
		if attempts >= config.MaxProtocolAttempts {
			return Verdict{}, fmt.Errorf(
				"qc verdict protocol attempts exhausted after %d attempts (maximum %d)",
				attempts,
				config.MaxProtocolAttempts,
			)
		}
		prompt = fmt.Sprintf("You must call the %q tool before QC can finish.", config.VerdictToolName)
	}
}

type VerdictRecorder struct {
	mu                 sync.Mutex
	verdicts           []Verdict
	failure            error
	completionVersion  uint64
	finalIssues        map[string]corespec.Issue
	finalReviewed      bool
	finalIssueHistory  map[string]corespec.Issue
	nextFinalIssueID   int
	completionPending  bool
	completionAttempts int
}

func NewVerdictRecorder() *VerdictRecorder {
	return &VerdictRecorder{finalIssues: make(map[string]corespec.Issue), finalIssueHistory: make(map[string]corespec.Issue)}
}

// OpenFinalIssues records semantic findings and assigns IDs owned by the host.
func (r *VerdictRecorder) OpenFinalIssues(issues []corespec.Issue) ([]corespec.Issue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalIssues == nil {
		r.finalIssues = make(map[string]corespec.Issue)
	}
	if r.finalIssueHistory == nil {
		r.finalIssueHistory = make(map[string]corespec.Issue)
	}
	for index, issue := range issues {
		if err := issue.Validate(); err != nil {
			return nil, fmt.Errorf("issue %d: %w", index, err)
		}
		if strings.TrimSpace(issue.ID) != "" {
			return nil, fmt.Errorf("issue %d: issue ID is host-generated and must be omitted", index)
		}
	}
	opened := make([]corespec.Issue, len(issues))
	for index, issue := range issues {
		r.nextFinalIssueID++
		issue.ID = fmt.Sprintf("FQ-%03d", r.nextFinalIssueID)
		opened[index] = issue
		r.finalIssues[issue.ID] = issue
		r.finalIssueHistory[issue.ID] = issue
	}
	if len(opened) > 0 {
		r.finalReviewed = true
		r.completionVersion++
	}
	return cloneIssues(opened), nil
}

// ResolveFinalIssues closes only currently active host-generated IDs.
func (r *VerdictRecorder) ResolveFinalIssues(ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("issue ID %d is required", index)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("issue ID %q appears more than once", id)
		}
		seen[id] = struct{}{}
		if _, ok := r.finalIssues[id]; !ok {
			return fmt.Errorf("unknown Final-QC issue ID %q", id)
		}
	}
	for id := range seen {
		delete(r.finalIssues, id)
	}
	if len(seen) > 0 {
		r.completionVersion++
	}
	return nil
}

// RecordFinalCompletion accepts completion only when every host-tracked issue is resolved.
func (r *VerdictRecorder) RecordFinalCompletion() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completionAttempts++
	if len(r.finalIssues) != 0 {
		return fmt.Errorf("unresolved Final-QC issues: %d", len(r.finalIssues))
	}
	r.finalReviewed = true
	r.completionPending = true
	r.completionVersion++
	return nil
}

// FinalCompletionAttempts reports how many times Final QC attempted completion.
func (r *VerdictRecorder) FinalCompletionAttempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.completionAttempts
}

// FinalIssues returns a snapshot of the current Final-QC issue baseline.
func (r *VerdictRecorder) FinalIssues() []corespec.Issue {
	r.mu.Lock()
	defer r.mu.Unlock()
	issues := make([]corespec.Issue, 0, len(r.finalIssues))
	for _, issue := range r.finalIssues {
		issues = append(issues, issue)
	}
	slices.SortFunc(issues, func(a, b corespec.Issue) int { return strings.Compare(a.ID, b.ID) })
	return cloneIssues(issues)
}

func (r *VerdictRecorder) Record(verdict Verdict) error {
	if err := verdict.Validate(); err != nil {
		return fmt.Errorf("record qc verdict: %w", err)
	}
	r.mu.Lock()
	verdict.Issues = cloneIssues(verdict.Issues)
	r.verdicts = append(r.verdicts, verdict)
	r.completionVersion++
	r.mu.Unlock()
	return nil
}

// RecordFinal validates and records one Final QC verdict. Issue IDs are useful
// for continuity in the QC prompt, but a repaired candidate may legitimately
// change an issue's wording or classification between confirmation attempts.
func (r *VerdictRecorder) RecordFinal(verdict Verdict) error {
	if err := verdict.Validate(); err != nil {
		return fmt.Errorf("record qc verdict: %w", err)
	}
	if verdict.Decision == DecisionReviseResearch {
		seen := make(map[string]corespec.Issue, len(verdict.Issues))
		for index := range verdict.Issues {
			issue := &verdict.Issues[index]
			id := strings.TrimSpace(issue.ID)
			if id == "" {
				return fmt.Errorf("record qc verdict: issue %d: issue id is required", index)
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("record qc verdict: issue %d: issue id %q appears more than once", index, id)
			}
			issue.ID = id
			seen[id] = cloneIssues([]corespec.Issue{*issue})[0]
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.finalIssues = seen
		r.finalReviewed = true
		verdict.Issues = cloneIssues(verdict.Issues)
		r.verdicts = append(r.verdicts, verdict)
		r.completionVersion++
		return nil
	}

	r.mu.Lock()
	verdict.Issues = cloneIssues(verdict.Issues)
	r.verdicts = append(r.verdicts, verdict)
	r.completionVersion++
	r.finalReviewed = true
	r.mu.Unlock()
	return nil
}

func (r *VerdictRecorder) RecordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.failure == nil {
		r.failure = err
		r.completionVersion++
	}
	r.mu.Unlock()
}

// CompletionVersion identifies the most recent QC outcome recorded for a session.
func (r *VerdictRecorder) CompletionVersion() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.completionVersion
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

func (r *VerdictRecorder) drainCompletion() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	completed := r.completionPending
	failure := r.failure
	r.completionPending = false
	r.failure = nil
	return completed, failure
}

type contextDocument struct {
	Task       Task               `json:"task"`
	Criteria   map[string]string  `json:"criteria"`
	Candidate  candidateDocument  `json:"candidate"`
	Artifacts  []artifactDocument `json:"artifacts"`
	OpenIssues []corespec.Issue   `json:"open_issues,omitempty"`
}

type candidateDocument struct {
	Result *string `json:"result,omitempty"`
}

type artifactDocument struct {
	Name     string                    `json:"name"`
	Type     researchspec.ArtifactType `json:"type"`
	Path     string                    `json:"path"`
	Required bool                      `json:"required"`
	NonEmpty bool                      `json:"non_empty"`
}

func contextPrompt(config Config, candidate researchruntime.Result) (string, error) {
	criteria, err := criteriaMap(config.Criteria)
	if err != nil {
		return "", err
	}
	document := contextDocument{
		Task:       config.Task,
		Criteria:   criteria,
		Candidate:  candidateDocument{Result: cloneString(candidate.Value)},
		Artifacts:  make([]artifactDocument, len(config.Artifacts)),
		OpenIssues: cloneIssues(config.OpenIssues),
	}
	for index, declared := range config.Artifacts {
		document.Artifacts[index] = artifactDocument{
			Name:     declared.Name,
			Type:     declared.Type,
			Path:     candidate.Artifacts[declared.Name],
			Required: declared.Required,
			NonEmpty: declared.NonEmpty,
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		// note: untested because contextDocument contains only JSON-compatible fields.
		return "", fmt.Errorf("encode qc context: %w", err)
	}
	return string(encoded), nil
}

func criteriaMap(value cty.Value) (map[string]string, error) {
	if value.Type().Equals(cty.NilType) {
		return nil, fmt.Errorf("qc criteria must be a non-empty map of string")
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsWhollyKnown() || unmarked.IsNull() || !unmarked.Type().Equals(cty.Map(cty.String)) || unmarked.LengthInt() == 0 {
		return nil, fmt.Errorf("qc criteria must be a non-empty map of string")
	}
	result := make(map[string]string, unmarked.LengthInt())
	for name, criterion := range unmarked.AsValueMap() {
		if criterion.IsNull() {
			return nil, fmt.Errorf("qc criteria values must not be null")
		}
		result[name] = criterion.AsString()
	}
	return result, nil
}

func cloneIssues(source []corespec.Issue) []corespec.Issue {
	result := make([]corespec.Issue, len(source))
	for index, issue := range source {
		result[index] = issue
		result[index].Path = cloneString(issue.Path)
		result[index].RepairHint = cloneString(issue.RepairHint)
	}
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
