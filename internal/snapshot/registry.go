// Package snapshot retains successful Collection acquisition results and
// manages registered source snapshots. It is independent of SDK sessions so
// registration mechanics are fully testable.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Registration describes one registered snapshot source.
type Registration struct {
	ID   string
	Path string
}

// Snapshot is a registered snapshot with its review state.
type Snapshot struct {
	ID       string
	Path     string
	Reviewed bool
}

// Registry retains acquisition results and registers snapshots for one
// workflow. Each dynamic research member owns an isolated Registry instance.
type Registry struct {
	workspace  string
	managedDir string

	mu sync.Mutex
	// retained maps tool-call IDs to their acquisition result text.
	retained map[string]string
	// registered maps snapshot IDs to registered paths.
	registered map[string]string
	// contentHashes maps content hashes to the first snapshot ID that
	// registered them, enabling deduplication.
	contentHashes map[string]string
	// reviewed tracks snapshot IDs that received a valid Collection-QC
	// verdict.
	reviewed map[string]struct{}
	// order preserves registration order for deterministic listing.
	order []string
}

// NewRegistry creates a registry rooted at the given block workspace. Managed
// tool-result files are written under <workspace>/.r42-snapshots.
func NewRegistry(workspace string) *Registry {
	return &Registry{
		workspace:     workspace,
		managedDir:    filepath.Join(workspace, ".r42-snapshots"),
		retained:      make(map[string]string),
		registered:    make(map[string]string),
		contentHashes: make(map[string]string),
		reviewed:      make(map[string]struct{}),
	}
}

// RetainToolResult stores a successful acquisition result by tool-call ID for
// the life of the workflow.
func (r *Registry) RetainToolResult(toolCallID, result string) error {
	if strings.TrimSpace(toolCallID) == "" {
		return errors.New("tool call id is required")
	}
	if strings.TrimSpace(result) == "" {
		return errors.New("tool result must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retained[toolCallID] = result
	return nil
}

// RegisterPath registers an existing non-empty file in the block workspace.
func (r *Registry) RegisterPath(path string) (Registration, error) {
	return r.registerPath(path)
}

// RegisterToolResult registers a previously retained acquisition result by
// writing its text into a managed file.
func (r *Registry) RegisterToolResult(toolCallID string) (Registration, error) {
	if strings.TrimSpace(toolCallID) == "" {
		return Registration{}, errors.New("tool call id is required")
	}
	r.mu.Lock()
	result, ok := r.retained[toolCallID]
	if !ok {
		r.mu.Unlock()
		return Registration{}, fmt.Errorf("tool call %q was not retained", toolCallID)
	}
	hash := contentHash([]byte(result))
	if existing, dup := r.contentHashes[hash]; dup {
		id := existing
		path := r.registered[id]
		r.mu.Unlock()
		return Registration{ID: id, Path: path}, nil
	}
	r.mu.Unlock()

	path, err := r.writeManaged(toolCallID, []byte(result))
	if err != nil {
		return Registration{}, err
	}
	registration, err := r.registerContent(path, hash)
	if err != nil {
		return Registration{}, err
	}
	// A concurrent registration of identical content may have claimed this
	// hash first; registerContent then returns the claimed registration whose
	// path differs from the managed file we just wrote, which is redundant.
	if registration.Path != path {
		r.mu.Lock()
		_ = os.Remove(path)
		r.mu.Unlock()
		return registration, nil
	}
	return registration, nil
}

// Register validates source exclusivity and dispatches to one of the two
// registration forms.
func (r *Registry) Register(path, toolCallID string) (Registration, error) {
	hasPath := strings.TrimSpace(path) != ""
	hasToolCall := strings.TrimSpace(toolCallID) != ""
	if hasPath == hasToolCall {
		return Registration{}, errors.New("exactly one source (path or source_tool_call_id) is required")
	}
	if hasPath {
		return r.registerPath(path)
	}
	return r.RegisterToolResult(toolCallID)
}

// PendingCount returns unique snapshots registered but not yet reviewed.
func (r *Registry) PendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, id := range r.order {
		if _, done := r.reviewed[id]; !done {
			count++
		}
	}
	return count
}

// PendingSnapshotIDs returns the unreviewed snapshot IDs in registration
// order.
func (r *Registry) PendingSnapshotIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]string, 0, len(r.order))
	for _, id := range r.order {
		if _, done := r.reviewed[id]; !done {
			result = append(result, id)
		}
	}
	return result
}

