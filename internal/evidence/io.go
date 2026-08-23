// Package evidence exposes restricted read-only snapshot and candidate
// artifact access plus a controlled Markdown writer. It implements the
// capability boundary for Research and both QC sessions: no arbitrary
// filesystem reads or writes.
package evidence

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lonegunmanb/r42/internal/snapshot"
)

const (
	maxSnapshotSearchBytes        = 32 << 20
	maxSnapshotSearchMatchRunes   = 2000
	maxSnapshotSearchExcerptRunes = 4000
)

// SnapshotInfo is a listable snapshot projection.
type SnapshotInfo struct {
	ID   string
	Path string
}

// SnapshotAccess provides read-only access to registered snapshots. Paths are
// resolved by snapshot ID only; callers never pass filesystem paths.
type SnapshotAccess struct {
	registry          *snapshot.Registry
	upstream          map[string]string
	reviewedLocalOnly bool
}

// SnapshotPage is a bounded byte range from a snapshot.
type SnapshotPage struct {
	Content         string `json:"content"`
	OffsetBytes     int    `json:"offset_bytes"`
	NextOffsetBytes int    `json:"next_offset_bytes"`
	TotalBytes      int    `json:"total_bytes"`
	Truncated       bool   `json:"truncated"`
}

// SnapshotSearchMatch is one normalized-text match and its source context.
type SnapshotSearchMatch struct {
	Line        int    `json:"line"`
	MatchedText string `json:"matched_text"`
	Excerpt     string `json:"excerpt"`
}

// SnapshotSearchResult is a bounded set of snapshot matches.
type SnapshotSearchResult struct {
	Matches   []SnapshotSearchMatch `json:"matches"`
	Truncated bool                  `json:"truncated"`
}

// NewSnapshotAccess creates snapshot read access over a registry rooted at the
// given workspace.
func NewSnapshotAccess(workspace string) (*SnapshotAccess, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("snapshot workspace is required")
	}
	return &SnapshotAccess{registry: snapshot.NewRegistry(workspace)}, nil
}

// NewSnapshotAccessWithRegistry creates read access over the Collection
// registry owned by the same workflow instance.
func NewSnapshotAccessWithRegistry(registry *snapshot.Registry) (*SnapshotAccess, error) {
	if registry == nil {
		return nil, errors.New("snapshot registry is required")
	}
	return &SnapshotAccess{registry: registry}, nil
}

// NewSnapshotAccessWithRegistryAndUpstream creates read access over the local
// Collection registry plus an explicitly authorized upstream ID-to-path view.
func NewSnapshotAccessWithRegistryAndUpstream(
	registry *snapshot.Registry,
	upstream map[string]string,
) (*SnapshotAccess, error) {
	access, err := NewSnapshotAccessWithRegistry(registry)
	if err != nil {
		return nil, err
	}
	access.upstream = make(map[string]string, len(upstream))
	access.reviewedLocalOnly = true
	maps.Copy(access.upstream, upstream)
	return access, nil
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
	page, err := a.ReadSnapshotPage(id, 0, maxBytes)
	return page.Content, err
}

// ReadSnapshotPage reads a bounded byte range from a registered snapshot.
func (a *SnapshotAccess) ReadSnapshotPage(id string, offsetBytes, maxBytes int) (SnapshotPage, error) {
	if offsetBytes < 0 {
		return SnapshotPage{}, errors.New("snapshot offset must not be negative")
	}
	if maxBytes <= 0 {
		return SnapshotPage{}, errors.New("read bound must be positive")
	}
	path, err := a.authorizedPath(id)
	if err != nil {
		return SnapshotPage{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return SnapshotPage{}, fmt.Errorf("open snapshot %q: %w", id, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return SnapshotPage{}, fmt.Errorf("stat snapshot %q: %w", id, err)
	}
	totalBytes := int(info.Size())
	if offsetBytes > totalBytes {
		return SnapshotPage{}, fmt.Errorf("snapshot offset %d exceeds total bytes %d", offsetBytes, totalBytes)
	}
	if offsetBytes < totalBytes {
		boundary := make([]byte, 1)
		if _, err = file.ReadAt(boundary, int64(offsetBytes)); err != nil {
			return SnapshotPage{}, fmt.Errorf("read snapshot %q offset: %w", id, err)
		}
		if !utf8.RuneStart(boundary[0]) {
			return SnapshotPage{}, fmt.Errorf("snapshot offset %d is not a utf-8 character boundary", offsetBytes)
		}
	}
	if _, err = file.Seek(int64(offsetBytes), io.SeekStart); err != nil {
		return SnapshotPage{}, fmt.Errorf("seek snapshot %q: %w", id, err)
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)))
	if err != nil {
		return SnapshotPage{}, fmt.Errorf("read snapshot %q: %w", id, err)
	}
	for len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
	}
	if len(content) == 0 && offsetBytes < totalBytes {
		return SnapshotPage{}, fmt.Errorf("max_bytes %d is too small for the next utf-8 character", maxBytes)
	}
	nextOffset := offsetBytes + len(content)
	return SnapshotPage{
		Content:         string(content),
		OffsetBytes:     offsetBytes,
		NextOffsetBytes: nextOffset,
		TotalBytes:      totalBytes,
		Truncated:       nextOffset < totalBytes,
	}, nil
}

