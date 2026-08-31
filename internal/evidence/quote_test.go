package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuoteRegistryCapturesAndExpandsSearchMatch(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("first\nsecond target\nthird\n"), 0o600))
	artifacts := artifactpkg.NewRegistry()
	registered, _, err := artifacts.RegisterEvidence(workspace, path, "https://example.test/source", "Source title")
	require.NoError(t, err)
	result, err := SearchArtifact(artifacts, registered.ID, "target", true, 1, 0)
	require.NoError(t, err)
	require.Len(t, result.Matches, 1)
	quotes := NewQuoteRegistry()

	captured, err := quotes.CaptureMatch(artifacts, registered.ID, result.Matches[0])
	require.NoError(t, err)
	again, err := quotes.CaptureMatch(artifacts, registered.ID, result.Matches[0])
	require.NoError(t, err)
	assert.Equal(t, captured.Ref, again.Ref)
	assert.Equal(t, "target", captured.ExactQuote)
	assert.Equal(t, "line 4", captured.Locator)
	assert.NotEmpty(t, captured.ArtifactDigest)

	expanded, err := quotes.Expand(artifacts, captured.Ref, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, "first second target third", expanded.ExactQuote)
	assert.Equal(t, "lines 3-5", expanded.Locator)
	resolved, ok := quotes.Resolve(expanded.Ref)
	require.True(t, ok)
	assert.Equal(t, expanded, resolved)

	clamped, err := quotes.Expand(artifacts, captured.Ref, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, "lines 4-5", clamped.Locator)
}

func TestQuoteRegistryRejectsExpansionAfterArtifactChangesButKeepsCapturedRecord(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("stable quote\n"), 0o600))
	artifacts := artifactpkg.NewRegistry()
	registered, _, err := artifacts.RegisterEvidence(workspace, path, "https://example.test/source", "Source title")
	require.NoError(t, err)
	result, err := SearchArtifact(artifacts, registered.ID, "stable quote", true, 1, 0)
	require.NoError(t, err)
	quotes := NewQuoteRegistry()
	captured, err := quotes.CaptureMatch(artifacts, registered.ID, result.Matches[0])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("changed\n"), 0o600))

	_, err = quotes.Expand(artifacts, captured.Ref, 1, 1)
	require.ErrorContains(t, err, "artifact changed")
	resolved, ok := quotes.Resolve(captured.Ref)
	require.True(t, ok)
	assert.Equal(t, "stable quote", resolved.ExactQuote)
}

func TestQuoteRegistryRejectsInvalidCaptureAndExpansion(t *testing.T) {
	t.Parallel()

	quotes := NewQuoteRegistry()
	artifacts := artifactpkg.NewRegistry()
	_, err := quotes.CaptureMatch(artifacts, "missing", ArtifactSearchMatch{})
	require.ErrorContains(t, err, "artifact search result")
	_, err = quotes.Expand(artifacts, "quote-ref-missing", 0, 0)
	require.ErrorContains(t, err, "unknown quote reference")
	_, err = quotes.Expand(artifacts, "quote-ref-missing", -1, 0)
	assert.ErrorContains(t, err, "between 0 and 20")
}

func TestQuoteRegistryRejectsOversizedExpansion(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	content := "target\n" + string(make([]byte, maxCapturedQuoteRunes+1)) + "\n"
	content = strings.ReplaceAll(content, "\x00", "x")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	artifacts := artifactpkg.NewRegistry()
	registered, _, err := artifacts.RegisterEvidence(workspace, path, "", "Source title")
	require.NoError(t, err)
	result, err := SearchArtifact(artifacts, registered.ID, "target", true, 1, 0)
	require.NoError(t, err)
	quotes := NewQuoteRegistry()
	captured, err := quotes.CaptureMatch(artifacts, registered.ID, result.Matches[0])
	require.NoError(t, err)

	_, err = quotes.Expand(artifacts, captured.Ref, 0, 1)
	assert.ErrorContains(t, err, "exceeds maximum")
}
