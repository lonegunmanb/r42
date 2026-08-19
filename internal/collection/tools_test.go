package collection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterToolHandler(t *testing.T) {
	t.Parallel()

	t.Run("registers path", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))

		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		response := handler.Register(RegisterArgs{Path: file})
		require.True(t, response.Accepted)
		require.NotNil(t, response.Output)
		assert.NotEmpty(t, response.Output.ID)
		assert.Equal(t, file, response.Output.Path)
		assert.Empty(t, response.Issues)
	})

	t.Run("registers retained tool result", func(t *testing.T) {
		t.Parallel()

		handler := NewRegisterHandler(NewContext(t.TempDir(), 10, nil))
		require.NoError(t, handler.Context().Registry.RetainToolResult("call-1", "evidence"))
		response := handler.Register(RegisterArgs{SourceToolCallID: "call-1"})
		require.True(t, response.Accepted)
		require.NotNil(t, response.Output)
		assert.FileExists(t, response.Output.Path)
	})

	t.Run("rejects both or neither source", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))

		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		response := handler.Register(RegisterArgs{})
		assert.False(t, response.Accepted)
		assert.Empty(t, response.Output)
		assert.NotEmpty(t, response.Issues)
		assert.Equal(t, "exactly_one_source", response.Issues[0].Code)

		response = handler.Register(RegisterArgs{Path: file, SourceToolCallID: "call-1"})
		assert.False(t, response.Accepted)
		assert.Equal(t, "exactly_one_source", response.Issues[0].Code)
	})

	t.Run("rejects bad path with repairable issue", func(t *testing.T) {
		t.Parallel()

		handler := NewRegisterHandler(NewContext(t.TempDir(), 10, nil))
		response := handler.Register(RegisterArgs{Path: filepath.Join(t.TempDir(), "missing.md")})
		assert.False(t, response.Accepted)
		require.NotEmpty(t, response.Issues)
		assert.Equal(t, "invalid_snapshot_source", response.Issues[0].Code)
	})

	t.Run("rejects unknown retained call", func(t *testing.T) {
		t.Parallel()

		handler := NewRegisterHandler(NewContext(t.TempDir(), 10, nil))
		response := handler.Register(RegisterArgs{SourceToolCallID: "unknown"})
		assert.False(t, response.Accepted)
		require.NotEmpty(t, response.Issues)
		assert.Equal(t, "invalid_snapshot_source", response.Issues[0].Code)
	})

	t.Run("tenth registration enters checkpoint pending", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		for index := range 10 {
			file := filepath.Join(workspace, "evidence-"+string(rune('a'+index))+".md")
			require.NoError(t, os.WriteFile(file, []byte("content "+string(rune('a'+index))), 0o644))
			response := handler.Register(RegisterArgs{Path: file})
			require.True(t, response.Accepted, "registration %d rejected: %v", index, response.Issues)
		}
		require.True(t, handler.Context().State.CheckpointPending())
	})

	t.Run("duplicate content does not advance pending count", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		for index := range 3 {
			file := filepath.Join(workspace, "dup-"+string(rune('a'+index))+".md")
			require.NoError(t, os.WriteFile(file, []byte("same"), 0o644))
			response := handler.Register(RegisterArgs{Path: file})
			require.True(t, response.Accepted)
		}
		assert.Equal(t, 1, handler.Context().State.UnreviewedSnapshotCount())
	})
}

