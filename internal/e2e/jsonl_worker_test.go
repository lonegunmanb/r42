package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestJSONLWorkerSubprocessReconstructsProgress(t *testing.T) {
	t.Parallel()

	output, stderr, err := runJSONLWorker(t, "success")

	require.NoError(t, err, stderr)
	view := newWorkerView(t, output)
	assert.Contains(t, view.nodes, "research.static.collect")
	assert.Contains(t, view.nodes, "research.dynamic")
	assert.Contains(t, view.nodes, "research.dynamic.tasks[0]")
	assert.Contains(t, view.nodes, "research.dynamic.tasks[1]")
	assert.Len(t, view.timelines["research.static.collect"], 200)
	assert.Equal(t, "run_completed", view.terminal)
	assert.False(t, view.progressIncomplete)
	assert.Equal(t, "succeeded", view.outcome(0, false))
	assert.Empty(t, stderr)
}

func TestWorkerViewUnknownRecordCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		frames     []map[string]any
		incomplete bool
	}{
		{
			name: "unknown non-critical record is ignored",
			frames: []map[string]any{
				{"type": "future_event", "critical": false},
				{"type": "run_completed", "critical": true},
			},
		},
		{
			name: "unknown critical record marks progress incomplete",
			frames: []map[string]any{
				{"type": "future_event", "critical": true},
				{"type": "run_completed", "critical": true},
			},
			incomplete: true,
		},
		{
			name:       "missing terminal record marks progress incomplete",
			frames:     []map[string]any{{"type": "run_snapshot", "critical": true, "nodes": []any{}}},
			incomplete: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			view := workerViewFromFrames(test.frames)

			assert.Equal(t, test.incomplete, view.progressIncomplete)
		})
	}
}

func TestJSONLWorkerSubprocessCancellationUsesTerminalAndExitStatus(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("os.Process.Signal does not support os.Interrupt on Windows")
	}

	worker := startJSONLWorker(t, "canceled")
	reader := worker.negotiate(t)
	frames := worker.readUntil(t, reader, "node_upsert")
	require.NoError(t, worker.command.Process.Signal(os.Interrupt))
	frames = append(frames, worker.readRemaining(t, reader)...)
	err := worker.command.Wait()

	require.Error(t, err)
	exitErr := new(exec.ExitError)
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.ExitCode())
	view := workerViewFromFrames(frames)
	assert.Equal(t, "run_canceled", view.terminal)
	assert.False(t, view.progressIncomplete)
	assert.Equal(t, "canceled", view.outcome(exitErr.ExitCode(), true))
}

func TestJSONLWorkerSubprocessClosedStdoutLeavesApplySuccessful(t *testing.T) {
	t.Parallel()

	worker := startJSONLWorker(t, "success")
	reader := worker.negotiate(t)
	require.NoError(t, worker.stdout.Close())
	_ = reader
	err := worker.command.Wait()

	require.NoError(t, err, worker.stderr.String())
	assert.Contains(t, worker.stderr.String(), "JSONL progress publication stopped:")
}

func TestJSONLWorkerSubprocessSlowStdoutLeavesExitStatusAuthoritative(t *testing.T) {
	t.Parallel()

	worker := startJSONLWorker(t, "flood")
	reader := worker.negotiate(t)
	_ = reader // Deliberately stop draining progress after ready.
	started := time.Now()
	err := worker.command.Wait()

	require.NoError(t, err, worker.stderr.String())
	assert.Less(t, time.Since(started), 7*time.Second)
}

//nolint:paralleltest // This is launched as the sole test in its subprocess.
func TestJSONLWorkerProcess(t *testing.T) {
	if os.Getenv("R42_JSONL_WORKER") != "1" {
		return
	}
	scenario := workerScenario(os.Args)
	runtime, err := newJSONLWorkerRuntime(scenario)
	require.NoError(t, err)
	ctx := context.Background()
	stop := func() {}
	if scenario == "canceled" {
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
	}
	command := cli.NewCommand(runtime)
	command.SetArgs([]string{"apply", "--ui=jsonl"})
	command.SetIn(os.Stdin)
	command.SetOut(os.Stdout)
	command.SetErr(os.Stderr)
	err = command.ExecuteContext(ctx)
	if scenario == "canceled" {
		require.ErrorIs(t, err, context.Canceled)
		stop()
		os.Exit(130)
	}
	require.NoError(t, err)
	stop()
	os.Exit(0)
}

type jsonlWorkerProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  bytes.Buffer
}

func startJSONLWorker(t *testing.T, scenario string) *jsonlWorkerProcess {
	t.Helper()
	arguments := []string{"-test.run=^TestJSONLWorkerProcess$", "--", scenario}
	command := exec.Command(os.Args[0], arguments...)
	command.Env = append(os.Environ(), "R42_JSONL_WORKER=1")
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	worker := &jsonlWorkerProcess{command: command, stdin: stdin, stdout: stdout}
	command.Stderr = &worker.stderr
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return worker
}

func (w *jsonlWorkerProcess) negotiate(t *testing.T) *bufio.Reader {
	t.Helper()
	reader := bufio.NewReader(w.stdout)
	hello := w.readFrame(t, reader)
	require.Equal(t, "hello", hello["type"])
	_, err := io.WriteString(w.stdin, `{"type":"select","handshake_version":1,"schema_version":1}`+"\n")
	require.NoError(t, err)
	require.NoError(t, w.stdin.Close())
	ready := w.readFrame(t, reader)
	require.Equal(t, "ready", ready["type"])
	return reader
}

func (w *jsonlWorkerProcess) readUntil(t *testing.T, reader *bufio.Reader, recordType string) []map[string]any {
	t.Helper()
	frames := make([]map[string]any, 0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frame := w.readFrame(t, reader)
		frames = append(frames, frame)
		if frame["type"] == recordType {
			return frames
		}
	}
	t.Fatalf("did not receive %s", recordType)
	return nil
}

func (w *jsonlWorkerProcess) readRemaining(t *testing.T, reader *bufio.Reader) []map[string]any {
	t.Helper()
	frames := make([]map[string]any, 0)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			frames = append(frames, decodeWorkerFrame(t, line))
		}
		if errors.Is(err, io.EOF) {
			return frames
		}
		require.NoError(t, err)
	}
}

func (w *jsonlWorkerProcess) readFrame(t *testing.T, reader *bufio.Reader) map[string]any {
	t.Helper()
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	return decodeWorkerFrame(t, line)
}

func runJSONLWorker(t *testing.T, scenario string) (string, string, error) {
	t.Helper()
	worker := startJSONLWorker(t, scenario)
	reader := worker.negotiate(t)
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		return "", worker.stderr.String(), readErr
	}
	err := worker.command.Wait()
	return `{"type":"hello"}` + "\n" + `{"type":"ready"}` + "\n" + string(output), worker.stderr.String(), err
}

type workerView struct {
	nodes              map[string]map[string]any
	timelines          map[string][]map[string]any
	terminal           string
	progressIncomplete bool
}

func newWorkerView(t *testing.T, output string) *workerView {
	t.Helper()
	frames := make([]map[string]any, 0)
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		frames = append(frames, decodeWorkerFrame(t, line))
	}
	return workerViewFromFrames(frames)
}

func workerViewFromFrames(frames []map[string]any) *workerView {
	view := &workerView{nodes: make(map[string]map[string]any), timelines: make(map[string][]map[string]any)}
	for _, frame := range frames {
		switch frame["type"] {
		case "run_snapshot", "dynamic_tasks_materialized":
			nodes, _ := frame["nodes"].([]any)
			for _, raw := range nodes {
				view.upsertNode(raw)
			}
		case "node_upsert":
			view.upsertNode(frame["node"])
		case "timeline_append":
			address, _ := frame["block_address"].(string)
			entries := view.timelines[address]
			entries = append(entries, frame)
			if len(entries) > 200 {
				entries = entries[len(entries)-200:]
			}
			view.timelines[address] = entries
		case "run_completed", "run_failed", "run_canceled":
			view.terminal, _ = frame["type"].(string)
		default:
			critical, _ := frame["critical"].(bool)
			if critical {
				view.progressIncomplete = true
			}
		}
	}
	if view.terminal == "" {
		view.progressIncomplete = true
	}
	return view
}

