package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/tool/document"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentToolRegistersEveryMarkItDownArgument(t *testing.T) {
	t.Parallel()
	tool := documentReadTool(t.Context(), t.TempDir(), func(context.Context, string, document.Input) (document.Result, error) { return document.Result{}, nil })
	properties, ok := tool.Parameters["properties"].(map[string]any)
	require.True(t, ok)
	for _, phrase := range []string{"Convert", "Markdown", "PDF", "Word", "Excel", "PowerPoint", "HTML", "CSV"} {
		assert.Contains(t, tool.Description, phrase)
	}
	assert.Len(t, properties, 17)
	for _, name := range []string{"filename", "output", "extension", "mime_type", "charset", "use_docintel", "endpoint", "use_cu", "cu_endpoint", "cu_analyzer", "cu_file_types", "use_plugins", "list_plugins", "keep_data_uris", "version", "help", "stdin"} {
		assert.Contains(t, properties, name)
	}
}

func TestDocumentToolPassesTypedArgumentsAndReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		args any
		err  error
		want string
	}{
		{name: "converted", args: map[string]any{"filename": "report.pdf", "use_plugins": true, "extension": ".pdf"}, want: "# Report"},
		{name: "invalid arguments", args: "invalid", want: "invalid_arguments"},
		{name: "invalid flag type", args: map[string]any{"version": "yes"}, want: "invalid_arguments"},
		{name: "conversion failed", args: map[string]any{"filename": "report.pdf", "use_plugins": true, "extension": ".pdf"}, err: errors.New("missing PDF dependency"), want: "document_conversion_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			tool := documentReadTool(t.Context(), workspace, func(ctx context.Context, root string, input document.Input) (document.Result, error) {
				assert.Equal(t, workspace, root)
				assert.Equal(t, document.Input{Filename: "report.pdf", UsePlugins: true, Extension: ".pdf"}, input)
				assert.NoError(t, ctx.Err())
				return document.Result{Content: "# Report"}, tt.err
			})
			result, err := tool.Handler(sdk.ToolInvocation{Arguments: tt.args})
			require.NoError(t, err)
			assert.Contains(t, result.TextResultForLLM, tt.want)
		})
	}
}

type fakeDocumentRunner struct{ available bool }

func (r fakeDocumentRunner) Available() bool { return r.available }
func (fakeDocumentRunner) Invoke(context.Context, string, document.Input) (document.Result, error) {
	return document.Result{Content: "# Report"}, nil
}

func TestDocumentToolsAreRegisteredOnlyWhenAvailableAtStartup(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		runner documentRunner
		want   int
	}{
		{name: "absent", runner: fakeDocumentRunner{}, want: 0},
		{name: "present", runner: fakeDocumentRunner{available: true}, want: 1},
		{name: "no configured runner", want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			factory := &runtimeFactory{documentRunner: tt.runner}
			assert.Len(t, factory.documentTools(t.Context(), t.TempDir()), tt.want)
		})
	}
}

func TestDocumentToolHonorsInvocationCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tool := documentReadTool(t.Context(), t.TempDir(), func(ctx context.Context, _ string, _ document.Input) (document.Result, error) {
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		return document.Result{}, ctx.Err()
	})
	result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"version": true}, TraceContext: ctx})
	require.NoError(t, err)
	assert.Contains(t, result.TextResultForLLM, "context canceled")
}

func TestDocumentToolHonorsOwnerCancellationWithInvocationContext(t *testing.T) {
	t.Parallel()
	for _, preCanceled := range []bool{false, true} {
		name := "live owner"
		if preCanceled {
			name = "pre-canceled owner"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			owner, cancel := context.WithCancel(t.Context())
			defer cancel()
			if preCanceled {
				cancel()
			}
			tool := documentReadTool(owner, t.TempDir(), func(ctx context.Context, _ string, _ document.Input) (document.Result, error) {
				if !preCanceled {
					require.NoError(t, ctx.Err())
					cancel()
				} else {
					require.ErrorIs(t, ctx.Err(), context.Canceled)
				}
				select {
				case <-ctx.Done():
					require.ErrorIs(t, ctx.Err(), context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("owner cancellation did not reach document invocation")
				}
				return document.Result{}, ctx.Err()
			})
			result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"version": true}, TraceContext: t.Context()})
			require.NoError(t, err)
			assert.Contains(t, result.TextResultForLLM, "context canceled")
		})
	}
}
