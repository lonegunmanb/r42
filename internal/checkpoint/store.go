// Package checkpoint persists recoverable workflow handoff state.
package checkpoint

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
)

const (
	// CurrentSchemaVersion is the checkpoint manifest format understood by this binary.
	CurrentSchemaVersion = 1
	checkpointsDirectory = "checkpoints"
	latestFile           = "latest"
	manifestFile         = "manifest.json"
)

// State is the recoverable host state at one successful workflow handoff.
// Further workflow/session fields are added to this versioned structure as the
// coordinator wiring lands; artifact recovery is intentionally self-contained.
type State struct {
	SchemaVersion         int                  `json:"schema_version"`
	CheckpointID          string               `json:"checkpoint_id"`
	CreatedAt             time.Time            `json:"created_at"`
	ArtifactRegistry      artifactpkg.Snapshot `json:"artifact_registry"`
	Payload               json.RawMessage      `json:"payload,omitempty"`
	SessionStoreDirectory string               `json:"session_store_directory,omitempty"`
	Sessions              []Session            `json:"sessions,omitempty"`
	WorkspaceRoots        []string             `json:"workspace_roots,omitempty"`
	Workspaces            []Workspace          `json:"workspaces"`
}

// Session identifies one SDK session persistence directory captured at the
// handoff. The SDK owns its contents; r42 treats it as an opaque tree.
type Session struct {
	ID      string          `json:"id"`
	Archive string          `json:"archive,omitempty"`
	Entries []WorkspaceFile `json:"entries,omitempty"`
}

// Workspace describes an exact workspace tree saved under a checkpoint.
type Workspace struct {
	Path    string          `json:"path"`
	Archive string          `json:"archive"`
	Entries []WorkspaceFile `json:"entries"`
}

// WorkspaceFile is a regular file or directory relative to a workspace root.
type WorkspaceFile struct {
	Path      string      `json:"path"`
	Directory bool        `json:"directory"`
	Mode      fs.FileMode `json:"mode"`
	SHA256    string      `json:"sha256,omitempty"`
}

type manifest struct {
	State  State  `json:"state"`
	SHA256 string `json:"sha256"`
}

// Store owns checkpoints for exactly one run directory.
type Store struct {
	runDirectory string
}

func NewStore(runDirectory string) *Store {
	return &Store{runDirectory: runDirectory}
}

// Commit captures all artifact workspaces then atomically publishes the
// checkpoint. A failed or interrupted staging directory has no latest pointer
// and is never considered recoverable.
func (s *Store) Commit(state State) error {
	if s == nil || strings.TrimSpace(s.runDirectory) == "" {
		return errors.New("checkpoint run directory is required")
	}
	if state.SchemaVersion == 0 {
		state.SchemaVersion = CurrentSchemaVersion
	}
	if state.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported checkpoint schema version %d", state.SchemaVersion)
	}
	if state.CheckpointID == "" {
		id, err := newCheckpointID()
		if err != nil {
			return err
		}
		state.CheckpointID = id
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}

	root := filepath.Join(s.runDirectory, checkpointsDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}
	if err := validateCheckpointID(state.CheckpointID); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return fmt.Errorf("create checkpoint staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	workspaces, err := snapshotWorkspaces(stage, state.ArtifactRegistry, state.WorkspaceRoots)
	if err != nil {
		return err
	}
	state.Workspaces = workspaces
	if err = snapshotSessions(stage, &state); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode checkpoint state: %w", err)
	}
	sum := sha256.Sum256(encoded)
	contents, err := json.Marshal(manifest{State: state, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return fmt.Errorf("encode checkpoint manifest: %w", err)
	}
	if err = writeSyncedFile(filepath.Join(stage, manifestFile), contents, 0o600); err != nil {
		return fmt.Errorf("write checkpoint manifest: %w", err)
	}
	if err = syncDirectory(stage); err != nil {
		return fmt.Errorf("sync checkpoint staging directory: %w", err)
	}
	destination := filepath.Join(root, state.CheckpointID)
	if _, statErr := os.Stat(destination); statErr == nil {
		return fmt.Errorf("checkpoint %q already exists", state.CheckpointID)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("stat checkpoint destination: %w", statErr)
	}
	if err = os.Rename(stage, destination); err != nil {
		return fmt.Errorf("publish checkpoint: %w", err)
	}
	if err = syncDirectory(root); err != nil {
		return fmt.Errorf("sync published checkpoint directory: %w", err)
	}
	if err = publishLatest(root, state.CheckpointID); err != nil {
		return err
	}
	return nil
}

