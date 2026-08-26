package starlarktool

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerStartsFreshWorkerAndLeavesNoSourceFile(t *testing.T) {
	temporaryDirectory := t.TempDir()
	t.Setenv("TMP", temporaryDirectory)
	t.Setenv("TMPDIR", temporaryDirectory)
	runner := testRunner(t)

	response, err := runner.Run(t.Context(), WorkerRequest{
		Code:     `result = {"answer": data + 1}`,
		DataJSON: "41",
	})

	require.NoError(t, err)
	require.NotNil(t, response.Result)
	assert.JSONEq(t, `{"answer":42}`, response.Result.ResultJSON)
	entries, err := os.ReadDir(temporaryDirectory)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRunnerTerminatesWorkerWhenContextExpires(t *testing.T) {
	t.Parallel()
	runner := testRunner(t)
	runner.environment = append(runner.environment, "R42_STARLARK_WORKER_STALL=1")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := runner.Run(ctx, WorkerRequest{Code: "result = 1", DataJSON: "null"})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRunnerDoesNotRetainStateBetweenWorkers(t *testing.T) {
	t.Parallel()
	runner := testRunner(t)

	first, err := runner.Run(t.Context(), WorkerRequest{Code: "remembered = 41\nresult = remembered", DataJSON: "null"})

	require.NoError(t, err)
	require.NotNil(t, first.Result)
	second, err := runner.Run(t.Context(), WorkerRequest{Code: "result = remembered", DataJSON: "null"})
	require.NoError(t, err)
	require.NotNil(t, second.Error)
	assert.Equal(t, "starlark_name_error", second.Error.Code)
}

func TestRunnerReturnsWorkerTimeoutAsRepairableResponse(t *testing.T) {
	t.Parallel()
	runner := testRunner(t)

	response, err := runner.Run(t.Context(), WorkerRequest{
		Code:         "def calculate():\n  total = 0\n  for item in range(100000000):\n    total += item\n  return total\nresult = calculate()",
		DataJSON:     "null",
		Config:       Config{MaxSteps: 10_000_000},
		TimeoutNanos: int64(10 * time.Millisecond),
	})

	require.NoError(t, err)
	require.Nil(t, response.Result)
	require.NotNil(t, response.Error)
	assert.Equal(t, "starlark_timeout", response.Error.Code)
}

func TestRunnerAllowsWorkerStartupBeforeEvaluationTimeout(t *testing.T) {
	t.Parallel()
	runner := testRunner(t)
	runner.environment = append(runner.environment, "R42_STARLARK_WORKER_START_DELAY=2s")

	response, err := runner.Run(t.Context(), WorkerRequest{
		Code:         "def calculate():\n  total = 0\n  for item in range(100000000):\n    total += item\n  return total\nresult = calculate()",
		DataJSON:     "null",
		Config:       Config{MaxSteps: 10_000_000},
		TimeoutNanos: int64(10 * time.Millisecond),
	})

	require.NoError(t, err)
	require.Nil(t, response.Result)
	require.NotNil(t, response.Error)
	assert.Equal(t, "starlark_timeout", response.Error.Code)
}

func TestRunnerReturnsUnexpectedWorkerExitAsRepairableResponse(t *testing.T) {
	t.Parallel()
	runner := testRunner(t)
	runner.environment = append(runner.environment, "R42_STARLARK_WORKER_EXIT=1")

	response, err := runner.Run(t.Context(), WorkerRequest{Code: "result = 1", DataJSON: "null"})

	require.NoError(t, err)
	require.Nil(t, response.Result)
	require.NotNil(t, response.Error)
	assert.Equal(t, "starlark_worker_exited", response.Error.Code)
}

//nolint:paralleltest // The test worker calls os.Exit after serving its protocol request.
func TestWorkerProcessServesExactlyOneInternalRequest(t *testing.T) {
	if os.Getenv("R42_STARLARK_WORKER_HELPER") != "1" {
		return
	}
	if delay, err := time.ParseDuration(os.Getenv("R42_STARLARK_WORKER_START_DELAY")); err == nil && delay > 0 {
		time.Sleep(delay)
	}
	if os.Getenv("R42_STARLARK_WORKER_STALL") == "1" {
		time.Sleep(time.Second)
	}
	if os.Getenv("R42_STARLARK_WORKER_EXIT") == "1" {
		os.Exit(7)
	}
	if err := Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		_, _ = os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

func testRunner(t *testing.T) *Runner {
	t.Helper()
	runner := NewRunner()
	runner.executable = os.Executable
	runner.arguments = []string{"-test.run=TestWorkerProcessServesExactlyOneInternalRequest", "--"}
	runner.environment = append(os.Environ(), "R42_STARLARK_WORKER_HELPER=1")
	return runner
}

func TestWorkerResponseJSONUsesStableWireNames(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(WorkerResponse{Result: &Result{ResultJSON: "1", Steps: 1}})

	require.NoError(t, err)
	assert.JSONEq(t, `{"result":{"result_json":"1","stdout":"","steps":1}}`, string(encoded))
}

func TestProcessCancelerTerminatesOnlyOnce(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	canceler := newProcessCanceler(func() error {
		calls.Add(1)
		return nil
	})
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() { assert.NoError(t, canceler.Cancel()) })
	}
	workers.Wait()

	assert.EqualValues(t, 1, calls.Load())
}
