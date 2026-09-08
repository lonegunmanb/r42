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
	InitialPrompt               string
	MaxProtocolAttempts         int
	CheckpointToolName          string
	ActiveInformationNeedStates []ActiveInformationNeedState
	CollectionToolNames         []string
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
			return outputs[0], nil
		}
		if attempt >= config.MaxProtocolAttempts {
			return CheckpointOutput{}, fmt.Errorf(
				"collection checkpoint protocol attempts exhausted after %d attempts (maximum %d)",
				attempt,
				config.MaxProtocolAttempts,
			)
		}
		prompt = checkpointRetryPrompt(config, attempt)
	}
}

func checkpointRetryPrompt(config RunConfig, attempt int) string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"Collection protocol retry %d: the previous response ended without an accepted %q checkpoint.\n",
		attempt,
		config.CheckpointToolName,
	)
	builder.WriteString("Before ending this round, resolve the following protocol requirements:\n")
	if len(config.ActiveInformationNeedStates) == 0 {
		fmt.Fprintf(
			&builder,
			"- Call %q before choosing a search direction. If it reports no frozen plan, "+
				"call r42_set_information_needs once and then call %q again.\n",
			ReadInformationNeedsToolName,
			ReadInformationNeedsToolName,
		)
	} else {
		builder.WriteString("- The following stop conditions are still unsatisfied; work only on these active needs:\n")
		fmt.Fprintf(
			&builder,
			"  If the IDs or conditions are unclear after context compaction, call %q and use its canonical active states.\n",
			ReadInformationNeedsToolName,
		)
		for _, state := range config.ActiveInformationNeedStates {
			fmt.Fprintf(&builder, "  - %s: %s\n", state.InformationNeed.ID, state.InformationNeed.Question)
			conditionByID := make(map[string]string, len(state.InformationNeed.StopConditions))
			for _, condition := range state.InformationNeed.StopConditions {
				conditionByID[condition.ID] = condition.Condition
			}
			for _, conditionID := range state.UnsatisfiedConditionIDs {
				if condition := conditionByID[conditionID]; condition != "" {
					fmt.Fprintf(&builder, "    - %s: %s\n", conditionID, condition)
					continue
				}
				fmt.Fprintf(&builder, "    - %s\n", conditionID)
			}
		}
	}
	if len(config.CollectionToolNames) > 0 {
		fmt.Fprintf(
			&builder,
			"- Make a genuine search or source-reading attempt with the relevant configured Collection tool(s): %s.\n",
			strings.Join(config.CollectionToolNames, ", "),
		)
	} else {
		builder.WriteString("- No configured Collection acquisition tool is available. Do not invent a tool; ")
		builder.WriteString(
			"call the checkpoint with search_disposition=stalled for each active need and explain the limitation " +
				"in empty_reason.\n",
		)
	}
	builder.WriteString(
		"- Persist every newly acquired source before another acquisition call: use r42_save_artifact for " +
			"new source content, " +
			"or r42_register_artifact for an existing workspace/retained result.\n",
	)
	fmt.Fprintf(
		&builder,
		"- Then call %q exactly once with one disposition for every active need. "+
			"Use search_disposition=continue only when a productive next search remains; after genuine effort "+
			"finds no productive next action, "+
			"use search_disposition=stalled and do not claim missing evidence.\n",
		config.CheckpointToolName,
	)
	builder.WriteString("- If this round added no evidence artifacts, include a non-empty empty_reason in the checkpoint.")
	return builder.String()
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
	output.ArtifactIDs = append([]string{}, output.ArtifactIDs...)
	output.NeedDispositions = append([]NeedDisposition{}, output.NeedDispositions...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.outputs) > 0 {
		return errors.New("r42_collection_checkpoint may be accepted exactly once in each Collection round")
	}
	r.outputs = append(r.outputs, output)
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
