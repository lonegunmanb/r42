go_tool "submit_track_evidence" {
  description = "Validate one graph-research track, write track-evidence.json, and return its JSON as the research result."

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
      URL          string `json:"url"`
      SnapshotPath string `json:"snapshot_path"`
      Locator      string `json:"locator"`
      ExactQuote   string `json:"exact_quote"`
    }

    type Finding struct {
      ID         string   `json:"id"`
      Claim      string   `json:"claim"`
      Relevance  string   `json:"relevance"`
      Confidence string   `json:"confidence"`
      QuoteIDs   []string `json:"quote_ids"`
    }

    type Input struct {
      ArtifactPath string    `json:"artifact_path"`
      Track        string    `json:"track"`
      Findings     []Finding `json:"findings"`
      Quotes       []Quote   `json:"quotes"`
      Unknowns     []string  `json:"unknowns"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateTrack(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode track evidence: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write track evidence: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateTrack(input Input) []Issue {
      issues := []Issue{}
      if !validArtifactPath(input.ArtifactPath, "track-evidence.json") {
        issues = append(issues, issue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in track-evidence.json"))
      }
      track := strings.TrimSpace(input.Track)
      if track == "" {
        issues = append(issues, issue("track", "track", "must not be empty"))
      }
      if len(input.Findings) == 0 {
        issues = append(issues, issue("findings", "findings", "submit at least one atomic finding"))
      }
      if len(input.Quotes) == 0 {
        issues = append(issues, issue("quotes", "quotes", "submit at least one fetched exact quote"))
      }

      quoteIDs := map[string]struct{}{}
      used := map[string]struct{}{}
      for index, quote := range input.Quotes {
        path := fmt.Sprintf("quotes[%d]", index)
        id := strings.TrimSpace(quote.ID)
        if id == "" || !strings.HasPrefix(id, track+"-") {
          issues = append(issues, issue("quote_id", path+".id", "must be unique and begin with the track plus a hyphen"))
        } else if _, exists := quoteIDs[id]; exists {
          issues = append(issues, issue("quote_id", path+".id", "must be unique"))
        } else {
          quoteIDs[id] = struct{}{}
        }
        if !validURL(quote.URL) {
          issues = append(issues, issue("url", path+".url", "must be an absolute HTTP or HTTPS URL"))
        }
        if !existingSnapshot(quote.SnapshotPath) {
          issues = append(issues, issue("snapshot_path", path+".snapshot_path", "must name an existing absolute Markdown snapshot"))
        }
        if strings.TrimSpace(quote.Locator) == "" || strings.TrimSpace(quote.ExactQuote) == "" {
          issues = append(issues, issue("quote", path, "locator and exact_quote must not be empty"))
        }
      }

      findingIDs := map[string]struct{}{}
      for index, finding := range input.Findings {
        path := fmt.Sprintf("findings[%d]", index)
        id := strings.TrimSpace(finding.ID)
        if id == "" || !strings.HasPrefix(id, track+"-") {
          issues = append(issues, issue("finding_id", path+".id", "must be unique and begin with the track plus a hyphen"))
        } else if _, exists := findingIDs[id]; exists {
          issues = append(issues, issue("finding_id", path+".id", "must be unique"))
        } else {
          findingIDs[id] = struct{}{}
        }
        if strings.TrimSpace(finding.Claim) == "" || strings.TrimSpace(finding.Relevance) == "" {
          issues = append(issues, issue("finding", path, "claim and relevance must not be empty"))
        }
        switch strings.ToLower(strings.TrimSpace(finding.Confidence)) {
        case "high", "medium", "low":
        default:
          issues = append(issues, issue("confidence", path+".confidence", "must be high, medium, or low"))
        }
        if len(finding.QuoteIDs) == 0 {
          issues = append(issues, issue("quote_ids", path+".quote_ids", "reference at least one quote"))
        }
        for quoteIndex, quoteID := range finding.QuoteIDs {
          if _, exists := quoteIDs[quoteID]; !exists {
            issues = append(issues, issue("quote_reference", fmt.Sprintf("%s.quote_ids[%d]", path, quoteIndex), "must reference a declared quote"))
          } else {
            used[quoteID] = struct{}{}
          }
        }
      }
      for quoteID := range quoteIDs {
        if _, exists := used[quoteID]; !exists {
          issues = append(issues, issue("unused_quote", "quotes", "quote "+quoteID+" is not referenced by a finding"))
        }
      }
      return issues
    }

    func validURL(raw string) bool {
      parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
      return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
    }

    func existingSnapshot(raw string) bool {
      path := filepath.Clean(strings.TrimSpace(raw))
      info, err := os.Stat(path)
      return filepath.IsAbs(path) && err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(path), ".md")
    }

    func validArtifactPath(raw, name string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+name)
    }

    func issue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_chokepoint_chain" {
  description = "Validate a bounded supply-chain graph and selected chokepoints, write chokepoints.json, and return its JSON."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Node struct {
      ID           string `json:"id"`
      Name         string `json:"name"`
      Type         string `json:"type"`
      Function     string `json:"function"`
      Terminal     bool   `json:"terminal"`
      StopReason   string `json:"stop_reason"`
    }

    type Edge struct {
      SupplierNodeID string `json:"supplier_node_id"`
      ConsumerNodeID string `json:"consumer_node_id"`
      Relation       string `json:"relation"`
    }

    type Chokepoint struct {
      NodeID                string   `json:"node_id"`
      Rank                  int      `json:"rank"`
      Mechanisms            []string `json:"mechanisms"`
      WhySelected           string   `json:"why_selected"`
      SubstitutionDifficulty string   `json:"substitution_difficulty"`
      TimeToRecover         string   `json:"time_to_recover"`
      EvidenceFindingIDs    []string `json:"evidence_finding_ids"`
    }

    type Input struct {
      ArtifactPath      string       `json:"artifact_path"`
      Topic             string       `json:"topic"`
      FocalNodeID       string       `json:"focal_node_id"`
      ReviewedArtifacts []string     `json:"reviewed_artifacts"`
      Nodes             []Node       `json:"nodes"`
      Edges             []Edge       `json:"edges"`
      Chokepoints       []Chokepoint `json:"chokepoints"`
      Unresolved        []string     `json:"unresolved"`
      Conclusion        string       `json:"conclusion"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateChain(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode chokepoint chain: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write chokepoint chain: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateChain(input Input) []Issue {
      issues := []Issue{}
      if !chainArtifactPath(input.ArtifactPath) {
        issues = append(issues, chainIssue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in chokepoints.json"))
      }
      if strings.TrimSpace(input.Topic) == "" || strings.TrimSpace(input.Conclusion) == "" {
        issues = append(issues, chainIssue("summary", "topic", "topic and conclusion must not be empty"))
      }
      if len(input.ReviewedArtifacts) != 5 {
        issues = append(issues, chainIssue("reviewed_artifacts", "reviewed_artifacts", "must contain the five validated track artifacts"))
      }
      for index, raw := range input.ReviewedArtifacts {
        path := filepath.Clean(strings.TrimSpace(raw))
        info, err := os.Stat(path)
        if !filepath.IsAbs(path) || err != nil || info.IsDir() || filepath.Base(path) != "track-evidence.json" {
          issues = append(issues, chainIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "must name an existing absolute track-evidence.json"))
        }
      }
      if len(input.Nodes) == 0 {
        issues = append(issues, chainIssue("nodes", "nodes", "submit at least the focal node"))
      }
      nodeIDs := map[string]struct{}{}
      for index, node := range input.Nodes {
        path := fmt.Sprintf("nodes[%d]", index)
        id := strings.TrimSpace(node.ID)
        if id == "" {
          issues = append(issues, chainIssue("node_id", path+".id", "must not be empty"))
        } else if _, exists := nodeIDs[id]; exists {
          issues = append(issues, chainIssue("node_id", path+".id", "must be unique"))
        } else {
          nodeIDs[id] = struct{}{}
        }
        if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Type) == "" || strings.TrimSpace(node.Function) == "" {
          issues = append(issues, chainIssue("node", path, "name, type, and function must not be empty"))
        }
        if node.Terminal && strings.TrimSpace(node.StopReason) == "" {
          issues = append(issues, chainIssue("stop_reason", path+".stop_reason", "terminal nodes require a stop reason"))
        }
      }
      if _, exists := nodeIDs[strings.TrimSpace(input.FocalNodeID)]; !exists {
        issues = append(issues, chainIssue("focal_node_id", "focal_node_id", "must reference a declared node"))
      }
      for index, edge := range input.Edges {
        path := fmt.Sprintf("edges[%d]", index)
        if _, exists := nodeIDs[edge.SupplierNodeID]; !exists {
          issues = append(issues, chainIssue("supplier_node_id", path+".supplier_node_id", "must reference a declared node"))
        }
        if _, exists := nodeIDs[edge.ConsumerNodeID]; !exists {
          issues = append(issues, chainIssue("consumer_node_id", path+".consumer_node_id", "must reference a declared node"))
        }
        if strings.TrimSpace(edge.Relation) == "" {
          issues = append(issues, chainIssue("relation", path+".relation", "must not be empty"))
        }
      }
      ranks := map[int]struct{}{}
      for index, item := range input.Chokepoints {
        path := fmt.Sprintf("chokepoints[%d]", index)
        if _, exists := nodeIDs[item.NodeID]; !exists {
          issues = append(issues, chainIssue("node_id", path+".node_id", "must reference a declared node"))
        }
        if item.Rank <= 0 {
          issues = append(issues, chainIssue("rank", path+".rank", "must be positive"))
        } else if _, exists := ranks[item.Rank]; exists {
          issues = append(issues, chainIssue("rank", path+".rank", "must be unique"))
        } else {
          ranks[item.Rank] = struct{}{}
        }
        if len(item.Mechanisms) == 0 || len(item.EvidenceFindingIDs) == 0 ||
          strings.TrimSpace(item.WhySelected) == "" || strings.TrimSpace(item.SubstitutionDifficulty) == "" || strings.TrimSpace(item.TimeToRecover) == "" {
          issues = append(issues, chainIssue("chokepoint", path, "mechanisms, selection rationale, substitution difficulty, recovery time, and evidence finding IDs are required"))
        }
      }
      return issues
    }

    func chainArtifactPath(raw string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/chokepoints.json")
    }

    func chainIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_candidates" {
  description = "Validate company hypotheses for exactly one chokepoint, write candidates.json, and return its JSON."

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

    type Evidence struct {
      Claim        string `json:"claim"`
      URL          string `json:"url"`
      SnapshotPath string `json:"snapshot_path"`
      Locator      string `json:"locator"`
      ExactQuote   string `json:"exact_quote"`
    }

    type Candidate struct {
      Name            string     `json:"name"`
      Ticker          string     `json:"ticker"`
      Market          string     `json:"market"`
      NodeID          string     `json:"node_id"`
      Relationship    string     `json:"relationship"`
      SelectionReason string     `json:"selection_reason"`
      Evidence        []Evidence `json:"evidence"`
    }

    type Input struct {
      ArtifactPath string      `json:"artifact_path"`
      NodeID       string      `json:"node_id"`
      MaxCandidates int        `json:"max_candidates"`
      Candidates   []Candidate `json:"candidates"`
      Conclusion   string      `json:"conclusion"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateCandidates(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode candidates: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.MkdirAll(filepath.Dir(filepath.Clean(input.ArtifactPath)), 0755); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create candidate artifact directory: %w", err)
      }
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write candidates: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateCandidates(input Input) []Issue {
      issues := []Issue{}
      if !nestedArtifactPath(input.ArtifactPath, "candidates.json") {
        issues = append(issues, candidateIssue("artifact_path", "artifact_path", "must be an absolute dynamic block_wd() child path ending in candidates.json"))
      }
      nodeID := strings.TrimSpace(input.NodeID)
      if nodeID == "" {
        issues = append(issues, candidateIssue("node_id", "node_id", "must not be empty"))
      }
      if input.MaxCandidates < 1 || input.MaxCandidates > 5 {
        issues = append(issues, candidateIssue("max_candidates", "max_candidates", "must be from 1 through 5"))
      }
      if len(input.Candidates) > input.MaxCandidates {
        issues = append(issues, candidateIssue("candidates", "candidates", "must not exceed max_candidates"))
      }
      if strings.TrimSpace(input.Conclusion) == "" {
        issues = append(issues, candidateIssue("conclusion", "conclusion", "must explain the result, including an empty candidate list"))
      }
      identities := map[string]struct{}{}
      for index, candidate := range input.Candidates {
        path := fmt.Sprintf("candidates[%d]", index)
        if strings.TrimSpace(candidate.Name) == "" && strings.TrimSpace(candidate.Ticker) == "" {
          issues = append(issues, candidateIssue("identity", path, "name or ticker is required"))
        }
        identity := strings.ToLower(strings.TrimSpace(candidate.Market)+":"+strings.TrimSpace(candidate.Ticker)+":"+strings.TrimSpace(candidate.Name))
        if _, exists := identities[identity]; exists {
          issues = append(issues, candidateIssue("identity", path, "candidate identity must be unique"))
        } else {
          identities[identity] = struct{}{}
        }
        if strings.TrimSpace(candidate.NodeID) != nodeID {
          issues = append(issues, candidateIssue("node_id", path+".node_id", "must equal the assigned node_id exactly"))
        }
        switch strings.ToLower(strings.TrimSpace(candidate.Relationship)) {
        case "controls_bottleneck", "critical_supplier", "unverified":
        default:
          issues = append(issues, candidateIssue("relationship", path+".relationship", "must be controls_bottleneck, critical_supplier, or unverified"))
        }
        if strings.TrimSpace(candidate.Market) == "" || strings.TrimSpace(candidate.SelectionReason) == "" || len(candidate.Evidence) == 0 {
          issues = append(issues, candidateIssue("candidate", path, "market, selection_reason, and evidence are required"))
        }
        for evidenceIndex, evidence := range candidate.Evidence {
          evidencePath := fmt.Sprintf("%s.evidence[%d]", path, evidenceIndex)
          if strings.TrimSpace(evidence.Claim) == "" || strings.TrimSpace(evidence.Locator) == "" || strings.TrimSpace(evidence.ExactQuote) == "" {
            issues = append(issues, candidateIssue("evidence", evidencePath, "claim, locator, and exact_quote are required"))
          }
          if !candidateURL(evidence.URL) || !candidateSnapshot(evidence.SnapshotPath) {
            issues = append(issues, candidateIssue("evidence_source", evidencePath, "URL and existing absolute Markdown snapshot_path are required"))
          }
        }
      }
      return issues
    }

    func candidateURL(raw string) bool {
      parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
      return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
    }

    func candidateSnapshot(raw string) bool {
      path := filepath.Clean(strings.TrimSpace(raw))
      info, err := os.Stat(path)
      return filepath.IsAbs(path) && err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(path), ".md")
    }

    func nestedArtifactPath(raw, name string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/"+name)
    }

    func candidateIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_candidate_scorecard" {
  description = "Validate one candidate's chain relationship and eight-factor scorecard, write scorecard.json, and return its JSON."

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

    type Candidate struct {
      Name         string `json:"name"`
      Ticker       string `json:"ticker"`
      Market       string `json:"market"`
      NodeID       string `json:"node_id"`
      Relationship string `json:"relationship"`
    }

    type Factors struct {
      DemandInflection        int `json:"demand_inflection"`
      ArchitectureCoupling    int `json:"architecture_coupling"`
      ChokepointSeverity      int `json:"chokepoint_severity"`
      SupplierConcentration   int `json:"supplier_concentration"`
      ExpansionDifficulty     int `json:"expansion_difficulty"`
      EvidenceQuality         int `json:"evidence_quality"`
      CatalystTiming          int `json:"catalyst_timing"`
      SubstitutionResilience  int `json:"substitution_resilience"`
    }

    type Evidence struct {
      Claim        string `json:"claim"`
      URL          string `json:"url"`
      SnapshotPath string `json:"snapshot_path"`
      Locator      string `json:"locator"`
      ExactQuote   string `json:"exact_quote"`
    }

    type Input struct {
      ArtifactPath       string    `json:"artifact_path"`
      Candidate          Candidate `json:"candidate"`
      ControlMechanism   string    `json:"control_mechanism"`
      Factors            Factors   `json:"factors"`
      Evidence           []Evidence `json:"evidence"`
      PeerAlternatives   []string  `json:"peer_alternatives"`
      Falsification      []string  `json:"falsification"`
      WhatCouldWeakenView []string `json:"what_could_weaken_view"`
      Conclusion         string    `json:"conclusion"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateScorecard(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(input, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode scorecard: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.MkdirAll(filepath.Dir(filepath.Clean(input.ArtifactPath)), 0755); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create scorecard artifact directory: %w", err)
      }
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write scorecard: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateScorecard(input Input) []Issue {
      issues := []Issue{}
      if !scorecardArtifactPath(input.ArtifactPath) {
        issues = append(issues, scorecardIssue("artifact_path", "artifact_path", "must be an absolute dynamic block_wd() child path ending in scorecard.json"))
      }
      if strings.TrimSpace(input.Candidate.Name) == "" && strings.TrimSpace(input.Candidate.Ticker) == "" {
        issues = append(issues, scorecardIssue("identity", "candidate", "candidate name or ticker is required"))
      }
      if strings.TrimSpace(input.Candidate.NodeID) == "" || strings.TrimSpace(input.Candidate.Market) == "" {
        issues = append(issues, scorecardIssue("candidate", "candidate", "node_id and market are required"))
      }
      switch strings.ToLower(strings.TrimSpace(input.Candidate.Relationship)) {
      case "controls_bottleneck", "critical_supplier", "unverified":
      default:
        issues = append(issues, scorecardIssue("relationship", "candidate.relationship", "must be controls_bottleneck, critical_supplier, or unverified"))
      }
      values := []int{
        input.Factors.DemandInflection, input.Factors.ArchitectureCoupling,
        input.Factors.ChokepointSeverity, input.Factors.SupplierConcentration,
        input.Factors.ExpansionDifficulty, input.Factors.EvidenceQuality,
        input.Factors.CatalystTiming, input.Factors.SubstitutionResilience,
      }
      for index, value := range values {
        if value < 0 || value > 5 {
          issues = append(issues, scorecardIssue("factor", fmt.Sprintf("factors[%d]", index), "every factor must be from 0 through 5"))
        }
      }
      if len(input.Evidence) == 0 || strings.TrimSpace(input.Conclusion) == "" {
        issues = append(issues, scorecardIssue("assessment", "evidence", "evidence and conclusion are required"))
      }
      if len(input.Falsification) == 0 || len(input.WhatCouldWeakenView) == 0 {
        issues = append(issues, scorecardIssue("risk", "falsification", "falsification and what_could_weaken_view must not be empty"))
      }
      if input.Candidate.Relationship != "unverified" && strings.TrimSpace(input.ControlMechanism) == "" {
        issues = append(issues, scorecardIssue("control_mechanism", "control_mechanism", "verified relationships require a control mechanism"))
      }
      for index, evidence := range input.Evidence {
        path := fmt.Sprintf("evidence[%d]", index)
        parsed, err := url.ParseRequestURI(strings.TrimSpace(evidence.URL))
        validURL := err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
        snapshot := filepath.Clean(strings.TrimSpace(evidence.SnapshotPath))
        info, statErr := os.Stat(snapshot)
        if strings.TrimSpace(evidence.Claim) == "" || strings.TrimSpace(evidence.Locator) == "" || strings.TrimSpace(evidence.ExactQuote) == "" ||
          !validURL || !filepath.IsAbs(snapshot) || statErr != nil || info.IsDir() {
          issues = append(issues, scorecardIssue("evidence", path, "claim, URL, existing snapshot_path, locator, and exact_quote are required"))
        }
      }
      return issues
    }

    func scorecardArtifactPath(raw string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/scorecard.json")
    }

    func scorecardIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}
