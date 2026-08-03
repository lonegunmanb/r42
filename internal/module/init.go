package module

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	getter "github.com/hashicorp/go-getter/v2"
	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/zclconf/go-cty/cty"
)

type FetchFunc func(context.Context, string, string, string) error

type InitOptions struct {
	ModulesDirectory string
	Upgrade          bool
	Fetch            FetchFunc
}

type moduleCall struct {
	label  string
	source string
}

func Init(ctx context.Context, directory string, options InitOptions) error {
	root, err := absoluteDirectory(directory)
	if err != nil {
		return fmt.Errorf("read root module: %w", err)
	}
	modules, err := absolutePath(options.ModulesDirectory)
	if err != nil {
		return fmt.Errorf("resolve modules directory: %w", err)
	}
	if options.Upgrade {
		return upgrade(ctx, root, modules, options)
	}
	if err = os.MkdirAll(modules, 0o700); err != nil {
		return fmt.Errorf("create modules directory %q: %w", modules, err)
	}
	installer := installer{modules: modules, options: options}
	return installer.installChildren(ctx, root, root, "", map[string]struct{}{"local:" + root: {}}, false)
}

func upgrade(ctx context.Context, root, modules string, options InitOptions) error {
	info, err := os.Stat(modules)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("modules directory %q is not a directory", modules)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect modules directory %q: %w", modules, err)
	}
	parent := filepath.Dir(modules)
	if err = os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create modules parent directory: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".r42-modules-")
	if err != nil {
		// note: untested because this requires a platform-specific permission or resource exhaustion failure.
		return fmt.Errorf("create modules staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	staging := filepath.Join(temporary, "content")
	if err = os.Mkdir(staging, 0o700); err != nil {
		// note: untested because staging is a new path inside a private temporary directory.
		return fmt.Errorf("create modules staging root: %w", err)
	}
	installer := installer{modules: staging, options: options}
	if err = installer.installChildren(ctx, root, root, "", map[string]struct{}{"local:" + root: {}}, false); err != nil {
		return err
	}
	if info == nil {
		if err = os.Rename(staging, modules); err != nil {
			// note: untested because both paths share a freshly created parent filesystem.
			return fmt.Errorf("activate modules: %w", err)
		}
		return nil
	}
	return replaceDirectory(staging, modules)
}

func Directory(modulesDirectory, canonicalAddress string) (string, error) {
	parts := strings.Split(canonicalAddress, ".")
	if len(parts) == 0 || len(parts)%2 != 0 {
		return "", fmt.Errorf("module address %q is invalid", canonicalAddress)
	}
	labels := make([]string, 0, len(parts)/2)
	for index := 0; index < len(parts); index += 2 {
		if parts[index] != "module" || !validPathLabel(parts[index+1]) {
			return "", fmt.Errorf("module address %q is invalid", canonicalAddress)
		}
		labels = append(labels, parts[index+1])
	}
	return filepath.Join(append([]string{modulesDirectory}, labels...)...), nil
}

type installer struct {
	modules string
	options InitOptions
}

func (i installer) installChildren(
	ctx context.Context,
	scanDirectory string,
	sourceDirectory string,
	addressPrefix string,
	stack map[string]struct{},
	replaceCopiedDestination bool,
) error {
	calls, err := moduleCalls(ctx, scanDirectory)
	if err != nil {
		return err
	}
	for _, call := range calls {
		address := "module." + call.label
		if addressPrefix != "" {
			address = addressPrefix + "." + address
		}
		destination, err := Directory(i.modules, address)
		if err != nil {
			return err
		}
		source, key, local, err := resolveSource(sourceDirectory, call.source)
		if err != nil {
			return fmt.Errorf("module %s: %w", address, err)
		}
		if _, exists := stack[key]; exists {
			return fmt.Errorf("module source cycle at %s", address)
		}
		installed, installErr := i.install(
			ctx, source, sourceDirectory, destination, local, replaceCopiedDestination,
		)
		if installErr != nil {
			return fmt.Errorf("install module %s: %w", address, installErr)
		}
		nextStack := cloneSet(stack)
		nextStack[key] = struct{}{}
		nextSourceDirectory := destination
		if local {
			nextSourceDirectory = source
		} else {
			nextStack["local:"+destination] = struct{}{}
		}
		if err = i.installChildren(
			ctx, destination, nextSourceDirectory, address, nextStack, installed,
		); err != nil {
			return err
		}
	}
	return nil
}

func (i installer) install(
	ctx context.Context,
	source string,
	workingDirectory string,
	destination string,
	local bool,
	replaceCopiedDestination bool,
) (bool, error) {
	info, err := os.Stat(destination)
	switch {
	case err == nil && !info.IsDir():
		return false, fmt.Errorf("destination %q is not a directory", destination)
	case err == nil && !i.options.Upgrade && !replaceCopiedDestination:
		return false, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("inspect destination %q: %w", destination, err)
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		// note: untested because the parent was just created by Init; failure requires a concurrent filesystem mutation.
		return false, fmt.Errorf("create module parent directory: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".r42-module-")
	if err != nil {
		// note: untested because this requires a platform-specific permission or resource exhaustion failure.
		return false, fmt.Errorf("create module staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	staging := filepath.Join(temporary, "content")
	if local {
		if err = os.CopyFS(staging, os.DirFS(source)); err != nil {
			return false, fmt.Errorf("copy local module: %w", err)
		}
	} else {
		if err = os.Mkdir(staging, 0o700); err != nil {
			// note: untested because staging is a new path inside a private temporary directory.
			return false, fmt.Errorf("create fetch destination: %w", err)
		}
		fetch := i.options.Fetch
		if fetch == nil {
			fetch = Fetch
		}
		if err = fetch(ctx, source, staging, workingDirectory); err != nil {
			return false, err
		}
	}
	if info == nil {
		if err = os.Rename(staging, destination); err != nil {
			// note: untested because both paths share a freshly created parent filesystem.
			return false, fmt.Errorf("activate module: %w", err)
		}
		return true, nil
	}
	if err = replaceDirectory(staging, destination); err != nil {
		return false, err
	}
	return true, nil
}

func replaceDirectory(staging, destination string) error {
	backup, err := os.MkdirTemp(filepath.Dir(destination), ".r42-module-backup-")
	if err != nil {
		// note: untested because this requires a platform-specific permission or resource exhaustion failure.
		return fmt.Errorf("reserve module backup path: %w", err)
	}
	if err = os.Remove(backup); err != nil {
		// note: untested because backup is an empty directory created by this function.
		return fmt.Errorf("prepare module backup path: %w", err)
	}
	defer func() { _ = os.RemoveAll(backup) }()
	if err = os.Rename(destination, backup); err != nil {
		// note: untested because both paths are controlled siblings; failure requires a concurrent filesystem mutation.
		return fmt.Errorf("back up existing module: %w", err)
	}
	if err = os.Rename(staging, destination); err != nil {
		// note: untested because both paths are controlled siblings; failure requires a concurrent filesystem mutation.
		restoreErr := os.Rename(backup, destination)
		return errors.Join(fmt.Errorf("activate upgraded module: %w", err), restoreErr)
	}
	if err = os.RemoveAll(backup); err != nil {
		// note: untested because backup is owned by this process and no longer active.
		return fmt.Errorf("remove previous module: %w", err)
	}
	return nil
}

func moduleCalls(ctx context.Context, directory string) ([]moduleCall, error) {
	loaded, diagnostics, err := config.LoadDirectoryContext(ctx, directory)
	if err != nil {
		return nil, fmt.Errorf("read module directory %q: %w", directory, err)
	}
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	calls := make([]moduleCall, 0)
	seen := make(map[string]struct{})
	for _, block := range loaded.Blocks {
		if block.Type != "module" {
			continue
		}
		label, ok := moduleLabel(block.Labels)
		if !ok || !validPathLabel(label) {
			return nil, fmt.Errorf("module label is invalid")
		}
		for previous := range seen {
			if !strings.EqualFold(previous, label) {
				continue
			}
			if previous == label {
				return nil, fmt.Errorf("module %q is declared more than once", label)
			}
			return nil, fmt.Errorf("module %q conflicts with module %q", label, previous)
		}
		seen[label] = struct{}{}
		attribute, ok := block.Attributes()["source"]
		if !ok || len(attribute.Expr.Variables()) != 0 {
			return nil, fmt.Errorf("module source must be a literal string")
		}
		value, valueDiagnostics := attribute.Expr.Value(&hcl.EvalContext{})
		if valueDiagnostics.HasErrors() || value.IsNull() || !value.IsWhollyKnown() || !value.Type().Equals(cty.String) {
			return nil, fmt.Errorf("module source must be a literal string")
		}
		source := value.AsString()
		if strings.TrimSpace(source) == "" {
			return nil, fmt.Errorf("module source must not be empty")
		}
		calls = append(calls, moduleCall{label: label, source: source})
	}
	sort.Slice(calls, func(left, right int) bool { return calls[left].label < calls[right].label })
	return calls, nil
}

func moduleLabel(labels []string) (string, bool) {
	switch {
	case len(labels) == 1:
		return labels[0], true
	case len(labels) == 2 && labels[0] == "":
		return labels[1], true
	default:
		return "", false
	}
}

func resolveSource(directory, source string) (resolved, key string, local bool, err error) {
	if !IsLocalSource(source) {
		return source, "remote:" + source, false, nil
	}
	resolved = source
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(directory, resolved)
	}
	resolved, err = absoluteDirectory(resolved)
	if err != nil {
		return "", "", false, err
	}
	return resolved, "local:" + resolved, true, nil
}

func IsLocalSource(source string) bool {
	if filepath.IsAbs(source) || source == "." || source == ".." {
		return true
	}
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") ||
		strings.HasPrefix(source, `.\`) || strings.HasPrefix(source, `..\`)
}

func absoluteDirectory(path string) (string, error) {
	absolute, err := absolutePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return absolute, nil
}

func absolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		// note: untested because filepath.Abs fails only when the process working directory is unavailable.
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func validPathLabel(label string) bool {
	return label != "." && label != ".." && filepath.IsLocal(label) && !strings.ContainsAny(label, `/\`)
}

func cloneSet(source map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(source))
	for key := range source {
		result[key] = struct{}{}
	}
	return result
}

func Fetch(ctx context.Context, source, destination, workingDirectory string) error {
	client := getter.Client{DisableSymlinks: true}
	_, err := client.Get(ctx, &getter.Request{
		Src:             source,
		Dst:             destination,
		Pwd:             workingDirectory,
		Umask:           0o022,
		GetMode:         getter.ModeDir,
		Copy:            true,
		DisableSymlinks: true,
	})
	if err != nil {
		message := strings.ReplaceAll(err.Error(), source, "<redacted-module-source>")
		return fmt.Errorf("go-getter: %s", message)
	}
	return nil
}
