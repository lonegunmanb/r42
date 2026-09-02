package deepresearch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepDiveCollectionsCanUseCopiedPPLXTools(t *testing.T) {
	t.Parallel()

	deepResearchModule, err := os.ReadFile("modules/pplx_tools/main.r42.hcl")
	require.NoError(t, err)
	chokepointModule, err := os.ReadFile("../chokepoint/modules/pplx_tools/main.r42.hcl")
	require.NoError(t, err)
	assert.Equal(t, string(chokepointModule), string(deepResearchModule))

	variables, err := os.ReadFile("variables.r42.hcl")
	require.NoError(t, err)
	assert.Contains(t, string(variables), `variable "use_pplx"`)

	configuration, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	main := string(configuration)
	assert.Contains(t, main, `module "pplx_tools"`)
	assert.Contains(t, main, "var.use_pplx ?")
	assert.Equal(t, 3, strings.Count(main, "collection_tool_ids = local.pplx_tool_ids"))
	assert.Equal(t, 3, strings.Count(main, "${local.source_tool_guidance}"))
	assert.Contains(t, main, "pplx_pro_search_tool_id")
	assert.Contains(t, main, "pplx_fetch_tool_id")
	planStart := strings.Index(main, `research "static" "plan"`)
	require.NotEqual(t, -1, planStart)
	planEnd := strings.Index(main[planStart:], "\nresearch ")
	require.NotEqual(t, -1, planEnd)
	assert.NotContains(t, main[planStart:planStart+planEnd], "\n  qc {")
}

func TestDeepResearchClosedSynthesisBlocksSkipCollection(t *testing.T) {
	t.Parallel()

	configuration, err := os.ReadFile("main.r42.hcl")
	require.NoError(t, err)
	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCL(configuration, "main.r42.hcl")
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok)

	found := map[string]bool{}
	for _, block := range body.Blocks {
		if block.Type != "research" || len(block.Labels) != 2 || block.Labels[0] != "static" {
			continue
		}
		if block.Labels[1] != "resolve_conflicts" && block.Labels[1] != "synthesize" {
			continue
		}
		found[block.Labels[1]] = true
		phase, phaseDiagnostics := block.Body.Attributes["phase_mode"].Expr.Value(nil)
		require.False(t, phaseDiagnostics.HasErrors(), phaseDiagnostics.Error())
		assert.Equal(t, "research_only", phase.AsString(), block.Labels[1])
		for _, nested := range block.Body.Blocks {
			assert.NotEqual(t, "collection_qc", nested.Type, block.Labels[1])
		}
	}
	assert.True(t, found["resolve_conflicts"])
	assert.True(t, found["synthesize"])
}

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
		artifactPath := filepath.Join(foreign, "task", "knowledge.json")

		response, invokeErr := program.Invoke(t.Context(), marshalInput(t, map[string]any{
			"_r42_artifact_path": artifactPath,
			"quote_id_prefix":    "task-quote-",
			"subquestion":        "What happened?",
			"knowledge": []any{map[string]any{
				"id": "kb-1", "claim": "A claim", "confidence": "high", "citations": []any{trustedCitation("quote-ref-1")},
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
			"quote_id_prefix":    "task-quote-",
			"subquestion":        "What happened?",
			"knowledge": []any{map[string]any{
				"id": "kb-1", "claim": "A claim", "confidence": "high", "citations": []any{trustedCitation("quote-ref-1")},
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
		"quote_id_prefix":    "task-quote-",
		"subquestion":        "What happened?",
		"knowledge": []any{map[string]any{
			"id": "kb-1", "claim": "A claim", "confidence": "high", "citations": []any{
				trustedCitation("quote-ref-1"), trustedCitation("quote-ref-1"),
			},
		}},
	}), workspace)

	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	require.NotNil(t, response.Output)
	assert.NotContains(t, string(*response.Output), artifactPath)
	var payload struct {
		Knowledge []struct {
			QuoteIDs []string `json:"quote_ids"`
		} `json:"knowledge"`
		Quotes []struct {
			ID       string `json:"id"`
			QuoteRef string `json:"quote_ref"`
		} `json:"quotes"`
	}
	var encoded string
	require.NoError(t, json.Unmarshal([]byte(*response.Output), &encoded))
	require.NoError(t, json.Unmarshal([]byte(encoded), &payload))
	require.Len(t, payload.Knowledge, 1)
	assert.Len(t, payload.Knowledge[0].QuoteIDs, 1)
	require.Len(t, payload.Quotes, 1)
	assert.Equal(t, "quote-ref-1", payload.Quotes[0].QuoteRef)
	assert.Equal(t, payload.Quotes[0].ID, payload.Knowledge[0].QuoteIDs[0])
	assert.FileExists(t, artifactPath)
}

func TestGenerateSourceTableGoToolUsesCanonicalKnowledgeMetadata(t *testing.T) {
	t.Parallel()

	workspace := filepath.Join(t.TempDir(), ".r42", "runs", "current", "blocks", "synthesis")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	reportPath := filepath.Join(workspace, "report.md")
	knowledgePath := filepath.Join(workspace, "knowledge.json")
	require.NoError(t, os.WriteFile(reportPath, []byte("# Report\n\nClaim [task-quote-001]\n\n## Sources\n\n| Quote ID | URL |\n| --- | --- |\n| task-quote-001 | https://wrong.example |\n"), 0o600))
	require.NoError(t, os.WriteFile(knowledgePath, []byte(`{"quotes":[{"id":"task-quote-001","url":"https://canonical.example/source"}]}`), 0o600))

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "generate_source_table"))
	require.NoError(t, err)
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_artifact_id":     "artifact-report",
		"_r42_report_path":       reportPath,
		"knowledge_artifact_ids": []string{"artifact-knowledge"},
		"_r42_knowledge_paths":   []string{knowledgePath},
	}), workspace)
	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	content, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "https://canonical.example/source")
	assert.NotContains(t, string(content), "https://wrong.example")

	second, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_artifact_id": "artifact-report", "_r42_report_path": reportPath,
		"knowledge_artifact_ids": []string{"artifact-knowledge"}, "_r42_knowledge_paths": []string{knowledgePath},
	}), workspace)
	require.NoError(t, err)
	assert.True(t, second.Accepted, "repeat call issues: %#v", second.Issues)
}

