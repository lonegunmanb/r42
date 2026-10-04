package s3

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// contentSampleSize bounds how many bytes MIME detection reads from a file.
const contentSampleSize = 512

// textExtensionHints maps extensions to text MIME types that content sniffing
// cannot distinguish from generic text or from each other.
var textExtensionHints = map[string]string{
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
}

// detectContentType best-effort determines the MIME type of a file from a
// bounded content sample, using the filename extension only where content
// cannot distinguish textual formats. Unrecognized content falls back to
// application/octet-stream.
func detectContentType(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("open source file: %w", err)
	}
	defer func() { _ = file.Close() }()
	sample := make([]byte, contentSampleSize)
	n, err := file.Read(sample)
	if err != nil && n == 0 && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read source file sample: %w", err)
	}
	sample = sample[:n]
	detected := http.DetectContentType(sample)
	if detected == "text/plain; charset=utf-8" && json.Valid(sample) {
		return "application/json", nil
	}
	ext := strings.ToLower(filepath.Ext(filename))
	// A JSON file larger than the detection sample is truncated mid-document;
	// treat text with a JSON extension hint as JSON rather than generic text.
	if ext == ".json" && strings.HasPrefix(detected, "text/plain") {
		return "application/json", nil
	}
	if hint, ok := textExtensionHints[ext]; ok && strings.HasPrefix(detected, "text/plain") {
		return hint, nil
	}
	return detected, nil
}
