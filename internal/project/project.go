package project

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	module "github.com/lonegunmanb/r42/internal/module"
)

const (
	configDirectoryName  = "config"
	markerFileName       = ".initialized.json"
	markerFormatVersion  = 2
	modulesDirectoryName = "modules"
	transactionFileName  = ".initializing"
)

type InitOptions struct {
	Upgrade bool
	Fetch   module.FetchFunc
}

type marker struct {
	FormatVersion int       `json:"format_version"`
	SourceKey     string    `json:"source_key"`
	InitializedAt time.Time `json:"initialized_at"`
}

func Init(ctx context.Context, sourceLocation, stateDirectory string, options InitOptions) error {
	state, err := filepath.Abs(stateDirectory)
	if err != nil {
		// note: untested because filepath.Abs fails only when the process working directory is unavailable.
		return fmt.Errorf("resolve r42 state directory: %w", err)
	}
	state = filepath.Clean(state)
	localSource := localConfigurationSource(sourceLocation)
	source, sourceKey, cleanupSource, err := resolveConfigurationSource(ctx, sourceLocation, options.Fetch)
	if err != nil {
		return err
	}
	defer cleanupSource()
	if isWithin(state, source) {
		return fmt.Errorf("configuration directory %q must be outside r42 state directory %q", source, state)
	}
	if err = os.MkdirAll(state, 0o700); err != nil {
		return fmt.Errorf("create r42 state directory: %w", err)
	}
	transaction := filepath.Join(state, transactionFileName)
	interrupted, err := pathExists(transaction)
	if err != nil {
		return fmt.Errorf("inspect initialization transaction: %w", err)
	}
	sourceChanged, err := configurationSourceChanged(state, sourceKey)
	if err != nil {
		return err
	}
	if err = os.WriteFile(transaction, []byte("initializing\n"), 0o600); err != nil {
		return fmt.Errorf("begin initialization transaction: %w", err)
	}
	modules := filepath.Join(state, modulesDirectoryName)
	if err = module.Init(ctx, source, module.InitOptions{
		ModulesDirectory: modules,
		Upgrade:          options.Upgrade || sourceChanged || interrupted,
		Fetch:            options.Fetch,
	}); err != nil {
		var cleanupErr error
		if !interrupted {
			cleanupErr = removeTransaction(transaction)
		}
		return errors.Join(fmt.Errorf("initialize modules: %w", err), cleanupErr)
	}

	temporary, err := os.MkdirTemp(state, ".r42-config-")
	if err != nil {
		// note: untested because this requires a platform-specific permission or resource exhaustion failure.
		return fmt.Errorf("create configuration staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	staging := filepath.Join(temporary, "content")
	if err = copyConfiguration(ctx, source, staging); err != nil {
		return err
	}
	initializedAt := time.Now().UTC()
	encoded, err := json.MarshalIndent(marker{
		FormatVersion: markerFormatVersion, SourceKey: sourceKey, InitializedAt: initializedAt,
	}, "", "  ")
	if err != nil {
		// note: untested because marker contains only JSON-supported scalar fields.
		return fmt.Errorf("encode initialization marker: %w", err)
	}
	if err = os.WriteFile(filepath.Join(staging, markerFileName), append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write initialization marker: %w", err)
	}
	if err = activateConfiguration(staging, filepath.Join(state, configDirectoryName)); err != nil {
		return err
	}
	stateSource := sourceLocation
	if localSource {
		stateSource = source
	}
	projectState := newState(state, stateSource, sourceKey, initializedAt, localSource)
	if err = writeState(filepath.Join(state, StateFileName), projectState); err != nil {
		return fmt.Errorf("write initialized project state: %w", err)
	}
	return removeTransaction(transaction)
}

func Open(stateDirectory string) (string, string, error) {
	config, modules, _, err := openProject(stateDirectory)
	return config, modules, err
}

func openProject(stateDirectory string) (string, string, State, error) {
	state, err := filepath.Abs(stateDirectory)
	if err != nil {
		// note: untested because filepath.Abs fails only when the process working directory is unavailable.
		return "", "", State{}, fmt.Errorf("resolve r42 state directory: %w", err)
	}
	config := filepath.Join(state, configDirectoryName)
	modules := filepath.Join(state, modulesDirectoryName)
	interrupted, err := pathExists(filepath.Join(state, transactionFileName))
	if err != nil {
		return "", "", State{}, notInitializedError(state, fmt.Errorf("inspect initialization transaction: %w", err))
	}
	if interrupted {
		return "", "", State{}, fmt.Errorf(
			"working directory initialization was interrupted at %q; run r42 init <source>", state,
		)
	}
	encoded, err := os.ReadFile(filepath.Join(config, markerFileName))
	if err != nil {
		return "", "", State{}, notInitializedError(state, err)
	}
	var initialized marker
	if err = json.Unmarshal(encoded, &initialized); err != nil {
		return "", "", State{}, notInitializedError(state, err)
	}
	if initialized.FormatVersion != markerFormatVersion || strings.TrimSpace(initialized.SourceKey) == "" {
		return "", "", State{}, notInitializedError(state, fmt.Errorf("invalid initialization marker"))
	}
	projectState, _, err := readState(state)
	if err != nil {
		return "", "", State{}, notInitializedError(state, err)
	}
	if projectState.Configuration.SourceKey != initialized.SourceKey ||
		!projectState.Configuration.InitializedAt.Equal(initialized.InitializedAt) ||
		filepath.Clean(projectState.Configuration.Directory) != filepath.Clean(config) {
		return "", "", State{}, notInitializedError(
			state, fmt.Errorf("project state does not match initialized configuration"),
		)
	}
	info, err := os.Stat(modules)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("modules path is not a directory")
		}
		return "", "", State{}, notInitializedError(state, err)
	}
	return config, modules, projectState, nil
}

