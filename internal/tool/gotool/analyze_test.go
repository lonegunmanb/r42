package gotool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestAnalyze(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     string
		errorText   string
		assertTypes func(*testing.T, Analysis)
	}{
		{
			name:    "valid declarations",
			fixture: "valid.go.txt",
			assertTypes: func(t *testing.T, analysis Analysis) {
				t.Helper()
				assert.True(t, analysis.InputType.IsObjectType())
				assert.True(t, analysis.InputType.AttributeOptional("limit"))
				assert.True(t, analysis.OutputType.Equals(cty.Object(map[string]cty.Type{
					"summary": cty.String,
				})))
			},
		},
		{name: "package clause", fixture: "package.go.txt", errorText: "package clause is not allowed"},
		{name: "main function", fixture: "main.go.txt", errorText: "main function is not allowed"},
		{name: "non-standard import", fixture: "nonstdlib.go.txt", errorText: "import must be from the Go standard library"},
		{name: "missing Input", fixture: "missing_input.go.txt", errorText: "type Input is required"},
		{name: "missing Output", fixture: "missing_output.go.txt", errorText: "type Output is required"},
		{name: "wrong Invoke signature", fixture: "wrong_invoke.go.txt", errorText: "Invoke must have signature"},
		{name: "wrong response type", fixture: "wrong_response.go.txt", errorText: "Invoke must return ToolResponse[Output]"},
		{name: "cty incompatible type", fixture: "incompatible_type.go.txt", errorText: "Input.jobs: unsupported Go type"},
		{name: "custom input JSON", fixture: "custom_json.go.txt", errorText: "Input: custom JSON or text methods are not supported"},
		{name: "nested custom JSON", fixture: "nested_custom_json.go.txt", errorText: "Input.payload: custom JSON or text methods are not supported"},
		{name: "custom response JSON", fixture: "custom_response_json.go.txt", errorText: "ToolResponse: custom JSON or text methods are not supported"},
		{name: "JSON string option", fixture: "json_string.go.txt", errorText: "Input.count: unsupported JSON tag option"},
		{name: "JSON omitzero option", fixture: "json_omitzero.go.txt", errorText: "Input.count: unsupported JSON tag option"},
		{name: "non-string map key", fixture: "map_key.go.txt", errorText: "Input: map keys must be strings"},
		{name: "custom map key text", fixture: "map_key_custom_text.go.txt", errorText: "Input.<key>: custom JSON or text methods are not supported"},
		{name: "embedded field", fixture: "embedded.go.txt", errorText: "Input.Payload: fields must be exported and non-embedded"},
		{name: "unexported field", fixture: "unexported.go.txt", errorText: "Input.value: fields must be exported and non-embedded"},
		{name: "duplicate JSON field", fixture: "duplicate_json.go.txt", errorText: "Input: duplicate JSON field"},
		{name: "recursive type", fixture: "recursive.go.txt", errorText: "recursive Go types are not supported"},
		{name: "imported named type", fixture: "imported_type.go.txt", errorText: "imported named Go types are not supported"},
		{name: "byte slice", fixture: "byte_slice.go.txt", errorText: "[]byte has JSON string semantics"},
		{
			name:    "dash JSON field name",
			fixture: "json_dash_name.go.txt",
			assertTypes: func(t *testing.T, analysis Analysis) {
				t.Helper()
				require.True(t, analysis.InputType.HasAttribute("-"))
				assert.True(t, analysis.InputType.AttributeOptional("-"))
			},
		},
		{
			name:    "invalid JSON field name",
			fixture: "json_invalid_name.go.txt",
			assertTypes: func(t *testing.T, analysis Analysis) {
				t.Helper()
				assert.True(t, analysis.InputType.HasAttribute("Value"))
				assert.False(t, analysis.InputType.HasAttribute("bad\\name"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			analysis, err := Analyze(readFixture(t, tt.fixture))
			if tt.errorText != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errorText)
				return
			}
			require.NoError(t, err)
			tt.assertTypes(t, analysis)
		})
	}
}

func TestAnalyzeMapsSupportedGoTypeFamilies(t *testing.T) {
	t.Parallel()

	analysis, err := Analyze(readFixture(t, "supported_types.go.txt"))
	require.NoError(t, err)

	expectedInput := cty.Object(map[string]cty.Type{
		"enabled": cty.Bool,
		"count":   cty.Number,
		"ratio":   cty.Number,
		"pair":    cty.Tuple([]cty.Type{cty.String, cty.String}),
		"labels":  cty.Map(cty.Number),
	})
	assert.True(t, expectedInput.Equals(analysis.InputType))
	assert.True(t, cty.List(cty.Map(cty.Number)).Equals(analysis.OutputType))
}

func TestAnalyzeTracksQuoteTypePaths(t *testing.T) {
	t.Parallel()

	analysis, err := Analyze(`
import "context"
type Quote string
type Input struct {
  Primary Quote ` + "`json:\"primary\"`" + `
  Many []Quote ` + "`json:\"many\"`" + `
  Nested struct {
    Sources []Quote ` + "`json:\"sources\"`" + `
  } ` + "`json:\"nested\"`" + `
}
type Output string
func Invoke(_ context.Context, _ Input) (ToolResponse[Output], error) { return ToolResponse[Output]{}, nil }
`)
	require.NoError(t, err)
	assert.ElementsMatch(t, [][]string{
		{"primary"},
		{"many", "[]"},
		{"nested", "sources", "[]"},
	}, analysis.QuotePaths)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(content)
}