func TestGenerateSourceTableGoToolRemovesDerivedQuotesAndLinksURLs(t *testing.T) {
	t.Parallel()

	workspace := filepath.Join(t.TempDir(), ".r42", "runs", "current", "blocks", "synthesis")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	reportPath := filepath.Join(workspace, "report.md")
	knowledgePath := filepath.Join(workspace, "knowledge.json")
	require.NoError(t, os.WriteFile(reportPath, []byte("# Report\n\nDerived [derived-quote-001](https://wrong.example/derived) and sourced [task-quote-001](https://wrong.example/topic-quote-999).\n\n## Sources\n\n| Quote ID | URL |\n| --- | --- |\n| derived-quote-001 | model-derived calculation snapshot |\n| task-quote-001 | https://wrong.example |\n"), 0o600))
	require.NoError(t, os.WriteFile(knowledgePath, []byte(`{"quotes":[
{"id":"derived-quote-001","url":"model-derived calculation snapshot"},
{"id":"task-quote-001","url":"HTTPS://canonical.example/source"}
]}`), 0o600))

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "generate_source_table"))
	require.NoError(t, err)
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_artifact_id":     "artifact-report",
		"_r42_report_path":       reportPath,
		"knowledge_artifact_ids": []string{"artifact-knowledge"},
		"_r42_knowledge_paths":   []string{knowledgePath},
	}), workspace)
	require.NoError(t, err)
	assert.True(t, response.Accepted, "issues: %#v", response.Issues)
	content, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	text := string(content)
	assert.NotContains(t, text, "derived-quote-001")
	assert.Contains(t, text, "[task-quote-001](HTTPS://canonical.example/source)")
	assert.NotContains(t, text, "topic-quote-999")
}

func TestGenerateSourceTableGoToolRejectsSymlinkedArtifacts(t *testing.T) {
	t.Parallel()

	workspace := filepath.Join(t.TempDir(), ".r42", "runs", "current", "blocks", "synthesis")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.MkdirAll(outside, 0o700))
	reportTarget := filepath.Join(outside, "report.md")
	knowledgeTarget := filepath.Join(outside, "knowledge.json")
	require.NoError(t, os.WriteFile(reportTarget, []byte("# Report\n\nClaim [task-quote-001]\n"), 0o600))
	require.NoError(t, os.WriteFile(knowledgeTarget, []byte(`{"quotes":[{"id":"task-quote-001","url":"https://canonical.example/source"}]}`), 0o600))

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), goToolSource(t, "generate_source_table"))
	require.NoError(t, err)

	linkedReport := filepath.Join(workspace, "report.md")
	linkedKnowledge := filepath.Join(workspace, "knowledge.json")
	require.NoError(t, os.Symlink(reportTarget, linkedReport))
	require.NoError(t, os.Symlink(knowledgeTarget, linkedKnowledge))
	response, err := program.Invoke(t.Context(), marshalInput(t, map[string]any{
		"report_artifact_id": "artifact-report", "_r42_report_path": linkedReport,
		"knowledge_artifact_ids": []string{"artifact-knowledge"}, "_r42_knowledge_paths": []string{linkedKnowledge},
	}), workspace)
	require.NoError(t, err)
	assert.False(t, response.Accepted)
	assert.NotEmpty(t, response.Issues)
}

func trustedCitation(ref string) string {
	return `{"_r42_quote":true,"quote_ref":"` + ref + `","source_title":"Source","url":"https://example.com/source","artifact_id":"artifact-123e4567-e89b-12d3-a456-426614174000","artifact_digest":"digest","locator":"line 1","exact_quote":"quoted text"}`
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
