go_tool "submit_morning_scan" {
  description = "Finish one fixed morning coverage-matrix scan. The collector must make one initial call to its assigned acquisition tool, with only the bounded cursor pagination allowed by that tool's schema for news results; list_news open-discovery tasks intentionally retain one page without following the cursor; an unsupported get_quote code may additionally be recovered by one quote://codes resource read and exactly one retry. Save the complete returned material from every page as an artifact, then submit status and artifact references here. Valid statuses are completed, no_material_news, check_failed, and unavailable. This tool performs mechanical checks only and never requests another search."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Input struct {
      ArtifactID       string   `json:"artifact_id"`
      ArtifactPath     string   `json:"_r42_artifact_path"`
      ActionID         string   `json:"action_id"`
      TrackID          string   `json:"track_id"`
      TargetID         string   `json:"target_id"`
      ScanType         string   `json:"scan_type"`
      Status           string   `json:"status"`
      Summary          string   `json:"summary"`
      SourceArtifactIDs []string `json:"source_artifact_ids"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := []Issue{}
      if !validMorningArtifactPath(input.ArtifactPath, "scan.json") {
        issues = append(issues, morningIssue("artifact_path", "_r42_artifact_path", "must be an absolute block artifact path ending in scan.json"))
      }
      for path, value := range map[string]string{
        "action_id": input.ActionID, "track_id": input.TrackID, "target_id": input.TargetID,
        "scan_type": input.ScanType, "status": input.Status, "summary": input.Summary,
      } {
        if strings.TrimSpace(value) == "" {
          issues = append(issues, morningIssue("required", path, "must not be empty"))
        }
      }
      validStatuses := map[string]struct{}{"completed": {}, "no_material_news": {}, "check_failed": {}, "unavailable": {}}
      if _, ok := validStatuses[input.Status]; !ok {
        issues = append(issues, morningIssue("status", "status", "must be completed, no_material_news, check_failed, or unavailable"))
      }
      validScanTypes := map[string]struct{}{"get_quote": {}, "search_flash": {}, "search_news": {}, "list_news": {}, "list_calendar": {}, "web_search": {}, "pplx_search": {}}
      if _, ok := validScanTypes[input.ScanType]; !ok {
        issues = append(issues, morningIssue("scan_type", "scan_type", "must be get_quote, search_flash, search_news, list_news, list_calendar, web_search, or pplx_search"))
      }
      if !validArtifactID(input.ArtifactID) {
        issues = append(issues, morningIssue("artifact_id", "artifact_id", "must be an r42 artifact ID"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      artifactPath := input.ArtifactPath
      input.ArtifactPath = ""
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode morning scan: %w", err)
      }
      payload = append(payload, '\n')
      if err := writeMorningFile(artifactPath, payload); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validMorningArtifactPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func validArtifactID(value string) bool {
      value = strings.TrimSpace(value)
      return strings.HasPrefix(value, "artifact-") && len(value) >= len("artifact-")+16
    }

    func writeMorningFile(path string, payload []byte) error {
      clean := filepath.Clean(path)
      if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("refuse symlink output %q", clean)
      }
      if err := os.MkdirAll(filepath.Dir(clean), 0700); err != nil {
        return fmt.Errorf("create output directory: %w", err)
      }
      if err := os.WriteFile(clean, payload, 0600); err != nil {
        return fmt.Errorf("write output: %w", err)
      }
      return nil
    }

    func morningIssue(code, path, message string) Issue {
      repair := "Correct every reported field and call the typed tool again."
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}

go_tool "submit_morning_evidence" {
  description = "Submit one gathering track's atomic evidence during closed Research only. `claims.category` is optional and may be `source_fact` (the default when omitted), `analysis` (a reasoned, conditional interpretation grounded in the cited facts), or `mixed` (the same claim combines a sourced fact with its clearly marked analysis). Copy each `quotes.exact_quote` verbatim from its referenced artifact; a quote may include the complete sentence or necessary adjacent context, but do not paraphrase, combine separate passages, or add unrelated text. This tool performs mechanical checks only (fields, IDs, dates, artifact membership, quote presence, claim/quote references, and required watchlist coverage); it does not judge whether a quote semantically supports a claim or whether a search was sufficiently broad. Final QC performs that semantic check. `claims.as_of` and `quotes.publication_date` must use `YYYY-MM-DD`. `claims.confidence` must be `high`, `medium`, or `low`; every claim must reference at least one quote and every quote must be used. A watchlist item with no worthwhile news must use `news_status = no_material_news` and explain the check in `summary`; this is a valid result, not a claim."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
      "time"
    )

    type EvidenceClaim struct {
      ID         string   `json:"id"`
      Statement  string   `json:"statement"`
      Category   string   `json:"category,omitempty"`
      AsOf       string   `json:"as_of"`
      Confidence string   `json:"confidence"`
      QuoteIDs   []string `json:"quote_ids"`
    }

    type EvidenceQuote struct {
      ID              string `json:"id"`
      SourceTitle     string `json:"source_title"`
      URL             string `json:"url"`
      ArtifactID      string `json:"artifact_id"`
      PublicationDate string `json:"publication_date"`
      Locator         string `json:"locator"`
      ExactQuote      string `json:"exact_quote"`
    }

    type CoverageRequirement struct {
      ID           string   `json:"id"`
      Name         string   `json:"name"`
      Kind         string   `json:"kind"`
      QuoteSymbols []string `json:"quote_symbols"`
      SearchTerms  []string `json:"search_terms"`
    }

    type CoverageRecord struct {
      ObjectID     string   `json:"object_id"`
      Name         string   `json:"name"`
      Kind         string   `json:"kind"`
      QuoteStatus  string   `json:"quote_status"`
      NewsStatus   string   `json:"news_status"`
      CheckedUntil string   `json:"checked_until"`
      Summary      string   `json:"summary"`
      ClaimIDs     []string `json:"claim_ids,omitempty"`
    }

    type Input struct {
      ArtifactID       string                `json:"artifact_id"`
      ArtifactPath     string                `json:"_r42_artifact_path"`
      TaskID           string                `json:"task_id"`
      EditionDate      string                `json:"edition_date"`
      Question         string                `json:"question"`
      RequiredCoverage []CoverageRequirement `json:"required_coverage"`
      Coverage         []CoverageRecord      `json:"coverage"`
      Claims           []EvidenceClaim       `json:"claims"`
      Quotes           []EvidenceQuote       `json:"quotes"`
      EmptyReason      string                `json:"empty_reason,omitempty"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateEvidence(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      artifactPath := input.ArtifactPath
      input.ArtifactPath = ""
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode morning evidence: %w", err)
      }
      payload = append(payload, '\n')
      if err := writeMorningFile(artifactPath, payload); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateEvidence(input Input) []Issue {
      issues := []Issue{}
      if !validMorningArtifactPath(input.ArtifactPath, "evidence.json") {
        issues = append(issues, morningIssue("artifact_path", "_r42_artifact_path", "must be an absolute block artifact path ending in evidence.json"))
      }
      if !validArtifactID(input.ArtifactID) {
        issues = append(issues, morningIssue("artifact_id", "artifact_id", "must be an r42 artifact ID"))
      }
      if strings.TrimSpace(input.TaskID) == "" {
        issues = append(issues, morningIssue("task_id", "task_id", "must not be empty"))
      }
      if strings.TrimSpace(input.Question) == "" {
        issues = append(issues, morningIssue("question", "question", "must not be empty"))
      }
      edition, err := time.Parse("2006-01-02", input.EditionDate)
      if err != nil {
        issues = append(issues, morningIssue("edition_date", "edition_date", "must use YYYY-MM-DD"))
      }
      hasClaims := len(input.Claims) > 0
      hasQuotes := len(input.Quotes) > 0
      hasEmptyReason := strings.TrimSpace(input.EmptyReason) != ""
      if !hasClaims && !hasQuotes {
        if !hasEmptyReason {
          issues = append(issues, morningIssue("empty_reason", "empty_reason", "must explain why no verifiable evidence was found when claims and quotes are empty"))
        }
      } else {
        if !hasClaims {
          issues = append(issues, morningIssue("claims", "claims", "must contain at least one claim unless quotes are also empty with empty_reason"))
        }
        if !hasQuotes {
          issues = append(issues, morningIssue("quotes", "quotes", "must contain at least one exact quote unless claims are also empty with empty_reason"))
        }
        if hasEmptyReason {
          issues = append(issues, morningIssue("empty_reason", "empty_reason", "must be empty when claims or quotes contain evidence"))
        }
      }

      issues = append(issues, validateCoverage(input.RequiredCoverage, input.Coverage, input.Claims)...)

      quoteIDs := map[string]struct{}{}
      usedQuoteIDs := map[string]struct{}{}
      for index, quote := range input.Quotes {
        path := fmt.Sprintf("quotes[%d]", index)
        id := strings.TrimSpace(quote.ID)
        if id == "" {
          issues = append(issues, morningIssue("quote_id", path+".id", "must not be empty"))
        } else if _, exists := quoteIDs[id]; exists {
          issues = append(issues, morningIssue("quote_id", path+".id", "must be unique"))
        } else {
          quoteIDs[id] = struct{}{}
        }
        if strings.TrimSpace(quote.SourceTitle) == "" || strings.TrimSpace(quote.Locator) == "" || strings.TrimSpace(quote.ExactQuote) == "" {
          issues = append(issues, morningIssue("quote_content", path, "source_title, locator, and exact_quote must not be empty"))
        }
        if strings.TrimSpace(quote.URL) == "" {
          issues = append(issues, morningIssue("source_url", path+".url", "must not be empty; use the stable source URL or source identifier"))
        }
        if !validArtifactID(quote.ArtifactID) {
          issues = append(issues, morningIssue("artifact_id", path+".artifact_id", "must be a registered r42 artifact ID"))
        }
        if publicationDate, dateErr := time.Parse("2006-01-02", quote.PublicationDate); dateErr != nil {
          issues = append(issues, morningIssue("publication_date", path+".publication_date", "must use YYYY-MM-DD"))
        } else if err == nil && publicationDate.After(edition) {
          issues = append(issues, morningIssue("future_source", path+".publication_date", "must not post-date edition_date"))
        }
      }

      claimIDs := map[string]struct{}{}
      validConfidence := map[string]struct{}{"high": {}, "medium": {}, "low": {}}
      validCategories := map[string]struct{}{"source_fact": {}, "analysis": {}, "mixed": {}}
      for index, claim := range input.Claims {
        path := fmt.Sprintf("claims[%d]", index)
        id := strings.TrimSpace(claim.ID)
        if id == "" {
          issues = append(issues, morningIssue("claim_id", path+".id", "must not be empty"))
        } else if _, exists := claimIDs[id]; exists {
          issues = append(issues, morningIssue("claim_id", path+".id", "must be unique"))
        } else {
          claimIDs[id] = struct{}{}
        }
        if strings.TrimSpace(claim.Statement) == "" {
          issues = append(issues, morningIssue("claim", path+".statement", "must not be empty"))
        }
        if category := claim.Category; category != "" {
          if _, exists := validCategories[category]; !exists {
            issues = append(issues, morningIssue("claim_category", path+".category", "must be source_fact, analysis, or mixed"))
          }
        }
        if _, exists := validConfidence[claim.Confidence]; !exists {
          issues = append(issues, morningIssue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        if asOf, dateErr := time.Parse("2006-01-02", claim.AsOf); dateErr != nil {
          issues = append(issues, morningIssue("as_of", path+".as_of", "must use YYYY-MM-DD"))
        } else if err == nil && asOf.After(edition) {
          issues = append(issues, morningIssue("future_data", path+".as_of", "must not post-date edition_date"))
        }
        if len(claim.QuoteIDs) == 0 {
          issues = append(issues, morningIssue("quote_reference", path+".quote_ids", "must reference at least one quote"))
        }
        for quoteIndex, quoteID := range claim.QuoteIDs {
          if _, exists := quoteIDs[quoteID]; !exists {
            issues = append(issues, morningIssue("quote_reference", fmt.Sprintf("%s.quote_ids[%d]", path, quoteIndex), "must reference a submitted quote ID"))
            continue
          }
          usedQuoteIDs[quoteID] = struct{}{}
        }
      }
      for quoteID := range quoteIDs {
        if _, used := usedQuoteIDs[quoteID]; !used {
          issues = append(issues, morningIssue("unused_quote", "quotes", "quote "+quoteID+" is not referenced by any claim"))
        }
      }
      return issues
    }

    func validateCoverage(requirements []CoverageRequirement, records []CoverageRecord, claims []EvidenceClaim) []Issue {
      issues := []Issue{}
      required := map[string]CoverageRequirement{}
      for index, item := range requirements {
        path := fmt.Sprintf("required_coverage[%d]", index)
        id := strings.TrimSpace(item.ID)
        if id == "" {
          issues = append(issues, morningIssue("coverage_requirement", path+".id", "must not be empty"))
        } else if _, exists := required[id]; exists {
          issues = append(issues, morningIssue("coverage_requirement", path+".id", "must be unique"))
        } else {
          required[id] = item
        }
      }
      claimIDs := map[string]struct{}{}
      for _, claim := range claims {
        if id := strings.TrimSpace(claim.ID); id != "" {
          claimIDs[id] = struct{}{}
        }
      }
      quoteStatuses := map[string]struct{}{"observed": {}, "unavailable": {}, "check_failed": {}, "not_applicable": {}}
      newsStatuses := map[string]struct{}{"material_news_found": {}, "no_material_news": {}, "check_failed": {}, "not_applicable": {}}
      seen := map[string]struct{}{}
      for index, record := range records {
        path := fmt.Sprintf("coverage[%d]", index)
        id := strings.TrimSpace(record.ObjectID)
        requirement, expected := required[id]
        if id == "" {
          issues = append(issues, morningIssue("coverage_object_id", path+".object_id", "must not be empty"))
        } else if !expected {
          issues = append(issues, morningIssue("coverage_object_id", path+".object_id", "must reference required_coverage"))
        } else if _, duplicate := seen[id]; duplicate {
          issues = append(issues, morningIssue("coverage_object_id", path+".object_id", "must be unique"))
        } else {
          seen[id] = struct{}{}
          if strings.TrimSpace(record.Name) != strings.TrimSpace(requirement.Name) {
            issues = append(issues, morningIssue("coverage_name", path+".name", "must match required_coverage name"))
          }
          if strings.TrimSpace(record.Kind) != strings.TrimSpace(requirement.Kind) {
            issues = append(issues, morningIssue("coverage_kind", path+".kind", "must match required_coverage kind"))
          }
        }
        if _, valid := quoteStatuses[record.QuoteStatus]; !valid {
          issues = append(issues, morningIssue("quote_status", path+".quote_status", "must be observed, unavailable, check_failed, or not_applicable"))
        }
        if _, valid := newsStatuses[record.NewsStatus]; !valid {
          issues = append(issues, morningIssue("news_status", path+".news_status", "must be material_news_found, no_material_news, check_failed, or not_applicable"))
        }
        if strings.TrimSpace(record.CheckedUntil) == "" {
          issues = append(issues, morningIssue("coverage_checked_until", path+".checked_until", "must not be empty"))
        }
        if strings.TrimSpace(record.Summary) == "" {
          issues = append(issues, morningIssue("coverage_summary", path+".summary", "must explain what was checked and the result"))
        }
        if record.NewsStatus == "material_news_found" && len(record.ClaimIDs) == 0 {
          issues = append(issues, morningIssue("coverage_claims", path+".claim_ids", "must reference at least one claim when material news was found"))
        }
        if record.NewsStatus == "no_material_news" && len(record.ClaimIDs) > 0 {
          issues = append(issues, morningIssue("coverage_claims", path+".claim_ids", "must be empty when no material news was found"))
        }
        for claimIndex, claimID := range record.ClaimIDs {
          if _, exists := claimIDs[claimID]; !exists {
            issues = append(issues, morningIssue("coverage_claims", fmt.Sprintf("%s.claim_ids[%d]", path, claimIndex), "must reference a claim submitted in this evidence artifact"))
          }
        }
      }
      for id := range required {
        if _, exists := seen[id]; !exists {
          issues = append(issues, morningIssue("coverage_missing", "coverage", "missing required coverage object "+id))
        }
      }
      return issues
    }

    func validMorningArtifactPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func validArtifactID(value string) bool {
      value = strings.TrimSpace(value)
      return strings.HasPrefix(value, "artifact-") && len(value) >= len("artifact-")+16
    }

    func writeMorningFile(path string, payload []byte) error {
      clean := filepath.Clean(path)
      if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("refuse symlink output %q", clean)
      }
      if err := os.MkdirAll(filepath.Dir(clean), 0700); err != nil {
        return fmt.Errorf("create output directory: %w", err)
      }
      if err := os.WriteFile(clean, payload, 0600); err != nil {
        return fmt.Errorf("write output: %w", err)
      }
      return nil
    }

    func morningIssue(code, path, message string) Issue {
      repair := "Correct every reported field and call the typed tool again."
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}

go_tool "submit_breakfast_packet" {
  description = "Validate and write the breakfast packet. This tool performs basic mechanical checks only. `market_snapshot.direction`: `up`, `down`, `flat`, `unavailable`; an unavailable market must use `as_of = unavailable`, while an observed market must use its actual YYYY-MM-DD quote date. `events.category`: `macro`, `policy`, `industry`, `company`; `evidence_catalog.category`: `source_fact` (default when omitted), `analysis`, or `mixed`; `events.status`: `occurred`, `announced`, `expected`. Required market keys: `sp500`, `nasdaq`, `china_adr`, `a50`, `usdcnh`, `gold`, `crude`. Every configured watchlist object must have a coverage record; submit its `object_id` and coverage result while the typed tool fills `name` and `kind` from `required_coverage`. `no_material_news` is a valid outcome and does not require an event. Missing optional source paths are skipped. Every HTTP(S) URL found in existing host-supplied source paths is automatically retained in the packet root `source_urls` index and should also be copied to the corresponding packet item when applicable."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "regexp"
      "sort"
      "strings"
      "time"
    )

    type MarketMetric struct {
      Key         string   `json:"key"`
      Label       string   `json:"label"`
      Value       string   `json:"value"`
      Change      string   `json:"change"`
      Direction   string   `json:"direction"`
      AsOf        string   `json:"as_of"`
      EvidenceIDs []string `json:"evidence_ids"`
      SourceURLs  []string `json:"source_urls,omitempty"`
    }

    type CoverageRequirement struct {
      ID           string   `json:"id"`
      Name         string   `json:"name"`
      Kind         string   `json:"kind"`
      QuoteSymbols []string `json:"quote_symbols"`
      SearchTerms  []string `json:"search_terms"`
    }

    type CoverageRecord struct {
      ObjectID     string   `json:"object_id"`
      Name         string   `json:"name,omitempty"`
      Kind         string   `json:"kind,omitempty"`
      QuoteStatus  string   `json:"quote_status"`
      NewsStatus   string   `json:"news_status"`
      CheckedUntil string   `json:"checked_until"`
      Summary      string   `json:"summary"`
      EventIDs     []string `json:"event_ids,omitempty"`
      SourceURLs   []string `json:"source_urls,omitempty"`
    }

    type BreakfastEvent struct {
      ID          string   `json:"id"`
      Headline    string   `json:"headline"`
      Category    string   `json:"category"`
      Status      string   `json:"status"`
      AsOf        string   `json:"as_of"`
      Importance  int      `json:"importance"`
      Summary     string   `json:"summary"`
      EvidenceIDs []string `json:"evidence_ids"`
      SourceURLs  []string `json:"source_urls,omitempty"`
    }

    type InstitutionalItem struct {
      ID          string   `json:"id"`
      Section     string   `json:"section"`
      Headline    string   `json:"headline"`
      Summary     string   `json:"summary"`
      EvidenceIDs []string `json:"evidence_ids"`
      SourceURLs  []string `json:"source_urls,omitempty"`
    }

    type CalendarEvent struct {
      ID          string   `json:"id"`
      PubTime     string   `json:"pub_time"`
      Importance  int      `json:"importance"`
      Title       string   `json:"title"`
      Previous    string   `json:"previous"`
      Consensus   string   `json:"consensus"`
      Actual      string   `json:"actual"`
      Affect      string   `json:"affect"`
      EvidenceIDs []string `json:"evidence_ids"`
      SourceURLs  []string `json:"source_urls,omitempty"`
    }

    type CatalogClaim struct {
      ID         string `json:"id"`
      Claim      string `json:"claim"`
      Category   string `json:"category,omitempty"`
      Confidence string `json:"confidence"`
      SourceURLs []string `json:"source_urls,omitempty"`
    }

    type UpstreamClaim struct {
      ID         string `json:"id"`
      Statement  string `json:"statement"`
      Category   string `json:"category,omitempty"`
      Confidence string `json:"confidence"`
    }

    type UpstreamEvidence struct {
      Claims []UpstreamClaim `json:"claims"`
    }

    type Input struct {
      ArtifactID       string           `json:"artifact_id"`
      ArtifactPath     string           `json:"_r42_artifact_path"`
      SourcePaths      []string         `json:"source_paths,omitempty"`
      SourceURLs       []string         `json:"source_urls,omitempty"`
      EditionDate      string           `json:"edition_date"`
      CutoffTime       string           `json:"cutoff_time"`
      ReviewedArtifacts []string                 `json:"reviewed_artifacts"`
      RequiredCoverage []CoverageRequirement     `json:"required_coverage"`
      Coverage         []CoverageRecord          `json:"coverage"`
      MarketSnapshot   []MarketMetric            `json:"market_snapshot"`
      Events           []BreakfastEvent          `json:"events"`
      NoiseNotes       []string                  `json:"noise_notes"`
      InstitutionalScan []InstitutionalItem      `json:"institutional_scan"`
      CalendarEvents   []CalendarEvent           `json:"calendar_events"`
      EvidenceCatalog  []CatalogClaim            `json:"evidence_catalog"`
    }

    type Output string

    var httpURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      normalizePacketCoverage(&input)
      issues := validatePacket(input)
      requiredURLs, readErr := sourceURLsFromPaths(input.SourcePaths)
      if readErr != "" {
        issues = append(issues, packetIssue("source_url_read", "source_paths", readErr))
      } else {
        input.SourceURLs = mergeSourceURLs(input.SourceURLs, requiredURLs)
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      path := input.ArtifactPath
      input.ArtifactPath = ""
      input.SourcePaths = nil
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode breakfast packet: %w", err)
      }
      payload = append(payload, '\n')
      if err := writePacketFile(path, payload); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func mergeSourceURLs(existing, required []string) []string {
      seen := map[string]struct{}{}
      merged := make([]string, 0, len(existing)+len(required))
      for _, urls := range [][]string{existing, required} {
        for _, url := range urls {
          url = strings.TrimSpace(url)
          if url == "" {
            continue
          }
          if _, ok := seen[url]; ok {
            continue
          }
          seen[url] = struct{}{}
          merged = append(merged, url)
        }
      }
      sort.Strings(merged)
      return merged
    }

    func sourceURLsFromPaths(paths []string) ([]string, string) {
      found := map[string]struct{}{}
      for _, path := range paths {
        clean := filepath.Clean(strings.TrimSpace(path))
        info, err := os.Stat(clean)
        if err != nil {
          if os.IsNotExist(err) {
            continue
          }
          return nil, fmt.Sprintf("cannot read source path %q: %v", clean, err)
        }
        visit := func(filePath string) error {
          payload, readErr := os.ReadFile(filePath)
          if readErr != nil {
            return readErr
          }
          var value any
          if json.Unmarshal(payload, &value) == nil {
            collectSourceURLs(value, found)
          }
          collectTextURLs(string(payload), found)
          return nil
        }
        if info.IsDir() {
          if walkErr := filepath.Walk(clean, func(filePath string, entry os.FileInfo, walkErr error) error {
            if walkErr != nil {
              return walkErr
            }
            if entry.IsDir() {
              return nil
            }
            return visit(filePath)
          }); walkErr != nil {
            return nil, fmt.Sprintf("cannot read source path %q: %v", clean, walkErr)
          }
        } else if err := visit(clean); err != nil {
          return nil, fmt.Sprintf("cannot read source path %q: %v", clean, err)
        }
      }
      urls := make([]string, 0, len(found))
      for url := range found {
        urls = append(urls, url)
      }
      sort.Strings(urls)
      return urls, ""
    }

    func normalizePacketCoverage(input *Input) {
      required := make(map[string]CoverageRequirement, len(input.RequiredCoverage))
      for _, requirement := range input.RequiredCoverage {
        id := strings.TrimSpace(requirement.ID)
        if id == "" {
          continue
        }
        required[id] = requirement
      }
      for index := range input.Coverage {
        id := strings.TrimSpace(input.Coverage[index].ObjectID)
        requirement, ok := required[id]
        if !ok {
          continue
        }
        input.Coverage[index].Name = requirement.Name
        input.Coverage[index].Kind = requirement.Kind
      }
    }

    func collectSourceURLs(value any, found map[string]struct{}) {
      switch item := value.(type) {
      case map[string]any:
        for key, child := range item {
          normalizedKey := strings.ToLower(strings.TrimSpace(key))
          if normalizedKey == "url" || normalizedKey == "urls" || normalizedKey == "source_urls" {
            collectURLValues(child, found)
            continue
          }
          collectSourceURLs(child, found)
        }
      case []any:
        for _, child := range item {
          collectSourceURLs(child, found)
        }
      }
    }

    func collectURLValues(value any, found map[string]struct{}) {
      switch item := value.(type) {
      case string:
        value := strings.TrimSpace(item)
        if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
          found[value] = struct{}{}
        }
      case []any:
        for _, child := range item {
          collectURLValues(child, found)
        }
      }
    }

    func collectTextURLs(text string, found map[string]struct{}) {
      for _, match := range httpURLPattern.FindAllString(text, -1) {
        match = strings.TrimRight(match, ".,;:!?)]}")
        if match != "" {
          found[match] = struct{}{}
        }
      }
    }

    func validatePacket(input Input) []Issue {
      issues := []Issue{}
      if !validPacketPath(input.ArtifactPath, "breakfast-packet.json") {
        issues = append(issues, packetIssue("artifact_path", "_r42_artifact_path", "must be an absolute block artifact path ending in breakfast-packet.json"))
      }
      if !validPacketArtifactID(input.ArtifactID) {
        issues = append(issues, packetIssue("artifact_id", "artifact_id", "must be an r42 artifact ID"))
      }
      edition, editionErr := time.Parse("2006-01-02", input.EditionDate)
      if editionErr != nil {
        issues = append(issues, packetIssue("edition_date", "edition_date", "must use YYYY-MM-DD"))
      }
      if strings.TrimSpace(input.CutoffTime) == "" {
        issues = append(issues, packetIssue("cutoff_time", "cutoff_time", "must state a time and timezone"))
      }
      upstreamClaims := map[string]UpstreamClaim{}
      validCategories := map[string]struct{}{"source_fact": {}, "analysis": {}, "mixed": {}}
      for index, path := range input.ReviewedArtifacts {
        if !validPacketPath(path, "evidence.json") {
          issues = append(issues, packetIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "must be an absolute block artifact path ending in evidence.json"))
          continue
        }
        document := UpstreamEvidence{Claims: []UpstreamClaim{}}
        payload, readErr := os.ReadFile(filepath.Clean(path))
        if readErr != nil {
          issues = append(issues, packetIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "cannot read evidence artifact"))
          continue
        }
        if decodeErr := json.Unmarshal(payload, &document); decodeErr != nil {
          issues = append(issues, packetIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "cannot decode evidence artifact"))
          continue
        }
        for claimIndex, claim := range document.Claims {
          if _, duplicate := upstreamClaims[claim.ID]; duplicate {
            issues = append(issues, packetIssue("upstream_claim_id", fmt.Sprintf("reviewed_artifacts[%d].claims[%d].id", index, claimIndex), "must be globally unique across evidence artifacts"))
            continue
          }
          if category := claim.Category; category != "" {
            if _, valid := validCategories[category]; !valid {
              issues = append(issues, packetIssue("upstream_claim_category", fmt.Sprintf("reviewed_artifacts[%d].claims[%d].category", index, claimIndex), "must be source_fact, analysis, or mixed"))
            }
          }
          upstreamClaims[claim.ID] = claim
        }
      }

      evidenceIDs := map[string]struct{}{}
      confidenceValues := map[string]struct{}{"high": {}, "medium": {}, "low": {}}
      for index, item := range input.EvidenceCatalog {
        path := fmt.Sprintf("evidence_catalog[%d]", index)
        id := strings.TrimSpace(item.ID)
        if id == "" {
          issues = append(issues, packetIssue("evidence_id", path+".id", "must not be empty"))
        } else if _, exists := evidenceIDs[id]; exists {
          issues = append(issues, packetIssue("evidence_id", path+".id", "must be unique"))
        } else {
          evidenceIDs[id] = struct{}{}
        }
        if strings.TrimSpace(item.Claim) == "" {
          issues = append(issues, packetIssue("evidence_claim", path+".claim", "must not be empty"))
        }
        if category := item.Category; category != "" {
          if _, valid := validCategories[category]; !valid {
            issues = append(issues, packetIssue("evidence_category", path+".category", "must be source_fact, analysis, or mixed"))
          }
        }
        if _, exists := confidenceValues[item.Confidence]; !exists {
          issues = append(issues, packetIssue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        if len(input.ReviewedArtifacts) > 0 {
          upstream, exists := upstreamClaims[id]
          if !exists {
            issues = append(issues, packetIssue("upstream_evidence_reference", path+".id", "must reference a claim in reviewed_artifacts"))
          } else {
            sameClaim := normalizeEvidenceText(upstream.Statement) == normalizeEvidenceText(item.Claim)
            sameCategory := normalizeClaimCategory(upstream.Category) == normalizeClaimCategory(item.Category)
            if !sameClaim || !sameCategory || upstream.Confidence != item.Confidence {
              issues = append(issues, packetIssue("upstream_evidence_mismatch", path, "claim text, category, and confidence must match the upstream evidence claim"))
            }
          }
        }
      }

      packetEventIDs := map[string]struct{}{}
      for _, event := range input.Events {
        packetEventIDs[event.ID] = struct{}{}
      }
      issues = append(issues, validatePacketCoverage(input.RequiredCoverage, input.Coverage, packetEventIDs)...)

      requiredKeys := []string{"sp500", "nasdaq", "china_adr", "a50", "usdcnh", "gold", "crude"}
      requiredSet := map[string]struct{}{}
      for _, key := range requiredKeys {
        requiredSet[key] = struct{}{}
      }
      seenKeys := map[string]struct{}{}
      directions := map[string]struct{}{"up": {}, "down": {}, "flat": {}, "unavailable": {}}
      for index, metric := range input.MarketSnapshot {
        path := fmt.Sprintf("market_snapshot[%d]", index)
        if _, expected := requiredSet[metric.Key]; !expected {
          issues = append(issues, packetIssue("market_key", path+".key", "must be one of the seven required market keys"))
        } else if _, exists := seenKeys[metric.Key]; exists {
          issues = append(issues, packetIssue("market_key", path+".key", "must not repeat a market key"))
        } else {
          seenKeys[metric.Key] = struct{}{}
        }
        if strings.TrimSpace(metric.Label) == "" || strings.TrimSpace(metric.Value) == "" {
          issues = append(issues, packetIssue("market_value", path, "label and value must not be empty; use unavailable explicitly"))
        }
        if _, exists := directions[metric.Direction]; !exists {
          issues = append(issues, packetIssue("direction", path+".direction", "must be up, down, flat, or unavailable"))
        }
        if metric.Direction != "unavailable" && strings.TrimSpace(metric.Change) == "" {
          issues = append(issues, packetIssue("market_change", path+".change", "must not be empty unless direction is unavailable"))
        }
        issues = append(issues, validateMarketSnapshotDate(metric.AsOf, metric.Direction, edition, editionErr, path+".as_of")...)
        if metric.Direction == "unavailable" {
          issues = append(issues, validateOptionalEvidenceReferences(metric.EvidenceIDs, evidenceIDs, path+".evidence_ids")...)
        } else {
          issues = append(issues, validateEvidenceReferences(metric.EvidenceIDs, evidenceIDs, path+".evidence_ids")...)
        }
      }
      for _, key := range requiredKeys {
        if _, exists := seenKeys[key]; !exists {
          issues = append(issues, packetIssue("market_coverage", "market_snapshot", "missing required market key "+key))
        }
      }

      eventIDs := map[string]struct{}{}
      headlines := map[string]struct{}{}
      categories := map[string]struct{}{"macro": {}, "policy": {}, "industry": {}, "company": {}}
      statuses := map[string]struct{}{"occurred": {}, "announced": {}, "expected": {}}
      for index, event := range input.Events {
        path := fmt.Sprintf("events[%d]", index)
        id := strings.TrimSpace(event.ID)
        if id == "" {
          issues = append(issues, packetIssue("event_id", path+".id", "must not be empty"))
        } else if _, exists := eventIDs[id]; exists {
          issues = append(issues, packetIssue("event_id", path+".id", "must be unique"))
        } else {
          eventIDs[id] = struct{}{}
        }
        normalizedHeadline := normalizeHeadline(event.Headline)
        if normalizedHeadline == "" {
          issues = append(issues, packetIssue("headline", path+".headline", "must not be empty"))
        } else if _, exists := headlines[normalizedHeadline]; exists {
          issues = append(issues, packetIssue("duplicate_headline", path+".headline", "duplicates another headline after whitespace normalization"))
        } else {
          headlines[normalizedHeadline] = struct{}{}
        }
        if _, exists := categories[event.Category]; !exists {
          issues = append(issues, packetIssue("event_category", path+".category", "must be macro, policy, industry, or company"))
        }
        if _, exists := statuses[event.Status]; !exists {
          issues = append(issues, packetIssue("event_status", path+".status", "must be occurred, announced, or expected"))
        }
        if event.Importance < 1 || event.Importance > 5 {
          issues = append(issues, packetIssue("importance", path+".importance", "must be an integer from 1 through 5"))
        }
        if strings.TrimSpace(event.Summary) == "" {
          issues = append(issues, packetIssue("event_summary", path+".summary", "must not be empty"))
        }
        issues = append(issues, validatePacketDate(event.AsOf, edition, editionErr, path+".as_of")...)
        issues = append(issues, validateEvidenceReferences(event.EvidenceIDs, evidenceIDs, path+".evidence_ids")...)
      }
      institutionalSections := map[string]struct{}{
        "market_liquidity": {}, "bonds_rates": {}, "commodities": {},
        "company_announcements": {}, "sell_side": {},
      }
      if len(input.InstitutionalScan) == 0 {
        issues = append(issues, packetIssue("institutional_scan", "institutional_scan", "must contain at least one decision-relevant institutional signal"))
      }
      scanIDs := map[string]struct{}{}
      for index, item := range input.InstitutionalScan {
        path := fmt.Sprintf("institutional_scan[%d]", index)
        if strings.TrimSpace(item.ID) == "" {
          issues = append(issues, packetIssue("institutional_scan_id", path+".id", "must not be empty"))
        } else if _, duplicate := scanIDs[item.ID]; duplicate {
          issues = append(issues, packetIssue("institutional_scan_id", path+".id", "must be unique"))
        } else {
          scanIDs[item.ID] = struct{}{}
        }
        if _, exists := institutionalSections[item.Section]; !exists {
          issues = append(issues, packetIssue("institutional_section", path+".section", "must be market_liquidity, bonds_rates, commodities, company_announcements, or sell_side"))
        }
        if strings.TrimSpace(item.Headline) == "" || strings.TrimSpace(item.Summary) == "" {
          issues = append(issues, packetIssue("institutional_scan", path, "headline and summary must not be empty"))
        }
        issues = append(issues, validateEvidenceReferences(item.EvidenceIDs, evidenceIDs, path+".evidence_ids")...)
      }
      if len(input.CalendarEvents) == 0 {
        issues = append(issues, packetIssue("calendar_events", "calendar_events", "must contain at least one scheduled event or an explicit no-event entry"))
      }
      calendarIDs := map[string]struct{}{}
      for index, item := range input.CalendarEvents {
        path := fmt.Sprintf("calendar_events[%d]", index)
        if strings.TrimSpace(item.ID) == "" {
          issues = append(issues, packetIssue("calendar_event_id", path+".id", "must not be empty"))
        } else if _, duplicate := calendarIDs[item.ID]; duplicate {
          issues = append(issues, packetIssue("calendar_event_id", path+".id", "must be unique"))
        } else {
          calendarIDs[item.ID] = struct{}{}
        }
        if strings.TrimSpace(item.PubTime) == "" || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Affect) == "" {
          issues = append(issues, packetIssue("calendar_event", path, "pub_time, title, and affect must not be empty"))
        }
        if item.Importance < 1 || item.Importance > 5 {
          issues = append(issues, packetIssue("importance", path+".importance", "must be an integer from 1 through 5"))
        }
        issues = append(issues, validateEvidenceReferences(item.EvidenceIDs, evidenceIDs, path+".evidence_ids")...)
      }
      return issues
    }

    func normalizeClaimCategory(value string) string {
      if strings.TrimSpace(value) == "" {
        return "source_fact"
      }
      return strings.TrimSpace(value)
    }

    func validatePacketCoverage(requirements []CoverageRequirement, records []CoverageRecord, eventIDs map[string]struct{}) []Issue {
      issues := []Issue{}
      required := map[string]CoverageRequirement{}
      for index, item := range requirements {
        path := fmt.Sprintf("required_coverage[%d]", index)
        id := strings.TrimSpace(item.ID)
        if id == "" {
          issues = append(issues, packetIssue("coverage_requirement", path+".id", "must not be empty"))
        } else if _, exists := required[id]; exists {
          issues = append(issues, packetIssue("coverage_requirement", path+".id", "must be unique"))
        } else {
          required[id] = item
        }
      }
      quoteStatuses := map[string]struct{}{"observed": {}, "unavailable": {}, "check_failed": {}, "not_applicable": {}}
      newsStatuses := map[string]struct{}{"material_news_found": {}, "no_material_news": {}, "check_failed": {}, "not_applicable": {}}
      seen := map[string]struct{}{}
      for index, record := range records {
        path := fmt.Sprintf("coverage[%d]", index)
        id := strings.TrimSpace(record.ObjectID)
        requirement, expected := required[id]
        if id == "" {
          issues = append(issues, packetIssue("coverage_object_id", path+".object_id", "must not be empty"))
        } else if !expected {
          issues = append(issues, packetIssue("coverage_object_id", path+".object_id", "must reference required_coverage"))
        } else if _, duplicate := seen[id]; duplicate {
          issues = append(issues, packetIssue("coverage_object_id", path+".object_id", "must be unique"))
        } else {
          seen[id] = struct{}{}
          if strings.TrimSpace(record.Name) != strings.TrimSpace(requirement.Name) {
            issues = append(issues, packetIssue("coverage_name", path+".name", "must match required_coverage name"))
          }
          if strings.TrimSpace(record.Kind) != strings.TrimSpace(requirement.Kind) {
            issues = append(issues, packetIssue("coverage_kind", path+".kind", "must match required_coverage kind"))
          }
        }
        if _, valid := quoteStatuses[record.QuoteStatus]; !valid {
          issues = append(issues, packetIssue("quote_status", path+".quote_status", "must be observed, unavailable, check_failed, or not_applicable"))
        }
        if _, valid := newsStatuses[record.NewsStatus]; !valid {
          issues = append(issues, packetIssue("news_status", path+".news_status", "must be material_news_found, no_material_news, check_failed, or not_applicable"))
        }
        if strings.TrimSpace(record.CheckedUntil) == "" {
          issues = append(issues, packetIssue("coverage_checked_until", path+".checked_until", "must not be empty"))
        }
        if strings.TrimSpace(record.Summary) == "" {
          issues = append(issues, packetIssue("coverage_summary", path+".summary", "must explain what was checked and the result"))
        }
        if record.NewsStatus == "material_news_found" && len(record.EventIDs) == 0 {
          issues = append(issues, packetIssue("coverage_events", path+".event_ids", "must reference at least one packet event when material news was found"))
        }
        if record.NewsStatus == "no_material_news" && len(record.EventIDs) > 0 {
          issues = append(issues, packetIssue("coverage_events", path+".event_ids", "must be empty when no material news was found"))
        }
        for eventIndex, eventID := range record.EventIDs {
          if _, exists := eventIDs[eventID]; !exists {
            issues = append(issues, packetIssue("coverage_events", fmt.Sprintf("%s.event_ids[%d]", path, eventIndex), "must reference an event in this packet"))
          }
        }
      }
      for id := range required {
        if _, exists := seen[id]; !exists {
          issues = append(issues, packetIssue("coverage_missing", "coverage", "missing required coverage object "+id))
        }
      }
      return issues
    }

    func validatePacketDate(value string, edition time.Time, editionErr error, path string) []Issue {
      parsed, err := time.Parse("2006-01-02", value)
      if err != nil {
        return []Issue{packetIssue("as_of", path, "must use YYYY-MM-DD")}
      }
      if editionErr == nil && parsed.After(edition) {
        return []Issue{packetIssue("future_data", path, "must not post-date edition_date")}
      }
      return []Issue{}
    }

    func validateMarketSnapshotDate(value, direction string, edition time.Time, editionErr error, path string) []Issue {
      if direction == "unavailable" {
        if strings.TrimSpace(value) == "unavailable" {
          return []Issue{}
        }
        return []Issue{packetIssue("as_of", path, "must be unavailable when direction is unavailable")}
      }
      return validatePacketDate(value, edition, editionErr, path)
    }

    func validateEvidenceReferences(ids []string, catalog map[string]struct{}, path string) []Issue {
      issues := []Issue{}
      if len(ids) == 0 {
        return []Issue{packetIssue("evidence_reference", path, "must contain at least one evidence ID")}
      }
      for index, id := range ids {
        if _, exists := catalog[id]; !exists {
          issues = append(issues, packetIssue("evidence_reference", fmt.Sprintf("%s[%d]", path, index), "must reference evidence_catalog"))
        }
      }
      return issues
    }

    func validateOptionalEvidenceReferences(ids []string, catalog map[string]struct{}, path string) []Issue {
      issues := []Issue{}
      for index, id := range ids {
        if _, exists := catalog[id]; !exists {
          issues = append(issues, packetIssue("evidence_reference", fmt.Sprintf("%s[%d]", path, index), "must reference evidence_catalog"))
        }
      }
      return issues
    }

    func normalizeHeadline(value string) string {
      return strings.ToLower(strings.Join(strings.Fields(value), " "))
    }

    func normalizeEvidenceText(value string) string {
      return strings.Join(strings.Fields(value), " ")
    }

    func validPacketPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func validPacketArtifactID(value string) bool {
      value = strings.TrimSpace(value)
      return strings.HasPrefix(value, "artifact-") && len(value) >= len("artifact-")+16
    }

    func writePacketFile(path string, payload []byte) error {
      clean := filepath.Clean(path)
      if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("refuse symlink output %q", clean)
      }
      if err := os.MkdirAll(filepath.Dir(clean), 0700); err != nil {
        return fmt.Errorf("create packet directory: %w", err)
      }
      if err := os.WriteFile(clean, payload, 0600); err != nil {
        return fmt.Errorf("write packet: %w", err)
      }
      return nil
    }

    func packetIssue(code, path, message string) Issue {
      repair := "Correct every reported packet field and call the typed tool again."
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}

