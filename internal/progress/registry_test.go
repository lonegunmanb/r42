package progress_test

import (
	"bytes"
	"testing"

	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryRejectsUnsupportedMajor(t *testing.T) {
	t.Parallel()

	_, err := progress.NewEncoder(new(bytes.Buffer), 99)
	require.Error(t, err)
	assert.ErrorContains(t, err, "unsupported schema")
}

func TestRegistryAdvertisesSchemaMajorOne(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []int{progress.SchemaMajor1}, progress.AdvertisedSchemaMajors())
}

func TestRegistryFixtureCoverage(t *testing.T) {
	t.Parallel()

	for _, major := range progress.AdvertisedSchemaMajors() {
		encoder, err := progress.NewEncoder(new(bytes.Buffer), major)
		require.NoError(t, err)
		require.NotNil(t, encoder)
	}
}

func TestInvalidRequiredFieldsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		record  progress.Record
		wantErr string
	}{
		{
			name:    "empty node address",
			record:  &progress.NodeRecord{},
			wantErr: "block_address",
		},
		{
			name:    "empty node kind",
			record:  &progress.NodeRecord{Node: progress.NodeProjection{BlockAddress: "a"}},
			wantErr: "block_kind",
		},
		{
			name:    "invalid status",
			record:  &progress.NodeRecord{Node: progress.NodeProjection{BlockAddress: "a", BlockKind: "research", Status: "watching"}},
			wantErr: "status",
		},
		{
			name:    "invalid phase",
			record:  &progress.NodeRecord{Node: progress.NodeProjection{BlockAddress: "a", BlockKind: "research", Phase: "x", Status: progress.StatusWaiting}},
			wantErr: "phase",
		},
		{
			name:    "invalid activity",
			record:  &progress.NodeRecord{Node: progress.NodeProjection{BlockAddress: "a", BlockKind: "research", Activity: "x", Status: progress.StatusWaiting}},
			wantErr: "activity",
		},
		{
			name:    "invalid tool_name",
			record:  &progress.NodeRecord{Node: progress.NodeProjection{BlockAddress: "a", BlockKind: "research", ToolName: "no spaces", Status: progress.StatusWaiting}},
			wantErr: "tool_name",
		},
		{
			name:    "empty run_completed status",
			record:  &progress.RunCompletedRecord{},
			wantErr: "status",
		},
		{
			name:    "empty run_failed summary",
			record:  &progress.RunFailedRecord{Status: "failed"},
			wantErr: "summary",
		},
		{
			name:    "empty run_canceled summary",
			record:  &progress.RunCanceledRecord{Status: "canceled"},
			wantErr: "summary",
		},
		{
			name:    "negative usage",
			record:  &progress.NodeRecord{Node: progress.NodeProjection{BlockAddress: "a", BlockKind: "research", Status: progress.StatusWaiting, Usage: &progress.TokenUsage{Input: -1}}},
			wantErr: "usage",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.record.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}
