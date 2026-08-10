go_tool "submit_supply_chain_scope" {
  description = "Validate the declared product boundary and coverage inventory, write scope.json, and return its JSON."

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

go_tool "stage_supply_chain" {
  description = "Stage one small, replaceable supply-chain batch. Use section metadata, nodes, edges, coverage, or chokepoints; keep node batches at most 10 items and edge batches at most 15 items."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "regexp"
      "strings"
    )

    type Node struct {
      ID                 string   `json:"id"`
      Name               string   `json:"name"`
      Type               string   `json:"type"`
      Function           string   `json:"function"`
      Stages             []string `json:"stages"`
      Status             string   `json:"status"`
      EvidenceClaimIDs   []string `json:"evidence_claim_ids,omitempty"`
      UnknownReason      string   `json:"unknown_reason,omitempty"`
      Terminal           bool     `json:"terminal"`
      StopReason         string   `json:"stop_reason,omitempty"`
    }

    type Edge struct {
      SupplierNodeID     string   `json:"supplier_node_id"`
      ConsumerNodeID     string   `json:"consumer_node_id"`
      Relation           string   `json:"relation"`
      Status             string   `json:"status"`
      EvidenceClaimIDs   []string `json:"evidence_claim_ids,omitempty"`
      UnknownReason      string   `json:"unknown_reason,omitempty"`
    }

    type Coverage struct {
      ID                 string   `json:"id"`
      Status             string   `json:"status"`
      NodeIDs            []string `json:"node_ids,omitempty"`
      EvidenceClaimIDs   []string `json:"evidence_claim_ids,omitempty"`
      Explanation        string   `json:"explanation"`
      ResearchAttempt    string   `json:"research_attempt,omitempty"`
      Impact             string   `json:"impact,omitempty"`
    }

    type Chokepoint struct {
      NodeID                 string   `json:"node_id"`
      Mechanisms             []string `json:"mechanisms"`
      WhySelected            string   `json:"why_selected"`
      DeliveryImpact         string   `json:"delivery_impact"`
      Substitutability       string   `json:"substitutability"`
      SupplierConcentration  string   `json:"supplier_concentration"`
      SwitchingTimeMinDays   int      `json:"switching_time_min_days"`
      SwitchingTimeMaxDays   int      `json:"switching_time_max_days"`
      RecoveryTimeMinDays    int      `json:"recovery_time_min_days"`
      RecoveryTimeMaxDays    int      `json:"recovery_time_max_days"`
      EvidenceClaimIDs       []string `json:"evidence_claim_ids"`
    }

    type Metadata struct {
      ScopeArtifact     string   `json:"scope_artifact"`
      ReconciledArtifact string  `json:"reconciled_artifact"`
      Topic             string   `json:"topic"`
      FocalNodeID       string   `json:"focal_node_id"`
      ReviewedArtifacts []string `json:"reviewed_artifacts"`
      Conclusion        string   `json:"conclusion"`
    }

    type Input struct {
      Section      string        `json:"section"`
      BatchID      string        `json:"batch_id"`
      Metadata     *Metadata     `json:"metadata,omitempty"`
      Nodes        []Node        `json:"nodes,omitempty"`
      Edges        []Edge        `json:"edges,omitempty"`
      Coverage     []Coverage    `json:"coverage,omitempty"`
      Chokepoints  []Chokepoint  `json:"chokepoints,omitempty"`
    }

    type Output string

    var batchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateBatch(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := batchPayload(input)
      if err != nil {
        return ToolResponse[Output]{}, err
      }
      directory := filepath.Join(".supply-chain-draft", input.Section)
      if err := os.MkdirAll(directory, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create supply-chain draft directory: %w", err)
      }
      path := filepath.Join(directory, input.BatchID+".json")
      if err := os.WriteFile(path, payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write supply-chain draft batch: %w", err)
      }
      summary, err := json.Marshal(map[string]any{
        "section": input.Section, "batch_id": input.BatchID, "count": batchCount(input),
      })
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode supply-chain batch summary: %w", err)
      }
      output := Output(summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateBatch(input Input) []Issue {
      issues := []Issue{}
      if !batchIDPattern.MatchString(input.BatchID) {
        issues = append(issues, stageIssue("batch_id", "batch_id", "must be 1-64 letters, digits, dots, underscores, or hyphens and begin with a letter or digit"))
      }
      payloads := 0
      if input.Metadata != nil { payloads++ }
      if input.Nodes != nil { payloads++ }
      if input.Edges != nil { payloads++ }
      if input.Coverage != nil { payloads++ }
      if input.Chokepoints != nil { payloads++ }
      if payloads != 1 {
        issues = append(issues, stageIssue("section_payload", "section", "provide exactly one payload matching section"))
        return issues
      }
      switch input.Section {
      case "metadata":
        if input.BatchID != "main" {
          issues = append(issues, stageIssue("batch_id", "batch_id", "metadata must use batch_id main"))
        }
        if input.Metadata == nil {
          issues = append(issues, stageIssue("section_payload", "metadata", "metadata section requires metadata"))
        } else {
          issues = append(issues, validateMetadata(*input.Metadata)...)
        }
      case "nodes":
        if input.Nodes == nil {
          issues = append(issues, stageIssue("section_payload", "nodes", "nodes section requires nodes"))
        } else {
          issues = append(issues, validateNodeBatch(input.Nodes)...)
        }
      case "edges":
        if input.Edges == nil {
          issues = append(issues, stageIssue("section_payload", "edges", "edges section requires edges"))
        } else {
          issues = append(issues, validateEdgeBatch(input.Edges)...)
        }
      case "coverage":
        if input.Coverage == nil {
          issues = append(issues, stageIssue("section_payload", "coverage", "coverage section requires coverage"))
        } else {
          issues = append(issues, validateCoverageBatch(input.Coverage)...)
        }
      case "chokepoints":
        if input.Chokepoints == nil {
          issues = append(issues, stageIssue("section_payload", "chokepoints", "chokepoints section requires chokepoints; use an empty list when none exist"))
        } else {
          issues = append(issues, validateChokepointBatch(input.Chokepoints)...)
        }
      default:
        issues = append(issues, stageIssue("section", "section", "must be metadata, nodes, edges, coverage, or chokepoints"))
      }
      return issues
    }

    func validateMetadata(value Metadata) []Issue {
      issues := []Issue{}
      if strings.TrimSpace(value.Topic) == "" || strings.TrimSpace(value.FocalNodeID) == "" || strings.TrimSpace(value.Conclusion) == "" {
        issues = append(issues, stageIssue("metadata", "metadata", "topic, focal_node_id, and conclusion must not be empty"))
      }
      if !absoluteNamedFile(value.ScopeArtifact, "scope.json") {
        issues = append(issues, stageIssue("scope_artifact", "metadata.scope_artifact", "must name an existing absolute scope.json"))
      }
      if !absoluteNamedFile(value.ReconciledArtifact, "evidence-resolution.json") {
        issues = append(issues, stageIssue("reconciled_artifact", "metadata.reconciled_artifact", "must name an existing absolute evidence-resolution.json"))
      }
      if len(value.ReviewedArtifacts) != 5 {
        issues = append(issues, stageIssue("reviewed_artifacts", "metadata.reviewed_artifacts", "must contain the five validated track artifacts"))
      }
      for index, path := range value.ReviewedArtifacts {
        if !absoluteNamedFile(path, "evidence-ledger.json") {
          issues = append(issues, stageIssue("reviewed_artifact", fmt.Sprintf("metadata.reviewed_artifacts[%d]", index), "must name an existing absolute evidence-ledger.json"))
        }
      }
      return issues
    }

    func validateNodeBatch(values []Node) []Issue {
      issues := []Issue{}
      if len(values) == 0 || len(values) > 10 {
        issues = append(issues, stageIssue("batch_size", "nodes", "must contain from 1 through 10 nodes"))
      }
      ids := map[string]struct{}{}
      types := map[string]struct{}{
        "product": {}, "component": {}, "material": {}, "process": {},
        "equipment": {}, "qualification": {}, "service": {}, "system": {},
      }
      for index, node := range values {
        path := fmt.Sprintf("nodes[%d]", index)
        id := strings.TrimSpace(node.ID)
        if id == "" {
          issues = append(issues, stageIssue("node_id", path+".id", "must not be empty"))
        } else if _, exists := ids[id]; exists {
          issues = append(issues, stageIssue("node_id", path+".id", "must be unique within the batch"))
        } else {
          ids[id] = struct{}{}
        }
        if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Function) == "" {
          issues = append(issues, stageIssue("node", path, "name and function must not be empty"))
        }
        if _, exists := types[node.Type]; !exists {
          issues = append(issues, stageIssue("node_type", path+".type", "must be product, component, material, process, equipment, qualification, service, or system"))
        }
        if len(node.Stages) == 0 {
          issues = append(issues, stageIssue("stage", path+".stages", "must not be empty"))
        }
        switch node.Status {
        case "supported":
          if len(node.EvidenceClaimIDs) == 0 {
            issues = append(issues, stageIssue("evidence_claim_id", path+".evidence_claim_ids", "supported nodes require evidence"))
          }
        case "unknown":
          if strings.TrimSpace(node.UnknownReason) == "" {
            issues = append(issues, stageIssue("unknown", path+".unknown_reason", "unknown nodes require a reason"))
          }
        default:
          issues = append(issues, stageIssue("node_status", path+".status", "must be supported or unknown"))
        }
        if node.Terminal && strings.TrimSpace(node.StopReason) == "" {
          issues = append(issues, stageIssue("stop_reason", path+".stop_reason", "terminal nodes require a stop reason"))
        }
      }
      return issues
    }

    func validateEdgeBatch(values []Edge) []Issue {
      issues := []Issue{}
      if len(values) > 15 {
        issues = append(issues, stageIssue("batch_size", "edges", "must contain at most 15 edges"))
      }
      relations := map[string]struct{}{
        "contains": {}, "supplies": {}, "transformed_into": {}, "assembled_into": {},
        "processed_by": {}, "tested_by": {}, "qualified_by": {}, "used_by": {},
      }
      for index, edge := range values {
        path := fmt.Sprintf("edges[%d]", index)
        if strings.TrimSpace(edge.SupplierNodeID) == "" || strings.TrimSpace(edge.ConsumerNodeID) == "" {
          issues = append(issues, stageIssue("edge_node_id", path, "supplier_node_id and consumer_node_id must not be empty"))
        }
        if _, exists := relations[edge.Relation]; !exists {
          issues = append(issues, stageIssue("relation", path+".relation", "must use a controlled supply-chain relation"))
        }
        switch edge.Status {
        case "supported":
          if len(edge.EvidenceClaimIDs) == 0 {
            issues = append(issues, stageIssue("evidence_claim_id", path+".evidence_claim_ids", "supported edges require evidence"))
          }
        case "unknown":
          if strings.TrimSpace(edge.UnknownReason) == "" {
            issues = append(issues, stageIssue("unknown", path+".unknown_reason", "unknown edges require a reason"))
          }
        default:
          issues = append(issues, stageIssue("edge_status", path+".status", "must be supported or unknown"))
        }
      }
      return issues
    }

    func validateCoverageBatch(values []Coverage) []Issue {
      issues := []Issue{}
      if len(values) == 0 || len(values) > 10 {
        issues = append(issues, stageIssue("batch_size", "coverage", "must contain from 1 through 10 coverage items"))
      }
      ids := map[string]struct{}{}
      for index, item := range values {
        path := fmt.Sprintf("coverage[%d]", index)
        if strings.TrimSpace(item.ID) == "" {
          issues = append(issues, stageIssue("coverage_item", path+".id", "must not be empty"))
        } else if _, exists := ids[item.ID]; exists {
          issues = append(issues, stageIssue("coverage_item", path+".id", "must be unique within the batch"))
        } else {
          ids[item.ID] = struct{}{}
        }
        switch item.Status {
        case "covered":
          if len(item.NodeIDs) == 0 || len(item.EvidenceClaimIDs) == 0 || strings.TrimSpace(item.Explanation) == "" {
            issues = append(issues, stageIssue("coverage", path, "covered items require nodes, evidence, and an explanation"))
          }
        case "unknown":
          if strings.TrimSpace(item.Explanation) == "" || strings.TrimSpace(item.ResearchAttempt) == "" || strings.TrimSpace(item.Impact) == "" {
            issues = append(issues, stageIssue("unknown", path, "unknown coverage requires explanation, research_attempt, and impact"))
          }
        case "not_applicable", "out_of_scope":
          if strings.TrimSpace(item.Explanation) == "" {
            issues = append(issues, stageIssue("coverage", path+".explanation", "must explain why the item is not applicable or out of scope"))
          }
        default:
          issues = append(issues, stageIssue("coverage_status", path+".status", "must be covered, unknown, not_applicable, or out_of_scope"))
        }
      }
      return issues
    }

    func validateChokepointBatch(values []Chokepoint) []Issue {
      issues := []Issue{}
      if len(values) > 10 {
        issues = append(issues, stageIssue("batch_size", "chokepoints", "must contain at most 10 chokepoints"))
      }
      for index, item := range values {
        path := fmt.Sprintf("chokepoints[%d]", index)
        if strings.TrimSpace(item.NodeID) == "" {
          issues = append(issues, stageIssue("node_id", path+".node_id", "must not be empty"))
        }
        switch item.DeliveryImpact {
        case "limited", "material", "production_stop":
        default:
          issues = append(issues, stageIssue("delivery_impact", path+".delivery_impact", "must be limited, material, or production_stop"))
        }
        switch item.Substitutability {
        case "qualified_alternatives", "lengthy_requalification", "no_known_substitute", "unknown":
        default:
          issues = append(issues, stageIssue("substitutability", path+".substitutability", "must use the controlled substitution enum"))
        }
        switch item.SupplierConcentration {
        case "diversified", "concentrated", "single_source", "unknown":
        default:
          issues = append(issues, stageIssue("supplier_concentration", path+".supplier_concentration", "must use the controlled concentration enum"))
        }
        invalidSwitchingRange := item.SwitchingTimeMinDays < 0 || item.SwitchingTimeMaxDays < item.SwitchingTimeMinDays
        invalidRecoveryRange := item.RecoveryTimeMinDays < 0 || item.RecoveryTimeMaxDays < item.RecoveryTimeMinDays
        if invalidSwitchingRange || invalidRecoveryRange {
          issues = append(issues, stageIssue("time_range", path, "switching and recovery day ranges must be non-negative with max greater than or equal to min"))
        }
        if len(item.Mechanisms) == 0 || len(item.EvidenceClaimIDs) == 0 || strings.TrimSpace(item.WhySelected) == "" {
          issues = append(issues, stageIssue("chokepoint", path, "mechanisms, selection rationale, and evidence claim IDs are required"))
        }
      }
      return issues
    }

    func batchPayload(input Input) ([]byte, error) {
      var value any
      switch input.Section {
      case "metadata": value = input.Metadata
      case "nodes": value = input.Nodes
      case "edges": value = input.Edges
      case "coverage": value = input.Coverage
      case "chokepoints": value = input.Chokepoints
      default: return nil, fmt.Errorf("unsupported supply-chain section %q", input.Section)
      }
      payload, err := json.MarshalIndent(value, "", "  ")
      if err != nil { return nil, fmt.Errorf("encode supply-chain draft batch: %w", err) }
      return append(payload, '\n'), nil
    }

    func batchCount(input Input) int {
      switch input.Section {
      case "metadata": return 1
      case "nodes": return len(input.Nodes)
      case "edges": return len(input.Edges)
      case "coverage": return len(input.Coverage)
      case "chokepoints": return len(input.Chokepoints)
      default: return 0
      }
    }

    func absoluteNamedFile(raw, name string) bool {
      path := filepath.Clean(strings.TrimSpace(raw))
      info, err := os.Stat(path)
      return filepath.IsAbs(path) && err == nil && !info.IsDir() && filepath.Base(path) == name
    }

    func stageIssue(code, path, message string) Issue {
      repair := "Correct this batch and call the staging tool again with the same batch_id."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "finalize_supply_chain" {
  description = "Assemble all staged supply-chain batches, run complete cross-batch validation, write supply-chain.json, and return its JSON. Call only after every section has been staged."

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
      ID                 string   `json:"id"`
      Name               string   `json:"name"`
      Type               string   `json:"type"`
      Function           string   `json:"function"`
      Stages             []string `json:"stages"`
      Status             string   `json:"status"`
      EvidenceClaimIDs   []string `json:"evidence_claim_ids,omitempty"`
      UnknownReason      string   `json:"unknown_reason,omitempty"`
      Terminal           bool     `json:"terminal"`
      StopReason         string   `json:"stop_reason,omitempty"`
    }

    type Edge struct {
      SupplierNodeID     string   `json:"supplier_node_id"`
      ConsumerNodeID     string   `json:"consumer_node_id"`
      Relation           string   `json:"relation"`
      Status             string   `json:"status"`
      EvidenceClaimIDs   []string `json:"evidence_claim_ids,omitempty"`
      UnknownReason      string   `json:"unknown_reason,omitempty"`
    }

    type Coverage struct {
      ID                 string   `json:"id"`
      Status             string   `json:"status"`
      NodeIDs            []string `json:"node_ids,omitempty"`
      EvidenceClaimIDs   []string `json:"evidence_claim_ids,omitempty"`
      Explanation        string   `json:"explanation"`
      ResearchAttempt    string   `json:"research_attempt,omitempty"`
      Impact             string   `json:"impact,omitempty"`
    }

    type Chokepoint struct {
      NodeID                 string   `json:"node_id"`
      Mechanisms             []string `json:"mechanisms"`
      WhySelected            string   `json:"why_selected"`
      DeliveryImpact         string   `json:"delivery_impact"`
      Substitutability       string   `json:"substitutability"`
      SupplierConcentration  string   `json:"supplier_concentration"`
      SwitchingTimeMinDays   int      `json:"switching_time_min_days"`
      SwitchingTimeMaxDays   int      `json:"switching_time_max_days"`
      RecoveryTimeMinDays    int      `json:"recovery_time_min_days"`
      RecoveryTimeMaxDays    int      `json:"recovery_time_max_days"`
      EvidenceClaimIDs       []string `json:"evidence_claim_ids"`
    }

    type Metadata struct {
      ScopeArtifact     string       `json:"scope_artifact"`
      ReconciledArtifact string      `json:"reconciled_artifact"`
      Topic             string       `json:"topic"`
      FocalNodeID       string       `json:"focal_node_id"`
      ReviewedArtifacts []string     `json:"reviewed_artifacts"`
      Conclusion        string       `json:"conclusion"`
    }

    type Chain struct {
      ArtifactPath      string       `json:"artifact_path"`
      ScopeArtifact     string       `json:"scope_artifact"`
      ReconciledArtifact string      `json:"reconciled_artifact"`
      Topic             string       `json:"topic"`
      FocalNodeID       string       `json:"focal_node_id"`
      ReviewedArtifacts []string     `json:"reviewed_artifacts"`
      Coverage          []Coverage   `json:"coverage"`
      Nodes             []Node       `json:"nodes"`
      Edges             []Edge       `json:"edges"`
      Chokepoints       []Chokepoint `json:"chokepoints"`
      Conclusion        string       `json:"conclusion"`
    }

    type Input struct {
      ArtifactPath string `json:"artifact_path"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      metadata, metadataIssues := loadMetadata()
      nodes, nodeIssues := loadBatches[Node]("nodes")
      edges, edgeIssues := loadBatches[Edge]("edges")
      coverage, coverageIssues := loadBatches[Coverage]("coverage")
      chokepoints, chokepointIssues := loadBatches[Chokepoint]("chokepoints")
      issues := append(metadataIssues, nodeIssues...)
      issues = append(issues, edgeIssues...)
      issues = append(issues, coverageIssues...)
      issues = append(issues, chokepointIssues...)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      chain := Chain{
        ArtifactPath: input.ArtifactPath,
        ScopeArtifact: metadata.ScopeArtifact,
        ReconciledArtifact: metadata.ReconciledArtifact,
        Topic: metadata.Topic,
        FocalNodeID: metadata.FocalNodeID,
        ReviewedArtifacts: metadata.ReviewedArtifacts,
        Coverage: coverage,
        Nodes: nodes,
        Edges: edges,
        Chokepoints: chokepoints,
        Conclusion: metadata.Conclusion,
      }
      issues = append(issues, validateChain(chain)...)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(chain, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode supply chain: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write supply chain: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func loadMetadata() (Metadata, []Issue) {
      path := filepath.Join(".supply-chain-draft", "metadata", "main.json")
      payload, err := os.ReadFile(path)
      if err != nil {
        return Metadata{}, []Issue{chainIssue("draft_metadata", "metadata", "stage metadata with batch_id main before finalizing")}
      }
      var result Metadata
      if err := json.Unmarshal(payload, &result); err != nil {
        return Metadata{}, []Issue{chainIssue("draft_metadata", "metadata", "staged metadata must contain valid JSON")}
      }
      return result, nil
    }

    func loadBatches[T any](section string) ([]T, []Issue) {
      pattern := filepath.Join(".supply-chain-draft", section, "*.json")
      paths, err := filepath.Glob(pattern)
      if err != nil || len(paths) == 0 {
        return nil, []Issue{chainIssue("draft_section", section, "stage at least one "+section+" batch before finalizing")}
      }
      result := []T{}
      issues := []Issue{}
      for _, path := range paths {
        payload, readErr := os.ReadFile(path)
        if readErr != nil {
          issues = append(issues, chainIssue("draft_batch", filepath.ToSlash(path), "staged batch must be readable"))
          continue
        }
        var batch []T
        if decodeErr := json.Unmarshal(payload, &batch); decodeErr != nil {
          issues = append(issues, chainIssue("draft_batch", filepath.ToSlash(path), "staged batch must contain valid JSON"))
          continue
        }
        result = append(result, batch...)
      }
      return result, issues
    }

    func validateChain(input Chain) []Issue {
      issues := []Issue{}
      if !chainArtifactPath(input.ArtifactPath) {
        issues = append(issues, chainIssue("artifact_path", "artifact_path", "must be the absolute block_wd() path ending in supply-chain.json"))
      }
      if strings.TrimSpace(input.Topic) == "" || strings.TrimSpace(input.Conclusion) == "" {
        issues = append(issues, chainIssue("summary", "topic", "topic and conclusion must not be empty"))
      }
      scopeItems, expectedStages, scopeIssues := chainScope(input.ScopeArtifact)
      issues = append(issues, scopeIssues...)
      reviewedClaimStatuses, artifactIssues := chainClaims(input.ReviewedArtifacts)
      issues = append(issues, artifactIssues...)
      claimStatuses, reconciliationIssues := chainReconciledClaims(input.ReconciledArtifact, reviewedClaimStatuses)
      issues = append(issues, reconciliationIssues...)
      if len(input.Nodes) == 0 {
        issues = append(issues, chainIssue("nodes", "nodes", "submit at least the focal node"))
      }
      validNodeTypes := map[string]struct{}{
        "product": {}, "component": {}, "material": {}, "process": {},
        "equipment": {}, "qualification": {}, "service": {}, "system": {},
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
        if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Function) == "" {
          issues = append(issues, chainIssue("node", path, "name and function must not be empty"))
        }
        if _, exists := validNodeTypes[node.Type]; !exists {
          issues = append(issues, chainIssue("node_type", path+".type", "must be product, component, material, process, equipment, qualification, service, or system"))
        }
        if len(node.Stages) == 0 {
          issues = append(issues, chainIssue("stage", path+".stages", "must reference at least one expected stage"))
        }
        for stageIndex, stage := range node.Stages {
          if _, exists := expectedStages[normalizeStage(stage)]; !exists {
            issues = append(issues, chainIssue("stage", fmt.Sprintf("%s.stages[%d]", path, stageIndex), "must reference a stage declared by scope.json"))
          }
        }
        switch node.Status {
        case "supported":
          if len(node.EvidenceClaimIDs) == 0 {
            issues = append(issues, chainIssue("evidence_claim_id", path+".evidence_claim_ids", "supported nodes require evidence"))
          }
          issues = append(issues, chainEvidenceIssues(path+".evidence_claim_ids", node.EvidenceClaimIDs, claimStatuses, false)...)
        case "unknown":
          if strings.TrimSpace(node.UnknownReason) == "" {
            issues = append(issues, chainIssue("unknown", path+".unknown_reason", "unknown nodes require a reason"))
          }
        default:
          issues = append(issues, chainIssue("node_status", path+".status", "must be supported or unknown"))
        }
        if node.Terminal && strings.TrimSpace(node.StopReason) == "" {
          issues = append(issues, chainIssue("stop_reason", path+".stop_reason", "terminal nodes require a stop reason"))
        }
      }
      if _, exists := nodeIDs[strings.TrimSpace(input.FocalNodeID)]; !exists {
        issues = append(issues, chainIssue("focal_node_id", "focal_node_id", "must reference a declared node"))
      }
      adjacency := map[string][]string{}
      validRelations := map[string]struct{}{
        "contains": {}, "supplies": {}, "transformed_into": {}, "assembled_into": {},
        "processed_by": {}, "tested_by": {}, "qualified_by": {}, "used_by": {},
      }
      for index, edge := range input.Edges {
        path := fmt.Sprintf("edges[%d]", index)
        if _, exists := nodeIDs[edge.SupplierNodeID]; !exists {
          issues = append(issues, chainIssue("supplier_node_id", path+".supplier_node_id", "must reference a declared node"))
        }
        if _, exists := nodeIDs[edge.ConsumerNodeID]; !exists {
          issues = append(issues, chainIssue("consumer_node_id", path+".consumer_node_id", "must reference a declared node"))
        }
        if _, exists := validRelations[edge.Relation]; !exists {
          issues = append(issues, chainIssue("relation", path+".relation", "must use a controlled supply-chain relation"))
        }
        switch edge.Status {
        case "supported":
          if len(edge.EvidenceClaimIDs) == 0 {
            issues = append(issues, chainIssue("evidence_claim_id", path+".evidence_claim_ids", "supported edges require evidence"))
          }
          issues = append(issues, chainEvidenceIssues(path+".evidence_claim_ids", edge.EvidenceClaimIDs, claimStatuses, false)...)
        case "unknown":
          if strings.TrimSpace(edge.UnknownReason) == "" {
            issues = append(issues, chainIssue("unknown", path+".unknown_reason", "unknown edges require a reason"))
          }
        default:
          issues = append(issues, chainIssue("edge_status", path+".status", "must be supported or unknown"))
        }
        adjacency[edge.SupplierNodeID] = append(adjacency[edge.SupplierNodeID], edge.ConsumerNodeID)
        adjacency[edge.ConsumerNodeID] = append(adjacency[edge.ConsumerNodeID], edge.SupplierNodeID)
      }
      issues = append(issues, chainConnectivity(input.FocalNodeID, nodeIDs, adjacency)...)

      submittedCoverage := map[string]struct{}{}
      for index, item := range input.Coverage {
        path := fmt.Sprintf("coverage[%d]", index)
        if _, exists := scopeItems[item.ID]; !exists {
          issues = append(issues, chainIssue("coverage_item", path+".id", "must reference a coverage item declared by scope.json"))
        } else if _, exists := submittedCoverage[item.ID]; exists {
          issues = append(issues, chainIssue("coverage_item", path+".id", "must be unique"))
        } else {
          submittedCoverage[item.ID] = struct{}{}
        }
        switch item.Status {
        case "covered":
          if len(item.NodeIDs) == 0 || len(item.EvidenceClaimIDs) == 0 || strings.TrimSpace(item.Explanation) == "" {
            issues = append(issues, chainIssue("coverage", path, "covered items require nodes, evidence, and an explanation"))
          }
          for nodeIndex, nodeID := range item.NodeIDs {
            if _, exists := nodeIDs[nodeID]; !exists {
              issues = append(issues, chainIssue("coverage_node", fmt.Sprintf("%s.node_ids[%d]", path, nodeIndex), "must reference a declared node"))
            }
          }
          issues = append(issues, chainEvidenceIssues(path+".evidence_claim_ids", item.EvidenceClaimIDs, claimStatuses, false)...)
        case "unknown":
          if strings.TrimSpace(item.Explanation) == "" || strings.TrimSpace(item.ResearchAttempt) == "" || strings.TrimSpace(item.Impact) == "" {
            issues = append(issues, chainIssue("unknown", path, "unknown coverage requires explanation, research_attempt, and impact"))
          }
        case "not_applicable", "out_of_scope":
          if strings.TrimSpace(item.Explanation) == "" {
            issues = append(issues, chainIssue("coverage", path+".explanation", "must explain why the item is not applicable or out of scope"))
          }
        default:
          issues = append(issues, chainIssue("coverage_status", path+".status", "must be covered, unknown, not_applicable, or out_of_scope"))
        }
      }
      for coverageID := range scopeItems {
        if _, exists := submittedCoverage[coverageID]; !exists {
          issues = append(issues, chainIssue("coverage_item", "coverage", "scope item "+coverageID+" must be resolved explicitly"))
        }
      }

      for index, item := range input.Chokepoints {
        path := fmt.Sprintf("chokepoints[%d]", index)
        if _, exists := nodeIDs[item.NodeID]; !exists {
          issues = append(issues, chainIssue("node_id", path+".node_id", "must reference a declared node"))
        }
        switch item.DeliveryImpact {
        case "limited", "material", "production_stop":
        default:
          issues = append(issues, chainIssue("delivery_impact", path+".delivery_impact", "must use the controlled delivery-impact enum"))
        }
        switch item.Substitutability {
        case "qualified_alternatives", "lengthy_requalification", "no_known_substitute", "unknown":
        default:
          issues = append(issues, chainIssue("substitutability", path+".substitutability", "must use the controlled substitution enum"))
        }
        switch item.SupplierConcentration {
        case "diversified", "concentrated", "single_source", "unknown":
        default:
          issues = append(issues, chainIssue("supplier_concentration", path+".supplier_concentration", "must use the controlled concentration enum"))
        }
        invalidSwitchingRange := item.SwitchingTimeMinDays < 0 || item.SwitchingTimeMaxDays < item.SwitchingTimeMinDays
        invalidRecoveryRange := item.RecoveryTimeMinDays < 0 || item.RecoveryTimeMaxDays < item.RecoveryTimeMinDays
        if invalidSwitchingRange || invalidRecoveryRange {
          issues = append(issues, chainIssue("time_range", path, "switching and recovery day ranges must be non-negative with max greater than or equal to min"))
        }
        if len(item.Mechanisms) == 0 || len(item.EvidenceClaimIDs) == 0 || strings.TrimSpace(item.WhySelected) == "" {
          issues = append(issues, chainIssue("chokepoint", path, "mechanisms, selection rationale, and evidence claim IDs are required"))
        }
        issues = append(issues, chainEvidenceIssues(path+".evidence_claim_ids", item.EvidenceClaimIDs, claimStatuses, true)...)
      }
      return issues
    }

    func chainScope(rawPath string) (map[string]struct{}, map[string]struct{}, []Issue) {
      items := map[string]struct{}{}
      stages := map[string]struct{}{}
      path := filepath.Clean(strings.TrimSpace(rawPath))
      clean := filepath.ToSlash(path)
      if !filepath.IsAbs(path) || !strings.Contains(clean, "/.r42/runs/") ||
        !strings.Contains(clean, "/blocks/") || !strings.HasSuffix(clean, "/scope.json") {
        return items, stages, []Issue{chainIssue("scope_artifact", "scope_artifact", "must name an existing absolute scope.json")}
      }
      payload, err := os.ReadFile(path)
      if err != nil {
        return items, stages, []Issue{chainIssue("scope_artifact", "scope_artifact", "must name an existing absolute scope.json")}
      }
      var scope struct {
        ExpectedStages []string `json:"expected_stages"`
        CoverageItems []struct {
          ID string `json:"id"`
        } `json:"coverage_items"`
      }
      if err := json.Unmarshal(payload, &scope); err != nil {
        return items, stages, []Issue{chainIssue("scope_artifact", "scope_artifact", "must contain a valid coverage inventory")}
      }
      for _, stage := range scope.ExpectedStages {
        normalized := normalizeStage(stage)
        if normalized != "" {
          stages[normalized] = struct{}{}
        }
      }
      for _, item := range scope.CoverageItems {
        if strings.TrimSpace(item.ID) != "" {
          items[item.ID] = struct{}{}
        }
      }
      if len(items) == 0 || len(stages) == 0 {
        return items, stages, []Issue{chainIssue("scope_artifact", "scope_artifact", "must declare stages and coverage items")}
      }
      return items, stages, nil
    }

    func normalizeStage(value string) string {
      return strings.Join(strings.Fields(value), " ")
    }

    func chainClaims(paths []string) (map[string]string, []Issue) {
      claims := map[string]string{}
      issues := []Issue{}
      if len(paths) != 5 {
        issues = append(issues, chainIssue("reviewed_artifacts", "reviewed_artifacts", "must contain the five validated track artifacts"))
      }
      tracks := map[string]struct{}{}
      for index, raw := range paths {
        path := filepath.Clean(strings.TrimSpace(raw))
        info, err := os.Stat(path)
        if !filepath.IsAbs(path) || err != nil || info.IsDir() || filepath.Base(path) != "evidence-ledger.json" {
          issues = append(issues, chainIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "must name an existing absolute evidence-ledger.json"))
          continue
        }
        payload, err := os.ReadFile(path)
        if err != nil {
          issues = append(issues, chainIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "must be readable"))
          continue
        }
        var artifact struct {
          Track string `json:"track"`
          Claims []struct {
            ID     string `json:"id"`
            Status string `json:"status"`
          } `json:"claims"`
        }
        if err := json.Unmarshal(payload, &artifact); err != nil {
          issues = append(issues, chainIssue("reviewed_artifact", fmt.Sprintf("reviewed_artifacts[%d]", index), "must contain valid track evidence"))
          continue
        }
        if _, exists := tracks[artifact.Track]; exists || strings.TrimSpace(artifact.Track) == "" {
          issues = append(issues, chainIssue("reviewed_track", fmt.Sprintf("reviewed_artifacts[%d]", index), "track must be non-empty and unique"))
        }
        tracks[artifact.Track] = struct{}{}
        for _, claim := range artifact.Claims {
          if strings.TrimSpace(claim.ID) == "" {
            continue
          }
          if _, exists := claims[claim.ID]; exists {
            issues = append(issues, chainIssue("claim_id", fmt.Sprintf("reviewed_artifacts[%d]", index), "claim IDs must be globally unique"))
          }
          claims[claim.ID] = claim.Status
        }
      }
      return claims, issues
    }

    func chainReconciledClaims(raw string, reviewed map[string]string) (map[string]string, []Issue) {
      statuses := map[string]string{}
      issues := []Issue{}
      path := filepath.Clean(strings.TrimSpace(raw))
      info, err := os.Stat(path)
      if !filepath.IsAbs(path) || err != nil || info.IsDir() || filepath.Base(path) != "evidence-resolution.json" {
        return statuses, []Issue{chainIssue("reconciled_artifact", "reconciled_artifact", "must name an existing absolute evidence-resolution.json")}
      }
      payload, err := os.ReadFile(path)
      if err != nil {
        return statuses, []Issue{chainIssue("reconciled_artifact", "reconciled_artifact", "must be readable")}
      }
      var artifact struct {
        Claims []struct {
          ID           string `json:"id"`
          Status       string `json:"status"`
          Availability string `json:"reconciliation_availability"`
        } `json:"claims"`
        Conflicts []struct {
          ClaimIDs []string `json:"claim_ids"`
          Resolution struct {
            ChosenClaimIDs []string `json:"chosen_claim_ids"`
          } `json:"resolution"`
        } `json:"conflicts"`
      }
      if err := json.Unmarshal(payload, &artifact); err != nil {
        return statuses, []Issue{chainIssue("reconciled_artifact", "reconciled_artifact", "must contain valid reconciled evidence")}
      }
      inheritedUnavailable := map[string]struct{}{}
      for _, claim := range artifact.Claims {
        reviewedStatus, exists := reviewed[claim.ID]
        if !exists {
          continue
        }
        if claim.Status != reviewedStatus {
          issues = append(issues, chainIssue("reconciled_claim", "reconciled_artifact", "claim "+claim.ID+" must preserve its reviewed evidence status"))
        }
        if claim.Availability == "excluded" || claim.Availability == "unresolved" {
          statuses[claim.ID] = claim.Availability
          inheritedUnavailable[claim.ID] = struct{}{}
        } else {
          statuses[claim.ID] = reviewedStatus
        }
      }
      for claimID := range reviewed {
        if _, exists := statuses[claimID]; !exists {
          issues = append(issues, chainIssue("reconciled_claim", "reconciled_artifact", "reviewed claim "+claimID+" must appear in reconciled evidence"))
        }
      }
      for _, conflict := range artifact.Conflicts {
        for _, claimID := range conflict.ClaimIDs {
          if _, inherited := inheritedUnavailable[claimID]; inherited {
            continue
          }
          statuses[claimID] = "excluded"
        }
        for _, claimID := range conflict.Resolution.ChosenClaimIDs {
          if _, inherited := inheritedUnavailable[claimID]; inherited {
            continue
          }
          if reviewedStatus, exists := reviewed[claimID]; exists {
            statuses[claimID] = reviewedStatus
          }
        }
      }
      return statuses, issues
    }

    func chainEvidenceIssues(path string, values []string, known map[string]string, formalConclusion bool) []Issue {
      issues := []Issue{}
      for index, value := range values {
        status, exists := known[value]
        if !exists {
          issues = append(issues, chainIssue("evidence_claim_id", fmt.Sprintf("%s[%d]", path, index), "must reference a claim in the reviewed track ledgers"))
          continue
        }
        if status == "excluded" || status == "unresolved" {
          issues = append(issues, chainIssue("reconciled_claim", fmt.Sprintf("%s[%d]", path, index), "must not reference a claim unavailable after evidence reconciliation"))
          continue
        }
        if formalConclusion && status != "confirmed" {
          issues = append(issues, chainIssue("evidence_level", fmt.Sprintf("%s[%d]", path, index), "formal chokepoints require confirmed and undisputed claims"))
        }
        if !formalConclusion && (status == "unknown" || status == "contradicted" || status == "disputed") {
          issues = append(issues, chainIssue("evidence_level", fmt.Sprintf("%s[%d]", path, index), "supported graph objects cannot rely on unknown, contradicted, or disputed claims"))
        }
      }
      return issues
    }

    func chainConnectivity(focal string, nodes map[string]struct{}, adjacency map[string][]string) []Issue {
      if _, exists := nodes[focal]; !exists {
        return nil
      }
      visited := map[string]struct{}{focal: {}}
      pending := []string{focal}
      for len(pending) > 0 {
        current := pending[0]
        pending = pending[1:]
        for _, next := range adjacency[current] {
          if _, exists := visited[next]; exists {
            continue
          }
          visited[next] = struct{}{}
          pending = append(pending, next)
        }
      }
      issues := []Issue{}
      for nodeID := range nodes {
        if _, exists := visited[nodeID]; !exists {
          issues = append(issues, chainIssue("graph_connectivity", "nodes", "node "+nodeID+" must be connected to the focal product"))
        }
      }
      return issues
    }

    func chainArtifactPath(raw string) bool {
      clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
      return filepath.IsAbs(raw) && strings.Contains(clean, "/.r42/runs/") &&
        strings.Contains(clean, "/blocks/") && strings.HasSuffix(clean, "/supply-chain.json")
    }

    func chainIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "read_chokepoint_evidence" {
  description = "Read only the reconciled claims, sources, and conflict decisions requested by claim ID for semantic chokepoint QC. This tool does not validate or modify the candidate."

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
      EvidenceResolutionPath string   `json:"evidence_resolution_path"`
      ClaimIDs               []string `json:"claim_ids"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      path, issues := evidenceContextPath(input.EvidenceResolutionPath)
      if len(input.ClaimIDs) == 0 {
        issues = append(issues, evidenceContextIssue("claim_ids", "claim_ids", "provide the claim IDs cited by the chokepoints under semantic review"))
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := os.ReadFile(path)
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read reconciled evidence: %w", err)
      }
      var artifact map[string]any
      if err = json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}), &artifact); err != nil {
        return ToolResponse[Output]{Accepted: false, Issues: []Issue{evidenceContextIssue("evidence_resolution", "evidence_resolution_path", "must contain valid reconciled evidence")}}, nil
      }
      wanted := map[string]struct{}{}
      for _, claimID := range input.ClaimIDs {
        wanted[strings.TrimSpace(claimID)] = struct{}{}
      }
      claims := []any{}
      sourceIDs := map[string]struct{}{}
      found := map[string]struct{}{}
      for _, raw := range evidenceContextList(artifact["claims"]) {
        claim, ok := raw.(map[string]any)
        if !ok {
          continue
        }
        claimID := evidenceContextString(claim["id"])
        if _, exists := wanted[claimID]; !exists {
          continue
        }
        found[claimID] = struct{}{}
        claims = append(claims, claim)
        for _, evidenceRaw := range evidenceContextList(claim["evidence"]) {
          if evidence, evidenceOK := evidenceRaw.(map[string]any); evidenceOK {
            sourceIDs[evidenceContextString(evidence["source_id"])] = struct{}{}
          }
        }
      }
      for claimID := range wanted {
        if _, exists := found[claimID]; !exists {
          issues = append(issues, evidenceContextIssue("claim_id", "claim_ids", "claim "+claimID+" does not exist in reconciled evidence"))
        }
      }
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      sources := []any{}
      for _, raw := range evidenceContextList(artifact["sources"]) {
        source, ok := raw.(map[string]any)
        if !ok {
          continue
        }
        if _, exists := sourceIDs[evidenceContextString(source["id"])]; exists {
          sources = append(sources, source)
        }
      }
      conflicts := []any{}
      for _, raw := range evidenceContextList(artifact["conflicts"]) {
        conflict, ok := raw.(map[string]any)
        if !ok {
          continue
        }
        include := false
        for _, claimIDRaw := range evidenceContextList(conflict["claim_ids"]) {
          if _, exists := wanted[evidenceContextString(claimIDRaw)]; exists {
            include = true
            break
          }
        }
        if include {
          conflicts = append(conflicts, conflict)
        }
      }
      encoded, err := json.MarshalIndent(map[string]any{
        "claims": claims, "sources": sources, "conflicts": conflicts,
      }, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode chokepoint evidence context: %w", err)
      }
      output := Output(encoded)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func evidenceContextPath(raw string) (string, []Issue) {
      workspace, err := os.Getwd()
      if err != nil {
        return "", []Issue{evidenceContextIssue("evidence_resolution_path", "evidence_resolution_path", "cannot resolve the current block workspace")}
      }
      root := evidenceContextBlocksRoot(workspace)
      path, pathErr := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      if pathErr != nil || !filepath.IsAbs(raw) || root == "" || filepath.Base(path) != "evidence-resolution.json" || !evidenceContextWithin(path, root) {
        return "", []Issue{evidenceContextIssue("evidence_resolution_path", "evidence_resolution_path", "must name evidence-resolution.json in the current run")}
      }
      info, statErr := os.Stat(path)
      if statErr != nil || info.IsDir() {
        return "", []Issue{evidenceContextIssue("evidence_resolution_path", "evidence_resolution_path", "must name readable reconciled evidence")}
      }
      return path, nil
    }

    func evidenceContextBlocksRoot(workspace string) string {
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

    func evidenceContextWithin(path, root string) bool {
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func evidenceContextList(value any) []any {
      if result, ok := value.([]any); ok {
        return result
      }
      return []any{}
    }

    func evidenceContextString(value any) string {
      if result, ok := value.(string); ok {
        return result
      }
      return fmt.Sprint(value)
    }

    func evidenceContextIssue(code, path, message string) Issue {
      repair := "Correct the read-only evidence request and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_candidates" {
  description = "Validate company hypotheses for exactly one chokepoint, write candidates.json, and return its JSON. workspace_dir is required, is created when missing, and bounds the ledger and artifact paths."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type Candidate struct {
      Name             string   `json:"name"`
      Ticker           string   `json:"ticker"`
      Market           string   `json:"market"`
      NodeID           string   `json:"node_id"`
      Relationship     string   `json:"relationship"`
      SelectionReason  string   `json:"selection_reason"`
      EvidenceClaimIDs []string `json:"evidence_claim_ids"`
    }

    type CandidateResult struct {
      Name             string   `json:"name"`
      Ticker           string   `json:"ticker"`
      Market           string   `json:"market"`
      NodeID           string   `json:"node_id"`
      Relationship     string   `json:"relationship"`
      SelectionReason  string   `json:"selection_reason"`
      EvidenceClaimIDs []string `json:"evidence_claim_ids"`
      EvidenceStatus   string   `json:"evidence_status"`
    }

    type Input struct {
      WorkspaceDir string      `json:"workspace_dir"`
      ArtifactPath string      `json:"artifact_path"`
      LedgerPath   string      `json:"ledger_path"`
      NodeID       string      `json:"node_id"`
      MaxCandidates int       `json:"max_candidates"`
      Candidates   []Candidate `json:"candidates"`
      Conclusion   string      `json:"conclusion"`
    }

    type Artifact struct {
      ArtifactPath string            `json:"artifact_path"`
      LedgerPath   string            `json:"ledger_path"`
      NodeID       string            `json:"node_id"`
      Candidates   []CandidateResult `json:"candidates"`
      Conclusion   string            `json:"conclusion"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := candidateWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create candidate workspace: %w", err)
      }
      statuses, ledgerIssues := candidateClaimStatuses(input.LedgerPath, workspace)
      issues = append(issues, ledgerIssues...)
      issues = append(issues, validateCandidates(input, statuses, workspace)...)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      candidates := make([]CandidateResult, 0, len(input.Candidates))
      for _, candidate := range input.Candidates {
        candidates = append(candidates, CandidateResult{
          Name: candidate.Name, Ticker: candidate.Ticker, Market: candidate.Market,
          NodeID: candidate.NodeID, Relationship: candidate.Relationship,
          SelectionReason: candidate.SelectionReason,
          EvidenceClaimIDs: candidate.EvidenceClaimIDs,
          EvidenceStatus: weakestEvidenceStatus(candidate.EvidenceClaimIDs, statuses),
        })
      }
      artifact := Artifact{
        ArtifactPath: input.ArtifactPath, LedgerPath: input.LedgerPath,
        NodeID: input.NodeID, Candidates: candidates, Conclusion: input.Conclusion,
      }
      payload, err := json.MarshalIndent(artifact, "", "  ")
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

    func validateCandidates(input Input, statuses map[string]string, workspace string) []Issue {
      issues := []Issue{}
      if !candidateArtifactPath(input.ArtifactPath, workspace, "candidates.json") {
        issues = append(issues, candidateIssue("artifact_path", "artifact_path", "must be an absolute path ending in candidates.json under workspace_dir"))
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
        if strings.TrimSpace(candidate.Market) == "" || strings.TrimSpace(candidate.SelectionReason) == "" || len(candidate.EvidenceClaimIDs) == 0 {
          issues = append(issues, candidateIssue("candidate", path, "market, selection_reason, and evidence_claim_ids are required"))
        }
        for claimIndex, claimID := range candidate.EvidenceClaimIDs {
          status, exists := statuses[claimID]
          if !exists {
            issues = append(issues, candidateIssue("evidence_claim_id", fmt.Sprintf("%s.evidence_claim_ids[%d]", path, claimIndex), "must reference a claim in ledger_path"))
            continue
          }
          verifiedRelationship := candidate.Relationship != "unverified"
          if verifiedRelationship && status != "confirmed" && status != "reported" {
            issues = append(issues, candidateIssue("evidence_level", fmt.Sprintf("%s.evidence_claim_ids[%d]", path, claimIndex), "verified relationships require confirmed or directly reported evidence"))
          }
        }
      }
      return issues
    }

    func candidateClaimStatuses(raw, workspace string) (map[string]string, []Issue) {
      statuses := map[string]string{}
      path := filepath.Clean(strings.TrimSpace(raw))
      if !filepath.IsAbs(path) || filepath.Base(path) != "evidence-ledger.json" || !candidateWithin(path, workspace) {
        return statuses, []Issue{candidateIssue("ledger_path", "ledger_path", "must name an existing absolute evidence-ledger.json under workspace_dir")}
      }
      payload, err := os.ReadFile(path)
      if err != nil {
        return statuses, []Issue{candidateIssue("ledger_path", "ledger_path", "must name an existing absolute evidence-ledger.json")}
      }
      var ledger struct {
        Claims []struct {
          ID     string `json:"id"`
          Status string `json:"status"`
        } `json:"claims"`
      }
      if err := json.Unmarshal(payload, &ledger); err != nil {
        return statuses, []Issue{candidateIssue("ledger_path", "ledger_path", "must contain a valid claim ledger")}
      }
      for _, claim := range ledger.Claims {
        statuses[claim.ID] = claim.Status
      }
      return statuses, nil
    }

    func weakestEvidenceStatus(ids []string, statuses map[string]string) string {
      order := map[string]int{
        "contradicted": 0, "unknown": 1, "disputed": 2,
        "inferred": 3, "reported": 4, "confirmed": 5,
      }
      result := "confirmed"
      for _, id := range ids {
        if order[statuses[id]] < order[result] {
          result = statuses[id]
        }
      }
      return result
    }

    func candidateWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := candidateBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !candidateWithin(workspace, blocksRoot) {
        return "", []Issue{candidateIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func candidateBlocksRoot(workspace string) string {
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

    func candidateArtifactPath(raw, workspace, name string) bool {
      path := filepath.Clean(strings.TrimSpace(raw))
      return filepath.IsAbs(path) && filepath.Base(path) == name && candidateWithin(path, workspace)
    }

    func candidateWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func candidateIssue(code, path, message string) Issue {
      repair := "Correct this field and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_candidate_assessment" {
  description = "Validate one candidate's exact-node relationship, controlled supplier maturity, alternatives, and falsification conditions. workspace_dir is required, is created when missing, and bounds the ledger and artifact paths. Evidence is referenced by accepted ledger claim IDs; no investment score or aggregate score is produced."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
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

    type Input struct {
      WorkspaceDir       string    `json:"workspace_dir"`
      ArtifactPath        string    `json:"artifact_path"`
      LedgerPath          string    `json:"ledger_path"`
      Candidate           Candidate `json:"candidate"`
      RelationshipMaturity string   `json:"relationship_maturity"`
      ControlMechanism    string    `json:"control_mechanism"`
      EvidenceClaimIDs    []string  `json:"evidence_claim_ids"`
      KeyClaimIDs         []string  `json:"key_claim_ids"`
      PeerAlternatives    []string  `json:"peer_alternatives"`
      SwitchingConstraints []string `json:"switching_constraints"`
      Falsification       []string  `json:"falsification"`
      WhatCouldWeakenView []string  `json:"what_could_weaken_view"`
      Conclusion          string    `json:"conclusion"`
    }

    type Artifact struct {
      Input
      EvidenceStatus       string           `json:"evidence_status"`
      EffectiveRelationship string          `json:"effective_relationship"`
      EffectiveMaturity    string           `json:"effective_relationship_maturity"`
      VerificationStatus   string           `json:"verification_status"`
      VerificationGaps     []string         `json:"verification_gaps"`
      KeyClaimReviews      []KeyClaimReview `json:"key_claim_reviews"`
    }

    type KeyClaimReview struct {
      ClaimID                string `json:"claim_id"`
      EvidenceStatus         string `json:"evidence_status"`
      DisputeStatus          string `json:"dispute_status"`
      FreshnessStatus        string `json:"freshness_status"`
      EffectiveEvidenceStatus string `json:"effective_evidence_status"`
      Gap                    string `json:"gap"`
    }

    type assessmentClaimState struct {
      Status          string
      EvidenceStatus  string
      DisputeStatus   string
      FreshnessStatus string
      FreshnessGap    string
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      currentDirectory, err := os.Getwd()
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err)
      }
      workspace, issues := assessmentWorkspaceDir(input.WorkspaceDir, currentDirectory)
      if workspace == "" {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if err = os.MkdirAll(workspace, 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create candidate assessment workspace: %w", err)
      }
      statuses, ledgerIssues := assessmentClaimStatuses(input.LedgerPath, workspace)
      issues = append(issues, ledgerIssues...)
      issues = append(issues, validateAssessment(input, statuses, workspace)...)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      reviews, verificationStatus, verificationGaps := assessmentKeyClaimReviews(input, statuses)
      effectiveRelationship := input.Candidate.Relationship
      effectiveMaturity := input.RelationshipMaturity
      if verificationStatus != "verified" {
        effectiveRelationship = "unverified"
        effectiveMaturity = "unknown"
      }
      artifact := Artifact{
        Input: input, EvidenceStatus: assessmentEvidenceStatus(input.EvidenceClaimIDs, statuses),
        EffectiveRelationship: effectiveRelationship, EffectiveMaturity: effectiveMaturity,
        VerificationStatus: verificationStatus, VerificationGaps: verificationGaps,
        KeyClaimReviews: reviews,
      }
      payload, err := json.MarshalIndent(artifact, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode candidate assessment: %w", err)
      }
      payload = append(payload, '\n')
      if err := os.MkdirAll(filepath.Dir(filepath.Clean(input.ArtifactPath)), 0755); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create candidate assessment directory: %w", err)
      }
      if err := os.WriteFile(filepath.Clean(input.ArtifactPath), payload, 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write candidate assessment: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateAssessment(input Input, statuses map[string]assessmentClaimState, workspace string) []Issue {
      issues := []Issue{}
      if !assessmentArtifactPath(input.ArtifactPath, workspace) {
        issues = append(issues, assessmentIssue("artifact_path", "artifact_path", "must be an absolute path ending in assessment.json under workspace_dir"))
      }
      if strings.TrimSpace(input.Candidate.Name) == "" && strings.TrimSpace(input.Candidate.Ticker) == "" {
        issues = append(issues, assessmentIssue("identity", "candidate", "candidate name or ticker is required"))
      }
      if strings.TrimSpace(input.Candidate.NodeID) == "" || strings.TrimSpace(input.Candidate.Market) == "" {
        issues = append(issues, assessmentIssue("candidate", "candidate", "node_id and market are required"))
      }
      switch strings.ToLower(strings.TrimSpace(input.Candidate.Relationship)) {
      case "controls_bottleneck", "critical_supplier", "unverified":
      default:
        issues = append(issues, assessmentIssue("relationship", "candidate.relationship", "must be controls_bottleneck, critical_supplier, or unverified"))
      }
      maturities := map[string]struct{}{
        "research": {}, "validation": {}, "order_received": {}, "batch_delivery": {},
        "mass_production": {}, "primary_supplier": {}, "unknown": {},
      }
      if _, exists := maturities[input.RelationshipMaturity]; !exists {
        issues = append(issues, assessmentIssue("relationship_maturity", "relationship_maturity", "must use the controlled supplier-maturity lifecycle"))
      }
      if len(input.EvidenceClaimIDs) == 0 || strings.TrimSpace(input.Conclusion) == "" {
        issues = append(issues, assessmentIssue("assessment", "evidence_claim_ids", "evidence claim IDs and conclusion are required"))
      }
      evidenceClaimIDs := make(map[string]struct{}, len(input.EvidenceClaimIDs))
      for index, claimID := range input.EvidenceClaimIDs {
        evidenceClaimIDs[claimID] = struct{}{}
        if _, exists := statuses[claimID]; !exists {
          issues = append(issues, assessmentIssue("evidence_claim_id", fmt.Sprintf("evidence_claim_ids[%d]", index), "must reference a claim in ledger_path"))
        }
      }
      for index, claimID := range input.KeyClaimIDs {
        path := fmt.Sprintf("key_claim_ids[%d]", index)
        if _, exists := statuses[claimID]; !exists {
          issues = append(issues, assessmentIssue("key_claim_id", path, "must reference a claim in ledger_path"))
          continue
        }
        if _, included := evidenceClaimIDs[claimID]; !included {
          issues = append(issues, assessmentIssue("key_claim_id", path, "must also appear in evidence_claim_ids"))
        }
      }
      if len(input.Falsification) == 0 || len(input.WhatCouldWeakenView) == 0 || len(input.SwitchingConstraints) == 0 {
        issues = append(issues, assessmentIssue("risk", "falsification", "switching_constraints, falsification, and what_could_weaken_view must not be empty"))
      }
      if input.Candidate.Relationship != "unverified" && strings.TrimSpace(input.ControlMechanism) == "" {
        issues = append(issues, assessmentIssue("control_mechanism", "control_mechanism", "verified relationships require a control mechanism"))
      }
      return issues
    }

    func assessmentClaimStatuses(raw, workspace string) (map[string]assessmentClaimState, []Issue) {
      statuses := map[string]assessmentClaimState{}
      path := filepath.Clean(strings.TrimSpace(raw))
      if !filepath.IsAbs(path) || filepath.Base(path) != "evidence-ledger.json" || !assessmentWithin(path, workspace) {
        return statuses, []Issue{assessmentIssue("ledger_path", "ledger_path", "must name an existing absolute evidence-ledger.json under workspace_dir")}
      }
      payload, err := os.ReadFile(path)
      if err != nil {
        return statuses, []Issue{assessmentIssue("ledger_path", "ledger_path", "must name an existing absolute evidence-ledger.json")}
      }
      var ledger struct {
        Claims []struct {
          ID             string `json:"id"`
          Status         string `json:"status"`
          EvidenceStatus string `json:"evidence_status"`
          DisputeStatus  string `json:"dispute_status"`
        } `json:"claims"`
        FreshnessChecks []struct {
          ClaimID string `json:"claim_id"`
          Outcome string `json:"outcome"`
          Gap     string `json:"gap"`
        } `json:"freshness_checks"`
      }
      if err := json.Unmarshal(payload, &ledger); err != nil {
        return statuses, []Issue{assessmentIssue("ledger_path", "ledger_path", "must contain a valid claim ledger")}
      }
      for _, claim := range ledger.Claims {
        evidenceStatus := claim.EvidenceStatus
        if evidenceStatus == "" {
          evidenceStatus = claim.Status
          if evidenceStatus == "disputed" || evidenceStatus == "contradicted" {
            evidenceStatus = "unknown"
          }
        }
        disputeStatus := claim.DisputeStatus
        if disputeStatus == "" {
          disputeStatus = "clean"
          if claim.Status == "disputed" || claim.Status == "contradicted" {
            disputeStatus = "disputed"
          }
        }
        statuses[claim.ID] = assessmentClaimState{
          Status: claim.Status, EvidenceStatus: evidenceStatus, DisputeStatus: disputeStatus,
        }
      }
      for _, check := range ledger.FreshnessChecks {
        state, exists := statuses[check.ClaimID]
        if !exists {
          continue
        }
        state.FreshnessStatus = check.Outcome
        state.FreshnessGap = check.Gap
        statuses[check.ClaimID] = state
      }
      return statuses, nil
    }

    func assessmentEvidenceStatus(ids []string, statuses map[string]assessmentClaimState) string {
      order := map[string]int{
        "contradicted": 0, "unknown": 1, "disputed": 2,
        "inferred": 3, "reported": 4, "confirmed": 5,
      }
      result := "confirmed"
      for _, id := range ids {
        status := statuses[id].EvidenceStatus
        if statuses[id].DisputeStatus == "disputed" {
          status = "disputed"
        }
        if order[status] < order[result] {
          result = status
        }
      }
      return result
    }

    func assessmentKeyClaimReviews(input Input, statuses map[string]assessmentClaimState) ([]KeyClaimReview, string, []string) {
      keyIDs := input.KeyClaimIDs
      if len(keyIDs) == 0 {
        keyIDs = input.EvidenceClaimIDs
      }
      reviews := make([]KeyClaimReview, 0, len(keyIDs))
      gaps := make([]string, 0)
      verified := true
      for _, claimID := range keyIDs {
        state := statuses[claimID]
        effectiveStatus := state.EvidenceStatus
        gap := strings.TrimSpace(state.FreshnessGap)
        currentCheck := state.FreshnessStatus == "verified_primary" || state.FreshnessStatus == "checked_no_primary"
        if !currentCheck {
          if effectiveStatus == "confirmed" {
            effectiveStatus = "reported"
          }
          if gap == "" {
            gap = "No completed current-source check was recorded for key claim " + claimID + "."
          }
        }
        if state.DisputeStatus == "disputed" {
          effectiveStatus = "unknown"
          if gap == "" {
            gap = "Key claim " + claimID + " remains disputed."
          }
        }
        if effectiveStatus != "confirmed" && gap == "" {
          gap = "Key claim " + claimID + " has only " + effectiveStatus + " evidence after the current-source check."
        }
        if effectiveStatus != "confirmed" || !currentCheck {
          verified = false
          gaps = append(gaps, gap)
        }
        reviews = append(reviews, KeyClaimReview{
          ClaimID: claimID, EvidenceStatus: state.EvidenceStatus, DisputeStatus: state.DisputeStatus,
          FreshnessStatus: state.FreshnessStatus, EffectiveEvidenceStatus: effectiveStatus, Gap: gap,
        })
      }
      if verified {
        return reviews, "verified", gaps
      }
      return reviews, "pending", gaps
    }

    func assessmentWorkspaceDir(raw, currentDirectory string) (string, []Issue) {
      workspace, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      blocksRoot := assessmentBlocksRoot(currentDirectory)
      if err != nil || !filepath.IsAbs(raw) || blocksRoot == "" || !assessmentWithin(workspace, blocksRoot) {
        return "", []Issue{assessmentIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")}
      }
      return workspace, nil
    }

    func assessmentBlocksRoot(workspace string) string {
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

    func assessmentArtifactPath(raw, workspace string) bool {
      path := filepath.Clean(strings.TrimSpace(raw))
      return filepath.IsAbs(path) && filepath.Base(path) == "assessment.json" && assessmentWithin(path, workspace)
    }

    func assessmentWithin(path, root string) bool {
      path, pathErr := filepath.Abs(filepath.Clean(path))
      root, rootErr := filepath.Abs(filepath.Clean(root))
      if pathErr != nil || rootErr != nil {
        return false
      }
      relative, err := filepath.Rel(root, path)
      return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
    }

    func assessmentIssue(code, path, message string) Issue {
      repair := "Correct this candidate assessment and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}