// Load returns the newest completely committed and integrity-checked state.
func (s *Store) Load() (State, error) {
	if s == nil || strings.TrimSpace(s.runDirectory) == "" {
		return State{}, errors.New("checkpoint run directory is required")
	}
	root := filepath.Join(s.runDirectory, checkpointsDirectory)
	latest, err := os.ReadFile(filepath.Join(root, latestFile))
	if err != nil {
		return State{}, fmt.Errorf("read latest checkpoint: %w", err)
	}
	id := strings.TrimSpace(string(latest))
	if err = validateCheckpointID(id); err != nil {
		return State{}, err
	}
	contents, err := os.ReadFile(filepath.Join(root, id, manifestFile))
	if err != nil {
		return State{}, fmt.Errorf("read checkpoint manifest: %w", err)
	}
	var saved manifest
	if err = json.Unmarshal(contents, &saved); err != nil {
		return State{}, fmt.Errorf("decode checkpoint manifest: %w", err)
	}
	encoded, err := json.Marshal(saved.State)
	if err != nil {
		return State{}, fmt.Errorf("encode checkpoint state for verification: %w", err)
	}
	sum := sha256.Sum256(encoded)
	if saved.SHA256 != hex.EncodeToString(sum[:]) {
		return State{}, errors.New("checkpoint manifest integrity check failed")
	}
	if saved.State.SchemaVersion != CurrentSchemaVersion {
		return State{}, fmt.Errorf("unsupported checkpoint schema version %d", saved.State.SchemaVersion)
	}
	if saved.State.CheckpointID != id {
		return State{}, errors.New("checkpoint manifest id does not match latest checkpoint")
	}
	if err = verifyWorkspaces(filepath.Join(root, id), saved.State.Workspaces); err != nil {
		return State{}, err
	}
	if err = verifySessions(filepath.Join(root, id), saved.State.Sessions); err != nil {
		return State{}, err
	}
	return saved.State, nil
}

func (s *Store) cleanupStaging() error {
	if s == nil || strings.TrimSpace(s.runDirectory) == "" {
		return errors.New("checkpoint run directory is required")
	}
	root := filepath.Join(s.runDirectory, checkpointsDirectory)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read checkpoint directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".staging-") {
			continue
		}
		if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove interrupted checkpoint staging directory: %w", err)
		}
	}
	return nil
}

// Restore returns to the latest committed artifact version and replaces the
// supplied registry metadata. It must run while no prior process can write the
// run directory.
func (s *Store) Restore(registry *artifactpkg.Registry) (State, error) {
	return s.restore(registry, false)
}

// RestoreMerged restores this workflow checkpoint while retaining artifact
// capabilities restored from other workflow checkpoints in the same run.
func (s *Store) RestoreMerged(registry *artifactpkg.Registry) (State, error) {
	return s.restore(registry, true)
}

// RestoreWorkspace restores the unit workspace and replaces only its artifact
// registry entries, preserving completed independent units.
func (s *Store) RestoreWorkspace(registry *artifactpkg.Registry) (State, error) {
	if registry == nil {
		return State{}, errors.New("artifact registry is required")
	}
	state, err := s.Load()
	if err != nil {
		return State{}, err
	}
	root := filepath.Join(s.runDirectory, checkpointsDirectory, state.CheckpointID)
	for _, workspace := range state.Workspaces {
		if err = restoreWorkspace(root, workspace); err != nil {
			return State{}, err
		}
		if err = registry.RestoreWorkspace(workspace.Path, state.ArtifactRegistry); err != nil {
			return State{}, err
		}
	}
	return state, nil
}

