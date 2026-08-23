package collection

import (
	"os"
	"path/filepath"
	"testing"

	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	corespec "github.com/lonegunmanb/r42/internal/spec"
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

	t.Run("registers description", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "described.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		handler := NewRegisterHandler(NewContext(workspace, 10, nil))

		response := handler.Register(RegisterArgs{Path: file, Description: "Regulatory filing excerpts"})

		require.True(t, response.Accepted)
		require.NotNil(t, response.Output)
		assert.Equal(t, "Regulatory filing excerpts", response.Output.Description)
		require.Len(t, handler.Context().Registry.Snapshots(), 1)
		assert.Equal(t, "Regulatory filing excerpts", handler.Context().Registry.Snapshots()[0].Description)
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

	t.Run("adds source header to path snapshot", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("evidence"), 0o644))

		response := NewRegisterHandler(NewContext(workspace, 10, nil)).Register(RegisterArgs{
			Path: file, Source: "local-record:42",
		})

		require.True(t, response.Accepted)
		content, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Equal(t, "- Source: local-record:42\n\nevidence", string(content))
	})

	t.Run("preserves compatible source header", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name    string
			content string
		}{
			{name: "source", content: "- Source: existing-record\n\nevidence"},
			{name: "legacy URL", content: "- URL: https://example.com/source\n\nevidence"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				workspace := t.TempDir()
				file := filepath.Join(workspace, "evidence.md")
				require.NoError(t, os.WriteFile(file, []byte(tt.content), 0o644))

				response := NewRegisterHandler(NewContext(workspace, 10, nil)).Register(RegisterArgs{
					Path: file, Source: "new-record",
				})

				require.True(t, response.Accepted)
				content, err := os.ReadFile(file)
				require.NoError(t, err)
				assert.Equal(t, tt.content, string(content))
			})
		}
	})

	t.Run("preserves valid URL after empty source metadata", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		file := filepath.Join(workspace, "evidence.md")
		original := "- Source:   \n- URL: https://example.com/ignored\n\nevidence"
		require.NoError(t, os.WriteFile(file, []byte(original), 0o644))

		response := NewRegisterHandler(NewContext(workspace, 10, nil)).Register(RegisterArgs{
			Path: file, Source: "local-record:42",
		})

		require.True(t, response.Accepted)
		content, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Equal(t, original, string(content))
	})

	t.Run("adds source header to retained tool result", func(t *testing.T) {
		t.Parallel()

		handler := NewRegisterHandler(NewContext(t.TempDir(), 10, nil))
		require.NoError(t, handler.Context().Registry.RetainToolResult("call-source", "evidence"))

		response := handler.Register(RegisterArgs{
			SourceToolCallID: "call-source", Source: "database-row:42",
		})

		require.True(t, response.Accepted)
		content, err := os.ReadFile(response.Output.Path)
		require.NoError(t, err)
		assert.Equal(t, "- Source: database-row:42\n\nevidence", string(content))
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
		first := true
		for index := range 3 {
			file := filepath.Join(workspace, "dup-"+string(rune('a'+index))+".md")
			require.NoError(t, os.WriteFile(file, []byte("same"), 0o644))
			response := handler.Register(RegisterArgs{Path: file})
			require.True(t, response.Accepted)
			if index == 0 {
				require.NotNil(t, response.Output)
			}
			if first {
				// The first registration creates a fresh snapshot.
				assert.Equal(t, 1, handler.Context().State.UnreviewedSnapshotCount())
				first = false
			}
		}
		assert.Equal(t, 1, handler.Context().State.UnreviewedSnapshotCount())
	})

	t.Run("default batch size ten enforces checkpoint pending", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 0, nil))
		for index := range 10 {
			file := filepath.Join(workspace, "batch-"+string(rune('a'+index))+".md")
			require.NoError(t, os.WriteFile(file, []byte("content "+string(rune('a'+index))), 0o644))
			response := handler.Register(RegisterArgs{Path: file})
			require.True(t, response.Accepted)
		}
		require.True(t, handler.Context().State.CheckpointPending())
	})
}

