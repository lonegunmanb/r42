// Package collection implements the mandatory Collection protocol tools:
// evidence-artifact registration, checkpoint submission, and the acquisition gate.
// The tools return structured ToolResponse envelopes so the model can repair
// rejections, while infrastructure failures surface as Go errors.
package collection

import (
	"errors"
	"fmt"
	"path/filepath"
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

	mu           sync.Mutex
	evidence     []string
	reviewed     map[string]struct{}
	checkpointed map[string]struct{}
	targets      []artifactTarget
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
	EmptyReason         string `json:"empty_reason"`
	CollectionExhausted bool   `json:"collection_exhausted"`
}

// CheckpointOutput lists the evidence artifact IDs submitted for review.
type CheckpointOutput struct {
	ArtifactIDs         []string `json:"artifact_ids"`
	EmptyReason         string   `json:"empty_reason,omitempty"`
	CollectionExhausted bool     `json:"collection_exhausted,omitempty"`
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
	pending := make([]string, 0)
	for _, id := range h.context.evidence {
		if _, submitted := h.context.checkpointed[id]; !submitted {
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 && strings.TrimSpace(args.EmptyReason) == "" {
		return rejection[CheckpointOutput]("empty_checkpoint", "no evidence artifacts are pending; provide an empty_reason explaining why")
	}
	if len(pending) > 0 && args.CollectionExhausted {
		return rejection[CheckpointOutput]("collection_exhausted", "collection_exhausted is only allowed for an empty checkpoint")
	}
	if len(pending) > 0 && args.EmptyReason != "" {
		return rejection[CheckpointOutput]("empty_checkpoint", "evidence artifacts are pending; empty_reason is only allowed for an empty checkpoint")
	}
	if args.CollectionExhausted {
		if err := h.context.State.MarkCollectionExhausted(); err != nil {
			return infrastructureRejection[CheckpointOutput]("state_update", err)
		}
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
		ArtifactIDs: pending, EmptyReason: args.EmptyReason,
		CollectionExhausted: args.CollectionExhausted,
	}
	return corespec.ToolResponse[CheckpointOutput]{Accepted: true, Output: &output}
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
