package evidence

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
)

const maxCapturedQuoteRunes = 12000

// QuoteRecord is immutable evidence text captured by a host-provided tool.
type QuoteRecord struct {
	Ref            string `json:"quote_ref"`
	SubmitReady    bool   `json:"submit_ready"`
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

// CanonicalMap returns the quote fields that may cross the typed-tool boundary.
// Host-only state such as SubmitReady is intentionally excluded.
func (r QuoteRecord) CanonicalMap() map[string]any {
	return map[string]any{
		"_r42_quote":      true,
		"quote_ref":       r.Ref,
		"artifact_id":     r.ArtifactID,
		"artifact_digest": r.ArtifactDigest,
		"source_title":    r.SourceTitle,
		"url":             r.URL,
		"locator":         r.Locator,
		"exact_quote":     r.ExactQuote,
	}
}

// QuoteRegistry owns trusted quote references for one apply run.
type QuoteRegistry struct {
	mu      sync.RWMutex
	byRef   map[string]QuoteRecord
	byRange map[string]string
}

// QuoteSnapshot is the trusted quote-reference state needed by a later
// workflow phase. It contains no artifact contents; those are checkpointed by
// the artifact registry separately.
type QuoteSnapshot struct {
	Records []QuoteSnapshotRecord `json:"records"`
}

// QuoteSnapshotRecord is the serializable host-owned portion of one quote.
type QuoteSnapshotRecord struct {
	Ref             string `json:"quote_ref"`
	SubmitReady     bool   `json:"submit_ready"`
	ArtifactID      string `json:"artifact_id"`
	ArtifactDigest  string `json:"artifact_digest"`
	SourceTitle     string `json:"source_title"`
	URL             string `json:"url"`
	Locator         string `json:"locator"`
	ExactQuote      string `json:"exact_quote"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	NormalizedStart int    `json:"normalized_start"`
	NormalizedEnd   int    `json:"normalized_end"`
}

func NewQuoteRegistry() *QuoteRegistry {
	return &QuoteRegistry{byRef: make(map[string]QuoteRecord), byRange: make(map[string]string)}
}

// Snapshot returns a deterministic copy of all quote references captured so
// far. It is safe to persist at a workflow handoff.
func (r *QuoteRegistry) Snapshot() QuoteSnapshot {
	if r == nil {
		return QuoteSnapshot{}
	}
	r.mu.RLock()
	records := make([]QuoteSnapshotRecord, 0, len(r.byRef))
	for _, quote := range r.byRef {
		records = append(records, quoteSnapshotRecord(quote))
	}
	r.mu.RUnlock()
	slices.SortFunc(records, func(left, right QuoteSnapshotRecord) int { return strings.Compare(left.Ref, right.Ref) })
	return QuoteSnapshot{Records: records}
}

// Restore replaces quote references from a checkpoint. References are
// re-derived from their immutable range identity to reject corrupted payloads.
func (r *QuoteRegistry) Restore(snapshot QuoteSnapshot) error {
	if r == nil {
		return errors.New("quote registry is required")
	}
	byRef, byRange, err := quoteSnapshotMaps(snapshot)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.byRef = byRef
	r.byRange = byRange
	r.mu.Unlock()
	return nil
}

// Merge adds quote references captured by another workflow checkpoint without
// discarding refs already restored for sibling workflows.
func (r *QuoteRegistry) Merge(snapshot QuoteSnapshot) error {
	if r == nil {
		return errors.New("quote registry is required")
	}
	byRef, _, err := quoteSnapshotMaps(snapshot)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for ref, quote := range byRef {
		if existing, exists := r.byRef[ref]; exists {
			if existing != quote {
				return fmt.Errorf("quote checkpoint conflicts with reference %q", ref)
			}
			continue
		}
		key := quoteRangeKey(quote)
		if existingRef, exists := r.byRange[key]; exists && existingRef != ref {
			return fmt.Errorf("quote checkpoint conflicts with captured range")
		}
		r.byRef[ref] = quote
		r.byRange[key] = ref
	}
	return nil
}

func quoteSnapshotMaps(snapshot QuoteSnapshot) (map[string]QuoteRecord, map[string]string, error) {
	byRef := make(map[string]QuoteRecord, len(snapshot.Records))
	byRange := make(map[string]string, len(snapshot.Records))
	for index, saved := range snapshot.Records {
		quote, err := quoteFromSnapshotRecord(saved)
		if err != nil {
			return nil, nil, fmt.Errorf("restore quote %d: %w", index, err)
		}
		key := quoteRangeKey(quote)
		if _, exists := byRef[quote.Ref]; exists {
			return nil, nil, fmt.Errorf("restore quote %d: duplicate quote reference %q", index, quote.Ref)
		}
		if _, exists := byRange[key]; exists {
			return nil, nil, fmt.Errorf("restore quote %d: duplicate quote range", index)
		}
		byRef[quote.Ref] = quote
		byRange[key] = quote.Ref
	}
	return byRef, byRange, nil
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
		SubmitReady: true,
		ArtifactID:  artifactID, ArtifactDigest: match.artifactDigest,
		SourceTitle: record.Description, URL: record.Source,
		Locator: lineLocator(match.Line, match.EndLine), ExactQuote: match.MatchedText,
		startLine: match.Line, endLine: match.EndLine,
		normalizedStart: match.normalizedStart, normalizedEnd: match.normalizedEnd,
	}
	return r.store(quote), nil
}

// CaptureMatchWithContext stores a search match with bounded surrounding lines
// as a submit-ready canonical quote.
func (r *QuoteRegistry) CaptureMatchWithContext(
	registry *artifactpkg.Registry,
	artifactID string,
	match ArtifactSearchMatch,
	beforeLines, afterLines int,
) (QuoteRecord, error) {
	base, err := r.CaptureMatch(registry, artifactID, match)
	if err != nil {
		return QuoteRecord{}, err
	}
	if beforeLines == 0 && afterLines == 0 {
		return base, nil
	}
	return r.Expand(registry, base.Ref, beforeLines, afterLines)
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
		SubmitReady: true,
		ArtifactID:  base.ArtifactID, ArtifactDigest: base.ArtifactDigest,
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
	key := quoteRangeKey(record)
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

func quoteSnapshotRecord(quote QuoteRecord) QuoteSnapshotRecord {
	return QuoteSnapshotRecord{
		Ref: quote.Ref, SubmitReady: quote.SubmitReady, ArtifactID: quote.ArtifactID, ArtifactDigest: quote.ArtifactDigest,
		SourceTitle: quote.SourceTitle, URL: quote.URL, Locator: quote.Locator, ExactQuote: quote.ExactQuote,
		StartLine: quote.startLine, EndLine: quote.endLine, NormalizedStart: quote.normalizedStart, NormalizedEnd: quote.normalizedEnd,
	}
}

func quoteFromSnapshotRecord(saved QuoteSnapshotRecord) (QuoteRecord, error) {
	if !saved.SubmitReady || strings.TrimSpace(saved.ArtifactID) == "" || strings.TrimSpace(saved.ArtifactDigest) == "" ||
		strings.TrimSpace(saved.ExactQuote) == "" || saved.StartLine <= 0 || saved.EndLine < saved.StartLine ||
		saved.NormalizedStart < 0 || saved.NormalizedEnd <= saved.NormalizedStart {
		return QuoteRecord{}, errors.New("invalid quote snapshot")
	}
	quote := QuoteRecord{
		Ref: saved.Ref, SubmitReady: saved.SubmitReady, ArtifactID: saved.ArtifactID, ArtifactDigest: saved.ArtifactDigest,
		SourceTitle: saved.SourceTitle, URL: saved.URL, Locator: saved.Locator, ExactQuote: saved.ExactQuote,
		startLine: saved.StartLine, endLine: saved.EndLine, normalizedStart: saved.NormalizedStart, normalizedEnd: saved.NormalizedEnd,
	}
	key := quoteRangeKey(quote)
	sum := sha256.Sum256([]byte(key))
	expected := fmt.Sprintf("quote-ref-%x", sum[:16])
	if quote.Ref != expected {
		return QuoteRecord{}, fmt.Errorf("quote reference integrity check failed for %q", quote.Ref)
	}
	return quote, nil
}

func quoteRangeKey(record QuoteRecord) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", record.ArtifactID, record.ArtifactDigest, record.normalizedStart, record.normalizedEnd)
}

func lineLocator(start, end int) string {
	if start == end {
		return fmt.Sprintf("line %d", start)
	}
	return fmt.Sprintf("lines %d-%d", start, end)
}
