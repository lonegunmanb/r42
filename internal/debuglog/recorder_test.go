package debuglog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		assert.Equal(t, events[index], actual)
	}
	assert.Contains(t, recorder.Warning(), "sensitive")
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(directory, debuglog.EventsFileName))
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
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

func TestRedactKnownSecretsForNormalOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		secrets []string
		want    string
	}{
		{
			name:    "all occurrences",
			content: "token=secret-value repeated=secret-value",
			secrets: []string{"secret-value"},
			want:    "token=<sensitive> repeated=<sensitive>",
		},
		{
			name:    "overlapping secrets use longest match",
			content: "authorization=Bearer abc123",
			secrets: []string{"abc123", "Bearer abc123"},
			want:    "authorization=<sensitive>",
		},
		{
			name:    "empty secrets are ignored",
			content: "unchanged",
			secrets: []string{"", ""},
			want:    "unchanged",
		},
		{
			name:    "duplicate secrets",
			content: "secret",
			secrets: []string{"secret", "secret"},
			want:    "<sensitive>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, debuglog.RedactKnownSecrets(tt.content, tt.secrets))
		})
	}
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}
