// Package evidence exposes restricted read-only snapshot and candidate
// artifact access plus a controlled Markdown writer. It implements the
// capability boundary for Research and both QC sessions: no arbitrary
// filesystem reads or writes.
package evidence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lonegunmanb/r42/internal/snapshot"
)

// SnapshotInfo is a listable snapshot projection.
type SnapshotInfo struct {
	ID   string
	Path string
}

// SnapshotAccess provides read-only access to registered snapshots. Paths are
// resolved by snapshot ID only; callers never pass filesystem paths.
type SnapshotAccess struct {
	registry *snapshot.Registry
}

// NewSnapshotAccess creates snapshot read access over a registry rooted at the
// given workspace.
func NewSnapshotAccess(workspace string) (*SnapshotAccess, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("snapshot workspace is required")
	}
	return &SnapshotAccess{registry: snapshot.NewRegistry(workspace)}, nil
}

// Register registers an existing workspace file as a snapshot.
func (a *SnapshotAccess) Register(path string) (string, error) {
	registration, err := a.registry.RegisterPath(path)
	if err != nil {
		return "", err
	}
	return registration.ID, nil
}

// ListSnapshots returns every registered snapshot projection in registration
// order.
func (a *SnapshotAccess) ListSnapshots() ([]SnapshotInfo, error) {
	snapshots := a.registry.Snapshots()
	result := make([]SnapshotInfo, len(snapshots))
	for index, snap := range snapshots {
		result[index] = SnapshotInfo{ID: snap.ID, Path: snap.Path}
	}
	return result, nil
}

// ReadSnapshot reads up to maxBytes of a registered snapshot's content.
func (a *SnapshotAccess) ReadSnapshot(id string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", errors.New("read bound must be positive")
	}
	path, err := a.registry.Snapshot(id)
	if err != nil {
		return "", err
	}
	return readBounded(path, maxBytes)
}

// ArtifactAccess provides read-only access to declared candidate artifacts.
// Artifact paths are constrained to the block workspace.
type ArtifactAccess struct {
	workspace string
}

// NewArtifactAccess creates candidate-artifact read access.
func NewArtifactAccess(workspace string) (*ArtifactAccess, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("artifact workspace is required")
	}
	return &ArtifactAccess{workspace: workspace}, nil
}

// ReadArtifact reads up to maxBytes of a declared artifact by name and path.
func (a *ArtifactAccess) ReadArtifact(name, path string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", errors.New("read bound must be positive")
	}
	resolved, err := a.resolve(name, path)
	if err != nil {
		return "", err
	}
	return readBounded(resolved, maxBytes)
}

func (a *ArtifactAccess) resolve(name, path string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("artifact name is required")
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("artifact path is required")
	}
	resolved, err := a.absolute(path)
	if err != nil {
		return "", err
	}
	if !withinWorkspace(a.workspace, resolved) {
		return "", fmt.Errorf("artifact %s path is outside the block workspace", name)
	}
	// Resolve symlinks so a workspace-internal link cannot read content from
	// outside the block workspace.
	secure, err := resolveWithin(a.workspace, resolved)
	if err != nil {
		return "", err
	}
	return secure, nil
}

func (a *ArtifactAccess) absolute(path string) (string, error) {
	clean := path
	if !filepath.IsAbs(path) {
		clean = filepath.Join(a.workspace, path)
	}
	resolved, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

// MarkdownWriter writes Markdown content to declared file artifacts inside
// the block workspace. It never follows symlinks or writes outside the
// workspace.
type MarkdownWriter struct {
	workspace string
}

// NewMarkdownWriter creates a restricted Markdown writer.
func NewMarkdownWriter(workspace string) (*MarkdownWriter, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("markdown workspace is required")
	}
	return &MarkdownWriter{workspace: workspace}, nil
}

// Write writes Markdown content to the given artifact path inside the
// workspace, returning the absolute path. It never follows symlinks or writes
// outside the workspace.
func (w *MarkdownWriter) Write(path, content string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("artifact path is required")
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New("markdown content must not be empty")
	}
	if !strings.HasSuffix(strings.ToLower(path), ".md") {
		return "", errors.New("markdown writer only accepts .md artifacts")
	}
	resolved, err := w.absolute(path)
	if err != nil {
		return "", err
	}
	// Reject lexical escapes before creating any directories.
	if !withinWorkspace(w.workspace, resolved) {
		return "", errors.New("markdown artifact path is outside the block workspace")
	}
	parent := filepath.Dir(resolved)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create artifact directory: %w", err)
	}
	secure, err := resolveWithin(w.workspace, resolved)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(secure, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write markdown artifact: %w", err)
	}
	return secure, nil
}

func (w *MarkdownWriter) absolute(path string) (string, error) {
	clean := path
	if !filepath.IsAbs(path) {
		clean = filepath.Join(w.workspace, path)
	}
	resolved, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("resolve markdown artifact: %w", err)
	}
	return filepath.Clean(resolved), nil
}

// resolveWithin resolves path and the workspace with EvalSymlinks and
// verifies the resolved path stays inside the resolved workspace. A symlinked
// final component (or any ancestor) that points outside is rejected.
func resolveWithin(workspace, path string) (string, error) {
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	parent := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve artifact directory: %w", err)
	}
	target := filepath.Join(resolvedParent, filepath.Base(path))
	// The final component may itself be a symlink; resolve it and verify the
	// target stays inside the workspace.
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve artifact target: %w", err)
		}
		resolvedTarget = target
	}
	if !withinWorkspace(resolvedWorkspace, resolvedTarget) {
		return "", errors.New("markdown artifact path is outside the block workspace")
	}
	return resolvedTarget, nil
}

func readBounded(path string, maxBytes int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	buffer := make([]byte, min(int64(maxBytes), info.Size()))
	if _, err := file.Read(buffer); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(buffer), nil
}

func withinWorkspace(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
