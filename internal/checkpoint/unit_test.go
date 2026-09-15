package checkpoint_test

import (
	"os"
	"path/filepath"
	"testing"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/checkpoint"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitRestoresUncommittedWorkspaceAndRegistry(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	workspace := filepath.Join(runDirectory, "blocks", "unit")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	registry := artifactpkg.NewRegistry()
	committed, err := registry.Declare(workspace, researchspec.Artifact{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Description: "Report"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(committed.Path, []byte("before"), 0o600))

	unit := checkpoint.NewUnit(filepath.Join(runDirectory, "unit"))
	require.NoError(t, unit.Begin(registry, workspace))
	require.NoError(t, os.WriteFile(committed.Path, []byte("after"), 0o600))
	created, err := registry.Declare(workspace, researchspec.Artifact{Name: "new", Type: researchspec.ArtifactTypeFile, Path: "new.md", Description: "New"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(created.Path, []byte("uncommitted"), 0o600))

	state, err := unit.Rollback(registry)

	require.NoError(t, err)
	assert.Equal(t, checkpoint.UnitStarted, state.Status)
	content, err := os.ReadFile(committed.Path)
	require.NoError(t, err)
	assert.Equal(t, "before", string(content))
	_, err = os.Stat(created.Path)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = registry.Record(created.ID)
	require.Error(t, err)
	assert.Equal(t, committed.ID, mustRecord(t, registry, committed.ID).ID)
}

func TestUnitRollbackPreservesOtherUnitArtifacts(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	workspaceA := filepath.Join(runDirectory, "blocks", "a")
	workspaceB := filepath.Join(runDirectory, "blocks", "b")
	require.NoError(t, os.MkdirAll(workspaceB, 0o700))
	registry := artifactpkg.NewRegistry()
	unitA := checkpoint.NewUnit(filepath.Join(runDirectory, "unit-a"))
	require.NoError(t, unitA.Begin(registry, workspaceA))
	artifactA, err := registry.Declare(workspaceA, researchspec.Artifact{Name: "a", Type: researchspec.ArtifactTypeFile, Path: "a.md", Description: "A"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(artifactA.Path, []byte("abandoned"), 0o600))
	artifactB, err := registry.Declare(workspaceB, researchspec.Artifact{Name: "b", Type: researchspec.ArtifactTypeFile, Path: "b.md", Description: "B"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(artifactB.Path, []byte("completed elsewhere"), 0o600))

	_, err = unitA.Rollback(registry)

	require.NoError(t, err)
	_, err = registry.Record(artifactA.ID)
	require.Error(t, err)
	assert.Equal(t, artifactB.ID, mustRecord(t, registry, artifactB.ID).ID)
	content, err := os.ReadFile(artifactB.Path)
	require.NoError(t, err)
	assert.Equal(t, "completed elsewhere", string(content))
}

func TestUnitMarksCompleteAndRestoresCommittedState(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	workspace := filepath.Join(runDirectory, "blocks", "unit")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	registry := artifactpkg.NewRegistry()
	record, err := registry.Declare(workspace, researchspec.Artifact{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Description: "Report"})
	require.NoError(t, err)
	unit := checkpoint.NewUnit(filepath.Join(runDirectory, "unit"))
	require.NoError(t, unit.Begin(registry, workspace))
	require.NoError(t, os.WriteFile(record.Path, []byte("complete"), 0o600))
	require.NoError(t, unit.Complete(registry, workspace, []byte(`{"value":"done"}`)))

	loaded, err := unit.Load()
	require.NoError(t, err)
	assert.Equal(t, checkpoint.UnitCompleted, loaded.Status)
	assert.JSONEq(t, `{"value":"done"}`, string(loaded.Result))
	require.NoError(t, os.WriteFile(record.Path, []byte("dirty"), 0o600))
	_, err = unit.RestoreCompleted(artifactpkg.NewRegistry())
	require.NoError(t, err)
	content, err := os.ReadFile(record.Path)
	require.NoError(t, err)
	assert.Equal(t, "complete", string(content))
}

func TestUnitLoadRemovesInterruptedStagingSnapshot(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	unit := checkpoint.NewUnit(directory)
	registry := artifactpkg.NewRegistry()
	workspace := filepath.Join(directory, "workspace")
	require.NoError(t, unit.Begin(registry, workspace))
	staging := filepath.Join(directory, "checkpoints", ".staging-interrupted")
	require.NoError(t, os.MkdirAll(staging, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "partial"), []byte("partial"), 0o600))

	_, err := unit.Load()

	require.NoError(t, err)
	assert.NoDirExists(t, staging)
}

func mustRecord(t *testing.T, registry *artifactpkg.Registry, id string) artifactpkg.Record {
	t.Helper()
	record, err := registry.Record(id)
	require.NoError(t, err)
	return record
}
