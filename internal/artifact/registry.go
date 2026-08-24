package artifact

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
)

type Kind string

const (
	KindArtifact     Kind = "artifact"
	KindArtifactFile Kind = "artifact_file"
)

type Purpose string

const (
	PurposeOutput   Purpose = "output"
	PurposeEvidence Purpose = "evidence"
)

// Record is one run-scoped readable artifact capability.
type Record struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name,omitempty"`
	Path        string                    `json:"path"`
	Description string                    `json:"description"`
	Kind        Kind                      `json:"kind"`
	Type        researchspec.ArtifactType `json:"type"`
	Purpose     Purpose                   `json:"purpose"`
	Source      string                    `json:"source,omitempty"`
}

type entry struct {
	Record
	workspace     string
	directoryRoot string
	relativePath  string
}

// Registry owns opaque artifact IDs for one apply run.
type Registry struct {
	mu               sync.RWMutex
	entries          map[string]entry
	order            []string
	children         map[string]string
	retainedMu       sync.Mutex
	retained         map[string]string
	retainedEvidence map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		entries:          make(map[string]entry),
		children:         make(map[string]string),
		retained:         make(map[string]string),
		retainedEvidence: make(map[string]string),
	}
}

// Declare allocates an opaque ID for a configured research artifact.
func (r *Registry) Declare(workspace string, declared researchspec.Artifact) (Record, error) {
	if r == nil {
		return Record{}, errors.New("artifact registry is required")
	}
	if err := declared.Validate(); err != nil {
		return Record{}, err
	}
	path, err := absoluteArtifactPath(workspace, declared.Path)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		ID: "artifact-" + uuid.NewString(), Name: declared.Name, Path: path,
		Description: strings.TrimSpace(declared.Description), Kind: KindArtifact, Type: declared.Type, Purpose: PurposeOutput,
	}
	r.mu.Lock()
	r.entries[record.ID] = entry{Record: record, workspace: workspace}
	r.order = append(r.order, record.ID)
	r.mu.Unlock()
	return record, nil
}

// RetainToolResult stores a successful acquisition result for later evidence
// registration. Collection owns the lifetime of retained results.
func (r *Registry) RetainToolResult(toolCallID, result string) error {
	if r == nil {
		return errors.New("artifact registry is required")
	}
	if strings.TrimSpace(toolCallID) == "" {
		return errors.New("tool call id is required")
	}
	if strings.TrimSpace(result) == "" {
		return errors.New("tool result must not be empty")
	}
	r.retainedMu.Lock()
	r.retained[toolCallID] = result
	r.retainedMu.Unlock()
	return nil
}

// RegisterEvidence registers one existing Collection evidence file. A source,
// when supplied, is recorded in a compatible Markdown header and in metadata.
func (r *Registry) RegisterEvidence(workspace, path, source, description string) (Record, bool, error) {
	if r == nil {
		return Record{}, false, errors.New("artifact registry is required")
	}
	resolved, err := evidencePath(workspace, path)
	if err != nil {
		return Record{}, false, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Record{}, false, fmt.Errorf("evidence path %q does not exist: %w", path, err)
	}
	if info.IsDir() {
		return Record{}, false, fmt.Errorf("evidence path %q is not a file", path)
	}
	if info.Size() == 0 {
		return Record{}, false, fmt.Errorf("evidence path %q is empty", path)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return Record{}, false, fmt.Errorf("read evidence path %q: %w", path, err)
	}
	prepared := withSourceHeader(content, source)
	if string(prepared) != string(content) {
		if err := os.WriteFile(resolved, prepared, info.Mode().Perm()); err != nil {
			return Record{}, false, fmt.Errorf("write evidence source header %q: %w", path, err)
		}
	}
	source = sourceFromContent(string(prepared))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.entries {
		if existing.Path != resolved || existing.Purpose != PurposeEvidence {
			continue
		}
		if existing.Description == "" {
			existing.Description = strings.TrimSpace(description)
			r.entries[existing.ID] = existing
		}
		return existing.Record, false, nil
	}
	record := Record{
		ID: "artifact-" + uuid.NewString(), Path: resolved, Description: strings.TrimSpace(description),
		Kind: KindArtifact, Type: researchspec.ArtifactTypeFile, Purpose: PurposeEvidence, Source: source,
	}
	r.entries[record.ID] = entry{Record: record, workspace: workspace}
	r.order = append(r.order, record.ID)
	return record, true, nil
}

