go_tool "submit_claim_cards" {
  description = "Submit one through five atomic fact cards. confirmed requires a direct authoritative primary source; reported requires a retained published source; inferred requires premise claim IDs and no direct quotation. Unknowns are gaps, not claim cards. All validation errors are returned together."

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
      "time"
    )

    type Card struct {
      ID string `json:"id"`
      Statement string `json:"statement"`
      Status string `json:"status"`
      Scope string `json:"scope"`
      AsOf string `json:"as_of"`
      SourceID string `json:"source_id"`
      SourceURL string `json:"source_url,omitempty"`
      ExactQuote string `json:"exact_quote"`
      Locator string `json:"locator"`
      DerivedFrom []string `json:"derived_from"`
    }
    type Source struct {
      ID string `json:"id"`
      URL string `json:"url"`
      CanonicalURL string `json:"canonical_url"`
      SourceClass string `json:"source_class"`
    }
    type Input struct {
      WorkspaceDir string `json:"workspace_dir"`
      ClaimsPath string `json:"claims_path"`
      Cards []Card `json:"cards"`
    }
    type Output struct {
      ClaimIDs []string `json:"claim_ids"`
      Staged int `json:"staged"`
    }

    var cardID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      cwd, err := os.Getwd()
      if err != nil { return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err) }
      workspace, issues := cardWorkspace(input.WorkspaceDir, cwd)
      claimsPath, pathIssues := cardArtifactPath(input.ClaimsPath, workspace, "claims.json")
      issues = append(issues, pathIssues...)
      if len(input.Cards) == 0 || len(input.Cards) > 5 {
        issues = append(issues, cardIssue("batch_size", "cards", "cards must contain from one through five items"))
      }
      sources, err := cardSources(filepath.Join(workspace, ".evidence-draft", "sources"))
      if err != nil { return ToolResponse[Output]{}, fmt.Errorf("load source registry draft: %w", err) }
      seen := map[string]struct{}{}
      for index, card := range input.Cards {
        path := fmt.Sprintf("cards[%d]", index)
        id := strings.TrimSpace(card.ID)
        if !cardID.MatchString(id) { issues = append(issues, cardIssue("claim_id", path+".id", "id must be a short stable identifier")) }
        if _, exists := seen[id]; exists { issues = append(issues, cardIssue("claim_id", path+".id", "claim IDs must be unique in the batch")) }
        seen[id] = struct{}{}
        for field, value := range map[string]string{"statement": card.Statement, "scope": card.Scope} {
          if strings.TrimSpace(value) == "" { issues = append(issues, cardIssue("claim", path+"."+field, field+" must not be empty")) }
        }
        if _, dateErr := time.Parse("2006-01-02", strings.TrimSpace(card.AsOf)); dateErr != nil {
          issues = append(issues, cardIssue("date", path+".as_of", "as_of must use YYYY-MM-DD"))
        }
        switch strings.TrimSpace(card.Status) {
        case "confirmed", "reported":
          source, exists := sources[strings.TrimSpace(card.SourceID)]
          if !exists { issues = append(issues, cardIssue("source_id", path+".source_id", "source_id must reference a registered source"))
          } else if source.SourceClass == "lead_only" {
            issues = append(issues, cardIssue("source_class", path+".source_id", "lead-only material cannot support a claim card"))
          } else if card.Status == "confirmed" && source.SourceClass != "authoritative_primary" {
            issues = append(issues, cardIssue("source_class", path+".source_id", "confirmed requires an authoritative primary source"))
          }
          if strings.TrimSpace(card.ExactQuote) == "" { issues = append(issues, cardIssue("evidence", path+".exact_quote", "exact_quote is required")) }
          if strings.TrimSpace(card.Locator) == "" { issues = append(issues, cardIssue("evidence", path+".locator", "locator is required")) }
          if len(card.DerivedFrom) != 0 { issues = append(issues, cardIssue("derived_from", path+".derived_from", "direct claims must not list inferred premises")) }
        case "inferred":
          if len(card.DerivedFrom) == 0 { issues = append(issues, cardIssue("derived_from", path+".derived_from", "inferred claims require one or more premise claim IDs")) }
          if strings.TrimSpace(card.SourceID) != "" || strings.TrimSpace(card.ExactQuote) != "" || strings.TrimSpace(card.Locator) != "" {
            issues = append(issues, cardIssue("inference", path, "inferred claims cite premise IDs instead of pretending a source directly states the inference"))
          }
        default:
          issues = append(issues, cardIssue("status", path+".status", "status must be confirmed, reported, or inferred; record unknowns separately"))
        }
      }
      if workspace == "" || claimsPath == "" || len(issues) > 0 { return ToolResponse[Output]{Accepted:false, Issues:issues}, nil }
      directory := filepath.Join(workspace, ".decision-draft", "claims")
      ids := make([]string, 0, len(input.Cards))
      for _, card := range input.Cards {
        if err = cardWriteJSON(filepath.Join(directory, card.ID+".json"), card); err != nil { return ToolResponse[Output]{}, fmt.Errorf("stage claim card: %w", err) }
        ids = append(ids, card.ID)
      }
      output := Output{ClaimIDs:ids, Staged:len(ids)}
      return ToolResponse[Output]{Accepted:true, Output:&output}, nil
    }

    func cardSources(directory string) (map[string]Source, error) {
      paths, err := filepath.Glob(filepath.Join(directory, "*.json")); if err != nil { return nil, err }
      result := map[string]Source{}
      for _, path := range paths { var source Source; if err = cardReadJSON(path, &source); err != nil { return nil, err }; result[source.ID] = source }
      return result, nil
    }
    func cardWorkspace(raw, cwd string) (string, []Issue) {
      path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw))); root := cardBlocksRoot(cwd)
      if err != nil || !filepath.IsAbs(raw) || root == "" || !cardWithin(path, root) { return "", []Issue{cardIssue("workspace_dir", "workspace_dir", "workspace_dir must be an absolute directory inside the current run's blocks directory")} }
      return path, nil
    }
    func cardArtifactPath(raw, workspace, name string) (string, []Issue) {
      path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
      if workspace == "" || err != nil || !filepath.IsAbs(raw) || filepath.Base(path) != name || !cardWithin(path, workspace) { return "", []Issue{cardIssue("invalid_path", "claims_path", "claims_path must end in "+name+" under workspace_dir")} }
      return path, nil
    }
    func cardBlocksRoot(path string) string { current, err := filepath.Abs(path); if err != nil { return "" }; for { if strings.EqualFold(filepath.Base(current), "blocks") && strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))), "runs") { return current }; parent := filepath.Dir(current); if parent == current { return "" }; current = parent } }
    func cardWithin(path, root string) bool { relative, err := filepath.Rel(root, path); return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) }
    func cardReadJSON(path string, value any) error { payload, err := os.ReadFile(path); if err != nil { return err }; return json.Unmarshal(bytes.TrimPrefix(payload, []byte{0xef,0xbb,0xbf}), value) }
    func cardWriteJSON(path string, value any) error { if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil { return err }; payload, err := json.MarshalIndent(value, "", "  "); if err != nil { return err }; return os.WriteFile(path, append(payload, '\n'), 0600) }
    func cardIssue(code, path, message string) Issue { repair := "Correct this field and call the tool again."; return Issue{Code:code, Message:message, Path:&path, RepairHint:&repair} }
  GO
}

