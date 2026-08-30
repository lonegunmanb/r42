package spec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ResolveSource resolves a folder source and verifies that it remains inside
// the active run root. The root and source must identify real directories.
func ResolveSource(source, runRoot string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("s3 folder source is required")
	}
	if containsDotDot(source) {
		return "", errors.New("s3 folder source contains path traversal")
	}
	root, err := filepath.Abs(filepath.Clean(filepath.FromSlash(runRoot)))
	if err != nil {
		return "", fmt.Errorf("resolve run root: %w", err)
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("stat run root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("s3 folder run root must not be a symlink")
	}
	if !rootInfo.IsDir() {
		return "", errors.New("s3 folder run root is not a directory")
	}

	resolved := filepath.FromSlash(source)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", fmt.Errorf("resolve source: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("check source containment: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("s3 folder source is outside run root: %q", resolved)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("s3 folder source does not exist: %q", resolved)
		}
		return "", fmt.Errorf("stat source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("s3 folder source must not be a symlink")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("s3 folder source is not a directory: %q", resolved)
	}
	return filepath.Clean(resolved), nil
}

func containsDotDot(value string) bool {
	if !strings.Contains(value, "..") {
		return false
	}
	return slices.Contains(strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }), "..")
}
