package debuglog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloseReportsFlushFailure(t *testing.T) {
	t.Parallel()

	recorder, err := NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	require.NoError(t, recorder.Record(Event{
		Kind:    EventMessage,
		Session: SessionResearch,
		Role:    RoleUser,
		Content: "buffered",
	}))
	_, err = recorder.buffer.WriteString("pending")
	require.NoError(t, err)
	require.NoError(t, recorder.file.Close())

	require.ErrorContains(t, recorder.Close(), "flushing debug events")
}

func TestCloseReportsFileCloseFailure(t *testing.T) {
	t.Parallel()

	recorder, err := NewRecorder(t.TempDir(), true)
	require.NoError(t, err)
	require.NoError(t, recorder.file.Close())

	require.ErrorContains(t, recorder.Close(), "closing debug events")
}
