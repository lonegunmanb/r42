package progress_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingWriter blocks every Write until release is closed.
type blockingWriter struct {
	release chan struct{}
}

func (w *blockingWriter) Write(payload []byte) (int, error) {
	<-w.release
	return len(payload), nil
}

// decodeLine decodes one JSONL frame into a generic map.
func decodeLine(line string) (map[string]any, error) {
	var decoded map[string]any
	err := json.Unmarshal([]byte(line), &decoded)
	return decoded, err
}

// publisherHarness owns a publisher backed by an in-memory buffer, its
// projector, and any captured warnings. When a custom encoder writer is used
// (e.g. a blocking or failing writer), writes are mirrored into the buffer so
// frames() can still inspect the encoded output.
type publisherHarness struct {
	publisher  *progress.Publisher
	buffer     *bytes.Buffer
	projector  *progress.Projector
	warnings   []string
	warningsMu sync.Mutex
}

func newPublisherHarness(t *testing.T, encoderWriter ioWriter, options ...progress.PublisherOption) *publisherHarness {
	t.Helper()
	buffer := new(bytes.Buffer)
	var writer ioWriter = buffer
	if encoderWriter != nil {
		writer = &mirrorWriter{primary: encoderWriter, mirror: buffer}
	}
	encoder, err := progress.NewEncoder(writer, progress.SchemaMajor1)
	require.NoError(t, err)
	projector := progress.NewProjector(buildProjectorPlan(t, testRunDirectory(t, "run-pub")))
	harness := &publisherHarness{buffer: buffer, projector: projector}
	options = append(options, progress.WithWarning(func(message string) {
		harness.warningsMu.Lock()
		harness.warnings = append(harness.warnings, message)
		harness.warningsMu.Unlock()
	}))
	harness.publisher = progress.NewPublisher(encoder, projector, "run-pub", options...)
	return harness
}

// mirrorWriter forwards writes to primary and mirrors them into mirror.
type mirrorWriter struct {
	primary ioWriter
	mirror  *bytes.Buffer
}

func (w *mirrorWriter) Write(payload []byte) (int, error) {
	_, _ = w.mirror.Write(payload)
	return w.primary.Write(payload)
}

func (h *publisherHarness) observe(address, action string) {
	h.publisher.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: action,
		BlockAddress: address, Session: debuglog.SessionResearch,
		Content: "delta",
	})
}

func (h *publisherHarness) observeLifecycle(address, action string, status debuglog.EventStatus) {
	h.publisher.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: action, Status: status,
		BlockAddress: address, BlockType: "research",
	})
}

func (h *publisherHarness) frames() []map[string]any {
	content := strings.TrimRight(h.buffer.String(), "\n")
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	frames := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		frame, err := decodeLine(line)
		if err != nil {
			continue
		}
		frames = append(frames, frame)
	}
	return frames
}

