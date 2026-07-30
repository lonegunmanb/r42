package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	corespec "github.com/lonegunmanb/r42/internal/spec"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const (
	MaxStreamBytes       = 100 << 20
	ErrorStderrTailBytes = 64 << 10
)

var errStreamLimit = errors.New("stream limit exceeded")

type Config struct {
	Program    []string
	Workspace  string
	WorkingDir string
	Input      toolspec.Constraint
	Output     toolspec.Constraint
}

type Result struct {
	Accepted bool
	Output   *cty.Value
	Issues   []corespec.Issue
	Stderr   string
}

type Runner struct {
	maxStreamBytes int64
}

type ExecutionError struct {
	cause  error
	stdout string
	stderr string
}

func NewRunner() *Runner {
	return &Runner{maxStreamBytes: MaxStreamBytes}
}

func (e *ExecutionError) Error() string {
	tail := e.stderr
	if len(tail) > ErrorStderrTailBytes {
		tail = tail[len(tail)-ErrorStderrTailBytes:]
	}
	if tail == "" {
		return e.cause.Error()
	}
	return fmt.Sprintf("%v: %s", e.cause, tail)
}

func (e *ExecutionError) Unwrap() error {
	return e.cause
}

func (e *ExecutionError) Stderr() string {
	return e.stderr
}

func (e *ExecutionError) Stdout() string {
	return e.stdout
}

func (r *Runner) Run(ctx context.Context, config Config, input cty.Value) (Result, error) {
	if len(config.Program) == 0 || strings.TrimSpace(config.Program[0]) == "" {
		return Result{}, fmt.Errorf("external tool program must contain an executable")
	}
	if config.Workspace == "" {
		return Result{}, fmt.Errorf("external tool workspace is required")
	}

	validated, err := config.Input.Apply(input)
	if err != nil {
		return Result{}, fmt.Errorf("validating external tool input: %w", err)
	}
	validated, _ = validated.UnmarkDeep()
	encoded, err := ctyjson.Marshal(validated, config.Input.Type())
	if err != nil {
		return Result{}, fmt.Errorf("encoding external tool input: %w", err)
	}

	command := exec.CommandContext(ctx, config.Program[0], config.Program[1:]...)
	command.Dir = resolveWorkingDirectory(config.Workspace, config.WorkingDir)
	configureProcess(command)
	command.Cancel = func() error {
		return terminateProcessTree(command.Process)
	}
	command.Stdin = bytes.NewReader(encoded)

	stdout := newLimitedBuffer(r.maxStreamBytes, func() { _ = command.Cancel() })
	stderr := newLimitedBuffer(r.maxStreamBytes, func() { _ = command.Cancel() })
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()

	if stdout.exceeded {
		return Result{}, executionError("external tool stdout exceeded 100 MiB limit", stdout.String(), stderr.String())
	}
	if stderr.exceeded {
		return Result{}, executionError("external tool stderr exceeded 100 MiB limit", stdout.String(), stderr.String())
	}
	if err = ctx.Err(); err != nil {
		return Result{}, &ExecutionError{cause: err, stdout: stdout.String(), stderr: stderr.String()}
	}
	if runErr != nil {
		return Result{}, &ExecutionError{
			cause:  fmt.Errorf("running external tool: %w", runErr),
			stdout: stdout.String(), stderr: stderr.String(),
		}
	}

	result, err := decodeResponse(stdout.Bytes(), config.Output)
	if err != nil {
		return Result{}, &ExecutionError{cause: err, stdout: stdout.String(), stderr: stderr.String()}
	}
	result.Stderr = stderr.String()
	return result, nil
}

func resolveWorkingDirectory(workspace, workingDirectory string) string {
	if workingDirectory == "" {
		return workspace
	}
	if filepath.IsAbs(workingDirectory) {
		return workingDirectory
	}
	return filepath.Join(workspace, workingDirectory)
}

