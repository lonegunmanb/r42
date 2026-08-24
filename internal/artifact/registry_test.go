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

func TestRegistryDeclaresAndReadsExistingArtifactByOpaqueID(t *testing.T) {
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
	page, err := registry.ReadPage(record.ID, 10, 5)
	require.NoError(t, err)
	assert.Equal(t, "[1,2]", page.Content)
	assert.Equal(t, 15, page.NextOffsetBytes)
}

func TestRegistryRegisterEvidenceRecordsSourceAndPurpose(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("evidence"), 0o600))
	registry := artifactpkg.NewRegistry()

	record, created, err := registry.RegisterEvidence(workspace, path, "local-record:42", "Primary source")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Regexp(t, `^artifact-`, record.ID)
	assert.Equal(t, artifactpkg.PurposeEvidence, record.Purpose)
	assert.Equal(t, "local-record:42", record.Source)
	assert.Equal(t, "Primary source", record.Description)
}

func TestRegistryRegisterRetainedEvidenceWritesSourceHeader(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	require.NoError(t, registry.RetainToolResult("call-1", "captured evidence"))

	record, created, err := registry.RegisterRetainedEvidence(workspace, "call-1", "database-row:42", "Captured record")

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "database-row:42", record.Source)
	content, err := os.ReadFile(record.Path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: database-row:42\n\ncaptured evidence", string(content))
}

func TestRegistryRegisterRetainedEvidenceIsIdempotentForToolCall(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	require.NoError(t, registry.RetainToolResult("call-1", "captured evidence"))

	first, firstCreated, err := registry.RegisterRetainedEvidence(workspace, "call-1", "database-row:42", "Captured record")
	require.NoError(t, err)
	second, secondCreated, err := registry.RegisterRetainedEvidence(workspace, "call-1", "database-row:42", "Captured record")

	require.NoError(t, err)
	assert.True(t, firstCreated)
	assert.False(t, secondCreated)
	assert.Equal(t, first, second)
	assert.Len(t, registry.RecordsByPurpose(artifactpkg.PurposeEvidence), 1)
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

	files, err := registry.ListDirectoryFiles(parent.ID)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, []string{"nested/two.json", "one.json"}, []string{files[0].Name, files[1].Name})
	assert.Equal(t, artifactpkg.KindArtifactFile, files[0].Kind)
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
	files, err := registry.ListDirectoryFiles(parent.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)

	require.NoError(t, os.Remove(childPath))
	require.NoError(t, os.Symlink(secretPath, childPath))

	page, err := registry.ReadPage(files[0].ID, 0, 100)
	require.Error(t, err)
	assert.NotContains(t, page.Content, `"secret":true`)
}