// SearchSnapshot searches Unicode-whitespace-normalized snapshot text with a
// RE2 pattern and returns bounded source context.
func (a *SnapshotAccess) SearchSnapshot(
	id, pattern string,
	caseSensitive bool,
	maxMatches, contextLines int,
) (SnapshotSearchResult, error) {
	if strings.TrimSpace(pattern) == "" {
		return SnapshotSearchResult{}, errors.New("snapshot search pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 100 {
		return SnapshotSearchResult{}, errors.New("snapshot search max matches must be between 1 and 100")
	}
	if contextLines < 0 || contextLines > 20 {
		return SnapshotSearchResult{}, errors.New("snapshot search context lines must be between 0 and 20")
	}
	path, err := a.authorizedPath(id)
	if err != nil {
		return SnapshotSearchResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return SnapshotSearchResult{}, fmt.Errorf("stat snapshot %q: %w", id, err)
	}
	if err = validateSnapshotSearchSize(info.Size()); err != nil {
		return SnapshotSearchResult{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return SnapshotSearchResult{}, fmt.Errorf("read snapshot %q: %w", id, err)
	}
	normalized, lineStarts := normalizeWhitespaceWithLines(string(content))
	expression := pattern
	if !caseSensitive {
		expression = "(?i:" + pattern + ")"
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return SnapshotSearchResult{}, fmt.Errorf("compile snapshot search pattern: %w", err)
	}
	if compiled.MatchString("") {
		return SnapshotSearchResult{}, errors.New("snapshot search pattern must not match empty text")
	}
	indices := compiled.FindAllStringIndex(normalized, maxMatches+1)
	for _, index := range indices {
		if index[0] == index[1] {
			return SnapshotSearchResult{}, errors.New("snapshot search pattern produced a zero-width match")
		}
		if utf8.RuneCountInString(normalized[index[0]:index[1]]) > maxSnapshotSearchMatchRunes {
			return SnapshotSearchResult{}, fmt.Errorf(
				"snapshot search match exceeds maximum %d characters",
				maxSnapshotSearchMatchRunes,
			)
		}
	}
	isTruncated := len(indices) > maxMatches
	if isTruncated {
		indices = indices[:maxMatches]
	}
	matches := make([]SnapshotSearchMatch, 0, len(indices))
	for _, index := range indices {
		lineIndex := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i].normalizedByte > index[0] }) - 1
		line := lineStarts[lineIndex].sourceLine
		startLine := max(1, line-contextLines)
		endLine := line + contextLines
		excerptStart, excerptEnd := normalizedLineRange(lineStarts, startLine, endLine, len(normalized))
		matches = append(matches, SnapshotSearchMatch{
			Line:        line,
			MatchedText: normalized[index[0]:index[1]],
			Excerpt: boundedSnapshotExcerpt(
				normalized[excerptStart:excerptEnd],
				index[0]-excerptStart,
				index[1]-excerptStart,
			),
		})
	}
	return SnapshotSearchResult{Matches: matches, Truncated: isTruncated}, nil
}

// HasSnapshot reports whether an ID is available through this authorized view.
func (a *SnapshotAccess) HasSnapshot(id string) bool {
	_, err := a.authorizedPath(id)
	return err == nil
}

// ContainsNormalizedText validates a quote while ignoring Unicode whitespace
// layout differences and preserving all other text, punctuation, and order.
func (a *SnapshotAccess) ContainsNormalizedText(id, quote string) (bool, error) {
	path, err := a.authorizedPath(id)
	if err != nil {
		return false, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read snapshot %q: %w", id, err)
	}
	normalizedQuote := normalizeWhitespace(quote)
	return normalizedQuote != "" && strings.Contains(normalizeWhitespace(string(content)), normalizedQuote), nil
}

