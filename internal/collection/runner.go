package collection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/github/copilot-sdk/go"
)

// Session is the persistent open-world Collection model session.
type Session interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
}

// RunConfig controls one Collection acquisition round.
type RunConfig struct {
	InitialPrompt       string
	MaxProtocolAttempts int
	CheckpointToolName  string
}

// Runner obtains one mandatory checkpoint from a persistent Collection
// session. Calling Run again starts another round on the same session.
type Runner struct {
	session     Session
	checkpoints *CheckpointRecorder
}

// NewRunner creates a Collection runner.
func NewRunner(session Session, checkpoints *CheckpointRecorder) *Runner {
	return &Runner{session: session, checkpoints: checkpoints}
}

// Run executes one Collection round.
func (r *Runner) Run(ctx context.Context, config RunConfig) (CheckpointOutput, error) {
	if r.session == nil {
		return CheckpointOutput{}, errors.New("collection session is required")
	}
	if r.checkpoints == nil {
		return CheckpointOutput{}, errors.New("collection checkpoint recorder is required")
	}
	if config.MaxProtocolAttempts <= 0 {
		return CheckpointOutput{}, errors.New("collection maximum protocol attempts must be positive")
	}
	if strings.TrimSpace(config.CheckpointToolName) == "" {
		return CheckpointOutput{}, errors.New("collection checkpoint tool name is required")
	}
	prompt := config.InitialPrompt
	for attempt := 1; ; attempt++ {
		if _, err := r.session.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt}); err != nil {
			return CheckpointOutput{}, fmt.Errorf("send collection prompt: %w", err)
		}
		outputs, failure := r.checkpoints.drain()
		if failure != nil {
			return CheckpointOutput{}, fmt.Errorf("collection checkpoint tool failed: %w", failure)
		}
		if len(outputs) > 0 {
			return mergeCheckpoints(outputs), nil
		}
		if attempt >= config.MaxProtocolAttempts {
			return CheckpointOutput{}, fmt.Errorf(
				"collection checkpoint protocol attempts exhausted after %d attempts (maximum %d)",
				attempt,
				config.MaxProtocolAttempts,
			)
		}
		prompt = fmt.Sprintf("You must call the %q tool before this Collection round can finish.", config.CheckpointToolName)
	}
}

func mergeCheckpoints(outputs []CheckpointOutput) CheckpointOutput {
	merged := CheckpointOutput{}
	seen := make(map[string]struct{})
	for _, output := range outputs {
		for _, id := range output.SnapshotIDs {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			merged.SnapshotIDs = append(merged.SnapshotIDs, id)
		}
		if strings.TrimSpace(output.EmptyReason) != "" {
			merged.EmptyReason = output.EmptyReason
		}
		merged.CollectionExhausted = merged.CollectionExhausted || output.CollectionExhausted
	}
	return merged
}

// CheckpointRecorder captures accepted checkpoint tool calls.
type CheckpointRecorder struct {
	mu      sync.Mutex
	outputs []CheckpointOutput
	failure error
}

// NewCheckpointRecorder creates an empty checkpoint recorder.
func NewCheckpointRecorder() *CheckpointRecorder { return &CheckpointRecorder{} }

// Record records one accepted checkpoint.
func (r *CheckpointRecorder) Record(output CheckpointOutput) error {
	output.SnapshotIDs = append([]string{}, output.SnapshotIDs...)
	r.mu.Lock()
	r.outputs = append(r.outputs, output)
	r.mu.Unlock()
	return nil
}

// RecordError preserves the first checkpoint handler failure.
func (r *CheckpointRecorder) RecordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.failure == nil {
		r.failure = err
	}
	r.mu.Unlock()
}

func (r *CheckpointRecorder) drain() ([]CheckpointOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	outputs := r.outputs
	failure := r.failure
	r.outputs = nil
	r.failure = nil
	return outputs, failure
}
