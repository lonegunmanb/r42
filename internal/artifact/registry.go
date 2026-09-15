package artifact

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	purposes      map[Purpose]struct{}
}

// Registry owns opaque artifact IDs for one apply run.
type Registry struct {
	mu               sync.RWMutex
	entries          map[string]entry
	order            []string
	children         map[string]string
	paths            map[string]string
	retainedMu       sync.Mutex
	retained         map[string]string
	retainedEvidence map[string]string
}

// Snapshot is the serializable run-scoped artifact registry state at a safe
// checkpoint. It deliberately preserves opaque IDs rather than rebuilding
// them from paths during recovery.
type Snapshot struct {
	Entries          []SnapshotEntry   `json:"entries"`
	Retained         map[string]string `json:"retained,omitempty"`
	RetainedEvidence map[string]string `json:"retained_evidence,omitempty"`
}

// SnapshotEntry contains the private registry indexes needed to restore one
// artifact capability without exposing the Registry implementation itself.
type SnapshotEntry struct {
	Record        Record    `json:"record"`
	Workspace     string    `json:"workspace"`
	DirectoryRoot string    `json:"directory_root,omitempty"`
	RelativePath  string    `json:"relative_path,omitempty"`
	Purposes      []Purpose `json:"purposes"`
}

func NewRegistry() *Registry {
	return &Registry{
		entries:          make(map[string]entry),
		children:         make(map[string]string),
		paths:            make(map[string]string),
		retained:         make(map[string]string),
		retainedEvidence: make(map[string]string),
	}
}

// Snapshot returns a deep copy of all registry state that is meaningful at a
// checkpoint. File contents are captured separately by the checkpoint store.
func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.RLock()
	entries := make([]SnapshotEntry, 0, len(r.order))
	for _, id := range r.order {
		item, ok := r.entries[id]
		if !ok {
			continue
		}
		purposes := make([]Purpose, 0, len(item.purposes))
		for purpose := range item.purposes {
			purposes = append(purposes, purpose)
		}
		slices.Sort(purposes)
		entries = append(entries, SnapshotEntry{
			Record: item.Record, Workspace: item.workspace, DirectoryRoot: item.directoryRoot,
			RelativePath: item.relativePath, Purposes: purposes,
		})
	}
	r.mu.RUnlock()
	r.retainedMu.Lock()
	defer r.retainedMu.Unlock()
	return Snapshot{
		Entries: entries, Retained: maps.Clone(r.retained), RetainedEvidence: maps.Clone(r.retainedEvidence),
	}
}

// SnapshotWorkspace returns checkpoint state owned by one workflow workspace.
// Imported artifacts stay owned by their source workflow checkpoint.
func (r *Registry) SnapshotWorkspace(workspace string) Snapshot {
	snapshot := r.Snapshot()
	cleaned := filepath.Clean(workspace)
	snapshot.Entries = slices.DeleteFunc(snapshot.Entries, func(item SnapshotEntry) bool {
		return filepath.Clean(item.Workspace) != cleaned
	})
	return snapshot
}

// Restore replaces registry state with a previously captured checkpoint.
// Callers restore artifact files before allowing a resumed phase to use this
// registry.
func (r *Registry) Restore(snapshot Snapshot) error {
	if r == nil {
		return errors.New("artifact registry is required")
	}
	entries := make(map[string]entry, len(snapshot.Entries))
	order := make([]string, 0, len(snapshot.Entries))
	paths := make(map[string]string, len(snapshot.Entries))
	children := make(map[string]string)
	for _, saved := range snapshot.Entries {
		if strings.TrimSpace(saved.Record.ID) == "" {
			return errors.New("artifact snapshot entry id is required")
		}
		if _, exists := entries[saved.Record.ID]; exists {
			return fmt.Errorf("artifact snapshot contains duplicate id %q", saved.Record.ID)
		}
		purposes := make(map[Purpose]struct{}, len(saved.Purposes))
		for _, purpose := range saved.Purposes {
			purposes[purpose] = struct{}{}
		}
		if len(purposes) == 0 {
			purposes[saved.Record.Purpose] = struct{}{}
		}
		item := entry{Record: saved.Record, workspace: saved.Workspace, directoryRoot: saved.DirectoryRoot, relativePath: saved.RelativePath, purposes: purposes}
		entries[saved.Record.ID] = item
		order = append(order, saved.Record.ID)
		paths[artifactPathKey(item.workspace, item.Path)] = item.ID
		if item.directoryRoot != "" && item.relativePath != "" {
			children[artifactPathKey(item.workspace, item.directoryRoot)+"\x00"+filepath.Clean(item.relativePath)] = item.ID
		}
	}
	r.mu.Lock()
	r.entries = entries
	r.order = order
	r.paths = paths
	r.children = children
	r.mu.Unlock()
	r.retainedMu.Lock()
	r.retained = maps.Clone(snapshot.Retained)
	r.retainedEvidence = maps.Clone(snapshot.RetainedEvidence)
	r.retainedMu.Unlock()
	return nil
}

