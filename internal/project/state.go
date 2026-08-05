package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	StateFileName      = "state.json"
	StateFormatVersion = 1
)

type State struct {
	FormatVersion int                `json:"format_version"`
	Configuration StateConfiguration `json:"configuration"`
	Outputs       *StateOutputs      `json:"outputs,omitempty"`
}

type StateConfiguration struct {
	Source        string    `json:"source"`
	SourceKey     string    `json:"source_key"`
	Directory     string    `json:"directory"`
	InitializedAt time.Time `json:"initialized_at"`
}

type StateOutputs struct {
	RunDirectory string          `json:"run_directory"`
	AppliedAt    time.Time       `json:"applied_at"`
	Values       json.RawMessage `json:"values"`
}

func SaveOutputs(stateDirectory, runDirectory string, outputs []byte) error {
	state, statePath, err := readState(stateDirectory)
	if err != nil {
		return err
	}
	if !jsonObject(outputs) {
		return fmt.Errorf("saved outputs must be a JSON object")
	}
	state.Outputs = &StateOutputs{
		RunDirectory: runDirectory,
		AppliedAt:    time.Now().UTC(),
		Values:       bytes.Clone(outputs),
	}
	if err = writeState(statePath, state); err != nil {
		return fmt.Errorf("save project outputs: %w", err)
	}
	return nil
}

func ReadOutputs(stateDirectory string) ([]byte, error) {
	_, _, state, err := openProject(stateDirectory)
	if err != nil {
		return nil, err
	}
	if state.Outputs == nil {
		return nil, fmt.Errorf("no saved outputs for the current configuration; run r42 apply")
	}
	if !jsonObject(state.Outputs.Values) {
		return nil, fmt.Errorf("saved outputs in project state must be a JSON object")
	}
	return bytes.Clone(state.Outputs.Values), nil
}

func newState(
	stateDirectory string,
	source string,
	sourceKey string,
	initializedAt time.Time,
	local bool,
) State {
	displaySource := remoteSourceDisplay(source)
	if local {
		displaySource = filepath.ToSlash(source)
	}
	return State{
		FormatVersion: StateFormatVersion,
		Configuration: StateConfiguration{
			Source: displaySource, SourceKey: sourceKey,
			Directory: filepath.Join(stateDirectory, configDirectoryName), InitializedAt: initializedAt,
		},
	}
}

func readState(stateDirectory string) (State, string, error) {
	statePath := filepath.Join(stateDirectory, StateFileName)
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		return State{}, statePath, fmt.Errorf("read project state %q: %w", statePath, err)
	}
	var state State
	if err = json.Unmarshal(encoded, &state); err != nil {
		return State{}, statePath, fmt.Errorf("decode project state %q: %w", statePath, err)
	}
	if state.FormatVersion != StateFormatVersion || strings.TrimSpace(state.Configuration.SourceKey) == "" {
		return State{}, statePath, fmt.Errorf("project state %q has an unsupported or invalid format", statePath)
	}
	return state, statePath, nil
}

func writeState(path string, state State) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project state: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".r42-state-")
	if err != nil {
		return fmt.Errorf("create project state temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(encoded)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write project state temporary file: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("activate project state: %w", err)
	}
	return nil
}

func jsonObject(encoded []byte) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(encoded, &value) == nil && value != nil
}

func remoteSourceDisplay(source string) string {
	prefix := ""
	if getter, locator, found := strings.Cut(source, "::"); found {
		prefix = getter + "::"
		source = locator
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return prefix + remoteShorthandDisplay(source)
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := make(url.Values)
	if ref := parsed.Query().Get("ref"); ref != "" {
		query.Set("ref", ref)
	}
	parsed.RawQuery = query.Encode()
	return prefix + parsed.String()
}

func remoteShorthandDisplay(source string) string {
	location, rawQuery, found := strings.Cut(source, "?")
	if strings.Contains(location, "@") {
		return "<redacted-remote-source>"
	}
	if !found {
		return location
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil || query.Get("ref") == "" {
		return location
	}
	return location + "?ref=" + url.QueryEscape(query.Get("ref"))
}
