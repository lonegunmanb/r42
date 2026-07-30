package debuglog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	EventsFileName = "events.jsonl"
	debugWarning   = "debug output contains sensitive prompts, messages, and tool data"
)

type EventKind string

const (
	EventMessage EventKind = "message"
	EventTool    EventKind = "tool"
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
	Kind         EventKind       `json:"kind"`
	BlockAddress string          `json:"block_address,omitempty"`
	Session      SessionKind     `json:"session"`
	Role         Role            `json:"role,omitempty"`
	Content      string          `json:"content,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Stdout       string          `json:"stdout,omitempty"`
	Stderr       string          `json:"stderr,omitempty"`
}

type Recorder struct {
	mu      sync.Mutex
	enabled bool
	file    *os.File
	buffer  *bufio.Writer
	encoder *json.Encoder
	closed  bool
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
	if err := r.encoder.Encode(event); err != nil {
		return fmt.Errorf("writing debug event: %w", err)
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
