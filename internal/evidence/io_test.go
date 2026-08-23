package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/r42/internal/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSnapshots(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))

	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(file)
	require.NoError(t, err)

	snapshots, err := access.ListSnapshots()
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, id, snapshots[0].ID)
	assert.Equal(t, file, snapshots[0].Path)
}

func TestReadSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("reads content with bounds", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("line1\nline2\nline3\n"), 0o644))

		access, err := NewSnapshotAccess(workspace)
		require.NoError(t, err)
		id, err := access.Register(file)
		require.NoError(t, err)

		content, err := access.ReadSnapshot(id, 100)
		require.NoError(t, err)
		assert.Equal(t, "line1\nline2\nline3\n", content)
	})

	t.Run("truncates beyond bound", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("0123456789"), 0o644))

		access, err := NewSnapshotAccess(workspace)
		require.NoError(t, err)
		id, err := access.Register(file)
		require.NoError(t, err)

		content, err := access.ReadSnapshot(id, 4)
		require.NoError(t, err)
		assert.Equal(t, "0123", content)
	})

	t.Run("rejects unknown id", func(t *testing.T) {
		t.Parallel()

		access, err := NewSnapshotAccess(t.TempDir())
		require.NoError(t, err)
		_, err = access.ReadSnapshot("unknown", 100)
		require.ErrorContains(t, err, "unknown snapshot")
	})

	t.Run("rejects zero bound", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))

		access, err := NewSnapshotAccess(workspace)
		require.NoError(t, err)
		id, err := access.Register(file)
		require.NoError(t, err)

		_, err = access.ReadSnapshot(id, 0)
		require.ErrorContains(t, err, "positive")
	})
}

func TestReadSnapshotPage(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(path, []byte("0123456789"), 0o644))
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)

	page, err := access.ReadSnapshotPage(id, 3, 4)
	require.NoError(t, err)
	assert.Equal(t, "3456", page.Content)
	assert.Equal(t, 3, page.OffsetBytes)
	assert.Equal(t, 7, page.NextOffsetBytes)
	assert.Equal(t, 10, page.TotalBytes)
	assert.True(t, page.Truncated)

	last, err := access.ReadSnapshotPage(id, page.NextOffsetBytes, 4)
	require.NoError(t, err)
	assert.Equal(t, "789", last.Content)
	assert.Equal(t, 10, last.NextOffsetBytes)
	assert.False(t, last.Truncated)

	_, err = access.ReadSnapshotPage(id, -1, 4)
	require.ErrorContains(t, err, "must not be negative")
	_, err = access.ReadSnapshotPage(id, 11, 4)
	require.ErrorContains(t, err, "exceeds total bytes")
}

func TestReadSnapshotPagePreservesUTF8(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(path, []byte("中A文"), 0o644))
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)

	_, err = access.ReadSnapshotPage(id, 0, 1)
	require.ErrorContains(t, err, "too small")
	_, err = access.ReadSnapshotPage(id, 1, 4)
	require.ErrorContains(t, err, "utf-8 character boundary")
	first, err := access.ReadSnapshotPage(id, 0, 4)
	require.NoError(t, err)
	assert.Equal(t, "中A", first.Content)
	assert.Equal(t, 4, first.NextOffsetBytes)
	second, err := access.ReadSnapshotPage(id, first.NextOffsetBytes, 4)
	require.NoError(t, err)
	assert.Equal(t, "文", second.Content)
}