func TestRegisterToolHandlerAddsSnapshotToArtifactRegistry(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("source"), 0o600))
	artifacts := artifactpkg.NewRegistry()
	handler := NewRegisterHandler(NewContextWithArtifactRegistry(workspace, 10, nil, artifacts))

	response := handler.Register(RegisterArgs{Path: path, Description: "Primary source"})

	require.True(t, response.Accepted)
	record, err := artifacts.Record(response.Output.ID)
	require.NoError(t, err)
	assert.Equal(t, "Primary source", record.Description)
	assert.Equal(t, artifactpkg.KindSnapshot, record.Kind)
	assert.False(t, record.Ready)

	require.NoError(t, handler.Context().MarkSnapshotReviewed(response.Output.ID))

	record, err = artifacts.Record(response.Output.ID)
	require.NoError(t, err)
	assert.True(t, record.Ready)
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
		assert.Empty(t, handler.Context().Registry.ReviewedSnapshotIDs())
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

	t.Run("empty checkpoint rejects whitespace reason", func(t *testing.T) {
		t.Parallel()

		checkpoint := NewCheckpointHandler(NewContext(t.TempDir(), 10, nil))
		response := checkpoint.Submit(CheckpointArgs{
			EmptyReason:         " \t\n ",
			CollectionExhausted: true,
		})

		assert.False(t, response.Accepted)
		require.NotEmpty(t, response.Issues)
		assert.Equal(t, "empty_checkpoint", response.Issues[0].Code)
	})

	t.Run("empty checkpoint can declare collection exhausted", func(t *testing.T) {
		t.Parallel()

		context := NewContext(t.TempDir(), 10, nil)
		checkpoint := NewCheckpointHandler(context)
		response := checkpoint.Submit(CheckpointArgs{
			EmptyReason:         "configured source tools are exhausted",
			CollectionExhausted: true,
		})

		require.True(t, response.Accepted)
		require.NotNil(t, response.Output)
		assert.True(t, response.Output.CollectionExhausted)
		assert.Equal(t, "configured source tools are exhausted", response.Output.EmptyReason)
		assert.True(t, context.State.CollectionLimitExhausted())
	})

	t.Run("exhaustion requires an empty checkpoint", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		handler := NewRegisterHandler(NewContext(workspace, 10, nil))
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		require.True(t, handler.Register(RegisterArgs{Path: file}).Accepted)

		response := NewCheckpointHandler(handler.Context()).Submit(CheckpointArgs{
			EmptyReason:         "cannot find more",
			CollectionExhausted: true,
		})

		assert.False(t, response.Accepted)
		require.NotEmpty(t, response.Issues)
		assert.Equal(t, "collection_exhausted", response.Issues[0].Code)
	})

	t.Run("exhaustion after an earlier checkpoint means no additional snapshots", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		context := NewContext(workspace, 10, nil)
		register := NewRegisterHandler(context)
		checkpoint := NewCheckpointHandler(context)
		file := filepath.Join(workspace, "evidence.md")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))
		registration := register.Register(RegisterArgs{Path: file})
		require.True(t, registration.Accepted)

		first := checkpoint.Submit(CheckpointArgs{})
		require.True(t, first.Accepted)
		require.NotNil(t, first.Output)
		assert.Equal(t, []string{registration.Output.ID}, first.Output.SnapshotIDs)

		final := checkpoint.Submit(CheckpointArgs{
			EmptyReason:         "supplementary search found no additional sources",
			CollectionExhausted: true,
		})

		require.True(t, final.Accepted)
		require.NotNil(t, final.Output)
		assert.Empty(t, final.Output.SnapshotIDs)
		assert.True(t, final.Output.CollectionExhausted)
		assert.True(t, context.State.CollectionLimitExhausted())
	})

	t.Run("later checkpoint submits only newly registered snapshots", func(t *testing.T) {
		t.Parallel()

		workspace := t.TempDir()
		context := NewContext(workspace, 10, nil)
		register := NewRegisterHandler(context)
		checkpoint := NewCheckpointHandler(context)
		registerFile := func(name, content string) string {
			path := filepath.Join(workspace, name)
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			response := register.Register(RegisterArgs{Path: path})
			require.True(t, response.Accepted)
			return response.Output.ID
		}

		firstID := registerFile("first.md", "first")
		first := checkpoint.Submit(CheckpointArgs{})
		require.True(t, first.Accepted)
		assert.Equal(t, []string{firstID}, first.Output.SnapshotIDs)

		secondID := registerFile("second.md", "second")
		second := checkpoint.Submit(CheckpointArgs{})

		require.True(t, second.Accepted)
		require.NotNil(t, second.Output)
		assert.Equal(t, []string{secondID}, second.Output.SnapshotIDs)
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

func TestProtocolHandlersRejectNilContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		invoke func() corespec.ToolResponse[struct{}]
	}{
		{
			name: "register",
			invoke: func() corespec.ToolResponse[struct{}] {
				response := NewRegisterHandler(nil).Register(RegisterArgs{Path: "snapshot.md"})
				return eraseResponse(response)
			},
		},
		{
			name: "checkpoint",
			invoke: func() corespec.ToolResponse[struct{}] {
				response := NewCheckpointHandler(nil).Submit(CheckpointArgs{EmptyReason: "none"})
				return eraseResponse(response)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var response corespec.ToolResponse[struct{}]
			require.NotPanics(t, func() { response = tt.invoke() })
			assert.False(t, response.Accepted)
			require.NotEmpty(t, response.Issues)
			assert.Equal(t, "context_validation", response.Issues[0].Code)
		})
	}
}

func eraseResponse[T any](response corespec.ToolResponse[T]) corespec.ToolResponse[struct{}] {
	return corespec.ToolResponse[struct{}]{Accepted: response.Accepted, Issues: response.Issues}
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