func (h *publisherHarness) waitForMinFrames(t *testing.T, minimum int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames := h.frames()
		if len(frames) >= minimum {
			return frames
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h.frames()
}

func (h *publisherHarness) waitForFrameType(t *testing.T, want string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, frame := range h.frames() {
			if frame["type"] == want {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ioWriter is the minimal writer interface used across the harness.
type ioWriter interface{ Write([]byte) (int, error) }

func TestPublisherEmitsInitialSnapshotOnStart(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, nil)
	harness.publisher.Start()
	harness.publisher.Close()

	frames := harness.frames()
	require.NotEmpty(t, frames)
	first := frames[0]
	assert.Equal(t, "run_snapshot", first["type"])
	assert.Equal(t, "run-pub", first["run_id"])
	nodes, ok := first["nodes"].([]any)
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(nodes), 4)
}

func TestPublisherEmitsNodeUpsertAndTimeline(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, nil)
	harness.publisher.Start()
	harness.observe("research.static.frame", "assistant.message")
	harness.publisher.Close()

	frames := harness.waitForMinFrames(t, 3)
	var sawUpsert, sawTimeline bool
	for _, frame := range frames {
		switch frame["type"] {
		case "node_upsert":
			sawUpsert = true
		case "timeline_append":
			sawTimeline = true
		}
	}
	assert.True(t, sawUpsert, "expected a node_upsert frame")
	assert.True(t, sawTimeline, "expected a timeline_append frame")
}

func TestPublisherObserveDoesNotBlockWhenWriterStalled(t *testing.T) {
	t.Parallel()

	blocked := &blockingWriter{release: make(chan struct{})}
	harness := newPublisherHarness(t, blocked, progress.WithPublisherCapacity(4))
	harness.publisher.Start()

	for range 50 {
		done := make(chan struct{})
		go func() {
			harness.observe("research.static.frame", "assistant.message_delta")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Observe blocked on a stalled writer")
		}
	}

	close(blocked.release)
	harness.publisher.Close()
}

func TestPublisherCoalescesNodeUpsertsPerAddressUnderPressure(t *testing.T) {
	t.Parallel()

	blocked := &blockingWriter{release: make(chan struct{})}
	harness := newPublisherHarness(t, blocked, progress.WithPublisherCapacity(8))
	harness.publisher.Start()

	// Flood one address through several statuses while the writer is stalled:
	// coalescing must keep only the newest pending node_upsert per address,
	// and that newest value must be the last status observed.
	for range 100 {
		harness.observeLifecycle("research.static.frame", "block.apply", debuglog.StatusStarted)
	}
	harness.observeLifecycle("research.static.frame", "block.apply", debuglog.StatusCompleted)
	close(blocked.release)
	harness.publisher.Close()

	frames := harness.waitForMinFrames(t, 1)
	var upsertsForAddress int
	var finalStatus string
	for _, frame := range frames {
		if frame["type"] != "node_upsert" {
			continue
		}
		node, ok := frame["node"].(map[string]any)
		require.True(t, ok)
		if node["block_address"] == "research.static.frame" {
			upsertsForAddress++
			finalStatus, _ = node["status"].(string)
		}
	}
	assert.LessOrEqual(t, upsertsForAddress, 1,
		"coalescing must keep only the newest pending upsert per address")
	assert.Equal(t, "succeeded", finalStatus,
		"the surviving upsert must carry the newest observed state")
}

func TestPublisherDropsOldestTimelineUnderPressure(t *testing.T) {
	t.Parallel()

	blocked := &blockingWriter{release: make(chan struct{})}
	harness := newPublisherHarness(t, blocked, progress.WithPublisherCapacity(5))
	harness.publisher.Start()

	// Flood timelines while the writer is stalled; the queue is bounded, so
	// only the newest timelines survive.
	for range 100 {
		harness.observe("research.static.frame", "assistant.message_delta")
	}
	close(blocked.release)
	harness.publisher.Close()

	frames := harness.waitForMinFrames(t, 1)
	var timelineCount int
	for _, frame := range frames {
		if frame["type"] == "timeline_append" {
			timelineCount++
		}
	}
	// The timeline queue is capped far below the 100 flooded events.
	assert.Less(t, timelineCount, 100)
}

func TestPublisherStructuralRecordSurvivesPressure(t *testing.T) {
	t.Parallel()

	blocked := &blockingWriter{release: make(chan struct{})}
	harness := newPublisherHarness(t, blocked, progress.WithPublisherCapacity(5))
	harness.publisher.Start()

	// Fill with timelines, then announce a structural record; the structural
	// record must survive while timelines are dropped.
	for range 50 {
		harness.observe("research.static.frame", "assistant.message_delta")
	}
	harness.publisher.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: "research.static.frame",
		Paths: []string{"research.static.frame.tasks[0]"}, Count: 1,
	})
	close(blocked.release)
	harness.publisher.Close()

	assert.True(t, harness.waitForFrameType(t, "dynamic_tasks_materialized"),
		"structural record must survive pressure")
}

func TestPublisherWarnsAtMostOnceAndStopsPublishingOnWriteFailure(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, &failingWriter{err: assert.AnError})
	harness.publisher.Start()
	for range 10 {
		harness.observeLifecycle("research.static.frame", "block.apply", debuglog.StatusStarted)
	}
	harness.publisher.Close()

	harness.warningsMu.Lock()
	assert.Len(t, harness.warnings, 1, "a write failure must warn at most once")
	harness.warningsMu.Unlock()

	// Publication is disabled after the first write failure: a failing writer
	// can write at most the initial run_snapshot (the first attempt), and no
	// further frames are attempted after the failure.
	frames := harness.frames()
	assert.LessOrEqual(t, len(frames), 1,
		"publication must stop after the first write failure")
}

