module "pplx_tools" {
  source = "./modules/pplx_tools"
}

research "static" "primary_source_baseline" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You establish the first-party evidence baseline for a supply-chain study.
    Retain current official filings, regulator records, official product
    documentation, and official statements before broader industry research.
    Do not infer unnamed counterparties or silently combine product generations.
    The evidence cutoff is fixed; never use a source published after it.

    Never use PowerShell, a shell, curl, wget, scripts, or command-line programs
    to search or download content. Only configured source tools may access remote
    sources. Stop searching once the current official corpus is adequately
    represented and remaining official-source gaps are explicit.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}

    ${local.source_tool_guidance}
    ${local.evidence_registration_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}
    Task workspace_dir: "${block_wd()}"
    Pass this exact workspace_dir to every configured evidence typed tool.
    The tools create workspace_dir if it does not exist.

    Find the current official corpus most likely to govern this study: issuer
    filings and prospectuses, official product specifications, regulator pages,
    official contract or customer disclosures, and applicable export-control or
    qualification rules. Save every retained source as a Markdown snapshot.

    For each retained snapshot, call ${go_tool.register_evidence_source.id}
    once with workspace_dir "${block_wd()}" and ledger_path
    "${block_wd()}/evidence-ledger.json".

    Submit atomic baseline claims in batches of at most five with
    ${go_tool.stage_evidence_claims.id}. Use claim_type
    organization_relationship, supplier_maturity, quantitative, technical,
    regulatory, product_structure, process, or other. Quantitative claims must
    provide unit, period, and derivation in qualifiers. Do not submit confidence:
    r42 separately derives evidence_status as confirmed, reported, inferred, or
    unknown and dispute_status as clean, challenged, or disputed.

    Finish by calling ${go_tool.finalize_evidence_ledger.id} with:
    - workspace_dir: "${block_wd()}"
    - ledger_path: "${block_wd()}/evidence-ledger.json"
    - source_registry_path: "${block_wd()}/source-registry.json"
    - mode: "baseline"
    - topic: the exact topic above
    - as_of_date: "${var.as_of_date}"
    - scope_artifact and track: empty strings
  PROMPT
  tool_ids = concat(local.pplx_tool_ids, [
    go_tool.register_evidence_source.id,
    go_tool.stage_evidence_claims.id,
    go_tool.finalize_evidence_ledger.id,
  ])
  tool_call_quota   = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.finalize_evidence_ledger.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "evidence_ledger" {
    type      = "file"
    path      = "${block_wd()}/evidence-ledger.json"
    required  = true
    non_empty = true
  }

  artifact "source_registry" {
    type      = "file"
    path      = "${block_wd()}/source-registry.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      corpus = "Verify the retained sources include the newest available first-party filings, regulator records, and official product documentation relevant to the topic as of the fixed cutoff. Return every material omission in one verdict."
      classification = "Judge whether each broad source classification, reporting basis, provenance, and authority-for-claim decision honestly describes the source. Anonymous reporting must not be presented as named or document-backed, and lead-only material must not substantively support a claim."
      claim_atomicity = "Judge whether each claim expresses one independently auditable, time-bounded assertion and whether the cited passage semantically entails that assertion without hidden extrapolation."
      lifecycle = "Judge whether supplier maturity is semantically calibrated to the evidence and never promotes research or validation into an order, delivery, production, or primary-supplier relationship."
    }
    model_provider    = model_provider.primary
    model             = local.qc_model
    reasoning_effort  = var.reasoning_effort
    max_qc_rounds     = var.max_qc_rounds
    disallowed_tools  = local.semantic_qc_disallowed_tools
    permission        = "approve_all"
  }
}

