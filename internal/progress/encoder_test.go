package progress_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	require.NoError(t, err)
	// Normalize line endings so the fixtures are portable across checkouts.
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func decodeJSON(t *testing.T, line string, target any) error {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(line))
	err := decoder.Decode(target)
	require.NoError(t, err)
	return nil
}

func encodeRecord(t *testing.T, record progress.Record, envelope progress.Envelope) string {
	t.Helper()
	var buffer bytes.Buffer
	encoder, err := progress.NewEncoder(&buffer, progress.SchemaMajor1)
	require.NoError(t, err)
	require.NoError(t, encoder.EncodeRecord(envelope, record))
	return buffer.String()
}

func TestSchemaOneNodeRecordEncoding(t *testing.T) {
	t.Parallel()

	record := &progress.NodeRecord{Node: progress.NodeProjection{
		BlockAddress: "research.static.collect",
		BlockKind:    "research",
		Dependencies: []string{},
		Phase:        progress.PhaseCollection,
		Status:       progress.StatusRunning,
		Activity:     progress.ActivityTool,
		ToolName:     "web_search",
		Usage:        &progress.TokenUsage{Input: 100, Output: 50},
	}}

	line := encodeRecord(t, record, progress.Envelope{RunID: "run-x", Sequence: 7})

	var decoded map[string]any
	require.NoError(t, decodeJSON(t, line, &decoded))
	assert.Equal(t, "node_upsert", decoded["type"])
	assert.Equal(t, "r42.progress", decoded["protocol"])
	assert.InDelta(t, 1, decoded["schema_version"], 0)
	assert.Equal(t, true, decoded["critical"])
	assert.Equal(t, "run-x", decoded["run_id"])
	assert.InDelta(t, 7, decoded["sequence"], 0)
	node, ok := decoded["node"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "research.static.collect", node["block_address"])
	assert.Equal(t, "web_search", node["tool_name"])
	usage, ok := node["usage"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 100, usage["input"], 0)
	assert.InDelta(t, 50, usage["output"], 0)
}

func TestSchemaOneStructuralRecordsUseCanonicalAddressFields(t *testing.T) {
	t.Parallel()

	node := progress.NodeProjection{
		BlockAddress:  "research.dynamic[0]",
		BlockKind:     "research",
		ParentAddress: "research.dynamic",
		Status:        progress.StatusWaiting,
	}
	tests := []struct {
		name   string
		record progress.Record
		want   string
	}{
		{
			name:   "run snapshot",
			record: &progress.RunSnapshotRecord{Nodes: []progress.NodeProjection{node}},
			want:   "run_snapshot",
		},
		{
			name: "dynamic tasks materialized",
			record: &progress.DynamicTasksMaterializedRecord{
				ParentAddress: "research.dynamic",
				Nodes:         []progress.NodeProjection{node},
			},
			want: "dynamic_tasks_materialized",
		},
		{
			name:   "node upsert",
			record: &progress.NodeRecord{Node: node},
			want:   "node_upsert",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			line := encodeRecord(t, test.record, progress.Envelope{RunID: "run-x"})
			var decoded map[string]any
			require.NoError(t, decodeJSON(t, line, &decoded))
			assert.Equal(t, test.want, decoded["type"])
			if test.want == "dynamic_tasks_materialized" {
				assert.Equal(t, "research.dynamic", decoded["parent_address"])
			}
			if test.want == "node_upsert" {
				encodedNode, ok := decoded["node"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "research.dynamic", encodedNode["parent_address"])
				assert.NotContains(t, encodedNode, "parent")
			}
		})
	}
}

func TestSchemaOneNodeRecordUnknownOptionalFieldIgnoredByConsumer(t *testing.T) {
	t.Parallel()

	// A consumer that only knows schema-1 fields must ignore an unknown
	// optional field such as a future "author" without failing.
	const frame = `{"type":"node_upsert","critical":true,"protocol":"r42.progress","schema_version":1,"run_id":"run-x","sequence":7,"timestamp":"2026-08-27T00:00:00Z","node":{"block_address":"a","block_kind":"research","phase":"collection","status":"running","activity":"idle","tool_name":"","dependencies":[],"usage":null,"parent":"","author":"future"}}`
	var decoded map[string]any
	require.NoError(t, decodeJSON(t, frame, &decoded))
	node, ok := decoded["node"].(map[string]any)
	require.True(t, ok)
	// Unknown field is preserved by generic decode but must not break the consumer.
	assert.Equal(t, "future", node["author"])
}