go_tool "finalize_claim_cards" {
  description = "Finalize staged atomic claim cards and the hidden source registry. Checks exact quotations against snapshots while ignoring only Unicode whitespace layout differences, validates inference references and cycles, and returns every problem together."

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
    type Card struct { ID string `json:"id"`; Statement string `json:"statement"`; Status string `json:"status"`; Scope string `json:"scope"`; AsOf string `json:"as_of"`; SourceID string `json:"source_id"`; SourceURL string `json:"source_url,omitempty"`; ExactQuote string `json:"exact_quote"`; Locator string `json:"locator"`; DerivedFrom []string `json:"derived_from"` }
    type Source struct { ID string `json:"id"`; URL string `json:"url"`; CanonicalURL string `json:"canonical_url"`; Title string `json:"title"`; Publisher string `json:"publisher"`; PublicationDate string `json:"publication_date"`; AccessedAt string `json:"accessed_at"`; SourceClass string `json:"source_class"`; SnapshotPath string `json:"snapshot_path"`; OriginID string `json:"origin_id"` }
    type Input struct { WorkspaceDir string `json:"workspace_dir"`; ClaimsPath string `json:"claims_path"`; SourceRegistryPath string `json:"source_registry_path"`; AsOfDate string `json:"as_of_date"`; AllowEmpty bool `json:"allow_empty"` }
    type Output string
    type ClaimDocument struct { ArtifactKind string `json:"artifact_kind"`; AsOfDate string `json:"as_of_date"`; Claims []Card `json:"claims"` }
    type Registry struct { AsOfDate string `json:"as_of_date"`; Sources []Source `json:"sources"` }

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      cwd, err := os.Getwd(); if err != nil { return ToolResponse[Output]{}, fmt.Errorf("resolve block workspace: %w", err) }
      workspace, issues := finalCardWorkspace(input.WorkspaceDir, cwd)
      claimsPath, pathIssues := finalCardPath(input.ClaimsPath, workspace, "claims.json", "claims_path"); issues = append(issues, pathIssues...)
      registryPath, registryIssues := finalCardPath(input.SourceRegistryPath, workspace, "source-registry.json", "source_registry_path"); issues = append(issues, registryIssues...)
      cutoff, dateErr := time.Parse("2006-01-02", strings.TrimSpace(input.AsOfDate)); if dateErr != nil { issues = append(issues, finalCardIssue("date", "as_of_date", "as_of_date must use YYYY-MM-DD")) }
      cards, err := finalCardRecords[Card](filepath.Join(workspace, ".decision-draft", "claims")); if err != nil { return ToolResponse[Output]{}, fmt.Errorf("load staged cards: %w", err) }
      sources, err := finalCardRecords[Source](filepath.Join(workspace, ".evidence-draft", "sources")); if err != nil { return ToolResponse[Output]{}, fmt.Errorf("load registered sources: %w", err) }
      if len(cards) == 0 && !input.AllowEmpty { issues = append(issues, finalCardIssue("claims", "claims", "submit at least one claim card")) }
      sourceByID := map[string]Source{}; usedSources := map[string]struct{}{}
      for _, source := range sources { sourceByID[source.ID] = source; published, parseErr := time.Parse("2006-01-02", strings.TrimSpace(source.PublicationDate)); if parseErr != nil { issues = append(issues, finalCardIssue("date", source.ID+".publication_date", "publication_date must use YYYY-MM-DD")) } else if dateErr == nil && published.After(cutoff) { issues = append(issues, finalCardIssue("source_after_as_of_date", source.ID, "source publication_date is later than as_of_date")) } }
      cardByID := map[string]*Card{}
      for index := range cards { card := &cards[index]; if _, exists := cardByID[card.ID]; exists { issues = append(issues, finalCardIssue("claim_id", card.ID, "claim IDs must be globally unique")) }; cardByID[card.ID] = card }
      for index := range cards {
        card := &cards[index]
        if card.Status == "inferred" { for _, premise := range card.DerivedFrom { if _, exists := cardByID[premise]; !exists { issues = append(issues, finalCardIssue("derived_from", card.ID, "derived_from references an unknown claim ID: "+premise)) } }; continue }
        source, exists := sourceByID[card.SourceID]; if !exists { issues = append(issues, finalCardIssue("source_id", card.ID, "claim references an unregistered source")); continue }
        usedSources[source.ID] = struct{}{}; card.SourceURL = strings.TrimSpace(source.CanonicalURL); if card.SourceURL == "" { card.SourceURL = strings.TrimSpace(source.URL) }
        if card.SourceURL == "" || strings.Contains(card.SourceURL, "...") { issues = append(issues, finalCardIssue("source_url", card.ID, "source URL must be complete and directly usable")) }
        snapshot, readErr := os.ReadFile(source.SnapshotPath); if readErr != nil { issues = append(issues, finalCardIssue("snapshot_path", source.ID, "cannot read registered snapshot")); continue }
        if !strings.Contains(finalCardWhitespace(string(snapshot)), finalCardWhitespace(card.ExactQuote)) { issues = append(issues, finalCardIssue("quote_not_found", card.ID+".exact_quote", "exact_quote is not present in the registered snapshot after Unicode whitespace normalization")) }
      }
      state := map[string]uint8{}; var visit func(string)
      visit = func(id string) { if state[id] == 1 { issues = append(issues, finalCardIssue("derived_from_cycle", id, "inferred claims must not form a cycle")); return }; if state[id] == 2 { return }; state[id] = 1; if card := cardByID[id]; card != nil { for _, premise := range card.DerivedFrom { visit(premise) } }; state[id] = 2 }
      for id := range cardByID { visit(id) }
      if workspace == "" || claimsPath == "" || registryPath == "" || len(issues) > 0 { return ToolResponse[Output]{Accepted:false, Issues:issues}, nil }
      sort.Slice(cards, func(i,j int) bool { return cards[i].ID < cards[j].ID })
      retained := make([]Source,0,len(usedSources)); for _, source := range sources { if _, ok := usedSources[source.ID]; ok { retained = append(retained, source) } }; sort.Slice(retained, func(i,j int) bool { return retained[i].ID < retained[j].ID })
      if err = finalCardWriteJSON(claimsPath, ClaimDocument{ArtifactKind:"r42_claim_cards", AsOfDate:strings.TrimSpace(input.AsOfDate), Claims:cards}); err != nil { return ToolResponse[Output]{}, fmt.Errorf("write claims: %w", err) }
      if err = finalCardWriteJSON(registryPath, Registry{AsOfDate:strings.TrimSpace(input.AsOfDate), Sources:retained}); err != nil { return ToolResponse[Output]{}, fmt.Errorf("write source registry: %w", err) }
      summary, err := json.Marshal(map[string]any{"claims_path":claimsPath,"source_registry_path":registryPath,"claim_count":len(cards)}); if err != nil { return ToolResponse[Output]{}, err }; output := Output(summary)
      return ToolResponse[Output]{Accepted:true, Output:&output}, nil
    }
    func finalCardRecords[T any](directory string) ([]T,error) { paths,err:=filepath.Glob(filepath.Join(directory,"*.json")); if err!=nil{return nil,err}; sort.Strings(paths); result:=make([]T,0,len(paths)); for _,path:=range paths { var item T; payload,readErr:=os.ReadFile(path); if readErr!=nil{return nil,readErr}; if readErr=json.Unmarshal(bytes.TrimPrefix(payload,[]byte{0xef,0xbb,0xbf}),&item);readErr!=nil{return nil,readErr}; result=append(result,item) }; return result,nil }
    func finalCardWhitespace(value string) string { return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ") }
    func finalCardWorkspace(raw,cwd string)(string,[]Issue){path,err:=filepath.Abs(filepath.Clean(strings.TrimSpace(raw)));root:=finalCardBlocksRoot(cwd);if err!=nil||!filepath.IsAbs(raw)||root==""||!finalCardWithin(path,root){return "",[]Issue{finalCardIssue("workspace_dir","workspace_dir","workspace_dir must be an absolute directory inside the current run's blocks directory")}};return path,nil}
    func finalCardPath(raw,workspace,name,field string)(string,[]Issue){path,err:=filepath.Abs(filepath.Clean(strings.TrimSpace(raw)));if workspace==""||err!=nil||!filepath.IsAbs(raw)||filepath.Base(path)!=name||!finalCardWithin(path,workspace){return "",[]Issue{finalCardIssue("invalid_path",field,field+" must end in "+name+" under workspace_dir")}};return path,nil}
    func finalCardBlocksRoot(path string)string{current,err:=filepath.Abs(path);if err!=nil{return ""};for{if strings.EqualFold(filepath.Base(current),"blocks")&&strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))),"runs"){return current};parent:=filepath.Dir(current);if parent==current{return ""};current=parent}}
    func finalCardWithin(path,root string)bool{relative,err:=filepath.Rel(root,path);return err==nil&&relative!=".."&&!strings.HasPrefix(relative,".."+string(filepath.Separator))}
    func finalCardWriteJSON(path string,value any)error{if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{return err};payload,err:=json.MarshalIndent(value,"","  ");if err!=nil{return err};return os.WriteFile(path,append(payload,'\n'),0600)}
    func finalCardIssue(code,path,message string)Issue{repair:="Correct all listed fields and call the tool again.";return Issue{Code:code,Message:message,Path:&path,RepairHint:&repair}}
  GO
}

