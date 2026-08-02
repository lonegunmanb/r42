package debuglog_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextRecorderPersistsSequencedLifecycleEvents(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)
	ctx := debuglog.WithRecorder(t.Context(), recorder)

	require.NoError(t, debuglog.Record(ctx, debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "golden.run_plan", Status: debuglog.StatusStarted,
	}))
	require.NoError(t, debuglog.Record(ctx, debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "golden.run_plan", Status: debuglog.StatusCompleted,
	}))
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	lines := splitLines(string(content))
	require.Len(t, lines, 2)
	var first, second debuglog.Event
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	var firstFields, secondFields map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &firstFields))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &secondFields))
	assert.Equal(t, uint64(1), first.Sequence)
	assert.Equal(t, uint64(2), second.Sequence)
	assert.False(t, first.Timestamp.IsZero())
	assert.False(t, second.Timestamp.Before(first.Timestamp))
	assert.Equal(t, "golden.run_plan", second.Action)
	assert.Equal(t, debuglog.StatusCompleted, second.Status)
	assert.NotContains(t, firstFields, "duration_ms")
	assert.Contains(t, secondFields, "duration_ms")
}

func TestContextRecorderWithoutRecorderIsNoop(t *testing.T) {
	t.Parallel()

	require.NoError(t, debuglog.Record(context.Background(), debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "operation.plan", Status: debuglog.StatusStarted,
	}))
}

func TestDisabledRecorderPersistsNothing(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "debug")
	recorder, err := debuglog.NewRecorder(directory, false)
	require.NoError(t, err)
	require.NoError(t, recorder.Record(debuglog.Event{
		Kind:    debuglog.EventMessage,
		Session: debuglog.SessionResearch,
		Role:    debuglog.RoleAssistant,
		Content: "not persisted",
	}))
	require.NoError(t, recorder.Close())

	_, err = os.Stat(directory)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, recorder.Warning())
}

func TestDisabledRecorderPublishesEventsToRunObservers(t *testing.T) {
	t.Parallel()

	bus := debuglog.NewEventBus()
	var observed []debuglog.Event
	unsubscribe := bus.Subscribe(func(event debuglog.Event) {
		observed = append(observed, event)
	})
	t.Cleanup(unsubscribe)
	recorder, err := debuglog.NewRecorder(filepath.Join(t.TempDir(), "debug"), false)
	require.NoError(t, err)
	recorder.SetEventBus(bus)

	require.NoError(t, recorder.Record(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "research.static.market",
	}))

	require.Len(t, observed, 1)
	assert.Equal(t, uint64(1), observed[0].Sequence)
	assert.False(t, observed[0].Timestamp.IsZero())
	assert.Equal(t, "research.static.market", observed[0].BlockAddress)
}

func TestEventBusSequencesDirectAndRecorderEventsMonotonically(t *testing.T) {
	t.Parallel()

	bus := debuglog.NewEventBus()
	var sequences []uint64
	unsubscribe := bus.Subscribe(func(event debuglog.Event) {
		sequences = append(sequences, event.Sequence)
	})
	t.Cleanup(unsubscribe)
	ctx := debuglog.WithEventBus(t.Context(), bus)
	require.NoError(t, debuglog.Record(ctx, debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "apply", Status: debuglog.StatusStarted,
	}))
	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	recorder.SetEventBus(bus)
	require.NoError(t, recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message", Content: "complete",
	}))

	assert.Equal(t, []uint64{1, 2}, sequences)
}

func TestEventBusDeliversConcurrentEventsInSequenceOrder(t *testing.T) {
	t.Parallel()

	bus := debuglog.NewEventBus()
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var sequences []uint64
	unsubscribe := bus.Subscribe(func(event debuglog.Event) {
		if event.Sequence == 1 {
			close(started)
			<-release
		}
		mu.Lock()
		sequences = append(sequences, event.Sequence)
		mu.Unlock()
	})
	t.Cleanup(unsubscribe)

	ctx := debuglog.WithEventBus(t.Context(), bus)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- debuglog.Record(ctx, debuglog.Event{Kind: debuglog.EventMessage})
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- debuglog.Record(ctx, debuglog.Event{Kind: debuglog.EventMessage})
	}()
	select {
	case <-secondDone:
		t.Fatal("second event was delivered before the first observer returned")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []uint64{1, 2}, sequences)
}

