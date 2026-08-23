package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestRegistryRegisterPath(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(file, []byte("evidence content"), 0o644))

	registry := NewRegistry(workspace)
	registration, err := registry.RegisterPath(file)
	require.NoError(t, err)
	assert.NotEmpty(t, registration.ID)
	assert.Equal(t, file, registration.Path)
	assert.Equal(t, 1, registry.PendingCount())
}

func TestRegistryPreservesSnapshotDescription(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(file, []byte("evidence content"), 0o644))

	registry := NewRegistry(workspace)
	registration, err := registry.RegisterPathWithMetadata(file, "record:42", "Quarterly revenue guidance")
	require.NoError(t, err)
	assert.Equal(t, "Quarterly revenue guidance", registration.Description)
	require.Len(t, registry.Snapshots(), 1)
	assert.Equal(t, "Quarterly revenue guidance", registry.Snapshots()[0].Description)
}

func TestRegistryRegisterPathErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(workspace string) string
		expectedError string
	}{
		{
			name: "missing file",
			setup: func(workspace string) string {
				return filepath.Join(workspace, "missing.md")
			},
			expectedError: "does not exist",
		},
		{
			name: "empty file",
			setup: func(workspace string) string {
				file := filepath.Join(workspace, "empty.md")
				require.NoError(t, os.WriteFile(file, nil, 0o644))
				return file
			},
			expectedError: "empty",
		},
		{
			name: "directory path",
			setup: func(workspace string) string {
				directory := filepath.Join(workspace, "dir")
				require.NoError(t, os.Mkdir(directory, 0o755))
				return directory
			},
			expectedError: "not a file",
		},
		{
			name: "outside workspace",
			setup: func(workspace string) string {
				other := t.TempDir()
				file := filepath.Join(other, "outside.md")
				require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
				return file
			},
			expectedError: "outside",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			registry := NewRegistry(workspace)
			_, err := registry.RegisterPath(tt.setup(workspace))
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestRegistryRetainsToolResult(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	require.NoError(t, registry.RetainToolResult("call-1", "textual evidence"))
	require.NoError(t, registry.RetainToolResult("call-2", `{"answer": 42}`))
	assert.Equal(t, 0, registry.PendingCount())
}

func TestRegistryRetainToolResultErrors(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	require.ErrorContains(t, registry.RetainToolResult("", "content"), "tool call id")
	require.ErrorContains(t, registry.RetainToolResult("call-1", ""), "result")
	require.ErrorContains(t, registry.RetainToolResult("call-1", "   "), "result")
}

func TestRegistryRegisterToolResult(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, registry.RetainToolResult("call-1", "textual evidence"))

	registration, err := registry.RegisterToolResult("call-1")
	require.NoError(t, err)
	assert.NotEmpty(t, registration.ID)
	assert.True(t, filepath.IsAbs(registration.Path))
	content, err := os.ReadFile(registration.Path)
	require.NoError(t, err)
	assert.Equal(t, "textual evidence", string(content))
	assert.Equal(t, 1, registry.PendingCount())
}

func TestRegistryRegisterToolResultErrors(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	_, err := registry.RegisterToolResult("unknown-call")
	require.ErrorContains(t, err, "not retained")

	require.NoError(t, registry.RetainToolResult("call-1", "first"))
	first, err := registry.RegisterToolResult("call-1")
	require.NoError(t, err)
	second, err := registry.RegisterToolResult("call-1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, 1, registry.PendingCount())
}

func TestRegistryRegisterToolResultWithDifferentSourcesKeepsBothSnapshots(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, registry.RetainToolResult("call-1", "evidence"))
	first, err := registry.RegisterToolResultWithSource("call-1", "source:first")
	require.NoError(t, err)
	second, err := registry.RegisterToolResultWithSource("call-1", "source:second")
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID)
	assert.NotEqual(t, first.Path, second.Path)
	firstContent, err := os.ReadFile(first.Path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: source:first\n\nevidence", string(firstContent))
	secondContent, err := os.ReadFile(second.Path)
	require.NoError(t, err)
	assert.Equal(t, "- Source: source:second\n\nevidence", string(secondContent))
}

func TestRegistryDeduplicatesContent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	first := filepath.Join(workspace, "first.md")
	second := filepath.Join(workspace, "second.md")
	require.NoError(t, os.WriteFile(first, []byte("same content"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("same content"), 0o644))

	registry := NewRegistry(workspace)
	firstRegistration, err := registry.RegisterPath(first)
	require.NoError(t, err)
	require.True(t, firstRegistration.New)
	assert.Equal(t, 1, registry.PendingCount())

	secondRegistration, err := registry.RegisterPath(second)
	require.NoError(t, err)
	assert.False(t, secondRegistration.New)
	assert.Equal(t, firstRegistration.ID, secondRegistration.ID)
	assert.Equal(t, 1, registry.PendingCount())

	require.NoError(t, registry.RetainToolResult("call-1", "same content"))
	toolRegistration, err := registry.RegisterToolResult("call-1")
	require.NoError(t, err)
	assert.Equal(t, firstRegistration.ID, toolRegistration.ID)
	assert.Equal(t, 1, registry.PendingCount())
}

func TestRegistryPathOwnershipAndExclusivity(t *testing.T) {
	t.Parallel()

	t.Run("path must be under workspace", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		registry := NewRegistry(workspace)
		other := t.TempDir()
		file := filepath.Join(other, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		_, err := registry.RegisterPath(file)
		require.ErrorContains(t, err, "outside")
	})

	t.Run("source is exclusive", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		registry := NewRegistry(workspace)
		_, err := registry.Register(file, "")
		require.NoError(t, err)
		_, err = registry.Register("", "")
		require.ErrorContains(t, err, "exactly one source")
		_, err = registry.Register(file, "call-1")
		require.ErrorContains(t, err, "exactly one source")
	})
}