go_tool "submit_node_assessment" {
  description = "Write one evidence-linked supply-chain node assessment. risk_scope describes applicability; conclusion independently describes proof strength. Unknowns and falsification conditions remain explicit."
  source = <<-GO
    import("context";"encoding/json";"fmt";"os";"path/filepath";"strings")
    type Input struct { WorkspaceDir string `json:"workspace_dir"`; ArtifactPath string `json:"artifact_path"`; ClaimPaths []string `json:"claim_paths"`; NodeID string `json:"node_id"`; NodeName string `json:"node_name"`; RiskScope string `json:"risk_scope"`; Branch string `json:"branch"`; Scenarios []string `json:"scenarios"`; ActualDependency string `json:"actual_dependency"`; QualifiedAlternatives string `json:"qualified_alternatives"`; SwitchingVsBuffer string `json:"switching_vs_buffer"`; Conclusion string `json:"conclusion"`; ClaimIDs []string `json:"claim_ids"`; Unknowns []string `json:"unknowns"`; FalsificationConditions []string `json:"falsification_conditions"` }
    type Output string
    type claimDocument struct { Claims []struct{ID string `json:"id"`} `json:"claims"` }
    func Invoke(_ context.Context,input Input)(ToolResponse[Output],error){
      cwd,err:=os.Getwd();if err!=nil{return ToolResponse[Output]{},err};issues:=[]Issue{}
      workspace:=nodeClean(input.WorkspaceDir);root:=nodeBlocksRoot(cwd);if workspace==""||root==""||!nodeWithin(workspace,root){issues=append(issues,nodeIssue("workspace_dir","workspace_dir","must be an absolute directory inside the current run's blocks directory"))}
      artifact:=nodeClean(input.ArtifactPath);if artifact==""||workspace==""||filepath.Base(artifact)!="node-assessment.json"||!nodeWithin(artifact,workspace){issues=append(issues,nodeIssue("artifact_path","artifact_path","must end in node-assessment.json under workspace_dir"))}
      known,loadErr:=nodeClaims(input.ClaimPaths,root);if loadErr!=nil{return ToolResponse[Output]{},fmt.Errorf("load claim cards: %w",loadErr)}
      for field,value:=range map[string]string{"node_id":input.NodeID,"node_name":input.NodeName,"actual_dependency":input.ActualDependency,"qualified_alternatives":input.QualifiedAlternatives,"switching_vs_buffer":input.SwitchingVsBuffer}{if strings.TrimSpace(value)==""{issues=append(issues,nodeIssue("required",field,field+" must not be empty"))}}
      if input.RiskScope!="global"&&input.RiskScope!="branch"{issues=append(issues,nodeIssue("risk_scope","risk_scope","must be global or branch"))};if input.RiskScope=="branch"&&strings.TrimSpace(input.Branch)==""{issues=append(issues,nodeIssue("branch","branch","branch is required for branch scope"))}
      if input.Conclusion!="confirmed"&&input.Conclusion!="candidate"&&input.Conclusion!="not_proven"{issues=append(issues,nodeIssue("conclusion","conclusion","must be confirmed, candidate, or not_proven"))}
      allowed:=map[string]bool{"current_production":true,"expansion_upgrade":true,"product_branch":true};if len(input.Scenarios)==0{issues=append(issues,nodeIssue("scenarios","scenarios","at least one scenario is required"))};for i,value:=range input.Scenarios{if !allowed[value]{issues=append(issues,nodeIssue("scenario",fmt.Sprintf("scenarios[%d]",i),"unsupported scenario"))}}
      if len(input.ClaimIDs)==0{issues=append(issues,nodeIssue("claim_ids","claim_ids","at least one supporting claim is required"))};for _,id:=range input.ClaimIDs{if !known[id]{issues=append(issues,nodeIssue("claim_id",id,"claim ID does not exist"))}}
      if len(input.Unknowns)==0{issues=append(issues,nodeIssue("unknowns","unknowns","record at least one material unknown"))};if len(input.FalsificationConditions)==0{issues=append(issues,nodeIssue("falsification_conditions","falsification_conditions","record at least one falsification condition"))}
      if len(issues)>0{return ToolResponse[Output]{Accepted:false,Issues:issues},nil};if err=os.MkdirAll(filepath.Dir(artifact),0700);err!=nil{return ToolResponse[Output]{},err};payload,err:=json.MarshalIndent(input,"","  ");if err!=nil{return ToolResponse[Output]{},err};if err=os.WriteFile(artifact,append(payload,'\n'),0600);err!=nil{return ToolResponse[Output]{},err};summary,err:=json.Marshal(input);if err!=nil{return ToolResponse[Output]{},err};output:=Output(summary);return ToolResponse[Output]{Accepted:true,Output:&output},nil
    }
    func nodeClaims(paths []string,root string)(map[string]bool,error){result:=map[string]bool{};for _,raw:=range paths{path:=nodeClean(raw);if path==""||root==""||!nodeWithin(path,root){return nil,fmt.Errorf("claim path must be absolute inside the current run's blocks directory")};var doc claimDocument;payload,err:=os.ReadFile(path);if err!=nil{return nil,err};if err=json.Unmarshal(payload,&doc);err!=nil{return nil,err};for _,claim:=range doc.Claims{result[claim.ID]=true}};return result,nil}
    func nodeClean(raw string)string{if !filepath.IsAbs(raw){return ""};path,err:=filepath.Abs(filepath.Clean(strings.TrimSpace(raw)));if err!=nil{return ""};return path}
    func nodeBlocksRoot(path string)string{current,err:=filepath.Abs(path);if err!=nil{return ""};for{if strings.EqualFold(filepath.Base(current),"blocks")&&strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))),"runs"){return current};parent:=filepath.Dir(current);if parent==current{return ""};current=parent}}
    func nodeWithin(path,root string)bool{relative,err:=filepath.Rel(root,path);return err==nil&&relative!=".."&&!strings.HasPrefix(relative,".."+string(filepath.Separator))}
    func nodeIssue(code,path,message string)Issue{repair:="Correct all listed fields and call the tool again.";return Issue{Code:code,Message:message,Path:&path,RepairHint:&repair}}
  GO
}