func (s *Store) restore(registry *artifactpkg.Registry, merge bool) (State, error) {
	if registry == nil {
		return State{}, errors.New("artifact registry is required")
	}
	state, err := s.Load()
	if err != nil {
		return State{}, err
	}
	root := filepath.Join(s.runDirectory, checkpointsDirectory, state.CheckpointID)
	for _, workspace := range state.Workspaces {
		if err = restoreWorkspace(root, workspace); err != nil {
			return State{}, err
		}
	}
	if err = restoreSessions(root, state); err != nil {
		return State{}, err
	}
	if merge {
		err = registry.Merge(state.ArtifactRegistry)
	} else {
		err = registry.Restore(state.ArtifactRegistry)
	}
	if err != nil {
		return State{}, err
	}
	return state, nil
}

func snapshotSessions(stage string, state *State) error {
	if len(state.Sessions) == 0 {
		return nil
	}
	if strings.TrimSpace(state.SessionStoreDirectory) == "" {
		return errors.New("checkpoint session store directory is required")
	}
	for index := range state.Sessions {
		id := state.Sessions[index].ID
		if err := validateCheckpointID(id); err != nil {
			return fmt.Errorf("invalid checkpoint session id: %w", err)
		}
		archive := filepath.ToSlash(filepath.Join("sessions", fmt.Sprintf("%03d", index)))
		entries, err := copyWorkspace(filepath.Join(state.SessionStoreDirectory, "session-state", id), filepath.Join(stage, archive))
		if err != nil {
			return fmt.Errorf("snapshot session %q: %w", id, err)
		}
		state.Sessions[index].Archive = archive
		state.Sessions[index].Entries = entries
	}
	return nil
}

func restoreSessions(root string, state State) error {
	for _, session := range state.Sessions {
		if err := validateCheckpointID(session.ID); err != nil {
			return fmt.Errorf("invalid checkpoint session id: %w", err)
		}
		if err := validateRelativePath(session.Archive); err != nil {
			return err
		}
		workspace := Workspace{Path: filepath.Join(state.SessionStoreDirectory, "session-state", session.ID), Archive: session.Archive, Entries: session.Entries}
		if err := restoreWorkspace(root, workspace); err != nil {
			return fmt.Errorf("restore session %q: %w", session.ID, err)
		}
	}
	return nil
}

func verifySessions(root string, sessions []Session) error {
	for _, session := range sessions {
		if err := validateCheckpointID(session.ID); err != nil {
			return fmt.Errorf("invalid checkpoint session id: %w", err)
		}
		if err := verifyWorkspaces(root, []Workspace{{Archive: session.Archive, Entries: session.Entries}}); err != nil {
			message := err.Error()
			message = strings.Replace(message, "checkpoint workspace file integrity check failed", "checkpoint session file integrity check failed", 1)
			return errors.New(message)
		}
	}
	return nil
}

func snapshotWorkspaces(stage string, registry artifactpkg.Snapshot, roots []string) ([]Workspace, error) {
	paths := make(map[string]struct{})
	for _, entry := range registry.Entries {
		if strings.TrimSpace(entry.Workspace) == "" {
			return nil, fmt.Errorf("artifact %q has no workspace", entry.Record.ID)
		}
		paths[entry.Workspace] = struct{}{}
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			return nil, errors.New("checkpoint workspace root is required")
		}
		paths[root] = struct{}{}
	}
	workspacePaths := make([]string, 0, len(paths))
	for path := range paths {
		workspacePaths = append(workspacePaths, path)
	}
	slices.Sort(workspacePaths)
	result := make([]Workspace, 0, len(workspacePaths))
	for index, path := range workspacePaths {
		archive := filepath.ToSlash(filepath.Join("workspaces", fmt.Sprintf("%03d", index)))
		entries, err := copyWorkspace(path, filepath.Join(stage, archive))
		if err != nil {
			return nil, err
		}
		result = append(result, Workspace{Path: path, Archive: archive, Entries: entries})
	}
	return result, nil
}

