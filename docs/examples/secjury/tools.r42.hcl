go_tool "update_dcf_progress" {
  description = "Replace progress.json with the complete current DCF plan and its persisted stage results. Completed steps cannot return to pending or lose recorded evidence."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    const (
      progressSchemaVersion = "dcf-progress.v1"
      progressPending       = "pending"
      progressCompleted     = "completed"
    )

    type ProgressStep struct {
      ID                  string   `json:"id"`
      Task                string   `json:"task"`
      Status              string   `json:"status"`
      Results             []string `json:"results,omitempty"`
      SourceIDs           []string `json:"source_ids,omitempty"`
      SourceURLs          []string `json:"source_urls,omitempty"`
      LocalReferences     []string `json:"local_references,omitempty"`
      Calculations        []string `json:"calculations,omitempty"`
      Assumptions         []string `json:"assumptions,omitempty"`
      UnresolvedQuestions []string `json:"unresolved_questions,omitempty"`
    }

    type Input struct {
      ProgressPath  string         `json:"progress_path"`
      Target        string         `json:"target"`
      ValuationDate string         `json:"valuation_date"`
      Steps         []ProgressStep `json:"steps"`
    }

    type ProgressDocument struct {
      SchemaVersion string         `json:"schema_version"`
      Target        string         `json:"target"`
      ValuationDate string         `json:"valuation_date"`
      Steps         []ProgressStep `json:"steps"`
    }

    type Output struct {
      Path           string `json:"path"`
      StepCount      int    `json:"step_count"`
      CompletedCount int    `json:"completed_count"`
    }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateProgressSteps(input.Steps)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      if strings.TrimSpace(input.ProgressPath) == "" {
        path := "progress_path"
        repair := "Use the workflow-owned progress artifact path."
        return ToolResponse[Output]{Accepted: false, Issues: []Issue{{Code: "progress_path", Message: "progress path is required", Path: &path, RepairHint: &repair}}}, nil
      }

      document := ProgressDocument{
        SchemaVersion: progressSchemaVersion,
        Target: input.Target, ValuationDate: input.ValuationDate, Steps: input.Steps,
      }
      previous, exists, err := readProgressDocument(input.ProgressPath)
      if err != nil {
        return ToolResponse[Output]{}, err
      }
      if exists {
        if transitionIssues := validateProgressTransition(previous, document); len(transitionIssues) > 0 {
          return ToolResponse[Output]{Accepted: false, Issues: transitionIssues}, nil
        }
      }

      payload, err := json.MarshalIndent(document, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode DCF progress: %w", err)
      }
      if err := os.MkdirAll(filepath.Dir(input.ProgressPath), 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create DCF progress directory: %w", err)
      }
      if err := os.WriteFile(input.ProgressPath, append(payload, '\n'), 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write DCF progress %q: %w", input.ProgressPath, err)
      }

      completed := 0
      for _, step := range input.Steps {
        if step.Status == progressCompleted {
          completed++
        }
      }
      output := Output{Path: input.ProgressPath, StepCount: len(input.Steps), CompletedCount: completed}
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateProgressSteps(steps []ProgressStep) []Issue {
      issues := make([]Issue, 0)
      if len(steps) == 0 {
        return append(issues, progressIssue("steps", "steps", "at least one step is required"))
      }
      for index, step := range steps {
        base := fmt.Sprintf("steps[%d]", index)
        if strings.TrimSpace(step.ID) == "" {
          issues = append(issues, progressIssue("step_id", base+".id", "id is required"))
        }
        if strings.TrimSpace(step.Task) == "" {
          issues = append(issues, progressIssue("step_task", base+".task", "task is required"))
        }
        switch step.Status {
        case progressPending:
        case progressCompleted:
          if len(step.Results) == 0 {
            issues = append(issues, progressIssue("step_results", base+".results", "completed step must record results"))
          }
        default:
          issues = append(issues, progressIssue("step_status", base+".status", "status must be pending or completed"))
        }
      }
      return issues
    }

    func readProgressDocument(path string) (ProgressDocument, bool, error) {
      payload, err := os.ReadFile(path)
      if os.IsNotExist(err) {
        return ProgressDocument{}, false, nil
      }
      if err != nil {
        return ProgressDocument{}, false, fmt.Errorf("read DCF progress %q: %w", path, err)
      }
      var document ProgressDocument
      if err := json.Unmarshal(payload, &document); err != nil {
        return ProgressDocument{}, false, fmt.Errorf("decode invalid progress checkpoint %q: %w", path, err)
      }
      return document, true, nil
    }

    func validateProgressTransition(previous, next ProgressDocument) []Issue {
      if previous.SchemaVersion != next.SchemaVersion || previous.Target != next.Target || previous.ValuationDate != next.ValuationDate {
        return []Issue{progressIssue("progress_identity", "steps", "updates must preserve schema, target, and valuation date")}
      }
      if len(previous.Steps) != len(next.Steps) {
        return []Issue{progressIssue("progress_plan", "steps", "updates must keep the same ordered plan")}
      }
      issues := make([]Issue, 0)
      for index := range previous.Steps {
        before, after := previous.Steps[index], next.Steps[index]
        path := fmt.Sprintf("steps[%d]", index)
        if before.ID != after.ID || before.Task != after.Task {
          issues = append(issues, progressIssue("progress_plan", path, "updates must keep the same ordered plan"))
          continue
        }
        if before.Status != progressCompleted {
          continue
        }
        if after.Status != progressCompleted {
          issues = append(issues, progressIssue("progress_status", path+".status", "completed step cannot return to pending"))
        }
        if !containsAllProgressEvidence(before, after) {
          issues = append(issues, progressIssue("progress_evidence", path, "completed step cannot remove recorded evidence"))
        }
      }
      return issues
    }

    func containsAllProgressEvidence(previous, next ProgressStep) bool {
      nextEvidence := make(map[string]struct{})
      for _, evidence := range progressEvidence(next) {
        nextEvidence[evidence] = struct{}{}
      }
      for _, evidence := range progressEvidence(previous) {
        if _, exists := nextEvidence[evidence]; !exists {
          return false
        }
      }
      return true
    }

    func progressEvidence(step ProgressStep) []string {
      evidence := make([]string, 0)
      appendValues := func(field string, values []string) {
        for _, value := range values {
          evidence = append(evidence, field+":"+value)
        }
      }
      appendValues("results", step.Results)
      appendValues("source_ids", step.SourceIDs)
      appendValues("source_urls", step.SourceURLs)
      appendValues("local_references", step.LocalReferences)
      appendValues("calculations", step.Calculations)
      appendValues("assumptions", step.Assumptions)
      return evidence
    }

    func progressIssue(code, path, message string) Issue {
      repair := "Submit the complete current ordered plan without removing completed work."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_dcf_model" {
  description = "Accept one complete dcf-model.v2 payload after the progress checkpoint is complete, then write the combined payload, frozen model, and source list."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "math"
      "net/url"
      "os"
      "path/filepath"
      "sort"
      "strings"
      "time"
    )

    type DCFCompany struct {
      Name     string `json:"name"`
      Ticker   string `json:"ticker"`
      Exchange string `json:"exchange"`
      Currency string `json:"currency"`
    }

    type DCFAssumptions struct {
      WACC           float64 `json:"wacc"`
      TerminalGrowth float64 `json:"terminal_growth"`
    }

    type DCFPeriod struct {
      Period         string  `json:"period"`
      Revenue        float64 `json:"revenue"`
      RevenueGrowth  float64 `json:"revenue_growth"`
      EBIT           float64 `json:"ebit"`
      EBITMargin     float64 `json:"ebit_margin"`
      TaxRate        float64 `json:"tax_rate"`
      NOPAT          float64 `json:"nopat"`
      DA             float64 `json:"da"`
      CapEx          float64 `json:"capex"`
      ChangeNWC      float64 `json:"change_nwc"`
      UFCF           float64 `json:"ufcf"`
      DiscountPeriod float64 `json:"discount_period"`
      DiscountFactor float64 `json:"discount_factor"`
      PVUFCF         float64 `json:"pv_ufcf"`
    }

    type DCFValuation struct {
      PVExplicitFCF        float64 `json:"pv_explicit_fcf"`
      TerminalFCF          float64 `json:"terminal_fcf"`
      TerminalValue        float64 `json:"terminal_value"`
      PVTerminalValue      float64 `json:"pv_terminal_value"`
      EnterpriseValue      float64 `json:"enterprise_value"`
      NetDebt              float64 `json:"net_debt"`
      EquityValue          float64 `json:"equity_value"`
      DilutedShares        float64 `json:"diluted_shares"`
      ImpliedValuePerShare float64 `json:"implied_value_per_share"`
      CurrentPrice         float64 `json:"current_price"`
      ImpliedReturn        float64 `json:"implied_return"`
    }

    type DCFSensitivityPoint struct {
      WACC                 float64 `json:"wacc"`
      TerminalGrowth       float64 `json:"terminal_growth"`
      ImpliedValuePerShare float64 `json:"implied_value_per_share"`
    }

    type DCFSource struct {
      ID            string `json:"id"`
      Title         string `json:"title"`
      URL           string `json:"url"`
      PublishedDate string `json:"published_date"`
      AccessedDate  string `json:"accessed_date"`
    }

    type DCFModel struct {
      SchemaVersion string                `json:"schema_version"`
      Company       DCFCompany            `json:"company"`
      ValuationDate string                `json:"valuation_date"`
      Assumptions   DCFAssumptions        `json:"assumptions"`
      Historical    []DCFPeriod           `json:"historical"`
      Projections   []DCFPeriod           `json:"projections"`
      Valuation     DCFValuation          `json:"valuation"`
      Sensitivity   []DCFSensitivityPoint `json:"sensitivity"`
    }

    type ProgressStep struct {
      Status string `json:"status"`
    }

    type ProgressDocument struct {
      SchemaVersion string         `json:"schema_version"`
      Target        string         `json:"target"`
      ValuationDate string         `json:"valuation_date"`
      Steps         []ProgressStep `json:"steps"`
    }

    type LinkedReverseMarketSnapshot struct {
      Price                        float64  `json:"price"`
      DilutedShares                float64  `json:"diluted_shares"`
      MarketCap                    float64  `json:"market_cap"`
      NetDebt                      float64  `json:"net_debt"`
      MarketImpliedEnterpriseValue float64  `json:"market_implied_enterprise_value"`
      PriceSourceIDs               []string `json:"price_source_ids"`
    }

    type LinkedReverseBaseCase struct {
      EnterpriseValue       float64 `json:"enterprise_value"`
      PVExplicitFCF          float64 `json:"pv_explicit_fcf"`
      ImpliedValuePerShare  float64 `json:"implied_value_per_share"`
      FinalProjectionPeriod string  `json:"final_projection_period"`
      FinalRevenue          float64 `json:"final_revenue"`
      FinalUFCF             float64 `json:"final_ufcf"`
    }

    type LinkedReverseFixedAssumptions struct {
      WACC                   float64 `json:"wacc"`
      TerminalGrowth         float64 `json:"terminal_growth"`
      TerminalDiscountPeriod float64 `json:"terminal_discount_period"`
    }

    type LinkedReverseImpliedExpectations struct {
      TerminalFCF         float64 `json:"terminal_fcf"`
      FinalYearFCF        float64 `json:"final_year_fcf"`
      FCFToModeledRevenue float64 `json:"fcf_to_modeled_revenue"`
      EnterpriseValueGap  float64 `json:"enterprise_value_gap"`
    }

    type LinkedReverseRevenueScenario struct {
      Name                     string  `json:"name"`
      FCFMargin                float64 `json:"fcf_margin"`
      RequiredRevenue          float64 `json:"required_revenue"`
      RevenueMultipleVsModeled float64 `json:"revenue_multiple_vs_modeled"`
      Interpretation           string  `json:"interpretation"`
    }

    type LinkedReverseOptionalityDriver struct {
      Name                      string   `json:"name"`
      CurrentEvidence           string   `json:"current_evidence"`
      CurrentContribution       string   `json:"current_contribution"`
      Stage                     string   `json:"stage"`
      SupportingSourceIDs       []string `json:"supporting_source_ids"`
      RequiredScaleOrMilestones []string `json:"required_scale_or_milestones"`
      Assessment                string   `json:"assessment"`
    }

    type LinkedReverseOptionality struct {
      UnexplainedEnterpriseValue float64                            `json:"unexplained_enterprise_value"`
      Drivers                    []LinkedReverseOptionalityDriver   `json:"drivers"`
      UnprovenRequirements       []string                           `json:"unproven_requirements"`
    }

    type LinkedReverseDCF struct {
      SchemaVersion       string                               `json:"schema_version"`
      ValuationDate       string                               `json:"valuation_date"`
      Currency            string                               `json:"currency"`
      MonetaryUnit        string                               `json:"monetary_unit"`
      MarketSnapshot      LinkedReverseMarketSnapshot          `json:"market_snapshot"`
      BaseCase            LinkedReverseBaseCase                `json:"base_case"`
      FixedAssumptions    LinkedReverseFixedAssumptions        `json:"fixed_assumptions"`
      ImpliedExpectations LinkedReverseImpliedExpectations     `json:"implied_expectations"`
      RevenueScenarios    []LinkedReverseRevenueScenario       `json:"revenue_scenarios"`
      Optionality         LinkedReverseOptionality             `json:"optionality"`
      Conclusion          string                               `json:"conclusion"`
      Limitations         []string                             `json:"limitations"`
    }

    type Input struct {
      CombinedPath  string      `json:"combined_path"`
      ModelPath     string      `json:"model_path"`
      SourcesPath   string      `json:"sources_path"`
      ProgressPath  string      `json:"progress_path"`
      ReverseDCFPath string     `json:"reverse_dcf_path,omitempty"`
      Target        string      `json:"target"`
      ValuationDate string      `json:"valuation_date"`
      Model         DCFModel    `json:"model"`
      Sources       []DCFSource `json:"sources"`
    }

    type DCFOutput struct {
      Model   DCFModel    `json:"model"`
      Sources []DCFSource `json:"sources"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateSubmission(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      outputDocument := DCFOutput{Model: input.Model, Sources: input.Sources}
      combined, err := json.MarshalIndent(outputDocument, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode DCF output: %w", err)
      }
      model, err := json.MarshalIndent(input.Model, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode DCF model: %w", err)
      }
      sources, err := json.MarshalIndent(input.Sources, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode DCF sources: %w", err)
      }
      for _, artifact := range []struct {
        path    string
        payload []byte
      }{
        {input.CombinedPath, combined},
        {input.ModelPath, model},
        {input.SourcesPath, sources},
      } {
        if err := os.MkdirAll(filepath.Dir(artifact.path), 0700); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("create DCF artifact directory: %w", err)
        }
        if err := os.WriteFile(artifact.path, append(artifact.payload, '\n'), 0600); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("write DCF artifact %q: %w", artifact.path, err)
        }
      }
      result := Output(combined)
      return ToolResponse[Output]{Accepted: true, Output: &result}, nil
    }

    func validateSubmission(input Input) []Issue {
      issues := make([]Issue, 0)
      if input.Model.SchemaVersion != "dcf-model.v2" {
        issues = append(issues, modelIssue("schema_version", "model.schema_version", "must equal dcf-model.v2"))
      }
      if input.Model.ValuationDate != input.ValuationDate {
        issues = append(issues, modelIssue("valuation_date", "model.valuation_date", "must equal the workflow valuation date"))
      }
      if strings.TrimSpace(input.Model.Company.Name) == "" || strings.TrimSpace(input.Model.Company.Currency) == "" {
        issues = append(issues, modelIssue("company", "model.company", "name and currency are required"))
      }
      if len(input.Model.Historical) < 3 || len(input.Model.Historical) > 5 {
        issues = append(issues, modelIssue("historical_periods", "model.historical", "must contain 3-5 periods"))
      }
      if len(input.Model.Projections) < 5 || len(input.Model.Projections) > 10 {
        issues = append(issues, modelIssue("projection_periods", "model.projections", "must contain 5-10 periods"))
      }
      issues = append(issues, validateSensitivityGrid(input.Model)...)
      if len(input.Sources) == 0 {
        issues = append(issues, modelIssue("sources", "sources", "must contain at least one source"))
      }
      issues = append(issues, validateSources(input.Sources)...)
      issues = append(issues, validateLinkedReverseDCF(input)...)
      progress, err := os.ReadFile(input.ProgressPath)
      if err != nil {
        issues = append(issues, modelIssue("progress", "progress_path", "completed progress checkpoint is required"))
        return issues
      }
      var document ProgressDocument
      if json.Unmarshal(progress, &document) != nil || document.SchemaVersion != "dcf-progress.v1" || document.Target != input.Target || document.ValuationDate != input.ValuationDate || len(document.Steps) == 0 {
        issues = append(issues, modelIssue("progress", "progress_path", "checkpoint must match target and valuation date"))
        return issues
      }
      for index, step := range document.Steps {
        if step.Status != "completed" {
          issues = append(issues, modelIssue("progress", fmt.Sprintf("progress.steps[%d].status", index), "all progress steps must be completed"))
        }
      }
      return issues
    }

    func validateSources(sources []DCFSource) []Issue {
      issues := make([]Issue, 0)
      knownIDs := make(map[string]struct{}, len(sources))
      for index, source := range sources {
        path := fmt.Sprintf("sources[%d]", index)
        missingRequired := strings.TrimSpace(source.ID) == "" || strings.TrimSpace(source.Title) == "" || strings.TrimSpace(source.URL) == "" || strings.TrimSpace(source.AccessedDate) == ""
        if missingRequired {
          issues = append(issues, modelIssue("source_fields", path, "id, title, URL, and accessed date are required"))
        }
        if source.ID != strings.TrimSpace(source.ID) {
          issues = append(issues, modelIssue("source_id", path+".id", "source ID must not have surrounding whitespace"))
        } else if _, exists := knownIDs[source.ID]; exists {
          issues = append(issues, modelIssue("source_id", path+".id", fmt.Sprintf("duplicate source ID %q", source.ID)))
        } else if source.ID != "" {
          knownIDs[source.ID] = struct{}{}
        }
        parsedURL, err := url.Parse(source.URL)
        validURL := err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
        if strings.TrimSpace(source.URL) != "" && !validURL {
          issues = append(issues, modelIssue("source_url", path+".url", "source URL must be an absolute HTTP or HTTPS URL"))
        }
        if strings.TrimSpace(source.AccessedDate) != "" {
          if _, err := time.Parse("2006-01-02", source.AccessedDate); err != nil {
            issues = append(issues, modelIssue("source_date", path+".accessed_date", "accessed date must use YYYY-MM-DD"))
          }
        }
        if strings.TrimSpace(source.PublishedDate) != "" {
          if _, err := time.Parse("2006-01-02", source.PublishedDate); err != nil {
            issues = append(issues, modelIssue("source_date", path+".published_date", "published date must use YYYY-MM-DD when provided"))
          }
        }
      }
      return issues
    }

    func validateLinkedReverseDCF(input Input) []Issue {
      if strings.TrimSpace(input.ReverseDCFPath) == "" {
        return nil
      }
      payload, err := os.ReadFile(input.ReverseDCFPath)
      if err != nil {
        return []Issue{modelIssue("reverse_artifact", "reverse_dcf_path", "accepted reverse DCF artifact is required before model submission")}
      }
      var reverseDCF LinkedReverseDCF
      if err := json.Unmarshal(payload, &reverseDCF); err != nil {
        return []Issue{modelIssue("reverse_artifact", "reverse_dcf_path", "reverse DCF artifact must contain valid JSON")}
      }

      issues := validateLinkedReverseContract(reverseDCF)
      identityMatches := reverseDCF.SchemaVersion == "reverse-dcf.v1" && reverseDCF.ValuationDate == input.Model.ValuationDate && reverseDCF.Currency == input.Model.Company.Currency
      if !identityMatches {
        issues = append(issues, modelIssue("reverse_model_match", "reverse_dcf_path", "reverse DCF schema, valuation date, and currency must match the model"))
      }
      market := reverseDCF.MarketSnapshot
      valuation := input.Model.Valuation
      marketMatches := modelApproximatelyEqual(market.Price, valuation.CurrentPrice) && modelApproximatelyEqual(market.DilutedShares, valuation.DilutedShares) && modelApproximatelyEqual(market.NetDebt, valuation.NetDebt)
      base := reverseDCF.BaseCase
      baseMatches := modelApproximatelyEqual(base.EnterpriseValue, valuation.EnterpriseValue) && modelApproximatelyEqual(base.PVExplicitFCF, valuation.PVExplicitFCF) && modelApproximatelyEqual(base.ImpliedValuePerShare, valuation.ImpliedValuePerShare)
      assumptions := reverseDCF.FixedAssumptions
      assumptionsMatch := modelApproximatelyEqual(assumptions.WACC, input.Model.Assumptions.WACC) && modelApproximatelyEqual(assumptions.TerminalGrowth, input.Model.Assumptions.TerminalGrowth)
      if len(input.Model.Projections) == 0 {
        baseMatches = false
        assumptionsMatch = false
      } else {
        final := input.Model.Projections[len(input.Model.Projections)-1]
        baseMatches = baseMatches && base.FinalProjectionPeriod == final.Period && modelApproximatelyEqual(base.FinalRevenue, final.Revenue) && modelApproximatelyEqual(base.FinalUFCF, final.UFCF)
        assumptionsMatch = assumptionsMatch && modelApproximatelyEqual(assumptions.TerminalDiscountPeriod, final.DiscountPeriod)
      }
      if !marketMatches || !baseMatches || !assumptionsMatch {
        issues = append(issues, modelIssue("reverse_model_match", "reverse_dcf_path", "reverse DCF market snapshot, base case, and fixed assumptions must match the submitted model"))
      }

      knownSourceIDs := make(map[string]struct{}, len(input.Sources))
      for _, source := range input.Sources {
        knownSourceIDs[source.ID] = struct{}{}
      }
      referencedSourceIDs := append([]string{}, market.PriceSourceIDs...)
      for _, driver := range reverseDCF.Optionality.Drivers {
        referencedSourceIDs = append(referencedSourceIDs, driver.SupportingSourceIDs...)
      }
      for _, sourceID := range referencedSourceIDs {
        if _, exists := knownSourceIDs[sourceID]; !exists {
          issues = append(issues, modelIssue("reverse_source_id", "reverse_dcf_path", fmt.Sprintf("reverse DCF references unknown source ID %q", sourceID)))
        }
      }
      return issues
    }

    func validateLinkedReverseContract(reverseDCF LinkedReverseDCF) []Issue {
      issues := make([]Issue, 0)
      add := func(path, message string) {
        issues = append(issues, modelIssue("reverse_contract", "reverse_dcf."+path, message))
      }
      if reverseDCF.SchemaVersion != "reverse-dcf.v1" {
        add("schema_version", "must equal reverse-dcf.v1")
      }
      missingIdentity := strings.TrimSpace(reverseDCF.ValuationDate) == "" || strings.TrimSpace(reverseDCF.Currency) == "" || strings.TrimSpace(reverseDCF.MonetaryUnit) == ""
      if missingIdentity {
        add("valuation_date", "valuation date, currency, and monetary unit are required")
      }
      market := reverseDCF.MarketSnapshot
      if market.Price <= 0 || market.DilutedShares <= 0 || len(market.PriceSourceIDs) == 0 {
        add("market_snapshot", "positive price, positive diluted shares, and price source IDs are required")
      }
      if !modelApproximatelyEqual(market.MarketCap, market.Price*market.DilutedShares) {
        add("market_snapshot.market_cap", "must equal price multiplied by diluted shares")
      }
      expectedMarketEV := market.MarketCap + market.NetDebt
      if !modelApproximatelyEqual(market.MarketImpliedEnterpriseValue, expectedMarketEV) {
        add("market_snapshot.market_implied_enterprise_value", "must equal market cap plus net debt")
      }
      assumptions := reverseDCF.FixedAssumptions
      invalidAssumptions := assumptions.WACC <= 0 || assumptions.WACC >= 1 || assumptions.TerminalGrowth <= -1 || assumptions.TerminalGrowth >= assumptions.WACC || assumptions.TerminalDiscountPeriod <= 0
      if invalidAssumptions {
        add("fixed_assumptions", "WACC, terminal growth, and terminal discount period must define a valid perpetuity")
      }
      implied := reverseDCF.ImpliedExpectations
      if !modelApproximatelyEqual(implied.EnterpriseValueGap, market.MarketImpliedEnterpriseValue-reverseDCF.BaseCase.EnterpriseValue) {
        add("implied_expectations.enterprise_value_gap", "must equal market-implied EV minus base-case EV")
      }
      if !invalidAssumptions {
        expectedTerminalFCF := (market.MarketImpliedEnterpriseValue - reverseDCF.BaseCase.PVExplicitFCF) * (assumptions.WACC - assumptions.TerminalGrowth) * math.Pow(1+assumptions.WACC, assumptions.TerminalDiscountPeriod)
        if !modelApproximatelyEqual(implied.TerminalFCF, expectedTerminalFCF) {
          add("implied_expectations.terminal_fcf", "must reconcile market-implied EV after preserving PV of explicit cash flows")
        }
      }
      if !modelApproximatelyEqual(implied.TerminalFCF, implied.FinalYearFCF*(1+assumptions.TerminalGrowth)) {
        add("implied_expectations.final_year_fcf", "must reconcile to terminal FCF and terminal growth")
      }
      if reverseDCF.BaseCase.FinalRevenue <= 0 || !modelApproximatelyEqual(implied.FCFToModeledRevenue, implied.FinalYearFCF/reverseDCF.BaseCase.FinalRevenue) {
        add("implied_expectations.fcf_to_modeled_revenue", "must equal implied final-year FCF divided by modeled final-year revenue")
      }
      if len(reverseDCF.RevenueScenarios) < 3 {
        add("revenue_scenarios", "at least three sustainable FCF-margin scenarios are required")
      }
      for index, scenario := range reverseDCF.RevenueScenarios {
        path := fmt.Sprintf("revenue_scenarios[%d]", index)
        missingText := strings.TrimSpace(scenario.Name) == "" || strings.TrimSpace(scenario.Interpretation) == ""
        if scenario.FCFMargin <= 0 || scenario.FCFMargin >= 1 || missingText {
          add(path, "name, interpretation, and an FCF margin between zero and one are required")
          continue
        }
        expectedRevenue := implied.FinalYearFCF / scenario.FCFMargin
        expectedMultiple := expectedRevenue / reverseDCF.BaseCase.FinalRevenue
        if !modelApproximatelyEqual(scenario.RequiredRevenue, expectedRevenue) || !modelApproximatelyEqual(scenario.RevenueMultipleVsModeled, expectedMultiple) {
          add(path, "required revenue and modeled-revenue multiple must reconcile to implied FCF and scenario margin")
        }
      }
      optionality := reverseDCF.Optionality
      if !modelApproximatelyEqual(optionality.UnexplainedEnterpriseValue, implied.EnterpriseValueGap) {
        add("optionality.unexplained_enterprise_value", "must equal the enterprise-value gap")
      }
      if implied.EnterpriseValueGap > 0 && len(optionality.UnprovenRequirements) == 0 {
        add("optionality.unproven_requirements", "a positive valuation gap requires explicit unproven requirements")
      }
      for index, driver := range optionality.Drivers {
        missingText := strings.TrimSpace(driver.Name) == "" || strings.TrimSpace(driver.CurrentEvidence) == "" || strings.TrimSpace(driver.CurrentContribution) == "" || strings.TrimSpace(driver.Stage) == "" || strings.TrimSpace(driver.Assessment) == ""
        missingSupport := len(driver.SupportingSourceIDs) == 0 || len(driver.RequiredScaleOrMilestones) == 0
        if missingText || missingSupport {
          add(fmt.Sprintf("optionality.drivers[%d]", index), "each driver requires evidence, contribution, stage, source IDs, required milestones, and an assessment")
        }
      }
      if strings.TrimSpace(reverseDCF.Conclusion) == "" || len(reverseDCF.Limitations) == 0 {
        add("conclusion", "a conclusion and at least one limitation are required")
      }
      return issues
    }

    func modelApproximatelyEqual(actual, expected float64) bool {
      tolerance := math.Max(0.01, math.Abs(expected)*0.005)
      return math.Abs(actual-expected) <= tolerance
    }

    func validateSensitivityGrid(model DCFModel) []Issue {
      path := "model.sensitivity"
      invalid := func(message string) []Issue {
        return []Issue{modelIssue("sensitivity_grid", path, message)}
      }
      if len(model.Sensitivity) == 0 {
        return invalid("must contain an odd square WACC/terminal-growth grid")
      }

      waccSet := make(map[float64]struct{})
      growthSet := make(map[float64]struct{})
      points := make(map[[2]float64]float64, len(model.Sensitivity))
      for _, point := range model.Sensitivity {
        key := [2]float64{point.WACC, point.TerminalGrowth}
        if _, duplicate := points[key]; duplicate {
          return invalid("must not contain duplicate WACC/terminal-growth points")
        }
        if point.WACC <= 0 || point.TerminalGrowth >= point.WACC {
          return invalid("every point must use positive WACC greater than terminal growth")
        }
        points[key] = point.ImpliedValuePerShare
        waccSet[point.WACC] = struct{}{}
        growthSet[point.TerminalGrowth] = struct{}{}
      }

      side := len(waccSet)
      if side < 3 || side != len(growthSet) || side%2 == 0 || len(points) != side*side {
        return invalid("must contain every point in an odd square WACC/terminal-growth grid of at least 3x3")
      }
      waccs := sortedSensitivityKeys(waccSet)
      growths := sortedSensitivityKeys(growthSet)

      center := side / 2
      if waccs[center] != model.Assumptions.WACC || growths[center] != model.Assumptions.TerminalGrowth {
        return invalid("base WACC and terminal growth must be the exact center point")
      }
      baseValue := points[[2]float64{model.Assumptions.WACC, model.Assumptions.TerminalGrowth}]
      if math.Abs(baseValue-model.Valuation.ImpliedValuePerShare) > 0.01 {
        return invalid("base-case value must equal valuation.implied_value_per_share within rounding tolerance")
      }
      return nil
    }

    func sortedSensitivityKeys(values map[float64]struct{}) []float64 {
      keys := make([]float64, 0, len(values))
      for value := range values {
        keys = append(keys, value)
      }
      sort.Float64s(keys)
      return keys
    }

    func modelIssue(code, path, message string) Issue {
      repair := "Correct the complete DCF payload or progress checkpoint and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_reverse_dcf" {
  description = "Validate and write reverse-dcf.v1 market-implied expectations. All numeric fields must come from the registered calculator."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "math"
      "os"
      "path/filepath"
      "strings"
    )

    type ReverseMarketSnapshot struct {
      Price                        float64  `json:"price"`
      DilutedShares                float64  `json:"diluted_shares"`
      MarketCap                    float64  `json:"market_cap"`
      NetDebt                      float64  `json:"net_debt"`
      MarketImpliedEnterpriseValue float64  `json:"market_implied_enterprise_value"`
      PriceSourceIDs               []string `json:"price_source_ids"`
    }

    type ReverseBaseCase struct {
      EnterpriseValue       float64 `json:"enterprise_value"`
      PVExplicitFCF          float64 `json:"pv_explicit_fcf"`
      ImpliedValuePerShare  float64 `json:"implied_value_per_share"`
      FinalProjectionPeriod string  `json:"final_projection_period"`
      FinalRevenue          float64 `json:"final_revenue"`
      FinalUFCF             float64 `json:"final_ufcf"`
    }

    type ReverseFixedAssumptions struct {
      WACC                   float64 `json:"wacc"`
      TerminalGrowth         float64 `json:"terminal_growth"`
      TerminalDiscountPeriod float64 `json:"terminal_discount_period"`
    }

    type ReverseImpliedExpectations struct {
      TerminalFCF          float64 `json:"terminal_fcf"`
      FinalYearFCF         float64 `json:"final_year_fcf"`
      FCFToModeledRevenue  float64 `json:"fcf_to_modeled_revenue"`
      EnterpriseValueGap   float64 `json:"enterprise_value_gap"`
    }

    type ReverseRevenueScenario struct {
      Name                     string  `json:"name"`
      FCFMargin                float64 `json:"fcf_margin"`
      RequiredRevenue          float64 `json:"required_revenue"`
      RevenueMultipleVsModeled float64 `json:"revenue_multiple_vs_modeled"`
      Interpretation           string  `json:"interpretation"`
    }

    type ReverseOptionalityDriver struct {
      Name                      string   `json:"name"`
      CurrentEvidence           string   `json:"current_evidence"`
      CurrentContribution       string   `json:"current_contribution"`
      Stage                     string   `json:"stage"`
      SupportingSourceIDs       []string `json:"supporting_source_ids"`
      RequiredScaleOrMilestones []string `json:"required_scale_or_milestones"`
      Assessment                string   `json:"assessment"`
    }

    type ReverseOptionality struct {
      UnexplainedEnterpriseValue float64  `json:"unexplained_enterprise_value"`
      Drivers                    []ReverseOptionalityDriver `json:"drivers"`
      UnprovenRequirements       []string `json:"unproven_requirements"`
    }

    type ReverseDCF struct {
      SchemaVersion       string                     `json:"schema_version"`
      ValuationDate       string                     `json:"valuation_date"`
      Currency            string                     `json:"currency"`
      MonetaryUnit        string                     `json:"monetary_unit"`
      MarketSnapshot      ReverseMarketSnapshot      `json:"market_snapshot"`
      BaseCase            ReverseBaseCase            `json:"base_case"`
      FixedAssumptions    ReverseFixedAssumptions    `json:"fixed_assumptions"`
      ImpliedExpectations ReverseImpliedExpectations `json:"implied_expectations"`
      RevenueScenarios    []ReverseRevenueScenario   `json:"revenue_scenarios"`
      Optionality         ReverseOptionality         `json:"optionality"`
      Conclusion          string                     `json:"conclusion"`
      Limitations         []string                   `json:"limitations"`
    }

    type Input struct {
      ReverseDCFPath      string                     `json:"reverse_dcf_path"`
      SchemaVersion       string                     `json:"schema_version"`
      ValuationDate       string                     `json:"valuation_date"`
      Currency            string                     `json:"currency"`
      MonetaryUnit        string                     `json:"monetary_unit"`
      MarketSnapshot      ReverseMarketSnapshot      `json:"market_snapshot"`
      BaseCase            ReverseBaseCase            `json:"base_case"`
      FixedAssumptions    ReverseFixedAssumptions    `json:"fixed_assumptions"`
      ImpliedExpectations ReverseImpliedExpectations `json:"implied_expectations"`
      RevenueScenarios    []ReverseRevenueScenario   `json:"revenue_scenarios"`
      Optionality         ReverseOptionality         `json:"optionality"`
      Conclusion          string                     `json:"conclusion"`
      Limitations         []string                   `json:"limitations"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      issues := validateReverseDCF(input)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      document := ReverseDCF{
        SchemaVersion: input.SchemaVersion, ValuationDate: input.ValuationDate,
        Currency: input.Currency, MonetaryUnit: input.MonetaryUnit,
        MarketSnapshot: input.MarketSnapshot, BaseCase: input.BaseCase,
        FixedAssumptions: input.FixedAssumptions, ImpliedExpectations: input.ImpliedExpectations,
        RevenueScenarios: input.RevenueScenarios, Optionality: input.Optionality,
        Conclusion: input.Conclusion, Limitations: input.Limitations,
      }
      payload, err := json.MarshalIndent(document, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode reverse DCF: %w", err)
      }
      if err := os.MkdirAll(filepath.Dir(input.ReverseDCFPath), 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create reverse DCF directory: %w", err)
      }
      if err := os.WriteFile(input.ReverseDCFPath, append(payload, '\n'), 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write reverse DCF %q: %w", input.ReverseDCFPath, err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateReverseDCF(input Input) []Issue {
      issues := make([]Issue, 0)
      if strings.TrimSpace(input.ReverseDCFPath) == "" {
        issues = append(issues, reverseIssue("reverse_path", "reverse_dcf_path", "workflow-owned path is required"))
      }
      if input.SchemaVersion != "reverse-dcf.v1" {
        issues = append(issues, reverseIssue("reverse_schema", "schema_version", "must equal reverse-dcf.v1"))
      }
      missingIdentity := strings.TrimSpace(input.ValuationDate) == "" || strings.TrimSpace(input.Currency) == "" || strings.TrimSpace(input.MonetaryUnit) == ""
      if missingIdentity {
        issues = append(issues, reverseIssue("reverse_identity", "valuation_date", "valuation date, currency, and monetary unit are required"))
      }
      market := input.MarketSnapshot
      if market.Price <= 0 || market.DilutedShares <= 0 || len(market.PriceSourceIDs) == 0 {
        issues = append(issues, reverseIssue("reverse_market_snapshot", "market_snapshot", "positive price, positive diluted shares, and price source IDs are required"))
      }
      if !approximatelyEqual(market.MarketCap, market.Price*market.DilutedShares) {
        issues = append(issues, reverseIssue("reverse_market_cap", "market_snapshot.market_cap", "must equal price multiplied by diluted shares in the declared unit"))
      }
      expectedMarketEV := market.MarketCap + market.NetDebt
      if !approximatelyEqual(market.MarketImpliedEnterpriseValue, expectedMarketEV) {
        issues = append(issues, reverseIssue("reverse_market_ev", "market_snapshot.market_implied_enterprise_value", "must equal market cap plus net debt; net cash is negative net debt"))
      }
      assumptions := input.FixedAssumptions
      invalidAssumptions := assumptions.WACC <= 0 || assumptions.WACC >= 1 || assumptions.TerminalGrowth <= -1 || assumptions.TerminalGrowth >= assumptions.WACC || assumptions.TerminalDiscountPeriod <= 0
      if invalidAssumptions {
        issues = append(issues, reverseIssue("reverse_assumptions", "fixed_assumptions", "WACC, terminal growth, and terminal discount period must define a valid perpetuity"))
      }
      implied := input.ImpliedExpectations
      if !approximatelyEqual(implied.EnterpriseValueGap, market.MarketImpliedEnterpriseValue-input.BaseCase.EnterpriseValue) {
        issues = append(issues, reverseIssue("reverse_ev_gap", "implied_expectations.enterprise_value_gap", "must equal market-implied EV minus base-case EV"))
      }
      if !invalidAssumptions {
        expectedTerminalFCF := (market.MarketImpliedEnterpriseValue - input.BaseCase.PVExplicitFCF) * (assumptions.WACC - assumptions.TerminalGrowth) * math.Pow(1+assumptions.WACC, assumptions.TerminalDiscountPeriod)
        if !approximatelyEqual(implied.TerminalFCF, expectedTerminalFCF) {
          issues = append(issues, reverseIssue("reverse_implied_fcf", "implied_expectations.terminal_fcf", "must reconcile market-implied EV after preserving PV of explicit cash flows"))
        }
      }
      if !approximatelyEqual(implied.TerminalFCF, implied.FinalYearFCF*(1+assumptions.TerminalGrowth)) {
        issues = append(issues, reverseIssue("reverse_terminal_fcf", "implied_expectations.terminal_fcf", "must equal final-year FCF grown by the terminal growth rate"))
      }
      if input.BaseCase.FinalRevenue <= 0 || !approximatelyEqual(implied.FCFToModeledRevenue, implied.FinalYearFCF/input.BaseCase.FinalRevenue) {
        issues = append(issues, reverseIssue("reverse_implied_margin", "implied_expectations.fcf_to_modeled_revenue", "must equal implied final-year FCF divided by modeled final-year revenue"))
      }
      if len(input.RevenueScenarios) < 3 {
        issues = append(issues, reverseIssue("reverse_revenue_scenarios", "revenue_scenarios", "at least three sustainable FCF-margin scenarios are required"))
      }
      for index, scenario := range input.RevenueScenarios {
        path := fmt.Sprintf("revenue_scenarios[%d]", index)
        missingText := strings.TrimSpace(scenario.Name) == "" || strings.TrimSpace(scenario.Interpretation) == ""
        if scenario.FCFMargin <= 0 || scenario.FCFMargin >= 1 || missingText {
          issues = append(issues, reverseIssue("reverse_revenue_scenario", path, "name, interpretation, and an FCF margin between zero and one are required"))
          continue
        }
        expectedRevenue := implied.FinalYearFCF / scenario.FCFMargin
        expectedMultiple := expectedRevenue / input.BaseCase.FinalRevenue
        if !approximatelyEqual(scenario.RequiredRevenue, expectedRevenue) || !approximatelyEqual(scenario.RevenueMultipleVsModeled, expectedMultiple) {
          issues = append(issues, reverseIssue("reverse_revenue_scenario", path, "required revenue and modeled-revenue multiple must reconcile to implied FCF and scenario margin"))
        }
      }
      if !approximatelyEqual(input.Optionality.UnexplainedEnterpriseValue, implied.EnterpriseValueGap) {
        issues = append(issues, reverseIssue("reverse_optionality_gap", "optionality.unexplained_enterprise_value", "must equal the enterprise-value gap"))
      }
      if implied.EnterpriseValueGap > 0 && len(input.Optionality.UnprovenRequirements) == 0 {
        issues = append(issues, reverseIssue("reverse_optionality", "optionality.unproven_requirements", "a positive valuation gap requires explicit unproven requirements"))
      }
      for index, driver := range input.Optionality.Drivers {
        missingText := strings.TrimSpace(driver.Name) == "" || strings.TrimSpace(driver.CurrentEvidence) == "" || strings.TrimSpace(driver.CurrentContribution) == "" || strings.TrimSpace(driver.Stage) == "" || strings.TrimSpace(driver.Assessment) == ""
        missingSupport := len(driver.SupportingSourceIDs) == 0 || len(driver.RequiredScaleOrMilestones) == 0
        if missingText || missingSupport {
          path := fmt.Sprintf("optionality.drivers[%d]", index)
          issues = append(issues, reverseIssue("reverse_optionality_driver", path, "each driver requires current evidence, contribution, stage, source IDs, required milestones, and an assessment"))
        }
      }
      if strings.TrimSpace(input.Conclusion) == "" || len(input.Limitations) == 0 {
        issues = append(issues, reverseIssue("reverse_conclusion", "conclusion", "a conclusion and at least one limitation are required"))
      }
      return issues
    }

    func approximatelyEqual(actual, expected float64) bool {
      tolerance := math.Max(0.01, math.Abs(expected)*0.005)
      return math.Abs(actual-expected) <= tolerance
    }

    func reverseIssue(code, path, message string) Issue {
      repair := "Correct all fields with the calculator result and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_dcf_juror_opinion" {
  description = "Submit one persona's structured review of the frozen DCF. Verdict allowed values: accept, revise, reject, abstain. Confidence must be between 0 and 1."

  source = <<-GO
    import (
      "context"
      "encoding/json"
      "fmt"
      "os"
      "path/filepath"
      "strings"
    )

    type DCFJurorFinding struct {
      Severity   string   `json:"severity"`
      Category   string   `json:"category"`
      Message    string   `json:"message"`
      ModelPaths []string `json:"model_paths"`
    }

    type Input struct {
      OpinionPath string             `json:"opinion_path"`
      JurorID     string             `json:"juror_id"`
      Verdict     string             `json:"verdict"`
      Confidence  float64            `json:"confidence"`
      Summary     string             `json:"summary"`
      Findings    []DCFJurorFinding `json:"findings"`
    }

    type Opinion struct {
      JurorID    string             `json:"juror_id"`
      Verdict    string             `json:"verdict"`
      Confidence float64            `json:"confidence"`
      Summary    string             `json:"summary"`
      Findings   []DCFJurorFinding `json:"findings"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      opinion := Opinion{
        JurorID: input.JurorID, Verdict: input.Verdict, Confidence: input.Confidence,
        Summary: input.Summary, Findings: input.Findings,
      }
      issues := validateOpinion(opinion)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }
      payload, err := json.MarshalIndent(opinion, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode DCF juror opinion: %w", err)
      }
      if err := os.MkdirAll(filepath.Dir(input.OpinionPath), 0700); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("create DCF juror directory: %w", err)
      }
      if err := os.WriteFile(input.OpinionPath, append(payload, '\n'), 0600); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("write DCF juror opinion: %w", err)
      }
      output := Output(payload)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateOpinion(opinion Opinion) []Issue {
      issues := make([]Issue, 0)
      if strings.TrimSpace(opinion.JurorID) == "" {
        issues = append(issues, opinionIssue("juror_id", "juror_id", "juror ID is required"))
      }
      switch opinion.Verdict {
      case "accept", "revise", "reject", "abstain":
      default:
        issues = append(issues, opinionIssue("juror_verdict", "verdict", "must be accept, revise, reject, or abstain"))
      }
      if opinion.Confidence < 0 || opinion.Confidence > 1 {
        issues = append(issues, opinionIssue("juror_confidence", "confidence", "must be between zero and one"))
      }
      if strings.TrimSpace(opinion.Summary) == "" {
        issues = append(issues, opinionIssue("juror_summary", "summary", "summary is required"))
      }
      return issues
    }

    func opinionIssue(code, path, message string) Issue {
      repair := "Correct the structured opinion and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}

go_tool "submit_dcf_report" {
  description = "Accept the structured synthesis and render the original SecJury DCF report layout from the frozen model, sources, and ordered juror opinions. Decision allowed values: accept, revise, reject."

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

    type DCFCompany struct {
      Name     string `json:"name"`
      Ticker   string `json:"ticker"`
      Exchange string `json:"exchange"`
      Currency string `json:"currency"`
    }

    type DCFAssumptions struct {
      WACC           float64 `json:"wacc"`
      TerminalGrowth float64 `json:"terminal_growth"`
    }

    type DCFPeriod struct {
      Period         string  `json:"period"`
      Revenue        float64 `json:"revenue"`
      RevenueGrowth  float64 `json:"revenue_growth"`
      EBIT           float64 `json:"ebit"`
      EBITMargin     float64 `json:"ebit_margin"`
      TaxRate        float64 `json:"tax_rate"`
      NOPAT          float64 `json:"nopat"`
      DA             float64 `json:"da"`
      CapEx          float64 `json:"capex"`
      ChangeNWC      float64 `json:"change_nwc"`
      UFCF           float64 `json:"ufcf"`
      DiscountPeriod float64 `json:"discount_period"`
      DiscountFactor float64 `json:"discount_factor"`
      PVUFCF         float64 `json:"pv_ufcf"`
    }

    type DCFValuation struct {
      PVExplicitFCF        float64 `json:"pv_explicit_fcf"`
      TerminalFCF          float64 `json:"terminal_fcf"`
      TerminalValue        float64 `json:"terminal_value"`
      PVTerminalValue      float64 `json:"pv_terminal_value"`
      EnterpriseValue      float64 `json:"enterprise_value"`
      NetDebt              float64 `json:"net_debt"`
      EquityValue          float64 `json:"equity_value"`
      DilutedShares        float64 `json:"diluted_shares"`
      ImpliedValuePerShare float64 `json:"implied_value_per_share"`
      CurrentPrice         float64 `json:"current_price"`
      ImpliedReturn        float64 `json:"implied_return"`
    }

    type DCFSensitivityPoint struct {
      WACC                 float64 `json:"wacc"`
      TerminalGrowth       float64 `json:"terminal_growth"`
      ImpliedValuePerShare float64 `json:"implied_value_per_share"`
    }

    type DCFModel struct {
      SchemaVersion string                `json:"schema_version"`
      Company       DCFCompany            `json:"company"`
      ValuationDate string                `json:"valuation_date"`
      Assumptions   DCFAssumptions        `json:"assumptions"`
      Historical    []DCFPeriod           `json:"historical"`
      Projections   []DCFPeriod           `json:"projections"`
      Valuation     DCFValuation          `json:"valuation"`
      Sensitivity   []DCFSensitivityPoint `json:"sensitivity"`
    }

    type DCFSource struct {
      ID            string `json:"id"`
      Title         string `json:"title"`
      URL           string `json:"url"`
      PublishedDate string `json:"published_date"`
      AccessedDate  string `json:"accessed_date"`
    }

    type ReverseMarketSnapshot struct {
      Price                        float64  `json:"price"`
      DilutedShares                float64  `json:"diluted_shares"`
      MarketCap                    float64  `json:"market_cap"`
      NetDebt                      float64  `json:"net_debt"`
      MarketImpliedEnterpriseValue float64  `json:"market_implied_enterprise_value"`
      PriceSourceIDs               []string `json:"price_source_ids"`
    }

    type ReverseBaseCase struct {
      EnterpriseValue       float64 `json:"enterprise_value"`
      PVExplicitFCF          float64 `json:"pv_explicit_fcf"`
      ImpliedValuePerShare  float64 `json:"implied_value_per_share"`
      FinalProjectionPeriod string  `json:"final_projection_period"`
      FinalRevenue          float64 `json:"final_revenue"`
      FinalUFCF             float64 `json:"final_ufcf"`
    }

    type ReverseFixedAssumptions struct {
      WACC                   float64 `json:"wacc"`
      TerminalGrowth         float64 `json:"terminal_growth"`
      TerminalDiscountPeriod float64 `json:"terminal_discount_period"`
    }

    type ReverseImpliedExpectations struct {
      TerminalFCF         float64 `json:"terminal_fcf"`
      FinalYearFCF        float64 `json:"final_year_fcf"`
      FCFToModeledRevenue float64 `json:"fcf_to_modeled_revenue"`
      EnterpriseValueGap  float64 `json:"enterprise_value_gap"`
    }

    type ReverseRevenueScenario struct {
      Name                     string  `json:"name"`
      FCFMargin                float64 `json:"fcf_margin"`
      RequiredRevenue          float64 `json:"required_revenue"`
      RevenueMultipleVsModeled float64 `json:"revenue_multiple_vs_modeled"`
      Interpretation           string  `json:"interpretation"`
    }

    type ReverseOptionalityDriver struct {
      Name                      string   `json:"name"`
      CurrentEvidence           string   `json:"current_evidence"`
      CurrentContribution       string   `json:"current_contribution"`
      Stage                     string   `json:"stage"`
      SupportingSourceIDs       []string `json:"supporting_source_ids"`
      RequiredScaleOrMilestones []string `json:"required_scale_or_milestones"`
      Assessment                string   `json:"assessment"`
    }

    type ReverseOptionality struct {
      UnexplainedEnterpriseValue float64  `json:"unexplained_enterprise_value"`
      Drivers                    []ReverseOptionalityDriver `json:"drivers"`
      UnprovenRequirements       []string `json:"unproven_requirements"`
    }

    type ReverseDCF struct {
      SchemaVersion       string                     `json:"schema_version"`
      ValuationDate       string                     `json:"valuation_date"`
      Currency            string                     `json:"currency"`
      MonetaryUnit        string                     `json:"monetary_unit"`
      MarketSnapshot      ReverseMarketSnapshot      `json:"market_snapshot"`
      BaseCase            ReverseBaseCase            `json:"base_case"`
      FixedAssumptions    ReverseFixedAssumptions    `json:"fixed_assumptions"`
      ImpliedExpectations ReverseImpliedExpectations `json:"implied_expectations"`
      RevenueScenarios    []ReverseRevenueScenario   `json:"revenue_scenarios"`
      Optionality         ReverseOptionality         `json:"optionality"`
      Conclusion          string                     `json:"conclusion"`
      Limitations         []string                   `json:"limitations"`
    }

    type DCFJurorFinding struct {
      Severity   string   `json:"severity"`
      Category   string   `json:"category"`
      Message    string   `json:"message"`
      ModelPaths []string `json:"model_paths"`
    }

    type DCFJurorOpinion struct {
      JurorID    string             `json:"juror_id"`
      Verdict    string             `json:"verdict"`
      Confidence float64            `json:"confidence"`
      Summary    string             `json:"summary"`
      Findings   []DCFJurorFinding `json:"findings"`
    }

    type DCFPersona struct {
      ID            string   `json:"id"`
      Name          string   `json:"name"`
      Group         string   `json:"group"`
      LensName      string   `json:"lens_name"`
      PlainQuestion string   `json:"plain_question"`
      Mandate       string   `json:"mandate"`
      RequiredTests []string `json:"required_tests"`
      OutOfScope    []string `json:"out_of_scope"`
      DecisionRule  string   `json:"decision_rule"`
    }

    type DCFReport struct {
      Decision    string   `json:"decision"`
      Headline    string   `json:"headline"`
      Summary     string   `json:"summary"`
      KeyFindings []string `json:"key_findings"`
      Limitations []string `json:"limitations"`
    }

    type Input struct {
      ReportJSONPath string       `json:"report_json_path"`
      ReportPath     string       `json:"report_path"`
      ModelPath      string       `json:"model_path"`
      SourcesPath    string       `json:"sources_path"`
      ReverseDCFPath string       `json:"reverse_dcf_path"`
      OpinionPaths   []string     `json:"opinion_paths"`
      Jurors         []DCFPersona `json:"jurors"`
      Decision       string       `json:"decision"`
      Headline       string       `json:"headline"`
      Summary        string       `json:"summary"`
      KeyFindings    []string     `json:"key_findings"`
      Limitations    []string     `json:"limitations"`
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      report := DCFReport{
        Decision: input.Decision, Headline: input.Headline, Summary: input.Summary,
        KeyFindings: input.KeyFindings, Limitations: input.Limitations,
      }
      issues := validateReport(report)
      if len(issues) > 0 {
        return ToolResponse[Output]{Accepted: false, Issues: issues}, nil
      }

      var model DCFModel
      if err := readStrictJSON(input.ModelPath, &model); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read frozen DCF model: %w", err)
      }
      var sources []DCFSource
      if err := readStrictJSON(input.SourcesPath, &sources); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read frozen DCF sources: %w", err)
      }
      var reverseDCF ReverseDCF
      if err := readStrictJSON(input.ReverseDCFPath, &reverseDCF); err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("read reverse DCF: %w", err)
      }
      opinions := make([]DCFJurorOpinion, 0, len(input.OpinionPaths))
      for _, path := range input.OpinionPaths {
        var opinion DCFJurorOpinion
        if err := readStrictJSON(path, &opinion); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("read DCF juror opinion %q: %w", path, err)
        }
        opinions = append(opinions, opinion)
      }

      reportJSON, err := json.MarshalIndent(report, "", "  ")
      if err != nil {
        return ToolResponse[Output]{}, fmt.Errorf("encode DCF report: %w", err)
      }
      markdown := renderDCFReport(model, sources, reverseDCF, input.Jurors, opinions, report)
      for _, artifact := range []struct {
        path    string
        payload []byte
      }{
        {input.ReportJSONPath, append(reportJSON, '\n')},
        {input.ReportPath, []byte(markdown)},
      } {
        if err := os.MkdirAll(filepath.Dir(artifact.path), 0700); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("create DCF report directory: %w", err)
        }
        if err := os.WriteFile(artifact.path, artifact.payload, 0600); err != nil {
          return ToolResponse[Output]{}, fmt.Errorf("write DCF report %q: %w", artifact.path, err)
        }
      }
      output := Output(reportJSON)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }

    func validateReport(report DCFReport) []Issue {
      issues := make([]Issue, 0)
      switch report.Decision {
      case "accept", "revise", "reject":
      default:
        issues = append(issues, reportIssue("report_decision", "decision", "must be accept, revise, or reject"))
      }
      if strings.TrimSpace(report.Headline) == "" || strings.TrimSpace(report.Summary) == "" {
        issues = append(issues, reportIssue("report_text", "summary", "headline and summary are required"))
      }
      return issues
    }

    func readStrictJSON(path string, target interface{}) error {
      payload, err := os.ReadFile(path)
      if err != nil {
        return err
      }
      decoder := json.NewDecoder(bytes.NewReader(payload))
      decoder.DisallowUnknownFields()
      return decoder.Decode(target)
    }

    func renderDCFReport(model DCFModel, sources []DCFSource, reverseDCF ReverseDCF, personas []DCFPersona, opinions []DCFJurorOpinion, report DCFReport) string {
      var output strings.Builder
      fmt.Fprintf(&output, "# %s\n\n%s\n\n**Decision:** %s\n", markdownCell(report.Headline), markdownCell(report.Summary), markdownCell(report.Decision))
      if len(report.KeyFindings) > 0 {
        output.WriteString("\n## Key Findings\n\n")
        for _, finding := range report.KeyFindings {
          fmt.Fprintf(&output, "- %s\n", markdownCell(finding))
        }
      }
      if len(report.Limitations) > 0 {
        output.WriteString("\n## Limitations\n\n")
        for _, limitation := range report.Limitations {
          fmt.Fprintf(&output, "- %s\n", markdownCell(limitation))
        }
      }
      renderHumanDCFModel(&output, model, sources, reverseDCF)
      personasByID := make(map[string]DCFPersona, len(personas))
      for _, persona := range personas {
        personasByID[persona.ID] = persona
      }
      output.WriteString("\n## Jury Opinions\n\n")
      output.WriteString("Celebrity names are familiar analytical mnemonics for the review lenses below. The real people did not participate in or endorse this report.\n")
      for _, opinion := range opinions {
        persona := personasByID[opinion.JurorID]
        name := strings.TrimSpace(persona.Name)
        if name == "" {
          name = opinion.JurorID
        }
        lensName := strings.TrimSpace(persona.LensName)
        if lensName == "" {
          lensName = opinion.JurorID
        }
        fmt.Fprintf(&output, "\n### %s · %s\n", markdownCell(name), markdownCell(lensName))
        if question := strings.TrimSpace(persona.PlainQuestion); question != "" {
          fmt.Fprintf(&output, "\n**Question this role represents:** %s\n", markdownCell(question))
        }
        fmt.Fprintf(&output, "\n**Review result:** %s  \n**Confidence:** %.0f%%\n\n**Plain-language takeaway:** %s\n", markdownCell(opinion.Verdict), opinion.Confidence*100, markdownCell(opinion.Summary))
        if len(opinion.Findings) == 0 {
          output.WriteString("\n_No specific findings._\n")
          continue
        }
        output.WriteString("\n| Severity | Category | Finding | Model paths |\n|---|---|---|---|\n")
        for _, finding := range opinion.Findings {
          fmt.Fprintf(&output, "| %s | %s | %s | %s |\n", markdownCell(finding.Severity), markdownCell(finding.Category), markdownCell(finding.Message), markdownCell(strings.Join(finding.ModelPaths, ", ")))
        }
      }
      return output.String()
    }

    func renderHumanDCFModel(output *strings.Builder, model DCFModel, sources []DCFSource, reverseDCF ReverseDCF) {
      output.WriteString("\n## DCF Model\n\n### Company\n\n| Field | Value |\n|---|---|\n")
      for _, row := range [][2]string{
        {"Name", model.Company.Name}, {"Ticker", model.Company.Ticker},
        {"Exchange", model.Company.Exchange}, {"Currency", model.Company.Currency},
        {"Valuation date", model.ValuationDate},
      } {
        fmt.Fprintf(output, "| %s | %s |\n", row[0], markdownCell(row[1]))
      }
      output.WriteString("\n### Assumptions\n\n| Assumption | Value |\n|---|---:|\n")
      fmt.Fprintf(output, "| WACC | %s |\n| Terminal growth | %s |\n", formatPercent(model.Assumptions.WACC), formatPercent(model.Assumptions.TerminalGrowth))

      output.WriteString("\n### Historical Financials\n\n")
      if len(model.Historical) == 0 {
        output.WriteString("_No historical periods supplied._\n")
      } else {
        output.WriteString("| Period | Revenue | Growth | EBIT | EBIT margin | Tax | NOPAT | D&A | CapEx | Change NWC | UFCF |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
        for _, period := range model.Historical {
          renderDCFPeriodRow(output, period)
        }
      }

      output.WriteString("\n### Projected Cash Flow\n\n")
      if len(model.Projections) == 0 {
        output.WriteString("_No projection periods supplied._\n")
      } else {
        output.WriteString("| Period | Revenue | Growth | EBIT | EBIT margin | Tax | NOPAT | D&A | CapEx | Change NWC | UFCF | Discount period | Discount factor | PV UFCF |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
        for _, period := range model.Projections {
          fmt.Fprintf(output, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %.2f | %.4f | %s |\n", markdownCell(period.Period), formatNumber(period.Revenue), formatPercent(period.RevenueGrowth), formatNumber(period.EBIT), formatPercent(period.EBITMargin), formatPercent(period.TaxRate), formatNumber(period.NOPAT), formatNumber(period.DA), formatNumber(period.CapEx), formatNumber(period.ChangeNWC), formatNumber(period.UFCF), period.DiscountPeriod, period.DiscountFactor, formatNumber(period.PVUFCF))
        }
      }

      output.WriteString("\n### Valuation Summary\n\n| Component | Value |\n|---|---:|\n")
      for _, row := range []struct {
        label string
        value float64
      }{
        {"PV explicit FCF", model.Valuation.PVExplicitFCF},
        {"Terminal FCF", model.Valuation.TerminalFCF},
        {"Terminal value", model.Valuation.TerminalValue},
        {"PV terminal value", model.Valuation.PVTerminalValue},
        {"Enterprise value", model.Valuation.EnterpriseValue},
        {"Net debt", model.Valuation.NetDebt},
        {"Equity value", model.Valuation.EquityValue},
        {"Diluted shares", model.Valuation.DilutedShares},
        {"Implied value per share", model.Valuation.ImpliedValuePerShare},
        {"Current price", model.Valuation.CurrentPrice},
      } {
        fmt.Fprintf(output, "| %s | %s |\n", row.label, formatNumber(row.value))
      }
      fmt.Fprintf(output, "| Implied return | %s |\n", formatPercent(model.Valuation.ImpliedReturn))
      renderReverseDCF(output, reverseDCF)
      renderSensitivityTable(output, model.Sensitivity)

      output.WriteString("\n### Sources\n\n")
      if len(sources) == 0 {
        output.WriteString("_No sources supplied._\n")
      } else {
        output.WriteString("| ID | Title | URL | Published | Accessed |\n|---|---|---|---|---|\n")
        for _, source := range sources {
          fmt.Fprintf(output, "| %s | %s | %s | %s | %s |\n", markdownCell(source.ID), markdownCell(source.Title), markdownCell(source.URL), markdownCell(source.PublishedDate), markdownCell(source.AccessedDate))
        }
      }
    }

    func renderReverseDCF(output *strings.Builder, reverseDCF ReverseDCF) {
      output.WriteString("\n## Market-Implied Expectations (Reverse DCF)\n\n")
      output.WriteString("This section does not change the evidence-supported base case. It asks what operating outcome the current market price would require.\n\n")
      output.WriteString("### Market Price Bridge\n\n| Item | Value |\n|---|---:|\n")
      for _, row := range []struct {
        label string
        value float64
      }{
        {"Current price", reverseDCF.MarketSnapshot.Price},
        {"Diluted shares", reverseDCF.MarketSnapshot.DilutedShares},
        {"Market capitalization", reverseDCF.MarketSnapshot.MarketCap},
        {"Net debt (negative means net cash)", reverseDCF.MarketSnapshot.NetDebt},
        {"Market-implied enterprise value", reverseDCF.MarketSnapshot.MarketImpliedEnterpriseValue},
        {"Base-model enterprise value", reverseDCF.BaseCase.EnterpriseValue},
        {"PV of explicit cash flows", reverseDCF.BaseCase.PVExplicitFCF},
        {"Enterprise-value gap", reverseDCF.ImpliedExpectations.EnterpriseValueGap},
      } {
        fmt.Fprintf(output, "| %s | %s |\n", row.label, formatNumber(row.value))
      }
      fmt.Fprintf(output, "\nCurrency: %s; monetary values use %s unless stated otherwise.\n", markdownCell(reverseDCF.Currency), markdownCell(reverseDCF.MonetaryUnit))

      output.WriteString("\n### What the Market Price Requires\n\n| Item | Value |\n|---|---:|\n")
      fmt.Fprintf(output, "| Fixed WACC | %s |\n", formatPercent(reverseDCF.FixedAssumptions.WACC))
      fmt.Fprintf(output, "| Fixed terminal growth | %s |\n", formatPercent(reverseDCF.FixedAssumptions.TerminalGrowth))
      fmt.Fprintf(output, "| Terminal discount period | %.2f |\n", reverseDCF.FixedAssumptions.TerminalDiscountPeriod)
      fmt.Fprintf(output, "| Implied terminal FCF | %s |\n", formatNumber(reverseDCF.ImpliedExpectations.TerminalFCF))
      fmt.Fprintf(output, "| Implied final-year FCF | %s |\n", formatNumber(reverseDCF.ImpliedExpectations.FinalYearFCF))
      fmt.Fprintf(output, "| Modeled final-year revenue | %s |\n", formatNumber(reverseDCF.BaseCase.FinalRevenue))
      fmt.Fprintf(output, "| Implied FCF / modeled revenue | %s |\n", formatPercent(reverseDCF.ImpliedExpectations.FCFToModeledRevenue))

      output.WriteString("\n### Revenue Scale Required\n\n| Sustainable FCF margin | Required revenue | Multiple of modeled revenue | Interpretation |\n|---|---:|---:|---|\n")
      for _, scenario := range reverseDCF.RevenueScenarios {
        fmt.Fprintf(output, "| %s | %s | %.2fx | %s |\n", markdownCell(scenario.Name), formatNumber(scenario.RequiredRevenue), scenario.RevenueMultipleVsModeled, markdownCell(scenario.Interpretation))
      }

      output.WriteString("\n### Optionality Gap\n\n")
      fmt.Fprintf(output, "Unexplained enterprise value: **%s**\n", formatNumber(reverseDCF.Optionality.UnexplainedEnterpriseValue))
      for _, driver := range reverseDCF.Optionality.Drivers {
        fmt.Fprintf(output, "\n#### %s\n\n", markdownCell(driver.Name))
        fmt.Fprintf(output, "**Current evidence:** %s  \n", markdownCell(driver.CurrentEvidence))
        fmt.Fprintf(output, "**Current contribution:** %s  \n", markdownCell(driver.CurrentContribution))
        fmt.Fprintf(output, "**Stage:** %s  \n", markdownCell(driver.Stage))
        fmt.Fprintf(output, "**Assessment:** %s\n", markdownCell(driver.Assessment))
        renderReverseList(output, "Supporting source IDs", driver.SupportingSourceIDs)
        renderReverseList(output, "Required scale or milestones", driver.RequiredScaleOrMilestones)
      }
      renderReverseList(output, "What remains unproven", reverseDCF.Optionality.UnprovenRequirements)
      fmt.Fprintf(output, "\n**Conclusion:** %s\n", markdownCell(reverseDCF.Conclusion))
      renderReverseList(output, "Reverse-DCF limitations", reverseDCF.Limitations)
    }

    func renderReverseList(output *strings.Builder, label string, values []string) {
      fmt.Fprintf(output, "\n**%s:**\n", label)
      if len(values) == 0 {
        output.WriteString("\n- None identified in the frozen packet.\n")
        return
      }
      output.WriteString("\n")
      for _, value := range values {
        fmt.Fprintf(output, "- %s\n", markdownCell(value))
      }
    }

    func renderDCFPeriodRow(output *strings.Builder, period DCFPeriod) {
      fmt.Fprintf(output, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", markdownCell(period.Period), formatNumber(period.Revenue), formatPercent(period.RevenueGrowth), formatNumber(period.EBIT), formatPercent(period.EBITMargin), formatPercent(period.TaxRate), formatNumber(period.NOPAT), formatNumber(period.DA), formatNumber(period.CapEx), formatNumber(period.ChangeNWC), formatNumber(period.UFCF))
    }

    func renderSensitivityTable(output *strings.Builder, points []DCFSensitivityPoint) {
      output.WriteString("\n### Sensitivity Analysis\n\n")
      if len(points) == 0 {
        output.WriteString("_No sensitivity points supplied._\n")
        return
      }
      waccSet := map[float64]struct{}{}
      growthSet := map[float64]struct{}{}
      values := map[[2]float64]float64{}
      for _, point := range points {
        waccSet[point.WACC] = struct{}{}
        growthSet[point.TerminalGrowth] = struct{}{}
        values[[2]float64{point.WACC, point.TerminalGrowth}] = point.ImpliedValuePerShare
      }
      waccs := sortedFloatKeys(waccSet)
      growths := sortedFloatKeys(growthSet)
      output.WriteString("| WACC / Terminal growth |")
      for _, growth := range growths {
        fmt.Fprintf(output, " %s |", formatPercent(growth))
      }
      output.WriteString("\n|---|")
      for range growths {
        output.WriteString("---:|")
      }
      output.WriteString("\n")
      for _, wacc := range waccs {
        fmt.Fprintf(output, "| %s |", formatPercent(wacc))
        for _, growth := range growths {
          value, exists := values[[2]float64{wacc, growth}]
          if exists {
            fmt.Fprintf(output, " %s |", formatNumber(value))
          } else {
            output.WriteString(" - |")
          }
        }
        output.WriteString("\n")
      }
    }

    func sortedFloatKeys(values map[float64]struct{}) []float64 {
      keys := make([]float64, 0, len(values))
      for value := range values {
        keys = append(keys, value)
      }
      sort.Float64s(keys)
      return keys
    }

    func formatNumber(value float64) string {
      return fmt.Sprintf("%.2f", value)
    }

    func formatPercent(value float64) string {
      return fmt.Sprintf("%.1f%%", value*100)
    }

    func markdownCell(value string) string {
      return strings.NewReplacer("\\", "\\\\", "|", "\\|", "\r", " ", "\n", " ").Replace(strings.TrimSpace(value))
    }

    func reportIssue(code, path, message string) Issue {
      repair := "Correct the structured synthesis and call the tool again."
      return Issue{Code: code, Message: message, Path: &path, RepairHint: &repair}
    }
  GO
}
