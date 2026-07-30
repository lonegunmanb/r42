package gotool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	corespec "github.com/lonegunmanb/r42/internal/spec"
)

type Compiler struct {
	mu        sync.Mutex
	directory string
	programs  map[string]*Program
	removeAll func(string) error
	closed    bool
}

type Program struct {
	path     string
	analysis Analysis
}

type Response struct {
	Accepted bool             `json:"accepted"`
	Output   *json.RawMessage `json:"output,omitempty"`
	Issues   []corespec.Issue `json:"issues,omitempty"`
	Stderr   string           `json:"-"`
}

func NewCompiler() (*Compiler, error) {
	directory, err := os.MkdirTemp("", "r42-go-tools-")
	if err != nil {
		return nil, fmt.Errorf("creating inline Go tool directory: %w", err)
	}
	return &Compiler{
		directory: directory,
		programs:  make(map[string]*Program),
		removeAll: os.RemoveAll,
	}, nil
}

func (c *Compiler) Compile(ctx context.Context, source string) (*Program, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("inline Go compiler is closed")
	}

	key := sourceKey(source)
	if cached, ok := c.programs[key]; ok {
		return cached, nil
	}
	analysis, err := Analyze(source)
	if err != nil {
		return nil, err
	}

	buildDirectory := filepath.Join(c.directory, key)
	if err = os.Mkdir(buildDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("creating inline Go build directory: %w", err)
	}
	sourcePath := filepath.Join(buildDirectory, "main.go")
	if err = os.WriteFile(sourcePath, []byte(wrapperSource(source)), 0o600); err != nil {
		_ = c.removeAll(buildDirectory)
		return nil, fmt.Errorf("writing inline Go wrapper: %w", err)
	}
	executable := filepath.Join(buildDirectory, executableName())
	command := exec.CommandContext(ctx, "go", "build", "-o", executable, sourcePath)
	command.Dir = buildDirectory
	output, err := command.CombinedOutput()
	if err != nil {
		_ = c.removeAll(buildDirectory)
		return nil, fmt.Errorf("compiling inline Go tool: %w: %s", err, strings.TrimSpace(string(output)))
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		_ = c.removeAll(buildDirectory)
		return nil, fmt.Errorf("resolving inline Go executable: %w", err)
	}
	program := &Program{path: absolute, analysis: analysis}
	c.programs[key] = program
	return program, nil
}

func (c *Compiler) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if err := c.removeAll(c.directory); err != nil {
		return fmt.Errorf("removing inline Go tool directory: %w", err)
	}
	c.closed = true
	c.programs = nil
	return nil
}

func (p *Program) Path() string {
	return p.path
}

func (p *Program) Analysis() Analysis {
	return p.analysis
}

func (p *Program) Invoke(ctx context.Context, input json.RawMessage) (Response, error) {
	command := exec.CommandContext(ctx, p.path)
	command.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return Response{}, fmt.Errorf("running inline Go tool: %w", contextError)
		}
		return Response{}, fmt.Errorf("running inline Go tool: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	decoder := json.NewDecoder(&stdout)
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decoding inline Go tool response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Response{}, fmt.Errorf("decoding inline Go tool response: expected exactly one JSON value")
	}
	validation := corespec.ToolResponse[json.RawMessage]{
		Accepted: response.Accepted,
		Output:   response.Output,
		Issues:   response.Issues,
	}
	if err := validation.Validate(); err != nil {
		return Response{}, fmt.Errorf("validating inline Go tool response: %w", err)
	}
	response.Stderr = stderr.String()
	return response, nil
}

func sourceKey(source string) string {
	digest := sha256.Sum256([]byte(runtime.Version() + "\x00" + source))
	return hex.EncodeToString(digest[:])
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "tool.exe"
	}
	return "tool"
}
