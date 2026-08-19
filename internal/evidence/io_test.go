package evidence

import (
	"os"
	"path/filepath"
	"testing"

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

func TestSnapshotAccessNoArbitraryPathReads(t *testing.T) {
	t.Parallel()

	access, err := NewSnapshotAccess(t.TempDir())
	require.NoError(t, err)
	_, err = access.ReadSnapshot("not-an-id", 100)
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
