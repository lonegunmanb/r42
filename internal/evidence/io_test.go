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

		access := NewArtifactAccess(workspace)
		content, err := access.ReadArtifact("report", "report.md", 100)
		require.NoError(t, err)
		assert.Equal(t, "candidate", content)
	})

	t.Run("rejects traversal path", func(t *testing.T) {
		t.Parallel()

		access := NewArtifactAccess(t.TempDir())
		_, err := access.ReadArtifact("report", "../../etc/passwd", 100)
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects absolute path outside workspace", func(t *testing.T) {
		t.Parallel()

		access := NewArtifactAccess(t.TempDir())
		other := filepath.Join(t.TempDir(), "secret.md")
		require.NoError(t, os.WriteFile(other, []byte("secret"), 0o644))
		_, err := access.ReadArtifact("report", other, 100)
		require.ErrorContains(t, err, "outside")
	})
}

func TestWriteMarkdownArtifact(t *testing.T) {
	t.Parallel()

	t.Run("writes declared file artifact", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		writer := NewMarkdownWriter(workspace)
		path, err := writer.Write("report.md", "# Report\n")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(workspace, "report.md"), path)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "# Report\n", string(content))
	})

	t.Run("rejects traversal path", func(t *testing.T) {
		t.Parallel()

		writer := NewMarkdownWriter(t.TempDir())
		_, err := writer.Write("../../escape.md", "# Report\n")
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects absolute path outside workspace", func(t *testing.T) {
		t.Parallel()

		writer := NewMarkdownWriter(t.TempDir())
		_, err := writer.Write(filepath.Join(t.TempDir(), "escape.md"), "# Report\n")
		require.ErrorContains(t, err, "outside")
	})

	t.Run("rejects non-markdown extension", func(t *testing.T) {
		t.Parallel()

		writer := NewMarkdownWriter(t.TempDir())
		_, err := writer.Write("report.txt", "# Report\n")
		require.ErrorContains(t, err, "markdown")
	})

	t.Run("rejects empty content", func(t *testing.T) {
		t.Parallel()

		writer := NewMarkdownWriter(t.TempDir())
		_, err := writer.Write("report.md", "")
		require.ErrorContains(t, err, "empty")
	})
}

func TestWriteMarkdownOverwritesExistingArtifact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(file, []byte("old"), 0o644))

	writer := NewMarkdownWriter(workspace)
	path, err := writer.Write("report.md", "# New\n")
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# New\n", string(content))
}