go_tool "submit_supply_chain_map" {
  description = "Write the evidence-linked reference supply chain and a short list of nodes that warrant assessment. This tool does not score, rank, or declare chokepoints."
  source = <<-GO
    import("context";"encoding/json";"fmt";"os";"path/filepath";"strings")
    type Node struct { ID string `json:"id"`;Name string `json:"name"`;Kind string `json:"kind"`;Stages []string `json:"stages"`;Branches []string `json:"branches"`;ClaimIDs []string `json:"claim_ids"`;Unknowns []string `json:"unknowns"` }
    type Edge struct { From string `json:"from"`;To string `json:"to"`;Relation string `json:"relation"`;ClaimIDs []string `json:"claim_ids"` }
    type AssessmentTarget struct { NodeID string `json:"node_id"`;NodeName string `json:"node_name"`;WhyAssess string `json:"why_assess"`;ClaimIDs []string `json:"claim_ids"` }
    type Input struct { WorkspaceDir string `json:"workspace_dir"`;ArtifactPath string `json:"artifact_path"`;Topic string `json:"topic"`;ScopePath string `json:"scope_path"`;ClaimPaths []string `json:"claim_paths"`;Nodes []Node `json:"nodes"`;Edges []Edge `json:"edges"`;AssessmentTargets []AssessmentTarget `json:"assessment_targets"`;Unknowns []string `json:"unknowns"` }
    type Output string
    type claimDocument struct { Claims []struct{ID string `json:"id"`} `json:"claims"` }
    type Document struct { ArtifactKind string `json:"artifact_kind"`;Topic string `json:"topic"`;ScopePath string `json:"scope_path"`;ClaimPaths []string `json:"claim_paths"`;Nodes []Node `json:"nodes"`;Edges []Edge `json:"edges"`;AssessmentTargets []AssessmentTarget `json:"assessment_targets"`;Unknowns []string `json:"unknowns"` }
    func Invoke(_ context.Context,input Input)(ToolResponse[Output],error){cwd,err:=os.Getwd();if err!=nil{return ToolResponse[Output]{},err};issues:=[]Issue{};workspace:=chainClean(input.WorkspaceDir);root:=chainBlocksRoot(cwd);if workspace==""||root==""||!chainWithin(workspace,root){issues=append(issues,chainIssue("workspace_dir","workspace_dir","must be an absolute directory inside the current run's blocks directory"))};artifact:=chainClean(input.ArtifactPath);if artifact==""||workspace==""||filepath.Base(artifact)!="supply-chain.json"||!chainWithin(artifact,workspace){issues=append(issues,chainIssue("artifact_path","artifact_path","must end in supply-chain.json under workspace_dir"))};scope:=chainClean(input.ScopePath);if scope==""||root==""||filepath.Base(scope)!="scope.json"||!chainWithin(scope,root){issues=append(issues,chainIssue("scope_path","scope_path","must name an existing scope.json inside the current run's blocks directory"))};if _,statErr:=os.Stat(scope);statErr!=nil{issues=append(issues,chainIssue("scope_path","scope_path","scope.json must exist"))};if strings.TrimSpace(input.Topic)==""{issues=append(issues,chainIssue("topic","topic","topic must not be empty"))};known,loadErr:=chainClaims(input.ClaimPaths,root);if loadErr!=nil{return ToolResponse[Output]{},fmt.Errorf("load claim cards: %w",loadErr)};nodes:=map[string]Node{};kinds:=map[string]bool{"product":true,"component":true,"material":true,"process":true,"equipment":true,"qualification":true,"service":true,"system":true};if len(input.Nodes)==0{issues=append(issues,chainIssue("nodes","nodes","at least one node is required"))};for index,node:=range input.Nodes{path:=fmt.Sprintf("nodes[%d]",index);if strings.TrimSpace(node.ID)==""||strings.TrimSpace(node.Name)==""{issues=append(issues,chainIssue("node",path,"id and name are required"))};if _,exists:=nodes[node.ID];exists{issues=append(issues,chainIssue("node_id",path+".id","node IDs must be unique"))};nodes[node.ID]=node;if !kinds[node.Kind]{issues=append(issues,chainIssue("node_kind",path+".kind","unsupported node kind"))};if len(node.Stages)==0||len(node.Branches)==0{issues=append(issues,chainIssue("node_scope",path,"stages and branches must not be empty"))};issues=append(issues,chainClaimIssues(path+".claim_ids",node.ClaimIDs,known)...)};relations:=map[string]bool{"contains":true,"supplies":true,"transformed_into":true,"assembled_into":true,"processed_by":true,"tested_by":true,"qualified_by":true,"used_by":true};for index,edge:=range input.Edges{path:=fmt.Sprintf("edges[%d]",index);if _,ok:=nodes[edge.From];!ok{issues=append(issues,chainIssue("edge_node",path+".from","from must reference a node"))};if _,ok:=nodes[edge.To];!ok{issues=append(issues,chainIssue("edge_node",path+".to","to must reference a node"))};if !relations[edge.Relation]{issues=append(issues,chainIssue("relation",path+".relation","unsupported relation"))};issues=append(issues,chainClaimIssues(path+".claim_ids",edge.ClaimIDs,known)...)};targetIDs:=map[string]bool{};for index,target:=range input.AssessmentTargets{path:=fmt.Sprintf("assessment_targets[%d]",index);node,ok:=nodes[target.NodeID];if !ok{issues=append(issues,chainIssue("node_id",path+".node_id","must reference a node"))}else if target.NodeName!=node.Name{issues=append(issues,chainIssue("node_name",path+".node_name","must equal the referenced node name"))};if targetIDs[target.NodeID]{issues=append(issues,chainIssue("node_id",path+".node_id","assessment targets must be unique"))};targetIDs[target.NodeID]=true;if strings.TrimSpace(target.WhyAssess)==""{issues=append(issues,chainIssue("why_assess",path+".why_assess","must not be empty"))};issues=append(issues,chainClaimIssues(path+".claim_ids",target.ClaimIDs,known)...)};if len(issues)>0{return ToolResponse[Output]{Accepted:false,Issues:issues},nil};document:=Document{ArtifactKind:"r42_supply_chain",Topic:input.Topic,ScopePath:scope,ClaimPaths:input.ClaimPaths,Nodes:input.Nodes,Edges:input.Edges,AssessmentTargets:input.AssessmentTargets,Unknowns:input.Unknowns};payload,err:=json.MarshalIndent(document,"","  ");if err!=nil{return ToolResponse[Output]{},err};if err=os.MkdirAll(filepath.Dir(artifact),0700);err!=nil{return ToolResponse[Output]{},err};if err=os.WriteFile(artifact,append(payload,'\n'),0600);err!=nil{return ToolResponse[Output]{},err};output:=Output(payload);return ToolResponse[Output]{Accepted:true,Output:&output},nil}
    func chainClaimIssues(path string,ids []string,known map[string]bool)[]Issue{issues:=[]Issue{};for _,id:=range ids{if !known[id]{issues=append(issues,chainIssue("claim_id",path,id+" does not exist"))}};return issues}
    func chainClaims(paths []string,root string)(map[string]bool,error){result:=map[string]bool{};for _,raw:=range paths{path:=chainClean(raw);if path==""||root==""||!chainWithin(path,root){return nil,fmt.Errorf("claim path must be absolute inside the current run's blocks directory")};var doc claimDocument;payload,err:=os.ReadFile(path);if err!=nil{return nil,err};if err=json.Unmarshal(payload,&doc);err!=nil{return nil,err};for _,claim:=range doc.Claims{result[claim.ID]=true}};return result,nil}
    func chainClean(raw string)string{if !filepath.IsAbs(raw){return ""};path,err:=filepath.Abs(filepath.Clean(strings.TrimSpace(raw)));if err!=nil{return ""};return path}
    func chainBlocksRoot(path string)string{current,err:=filepath.Abs(path);if err!=nil{return ""};for{if strings.EqualFold(filepath.Base(current),"blocks")&&strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))),"runs"){return current};parent:=filepath.Dir(current);if parent==current{return ""};current=parent}}
    func chainWithin(path,root string)bool{relative,err:=filepath.Rel(root,path);return err==nil&&relative!=".."&&!strings.HasPrefix(relative,".."+string(filepath.Separator))}
    func chainIssue(code,path,message string)Issue{repair:="Correct all listed fields and call the tool again.";return Issue{Code:code,Message:message,Path:&path,RepairHint:&repair}}
  GO
}

