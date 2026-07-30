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

func TestValidateResolvesAllowedArtifactPaths(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "absolute.txt")
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{name: "relative", path: "report.txt", expected: filepath.Join(workspace, "report.txt")},
		{name: "absolute", path: absolute, expected: absolute},
		{name: "escaping", path: filepath.Join("..", "shared", "data.txt"), expected: filepath.Join(workspace, "..", "shared", "data.txt")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := artifactpkg.Validate(workspace, []researchspec.Artifact{{
				Name: tt.name, Type: researchspec.ArtifactTypeFile, Path: tt.path,
			}})
			require.NoError(t, err)
			assert.Empty(t, result.Issues)
			expected, err := filepath.Abs(tt.expected)
			require.NoError(t, err)
			assert.Equal(t, filepath.Clean(expected), result.Paths[tt.name])
		})
	}
}

func TestValidateReportsRepairableArtifactIssues(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "empty.txt"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "full.txt"), []byte("report"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "empty-dir"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "empty-tree", "nested"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "nested-dir", "empty"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "nested-dir", "report.txt"), []byte("report"), 0o600))

	artifacts := []researchspec.Artifact{
		{Name: "missing", Type: researchspec.ArtifactTypeFile, Path: "missing.txt", Required: true},
		{Name: "optional", Type: researchspec.ArtifactTypeFile, Path: "optional.txt"},
		{Name: "empty_file", Type: researchspec.ArtifactTypeFile, Path: "empty.txt", Required: true, NonEmpty: true},
		{Name: "empty_file_allowed", Type: researchspec.ArtifactTypeFile, Path: "empty.txt", Required: true},
		{Name: "wrong_file_type", Type: researchspec.ArtifactTypeFile, Path: "empty-dir", Required: true},
		{Name: "empty_directory", Type: researchspec.ArtifactTypeDirectory, Path: "empty-dir", Required: true, NonEmpty: true},
		{Name: "empty_tree", Type: researchspec.ArtifactTypeDirectory, Path: "empty-tree", Required: true, NonEmpty: true},
		{Name: "wrong_directory_type", Type: researchspec.ArtifactTypeDirectory, Path: "full.txt", Required: true},
		{Name: "full_file", Type: researchspec.ArtifactTypeFile, Path: "full.txt", Required: true, NonEmpty: true},
		{Name: "nested_directory", Type: researchspec.ArtifactTypeDirectory, Path: "nested-dir", Required: true, NonEmpty: true},
	}
	result, err := artifactpkg.Validate(workspace, artifacts)
	require.NoError(t, err)
	require.Len(t, result.Paths, len(artifacts))

	codes := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		require.NoError(t, issue.Validate())
		require.NotNil(t, issue.Path)
		codes = append(codes, issue.Code)
	}
	assert.ElementsMatch(t, []string{
		"artifact_missing",
		"artifact_empty",
		"artifact_type_mismatch",
		"artifact_empty",
		"artifact_empty",
		"artifact_type_mismatch",
	}, codes)
	assert.Contains(t, result.Issues[0].Message, "missing")
	assert.NotNil(t, result.Issues[0].RepairHint)
}

func TestValidateRejectsInvalidDeclarationsAndFilesystemErrors(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	_, err := artifactpkg.Validate("", nil)
	require.EqualError(t, err, "artifact workspace is required")

	tests := []struct {
		name          string
		artifacts     []researchspec.Artifact
		expectedError string
	}{
		{
			name:          "invalid declaration",
			artifacts:     []researchspec.Artifact{{Name: "report", Type: "other", Path: "report.txt"}},
			expectedError: "type must be file or directory",
		},
		{
			name: "duplicate name",
			artifacts: []researchspec.Artifact{
				{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "one.txt"},
				{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "two.txt"},
			},
			expectedError: "duplicate artifact name",
		},
		{
			name:          "filesystem error",
			artifacts:     []researchspec.Artifact{{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "bad\x00path", Required: true}},
			expectedError: "artifact report",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := artifactpkg.Validate(workspace, tt.artifacts)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestValidateReportsBlockedRequiredArtifactPathAsRepairable(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "blocked"), []byte("file"), 0o600))

	result, err := artifactpkg.Validate(workspace, []researchspec.Artifact{{
		Name:     "report",
		Type:     researchspec.ArtifactTypeFile,
		Path:     filepath.Join("blocked", "missing", "report.txt"),
		Required: true,
	}})
	require.NoError(t, err)
	require.Len(t, result.Issues, 1)
	assert.Equal(t, "artifact_path_blocked", result.Issues[0].Code)
	require.NotNil(t, result.Issues[0].RepairHint)
	assert.Contains(t, *result.Issues[0].RepairHint, "parent")
}
