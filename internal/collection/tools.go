// Package collection implements the mandatory Collection protocol tools:
// snapshot registration, checkpoint submission, and the acquisition gate.
// The tools return structured ToolResponse envelopes so the model can repair
// rejections, while infrastructure failures surface as Go errors.
package collection

import (
	"errors"
	"strings"
	"sync"

	"github.com/lonegunmanb/r42/internal/snapshot"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/lonegunmanb/r42/internal/workflow"
)

// Context wires the workflow state machine and snapshot registry for one
// Collection phase.
type Context struct {
	Workspace string
	State     *workflow.State
	Registry  *snapshot.Registry
	batchSize int

	mu           sync.Mutex
	checkpointed map[string]struct{}
}

// NewContext creates a Collection protocol context with default batch size 10
// and unlimited collection rounds.
func NewContext(workspace string, batchSize int, maxCollectionRounds *int) *Context {
	if batchSize == 0 {
		batchSize = workflow.DefaultBatchSize
	}
	return &Context{
		Workspace:    workspace,
		State:        workflow.New(workflow.Config{MaxCollectionRounds: maxCollectionRounds, BatchSize: batchSize}),
		Registry:     snapshot.NewRegistry(workspace),
		batchSize:    batchSize,
		checkpointed: make(map[string]struct{}),
	}
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

// AcquisitionGate rejects new acquisition calls while a checkpoint is pending.
type AcquisitionGate struct {
	state *workflow.State
}

// Acquire checks the checkpoint_pending gate before a new acquisition call.
func (g *AcquisitionGate) Acquire() error {
	return g.state.AcquireGate()
}

// RegisterArgs is the model-facing input of the snapshot registration tool.
type RegisterArgs struct {
	Path             string `json:"path"`
	SourceToolCallID string `json:"source_tool_call_id"`
}

// RegistrationOutput is the model-facing output of a successful registration.
type RegistrationOutput struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// RegisterHandler owns the mandatory snapshot registration tool.
type RegisterHandler struct {
	context *Context
}

// NewRegisterHandler creates a registration tool handler.
func NewRegisterHandler(context *Context) *RegisterHandler {
	return &RegisterHandler{context: context}
}

// Context exposes the handler's protocol context.
func (h *RegisterHandler) Context() *Context { return h.context }

// Register performs one snapshot registration, returning a structured
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
	var registration snapshot.Registration
	var err error
	if hasPath {
		registration, err = h.context.Registry.RegisterPath(args.Path)
	} else {
		registration, err = h.context.Registry.RegisterToolResult(args.SourceToolCallID)
	}
	if err != nil {
		return rejection[RegistrationOutput]("invalid_snapshot_source", err.Error())
	}
	if registration.New {
		if err = h.context.State.RegisterSnapshot(); err != nil {
			return infrastructureRejection[RegistrationOutput]("state_update", err)
		}
	}
	output := RegistrationOutput{ID: registration.ID, Path: registration.Path}
	return corespec.ToolResponse[RegistrationOutput]{Accepted: true, Output: &output}
}

// CheckpointArgs is the model-facing input of the checkpoint tool.
type CheckpointArgs struct {
	EmptyReason         string `json:"empty_reason"`
	CollectionExhausted bool   `json:"collection_exhausted"`
}

// CheckpointOutput lists the snapshot IDs submitted for review.
type CheckpointOutput struct {
	SnapshotIDs         []string `json:"snapshot_ids"`
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

// Submit submits every unreviewed snapshot. An empty checkpoint requires a
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
	for _, registered := range h.context.Registry.Snapshots() {
		if _, submitted := h.context.checkpointed[registered.ID]; !submitted {
			pending = append(pending, registered.ID)
		}
	}
	if len(pending) == 0 && strings.TrimSpace(args.EmptyReason) == "" {
		return rejection[CheckpointOutput]("empty_checkpoint", "no snapshots are pending; provide an empty_reason explaining why")
	}
	if len(pending) > 0 && args.CollectionExhausted {
		return rejection[CheckpointOutput]("collection_exhausted", "collection_exhausted is only allowed for an empty checkpoint")
	}
	if len(pending) > 0 && args.EmptyReason != "" {
		return rejection[CheckpointOutput]("empty_checkpoint", "snapshots are pending; empty_reason is only allowed for an empty checkpoint")
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
		SnapshotIDs: pending, EmptyReason: args.EmptyReason,
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
