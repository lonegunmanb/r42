package spec

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar"
)

type SourceFile struct {
	AbsolutePath string
	RelativePath string
}

func ListSourceFiles(root string, excludes []string) ([]SourceFile, error) {
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("stat source root: %w", err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("source root is not a directory: %q", root)
	}
	patterns := make([]string, len(excludes))
	for index, pattern := range excludes {
		patterns[index] = strings.ReplaceAll(filepath.ToSlash(pattern), "\\", "/")
		if _, err := doublestar.Match(patterns[index], ""); err != nil {
			return nil, fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
		}
	}
	files := make([]SourceFile, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative source path: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			matched, err := excluded(patterns, relative)
			if err != nil {
				return err
			}
			if matched {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		matched, err := excluded(patterns, relative)
		if err != nil {
			return err
		}
		if !matched {
			files = append(files, SourceFile{AbsolutePath: filepath.Clean(path), RelativePath: relative})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk source root: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files, nil
}

func excluded(patterns []string, path string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, path)
		if err != nil {
			return false, fmt.Errorf("match exclude pattern %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
