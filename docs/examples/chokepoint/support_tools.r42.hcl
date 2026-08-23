go_tool "register_evidence_source" {
  description = <<-DESC
    Register one retained source in the current evidence-ledger draft. `url` must equal the fetched URL recorded in the referenced snapshot; this is enforced by the typed-tool host. `canonical_url` is optional publication identity metadata and may equal `url`. `source_type` allowed values: `authoritative_primary`, `official_filing`, `official_product`, `official_statement`, `regulator`, `qualified_media`, `credible_media`, `named_media`, `peer_reviewed`, `industry_research`, `other_published`, `other`, `lead_only`, `self_media`, `forum`, `aggregator`. `reporting_basis` allowed values: `public_document`, `named_source`, `anonymous_sources`, `direct_observation`, `published_methodology`. `provenance` allowed values: `original`, `syndication`, `aggregation`. Unfamiliar values are retained as unknown instead of rejecting the call. The host derives source, origin, and independence IDs.
  DESC

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "net/url"
      "os"
      "path/filepath"
      "regexp"
      "strings"
      "time"
    )

    type Input struct {
      WorkspaceDir    string   `json:"workspace_dir"`
      LedgerPath      string   `json:"ledger_path"`
      URL             string   `json:"url"`
      CanonicalURL    string   `json:"canonical_url"`
      Title           string   `json:"title"`
      Publisher       string   `json:"publisher"`
      PublicationDate string   `json:"publication_date"`
      AccessedAt      string   `json:"accessed_at"`
      SourceType      string   `json:"source_type"`
      ReportingBasis  string   `json:"reporting_basis"`
      Provenance      string   `json:"provenance"`
      SnapshotID      string   `json:"snapshot_id"`
      NamedEntities   []string `json:"named_entities"`
    }

    type Output struct {
      SourceID   string `json:"source_id"`
      SourcePath string `json:"source_path"`
      CanonicalURL string `json:"canonical_url"`
      OriginID string `json:"origin_id"`
      IndependenceGroup string `json:"independence_group"`
      SourceClass string `json:"source_class"`
    }

    type SourceRecord struct {
      ID              string   `json:"id"`
      URL             string   `json:"url"`
      NormalizedURL   string   `json:"normalized_url"`
      CanonicalURL    string   `json:"canonical_url"`
      OriginID        string   `json:"origin_id"`
      IndependenceGroup string `json:"independence_group"`
      Title           string   `json:"title"`
      Publisher       string   `json:"publisher"`
      PublicationDate string   `json:"publication_date"`
      AccessedAt      string   `json:"accessed_at"`
      SourceType      string   `json:"source_type"`
      SourceClass     string   `json:"source_class"`
      ReportingBasis  string   `json:"reporting_basis"`
      Provenance      string   `json:"provenance"`
      SnapshotID      string   `json:"snapshot_id"`
      NamedEntities   []string `json:"named_entities"`
    }

    var evidenceSnapshotID = regexp.MustCompile(`^snapshot-(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := evidenceWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create evidence workspace: %w", err)
      }
      ledgerPath, issues := evidenceLedgerPath(input.LedgerPath, workspace)
      normalizedURL, validURL := normalizeEvidenceURL(input.URL)
      if !validURL {
        issues = append(issues, evidenceIssue("url", "url", "url must be an absolute HTTP or HTTPS URL"))
      }
      sourceType := strings.TrimSpace(input.SourceType)
      sourceClass := evidenceSourceClass(sourceType)
      canonicalURL := normalizedURL
      if strings.TrimSpace(input.CanonicalURL) != "" {
        var validCanonical bool
        canonicalURL, validCanonical = normalizeEvidenceURL(input.CanonicalURL)
        if !validCanonical {
          issues = append(issues, evidenceIssue("canonical_url", "canonical_url", "canonical_url must be an absolute HTTP or HTTPS URL"))
        }
      }
      if strings.TrimSpace(input.Title) == "" {
        issues = append(issues, evidenceIssue("source", "title", "title must not be empty"))
      }
      if strings.TrimSpace(input.Publisher) == "" {
        issues = append(issues, evidenceIssue("source", "publisher", "publisher must not be empty"))
      }
      if !evidenceDate(input.PublicationDate) {
        issues = append(issues, evidenceIssue("date", "publication_date", "publication_date must use YYYY-MM-DD"))
      }
      if !evidenceDate(input.AccessedAt) {
        issues = append(issues, evidenceIssue("date", "accessed_at", "accessed_at must use YYYY-MM-DD"))
      }
      snapshotID := strings.TrimSpace(input.SnapshotID)
      if !evidenceSnapshotID.MatchString(snapshotID) {
        issues = append(issues, evidenceIssue("snapshot_id", "snapshot_id", "snapshot_id must use a registered snapshot ID, either snapshot- plus 32 lowercase hexadecimal characters or a UUID, not a filesystem path"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      suffix := strings.TrimPrefix(snapshotID, "snapshot-")
      sourceID := "source-" + suffix
      originID := "origin-" + suffix[:20]
      independenceGroup := originID
      existing, loadErr := loadEvidenceSources(filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft", "sources"))
      if loadErr != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load registered evidence sources: %w", loadErr)
      }
      for _, candidate := range existing {
        if candidate.CanonicalURL == canonicalURL && candidate.OriginID != "" {
          originID = candidate.OriginID
          independenceGroup = candidate.IndependenceGroup
          break
        }
      }
      entities := make([]string, 0, len(input.NamedEntities))
      for _, entity := range input.NamedEntities {
        if entity = strings.TrimSpace(entity); entity != "" {
          entities = append(entities, entity)
        }
      }
      record := SourceRecord{
        ID: sourceID, URL: strings.TrimSpace(input.URL), NormalizedURL: normalizedURL,
        CanonicalURL: canonicalURL, OriginID: originID, IndependenceGroup: independenceGroup,
        Title: strings.TrimSpace(input.Title), Publisher: strings.TrimSpace(input.Publisher),
        PublicationDate: strings.TrimSpace(input.PublicationDate), AccessedAt: strings.TrimSpace(input.AccessedAt),
        SourceType: sourceType, SourceClass: sourceClass,
        ReportingBasis: evidenceReportingBasis(input.ReportingBasis), Provenance: evidenceProvenance(input.Provenance),
        SnapshotID: snapshotID, NamedEntities: entities,
      }
      sourcePath := filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft", "sources", sourceID+".json")
      if err = writeEvidenceJSON(sourcePath, record); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write evidence source: %w", err)
      }
      output := Output{
        SourceID: sourceID, SourcePath: sourcePath, CanonicalURL: canonicalURL,
        OriginID: originID, IndependenceGroup: independenceGroup, SourceClass: sourceClass,
      }
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func evidenceLedgerPath(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{evidenceIssue("invalid_path", "ledger_path", "ledger_path must be absolute")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-ledger.json" || !evidenceWithin(path, workspace) {
        return "", []Issue{evidenceIssue("invalid_path", "ledger_path", "ledger_path must end in evidence-ledger.json under workspace_dir")}
      }
      return path, nil
    }

    func evidenceWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := evidenceBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !evidenceWithin(workspace, blocksRoot) {
        return "", []Issue{evidenceIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func normalizeEvidenceURL(raw string) (string, bool) {
      parsed, err := url.Parse(strings.TrimSpace(raw))
      if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
        return "", false
      }
      parsed.Scheme = strings.ToLower(parsed.Scheme)
      parsed.Host = strings.ToLower(parsed.Host)
      parsed.Path = strings.TrimRight(parsed.Path, "/")
      parsed.Fragment = ""
      return parsed.String(), true
    }

    func evidenceSourceClass(raw string) string {
      switch strings.ToLower(strings.TrimSpace(raw)) {
      case "authoritative_primary", "official_filing", "official_product", "official_statement", "regulator":
        return "authoritative_primary"
      case "qualified_media", "credible_media", "named_media", "peer_reviewed", "industry_research":
        return "qualified_media"
      case "other_published", "other":
        return "other_published"
      case "lead_only", "self_media", "forum", "aggregator":
        return "lead_only"
      default:
        return "unknown"
      }
    }

    func evidenceReportingBasis(raw string) string {
      switch strings.ToLower(strings.TrimSpace(raw)) {
      case "public_document", "named_source", "anonymous_sources", "direct_observation", "published_methodology":
        return strings.ToLower(strings.TrimSpace(raw))
      default:
        return "unspecified"
      }
    }

    func evidenceProvenance(raw string) string {
      switch strings.ToLower(strings.TrimSpace(raw)) {
      case "original", "syndication", "aggregation":
        return strings.ToLower(strings.TrimSpace(raw))
      default:
        return "unknown"
      }
    }

    func loadEvidenceSources(directory string) ([]SourceRecord, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
      if err != nil {
        return nil, err
      }
      records := make([]SourceRecord, 0, len(paths))
      for _, path := range paths {
        payload, readErr := os.ReadFile(path)
        if readErr != nil {
          return nil, readErr
        }
        var record SourceRecord
        if decodeErr := json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), &record); decodeErr != nil {
          return nil, decodeErr
        }
        records = append(records, record)
      }
      return records, nil
    }

    func evidenceDate(raw string) bool {
      _, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
      return err == nil
    }

    func evidenceBlocksRoot(workspace string) string {
      current, err := filepath.Abs(workspace)
      if err != nil {
        return ""
      }
      for {
        if strings.EqualFold(filepath.Base(current), "blocks") && strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))), "runs") {
          return current
        }
        parent := filepath.Dir(current)
        if parent == current {
          return ""
        }
        current = parent
      }
    }

    func evidenceWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func writeEvidenceJSON(path string, value any) error {
      if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
        return err
      }
      var buffer bytes.Buffer
      encoder := json.NewEncoder(&buffer)
      encoder.SetEscapeHTML(false)
      encoder.SetIndent("", "  ")
      if err := encoder.Encode(value); err != nil {
        return err
      }
      return os.WriteFile(path, buffer.Bytes(), 0600)
    }

    func evidenceIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_supply_chain_scope" {
  description = "Validate the declared product boundary and coverage inventory, write scope.json, and return its JSON. `coverage_items.track` allowed values: `product_structure`, `manufacturing_testing`, `equipment`, `materials_chemicals`, `qualification_integration`."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type CoverageItem struct {
      ID          string   `json:"id"`
      Description string   `json:"description"`
      Track       string   `json:"track"`
      Components  []string `json:"components"`
      Stages      []string `json:"stages"`
    }

    type Input struct {
      ArtifactPath       string         `json:"artifact_path"`
      Topic              string         `json:"topic"`
      FocalProduct       string         `json:"focal_product"`
      ProductVariants    []string       `json:"product_variants"`
      ExpectedComponents []string       `json:"expected_components"`
      ExpectedStages     []string       `json:"expected_stages"`
      UpstreamBoundaries []string       `json:"upstream_boundaries"`
      DownstreamBoundary string         `json:"downstream_boundary"`
      CoverageItems      []CoverageItem `json:"coverage_items"`
      OpenQuestions      []string       `json:"open_questions"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateScope(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode supply-chain scope: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write supply-chain scope: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateScope(input Input) []Issue {
      issues := []Issue{}
      if !scopeArtifactPath(input.ArtifactPath) {
        issues = append(issues, scopeIssue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in scope.json"))
      }
      if strings.TrimSpace(input.Topic) == "" || strings.TrimSpace(input.FocalProduct) == "" {
        issues = append(issues, scopeIssue("scope", "topic", "topic and focal_product must not be empty"))
      }
      if len(input.ProductVariants) == 0 || len(input.ExpectedComponents) == 0 || len(input.ExpectedStages) == 0 {
        issues = append(issues, scopeIssue("inventory", "coverage_items", "product_variants, expected_components, and expected_stages must not be empty"))
      }
      if len(input.UpstreamBoundaries) == 0 || strings.TrimSpace(input.DownstreamBoundary) == "" {
        issues = append(issues, scopeIssue("boundary", "upstream_boundaries", "upstream and downstream boundaries must be explicit"))
      }

      validTracks := map[string]struct{}{
        "product_structure": {}, "manufacturing_testing": {}, "equipment": {},
        "materials_chemicals": {}, "qualification_integration": {},
      }
      expectedComponents := stringSet(input.ExpectedComponents)
      expectedStages := stringSet(input.ExpectedStages)
      coveredComponents := map[string]struct{}{}
      coveredStages := map[string]struct{}{}
      itemIDs := map[string]struct{}{}
      for index, item := range input.CoverageItems {
        path := fmt.Sprintf("coverage_items[%d]", index)
        id := strings.TrimSpace(item.ID)
        if id == "" {
          issues = append(issues, scopeIssue("coverage_id", path+".id", "must not be empty"))
        } else if _, exists := itemIDs[id]; exists {
          issues = append(issues, scopeIssue("coverage_id", path+".id", "must be unique"))
        } else {
          itemIDs[id] = struct{}{}
        }
        if strings.TrimSpace(item.Description) == "" {
          issues = append(issues, scopeIssue("coverage_item", path+".description", "must not be empty"))
        }
        if _, exists := validTracks[item.Track]; !exists {
          issues = append(issues, scopeIssue("track", path+".track", "must name one of the five graph tracks"))
        }
        if len(item.Components) == 0 || len(item.Stages) == 0 {
          issues = append(issues, scopeIssue("coverage_item", path, "components and stages must not be empty"))
        }
        for componentIndex, component := range item.Components {
          component = strings.TrimSpace(component)
          if _, exists := expectedComponents[component]; !exists {
            issues = append(issues, scopeIssue("component", fmt.Sprintf("%s.components[%d]", path, componentIndex), "must reference expected_components"))
          } else {
            coveredComponents[component] = struct{}{}
          }
        }
        for stageIndex, stage := range item.Stages {
          stage = strings.TrimSpace(stage)
          if _, exists := expectedStages[stage]; !exists {
            issues = append(issues, scopeIssue("stage", fmt.Sprintf("%s.stages[%d]", path, stageIndex), "must reference expected_stages"))
          } else {
            coveredStages[stage] = struct{}{}
          }
        }
      }
      for component := range expectedComponents {
        if _, exists := coveredComponents[component]; !exists {
          issues = append(issues, scopeIssue("component_coverage", "expected_components", "component "+component+" has no coverage item"))
        }
      }
      for stage := range expectedStages {
        if _, exists := coveredStages[stage]; !exists {
          issues = append(issues, scopeIssue("stage_coverage", "expected_stages", "stage "+stage+" has no coverage item"))
        }
      }
      return issues
    }

    func stringSet(values []string) map[string]struct{} {
      result := map[string]struct{}{}
      for _, value := range values {
        value = strings.TrimSpace(value)
        if value != "" {
          result[value] = struct{}{}
        }
      }
      return result
    }

    func scopeArtifactPath(raw string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/scope.json")
    }

    func scopeIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}
