go_tool "pplx_finance_search" {
  description = "Search Perplexity Finance for quotes, market cap, statements, ratios, estimates, peers, earnings, ownership, or ETFs. Call with one concise financial-data question."

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

    type FinanceResult struct {
      Category string   `json:"category"`
      Tickers  []string `json:"tickers,omitempty"`
      Content  string   `json:"content"`
      Sources  []string `json:"sources,omitempty"`
    }

    type Output struct {
      Answer  string          `json:"answer,omitempty"`
      Results []FinanceResult `json:"results"`
    }

    type agentResponse struct {
      Output []json.RawMessage `json:"output"`
    }

    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      query := strings.TrimSpace(input.Query)
      if query == "" {
        return ToolResponse[Output]{Issues: []Issue{{
          Code: "query_required", Message: "query must not be empty",
        }}}, nil
      }

      body := map[string]any{
        "model": "perplexity/sonar",
        "input": query,
        "tools": []map[string]any{{"type": "finance_search"}},
        "max_steps": 3,
        "max_output_tokens": 2048,
      }
      var response agentResponse
      if err := postJSON(ctx, "/v1/agent", body, &response); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_finance_search: %w", err)
      }

      output := response.financeOutput()
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

    func (response agentResponse) financeOutput() Output {
      output := Output{Results: []FinanceResult{}}
      for _, raw := range response.Output {
        var item struct {
          Type       string            `json:"type"`
          Category   string            `json:"category"`
          Categories []string          `json:"categories"`
          Tickers    []string          `json:"tickers"`
          Sources    []string          `json:"sources"`
          Results    []json.RawMessage `json:"results"`
          Content    json.RawMessage   `json:"content"`
        }
        if json.Unmarshal(raw, &item) != nil {
          continue
        }
        if item.Type == "finance_results" {
          for _, rawResult := range item.Results {
            var result FinanceResult
            if json.Unmarshal(rawResult, &result) == nil {
              output.Results = append(output.Results, cleanFinanceResult(result))
            }
          }
          if len(item.Results) == 0 {
            content := rawText(item.Content)
            if content != "" || len(item.Tickers) > 0 || len(item.Sources) > 0 || len(item.Categories) > 0 || strings.TrimSpace(item.Category) != "" {
              category := strings.TrimSpace(item.Category)
              if category == "" && len(item.Categories) > 0 {
                category = strings.TrimSpace(item.Categories[0])
              }
              output.Results = append(output.Results, cleanFinanceResult(FinanceResult{
                Category: category, Tickers: item.Tickers, Content: content, Sources: item.Sources,
              }))
            }
          }
          continue
        }
        if output.Answer == "" {
          output.Answer = contentAnswer(item.Content)
        }
      }
      return output
    }

    func contentAnswer(raw json.RawMessage) string {
      if text := rawText(raw); text != "" {
        return text
      }
      var blocks []struct {
        Text string `json:"text"`
      }
      if json.Unmarshal(raw, &blocks) != nil {
        return ""
      }
      for _, block := range blocks {
        if text := strings.TrimSpace(block.Text); text != "" {
          return text
        }
      }
      return ""
    }

    func rawText(raw json.RawMessage) string {
      var text string
      if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
        return ""
      }
      return strings.TrimSpace(text)
    }

    func cleanFinanceResult(result FinanceResult) FinanceResult {
      return FinanceResult{
        Category: strings.TrimSpace(result.Category),
        Tickers: cleanStrings(result.Tickers),
        Content: strings.TrimSpace(result.Content),
        Sources: cleanStrings(result.Sources),
      }
    }

    func cleanStrings(values []string) []string {
      if values == nil {
        return nil
      }
      output := make([]string, len(values))
      for index, value := range values {
        output[index] = strings.TrimSpace(value)
      }
      return output
    }
  GO
}

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
  description = "Fetch one HTTP or HTTPS URL through Perplexity and write a uniquely named Markdown evidence artifact under the required absolute artifact_dir within the current Collection workspace. On success it returns artifact_path only; call r42_register_artifact with that path to receive an artifact_id. If it returns the fetch_failed issue, Perplexity could not retrieve usable content: no artifact was written, so do not register a file. Try another source URL or continue with the remaining sources. Do not call r42_save_artifact for this already-written file."

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
      "path/filepath"
      "strings"
      "time"
    )

    type Input struct {
      URL         string `json:"url"`
      ArtifactDir string `json:"artifact_dir"`
    }

    type Output struct {
      Title        string `json:"title"`
      URL          string `json:"url"`
      ArtifactPath string `json:"artifact_path"`
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
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_fetch: resolve collection workspace: %w", err)
      }
      artifactDir, err := requireArtifactDir(input.ArtifactDir, workspace)
      if err != nil {
        return ToolResponse[Output]{Issues: []Issue{{
          Code: "invalid_artifact_dir", Message: err.Error(),
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
      if content == "" || isFetchFailureText(content) {
        return ToolResponse[Output]{Issues: []Issue{{
          Code: "fetch_failed",
          Message: "Perplexity could not retrieve usable content for " + sourceURL + "; no artifact was written. Try another source URL.",
        }}}, nil
      }
      if title == "" {
        title = sourceURL
      }
      fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
      artifactPath, err := writeArtifact(artifactDir, title, fetchedURL, content, fetchedAt)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("pplx_fetch: %w", err)
      }

      output := Output{Title: title, URL: fetchedURL, ArtifactPath: artifactPath, FetchedAt: fetchedAt}
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

    func isFetchFailureText(content string) bool {
      normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
      normalized = strings.Trim(normalized, " .:;!?")
      switch normalized {
      case "unable to retrieve content from the provided url",
        "could not retrieve content from the provided url",
        "failed to retrieve content from the provided url",
        "failed to fetch the provided url":
        return true
      default:
        return false
      }
    }

    func requireHTTPURL(raw string) error {
      parsed, err := url.Parse(raw)
      if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
        return fmt.Errorf("url must be an absolute HTTP or HTTPS URL")
      }
      return nil
    }

    func requireArtifactDir(raw, workspace string) (string, error) {
      if strings.TrimSpace(raw) == "" {
        return "", fmt.Errorf("artifact_dir is required")
      }
      directory, err := filepath.Abs(filepath.Clean(raw))
      if err != nil {
        return "", fmt.Errorf("artifact_dir could not be resolved")
      }
      if !filepath.IsAbs(directory) {
        return "", fmt.Errorf("artifact_dir must be absolute")
      }
      workspaceInfo, err := os.Lstat(workspace)
      if err != nil || !workspaceInfo.IsDir() || workspaceInfo.Mode()&os.ModeSymlink != 0 {
        return "", fmt.Errorf("current Collection workspace must be a real directory")
      }
      resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
      if err != nil {
        return "", fmt.Errorf("current Collection workspace could not be resolved")
      }
      if !pathWithin(resolvedWorkspace, directory) {
        return "", fmt.Errorf("artifact_dir must be inside the current Collection workspace")
      }
      directoryInfo, err := os.Lstat(directory)
      if err == nil {
        if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
          return "", fmt.Errorf("artifact_dir must be a real directory inside the current Collection workspace")
        }
        resolvedDirectory, resolveErr := filepath.EvalSymlinks(directory)
        if resolveErr != nil || !pathWithin(resolvedWorkspace, resolvedDirectory) {
          return "", fmt.Errorf("artifact_dir must not resolve outside the current Collection workspace")
        }
        return directory, nil
      }
      if !os.IsNotExist(err) {
        return "", fmt.Errorf("artifact_dir could not be inspected")
      }
      ancestor, ok := nearestExistingDirectory(filepath.Dir(directory))
      if !ok {
        return "", fmt.Errorf("artifact_dir has no existing parent directory")
      }
      resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
      if err != nil || !pathWithin(resolvedWorkspace, resolvedAncestor) {
        return "", fmt.Errorf("artifact_dir must not traverse a symbolic link outside the current Collection workspace")
      }
      return directory, nil
    }

    func writeArtifact(directory, title, rawURL, content, fetchedAt string) (string, error) {
      artifact := strings.Join([]string{
        "# " + title,
        "",
        "- URL: " + rawURL,
        "- Fetched at: " + fetchedAt,
        "- Artifact source: Perplexity fetch_url",
        "",
        "## Extracted Content",
        "",
        strings.TrimSpace(content),
        "",
      }, "\n")
      if err := os.MkdirAll(directory, 0700); err != nil {
        return "", err
      }
      file, err := os.CreateTemp(directory, "artifact-*.md")
      if err != nil {
        return "", err
      }
      path := file.Name()
      if _, err := file.WriteString(artifact); err != nil {
        _ = file.Close()
        return "", err
      }
      if err := file.Close(); err != nil {
        return "", err
      }
      return filepath.ToSlash(path), nil
    }

    func nearestExistingDirectory(path string) (string, bool) {
      for {
        info, err := os.Lstat(path)
        if err == nil {
          return path, info.IsDir()
        }
        if !os.IsNotExist(err) {
          return "", false
        }
        parent := filepath.Dir(path)
        if parent == path {
          return "", false
        }
        path = parent
      }
    }

    func pathWithin(root, path string) bool {
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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

output "pplx_finance_search_tool_id" {
  description = "Generated ID of the Perplexity finance search tool."
  value       = go_tool.pplx_finance_search.id
}

output "pplx_pro_search_tool_id" {
  description = "Generated ID of the Perplexity search tool."
  value       = go_tool.pplx_pro_search.id
}

output "pplx_fetch_tool_id" {
  description = "Generated ID of the Perplexity fetch tool."
  value       = go_tool.pplx_fetch.id
}
