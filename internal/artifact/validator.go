package artifact

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	corespec "github.com/lonegunmanb/r42/internal/spec"
)

type Result struct {
	Paths  map[string]string
	Issues []corespec.Issue
}

func Validate(workspace string, artifacts []researchspec.Artifact) (Result, error) {
	if strings.TrimSpace(workspace) == "" {
		return Result{}, fmt.Errorf("artifact workspace is required")
	}
	result := Result{
		Paths:  make(map[string]string, len(artifacts)),
		Issues: []corespec.Issue{},
	}
	for _, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			return Result{}, err
		}
		if _, exists := result.Paths[artifact.Name]; exists {
			return Result{}, fmt.Errorf("duplicate artifact name %q", artifact.Name)
		}

		path, err := resolvePath(workspace, artifact.Path)
		if err != nil {
			return Result{}, fmt.Errorf("resolve artifact %s path: %w", artifact.Name, err)
		}
		result.Paths[artifact.Name] = path
		issues, err := validateArtifact(path, artifact)
		if err != nil {
			return Result{}, err
		}
		result.Issues = append(result.Issues, issues...)
	}
	return result, nil
}

func resolvePath(workspace, path string) (string, error) {
	// Artifact authors deliberately may address files outside the block workspace.
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func validateArtifact(path string, artifact researchspec.Artifact) ([]corespec.Issue, error) {
	info, err := os.Stat(path)
	if err != nil {
		blockedParent, blocked, parentErr := nonDirectoryAncestor(path)
		if parentErr != nil {
			return nil, fmt.Errorf("inspect artifact %s parent: %w", artifact.Name, parentErr)
		}
		if blocked {
			if artifact.Required {
				return []corespec.Issue{artifactIssue(
					"artifact_path_blocked",
					fmt.Sprintf("required artifact %q is blocked by non-directory parent %q", artifact.Name, blockedParent),
					path,
					"replace the blocking parent with a directory and create the artifact",
				)}, nil
			}
			return []corespec.Issue{}, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			if artifact.Required {
				return []corespec.Issue{artifactIssue(
					"artifact_missing",
					fmt.Sprintf("required artifact %q does not exist", artifact.Name),
					path,
					"create the artifact at the declared path",
				)}, nil
			}
			return []corespec.Issue{}, nil
		}
		return nil, fmt.Errorf("inspect artifact %s: %w", artifact.Name, err)
	}

	typeMatches := info.Mode().IsRegular()
	if artifact.Type == researchspec.ArtifactTypeDirectory {
		typeMatches = info.IsDir()
	}
	if !typeMatches {
		return []corespec.Issue{artifactIssue(
			"artifact_type_mismatch",
			fmt.Sprintf("artifact %q is not a %s", artifact.Name, artifact.Type),
			path,
			fmt.Sprintf("replace it with a %s", artifact.Type),
		)}, nil
	}
	if !artifact.NonEmpty {
		return []corespec.Issue{}, nil
	}

	nonEmpty := info.Size() > 0
	if artifact.Type == researchspec.ArtifactTypeDirectory {
		nonEmpty, err = directoryContainsRegularFile(path)
		if err != nil {
			return nil, fmt.Errorf("inspect artifact %s directory: %w", artifact.Name, err)
		}
	}
	if nonEmpty {
		return []corespec.Issue{}, nil
	}
	return []corespec.Issue{artifactIssue(
		"artifact_empty",
		fmt.Sprintf("artifact %q is empty", artifact.Name),
		path,
		"write non-empty artifact content at the declared path",
	)}, nil
}

func nonDirectoryAncestor(path string) (string, bool, error) {
	return nonDirectoryAncestorWith(path, os.Stat)
}

func nonDirectoryAncestorWith(path string, stat func(string) (fs.FileInfo, error)) (string, bool, error) {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := stat(parent)
		if err == nil {
			if !info.IsDir() {
				return parent, true, nil
			}
			return "", false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", false, err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", false, nil
		}
	}
}

func directoryContainsRegularFile(directory string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			nonEmpty, childErr := directoryContainsRegularFile(path)
			if childErr != nil {
				return false, childErr
			}
			if nonEmpty {
				return true, nil
			}
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return false, infoErr
		}
		if info.Mode().IsRegular() {
			return true, nil
		}
	}
	return false, nil
}

func artifactIssue(code, message, path, repairHint string) corespec.Issue {
	return corespec.Issue{
		Code:       code,
		Message:    message,
		Path:       &path,
		RepairHint: &repairHint,
	}
}
