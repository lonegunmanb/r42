module "pplx_tools" {
  source = "./modules/pplx_tools"
}

research "static" "whiteboard" {
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You chair an exploratory supply-chain whiteboard. Generate competing
    hypotheses before committing to a graph. Separate structural chokepoints
    from fashionable companies and generic demand beneficiaries.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Market universe: ${var.market}

    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}

    Test the most important hypotheses and preserve the most decision-relevant
    sources. Write ${block_wd()}/whiteboard.md with:

    - the focal system boundary;
    - competing hypotheses about components, processes, equipment, materials,
      and qualification lock-in;
    - possible convergence points and substitution mechanisms;
    - disputed assumptions and explicit research questions for the five tracks;
    - every fetched snapshot path next to the claim it informs.

    This is ideation, not final selection. Do not name candidate companies.
  PROMPT
  tool_ids             = local.pplx_tool_ids
  typed_tool_call_quota = local.pplx_tool_call_quota
  disallowed_tools      = local.research_disallowed_tools
  permission            = "approve_all"

  artifact "whiteboard" {
    type      = "file"
    path      = "${block_wd()}/whiteboard.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      scope = "Read whiteboard.md and verify it defines one bounded focal system and does not mix industry-function nodes with company names."
      breadth = "Verify the whiteboard proposes testable hypotheses for product structure, manufacturing and testing, equipment, materials, and qualification or integration lock-in."
      evidence = "For every source-backed claim, verify the referenced snapshot exists and supports the claim; hypotheses may remain explicitly unverified."
      uncertainty = "Verify competing explanations and material unknowns are preserved rather than silently collapsed into one story."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
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
    Track: ${each.value.title}
    Assigned question: ${each.value.question}
    Whiteboard artifact: ${one(research.static.whiteboard.artifact).path}

    Read the whiteboard, then research only this track.
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}

    Finish by calling ${go_tool.submit_track_evidence.id}. Set artifact_path to
    "${block_wd()}/track-evidence.json", track to "${each.key}", and submit
    atomic findings plus exact quotes. Finding IDs and quote IDs must begin with
    "${each.key}-". Do not name or rank candidate companies.
  PROMPT
  tool_ids = concat(local.pplx_tool_ids, [
    go_tool.submit_track_evidence.id,
  ])
  typed_tool_call_quota = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.submit_track_evidence.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "evidence" {
    type      = "file"
    path      = "${block_wd()}/track-evidence.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      assignment = "Verify every finding addresses only the assigned track and distinguishes structural nodes from companies."
      quote_fidelity = "For each quote, read snapshot_path and verify exact_quote is verbatim and the locator is sufficient to find it."
      traceability = "Verify every finding cites one or more declared quote IDs, every cited quote exists, and no quote is unused."
      decision_value = "Verify each proposed node, edge, stopping boundary, or uncertainty could affect bottleneck, substitution, qualification, capacity, yield, or recovery-time analysis."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
    permission       = "approve_all"
  }
}