// RestoreWorkspace replaces only artifact records owned by workspace. It
// preserves other dynamic tasks that may have completed independently.
func (r *Registry) RestoreWorkspace(workspace string, snapshot Snapshot) error {
	if r == nil {
		return errors.New("artifact registry is required")
	}
	cleaned := filepath.Clean(workspace)
	for _, entry := range snapshot.Entries {
		if filepath.Clean(entry.Workspace) != cleaned {
			return fmt.Errorf("artifact workspace snapshot contains foreign artifact %q", entry.Record.ID)
		}
	}
	current := r.Snapshot()
	current.Entries = slices.DeleteFunc(current.Entries, func(entry SnapshotEntry) bool {
		return filepath.Clean(entry.Workspace) == cleaned
	})
	current.Entries = append(current.Entries, snapshot.Entries...)
	return r.Restore(current)
}

// Merge adds checkpoint state for one workflow to this run-wide registry.
// Resume constructs independent workflow blocks from separate checkpoints, so
// replacing the whole registry would discard artifacts restored by siblings.
func (r *Registry) Merge(snapshot Snapshot) error {
	if r == nil {
		return errors.New("artifact registry is required")
	}
	staged := NewRegistry()
	if err := staged.Restore(snapshot); err != nil {
		return err
	}
	staged.mu.RLock()
	defer staged.mu.RUnlock()
	r.mu.Lock()
	for _, id := range staged.order {
		item := staged.entries[id]
		if existing, exists := r.entries[id]; exists {
			if existing.Record != item.Record || existing.workspace != item.workspace || existing.directoryRoot != item.directoryRoot || existing.relativePath != item.relativePath || !samePurposes(existing.purposes, item.purposes) {
				r.mu.Unlock()
				return fmt.Errorf("artifact checkpoint conflicts with existing id %q", id)
			}
			continue
		}
		pathKey := artifactPathKey(item.workspace, item.Path)
		if existingID, exists := r.paths[pathKey]; exists && existingID != id {
			r.mu.Unlock()
			return fmt.Errorf("artifact checkpoint conflicts with existing path %q", item.Path)
		}
		r.entries[id] = item
		r.order = append(r.order, id)
		r.paths[pathKey] = id
		if item.directoryRoot != "" && item.relativePath != "" {
			childKey := artifactPathKey(item.workspace, item.directoryRoot) + "\x00" + filepath.Clean(item.relativePath)
			if existingID, exists := r.children[childKey]; exists && existingID != id {
				r.mu.Unlock()
				return fmt.Errorf("artifact checkpoint conflicts with existing child path %q", item.relativePath)
			}
			r.children[childKey] = id
		}
	}
	r.mu.Unlock()
	staged.retainedMu.Lock()
	defer staged.retainedMu.Unlock()
	r.retainedMu.Lock()
	defer r.retainedMu.Unlock()
	for id, result := range staged.retained {
		if existing, exists := r.retained[id]; exists && existing != result {
			return fmt.Errorf("artifact checkpoint conflicts with retained result %q", id)
		}
		r.retained[id] = result
	}
	for id, artifactID := range staged.retainedEvidence {
		if existing, exists := r.retainedEvidence[id]; exists && existing != artifactID {
			return fmt.Errorf("artifact checkpoint conflicts with retained evidence %q", id)
		}
		r.retainedEvidence[id] = artifactID
	}
	return nil
}