func TestPublisherIgnoresUnknownAddressEvents(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, nil)
	harness.publisher.Start()

	// Wait for the asynchronous initial run_snapshot to be written so the
	// baseline frame count is stable before feeding unknown-address events.
	harness.waitForFrameType(t, "run_snapshot")
	baseline := len(harness.frames())

	harness.publisher.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "no.such.block", BlockType: "research",
	})
	harness.publisher.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: "no.such.block", Session: debuglog.SessionResearch,
		Content: "must not be published",
	})
	harness.publisher.Close()

	// Unknown addresses produce no records; the privacy boundary is enforced
	// at the publisher layer too, not just the projector.
	assert.Len(t, harness.frames(), baseline,
		"unknown address events must not produce any frames")
}

func TestPublisherIgnoresUnknownDynamicMaterialization(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, nil)
	harness.publisher.Start()
	require.True(t, harness.waitForFrameType(t, "run_snapshot"))
	baseline := len(harness.frames())

	harness.publisher.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: "no.such.block",
		Paths: []string{"no.such.block.tasks[0]"}, Count: 1,
	})
	harness.publisher.Close()

	assert.Len(t, harness.frames(), baseline,
		"unknown dynamic materialization must not produce a structural record")
}

func TestPublisherCloseIsBoundedWithStalledWriter(t *testing.T) {
	t.Parallel()

	blocked := &blockingWriter{release: make(chan struct{})}
	harness := newPublisherHarness(t, blocked, progress.WithDrainTimeout(50*time.Millisecond))
	harness.publisher.Start()
	harness.observeLifecycle("research.static.frame", "block.apply", debuglog.StatusStarted)

	started := time.Now()
	harness.publisher.Close()
	assert.Less(t, time.Since(started), time.Second)

	close(blocked.release)
	time.Sleep(20 * time.Millisecond)
}

func TestPublisherCloseWithoutStartReturnsImmediately(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, nil, progress.WithDrainTimeout(100*time.Millisecond))
	started := time.Now()
	harness.publisher.Close()

	assert.Less(t, time.Since(started), 50*time.Millisecond)
}

func TestPublisherFinishEmitsExactlyOneTerminalRecord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		want    string
		summary string
	}{
		{name: "success", want: "run_completed"},
		{name: "failure", err: errors.New("failed\x1b[31m"), want: "run_failed", summary: "failed"},
		{name: "canceled", err: context.Canceled, want: "run_canceled", summary: "Apply canceled"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newPublisherHarness(t, nil)
			harness.publisher.Start()
			harness.publisher.Finish(test.err)
			harness.publisher.Finish(assert.AnError)
			harness.publisher.Close()

			var terminals []map[string]any
			for _, frame := range harness.frames() {
				if frame["type"] == test.want {
					terminals = append(terminals, frame)
				}
			}
			require.Len(t, terminals, 1)
			assert.Equal(t, true, terminals[0]["critical"])
			if test.summary != "" {
				assert.Contains(t, terminals[0]["summary"], test.summary)
				assert.NotContains(t, terminals[0]["summary"], "\x1b")
			}
		})
	}
}

func TestPublisherFinishFailureDoesNotExposeApplyErrorText(t *testing.T) {
	t.Parallel()

	harness := newPublisherHarness(t, nil)
	harness.publisher.Start()
	harness.publisher.Finish(errors.New("report=/private/run/report.md output=secret-value\x1b[31m"))
	harness.publisher.Close()

	for _, frame := range harness.frames() {
		if frame["type"] != "run_failed" {
			continue
		}
		assert.Equal(t, "Apply failed", frame["summary"])
		assert.NotContains(t, frame["summary"], "report")
		assert.NotContains(t, frame["summary"], "secret-value")
		return
	}
	t.Fatal("expected run_failed terminal record")
}