research "static" "brainstorm" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You facilitate an exploratory product and supply-chain brainstorm. Define
    a coverage-complete reference chain before selecting chokepoints. Keep the
    generic product architecture separate from facts specifically confirmed
    for the organization or product named by the topic. Never silently combine
    materially different product generations or form factors.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Market universe: ${var.market}
    Primary-source baseline: ${one([for item in research.static.primary_source_baseline.artifact : item.path if item.name == "evidence_ledger"])}
    Primary-source registry: ${one([for item in research.static.primary_source_baseline.artifact : item.path if item.name == "source_registry"])}

    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}

    Read both baseline artifacts before defining scope. Test the most important
    hypotheses and preserve the most decision-relevant sources. Write
    ${block_wd()}/brainstorm.md with:

    - the focal system boundary;
    - a layered product and component decomposition;
    - stages from product definition through manufacturing, packaging, testing,
      module integration, and downstream system qualification;
    - competing hypotheses about equipment, materials, and qualification lock-in;
    - an explicit distinction between reference-chain facts and target facts that
      are confirmed, reported, inferred, or unknown, with challenges or disputes
      represented separately;
    - possible convergence points and substitution mechanisms;
    - disputed assumptions and explicit research questions for the five tracks;
    - every fetched snapshot path next to the claim it informs.

    Stop upstream decomposition at production equipment and input materials:
    do not decompose equipment into subassemblies or trace materials to their
    raw extraction origin. Continue downstream through module components and
    system qualification. This is ideation, not final selection. Do not create
    company nodes.

    Then call ${go_tool.submit_supply_chain_scope.id} with artifact_path
    "${block_wd()}/scope.json". Declare focal_product, distinct product_variants,
    expected_components, expected_stages, both research boundaries, and a
    coverage_items inventory. Every coverage item must name one or more declared
    components and stages and exactly one track from product_structure,
    manufacturing_testing, equipment, materials_chemicals, or
    qualification_integration. Together the items must cover every expected
    component and stage. Record ambiguity as open_questions instead of silently
    merging variants.
  PROMPT
  tool_ids = concat(local.pplx_tool_ids, [
    go_tool.submit_supply_chain_scope.id,
  ])
  tool_call_quota       = local.pplx_tool_call_quota
  terminate_tool_id     = go_tool.submit_supply_chain_scope.id
  disallowed_tools      = local.research_disallowed_tools
  permission            = "approve_all"

  artifact "brainstorm" {
    type      = "file"
    path      = "${block_wd()}/brainstorm.md"
    required  = true
    non_empty = true
  }

  artifact "scope" {
    type      = "file"
    path      = "${block_wd()}/scope.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      scope_challenge = "Act as an independent scope challenger, not a checker of the author's own inventory. Read the official baseline, source registry, brainstorm.md, and scope.json. Identify every material component, stage, purchase category, equipment or service dependency, module branch, and qualification step missing from scope. Return all omissions in one verdict."
      boundary = "Verify one bounded focal product is defined, upstream decomposition stops at production equipment and input materials, and downstream coverage continues through module components and system qualification."
      coverage_design = "Judge whether the five-track partition meaningfully covers the declared product and process without hiding overlaps, omitting important dependencies, or silently merging materially different variants."
      target_mapping = "Verify reference-chain knowledge is distinguished from target facts that are confirmed, reported, inferred, or unknown, with challenges and disputes represented separately; company names must not become graph nodes."
      evidence = "Judge whether source-backed statements are semantically supported and whether hypotheses that exceed the evidence remain explicitly unverified. Treat typed-tool path and reference validation as authoritative."
      uncertainty = "Verify competing explanations and material unknowns are preserved rather than silently collapsed into one story."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "graph_track" {
  for_each = local.graph_tracks

  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt    = var.research_system_prompt
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Track: ${each.value.title}
    Assigned question: ${each.value.question}
    Brainstorm artifact: ${one([for item in research.static.brainstorm.artifact : item.path if item.name == "brainstorm"])}
    Scope artifact: ${one([for item in research.static.brainstorm.artifact : item.path if item.name == "scope"])}

    Read both artifacts, then research only this track. Address every scope
    coverage item assigned to "${each.key}". Preserve the distinction between
    the generic reference chain and target facts that are confirmed, reported,
    inferred, or unknown, with challenge or dispute represented independently.
    Keep materially different product variants separate.
    ${local.source_tool_guidance}
    ${local.evidence_registration_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}
    Task workspace_dir: "${block_wd()}"
    Pass this exact workspace_dir to every configured evidence typed tool.
    The tools create workspace_dir if it does not exist.

    For every retained source, call ${go_tool.register_evidence_source.id}
    with workspace_dir "${block_wd()}" and ledger_path
    "${block_wd()}/evidence-ledger.json". Submit atomic claims
    in batches of at most five with ${go_tool.stage_evidence_claims.id}.
    Claim IDs must begin with "${each.key}-" and every claim must reference its
    assigned coverage_item_ids. Register exact quotes as evidence edges; do not
    submit a confidence value because the host derives evidence status.

    If retained public evidence cannot resolve an assigned item, call
    ${go_tool.stage_evidence_gaps.id} with its coverage_item_id, reason,
    research_attempt, and impact. Do not name or rank candidate companies.

    Finish by calling ${go_tool.finalize_evidence_ledger.id} with:
    - workspace_dir: "${block_wd()}"
    - ledger_path: "${block_wd()}/evidence-ledger.json"
    - source_registry_path: "${block_wd()}/source-registry.json"
    - mode: "track"
    - topic: the exact topic above
    - as_of_date: "${var.as_of_date}"
    - scope_artifact: the absolute scope path above
    - track: "${each.key}"
  PROMPT
  tool_ids = concat(local.pplx_tool_ids, local.evidence_tool_ids)
  tool_call_quota  = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.finalize_evidence_ledger.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "evidence_ledger" {
    type      = "file"
    path      = "${block_wd()}/evidence-ledger.json"
    required  = true
    non_empty = true
  }

  artifact "source_registry" {
    type      = "file"
    path      = "${block_wd()}/source-registry.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      track_scope = "Judge whether the claims and explicit gaps answer this track's assigned questions without drifting into company selection or leaving a materially important part of the assigned domain unexplained."
      source_policy = "Judge whether broad source class, reporting basis, provenance, directness, and authority-for-claim choices are semantically honest. Named or document-backed high-quality media may confirm; anonymous reporting requires genuinely independent qualified-media origins; lead-only material cannot substantively support a claim."
      evidence_entailment = "Judge whether each cited passage semantically entails its atomic claim, including its period, product variant, and named parties. Accept the finalizer's whitespace-tolerant text matching and do not repeat it."
      status_calibration = "Judge whether inference, evidence strength, and challenge or dispute are communicated as separate concepts, without upgrading an analytical inference or hiding contrary evidence. Treat host-derived statuses as authoritative once source semantics are accepted."
      decision_value = "Verify each proposed node, edge, stopping boundary, or uncertainty could affect bottleneck, substitution, qualification, capacity, yield, or recovery-time analysis."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "reconcile_chain_evidence" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the evidence reconciliation chair. Resolve contradictions only from
    accepted ledgers. Never search for new facts, suppress a conflicting value,
    or treat source authority as a substitute for checking time and directness.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Baseline ledger: ${one([for item in research.static.primary_source_baseline.artifact : item.path if item.name == "evidence_ledger"])}

    Track ledgers:
    ${join("\n", [for item in research.static.graph_track : "- ${one([for artifact in item.artifact : artifact.path if artifact.name == "evidence_ledger"])}"])}

    Call ${go_tool.prepare_evidence_reconciliation.id} once with
    artifact_path "${block_wd()}/evidence-resolution.json" and ledger_paths
    containing the baseline ledger followed by all five track ledgers, plus
    assessment_paths = []. Read the returned draft_path. For every conflict ID, call
    ${go_tool.resolve_evidence_conflict.id} exactly once. Use prefer only
    when a named, direct, temporally applicable source justifies it; use
    preserve_both when values apply to different variants or periods; otherwise
    use unresolved. Finish with
    ${go_tool.finalize_evidence_reconciliation.id} and only artifact_path.
  PROMPT
  tool_ids = [
    go_tool.prepare_evidence_reconciliation.id,
    go_tool.resolve_evidence_conflict.id,
    go_tool.finalize_evidence_reconciliation.id,
  ]
  terminate_tool_id = go_tool.finalize_evidence_reconciliation.id
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "evidence_resolution" {
    type      = "file"
    path      = "${block_wd()}/evidence-resolution.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      conflict_reasoning = "Judge whether each reconciliation decision resolves claims that really concern the same fact, or correctly preserves both when product variant, period, organization, or qualifier differs. Treat the typed tool's conflict inventory and decision coverage as authoritative."
      authority = "Verify prefer decisions respect source authority, directness, named parties, product variant, and period. Newer or official is not automatically decisive when it addresses a different fact."
      uncertainty = "Verify unresolved and preserve_both decisions retain the competing values and explain their downstream effect instead of manufacturing consensus."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "select_chokepoints" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the supply-chain graph chair. Reconcile five independently checked
    evidence tracks into a coverage-complete reference graph, then select only
    genuine structural chokepoints. Preserve ordinary non-chokepoint nodes and
    explicit unknowns. A required input is not automatically a chokepoint.

    This is an offline reconciliation stage. Never search for or fetch new
    evidence, and never use PowerShell, a shell, curl, wget, scripts, or other
    command-line programs to do so. Work only from the accepted scope,
    reconciled evidence, and validated track ledgers supplied in the prompt.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Brainstorm: ${one([for item in research.static.brainstorm.artifact : item.path if item.name == "brainstorm"])}
    Scope artifact: ${one([for item in research.static.brainstorm.artifact : item.path if item.name == "scope"])}
    Reconciled evidence: ${one(research.static.reconcile_chain_evidence.artifact).path}

    Validated track ledgers:
    ${join("\n", [for item in research.static.graph_track : "- ${one([for artifact in item.artifact : artifact.path if artifact.name == "evidence_ledger"])}"])}

    Read every artifact. Do not perform new research. If evidence remains
    insufficient, preserve the gap and exclude the affected claim from formal
    chokepoint selection.

    Build the graph in small calls to
    ${go_tool.stage_supply_chain.id}. Each call must contain section, batch_id,
    and exactly one matching payload:
    - metadata: use batch_id "main" and provide topic, focal_node_id,
      scope_artifact, reconciled_artifact set to the absolute reconciled
      evidence path above, all five reviewed_artifacts, and conclusion;
    - nodes: submit 1-10 nodes per batch;
    - edges: submit at most 15 edges per batch;
    - coverage: submit 1-10 coverage resolutions per batch;
    - chokepoints: submit at most 10 per batch, using an explicit empty list
      when no genuine chokepoint exists.

    Give every batch a short stable ID. Reusing section plus batch_id replaces
    only that batch, so repair a rejected batch without resending the complete
    graph. After every section is staged, call
    ${go_tool.finalize_supply_chain.id} with only artifact_path
    "${block_wd()}/supply-chain.json". If finalization reports a cross-batch
    issue, replace only the affected batches and call finalize again.

    Resolve every scope coverage item exactly once as covered, unknown,
    not_applicable, or out_of_scope. Unknown items require an explanation,
    research_attempt, and impact. Build the complete product hierarchy and
    process flow before selecting chokepoints. Nodes may belong to multiple
    declared stages. Node and edge status must be exactly "supported" or
    "unknown". Supported nodes and edges must cite claim IDs from the five
    track ledgers; unknown nodes and edges require an explicit reason.
    Use only contains, supplies, transformed_into, assembled_into, processed_by,
    tested_by, qualified_by, or used_by as edge relations. Keep all nodes in one
    connected graph around the focal product. Mark equipment, materials, and
    downstream system qualification as explicit terminal boundaries where
    applicable; do not decompose them beyond scope.

    Select a node as a chokepoint only when confirmed, undisputed claim IDs
    support delivery impact, technical irreplaceability, qualification or
    switching cost, concentration, convergence, or capacity/yield constraints.
    Do not assign a composite score or rank. For each chokepoint separately set:
    - delivery_impact: limited, material, or production_stop;
    - substitutability: qualified_alternatives, lengthy_requalification,
      no_known_substitute, or unknown;
    - supplier_concentration: diversified, concentrated, single_source, or unknown;
    - non-negative min/max day ranges for switching and recovery.
    Keep company identities out of nodes and preserve generic reference-chain
    facts separately from target-specific evidence strength (confirmed, reported,
    inferred, or unknown) and dispute state (clean, challenged, or disputed).
  PROMPT
  tool_ids = [
    go_tool.stage_supply_chain.id,
    go_tool.finalize_supply_chain.id,
  ]
  terminate_tool_id = go_tool.finalize_supply_chain.id
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "supply_chain" {
    type      = "file"
    path      = "${block_wd()}/supply-chain.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      evidence_meaning = "Treat finalize_supply_chain as authoritative for schema, references, evidence levels, conflict decisions, coverage, and graph integrity. Read supply-chain.json, then call the configured read-only evidence tool once with all claim IDs cited by its chokepoints. Judge only whether the returned evidence semantically supports each stated mechanism, delivery impact, substitution constraint, concentration claim, and switching or recovery estimate."
      chokepoint_distinction = "Judge whether each selected node is a genuine structural chokepoint rather than merely a required input, a supplier commercial moat, demand growth, investment popularity, or an important but readily substitutable component."
      variant_scope = "Judge whether materially different product generations, form factors, periods, and qualification contexts remain meaningfully separated, and whether generic reference-chain facts are improperly presented as confirmed facts about the named target."
      uncertainty = "Judge whether conclusions remain calibrated to the evidence, competing explanations and material unknowns are preserved, and no recommendation or false precision is introduced. Do not repeat deterministic validation already enforced by the typed finalizer."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    tool_ids         = [go_tool.read_chokepoint_evidence.id]
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "dynamic" "discover_candidates" {
  tasks = [
    for index, chokepoint in jsondecode(research.static.select_chokepoints.result).chokepoints : {
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = var.research_system_prompt
      prompt = <<-PROMPT
        Topic: ${var.topic}
        Evidence cutoff: ${var.as_of_date}
        Market universe: ${var.market}
        Audited chokepoint: ${jsonencode(chokepoint)}
        Complete chain artifact: ${one(research.static.select_chokepoints.artifact).path}

        Discover at most ${var.max_candidates_per_chokepoint} publicly traded
        companies that directly control or critically supply this exact node.
        Exclude generic beneficiaries, downstream customers, funds, and firms
        linked only to adjacent nodes.
        ${local.source_tool_guidance}
        ${local.evidence_registration_guidance}
        ${var.use_pplx ? format("Perplexity snapshot_dir: %s/%03d/sources", block_wd(), index + 1) : ""}
        Task workspace_dir:
        "${block_wd()}/${format("%03d", index + 1)}"
        block_wd() is the shared dynamic-block directory, not this task's
        directory. Write every task-owned file beneath the task workspace_dir
        above. Pass that exact workspace_dir to every configured evidence and
        submission typed tool; the tools create it if it does not exist.

        Register every retained source with
        ${go_tool.register_evidence_source.id}, using the task workspace_dir and ledger_path
        "${block_wd()}/${format("%03d", index + 1)}/evidence-ledger.json".
        Submit atomic organization_relationship claims in batches of at most
        five with ${go_tool.stage_evidence_claims.id}. Claim IDs must begin
        with "candidate-${format("%03d", index + 1)}-". Finish the candidate
        ledger with ${go_tool.finalize_evidence_ledger.id}, mode
        "candidate", topic and as_of_date above, and empty scope_artifact and
        track. Its source_registry_path is
        "${block_wd()}/${format("%03d", index + 1)}/source-registry.json".
        If no candidate survives, stage one evidence gap whose
        coverage_item_id is the assigned chokepoint node_id and whose research
        attempt explains the searches performed; candidate mode permits that
        explicit gap even when no source was retained.

        Finally call ${go_tool.submit_candidates.id} with artifact_path
        "${block_wd()}/${format("%03d", index + 1)}/candidates.json", ledger_path
        "${block_wd()}/${format("%03d", index + 1)}/evidence-ledger.json", the
        task workspace_dir above, exact chokepoint node_id, max_candidates
        ${var.max_candidates_per_chokepoint}, and candidate evidence_claim_ids.
        An empty candidate list is valid when conclusion explains the gap.
      PROMPT
      tool_ids = concat(local.pplx_tool_ids, local.evidence_tool_ids, [
        go_tool.submit_candidates.id,
      ])
      tool_call_quota  = local.pplx_tool_call_quota
      terminate_tool_id = go_tool.submit_candidates.id
      disallowed_tools  = local.research_disallowed_tools
      permission        = "approve_all"
      artifacts = [
        {
          name      = "evidence_ledger"
          type      = "file"
          path      = "${block_wd()}/${format("%03d", index + 1)}/evidence-ledger.json"
          required  = true
          non_empty = true
        },
        {
          name      = "source_registry"
          type      = "file"
          path      = "${block_wd()}/${format("%03d", index + 1)}/source-registry.json"
          required  = true
          non_empty = true
        },
        {
          name      = "candidates"
          type      = "file"
          path      = "${block_wd()}/${format("%03d", index + 1)}/candidates.json"
          required  = true
          non_empty = true
        },
      ]
      retry = null
      qc = {
        criteria = {
          node_binding = "Judge whether each candidate directly controls or critically supplies the assigned node rather than merely participating in an adjacent node or downstream market."
          identity = "Judge whether the evidence identifies the same legal entity and publicly traded security claimed by the candidate, without conflating subsidiaries, parents, brands, or similarly named firms."
          relationship_support = "Judge whether the cited evidence semantically supports the exact-node relationship and whether indirect, inferred, challenged, or disputed evidence is described with appropriate caution."
          exclusions = "Reject funds, private entities presented as public, generic beneficiaries, and customers without evidence of control or critical supply. Treat the submission tool's candidate-count and reference checks as authoritative."
        }
        model_provider   = model_provider.primary
        model            = local.qc_model
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = var.max_qc_rounds
        disallowed_tools = local.semantic_qc_disallowed_tools
        permission       = "approve_all"
      }
    }
  ]
}

research "dynamic" "assess_candidates" {
  tasks = flatten([
    for discovery_index, discovery in research.dynamic.discover_candidates.tasks : [
      for candidate_index, candidate in jsondecode(discovery.result).candidates : {
        model_provider   = model_provider.primary
        model            = var.model
        reasoning_effort = var.reasoning_effort
        system_prompt    = var.research_system_prompt
        prompt = <<-PROMPT
          Topic: ${var.topic}
          Evidence cutoff: ${var.as_of_date}
          Candidate hypothesis: ${jsonencode(candidate)}
          Audited chain: ${one(research.static.select_chokepoints.artifact).path}
          Candidate-discovery artifact: ${one([for artifact in discovery.artifacts : artifact.path if artifact.name == "candidates"])}

          Independently verify the company's relationship to the exact node.
          Investigate control mechanism, supplier maturity, qualification status,
          peer alternatives, switching constraints, and concrete falsification
          conditions. This is a buyer continuity-risk assessment, not an
          investment score or recommendation.
          ${local.source_tool_guidance}
          ${local.evidence_registration_guidance}
          ${var.use_pplx ? format("Perplexity snapshot_dir: %s/%03d-%03d/sources", block_wd(), discovery_index + 1, candidate_index + 1) : ""}
          Task workspace_dir:
          "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}"
          block_wd() is the shared dynamic-block directory, not this task's
          directory. Write every task-owned file beneath the task workspace_dir
          above. Pass that exact workspace_dir to every configured evidence and
          submission typed tool; the tools create it if it does not exist.

          Register retained sources and stage atomic claims with the evidence
          tools, using the task workspace_dir and ledger_path
          "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/evidence-ledger.json".
          Claim IDs must begin with
          "assessment-${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}-".
          A supplier_maturity claim must use exactly research, validation,
          order_received, batch_delivery, mass_production, primary_supplier, or
          unknown.

          Identify the small set of key claim IDs that materially determines the
          candidate's legal identity and listing status, exact-node relationship,
          control mechanism, or supplier maturity. Also include any contract,
          market-share, capacity, or date claim used in the conclusion. Before
          finalizing the ledger, check the current official channels relevant to
          each key claim and call
          ${go_tool.stage_claim_freshness_checks.id} in batches of at most five.
          Use checked_at "${var.as_of_date}". Use verified_primary only when
          latest_primary_source_ids names registered authoritative primary sources
          already attached to that claim as direct supports with authority_for_claim;
          use checked_no_primary when the named official_channels were checked but
          no current primary source was found; otherwise use not_verified with a
          concise gap. Ambiguous outcomes are intentionally accepted and normalized
          to not_verified. Never fabricate a freshness result to avoid downgrade.

          Finalize the ledger in candidate mode with source_registry_path
          "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/source-registry.json",
          the topic and cutoff above, and empty scope_artifact and track.

          Then call ${go_tool.submit_candidate_assessment.id} with artifact_path
          "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/assessment.json",
          the task workspace_dir above, ledger path, controlled
          relationship_maturity, evidence_claim_ids, the key_claim_ids checked
          above,
          peer alternatives, switching constraints, falsification conditions,
          and conclusion. A missing or incomplete current-source check must not
          cause repeated research: the typed tool keeps the candidate visible but
          downgrades its effective relationship and maturity. Keep weak, disputed,
          pending, and rejected relationships visible.
        PROMPT
        tool_ids = concat(local.pplx_tool_ids, local.evidence_tool_ids, [
          go_tool.submit_candidate_assessment.id,
        ])
        tool_call_quota  = local.pplx_tool_call_quota
        terminate_tool_id = go_tool.submit_candidate_assessment.id
        disallowed_tools  = local.research_disallowed_tools
        permission        = "approve_all"
        artifacts = [
          {
            name      = "evidence_ledger"
            type      = "file"
            path      = "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/evidence-ledger.json"
            required  = true
            non_empty = true
          },
          {
            name      = "source_registry"
            type      = "file"
            path      = "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/source-registry.json"
            required  = true
            non_empty = true
          },
          {
            name      = "assessment"
            type      = "file"
            path      = "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/assessment.json"
            required  = true
            non_empty = true
          },
        ]
        retry = null
        qc = {
          criteria = {
            chain_fit = "Judge whether the assessed company really controls or critically supplies the discovery node, rather than an adjacent process, customer, or generic market exposure."
            maturity = "Judge whether relationship maturity matches what the evidence actually establishes and never promotes validation into an order, delivery, production, or primary-supplier relationship."
            evidence_entailment = "Judge whether the cited claims semantically support the control mechanism, exact-node relationship, and conclusion, while preserving challenges, disputes, and analytical inference."
            freshness = "Judge whether the current-source check examined the official channels that a reasonable analyst would use for the key company claims. A pending or incomplete check may remain visible but must not be promoted into a verified relationship or maturity conclusion."
            alternatives = "Verify peer alternatives, switching constraints, and falsification conditions are concrete rather than generic caveats."
            methodology = "Verify there is no investment score, aggregate score, demand-inflection factor, catalyst timing, or stock recommendation."
          }
          model_provider   = model_provider.primary
          model            = local.qc_model
          reasoning_effort = var.reasoning_effort
          max_qc_rounds    = var.max_qc_rounds
          disallowed_tools = local.semantic_qc_disallowed_tools
          permission       = "approve_all"
        }
      }
    ]
  ])
}

research "static" "reconcile_report_evidence" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the final evidence reconciliation chair. Reconcile accepted chain,
    candidate-discovery, and candidate-assessment claims before report writing.
    Never search for new evidence or erase an unresolved contradiction.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Reconciled chain evidence: ${one(research.static.reconcile_chain_evidence.artifact).path}

    Candidate-discovery ledgers:
    ${join("\n", [for task in research.dynamic.discover_candidates.tasks : "- ${one([for artifact in task.artifacts : artifact.path if artifact.name == "evidence_ledger"])}"])}

    Candidate-assessment ledgers:
    ${join("\n", [for task in research.dynamic.assess_candidates.tasks : "- ${one([for artifact in task.artifacts : artifact.path if artifact.name == "evidence_ledger"])}"])}

    Candidate-assessment artifacts:
    ${join("\n", [for task in research.dynamic.assess_candidates.tasks : "- ${one([for artifact in task.artifacts : artifact.path if artifact.name == "assessment"])}"])}

    Call ${go_tool.prepare_evidence_reconciliation.id} with artifact_path
    "${block_wd()}/evidence-resolution.json" and ledger_paths containing the
    reconciled chain evidence followed by every discovery and assessment ledger,
    plus assessment_paths containing every candidate-assessment artifact above.
    Read its draft and resolve every returned conflict ID one at a time with
    ${go_tool.resolve_evidence_conflict.id}. Prefer only directly supported,
    temporally applicable claims; preserve both when period or variant differs;
    otherwise leave the conflict unresolved. Finish with
    ${go_tool.finalize_evidence_reconciliation.id}.
  PROMPT
  tool_ids = [
    go_tool.prepare_evidence_reconciliation.id,
    go_tool.resolve_evidence_conflict.id,
    go_tool.finalize_evidence_reconciliation.id,
  ]
  terminate_tool_id = go_tool.finalize_evidence_reconciliation.id
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "evidence_resolution" {
    type      = "file"
    path      = "${block_wd()}/evidence-resolution.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      integration = "Judge whether reconciliation preserves all materially distinct company, relationship, period, and product-variant evidence needed for the final report. Treat ledger inclusion and conflict-decision coverage enforced by the typed tools as authoritative."
      semantics = "Verify selected claims really address the same product variant, period, organization, and relationship; do not resolve apparent conflicts created by different qualifiers."
      preservation = "Verify disputed and unresolved claims remain visible for synthesis and no lower-authority claim is silently upgraded."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "synthesize" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the senior chokepoint editor. Produce a buyer continuity-risk report that
    first explains the complete declared product and supply-chain structure,
    then separates structural chokepoint conclusions from company hypotheses.
    Distinguish generic reference-chain knowledge from facts confirmed, reported,
    inferred, or unknown for the target named by the topic. Keep that evidence
    strength separate from whether a claim is clean, challenged, or disputed.
    Preserve rejected candidates, unresolved coverage, variant differences, and
    every material uncertainty.
    Do not recommend stocks, identify best investments, or infer investment
    attractiveness from supplier relationships. Candidate companies are included
    only to explain control, supply, alternatives, and relationship maturity.

    Never perform new research. Use only accepted artifacts and the final
    reconciled evidence ledger. Preserve gaps instead of filling them from memory.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Brainstorm: ${one([for item in research.static.brainstorm.artifact : item.path if item.name == "brainstorm"])}
    Scope: ${one([for item in research.static.brainstorm.artifact : item.path if item.name == "scope"])}
    Audited supply chain: ${one(research.static.select_chokepoints.artifact).path}
    Final reconciled evidence: ${one(research.static.reconcile_report_evidence.artifact).path}

    Candidate discovery artifacts:
    ${join("\n", [for task in research.dynamic.discover_candidates.tasks : "- ${one([for artifact in task.artifacts : artifact.path if artifact.name == "candidates"])}"])}

    Candidate assessment artifacts:
    ${join("\n", [for task in research.dynamic.assess_candidates.tasks : "- ${one([for artifact in task.artifacts : artifact.path if artifact.name == "assessment"])}"])}

    Read every artifact.
    Write ${block_wd()}/report.md. Begin with scope, product variants, and the
    complete declared reference chain: layered components, manufacturing and
    assembly stages, equipment and material boundaries, and downstream system
    qualification. Explain how inputs are transformed, assembled, tested, and
    qualified. Include ordinary nodes, explicit unknowns, and coverage status;
    do not reduce the chain to selected chokepoints.

    Then map what is confirmed, reported, inferred, or unknown for the named
    target, and separately identify claims that are challenged or disputed,
    followed by the critical paths, why each selected node is a chokepoint,
    candidates grouped by node, controlled supplier maturity, alternatives,
    rejected or unverified relationships, falsification conditions, and
    limitations. Show delivery impact, substitutability, supplier
    concentration, switching-time range, recovery-time range, and evidence status
    separately. Do not calculate an aggregate score or rank unknown-heavy items.

    Atomize every material factual or analytical conclusion into independently
    auditable clauses, each with its own stable report claim ID. A readable
    sentence may contain several clauses, but each substantive clause must have
    its own immediately adjacent [^REPORT-ID] marker. Do not hide several factual
    assertions behind one marker.

    Stage those clauses in batches of at most five with
    ${go_tool.stage_report_claims.id}, manifest_path
    "${block_wd()}/report-manifest.json". Set claim_kind to fact or inference.
    Each staged statement must appear text-equivalently in report.md immediately
    followed, allowing only whitespace, by [^REPORT_CLAIM_ID], and its
    supporting_claim_ids must exist in the final reconciled evidence artifact.
    Never cite a claim whose reconciliation_availability is excluded or
    unresolved. If a key-claim review is pending or has a downgraded effective
    evidence status, keep the company in a clearly unverified section and do
    not use it in a formal relationship, ranking, or recommendation-like claim.
    Finish by calling ${go_tool.finalize_report_manifest.id} with report_path,
    manifest_path, and evidence_paths containing exactly the one final reconciled
    evidence-resolution.json above.
    Do not hand-maintain a separate URL table or footnote block. The finalizer
    resolves every supporting and contradicting claim to deduplicated canonical
    URLs and writes the authoritative footnote definitions into report.md. Thus
    every substantive report claim remains directly traceable to its URLs.
  PROMPT
  tool_ids = [
    go_tool.stage_report_claims.id,
    go_tool.finalize_report_manifest.id,
  ]
  terminate_tool_id = go_tool.finalize_report_manifest.id
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
    required  = true
    non_empty = true
  }

  artifact "report_manifest" {
    type      = "file"
    path      = "${block_wd()}/report-manifest.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      coverage_and_structure = "Judge whether the report gives a decision-useful explanation of every materially distinct product branch, transformation stage, ordinary dependency, chokepoint, and candidate, including important unknowns, rather than merely reproducing an inventory."
      target_mapping = "Judge whether generic reference-chain knowledge is separated from target-specific evidence, materially different product variants are kept distinct, and structural chokepoints are not confused with company-specific supply relationships."
      claim_semantics = "Use ${go_tool.read_report_claim_evidence.id} with report_manifest_path ${block_wd()}/report-manifest.json and batches of at most ten report claim IDs to inspect only the evidence needed for each clause. Judge whether every substantive clause is genuinely atomic and whether its upstream claims logically entail the complete clause without extrapolation, concept substitution, or silent qualifier loss. Do not redo marker, ID, path, URL, or exact-text validation already enforced by the typed finalizer."
      source_and_freshness = "Judge whether source classifications are semantically credible, inference remains inference, independent reporting is genuinely independent, and final-company claims with incomplete current-source checks remain pending rather than entering formal ranking or recommendation-like conclusions."
      methodology = "Verify the report is strictly a buyer continuity-risk analysis: no aggregate score, unknown-heavy ranking, best-investment conclusion, demand-inflection score, catalyst timing, valuation claim, or stock recommendation is allowed."
      uncertainty = "Judge whether challenges, disputes, rejected relationships, freshness gaps, and unresolved reconciliation outcomes remain visible and calibrated instead of being collapsed into false certainty."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    tool_ids         = [go_tool.read_report_claim_evidence.id]
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

output "report_path" {
  description = "Final Markdown chokepoint report."
  value       = one([for item in research.static.synthesize.artifact : item.path if item.name == "report"])
}

output "scope_path" {
  description = "Machine-readable product boundary and coverage inventory."
  value       = one([for item in research.static.brainstorm.artifact : item.path if item.name == "scope"])
}

output "supply_chain_path" {
  description = "Machine-readable evidence-backed supply-chain graph and selected chokepoints."
  value       = one(research.static.select_chokepoints.artifact).path
}

output "report_manifest_path" {
  description = "Machine-readable report claims and their upstream evidence claim IDs."
  value       = one([for item in research.static.synthesize.artifact : item.path if item.name == "report_manifest"])
}

output "evidence_resolution_path" {
  description = "Final merged claim ledger with explicit conflict resolutions."
  value       = one(research.static.reconcile_report_evidence.artifact).path
}
