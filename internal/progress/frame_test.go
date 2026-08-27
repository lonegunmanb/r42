package progress_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelloFrameEncodeMatchesFixture(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	encoder, err := progress.NewEncoder(&buffer, progress.SchemaMajor1)
	require.NoError(t, err)
	require.NoError(t, encoder.EncodeFrame(progress.NewHelloFrame()))

	assert.Equal(t, fixture(t, "hello.ndjson"), buffer.String())
}

func TestSelectFrameEncodeMatchesFixture(t *testing.T) {
	t.Parallel()

	frame := progress.NewSelectFrame(progress.SchemaMajor1)
	var buffer bytes.Buffer
	encoder, err := progress.NewEncoder(&buffer, progress.SchemaMajor1)
	require.NoError(t, err)
	require.NoError(t, encoder.EncodeFrame(frame))

	assert.Equal(t, fixture(t, "select.ndjson"), buffer.String())
}

func TestReadyFrameEncodeMatchesFixture(t *testing.T) {
	t.Parallel()

	frame := progress.NewReadyFrame(progress.SchemaMajor1)
	var buffer bytes.Buffer
	encoder, err := progress.NewEncoder(&buffer, progress.SchemaMajor1)
	require.NoError(t, err)
	require.NoError(t, encoder.EncodeFrame(frame))

	assert.Equal(t, fixture(t, "ready.ndjson"), buffer.String())
}

func TestPointerFramesEncodeIdentically(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	encoder, err := progress.NewEncoder(&buffer, progress.SchemaMajor1)
	require.NoError(t, err)
	require.NoError(t, encoder.EncodeFrame(&progress.SelectFrame{
		HandshakeVersion: progress.HandshakeVersion, SchemaVersion: progress.SchemaMajor1,
	}))

	assert.Equal(t, fixture(t, "select.ndjson"), buffer.String())
}

func TestSchemaRecordsMatchFixtures(t *testing.T) {
	t.Parallel()

	node := progress.NodeProjection{
		BlockAddress:  "research.root",
		BlockKind:     "research",
		ParentAddress: "module.root",
		Dependencies:  []string{"setup"},
		Phase:         progress.PhaseResearch,
		Status:        progress.StatusRunning,
		Activity:      progress.ActivityTool,
		ToolName:      "web_search",
		Usage: &progress.TokenUsage{
			Input: 100, Output: 50, Reasoning: 25, CacheRead: 10, CacheWrite: 5,
		},
	}
	envelope := progress.Envelope{
		RunID: "run-x", Sequence: 7, Timestamp: time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		record progress.Record
	}{
		{
			name:   "run snapshot",
			record: &progress.RunSnapshotRecord{Nodes: []progress.NodeProjection{node}},
		},
		{
			name: "dynamic tasks materialized",
			record: &progress.DynamicTasksMaterializedRecord{
				ParentAddress: "module.root", Nodes: []progress.NodeProjection{node},
			},
		},
		{
			name:   "node upsert",
			record: &progress.NodeRecord{Node: node},
		},
		{
			name:   "timeline append",
			record: &progress.TimelineRecord{BlockAddress: "research.root", Activity: progress.ActivityTool, Summary: "Running web_search"},
		},
		{
			name:   "run completed",
			record: &progress.RunCompletedRecord{Status: progress.StatusSucceeded, Total: 2, Succeeded: 2},
		},
		{
			name:   "run failed",
			record: &progress.RunFailedRecord{Status: progress.StatusFailed, Summary: "apply failed"},
		},
		{
			name:   "run canceled",
			record: &progress.RunCanceledRecord{Status: progress.StatusCanceled, Summary: "worker canceled"},
		},
	}

	for _, major := range progress.AdvertisedSchemaMajors() {
		for _, test := range tests {
			t.Run(fmt.Sprintf("schema %d/%s", major, test.name), func(t *testing.T) {
				t.Parallel()
				var buffer bytes.Buffer
				encoder, err := progress.NewEncoder(&buffer, major)
				require.NoError(t, err)
				require.NoError(t, encoder.EncodeRecord(envelope, test.record))
				fixtureName := fmt.Sprintf("schema%d-%s.ndjson", major, test.record.Type())
				assert.Equal(t, fixture(t, fixtureName), buffer.String())
			})
		}
	}
}

func TestHandshakeFrameValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		frame   progress.Frame
		wantErr string
	}{
		{
			name:    "hello with wrong handshake version",
			frame:   progress.HelloFrame{HandshakeVersion: 2, Protocol: progress.Protocol, SupportedSchemaVersions: []int{1}, R42Version: progress.R42Version},
			wantErr: "handshake_version",
		},
		{
			name:    "hello with empty advertised list",
			frame:   progress.HelloFrame{HandshakeVersion: progress.HandshakeVersion, Protocol: progress.Protocol, SupportedSchemaVersions: nil, R42Version: progress.R42Version},
			wantErr: "supported_schema_versions",
		},
		{
			name:    "hello with unsupported advertised major",
			frame:   progress.HelloFrame{HandshakeVersion: progress.HandshakeVersion, Protocol: progress.Protocol, SupportedSchemaVersions: []int{1, 99}, R42Version: progress.R42Version},
			wantErr: "schema",
		},
		{
			name:    "hello with empty r42 version",
			frame:   progress.HelloFrame{HandshakeVersion: progress.HandshakeVersion, Protocol: progress.Protocol, SupportedSchemaVersions: []int{progress.SchemaMajor1}},
			wantErr: "r42_version",
		},
		{
			name:    "select with wrong handshake version",
			frame:   progress.SelectFrame{HandshakeVersion: 2, SchemaVersion: progress.SchemaMajor1},
			wantErr: "handshake_version",
		},
		{
			name:    "select with unsupported schema",
			frame:   progress.SelectFrame{HandshakeVersion: progress.HandshakeVersion, SchemaVersion: 99},
			wantErr: "schema_version",
		},
		{
			name:    "ready with wrong handshake version",
			frame:   progress.ReadyFrame{HandshakeVersion: 2, SchemaVersion: progress.SchemaMajor1},
			wantErr: "handshake_version",
		},
		{
			name:    "ready with unsupported schema",
			frame:   progress.ReadyFrame{HandshakeVersion: progress.HandshakeVersion, SchemaVersion: 99},
			wantErr: "schema_version",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.frame.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}