// ReviewedSnapshotIDs returns snapshot IDs that received a valid verdict.
// The order is not deterministic.
func (r *Registry) ReviewedSnapshotIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]string, 0, len(r.reviewed))
	for id := range r.reviewed {
		result = append(result, id)
	}
	return result
}

// MarkReviewed advances the review state for one snapshot ID.
func (r *Registry) MarkReviewed(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviewed[id] = struct{}{}
}

// Snapshots returns every registered snapshot in registration order.
func (r *Registry) Snapshots() []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Snapshot, 0, len(r.order))
	for _, id := range r.order {
		_, reviewed := r.reviewed[id]
		result = append(result, Snapshot{ID: id, Path: r.registered[id], Reviewed: reviewed})
	}
	return result
}

// Snapshot returns the registered path for one snapshot ID.
func (r *Registry) Snapshot(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path, ok := r.registered[id]
	if !ok {
		return "", fmt.Errorf("unknown snapshot %q", id)
	}
	return path, nil
}

func (r *Registry) registerPath(path string) (Registration, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return Registration{}, fmt.Errorf("resolve snapshot path: %w", err)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return Registration{}, fmt.Errorf("snapshot path %q does not exist: %w", path, err)
	}
	if info.IsDir() {
		return Registration{}, fmt.Errorf("snapshot path %q is not a file", path)
	}
	// Resolve symlinks so a workspace-internal link cannot smuggle in
	// content from outside the block workspace.
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return Registration{}, fmt.Errorf("resolve snapshot path %q: %w", path, err)
	}
	workspace, err := filepath.EvalSymlinks(r.workspace)
	if err != nil {
		return Registration{}, fmt.Errorf("resolve workspace: %w", err)
	}
	if !isWithin(workspace, resolved) {
		return Registration{}, fmt.Errorf("snapshot path %q is outside the block workspace", path)
	}
	if info.Size() == 0 {
		return Registration{}, fmt.Errorf("snapshot path %q is empty", path)
	}
	content, err := os.ReadFile(clean)
	if err != nil {
		return Registration{}, fmt.Errorf("read snapshot path %q: %w", path, err)
	}
	return r.registerContent(clean, contentHash(content))
}

func (r *Registry) registerContent(path, hash string) (Registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, dup := r.contentHashes[hash]; dup {
		return Registration{ID: existing, Path: r.registered[existing]}, nil
	}
	id := newSnapshotID(r.workspace, hash)
	r.contentHashes[hash] = id
	r.registered[id] = path
	r.order = append(r.order, id)
	return Registration{ID: id, Path: path}, nil
}

func (r *Registry) writeManaged(toolCallID string, content []byte) (string, error) {
	if err := os.MkdirAll(r.managedDir, 0o755); err != nil {
		return "", fmt.Errorf("create snapshot directory: %w", err)
	}
	path := filepath.Join(r.managedDir, managedFileName(toolCallID))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf("write managed snapshot: %w", err)
	}
	return path, nil
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func newSnapshotID(workspace, contentHash string) string {
	sum := sha256.Sum256([]byte("r42/snapshot/v1:" + workspace + ":" + contentHash))
	return "snapshot-" + hex.EncodeToString(sum[:16])
}

func managedFileName(toolCallID string) string {
	sum := sha256.Sum256([]byte("r42/managed:" + toolCallID))
	return "tool-" + hex.EncodeToString(sum[:12]) + ".txt"
}

func isWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