go_tool "submit_company_priorities" {
  description = "Write a company research-priority list for one assessed node. A/B/C are follow-up research priorities, never investment ratings. Priority A requires a confirmed exact-node relationship; related-product-only companies cannot be A."
  source = <<-GO
    import("context";"encoding/json";"fmt";"os";"path/filepath";"strings")
    type Company struct { Company string `json:"company"`; Ticker string `json:"ticker"`; Market string `json:"market"`; Role string `json:"role"`; Priority string `json:"priority"`; RelationshipClaimIDs []string `json:"relationship_claim_ids"`; EconomicImpactClaimIDs []string `json:"economic_impact_claim_ids"`; WhyResearch string `json:"why_research"`; LargestUnknown string `json:"largest_unknown"`; NextCheck string `json:"next_check"` }
    type Input struct { WorkspaceDir string `json:"workspace_dir"`; ArtifactPath string `json:"artifact_path"`; NodeAssessmentPath string `json:"node_assessment_path"`; ClaimPaths []string `json:"claim_paths"`; Companies []Company `json:"companies"`; Conclusion string `json:"conclusion"` }
    type Output string
    type claimDocument struct { Claims []struct{ID string `json:"id"`;Status string `json:"status"`} `json:"claims"` }
    type nodeAssessment struct { NodeID string `json:"node_id"`;NodeName string `json:"node_name"`;Conclusion string `json:"conclusion"` }
    type priorityDocument struct { NodeID string `json:"node_id"`;NodeName string `json:"node_name"`;NodeConclusion string `json:"node_conclusion"`;Companies []Company `json:"companies"`;Conclusion string `json:"conclusion"` }
    func Invoke(_ context.Context,input Input)(ToolResponse[Output],error){cwd,err:=os.Getwd();if err!=nil{return ToolResponse[Output]{},err};issues:=[]Issue{};workspace:=priorityClean(input.WorkspaceDir);root:=priorityBlocksRoot(cwd);if workspace==""||root==""||!priorityWithin(workspace,root){issues=append(issues,priorityIssue("workspace_dir","workspace_dir","must be an absolute directory inside the current run's blocks directory"))};artifact:=priorityClean(input.ArtifactPath);if artifact==""||workspace==""||filepath.Base(artifact)!="company-priorities.json"||!priorityWithin(artifact,workspace){issues=append(issues,priorityIssue("artifact_path","artifact_path","must end in company-priorities.json under workspace_dir"))};nodePath:=priorityClean(input.NodeAssessmentPath);if nodePath==""||root==""||!priorityWithin(nodePath,root){issues=append(issues,priorityIssue("node_assessment_path","node_assessment_path","must be inside the current run's blocks directory"))}
      claims,loadErr:=priorityClaims(input.ClaimPaths,root);if loadErr!=nil{return ToolResponse[Output]{},fmt.Errorf("load claim cards: %w",loadErr)};var node nodeAssessment;if nodePath!=""{payload,readErr:=os.ReadFile(nodePath);if readErr!=nil{return ToolResponse[Output]{},readErr};if readErr=json.Unmarshal(payload,&node);readErr!=nil{return ToolResponse[Output]{},readErr}}
      if strings.TrimSpace(input.Conclusion)==""{issues=append(issues,priorityIssue("conclusion","conclusion","conclusion must not be empty"))};roles:=map[string]bool{"existing_supplier":true,"qualified_alternative":true,"related_product_only":true,"unverified":true};levels:=map[string]bool{"A":true,"B":true,"C":true,"do_not_research":true}
      for index,company:=range input.Companies{path:=fmt.Sprintf("companies[%d]",index);for field,value:=range map[string]string{"company":company.Company,"market":company.Market,"why_research":company.WhyResearch,"largest_unknown":company.LargestUnknown,"next_check":company.NextCheck}{if strings.TrimSpace(value)==""{issues=append(issues,priorityIssue("required",path+"."+field,field+" must not be empty"))}};if !roles[company.Role]{issues=append(issues,priorityIssue("role",path+".role","unsupported company role"))};if !levels[company.Priority]{issues=append(issues,priorityIssue("priority",path+".priority","priority must be A, B, C, or do_not_research"))};for _,id:=range append(append([]string{},company.RelationshipClaimIDs...),company.EconomicImpactClaimIDs...){if _,ok:=claims[id];!ok{issues=append(issues,priorityIssue("claim_id",path,id+" does not exist"))}}
        if company.Priority=="A"{if node.Conclusion=="not_proven"||company.Role=="related_product_only"||company.Role=="unverified"||len(company.RelationshipClaimIDs)==0{issues=append(issues,priorityIssue("priority",path+".priority","A requires a proven node and an existing-supplier or qualified-alternative relationship"))}else{for _,id:=range company.RelationshipClaimIDs{if claims[id]!="confirmed"{issues=append(issues,priorityIssue("priority",path+".relationship_claim_ids","A relationship claims must be confirmed"))}}}}
        if company.Priority=="B"&&node.Conclusion=="not_proven"{issues=append(issues,priorityIssue("priority",path+".priority","B requires a confirmed or candidate node"))}
      }
      if len(issues)>0{return ToolResponse[Output]{Accepted:false,Issues:issues},nil};document:=priorityDocument{NodeID:node.NodeID,NodeName:node.NodeName,NodeConclusion:node.Conclusion,Companies:input.Companies,Conclusion:input.Conclusion};payload,err:=json.MarshalIndent(document,"","  ");if err!=nil{return ToolResponse[Output]{},err};if err=os.MkdirAll(filepath.Dir(artifact),0700);err!=nil{return ToolResponse[Output]{},err};if err=os.WriteFile(artifact,append(payload,'\n'),0600);err!=nil{return ToolResponse[Output]{},err};summary,_:=json.Marshal(map[string]any{"artifact_path":artifact,"company_count":len(input.Companies)});output:=Output(summary);return ToolResponse[Output]{Accepted:true,Output:&output},nil}
    func priorityClaims(paths []string,root string)(map[string]string,error){result:=map[string]string{};for _,raw:=range paths{path:=priorityClean(raw);if path==""||root==""||!priorityWithin(path,root){return nil,fmt.Errorf("claim path must be absolute inside the current run's blocks directory")};var doc claimDocument;payload,err:=os.ReadFile(path);if err!=nil{return nil,err};if err=json.Unmarshal(payload,&doc);err!=nil{return nil,err};for _,claim:=range doc.Claims{result[claim.ID]=claim.Status}};return result,nil}
    func priorityClean(raw string)string{if !filepath.IsAbs(raw){return ""};path,err:=filepath.Abs(filepath.Clean(strings.TrimSpace(raw)));if err!=nil{return ""};return path}
    func priorityBlocksRoot(path string)string{current,err:=filepath.Abs(path);if err!=nil{return ""};for{if strings.EqualFold(filepath.Base(current),"blocks")&&strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))),"runs"){return current};parent:=filepath.Dir(current);if parent==current{return ""};current=parent}}
    func priorityWithin(path,root string)bool{relative,err:=filepath.Rel(root,path);return err==nil&&relative!=".."&&!strings.HasPrefix(relative,".."+string(filepath.Separator))}
    func priorityIssue(code,path,message string)Issue{repair:="Correct all listed fields and call the tool again.";return Issue{Code:code,Message:message,Path:&path,RepairHint:&repair}}
  GO
}