research "static" "select_chokepoints" {
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the supply-chain graph chair. Reconcile five independently checked
    evidence tracks into one bounded graph, then select only genuine structural
    chokepoints. A required input is not automatically a chokepoint. Preserve
    disagreements when the evidence cannot resolve them.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Whiteboard: ${one(research.static.whiteboard.artifact).path}

    Validated track artifacts:
    ${join("\n", [for item in research.static.graph_track : "- ${one(item.artifact).path}"])}

    Read every artifact.
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}

    Only perform new research to close a material gap or resolve a
    contradiction. Then call ${go_tool.submit_chokepoint_chain.id} with
    artifact_path "${block_wd()}/chokepoints.json".

    Build typed industry-function nodes and supplier-to-consumer edges. Select
    a node as a chokepoint only when evidence supports delivery impact,
    technical irreplaceability, qualification or switching cost, concentration,
    convergence, or capacity/yield constraints. Include reviewed_artifacts and
    evidence_finding_ids so every decision is traceable to the five tracks.
  PROMPT
  tool_ids = concat(local.pplx_tool_ids, [
    go_tool.submit_chokepoint_chain.id,
  ])
  typed_tool_call_quota = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.submit_chokepoint_chain.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "chokepoints" {
    type      = "file"
    path      = "${block_wd()}/chokepoints.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      coverage = "Read every validated track artifact and verify reviewed_artifacts includes all five exactly once."
      graph_integrity = "Verify node IDs are unique, every edge endpoint exists, every node reaches the focal node or has an explicit stopping boundary, and company names are absent."
      selection = "Verify each chokepoint names an existing node and is supported by evidence for structural control, not merely demand growth or investment popularity."
      traceability = "Resolve every evidence_finding_id against the track artifacts and verify it supports the claimed mechanism, substitution difficulty, and recovery time."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
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
        Market universe: ${var.market}
        Audited chokepoint: ${jsonencode(chokepoint)}
        Complete chain artifact: ${one(research.static.select_chokepoints.artifact).path}

        Discover at most ${var.max_candidates_per_chokepoint} publicly traded
        companies that directly control or critically supply this exact node.
        Exclude generic beneficiaries, downstream customers, funds, and firms
        linked only to adjacent nodes.
        ${local.source_tool_guidance}
        ${var.use_pplx ? format("Perplexity snapshot_dir: %s/%03d/sources", block_wd(), index + 1) : ""}

        Call ${go_tool.submit_candidates.id} with artifact_path
        "${block_wd()}/${format("%03d", index + 1)}/candidates.json", the exact
        chokepoint node_id, max_candidates
        ${var.max_candidates_per_chokepoint}, and evidence-backed candidates.
        An empty candidate list is valid when conclusion explains the gap.
      PROMPT
      tool_ids = concat(local.pplx_tool_ids, [
        go_tool.submit_candidates.id,
      ])
      typed_tool_call_quota = local.pplx_tool_call_quota
      terminate_tool_id = go_tool.submit_candidates.id
      disallowed_tools  = local.research_disallowed_tools
      permission        = "approve_all"
      artifacts = [{
        name      = "candidates"
        type      = "file"
        path      = "${block_wd()}/${format("%03d", index + 1)}/candidates.json"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          node_binding = "Verify every candidate directly controls or critically supplies the assigned node, not merely an adjacent node or downstream market."
          identity = "Verify company name, ticker, public market, and legal identity against fetched source snapshots."
          evidence = "Read every evidence snapshot and verify its exact quote supports the stated relationship and selection reason."
          exclusions = "Reject funds, private entities presented as public, generic beneficiaries, customers without control, and candidates exceeding max_candidates."
        }
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = 3
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
          Candidate hypothesis: ${jsonencode(candidate)}
          Audited chain: ${one(research.static.select_chokepoints.artifact).path}
          Candidate-discovery artifact: ${one(discovery.artifacts).path}

          Independently verify the company's relationship to the exact node.
          Investigate control mechanism, qualification status, capacity or
          yield constraints, peer alternatives, substitution resilience, and
          concrete falsification conditions.
          ${local.source_tool_guidance}
          ${var.use_pplx ? format("Perplexity snapshot_dir: %s/%03d-%03d/sources", block_wd(), discovery_index + 1, candidate_index + 1) : ""}

          Call ${go_tool.submit_candidate_scorecard.id} with artifact_path
          "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/scorecard.json".
          Score all eight factors from 0 through 5. A weak or rejected
          relationship must remain in the result with low evidence quality and
          an explicit conclusion; do not silently delete it.
        PROMPT
        tool_ids = concat(local.pplx_tool_ids, [
          go_tool.submit_candidate_scorecard.id,
        ])
        typed_tool_call_quota = local.pplx_tool_call_quota
        terminate_tool_id = go_tool.submit_candidate_scorecard.id
        disallowed_tools  = local.research_disallowed_tools
        permission        = "approve_all"
        artifacts = [{
          name      = "scorecard"
          type      = "file"
          path      = "${block_wd()}/${format("%03d", discovery_index + 1)}-${format("%03d", candidate_index + 1)}/scorecard.json"
          required  = true
          non_empty = true
        }]
        retry = null
        qc = {
          criteria = {
            chain_fit = "Verify the assessed node_id equals the discovery node and the relationship is controls_bottleneck, critical_supplier, or unverified based on evidence."
            score_support = "Check each of the eight 0-5 ratings against exact quotes in fetched snapshots; reject ratings whose direction or magnitude is unsupported."
            alternatives = "Verify peer alternatives, switching constraints, and falsification conditions are concrete rather than generic caveats."
            artifact_consistency = "Read scorecard.json and verify it is semantically identical to the accepted typed-tool candidate."
          }
          reasoning_effort = var.reasoning_effort
          max_qc_rounds    = 3
          permission       = "approve_all"
        }
      }
    ]
  ])
}

research "static" "synthesize" {
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the senior chokepoint editor. Produce a decision-useful report that
    separates structural supply-chain conclusions from company hypotheses.
    Preserve rejected and unverified candidates and every material uncertainty.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Whiteboard: ${one(research.static.whiteboard.artifact).path}
    Audited chain: ${one(research.static.select_chokepoints.artifact).path}

    Candidate discovery artifacts:
    ${join("\n", [for task in research.dynamic.discover_candidates.tasks : "- ${one(task.artifacts).path}"])}

    Candidate scorecard artifacts:
    ${join("\n", [for task in research.dynamic.assess_candidates.tasks : "- ${one(task.artifacts).path}"])}

    Read every artifact.
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}

    Only perform new research for a clearly identified final evidence gap.
    Write ${block_wd()}/report.md with an executive summary, the audited graph
    and critical paths, why each selected node is a chokepoint, candidates
    grouped by node, score comparisons, rejected or unverified relationships,
    falsification conditions, limitations, and a source table.
  PROMPT
  tool_ids              = local.pplx_tool_ids
  typed_tool_call_quota = local.pplx_tool_call_quota
  disallowed_tools      = local.research_disallowed_tools
  permission            = "approve_all"

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      coverage = "Read the audited chain, every discovery artifact, and every scorecard; verify every chokepoint and candidate is represented."
      separation = "Verify the report clearly separates structural chokepoint evidence from company-specific control or supply relationships."
      scoring = "Recalculate comparisons from the eight score factors and verify rejected or unverified candidates are not promoted as confirmed controllers."
      citations = "Verify material factual claims trace to exact quotes and snapshot paths, and the source table contains no invented or unused sources."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
    permission       = "approve_all"
  }
}

output "report_path" {
  description = "Final Markdown chokepoint report."
  value       = one(research.static.synthesize.artifact).path
}
