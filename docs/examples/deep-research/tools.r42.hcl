go_tool "submit_knowledge" {
  description = "Submit one subquestion's knowledge records and exact source quotes, validate their links, and write knowledge.json."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "net/url"
      "os"
      "path/filepath"
      "strings"
    )

    type Quote struct {
      ID          string `json:"id"`
      SourceTitle string `json:"source_title"`
      URL         string `json:"url"`
      Locator     string `json:"locator"`
      ExactQuote  string `json:"exact_quote"`
    }

    type KnowledgeItem struct {
      ID         string   `json:"id"`
      Claim      string   `json:"claim"`
      Confidence string   `json:"confidence"`
      QuoteIDs   []string `json:"quote_ids"`
    }

    type Input struct {
      ArtifactPath string          `json:"artifact_path"`
      Subquestion  string          `json:"subquestion"`
      Knowledge    []KnowledgeItem `json:"knowledge"`
      Quotes       []Quote         `json:"quotes"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateKnowledge(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode knowledge artifact: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write knowledge artifact: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateKnowledge(input Input) []Issue {
      issues := make([]Issue, 0)
      if !validBlockArtifactPath(input.ArtifactPath, "knowledge.json") {
        issues = append(issues, newIssue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in knowledge.json"))
      }
      if strings.TrimSpace(input.Subquestion) == "" {
        issues = append(issues, newIssue("subquestion", "subquestion", "must not be empty"))
      }
      if len(input.Knowledge) == 0 {
        issues = append(issues, newIssue("knowledge", "knowledge", "submit at least one knowledge item"))
      }
      if len(input.Quotes) == 0 {
        issues = append(issues, newIssue("quotes", "quotes", "submit at least one exact quote"))
      }

      quoteIDs := make(map[string]struct{}, len(input.Quotes))
      usedQuoteIDs := make(map[string]struct{}, len(input.Quotes))
      for index, quote := range input.Quotes {
        path := fmt.Sprintf("quotes[%d]", index)
        id := strings.TrimSpace(quote.ID)
        if id == "" {
          issues = append(issues, newIssue("quote_id", path+".id", "must not be empty"))
        } else if _, exists := quoteIDs[id]; exists {
          issues = append(issues, newIssue("quote_id", path+".id", "must be unique"))
        } else {
          quoteIDs[id] = struct{}{}
        }
        if strings.TrimSpace(quote.SourceTitle) == "" {
          issues = append(issues, newIssue("source_title", path+".source_title", "must not be empty"))
        }
        if strings.TrimSpace(quote.Locator) == "" {
          issues = append(issues, newIssue("locator", path+".locator", "identify a page, section, paragraph, timestamp, or table"))
        }
        if strings.TrimSpace(quote.ExactQuote) == "" {
          issues = append(issues, newIssue("exact_quote", path+".exact_quote", "must contain verbatim source text"))
        }
        if !validHTTPURL(quote.URL) {
          issues = append(issues, newIssue("quote_url", path+".url", "must be an absolute HTTP or HTTPS URL"))
        }
      }

      knowledgeIDs := make(map[string]struct{}, len(input.Knowledge))
      for index, item := range input.Knowledge {
        path := fmt.Sprintf("knowledge[%d]", index)
        id := strings.TrimSpace(item.ID)
        if id == "" {
          issues = append(issues, newIssue("knowledge_id", path+".id", "must not be empty"))
        } else if _, exists := knowledgeIDs[id]; exists {
          issues = append(issues, newIssue("knowledge_id", path+".id", "must be unique"))
        } else {
          knowledgeIDs[id] = struct{}{}
        }
        if strings.TrimSpace(item.Claim) == "" {
          issues = append(issues, newIssue("claim", path+".claim", "must be a complete, falsifiable claim"))
        }
        switch strings.ToLower(strings.TrimSpace(item.Confidence)) {
        case "high", "medium", "low":
        default:
          issues = append(issues, newIssue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        if len(item.QuoteIDs) == 0 {
          issues = append(issues, newIssue("quote_ids", path+".quote_ids", "reference at least one quote"))
        }
        for quoteIndex, quoteID := range item.QuoteIDs {
          quoteID = strings.TrimSpace(quoteID)
          refPath := fmt.Sprintf("%s.quote_ids[%d]", path, quoteIndex)
          if _, exists := quoteIDs[quoteID]; !exists {
            issues = append(issues, newIssue("quote_reference", refPath, "must reference an ID declared in quotes"))
            continue
          }
          usedQuoteIDs[quoteID] = struct{}{}
        }
      }
      for quoteID := range quoteIDs {
        if _, used := usedQuoteIDs[quoteID]; !used {
          issues = append(issues, newIssue("unused_quote", "quotes", "quote "+quoteID+" is not referenced by any knowledge item"))
        }
      }
      return issues
    }

    func validHTTPURL(raw string) bool {
      parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
      return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
    }

    func validBlockArtifactPath(raw, name string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+name)
    }

    func newIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_conflict_resolution" {
  description = "Submit detected cross-subquestion conflicts and their evidence-backed resolutions, then write resolution.json."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type ConflictDecision struct {
      ID                    string   `json:"id"`
      KnowledgeIDs          []string `json:"knowledge_ids"`
      Description           string   `json:"description"`
      Status                string   `json:"status"`
      Resolution            string   `json:"resolution"`
      Rationale             string   `json:"rationale"`
      PreferredKnowledgeIDs []string `json:"preferred_knowledge_ids"`
      SupportingQuoteIDs    []string `json:"supporting_quote_ids"`
    }

    type Input struct {
      ArtifactPath      string             `json:"artifact_path"`
      Topic             string             `json:"topic"`
      ReviewedArtifacts []string           `json:"reviewed_artifacts"`
      Conflicts         []ConflictDecision `json:"conflicts"`
      SynthesisGuidance string             `json:"synthesis_guidance"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateResolution(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode conflict artifact: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write conflict artifact: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateResolution(input Input) []Issue {
      issues := make([]Issue, 0)
      if !validResolutionArtifactPath(input.ArtifactPath) {
        issues = append(issues, resolutionIssue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in resolution.json"))
      }
      if strings.TrimSpace(input.Topic) == "" {
        issues = append(issues, resolutionIssue("topic", "topic", "must not be empty"))
      }
      if len(input.ReviewedArtifacts) == 0 {
        issues = append(issues, resolutionIssue("reviewed_artifacts", "reviewed_artifacts", "list every upstream knowledge.json artifact"))
      }
      seenArtifacts := make(map[string]struct{}, len(input.ReviewedArtifacts))
      for index, raw := range input.ReviewedArtifacts {
        path := fmt.Sprintf("reviewed_artifacts[%d]", index)
        clean := filepath.Clean(strings.TrimSpace(raw))
        if !filepath.IsAbs(clean) || filepath.Base(clean) != "knowledge.json" {
          issues = append(issues, resolutionIssue("reviewed_artifact", path, "must be an absolute knowledge.json path"))
          continue
        }
        if _, exists := seenArtifacts[clean]; exists {
          issues = append(issues, resolutionIssue("reviewed_artifact", path, "must not be duplicated"))
          continue
        }
        seenArtifacts[clean] = struct{}{}
        if info, err := os.Stat(clean); err != nil || info.IsDir() {
          issues = append(issues, resolutionIssue("reviewed_artifact", path, "must name an existing knowledge.json file"))
        }
      }

      seenConflicts := make(map[string]struct{}, len(input.Conflicts))
      for index, conflict := range input.Conflicts {
        path := fmt.Sprintf("conflicts[%d]", index)
        id := strings.TrimSpace(conflict.ID)
        if id == "" {
          issues = append(issues, resolutionIssue("conflict_id", path+".id", "must not be empty"))
        } else if _, exists := seenConflicts[id]; exists {
          issues = append(issues, resolutionIssue("conflict_id", path+".id", "must be unique"))
        } else {
          seenConflicts[id] = struct{}{}
        }
        if len(conflict.KnowledgeIDs) < 2 {
          issues = append(issues, resolutionIssue("knowledge_ids", path+".knowledge_ids", "identify at least two conflicting knowledge items"))
        }
        if strings.TrimSpace(conflict.Description) == "" {
          issues = append(issues, resolutionIssue("description", path+".description", "describe the contradiction precisely"))
        }
        status := strings.ToLower(strings.TrimSpace(conflict.Status))
        if status != "resolved" && status != "unresolved" {
          issues = append(issues, resolutionIssue("status", path+".status", "must be resolved or unresolved"))
        }
        if strings.TrimSpace(conflict.Resolution) == "" {
          issues = append(issues, resolutionIssue("resolution", path+".resolution", "state the resolution or why uncertainty must be preserved"))
        }
        if strings.TrimSpace(conflict.Rationale) == "" {
          issues = append(issues, resolutionIssue("rationale", path+".rationale", "compare the conflicting evidence"))
        }
        if len(conflict.SupportingQuoteIDs) == 0 {
          issues = append(issues, resolutionIssue("supporting_quote_ids", path+".supporting_quote_ids", "cite quotes supporting the decision"))
        }
      }
      if strings.TrimSpace(input.SynthesisGuidance) == "" {
        issues = append(issues, resolutionIssue("synthesis_guidance", "synthesis_guidance", "explain how the final report should handle the reviewed evidence"))
      }
      return issues
    }

    func validResolutionArtifactPath(raw string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/resolution.json")
    }

    func resolutionIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}
