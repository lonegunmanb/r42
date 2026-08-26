package secjury_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/r42/internal/tool/gotool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedPPLXRequest struct {
	method        string
	path          string
	authorization string
	contentType   string
	body          []byte
}

func TestPPLXFinanceSearchRejectsEmptyQuery(t *testing.T) {
	t.Parallel()

	response := invokeTool(t, compileTool(t, "pplx_finance_search"), map[string]any{"query": "  "}, t.TempDir())
	assert.False(t, response.Accepted)
	assert.Contains(t, issueCodes(response), "query_required")
}

func TestPPLXFinanceSearchPreservesAgentResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fixture  string
		expected string
	}{
		{
			name:     "nested result and answer",
			fixture:  `{"output":[{"type":"finance_results","categories":["quote"],"tickers":["4062.T"],"results":[{"category":"quote","tickers":["4062.T"],"content":"price 19910 JPY","sources":["https://www.perplexity.ai/finance/4062.T"]}]},{"type":"message","content":[{"type":"output_text","text":"IBIDEN trades at 19910 JPY."}]}]}`,
			expected: `{"answer":"IBIDEN trades at 19910 JPY.","results":[{"category":"quote","tickers":["4062.T"],"content":"price 19910 JPY","sources":["https://www.perplexity.ai/finance/4062.T"]}]}`,
		},
		{
			name:     "direct result",
			fixture:  `{"output":[{"type":"finance_results","category":"quote","tickers":["4062.T"],"content":"direct price 19910 JPY","sources":["https://www.perplexity.ai/finance/4062.T"]}]}`,
			expected: `{"results":[{"category":"quote","tickers":["4062.T"],"content":"direct price 19910 JPY","sources":["https://www.perplexity.ai/finance/4062.T"]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			program := compilePPLXFinanceToolWithResponse(t, tt.fixture)
			response := invokeTool(t, program, map[string]any{"query": "4062.T current quote"}, t.TempDir())
			require.True(t, response.Accepted, "issues: %#v", response.Issues)
			require.NotNil(t, response.Output)
			assert.JSONEq(t, tt.expected, string(*response.Output))
		})
	}
}

func TestPPLXFinanceSearchSendsOriginalSecJuryRequest(t *testing.T) {
	requests := make(chan recordedPPLXRequest, 1)
	server := newPPLXServer(t, requests, `{"output":[{"type":"finance_results","category":"quote","tickers":["MSFT"],"content":"price 500 USD"}]}`)
	program := compilePPLXToolAgainstServer(t, "pplx_finance_search", server.URL)
	t.Setenv("PPLX_API_KEY", "finance-test-key")

	response := invokeTool(t, program, map[string]any{"query": "  MSFT current quote  "}, t.TempDir())
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	request := <-requests
	assert.Equal(t, http.MethodPost, request.method)
	assert.Equal(t, "/v1/agent", request.path)
	assert.Equal(t, "Bearer finance-test-key", request.authorization)
	assert.Equal(t, "application/json", request.contentType)
	assert.JSONEq(t, `{"model":"perplexity/sonar","input":"MSFT current quote","tools":[{"type":"finance_search"}],"max_steps":3,"max_output_tokens":2048}`, string(request.body))
}

func TestPPLXProSearchAndFetchMatchChokepoint(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"pplx_pro_search", "pplx_fetch"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			secjurySource, ok := goToolSourceFromFile(t, "modules/pplx_tools/main.r42.hcl", name)
			require.True(t, ok)
			chokepointSource, ok := goToolSourceFromFile(t, "../chokepoint/modules/pplx_tools/main.r42.hcl", name)
			require.True(t, ok)
			assert.Equal(t, chokepointSource, secjurySource)
		})
	}
}

func TestPPLXFetchSendsRequestAndWritesEvidenceArtifact(t *testing.T) {
	requests := make(chan recordedPPLXRequest, 1)
	server := newPPLXServer(t, requests, `{"output":[{"type":"fetch_url_results","contents":[{"title":"Example filing","url":"https://example.com/filing","snippet":"Audited revenue was 100."}]}]}`)
	program := compilePPLXToolAgainstServer(t, "pplx_fetch", server.URL)
	t.Setenv("PPLX_API_KEY", "fetch-test-key")
	workspace := t.TempDir()
	artifactDir := filepath.Join(workspace, "artifacts")

	response := invokeTool(t, program, map[string]any{
		"url": "https://example.com/filing", "artifact_dir": artifactDir,
	}, workspace)
	require.True(t, response.Accepted, "issues: %#v", response.Issues)
	request := <-requests
	assert.Equal(t, http.MethodPost, request.method)
	assert.Equal(t, "/v1/agent", request.path)
	assert.Equal(t, "Bearer fetch-test-key", request.authorization)
	assert.JSONEq(t, `{"model":"perplexity/sonar","input":"Fetch and extract the main content from this URL: https://example.com/filing","instructions":"Use fetch_url for the provided URL. Return only information grounded in the fetched content.","tools":[{"type":"fetch_url","max_urls":1}],"max_steps":2}`, string(request.body))

	require.NotNil(t, response.Output)
	var output struct {
		ArtifactPath string `json:"artifact_path"`
	}
	require.NoError(t, json.Unmarshal(*response.Output, &output))
	assert.FileExists(t, output.ArtifactPath)
	artifact, err := os.ReadFile(output.ArtifactPath)
	require.NoError(t, err)
	assert.Contains(t, string(artifact), "# Example filing")
	assert.Contains(t, string(artifact), "Audited revenue was 100.")
	relative, err := filepath.Rel(artifactDir, output.ArtifactPath)
	require.NoError(t, err)
	assert.False(t, relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func compilePPLXFinanceToolWithResponse(t *testing.T, fixture string) *gotool.Program {
	t.Helper()
	source := goToolSource(t, "pplx_finance_search")
	require.Contains(t, source, "func postJSON(")
	source = strings.Replace(source, "func postJSON(", "func originalPostJSON(", 1)
	fixtureLiteral, err := json.Marshal(fixture)
	require.NoError(t, err)
	source += `
func postJSON(_ context.Context, _ string, _ any, output any) error {
  return json.Unmarshal([]byte(` + string(fixtureLiteral) + `), output)
}
`

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), source)
	require.NoError(t, err)
	return program
}

func compilePPLXToolAgainstServer(t *testing.T, name, serverURL string) *gotool.Program {
	t.Helper()
	source := goToolSource(t, name)
	require.Contains(t, source, "https://api.perplexity.ai")
	source = strings.Replace(source, "https://api.perplexity.ai", serverURL, 1)

	compiler, err := gotool.NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, compiler.Close()) })
	program, err := compiler.Compile(t.Context(), source)
	require.NoError(t, err)
	return program
}

func newPPLXServer(t *testing.T, requests chan<- recordedPPLXRequest, responseBody string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- recordedPPLXRequest{
			method: request.Method, path: request.URL.Path,
			authorization: request.Header.Get("Authorization"),
			contentType:   request.Header.Get("Content-Type"), body: body,
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, responseBody)
	}))
	t.Cleanup(server.Close)
	return server
}
