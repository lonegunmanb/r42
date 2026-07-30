package artifact

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonDirectoryAncestorContinuesPastENOTDIR(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	blocker := filepath.Join(workspace, "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o600))
	blockerInfo, err := os.Stat(blocker)
	require.NoError(t, err)

	calls := 0
	stat := func(path string) (fs.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, &os.PathError{Op: "stat", Path: path, Err: syscall.ENOTDIR}
		}
		return blockerInfo, nil
	}
	path := filepath.Join(blocker, "missing", "report.txt")
	actual, blocked, err := nonDirectoryAncestorWith(path, stat)
	require.NoError(t, err)
	assert.True(t, blocked)
	assert.Equal(t, blocker, actual)
}
