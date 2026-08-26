package starlarktool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"time"
)

// WorkerRequest is one parent-to-worker invocation.
type WorkerRequest struct {
	Code         string `json:"code"`
	DataJSON     string `json:"data_json"`
	Config       Config `json:"config"`
	TimeoutNanos int64  `json:"timeout_nanos"`
	MemoryLimit  int64  `json:"memory_limit"`
}

// WorkerError makes an evaluator failure safe to send over the worker protocol.
type WorkerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Stdout  string `json:"stdout,omitempty"`
	Steps   uint64 `json:"steps,omitempty"`
}

// WorkerResponse is the sole worker stdout document.
type WorkerResponse struct {
	Result *Result      `json:"result,omitempty"`
	Error  *WorkerError `json:"error,omitempty"`
}

// Serve reads and writes exactly one JSON document without creating source files.
func Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	var request WorkerRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode starlark worker request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("decode starlark worker request: expected exactly one JSON value")
	}
	if request.TimeoutNanos > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(request.TimeoutNanos))
		defer cancel()
	}
	if request.MemoryLimit > 0 {
		previous := debug.SetMemoryLimit(request.MemoryLimit)
		defer debug.SetMemoryLimit(previous)
	}
	result, err := Evaluate(ctx, request.Config, request.Code, request.DataJSON)
	response := WorkerResponse{Result: &result}
	if err != nil {
		var evaluationErr *Error
		if !errors.As(err, &evaluationErr) {
			return fmt.Errorf("evaluate starlark worker request: %w", err)
		}
		response.Result = nil
		response.Error = &WorkerError{
			Code: evaluationErr.Code, Message: evaluationErr.Error(),
			Stdout: evaluationErr.Stdout, Steps: evaluationErr.Steps,
		}
	}
	encoder := json.NewEncoder(output)
	if err := encoder.Encode(response); err != nil {
		return fmt.Errorf("encode starlark worker response: %w", err)
	}
	return nil
}