func copyWorkspace(source, destination string) ([]WorkspaceFile, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat artifact workspace %q: %w", source, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("artifact workspace %q is not a directory", source)
	}
	entries := make([]WorkspaceFile, 0)
	err = filepath.WalkDir(source, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact workspace %q contains unsupported symlink %q", source, relative)
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		entry := WorkspaceFile{Path: filepath.ToSlash(relative), Directory: item.IsDir(), Mode: info.Mode().Perm()}
		target := filepath.Join(destination, relative)
		if item.IsDir() {
			if err = os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			entries = append(entries, entry)
			return nil
		}
		if !item.Type().IsRegular() {
			return fmt.Errorf("artifact workspace %q contains unsupported file %q", source, relative)
		}
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		sum, err := copyFile(path, target, info.Mode().Perm())
		if err != nil {
			return err
		}
		entry.SHA256 = sum
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot artifact workspace %q: %w", source, err)
	}
	return entries, nil
}

func verifyWorkspaces(root string, workspaces []Workspace) error {
	for _, workspace := range workspaces {
		if err := validateRelativePath(workspace.Archive); err != nil {
			return fmt.Errorf("invalid checkpoint workspace archive: %w", err)
		}
		for _, entry := range workspace.Entries {
			if err := validateRelativePath(entry.Path); err != nil {
				return fmt.Errorf("invalid checkpoint workspace entry: %w", err)
			}
			if entry.Directory {
				continue
			}
			sum, err := fileSHA256(filepath.Join(root, workspace.Archive, entry.Path))
			if err != nil {
				return fmt.Errorf("verify checkpoint workspace file %q: %w", entry.Path, err)
			}
			if sum != entry.SHA256 {
				return fmt.Errorf("checkpoint workspace file integrity check failed for %q", entry.Path)
			}
		}
	}
	return nil
}

func restoreWorkspace(root string, workspace Workspace) error {
	if strings.TrimSpace(workspace.Path) == "" {
		return errors.New("checkpoint workspace path is required")
	}
	if err := validateRelativePath(workspace.Archive); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace.Path, 0o700); err != nil {
		return fmt.Errorf("create artifact workspace %q: %w", workspace.Path, err)
	}
	current, err := os.ReadDir(workspace.Path)
	if err != nil {
		return fmt.Errorf("read artifact workspace %q: %w", workspace.Path, err)
	}
	for _, entry := range current {
		if err = os.RemoveAll(filepath.Join(workspace.Path, entry.Name())); err != nil {
			return fmt.Errorf("clear artifact workspace %q: %w", workspace.Path, err)
		}
	}
	for _, entry := range workspace.Entries {
		if err = validateRelativePath(entry.Path); err != nil {
			return err
		}
		target := filepath.Join(workspace.Path, entry.Path)
		if entry.Directory {
			if err = os.MkdirAll(target, entry.Mode.Perm()); err != nil {
				return fmt.Errorf("restore artifact directory %q: %w", entry.Path, err)
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("restore artifact parent directory %q: %w", entry.Path, err)
		}
		if _, err = copyFile(filepath.Join(root, workspace.Archive, entry.Path), target, entry.Mode.Perm()); err != nil {
			return fmt.Errorf("restore artifact file %q: %w", entry.Path, err)
		}
	}
	return nil
}

func publishLatest(root, id string) error {
	temporary, err := os.CreateTemp(root, ".latest-")
	if err != nil {
		return fmt.Errorf("create latest checkpoint pointer: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if _, err = temporary.WriteString(id + "\n"); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write latest checkpoint pointer: %w", err)
	}
	if err = os.Rename(temporaryName, filepath.Join(root, latestFile)); err != nil {
		return fmt.Errorf("publish latest checkpoint pointer: %w", err)
	}
	if err = syncDirectory(root); err != nil {
		return fmt.Errorf("sync latest checkpoint pointer: %w", err)
	}
	return nil
}

func writeSyncedFile(path string, contents []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(contents); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func copyFile(source, destination string, mode fs.FileMode) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateCheckpointID(id string) error {
	if strings.TrimSpace(id) == "" || filepath.Base(id) != id || strings.Contains(id, string(filepath.Separator)) {
		return fmt.Errorf("invalid checkpoint id %q", id)
	}
	return nil
}

func validateRelativePath(path string) error {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
		return fmt.Errorf("path %q is not relative", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes checkpoint workspace", path)
	}
	return nil
}

func newCheckpointID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		// note: untested because crypto/rand.Reader cannot be replaced without global process mutation.
		return "", fmt.Errorf("generate checkpoint id: %w", err)
	}
	return "checkpoint-" + hex.EncodeToString(random), nil
}
