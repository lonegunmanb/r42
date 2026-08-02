package module_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	module "github.com/lonegunmanb/r42/internal/module"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModuleDirectoryUsesCanonicalModuleAddress(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "modules")
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{name: "root child", address: "module.a", want: filepath.Join(root, "a")},
		{name: "nested child", address: "module.a.module.b.module.c", want: filepath.Join(root, "a", "b", "c")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := module.Directory(root, test.address)

			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestModuleDirectoryRejectsInvalidAddresses(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"", "research.a", "module", "module.a.module", "module...module.b"} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()

			_, err := module.Directory(t.TempDir(), address)

			require.Error(t, err)
			assert.ErrorContains(t, err, "module address")
		})
	}
}

func TestInitCopiesNestedLocalModulesToCanonicalDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	child := filepath.Join(root, "child")
	grandchild := filepath.Join(child, "grandchild")
	require.NoError(t, os.MkdirAll(grandchild, 0o700))
	writeConfig(t, root, `module "a" { source = "./child" }`)
	writeConfig(t, child, `module "b" { source = "./grandchild" }`)
	writeConfig(t, grandchild, `output "answer" { value = "42" }`)

	err := module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules})

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(modules, "a", "main.r42.hcl"))
	assert.FileExists(t, filepath.Join(modules, "a", "b", "main.r42.hcl"))
}

func TestInitResolvesNestedLocalSourcesFromOriginalModuleDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	parent := filepath.Join(root, "parent")
	shared := filepath.Join(root, "shared")
	require.NoError(t, os.Mkdir(parent, 0o700))
	require.NoError(t, os.Mkdir(shared, 0o700))
	writeConfig(t, root, `module "parent" { source = "./parent" }`)
	writeConfig(t, parent, `module "nested" { source = "../shared" }`)
	writeConfig(t, shared, `output "answer" { value = "42" }`)

	err := module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules})

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(modules, "parent", "nested", "main.r42.hcl"))
}

func TestInitReplacesCopiedDirectoryThatConflictsWithNestedModuleLabel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	parent := filepath.Join(root, "parent")
	conflicting := filepath.Join(parent, "nested")
	shared := filepath.Join(root, "shared")
	require.NoError(t, os.MkdirAll(conflicting, 0o700))
	require.NoError(t, os.Mkdir(shared, 0o700))
	writeConfig(t, root, `module "parent" { source = "./parent" }`)
	writeConfig(t, parent, `module "nested" { source = "../shared" }`)
	writeConfig(t, conflicting, `output "origin" { value = "copied parent directory" }`)
	writeConfig(t, shared, `output "origin" { value = "declared module source" }`)

	err := module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules})

	require.NoError(t, err)
	installed, err := os.ReadFile(filepath.Join(modules, "parent", "nested", "main.r42.hcl"))
	require.NoError(t, err)
	assert.Equal(t, "output \"origin\" { value = \"declared module source\" }\n", string(installed))
}

func TestInitRejectsNestedLocalSourceCycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	writeConfig(t, root, `module "child" { source = "./child" }`)
	writeConfig(t, child, `module "root" { source = ".." }`)

	err := module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules})

	require.Error(t, err)
	assert.ErrorContains(t, err, "module source cycle")
}

func TestInitUsesFetcherForNonLocalSources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	writeConfig(t, root, `module "remote" { source = "example.com/acme/research" }`)
	var gotSource string
	var gotWorkingDirectory string
	fetch := func(_ context.Context, source, destination, workingDirectory string) error {
		gotSource = source
		gotWorkingDirectory = workingDirectory
		return os.WriteFile(filepath.Join(destination, "main.r42.hcl"), []byte(`output "answer" { value = "remote" }`), 0o600)
	}

	err := module.Init(t.Context(), root, module.InitOptions{
		ModulesDirectory: modules,
		Fetch:            fetch,
	})

	require.NoError(t, err)
	assert.Equal(t, "example.com/acme/research", gotSource)
	assert.Equal(t, root, gotWorkingDirectory)
	assert.FileExists(t, filepath.Join(modules, "remote", "main.r42.hcl"))
}