func samePurposes(left, right map[Purpose]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for purpose := range left {
		if _, found := right[purpose]; !found {
			return false
		}
	}
	return true
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
	key := artifactPathKey(workspace, path)
	if existingID, exists := r.paths[key]; exists {
		existing := r.entries[existingID]
		if existing.Type != record.Type {
			r.mu.Unlock()
			return Record{}, fmt.Errorf("artifact path %q is already registered with type %q", declared.Path, existing.Type)
		}
		if existing.purposes == nil {
			existing.purposes = make(map[Purpose]struct{})
		}
		existing.Name = record.Name
		existing.Description = record.Description
		existing.Kind = record.Kind
		existing.Type = record.Type
		existing.purposes[PurposeOutput] = struct{}{}
		r.entries[existingID] = existing
		r.mu.Unlock()
		return existing.Record, nil
	}
	r.entries[record.ID] = entry{Record: record, workspace: workspace, purposes: map[Purpose]struct{}{PurposeOutput: {}}}
	r.order = append(r.order, record.ID)
	r.paths[key] = record.ID
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
	logicalPath, err := absoluteArtifactPath(workspace, path)
	if err != nil {
		return Record{}, false, err
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
	key := artifactPathKey(workspace, resolved)
	existingID, exists := r.paths[key]
	if !exists {
		existingID, exists = r.paths[artifactLogicalPathKey(workspace, logicalPath)]
		if exists {
			r.paths[key] = existingID
		}
	}
	if exists {
		existing := r.entries[existingID]
		if existing.Type != researchspec.ArtifactTypeFile {
			return Record{}, false, fmt.Errorf("evidence path %q is not a file artifact", path)
		}
		if _, alreadyEvidence := existing.purposes[PurposeEvidence]; alreadyEvidence {
			return existing.Record, false, nil
		}
		if existing.Description == "" {
			existing.Description = strings.TrimSpace(description)
		}
		existing.Path = resolved
		existing.Source = source
		existing.Purpose = PurposeEvidence
		if existing.purposes == nil {
			existing.purposes = make(map[Purpose]struct{})
		}
		existing.purposes[PurposeEvidence] = struct{}{}
		r.entries[existing.ID] = existing
		return existing.Record, true, nil
	}
	record := Record{
		ID: "artifact-" + uuid.NewString(), Path: resolved, Description: strings.TrimSpace(description),
		Kind: KindArtifact, Type: researchspec.ArtifactTypeFile, Purpose: PurposeEvidence, Source: source,
	}
	r.entries[record.ID] = entry{Record: record, workspace: workspace, purposes: map[Purpose]struct{}{PurposeEvidence: {}}}
	r.order = append(r.order, record.ID)
	r.paths[key] = record.ID
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
			pathKey := artifactPathKey(parent.workspace, path)
			if existingID, pathExists := r.paths[pathKey]; pathExists {
				childID = existingID
				existing := r.entries[childID]
				if existing.Name == "" {
					existing.Name = filepath.ToSlash(relative)
					existing.Kind = KindArtifactFile
				}
				existing.directoryRoot = parent.Path
				existing.relativePath = filepath.Clean(relative)
				if existing.purposes == nil {
					existing.purposes = make(map[Purpose]struct{})
				}
				existing.purposes[parent.Purpose] = struct{}{}
				r.entries[childID] = existing
			} else {
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
					purposes:      map[Purpose]struct{}{parent.Purpose: {}},
				}
				r.order = append(r.order, childID)
				r.paths[pathKey] = childID
			}
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
		if record, ok := r.entries[id]; ok && record.hasPurpose(purpose) {
			result = append(result, record.Record)
		}
	}
	return result
}

// HasPurpose reports whether an artifact has the requested run-scoped capability.
func (r *Registry) HasPurpose(id string, purpose Purpose) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.entries[id]
	return ok && item.hasPurpose(purpose)
}

func (e entry) hasPurpose(purpose Purpose) bool {
	if len(e.purposes) > 0 {
		_, ok := e.purposes[purpose]
		return ok
	}
	return e.Purpose == purpose
}

func artifactPathKey(workspace, path string) string {
	workspace = canonicalArtifactPath(workspace)
	path = canonicalArtifactPath(path)
	return scopedArtifactPathKey(workspace, path)
}

func artifactLogicalPathKey(workspace, path string) string {
	workspace = canonicalArtifactPath(workspace)
	path, _ = filepath.Abs(filepath.Clean(path))
	return scopedArtifactPathKey(workspace, path)
}

func scopedArtifactPathKey(workspace, path string) string {
	key := workspace + "\x00" + path
	if runtime.GOOS == "windows" {
		return strings.ToLower(key)
	}
	return key
}

func canonicalArtifactPath(path string) string {
	path, _ = filepath.Abs(filepath.Clean(path))
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
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