go_tool "finalize_research_report" {
  description = "Finalize report.md by replacing every [[claim:ID]] marker with a direct original-source URL and appending the referenced atomic evidence cards. Invalid or unsupported markers reject without rewriting the report."
  source = <<-GO
    import("context";"encoding/json";"fmt";"os";"path/filepath";"regexp";"sort";"strings")
    type Input struct { ReportPath string `json:"report_path"`; ClaimPaths []string `json:"claim_paths"` }
    type Output string
    type Card struct { ID string `json:"id"`;Statement string `json:"statement"`;Status string `json:"status"`;Scope string `json:"scope"`;AsOf string `json:"as_of"`;SourceURL string `json:"source_url"`;ExactQuote string `json:"exact_quote"`;Locator string `json:"locator"`;DerivedFrom []string `json:"derived_from"` }
    type claimDocument struct { Claims []Card `json:"claims"` }
    var reportMarker=regexp.MustCompile(`\[\[claim:([A-Za-z][A-Za-z0-9_-]{0,127})\]\]`)
    func Invoke(_ context.Context,input Input)(ToolResponse[Output],error){cwd,err:=os.Getwd();if err!=nil{return ToolResponse[Output]{},err};root:=reportBlocksRoot(cwd);issues:=[]Issue{};report:=reportClean(input.ReportPath);if report==""||root==""||filepath.Base(report)!="report.md"||!reportWithin(report,root){issues=append(issues,reportIssue("report_path","report_path","report_path must be an absolute report.md inside the current run's blocks directory"))};cards:=map[string]Card{};for _,raw:=range input.ClaimPaths{path:=reportClean(raw);if path==""||root==""||!reportWithin(path,root){issues=append(issues,reportIssue("claim_path","claim_paths","claim paths must be inside the current run's blocks directory"));continue};var doc claimDocument;payload,readErr:=os.ReadFile(path);if readErr!=nil{return ToolResponse[Output]{},readErr};if readErr=json.Unmarshal(payload,&doc);readErr!=nil{return ToolResponse[Output]{},readErr};for _,card:=range doc.Claims{cards[card.ID]=card}}
      if len(issues)>0{return ToolResponse[Output]{Accepted:false,Issues:issues},nil};original,err:=os.ReadFile(report);if err!=nil{return ToolResponse[Output]{},err};text:=string(original);matches:=reportMarker.FindAllStringSubmatch(text,-1);if len(matches)==0{issues=append(issues,reportIssue("claim_marker","report.md","report must cite substantive statements with [[claim:ID]] markers"))};referenced:=map[string]bool{};for _,match:=range matches{id:=match[1];card,exists:=cards[id];if !exists{issues=append(issues,reportIssue("claim_id",id,"report references an unknown claim ID"));continue};urls:=reportCardURLs(card,cards,map[string]bool{});if len(urls)==0{issues=append(issues,reportIssue("source_url",id,"claim does not resolve to a complete original-source URL"));continue};valid:=true;for _,url:=range urls{if strings.Contains(url,"..."){issues=append(issues,reportIssue("source_url",id,"claim does not resolve to a complete original-source URL"));valid=false}};if !valid{continue};referenced[id]=true;text=strings.ReplaceAll(text,match[0],reportLinks(id,urls))};if len(issues)>0{return ToolResponse[Output]{Accepted:false,Issues:issues},nil}
      ids:=make([]string,0,len(referenced));for id:=range referenced{ids=append(ids,id)};sort.Strings(ids);var appendix strings.Builder;appendix.WriteString("\n\n## Evidence cards\n");for _,id:=range ids{card:=cards[id];urls:=reportCardURLs(card,cards,map[string]bool{});appendix.WriteString("\n### "+id+"\n\n");appendix.WriteString("- Statement: "+card.Statement+"\n- Status: "+card.Status+"\n- Scope: "+card.Scope+"\n- As of: "+card.AsOf+"\n- Sources: "+strings.Join(urls,", ")+"\n");if card.ExactQuote!=""{appendix.WriteString("- Exact quote: "+strings.Join(strings.Fields(card.ExactQuote)," ")+"\n- Locator: "+card.Locator+"\n")};if len(card.DerivedFrom)>0{appendix.WriteString("- Derived from: "+strings.Join(card.DerivedFrom,", ")+"\n")}}
      text+=appendix.String();if err=os.WriteFile(report,[]byte(text),0600);err!=nil{return ToolResponse[Output]{},err};summary,_:=json.Marshal(map[string]any{"report_path":report,"referenced_claim_count":len(ids)});output:=Output(summary);return ToolResponse[Output]{Accepted:true,Output:&output},nil}
    func reportCardURLs(card Card,cards map[string]Card,visiting map[string]bool)[]string{if strings.TrimSpace(card.SourceURL)!=""{return []string{strings.TrimSpace(card.SourceURL)}};if visiting[card.ID]{return nil};visiting[card.ID]=true;defer delete(visiting,card.ID);urls:=[]string{};seen:=map[string]bool{};for _,id:=range card.DerivedFrom{if premise,ok:=cards[id];ok{for _,url:=range reportCardURLs(premise,cards,visiting){if !seen[url]{seen[url]=true;urls=append(urls,url)}}}};return urls}
    func reportLinks(id string,urls []string)string{if len(urls)==1{return "["+id+"]("+urls[0]+")"};links:=make([]string,0,len(urls));for index,url:=range urls{links=append(links,fmt.Sprintf("[%s:%d](%s)",id,index+1,url))};return strings.Join(links,", ")}
    func reportClean(raw string)string{if !filepath.IsAbs(raw){return ""};path,err:=filepath.Abs(filepath.Clean(strings.TrimSpace(raw)));if err!=nil{return ""};return path}
    func reportBlocksRoot(path string)string{current,err:=filepath.Abs(path);if err!=nil{return ""};for{if strings.EqualFold(filepath.Base(current),"blocks")&&strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(current))),"runs"){return current};parent:=filepath.Dir(current);if parent==current{return ""};current=parent}}
    func reportWithin(path,root string)bool{relative,err:=filepath.Rel(root,path);return err==nil&&relative!=".."&&!strings.HasPrefix(relative,".."+string(filepath.Separator))}
    func reportIssue(code,path,message string)Issue{repair:="Correct all listed fields and call the tool again.";return Issue{Code:code,Message:message,Path:&path,RepairHint:&repair}}
  GO
}
