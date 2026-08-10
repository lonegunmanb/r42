go_tool "register_evidence_source" {
  description = "Register one retained source in the current evidence-ledger draft. url is the fetched page; canonical_url is the original publication URL and may equal url. source_type, reporting_basis, and provenance use broad classifications; unfamiliar values are retained as unknown instead of rejecting the call. The host derives source, origin, and independence IDs."

  source = <<-GO
    import (
      "bytes"
      "context"
      "crypto/sha256"
      "encoding/json"
      "fmt"
      "net/url"
      "os"
      "path/filepath"
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
      SnapshotPath    string   `json:"snapshot_path"`
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
      ContentFingerprint string `json:"content_fingerprint"`
      Title           string   `json:"title"`
      Publisher       string   `json:"publisher"`
      PublicationDate string   `json:"publication_date"`
      AccessedAt      string   `json:"accessed_at"`
      SourceType      string   `json:"source_type"`
      SourceClass     string   `json:"source_class"`
      ReportingBasis  string   `json:"reporting_basis"`
      Provenance      string   `json:"provenance"`
      SnapshotPath    string   `json:"snapshot_path"`
      SnapshotSHA256  string   `json:"snapshot_sha256"`
      NamedEntities   []string `json:"named_entities"`
    }

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
      snapshotPath, snapshotErr := filepath.Abs(filepath.Clean(strings.TrimSpace(input.SnapshotPath)))
      if snapshotErr != nil || !filepath.IsAbs(input.SnapshotPath) || !strings.EqualFold(filepath.Ext(snapshotPath), ".md") || !evidenceFileExists(snapshotPath) {
        issues = append(issues, evidenceIssue("snapshot_path", "snapshot_path", "snapshot_path must name an existing absolute Markdown snapshot"))
      } else {
        root := evidenceBlocksRoot(workspace)
        if root == "" || !evidenceWithin(snapshotPath, root) {
          issues = append(issues, evidenceIssue("snapshot_path", "snapshot_path", "snapshot_path must be inside the current run's blocks directory"))
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      snapshot, err := os.ReadFile(snapshotPath)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read evidence snapshot: %w", err)
      }
      snapshotDigest := sha256.Sum256(snapshot)
      snapshotHash := fmt.Sprintf("%x", snapshotDigest)
      contentDigest := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(string(snapshot)), " "))))
      contentFingerprint := fmt.Sprintf("%x", contentDigest)
      originDigest := sha256.Sum256([]byte(canonicalURL))
      originID := "origin-" + fmt.Sprintf("%x", originDigest)[:20]
      independenceGroup := originID
      existing, loadErr := loadEvidenceSources(filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft", "sources"))
      if loadErr != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load registered evidence sources: %w", loadErr)
      }
      for _, candidate := range existing {
        if candidate.ContentFingerprint == contentFingerprint && candidate.IndependenceGroup != "" {
          independenceGroup = candidate.IndependenceGroup
          break
        }
      }
      sourceDigest := sha256.Sum256([]byte(normalizedURL + ":" + snapshotHash))
      sourceID := "source-" + fmt.Sprintf("%x", sourceDigest)[:20]
      entities := make([]string, 0, len(input.NamedEntities))
      for _, entity := range input.NamedEntities {
        if entity = strings.TrimSpace(entity); entity != "" {
          entities = append(entities, entity)
        }
      }
      record := SourceRecord{
        ID: sourceID, URL: strings.TrimSpace(input.URL), NormalizedURL: normalizedURL,
        CanonicalURL: canonicalURL, OriginID: originID, IndependenceGroup: independenceGroup,
        ContentFingerprint: contentFingerprint,
        Title: strings.TrimSpace(input.Title), Publisher: strings.TrimSpace(input.Publisher),
        PublicationDate: strings.TrimSpace(input.PublicationDate), AccessedAt: strings.TrimSpace(input.AccessedAt),
        SourceType: sourceType, SourceClass: sourceClass,
        ReportingBasis: evidenceReportingBasis(input.ReportingBasis), Provenance: evidenceProvenance(input.Provenance),
        SnapshotPath: snapshotPath, SnapshotSHA256: snapshotHash, NamedEntities: entities,
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

    func evidenceFileExists(path string) bool {
      info, err := os.Stat(path)
      return err == nil && !info.IsDir()
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

go_tool "stage_evidence_claims" {
  description = "Stage from one through five atomic claims. workspace_dir is required, is created when missing, and bounds the ledger path. Each claim cites previously registered source IDs. The host computes final evidence status during finalization; do not add a confidence field. Reusing a claim ID replaces only that claim."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "regexp"
      "sort"
      "strings"
    )

    type Evidence struct {
      SourceID  string `json:"source_id"`
      Relation  string `json:"relation"`
      Directness string `json:"directness"`
      AuthorityForClaim bool `json:"authority_for_claim"`
      Locator   string `json:"locator"`
      ExactQuote string `json:"exact_quote"`
    }

    type Claim struct {
      ID              string            `json:"id"`
      ClaimType       string            `json:"claim_type"`
      Subject         string            `json:"subject"`
      Predicate       string            `json:"predicate"`
      Value           string            `json:"value"`
      Qualifiers      map[string]string `json:"qualifiers"`
      CoverageItemIDs []string          `json:"coverage_item_ids"`
      Inference       string            `json:"inference"`
      Evidence        []Evidence        `json:"evidence"`
    }

    type Input struct {
      WorkspaceDir string  `json:"workspace_dir"`
      LedgerPath   string  `json:"ledger_path"`
      Claims       []Claim `json:"claims"`
    }

    type Output struct {
      ClaimIDs []string `json:"claim_ids"`
      Staged   int      `json:"staged"`
    }

    type Source struct {
      ID string `json:"id"`
    }

    var evidenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := claimWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create evidence workspace: %w", err)
      }
      ledgerPath, issues := claimLedgerPath(input.LedgerPath, workspace)
      if len(input.Claims) == 0 {
        issues = append(issues, claimIssue("claims", "claims", "claims must contain from one through five items"))
      }
      if len(input.Claims) > 5 {
        issues = append(issues, claimIssue("batch_size", "claims", "submit at most five claims per call"))
      }
      if ledgerPath == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      knownSources, err := loadClaimSources(filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft", "sources"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load registered evidence sources: %w", err)
      }
      seen := map[string]struct{}{}
      for index, claim := range input.Claims {
        path := fmt.Sprintf("claims[%d]", index)
        id := strings.TrimSpace(claim.ID)
        if !evidenceID.MatchString(id) {
          issues = append(issues, claimIssue("claim_id", path+".id", "id must be a short stable identifier"))
        }
        if _, exists := seen[id]; exists {
          issues = append(issues, claimIssue("claim_id", path+".id", "claim IDs must be unique in the batch"))
        }
        seen[id] = struct{}{}
        issues = append(issues, validateClaim(claim, path, knownSources)...)
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      directory := filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft", "claims")
      ids := make([]string, 0, len(input.Claims))
      for _, claim := range input.Claims {
        if err = writeClaimJSON(filepath.Join(directory, claim.ID+".json"), claim); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("write staged evidence claim: %w", err)
        }
        ids = append(ids, claim.ID)
      }
      output := Output{ClaimIDs: ids, Staged: len(ids)}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateClaim(claim Claim, path string, sources map[string]struct{}) []Issue {
      issues := []Issue{}
      claimTypes := map[string]struct{}{
        "organization_relationship": {}, "supplier_maturity": {}, "quantitative": {}, "technical": {},
        "regulatory": {}, "product_structure": {}, "process": {}, "other": {},
      }
      if _, ok := claimTypes[strings.TrimSpace(claim.ClaimType)]; !ok {
        issues = append(issues, claimIssue("claim_type", path+".claim_type", "claim_type is not supported"))
      }
      for field, value := range map[string]string{"subject": claim.Subject, "predicate": claim.Predicate, "value": claim.Value} {
        if strings.TrimSpace(value) == "" {
          issues = append(issues, claimIssue("claim", path+"."+field, field+" must not be empty"))
        }
      }
      if claim.Qualifiers == nil {
        issues = append(issues, claimIssue("qualifiers", path+".qualifiers", "qualifiers must be a map"))
      }
      if claim.ClaimType == "quantitative" {
        if strings.TrimSpace(claim.Qualifiers["unit"]) == "" || strings.TrimSpace(claim.Qualifiers["period"]) == "" || strings.TrimSpace(claim.Qualifiers["derivation"]) == "" {
          issues = append(issues, claimIssue("quantitative_qualifiers", path+".qualifiers", "quantitative claims require unit, period, and derivation qualifiers"))
        }
      }
      if claim.ClaimType == "supplier_maturity" {
        maturity := map[string]struct{}{
          "research": {}, "validation": {}, "order_received": {}, "batch_delivery": {},
          "mass_production": {}, "primary_supplier": {}, "unknown": {},
        }
        if _, ok := maturity[claim.Value]; !ok {
          issues = append(issues, claimIssue("supplier_maturity", path+".value", "supplier maturity must use the controlled lifecycle enum"))
        }
      }
      if claim.CoverageItemIDs == nil {
        issues = append(issues, claimIssue("coverage_item", path+".coverage_item_ids", "coverage_item_ids must be a list"))
      }
      if len(claim.Evidence) == 0 {
        return append(issues, claimIssue("evidence", path+".evidence", "claim evidence must not be empty"))
      }
      for index, item := range claim.Evidence {
        evidencePath := fmt.Sprintf("%s.evidence[%d]", path, index)
        if _, ok := sources[item.SourceID]; !ok {
          issues = append(issues, claimIssue("source_id", evidencePath+".source_id", "source_id must be registered first"))
        }
        if item.Relation != "supports" && item.Relation != "contradicts" {
          issues = append(issues, claimIssue("relation", evidencePath+".relation", "relation must be supports or contradicts"))
        }
        if item.Directness != "direct" && item.Directness != "indirect" {
          issues = append(issues, claimIssue("directness", evidencePath+".directness", "directness must be direct or indirect"))
        }
        if strings.TrimSpace(item.Locator) == "" {
          issues = append(issues, claimIssue("evidence", evidencePath+".locator", "locator is required"))
        }
        if strings.TrimSpace(item.ExactQuote) == "" {
          issues = append(issues, claimIssue("evidence", evidencePath+".exact_quote", "exact_quote is required"))
        }
      }
      return issues
    }

    func claimLedgerPath(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{claimIssue("invalid_path", "ledger_path", "ledger_path must be absolute")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-ledger.json" || !claimWithin(path, workspace) {
        return "", []Issue{claimIssue("invalid_path", "ledger_path", "ledger_path must end in evidence-ledger.json under workspace_dir")}
      }
      return path, nil
    }

    func claimWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := claimBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !claimWithin(workspace, blocksRoot) {
        return "", []Issue{claimIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func claimBlocksRoot(workspace string) string {
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

    func loadClaimSources(directory string) (map[string]struct{}, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
      if err != nil {
        return nil, err
      }
      sort.Strings(paths)
      result := map[string]struct{}{}
      for _, path := range paths {
        var source Source
        if err = readClaimJSON(path, &source); err != nil {
          return nil, err
        }
        result[source.ID] = struct{}{}
      }
      return result, nil
    }

    func claimWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func readClaimJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
      return json.Unmarshal(payload, value)
    }

    func writeClaimJSON(path string, value any) error {
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

    func claimIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "stage_claim_freshness_checks" {
  description = "Stage one through five current-source checks for claims that may drive a final company conclusion. verified_primary sources must be direct authoritative supporting evidence for that claim. Ambiguous outcome text is accepted as not_verified. Reusing a claim_id replaces only that check; missing or failed checks downgrade the final candidate instead of failing the research."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "sort"
      "strings"
      "time"
    )

    type FreshnessCheck struct {
      ClaimID                string   `json:"claim_id"`
      CheckedAt              string   `json:"checked_at"`
      OfficialChannels       []string `json:"official_channels"`
      LatestPrimarySourceIDs []string `json:"latest_primary_source_ids"`
      Outcome                string   `json:"outcome"`
      Gap                    string   `json:"gap"`
    }

    type Input struct {
      WorkspaceDir string           `json:"workspace_dir"`
      LedgerPath   string           `json:"ledger_path"`
      Checks       []FreshnessCheck `json:"checks"`
    }

    type Output struct {
      ClaimIDs []string `json:"claim_ids"`
      Staged   int      `json:"staged"`
    }

    type freshnessSource struct {
      ID          string `json:"id"`
      SourceType  string `json:"source_type"`
      SourceClass string `json:"source_class"`
    }

    type freshnessEvidence struct {
      SourceID         string `json:"source_id"`
      Relation         string `json:"relation"`
      Directness       string `json:"directness"`
      AuthorityForClaim bool  `json:"authority_for_claim"`
    }

    type freshnessClaim struct {
      ID       string              `json:"id"`
      Evidence []freshnessEvidence `json:"evidence"`
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := freshnessWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create evidence workspace: %w", err)
      }
      ledgerPath, pathIssues := freshnessLedgerPath(input.LedgerPath, workspace)
      issues = append(issues, pathIssues...)
      if len(input.Checks) == 0 {
        issues = append(issues, freshnessIssue("checks", "checks", "checks must contain from one through five items"))
      }
      if len(input.Checks) > 5 {
        issues = append(issues, freshnessIssue("batch_size", "checks", "submit at most five freshness checks per call"))
      }
      if ledgerPath == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      draft := filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft")
      claims, err := loadFreshnessClaims(filepath.Join(draft, "claims"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load staged claims: %w", err)
      }
      sources, err := loadFreshnessSources(filepath.Join(draft, "sources"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load registered sources: %w", err)
      }
      normalized := make([]FreshnessCheck, len(input.Checks))
      seen := map[string]struct{}{}
      for index, check := range input.Checks {
        path := fmt.Sprintf("checks[%d]", index)
        check.ClaimID = strings.TrimSpace(check.ClaimID)
        check.Outcome = normalizeFreshnessOutcome(check.Outcome)
        check.CheckedAt = strings.TrimSpace(check.CheckedAt)
        check.Gap = strings.TrimSpace(check.Gap)
        check.OfficialChannels = nonEmptyFreshnessValues(check.OfficialChannels)
        check.LatestPrimarySourceIDs = nonEmptyFreshnessValues(check.LatestPrimarySourceIDs)
        claim, claimExists := claims[check.ClaimID]
        if !claimExists {
          issues = append(issues, freshnessIssue("claim_id", path+".claim_id", "claim_id must reference a staged claim"))
        }
        if _, duplicate := seen[check.ClaimID]; duplicate {
          issues = append(issues, freshnessIssue("claim_id", path+".claim_id", "claim IDs must be unique in the batch"))
        }
        seen[check.ClaimID] = struct{}{}
        if _, dateErr := time.Parse("2006-01-02", check.CheckedAt); dateErr != nil {
          issues = append(issues, freshnessIssue("date", path+".checked_at", "checked_at must use YYYY-MM-DD"))
        }
        if check.Outcome == "verified_primary" {
          if len(check.LatestPrimarySourceIDs) == 0 {
            issues = append(issues, freshnessIssue("primary_source", path+".latest_primary_source_ids", "verified_primary requires at least one authoritative primary source"))
          }
          authoritativeSources := freshnessAuthoritativeSources(claim)
          for sourceIndex, sourceID := range check.LatestPrimarySourceIDs {
            source, exists := sources[sourceID]
            _, supportsClaim := authoritativeSources[sourceID]
            if !exists || freshnessSourceClass(source) != "authoritative_primary" || !claimExists || !supportsClaim {
              sourcePath := fmt.Sprintf("%s.latest_primary_source_ids[%d]", path, sourceIndex)
              issues = append(issues, freshnessIssue("primary_source", sourcePath, "source must be registered authoritative primary evidence that directly supports this claim with authority_for_claim"))
            }
          }
        }
        if check.Outcome == "checked_no_primary" && len(check.OfficialChannels) == 0 {
          issues = append(issues, freshnessIssue("official_channels", path+".official_channels", "checked_no_primary requires the official channels that were checked"))
        }
        if check.Outcome == "not_verified" && check.Gap == "" {
          issues = append(issues, freshnessIssue("freshness_gap", path+".gap", "not_verified requires a concise gap"))
        }
        normalized[index] = check
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      directory := filepath.Join(draft, "freshness")
      ids := make([]string, 0, len(normalized))
      for _, check := range normalized {
        if err = writeFreshnessJSON(filepath.Join(directory, check.ClaimID+".json"), check); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("write claim freshness check: %w", err)
        }
        ids = append(ids, check.ClaimID)
      }
      output := Output{ClaimIDs: ids, Staged: len(ids)}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func normalizeFreshnessOutcome(raw string) string {
      switch strings.ToLower(strings.TrimSpace(raw)) {
      case "verified_primary", "checked_no_primary", "not_verified":
        return strings.ToLower(strings.TrimSpace(raw))
      default:
        return "not_verified"
      }
    }

    func freshnessSourceClass(source freshnessSource) string {
      if source.SourceClass != "" {
        return source.SourceClass
      }
      switch source.SourceType {
      case "authoritative_primary", "official_filing", "official_product", "official_statement", "regulator":
        return "authoritative_primary"
      default:
        return "unknown"
      }
    }

    func nonEmptyFreshnessValues(values []string) []string {
      result := make([]string, 0, len(values))
      for _, value := range values {
        if value = strings.TrimSpace(value); value != "" {
          result = append(result, value)
        }
      }
      return result
    }

    func freshnessAuthoritativeSources(claim freshnessClaim) map[string]struct{} {
      result := map[string]struct{}{}
      for _, evidence := range claim.Evidence {
        if evidence.Relation == "supports" && evidence.Directness == "direct" && evidence.AuthorityForClaim {
          result[evidence.SourceID] = struct{}{}
        }
      }
      return result
    }

    func loadFreshnessClaims(directory string) (map[string]freshnessClaim, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
      if err != nil {
        return nil, err
      }
      sort.Strings(paths)
      result := map[string]freshnessClaim{}
      for _, path := range paths {
        var claim freshnessClaim
        if err = readFreshnessJSON(path, &claim); err != nil {
          return nil, err
        }
        result[claim.ID] = claim
      }
      return result, nil
    }

    func loadFreshnessSources(directory string) (map[string]freshnessSource, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
      if err != nil {
        return nil, err
      }
      sort.Strings(paths)
      result := map[string]freshnessSource{}
      for _, path := range paths {
        var source freshnessSource
        if err = readFreshnessJSON(path, &source); err != nil {
          return nil, err
        }
        result[source.ID] = source
      }
      return result, nil
    }

    func freshnessLedgerPath(raw, workspace string) (string, []Issue) {
      path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      if err != nil || !filepath.IsAbs(raw) || filepath.Base(path) != "evidence-ledger.json" || !freshnessWithin(path, workspace) {
        return "", []Issue{freshnessIssue("invalid_path", "ledger_path", "ledger_path must end in evidence-ledger.json under workspace_dir")}
      }
      return path, nil
    }

    func freshnessWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := freshnessBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !freshnessWithin(workspace, blocksRoot) {
        return "", []Issue{freshnessIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func freshnessBlocksRoot(workspace string) string {
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

    func freshnessWithin(path, root string) bool {
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func readFreshnessJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), value)
    }

    func writeFreshnessJSON(path string, value any) error {
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

    func freshnessIssue(code, path, message string) Issue {
      repair := "Correct every listed field and call the tool once more. Ambiguous classifications may use not_verified."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "stage_evidence_gaps" {
  description = "Stage from one through five unresolved scope items. workspace_dir is required, is created when missing, and bounds the ledger path. Use this instead of inventing a claim when retained public evidence cannot resolve an assigned coverage item. Reusing a coverage_item_id replaces only that gap."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "regexp"
      "strings"
    )

    type Gap struct {
      CoverageItemID string `json:"coverage_item_id"`
      Reason         string `json:"reason"`
      ResearchAttempt string `json:"research_attempt"`
      Impact         string `json:"impact"`
    }

    type Input struct {
      WorkspaceDir string `json:"workspace_dir"`
      LedgerPath   string `json:"ledger_path"`
      Gaps         []Gap  `json:"gaps"`
    }

    type Output struct {
      CoverageItemIDs []string `json:"coverage_item_ids"`
      Staged          int      `json:"staged"`
    }

    var gapID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := gapWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create evidence workspace: %w", err)
      }
      ledgerPath, issues := gapLedgerPath(input.LedgerPath, workspace)
      if len(input.Gaps) == 0 {
        issues = append(issues, gapIssue("gaps", "gaps", "gaps must contain from one through five items"))
      }
      if len(input.Gaps) > 5 {
        issues = append(issues, gapIssue("batch_size", "gaps", "submit at most five gaps per call"))
      }
      seen := map[string]struct{}{}
      for index, gap := range input.Gaps {
        path := fmt.Sprintf("gaps[%d]", index)
        id := strings.TrimSpace(gap.CoverageItemID)
        if !gapID.MatchString(id) {
          issues = append(issues, gapIssue("coverage_item", path+".coverage_item_id", "coverage item ID is required"))
        }
        if _, exists := seen[id]; exists {
          issues = append(issues, gapIssue("coverage_item", path+".coverage_item_id", "coverage item IDs must be unique"))
        }
        seen[id] = struct{}{}
        for field, value := range map[string]string{"reason": gap.Reason, "research_attempt": gap.ResearchAttempt, "impact": gap.Impact} {
          if strings.TrimSpace(value) == "" {
            issues = append(issues, gapIssue("gap", path+"."+field, field+" must not be empty"))
          }
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      directory := filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft", "gaps")
      ids := make([]string, 0, len(input.Gaps))
      for _, gap := range input.Gaps {
        if err = writeGapJSON(filepath.Join(directory, gap.CoverageItemID+".json"), gap); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("write staged evidence gap: %w", err)
        }
        ids = append(ids, gap.CoverageItemID)
      }
      output := Output{CoverageItemIDs: ids, Staged: len(ids)}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func gapLedgerPath(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{gapIssue("invalid_path", "ledger_path", "ledger_path must be absolute")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-ledger.json" || !gapWithin(path, workspace) {
        return "", []Issue{gapIssue("invalid_path", "ledger_path", "ledger_path must end in evidence-ledger.json under workspace_dir")}
      }
      return path, nil
    }

    func gapWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := gapBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !gapWithin(workspace, blocksRoot) {
        return "", []Issue{gapIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func gapBlocksRoot(workspace string) string {
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

    func gapWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func writeGapJSON(path string, value any) error {
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

    func gapIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "finalize_evidence_ledger" {
  description = "Finalize the staged evidence ledger and source registry. workspace_dir is required, is created when missing, and bounds both output paths. The host verifies dates, snapshot quote equivalence, source references, controlled supplier maturity, quantitative qualifiers, and track coverage. Input contains paths and context only; repair rejected source, claim, or gap batches separately before retrying."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "sort"
      "strings"
      "time"
      "unicode"
    )

    type Input struct {
      WorkspaceDir     string `json:"workspace_dir"`
      LedgerPath        string `json:"ledger_path"`
      SourceRegistryPath string `json:"source_registry_path"`
      Mode              string `json:"mode"`
      Topic             string `json:"topic"`
      AsOfDate          string `json:"as_of_date"`
      ScopeArtifact     string `json:"scope_artifact"`
      Track             string `json:"track"`
    }

    type Output string

    type SourceRecord struct {
      ID              string   `json:"id"`
      URL             string   `json:"url"`
      NormalizedURL   string   `json:"normalized_url"`
      CanonicalURL    string   `json:"canonical_url"`
      OriginID        string   `json:"origin_id"`
      IndependenceGroup string `json:"independence_group"`
      ContentFingerprint string `json:"content_fingerprint"`
      Title           string   `json:"title"`
      Publisher       string   `json:"publisher"`
      PublicationDate string   `json:"publication_date"`
      AccessedAt      string   `json:"accessed_at"`
      SourceType      string   `json:"source_type"`
      SourceClass     string   `json:"source_class"`
      ReportingBasis  string   `json:"reporting_basis"`
      Provenance      string   `json:"provenance"`
      SnapshotPath    string   `json:"snapshot_path"`
      SnapshotSHA256  string   `json:"snapshot_sha256"`
      NamedEntities   []string `json:"named_entities"`
    }

    type Evidence struct {
      SourceID   string `json:"source_id"`
      Relation   string `json:"relation"`
      Directness string `json:"directness"`
      AuthorityForClaim bool `json:"authority_for_claim"`
      Locator    string `json:"locator"`
      ExactQuote string `json:"exact_quote"`
    }

    type Claim struct {
      ID              string            `json:"id"`
      ClaimType       string            `json:"claim_type"`
      Subject         string            `json:"subject"`
      Predicate       string            `json:"predicate"`
      Value           string            `json:"value"`
      Qualifiers      map[string]string `json:"qualifiers"`
      CoverageItemIDs []string          `json:"coverage_item_ids"`
      Inference       string            `json:"inference"`
      Evidence        []Evidence        `json:"evidence"`
      Status          string            `json:"status"`
      EvidenceStatus  string            `json:"evidence_status"`
      DisputeStatus   string            `json:"dispute_status"`
      ConfirmationBasis string          `json:"confirmation_basis"`
      IndependentSupportOrigins int      `json:"independent_support_origins"`
    }

    type Gap struct {
      CoverageItemID string `json:"coverage_item_id"`
      Reason         string `json:"reason"`
      ResearchAttempt string `json:"research_attempt"`
      Impact         string `json:"impact"`
    }

    type FreshnessCheck struct {
      ClaimID                string   `json:"claim_id"`
      CheckedAt              string   `json:"checked_at"`
      OfficialChannels       []string `json:"official_channels"`
      LatestPrimarySourceIDs []string `json:"latest_primary_source_ids"`
      Outcome                string   `json:"outcome"`
      Gap                    string   `json:"gap"`
    }

    type Ledger struct {
      Topic         string         `json:"topic"`
      AsOfDate      string         `json:"as_of_date"`
      Mode          string         `json:"mode"`
      ScopeArtifact string         `json:"scope_artifact"`
      Track         string         `json:"track"`
      Sources       []SourceRecord `json:"sources"`
      Claims        []Claim        `json:"claims"`
      Gaps          []Gap          `json:"gaps"`
      FreshnessChecks []FreshnessCheck `json:"freshness_checks"`
    }

    type Registry struct {
      AsOfDate string         `json:"as_of_date"`
      Sources  []SourceRecord `json:"sources"`
      SourceRecordCount int   `json:"source_record_count"`
      UniqueCanonicalURLCount int `json:"unique_canonical_url_count"`
      IndependentOriginCount int `json:"independent_origin_count"`
    }

    type ScopeItem struct {
      ID    string `json:"id"`
      Track string `json:"track"`
    }

    type Scope struct {
      CoverageItems []ScopeItem `json:"coverage_items"`
    }

    type Summary struct {
      LedgerPath        string `json:"ledger_path"`
      SourceRegistryPath string `json:"source_registry_path"`
      SourceCount       int    `json:"source_count"`
      ClaimCount        int    `json:"claim_count"`
      GapCount          int    `json:"gap_count"`
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := finalWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create evidence workspace: %w", err)
      }
      ledgerPath, issues := finalLedgerPath(input.LedgerPath, workspace)
      if ledgerPath == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      registryPath, registryErr := filepath.Abs(filepath.Clean(input.SourceRegistryPath))
      if registryErr != nil || !filepath.IsAbs(input.SourceRegistryPath) || filepath.Base(registryPath) != "source-registry.json" || filepath.Dir(registryPath) != filepath.Dir(ledgerPath) {
        issues = append(issues, finalIssue("invalid_path", "source_registry_path", "source_registry_path must be beside evidence-ledger.json"))
      }
      mode := strings.TrimSpace(input.Mode)
      if mode != "baseline" && mode != "track" && mode != "candidate" {
        issues = append(issues, finalIssue("mode", "mode", "mode must be baseline, track, or candidate"))
      }
      topic := strings.TrimSpace(input.Topic)
      if topic == "" {
        issues = append(issues, finalIssue("topic", "topic", "topic must not be empty"))
      }
      asOf, dateErr := time.Parse("2006-01-02", strings.TrimSpace(input.AsOfDate))
      if dateErr != nil {
        issues = append(issues, finalIssue("date", "as_of_date", "as_of_date must use YYYY-MM-DD"))
      }
      draft := filepath.Join(filepath.Dir(ledgerPath), ".evidence-draft")
      sources, err := loadFinalRecords[SourceRecord](filepath.Join(draft, "sources"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load evidence sources: %w", err)
      }
      finalAssignIndependenceGroups(sources)
      claims, err := loadFinalRecords[Claim](filepath.Join(draft, "claims"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load evidence claims: %w", err)
      }
      gaps, err := loadFinalRecords[Gap](filepath.Join(draft, "gaps"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load evidence gaps: %w", err)
      }
      freshnessChecks, err := loadFinalRecords[FreshnessCheck](filepath.Join(draft, "freshness"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load claim freshness checks: %w", err)
      }
      if len(sources) == 0 && !(mode == "candidate" && len(gaps) > 0) {
        issues = append(issues, finalIssue("sources", "sources", "register at least one retained source"))
      }
      if len(claims) == 0 && len(gaps) == 0 {
        issues = append(issues, finalIssue("claims", "claims", "stage at least one claim or structured gap"))
      }
      sourceByID := map[string]SourceRecord{}
      for _, source := range sources {
        sourceByID[source.ID] = source
        published, parseErr := time.Parse("2006-01-02", strings.TrimSpace(source.PublicationDate))
        if parseErr != nil {
          issues = append(issues, finalIssue("date", "publication_date", "publication_date must use YYYY-MM-DD"))
        } else if dateErr == nil && published.After(asOf) {
          issues = append(issues, finalIssue("source_after_as_of_date", source.ID, "source publication_date is later than the fixed as_of_date"))
        }
      }
      if dateErr == nil {
        for _, check := range freshnessChecks {
          if strings.TrimSpace(check.CheckedAt) != asOf.Format("2006-01-02") {
            issues = append(issues, finalIssue("freshness_date", check.ClaimID, "freshness checked_at must equal the fixed as_of_date"))
          }
        }
      }
      addressed := map[string]struct{}{}
      for claimIndex := range claims {
        claim := &claims[claimIndex]
        for evidenceIndex, evidence := range claim.Evidence {
          source, exists := sourceByID[evidence.SourceID]
          if !exists {
            path := fmt.Sprintf("%s.evidence[%d]", claim.ID, evidenceIndex)
            issues = append(issues, finalIssue("source_id", path, "claim references an unregistered source"))
            continue
          }
          snapshot, readErr := os.ReadFile(source.SnapshotPath)
          if readErr != nil {
            issues = append(issues, finalIssue("snapshot_path", source.ID, "cannot read snapshot: "+readErr.Error()))
            continue
          }
          if !finalQuoteMatches(evidence.ExactQuote, string(bytes.TrimPrefix(snapshot, []byte{0xef, 0xbb, 0xbf}))) {
            path := fmt.Sprintf("%s.evidence[%d].exact_quote", claim.ID, evidenceIndex)
            issues = append(issues, finalIssue("quote_not_found", path, "exact_quote is not text-equivalent to the registered snapshot"))
          }
        }
        status := finalClaimStatus(*claim, sourceByID)
        claim.EvidenceStatus = status.EvidenceStatus
        claim.DisputeStatus = status.DisputeStatus
        claim.ConfirmationBasis = status.ConfirmationBasis
        claim.IndependentSupportOrigins = status.IndependentSupportOrigins
        claim.Status = status.DisplayStatus
        for _, coverageID := range claim.CoverageItemIDs {
          addressed[coverageID] = struct{}{}
        }
      }
      for _, gap := range gaps {
        addressed[gap.CoverageItemID] = struct{}{}
      }
      if mode == "track" {
        assigned, scopeIssues := finalAssignedScope(input.ScopeArtifact, input.Track)
        issues = append(issues, scopeIssues...)
        if len(assigned) == 0 {
          issues = append(issues, finalIssue("scope_artifact", "track", "scope must assign at least one item to this track"))
        }
        for _, claim := range claims {
          for _, coverageID := range claim.CoverageItemIDs {
            if _, exists := assigned[coverageID]; !exists {
              issues = append(issues, finalIssue("coverage_item", claim.ID, "claim must reference only coverage items assigned to this track"))
            }
          }
        }
        for _, gap := range gaps {
          if _, exists := assigned[gap.CoverageItemID]; !exists {
            issues = append(issues, finalIssue("coverage_item", gap.CoverageItemID, "gap must reference a coverage item assigned to this track"))
          }
        }
        assignedIDs := make([]string, 0, len(assigned))
        for id := range assigned {
          assignedIDs = append(assignedIDs, id)
        }
        sort.Strings(assignedIDs)
        for _, id := range assignedIDs {
          if _, exists := addressed[id]; !exists {
            issues = append(issues, finalIssue("coverage_item", id, "scope item must have an evidence-backed claim or structured gap"))
          }
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      ledger := Ledger{
        Topic: topic, AsOfDate: strings.TrimSpace(input.AsOfDate), Mode: mode,
        ScopeArtifact: strings.TrimSpace(input.ScopeArtifact), Track: strings.TrimSpace(input.Track),
        Sources: sources, Claims: claims, Gaps: gaps, FreshnessChecks: freshnessChecks,
      }
      if err = writeFinalJSON(ledgerPath, ledger); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write evidence ledger: %w", err)
      }
      canonicalURLs := map[string]struct{}{}
      independentOrigins := map[string]struct{}{}
      for _, source := range sources {
        if source.CanonicalURL != "" {
          canonicalURLs[source.CanonicalURL] = struct{}{}
        }
        independenceGroup := source.IndependenceGroup
        if independenceGroup == "" {
          independenceGroup = source.OriginID
        }
        if independenceGroup != "" {
          independentOrigins[independenceGroup] = struct{}{}
        }
      }
      registry := Registry{
        AsOfDate: ledger.AsOfDate, Sources: sources, SourceRecordCount: len(sources),
        UniqueCanonicalURLCount: len(canonicalURLs), IndependentOriginCount: len(independentOrigins),
      }
      if err = writeFinalJSON(registryPath, registry); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write evidence source registry: %w", err)
      }
      summary, err := json.Marshal(Summary{
        LedgerPath: ledgerPath, SourceRegistryPath: registryPath,
        SourceCount: len(sources), ClaimCount: len(claims), GapCount: len(gaps),
      })
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode evidence ledger summary: %w", err)
      }
      output := Output(summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func finalLedgerPath(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{finalIssue("invalid_path", "ledger_path", "ledger_path must be absolute")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-ledger.json" || !finalWithin(path, workspace) {
        return "", []Issue{finalIssue("invalid_path", "ledger_path", "ledger_path must end in evidence-ledger.json under workspace_dir")}
      }
      return path, nil
    }

    func finalWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := finalBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !finalWithin(workspace, blocksRoot) {
        return "", []Issue{finalIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func finalBlocksRoot(workspace string) string {
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

    func loadFinalRecords[T any](directory string) ([]T, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
      if err != nil {
        return nil, err
      }
      sort.Strings(paths)
      records := make([]T, 0, len(paths))
      for _, path := range paths {
        var record T
        if err = readFinalJSON(path, &record); err != nil {
          return nil, err
        }
        records = append(records, record)
      }
      return records, nil
    }

    func finalQuoteMatches(quote, snapshot string) bool {
      if strings.Contains(snapshot, quote) {
        return true
      }
      quoteLines := finalLineEndings(quote)
      snapshotLines := finalLineEndings(snapshot)
      if strings.Contains(snapshotLines, quoteLines) {
        return true
      }
      quoteSpace := finalWhitespace(quoteLines)
      snapshotSpace := finalWhitespace(snapshotLines)
      if quoteSpace != "" && strings.Contains(snapshotSpace, quoteSpace) {
        return true
      }
      validQuote := strings.ToValidUTF8(quoteSpace, "�")
      validSnapshot := strings.ToValidUTF8(snapshotSpace, "�")
      return validQuote != "" && strings.Contains(validSnapshot, validQuote)
    }

    func finalLineEndings(value string) string {
      return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
    }

    func finalWhitespace(value string) string {
      fields := strings.FieldsFunc(value, func(character rune) bool { return unicode.IsSpace(character) })
      return strings.Join(fields, " ")
    }

    type finalStatus struct {
      EvidenceStatus string
      DisputeStatus string
      ConfirmationBasis string
      IndependentSupportOrigins int
      DisplayStatus string
    }

    func finalAssignIndependenceGroups(sources []SourceRecord) {
      parent := make([]int, len(sources))
      for index := range parent {
        parent[index] = index
      }
      var find func(int) int
      find = func(index int) int {
        if parent[index] != index {
          parent[index] = find(parent[index])
        }
        return parent[index]
      }
      union := func(left, right int) {
        leftRoot, rightRoot := find(left), find(right)
        if leftRoot != rightRoot {
          parent[rightRoot] = leftRoot
        }
      }
      firstByOrigin := map[string]int{}
      firstByContent := map[string]int{}
      for index, source := range sources {
        if source.OriginID != "" {
          if first, exists := firstByOrigin[source.OriginID]; exists {
            union(index, first)
          } else {
            firstByOrigin[source.OriginID] = index
          }
        }
        if source.ContentFingerprint != "" {
          if first, exists := firstByContent[source.ContentFingerprint]; exists {
            union(index, first)
          } else {
            firstByContent[source.ContentFingerprint] = index
          }
        }
      }
      groupByRoot := map[int]string{}
      for index, source := range sources {
        root := find(index)
        candidate := source.OriginID
        if candidate == "" {
          candidate = source.ID
        }
        if group, exists := groupByRoot[root]; !exists || candidate < group {
          groupByRoot[root] = candidate
        }
      }
      for index := range sources {
        sources[index].IndependenceGroup = groupByRoot[find(index)]
      }
    }

    func finalClaimStatus(claim Claim, sources map[string]SourceRecord) finalStatus {
      supports := make([]Evidence, 0)
      contradictions := make([]Evidence, 0)
      for _, evidence := range claim.Evidence {
        if evidence.Relation == "supports" {
          supports = append(supports, evidence)
        } else {
          contradictions = append(contradictions, evidence)
        }
      }
      evidenceStatus := "unknown"
      confirmationBasis := "none"
      independentAnonymous := map[string]struct{}{}
      hasIndirectSupport := false
      for _, evidence := range supports {
        source := sources[evidence.SourceID]
        sourceClass := finalSourceClass(source)
        if evidence.Directness == "indirect" && sourceClass != "lead_only" && sourceClass != "unknown" {
          hasIndirectSupport = true
          continue
        }
        if evidence.Directness != "direct" {
          continue
        }
        if sourceClass == "authoritative_primary" && evidence.AuthorityForClaim {
          evidenceStatus = "confirmed"
          confirmationBasis = "official_primary"
          break
        }
        if sourceClass == "qualified_media" && finalStrongMediaBasis(source.ReportingBasis) {
          evidenceStatus = "confirmed"
          confirmationBasis = "high_quality_media"
          break
        }
        if sourceClass == "qualified_media" && source.ReportingBasis == "anonymous_sources" && source.Provenance == "original" {
          independentAnonymous[source.IndependenceGroup] = struct{}{}
        }
        if sourceClass == "qualified_media" || sourceClass == "other_published" || sourceClass == "authoritative_primary" {
          if evidenceStatus == "unknown" {
            evidenceStatus = "reported"
          }
        }
      }
      if evidenceStatus != "confirmed" && len(independentAnonymous) >= 2 {
        evidenceStatus = "confirmed"
        confirmationBasis = "corroborated_media"
      }
      if strings.TrimSpace(claim.Inference) != "" && len(supports) > 0 {
        evidenceStatus = "inferred"
        confirmationBasis = "none"
      } else if evidenceStatus == "unknown" && hasIndirectSupport {
        evidenceStatus = "inferred"
      }
      disputeStatus := "clean"
      independentAnonymousContradictions := map[string]struct{}{}
      for _, evidence := range contradictions {
        source := sources[evidence.SourceID]
        sourceClass := finalSourceClass(source)
        strong := evidence.Directness == "direct" && (
          (sourceClass == "authoritative_primary" && evidence.AuthorityForClaim) ||
          (sourceClass == "qualified_media" && finalStrongMediaBasis(source.ReportingBasis)))
        if strong {
          disputeStatus = "disputed"
          break
        }
        if evidence.Directness == "direct" && sourceClass == "qualified_media" &&
          source.ReportingBasis == "anonymous_sources" && source.Provenance == "original" &&
          source.IndependenceGroup != "" {
          independentAnonymousContradictions[source.IndependenceGroup] = struct{}{}
        }
        disputeStatus = "challenged"
      }
      if disputeStatus != "disputed" && len(independentAnonymousContradictions) >= 2 {
        disputeStatus = "disputed"
      }
      displayStatus := evidenceStatus
      if disputeStatus == "disputed" {
        displayStatus = "disputed"
      }
      return finalStatus{
        EvidenceStatus: evidenceStatus, DisputeStatus: disputeStatus,
        ConfirmationBasis: confirmationBasis,
        IndependentSupportOrigins: len(independentAnonymous), DisplayStatus: displayStatus,
      }
    }

    func finalSourceClass(source SourceRecord) string {
      if source.SourceClass != "" {
        return source.SourceClass
      }
      switch source.SourceType {
      case "official_filing", "official_product", "official_statement", "regulator", "authoritative_primary":
        return "authoritative_primary"
      case "credible_media", "named_media", "peer_reviewed", "industry_research", "qualified_media":
        return "qualified_media"
      case "other", "other_published":
        return "other_published"
      case "self_media", "forum", "aggregator", "lead_only":
        return "lead_only"
      default:
        return "unknown"
      }
    }

    func finalStrongMediaBasis(value string) bool {
      switch value {
      case "public_document", "named_source", "direct_observation", "published_methodology":
        return true
      default:
        return false
      }
    }

    func finalAssignedScope(path, track string) (map[string]struct{}, []Issue) {
      if !filepath.IsAbs(path) || filepath.Base(filepath.Clean(path)) != "scope.json" {
        return map[string]struct{}{}, []Issue{finalIssue("scope_artifact", "scope_artifact", "scope_artifact must name scope.json")}
      }
      var scope Scope
      if err := readFinalJSON(path, &scope); err != nil {
        return map[string]struct{}{}, []Issue{finalIssue("scope_artifact", "scope_artifact", "scope_artifact must contain valid JSON")}
      }
      result := map[string]struct{}{}
      for _, item := range scope.CoverageItems {
        if item.Track == track && strings.TrimSpace(item.ID) != "" {
          result[strings.TrimSpace(item.ID)] = struct{}{}
        }
      }
      return result, nil
    }

    func finalWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func readFinalJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
      return json.Unmarshal(payload, value)
    }

    func writeFinalJSON(path string, value any) error {
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

    func finalIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "prepare_evidence_reconciliation" {
  description = "Merge accepted evidence ledgers, freshness records, and compact candidate key-claim reviews, then prepare deterministic conflicts for claims that share claim type, subject, predicate, and qualifiers but have different values. assessment_paths may be empty. Writes a draft and returns only its path and conflict IDs."

  source = <<-GO
    import (
      "bytes"
      "context"
      "crypto/sha256"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "regexp"
      "sort"
      "strings"
    )

    type Input struct {
      ArtifactPath   string   `json:"artifact_path"`
      LedgerPaths    []string `json:"ledger_paths"`
      AssessmentPaths []string `json:"assessment_paths"`
    }

    type Output struct {
      DraftPath    string   `json:"draft_path"`
      ClaimCount   int      `json:"claim_count"`
      ConflictCount int     `json:"conflict_count"`
      ConflictIDs  []string `json:"conflict_ids"`
    }

    var reconciliationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      artifact, issues := reconciliationArtifact(input.ArtifactPath, workspace)
      if len(input.LedgerPaths) == 0 {
        issues = append(issues, reconciliationIssue("ledger_paths", "ledger_paths", "ledger_paths must not be empty"))
      }
      root := reconciliationBlocksRoot(workspace)
      documents := make([]map[string]any, 0, len(input.LedgerPaths))
      for index, raw := range input.LedgerPaths {
        path, pathErr := filepath.Abs(filepath.Clean(raw))
        if pathErr != nil || !filepath.IsAbs(raw) || root == "" || !reconciliationWithin(path, root) {
          issues = append(issues, reconciliationIssue("ledger_path", fmt.Sprintf("ledger_paths[%d]", index), "ledger must belong to the current run"))
          continue
        }
        var document map[string]any
        if readErr := readReconciliationJSON(path, &document); readErr != nil {
          issues = append(issues, reconciliationIssue("ledger_path", fmt.Sprintf("ledger_paths[%d]", index), "cannot read evidence ledger: "+readErr.Error()))
          continue
        }
        if _, ok := document["claims"].([]any); !ok {
          issues = append(issues, reconciliationIssue("ledger_path", fmt.Sprintf("ledger_paths[%d]", index), "ledger must contain a claims list"))
          continue
        }
        documents = append(documents, document)
      }
      claimReviews := make([]any, 0)
      for index, raw := range input.AssessmentPaths {
        path, pathErr := filepath.Abs(filepath.Clean(raw))
        issuePath := fmt.Sprintf("assessment_paths[%d]", index)
        if pathErr != nil || !filepath.IsAbs(raw) || root == "" || filepath.Base(path) != "assessment.json" || !reconciliationWithin(path, root) {
          issues = append(issues, reconciliationIssue("assessment_path", issuePath, "assessment must be assessment.json in the current run"))
          continue
        }
        var document map[string]any
        if readErr := readReconciliationJSON(path, &document); readErr != nil {
          issues = append(issues, reconciliationIssue("assessment_path", issuePath, "cannot read candidate assessment: "+readErr.Error()))
          continue
        }
        reviews, ok := document["key_claim_reviews"].([]any)
        if !ok {
          issues = append(issues, reconciliationIssue("assessment_path", issuePath, "assessment must contain key_claim_reviews"))
          continue
        }
        claimReviews = append(claimReviews, reviews...)
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      claims := make([]any, 0)
      sources := map[string]any{}
      gaps := make([]any, 0)
      freshnessChecks := make([]any, 0)
      claimIDs := map[string]struct{}{}
      for _, document := range documents {
        for _, raw := range reconciliationList(document["sources"]) {
          source, ok := raw.(map[string]any)
          if !ok {
            continue
          }
          id := strings.TrimSpace(reconciliationString(source["id"]))
          if existing, duplicate := sources[id]; duplicate {
            if !reconciliationSameSourceIdentity(existing, source) {
              issues = append(issues, reconciliationIssue("source_id_conflict", id, "duplicate source ID has conflicting identity or metadata"))
            }
            continue
          }
          sources[id] = source
        }
        gaps = append(gaps, reconciliationList(document["gaps"])...)
        freshnessChecks = append(freshnessChecks, reconciliationList(document["freshness_checks"])...)
        for _, raw := range reconciliationList(document["claims"]) {
          claim, ok := raw.(map[string]any)
          if !ok {
            issues = append(issues, reconciliationIssue("claim_id", "claims", "claim IDs must be valid and globally unique"))
            continue
          }
          id := strings.TrimSpace(reconciliationString(claim["id"]))
          _, duplicate := claimIDs[id]
          if !reconciliationID.MatchString(id) || duplicate {
            issues = append(issues, reconciliationIssue("claim_id", id, "claim IDs must be valid and globally unique"))
            continue
          }
          claimIDs[id] = struct{}{}
          claims = append(claims, claim)
        }
      }
      for index, raw := range claimReviews {
        review, ok := raw.(map[string]any)
        claimID := ""
        if ok {
          claimID = strings.TrimSpace(reconciliationString(review["claim_id"]))
        }
        if _, exists := claimIDs[claimID]; !ok || !exists {
          issues = append(issues, reconciliationIssue("claim_review", fmt.Sprintf("claim_reviews[%d]", index), "key claim review must reference a merged claim"))
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      grouped := map[string][]map[string]any{}
      for _, raw := range claims {
        claim := raw.(map[string]any)
        availability, _ := claim["reconciliation_availability"].(string)
        availability = strings.TrimSpace(availability)
        if availability != "" && availability != "available" {
          continue
        }
        key, keyErr := reconciliationClaimKey(claim)
        if keyErr != nil {
          return ToolResponse[Output]{}, fmt.Errorf("build evidence claim key: %w", keyErr)
        }
        grouped[key] = append(grouped[key], claim)
      }
      keys := make([]string, 0, len(grouped))
      for key := range grouped {
        keys = append(keys, key)
      }
      sort.Strings(keys)
      conflicts := make([]any, 0)
      conflictIDs := make([]string, 0)
      for _, key := range keys {
        group := grouped[key]
        normalizedValues := map[string]struct{}{}
        values := map[string]struct{}{}
        ids := make([]string, 0, len(group))
        for _, claim := range group {
          value := reconciliationString(claim["value"])
          normalizedValues[reconciliationNormalized(value)] = struct{}{}
          values[value] = struct{}{}
          ids = append(ids, reconciliationString(claim["id"]))
        }
        if len(normalizedValues) < 2 {
          continue
        }
        sort.Strings(ids)
        valueList := make([]string, 0, len(values))
        for value := range values {
          valueList = append(valueList, value)
        }
        sort.Strings(valueList)
        digest := sha256.Sum256([]byte(key + "|" + strings.Join(ids, "|")))
        id := "conflict-" + fmt.Sprintf("%x", digest)[:16]
        conflicts = append(conflicts, map[string]any{
          "id": id, "claim_ids": ids, "subject": reconciliationString(group[0]["subject"]),
          "predicate": reconciliationString(group[0]["predicate"]), "values": valueList,
        })
        conflictIDs = append(conflictIDs, id)
      }
      sourceList := make([]any, 0, len(sources))
      sourceIDs := make([]string, 0, len(sources))
      for id := range sources {
        sourceIDs = append(sourceIDs, id)
      }
      sort.Strings(sourceIDs)
      for _, id := range sourceIDs {
        sourceList = append(sourceList, sources[id])
      }
      reconciliationAssignIndependenceGroups(sourceList)
      draft := map[string]any{
        "sources": sourceList, "claims": claims, "gaps": gaps, "freshness_checks": freshnessChecks,
        "claim_reviews": claimReviews, "conflicts": conflicts,
      }
      draftPath := filepath.Join(filepath.Dir(artifact), ".evidence-reconciliation", "merged.json")
      if err = writeReconciliationJSON(draftPath, draft); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write evidence reconciliation draft: %w", err)
      }
      output := Output{
        DraftPath: draftPath, ClaimCount: len(claims), ConflictCount: len(conflicts), ConflictIDs: conflictIDs,
      }
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func reconciliationArtifact(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{reconciliationIssue("invalid_path", "artifact_path", "artifact_path must end in evidence-resolution.json under this block workspace")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-resolution.json" || !reconciliationWithin(path, workspace) {
        return "", []Issue{reconciliationIssue("invalid_path", "artifact_path", "artifact_path must end in evidence-resolution.json under this block workspace")}
      }
      return path, nil
    }

    func reconciliationClaimKey(claim map[string]any) (string, error) {
      qualifiers, err := json.Marshal(claim["qualifiers"])
      if err != nil {
        return "", err
      }
      values := []string{
        reconciliationString(claim["claim_type"]), reconciliationString(claim["subject"]),
        reconciliationString(claim["predicate"]), string(qualifiers),
      }
      for index := range values {
        values[index] = reconciliationNormalized(values[index])
      }
      return strings.Join(values, "|"), nil
    }

    func reconciliationNormalized(value string) string {
      return strings.ToLower(strings.Join(strings.Fields(value), " "))
    }

    func reconciliationString(value any) string {
      if result, ok := value.(string); ok {
        return result
      }
      return fmt.Sprint(value)
    }

    func reconciliationList(value any) []any {
      if result, ok := value.([]any); ok {
        return result
      }
      return []any{}
    }

    func reconciliationSameSourceIdentity(left, right any) bool {
      keys := []string{
        "normalized_url", "canonical_url", "origin_id", "content_fingerprint", "snapshot_sha256",
        "title", "publisher", "publication_date", "source_type", "source_class", "reporting_basis", "provenance",
      }
      identity := func(value any) map[string]any {
        source, _ := value.(map[string]any)
        result := make(map[string]any, len(keys))
        for _, key := range keys {
          result[key] = source[key]
        }
        return result
      }
      leftPayload, leftErr := json.Marshal(identity(left))
      rightPayload, rightErr := json.Marshal(identity(right))
      return leftErr == nil && rightErr == nil && bytes.Equal(leftPayload, rightPayload)
    }

    func reconciliationAssignIndependenceGroups(sources []any) {
      parent := make([]int, len(sources))
      for index := range parent {
        parent[index] = index
      }
      var find func(int) int
      find = func(index int) int {
        if parent[index] != index {
          parent[index] = find(parent[index])
        }
        return parent[index]
      }
      union := func(left, right int) {
        leftRoot, rightRoot := find(left), find(right)
        if leftRoot != rightRoot {
          parent[rightRoot] = leftRoot
        }
      }
      firstByOrigin := map[string]int{}
      firstByContent := map[string]int{}
      for index, raw := range sources {
        source, _ := raw.(map[string]any)
        origin, _ := source["origin_id"].(string)
        content, _ := source["content_fingerprint"].(string)
        if origin != "" {
          if first, exists := firstByOrigin[origin]; exists {
            union(index, first)
          } else {
            firstByOrigin[origin] = index
          }
        }
        if content != "" {
          if first, exists := firstByContent[content]; exists {
            union(index, first)
          } else {
            firstByContent[content] = index
          }
        }
      }
      groupByRoot := map[int]string{}
      for index, raw := range sources {
        source, _ := raw.(map[string]any)
        candidate, _ := source["origin_id"].(string)
        if candidate == "" {
          candidate, _ = source["id"].(string)
        }
        root := find(index)
        if group, exists := groupByRoot[root]; !exists || candidate < group {
          groupByRoot[root] = candidate
        }
      }
      for index, raw := range sources {
        if source, ok := raw.(map[string]any); ok {
          source["independence_group"] = groupByRoot[find(index)]
        }
      }
    }

    func reconciliationBlocksRoot(workspace string) string {
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

    func reconciliationWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func readReconciliationJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
      return json.Unmarshal(payload, value)
    }

    func writeReconciliationJSON(path string, value any) error {
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

    func reconciliationIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "resolve_evidence_conflict" {
  description = "Resolve exactly one prepared evidence conflict. Use prefer with selected claim IDs, preserve_both with every conflicting claim ID, or unresolved when retained evidence cannot decide."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Input struct {
      ArtifactPath   string   `json:"artifact_path"`
      ConflictID     string   `json:"conflict_id"`
      Decision       string   `json:"decision"`
      ChosenClaimIDs []string `json:"chosen_claim_ids"`
      Rationale      string   `json:"rationale"`
    }

    type Output struct {
      ConflictID    string `json:"conflict_id"`
      ResolutionPath string `json:"resolution_path"`
    }

    type Conflict struct {
      ID       string   `json:"id"`
      ClaimIDs []string `json:"claim_ids"`
    }

    type Draft struct {
      Conflicts []Conflict `json:"conflicts"`
    }

    type Resolution struct {
      ConflictID     string   `json:"conflict_id"`
      Decision       string   `json:"decision"`
      ChosenClaimIDs []string `json:"chosen_claim_ids"`
      Rationale      string   `json:"rationale"`
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      artifact, issues := resolveArtifact(input.ArtifactPath, workspace)
      decision := strings.TrimSpace(input.Decision)
      if decision != "prefer" && decision != "preserve_both" && decision != "unresolved" {
        issues = append(issues, resolveIssue("decision", "decision", "decision must be prefer, preserve_both, or unresolved"))
      }
      chosen := map[string]struct{}{}
      for index, id := range input.ChosenClaimIDs {
        path := fmt.Sprintf("chosen_claim_ids[%d]", index)
        if _, duplicate := chosen[id]; duplicate {
          issues = append(issues, resolveIssue("chosen_claim_ids", path, "chosen claim IDs must be unique"))
        }
        chosen[id] = struct{}{}
      }
      if decision == "prefer" && len(input.ChosenClaimIDs) == 0 {
        issues = append(issues, resolveIssue("chosen_claim_ids", "chosen_claim_ids", "prefer requires a chosen claim"))
      }
      if decision == "unresolved" && len(input.ChosenClaimIDs) > 0 {
        issues = append(issues, resolveIssue("chosen_claim_ids", "chosen_claim_ids", "unresolved must not choose any conflicting claim"))
      }
      rationale := strings.TrimSpace(input.Rationale)
      if rationale == "" {
        issues = append(issues, resolveIssue("rationale", "rationale", "rationale must not be empty"))
      }
      if artifact == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      draftPath := filepath.Join(filepath.Dir(artifact), ".evidence-reconciliation", "merged.json")
      var draft Draft
      if err = readResolveJSON(draftPath, &draft); err != nil {
        issues = append(issues, resolveIssue("draft", "artifact_path", "call prepare before resolving conflicts"))
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      conflictID := strings.TrimSpace(input.ConflictID)
      var conflict *Conflict
      for index := range draft.Conflicts {
        if draft.Conflicts[index].ID == conflictID {
          conflict = &draft.Conflicts[index]
          break
        }
      }
      if conflict == nil {
        issues = append(issues, resolveIssue("conflict_id", "conflict_id", "conflict_id must reference the prepared draft"))
      }
      allowed := map[string]struct{}{}
      if conflict != nil {
        for _, id := range conflict.ClaimIDs {
          allowed[id] = struct{}{}
        }
      }
      for index, id := range input.ChosenClaimIDs {
        path := fmt.Sprintf("chosen_claim_ids[%d]", index)
        if _, ok := allowed[id]; !ok {
          issues = append(issues, resolveIssue("chosen_claim_ids", path, "chosen claims must belong to this conflict"))
        }
      }
      if decision == "prefer" && len(allowed) > 0 && len(chosen) >= len(allowed) {
        issues = append(issues, resolveIssue("chosen_claim_ids", "chosen_claim_ids", "prefer must choose a strict subset of the conflicting claims"))
      }
      if decision == "preserve_both" && len(chosen) != len(allowed) {
        issues = append(issues, resolveIssue("chosen_claim_ids", "chosen_claim_ids", "preserve_both must retain every conflicting claim"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      resolution := Resolution{
        ConflictID: conflictID, Decision: decision,
        ChosenClaimIDs: input.ChosenClaimIDs, Rationale: rationale,
      }
      resolutionPath := filepath.Join(filepath.Dir(artifact), ".evidence-reconciliation", "resolutions", conflictID+".json")
      if err = writeResolveJSON(resolutionPath, resolution); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write evidence conflict resolution: %w", err)
      }
      output := Output{ConflictID: conflictID, ResolutionPath: resolutionPath}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func resolveArtifact(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{resolveIssue("invalid_path", "artifact_path", "artifact_path must end in evidence-resolution.json under this block workspace")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-resolution.json" || !resolveWithin(path, workspace) {
        return "", []Issue{resolveIssue("invalid_path", "artifact_path", "artifact_path must end in evidence-resolution.json under this block workspace")}
      }
      return path, nil
    }

    func resolveWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func readResolveJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), value)
    }

    func writeResolveJSON(path string, value any) error {
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

    func resolveIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "finalize_evidence_reconciliation" {
  description = "Finalize a prepared evidence reconciliation after every detected conflict has an explicit resolution. Input contains only the final artifact path."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "sort"
      "strings"
    )

    type Input struct {
      ArtifactPath string `json:"artifact_path"`
    }

    type Output string

    type Summary struct {
      ArtifactPath  string `json:"artifact_path"`
      ClaimCount    int    `json:"claim_count"`
      ConflictCount int    `json:"conflict_count"`
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      artifact, issues := finalReconciliationArtifact(input.ArtifactPath, workspace)
      if artifact == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      root := filepath.Join(filepath.Dir(artifact), ".evidence-reconciliation")
      var draft map[string]any
      if err = readFinalReconciliationJSON(filepath.Join(root, "merged.json"), &draft); err != nil {
        return ToolResponse[Output]{Accepted: false, Issues: []Issue{finalReconciliationIssue("draft", "artifact_path", "call prepare before finalizing")}}, nil
      }
      resolutions := map[string]map[string]any{}
      paths, globErr := filepath.Glob(filepath.Join(root, "resolutions", "*.json"))
      if globErr != nil {
        return ToolResponse[Output]{}, fmt.Errorf("list evidence conflict resolutions: %w", globErr)
      }
      sort.Strings(paths)
      for _, path := range paths {
        var resolution map[string]any
        if err = readFinalReconciliationJSON(path, &resolution); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("read evidence conflict resolution: %w", err)
        }
        if id, ok := resolution["conflict_id"].(string); ok {
          resolutions[id] = resolution
        }
      }
      conflicts, _ := draft["conflicts"].([]any)
      availability := map[string]string{}
      claims, _ := draft["claims"].([]any)
      for _, raw := range claims {
        if claim, ok := raw.(map[string]any); ok {
          if id, ok := claim["id"].(string); ok {
            inherited, _ := claim["reconciliation_availability"].(string)
            if inherited != "excluded" && inherited != "unresolved" {
              inherited = "available"
            }
            availability[id] = inherited
          }
        }
      }
      for _, raw := range conflicts {
        conflict, ok := raw.(map[string]any)
        if !ok {
          continue
        }
        id, _ := conflict["id"].(string)
        resolution, exists := resolutions[id]
        if !exists {
          issues = append(issues, finalReconciliationIssue("unresolved_conflict", id, "every prepared conflict requires an explicit resolution"))
          continue
        }
        conflict["resolution"] = resolution
        chosen := map[string]struct{}{}
        for _, rawID := range finalReconciliationList(resolution["chosen_claim_ids"]) {
          if claimID, ok := rawID.(string); ok {
            chosen[claimID] = struct{}{}
          }
        }
        decision, _ := resolution["decision"].(string)
        for _, rawID := range finalReconciliationList(conflict["claim_ids"]) {
          claimID, ok := rawID.(string)
          if !ok {
            continue
          }
          if availability[claimID] != "available" {
            continue
          }
          switch decision {
          case "prefer":
            if _, selected := chosen[claimID]; selected {
              availability[claimID] = "available"
            } else {
              availability[claimID] = "excluded"
            }
          case "unresolved":
            availability[claimID] = "unresolved"
          }
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      for _, raw := range claims {
        if claim, ok := raw.(map[string]any); ok {
          if id, ok := claim["id"].(string); ok {
            claim["reconciliation_availability"] = availability[id]
          }
        }
      }
      if reviews, ok := draft["claim_reviews"].([]any); ok {
        for _, raw := range reviews {
          review, ok := raw.(map[string]any)
          if !ok {
            continue
          }
          claimID, _ := review["claim_id"].(string)
          claimAvailability := availability[claimID]
          if claimAvailability == "available" || claimAvailability == "" {
            continue
          }
          review["effective_evidence_status"] = "unknown"
          message := "Key claim " + claimID + " is " + claimAvailability + " after final evidence reconciliation."
          if gap, _ := review["gap"].(string); strings.TrimSpace(gap) != "" {
            message = strings.TrimSpace(gap) + " " + message
          }
          review["gap"] = message
        }
      }
      draft["artifact_kind"] = "r42_evidence_resolution"
      draft["reconciliation_status"] = "finalized"
      if err = writeFinalReconciliationJSON(artifact, draft); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write evidence reconciliation: %w", err)
      }
      summary, err := json.Marshal(Summary{ArtifactPath: artifact, ClaimCount: len(claims), ConflictCount: len(conflicts)})
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode evidence reconciliation summary: %w", err)
      }
      output := Output(summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func finalReconciliationArtifact(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{finalReconciliationIssue("invalid_path", "artifact_path", "artifact_path must end in evidence-resolution.json under this block workspace")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "evidence-resolution.json" || !finalReconciliationWithin(path, workspace) {
        return "", []Issue{finalReconciliationIssue("invalid_path", "artifact_path", "artifact_path must end in evidence-resolution.json under this block workspace")}
      }
      return path, nil
    }

    func finalReconciliationWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func finalReconciliationList(value any) []any {
      if result, ok := value.([]any); ok {
        return result
      }
      return []any{}
    }

    func readFinalReconciliationJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), value)
    }

    func writeFinalReconciliationJSON(path string, value any) error {
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

    func finalReconciliationIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "stage_report_claims" {
  description = "Stage from one through five atomic report clauses. statement is one independently auditable factual or analytical clause. claim_kind may be fact or inference; unfamiliar values conservatively become inference. Each clause cites upstream evidence claim IDs, and reusing an ID replaces only that report claim."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "regexp"
      "strings"
    )

    type ReportClaim struct {
      ID                 string   `json:"id"`
      Section            string   `json:"section"`
      Statement          string   `json:"statement"`
      ClaimKind          string   `json:"claim_kind"`
      SupportingClaimIDs []string `json:"supporting_claim_ids"`
    }

    type Input struct {
      ManifestPath string        `json:"manifest_path"`
      Claims       []ReportClaim `json:"claims"`
    }

    type Output struct {
      ClaimIDs []string `json:"claim_ids"`
      Staged   int      `json:"staged"`
    }

    var reportClaimID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      manifest, issues := stagedManifestPath(input.ManifestPath, workspace)
      if len(input.Claims) == 0 {
        issues = append(issues, stagedManifestIssue("claims", "claims", "claims must contain from one through five items"))
      }
      if len(input.Claims) > 5 {
        issues = append(issues, stagedManifestIssue("batch_size", "claims", "submit at most five report claims per call"))
      }
      seen := map[string]struct{}{}
      for index, claim := range input.Claims {
        path := fmt.Sprintf("claims[%d]", index)
        id := strings.TrimSpace(claim.ID)
        if !reportClaimID.MatchString(id) {
          issues = append(issues, stagedManifestIssue("claim_id", path+".id", "id must be a short stable identifier"))
        }
        if _, exists := seen[id]; exists {
          issues = append(issues, stagedManifestIssue("claim_id", path+".id", "claim IDs must be unique in the batch"))
        }
        seen[id] = struct{}{}
        if strings.TrimSpace(claim.Section) == "" {
          issues = append(issues, stagedManifestIssue("claim", path+".section", "section must not be empty"))
        }
        if strings.TrimSpace(claim.Statement) == "" {
          issues = append(issues, stagedManifestIssue("claim", path+".statement", "statement must not be empty"))
        }
        if len(claim.SupportingClaimIDs) == 0 {
          issues = append(issues, stagedManifestIssue("supporting_claim_ids", path+".supporting_claim_ids", "every report claim must cite at least one upstream claim"))
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      directory := filepath.Join(filepath.Dir(manifest), ".report-manifest-draft")
      ids := make([]string, 0, len(input.Claims))
      for _, claim := range input.Claims {
        switch strings.ToLower(strings.TrimSpace(claim.ClaimKind)) {
        case "", "fact":
          claim.ClaimKind = "fact"
        default:
          claim.ClaimKind = "inference"
        }
        if err = writeStagedManifestJSON(filepath.Join(directory, claim.ID+".json"), claim); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("write staged report claim: %w", err)
        }
        ids = append(ids, claim.ID)
      }
      output := Output{ClaimIDs: ids, Staged: len(ids)}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func stagedManifestPath(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{stagedManifestIssue("invalid_path", "manifest_path", "manifest_path must end in report-manifest.json under this block workspace")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "report-manifest.json" || !stagedManifestWithin(path, workspace) {
        return "", []Issue{stagedManifestIssue("invalid_path", "manifest_path", "manifest_path must end in report-manifest.json under this block workspace")}
      }
      return path, nil
    }

    func stagedManifestWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func writeStagedManifestJSON(path string, value any) error {
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

    func stagedManifestIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "finalize_report_manifest" {
  description = "Finalize report-manifest.json and mechanically audit atomic report footnotes against exactly one finalized evidence-resolution.json. Every staged statement must be immediately followed, allowing only whitespace, by [^CLAIM_ID]. The host applies reconciliation availability and freshness downgrades, derives evidence and dispute status, resolves every canonical source URL, stores the minimal upstream semantic context for QC, and idempotently writes the footnote definitions into report.md."

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
      "sort"
      "strings"
      "unicode"
    )

    const claimSourcesStart = "<!-- r42:claim-sources:start -->"
    const claimSourcesEnd = "<!-- r42:claim-sources:end -->"

    type Input struct {
      ReportPath    string   `json:"report_path"`
      ManifestPath  string   `json:"manifest_path"`
      EvidencePaths []string `json:"evidence_paths"`
    }

    type Output string

    type ReportSource struct {
      SourceID       string `json:"source_id"`
      SourceRecordIDs []string `json:"source_record_ids"`
      Relation       string `json:"relation"`
      Title          string `json:"title"`
      Publisher      string `json:"publisher"`
      PublicationDate string `json:"publication_date"`
      URL            string `json:"url"`
      RetrievalURL   string `json:"retrieval_url"`
      OriginID       string `json:"origin_id"`
    }

    type ReportClaim struct {
      ID                 string         `json:"id"`
      Section            string         `json:"section"`
      Statement          string         `json:"statement"`
      ClaimKind          string         `json:"claim_kind"`
      SupportingClaimIDs []string       `json:"supporting_claim_ids"`
      EvidenceStatus     string         `json:"evidence_status"`
      DisputeStatus      string         `json:"dispute_status"`
      ConfirmationBasis  string         `json:"confirmation_basis"`
      FreshnessStatus    string         `json:"freshness_status"`
      FreshnessGaps      []string       `json:"freshness_gaps,omitempty"`
      Sources            []ReportSource `json:"sources,omitempty"`
      UpstreamClaims     []UpstreamClaim `json:"upstream_claims,omitempty"`
    }

    type UpstreamSource struct {
      ID              string `json:"id"`
      URL             string `json:"url"`
      NormalizedURL   string `json:"normalized_url"`
      CanonicalURL    string `json:"canonical_url"`
      OriginID        string `json:"origin_id"`
      IndependenceGroup string `json:"independence_group"`
      Title           string `json:"title"`
      Publisher       string `json:"publisher"`
      PublicationDate string `json:"publication_date"`
      SourceType      string `json:"source_type"`
      SourceClass     string `json:"source_class"`
      ReportingBasis  string `json:"reporting_basis"`
      Provenance      string `json:"provenance"`
    }

    type UpstreamEvidence struct {
      SourceID         string          `json:"source_id"`
      Relation         string          `json:"relation"`
      Directness       string          `json:"directness"`
      AuthorityForClaim bool           `json:"authority_for_claim"`
      Locator          string          `json:"locator"`
      ExactQuote       string          `json:"exact_quote"`
      Source           *UpstreamSource `json:"source,omitempty"`
    }

    type UpstreamClaim struct {
      ID       string             `json:"id"`
      ClaimType string            `json:"claim_type"`
      Subject   string            `json:"subject"`
      Predicate string            `json:"predicate"`
      Value     string            `json:"value"`
      Qualifiers map[string]string `json:"qualifiers"`
      Inference string            `json:"inference"`
      Status   string             `json:"status"`
      EvidenceStatus string       `json:"evidence_status"`
      DisputeStatus string        `json:"dispute_status"`
      ConfirmationBasis string    `json:"confirmation_basis"`
      ReconciliationAvailability string `json:"reconciliation_availability"`
      EffectiveEvidenceStatus string   `json:"effective_evidence_status"`
      FreshnessStatus string            `json:"freshness_status"`
      FreshnessGap string               `json:"freshness_gap"`
      FreshnessCheck *FreshnessCheck    `json:"freshness_check,omitempty"`
      Evidence []UpstreamEvidence `json:"evidence"`
    }

    type FreshnessCheck struct {
      ClaimID                string   `json:"claim_id"`
      CheckedAt              string   `json:"checked_at"`
      OfficialChannels       []string `json:"official_channels"`
      LatestPrimarySourceIDs []string `json:"latest_primary_source_ids"`
      Outcome                string   `json:"outcome"`
      Gap                    string   `json:"gap"`
    }

    type ClaimReview struct {
      ClaimID                 string `json:"claim_id"`
      EffectiveEvidenceStatus string `json:"effective_evidence_status"`
      FreshnessStatus         string `json:"freshness_status"`
      Gap                     string `json:"gap"`
    }

    type EvidenceResolution struct {
      Decision       string   `json:"decision"`
      ChosenClaimIDs []string `json:"chosen_claim_ids"`
    }

    type EvidenceConflict struct {
      ClaimIDs  []string           `json:"claim_ids"`
      Resolution EvidenceResolution `json:"resolution"`
    }

    type EvidenceDocument struct {
      ArtifactKind         string             `json:"artifact_kind"`
      ReconciliationStatus string             `json:"reconciliation_status"`
      Sources              []UpstreamSource   `json:"sources"`
      Claims               []UpstreamClaim    `json:"claims"`
      ClaimReviews         []ClaimReview      `json:"claim_reviews"`
      FreshnessChecks      []FreshnessCheck   `json:"freshness_checks"`
      Conflicts            []EvidenceConflict `json:"conflicts"`
    }

    type Manifest struct {
      ReportPath string        `json:"report_path"`
      Claims     []ReportClaim `json:"claims"`
    }

    type Summary struct {
      ReportPath         string `json:"report_path"`
      ManifestPath       string `json:"manifest_path"`
      ReportClaimCount   int    `json:"report_claim_count"`
      UpstreamClaimCount int    `json:"upstream_claim_count"`
    }

    var reportClaimTag = regexp.MustCompile(`\[\^([A-Za-z0-9][A-Za-z0-9_-]{0,127})\]`)
    var existingClaimSources = regexp.MustCompile(`(?s)\n?<!-- r42:claim-sources:start -->.*?<!-- r42:claim-sources:end -->\s*`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      manifestPath, issues := finalManifestPath(input.ManifestPath, workspace)
      if len(input.EvidencePaths) != 1 {
        issues = append(issues, finalManifestIssue("evidence_paths", "evidence_paths", "provide exactly one finalized evidence-resolution.json artifact"))
      }
      if manifestPath == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      reportPath, reportErr := filepath.Abs(filepath.Clean(input.ReportPath))
      if reportErr != nil || !filepath.IsAbs(input.ReportPath) || filepath.Base(reportPath) != "report.md" || filepath.Dir(reportPath) != filepath.Dir(manifestPath) || !finalManifestFileExists(reportPath) {
        issues = append(issues, finalManifestIssue("report_path", "report_path", "report_path must name report.md beside the manifest"))
      }
      staged, err := loadReportClaims(filepath.Join(filepath.Dir(manifestPath), ".report-manifest-draft"))
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("load staged report claims: %w", err)
      }
      if len(staged) == 0 {
        issues = append(issues, finalManifestIssue("manifest", "manifest_path", "stage at least one report claim before finalizing"))
      }
      upstream, sources, upstreamIssues := loadUpstreamEvidence(input.EvidencePaths, workspace)
      issues = append(issues, upstreamIssues...)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      reportPayload, err := os.ReadFile(reportPath)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read report: %w", err)
      }
      reportText := string(bytes.TrimPrefix(reportPayload, []byte{0xef, 0xbb, 0xbf}))
      reportBody := existingClaimSources.ReplaceAllString(reportText, "")
      reportTags := map[string]int{}
      for _, match := range reportClaimTag.FindAllStringSubmatch(reportBody, -1) {
        reportTags[match[1]]++
      }
      manifestIDs := map[string]struct{}{}
      for _, claim := range staged {
        manifestIDs[claim.ID] = struct{}{}
      }
      unknownTags := make([]string, 0)
      for id := range reportTags {
        if _, exists := manifestIDs[id]; !exists {
          unknownTags = append(unknownTags, id)
        }
      }
      sort.Strings(unknownTags)
      for _, id := range unknownTags {
        issues = append(issues, finalManifestIssue("unknown_report_claim", id, "report tag must reference a staged report claim"))
      }
      enriched := make([]ReportClaim, 0, len(staged))
      for _, claim := range staged {
        tagCount := reportTags[claim.ID]
        if tagCount == 0 {
          issues = append(issues, finalManifestIssue("unused_report_claim", claim.ID, "every manifest claim must be cited by report.md"))
        }
        if tagCount > 1 {
          issues = append(issues, finalManifestIssue("duplicate_report_claim_tag", claim.ID, "each atomic report claim marker must appear exactly once"))
        }
        if tagCount > 0 && !finalManifestAdjacent(reportBody, claim.Statement, claim.ID) {
          issues = append(issues, finalManifestIssue("claim_marker_not_adjacent", claim.ID, "the atomic statement must be immediately followed by its footnote marker, allowing only whitespace"))
        }
        claimSources, sourceIssues := sourcesForReportClaim(claim, upstream, sources)
        issues = append(issues, sourceIssues...)
        claim.Sources = claimSources
        claim.EvidenceStatus, claim.DisputeStatus, claim.ConfirmationBasis = reportClaimStatus(claim, upstream)
        claim.FreshnessStatus, claim.FreshnessGaps = reportClaimFreshness(claim, upstream)
        claim.UpstreamClaims = reportUpstreamClaims(claim, upstream, sources)
        enriched = append(enriched, claim)
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      reportWithSources := withClaimSources(reportText, enriched)
      if err = os.WriteFile(reportPath, []byte(reportWithSources), 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write report claim sources: %w", err)
      }
      if err = writeFinalManifestJSON(manifestPath, Manifest{ReportPath: reportPath, Claims: enriched}); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write report manifest: %w", err)
      }
      summary, err := json.Marshal(Summary{
        ReportPath: reportPath, ManifestPath: manifestPath,
        ReportClaimCount: len(staged), UpstreamClaimCount: len(upstream),
      })
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode report manifest summary: %w", err)
      }
      output := Output(summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func loadReportClaims(directory string) ([]ReportClaim, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
      if err != nil {
        return nil, err
      }
      sort.Strings(paths)
      claims := make([]ReportClaim, 0, len(paths))
      for _, path := range paths {
        var claim ReportClaim
        if err = readFinalManifestJSON(path, &claim); err != nil {
          return nil, err
        }
        claims = append(claims, claim)
      }
      return claims, nil
    }

    func loadUpstreamEvidence(paths []string, workspace string) (map[string]UpstreamClaim, map[string]UpstreamSource, []Issue) {
      root := finalManifestBlocksRoot(workspace)
      claims := map[string]UpstreamClaim{}
      sources := map[string]UpstreamSource{}
      issues := []Issue{}
      for index, raw := range paths {
        path, pathErr := filepath.Abs(filepath.Clean(raw))
        if pathErr != nil || !filepath.IsAbs(raw) || root == "" || filepath.Base(path) != "evidence-resolution.json" || !finalManifestWithin(path, root) {
          issues = append(issues, finalManifestIssue("evidence_path", fmt.Sprintf("evidence_paths[%d]", index), "evidence artifact must be evidence-resolution.json in this run"))
          continue
        }
        var document EvidenceDocument
        if err := readFinalManifestJSON(path, &document); err != nil {
          issues = append(issues, finalManifestIssue("evidence_path", fmt.Sprintf("evidence_paths[%d]", index), "cannot read evidence artifact: "+err.Error()))
          continue
        }
        if document.ArtifactKind != "r42_evidence_resolution" || document.ReconciliationStatus != "finalized" {
          issues = append(issues, finalManifestIssue("evidence_not_finalized", fmt.Sprintf("evidence_paths[%d]", index), "evidence artifact must be produced by finalize_evidence_reconciliation"))
          continue
        }
        for _, source := range document.Sources {
          if strings.TrimSpace(source.ID) != "" {
            sources[source.ID] = source
          }
        }
        availability := map[string]string{}
        for _, claim := range document.Claims {
          status := claim.ReconciliationAvailability
          if status == "" {
            status = "available"
          }
          availability[claim.ID] = status
        }
        for _, conflict := range document.Conflicts {
          chosen := map[string]struct{}{}
          for _, claimID := range conflict.Resolution.ChosenClaimIDs {
            chosen[claimID] = struct{}{}
          }
          for _, claimID := range conflict.ClaimIDs {
            if availability[claimID] != "available" {
              continue
            }
            switch conflict.Resolution.Decision {
            case "prefer":
              if _, selected := chosen[claimID]; selected {
                availability[claimID] = "available"
              } else {
                availability[claimID] = "excluded"
              }
            case "unresolved":
              availability[claimID] = "unresolved"
            }
          }
        }
        reviews := map[string]ClaimReview{}
        for _, review := range document.ClaimReviews {
          reviews[review.ClaimID] = review
        }
        freshnessChecks := map[string]FreshnessCheck{}
        for _, check := range document.FreshnessChecks {
          freshnessChecks[check.ClaimID] = check
        }
        for _, claim := range document.Claims {
          if _, duplicate := claims[claim.ID]; duplicate {
            issues = append(issues, finalManifestIssue("claim_id", claim.ID, "upstream claim IDs must be globally unique"))
          }
          claim.ReconciliationAvailability = availability[claim.ID]
          if review, exists := reviews[claim.ID]; exists {
            claim.EffectiveEvidenceStatus = review.EffectiveEvidenceStatus
            claim.FreshnessStatus = review.FreshnessStatus
            claim.FreshnessGap = review.Gap
          }
          if check, exists := freshnessChecks[claim.ID]; exists {
            checkCopy := check
            claim.FreshnessCheck = &checkCopy
          }
          claims[claim.ID] = claim
        }
      }
      return claims, sources, issues
    }

    func sourcesForReportClaim(claim ReportClaim, upstream map[string]UpstreamClaim, sources map[string]UpstreamSource) ([]ReportSource, []Issue) {
      result := make([]ReportSource, 0)
      seen := map[string]int{}
      issues := []Issue{}
      for _, supportingID := range claim.SupportingClaimIDs {
        upstreamClaim, exists := upstream[supportingID]
        if !exists {
          issues = append(issues, finalManifestIssue("unknown_evidence_claim", claim.ID, "supporting claim does not exist: "+supportingID))
          continue
        }
        if upstreamClaim.ReconciliationAvailability != "available" {
          issues = append(issues, finalManifestIssue("unavailable_evidence_claim", claim.ID, "supporting claim is "+upstreamClaim.ReconciliationAvailability+" after reconciliation: "+supportingID))
          continue
        }
        if len(upstreamClaim.Evidence) == 0 {
          issues = append(issues, finalManifestIssue("missing_claim_sources", claim.ID, "supporting claim has no source evidence: "+supportingID))
          continue
        }
        for index, edge := range upstreamClaim.Evidence {
          source, exists := sources[strings.TrimSpace(edge.SourceID)]
          if !exists {
            path := fmt.Sprintf("%s.%s.evidence[%d]", claim.ID, supportingID, index)
            sourceID := strings.TrimSpace(edge.SourceID)
            if sourceID == "" {
              sourceID = "<empty>"
            }
            issues = append(issues, finalManifestIssue("unknown_source_id", path, "source does not exist: "+sourceID))
            continue
          }
          if !validReportSourceURL(source.URL) {
            issues = append(issues, finalManifestIssue("invalid_source_url", source.ID, "source URL must be an absolute HTTP or HTTPS URL safe for Markdown"))
            continue
          }
          canonicalURL := strings.TrimSpace(source.CanonicalURL)
          if canonicalURL == "" {
            canonicalURL = strings.TrimSpace(source.URL)
          }
          if !validReportSourceURL(canonicalURL) {
            issues = append(issues, finalManifestIssue("invalid_canonical_url", source.ID, "canonical source URL must be an absolute HTTP or HTTPS URL safe for Markdown"))
            continue
          }
          key := canonicalURL + "\x00" + edge.Relation
          if existingIndex, duplicate := seen[key]; duplicate {
            item := &result[existingIndex]
            item.SourceRecordIDs = appendUniqueReportSourceID(item.SourceRecordIDs, source.ID)
            continue
          }
          seen[key] = len(result)
          result = append(result, ReportSource{
            SourceID: source.ID, SourceRecordIDs: []string{source.ID}, Relation: edge.Relation, Title: strings.TrimSpace(source.Title),
            Publisher: strings.TrimSpace(source.Publisher), PublicationDate: strings.TrimSpace(source.PublicationDate),
            URL: canonicalURL, RetrievalURL: strings.TrimSpace(source.URL), OriginID: strings.TrimSpace(source.OriginID),
          })
        }
      }
      return result, issues
    }

    func reportUpstreamClaims(claim ReportClaim, upstream map[string]UpstreamClaim, sources map[string]UpstreamSource) []UpstreamClaim {
      result := make([]UpstreamClaim, 0, len(claim.SupportingClaimIDs))
      for _, claimID := range claim.SupportingClaimIDs {
        item, exists := upstream[claimID]
        if !exists {
          continue
        }
        evidence := make([]UpstreamEvidence, 0, len(item.Evidence))
        for _, edge := range item.Evidence {
          if source, sourceExists := sources[edge.SourceID]; sourceExists {
            sourceCopy := source
            edge.Source = &sourceCopy
          }
          evidence = append(evidence, edge)
        }
        item.Evidence = evidence
        result = append(result, item)
      }
      return result
    }

    func validReportSourceURL(raw string) bool {
      value := strings.TrimSpace(raw)
      if value == "" || strings.ContainsAny(value, "<>\t\r\n ") {
        return false
      }
      parsed, err := url.Parse(value)
      return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
    }

    func withClaimSources(report string, claims []ReportClaim) string {
      withoutSources := strings.TrimSpace(existingClaimSources.ReplaceAllString(report, ""))
      var builder strings.Builder
      builder.WriteString(withoutSources)
      builder.WriteString("\n\n")
      builder.WriteString(claimSourcesStart)
      builder.WriteString("\n## Claim sources\n\n")
      for _, claim := range claims {
        fmt.Fprintf(
          &builder,
          "[^%s]: **Evidence:** `%s`; **Dispute:** `%s`; **Basis:** `%s`.\n",
          claim.ID,
          claim.EvidenceStatus,
          claim.DisputeStatus,
          claim.ConfirmationBasis,
        )
        fmt.Fprintf(&builder, "    - **Freshness:** `%s`.\n", claim.FreshnessStatus)
        relations := []string{"supports", "contradicts"}
        for _, relation := range relations {
          for _, source := range claim.Sources {
            if source.Relation != relation {
              continue
            }
          label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(source.Title)
          if label == "" {
            label = source.SourceID
          }
          metadata := make([]string, 0, 2)
          if source.Publisher != "" {
            metadata = append(metadata, source.Publisher)
          }
          if source.PublicationDate != "" {
            metadata = append(metadata, source.PublicationDate)
          }
          suffix := ""
          if len(metadata) > 0 {
            suffix = " - " + strings.Join(metadata, ", ")
          }
            fmt.Fprintf(&builder, "    - `%s` [%s](<%s>)%s\n", source.Relation, label, source.URL, suffix)
          }
        }
        builder.WriteString("\n")
      }
      builder.WriteString(claimSourcesEnd)
      builder.WriteString("\n")
      return builder.String()
    }

    func finalManifestAdjacent(report, statement, claimID string) bool {
      marker := "[^" + claimID + "]"
      report = strings.ReplaceAll(report, marker, " "+marker)
      needle := finalManifestNormalized(statement) + " " + marker
      return strings.Count(finalManifestNormalized(report), needle) == 1
    }

    func finalManifestNormalized(value string) string {
      fields := strings.FieldsFunc(value, func(character rune) bool { return unicode.IsSpace(character) })
      return strings.Join(fields, " ")
    }

    func appendUniqueReportSourceID(values []string, value string) []string {
      for _, existing := range values {
        if existing == value {
          return values
        }
      }
      return append(values, value)
    }

    func reportClaimStatus(claim ReportClaim, upstream map[string]UpstreamClaim) (string, string, string) {
      if claim.ClaimKind == "inference" {
        return "inferred", reportDisputeStatus(claim, upstream), "none"
      }
      order := map[string]int{"unknown": 0, "inferred": 1, "reported": 2, "confirmed": 3}
      evidenceStatus := "confirmed"
      confirmationBasis := ""
      mixedBasis := false
      for _, id := range claim.SupportingClaimIDs {
        item := upstream[id]
        status := item.EffectiveEvidenceStatus
        if status == "" {
          status = item.EvidenceStatus
        }
        if status == "" {
          status = item.Status
          if status == "disputed" || status == "contradicted" {
            status = "unknown"
          }
        }
        if _, exists := order[status]; !exists {
          status = "unknown"
        }
        if order[status] < order[evidenceStatus] {
          evidenceStatus = status
        }
        basis := item.ConfirmationBasis
        if basis == "" {
          basis = "none"
        }
        if confirmationBasis == "" {
          confirmationBasis = basis
        } else if confirmationBasis != basis {
          mixedBasis = true
        }
      }
      if confirmationBasis == "" || evidenceStatus != "confirmed" {
        confirmationBasis = "none"
      } else if mixedBasis {
        confirmationBasis = "mixed"
      }
      return evidenceStatus, reportDisputeStatus(claim, upstream), confirmationBasis
    }

    func reportClaimFreshness(claim ReportClaim, upstream map[string]UpstreamClaim) (string, []string) {
      status := "not_required"
      gaps := make([]string, 0)
      seenGaps := map[string]struct{}{}
      for _, id := range claim.SupportingClaimIDs {
        item := upstream[id]
        if item.FreshnessStatus == "" {
          continue
        }
        if status == "not_required" {
          status = "current"
        }
        if item.FreshnessStatus != "verified_primary" && item.FreshnessStatus != "checked_no_primary" {
          status = "pending"
        }
        gap := strings.TrimSpace(item.FreshnessGap)
        if gap != "" {
          if _, duplicate := seenGaps[gap]; !duplicate {
            seenGaps[gap] = struct{}{}
            gaps = append(gaps, gap)
          }
        }
      }
      return status, gaps
    }

    func reportDisputeStatus(claim ReportClaim, upstream map[string]UpstreamClaim) string {
      result := "clean"
      for _, id := range claim.SupportingClaimIDs {
        status := upstream[id].DisputeStatus
        if status == "" && (upstream[id].Status == "disputed" || upstream[id].Status == "contradicted") {
          status = "disputed"
        }
        if status == "disputed" {
          return "disputed"
        }
        if status == "challenged" {
          result = "challenged"
        }
      }
      return result
    }

    func finalManifestPath(raw, workspace string) (string, []Issue) {
      if !filepath.IsAbs(raw) {
        return "", []Issue{finalManifestIssue("invalid_path", "manifest_path", "manifest_path must end in report-manifest.json under this block workspace")}
      }
      path, err := filepath.Abs(filepath.Clean(raw))
      if err != nil || filepath.Base(path) != "report-manifest.json" || !finalManifestWithin(path, workspace) {
        return "", []Issue{finalManifestIssue("invalid_path", "manifest_path", "manifest_path must end in report-manifest.json under this block workspace")}
      }
      return path, nil
    }

    func finalManifestBlocksRoot(workspace string) string {
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

    func finalManifestWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func finalManifestFileExists(path string) bool {
      info, err := os.Stat(path)
      return err == nil && !info.IsDir()
    }

    func readFinalManifestJSON(path string, value any) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), value)
    }

    func writeFinalManifestJSON(path string, value any) error {
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

    func finalManifestIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "read_report_claim_evidence" {
  description = "Read from one through ten finalized report claims by ID for semantic QC. Returns only the selected atomic statements and their upstream claim semantics, evidence edges and exact quotes, source classification and independence metadata, reconciliation availability, freshness state, and canonical URLs. It never returns unrelated claims and never modifies an artifact."

  source = <<-GO
    import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Input struct {
      ReportManifestPath string   `json:"report_manifest_path"`
      ClaimIDs           []string `json:"claim_ids"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      workspace, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      manifestPath, issues := reportEvidenceManifestPath(input.ReportManifestPath, workspace)
      if len(input.ClaimIDs) == 0 {
        issues = append(issues, reportEvidenceIssue("claim_ids", "claim_ids", "provide from one through ten report claim IDs"))
      }
      if len(input.ClaimIDs) > 10 {
        issues = append(issues, reportEvidenceIssue("batch_size", "claim_ids", "request at most ten report claims per call"))
      }
      requested := make([]string, 0, len(input.ClaimIDs))
      requestedIndexes := map[string]int{}
      for index, raw := range input.ClaimIDs {
        claimID := strings.TrimSpace(raw)
        path := fmt.Sprintf("claim_ids[%d]", index)
        if claimID == "" {
          issues = append(issues, reportEvidenceIssue("claim_id", path, "claim ID must not be empty"))
          continue
        }
        if _, duplicate := requestedIndexes[claimID]; duplicate {
          issues = append(issues, reportEvidenceIssue("claim_id", path, "claim IDs must be unique in one request"))
          continue
        }
        requestedIndexes[claimID] = index
        requested = append(requested, claimID)
      }
      if manifestPath == "" || len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := os.ReadFile(manifestPath)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read report manifest: %w", err)
      }
      var manifest struct {
        Claims []map[string]any `json:"claims"`
      }
      if err = json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), &manifest); err != nil {
        return ToolResponse[Output]{Accepted: false, Issues: []Issue{
          reportEvidenceIssue("report_manifest", "report_manifest_path", "must contain a valid finalized report manifest"),
        }}, nil
      }
      byID := make(map[string]map[string]any, len(manifest.Claims))
      for _, claim := range manifest.Claims {
        if claimID, ok := claim["id"].(string); ok && strings.TrimSpace(claimID) != "" {
          byID[claimID] = claim
        }
      }
      selected := make([]map[string]any, 0, len(requested))
      for _, claimID := range requested {
        claim, exists := byID[claimID]
        if !exists {
          index := requestedIndexes[claimID]
          path := fmt.Sprintf("claim_ids[%d]", index)
          issues = append(issues, reportEvidenceIssue("claim_id", path, "claim does not exist in the finalized report manifest: "+claimID))
          continue
        }
        selected = append(selected, claim)
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      encoded, err := json.MarshalIndent(map[string]any{"claims": selected}, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode report claim evidence: %w", err)
      }
      output := Output(encoded)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func reportEvidenceManifestPath(raw, workspace string) (string, []Issue) {
      path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      root := reportEvidenceBlocksRoot(workspace)
      if err != nil || !filepath.IsAbs(raw) || root == "" || filepath.Base(path) != "report-manifest.json" || !reportEvidenceWithin(path, root) {
        return "", []Issue{reportEvidenceIssue("report_manifest_path", "report_manifest_path", "must name report-manifest.json in the current run")}
      }
      info, statErr := os.Stat(path)
      if statErr != nil || info.IsDir() {
        return "", []Issue{reportEvidenceIssue("report_manifest_path", "report_manifest_path", "must name a readable finalized report manifest")}
      }
      return path, nil
    }

    func reportEvidenceBlocksRoot(workspace string) string {
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

    func reportEvidenceWithin(path, root string) bool {
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func reportEvidenceIssue(code, path, message string) Issue {
      repair := "Correct every listed report-evidence request field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}
