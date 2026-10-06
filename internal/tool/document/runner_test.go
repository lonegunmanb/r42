package document

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samber/oops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestRunnerDetectsMarkItDownOnlyAtStartup(t *testing.T) {
	t.Parallel()
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprint(installed), func(t *testing.T) {
			t.Parallel()
			calls := 0
			runner := newRunner(func(name string) (string, error) {
				calls++
				assert.Equal(t, "markitdown", name)
				if !installed {
					return "", exec.ErrNotFound
				}
				return "installed-markitdown", nil
			})
			assert.Equal(t, installed, runner.Available())
			assert.Equal(t, installed, runner.Available())
			assert.Equal(t, 1, calls)
		})
	}
}

func TestRunnerPassesAllMarkItDownOptions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		input Input
		flags []string
	}{
		{name: "format and plugins", input: Input{Filename: "report.pdf", Extension: ".pdf", MIMEType: "application/pdf", Charset: "utf-8", UsePlugins: true, KeepDataURIs: true}, flags: []string{"--extension", ".pdf", "--mime-type", "application/pdf", "--charset", "utf-8", "--use-plugins", "--keep-data-uris"}},
		{name: "document intelligence", input: Input{Filename: "report.pdf", UseDocIntel: true, Endpoint: "https://doc.example.test"}, flags: []string{"--use-docintel", "--endpoint", "https://doc.example.test"}},
		{name: "content understanding", input: Input{Filename: "report.pdf", UseCU: true, CUEndpoint: "https://cu.example.test", CUAnalyzer: "prebuilt-document", CUFileTypes: "pdf,jpeg"}, flags: []string{"--use-cu", "--cu-endpoint", "https://cu.example.test", "--cu-analyzer", "prebuilt-document", "--cu-file-types", "pdf,jpeg"}},
		{name: "content understanding stdin", input: Input{Stdin: "plain text", UseCU: true}, flags: []string{"--use-cu"}},
		{name: "output", input: Input{Filename: "report.pdf", Output: "report.md"}, flags: []string{"--output"}},
		{name: "stdin", input: Input{Stdin: "plain text", Extension: ".txt"}, flags: []string{"--extension", ".txt"}},
		{name: "version", input: Input{Version: true}, flags: []string{"--version"}},
		{name: "help", input: Input{Help: true}, flags: []string{"--help"}},
		{name: "plugins", input: Input{ListPlugins: true}, flags: []string{"--list-plugins"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.pdf"), []byte("document payload"), 0o600))
			runner := helperRunner("arguments")
			result, err := runner.Invoke(t.Context(), workspace, tt.input)
			require.NoError(t, err)
			var observed struct {
				Args    []string
				Payload string
			}
			require.NoError(t, json.Unmarshal([]byte(result.Content), &observed))
			for _, flag := range tt.flags {
				assert.Contains(t, observed.Args, flag)
			}
			if tt.input.Filename != "" {
				assert.Equal(t, "document payload", observed.Payload)
			}
			if tt.input.Stdin != "" {
				assert.Equal(t, tt.input.Stdin, observed.Payload)
			}
			if tt.input.Output != "" {
				content, readErr := os.ReadFile(filepath.Join(workspace, tt.input.Output))
				require.NoError(t, readErr)
				assert.Equal(t, result.Content, string(content))
				assert.Equal(t, tt.input.Output, result.Output)
			}
		})
	}
}

func TestRunnerReturnsRepairableDocumentErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		input Input
		mode  string
		want  string
	}{
		{name: "escape input", input: Input{Filename: "../secret.pdf"}, mode: "arguments", want: "local workspace path"},
		{name: "escape output", input: Input{Filename: "report.pdf", Output: "../secret.md"}, mode: "arguments", want: "local workspace path"},
		{name: "missing file", input: Input{Filename: "missing.pdf"}, mode: "arguments", want: "open document"},
		{name: "directory", input: Input{Filename: "folder.pdf"}, mode: "arguments", want: "regular file"},
		{name: "missing input", input: Input{}, mode: "arguments", want: "filename or stdin"},
		{name: "ambiguous input", input: Input{Filename: "report.pdf", Stdin: "text"}, mode: "arguments", want: "mutually exclusive"},
		{name: "cloud modes conflict", input: Input{Filename: "report.pdf", UseDocIntel: true, UseCU: true}, mode: "arguments", want: "mutually exclusive"},
		{name: "cloud requires filename", input: Input{Stdin: "text", UseDocIntel: true}, mode: "arguments", want: "requires filename"},
		{name: "missing program", input: Input{Filename: "report.pdf"}, mode: "unavailable", want: "markitdown is unavailable"},
		{name: "failure", input: Input{Filename: "report.pdf"}, mode: "fail", want: "conversion failed"},
		{name: "empty", input: Input{Filename: "report.pdf"}, mode: "empty", want: "empty output"},
		{name: "invalid encoding", input: Input{Filename: "report.pdf"}, mode: "invalid", want: "UTF-8"},
		{name: "too much output", input: Input{Filename: "report.pdf"}, mode: "large", want: "output exceeds"},
		{name: "output write failed", input: Input{Filename: "report.pdf", Output: "missing/report.md"}, mode: "arguments", want: "write document output"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.pdf"), []byte("payload"), 0o600))
			require.NoError(t, os.Mkdir(filepath.Join(workspace, "folder.pdf"), 0o700))
			result, err := helperRunner(tt.mode).Invoke(t.Context(), workspace, tt.input)
			require.ErrorContains(t, err, tt.want)
			var structured oops.OopsError
			require.ErrorAs(t, err, &structured)
			assert.Empty(t, result.Content)
			assert.Less(t, len(err.Error()), 5000)
		})
	}
}

func TestRunnerReportsMissingCommandOutput(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	_, err := helperRunner("missing-output").Invoke(t.Context(), workspace, Input{Stdin: "text", Output: "report.md"})
	require.ErrorContains(t, err, "read document output")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunnerRejectsOversizedStreamsWithoutPublishingOutput(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		stdin string
		mode  string
		want  string
	}{
		{name: "stdin", stdin: strings.Repeat("x", 64<<20+1), mode: "arguments", want: "document exceeds 64 MiB"},
		{name: "output file", stdin: "text", mode: "large", want: "document output exceeds 8 MiB"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			result, err := helperRunner(tt.mode).Invoke(t.Context(), workspace, Input{Stdin: tt.stdin, Output: "report.md"})
			require.ErrorContains(t, err, tt.want)
			assert.Empty(t, result)
			_, err = os.Stat(filepath.Join(workspace, "report.md"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestRunnerBoundsInputAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	file, err := os.Create(filepath.Join(workspace, "large.pdf"))
	require.NoError(t, err)
	require.NoError(t, file.Truncate(64<<20+1))
	require.NoError(t, file.Close())
	_, err = helperRunner("arguments").Invoke(t.Context(), workspace, Input{Filename: "large.pdf"})
	require.ErrorContains(t, err, "document exceeds")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = helperRunner("arguments").Invoke(ctx, workspace, Input{Version: true})
	require.ErrorIs(t, err, context.Canceled)
	_, err = helperRunner("arguments").Invoke(t.Context(), filepath.Join(workspace, "missing"), Input{Filename: "report.pdf"})
	assert.ErrorContains(t, err, "open document workspace")
}

func helperRunner(mode string) *Runner {
	runner := newRunner(func(string) (string, error) {
		if mode == "unavailable" {
			return "", exec.ErrNotFound
		}
		return os.Args[0], nil
	})
	runner.command = func(ctx context.Context, executable string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, executable, append([]string{"-test.run=^TestDocumentCommandProcess$", "--", "--r42-document-helper", mode}, args...)...)
	}
	return runner
}

func TestDocumentCommandProcess(t *testing.T) {
	t.Parallel()
	index := slices.Index(os.Args, "--r42-document-helper")
	if index < 0 {
		return
	}
	mode, args := os.Args[index+1], os.Args[index+2:]
	var payload []byte
	if len(args) > 0 && !strings.HasPrefix(args[len(args)-1], "--") {
		payload, _ = os.ReadFile(args[len(args)-1])
	}
	if len(payload) == 0 {
		payload, _ = io.ReadAll(os.Stdin)
	}
	var content []byte
	switch mode {
	case "fail":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("failure detail ", 1000))
		os.Exit(1)
	case "missing-output":
		os.Exit(0)
	case "empty":
		content = []byte(" \n")
	case "invalid":
		content = []byte{0xff}
	case "large":
		content = []byte(strings.Repeat("x", 8<<20+1))
	default:
		content, _ = json.Marshal(struct {
			Args    []string
			Payload string
		}{args, string(payload)})
	}
	outputIndex := slices.Index(args, "--output")
	if outputIndex >= 0 {
		_ = os.WriteFile(args[outputIndex+1], content, 0o600)
	} else {
		_, _ = os.Stdout.Write(content)
	}
	os.Exit(0)
}
