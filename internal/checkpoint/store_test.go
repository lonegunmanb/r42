package checkpoint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/checkpoint"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreCommitPublishesLatestCheckpoint(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"empty workspace", "populated workspace"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runDirectory := t.TempDir()
			workspace := filepath.Join(runDirectory, "workspace")
			require.NoError(t, os.MkdirAll(workspace, 0o700))
			if name == "populated workspace" {
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.md"), []byte("report"), 0o600))
			}
			store := checkpoint.NewStore(runDirectory)
			for _, id := range []string{"first", "second"} {
				payload := json.RawMessage(`{"checkpoint":"` + id + `"}`)
				require.NoError(t, store.Commit(checkpoint.State{
					CheckpointID: id, Payload: payload, WorkspaceRoots: []string{workspace},
				}))

				loaded, err := checkpoint.NewStore(runDirectory).Load()
				require.NoError(t, err)
				assert.Equal(t, id, loaded.CheckpointID)
				assert.JSONEq(t, string(payload), string(loaded.Payload))
				assert.Len(t, loaded.Workspaces, 1)
			}
			entries, err := os.ReadDir(filepath.Join(runDirectory, "checkpoints"))
			require.NoError(t, err)
			assert.Len(t, entries, 3, "only two committed checkpoints and the latest pointer remain")
		})
	}
}

func TestStoreRestoreReinstatesCommittedArtifactWorkspace(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	workspace := filepath.Join(runDirectory, "blocks", "research")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	committedPath := filepath.Join(workspace, "report.md")
	require.NoError(t, os.WriteFile(committedPath, []byte("committed report"), 0o600))
	registry := artifactpkg.NewRegistry()
	committed, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Description: "Report",
	})
	require.NoError(t, err)
	store := checkpoint.NewStore(runDirectory)
	payload := json.RawMessage(`{"next_phase":"collection_qc"}`)
	require.NoError(t, store.Commit(checkpoint.State{ArtifactRegistry: registry.Snapshot(), Payload: payload}))

	require.NoError(t, os.WriteFile(committedPath, []byte("dirty report"), 0o600))
	require.NoError(t, os.Remove(committedPath))
	dirtyPath := filepath.Join(workspace, "dirty.md")
	require.NoError(t, os.WriteFile(dirtyPath, []byte("uncommitted"), 0o600))
	dirty, err := registry.Declare(workspace, researchspec.Artifact{
		Name: "dirty", Type: researchspec.ArtifactTypeFile, Path: "dirty.md", Description: "Dirty",
	})
	require.NoError(t, err)

	state, err := store.Restore(registry)
	require.NoError(t, err)
	assert.Equal(t, checkpoint.CurrentSchemaVersion, state.SchemaVersion)
	assert.JSONEq(t, string(payload), string(state.Payload))
	content, err := os.ReadFile(committedPath)
	require.NoError(t, err)
	assert.Equal(t, "committed report", string(content))
	_, err = os.Stat(dirtyPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = registry.Record(committed.ID)
	require.NoError(t, err)
	_, err = registry.Record(dirty.ID)
	assert.ErrorContains(t, err, "unknown artifact")
}

func TestStoreRestoreReinstatesCommittedSessionHistory(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	sessionStore := filepath.Join(runDirectory, "copilot")
	sessionID := "session-safe"
	events := filepath.Join(sessionStore, "session-state", sessionID, "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(events), 0o700))
	require.NoError(t, os.WriteFile(events, []byte("safe history\n"), 0o600))
	store := checkpoint.NewStore(runDirectory)
	require.NoError(t, store.Commit(checkpoint.State{
		SessionStoreDirectory: sessionStore, Sessions: []checkpoint.Session{{ID: sessionID}},
	}))
	require.NoError(t, os.WriteFile(events, []byte("safe history\ndirty history\n"), 0o600))

	_, err := store.Restore(artifactpkg.NewRegistry())
	require.NoError(t, err)
	content, err := os.ReadFile(events)
	require.NoError(t, err)
	assert.Equal(t, "safe history\n", string(content))
}

func TestStoreRestoreMergedPreservesOtherWorkflowRegistryEntries(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	workspaceA := filepath.Join(runDirectory, "blocks", "a")
	workspaceB := filepath.Join(runDirectory, "blocks", "b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o700))
	require.NoError(t, os.MkdirAll(workspaceB, 0o700))
	registryA := artifactpkg.NewRegistry()
	artifactA, err := registryA.Declare(workspaceA, researchspec.Artifact{Name: "a", Type: researchspec.ArtifactTypeFile, Path: "a.md", Description: "A"})
	require.NoError(t, err)
	registryB := artifactpkg.NewRegistry()
	artifactB, err := registryB.Declare(workspaceB, researchspec.Artifact{Name: "b", Type: researchspec.ArtifactTypeFile, Path: "b.md", Description: "B"})
	require.NoError(t, err)

	storeA := checkpoint.NewStore(filepath.Join(runDirectory, "checkpoint-a"))
	storeB := checkpoint.NewStore(filepath.Join(runDirectory, "checkpoint-b"))
	require.NoError(t, storeA.Commit(checkpoint.State{ArtifactRegistry: registryA.Snapshot()}))
	require.NoError(t, storeB.Commit(checkpoint.State{ArtifactRegistry: registryB.Snapshot()}))
	resumed := artifactpkg.NewRegistry()
	_, err = storeA.RestoreMerged(resumed)
	require.NoError(t, err)
	_, err = storeB.RestoreMerged(resumed)
	require.NoError(t, err)
	_, err = resumed.Record(artifactA.ID)
	require.NoError(t, err)
	_, err = resumed.Record(artifactB.ID)
	require.NoError(t, err)
}

func TestStoreLoadRejectsCorruptedSessionArchive(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	sessionStore := filepath.Join(runDirectory, "copilot")
	sessionID := "session-safe"
	events := filepath.Join(sessionStore, "session-state", sessionID, "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(events), 0o700))
	require.NoError(t, os.WriteFile(events, []byte("safe history\n"), 0o600))
	store := checkpoint.NewStore(runDirectory)
	require.NoError(t, store.Commit(checkpoint.State{
		SessionStoreDirectory: sessionStore, Sessions: []checkpoint.Session{{ID: sessionID}},
	}))

	entries, err := os.ReadDir(filepath.Join(runDirectory, "checkpoints"))
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() && entry.Name()[0] != '.' {
			require.NoError(t, os.WriteFile(filepath.Join(runDirectory, "checkpoints", entry.Name(), "sessions", "000", "events.jsonl"), []byte("corrupt"), 0o600))
		}
	}

	_, err = store.Load()
	require.ErrorContains(t, err, "checkpoint session file integrity check failed")
}