go_tool "submit_morning_news_digest" {
  description = "Write the bounded news selection for Publisher. Selected event IDs must exist in the packet, each selected source URL must come from that event or the packet source index, and every selected URL requires a saved fetch artifact. Summaries are limited to three sentences; this tool performs mechanical checks only and does not perform semantic QC."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
      "unicode"
    )

    type DigestItem struct {
      EventID          string   `json:"event_id"`
      Headline         string   `json:"headline"`
      SourceURLs       []string `json:"source_urls,omitempty"`
      Status           string   `json:"status"`
      Summary          string   `json:"summary"`
      FetchArtifactIDs []string `json:"fetch_artifact_ids,omitempty"`
    }

    type Input struct {
      ArtifactID   string       `json:"artifact_id"`
      ArtifactPath string       `json:"_r42_artifact_path"`
      PacketPath   string       `json:"packet_path"`
      MaxItems     int          `json:"max_items"`
      Items        []DigestItem `json:"items"`
    }

    type DigestPacketEvent struct {
      ID         string   `json:"id"`
      Headline   string   `json:"headline"`
      SourceURLs []string `json:"source_urls"`
    }

    type DigestPacket struct {
      SourceURLs []string             `json:"source_urls"`
      Events     []DigestPacketEvent `json:"events"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      packet, readErr := readDigestPacket(input.PacketPath)
      issues := validateNewsDigest(input, packet, readErr)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      path := input.ArtifactPath
      input.ArtifactPath = ""
      input.PacketPath = ""
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode news digest: %w", err)
      }
      payload = append(payload, '\n')
      if err := writeDigestFile(path, payload); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateNewsDigest(input Input, packet DigestPacket, readErr error) []Issue {
      issues := []Issue{}
      if !validDigestPath(input.ArtifactPath, "news-digest.json") {
        issues = append(issues, digestIssue("artifact_path", "_r42_artifact_path", "must be an absolute block artifact path ending in news-digest.json"))
      }
      if !validDigestArtifactID(input.ArtifactID) {
        issues = append(issues, digestIssue("artifact_id", "artifact_id", "must be an r42 artifact ID"))
      }
      if !validDigestPath(input.PacketPath, "breakfast-packet.json") {
        issues = append(issues, digestIssue("packet_path", "packet_path", "must be an absolute block artifact path ending in breakfast-packet.json"))
      } else if readErr != nil {
        issues = append(issues, digestIssue("packet_read", "packet_path", readErr.Error()))
      }
      if input.MaxItems < 1 {
        issues = append(issues, digestIssue("max_items", "max_items", "must be positive"))
      }
      if len(input.Items) > input.MaxItems {
        issues = append(issues, digestIssue("item_count", "items", "must not exceed max_items"))
      }
      events := map[string]DigestPacketEvent{}
      packetURLs := map[string]struct{}{}
      for _, url := range packet.SourceURLs {
        packetURLs[strings.TrimSpace(url)] = struct{}{}
      }
      for _, event := range packet.Events {
        events[event.ID] = event
        for _, url := range event.SourceURLs {
          packetURLs[strings.TrimSpace(url)] = struct{}{}
        }
      }
      seenEvents := map[string]struct{}{}
      statuses := map[string]struct{}{"fetched": {}, "fetch_failed": {}, "no_url": {}}
      for index, item := range input.Items {
        path := fmt.Sprintf("items[%d]", index)
        event, exists := events[item.EventID]
        if !exists {
          issues = append(issues, digestIssue("event_reference", path+".event_id", "must reference an event in the packet"))
        } else {
          if _, duplicate := seenEvents[item.EventID]; duplicate {
            issues = append(issues, digestIssue("event_reference", path+".event_id", "must not repeat an event"))
          }
          seenEvents[item.EventID] = struct{}{}
          if strings.TrimSpace(item.Headline) == "" {
            issues = append(issues, digestIssue("headline", path+".headline", "must not be empty"))
          }
          requiredURLs := nonEmptyURLs(event.SourceURLs)
          submittedURLs := nonEmptyURLs(item.SourceURLs)
          if len(requiredURLs) > 0 && !sameURLSet(requiredURLs, submittedURLs) {
            issues = append(issues, digestIssue("source_url", path+".source_urls", "must include every URL on the selected packet event"))
          }
          for url := range submittedURLs {
            if _, allowed := packetURLs[url]; !allowed {
              issues = append(issues, digestIssue("source_url", path+".source_urls", "must reference a URL in the packet"))
            }
          }
          if len(requiredURLs) == 0 && len(submittedURLs) > 0 {
            issues = append(issues, digestIssue("source_url", path+".source_urls", "must be empty when the packet event has no URL"))
          }
          if len(requiredURLs) > 0 && len(item.FetchArtifactIDs) == 0 {
            issues = append(issues, digestIssue("fetch_artifact", path+".fetch_artifact_ids", "a selected URL requires at least one saved web_fetch artifact"))
          }
          if len(requiredURLs) == 0 && len(item.FetchArtifactIDs) > 0 {
            issues = append(issues, digestIssue("fetch_artifact", path+".fetch_artifact_ids", "must be empty when the packet event has no URL"))
          }
        }
        if _, valid := statuses[item.Status]; !valid {
          issues = append(issues, digestIssue("status", path+".status", "must be fetched, fetch_failed, or no_url"))
        }
        if strings.TrimSpace(item.Summary) == "" {
          issues = append(issues, digestIssue("summary", path+".summary", "must not be empty"))
        } else if sentenceCount(item.Summary) > 3 {
          issues = append(issues, digestIssue("summary", path+".summary", "must contain at most three sentences"))
        }
      }
      return issues
    }

    func readDigestPacket(path string) (DigestPacket, error) {
      packet := DigestPacket{SourceURLs: []string{}, Events: []DigestPacketEvent{}}
      payload, err := os.ReadFile(filepath.Clean(path))
      if err != nil {
        return packet, fmt.Errorf("read packet: %w", err)
      }
      if err := json.Unmarshal(payload, &packet); err != nil {
        return packet, fmt.Errorf("decode packet: %w", err)
      }
      return packet, nil
    }

    func nonEmptyURLs(urls []string) map[string]struct{} {
      result := map[string]struct{}{}
      for _, url := range urls {
        if value := strings.TrimSpace(url); value != "" {
          result[value] = struct{}{}
        }
      }
      return result
    }

    func sameURLSet(left, right map[string]struct{}) bool {
      if len(left) != len(right) {
        return false
      }
      for url := range left {
        if _, ok := right[url]; !ok {
          return false
        }
      }
      return true
    }

    func sentenceCount(value string) int {
      count := 0
      previousPunctuation := false
      for _, runeValue := range strings.TrimSpace(value) {
        punctuation := strings.ContainsRune("。！？.!?", runeValue)
        if punctuation && !previousPunctuation {
          count++
        }
        previousPunctuation = punctuation || unicode.IsSpace(runeValue) && previousPunctuation
      }
      if count == 0 && strings.TrimSpace(value) != "" {
        return 1
      }
      return count
    }

    func validDigestPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func validDigestArtifactID(value string) bool {
      value = strings.TrimSpace(value)
      return strings.HasPrefix(value, "artifact-") && len(value) >= len("artifact-")+16
    }

    func writeDigestFile(path string, payload []byte) error {
      clean := filepath.Clean(path)
      if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("refuse symlink output %q", clean)
      }
      if err := os.MkdirAll(filepath.Dir(clean), 0700); err != nil {
        return fmt.Errorf("create digest directory: %w", err)
      }
      if err := os.WriteFile(clean, payload, 0600); err != nil {
        return fmt.Errorf("write digest: %w", err)
      }
      return nil
    }

    func digestIssue(code, path, message string) Issue {
      repair := "Correct the news digest and call the typed tool again."
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}

go_tool "submit_breakfast_review" {
  description = "Submit one independent packet review. `role`: `macro`, `sentiment`, `strategy`; `findings.confidence`: `high`, `medium`, `low`. Evidence IDs must exist in the frozen packet."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type ReviewFinding struct {
      ID                    string   `json:"id"`
      Statement             string   `json:"statement"`
      PlainLanguage         string   `json:"plain_language"`
      EvidenceIDs           []string `json:"evidence_ids"`
      Confidence            string   `json:"confidence"`
      Counterpoint          string   `json:"counterpoint"`
      FalsificationCondition string   `json:"falsification_condition"`
    }

    type Input struct {
      ArtifactID   string          `json:"artifact_id"`
      ArtifactPath string          `json:"_r42_artifact_path"`
      PacketPath   string          `json:"packet_path"`
      Role         string          `json:"role"`
      Headline     string          `json:"headline"`
      Findings     []ReviewFinding `json:"findings"`
    }

    type packetMetric struct {
      Key string `json:"key"`
    }

    type packetEvent struct {
      ID string `json:"id"`
    }

    type packetEvidence struct {
      ID string `json:"id"`
    }

    type packetDocument struct {
      MarketSnapshot  []packetMetric   `json:"market_snapshot"`
      Events          []packetEvent    `json:"events"`
      EvidenceCatalog []packetEvidence `json:"evidence_catalog"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      packet, readErr := readPacket(input.PacketPath)
      issues := validateReview(input, packet, readErr)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      path := input.ArtifactPath
      input.ArtifactPath = ""
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode breakfast review: %w", err)
      }
      payload = append(payload, '\n')
      if err := writeReviewFile(path, payload); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateReview(input Input, packet packetDocument, readErr error) []Issue {
      issues := []Issue{}
      if !validReviewPath(input.ArtifactPath, "review.json") {
        issues = append(issues, reviewIssue("artifact_path", "_r42_artifact_path", "must be an absolute block artifact path ending in review.json"))
      }
      if !validReviewArtifactID(input.ArtifactID) {
        issues = append(issues, reviewIssue("artifact_id", "artifact_id", "must be an r42 artifact ID"))
      }
      if !validReviewPath(input.PacketPath, "breakfast-packet.json") {
        issues = append(issues, reviewIssue("packet_path", "packet_path", "must be an absolute block artifact path ending in breakfast-packet.json"))
      } else if readErr != nil {
        issues = append(issues, reviewIssue("packet_read", "packet_path", readErr.Error()))
      }
      roles := map[string]struct{}{"macro": {}, "sentiment": {}, "strategy": {}}
      if _, exists := roles[input.Role]; !exists {
        issues = append(issues, reviewIssue("role", "role", "must be macro, sentiment, or strategy"))
      }
      if strings.TrimSpace(input.Headline) == "" {
        issues = append(issues, reviewIssue("headline", "headline", "must not be empty"))
      }
      if len(input.Findings) < 2 || len(input.Findings) > 5 {
        issues = append(issues, reviewIssue("findings", "findings", "must contain two through five findings"))
      }

      allowedEvidence := map[string]struct{}{}
      for _, metric := range packet.MarketSnapshot {
        allowedEvidence[metric.Key] = struct{}{}
      }
      for _, event := range packet.Events {
        allowedEvidence[event.ID] = struct{}{}
      }
      for _, evidence := range packet.EvidenceCatalog {
        allowedEvidence[evidence.ID] = struct{}{}
      }
      findingIDs := map[string]struct{}{}
      confidenceValues := map[string]struct{}{"high": {}, "medium": {}, "low": {}}
      for index, finding := range input.Findings {
        path := fmt.Sprintf("findings[%d]", index)
        id := strings.TrimSpace(finding.ID)
        if id == "" {
          issues = append(issues, reviewIssue("finding_id", path+".id", "must not be empty"))
        } else if _, exists := findingIDs[id]; exists {
          issues = append(issues, reviewIssue("finding_id", path+".id", "must be unique"))
        } else {
          findingIDs[id] = struct{}{}
        }
        if strings.TrimSpace(finding.Statement) == "" || strings.TrimSpace(finding.PlainLanguage) == "" {
          issues = append(issues, reviewIssue("finding_content", path, "statement and plain_language must not be empty"))
        }
        if strings.TrimSpace(finding.Counterpoint) == "" || strings.TrimSpace(finding.FalsificationCondition) == "" {
          issues = append(issues, reviewIssue("uncertainty", path, "counterpoint and falsification_condition must not be empty"))
        }
        if _, exists := confidenceValues[finding.Confidence]; !exists {
          issues = append(issues, reviewIssue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        if len(finding.EvidenceIDs) == 0 {
          issues = append(issues, reviewIssue("evidence_reference", path+".evidence_ids", "must contain at least one packet evidence ID"))
        }
        for evidenceIndex, evidenceID := range finding.EvidenceIDs {
          if _, exists := allowedEvidence[evidenceID]; !exists {
            issues = append(issues, reviewIssue("evidence_reference", fmt.Sprintf("%s.evidence_ids[%d]", path, evidenceIndex), "must reference a metric key, event ID, or evidence ID in the packet"))
          }
        }
      }
      return issues
    }

    func readPacket(path string) (packetDocument, error) {
      document := packetDocument{
        MarketSnapshot: []packetMetric{}, Events: []packetEvent{}, EvidenceCatalog: []packetEvidence{},
      }
      payload, err := os.ReadFile(filepath.Clean(path))
      if err != nil {
        return document, fmt.Errorf("read packet: %w", err)
      }
      if err := json.Unmarshal(payload, &document); err != nil {
        return document, fmt.Errorf("decode packet: %w", err)
      }
      return document, nil
    }

    func validReviewPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func validReviewArtifactID(value string) bool {
      value = strings.TrimSpace(value)
      return strings.HasPrefix(value, "artifact-") && len(value) >= len("artifact-")+16
    }

    func writeReviewFile(path string, payload []byte) error {
      clean := filepath.Clean(path)
      if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("refuse symlink output %q", clean)
      }
      if err := os.MkdirAll(filepath.Dir(clean), 0700); err != nil {
        return fmt.Errorf("create review directory: %w", err)
      }
      if err := os.WriteFile(clean, payload, 0600); err != nil {
        return fmt.Errorf("write review: %w", err)
      }
      return nil
    }

    func reviewIssue(code, path, message string) Issue {
      repair := "Correct every reported review field and call the typed tool again."
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}

go_tool "submit_morning_draft" {
  description = "保存 Publisher 写出的带出处财经早餐草稿，并生成内部出处索引和去掉标记的 reader-facing morning.md。模型只需传入 Markdown，不传 artifact ID 或路径。"

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Input struct {
      AnnotatedMarkdown string `json:"markdown"`
      AnnotatedArtifactID string `json:"annotated_artifact_id"`
      ProvenanceArtifactID string `json:"provenance_artifact_id"`
      MarkdownArtifactID string `json:"markdown_artifact_id"`
      SourcePaths       []string `json:"source_paths"`
      AnnotatedPath     string `json:"_r42_annotated_path"`
      ProvenancePath    string `json:"_r42_provenance_path"`
      MarkdownPath      string `json:"_r42_markdown_path"`
      EditionDate       string `json:"edition_date"`
    }

    type ProvenanceEntry struct {
      Line    int      `json:"line"`
      Text    string   `json:"text"`
      Sources []string `json:"sources"`
    }

    type ProvenanceDocument struct {
      EditionDate string             `json:"edition_date"`
      Entries     []ProvenanceEntry `json:"entries"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := []Issue{}
      if !validDraftPath(input.AnnotatedPath, "morning-draft.annotated.md") {
        issues = append(issues, draftIssue("annotated_path", "_r42_annotated_path", "must be an absolute block artifact path ending in morning-draft.annotated.md"))
      }
      for field, value := range map[string]string{
        "annotated_artifact_id": input.AnnotatedArtifactID,
        "provenance_artifact_id": input.ProvenanceArtifactID,
        "markdown_artifact_id": input.MarkdownArtifactID,
      } {
        if strings.TrimSpace(value) == "" {
          issues = append(issues, draftIssue("artifact_id", field, "must be supplied by the host"))
        }
      }
      if !validDraftPath(input.ProvenancePath, "morning-provenance.json") {
        issues = append(issues, draftIssue("provenance_path", "_r42_provenance_path", "must be an absolute block artifact path ending in morning-provenance.json"))
      }
      if !validDraftPath(input.MarkdownPath, "morning.md") {
        issues = append(issues, draftIssue("markdown_path", "_r42_markdown_path", "must be an absolute block artifact path ending in morning.md"))
      }
      if strings.TrimSpace(input.EditionDate) == "" {
        issues = append(issues, draftIssue("edition_date", "edition_date", "must not be empty"))
      }
      if strings.TrimSpace(input.AnnotatedMarkdown) == "" {
        issues = append(issues, draftIssue("markdown", "markdown", "must not be empty"))
      }
      clean, entries, annotationIssues := parseDraft(input.AnnotatedMarkdown)
      issues = append(issues, annotationIssues...)
      allowedIDs, sourceErr := loadDraftSourceIDs(input.SourcePaths)
      if sourceErr != "" {
        issues = append(issues, draftIssue("source_read", "source_paths", sourceErr))
      }
      for index := range entries {
        for _, source := range entries[index].Sources {
          if len(allowedIDs) > 0 {
            if _, ok := allowedIDs[source]; !ok {
              issues = append(issues, draftIssue("unknown_provenance", fmt.Sprintf("markdown.line[%d]", entries[index].Line), "provenance ID is not present in the frozen packet or validated reviews"))
            }
          }
        }
      }
      if len(entries) == 0 && strings.TrimSpace(input.AnnotatedMarkdown) != "" {
        issues = append(issues, draftIssue("provenance", "markdown", "at least one sourced prose line is required"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      provenance, err := json.MarshalIndent(ProvenanceDocument{EditionDate: input.EditionDate, Entries: entries}, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode morning provenance: %w", err)
      }
      provenance = append(provenance, '\n')
      if err = writeDraftFile(input.AnnotatedPath, []byte(input.AnnotatedMarkdown)); err != nil {
        return ToolResponse[Output]{}, err
      }
      if err = writeDraftFile(input.ProvenancePath, provenance); err != nil {
        return ToolResponse[Output]{}, err
      }
      if err = writeDraftFile(input.MarkdownPath, []byte(clean)); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output("morning draft written: " + input.MarkdownPath)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func parseDraft(markdown string) (string, []ProvenanceEntry, []Issue) {
      lines := strings.Split(markdown, "\n")
      cleanLines := make([]string, 0, len(lines))
      entries := []ProvenanceEntry{}
      issues := []Issue{}
      for index, line := range lines {
        cleanLine, sources, found, errMessage := removeDraftMarkers(line)
        trimmed := strings.TrimSpace(cleanLine)
        if errMessage != "" {
          issues = append(issues, draftIssue("provenance_marker", fmt.Sprintf("markdown.line[%d]", index+1), errMessage))
        } else if found {
          if len(sources) == 0 {
            issues = append(issues, draftIssue("provenance_marker", fmt.Sprintf("markdown.line[%d]", index+1), "marker must contain at least one claim= or evidence= source"))
          } else {
            entries = append(entries, ProvenanceEntry{Line: index + 1, Text: trimmed, Sources: sources})
          }
        } else if requiresDraftMarker(trimmed) {
          issues = append(issues, draftIssue("missing_provenance", fmt.Sprintf("markdown.line[%d]", index+1), "every prose, bullet, table-data, and caption line must end with a provenance marker"))
        }
        cleanLines = append(cleanLines, cleanLine)
      }
      return strings.Join(cleanLines, "\n"), entries, issues
    }

    func removeDraftMarkers(line string) (string, []string, bool, string) {
      sources := []string{}
      found := false
      for {
        start := strings.Index(line, "<!-- r42:")
        if start < 0 {
          break
        }
        relativeEnd := strings.Index(line[start:], "-->")
        if relativeEnd < 0 {
          return line, sources, true, "provenance marker is missing -->"
        }
        end := start + relativeEnd
        trailing := strings.TrimSpace(line[end+3:])
        if trailing != "" && !strings.HasPrefix(trailing, "<!-- r42:") {
          return line, sources, true, "provenance marker must be at the end of the line"
        }
        payload := strings.TrimSpace(line[start+len("<!-- r42:"):end])
        for _, field := range strings.Fields(payload) {
          key, value, ok := strings.Cut(field, "=")
          if !ok || (key != "claim" && key != "evidence") || strings.TrimSpace(value) == "" {
            return line, sources, true, "provenance marker fields must be claim=<id> or evidence=<id>"
          }
          for _, source := range strings.Split(value, ",") {
            source = strings.TrimSpace(source)
            if source == "" {
              return line, sources, true, "provenance source IDs must not be empty"
            }
            sources = append(sources, source)
          }
        }
        found = true
        line = line[:start] + line[end+3:]
      }
      return line, sources, found, ""
    }

    func requiresDraftMarker(line string) bool {
      if line == "" || strings.HasPrefix(line, "#") || line == "---" {
        return false
      }
      if strings.HasPrefix(line, "|---") || strings.HasPrefix(line, "| ---") {
        return false
      }
      return true
    }

    func loadDraftSourceIDs(paths []string) (map[string]struct{}, string) {
      allowed := map[string]struct{}{}
      for _, path := range paths {
        payload, err := os.ReadFile(filepath.Clean(path))
        if err != nil {
          return allowed, fmt.Sprintf("read source %q: %v", path, err)
        }
        var value any
        if err := json.Unmarshal(payload, &value); err != nil {
          return allowed, fmt.Sprintf("decode source %q: %v", path, err)
        }
        collectDraftSourceIDs(value, allowed)
      }
      return allowed, ""
    }

    func collectDraftSourceIDs(value any, allowed map[string]struct{}) {
      switch item := value.(type) {
      case map[string]any:
        for key, nested := range item {
          if key == "id" {
            if id, ok := nested.(string); ok && strings.TrimSpace(id) != "" {
              allowed[id] = struct{}{}
            }
          }
          if strings.HasSuffix(key, "_ids") {
            if ids, ok := nested.([]any); ok {
              for _, raw := range ids {
                if id, ok := raw.(string); ok && strings.TrimSpace(id) != "" {
                  allowed[id] = struct{}{}
                }
              }
            }
          }
          collectDraftSourceIDs(nested, allowed)
        }
      case []any:
        for _, nested := range item {
          collectDraftSourceIDs(nested, allowed)
        }
      }
    }

    func validDraftPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func writeDraftFile(path string, payload []byte) error {
      if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
        return fmt.Errorf("create morning artifact directory: %w", err)
      }
      if err := os.WriteFile(path, payload, 0o600); err != nil {
        return fmt.Errorf("write morning artifact %q: %w", path, err)
      }
      return nil
    }

    func draftIssue(code, path, message string) Issue {
      repair := "rewrite the annotated Markdown and call submit_morning_draft again"
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}

go_tool "submit_morning_report" {
  description = "Validate a plain-language morning report and deterministically render JSON plus Markdown. `sentiment.label`: `fear`, `neutral`, `greed`, `mixed`; `themes.confidence`: `high`, `medium`, `low`. Evidence IDs remain in the JSON for validation but are not rendered in the reader-facing Markdown."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "sort"
      "strings"
    )

    type SnapshotItem struct {
      MetricKey    string `json:"metric_key"`
      PlainLanguage string `json:"plain_language"`
    }

    type Story struct {
      Headline      string   `json:"headline"`
      WhatHappened  string   `json:"what_happened"`
      WhyItMatters  string   `json:"why_it_matters"`
      WhatToWatch   string   `json:"what_to_watch"`
      EvidenceIDs   []string `json:"evidence_ids"`
    }

    type Theme struct {
      Name                 string   `json:"name"`
      LogicChain           string   `json:"logic_chain"`
      PossibleBeneficiaries []string `json:"possible_beneficiaries"`
      PressurePoints       []string `json:"pressure_points"`
      Counterpoint         string   `json:"counterpoint"`
      FalsificationCondition string `json:"falsification_condition"`
      Confidence           string   `json:"confidence"`
      EvidenceIDs          []string `json:"evidence_ids"`
    }

    type PremarketSetup struct {
      Name                  string   `json:"name"`
      Trigger               string   `json:"trigger"`
      Transmission          string   `json:"transmission"`
      AffectedAreas         []string `json:"affected_areas"`
      ConfirmationSignals   []string `json:"confirmation_signals"`
      InvalidationCondition string   `json:"invalidation_condition"`
      Horizon               string   `json:"horizon"`
      Confidence            string   `json:"confidence"`
      EvidenceIDs           []string `json:"evidence_ids"`
    }

    type InstitutionalScanItem struct {
      Section     string   `json:"section"`
      Headline    string   `json:"headline"`
      Summary     string   `json:"summary"`
      EvidenceIDs []string `json:"evidence_ids"`
    }

    type CalendarItem struct {
      Time        string `json:"time"`
      Event       string `json:"event"`
      WhatToWatch string `json:"what_to_watch"`
    }

    type Sentiment struct {
      Label       string `json:"label"`
      Basis       string `json:"basis"`
      Limitations string `json:"limitations"`
    }

    type Input struct {
      ReportJSONArtifactID     string         `json:"report_json_artifact_id,omitempty"`
      ReportMarkdownArtifactID string         `json:"report_markdown_artifact_id,omitempty"`
      ReportJSONPath     string         `json:"_r42_report_json_path"`
      ReportMarkdownPath string         `json:"_r42_report_markdown_path"`
      PacketPath         string         `json:"packet_path"`
      ReviewPaths        []string       `json:"review_paths"`
      EditionDate        string         `json:"edition_date"`
      Title              string         `json:"title"`
      Lead               []string       `json:"lead"`
      Snapshot           []SnapshotItem `json:"snapshot"`
      Stories            []Story        `json:"stories"`
      Themes             []Theme        `json:"themes"`
      Setups             []PremarketSetup `json:"setups"`
      InstitutionalScan  []InstitutionalScanItem `json:"institutional_scan"`
      CalendarEvents     []CalendarItem `json:"calendar_events"`
      Sentiment          Sentiment      `json:"sentiment"`
      Limitations        []string       `json:"limitations"`
    }

    type packetMetric struct {
      Key       string `json:"key"`
      Label     string `json:"label"`
      Value     string `json:"value"`
      Change    string `json:"change"`
      Direction string `json:"direction"`
      AsOf      string `json:"as_of"`
    }

    type packetEvent struct {
      ID string `json:"id"`
    }

    type packetEvidence struct {
      ID string `json:"id"`
    }

    type packetInstitutionalItem struct {
      ID string `json:"id"`
    }

    type packetCalendarEvent struct {
      ID string `json:"id"`
    }

    type packetDocument struct {
      EditionDate     string           `json:"edition_date"`
      CutoffTime      string           `json:"cutoff_time"`
      MarketSnapshot  []packetMetric   `json:"market_snapshot"`
      Events          []packetEvent    `json:"events"`
      InstitutionalScan []packetInstitutionalItem `json:"institutional_scan"`
      CalendarEvents  []packetCalendarEvent `json:"calendar_events"`
      EvidenceCatalog []packetEvidence `json:"evidence_catalog"`
    }

    type reviewDocument struct {
      Role string `json:"role"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      packet, packetErr := readReportPacket(input.PacketPath)
      issues := validateReport(input, packet, packetErr)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      reportJSONPath := input.ReportJSONPath
      reportMarkdownPath := input.ReportMarkdownPath
      input.ReportJSONArtifactID = ""
      input.ReportMarkdownArtifactID = ""
      input.ReportJSONPath = ""
      input.ReportMarkdownPath = ""
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode morning report: %w", err)
      }
      payload = append(payload, '\n')
      markdown := renderMorningMarkdown(input, packet)
      if err := writeReportFile(reportJSONPath, payload); err != nil {
        return ToolResponse[Output]{}, err
      }
      if err := writeReportFile(reportMarkdownPath, []byte(markdown)); err != nil {
        return ToolResponse[Output]{}, err
      }
      output := Output(fmt.Sprintf("reports written: %s and %s", reportJSONPath, reportMarkdownPath))
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateReport(input Input, packet packetDocument, packetErr error) []Issue {
      issues := []Issue{}
      if !validReportPath(input.ReportJSONPath, "morning-report.json") {
        issues = append(issues, reportIssue("report_json_path", "_r42_report_json_path", "must be an absolute block artifact path ending in morning-report.json"))
      }
      if !validReportPath(input.ReportMarkdownPath, "morning.md") {
        issues = append(issues, reportIssue("report_markdown_path", "_r42_report_markdown_path", "must be an absolute block artifact path ending in morning.md"))
      }
      if !validReportPath(input.PacketPath, "breakfast-packet.json") {
        issues = append(issues, reportIssue("packet_path", "packet_path", "must be an absolute block artifact path ending in breakfast-packet.json"))
      } else if packetErr != nil {
        issues = append(issues, reportIssue("packet_read", "packet_path", packetErr.Error()))
      }
      requiredRoles := map[string]struct{}{"macro": {}, "sentiment": {}, "strategy": {}}
      seenRoles := map[string]struct{}{}
      for index, path := range input.ReviewPaths {
        if !validReportPath(path, "review.json") {
          issues = append(issues, reportIssue("review_path", fmt.Sprintf("review_paths[%d]", index), "must be an absolute block artifact path ending in review.json"))
          continue
        }
        review, err := readReportReview(path)
        if err != nil {
          issues = append(issues, reportIssue("review_read", fmt.Sprintf("review_paths[%d]", index), err.Error()))
          continue
        }
        if _, expected := requiredRoles[review.Role]; !expected {
          issues = append(issues, reportIssue("review_role", fmt.Sprintf("review_paths[%d]", index), "review role must be macro, sentiment, or strategy"))
        } else if _, duplicate := seenRoles[review.Role]; duplicate {
          issues = append(issues, reportIssue("review_role", fmt.Sprintf("review_paths[%d]", index), "review role must not be duplicated"))
        } else {
          seenRoles[review.Role] = struct{}{}
        }
      }
      for role := range requiredRoles {
        if _, exists := seenRoles[role]; !exists {
          issues = append(issues, reportIssue("review_coverage", "review_paths", "missing required review role "+role))
        }
      }
      if packetErr == nil && packet.EditionDate != input.EditionDate {
        issues = append(issues, reportIssue("edition_date", "edition_date", "must equal the packet edition_date"))
      }
      if strings.TrimSpace(input.Title) == "" {
        issues = append(issues, reportIssue("title", "title", "must not be empty"))
      }
      if len(input.Lead) != 3 {
        issues = append(issues, reportIssue("lead", "lead", "must contain exactly three conclusions"))
      }
      for index, item := range input.Lead {
        if strings.TrimSpace(item) == "" {
          issues = append(issues, reportIssue("lead", fmt.Sprintf("lead[%d]", index), "must not be empty"))
        }
      }

      allowedEvidence := map[string]struct{}{}
      requiredMetrics := map[string]struct{}{}
      for _, metric := range packet.MarketSnapshot {
        allowedEvidence[metric.Key] = struct{}{}
        requiredMetrics[metric.Key] = struct{}{}
      }
      for _, event := range packet.Events {
        allowedEvidence[event.ID] = struct{}{}
      }
      for _, item := range packet.InstitutionalScan {
        allowedEvidence[item.ID] = struct{}{}
      }
      for _, item := range packet.CalendarEvents {
        allowedEvidence[item.ID] = struct{}{}
      }
      for _, evidence := range packet.EvidenceCatalog {
        allowedEvidence[evidence.ID] = struct{}{}
      }

      snapshotKeys := map[string]struct{}{}
      for index, item := range input.Snapshot {
        path := fmt.Sprintf("snapshot[%d]", index)
        if _, exists := requiredMetrics[item.MetricKey]; !exists {
          issues = append(issues, reportIssue("snapshot_key", path+".metric_key", "must reference a packet market key"))
        } else if _, duplicate := snapshotKeys[item.MetricKey]; duplicate {
          issues = append(issues, reportIssue("snapshot_key", path+".metric_key", "must not repeat a market key"))
        } else {
          snapshotKeys[item.MetricKey] = struct{}{}
        }
        if strings.TrimSpace(item.PlainLanguage) == "" {
          issues = append(issues, reportIssue("readability", path+".plain_language", "must explain the metric for an ordinary reader"))
        }
      }
      for key := range requiredMetrics {
        if _, exists := snapshotKeys[key]; !exists {
          issues = append(issues, reportIssue("snapshot_coverage", "snapshot", "missing packet metric "+key))
        }
      }

      if len(input.Stories) < 5 || len(input.Stories) > 7 {
        issues = append(issues, reportIssue("stories", "stories", "must contain five through seven major stories"))
      }
      for index, story := range input.Stories {
        path := fmt.Sprintf("stories[%d]", index)
        if strings.TrimSpace(story.Headline) == "" || strings.TrimSpace(story.WhatHappened) == "" ||
          strings.TrimSpace(story.WhyItMatters) == "" || strings.TrimSpace(story.WhatToWatch) == "" {
          issues = append(issues, reportIssue("story_structure", path, "headline, what_happened, why_it_matters, and what_to_watch must not be empty"))
        }
        issues = append(issues, reportEvidenceIssues(story.EvidenceIDs, allowedEvidence, path+".evidence_ids")...)
      }
      confidenceValues := map[string]struct{}{"high": {}, "medium": {}, "low": {}}
      for index, theme := range input.Themes {
        path := fmt.Sprintf("themes[%d]", index)
        if strings.TrimSpace(theme.Name) == "" || strings.TrimSpace(theme.LogicChain) == "" ||
          strings.TrimSpace(theme.Counterpoint) == "" || strings.TrimSpace(theme.FalsificationCondition) == "" {
          issues = append(issues, reportIssue("theme_structure", path, "name, logic_chain, counterpoint, and falsification_condition must not be empty"))
        }
        if len(theme.PossibleBeneficiaries) == 0 && len(theme.PressurePoints) == 0 {
          issues = append(issues, reportIssue("theme_mapping", path, "must identify possible beneficiaries or pressure points"))
        }
        if _, exists := confidenceValues[theme.Confidence]; !exists {
          issues = append(issues, reportIssue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        issues = append(issues, reportEvidenceIssues(theme.EvidenceIDs, allowedEvidence, path+".evidence_ids")...)
      }
      if len(input.Setups) == 0 {
        issues = append(issues, reportIssue("setups", "setups", "must contain at least one conditional premarket setup"))
      }
      for index, setup := range input.Setups {
        path := fmt.Sprintf("setups[%d]", index)
        if strings.TrimSpace(setup.Name) == "" || strings.TrimSpace(setup.Trigger) == "" ||
          strings.TrimSpace(setup.Transmission) == "" || strings.TrimSpace(setup.InvalidationCondition) == "" ||
          strings.TrimSpace(setup.Horizon) == "" {
          issues = append(issues, reportIssue("setup_structure", path, "name, trigger, transmission, invalidation_condition, and horizon must not be empty"))
        }
        if len(setup.AffectedAreas) == 0 || len(setup.ConfirmationSignals) == 0 {
          issues = append(issues, reportIssue("setup_structure", path, "affected_areas and confirmation_signals must not be empty"))
        }
        if _, exists := confidenceValues[setup.Confidence]; !exists {
          issues = append(issues, reportIssue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        issues = append(issues, reportEvidenceIssues(setup.EvidenceIDs, allowedEvidence, path+".evidence_ids")...)
      }
      institutionalSections := map[string]struct{}{
        "market_liquidity": {}, "bonds_rates": {}, "commodities": {},
        "company_announcements": {}, "sell_side": {},
      }
      if len(input.InstitutionalScan) == 0 {
        issues = append(issues, reportIssue("institutional_scan", "institutional_scan", "must contain at least one institutional signal"))
      }
      for index, item := range input.InstitutionalScan {
        path := fmt.Sprintf("institutional_scan[%d]", index)
        if _, exists := institutionalSections[item.Section]; !exists {
          issues = append(issues, reportIssue("institutional_section", path+".section", "uses an unsupported section"))
        }
        if strings.TrimSpace(item.Headline) == "" || strings.TrimSpace(item.Summary) == "" {
          issues = append(issues, reportIssue("institutional_scan", path, "headline and summary must not be empty"))
        }
        issues = append(issues, reportEvidenceIssues(item.EvidenceIDs, allowedEvidence, path+".evidence_ids")...)
      }
      if len(input.CalendarEvents) == 0 {
        issues = append(issues, reportIssue("calendar_events", "calendar_events", "must contain at least one scheduled event or explicit no-event entry"))
      }
      for index, item := range input.CalendarEvents {
        if strings.TrimSpace(item.Time) == "" || strings.TrimSpace(item.Event) == "" || strings.TrimSpace(item.WhatToWatch) == "" {
          issues = append(issues, reportIssue("calendar_event", fmt.Sprintf("calendar_events[%d]", index), "time, event, and what_to_watch must not be empty"))
        }
      }
      sentimentValues := map[string]struct{}{"fear": {}, "neutral": {}, "greed": {}, "mixed": {}}
      if _, exists := sentimentValues[input.Sentiment.Label]; !exists {
        issues = append(issues, reportIssue("sentiment", "sentiment.label", "must be fear, neutral, greed, or mixed"))
      }
      if strings.TrimSpace(input.Sentiment.Basis) == "" || strings.TrimSpace(input.Sentiment.Limitations) == "" {
        issues = append(issues, reportIssue("sentiment", "sentiment", "basis and limitations must not be empty"))
      }
      if len(input.Limitations) == 0 {
        issues = append(issues, reportIssue("limitations", "limitations", "must contain at least one edition-specific limitation"))
      }
      issues = append(issues, prohibitedLanguageIssues(input)...)
      return issues
    }

    func reportEvidenceIssues(ids []string, allowed map[string]struct{}, path string) []Issue {
      if len(ids) == 0 {
        return []Issue{reportIssue("evidence_reference", path, "must contain at least one packet evidence ID")}
      }
      issues := []Issue{}
      for index, id := range ids {
        if _, exists := allowed[id]; !exists {
          issues = append(issues, reportIssue("evidence_reference", fmt.Sprintf("%s[%d]", path, index), "must reference a packet metric, event, or evidence ID"))
        }
      }
      return issues
    }

    func prohibitedLanguageIssues(input Input) []Issue {
      payload, err := json.Marshal(input)
      if err != nil {
        return []Issue{reportIssue("encoding", "report", "could not inspect report language")}
      }
      normalized := strings.ToLower(string(payload))
      prohibited := []string{"保证收益", "稳赚", "必涨", "无风险", "满仓", "梭哈", "重仓买入", "加杠杆买入", "guaranteed return", "risk-free"}
      issues := []Issue{}
      for _, phrase := range prohibited {
        if strings.Contains(normalized, phrase) {
          issues = append(issues, reportIssue("prohibited_advice", "report", "contains prohibited certainty or concentrated-trading language: "+phrase))
        }
      }
      return issues
    }

    func readReportPacket(path string) (packetDocument, error) {
      document := packetDocument{
        MarketSnapshot: []packetMetric{}, Events: []packetEvent{}, InstitutionalScan: []packetInstitutionalItem{},
        CalendarEvents: []packetCalendarEvent{}, EvidenceCatalog: []packetEvidence{},
      }
      payload, err := os.ReadFile(filepath.Clean(path))
      if err != nil {
        return document, fmt.Errorf("read packet: %w", err)
      }
      if err := json.Unmarshal(payload, &document); err != nil {
        return document, fmt.Errorf("decode packet: %w", err)
      }
      return document, nil
    }

    func readReportReview(path string) (reviewDocument, error) {
      document := reviewDocument{}
      payload, err := os.ReadFile(filepath.Clean(path))
      if err != nil {
        return document, fmt.Errorf("read review: %w", err)
      }
      if err := json.Unmarshal(payload, &document); err != nil {
        return document, fmt.Errorf("decode review: %w", err)
      }
      return document, nil
    }

    func renderMorningMarkdown(input Input, packet packetDocument) string {
      metrics := map[string]packetMetric{}
      for _, metric := range packet.MarketSnapshot {
        metrics[metric.Key] = metric
      }
      var output strings.Builder
      fmt.Fprintf(&output, "# %s\n\n", markdownText(input.Title))
      fmt.Fprintf(&output, "> %s · 信息截点：%s · 预计阅读 5-8 分钟\n\n", markdownText(input.EditionDate), markdownText(packet.CutoffTime))
      output.WriteString("## 今早先看三件事\n\n")
      for _, item := range input.Lead {
        fmt.Fprintf(&output, "- %s\n", markdownText(item))
      }
      marketDate := marketDataDateLabel(packet.MarketSnapshot)
      fmt.Fprintf(&output, "\n## 最新已收盘行情（数据日期：%s）\n\n| 市场 | 最新收盘表现 | 盘前解读 |\n|---|---|---|\n", markdownText(marketDate))
      for _, item := range input.Snapshot {
        metric := metrics[item.MetricKey]
        performance := strings.TrimSpace(strings.Join([]string{metric.Value, metric.Change}, " "))
        fmt.Fprintf(&output, "| %s | %s | %s |\n", markdownText(metric.Label), markdownText(performance), markdownText(item.PlainLanguage))
      }
      output.WriteString("\n## 今晨大事\n")
      for _, story := range input.Stories {
        fmt.Fprintf(&output, "\n### %s\n\n", markdownText(story.Headline))
        fmt.Fprintf(&output, "%s %s 接下来可以留意%s\n", markdownSentence(story.WhatHappened), markdownSentence(story.WhyItMatters), markdownText(story.WhatToWatch))
      }
      output.WriteString("\n## 今日主线与市场影响\n")
      for _, theme := range input.Themes {
        fmt.Fprintf(&output, "\n### %s（置信度：%s）\n\n", markdownText(theme.Name), confidenceLabel(theme.Confidence))
        fmt.Fprintf(&output, "**逻辑链：** %s\n\n", markdownText(theme.LogicChain))
        renderList(&output, "可能受益", theme.PossibleBeneficiaries)
        renderList(&output, "可能承压", theme.PressurePoints)
        fmt.Fprintf(&output, "**另一面：** %s\n\n", markdownText(theme.Counterpoint))
        fmt.Fprintf(&output, "**什么会推翻这条逻辑：** %s\n", markdownText(theme.FalsificationCondition))
      }
      output.WriteString("\n## 盘前观察清单\n")
      for _, setup := range input.Setups {
        fmt.Fprintf(&output, "\n### %s（%s，置信度：%s）\n\n", markdownText(setup.Name), markdownText(setup.Horizon), confidenceLabel(setup.Confidence))
        fmt.Fprintf(&output, "若%s，可能通过%s影响%s。\n\n", markdownText(setup.Trigger), markdownText(setup.Transmission), markdownText(strings.Join(setup.AffectedAreas, "、")))
        fmt.Fprintf(&output, "**确认信号：** %s\n\n", markdownText(strings.Join(setup.ConfirmationSignals, "；")))
        fmt.Fprintf(&output, "**失效条件：** %s\n", markdownText(setup.InvalidationCondition))
      }
      output.WriteString("\n## 机构信息扫描\n\n")
      for _, item := range input.InstitutionalScan {
        fmt.Fprintf(&output, "- **%s｜%s：** %s\n", institutionalSectionLabel(item.Section), markdownText(item.Headline), markdownText(item.Summary))
      }
      output.WriteString("\n## 财经日历\n\n| 时间 | 事件 | 普通读者该看什么 |\n|---|---|---|\n")
      for _, item := range input.CalendarEvents {
        fmt.Fprintf(&output, "| %s | %s | %s |\n", markdownText(item.Time), markdownText(item.Event), markdownText(item.WhatToWatch))
      }
      fmt.Fprintf(&output, "\n## 上一交易时段的市场信号\n\n**%s。** %s\n\n以上判断基于截至 %s 的最新已收盘行情，不是晨报发布日期的盘中走势。\n\n数据限制：%s\n", sentimentLabel(input.Sentiment.Label), markdownText(input.Sentiment.Basis), markdownText(marketDate), markdownText(input.Sentiment.Limitations))
      output.WriteString("\n## 本期局限\n\n")
      for _, limitation := range input.Limitations {
        fmt.Fprintf(&output, "- %s\n", markdownText(limitation))
      }
      return output.String()
    }

    func marketDataDateLabel(metrics []packetMetric) string {
      dates := map[string]struct{}{}
      for _, metric := range metrics {
        if metric.Direction == "unavailable" || strings.TrimSpace(metric.Value) == "" || metric.Value == "unavailable" {
          continue
        }
        if value := strings.TrimSpace(metric.AsOf); value != "" {
          dates[value] = struct{}{}
        }
      }
      if len(dates) == 0 {
        return "暂无可用行情"
      }
      values := make([]string, 0, len(dates))
      for value := range dates {
        values = append(values, value)
      }
      sort.Strings(values)
      if len(values) == 1 {
        return values[0]
      }
      return values[0] + " 至 " + values[len(values)-1]
    }

    func renderList(output *strings.Builder, label string, values []string) {
      if len(values) == 0 {
        return
      }
      cleaned := append([]string{}, values...)
      sort.Strings(cleaned)
      fmt.Fprintf(output, "**%s：** %s\n\n", label, markdownText(strings.Join(cleaned, "、")))
    }

    func confidenceLabel(value string) string {
      labels := map[string]string{"high": "高", "medium": "中", "low": "低"}
      return labels[value]
    }

    func institutionalSectionLabel(value string) string {
      labels := map[string]string{
        "market_liquidity": "市场与资金面", "bonds_rates": "债券与利率",
        "commodities": "大宗商品", "company_announcements": "公司公告",
        "sell_side": "机构观点",
      }
      return labels[value]
    }

    func markdownSentence(value string) string {
      cleaned := markdownText(value)
      if strings.HasSuffix(cleaned, "。") || strings.HasSuffix(cleaned, "！") || strings.HasSuffix(cleaned, "？") ||
        strings.HasSuffix(cleaned, ".") || strings.HasSuffix(cleaned, "!") || strings.HasSuffix(cleaned, "?") {
        return cleaned
      }
      return cleaned + "。"
    }

    func sentimentLabel(value string) string {
      labels := map[string]string{"fear": "偏谨慎", "neutral": "中性", "greed": "偏乐观", "mixed": "信号混合"}
      return labels[value]
    }

    func markdownText(value string) string {
      return strings.NewReplacer("\\", "\\\\", "|", "\\|", "\r", " ", "\n", " ").Replace(strings.TrimSpace(value))
    }

    func validReportPath(path, suffix string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
      return filepath.IsAbs(path) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+suffix)
    }

    func writeReportFile(path string, payload []byte) error {
      clean := filepath.Clean(path)
      if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("refuse symlink output %q", clean)
      }
      if err := os.MkdirAll(filepath.Dir(clean), 0700); err != nil {
        return fmt.Errorf("create report directory: %w", err)
      }
      if err := os.WriteFile(clean, payload, 0600); err != nil {
        return fmt.Errorf("write report: %w", err)
      }
      return nil
    }

    func reportIssue(code, path, message string) Issue {
      repair := "Correct every reported report field and call the typed tool again."
      return Issue{Code: code, Path: &path, Message: message, RepairHint: &repair}
    }
  GO
}