func TestRegistryStateIsolatedPerRegistry(t *testing.T) {
	t.Parallel()

	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	file := filepath.Join(firstWorkspace, "evidence.md")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))

	first := NewRegistry(firstWorkspace)
	second := NewRegistry(secondWorkspace)
	registration, err := first.RegisterPath(file)
	require.NoError(t, err)
	assert.Equal(t, 1, first.PendingCount())
	assert.Equal(t, 0, second.PendingCount())

	secondFile := filepath.Join(secondWorkspace, "evidence.md")
	require.NoError(t, os.WriteFile(secondFile, []byte("content"), 0o644))
	secondRegistration, err := second.RegisterPath(secondFile)
	require.NoError(t, err)
	assert.NotEqual(t, registration.ID, secondRegistration.ID)
	assert.Equal(t, 1, second.PendingCount())
}

func TestRegistryListAndSnapshotIDs(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	first := filepath.Join(workspace, "first.md")
	second := filepath.Join(workspace, "second.md")
	require.NoError(t, os.WriteFile(first, []byte("first content"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("second content"), 0o644))

	registry := NewRegistry(workspace)
	firstRegistration, err := registry.RegisterPath(first)
	require.NoError(t, err)
	secondRegistration, err := registry.RegisterPath(second)
	require.NoError(t, err)

	snapshots := registry.Snapshots()
	require.Len(t, snapshots, 2)
	ids := make(map[string]string, len(snapshots))
	for _, snap := range snapshots {
		ids[snap.ID] = snap.Path
	}
	assert.Equal(t, firstRegistration.Path, ids[firstRegistration.ID])
	assert.Equal(t, secondRegistration.Path, ids[secondRegistration.ID])
}

func TestRegistryUnknownSnapshot(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	_, err := registry.Snapshot("unknown-id")
	require.ErrorContains(t, err, "unknown snapshot")
}

func TestRegistryIDStability(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	file := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(file, []byte("stable content"), 0o644))

	registry := NewRegistry(workspace)
	first, err := registry.RegisterPath(file)
	require.NoError(t, err)
	second, err := registry.RegisterPath(file)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, 1, registry.PendingCount())
}

func TestRegistryPendingAndReviewedSnapshotIDs(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	for index := range 3 {
		file := filepath.Join(workspace, "evidence-"+string(rune('a'+index))+".md")
		require.NoError(t, os.WriteFile(file, []byte("content "+string(rune('a'+index))), 0o644))
		_, err := registry.RegisterPath(file)
		require.NoError(t, err)
	}
	require.Equal(t, 3, registry.PendingCount())
	require.Empty(t, registry.ReviewedSnapshotIDs())

	pending := registry.PendingSnapshotIDs()
	require.Len(t, pending, 3)

	registry.MarkReviewed(pending[0])
	assert.Equal(t, 2, registry.PendingCount())
	assert.Equal(t, []string{pending[0]}, registry.ReviewedSnapshotIDs())
	assert.NotContains(t, registry.PendingSnapshotIDs(), pending[0])
}

func TestRegistryManagedFileLayout(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, registry.RetainToolResult("call-1", "evidence"))
	registration, err := registry.RegisterToolResult("call-1")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(registration.Path, filepath.Join(workspace, ".r42-snapshots")))
}

func TestRegistryRegisterToolResultRejectsMissingToolCall(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	_, err := registry.RegisterToolResult("")
	require.ErrorContains(t, err, "tool call id")
}

func TestRegistryRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("outside content"), 0o644))

	link := filepath.Join(workspace, "link.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	registry := NewRegistry(workspace)
	_, err := registry.RegisterPath(link)
	require.Error(t, err)
	assert.ErrorContains(t, err, "outside the block workspace")
}

func TestRegistryConcurrentToolResultDedup(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, registry.RetainToolResult("call-1", "identical content"))
	require.NoError(t, registry.RetainToolResult("call-2", "identical content"))

	var wg sync.WaitGroup
	results := make([]Registration, 2)
	errors := make([]error, 2)
	for index := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errors[index] = registry.RegisterToolResult(fmt.Sprintf("call-%d", index+1))
		}(index)
	}
	wg.Wait()

	require.NoError(t, errors[0])
	require.NoError(t, errors[1])
	assert.Equal(t, results[0].ID, results[1].ID)
	assert.Equal(t, 1, registry.PendingCount())

	managedFiles, err := os.ReadDir(filepath.Join(workspace, ".r42-snapshots"))
	require.NoError(t, err)
	assert.Len(t, managedFiles, 1)
}

func TestRegistryConcurrentToolResultWithDifferentSourcesKeepsBothSnapshots(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, registry.RetainToolResult("call-1", "evidence"))

	sources := []string{"source:first", "source:second"}
	results := make([]Registration, len(sources))
	errors := make([]error, len(sources))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range sources {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errors[index] = registry.RegisterToolResultWithSource("call-1", sources[index])
		}(index)
	}
	close(start)
	wg.Wait()

	require.NoError(t, errors[0])
	require.NoError(t, errors[1])
	assert.NotEqual(t, results[0].ID, results[1].ID)
	assert.NotEqual(t, results[0].Path, results[1].Path)
	for index, result := range results {
		content, err := os.ReadFile(result.Path)
		require.NoError(t, err)
		assert.Equal(t, "- Source: "+sources[index]+"\n\nevidence", string(content))
	}
}
