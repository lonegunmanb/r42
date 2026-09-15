package run_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runpkg "github.com/lonegunmanb/r42/internal/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerCreatesUniqueRetainedRuns(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	manager := runpkg.NewManager(project)

	first, err := manager.Create()
	require.NoError(t, err)
	second, err := manager.Create()
	require.NoError(t, err)

	assert.NotEqual(t, first.ID(), second.ID())
	assert.NotEqual(t, first.Directory(), second.Directory())
	for _, created := range []*runpkg.Run{first, second} {
		info, statErr := os.Stat(created.Directory())
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
		assert.Equal(t, filepath.Join(project, ".r42", "runs", created.ID()), created.Directory())
		if runtime.GOOS != "windows" {
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		}
	}
}

func TestManagerReservesRunAndBlockWorkspaceWithoutCreatingDirectories(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	reserved, err := runpkg.NewManager(project).Reserve()
	require.NoError(t, err)

	assert.True(t, filepath.IsAbs(reserved.Directory()))
	assert.NoDirExists(t, filepath.Join(project, ".r42"))
	workspace, err := reserved.WorkspacePath("research.market")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(filepath.FromSlash(workspace)))
	assert.NotContains(t, workspace, `\`)
	assert.NoDirExists(t, filepath.Join(project, ".r42"))
}

func TestManagerFindsMostRecentRunWithCommittedCheckpoint(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	manager := runpkg.NewManager(project)
	first, err := manager.Create()
	require.NoError(t, err)
	second, err := manager.Create()
	require.NoError(t, err)
	for _, created := range []*runpkg.Run{first, second} {
		checkpoint := filepath.Join(created.Directory(), "unit-checkpoints", "unit", "checkpoints", "latest")
		require.NoError(t, os.MkdirAll(filepath.Dir(checkpoint), 0o700))
		require.NoError(t, os.WriteFile(checkpoint, []byte("checkpoint-test\n"), 0o600))
	}
	newer := time.Now().Add(time.Second)
	require.NoError(t, os.Chtimes(filepath.Join(second.Directory(), "unit-checkpoints", "unit", "checkpoints", "latest"), newer, newer))

	resumed, err := manager.LatestCheckpointRun()

	require.NoError(t, err)
	assert.Equal(t, second.Directory(), resumed.Directory())
}

func TestManagerFindsRunWithDynamicMaterializationCheckpoint(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	created, err := runpkg.NewManager(project).Create()
	require.NoError(t, err)
	checkpoint := filepath.Join(created.Directory(), "dynamic-checkpoints", "tasks", "checkpoints", "latest")
	require.NoError(t, os.MkdirAll(filepath.Dir(checkpoint), 0o700))
	require.NoError(t, os.WriteFile(checkpoint, []byte("checkpoint-test\n"), 0o600))

	resumed, err := runpkg.NewManager(project).LatestCheckpointRun()

	require.NoError(t, err)
	assert.Equal(t, created.Directory(), resumed.Directory())
}

func TestRunCreatesCollisionFreeBlockWorkspaces(t *testing.T) {
	t.Parallel()
	created, err := runpkg.NewManager(t.TempDir()).Create()
	require.NoError(t, err)

	addresses := []string{
		"research.market/a",
		"research.market?a",
		"research.foG",
		"research.foa",
		"module.sector.research.market",
		"../escape",
		"research." + strings.Repeat("deep-segment-", 100),
	}
	paths := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		workspace, workspaceErr := created.Workspace(address)
		require.NoError(t, workspaceErr)
		assert.NotContains(t, filepath.Base(workspace), "..")
		assert.Len(t, filepath.Base(workspace), 64)
		assert.Equal(t, filepath.Join(created.Directory(), "blocks"), filepath.Dir(workspace))
		_, duplicate := paths[workspace]
		assert.False(t, duplicate)
		paths[workspace] = struct{}{}
	}
	firstInfo, err := os.Stat(mustWorkspace(t, created, "research.foG"))
	require.NoError(t, err)
	secondInfo, err := os.Stat(mustWorkspace(t, created, "research.foa"))
	require.NoError(t, err)
	assert.False(t, os.SameFile(firstInfo, secondInfo))

	first, err := created.Workspace(addresses[0])
	require.NoError(t, err)
	again, err := created.Workspace(addresses[0])
	require.NoError(t, err)
	assert.Equal(t, first, again)

	_, err = created.Workspace(" \t")
	assert.EqualError(t, err, "block address is required")
}

func mustWorkspace(t *testing.T, created *runpkg.Run, address string) string {
	t.Helper()
	workspace, err := created.Workspace(address)
	require.NoError(t, err)
	return workspace
}

func TestManagerReportsWorkspaceFilesystemErrors(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, ".r42"), []byte("occupied"), 0o600))
	_, err := runpkg.NewManager(project).Create()
	require.Error(t, err)
	require.ErrorContains(t, err, "creating runs directory")

	created, err := runpkg.NewManager(t.TempDir()).Create()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(created.Directory(), "blocks"), []byte("occupied"), 0o600))
	_, err = created.Workspace("research.market")
	require.Error(t, err)
	require.ErrorContains(t, err, "creating blocks directory")
}

func TestRunRejectsWorkspaceIdentityMismatch(t *testing.T) {
	t.Parallel()
	created, err := runpkg.NewManager(t.TempDir()).Create()
	require.NoError(t, err)
	workspace := mustWorkspace(t, created, "research.market")
	identity := filepath.Join(created.Directory(), "block-addresses", filepath.Base(workspace))
	require.NoError(t, os.WriteFile(identity, []byte("research.other"), 0o600))

	_, err = created.Workspace("research.market")
	require.Error(t, err)
	require.ErrorContains(t, err, "block workspace identity collision")
}
