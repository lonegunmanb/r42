package run

import (
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
	projectDirectory, err := filepath.Abs(m.projectDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolving project directory: %w", err)
	}
	runsDirectory := filepath.Join(projectDirectory, ".r42", "runs")
	if err = os.MkdirAll(runsDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("creating runs directory: %w", err)
	}
	directory, err := os.MkdirTemp(runsDirectory, "run-")
	if err != nil {
		return nil, fmt.Errorf("creating run directory: %w", err)
	}
	return &Run{id: filepath.Base(directory), directory: directory}, nil
}

func (r *Run) ID() string {
	return r.id
}

func (r *Run) Directory() string {
	return r.directory
}

func (r *Run) Workspace(address string) (string, error) {
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("block address is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	digest := sha256.Sum256([]byte(address))
	key := hex.EncodeToString(digest[:])
	blocksDirectory := filepath.Join(r.directory, "blocks")
	identitiesDirectory := filepath.Join(r.directory, "block-addresses")
	if err := os.MkdirAll(blocksDirectory, 0o700); err != nil {
		return "", fmt.Errorf("creating blocks directory: %w", err)
	}
	if err := os.MkdirAll(identitiesDirectory, 0o700); err != nil {
		return "", fmt.Errorf("creating block identities directory: %w", err)
	}

	directory := filepath.Join(blocksDirectory, key)
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
