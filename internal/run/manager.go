package run

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Manager struct {
	projectDirectory string
}

type Run struct {
	mu        sync.Mutex
	id        string
	directory string
}

func NewManager(projectDirectory string) *Manager {
	return &Manager{projectDirectory: projectDirectory}
}

func (m *Manager) Create() (*Run, error) {
	reserved, err := m.Reserve()
	if err != nil {
		return nil, err
	}
	if err = reserved.Ensure(); err != nil {
		return nil, err
	}
	return reserved, nil
}

func (m *Manager) Reserve() (*Run, error) {
	projectDirectory, err := filepath.Abs(m.projectDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolving project directory: %w", err)
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		// note: untested because crypto/rand.Reader cannot be replaced without global process mutation.
		return nil, fmt.Errorf("reserve run directory: %w", err)
	}
	id := "run-" + hex.EncodeToString(random)
	return &Run{id: id, directory: filepath.Join(projectDirectory, ".r42", "runs", id)}, nil
}

func Open(directory string) (*Run, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolving run directory: %w", err)
	}
	return &Run{id: filepath.Base(absolute), directory: absolute}, nil
}

func (r *Run) ID() string {
	return r.id
}

func (r *Run) Directory() string {
	return r.directory
}

func (r *Run) Ensure() error {
	if err := os.MkdirAll(filepath.Dir(r.directory), 0o700); err != nil {
		return fmt.Errorf("creating runs directory: %w", err)
	}
	if err := os.MkdirAll(r.directory, 0o700); err != nil {
		return fmt.Errorf("creating run directory: %w", err)
	}
	return nil
}

func (r *Run) WorkspacePath(address string) (string, error) {
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("block address is required")
	}
	digest := sha256.Sum256([]byte(address))
	key := hex.EncodeToString(digest[:])
	return filepath.ToSlash(filepath.Join(r.directory, "blocks", key)), nil
}

func (r *Run) Workspace(address string) (string, error) {
	directory, err := r.WorkspacePath(address)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if err = r.Ensure(); err != nil {
		return "", err
	}
	directory = filepath.FromSlash(directory)
	key := filepath.Base(directory)
	blocksDirectory := filepath.Join(r.directory, "blocks")
	identitiesDirectory := filepath.Join(r.directory, "block-addresses")
	if err := os.MkdirAll(blocksDirectory, 0o700); err != nil {
		return "", fmt.Errorf("creating blocks directory: %w", err)
	}
	if err := os.MkdirAll(identitiesDirectory, 0o700); err != nil {
		return "", fmt.Errorf("creating block identities directory: %w", err)
	}

	identityPath := filepath.Join(identitiesDirectory, key)
	identity, err := os.ReadFile(identityPath)
	if err == nil {
		if string(identity) != address {
			return "", fmt.Errorf("block workspace identity collision for %q", address)
		}
		if err = os.MkdirAll(directory, 0o700); err != nil {
			return "", fmt.Errorf("creating block workspace: %w", err)
		}
		return directory, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("reading block workspace identity: %w", err)
	}
	if _, err = os.Stat(directory); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("block workspace identity is missing for %q", address)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("creating block workspace: %w", err)
	}
	if err = os.WriteFile(identityPath, []byte(address), 0o600); err != nil {
		_ = os.Remove(directory)
		return "", fmt.Errorf("writing block workspace identity: %w", err)
	}
	return directory, nil
}
