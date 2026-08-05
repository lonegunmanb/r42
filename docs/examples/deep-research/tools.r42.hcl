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

go_tool "save_snapshot" {
  description = "Save complete source material as a Markdown snapshot under the current research block's snapshots directory and return the absolute path."

  source = <<-GO
    import (
      "context"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Input struct {
      SnapshotPath string `json:"snapshot_path"`
      Content      string `json:"content"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      path := filepath.Clean(strings.TrimSpace(input.SnapshotPath))
      issues := make([]Issue, 0)
      snapshotRoot, err := prepareSnapshotRoot()
      if err != nil {
        return ToolResponse[Output]{}, err
      }
      if !validSnapshotPath(path, snapshotRoot) {
        issues = append(issues, snapshotIssue("snapshot_path", "must be an absolute .md path under the current block's snapshots directory"))
      }
      if strings.TrimSpace(input.Content) == "" {
        issues = append(issues, snapshotIssue("content", "must contain the complete source material in Markdown"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create snapshot directory: %w", err)
      }
      if err := os.WriteFile(path, []byte(input.Content), 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write snapshot: %w", err)
      }
      output := Output(path)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func prepareSnapshotRoot() (string, error) {
      workspace, err := os.Getwd()
      if err != nil {
        return "", fmt.Errorf("resolve block workspace: %w", err)
      }
      root := filepath.Join(workspace, "snapshots")
      info, err := os.Lstat(root)
      if os.IsNotExist(err) {
        if err = os.MkdirAll(root, 0700); err != nil {
          return "", fmt.Errorf("create snapshot root: %w", err)
        }
        return root, nil
      }
      if err != nil {
        return "", fmt.Errorf("inspect snapshot root: %w", err)
      }
      if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
        return "", fmt.Errorf("snapshot root must be a real directory")
      }
      return root, nil
    }

    func validSnapshotPath(path, root string) bool {
      if !filepath.IsAbs(path) || !strings.HasSuffix(strings.ToLower(path), ".md") || !pathWithin(root, path) {
        return false
      }
      targetInfo, targetErr := os.Lstat(path)
      if targetErr != nil && !os.IsNotExist(targetErr) {
        return false
      }
      if targetErr == nil && (targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0) {
        return false
      }
      ancestor, ok := nearestExistingDirectory(filepath.Dir(path))
      if !ok {
        return false
      }
      resolvedRoot, rootErr := filepath.EvalSymlinks(root)
      resolvedAncestor, ancestorErr := filepath.EvalSymlinks(ancestor)
      return rootErr == nil && ancestorErr == nil && pathWithin(resolvedRoot, resolvedAncestor)
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

    func snapshotIssue(path, message string) Issue {
      repair := "Correct this field and call save_snapshot again."
      return Issue{Code: "snapshot_" + path, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_knowledge" {
  description = "Submit one subquestion's knowledge records and exact source quotes linked to saved Markdown snapshots, validate their links, and write knowledge.json."

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
      ID           string `json:"id"`
      SourceTitle  string `json:"source_title"`
      URL          string `json:"url"`
      SnapshotPath string `json:"snapshot_path"`
      Locator      string `json:"locator"`
      ExactQuote   string `json:"exact_quote"`
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
        if !existingSnapshot(quote.SnapshotPath) {
          issues = append(issues, newIssue("snapshot_path", path+".snapshot_path", "must name an existing absolute Markdown snapshot under the current block's snapshots directory"))
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

    func existingSnapshot(raw string) bool {
      path := filepath.Clean(strings.TrimSpace(raw))
      workspace, err := os.Getwd()
      if err != nil {
        return false
      }
      return safePathUnderRoot(path, filepath.Join(workspace, "snapshots"), ".md", true)
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
  description = "Audit final-report citation structure, source URL mappings, snapshot existence, and quote text equivalence in one bounded call. The tool performs exact, line-ending, paragraph-whitespace, and Unicode-equivalent matching internally and writes the full result to synthesis-audit.json. Call it exactly once per QC round."
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
    snapshots_checked   = number
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
