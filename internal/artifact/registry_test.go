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