func TestSearchSnapshot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(path, []byte("Header\nMarketing  authorisation\nOther line\nMARKETING AUTHORISATION\nmarketing authorisation\nTail\n"), 0o644))
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)

	results, err := access.SearchSnapshot(id, "marketing authorisation", false, 2, 1)
	require.NoError(t, err)
	require.Len(t, results.Matches, 2)
	assert.Equal(t, 2, results.Matches[0].Line)
	assert.Equal(t, "Marketing authorisation", results.Matches[0].MatchedText)
	assert.Contains(t, results.Matches[0].Excerpt, "Header")
	assert.Equal(t, 4, results.Matches[1].Line)
	assert.True(t, results.Truncated)

	caseSensitive, err := access.SearchSnapshot(id, "MARKETING AUTHORISATION", true, 10, 0)
	require.NoError(t, err)
	require.Len(t, caseSensitive.Matches, 1)
	assert.Equal(t, 4, caseSensitive.Matches[0].Line)
	assert.False(t, caseSensitive.Truncated)
}

func TestSearchSnapshotRejectsInvalidBoundsAndPatterns(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(path, []byte("source text"), 0o644))
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)

	tests := []struct {
		name         string
		pattern      string
		maxMatches   int
		contextLines int
		expected     string
	}{
		{name: "empty pattern", pattern: " ", maxMatches: 1, expected: "pattern is required"},
		{name: "invalid regex", pattern: "[", maxMatches: 1, expected: "compile snapshot search pattern"},
		{name: "empty match", pattern: ".*", maxMatches: 1, expected: "must not match empty text"},
		{name: "zero matches", pattern: "source", maxMatches: 0, expected: "between 1 and 100"},
		{name: "too many matches", pattern: "source", maxMatches: 101, expected: "between 1 and 100"},
		{name: "negative context", pattern: "source", maxMatches: 1, contextLines: -1, expected: "between 0 and 20"},
		{name: "too much context", pattern: "source", maxMatches: 1, contextLines: 21, expected: "between 0 and 20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, searchErr := access.SearchSnapshot(id, tt.pattern, true, tt.maxMatches, tt.contextLines)
			require.ErrorContains(t, searchErr, tt.expected)
		})
	}
}

func TestSearchSnapshotRejectsZeroWidthMatchOutsideEmptyText(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(path, []byte("a"), 0o644))
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		_, searchErr := access.SearchSnapshot(id, `$\b`, true, 1, 0)
		require.ErrorContains(t, searchErr, "zero-width")
	})
}

func TestValidateSnapshotSearchSize(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateSnapshotSearchSize(maxSnapshotSearchBytes))
	require.ErrorContains(t, validateSnapshotSearchSize(maxSnapshotSearchBytes+1), "exceeds")

	workspace := t.TempDir()
	path := filepath.Join(workspace, "oversized.md")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxSnapshotSearchBytes+1))
	require.NoError(t, file.Close())
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)
	_, err = access.SearchSnapshot(id, "source", true, 1, 0)
	require.ErrorContains(t, err, "exceeds")
}

func TestSearchSnapshotBoundsLongMatchesAndExcerpts(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	content := strings.Repeat("context ", 2000) + strings.Repeat("target ", 20)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	access, err := NewSnapshotAccess(workspace)
	require.NoError(t, err)
	id, err := access.Register(path)
	require.NoError(t, err)

	result, err := access.SearchSnapshot(id, "target", true, 20, 20)
	require.NoError(t, err)
	require.Len(t, result.Matches, 20)
	for _, match := range result.Matches {
		assert.LessOrEqual(t, len([]rune(match.Excerpt)), maxSnapshotSearchExcerptRunes)
		assert.Contains(t, match.Excerpt, match.MatchedText)
	}

	_, err = access.SearchSnapshot(id, `(?:context )+`, true, 1, 0)
	require.ErrorContains(t, err, "match exceeds")
}

func TestSnapshotAccessNoArbitraryPathReads(t *testing.T) {
	t.Parallel()

	access, err := NewSnapshotAccess(t.TempDir())
	require.NoError(t, err)
	_, err = access.ReadSnapshot("not-an-id", 100)
	require.ErrorContains(t, err, "unknown snapshot")
}

