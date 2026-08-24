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

	t.Run("submit knowledge", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "submit_knowledge"))
		require.NoError(t, compileErr)
		artifactPath := filepath.Join(current, "task", "knowledge.json")

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"_r42_artifact_path": artifactPath,
			"subquestion":        "What happened?",
			"knowledge": []any{map[string]any{
				"id": "kb-1", "claim": "A claim", "confidence": "high", "quote_ids": []string{"quote-1"},
			}},
			"quotes": []any{map[string]any{
				"id": "quote-1", "source_title": "Source", "url": "https://example.com/source",
				"artifact_id": "invalid", "locator": "paragraph 1", "exact_quote": "quoted text",
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
			"_r42_artifact_path": foreignArtifact,
			"topic":              "Topic",
			"reviewed_artifacts": []string{upstreamArtifact},
			"conflicts":          []any{},
			"synthesis_guidance": "Preserve uncertainty.",
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		assert.NoFileExists(t, foreignArtifact)
	})

	t.Run("submit knowledge output symlink", func(t *testing.T) {
		t.Parallel()
		program, compileErr := compiler.Compile(t.Context(), goToolSource(t, "submit_knowledge"))
		require.NoError(t, compileErr)
		foreignTarget := filepath.Join(foreign, "knowledge-target.json")
		require.NoError(t, os.WriteFile(foreignTarget, []byte("original"), 0o600))
		linkedArtifact := filepath.Join(current, "linked", "knowledge.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(linkedArtifact), 0o700))
		require.NoError(t, os.Symlink(foreignTarget, linkedArtifact))

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"_r42_artifact_path": linkedArtifact,
			"subquestion":        "What happened?",
			"knowledge": []any{map[string]any{
				"id": "kb-1", "claim": "A claim", "confidence": "high", "quote_ids": []string{"quote-1"},
			}},
			"quotes": []any{map[string]any{
				"id": "quote-1", "source_title": "Source", "url": "https://example.com/source",
				"artifact_id": "artifact-33333333333333333333333333333333", "locator": "paragraph 1", "exact_quote": "quoted text",
			}},
		}), current)

		require.NoError(t, invokeErr)
		assert.False(t, response.Accepted)
		content, readErr := os.ReadFile(foreignTarget)
		require.NoError(t, readErr)
		assert.Equal(t, "original", string(content))
	})
}

func TestSubmitKnowledgeAcceptsBuiltinUUIDArtifactID(t *testing.T) {
	t.Parallel()

	workspace := filepath.Join(t.TempDir(), ".r42", "runs", "current", "blocks", "knowledge")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "submit_knowledge"))
	require.NoError(t, err)
	artifactPath := filepath.Join(workspace, "knowledge.json")

	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"_r42_artifact_path": artifactPath,
		"subquestion":        "What happened?",
		"knowledge": []any{map[string]any{
			"id": "kb-1", "claim": "A claim", "confidence": "high", "quote_ids": []string{"quote-1"},
		}},
		"quotes": []any{map[string]any{
			"id": "quote-1", "source_title": "Source", "url": "https://example.com/source",
			"artifact_id": "artifact-123e4567-e89b-12d3-a456-426614174000", "locator": "paragraph 1", "exact_quote": "quoted text",
		}},
	}), workspace)

	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	require.NotNil(t, response.Output)
	assert.NotContains(t, string(*response.Output), artifactPath)
	assert.FileExists(t, artifactPath)
}

func TestTypedToolDescriptionsPublishAllowedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool    string
		field   string
		allowed []string
	}{
		{tool: "submit_knowledge", field: "knowledge.confidence", allowed: []string{"high", "medium", "low"}},
		{tool: "submit_conflict_resolution", field: "conflicts.status", allowed: []string{"resolved", "unresolved"}},
	}

	for _, tt := range tests {
		t.Run(tt.tool+"/"+tt.field, func(t *testing.T) {
			t.Parallel()

			description := goToolDescription(t, tt.tool)
			assert.Contains(t, description, "`"+tt.field+"`")
			for _, value := range tt.allowed {
				assert.Contains(t, description, "`"+value+"`", "allowed value %q must be published", value)
			}
		})
	}
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

func goToolDescription(t *testing.T, name string) string {
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
		value, valueDiagnostics := block.Body.Attributes["description"].Expr.Value(nil)
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
