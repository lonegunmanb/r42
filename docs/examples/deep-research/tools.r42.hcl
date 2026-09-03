go_tool "submit_research_plan" {
  description = "Submit a topic decomposition containing parallel tasks, serial tasks that start alongside them, and final serial tasks that wait for both earlier groups. Any group may be empty, but the complete plan must contain at least one task."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "strings"
    )

    type ResearchTask struct {
      ID           string `json:"id"`
      Subquestion  string `json:"subquestion"`
      Instructions string `json:"instructions"`
    }

    type Input struct {
      Topic                  string         `json:"topic"`
      ParallelTasks          []ResearchTask `json:"parallel_tasks"`
      IndependentSerialTasks []ResearchTask `json:"independent_serial_tasks"`
      FinalSerialTasks       []ResearchTask `json:"final_serial_tasks"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateResearchPlan(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      payload, err := json.Marshal(input)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode research plan: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateResearchPlan(input Input) []Issue {
      issues := make([]Issue, 0)
      if strings.TrimSpace(input.Topic) == "" {
        issues = append(issues, researchPlanIssue("topic", "topic", "must not be empty"))
      }

      groups := []struct {
        name  string
        tasks []ResearchTask
      }{
        {name: "parallel_tasks", tasks: input.ParallelTasks},
        {name: "independent_serial_tasks", tasks: input.IndependentSerialTasks},
        {name: "final_serial_tasks", tasks: input.FinalSerialTasks},
      }
      taskCount := 0
      taskIDs := make(map[string]struct{})
      for _, group := range groups {
        taskCount += len(group.tasks)
        for index, task := range group.tasks {
          path := fmt.Sprintf("%s[%d]", group.name, index)
          id := strings.TrimSpace(task.ID)
          if !validResearchTaskID(id) {
            issues = append(issues, researchPlanIssue(
              "task_id", path+".id",
              "must contain 1-64 lowercase letters, digits, hyphens, or underscores and start with a letter or digit",
            ))
          } else if _, exists := taskIDs[id]; exists {
            issues = append(issues, researchPlanIssue("task_id", path+".id", "must be globally unique across all groups"))
          } else {
            taskIDs[id] = struct{}{}
          }
          if strings.TrimSpace(task.Subquestion) == "" {
            issues = append(issues, researchPlanIssue("subquestion", path+".subquestion", "must not be empty"))
          }
          if strings.TrimSpace(task.Instructions) == "" {
            issues = append(issues, researchPlanIssue("instructions", path+".instructions", "must define the task's evidence and reasoning scope"))
          }
        }
      }
      if taskCount == 0 {
        issues = append(issues, researchPlanIssue("tasks", "parallel_tasks", "the three groups must contain at least one task in total"))
      }
      return issues
    }

    func validResearchTaskID(value string) bool {
      if len(value) == 0 || len(value) > 64 {
        return false
      }
      for index, character := range value {
        letter := character >= 'a' && character <= 'z'
        digit := character >= '0' && character <= '9'
        if index == 0 && !letter && !digit {
          return false
        }
        if !letter && !digit && character != '-' && character != '_' {
          return false
        }
      }
      return true
    }

    func researchPlanIssue(code, path, message string) Issue {
      repair := "Correct the research plan and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_knowledge" {
  description = "Submit one subquestion's knowledge claims using trusted quote_ref values returned by r42_search_artifact or r42_capture_quote, then write the declared knowledge artifact. `knowledge.confidence` allowed values: `high`, `medium`, `low`."

  source = <<-GO
    import (
      "context"
      "crypto/sha256"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Quote string

    type KnowledgeInput struct {
      ID         string     `json:"id"`
      Claim      string     `json:"claim"`
      Confidence string     `json:"confidence"`
      Citations  []Quote    `json:"citations"`
    }

    type Input struct {
      ArtifactID    string           `json:"artifact_id"`
      ArtifactPath  string           `json:"_r42_artifact_path"`
      QuoteIDPrefix string           `json:"quote_id_prefix"`
      Subquestion   string           `json:"subquestion"`
      Knowledge     []KnowledgeInput `json:"knowledge"`
    }

    type QuoteRecord struct {
      ID             string `json:"id"`
      QuoteRef       string `json:"quote_ref"`
      SourceTitle    string `json:"source_title"`
      URL            string `json:"url"`
      ArtifactID     string `json:"artifact_id"`
      ArtifactDigest string `json:"artifact_digest"`
      Locator        string `json:"locator"`
      ExactQuote     string `json:"exact_quote"`
    }

    type KnowledgeItem struct {
      ID         string   `json:"id"`
      Claim      string   `json:"claim"`
      Confidence string   `json:"confidence"`
      QuoteIDs   []string `json:"quote_ids"`
    }

    type KnowledgeArtifact struct {
      ArtifactID  string          `json:"artifact_id"`
      Subquestion string          `json:"subquestion"`
      Knowledge   []KnowledgeItem `json:"knowledge"`
      Quotes      []QuoteRecord   `json:"quotes"`
    }

    type Output string

    func decodeQuote(raw Quote) (QuoteRecord, error) {
      var quote QuoteRecord
      if err := json.Unmarshal([]byte(raw), &quote); err != nil {
        return QuoteRecord{}, fmt.Errorf("decode resolved quote: %w", err)
      }
      if strings.TrimSpace(quote.QuoteRef) == "" || strings.TrimSpace(quote.ExactQuote) == "" {
        return QuoteRecord{}, fmt.Errorf("resolved quote is missing quote_ref or exact_quote")
      }
      return quote, nil
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateKnowledge(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      artifact := buildKnowledgeArtifact(input)
      payload, err := json.MarshalIndent(artifact, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode knowledge artifact: %w", err)
      }
      payload = append(payload, '\n')
      artifactPath := filepath.Clean(input.ArtifactPath)
      if err := os.MkdirAll(filepath.Dir(artifactPath), 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create knowledge artifact directory: %w", err)
      }
      if err := os.WriteFile(artifactPath, payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write knowledge artifact: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func buildKnowledgeArtifact(input Input) KnowledgeArtifact {
      artifact := KnowledgeArtifact{
        ArtifactID: input.ArtifactID,
        Subquestion: input.Subquestion,
        Knowledge: make([]KnowledgeItem, 0, len(input.Knowledge)),
        Quotes: make([]QuoteRecord, 0),
      }
      quoteIDs := make(map[string]string)
      for _, item := range input.Knowledge {
        outputItem := KnowledgeItem{ID: item.ID, Claim: item.Claim, Confidence: item.Confidence, QuoteIDs: make([]string, 0, len(item.Citations))}
        seen := make(map[string]struct{}, len(item.Citations))
        for _, rawQuote := range item.Citations {
          citation, _ := decodeQuote(rawQuote)
          quoteID, exists := quoteIDs[citation.QuoteRef]
          if !exists {
            digest := sha256.Sum256([]byte(citation.QuoteRef))
            quoteID = fmt.Sprintf("%s%x", input.QuoteIDPrefix, digest[:16])
            quoteIDs[citation.QuoteRef] = quoteID
            artifact.Quotes = append(artifact.Quotes, QuoteRecord{
              ID: quoteID, QuoteRef: citation.QuoteRef,
              SourceTitle: citation.SourceTitle, URL: citation.URL,
              ArtifactID: citation.ArtifactID, ArtifactDigest: citation.ArtifactDigest,
              Locator: citation.Locator, ExactQuote: citation.ExactQuote,
            })
          }
          if _, duplicate := seen[quoteID]; !duplicate {
            outputItem.QuoteIDs = append(outputItem.QuoteIDs, quoteID)
            seen[quoteID] = struct{}{}
          }
        }
        artifact.Knowledge = append(artifact.Knowledge, outputItem)
      }
      return artifact
    }

    func validateKnowledge(input Input) []Issue {
      issues := make([]Issue, 0)
      if !validBlockArtifactPath(input.ArtifactPath, "knowledge.json") {
        issues = append(issues, newIssue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in knowledge.json"))
      }
      if strings.TrimSpace(input.Subquestion) == "" {
        issues = append(issues, newIssue("subquestion", "subquestion", "must not be empty"))
      }
      if strings.TrimSpace(input.QuoteIDPrefix) == "" {
        issues = append(issues, newIssue("quote_id_prefix", "quote_id_prefix", "must not be empty"))
      }
      if len(input.Knowledge) == 0 {
        issues = append(issues, newIssue("knowledge", "knowledge", "submit at least one knowledge item"))
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
        if len(item.Citations) == 0 {
          issues = append(issues, newIssue("citations", path+".citations", "reference at least one trusted quote_ref"))
        }
        for citationIndex, rawQuote := range item.Citations {
          citationPath := fmt.Sprintf("%s.citations[%d]", path, citationIndex)
          if strings.TrimSpace(string(rawQuote)) == "" {
            issues = append(issues, newIssue("quote_ref", citationPath, "must be returned by r42_search_artifact or r42_capture_quote"))
            continue
          }
          if _, err := decodeQuote(rawQuote); err != nil {
            issues = append(issues, newIssue("quote_ref", citationPath, err.Error()))
          }
        }
      }
      return issues
    }

    func validBlockArtifactPath(raw, name string) bool {
      workspace, err := os.Getwd()
      if err != nil {
        return false
      }
      return safePathUnderRoot(filepath.Clean(strings.TrimSpace(raw)), workspace, name, false)
    }

    func safePathUnderRoot(path, root, expectedName string, requireExisting bool) bool {
      if !filepath.IsAbs(path) || !pathWithin(root, path) {
        return false
      }
      if strings.HasPrefix(expectedName, ".") {
        if !strings.HasSuffix(strings.ToLower(path), expectedName) {
          return false
        }
      } else if filepath.Base(path) != expectedName {
        return false
      }
      rootInfo, err := os.Lstat(root)
      if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
        return false
      }
      resolvedRoot, err := filepath.EvalSymlinks(root)
      if err != nil {
        return false
      }
      targetInfo, targetErr := os.Lstat(path)
      if targetErr != nil && !os.IsNotExist(targetErr) {
        return false
      }
      if requireExisting {
        if targetErr != nil || targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
          return false
        }
        resolvedPath, resolveErr := filepath.EvalSymlinks(path)
        return resolveErr == nil && pathWithin(resolvedRoot, resolvedPath)
      }
      if targetErr == nil && (targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0) {
        return false
      }
      ancestor, ok := nearestExistingDirectory(filepath.Dir(path))
      if !ok {
        return false
      }
      resolvedAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
      return resolveErr == nil && pathWithin(resolvedRoot, resolvedAncestor)
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
      return err == nil && relative != ".." &&
        !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
    }

    func newIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "patch_knowledge" {
  description = "Patch one knowledge item in an existing knowledge.json by stable item ID. Only supplied fields change; citations must be trusted quote JSON returned by the host, unused quotes are removed, and the tool returns a change summary rather than the full artifact. Call again for another item."

  source = <<-GO
    import (
      "context"
      "crypto/sha256"
      "encoding/hex"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Quote string

    type KnowledgePatch struct {
      ID         string    `json:"id"`
      Claim      *string   `json:"claim,omitempty"`
      Confidence *string   `json:"confidence,omitempty"`
      Citations  *[]Quote  `json:"citations,omitempty"`
    }

    type Input struct {
      ArtifactID   string           `json:"artifact_id"`
      ArtifactPath string           `json:"_r42_artifact_path"`
      Patch        *KnowledgePatch `json:"patch,omitempty"`
      RemoveID     string          `json:"remove_id,omitempty"`
    }

    type QuoteRecord struct {
      ID             string `json:"id"`
      QuoteRef       string `json:"quote_ref"`
      SourceTitle    string `json:"source_title"`
      URL            string `json:"url"`
      ArtifactID     string `json:"artifact_id"`
      ArtifactDigest string `json:"artifact_digest"`
      Locator        string `json:"locator"`
      ExactQuote     string `json:"exact_quote"`
    }

    type KnowledgeItem struct {
      ID         string   `json:"id"`
      Claim      string   `json:"claim"`
      Confidence string   `json:"confidence"`
      QuoteIDs   []string `json:"quote_ids"`
    }

    type KnowledgeArtifact struct {
      ArtifactID  string          `json:"artifact_id"`
      Subquestion string          `json:"subquestion"`
      Knowledge   []KnowledgeItem `json:"knowledge"`
      Quotes      []QuoteRecord   `json:"quotes"`
    }

    type Output struct {
      ArtifactID string   `json:"artifact_id"`
      ChangedIDs []string `json:"changed_ids"`
      RemovedIDs []string `json:"removed_ids"`
      Digest     string   `json:"digest"`
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validatePatchInput(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if !safeKnowledgePath(input.ArtifactPath) {
        return ToolResponse[Output]{Accepted: false, Issues: []Issue{patchIssue("artifact_path", "must be the block knowledge.json artifact path")}}, nil
      }
      payload, err := os.ReadFile(input.ArtifactPath)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read knowledge artifact: %w", err)
      }
      var artifact KnowledgeArtifact
      if err = json.Unmarshal(payload, &artifact); err != nil {
        return ToolResponse[Output]{Accepted: false, Issues: []Issue{patchIssue("artifact", "knowledge.json is not valid JSON")}}, nil
      }
      changed, removed, issues := applyKnowledgePatches(&artifact, input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      encoded, err := json.MarshalIndent(artifact, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode knowledge artifact: %w", err)
      }
      encoded = append(encoded, '\n')
      temporary, err := os.CreateTemp(filepath.Dir(input.ArtifactPath), ".r42-knowledge-patch-*")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create knowledge patch: %w", err)
      }
      temporaryPath := temporary.Name()
      defer os.Remove(temporaryPath)
      if _, err = temporary.Write(encoded); err == nil {
        err = temporary.Close()
      } else {
        _ = temporary.Close()
      }
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write knowledge patch: %w", err)
      }
      if err = os.Rename(temporaryPath, input.ArtifactPath); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("commit knowledge patch: %w", err)
      }
      digest := sha256.Sum256(encoded)
      return ToolResponse[Output]{Accepted: true, Output: &Output{
        ArtifactID: input.ArtifactID, ChangedIDs: changed, RemovedIDs: removed,
        Digest: "sha256:" + hex.EncodeToString(digest[:]),
      }}, nil
    }

    func validatePatchInput(input Input) []Issue {
      issues := make([]Issue, 0)
      if strings.TrimSpace(input.ArtifactID) == "" {
        issues = append(issues, patchIssue("artifact_id", "must not be empty"))
      }
      if (input.Patch == nil) == (strings.TrimSpace(input.RemoveID) == "") {
        issues = append(issues, patchIssue("patch", "provide exactly one patch or remove_id"))
      }
      if input.Patch != nil && strings.TrimSpace(input.Patch.ID) == "" {
        issues = append(issues, patchIssue("patch.id", "must not be empty"))
      }
      return issues
    }

    func applyKnowledgePatches(artifact *KnowledgeArtifact, input Input) ([]string, []string, []Issue) {
      byID := make(map[string]int, len(artifact.Knowledge))
      for index, item := range artifact.Knowledge {
        if _, exists := byID[item.ID]; exists {
          return nil, nil, []Issue{patchIssue("knowledge", "knowledge item IDs must be unique")}
        }
        byID[item.ID] = index
      }
      changed := make([]string, 0, 1)
      quoteByRef := make(map[string]QuoteRecord, len(artifact.Quotes))
      for _, quote := range artifact.Quotes {
        quoteByRef[quote.QuoteRef] = quote
      }
      patches := make([]KnowledgePatch, 0, 1)
      if input.Patch != nil {
        patches = append(patches, *input.Patch)
      }
      for _, patch := range patches {
        index, exists := byID[patch.ID]
        if !exists {
          return nil, nil, []Issue{patchIssue("patch.id", "unknown knowledge item ID: "+patch.ID)}
        }
        item := &artifact.Knowledge[index]
        if patch.Claim != nil { item.Claim = strings.TrimSpace(*patch.Claim) }
        if patch.Confidence != nil { item.Confidence = strings.ToLower(strings.TrimSpace(*patch.Confidence)) }
        if patch.Citations != nil {
          item.QuoteIDs = item.QuoteIDs[:0]
          for _, raw := range *patch.Citations {
            var quote QuoteRecord
            if err := json.Unmarshal([]byte(raw), &quote); err != nil || strings.TrimSpace(quote.QuoteRef) == "" || strings.TrimSpace(quote.ExactQuote) == "" {
              return nil, nil, []Issue{patchIssue("patch.citations", "citations must contain trusted resolved quote JSON")}
            }
            if quote.ID == "" {
              sum := sha256.Sum256([]byte(quote.QuoteRef))
              quote.ID = "quote-" + hex.EncodeToString(sum[:8])
            }
            quoteByRef[quote.QuoteRef] = quote
            if !containsString(item.QuoteIDs, quote.ID) { item.QuoteIDs = append(item.QuoteIDs, quote.ID) }
          }
        }
        changed = append(changed, patch.ID)
      }
      removed := make([]string, 0, 1)
      remove := make(map[string]struct{}, 1)
      if id := strings.TrimSpace(input.RemoveID); id != "" { remove[id] = struct{}{} }
      kept := artifact.Knowledge[:0]
      for _, item := range artifact.Knowledge {
        if _, drop := remove[item.ID]; drop { removed = append(removed, item.ID); continue }
        kept = append(kept, item)
      }
      artifact.Knowledge = kept
      used := make(map[string]struct{})
      for _, item := range artifact.Knowledge { for _, id := range item.QuoteIDs { used[id] = struct{}{} } }
      quotes := artifact.Quotes[:0]
      for _, quote := range quoteByRef {
        if _, keep := used[quote.ID]; keep { quotes = append(quotes, quote) }
      }
      artifact.Quotes = quotes
      if err := validateKnowledgeArtifact(*artifact); err != nil { return nil, nil, []Issue{patchIssue("knowledge", err.Error())} }
      return changed, removed, nil
    }

    func validateKnowledgeArtifact(artifact KnowledgeArtifact) error {
      ids := make(map[string]struct{}, len(artifact.Knowledge))
      quotes := make(map[string]struct{}, len(artifact.Quotes))
      for _, quote := range artifact.Quotes { quotes[quote.ID] = struct{}{} }
      for _, item := range artifact.Knowledge {
        if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Claim) == "" { return fmt.Errorf("knowledge items require id and claim") }
        switch item.Confidence { case "high", "medium", "low": default: return fmt.Errorf("knowledge item %q has invalid confidence", item.ID) }
        if len(item.QuoteIDs) == 0 { return fmt.Errorf("knowledge item %q requires citations", item.ID) }
        for _, id := range item.QuoteIDs { if _, exists := quotes[id]; !exists { return fmt.Errorf("knowledge item %q references unknown quote %q", item.ID, id) } }
        ids[item.ID] = struct{}{}
      }
      return nil
    }

    func containsString(values []string, target string) bool { for _, value := range values { if value == target { return true } }; return false }
    func patchIssue(path, message string) Issue { repair := "Correct the patch and call the tool again."; return Issue{Code: "knowledge_patch", Message: message, Path: &path, RepairHint: &repair} }
    func safeKnowledgePath(path string) bool {
      if !filepath.IsAbs(path) || filepath.Base(path) != "knowledge.json" { return false }
      root, err := os.Getwd(); if err != nil { return false }
      relative, err := filepath.Rel(root, path); return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
    }
  GO
}

go_tool "generate_source_table" {
  description = "Generate or replace the Sources table in the declared report Markdown artifact from canonical metadata in the validated knowledge artifacts. Report citations must use only quote IDs from quotes[].id and must be written in Markdown as [QUOTE_ID], never as knowledge[].id or backtick-wrapped text; do not supply URLs because this tool adds canonical URLs. The only model-supplied argument is report_artifact_id; call this after writing or revising the report and before finalizing Research."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "net/url"
      "os"
      "path/filepath"
      "regexp"
      "sort"
      "strings"
    )

    type Input struct {
      ReportArtifactID   string   `json:"report_artifact_id"`
      ReportPath         string   `json:"_r42_report_path"`
      KnowledgeArtifactIDs []string `json:"knowledge_artifact_ids"`
      KnowledgePaths     []string `json:"_r42_knowledge_paths"`
    }

    type Output struct {
      ArtifactID string `json:"artifact_id"`
      Rows       int    `json:"rows"`
    }

    type quoteRecord struct {
      ID string
      URLs []string
    }

    const sourceQuoteIDPatternText = `(?:[A-Za-z0-9][A-Za-z0-9_-]*-quote-[A-Za-z0-9][A-Za-z0-9_-]*|quote-[A-Za-z0-9][A-Za-z0-9_-]*)`
    var sourceCitationPattern = regexp.MustCompile(`\[(` + sourceQuoteIDPatternText + `)\](?:\([^\)\r\n]*\))?`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := make([]Issue, 0)
      if strings.TrimSpace(input.ReportArtifactID) == "" {
        issues = append(issues, sourceTableIssue("report_artifact_id", "report_artifact_id must not be empty"))
      }
      workspace, workspaceErr := os.Getwd()
      if workspaceErr != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve tool workspace: %w", workspaceErr)
      }
      if !safePath(input.ReportPath, workspace, "report.md") {
        issues = append(issues, sourceTableIssue("report_artifact_id", "report_artifact_id must identify the declared report.md artifact"))
      }
      if len(input.KnowledgePaths) == 0 {
        issues = append(issues, sourceTableIssue("knowledge_artifact_ids", "validated knowledge artifacts are required"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      quotes := make(map[string]quoteRecord)
      for _, path := range input.KnowledgePaths {
        if !safePath(path, filepath.Dir(workspace), "knowledge.json") {
          issues = append(issues, sourceTableIssue("knowledge_artifact_ids", "every source artifact must be knowledge.json"))
          continue
        }
        payload, err := os.ReadFile(path)
        if err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("read knowledge artifact: %w", err)
        }
        var document struct { Quotes []map[string]any `json:"quotes"` }
        if err = json.Unmarshal(payload, &document); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("decode knowledge artifact: %w", err)
        }
        for _, raw := range document.Quotes {
          id, _ := raw["id"].(string)
          id = strings.TrimSpace(id)
          if id == "" { continue }
          urls := quoteURLs(raw)
          if previous, exists := quotes[id]; exists && !sameStrings(previous.URLs, urls) {
            issues = append(issues, sourceTableIssue("knowledge_artifact_ids", "quote ID has conflicting canonical URLs: "+id))
          } else if !exists {
            quotes[id] = quoteRecord{ID: id, URLs: urls}
          }
        }
      }
      report, err := os.ReadFile(input.ReportPath)
      if err != nil { return ToolResponse[Output]{}, fmt.Errorf("read report artifact: %w", err) }
      body := removeSources(string(report))
      ids := citationIDs(body)
      for _, id := range ids {
        if _, exists := quotes[id]; !exists { issues = append(issues, sourceTableIssue("report_artifact_id", "report cites unknown quote ID: "+id)) }
      }
      if len(issues) > 0 { return ToolResponse[Output]{Accepted: false, Issues: issues}, nil }

      body = rewriteQuoteCitations(body, ids, quotes)
      ids = citationIDs(body)
      if len(ids) == 0 {
        issues = append(issues, sourceTableIssue("report_artifact_id", "report body must cite at least one quote with a canonical URL"))
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      for _, id := range ids {
        if len(quotes[id].URLs) == 0 {
          issues = append(issues, sourceTableIssue("report_artifact_id", "quote has no canonical URL: "+id))
        }
      }
      if len(issues) > 0 { return ToolResponse[Output]{Accepted: false, Issues: issues}, nil }

      updated := replaceSources(body, ids, quotes, string(report))
      temporary, err := os.CreateTemp(filepath.Dir(input.ReportPath), ".r42-source-table-*")
      if err != nil { return ToolResponse[Output]{}, fmt.Errorf("create source table temp file: %w", err) }
      temporaryPath := temporary.Name()
      defer os.Remove(temporaryPath)
      if _, err = temporary.WriteString(updated); err == nil { err = temporary.Close() } else { _ = temporary.Close() }
      if err != nil { return ToolResponse[Output]{}, fmt.Errorf("write source table: %w", err) }
      if err = os.Rename(temporaryPath, input.ReportPath); err != nil { return ToolResponse[Output]{}, fmt.Errorf("commit source table: %w", err) }
      return ToolResponse[Output]{Accepted: true, Output: &Output{ArtifactID: input.ReportArtifactID, Rows: len(ids)}}, nil
    }

    func quoteURLs(raw map[string]any) []string {
      values := make([]string, 0)
      var appendValue func(any)
      appendValue = func(value any) {
        switch typed := value.(type) {
        case string:
          if strings.TrimSpace(typed) != "" { values = append(values, strings.TrimSpace(typed)) }
        case []any:
          for _, item := range typed { appendValue(item) }
        }
      }
      appendValue(raw["sources"]); appendValue(raw["urls"]); appendValue(raw["url"]); appendValue(raw["source_url"])
      canonical := make([]string, 0, len(values))
      for _, value := range values { if isCanonicalURL(value) { canonical = append(canonical, value) } }
      return uniqueSorted(canonical)
    }

    func isCanonicalURL(value string) bool {
      parsed, err := url.Parse(strings.TrimSpace(value)); if err != nil { return false }
      scheme := strings.ToLower(parsed.Scheme)
      return (scheme == "http" || scheme == "https") && parsed.Host != ""
    }

    func citationIDs(body string) []string {
      matches := sourceCitationPattern.FindAllStringSubmatch(body, -1)
      ids := make([]string, 0, len(matches))
      for _, match := range matches { if len(match) > 1 { ids = append(ids, match[1]) } }
      return uniqueSorted(ids)
    }

    func rewriteQuoteCitations(body string, ids []string, quotes map[string]quoteRecord) string {
      updated := body
      for _, id := range ids {
        pattern := regexp.MustCompile(`\[` + regexp.QuoteMeta(id) + `\](?:\([^\)\r\n]*\))?`)
        replacement := ""
        if len(quotes[id].URLs) > 0 { replacement = "[" + id + "](" + quotes[id].URLs[0] + ")" }
        updated = pattern.ReplaceAllStringFunc(updated, func(string) string { return replacement })
      }
      return updated
    }

    func removeSources(report string) string {
      lines := strings.Split(strings.ReplaceAll(report, "\r\n", "\n"), "\n")
      start := -1
      for index, line := range lines { if strings.EqualFold(strings.TrimSpace(line), "## Sources") { start = index; break } }
      if start < 0 { return report }
      end := len(lines)
      for index := start + 1; index < len(lines); index++ { if strings.HasPrefix(strings.TrimSpace(lines[index]), "#") { end = index; break } }
      prefix := strings.TrimRight(strings.Join(lines[:start], "\n"), "\n")
      suffix := strings.TrimLeft(strings.Join(lines[end:], "\n"), "\n")
      if suffix == "" { return prefix + "\n" }
      return prefix + "\n" + suffix
    }

    func replaceSources(body string, ids []string, quotes map[string]quoteRecord, original string) string {
      rows := []string{"## Sources", "", "| Quote ID | URL |", "| --- | --- |"}
      for _, id := range ids { rows = append(rows, "| "+id+" | "+strings.Join(quotes[id].URLs, " ; ")+" |") }
      updated := strings.TrimRight(body, "\r\n") + "\n\n" + strings.Join(rows, "\n") + "\n"
      originalLines := strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
      start := -1
      for index, line := range originalLines { if strings.EqualFold(strings.TrimSpace(line), "## Sources") { start = index; break } }
      if start >= 0 {
        end := len(originalLines)
        for index := start + 1; index < len(originalLines); index++ { if strings.HasPrefix(strings.TrimSpace(originalLines[index]), "#") { end = index; break } }
        if end < len(originalLines) { updated += strings.TrimLeft(strings.Join(originalLines[end:], "\n"), "\n") + "\n" }
      }
      return updated
    }

    func uniqueSorted(values []string) []string {
      seen := make(map[string]struct{}, len(values)); result := make([]string, 0, len(values))
      for _, value := range values { if _, exists := seen[value]; !exists { seen[value] = struct{}{}; result = append(result, value) } }
      sort.Strings(result); return result
    }

    func sameStrings(left, right []string) bool { return strings.Join(left, "\x00") == strings.Join(right, "\x00") }
    func sourceTableIssue(path, message string) Issue { repair := "Correct the report or knowledge artifact and call the tool again."; return Issue{Code: "source_table", Message: message, Path: &path, RepairHint: &repair} }
    func safePath(path, root, base string) bool {
      if !filepath.IsAbs(path) || filepath.Base(path) != base { return false }
      root = filepath.Clean(root); path = filepath.Clean(path)
      relative, err := filepath.Rel(root, path)
      if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) { return false }
      rootInfo, err := os.Lstat(root)
      if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 { return false }
      current := root
      for _, part := range strings.Split(relative, string(os.PathSeparator)) {
        if part == "." || part == "" { continue }
        current = filepath.Join(current, part)
        info, statErr := os.Lstat(current)
        if statErr != nil || info.Mode()&os.ModeSymlink != 0 { return false }
      }
      return true
    }
  GO
}

go_tool "submit_conflict_resolution" {
  description = "Submit detected cross-subquestion conflicts and their evidence-backed resolutions, then write the declared resolution artifact identified by artifact_id. `conflicts.status` allowed values: `resolved`, `unresolved`."

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
      ArtifactID        string             `json:"artifact_id"`
      ArtifactPath      string             `json:"_r42_artifact_path"`
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

      artifactPath := input.ArtifactPath
      input.ArtifactPath = ""
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode conflict artifact: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(artifactPath), payload, 0600); err != nil {
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
        if !existingKnowledgeArtifact(clean) {
          issues = append(issues, resolutionIssue("reviewed_artifact", path, "must name an existing knowledge.json file from the current run"))
          continue
        }
        if _, exists := seenArtifacts[clean]; exists {
          issues = append(issues, resolutionIssue("reviewed_artifact", path, "must not be duplicated"))
          continue
        }
        seenArtifacts[clean] = struct{}{}
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
      workspace, err := os.Getwd()
      if err != nil {
        return false
      }
      path := filepath.Clean(strings.TrimSpace(raw))
      expected := filepath.Join(workspace, "resolution.json")
      if path != expected {
        return false
      }
      info, err := os.Lstat(path)
      return os.IsNotExist(err) || (err == nil && info.Mode()&os.ModeSymlink == 0 && !info.IsDir())
    }

    func existingKnowledgeArtifact(path string) bool {
      workspace, err := os.Getwd()
      if err != nil {
        return false
      }
      blocksRoot := filepath.Dir(workspace)
      if !strings.EqualFold(filepath.Base(blocksRoot), "blocks") || !pathWithin(blocksRoot, path) ||
        filepath.Base(path) != "knowledge.json" {
        return false
      }
      rootInfo, err := os.Lstat(blocksRoot)
      if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
        return false
      }
      info, err := os.Lstat(path)
      if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
        return false
      }
      resolvedRoot, rootErr := filepath.EvalSymlinks(blocksRoot)
      resolvedPath, pathErr := filepath.EvalSymlinks(path)
      return rootErr == nil && pathErr == nil && pathWithin(resolvedRoot, resolvedPath)
    }

    func pathWithin(root, path string) bool {
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." &&
        !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
    }

    func resolutionIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

external_tool "audit_synthesis" {
  description = "Audit final-report artifact paths, readable knowledge and resolution artifacts, and referenced artifact IDs in one bounded call. Citation IDs, quote references, and source-table rows are generated upstream and are not rechecked here. The tool writes the full result to synthesis-audit.json. Call it exactly once per QC round."
  program     = ["python", "${path.module}/audit_synthesis.py"]

  input_type = object({
    report_path     = string
    knowledge_paths = list(string)
    resolution_path = string
  })

  output_type = object({
    pass                = bool
    report_quote_ids    = number
    knowledge_quote_ids = number
    knowledge_artifacts = number
    artifacts_checked   = number
    conflicts            = number
    match_modes          = map(number)
    issue_count          = number
    issues = list(object({
      code        = string
      message     = string
      path        = string
      repair_hint = string
    }))
    audit_path = string
  })
}
