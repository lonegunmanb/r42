package s3

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectContentType(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R'}
	tests := []struct {
		name     string
		filename string
		contents []byte
		want     string
	}{
		{name: "markdown report", filename: "report.md", contents: []byte("# Report\n\nsome **markdown** body\n"), want: "text/markdown"},
		{name: "markdown long extension", filename: "report.markdown", contents: []byte("# Report\n"), want: "text/markdown"},
		{name: "plain text", filename: "notes.txt", contents: []byte("plain words without structure\n"), want: "text/plain"},
		{name: "plain text without extension", filename: "notes", contents: []byte("plain words without structure\n"), want: "text/plain; charset=utf-8"},
		{name: "json object", filename: "snapshot.json", contents: []byte(`{"key": "value"}`), want: "application/json"},
		{name: "json array without extension", filename: "data", contents: []byte(`[1, 2, 3]`), want: "application/json"},
		{name: "json content with misleading text extension", filename: "data.txt", contents: []byte(`{"key": "value"}`), want: "application/json"},
		{name: "non-json text with json extension hint", filename: "report.json", contents: []byte("# Report\n\nnot json at all\n"), want: "application/json"},
		{name: "png image", filename: "image.png", contents: png, want: "image/png"},
		{name: "binary content with misleading markdown extension", filename: "image.md", contents: png, want: "image/png"},
		{name: "unknown binary", filename: "blob", contents: []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}, want: "application/octet-stream"},
		{name: "empty markdown file", filename: "empty.md", contents: nil, want: "text/markdown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			filename := filepath.Join(root, tt.filename)
			require.NoError(t, os.WriteFile(filename, tt.contents, 0o600))
			got, err := detectContentType(filename)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectContentTypeReadsOnlyBoundedSample(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A JSON document larger than the detection sample still detects as JSON via
	// the extension hint, and detection must not require reading the whole file.
	filename := filepath.Join(root, "large.json")
	contents := `{"padding": "` + strings.Repeat("x", 8192) + `"}`
	require.NoError(t, os.WriteFile(filename, []byte(contents), 0o600))
	got, err := detectContentType(filename)
	require.NoError(t, err)
	assert.Equal(t, "application/json", got)
}

func TestDetectContentTypeMissingFile(t *testing.T) {
	t.Parallel()
	_, err := detectContentType(filepath.Join(t.TempDir(), "missing.md"))
	require.Error(t, err)
}
