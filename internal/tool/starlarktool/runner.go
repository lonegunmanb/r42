package starlarktool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const maxWorkerResponseBytes = 32 << 20

const workerTimeoutGracePeriod = time.Second

// Runner invokes the current r42 executable as a fresh calculator worker.
type Runner struct {
	executable  func() (string, error)
	arguments   []string
	environment []string
}

// NewRunner constructs a worker runner for the current r42 executable.
func NewRunner() *Runner {
	return &Runner{
		executable: os.Executable,
		arguments:  []string{"--internal-starlark-worker"},
	}
}

// Run sends one request to an isolated worker and returns its single response.
func (r *Runner) Run(ctx context.Context, request WorkerRequest) (WorkerResponse, error) {
	if r == nil || r.executable == nil {
		return WorkerResponse{}, errors.New("starlark worker runner is not configured")
	}
	executable, err := r.executable()
	if err != nil {
		return WorkerResponse{}, fmt.Errorf("locating r42 executable: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return WorkerResponse{}, fmt.Errorf("encoding starlark worker request: %w", err)
	}

	commandContext := ctx
	cancel := func() {}
	if request.TimeoutNanos > 0 {
		// The worker owns the configured timeout so it can return a repairable typed-tool rejection.
		// The parent retains a small grace period to kill a wedged worker tree.
		commandContext, cancel = context.WithTimeout(ctx, time.Duration(request.TimeoutNanos)+workerTimeoutGracePeriod)
	}
	defer cancel()

	command := exec.CommandContext(commandContext, executable, r.arguments...)
	if r.environment != nil {
		command.Env = r.environment
	}
	configureWorkerProcess(command)
	canceler := newProcessCanceler(func() error {
		err := terminateWorkerProcessTree(command.Process)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	})
	command.Cancel = canceler.Cancel
	command.WaitDelay = time.Second
	command.Stdin = bytes.NewReader(encoded)
	stdout := newResponseBuffer(maxWorkerResponseBytes, func() { _ = command.Cancel() })
	stderr := newResponseBuffer(maxWorkerResponseBytes, func() { _ = command.Cancel() })
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	if stdout.exceeded {
		return WorkerResponse{}, workerInvocationError("starlark worker response exceeded 32 MiB", stdout.String(), stderr.String())
	}
	if stderr.exceeded {
		return WorkerResponse{}, workerInvocationError("starlark worker stderr exceeded 32 MiB", stdout.String(), stderr.String())
	}
	if err := commandContext.Err(); err != nil {
		return WorkerResponse{}, &InvocationError{cause: err, stdout: stdout.String(), stderr: stderr.String()}
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && len(stdout.Bytes()) == 0 {
			return WorkerResponse{Error: &WorkerError{
				Code: "starlark_worker_exited", Message: "worker exited before returning a valid response",
				Stdout: stdout.String(),
			}}, nil
		}
		return WorkerResponse{}, &InvocationError{
			cause:  fmt.Errorf("running starlark worker: %w", runErr),
			stdout: stdout.String(),
			stderr: stderr.String(),
		}
	}
	response, err := decodeWorkerResponse(stdout.Bytes())
	if err != nil {
		return WorkerResponse{}, &InvocationError{cause: err, stdout: stdout.String(), stderr: stderr.String()}
	}
	return response, nil
}

// InvocationError keeps bounded worker output available to the typed-tool adapter.
type InvocationError struct {
	cause  error
	stdout string
	stderr string
}

func (e *InvocationError) Error() string { return e.cause.Error() }

func (e *InvocationError) Unwrap() error { return e.cause }

func (e *InvocationError) Stdout() string { return e.stdout }

func (e *InvocationError) Stderr() string { return e.stderr }

func workerInvocationError(message, stdout, stderr string) error {
	return &InvocationError{cause: errors.New(message), stdout: stdout, stderr: stderr}
}

func decodeWorkerResponse(content []byte) (WorkerResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var response WorkerResponse
	if err := decoder.Decode(&response); err != nil {
		return WorkerResponse{}, fmt.Errorf("decoding starlark worker response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return WorkerResponse{}, errors.New("decoding starlark worker response: expected exactly one JSON value")
	}
	if (response.Result == nil) == (response.Error == nil) {
		return WorkerResponse{}, errors.New("decoding starlark worker response: expected exactly one result or error")
	}
	if response.Result != nil && !json.Valid([]byte(response.Result.ResultJSON)) {
		return WorkerResponse{}, errors.New("decoding starlark worker response: result_json must be valid JSON")
	}
	return response, nil
}

type responseBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	onLimit  func()
}

func newResponseBuffer(limit int, onLimit func()) *responseBuffer {
	return &responseBuffer{limit: limit, onLimit: onLimit}
}

func (b *responseBuffer) Write(content []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(content) <= remaining {
		return b.buffer.Write(content)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(content[:remaining])
	}
	b.exceeded = true
	b.onLimit()
	return len(content), nil
}

func (b *responseBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *responseBuffer) String() string { return b.buffer.String() }

type processCanceler struct {
	once   sync.Once
	cancel func() error
	err    error
}

func newProcessCanceler(cancel func() error) *processCanceler {
	return &processCanceler{cancel: cancel}
}

func (c *processCanceler) Cancel() error {
	c.once.Do(func() { c.err = c.cancel() })
	return c.err
}