func decodeResponse(stdout []byte, output toolspec.Constraint) (Result, error) {
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var wire corespec.ToolResponse[json.RawMessage]
	if err := decoder.Decode(&wire); err != nil {
		return Result{}, fmt.Errorf("decoding external tool response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("decoding external tool response: expected exactly one JSON value")
	}
	if err := wire.Validate(); err != nil {
		return Result{}, fmt.Errorf("validating external tool response: %w", err)
	}

	result := Result{Accepted: wire.Accepted, Issues: wire.Issues}
	if wire.Output == nil {
		return result, nil
	}
	if err := validateJSON(*wire.Output, output.Type()); err != nil {
		return Result{}, fmt.Errorf("decoding external tool output: %w", err)
	}
	value, err := ctyjson.Unmarshal(*wire.Output, output.Type())
	if err != nil {
		return Result{}, fmt.Errorf("decoding external tool output: %w", err)
	}
	value, err = output.Apply(value)
	if err != nil {
		return Result{}, fmt.Errorf("validating external tool output: %w", err)
	}
	result.Output = &value
	return result, nil
}

func validateJSON(content []byte, expected cty.Type) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return validateJSONValue(value, expected, "output", false)
}

func validateJSONValue(value any, expected cty.Type, path string, nullable bool) error {
	if value == nil {
		if nullable {
			return nil
		}
		return fmt.Errorf("null is not allowed at %s", path)
	}
	switch {
	case expected.Equals(cty.String):
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string at %s", path)
		}
	case expected.Equals(cty.Number):
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("expected number at %s", path)
		}
	case expected.Equals(cty.Bool):
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean at %s", path)
		}
	case expected.IsListType(), expected.IsSetType():
		return validateJSONArray(value, expected.ElementType(), nil, path)
	case expected.IsTupleType():
		return validateJSONArray(value, cty.NilType, expected.TupleElementTypes(), path)
	case expected.IsMapType():
		return validateJSONObject(value, expected.ElementType(), cty.NilType, path)
	case expected.IsObjectType():
		return validateJSONObject(value, cty.NilType, expected, path)
	}
	return nil
}

func validateJSONArray(value any, elementType cty.Type, tupleTypes []cty.Type, path string) error {
	elements, ok := value.([]any)
	if !ok {
		return fmt.Errorf("expected array at %s", path)
	}
	if tupleTypes != nil && len(elements) != len(tupleTypes) {
		return fmt.Errorf("expected %d elements at %s", len(tupleTypes), path)
	}
	for index, element := range elements {
		expected := elementType
		if tupleTypes != nil {
			expected = tupleTypes[index]
		}
		if err := validateJSONValue(element, expected, fmt.Sprintf("%s[%d]", path, index), false); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONObject(value any, elementType, objectType cty.Type, path string) error {
	attributes, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("expected object at %s", path)
	}
	if !objectType.Equals(cty.NilType) {
		for name := range attributes {
			if !objectType.HasAttribute(name) {
				return fmt.Errorf("undeclared attribute at %s.%s", path, name)
			}
		}
		for name, attributeType := range objectType.AttributeTypes() {
			attribute, exists := attributes[name]
			if !exists {
				if objectType.AttributeOptional(name) {
					continue
				}
				return fmt.Errorf("required attribute is missing at %s.%s", path, name)
			}
			if err := validateJSONValue(attribute, attributeType, path+"."+name, objectType.AttributeOptional(name)); err != nil {
				return err
			}
		}
		return nil
	}
	for name, element := range attributes {
		if err := validateJSONValue(element, elementType, path+"."+name, false); err != nil {
			return err
		}
	}
	return nil
}

func executionError(message, stdout, stderr string) error {
	return &ExecutionError{cause: errors.New(message), stdout: stdout, stderr: stderr}
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
	onLimit  func()
}

func newLimitedBuffer(limit int64, onLimit func()) *limitedBuffer {
	return &limitedBuffer{limit: limit, onLimit: onLimit}
}

func (b *limitedBuffer) Write(content []byte) (int, error) {
	remaining := b.limit - int64(b.buffer.Len())
	if int64(len(content)) <= remaining {
		return b.buffer.Write(content)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(content[:remaining])
	}
	b.exceeded = true
	b.onLimit()
	return int(max(remaining, 0)), errStreamLimit
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
