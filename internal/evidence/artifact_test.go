package evidence

import (
	"os"
	"path/filepath"
	"testing"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactEvidenceAccessSearchesNormalizedArtifactText(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "evidence.md")
	require.NoError(t, os.WriteFile(
		path,
		[]byte("Header\nMarketing  authorisation\nOther line\n"),
		0o600,
	))
	registry := artifactpkg.NewRegistry()
	registration, _, err := registry.RegisterEvidence(workspace, path, "https://example.com/source", "test evidence")
	require.NoError(t, err)
	access, err := NewArtifactEvidenceAccess(registry, []string{registration.ID})
	require.NoError(t, err)

	result, err := access.Search(registration.ID, "marketing authorisation", false, 1, 1)

	require.NoError(t, err)
	require.Len(t, result.Matches, 1)
	assert.Equal(t, 4, result.Matches[0].Line)
	assert.Equal(t, "Marketing authorisation", result.Matches[0].MatchedText)
	assert.Contains(t, result.Matches[0].Excerpt, "Header")
}
