package evidence

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
)

const maxCapturedQuoteRunes = 12000

// QuoteRecord is immutable evidence text captured by a host-provided tool.
type QuoteRecord struct {
	Ref            string `json:"quote_ref"`
	ArtifactID     string `json:"artifact_id"`
	ArtifactDigest string `json:"artifact_digest"`
	SourceTitle    string `json:"source_title"`
	URL            string `json:"url"`
	Locator        string `json:"locator"`
	ExactQuote     string `json:"exact_quote"`

	startLine       int
	endLine         int
	normalizedStart int
	normalizedEnd   int
}

// QuoteRegistry owns trusted quote references for one apply run.
type QuoteRegistry struct {
	mu      sync.RWMutex
	byRef   map[string]QuoteRecord
	byRange map[string]string
}

func NewQuoteRegistry() *QuoteRegistry {
	return &QuoteRegistry{byRef: make(map[string]QuoteRecord), byRange: make(map[string]string)}
}

func (r *QuoteRegistry) CaptureMatch(registry *artifactpkg.Registry, artifactID string, match ArtifactSearchMatch) (QuoteRecord, error) {
	if r == nil {
		return QuoteRecord{}, errors.New("quote registry is required")
	}
	if registry == nil {
		return QuoteRecord{}, errors.New("artifact registry is required")
	}
	if match.artifactDigest == "" || match.normalizedStart < 0 || match.normalizedEnd <= match.normalizedStart {
		return QuoteRecord{}, errors.New("quote must originate from an artifact search result")
	}
	record, err := registry.Record(artifactID)
	if err != nil {
		return QuoteRecord{}, err
	}
	quote := QuoteRecord{
		ArtifactID: artifactID, ArtifactDigest: match.artifactDigest,
		SourceTitle: record.Description, URL: record.Source,
		Locator: lineLocator(match.Line, match.EndLine), ExactQuote: match.MatchedText,
		startLine: match.Line, endLine: match.EndLine,
		normalizedStart: match.normalizedStart, normalizedEnd: match.normalizedEnd,
	}
	return r.store(quote), nil
}

func (r *QuoteRegistry) Expand(registry *artifactpkg.Registry, ref string, beforeLines, afterLines int) (QuoteRecord, error) {
	if r == nil {
		return QuoteRecord{}, errors.New("quote registry is required")
	}
	if registry == nil {
		return QuoteRecord{}, errors.New("artifact registry is required")
	}
	if beforeLines < 0 || afterLines < 0 || beforeLines > 20 || afterLines > 20 {
		return QuoteRecord{}, errors.New("quote context lines must be between 0 and 20")
	}
	base, ok := r.Resolve(ref)
	if !ok {
		return QuoteRecord{}, fmt.Errorf("unknown quote reference %q", ref)
	}
	record, err := registry.Record(base.ArtifactID)
	if err != nil {
		return QuoteRecord{}, err
	}
	content, err := os.ReadFile(record.Path)
	if err != nil {
		return QuoteRecord{}, fmt.Errorf("read evidence artifact %q: %w", base.ArtifactID, err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	if digest != base.ArtifactDigest {
		return QuoteRecord{}, fmt.Errorf("artifact changed after quote %q was captured", ref)
	}
	normalized, lineStarts := normalizeWhitespaceWithLines(string(content))
	startLine := max(1, base.startLine-beforeLines)
	endLine := min(base.endLine+afterLines, lineStarts[len(lineStarts)-1].sourceLine)
	start, end := normalizedLineRange(lineStarts, startLine, endLine, len(normalized))
	if start >= end {
		return QuoteRecord{}, errors.New("expanded quote is empty")
	}
	exact := strings.TrimSpace(normalized[start:end])
	if len([]rune(exact)) > maxCapturedQuoteRunes {
		return QuoteRecord{}, fmt.Errorf("expanded quote exceeds maximum %d characters", maxCapturedQuoteRunes)
	}
	expanded := QuoteRecord{
		ArtifactID: base.ArtifactID, ArtifactDigest: base.ArtifactDigest,
		SourceTitle: base.SourceTitle, URL: base.URL,
		Locator: lineLocator(startLine, endLine), ExactQuote: exact,
		startLine: startLine, endLine: endLine, normalizedStart: start, normalizedEnd: end,
	}
	return r.store(expanded), nil
}

func (r *QuoteRegistry) Resolve(ref string) (QuoteRecord, bool) {
	if r == nil {
		return QuoteRecord{}, false
	}
	r.mu.RLock()
	record, ok := r.byRef[strings.TrimSpace(ref)]
	r.mu.RUnlock()
	return record, ok
}

func (r *QuoteRegistry) store(record QuoteRecord) QuoteRecord {
	key := fmt.Sprintf("%s\x00%s\x00%d\x00%d", record.ArtifactID, record.ArtifactDigest, record.normalizedStart, record.normalizedEnd)
	sum := sha256.Sum256([]byte(key))
	ref := fmt.Sprintf("quote-ref-%x", sum[:16])
	r.mu.Lock()
	defer r.mu.Unlock()
	if existingRef, ok := r.byRange[key]; ok {
		return r.byRef[existingRef]
	}
	record.Ref = ref
	r.byRef[ref] = record
	r.byRange[key] = ref
	return record
}

func lineLocator(start, end int) string {
	if start == end {
		return fmt.Sprintf("line %d", start)
	}
	return fmt.Sprintf("lines %d-%d", start, end)
}
