package ui_test

import (
	"path/filepath"
	"testing"
)

func testRunDirectory(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}
