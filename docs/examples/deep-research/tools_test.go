package deepresearch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactToolsRejectPathsOutsideWorkingDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	current := filepath.Join(root, ".r42", "runs", "current", "blocks", "current")
	foreign := filepath.Join(root, ".r42", "runs", "foreign", "blocks", "foreign")
	require.NoError(t, os.MkdirAll(current, 0o700))
	require.NoError(t, os.MkdirAll(foreign, 0o700))

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })

	t.Run("save snapshot", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "save_snapshot"))
		require.NoError(t, compileErr)
		path := filepath.Join(foreign, "snapshots", "source-save.md")

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"snapshot_path": path,
			"content":       "foreign content",
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		assert.NoFileExists(t, path)
	})

	t.Run("submit knowledge", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "submit_knowledge"))
		require.NoError(t, compileErr)
		foreignSnapshot := filepath.Join(foreign, "snapshots", "source-submit.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(foreignSnapshot), 0o700))
		require.NoError(t, os.WriteFile(foreignSnapshot, []byte("quoted text"), 0o600))
		artifactPath := filepath.Join(current, "task", "knowledge.json")

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"artifact_path": artifactPath,
			"subquestion":   "What happened?",
			"knowledge": []any{map[string]any{
				"id": "kb-1", "claim": "A claim", "confidence": "high", "quote_ids": []string{"quote-1"},
			}},
			"quotes": []any{map[string]any{
				"id": "quote-1", "source_title": "Source", "url": "https://example.com/source",
				"snapshot_path": foreignSnapshot, "locator": "paragraph 1", "exact_quote": "quoted text",
			}},
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		assert.NoFileExists(t, artifactPath)
	})

	t.Run("submit conflict resolution", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "submit_conflict_resolution"))
		require.NoError(t, compileErr)
		foreignArtifact := filepath.Join(foreign, "resolution.json")
		upstreamArtifact := filepath.Join(root, ".r42", "runs", "current", "blocks", "upstream", "knowledge.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(upstreamArtifact), 0o700))
		require.NoError(t, os.WriteFile(upstreamArtifact, []byte("{}"), 0o600))

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"artifact_path":      foreignArtifact,
			"topic":              "Topic",
			"reviewed_artifacts": []string{upstreamArtifact},
			"conflicts":          []any{},
			"synthesis_guidance": "Preserve uncertainty.",
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		assert.NoFileExists(t, foreignArtifact)
	})

	t.Run("save snapshot output symlink", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "save_snapshot"))
		require.NoError(t, compileErr)
		foreignTarget := filepath.Join(foreign, "snapshot-target.md")
		require.NoError(t, os.WriteFile(foreignTarget, []byte("original"), 0o600))
		linkedPath := filepath.Join(current, "snapshots", "linked.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(linkedPath), 0o700))
		require.NoError(t, os.Symlink(foreignTarget, linkedPath))

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"snapshot_path": linkedPath,
			"content":       "overwritten",
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		content, readErr := os.ReadFile(foreignTarget)
		require.NoError(t, readErr)
		assert.Equal(t, "original", string(content))
	})

	t.Run("submit knowledge output symlink", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "submit_knowledge"))
		require.NoError(t, compileErr)
		validSnapshot := filepath.Join(current, "snapshots", "valid.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(validSnapshot), 0o700))
		require.NoError(t, os.WriteFile(validSnapshot, []byte("quoted text"), 0o600))
		foreignTarget := filepath.Join(foreign, "knowledge-target.json")
		require.NoError(t, os.WriteFile(foreignTarget, []byte("original"), 0o600))
		linkedArtifact := filepath.Join(current, "linked", "knowledge.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(linkedArtifact), 0o700))
		require.NoError(t, os.Symlink(foreignTarget, linkedArtifact))

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"artifact_path": linkedArtifact,
			"subquestion":   "What happened?",
			"knowledge": []any{map[string]any{
				"id": "kb-1", "claim": "A claim", "confidence": "high", "quote_ids": []string{"quote-1"},
			}},
			"quotes": []any{map[string]any{
				"id": "quote-1", "source_title": "Source", "url": "https://example.com/source",
				"snapshot_path": validSnapshot, "locator": "paragraph 1", "exact_quote": "quoted text",
			}},
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		content, readErr := os.ReadFile(foreignTarget)
		require.NoError(t, readErr)
		assert.Equal(t, "original", string(content))
	})
}

func goToolSource(t *testing.T, name string) string {
	t.Helper()
	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile("tools.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)
	for _, block := range body.Blocks {
		if block.Type != "go_tool" || len(block.Labels) != 1 || block.Labels[0] != name {
			continue
		}
		value, valueDiagnostics := block.Body.Attributes["source"].Expr.Value(nil)
		require.False(t, valueDiagnostics.HasErrors(), valueDiagnostics.Error())
		return value.AsString()
	}
	require.FailNow(t, "go tool not found", "name=%s", name)
	return ""
}

func marshalInput(t *testing.T, input any) json.RawMessage {
	t.Helper()
	value, err := json.Marshal(input)
	require.NoError(t, err)
	return value
}
