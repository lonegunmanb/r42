package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

const sourceQuoteIDPatternText = `[A-Za-z0-9][A-Za-z0-9_-]*-quote-[A-Za-z0-9][A-Za-z0-9_-]*`

var sourceCitationPattern = regexp.MustCompile(`\[(` + sourceQuoteIDPatternText + `)\](?:\([^\)\r\n]*\))?`)

// GenerateSourceTable rebuilds the Sources section from quote metadata in the
// validated knowledge artifacts. It returns the number of generated rows.
func GenerateSourceTable(reportPath string, knowledgePaths []string) (int, error) {
	if strings.TrimSpace(reportPath) == "" {
		return 0, errors.New("report path is required")
	}
	if len(knowledgePaths) == 0 {
		return 0, errors.New("knowledge paths are required")
	}
	quotes := make(map[string][]string)
	for _, path := range knowledgePaths {
		content, err := os.ReadFile(path)
		if err != nil {
			return 0, fmt.Errorf("read knowledge artifact %q: %w", path, err)
		}
		var document struct {
			Quotes []map[string]any `json:"quotes"`
		}
		if err := json.Unmarshal(content, &document); err != nil {
			return 0, fmt.Errorf("decode knowledge artifact %q: %w", path, err)
		}
		for _, quote := range document.Quotes {
			id, _ := quote["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			urls := canonicalQuoteURLs(quote)
			if previous, exists := quotes[id]; exists && !sameStrings(previous, urls) {
				return 0, fmt.Errorf("quote ID %q has conflicting canonical URLs", id)
			}
			quotes[id] = urls
		}
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		return 0, fmt.Errorf("read report: %w", err)
	}
	text := string(report)
	body, sourceStart, sourceEnd := markdownBody(text)
	ids := citationIDs(body)
	for _, id := range ids {
		if _, exists := quotes[id]; !exists {
			return 0, fmt.Errorf("quote ID %q has no canonical URL", id)
		}
	}
	body = rewriteQuoteCitations(body, ids, quotes)
	ids = citationIDs(body)
	for _, id := range ids {
		urls := quotes[id]
		if len(urls) == 0 {
			return 0, fmt.Errorf("quote ID %q has no canonical URL", id)
		}
	}
	rows := make([]string, 0, len(ids)+4)
	rows = append(rows, "## Sources", "", "| Quote ID | URL |", "| --- | --- |")
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf("| %s | %s |", id, strings.Join(quotes[id], " ; ")))
	}
	section := strings.Join(rows, "\n") + "\n"
	var updated string
	if sourceStart < 0 {
		updated = strings.TrimRight(body, "\r\n") + "\n\n" + section
	} else {
		updated = strings.TrimRight(body, "\r\n") + "\n\n" + section
		if sourceEnd < len(text) {
			updated += strings.TrimLeft(text[sourceEnd:], "\r\n")
			updated += "\n"
		}
	}
	if err := os.WriteFile(reportPath, []byte(updated), 0o644); err != nil {
		return 0, fmt.Errorf("write source table: %w", err)
	}
	return len(ids), nil
}

func citationIDs(body string) []string {
	matches := sourceCitationPattern.FindAllStringSubmatch(body, -1)
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			ids = append(ids, match[1])
		}
	}
	return uniqueSorted(ids)
}

func rewriteQuoteCitations(body string, ids []string, quotes map[string][]string) string {
	updated := body
	for _, id := range ids {
		pattern := regexp.MustCompile(`\[` + regexp.QuoteMeta(id) + `\](?:\([^\)\r\n]*\))?`)
		urls := quotes[id]
		replacement := ""
		if len(urls) > 0 {
			replacement = "[" + id + "](" + urls[0] + ")"
		}
		updated = pattern.ReplaceAllStringFunc(updated, func(string) string {
			return replacement
		})
	}
	return updated
}

func canonicalQuoteURLs(quote map[string]any) []string {
	values := make([]string, 0)
	var appendValue func(any)
	appendValue = func(value any) {
		switch value := value.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				values = append(values, strings.TrimSpace(value))
			}
		case []any:
			for _, item := range value {
				appendValue(item)
			}
		}
	}
	for _, key := range []string{"sources", "urls", "url", "source_url"} {
		appendValue(quote[key])
	}
	canonical := make([]string, 0, len(values))
	for _, candidate := range values {
		if isCanonicalURL(candidate) {
			canonical = append(canonical, candidate)
		}
	}
	return uniqueSorted(canonical)
}

func isCanonicalURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "http" || scheme == "https") && parsed.Host != ""
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}