func TestSnapshotAccessUsesSharedRegistry(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	require.NoError(t, os.WriteFile(path, []byte("shared evidence"), 0o600))
	registry := snapshot.NewRegistry(workspace)
	registered, err := registry.RegisterPath(path)
	require.NoError(t, err)

	access, err := NewSnapshotAccessWithRegistry(registry)
	require.NoError(t, err)
	content, err := access.ReadSnapshot(registered.ID, 100)

	require.NoError(t, err)
	assert.Equal(t, "shared evidence", content)
}

func TestSnapshotSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		expected  string
		errorText string
	}{
		{name: "reads arbitrary source header", content: "- Source: local-record:42\n\nBody", expected: "local-record:42"},
		{name: "reads legacy URL header", content: "# Source\n\n- URL: https://example.com/source\n\nBody", expected: "https://example.com/source"},
		{name: "skips empty header before valid URL", content: "- Source:   \n- URL: https://example.com/source\n\nBody", expected: "https://example.com/source"},
		{name: "rejects empty source header", content: "- Source:   \n\nBody", errorText: "empty Source header"},
		{name: "rejects missing source header", content: "# Source\n\nBody", errorText: "no Source header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.md")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			access, err := NewSnapshotAccess(workspace)
			require.NoError(t, err)
			id, err := access.Register(path)
			require.NoError(t, err)

			actual, err := access.SnapshotSource(id)

			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestSnapshotAccessReadsOnlyAuthorizedUpstreamSnapshotsByID(t *testing.T) {
	t.Parallel()

	local := snapshot.NewRegistry(t.TempDir())
	upstreamWorkspace := t.TempDir()
	upstreamPath := filepath.Join(upstreamWorkspace, "source.md")
	require.NoError(t, os.WriteFile(upstreamPath, []byte("upstream evidence"), 0o600))
	upstream := snapshot.NewRegistry(upstreamWorkspace)
	registered, err := upstream.RegisterPath(upstreamPath)
	require.NoError(t, err)

	access, err := NewSnapshotAccessWithRegistryAndUpstream(local, map[string]string{
		registered.ID: registered.Path,
	})
	require.NoError(t, err)
	content, err := access.ReadSnapshot(registered.ID, 100)
	require.NoError(t, err)
	assert.Equal(t, "upstream evidence", content)

	unapproved, err := NewSnapshotAccessWithRegistryAndUpstream(local, map[string]string{})
	require.NoError(t, err)
	_, err = unapproved.ReadSnapshot(registered.ID, 100)
	require.ErrorContains(t, err, "unknown snapshot")
}

func TestReadDeclaredArtifact(t *testing.T) {
	t.Parallel()

	t.Run("reads declared artifact", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "report.md")
		require.NoError(t, os.WriteFile(file, []byte("candidate"), 0o644))

		access, err := NewArtifactAccess(workspace)
		require.NoError(t, err)
		content, err := access.ReadArtifact("report", "report.md", 100)
		require.NoError(t, err)
		assert.Equal(t, "candidate", content)
	})

	t.Run("rejects traversal path", func(t *testing.T) {
		t.Parallel()

		access, err := NewArtifactAccess(t.TempDir())
		require.NoError(t, err)
		_, err = access.ReadArtifact("report", "../../etc/passwd", 100)
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects absolute path outside workspace", func(t *testing.T) {
		t.Parallel()

		access, err := NewArtifactAccess(t.TempDir())
		require.NoError(t, err)
		other := filepath.Join(t.TempDir(), "secret.md")
		require.NoError(t, os.WriteFile(other, []byte("secret"), 0o644))
		_, err = access.ReadArtifact("report", other, 100)
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects symlink pointing outside workspace", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		outside := t.TempDir()
		secret := filepath.Join(outside, "secret.md")
		require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o644))
		link := filepath.Join(workspace, "report.md")
		if err := os.Symlink(secret, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		access, err := NewArtifactAccess(workspace)
		require.NoError(t, err)
		_, err = access.ReadArtifact("report", "report.md", 100)
		require.ErrorContains(t, err, "outside")
	})
}