func TestInitDownloadsRemoteModuleWithGoGetter(t *testing.T) {
	t.Parallel()

	archive := newModuleArchive(t, `output "answer" { value = "remote" }`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/zip")
		_, _ = response.Write(archive)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	writeConfig(t, root, `module "remote" { source = "`+server.URL+`/module.zip" }`)

	err := module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules})

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(modules, "remote", "main.r42.hcl"))
}

func TestInitReusesModulesUnlessUpgradeIsSet(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	writeConfig(t, root, `module "child" { source = "./child" }`)
	writeConfig(t, child, `output "version" { value = "one" }`)
	require.NoError(t, module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules}))

	writeConfig(t, child, `output "version" { value = "two" }`)
	require.NoError(t, module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules}))
	installed, err := os.ReadFile(filepath.Join(modules, "child", "main.r42.hcl"))
	require.NoError(t, err)
	assert.Contains(t, string(installed), `value = "one"`)

	require.NoError(t, module.Init(t.Context(), root, module.InitOptions{
		ModulesDirectory: modules,
		Upgrade:          true,
	}))
	installed, err = os.ReadFile(filepath.Join(modules, "child", "main.r42.hcl"))
	require.NoError(t, err)
	assert.Contains(t, string(installed), `value = "two"`)
}