func (v *workerView) upsertNode(raw any) {
	node, ok := raw.(map[string]any)
	if !ok {
		return
	}
	address, _ := node["block_address"].(string)
	if address != "" {
		v.nodes[address] = node
	}
}

func (v *workerView) outcome(exitCode int, workerCanceled bool) string {
	if workerCanceled {
		return "canceled"
	}
	if exitCode == 0 {
		return "succeeded"
	}
	return "failed"
}

type jsonlWorkerRuntime struct {
	planned  *plan.Plan
	scenario string
}

func newJSONLWorkerRuntime(scenario string) (*jsonlWorkerRuntime, error) {
	directory, err := os.MkdirTemp("", "r42-jsonl-worker-")
	if err != nil {
		return nil, err
	}
	planned, err := plan.NewForRun(directory, filepath.Join(directory, "run"), []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
		{Address: "research.dynamic", Kind: "research"},
	}, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return &jsonlWorkerRuntime{planned: planned, scenario: scenario}, nil
}

func (r *jsonlWorkerRuntime) OpenProject(string) (string, string, error) {
	return r.planned.Directory(), r.planned.Directory(), nil
}

func (r *jsonlWorkerRuntime) Config(_ string, options executor.ResearchConfigOptions) (*executor.ResearchConfig, error) {
	return r.config(options)
}

func (r *jsonlWorkerRuntime) ConfigFromPlan(_ *plan.Plan, options executor.ResearchConfigOptions) (*executor.ResearchConfig, error) {
	return r.config(options)
}

func (r *jsonlWorkerRuntime) config(options executor.ResearchConfigOptions) (*executor.ResearchConfig, error) {
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		if err := emitWorkerEvents(options.Context, r.scenario); err != nil {
			return nil, nil, err
		}
		if r.scenario == "canceled" {
			<-options.Context.Done()
			return nil, nil, options.Context.Err()
		}
		return nil, nil, nil
	}
	return executor.NewResearchConfigFromPlan(r.planned, options)
}

func (*jsonlWorkerRuntime) SaveProjectOutputs(string, string, map[string]cty.Value) error { return nil }

func (*jsonlWorkerRuntime) ReadProjectOutputs(string) ([]byte, error) { return nil, nil }

func emitWorkerEvents(ctx context.Context, scenario string) error {
	if err := debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusStarted, debuglog.Event{
		BlockAddress: "research.static.collect", BlockType: "research",
	}); err != nil {
		return err
	}
	count, content := 205, "static progress"
	if scenario == "flood" {
		count, content = 2000, strings.Repeat("x", 4096)
	}
	for range count {
		if err := debuglog.Record(ctx, debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.message_delta",
			BlockAddress: "research.static.collect", Session: debuglog.SessionResearch, Content: content,
		}); err != nil {
			return err
		}
	}
	if err := debuglog.Lifecycle(ctx, "dynamic.tasks.materialized", debuglog.StatusCompleted, debuglog.Event{
		BlockAddress: "research.dynamic", BlockType: "research",
		Paths: []string{"research.dynamic.tasks[0]", "research.dynamic.tasks[1]"}, Count: 2,
	}); err != nil {
		return err
	}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, address := range []string{"research.dynamic.tasks[0]", "research.dynamic.tasks[1]"} {
		group.Go(func() {
			if err := debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusStarted, debuglog.Event{BlockAddress: address, BlockType: "research"}); err != nil {
				errs <- err
				return
			}
			if err := debuglog.Record(ctx, debuglog.Event{
				Kind: debuglog.EventMessage, Action: "assistant.message_delta",
				BlockAddress: address, Session: debuglog.SessionResearch, Content: "dynamic progress",
			}); err != nil {
				errs <- err
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func workerScenario(arguments []string) string {
	for index, argument := range arguments {
		if argument == "--" && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return "success"
}

func decodeWorkerFrame(t *testing.T, line string) map[string]any {
	t.Helper()
	var frame map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &frame))
	return frame
}
