package chokepoint_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/require"
)

func blockDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), ".r42", "runs", "run", "blocks", name)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	return directory
}

func goToolSource(t *testing.T, name string) string {
	t.Helper()
	for _, filename := range []string{"support_tools.r42.hcl", "decision_tools.r42.hcl"} {
		parser := hclparse.NewParser()
		file, diagnostics := parser.ParseHCLFile(filename)
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

func writeJSON(path string, input any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

func readJSON(path string, output any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), output)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	return payload
}

func mapValue[T any](t *testing.T, input map[string]any, key string) T {
	t.Helper()
	return valueAs[T](t, input[key])
}

func valueAs[T any](t *testing.T, input any) T {
	t.Helper()
	value, ok := input.(T)
	require.True(t, ok, "unexpected value type %T", input)
	return value
}

func issueCodes(response gotool.Response) []string {
	codes := make([]string, 0, len(response.Issues))
	for _, issue := range response.Issues {
		codes = append(codes, issue.Code)
	}
	return codes
}