// SnapshotSource returns the source identifier recorded in the snapshot
// Markdown header. The legacy URL header remains readable for existing runs.
func (a *SnapshotAccess) SnapshotSource(id string) (string, error) {
	path, err := a.authorizedPath(id)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open snapshot %q: %w", id, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	foundHeader := false
	for range 64 {
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		for _, prefix := range []string{"- Source:", "- URL:"} {
			if value, found := strings.CutPrefix(line, prefix); found {
				foundHeader = true
				source := strings.TrimSpace(value)
				if source != "" {
					return source, nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read snapshot %q header: %w", id, err)
	}
	if foundHeader {
		return "", fmt.Errorf("snapshot %q has an empty Source header", id)
	}
	return "", fmt.Errorf("snapshot %q has no Source header", id)
}

func (a *SnapshotAccess) authorizedPath(id string) (string, error) {
	path, err := a.registry.Snapshot(id)
	localAllowed := err == nil && (!a.reviewedLocalOnly || a.registry.IsReviewed(id))
	if localAllowed {
		return path, nil
	}
	if path, ok := a.upstream[id]; ok {
		return path, nil
	}
	return "", fmt.Errorf("unknown snapshot %q", id)
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

type normalizedLineStart struct {
	normalizedByte int
	sourceLine     int
}

func normalizeWhitespaceWithLines(value string) (string, []normalizedLineStart) {
	var normalized strings.Builder
	lineStarts := make([]normalizedLineStart, 0)
	line := 1
	hasPendingSpace := false
	lastMappedLine := 0
	for _, character := range value {
		if character == '\n' {
			line++
		}
		if unicode.IsSpace(character) {
			hasPendingSpace = normalized.Len() > 0
			continue
		}
		if hasPendingSpace {
			normalized.WriteByte(' ')
			hasPendingSpace = false
		}
		if line != lastMappedLine {
			lineStarts = append(lineStarts, normalizedLineStart{normalizedByte: normalized.Len(), sourceLine: line})
			lastMappedLine = line
		}
		normalized.WriteRune(character)
	}
	return normalized.String(), lineStarts
}

func normalizedLineRange(lineStarts []normalizedLineStart, startLine, endLine, totalBytes int) (int, int) {
	startIndex := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i].sourceLine >= startLine })
	start := totalBytes
	if startIndex < len(lineStarts) {
		start = lineStarts[startIndex].normalizedByte
	}
	endIndex := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i].sourceLine > endLine })
	end := totalBytes
	if endIndex < len(lineStarts) {
		end = lineStarts[endIndex].normalizedByte
	}
	return start, end
}

func boundedSnapshotExcerpt(text string, matchStartByte, matchEndByte int) string {
	runes := []rune(text)
	if len(runes) <= maxSnapshotSearchExcerptRunes {
		return text
	}
	matchStart := utf8.RuneCountInString(text[:matchStartByte])
	matchEnd := matchStart + utf8.RuneCountInString(text[matchStartByte:matchEndByte])
	availableContext := maxSnapshotSearchExcerptRunes - (matchEnd - matchStart)
	start := max(0, matchStart-availableContext/2)
	if start+maxSnapshotSearchExcerptRunes > len(runes) {
		start = len(runes) - maxSnapshotSearchExcerptRunes
	}
	return string(runes[start : start+maxSnapshotSearchExcerptRunes])
}

func validateSnapshotSearchSize(size int64) error {
	if size > maxSnapshotSearchBytes {
		return fmt.Errorf("snapshot search size %d exceeds maximum %d bytes", size, maxSnapshotSearchBytes)
	}
	return nil
}

// ArtifactAccess provides read-only access to declared candidate artifacts.
// Artifact paths are constrained to the block workspace.
type ArtifactAccess struct {
	workspace string
}

// ArtifactPage is a bounded byte range from a declared artifact.
type ArtifactPage struct {
	Content         string `json:"content"`
	OffsetBytes     int    `json:"offset_bytes"`
	NextOffsetBytes int    `json:"next_offset_bytes"`
	TotalBytes      int    `json:"total_bytes"`
	Truncated       bool   `json:"truncated"`
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
	page, err := a.ReadArtifactPage(name, path, 0, maxBytes)
	return page.Content, err
}

