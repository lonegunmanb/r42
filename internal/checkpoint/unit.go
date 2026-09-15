package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
)

type UnitStatus string

const (
	UnitStarted   UnitStatus = "started"
	UnitCompleted UnitStatus = "completed"
)

// UnitState is the durable outcome of one whole research block or dynamic task.
// A started unit is always rerun from its pre-unit snapshot.
type UnitState struct {
	Status UnitStatus      `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Unit owns the two durable states of a block/task transaction.
type Unit struct {
	store *Store
}

func NewUnit(directory string) *Unit {
	return &Unit{store: NewStore(directory)}
}

func (u *Unit) Begin(registry *artifactpkg.Registry, workspace string) error {
	if registry == nil {
		return errors.New("artifact registry is required")
	}
	if workspace == "" {
		return errors.New("unit workspace is required")
	}
	return u.commit(registry, workspace, UnitState{Status: UnitStarted})
}

func (u *Unit) Complete(registry *artifactpkg.Registry, workspace string, result []byte) error {
	if registry == nil {
		return errors.New("artifact registry is required")
	}
	if workspace == "" {
		return errors.New("unit workspace is required")
	}
	if !json.Valid(result) {
		return errors.New("unit result must be valid JSON")
	}
	return u.commit(registry, workspace, UnitState{Status: UnitCompleted, Result: result})
}

func (u *Unit) Load() (UnitState, error) {
	if u == nil || u.store == nil {
		return UnitState{}, errors.New("unit checkpoint store is required")
	}
	if err := u.store.cleanupStaging(); err != nil {
		return UnitState{}, err
	}
	saved, err := u.store.Load()
	if err != nil {
		return UnitState{}, err
	}
	var state UnitState
	if err = json.Unmarshal(saved.Payload, &state); err != nil {
		return UnitState{}, fmt.Errorf("decode unit checkpoint: %w", err)
	}
	if state.Status != UnitStarted && state.Status != UnitCompleted {
		return UnitState{}, fmt.Errorf("invalid unit checkpoint status %q", state.Status)
	}
	if state.Status == UnitCompleted && !json.Valid(state.Result) {
		return UnitState{}, errors.New("completed unit checkpoint has invalid result")
	}
	return state, nil
}

func (u *Unit) Rollback(registry *artifactpkg.Registry) (UnitState, error) {
	state, err := u.Load()
	if err != nil {
		return UnitState{}, err
	}
	if state.Status != UnitStarted {
		return UnitState{}, errors.New("cannot roll back a completed unit")
	}
	if _, err = u.store.RestoreWorkspace(registry); err != nil {
		return UnitState{}, fmt.Errorf("restore unit start snapshot: %w", err)
	}
	return state, nil
}

func (u *Unit) RestoreCompleted(registry *artifactpkg.Registry) (UnitState, error) {
	state, err := u.Load()
	if err != nil {
		return UnitState{}, err
	}
	if state.Status != UnitCompleted {
		return UnitState{}, errors.New("unit has not completed")
	}
	if _, err = u.store.RestoreWorkspace(registry); err != nil {
		return UnitState{}, fmt.Errorf("restore completed unit: %w", err)
	}
	return state, nil
}

func (u *Unit) commit(registry *artifactpkg.Registry, workspace string, state UnitState) error {
	if u == nil || u.store == nil {
		return errors.New("unit checkpoint store is required")
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return fmt.Errorf("create unit workspace: %w", err)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode unit checkpoint: %w", err)
	}
	if err = u.store.Commit(State{
		ArtifactRegistry: registry.SnapshotWorkspace(workspace), Payload: payload,
		WorkspaceRoots: []string{filepath.Clean(workspace)},
	}); err != nil {
		return fmt.Errorf("commit unit checkpoint: %w", err)
	}
	return nil
}
