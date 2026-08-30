package spec_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSourceFilesIncludesHiddenFilesAndSorts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested", ".hidden"), 0o700))
	for _, name := range []string{"z.txt", ".root", filepath.Join("nested", "a.txt"), filepath.Join("nested", ".hidden", "b.txt")} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o600))
	}
	files, err := s3spec.ListSourceFiles(root, nil)
	require.NoError(t, err)
	paths := sourcePaths(files)
	assert.Equal(t, []string{".root", "nested/.hidden/b.txt", "nested/a.txt", "z.txt"}, paths)
	assert.True(t, sort.StringsAreSorted(paths))
}

func TestListSourceFilesSupportsDoubleStarDirectoryExcludes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"keep/a.txt", "drop/b.txt", "drop/deep/c.txt", "keep.tmp"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o600))
	}
	files, err := s3spec.ListSourceFiles(root, []string{"drop/**", "**/*.tmp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"keep/a.txt"}, sourcePaths(files))
}

func TestListSourceFilesSkipsSymlinksAndSpecialFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "regular.txt"), []byte("x"), 0o600))
	if err := os.Symlink(filepath.Join(root, "regular.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	files, err := s3spec.ListSourceFiles(root, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"regular.txt"}, sourcePaths(files))
}

func TestListSourceFilesEmptyResultSucceeds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files, err := s3spec.ListSourceFiles(root, []string{"**"})
	require.NoError(t, err)
	assert.Empty(t, files)
}

func sourcePaths(files []s3spec.SourceFile) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.RelativePath
	}
	return paths
}