func TestDebugRecorderPersistsCompleteTranscriptToFile(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "debug")
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)

	events := []debuglog.Event{
		{
			Kind:         debuglog.EventMessage,
			BlockAddress: "research.market",
			Session:      debuglog.SessionResearch,
			Role:         debuglog.RoleSystem,
			Content:      "r42 protocol\nauthor system prompt",
		},
		{
			Kind:         debuglog.EventMessage,
			BlockAddress: "research.market",
			Session:      debuglog.SessionResearch,
			Role:         debuglog.RoleUser,
			Content:      "research this market",
		},
		{
			Kind:         debuglog.EventMessage,
			BlockAddress: "research.market",
			Session:      debuglog.SessionResearch,
			Role:         debuglog.RoleAssistant,
			Content:      "draft answer",
		},
		{
			Kind:         debuglog.EventMessage,
			BlockAddress: "research.market",
			Session:      debuglog.SessionQC,
			Role:         debuglog.RoleAssistant,
			Content:      "QC found an issue",
		},
		{
			Kind:         debuglog.EventTool,
			BlockAddress: "research.market",
			Session:      debuglog.SessionResearch,
			ToolName:     "external_tool_lookup",
			Arguments:    json.RawMessage(`{"query":"market"}`),
			Result:       json.RawMessage(`{"accepted":true,"output":"answer"}`),
			Stdout:       "complete stdout",
			Stderr:       "complete stderr",
		},
	}
	for _, event := range events {
		require.NoError(t, recorder.Record(event))
	}
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	lines := splitLines(string(content))
	require.Len(t, lines, len(events))
	for index, line := range lines {
		var actual debuglog.Event
		require.NoError(t, json.Unmarshal([]byte(line), &actual))
		assert.Equal(t, uint64(index+1), actual.Sequence)
		assert.False(t, actual.Timestamp.IsZero())
		actual.Sequence = 0
		actual.Timestamp = time.Time{}
		assert.Equal(t, events[index], actual)
	}
	assert.Contains(t, recorder.Warning(), "sensitive")
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(directory, debuglog.EventsFileName))
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestRecorderFlushesEachEventForLiveProgress(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })

	require.NoError(t, recorder.Record(debuglog.Event{
		Kind:    debuglog.EventMessage,
		Session: debuglog.SessionResearch,
		Role:    debuglog.RoleUser,
		Content: "visible while the run is active",
	}))

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), "visible while the run is active")
}

func TestRecorderRejectsInvalidLifecycleOperations(t *testing.T) {
	t.Parallel()

	var nilRecorder *debuglog.Recorder
	require.EqualError(t, nilRecorder.Record(debuglog.Event{}), "debug recorder is required")
	require.NoError(t, nilRecorder.Close())
	assert.Empty(t, nilRecorder.Warning())

	_, err := debuglog.NewRecorder("", true)
	require.EqualError(t, err, "debug directory is required")

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)
	require.ErrorContains(t, recorder.Record(debuglog.Event{
		Kind:      debuglog.EventTool,
		Session:   debuglog.SessionResearch,
		Arguments: json.RawMessage(`{`),
	}), "writing debug event")
	require.NoError(t, recorder.Close())
	require.NoError(t, recorder.Close())
	require.EqualError(t, recorder.Record(debuglog.Event{}), "debug recorder is closed")
}

func TestRecorderReportsFilesystemCreationErrors(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "file")
	require.NoError(t, os.WriteFile(notDirectory, []byte("content"), 0o600))
	_, err := debuglog.NewRecorder(filepath.Join(notDirectory, "debug"), true)
	require.ErrorContains(t, err, "creating debug directory")

	directory := filepath.Join(parent, "events-is-directory")
	require.NoError(t, os.MkdirAll(filepath.Join(directory, debuglog.EventsFileName), 0o700))
	_, err = debuglog.NewRecorder(directory, true)
	require.ErrorContains(t, err, "creating debug events file")
}

func TestRecorderSerializesConcurrentEvents(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	recorder, err := debuglog.NewRecorder(directory, true)
	require.NoError(t, err)
	const count = 32
	var workers sync.WaitGroup
	workers.Add(count)
	errs := make(chan error, count)
	for index := range count {
		go func() {
			defer workers.Done()
			errs <- recorder.Record(debuglog.Event{
				Kind:    debuglog.EventMessage,
				Session: debuglog.SessionResearch,
				Role:    debuglog.RoleAssistant,
				Content: string(rune('a' + index)),
			})
		}()
	}
	workers.Wait()
	for range count {
		require.NoError(t, <-errs)
	}
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(directory, debuglog.EventsFileName))
	require.NoError(t, err)
	assert.Len(t, splitLines(string(content)), count)
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}
