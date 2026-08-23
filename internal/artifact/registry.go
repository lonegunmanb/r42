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
	KindSnapshot     Kind = "snapshot"
)

// Record is one run-scoped readable artifact capability.
type Record struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name,omitempty"`
	Path        string                    `json:"path"`
	Description string                    `json:"description"`
	Kind        Kind                      `json:"kind"`
	Type        researchspec.ArtifactType `json:"type"`
	Ready       bool                      `json:"-"`
}

type entry struct {
	Record
	workspace     string
	directoryRoot string
	relativePath  string
}

// Registry owns opaque artifact IDs for one apply run.
type Registry struct {
	mu       sync.RWMutex
	entries  map[string]entry
	order    []string
	children map[string]string
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]entry), children: make(map[string]string)}
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
		Description: strings.TrimSpace(declared.Description), Kind: KindArtifact, Type: declared.Type,
	}
	r.mu.Lock()
	r.entries[record.ID] = entry{Record: record, workspace: workspace}
	r.order = append(r.order, record.ID)
	r.mu.Unlock()
	return record, nil
}

// RegisterSnapshot adds snapshot metadata using its existing evidence ID.
func (r *Registry) RegisterSnapshot(workspace, id, path, description string) (Record, error) {
	if r == nil {
		return Record{}, errors.New("artifact registry is required")
	}
	if strings.TrimSpace(id) == "" {
		return Record{}, errors.New("snapshot id is required")
	}
	resolved, err := absoluteArtifactPath(workspace, path)
	if err != nil {
		return Record{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.entries[id]; ok {
		if existing.Path != resolved || existing.Kind != KindSnapshot {
			return Record{}, fmt.Errorf("artifact id %q is already registered", id)
		}
		if existing.Description == "" {
			existing.Description = strings.TrimSpace(description)
			r.entries[id] = existing
		}
		return existing.Record, nil
	}
	record := Record{
		ID: id, Path: resolved, Description: strings.TrimSpace(description), Kind: KindSnapshot, Type: researchspec.ArtifactTypeFile,
	}
	r.entries[id] = entry{Record: record, workspace: workspace}
	r.order = append(r.order, id)
	return record, nil
}

// DeclareSnapshot reserves an ID-backed Collection snapshot target before its
// file or directory exists. File targets become ready after Collection QC;
// directory targets are readable containers immediately.
func (r *Registry) DeclareSnapshot(workspace, id string, declared researchspec.Artifact) (Record, error) {
	if r == nil {
		return Record{}, errors.New("artifact registry is required")
	}
	if strings.TrimSpace(id) == "" {
		return Record{}, errors.New("snapshot id is required")
	}
	if err := declared.Validate(); err != nil {
		return Record{}, err
	}
	path, err := absoluteArtifactPath(workspace, declared.Path)
	if err != nil {
		return Record{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.entries[id]; ok {
		return existing.Record, nil
	}
	record := Record{
		ID: id, Name: declared.Name, Path: path, Description: strings.TrimSpace(declared.Description),
		Kind: KindSnapshot, Type: declared.Type, Ready: declared.Type == researchspec.ArtifactTypeDirectory,
	}
	r.entries[id] = entry{Record: record, workspace: workspace}
	r.order = append(r.order, id)
	return record, nil
}

// ListDirectoryFiles registers regular files below a ready directory artifact
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
	if !parent.Ready {
		return nil, fmt.Errorf("artifact %q is not ready", id)
	}
	if (parent.Kind != KindArtifact && parent.Kind != KindSnapshot) || parent.Type != researchspec.ArtifactTypeDirectory {
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
				Kind:        KindArtifactFile, Type: researchspec.ArtifactTypeFile, Ready: true,
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

func (r *Registry) MarkReady(id string) error {
	if r == nil {
		return errors.New("artifact registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.entries[id]
	if !ok {
		return fmt.Errorf("unknown artifact %q", id)
	}
	item.Ready = true
	r.entries[id] = item
	return nil
}

func (r *Registry) ReadyRecords() []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Record, 0, len(r.order))
	for _, id := range r.order {
		if item := r.entries[id]; item.Ready {
			result = append(result, item.Record)
		}
	}
	return result
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
	if !item.Ready {
		return Page{}, fmt.Errorf("artifact %q is not ready", id)
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
