package spec_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSourceBelowRunRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "blocks", "research")
	require.NoError(t, os.MkdirAll(source, 0o700))
	outside := filepath.Dir(root)
	tests := []struct {
		name   string
		source string
		want   string
		err    string
	}{
		{name: "relative", source: filepath.Join("blocks", "research"), want: source},
		{name: "absolute", source: source, want: source},
		{name: "root equality", source: ".", want: root},
		{name: "dot dot component", source: `blocks\..\outside`, err: "path traversal"},
		{name: "outside", source: outside, err: "outside run root"},
		{name: "missing", source: filepath.Join(root, "missing"), err: "does not exist"},
		{name: "file", source: filepath.Join(root, "file.txt"), err: "not a directory"},
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o600))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := s3spec.ResolveSource(tt.source, root)
			if tt.err != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, filepath.Clean(tt.want), got)
		})
	}
}

func TestResolveSourceRejectsRootSymlink(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "run-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	_, err := s3spec.ResolveSource(".", link)
	require.Error(t, err)
	assert.ErrorContains(t, err, "symlink")
}

func TestResolveSourceRejectsWindowsStyleTraversalComponent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "safe"), 0o700))
	_, err := s3spec.ResolveSource(`safe\..\outside`, root)
	require.Error(t, err)
	assert.ErrorContains(t, err, "path traversal")
}

func TestResolveSourceRejectsPathOnAnotherWindowsVolume(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("volume-relative paths are Windows-specific")
	}
	root := t.TempDir()
	volume := filepath.VolumeName(root)
	other := `D:`
	if strings.EqualFold(volume, other) {
		other = `C:`
	}
	if _, err := os.Stat(other + `\`); err != nil {
		t.Skipf("alternate volume unavailable: %v", err)
	}
	_, err := s3spec.ResolveSource(filepath.Join(other+`\`, "outside"), root)
	require.Error(t, err)
	assert.ErrorContains(t, err, "check source containment")
}
