go_tool "pplx_pro_search" {
  description = "Search current web sources with the Perplexity Search API. Call with one concise query."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "io"
      "net/http"
      "net/url"
      "os"
      "strings"
    )

    type Input struct {
      Query string `json:"query"`
    }

    type SearchResult struct {
      Title       string `json:"title"`
      URL         string `json:"url"`
      Snippet     string `json:"snippet"`
      Date        string `json:"date"`
      LastUpdated string `json:"last_updated"`
      Source      string `json:"source"`
    }

    type Output struct {
      Results []SearchResult `json:"results"`
    }

    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      query := strings.TrimSpace(input.Query)
      if query == "" {
        return ToolResponse[Output]{Issues: []Issue{{
          Code: "query_required", Message: "query must not be empty",
        }}}, nil
      }

      var response struct {
        Results []SearchResult `json:"results"`
      }
      if err := postJSON(ctx, "/search", map[string]any{"query": query}, &response); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_pro_search: %w", err)
      }

      results := dedupeResults(response.Results)
      output := Output{Results: results}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func postJSON(ctx context.Context, path string, body, output any) error {
      apiKey := strings.TrimSpace(os.Getenv("PPLX_API_KEY"))
      if apiKey == "" {
        return fmt.Errorf("PPLX_API_KEY is required")
      }
      payload, err := json.Marshal(body)
      if err != nil {
        return err
      }
      endpoint, err := url.JoinPath("https://api.perplexity.ai", path)
      if err != nil {
        return err
      }
      request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
      if err != nil {
        return err
      }
      request.Header.Set("Authorization", "Bearer "+apiKey)
      request.Header.Set("Content-Type", "application/json")
      response, err := http.DefaultClient.Do(request)
      if err != nil {
        return err
      }
      defer response.Body.Close()
      responsePayload, err := io.ReadAll(io.LimitReader(response.Body, 10<<20))
      if err != nil {
        return err
      }
      if response.StatusCode < 200 || response.StatusCode >= 300 {
        return fmt.Errorf("pplx api status %d: %s", response.StatusCode, strings.TrimSpace(string(responsePayload)))
      }
      return json.Unmarshal(responsePayload, output)
    }

    func dedupeResults(results []SearchResult) []SearchResult {
      seen := map[string]struct{}{}
      output := make([]SearchResult, 0, len(results))
      for _, result := range results {
        result = cleanResult(result)
        key := result.URL
        if key == "" {
          key = result.Title + "\x00" + result.Snippet
        }
        if strings.TrimSpace(key) == "" {
          continue
        }
        if _, exists := seen[key]; exists {
          continue
        }
        seen[key] = struct{}{}
        output = append(output, result)
      }
      return output
    }

    func cleanResult(result SearchResult) SearchResult {
      return SearchResult{
        Title: strings.TrimSpace(result.Title), URL: strings.TrimSpace(result.URL),
        Snippet: strings.TrimSpace(result.Snippet), Date: strings.TrimSpace(result.Date),
        LastUpdated: strings.TrimSpace(result.LastUpdated), Source: strings.TrimSpace(result.Source),
      }
    }
  GO
}

