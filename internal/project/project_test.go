package project

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitCopiesConfigurationAndInstallsModules(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, source, "main.r42.hcl", `
module "child" { source = "./child" }
output "answer" { value = module.child.answer }
`)
	writeProjectFile(t, source, "child/main.r42.hcl", `output "answer" { value = "42" }`)
	writeProjectFile(t, source, "skills/check/SKILL.md", "# Check\n")
	writeProjectFile(t, source, ".git/config", "ignored")
	writeProjectFile(t, source, ".r42/runs/old/events.jsonl", "ignored")

	err := Init(t.Context(), source, state, InitOptions{})

	require.NoError(t, err)
	configDirectory, modulesDirectory, err := Open(state)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(configDirectory, "main.r42.hcl"))
	assert.FileExists(t, filepath.Join(configDirectory, "skills", "check", "SKILL.md"))
	assert.NoDirExists(t, filepath.Join(configDirectory, ".git"))
	assert.NoDirExists(t, filepath.Join(configDirectory, ".r42"))
	assert.FileExists(t, filepath.Join(modulesDirectory, "child", "main.r42.hcl"))
}

func TestInitFetchesRemoteConfigurationSource(t *testing.T) {
	t.Parallel()

	const source = "git::https://example.invalid/research.git//r42?ref=v1"
	state := filepath.Join(t.TempDir(), ".r42")
	var fetchedSource string
	var fetchWorkingDirectory string
	fetch := func(_ context.Context, actualSource, destination, workingDirectory string) error {
		fetchedSource = actualSource
		fetchWorkingDirectory = workingDirectory
		writeProjectFile(t, destination, "main.r42.hcl", `module "child" { source = "./child" }`)
		writeProjectFile(t, destination, "child/main.r42.hcl", `output "answer" { value = "42" }`)
		return nil
	}

	err := Init(t.Context(), source, state, InitOptions{Fetch: fetch})

	require.NoError(t, err)
	assert.Equal(t, source, fetchedSource)
	assert.NotEmpty(t, fetchWorkingDirectory)
	configDirectory, modulesDirectory, err := Open(state)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(configDirectory, "main.r42.hcl"))
	assert.FileExists(t, filepath.Join(modulesDirectory, "child", "main.r42.hcl"))
	encodedMarker, err := os.ReadFile(filepath.Join(configDirectory, markerFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(encodedMarker), source)
	assert.Contains(t, string(encodedMarker), `"source_key"`)
}

func TestInitDownloadsRemoteConfigurationWithGoGetter(t *testing.T) {
	t.Parallel()

	archive := newProjectArchive(t, `output "answer" { value = "remote" }`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/zip")
		_, _ = response.Write(archive)
	}))
	t.Cleanup(server.Close)
	state := filepath.Join(t.TempDir(), ".r42")

	err := Init(t.Context(), server.URL+"/research.zip", state, InitOptions{})

	require.NoError(t, err)
	configDirectory, _, err := Open(state)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(configDirectory, "main.r42.hcl"))
}

func TestInitRemoteFetchFailurePreservesActiveProjectAndRedactsSource(t *testing.T) {
	t.Parallel()

	localSource := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, localSource, "main.r42.hcl", `output "version" { value = "one" }`)
	require.NoError(t, Init(t.Context(), localSource, state, InitOptions{}))
	const remoteSource = "https://user:secret@example.invalid/research.tar.gz?token=sensitive"

	err := Init(t.Context(), remoteSource, state, InitOptions{
		Fetch: func(context.Context, string, string, string) error {
			return errors.New("download failed for " + remoteSource)
		},
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "sensitive")
	configDirectory, _, openErr := Open(state)
	require.NoError(t, openErr)
	content, readErr := os.ReadFile(filepath.Join(configDirectory, "main.r42.hcl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(content), `value = "one"`)
}

func TestInitRemoteFetchPreservesContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := Init(ctx, "https://example.invalid/research.zip", filepath.Join(t.TempDir(), ".r42"), InitOptions{
		Fetch: func(ctx context.Context, _, _, _ string) error {
			return ctx.Err()
		},
	})

	require.ErrorIs(t, err, context.Canceled)
}

func TestInitRefreshesConfigurationSnapshot(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, source, "main.r42.hcl", `output "version" { value = "one" }`)
	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))

	writeProjectFile(t, source, "main.r42.hcl", `output "version" { value = "two" }`)
	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))

	configDirectory, _, err := Open(state)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(configDirectory, "main.r42.hcl"))
	require.NoError(t, err)
	assert.Contains(t, string(content), `value = "two"`)
}

