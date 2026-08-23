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
	ID          string
	Path        string
	Description string
	// New reports whether this registration created a fresh unique snapshot.
	// A duplicate-content registration returns the existing snapshot with
	// New=false.
	New bool
}

// Snapshot is a registered snapshot with its review state.
type Snapshot struct {
	ID          string
	Path        string
	Description string
	Reviewed    bool
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
	// descriptions stores optional semantic descriptions by snapshot ID.
	descriptions map[string]string
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
		descriptions:  make(map[string]string),
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
	return r.registerPath(path, "", "")
}

// RegisterPathWithSource registers an existing file and adds a source header
// when source is provided and the file has no compatible header.
func (r *Registry) RegisterPathWithSource(path, source string) (Registration, error) {
	return r.registerPath(path, source, "")
}

// RegisterPathWithMetadata registers a path with optional source and semantic description.
func (r *Registry) RegisterPathWithMetadata(path, source, description string) (Registration, error) {
	return r.registerPath(path, source, description)
}

// RegisterToolResult registers a previously retained acquisition result by
// writing its text into a managed file.
func (r *Registry) RegisterToolResult(toolCallID string) (Registration, error) {
	return r.RegisterToolResultWithMetadata(toolCallID, "", "")
}

// RegisterToolResultWithSource registers retained acquisition text and adds a
// source header when source is provided and the text has no compatible header.
func (r *Registry) RegisterToolResultWithSource(toolCallID, source string) (Registration, error) {
	return r.RegisterToolResultWithMetadata(toolCallID, source, "")
}

// RegisterToolResultWithMetadata registers retained acquisition text with optional metadata.
func (r *Registry) RegisterToolResultWithMetadata(toolCallID, source, description string) (Registration, error) {
	if strings.TrimSpace(toolCallID) == "" {
		return Registration{}, errors.New("tool call id is required")
	}
	r.mu.Lock()
	result, ok := r.retained[toolCallID]
	if !ok {
		r.mu.Unlock()
		return Registration{}, fmt.Errorf("tool call %q was not retained", toolCallID)
	}
	content := withSourceHeader([]byte(result), source)
	hash := contentHash(content)
	if existing, dup := r.contentHashes[hash]; dup {
		r.mergeDescription(existing, description)
		path := r.registered[existing]
		description = r.descriptions[existing]
		r.mu.Unlock()
		return Registration{ID: existing, Path: path, Description: description}, nil
	}
	r.mu.Unlock()

	path, err := r.writeManaged(toolCallID, content)
	if err != nil {
		return Registration{}, err
	}
	registration, err := r.registerContent(path, hash, description)
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
		return r.registerPath(path, "", "")
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

// ReviewedSnapshots returns reviewed snapshots in registration order.
func (r *Registry) ReviewedSnapshots() []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Snapshot, 0, len(r.reviewed))
	for _, id := range r.order {
		if _, reviewed := r.reviewed[id]; !reviewed {
			continue
		}
		result = append(result, Snapshot{
			ID: id, Path: r.registered[id], Description: r.descriptions[id], Reviewed: true,
		})
	}
	return result
}

// MarkReviewed advances the review state for one snapshot ID.
func (r *Registry) MarkReviewed(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviewed[id] = struct{}{}
}

// IsReviewed reports whether Collection QC accepted a registered snapshot ID.
func (r *Registry) IsReviewed(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.reviewed[id]
	return ok
}

// Snapshots returns every registered snapshot in registration order.
func (r *Registry) Snapshots() []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Snapshot, 0, len(r.order))
	for _, id := range r.order {
		_, reviewed := r.reviewed[id]
		result = append(result, Snapshot{
			ID: id, Path: r.registered[id], Description: r.descriptions[id], Reviewed: reviewed,
		})
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

func (r *Registry) registerPath(path, source, description string) (Registration, error) {
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
	prepared := withSourceHeader(content, source)
	if len(prepared) != len(content) {
		if err = os.WriteFile(clean, prepared, info.Mode().Perm()); err != nil {
			return Registration{}, fmt.Errorf("write snapshot source header %q: %w", path, err)
		}
		content = prepared
	}
	return r.registerContent(clean, contentHash(content), description)
}

func withSourceHeader(content []byte, source string) []byte {
	normalized := strings.Join(strings.Fields(source), " ")
	if normalized == "" || hasSourceHeader(string(content)) {
		return content
	}
	return append([]byte("- Source: "+normalized+"\n\n"), content...)
}

func hasSourceHeader(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) > 64 {
		lines = lines[:64]
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"- Source:", "- URL:"} {
			if value, found := strings.CutPrefix(trimmed, prefix); found {
				if strings.TrimSpace(value) != "" {
					return true
				}
			}
		}
	}
	return false
}

func (r *Registry) registerContent(path, hash, description string) (Registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, dup := r.contentHashes[hash]; dup {
		r.mergeDescription(existing, description)
		return Registration{
			ID: existing, Path: r.registered[existing], Description: r.descriptions[existing],
		}, nil
	}
	id := newSnapshotID(r.workspace, hash)
	r.contentHashes[hash] = id
	r.registered[id] = path
	r.descriptions[id] = strings.TrimSpace(description)
	r.order = append(r.order, id)
	return Registration{ID: id, Path: path, Description: r.descriptions[id], New: true}, nil
}

func (r *Registry) mergeDescription(id, description string) {
	if r.descriptions[id] == "" {
		r.descriptions[id] = strings.TrimSpace(description)
	}
}

func (r *Registry) writeManaged(toolCallID string, content []byte) (string, error) {
	if err := os.MkdirAll(r.managedDir, 0o755); err != nil {
		return "", fmt.Errorf("create snapshot directory: %w", err)
	}
	file, err := os.CreateTemp(r.managedDir, "tool-*.txt")
	if err != nil {
		return "", fmt.Errorf("create managed snapshot: %w", err)
	}
	path := file.Name()
	if _, err = file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write managed snapshot: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close managed snapshot: %w", err)
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

func isWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
