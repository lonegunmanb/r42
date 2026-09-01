package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	"github.com/lonegunmanb/r42/internal/collection"
	"github.com/lonegunmanb/r42/internal/collectionqc"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/evidence"
	"github.com/lonegunmanb/r42/internal/qc"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type quoteSchemaContractTool struct {
	name       string
	parameters map[string]any
	bound      bool
	quotePaths [][]string
}

type quoteSchemaProbe struct {
	arguments any
	path      []any
	schema    map[string]any
}

//nolint:paralleltest // This repository-wide compiler scan must not contend with timeout-sensitive runtime tests.
func TestQuoteHydrationSchemaContractCoversExamplesAndBuiltins(t *testing.T) {
	tools := collectQuoteSchemaContractTools(t)
	if len(tools) == 0 {
		t.Fatal("expected example and builtin tools")
	}
	quoteTools := 0
	for _, tool := range tools {
		if len(tool.quotePaths) > 0 || len(quoteSchemaProbes(tool.parameters, "quote-ref-contract")) > 0 {
			quoteTools++
		}
		assertQuoteHydrationCompatible(t, tool)
	}
	assert.GreaterOrEqual(t, quoteTools, 3, "the contract must exercise several nested quote schemas")
}

func collectQuoteSchemaContractTools(t *testing.T) []quoteSchemaContractTool {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	examplesRoot := filepath.Join(repoRoot, "docs", "examples")

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })

	tools := make([]quoteSchemaContractTool, 0)
	err = filepath.WalkDir(examplesRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".hcl" {
			return nil
		}
		sourceBytes, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		parser := hclparse.NewParser()
		file, diagnostics := parser.ParseHCLFile(path)
		if diagnostics.HasErrors() {
			return diagnostics
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			return fmt.Errorf("%s: expected native HCL body", path)
		}
		for _, block := range body.Blocks {
			if len(block.Labels) != 1 || (block.Type != "go_tool" && block.Type != "external_tool") {
				continue
			}
			toolName := filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(path)), block.Type+"."+block.Labels[0]))
			if block.Type == "go_tool" {
				source := hclAttributeString(t, block, "source", sourceBytes)
				program, compileErr := compiler.Compile(t.Context(), source)
				if compileErr != nil {
					return fmt.Errorf("%s: compile %s: %w", path, block.Labels[0], compileErr)
				}
				analysis := program.Analysis()
				parameters, schemaErr := toolspecJSONSchema(analysis.InputType)
				if schemaErr != nil {
					return fmt.Errorf("%s: schema %s: %w", path, block.Labels[0], schemaErr)
				}
				tools = append(tools, quoteSchemaContractTool{name: toolName, parameters: parameters, bound: true, quotePaths: analysis.QuotePaths})
				continue
			}
			inputType := hclAttributeString(t, block, "input_type", sourceBytes)
			constraint, parseErr := parseConstraint(inputType)
			if parseErr != nil {
				return fmt.Errorf("%s: parse input_type %s: %w", path, block.Labels[0], parseErr)
			}
			parameters, schemaErr := constraint.JSONSchema()
			if schemaErr != nil {
				return fmt.Errorf("%s: schema %s: %w", path, block.Labels[0], schemaErr)
			}
			tools = append(tools, quoteSchemaContractTool{name: toolName, parameters: parameters, bound: true})
		}
		return nil
	})
	require.NoError(t, err)

	workspace := t.TempDir()
	registry := artifactpkg.NewRegistry()
	context := collection.NewContextWithArtifactRegistry(workspace, 10, nil, registry)
	tools = append(tools, sdkSchemaContractTools(t, workspace, registry, context)...)
	boundTools := 0
	for _, tool := range tools {
		if tool.bound {
			boundTools++
		}
	}
	require.GreaterOrEqual(t, boundTools, 35, "all example typed tool definitions should be compiled")
	require.GreaterOrEqual(t, len(tools), 20, "examples and builtin tool inventory is unexpectedly small")
	return tools
}