go_tool "pplx_fetch" {
  description = "Fetch one HTTP or HTTPS URL through Perplexity and write a URL-addressed Markdown snapshot under the required absolute snapshot_dir."

  source = <<-GO
    import (
      "bytes"
      "context"
      "crypto/sha256"
      "encoding/hex"
      "encoding/json"
      "fmt"
      "io"
      "net/http"
      "net/url"
      "os"
      "path/filepath"
      "strings"
      "time"
    )

    type Input struct {
      URL         string `json:"url"`
      SnapshotDir string `json:"snapshot_dir"`
    }

    type Output struct {
      Title        string `json:"title"`
      URL          string `json:"url"`
      SnapshotPath string `json:"snapshot_path"`
      FetchedAt    string `json:"fetched_at"`
    }

    type SearchResult struct {
      Title   string `json:"title"`
      URL     string `json:"url"`
      Snippet string `json:"snippet"`
    }

    type agentResponse struct {
      Output []json.RawMessage `json:"output"`
    }

    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      sourceURL := strings.TrimSpace(input.URL)
      if err := requireHTTPURL(sourceURL); err != nil {
        return ToolResponse[Output]{Issues: []Issue{{
          Code: "invalid_url", Message: err.Error(),
        }}}, nil
      }
      snapshotDir, err := requireSnapshotDir(input.SnapshotDir)
      if err != nil {
        return ToolResponse[Output]{Issues: []Issue{{
          Code: "invalid_snapshot_dir", Message: err.Error(),
        }}}, nil
      }

      body := map[string]any{
        "model": "perplexity/sonar",
        "input": "Fetch and extract the main content from this URL: " + sourceURL,
        "instructions": "Use fetch_url for the provided URL. Return only information grounded in the fetched content.",
        "tools": []map[string]any{{"type": "fetch_url", "max_urls": 1}},
        "max_steps": 2,
      }
      var response agentResponse
      if err := postJSON(ctx, "/v1/agent", body, &response); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_fetch: %w", err)
      }

      title, fetchedURL, content := response.fetchedContent(sourceURL)
      if content == "" {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_fetch: fetched content is empty")
      }
      if title == "" {
        title = sourceURL
      }
      fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
      snapshotPath, err := writeSnapshot(snapshotDir, title, fetchedURL, content, fetchedAt)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_fetch: %w", err)
      }

      output := Output{Title: title, URL: fetchedURL, SnapshotPath: snapshotPath, FetchedAt: fetchedAt}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func postJSON(ctx context.Context, path string, body, output any) error {
      apiKey := strings.TrimSpace(os.Getenv("PPLX_API_KEY"))
      if apiKey == "" {
        return fmt.Errorf("PPLX_API_KEY is required")
      }
      payload, err := json.Marshal(body)
      if err != nil {
        return err
      }
      endpoint, err := url.JoinPath("https://api.perplexity.ai", path)
      if err != nil {
        return err
      }
      request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
      if err != nil {
        return err
      }
      request.Header.Set("Authorization", "Bearer "+apiKey)
      request.Header.Set("Content-Type", "application/json")
      response, err := http.DefaultClient.Do(request)
      if err != nil {
        return err
      }
      defer response.Body.Close()
      responsePayload, err := io.ReadAll(io.LimitReader(response.Body, 10<<20))
      if err != nil {
        return err
      }
      if response.StatusCode < 200 || response.StatusCode >= 300 {
        return fmt.Errorf("pplx api status %d: %s", response.StatusCode, strings.TrimSpace(string(responsePayload)))
      }
      return json.Unmarshal(responsePayload, output)
    }

    func (response agentResponse) fetchedContent(requestedURL string) (string, string, string) {
      fallback := ""
      for _, raw := range response.Output {
        var item struct {
          Contents []SearchResult `json:"contents"`
          Content  json.RawMessage `json:"content"`
        }
        if json.Unmarshal(raw, &item) != nil {
          continue
        }
        for _, content := range item.Contents {
          snippet := strings.TrimSpace(content.Snippet)
          if snippet != "" {
            return strings.TrimSpace(content.Title), firstNonEmpty(content.URL, requestedURL), snippet
          }
        }
        if fallback == "" {
          fallback = contentText(item.Content)
        }
      }
      return requestedURL, requestedURL, fallback
    }

    func contentText(raw json.RawMessage) string {
      if len(raw) == 0 {
        return ""
      }
      var text string
      if json.Unmarshal(raw, &text) == nil {
        return strings.TrimSpace(text)
      }
      var blocks []struct {
        Text string `json:"text"`
      }
      if json.Unmarshal(raw, &blocks) != nil {
        return ""
      }
      values := make([]string, 0, len(blocks))
      for _, block := range blocks {
        if text := strings.TrimSpace(block.Text); text != "" {
          values = append(values, text)
        }
      }
      return strings.Join(values, "\n\n")
    }

    func requireHTTPURL(raw string) error {
      parsed, err := url.Parse(raw)
      if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
        return fmt.Errorf("url must be an absolute HTTP or HTTPS URL")
      }
      return nil
    }

    func requireSnapshotDir(raw string) (string, error) {
      if strings.TrimSpace(raw) == "" {
        return "", fmt.Errorf("snapshot_dir is required")
      }
      directory := filepath.Clean(raw)
      if !filepath.IsAbs(directory) {
        return "", fmt.Errorf("snapshot_dir must be absolute")
      }
      return directory, nil
    }

    func writeSnapshot(directory, title, rawURL, content, fetchedAt string) (string, error) {
      digest := sha256.Sum256([]byte(rawURL))
      name := "snapshot-" + hex.EncodeToString(digest[:8]) + ".md"
      path := filepath.Join(directory, name)
      snapshot := strings.Join([]string{
        "# " + title,
        "",
        "- URL: " + rawURL,
        "- Fetched at: " + fetchedAt,
        "- Snapshot source: Perplexity fetch_url",
        "",
        "## Extracted Content",
        "",
        strings.TrimSpace(content),
        "",
      }, "\n")
      if err := os.MkdirAll(directory, 0755); err != nil {
        return "", err
      }
      if err := os.WriteFile(path, []byte(snapshot), 0600); err != nil {
        return "", err
      }
      return filepath.ToSlash(path), nil
    }

    func firstNonEmpty(values ...string) string {
      for _, value := range values {
        if strings.TrimSpace(value) != "" {
          return strings.TrimSpace(value)
        }
      }
      return ""
    }
  GO
}

output "pplx_pro_search_tool_id" {
  description = "Generated ID of the Perplexity search tool."
  value       = go_tool.pplx_pro_search.id
}

output "pplx_fetch_tool_id" {
  description = "Generated ID of the Perplexity fetch tool."
  value       = go_tool.pplx_fetch.id
}
