package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samber/oops"
)

const (
	maxInputBytes  = 64 << 20
	maxOutputBytes = 8 << 20
)

// Input exposes the MarkItDown CLI options and optional text supplied on stdin.
type Input struct {
	Filename     string `json:"filename,omitempty"`
	Output       string `json:"output,omitempty"`
	Extension    string `json:"extension,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	Charset      string `json:"charset,omitempty"`
	UseDocIntel  bool   `json:"use_docintel,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	UseCU        bool   `json:"use_cu,omitempty"`
	CUEndpoint   string `json:"cu_endpoint,omitempty"`
	CUAnalyzer   string `json:"cu_analyzer,omitempty"`
	CUFileTypes  string `json:"cu_file_types,omitempty"`
	UsePlugins   bool   `json:"use_plugins,omitempty"`
	ListPlugins  bool   `json:"list_plugins,omitempty"`
	KeepDataURIs bool   `json:"keep_data_uris,omitempty"`
	Version      bool   `json:"version,omitempty"`
	Help         bool   `json:"help,omitempty"`
	Stdin        string `json:"stdin,omitempty"`
}

// Result contains complete CLI text and the optional workspace output path.
type Result struct {
	Content string `json:"content"`
	Output  string `json:"output,omitempty"`
}

// Runner retains the executable detected at startup; it never polls PATH.
type Runner struct {
	executable string
	command    func(context.Context, string, ...string) *exec.Cmd
}

func NewRunner() *Runner { return newRunner(exec.LookPath) }

func newRunner(lookup func(string) (string, error)) *Runner {
	executable, err := lookup("markitdown")
	if err != nil {
		executable = ""
	}
	return &Runner{executable: executable, command: exec.CommandContext}
}

// Available reports the startup detection result.
func (r *Runner) Available() bool { return r.executable != "" }