func copyConfiguration(ctx context.Context, source, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		// note: untested because destination is a fresh path inside a private staging directory.
		return fmt.Errorf("create configuration snapshot: %w", err)
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return fmt.Errorf("open configuration directory: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		// note: untested because destination was created immediately above.
		return fmt.Errorf("open configuration snapshot: %w", err)
	}
	defer func() { _ = destinationRoot.Close() }()

	err = fs.WalkDir(sourceRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if entry.IsDir() && (entry.Name() == ".r42" || entry.Name() == ".git") {
			return fs.SkipDir
		}
		if entry.IsDir() {
			if err := destinationRoot.MkdirAll(path, 0o700); err != nil {
				return fmt.Errorf("create directory %q: %w", path, err)
			}
			return nil
		}
		return copyConfigurationFile(sourceRoot, destinationRoot, path)
	})
	if err != nil {
		return fmt.Errorf("copy configuration: %w", err)
	}
	return nil
}

func copyConfigurationFile(source, destination *os.Root, path string) error {
	input, err := source.Open(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("configuration entry %q is not a regular file", path)
	}
	output, err := destination.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("copy %q: %w", path, err)
	}
	return nil
}

func activateConfiguration(staging, destination string) error {
	info, err := os.Stat(destination)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err = os.Rename(staging, destination); err != nil {
			// note: untested because both paths are controlled siblings on the same filesystem.
			return fmt.Errorf("activate configuration: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("inspect active configuration: %w", err)
	case !info.IsDir():
		return fmt.Errorf("active configuration %q is not a directory", destination)
	}

	backup, err := os.MkdirTemp(filepath.Dir(destination), ".r42-config-backup-")
	if err != nil {
		// note: untested because this requires a platform-specific permission or resource exhaustion failure.
		return fmt.Errorf("reserve configuration backup path: %w", err)
	}
	if err = os.Remove(backup); err != nil {
		// note: untested because backup is an empty directory created immediately above.
		return fmt.Errorf("prepare configuration backup path: %w", err)
	}
	defer func() { _ = os.RemoveAll(backup) }()
	if err = os.Rename(destination, backup); err != nil {
		// note: untested because both paths are controlled siblings on the same filesystem.
		return fmt.Errorf("back up active configuration: %w", err)
	}
	if err = os.Rename(staging, destination); err != nil {
		// note: untested because both paths are controlled siblings on the same filesystem.
		restoreErr := os.Rename(backup, destination)
		return errors.Join(fmt.Errorf("activate configuration: %w", err), restoreErr)
	}
	if err = os.RemoveAll(backup); err != nil {
		// note: untested because backup is owned by this process and no longer active.
		return fmt.Errorf("remove previous configuration: %w", err)
	}
	return nil
}

func absoluteDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		// note: untested because filepath.Abs fails only when the process working directory is unavailable.
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return absolute, nil
}

func isWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func configurationSourceChanged(state, sourceKey string) (bool, error) {
	encoded, err := os.ReadFile(filepath.Join(state, configDirectoryName, markerFileName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read previous initialization marker: %w", err)
	}
	var previous marker
	if json.Unmarshal(encoded, &previous) != nil {
		return true, nil //nolint:nilerr // A corrupt marker is recoverable by forcing a complete module refresh.
	}
	return previous.SourceKey != sourceKey, nil
}

func resolveConfigurationSource(
	ctx context.Context,
	source string,
	fetch module.FetchFunc,
) (string, string, func(), error) {
	if strings.TrimSpace(source) == "" {
		return "", "", func() {}, fmt.Errorf("configuration source is required")
	}
	if localConfigurationSource(source) {
		directory, resolveErr := absoluteDirectory(source)
		if resolveErr != nil {
			return "", "", func() {}, fmt.Errorf("resolve configuration directory: %w", resolveErr)
		}
		return directory, configurationSourceKey(directory), func() {}, nil
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		// note: untested because os.Getwd failure requires invalidating process-wide filesystem state.
		return "", "", func() {}, fmt.Errorf("get configuration source working directory: %w", err)
	}
	temporary, err := os.MkdirTemp("", "r42-source-")
	if err != nil {
		// note: untested because this requires a platform-specific permission or resource exhaustion failure.
		return "", "", func() {}, fmt.Errorf("create configuration source staging directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temporary) }
	destination := filepath.Join(temporary, "content")
	if err = os.Mkdir(destination, 0o700); err != nil {
		cleanup()
		// note: untested because destination is a fresh path inside a private temporary directory.
		return "", "", func() {}, fmt.Errorf("create configuration source destination: %w", err)
	}
	if fetch == nil {
		fetch = module.Fetch
	}
	if err = fetch(ctx, source, destination, workingDirectory); err != nil {
		cleanup()
		if contextErr := ctx.Err(); contextErr != nil {
			return "", "", func() {}, fmt.Errorf("fetch configuration source: %w", contextErr)
		}
		message := strings.ReplaceAll(err.Error(), source, "<redacted-configuration-source>")
		return "", "", func() {}, fmt.Errorf("fetch configuration source: %s", message)
	}
	return destination, configurationSourceKey(source), cleanup, nil
}

func localConfigurationSource(source string) bool {
	if module.IsLocalSource(source) {
		return true
	}
	_, err := os.Stat(source)
	return err == nil
}

func configurationSourceKey(source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func removeTransaction(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("complete initialization transaction: %w", err)
	}
	return nil
}

func notInitializedError(state string, cause error) error {
	return fmt.Errorf("working directory is not initialized at %q; run r42 init <source>: %w", state, cause)
}
