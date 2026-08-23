package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryDeclaresAndReadsArtifactByOpaqueID(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "claims.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"claims":[1,2]}`), 0o600))
	registry := artifactpkg.NewRegistry()

	record, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "claims", Type: researchspec.ArtifactTypeFile, Path: "claims.json",
		Description: "Validated claims",
	})
	require.NoError(t, err)
	assert.Regexp(t, `^artifact-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, record.ID)
	assert.Equal(t, path, record.Path)
	assert.Equal(t, "Validated claims", record.Description)
	assert.Empty(t, registry.ReadyRecords())

	require.NoError(t, registry.MarkReady(record.ID))
	require.Len(t, registry.ReadyRecords(), 1)
	page, err := registry.ReadPage(record.ID, 10, 5)
	require.NoError(t, err)
	assert.Equal(t, "[1,2]", page.Content)
	assert.Equal(t, 15, page.NextOffsetBytes)
}

func TestRegistryRegistersSnapshotWithExistingID(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("source"), 0o600))
	registry := artifactpkg.NewRegistry()

	record, err := registry.RegisterSnapshot(
		workspace, "snapshot-0123456789abcdef0123456789abcdef", path, "Regulatory filing",
	)
	require.NoError(t, err)
	assert.Equal(t, "snapshot-0123456789abcdef0123456789abcdef", record.ID)
	assert.Equal(t, artifactpkg.KindSnapshot, record.Kind)
}

func TestRegistryListsDirectoryFilesAsReadableChildArtifacts(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	directory := filepath.Join(workspace, "evidence")
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "nested"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "one.json"), []byte(`{"one":true}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "nested", "two.json"), []byte(`{"two":true}`), 0o600))
	registry := artifactpkg.NewRegistry()
	parent, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence", Description: "Evidence files",
	})
	require.NoError(t, err)
	require.NoError(t, registry.MarkReady(parent.ID))

	files, err := registry.ListDirectoryFiles(parent.ID)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, []string{"nested/two.json", "one.json"}, []string{files[0].Name, files[1].Name})
	assert.Equal(t, artifactpkg.KindArtifactFile, files[0].Kind)
	assert.True(t, files[0].Ready)
	page, err := registry.ReadPage(files[0].ID, 0, 32)
	require.NoError(t, err)
	assert.Equal(t, `{"two":true}`, page.Content)

	again, err := registry.ListDirectoryFiles(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, files, again)
}

func TestRegistryRefusesDirectoryChildReplacedBySymlink(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	directory := filepath.Join(workspace, "evidence")
	childPath := filepath.Join(directory, "source.json")
	secretPath := filepath.Join(workspace, "secret.json")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(childPath, []byte(`{"source":true}`), 0o600))
	require.NoError(t, os.WriteFile(secretPath, []byte(`{"secret":true}`), 0o600))

	registry := artifactpkg.NewRegistry()
	parent, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "evidence", Type: researchspec.ArtifactTypeDirectory, Path: "evidence", Description: "Evidence files",
	})
	require.NoError(t, err)
	require.NoError(t, registry.MarkReady(parent.ID))
	files, err := registry.ListDirectoryFiles(parent.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)

	require.NoError(t, os.Remove(childPath))
	require.NoError(t, os.Symlink(secretPath, childPath))

	page, err := registry.ReadPage(files[0].ID, 0, 100)
	require.Error(t, err)
	assert.NotContains(t, page.Content, `"secret":true`)
}