func sdkSchemaContractTools(t *testing.T, workspace string, registry *artifactpkg.Registry, context *collection.Context) []quoteSchemaContractTool {
	t.Helper()
	tools := make([]quoteSchemaContractTool, 0)
	appendTools := func(prefix string, sdkTools []sdk.Tool) {
		for _, tool := range sdkTools {
			tools = append(tools, quoteSchemaContractTool{name: prefix + "/" + tool.Name, parameters: tool.Parameters})
		}
	}
	appendTools("collection", collectionProtocolTools(context, collection.NewCheckpointRecorder()))
	appendTools("collection-qc", []sdk.Tool{collectionQCVerdictTool(context, collectionqc.NewVerdictRecorder())})
	evidenceTools, err := evidenceToolsWithArtifactRegistry(workspace, nil, true, registry, nil, nil)
	require.NoError(t, err)
	appendTools("evidence", evidenceTools)
	readOnlyEvidenceTools, err := evidenceToolsWithArtifactRegistry(workspace, nil, false, registry, nil, nil)
	require.NoError(t, err)
	appendTools("evidence-read-only", readOnlyEvidenceTools)
	appendTools("artifact-only", collectionOnlyArtifactTools(workspace, registry, nil))

	recorder, err := debuglog.NewRecorder(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	appendTools("final-qc", []sdk.Tool{qcVerdictTool("research.static.test", recorder, qc.NewVerdictRecorder())})
	appendTools("final-qc", []sdk.Tool{qcExpandQuoteTool(registry, evidence.NewQuoteRegistry(), func(string) bool { return true })})
	return tools
}

func assertQuoteHydrationCompatible(t *testing.T, tool quoteSchemaContractTool) {
	t.Helper()
	if len(tool.quotePaths) > 0 {
		quotes, quoteRef := contractQuoteRegistry(t)
		for _, path := range tool.quotePaths {
			arguments := quotePathArguments(path, quoteRef)
			unknown, err := materializeQuoteReferences(arguments, [][]string{path}, quotes)
			require.NoError(t, err, "%s: materialize quote path %v", tool.name, path)
			assert.Empty(t, unknown, "%s: quote path %v should resolve", tool.name, path)
			resolved, ok := valueAtStringPath(arguments, path).(string)
			if assert.True(t, ok, "%s: quote path %v should remain a JSON string", tool.name, path) {
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(resolved), &payload), "%s: quote path %v should contain JSON", tool.name, path)
				assert.Equal(t, true, payload["_r42_quote"], "%s: quote path %v must be marked canonical", tool.name, path)
				assert.NotContains(t, payload, "submit_ready", "%s: host-only metadata leaked at path %v", tool.name, path)
			}
		}
	}
	if !tool.bound {
		assertNoSubmitReadyInput(t, tool)
		return
	}
	quotes, quoteRef := contractQuoteRegistry(t)
	probes := quoteSchemaProbes(tool.parameters, quoteRef)
	for _, probe := range probes {
		arguments := probe.arguments
		unknown := resolveQuoteReferences(arguments, quotes)
		assert.Empty(t, unknown, "%s: quote probe path %v should resolve", tool.name, probe.path)
		resolved, ok := valueAtPath(arguments, probe.path).(map[string]any)
		if !assert.True(t, ok, "%s: quote probe path %v must remain an object", tool.name, probe.path) {
			continue
		}
		properties, ok := probe.schema["properties"].(map[string]any)
		if !assert.True(t, ok, "%s: quote schema path %v must expose properties", tool.name, probe.path) {
			continue
		}
		for field := range resolved {
			assert.Contains(t, properties, field, "%s: hydrated field %q at path %v is absent from original schema", tool.name, field, probe.path)
		}
		assert.NotContains(t, resolved, "submit_ready", "%s: output-only submit_ready leaked into typed tool input at path %v", tool.name, probe.path)
	}
}

func quotePathArguments(path []string, quoteRef string) map[string]any {
	value := quotePathValue(path, quoteRef)
	arguments, ok := value.(map[string]any)
	if !ok {
		panic("quote path must start with an object field")
	}
	return arguments
}