func TestInitPreservesInstalledModuleWhenUpgradeFetchFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	installed := filepath.Join(modules, "remote")
	require.NoError(t, os.MkdirAll(installed, 0o700))
	writeConfig(t, root, `module "remote" { source = "example.com/acme/research" }`)
	writeConfig(t, installed, `output "version" { value = "existing" }`)

	err := module.Init(t.Context(), root, module.InitOptions{
		ModulesDirectory: modules,
		Upgrade:          true,
		Fetch: func(context.Context, string, string, string) error {
			return errors.New("download failed")
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "download failed")
	installedSource, readErr := os.ReadFile(filepath.Join(installed, "main.r42.hcl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(installedSource), "existing")
}

func TestInitPreservesInstalledTreeWhenNestedUpgradeFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "modules")
	parent := filepath.Join(root, "parent")
	require.NoError(t, os.Mkdir(parent, 0o700))
	writeConfig(t, root, `module "parent" { source = "./parent" }`)
	writeConfig(t, parent, `output "version" { value = "existing" }`)
	require.NoError(t, module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules}))

	writeConfig(t, parent, `
module "nested" { source = "example.com/acme/research" }
output "version" { value = "replacement" }
`)
	err := module.Init(t.Context(), root, module.InitOptions{
		ModulesDirectory: modules,
		Upgrade:          true,
		Fetch: func(context.Context, string, string, string) error {
			return errors.New("nested download failed")
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "nested download failed")
	installedSource, readErr := os.ReadFile(filepath.Join(modules, "parent", "main.r42.hcl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(installedSource), "existing")
	assert.NotContains(t, string(installedSource), "replacement")
}

func TestInitRejectsInvalidModuleDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		wantError string
	}{
		{name: "missing source", source: `module "child" {}`, wantError: "module source must be a literal string"},
		{name: "wrong source type", source: `module "child" { source = 42 }`, wantError: "module source must be a literal string"},
		{name: "unsafe label", source: `module "../child" { source = "./child" }`, wantError: "module label is invalid"},
		{
			name: "duplicate label",
			source: `
module "child" { source = "./first" }
module "child" { source = "./second" }
`,
			wantError: `module "child" is declared more than once`,
		},
		{
			name: "case-insensitive label collision",
			source: `
module "Child" { source = "./first" }
module "child" { source = "./second" }
`,
			wantError: `module "child" conflicts with module "Child"`,
		},
		{name: "source cycle", source: `module "self" { source = "." }`, wantError: "module source cycle"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeConfig(t, root, test.source)
			err := module.Init(t.Context(), root, module.InitOptions{
				ModulesDirectory: filepath.Join(t.TempDir(), "modules"),
			})

			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestInitRejectsFileAtModuleDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	modules := filepath.Join(t.TempDir(), "modules")
	require.NoError(t, os.Mkdir(child, 0o700))
	require.NoError(t, os.MkdirAll(modules, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(modules, "child"), []byte("not a directory"), 0o600))
	writeConfig(t, child, `output "answer" { value = "42" }`)
	writeConfig(t, root, `module "child" { source = "./child" }`)

	err := module.Init(t.Context(), root, module.InitOptions{ModulesDirectory: modules})

	require.Error(t, err)
	assert.ErrorContains(t, err, "is not a directory")
}

func TestInitRejectsComputedModuleSource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
locals { child_source = "./child" }
module "child" { source = local.child_source }
`)

	err := module.Init(t.Context(), root, module.InitOptions{
		ModulesDirectory: filepath.Join(t.TempDir(), "modules"),
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "module source must be a literal string")
}

func TestInitRejectsInvalidRootAndModulesDirectories(t *testing.T) {
	t.Parallel()

	rootFile := filepath.Join(t.TempDir(), "root.r42.hcl")
	require.NoError(t, os.WriteFile(rootFile, []byte(""), 0o600))
	blockedModules := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(blockedModules, []byte(""), 0o600))
	tests := []struct {
		name      string
		root      string
		modules   string
		wantError string
	}{
		{
			name: "missing root", root: filepath.Join(t.TempDir(), "missing"),
			modules: filepath.Join(t.TempDir(), "modules"), wantError: "read root module",
		},
		{
			name: "root is a file", root: rootFile,
			modules: filepath.Join(t.TempDir(), "modules"), wantError: "is not a directory",
		},
		{name: "empty modules path", root: t.TempDir(), modules: "", wantError: "path is required"},
		{
			name: "modules path is a file", root: t.TempDir(), modules: blockedModules,
			wantError: "create modules directory",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := module.Init(t.Context(), test.root, module.InitOptions{ModulesDirectory: test.modules})

			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestInitRejectsInvalidConfigurationAndUnavailableSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		wantError string
	}{
		{name: "invalid HCL", source: `module "child" {`, wantError: "Unclosed configuration block"},
		{name: "empty source", source: `module "child" { source = "" }`, wantError: "module source must not be empty"},
		{
			name: "missing local source", source: `module "child" { source = "./missing" }`,
			wantError: "module module.child",
		},
		{
			name: "unsupported remote source", source: `module "child" { source = "unknown::module" }`,
			wantError: "go-getter",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeConfig(t, root, test.source)
			err := module.Init(t.Context(), root, module.InitOptions{
				ModulesDirectory: filepath.Join(t.TempDir(), "modules"),
			})

			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestInitRedactsCredentialsFromGoGetterErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
module "remote" {
  source = "unknown::https://user:secret-password@example.test/module?token=secret-query"
}
`)

	err := module.Init(t.Context(), root, module.InitOptions{
		ModulesDirectory: filepath.Join(t.TempDir(), "modules"),
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-password")
	assert.NotContains(t, err.Error(), "secret-query")
}

func writeConfig(t *testing.T, directory, source string) {
	t.Helper()
	require.NoError(t, os.WriteFile(
		filepath.Join(directory, "main.r42.hcl"),
		[]byte(source+"\n"),
		0o600,
	))
}

func newModuleArchive(t *testing.T, source string) []byte {
	t.Helper()

	var encoded bytes.Buffer
	archive := zip.NewWriter(&encoded)
	file, err := archive.Create("main.r42.hcl")
	require.NoError(t, err)
	_, err = file.Write([]byte(source + "\n"))
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	return encoded.Bytes()
}