// RegisterRetainedEvidence materializes one retained acquisition result under
// the Collection workspace, then registers it as evidence.
func (r *Registry) RegisterRetainedEvidence(workspace, toolCallID, source, description string) (Record, bool, error) {
	if r == nil {
		return Record{}, false, errors.New("artifact registry is required")
	}
	if strings.TrimSpace(toolCallID) == "" {
		return Record{}, false, errors.New("tool call id is required")
	}
	r.retainedMu.Lock()
	defer r.retainedMu.Unlock()
	if id, ok := r.retainedEvidence[toolCallID]; ok {
		record, err := r.Record(id)
		if err != nil {
			return Record{}, false, err
		}
		return record, false, nil
	}
	content, ok := r.retained[toolCallID]
	if !ok {
		return Record{}, false, fmt.Errorf("tool call %q was not retained", toolCallID)
	}
	directory := filepath.Join(workspace, ".r42-artifacts")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return Record{}, false, fmt.Errorf("create retained artifact directory: %w", err)
	}
	file, err := os.CreateTemp(directory, "tool-*.md")
	if err != nil {
		return Record{}, false, fmt.Errorf("create retained artifact: %w", err)
	}
	path := file.Name()
	if _, err = file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return Record{}, false, fmt.Errorf("write retained artifact: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return Record{}, false, fmt.Errorf("close retained artifact: %w", err)
	}
	record, created, err := r.RegisterEvidence(workspace, path, source, description)
	if err != nil {
		_ = os.Remove(path)
		return Record{}, false, err
	}
	r.retainedEvidence[toolCallID] = record.ID
	return record, created, nil
}

