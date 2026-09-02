// Package evidence provides restricted artifact access and controlled Markdown
// output for Collection, Research, and QC sessions.
package evidence

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxArtifactSearchBytes        = 32 << 20
	maxArtifactSearchMatchRunes   = 2000
	maxArtifactSearchExcerptRunes = 4000
)

type ArtifactSearchMatch struct {
	QuoteRef    string `json:"quote_ref,omitempty"`
	SubmitReady bool   `json:"submit_ready,omitempty"`
	Line        int    `json:"line"`
	EndLine     int    `json:"end_line"`
	MatchedText string `json:"matched_text"`
	Excerpt     string `json:"excerpt"`

	artifactDigest  string
	normalizedStart int
	normalizedEnd   int
}

type ArtifactSearchResult struct {
	Matches   []ArtifactSearchMatch `json:"matches"`
	Truncated bool                  `json:"truncated"`
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

func boundedArtifactExcerpt(text string, matchStartByte, matchEndByte int) string {
	runes := []rune(text)
	if len(runes) <= maxArtifactSearchExcerptRunes {
		return text
	}
	matchStart := utf8.RuneCountInString(text[:matchStartByte])
	matchEnd := matchStart + utf8.RuneCountInString(text[matchStartByte:matchEndByte])
	availableContext := maxArtifactSearchExcerptRunes - (matchEnd - matchStart)
	start := max(0, matchStart-availableContext/2)
	if start+maxArtifactSearchExcerptRunes > len(runes) {
		start = len(runes) - maxArtifactSearchExcerptRunes
	}
	return string(runes[start : start+maxArtifactSearchExcerptRunes])
}

func validateArtifactSearchSize(size int64) error {
	if size > maxArtifactSearchBytes {
		return fmt.Errorf("artifact search size %d exceeds maximum %d bytes", size, maxArtifactSearchBytes)
	}
	return nil
}

// MarkdownWriter writes Markdown content to declared file artifacts inside the
// block workspace. It never follows symlinks or writes outside the workspace.
type MarkdownWriter struct {
	workspace string
}

// TextPatch describes one exact replacement in a declared text artifact.
type TextPatch struct {
	Expected    string `json:"expected"`
	Replacement string `json:"replacement"`
}

// ArtifactWriter applies a batch of non-overlapping exact text replacements
// to any declared file artifact.
type ArtifactWriter struct {
	workspace string
}

func NewArtifactWriter(workspace string) (*ArtifactWriter, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("artifact workspace is required")
	}
	return &ArtifactWriter{workspace: workspace}, nil
}

func (w *ArtifactWriter) Patch(path string, patches []TextPatch) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("artifact path is required")
	}
	if len(patches) == 0 {
		return "", errors.New("artifact patch requires at least one patch")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if !withinWorkspace(w.workspace, resolved) {
		return "", errors.New("artifact path is outside the block workspace")
	}
	secure, err := resolveWithin(w.workspace, resolved, "artifact")
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(secure)
	if err != nil {
		return "", fmt.Errorf("read artifact: %w", err)
	}
	info, err := os.Stat(secure)
	if err != nil {
		return "", fmt.Errorf("stat artifact: %w", err)
	}
	type match struct {
		start       int
		end         int
		replacement string
	}
	matches := make([]match, 0, len(patches))
	for _, patch := range patches {
		if strings.TrimSpace(patch.Expected) == "" {
			return "", errors.New("artifact patch expected text must not be empty")
		}
		if strings.Count(string(content), patch.Expected) != 1 {
			return "", errors.New("artifact patch expected text must occur exactly once")
		}
		start := strings.Index(string(content), patch.Expected)
		matches = append(matches, match{start: start, end: start + len(patch.Expected), replacement: patch.Replacement})
	}
	for i := range matches {
		for j := i + 1; j < len(matches); j++ {
			if matches[i].start < matches[j].end && matches[j].start < matches[i].end {
				return "", errors.New("artifact patches overlap")
			}
		}
	}
	slices.SortFunc(matches, func(a, b match) int { return b.start - a.start })
	updated := string(content)
	for _, item := range matches {
		updated = updated[:item.start] + item.replacement + updated[item.end:]
	}
	if err := atomicWriteFile(secure, []byte(updated), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("write artifact patch: %w", err)
	}
	return secure, nil
}

func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".r42-patch-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(content)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func NewMarkdownWriter(workspace string) (*MarkdownWriter, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("markdown workspace is required")
	}
	return &MarkdownWriter{workspace: workspace}, nil
}

func (w *MarkdownWriter) Write(path, content string) (string, error) {
	return w.write(path, content, false)
}

func (w *MarkdownWriter) WriteNew(path, content string) (string, error) {
	return w.write(path, content, true)
}

// Patch replaces one exact occurrence in the non-Sources portion of a
// declared Markdown artifact. The expected text must occur exactly once.
func (w *MarkdownWriter) Patch(path, expected, replacement string) (string, error) {
	if strings.TrimSpace(expected) == "" {
		return "", errors.New("markdown patch expected text must not be empty")
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".md") {
		return "", errors.New("markdown writer only accepts .md artifacts")
	}
	if _, sourceStart, _ := markdownBody(replacement); sourceStart >= 0 {
		return "", errors.New("markdown patch cannot add a Sources section")
	}
	resolved, err := w.absolute(path)
	if err != nil {
		return "", err
	}
	if !withinWorkspace(w.workspace, resolved) {
		return "", errors.New("markdown artifact path is outside the block workspace")
	}
	secure, err := resolveWithin(w.workspace, resolved, "markdown")
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(secure)
	if err != nil {
		return "", fmt.Errorf("read markdown artifact: %w", err)
	}
	text := string(content)
	body, sourceStart, sourceEnd := markdownBody(text)
	if strings.Count(body, expected) != 1 {
		if sourceStart >= 0 && strings.Contains(text[sourceStart:sourceEnd], expected) {
			return "", errors.New("markdown patch cannot modify the Sources section")
		}
		return "", errors.New("markdown patch expected text must occur exactly once outside the Sources section")
	}
	index := strings.Index(body, expected)
	updated := body[:index] + replacement + body[index+len(expected):]
	if sourceStart >= 0 {
		updated += text[sourceStart:sourceEnd]
		updated += text[sourceEnd:]
	}
	return w.Write(secure, updated)
}

func markdownBody(text string) (body string, sourceStart, sourceEnd int) {
	sourceStart, sourceEnd = -1, -1
	offset := 0
	lines := strings.SplitAfter(text, "\n")
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if strings.EqualFold(strings.TrimSpace(heading), "sources") {
				sourceStart = offset
				sourceEnd = offset + len(line)
				for end := index + 1; end < len(lines); end++ {
					if strings.HasPrefix(strings.TrimSpace(lines[end]), "#") {
						break
					}
					sourceEnd += len(lines[end])
				}
				break
			}
		}
		offset += len(line)
	}
	if sourceStart < 0 {
		return text, -1, -1
	}
	return text[:sourceStart], sourceStart, sourceEnd
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
	if !withinWorkspace(w.workspace, resolved) {
		return "", errors.New("markdown artifact path is outside the block workspace")
	}
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
		return "", fmt.Errorf("resolve markdown artifact path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func resolveWithin(workspace, path, kind string) (string, error) {
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
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
	target := resolvedAncestor
	for _, component := range missing {
		target = filepath.Join(target, component)
	}
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
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
