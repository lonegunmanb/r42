package evidence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkdownWriterPatchReplacesUniqueText(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(path, []byte("# Report\n\nold claim\n\n## Sources\n\n| Quote ID | URL |\n"), 0o600))

	writer, err := NewMarkdownWriter(workspace)
	require.NoError(t, err)
	patched, err := writer.Patch(path, "old claim", "new claim")
	require.NoError(t, err)
	assert.Equal(t, path, patched)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# Report\n\nnew claim\n\n## Sources\n\n| Quote ID | URL |\n", string(content))
}

func TestMarkdownWriterPatchRejectsSourcesSection(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(path, []byte("# Report\n\n## Sources\n\n| q | https://example.test |\n"), 0o600))

	writer, err := NewMarkdownWriter(workspace)
	require.NoError(t, err)
	_, err = writer.Patch(path, "https://example.test", "https://evil.test")
	require.ErrorContains(t, err, "Sources section")
}

func TestMarkdownWriterPatchRejectsNonMarkdown(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "artifact.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"value":"original"}`), 0o600))

	writer, err := NewMarkdownWriter(workspace)
	require.NoError(t, err)
	_, err = writer.Patch(path, "original", "changed")
	require.ErrorContains(t, err, "only accepts .md")
}

func TestMarkdownWriterPatchRejectsSourcesHeadingInReplacement(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(path, []byte("# Report\n\nold claim\n"), 0o600))

	writer, err := NewMarkdownWriter(workspace)
	require.NoError(t, err)
	_, err = writer.Patch(path, "old claim", "new claim\n\n## Sources\n\n| q | https://evil.test |\n")
	require.ErrorContains(t, err, "Sources section")
}

func TestArtifactWriterPatchRequiresUniqueMatchesAndIsAtomic(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "artifact.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"a":"one","b":"one"}`), 0o600))

	writer, err := NewArtifactWriter(workspace)
	require.NoError(t, err)
	_, err = writer.Patch(path, []TextPatch{{Expected: `"one"`, Replacement: `"two"`}})
	require.ErrorContains(t, err, "exactly once")
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.JSONEq(t, `{"a":"one","b":"one"}`, string(content))
}

func TestArtifactWriterPatchAppliesNonOverlappingBatch(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "artifact.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"a":"one","b":"two"}`), 0o600))

	writer, err := NewArtifactWriter(workspace)
	require.NoError(t, err)
	patched, err := writer.Patch(path, []TextPatch{
		{Expected: `"one"`, Replacement: `"uno"`},
		{Expected: `"two"`, Replacement: `"dos"`},
	})
	require.NoError(t, err)
	assert.Equal(t, path, patched)
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.JSONEq(t, `{"a":"uno","b":"dos"}`, string(content))
}

func TestArtifactWriterPatchRejectsOverlappingBatchAtomically(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "artifact.txt")
	require.NoError(t, os.WriteFile(path, []byte("abcdef"), 0o600))

	writer, err := NewArtifactWriter(workspace)
	require.NoError(t, err)
	_, err = writer.Patch(path, []TextPatch{
		{Expected: "abc", Replacement: "x"},
		{Expected: "cde", Replacement: "y"},
	})
	require.ErrorContains(t, err, "overlap")
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "abcdef", string(content))
}