func TestSchemaOneTimelineRecordIsNonCritical(t *testing.T) {
	t.Parallel()

	record := &progress.TimelineRecord{
		BlockAddress: "research.static.collect",
		Activity:     progress.ActivityTool,
		Summary:      "Running web_search",
	}

	line := encodeRecord(t, record, progress.Envelope{RunID: "run-x"})

	var decoded map[string]any
	require.NoError(t, decodeJSON(t, line, &decoded))
	assert.Equal(t, "timeline_append", decoded["type"])
	assert.Equal(t, false, decoded["critical"])
}

func TestSchemaOneTerminalRecords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record progress.Record
		want   string
	}{
		{
			name:   "run_completed",
			record: &progress.RunCompletedRecord{Status: "succeeded", Total: 3, Succeeded: 3, Failed: 0},
			want:   "run_completed",
		},
		{
			name:   "run_failed",
			record: &progress.RunFailedRecord{Status: "failed", Summary: "apply failed"},
			want:   "run_failed",
		},
		{
			name:   "run_canceled",
			record: &progress.RunCanceledRecord{Status: "canceled", Summary: "worker canceled"},
			want:   "run_canceled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			line := encodeRecord(t, test.record, progress.Envelope{RunID: "run-x"})
			var decoded map[string]any
			require.NoError(t, decodeJSON(t, line, &decoded))
			assert.Equal(t, test.want, decoded["type"])
			assert.Equal(t, true, decoded["critical"])
		})
	}
}

func TestSchemaOneTerminalRecordsRejectMismatchedStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record progress.Record
	}{
		{
			name:   "completed cannot report failure",
			record: &progress.RunCompletedRecord{Status: progress.StatusFailed},
		},
		{
			name:   "failed cannot report success",
			record: &progress.RunFailedRecord{Status: progress.StatusSucceeded, Summary: "failed"},
		},
		{
			name:   "canceled cannot report success",
			record: &progress.RunCanceledRecord{Status: progress.StatusSucceeded, Summary: "canceled"},
		},
	}

	encoder, err := progress.NewEncoder(io.Discard, progress.SchemaMajor1)
	require.NoError(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, test.record)
			require.Error(t, err)
			assert.ErrorContains(t, err, "status")
		})
	}
}

func TestSchemaOneCompletedRecordRequiresConsistentCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		record  *progress.RunCompletedRecord
		wantErr string
	}{
		{
			name:   "zero-node success keeps zero counts on the wire",
			record: &progress.RunCompletedRecord{Status: progress.StatusSucceeded},
		},
		{
			name:    "failed nodes cannot be completed success",
			record:  &progress.RunCompletedRecord{Status: progress.StatusSucceeded, Total: 2, Succeeded: 1, Failed: 1},
			wantErr: "counts",
		},
		{
			name:    "success count must equal total",
			record:  &progress.RunCompletedRecord{Status: progress.StatusSucceeded, Total: 2, Succeeded: 1},
			wantErr: "counts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			encoder, err := progress.NewEncoder(&buffer, progress.SchemaMajor1)
			require.NoError(t, err)
			err = encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, test.record)
			if test.wantErr != "" {
				// The invalid cases must be rejected before any bytes are emitted.
				require.ErrorContains(t, err, test.wantErr)
				assert.Empty(t, buffer.String())
				return
			}
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, decodeJSON(t, buffer.String(), &decoded))
			assert.InDelta(t, 0, decoded["total"], 0)
			assert.InDelta(t, 0, decoded["succeeded"], 0)
			assert.InDelta(t, 0, decoded["failed"], 0)
		})
	}
}

func TestSchemaOneRecordNormalizesTimestampToUTC(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, time.August, 27, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	line := encodeRecord(t, &progress.NodeRecord{Node: progress.NodeProjection{
		BlockAddress: "a", BlockKind: "research", Status: progress.StatusWaiting,
	}}, progress.Envelope{RunID: "run-x", Timestamp: timestamp})

	var decoded map[string]any
	require.NoError(t, decodeJSON(t, line, &decoded))
	assert.Equal(t, "2026-08-27T00:00:00Z", decoded["timestamp"])
}

func TestSchemaOneEncoderRejectsShortWrites(t *testing.T) {
	t.Parallel()

	encoder, err := progress.NewEncoder(shortWriter{}, progress.SchemaMajor1)
	require.NoError(t, err)

	err = encoder.EncodeFrame(progress.NewHelloFrame())

	require.ErrorIs(t, err, io.ErrShortWrite)
}