// ListDirectoryFiles registers regular files below a declared directory artifact
// as read-only child capabilities. Symlinks are deliberately excluded.
func (r *Registry) ListDirectoryFiles(id string) ([]Record, error) {
	if r == nil {
		return nil, errors.New("artifact registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	parent, ok := r.entries[id]
	if !ok {
		return nil, fmt.Errorf("unknown artifact %q", id)
	}
	if parent.Kind != KindArtifact || parent.Type != researchspec.ArtifactTypeDirectory {
		return nil, fmt.Errorf("artifact %q is not a directory", id)
	}
	result := make([]Record, 0)
	err := filepath.WalkDir(parent.Path, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if item.IsDir() || !item.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(parent.Path, path)
		if err != nil {
			return err
		}
		key := id + "\x00" + filepath.Clean(relative)
		childID, exists := r.children[key]
		if !exists {
			childID = "artifact-" + uuid.NewString()
			record := Record{
				ID: childID, Name: filepath.ToSlash(relative), Path: path,
				Description: "File from directory artifact " + parent.Name + ": " + filepath.ToSlash(relative),
				Kind:        KindArtifactFile, Type: researchspec.ArtifactTypeFile, Purpose: parent.Purpose,
			}
			r.entries[childID] = entry{
				Record:        record,
				workspace:     parent.workspace,
				directoryRoot: parent.Path,
				relativePath:  filepath.Clean(relative),
			}
			r.order = append(r.order, childID)
			r.children[key] = childID
		}
		result = append(result, r.entries[childID].Record)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list directory artifact %q: %w", id, err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (r *Registry) Record(id string) (Record, error) {
	if r == nil {
		return Record{}, errors.New("artifact registry is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.entries[id]
	if !ok {
		return Record{}, fmt.Errorf("unknown artifact %q", id)
	}
	return item.Record, nil
}

func (r *Registry) Records(ids []string) ([]Record, error) {
	result := make([]Record, len(ids))
	for index, id := range ids {
		item, err := r.Record(id)
		if err != nil {
			return nil, err
		}
		result[index] = item
	}
	return result, nil
}

// RecordsByPurpose returns matching records in registration order.
func (r *Registry) RecordsByPurpose(purpose Purpose) []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Record, 0)
	for _, id := range r.order {
		if record, ok := r.entries[id]; ok && record.Purpose == purpose {
			result = append(result, record.Record)
		}
	}
	return result
}

// Page is a bounded UTF-8 byte range from an artifact.
type Page struct {
	Content         string `json:"content"`
	OffsetBytes     int    `json:"offset_bytes"`
	NextOffsetBytes int    `json:"next_offset_bytes"`
	TotalBytes      int    `json:"total_bytes"`
	Truncated       bool   `json:"truncated"`
}

func (r *Registry) ReadPage(id string, offsetBytes, maxBytes int) (Page, error) {
	if offsetBytes < 0 {
		return Page{}, errors.New("artifact offset must not be negative")
	}
	if maxBytes <= 0 {
		return Page{}, errors.New("read bound must be positive")
	}
	r.mu.RLock()
	item, ok := r.entries[id]
	r.mu.RUnlock()
	if !ok {
		return Page{}, fmt.Errorf("unknown artifact %q", id)
	}
	var root *os.Root
	var file *os.File
	var err error
	if item.directoryRoot != "" {
		root, err = os.OpenRoot(item.directoryRoot)
		if err != nil {
			return Page{}, fmt.Errorf("open artifact %q: %w", id, err)
		}
		file, err = root.Open(item.relativePath)
		if err != nil {
			_ = root.Close()
			return Page{}, fmt.Errorf("open artifact %q: %w", id, err)
		}
	} else {
		file, err = os.Open(item.Path)
	}
	if err != nil {
		return Page{}, fmt.Errorf("open artifact %q: %w", id, err)
	}
	if root != nil {
		defer func() { _ = root.Close() }()
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Page{}, fmt.Errorf("stat artifact %q: %w", id, err)
	}
	if info.IsDir() {
		return Page{}, fmt.Errorf("artifact %q is not a file", id)
	}
	totalBytes := int(info.Size())
	if offsetBytes > totalBytes {
		return Page{}, fmt.Errorf("artifact offset %d exceeds total bytes %d", offsetBytes, totalBytes)
	}
	if offsetBytes < totalBytes {
		boundary := make([]byte, 1)
		if _, err = file.ReadAt(boundary, int64(offsetBytes)); err != nil {
			return Page{}, fmt.Errorf("read artifact %q offset: %w", id, err)
		}
		if !utf8.RuneStart(boundary[0]) {
			return Page{}, fmt.Errorf("artifact offset %d is not a utf-8 character boundary", offsetBytes)
		}
	}
	if _, err = file.Seek(int64(offsetBytes), io.SeekStart); err != nil {
		return Page{}, fmt.Errorf("seek artifact %q: %w", id, err)
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)))
	if err != nil {
		return Page{}, fmt.Errorf("read artifact %q: %w", id, err)
	}
	for len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
	}
	if len(content) == 0 && offsetBytes < totalBytes {
		return Page{}, fmt.Errorf("max_bytes %d is too small for the next utf-8 character", maxBytes)
	}
	nextOffset := offsetBytes + len(content)
	return Page{
		Content: string(content), OffsetBytes: offsetBytes, NextOffsetBytes: nextOffset,
		TotalBytes: totalBytes, Truncated: nextOffset < totalBytes,
	}, nil
}

func absoluteArtifactPath(workspace, path string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", errors.New("artifact workspace is required")
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("artifact path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func evidencePath(workspace, path string) (string, error) {
	resolved, err := absoluteArtifactPath(workspace, path)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve evidence path %q: %w", path, err)
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve evidence workspace: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence path %q is outside the collection workspace", path)
	}
	return resolved, nil
}

func withSourceHeader(content []byte, source string) []byte {
	normalized := strings.Join(strings.Fields(source), " ")
	if normalized == "" || sourceFromContent(string(content)) != "" {
		return content
	}
	return append([]byte("- Source: "+normalized+"\n\n"), content...)
}

func sourceFromContent(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 64 {
		lines = lines[:64]
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"- Source:", "- URL:"} {
			if value, found := strings.CutPrefix(trimmed, prefix); found && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}