// Invoke runs MarkItDown with typed options and confines file I/O to workspace.
func (r *Runner) Invoke(ctx context.Context, workspace string, input Input) (result Result, err error) {
	defer func() {
		if err != nil {
			err = oops.In("document").With("filename", input.Filename).Wrap(err)
		}
	}()
	if !r.Available() {
		return Result{}, fmt.Errorf("markitdown is unavailable")
	}
	if input.UseDocIntel && input.UseCU {
		return Result{}, fmt.Errorf("use_docintel and use_cu are mutually exclusive")
	}
	metadata := input.Help || input.Version || input.ListPlugins
	if !metadata {
		if input.Filename == "" && input.Stdin == "" {
			return Result{}, fmt.Errorf("provide filename or stdin")
		}
		if input.Filename != "" && input.Stdin != "" {
			return Result{}, fmt.Errorf("filename and stdin are mutually exclusive")
		}
		if input.UseDocIntel && input.Filename == "" {
			return Result{}, fmt.Errorf("cloud conversion requires filename")
		}
	}
	for _, path := range []string{input.Filename, input.Output} {
		if path != "" && !filepath.IsLocal(path) {
			return Result{}, fmt.Errorf("document input and output must use a local workspace path")
		}
	}
	if len(input.Stdin) > maxInputBytes {
		return Result{}, fmt.Errorf("document exceeds 64 MiB")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return Result{}, fmt.Errorf("open document workspace: %w", err)
	}
	defer func() { _ = root.Close() }()
	temporary, err := os.MkdirTemp("", "r42-document-")
	if err != nil {
		// note: untested because this requires failure of the operating system temporary directory.
		return Result{}, fmt.Errorf("create document temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	filename := ""
	if input.Filename != "" && !metadata {
		filename, err = stageDocument(root, input.Filename, temporary)
		if err != nil {
			return Result{}, err
		}
	}
	outputPath := ""
	if input.Output != "" {
		outputPath = filepath.Join(temporary, "output.md")
	}
	args := markItDownArgs(input, outputPath)
	if filename != "" {
		args = append(args, filename)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := r.command(ctx, r.executable, args...)
	command.Dir = workspace
	command.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	command.Stdin = strings.NewReader(input.Stdin)
	output := boundedOutput{limit: maxOutputBytes}
	diagnostic := boundedOutput{limit: 4096}
	command.Stdout, command.Stderr = &output, &diagnostic
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("document conversion failed: %w", ctx.Err())
		}
		return Result{}, fmt.Errorf("document conversion failed: %w: %s", err, diagnostic.buffer.String())
	}
	content := output.buffer.Bytes()
	if outputPath != "" && !metadata {
		file, openErr := os.Open(outputPath)
		if openErr != nil {
			return Result{}, fmt.Errorf("read document output: %w", openErr)
		}
		defer func() { _ = file.Close() }()
		content, err = io.ReadAll(io.LimitReader(file, maxOutputBytes+1))
		if err != nil {
			// note: untested because this requires an I/O failure after opening the temporary output file.
			return Result{}, fmt.Errorf("read document output: %w", err)
		}
	}
	if output.truncated || len(content) > maxOutputBytes {
		return Result{}, fmt.Errorf("document output exceeds 8 MiB")
	}
	if !utf8.Valid(content) {
		return Result{}, fmt.Errorf("document output is not UTF-8")
	}
	if strings.TrimSpace(string(content)) == "" {
		return Result{}, fmt.Errorf("document conversion returned empty output")
	}
	result = Result{Content: string(content)}
	if outputPath != "" && !metadata {
		if err = root.WriteFile(input.Output, content, 0o600); err != nil {
			return Result{}, fmt.Errorf("write document output: %w", err)
		}
		result.Output = input.Output
	}
	return result, nil
}

func stageDocument(root *os.Root, path, temporary string) (string, error) {
	file, err := root.Open(path)
	if err != nil {
		return "", fmt.Errorf("open document: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		// note: untested because this requires failure of Stat on an already open regular file.
		return "", fmt.Errorf("stat document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("document must be a regular file")
	}
	if info.Size() > maxInputBytes {
		return "", fmt.Errorf("document exceeds 64 MiB")
	}
	stagedPath := filepath.Join(temporary, filepath.Base(path))
	staged, err := os.Create(stagedPath)
	if err != nil {
		// note: untested because this requires failure to create a file in a newly allocated temporary directory.
		return "", fmt.Errorf("stage document: %w", err)
	}
	written, copyErr := io.Copy(staged, io.LimitReader(file, maxInputBytes+1))
	closeErr := staged.Close()
	if copyErr != nil || closeErr != nil {
		// note: untested because this requires a disk I/O failure while staging an open document.
		return "", fmt.Errorf("stage document: %w", errors.Join(copyErr, closeErr))
	}
	if written > maxInputBytes {
		// note: untested because this requires the source file to grow concurrently after Stat.
		return "", fmt.Errorf("document exceeds 64 MiB")
	}
	return stagedPath, nil
}

func markItDownArgs(input Input, output string) []string {
	args := make([]string, 0, 24)
	for _, option := range []struct{ flag, value string }{
		{"--output", output},
		{"--extension", input.Extension},
		{"--mime-type", input.MIMEType},
		{"--charset", input.Charset},
		{"--endpoint", input.Endpoint},
		{"--cu-endpoint", input.CUEndpoint},
		{"--cu-analyzer", input.CUAnalyzer},
		{"--cu-file-types", input.CUFileTypes},
	} {
		if option.value != "" {
			args = append(args, option.flag, option.value)
		}
	}
	for _, option := range []struct {
		flag    string
		enabled bool
	}{
		{"--use-docintel", input.UseDocIntel},
		{"--use-cu", input.UseCU},
		{"--use-plugins", input.UsePlugins},
		{"--list-plugins", input.ListPlugins},
		{"--keep-data-uris", input.KeepDataURIs},
		{"--version", input.Version},
		{"--help", input.Help},
	} {
		if option.enabled {
			args = append(args, option.flag)
		}
	}
	return args
}

type boundedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(payload []byte) (int, error) {
	length := len(payload)
	remaining := b.limit - b.buffer.Len()
	if length > remaining {
		payload = payload[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(payload)
	return length, nil
}