func TestReadDeclaredArtifactPage(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "claims.json")
	require.NoError(t, os.WriteFile(file, []byte("0123456789"), 0o644))

	access, err := NewArtifactAccess(workspace)
	require.NoError(t, err)
	page, err := access.ReadArtifactPage("claims", "claims.json", 4, 3)
	require.NoError(t, err)
	assert.Equal(t, "456", page.Content)
	assert.Equal(t, 4, page.OffsetBytes)
	assert.Equal(t, 7, page.NextOffsetBytes)
	assert.Equal(t, 10, page.TotalBytes)
	assert.True(t, page.Truncated)
}

func TestWriteMarkdownArtifact(t *testing.T) {
	t.Parallel()

	t.Run("writes declared file artifact", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		writer, err := NewMarkdownWriter(workspace)
		require.NoError(t, err)
		path, err := writer.Write("report.md", "# Report\n")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(workspace, "report.md"), path)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "# Report\n", string(content))
	})

	t.Run("writes into nested subdirectory", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		writer, err := NewMarkdownWriter(workspace)
		require.NoError(t, err)
		path, err := writer.Write(filepath.Join("subdir", "report.md"), "# Nested\n")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(workspace, "subdir", "report.md"), path)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "# Nested\n", string(content))
	})

	t.Run("rejects traversal path", func(t *testing.T) {
		t.Parallel()

		writer, err := NewMarkdownWriter(t.TempDir())
		require.NoError(t, err)
		_, err = writer.Write("../../escape.md", "# Report\n")
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects absolute path outside workspace", func(t *testing.T) {
		t.Parallel()

		writer, err := NewMarkdownWriter(t.TempDir())
		require.NoError(t, err)
		_, err = writer.Write(filepath.Join(t.TempDir(), "escape.md"), "# Report\n")
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects non-markdown extension", func(t *testing.T) {
		t.Parallel()

		writer, err := NewMarkdownWriter(t.TempDir())
		require.NoError(t, err)
		_, err = writer.Write("report.txt", "# Report\n")
		require.ErrorContains(t, err, "markdown")
	})

	t.Run("rejects empty content", func(t *testing.T) {
		t.Parallel()

		writer, err := NewMarkdownWriter(t.TempDir())
		require.NoError(t, err)
		_, err = writer.Write("report.md", "")
		require.ErrorContains(t, err, "empty")
	})

	t.Run("rejects symlink target outside workspace", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		outside := t.TempDir()
		target := filepath.Join(outside, "victim.md")
		require.NoError(t, os.WriteFile(target, []byte("original"), 0o644))
		link := filepath.Join(workspace, "report.md")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		writer, err := NewMarkdownWriter(workspace)
		require.NoError(t, err)
		_, err = writer.Write("report.md", "# Pwned\n")
		require.ErrorContains(t, err, "outside")
		content, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "original", string(content))
	})

	t.Run("rejects symlinked parent directory outside workspace", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		outside := t.TempDir()
		outsideDir := filepath.Join(outside, "dir")
		require.NoError(t, os.Mkdir(outsideDir, 0o755))
		link := filepath.Join(workspace, "linkdir")
		if err := os.Symlink(outsideDir, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		writer, err := NewMarkdownWriter(workspace)
		require.NoError(t, err)
		_, err = writer.Write(filepath.Join("linkdir", "newsub", "report.md"), "# Pwned\n")
		require.ErrorContains(t, err, "outside")
		// The external newsub directory must not have been created.
		_, statErr := os.Stat(filepath.Join(outsideDir, "newsub"))
		require.Error(t, statErr)
	})
}

func TestWriteMarkdownOverwritesExistingArtifact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(file, []byte("old"), 0o644))

	writer, err := NewMarkdownWriter(workspace)
	require.NoError(t, err)
	path, err := writer.Write("report.md", "# New\n")
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# New\n", string(content))
}
