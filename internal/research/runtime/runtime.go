package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/artifact"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
)

type Session interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
}

type Config struct {
	InitialPrompt       string
	TerminateToolName   string
	MaxProtocolAttempts int
	Timeout             *time.Duration
	Workspace           string
	Artifacts           []researchspec.Artifact
}

type Result struct {
	Value            *string
	Artifacts        map[string]string
	ProtocolAttempts int
}

type Runner struct {
	session  Session
	terminal *TerminalRecorder
}

func NewRunner(session Session, terminal *TerminalRecorder) *Runner {
	return &Runner{session: session, terminal: terminal}
}

func (r *Runner) Run(ctx context.Context, config Config) (Result, error) {
	if r.session == nil {
		return Result{}, fmt.Errorf("research session is required")
	}
	if config.MaxProtocolAttempts < 0 {
		return Result{}, fmt.Errorf("maximum protocol attempts must not be negative")
	}
	hasTerminal := strings.TrimSpace(config.TerminateToolName) != ""
	if hasTerminal && r.terminal == nil {
		return Result{}, fmt.Errorf("terminal recorder is required")
	}
	if config.Timeout != nil {
		if *config.Timeout <= 0 {
			return Result{}, fmt.Errorf("research timeout must be positive")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *config.Timeout)
		defer cancel()
	}

	prompt := config.InitialPrompt
	attempts := 0
	for {
		if _, err := r.session.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt}); err != nil {
			return Result{}, fmt.Errorf("send research prompt: %w", err)
		}

		if !hasTerminal {
			validated, err := artifact.Validate(config.Workspace, config.Artifacts)
			if err != nil {
				return Result{}, fmt.Errorf("validate research artifacts: %w", err)
			}
			if len(validated.Issues) == 0 {
				return Result{Artifacts: validated.Paths, ProtocolAttempts: attempts}, nil
			}
			attempts++
			if attempts >= config.MaxProtocolAttempts {
				return Result{}, protocolExhausted(attempts, config.MaxProtocolAttempts)
			}
			prompt = repairPrompt(validated.Issues, "")
			continue
		}

		calls, terminalErr := r.terminal.drain()
		if terminalErr != nil {
			return Result{}, fmt.Errorf("terminal tool failed: %w", terminalErr)
		}
		accepted, rejected := firstAccepted(calls)
		attempts += len(rejected)
		if accepted != nil {
			validated, err := artifact.Validate(config.Workspace, config.Artifacts)
			if err != nil {
				return Result{}, fmt.Errorf("validate research artifacts: %w", err)
			}
			if len(validated.Issues) == 0 {
				return Result{
					Value:            cloneString(accepted.Output),
					Artifacts:        validated.Paths,
					ProtocolAttempts: attempts,
				}, nil
			}
			attempts++
			if attempts >= config.MaxProtocolAttempts {
				return Result{}, protocolExhausted(attempts, config.MaxProtocolAttempts)
			}
			prompt = repairPrompt(validated.Issues, config.TerminateToolName)
			continue
		}

		issues := flattenIssues(rejected)
		if len(calls) == 0 {
			attempts++
		}
		if attempts >= config.MaxProtocolAttempts {
			return Result{}, protocolExhausted(attempts, config.MaxProtocolAttempts)
		}
		prompt = terminalPrompt(config.TerminateToolName, issues)
	}
}

type TerminalRecorder struct {
	mu                sync.Mutex
	calls             []corespec.ToolResponse[string]
	failure           error
	completionVersion uint64
}

func NewTerminalRecorder() *TerminalRecorder {
	return &TerminalRecorder{}
}

func (r *TerminalRecorder) Record(response corespec.ToolResponse[string]) error {
	if err := response.Validate(); err != nil {
		failure := fmt.Errorf("record terminal response: %w", err)
		r.RecordError(failure)
		return failure
	}
	response.Output = cloneString(response.Output)
	response.Issues = cloneIssues(response.Issues)
	r.mu.Lock()
	r.calls = append(r.calls, response)
	r.completionVersion++
	r.mu.Unlock()
	return nil
}

func (r *TerminalRecorder) RecordError(err error) {
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

// CompletionVersion identifies the most recent terminal outcome recorded for a session.
func (r *TerminalRecorder) CompletionVersion() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.completionVersion
}

func (r *TerminalRecorder) drain() ([]corespec.ToolResponse[string], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	failure := r.failure
	r.calls = nil
	r.failure = nil
	return calls, failure
}

func firstAccepted(calls []corespec.ToolResponse[string]) (
	*corespec.ToolResponse[string],
	[]corespec.ToolResponse[string],
) {
	rejected := make([]corespec.ToolResponse[string], 0, len(calls))
	for index := range calls {
		if calls[index].Accepted {
			return &calls[index], rejected
		}
		rejected = append(rejected, calls[index])
	}
	return nil, rejected
}

func flattenIssues(calls []corespec.ToolResponse[string]) []corespec.Issue {
	var result []corespec.Issue
	for _, call := range calls {
		result = append(result, call.Issues...)
	}
	return result
}

func terminalPrompt(toolName string, issues []corespec.Issue) string {
	if len(issues) == 0 {
		return fmt.Sprintf("You must call the %q tool before this research block can finish.", toolName)
	}
	return fmt.Sprintf(
		"The %q tool rejected its last call. Fix these issues and call the tool again:\n%s",
		toolName,
		formatIssues(issues),
	)
}

func repairPrompt(issues []corespec.Issue, toolName string) string {
	next := "Finish the research response after repairing them."
	if toolName != "" {
		next = fmt.Sprintf("Call the %q tool again after repairing them.", toolName)
	}
	return fmt.Sprintf("Required artifact validation failed:\n%s\n%s", formatIssues(issues), next)
}

func formatIssues(issues []corespec.Issue) string {
	var result strings.Builder
	for index, issue := range issues {
		if index > 0 {
			result.WriteByte('\n')
		}
		fmt.Fprintf(&result, "- [%s] %s", issue.Code, issue.Message)
		if issue.Path != nil {
			fmt.Fprintf(&result, " (path: %s)", *issue.Path)
		}
		if issue.RepairHint != nil {
			fmt.Fprintf(&result, " Repair: %s", *issue.RepairHint)
		}
	}
	return result.String()
}

func protocolExhausted(attempts, maximum int) error {
	return fmt.Errorf("research protocol attempts exhausted after %d attempts (maximum %d)", attempts, maximum)
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