func TestCheckpointToolHandler(t *testing.T) {
	t.Parallel()

	t.Run("submits all unreviewed snapshots", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		ids := make([]string, 3)
		for index := range 3 {
			file := filepath.Join(workspace, "evidence-"+string(rune('a'+index))+".md")
			require.NoError(t, os.WriteFile(file, []byte("content "+string(rune('a'+index))), 0o644))
			response := handler.Register(RegisterArgs{Path: file})
			require.True(t, response.Accepted)
			ids[index] = response.Output.ID
		}
		checkpoint := NewCheckpointHandler(handler.Context())
		response := checkpoint.Submit(CheckpointArgs{})
		require.True(t, response.Accepted)
		require.NotNil(t, response.Output)
		assert.ElementsMatch(t, ids, response.Output.SnapshotIDs)
		assert.Equal(t, 0, handler.Context().State.UnreviewedSnapshotCount())
	})

	t.Run("empty checkpoint requires reason", func(t *testing.T) {
		t.Parallel()

		checkpoint := NewCheckpointHandler(NewContext(t.TempDir(), 10, nil))
		response := checkpoint.Submit(CheckpointArgs{})
		assert.False(t, response.Accepted)
		require.NotEmpty(t, response.Issues)
		assert.Equal(t, "empty_checkpoint", response.Issues[0].Code)

		response = checkpoint.Submit(CheckpointArgs{EmptyReason: "no sources found"})
		require.True(t, response.Accepted)
		require.NotNil(t, response.Output)
		assert.Empty(t, response.Output.SnapshotIDs)
	})

	t.Run("checkpoint cannot omit selected snapshots", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		response := handler.Register(RegisterArgs{Path: file})
		require.True(t, response.Accepted)

		checkpoint := NewCheckpointHandler(handler.Context())
		checkpointResponse := checkpoint.Submit(CheckpointArgs{EmptyReason: "selective"})
		assert.False(t, checkpointResponse.Accepted)
		require.NotEmpty(t, checkpointResponse.Issues)
		assert.Equal(t, "empty_checkpoint", checkpointResponse.Issues[0].Code)
	})
}

func TestAcquisitionGate(t *testing.T) {
	t.Parallel()

	t.Run("rejects new acquisition when pending", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 2, nil))
		for index := range 2 {
			file := filepath.Join(workspace, "evidence-"+string(rune('a'+index))+".md")
			require.NoError(t, os.WriteFile(file, []byte("content "+string(rune('a'+index))), 0o644))
			response := handler.Register(RegisterArgs{Path: file})
			require.True(t, response.Accepted)
		}
		require.True(t, handler.Context().State.CheckpointPending())

		gate := handler.Context().Gate()
		err := gate.Acquire()
		require.Error(t, err)
		assert.ErrorContains(t, err, "checkpoint pending")
	})

	t.Run("allows in-flight completion while pending", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 1, nil))
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		response := handler.Register(RegisterArgs{Path: file})
		require.True(t, response.Accepted)
		require.True(t, handler.Context().State.CheckpointPending())

		// Registration and checkpoint remain available while pending.
		file2 := filepath.Join(workspace, "evidence2.md")
		require.NoError(t, os.WriteFile(file2, []byte("content2"), 0o644))
		registrationResponse := handler.Register(RegisterArgs{Path: file2})
		require.True(t, registrationResponse.Accepted)

		checkpoint := NewCheckpointHandler(handler.Context())
		checkpointResponse := checkpoint.Submit(CheckpointArgs{})
		require.True(t, checkpointResponse.Accepted)
		assert.False(t, handler.Context().State.CheckpointPending())
	})

	t.Run("allows acquisition below batch", func(t *testing.T) {
		t.Parallel()

		context := NewContext(t.TempDir(), 10, nil)
		require.NoError(t, context.BeginWorkflow())
		require.NoError(t, context.Gate().Acquire())
	})
}

func TestContextValidation(t *testing.T) {
	t.Parallel()

	require.Error(t, NewContext("", 10, nil).Validate())
	// Batch size 0 normalizes to the default of 10 in NewContext.
	require.NoError(t, NewContext(t.TempDir(), 0, nil).Validate())
	require.Error(t, NewContext(t.TempDir(), -1, nil).Validate())
	require.NoError(t, NewContext(t.TempDir(), 10, nil).Validate())
}

func TestRegisterHandlerInfrastructureErrors(t *testing.T) {
	t.Parallel()

	t.Run("retain failure surfaces as an error", func(t *testing.T) {
		t.Parallel()

		handler := NewRegisterHandler(NewContext(t.TempDir(), 10, nil))
		err := handler.Context().Registry.RetainToolResult("", "content")
		require.Error(t, err)
		assert.ErrorContains(t, err, "tool call id")
	})
}