// ReadArtifactPage reads a bounded byte range from a declared artifact.
func (a *ArtifactAccess) ReadArtifactPage(name, path string, offsetBytes, maxBytes int) (ArtifactPage, error) {
	if offsetBytes < 0 {
		return ArtifactPage{}, errors.New("artifact offset must not be negative")
	}
	if maxBytes <= 0 {
		return ArtifactPage{}, errors.New("read bound must be positive")
	}
	resolved, err := a.resolve(name, path)
	if err != nil {
		return ArtifactPage{}, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return ArtifactPage{}, fmt.Errorf("open artifact %q: %w", name, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ArtifactPage{}, fmt.Errorf("stat artifact %q: %w", name, err)
	}
	totalBytes := int(info.Size())
	if offsetBytes > totalBytes {
		return ArtifactPage{}, fmt.Errorf("artifact offset %d exceeds total bytes %d", offsetBytes, totalBytes)
	}
	if offsetBytes < totalBytes {
		boundary := make([]byte, 1)
		if _, err = file.ReadAt(boundary, int64(offsetBytes)); err != nil {
			return ArtifactPage{}, fmt.Errorf("read artifact %q offset: %w", name, err)
		}
		if !utf8.RuneStart(boundary[0]) {
			return ArtifactPage{}, fmt.Errorf("artifact offset %d is not a utf-8 character boundary", offsetBytes)
		}
	}
	if _, err = file.Seek(int64(offsetBytes), io.SeekStart); err != nil {
		return ArtifactPage{}, fmt.Errorf("seek artifact %q: %w", name, err)
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)))
	if err != nil {
		return ArtifactPage{}, fmt.Errorf("read artifact %q: %w", name, err)
	}
	for len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
	}
	if len(content) == 0 && offsetBytes < totalBytes {
		return ArtifactPage{}, fmt.Errorf("max_bytes %d is too small for the next utf-8 character", maxBytes)
	}
	nextOffset := offsetBytes + len(content)
	return ArtifactPage{
		Content: string(content), OffsetBytes: offsetBytes, NextOffsetBytes: nextOffset,
		TotalBytes: totalBytes, Truncated: nextOffset < totalBytes,
	}, nil
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
	secure, err := resolveWithin(a.workspace, resolved, "candidate")
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
	return w.write(path, content, false)
}

// WriteNew writes Markdown content only when the target path does not exist.
func (w *MarkdownWriter) WriteNew(path, content string) (string, error) {
	return w.write(path, content, true)
}

func (w *MarkdownWriter) write(path, content string, exclusive bool) (string, error) {
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
	// Resolve and verify the parent directory before creating anything so a
	// symlinked parent cannot create directories outside the workspace.
	secure, err := resolveWithin(w.workspace, resolved, "markdown")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(secure), 0o755); err != nil {
		return "", fmt.Errorf("create artifact directory: %w", err)
	}
	if !exclusive {
		if err := os.WriteFile(secure, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("write markdown artifact: %w", err)
		}
		return secure, nil
	}
	file, err := os.OpenFile(secure, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("write markdown artifact: %w", err)
	}
	_, writeErr := io.WriteString(file, content)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(secure)
		return "", fmt.Errorf("write markdown artifact: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(secure)
		return "", fmt.Errorf("close markdown artifact: %w", closeErr)
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
// final component (or any ancestor) that points outside is rejected. The kind
// label appears in the rejection message. Missing ancestors are tolerated so
// nested directories can still be created by the caller.
func resolveWithin(workspace, path, kind string) (string, error) {
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	// Find the nearest existing ancestor so a not-yet-created parent does not
	// break resolution; its resolved form must stay inside the workspace.
	existing := path
	missing := []string{}
	for {
		_, statErr := os.Lstat(existing)
		if statErr == nil {
			break
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("resolve artifact path: %w", statErr)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve artifact directory: %w", err)
	}
	if !withinWorkspace(resolvedWorkspace, resolvedAncestor) {
		return "", fmt.Errorf("%s artifact path is outside the block workspace", kind)
	}
	// Rebuild the full path from the resolved ancestor plus the missing tail.
	target := resolvedAncestor
	for _, component := range missing {
		target = filepath.Join(target, component)
	}
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
		return "", fmt.Errorf("%s artifact path is outside the block workspace", kind)
	}
	return resolvedTarget, nil
}

func withinWorkspace(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