func quotePathValue(path []string, quoteRef string) any {
	if len(path) == 0 {
		return quoteRef
	}
	if path[0] == "[]" {
		return []any{quotePathValue(path[1:], quoteRef)}
	}
	if path[0] == "*" {
		return map[string]any{"item": quotePathValue(path[1:], quoteRef)}
	}
	return map[string]any{path[0]: quotePathValue(path[1:], quoteRef)}
}

func valueAtStringPath(value any, path []string) any {
	converted := make([]any, len(path))
	for index, segment := range path {
		if segment == "[]" {
			converted[index] = 0
			continue
		}
		converted[index] = segment
	}
	return valueAtPath(value, converted)
}

func assertNoSubmitReadyInput(t *testing.T, tool quoteSchemaContractTool) {
	t.Helper()
	for _, probe := range quoteSchemaProbes(tool.parameters, "quote-ref-contract") {
		properties, ok := probe.schema["properties"].(map[string]any)
		if assert.True(t, ok, "%s: quote schema path %v must expose properties", tool.name, probe.path) {
			assert.NotContains(t, properties, "submit_ready", "%s: submit_ready must never be an input field", tool.name)
		}
	}
}

func quoteSchemaProbes(schema map[string]any, quoteRef string) []quoteSchemaProbe {
	if properties, ok := schema["properties"].(map[string]any); ok {
		if _, hasQuoteRef := properties["quote_ref"]; hasQuoteRef {
			return []quoteSchemaProbe{{arguments: map[string]any{"quote_ref": quoteRef}, schema: schema}}
		}
		result := make([]quoteSchemaProbe, 0)
		for name, raw := range properties {
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, probe := range quoteSchemaProbes(child, quoteRef) {
				probe.path = append([]any{name}, probe.path...)
				probe.arguments = map[string]any{name: probe.arguments}
				result = append(result, probe)
			}
		}
		return result
	}
	if items, ok := schema["items"].(map[string]any); ok {
		result := make([]quoteSchemaProbe, 0)
		for _, probe := range quoteSchemaProbes(items, quoteRef) {
			probe.path = append([]any{0}, probe.path...)
			probe.arguments = []any{probe.arguments}
			result = append(result, probe)
		}
		return result
	}
	return nil
}

func valueAtPath(value any, path []any) any {
	for _, segment := range path {
		switch typed := segment.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			value = object[typed]
		case int:
			array, ok := value.([]any)
			if !ok || typed < 0 || typed >= len(array) {
				return nil
			}
			value = array[typed]
		}
	}
	return value
}

func hclAttributeString(t *testing.T, block *hclsyntax.Block, name string, source []byte) string {
	t.Helper()
	attribute, ok := block.Body.Attributes[name]
	require.True(t, ok, "%s block %q must define %s", block.Type, block.Labels[0], name)
	if name == "input_type" {
		rangeValue := attribute.Expr.Range()
		require.GreaterOrEqual(t, rangeValue.Start.Byte, 1)
		require.LessOrEqual(t, rangeValue.End.Byte, len(source)+1)
		return strings.ReplaceAll(strings.TrimSpace(string(source[rangeValue.Start.Byte-1:rangeValue.End.Byte])), "\r\n", "\n")
	}
	value, diagnostics := attribute.Expr.Value(nil)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	return value.AsString()
}

func toolspecJSONSchema(inputType cty.Type) (map[string]any, error) {
	return toolspec.NewConstraint(inputType).JSONSchema()
}

func contractQuoteRegistry(t *testing.T) (*evidence.QuoteRegistry, string) {
	t.Helper()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.md")
	require.NoError(t, os.WriteFile(path, []byte("contract quote\n"), 0o600))
	artifacts := artifactpkg.NewRegistry()
	registered, _, err := artifacts.RegisterEvidence(workspace, path, "", "Contract source")
	require.NoError(t, err)
	search, err := evidence.SearchArtifact(artifacts, registered.ID, "contract quote", true, 1, 0)
	require.NoError(t, err)
	require.Len(t, search.Matches, 1)
	quotes := evidence.NewQuoteRegistry()
	captured, err := quotes.CaptureMatch(artifacts, registered.ID, search.Matches[0])
	require.NoError(t, err)
	return quotes, captured.Ref
}
