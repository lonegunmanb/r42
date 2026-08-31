package evidence

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
)

// ArtifactEvidenceAccess exposes authorized evidence artifacts. Filesystem
// paths are never model inputs.
type ArtifactEvidenceAccess struct {
	registry *artifactpkg.Registry
	allowed  map[string]struct{}
}

func NewArtifactEvidenceAccess(registry *artifactpkg.Registry, ids []string) (*ArtifactEvidenceAccess, error) {
	if registry == nil {
		return nil, errors.New("artifact registry is required")
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, err := registry.Record(id); err != nil {
			return nil, err
		}
		if registry.HasPurpose(id, artifactpkg.PurposeEvidence) {
			allowed[id] = struct{}{}
		}
	}
	return &ArtifactEvidenceAccess{registry: registry, allowed: allowed}, nil
}

func (a *ArtifactEvidenceAccess) HasArtifact(id string) bool {
	_, ok := a.allowed[id]
	return ok
}

func (a *ArtifactEvidenceAccess) Source(id string) (string, error) {
	if !a.HasArtifact(id) {
		return "", fmt.Errorf("unknown evidence artifact %q", id)
	}
	record, err := a.registry.Record(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(record.Source) == "" {
		return "", fmt.Errorf("evidence artifact %q has no source", id)
	}
	return record.Source, nil
}

func (a *ArtifactEvidenceAccess) ReadPage(id string, offsetBytes, maxBytes int) (artifactpkg.Page, error) {
	if !a.HasArtifact(id) {
		return artifactpkg.Page{}, fmt.Errorf("unknown evidence artifact %q", id)
	}
	return a.registry.ReadPage(id, offsetBytes, maxBytes)
}

func (a *ArtifactEvidenceAccess) ContainsNormalizedText(id, quote string) (bool, error) {
	if !a.HasArtifact(id) {
		return false, fmt.Errorf("unknown evidence artifact %q", id)
	}
	record, err := a.registry.Record(id)
	if err != nil {
		return false, err
	}
	content, err := os.ReadFile(record.Path)
	if err != nil {
		return false, fmt.Errorf("read evidence artifact %q: %w", id, err)
	}
	normalized := normalizeWhitespace(quote)
	return normalized != "" && strings.Contains(normalizeWhitespace(string(content)), normalized), nil
}

// Search returns bounded whitespace-normalized evidence matches.
func (a *ArtifactEvidenceAccess) Search(id, pattern string, caseSensitive bool, maxMatches, contextLines int) (ArtifactSearchResult, error) {
	if !a.HasArtifact(id) {
		return ArtifactSearchResult{}, fmt.Errorf("unknown evidence artifact %q", id)
	}
	return SearchArtifact(a.registry, id, pattern, caseSensitive, maxMatches, contextLines)
}

// SearchArtifact searches one registered file artifact without applying an
// evidence-purpose restriction. Callers must enforce their artifact capability.
func SearchArtifact(registry *artifactpkg.Registry, id, pattern string, caseSensitive bool, maxMatches, contextLines int) (ArtifactSearchResult, error) {
	if registry == nil {
		return ArtifactSearchResult{}, errors.New("artifact registry is required")
	}
	record, err := registry.Record(id)
	if err != nil {
		return ArtifactSearchResult{}, err
	}
	if record.Type != "file" {
		return ArtifactSearchResult{}, fmt.Errorf("artifact %q is not a file", id)
	}
	info, err := os.Stat(record.Path)
	if err != nil {
		return ArtifactSearchResult{}, err
	}
	if err = validateArtifactSearchSize(info.Size()); err != nil {
		return ArtifactSearchResult{}, err
	}
	content, err := os.ReadFile(record.Path)
	if err != nil {
		return ArtifactSearchResult{}, err
	}
	normalized, lineStarts := normalizeWhitespaceWithLines(string(content))
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	expression := pattern
	if !caseSensitive {
		expression = "(?i:" + pattern + ")"
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return ArtifactSearchResult{}, fmt.Errorf("compile evidence artifact search pattern: %w", err)
	}
	if compiled.MatchString("") {
		return ArtifactSearchResult{}, errors.New("evidence artifact search pattern must not match empty text")
	}
	indices := compiled.FindAllStringIndex(normalized, maxMatches+1)
	for _, index := range indices {
		if index[0] == index[1] || utf8.RuneCountInString(normalized[index[0]:index[1]]) > maxArtifactSearchMatchRunes {
			return ArtifactSearchResult{}, errors.New("evidence artifact search pattern produced an invalid match")
		}
	}
	truncated := len(indices) > maxMatches
	if truncated {
		indices = indices[:maxMatches]
	}
	matches := make([]ArtifactSearchMatch, 0, len(indices))
	for _, index := range indices {
		lineIndex := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i].normalizedByte > index[0] }) - 1
		line := lineStarts[lineIndex].sourceLine
		endLineIndex := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i].normalizedByte >= index[1] }) - 1
		endLine := lineStarts[max(0, endLineIndex)].sourceLine
		start, end := normalizedLineRange(lineStarts, max(1, line-contextLines), line+contextLines, len(normalized))
		matches = append(matches, ArtifactSearchMatch{
			Line: line, EndLine: endLine, MatchedText: normalized[index[0]:index[1]],
			Excerpt:        boundedArtifactExcerpt(normalized[start:end], index[0]-start, index[1]-start),
			artifactDigest: digest, normalizedStart: index[0], normalizedEnd: index[1],
		})
	}
	return ArtifactSearchResult{Matches: matches, Truncated: truncated}, nil
}
