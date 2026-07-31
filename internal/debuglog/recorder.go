package debuglog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	EventsFileName = "events.jsonl"
	debugWarning   = "debug output contains sensitive prompts, messages, and tool data"
)

type EventKind string

const (
	EventMessage   EventKind = "message"
	EventTool      EventKind = "tool"
	EventLifecycle EventKind = "lifecycle"
)

type EventStatus string

const (
	StatusStarted   EventStatus = "started"
	StatusCompleted EventStatus = "completed"
	StatusFailed    EventStatus = "failed"
	StatusSkipped   EventStatus = "skipped"
)

type SessionKind string

const (
	SessionResearch SessionKind = "research"
	SessionQC       SessionKind = "qc"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Event struct {
	Sequence     uint64          `json:"sequence,omitempty"`
	Timestamp    time.Time       `json:"timestamp,omitzero"`
	Kind         EventKind       `json:"kind"`
	BlockAddress string          `json:"block_address,omitempty"`
	BlockType    string          `json:"block_type,omitempty"`
	Action       string          `json:"action,omitempty"`
	Status       EventStatus     `json:"status,omitempty"`
	Path         string          `json:"path,omitempty"`
	Paths        []string        `json:"paths,omitempty"`
	Dependencies []string        `json:"dependencies,omitempty"`
	SourceRange  string          `json:"source_range,omitempty"`
	Count        int             `json:"count,omitempty"`
	Bytes        int             `json:"bytes,omitempty"`
	DurationMS   *int64          `json:"duration_ms,omitempty"`
	Error        string          `json:"error,omitempty"`
	Session      SessionKind     `json:"session,omitempty"`
	Model        string          `json:"model,omitempty"`
	WorkingDir   string          `json:"working_directory,omitempty"`
	ToolNames    []string        `json:"tool_names,omitempty"`
	Role         Role            `json:"role,omitempty"`
	Content      string          `json:"content,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Stdout       string          `json:"stdout,omitempty"`
	Stderr       string          `json:"stderr,omitempty"`
}

type Recorder struct {
	mu       sync.Mutex
	enabled  bool
	file     *os.File
	buffer   *bufio.Writer
	encoder  *json.Encoder
	closed   bool
	sequence uint64
}

type recorderContextKey struct{}

func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	return context.WithValue(ctx, recorderContextKey{}, recorder)
}

func Record(ctx context.Context, event Event) error {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(recorderContextKey{}).(*Recorder)
	if recorder == nil {
		return nil
	}
	return recorder.Record(event)
}

func Lifecycle(ctx context.Context, action string, status EventStatus, event Event) error {
	event.Kind = EventLifecycle
	event.Action = action
	event.Status = status
	return Record(ctx, event)
}

func CompleteLifecycle(
	ctx context.Context,
	action string,
	started time.Time,
	operationErr error,
	event Event,
) error {
	duration := time.Since(started).Milliseconds()
	event.DurationMS = &duration
	status := StatusCompleted
	if operationErr != nil {
		status = StatusFailed
		event.Error = operationErr.Error()
	}
	return Lifecycle(ctx, action, status, event)
}

func PlanBlock(ctx context.Context, address, blockType string, plan func() error) error {
	event := Event{BlockAddress: address, BlockType: blockType}
	if err := Lifecycle(ctx, "block.decode", StatusCompleted, event); err != nil {
		return err
	}
	started := time.Now()
	if err := Lifecycle(ctx, "block.plan", StatusStarted, event); err != nil {
		return err
	}
	planErr := plan()
	logErr := CompleteLifecycle(ctx, "block.plan", started, planErr, event)
	return errors.Join(planErr, logErr)
}

func NewRecorder(directory string, enabled bool) (*Recorder, error) {
	recorder := &Recorder{enabled: enabled}
	if !enabled {
		return recorder, nil
	}
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("debug directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("creating debug directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(directory, EventsFileName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating debug events file: %w", err)
	}
	buffer := bufio.NewWriter(file)
	recorder.file = file
	recorder.buffer = buffer
	recorder.encoder = json.NewEncoder(buffer)
	return recorder, nil
}

func (r *Recorder) Record(event Event) error {
	if r == nil {
		return fmt.Errorf("debug recorder is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled {
		return nil
	}
	if r.closed {
		return fmt.Errorf("debug recorder is closed")
	}
	r.sequence++
	event.Sequence = r.sequence
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Kind == EventLifecycle && event.Status != StatusStarted && event.DurationMS == nil {
		event.DurationMS = new(int64)
	}
	if err := r.encoder.Encode(event); err != nil {
		return fmt.Errorf("writing debug event: %w", err)
	}
	if err := r.buffer.Flush(); err != nil {
		return fmt.Errorf("flushing debug event: %w", err)
	}
	return nil
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.closed {
		return nil
	}
	r.closed = true
	if err := r.buffer.Flush(); err != nil {
		_ = r.file.Close()
		return fmt.Errorf("flushing debug events: %w", err)
	}
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("closing debug events: %w", err)
	}
	return nil
}

func (r *Recorder) Warning() string {
	if r == nil || !r.enabled {
		return ""
	}
	return debugWarning
}

func RedactKnownSecrets(content string, secrets []string) string {
	ordered := make([]string, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		seen[secret] = struct{}{}
		ordered = append(ordered, secret)
	}
	slices.SortFunc(ordered, func(left, right string) int {
		return len(right) - len(left)
	})
	for _, secret := range ordered {
		content = strings.ReplaceAll(content, secret, "<sensitive>")
	}
	return content
}