func TestSchemaOneEncoderRejectsNilProtocolInputs(t *testing.T) {
	t.Parallel()

	t.Run("nil writer", func(t *testing.T) {
		t.Parallel()
		encoder, err := progress.NewEncoder(nil, progress.SchemaMajor1)
		assert.Nil(t, encoder)
		require.ErrorContains(t, err, "writer")
	})

	t.Run("typed nil writer", func(t *testing.T) {
		t.Parallel()
		var writer *bytes.Buffer
		encoder, err := progress.NewEncoder(writer, progress.SchemaMajor1)
		assert.Nil(t, encoder)
		require.ErrorContains(t, err, "writer")
	})

	t.Run("nil frame", func(t *testing.T) {
		t.Parallel()
		encoder, err := progress.NewEncoder(io.Discard, progress.SchemaMajor1)
		require.NoError(t, err)
		var encodeErr error
		require.NotPanics(t, func() { encodeErr = encoder.EncodeFrame(nil) })
		require.ErrorContains(t, encodeErr, "frame")
	})

	t.Run("nil record", func(t *testing.T) {
		t.Parallel()
		encoder, err := progress.NewEncoder(io.Discard, progress.SchemaMajor1)
		require.NoError(t, err)
		var record *progress.NodeRecord
		var encodeErr error
		require.NotPanics(t, func() {
			encodeErr = encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, record)
		})
		require.ErrorContains(t, encodeErr, "record")
	})
}

func TestSchemaOneEncoderRejectsUnknownRecordType(t *testing.T) {
	t.Parallel()

	encoder, err := progress.NewEncoder(new(bytes.Buffer), progress.SchemaMajor1)
	require.NoError(t, err)
	err = encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, unknownRecordType{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown record")
}

func TestSchemaOneStructuralRecordsRejectMissingNodes(t *testing.T) {
	t.Parallel()

	encoder, err := progress.NewEncoder(io.Discard, progress.SchemaMajor1)
	require.NoError(t, err)
	for _, record := range []progress.Record{
		&progress.RunSnapshotRecord{},
		&progress.DynamicTasksMaterializedRecord{ParentAddress: "research.dynamic"},
	} {
		require.ErrorContains(t, encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, record), "nodes")
	}
}

func TestSchemaOneEncodeRejectsInvalidRecord(t *testing.T) {
	t.Parallel()

	encoder, err := progress.NewEncoder(new(bytes.Buffer), progress.SchemaMajor1)
	require.NoError(t, err)
	err = encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, &progress.NodeRecord{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "block_address")
}

func TestSchemaOneEncodeRejectsMissingRunID(t *testing.T) {
	t.Parallel()

	encoder, err := progress.NewEncoder(new(bytes.Buffer), progress.SchemaMajor1)
	require.NoError(t, err)
	err = encoder.EncodeRecord(progress.Envelope{}, &progress.NodeRecord{Node: progress.NodeProjection{
		BlockAddress: "a", BlockKind: "research",
	}})
	require.Error(t, err)
	assert.ErrorContains(t, err, "run_id")
}

func TestSchemaOneRejectsRecordClaimingDebugEventName(t *testing.T) {
	t.Parallel()

	// The encoder allowlist is type-based, not name-based. A Record that
	// claims a debug event name (assistant.reasoning_delta) is still rejected
	// because raw debuglog.Event values must never be serialized, even if a
	// future type were to implement the Record interface with such a name.
	encoder, err := progress.NewEncoder(new(bytes.Buffer), progress.SchemaMajor1)
	require.NoError(t, err)
	err = encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, debugEventNameRecord{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown record")
}

// debugEventNameRecord mimics what a debuglog.Event would look like if it
// were a Record: a type whose name collides with an internal debug event.
type debugEventNameRecord struct{}

func (debugEventNameRecord) Type() string    { return "assistant.reasoning_delta" }
func (debugEventNameRecord) Critical() bool  { return true }
func (debugEventNameRecord) Validate() error { return nil }

type unknownRecordType struct{}

func (unknownRecordType) Type() string    { return "future.record" }
func (unknownRecordType) Critical() bool  { return false }
func (unknownRecordType) Validate() error { return nil }

type shortWriter struct{}

func (shortWriter) Write(payload []byte) (int, error) {
	return len(payload) - 1, nil
}