func TestInitSwitchingConfigurationSourceRefreshesModules(t *testing.T) {
	t.Parallel()

	first := t.TempDir()
	second := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, first, "main.r42.hcl", `module "child" { source = "./child" }`)
	writeProjectFile(t, first, "child/main.r42.hcl", `output "version" { value = "one" }`)
	writeProjectFile(t, second, "main.r42.hcl", `module "child" { source = "./child" }`)
	writeProjectFile(t, second, "child/main.r42.hcl", `output "version" { value = "two" }`)
	require.NoError(t, Init(t.Context(), first, state, InitOptions{}))

	require.NoError(t, Init(t.Context(), second, state, InitOptions{}))

	_, modulesDirectory, err := Open(state)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(modulesDirectory, "child", "main.r42.hcl"))
	require.NoError(t, err)
	assert.Contains(t, string(content), `value = "two"`)
}

func TestInitFailurePreservesPreviousConfigurationSnapshot(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, source, "main.r42.hcl", `output "version" { value = "one" }`)
	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))

	writeProjectFile(t, source, "main.r42.hcl", `module "broken" { source = "./missing" }`)
	err := Init(t.Context(), source, state, InitOptions{})

	require.Error(t, err)
	configDirectory, _, openErr := Open(state)
	require.NoError(t, openErr)
	content, readErr := os.ReadFile(filepath.Join(configDirectory, "main.r42.hcl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(content), `value = "one"`)
}

func TestInitFromWorkingDirectoryDoesNotCopyStateDirectory(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	state := filepath.Join(source, ".r42")
	writeProjectFile(t, source, "main.r42.hcl", `output "answer" { value = "42" }`)
	writeProjectFile(t, source, ".r42/old.txt", "old state")

	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))

	configDirectory, _, err := Open(state)
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(configDirectory, ".r42"))
}

func TestOpenRejectsIncompleteInitialization(t *testing.T) {
	t.Parallel()

	state := filepath.Join(t.TempDir(), ".r42")
	require.NoError(t, os.MkdirAll(filepath.Join(state, "config"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "modules"), 0o700))

	_, _, err := Open(state)

	require.Error(t, err)
	assert.ErrorContains(t, err, "run r42 init")
}

func TestOpenRejectsInterruptedInitialization(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, source, "main.r42.hcl", `output "answer" { value = "42" }`)
	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))
	require.NoError(t, os.WriteFile(filepath.Join(state, transactionFileName), []byte("interrupted\n"), 0o600))

	_, _, err := Open(state)

	require.Error(t, err)
	require.ErrorContains(t, err, "initialization was interrupted")
	require.ErrorContains(t, err, "run r42 init")
}

func TestInitRecoversInterruptedInitialization(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	state := filepath.Join(t.TempDir(), ".r42")
	writeProjectFile(t, source, "main.r42.hcl", `module "child" { source = "./child" }`)
	writeProjectFile(t, source, "child/main.r42.hcl", `output "version" { value = "one" }`)
	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))

	writeProjectFile(t, source, "child/main.r42.hcl", `output "version" { value = "two" }`)
	require.NoError(t, os.WriteFile(filepath.Join(state, transactionFileName), []byte("interrupted\n"), 0o600))
	require.NoError(t, Init(t.Context(), source, state, InitOptions{}))

	_, modulesDirectory, err := Open(state)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(modulesDirectory, "child", "main.r42.hcl"))
	require.NoError(t, err)
	assert.Contains(t, string(content), `value = "two"`)
	assert.NoFileExists(t, filepath.Join(state, transactionFileName))
}

func TestInitRejectsConfigurationInsideStateDirectory(t *testing.T) {
	t.Parallel()

	state := filepath.Join(t.TempDir(), ".r42")
	source := filepath.Join(state, "source")
	writeProjectFile(t, source, "main.r42.hcl", `output "answer" { value = "42" }`)

	err := Init(t.Context(), source, state, InitOptions{})

	require.Error(t, err)
	assert.ErrorContains(t, err, "must be outside")
}

func writeProjectFile(t *testing.T, root, relativePath, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content+"\n"), 0o600))
}

func newProjectArchive(t *testing.T, source string) []byte {
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
