module "pplx_tools" {
  source = "./modules/pplx_tools"
}

research "static" "primary_source_baseline" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You establish the authoritative primary-source baseline for a supply-chain
    study. Keep each claim atomic and time bounded. Confirm only what an official
    filing, regulator record, official product document, or official statement
    directly says. Never infer an unnamed counterparty.

    Never use PowerShell, a shell, curl, wget, scripts, or command-line programs
    to search or download content. Stop searching when the newest relevant
    primary corpus is represented and remaining gaps are explicit.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}
    Workspace: "${block_wd()}"

    Find the newest official filings, regulator records, product specifications,
    customer or contract disclosures, and applicable rules available by the
    cutoff. Save every retained source as a Markdown snapshot. Register each
    source with ${go_tool.register_evidence_source.id}, using workspace_dir
    "${block_wd()}" and ledger_path "${block_wd()}/evidence-ledger.json".

    Submit atomic cards in batches of at most five with
    ${go_tool.submit_claim_cards.id}. Use confirmed only when an authoritative
    primary source directly states the claim. Use reported for direct published
    reporting. Use inferred only for a conclusion derived from existing card IDs;
    inferred cards use derived_from and no source_id, quote, or locator. Do not
    submit unknown as a claim card.

    Finish with ${go_tool.finalize_claim_cards.id}:
    workspace_dir "${block_wd()}", claims_path "${block_wd()}/claims.json",
    source_registry_path "${block_wd()}/source-registry.json", as_of_date
    "${var.as_of_date}", and allow_empty false.
  PROMPT
  collection_tool_ids = local.pplx_tool_ids
  tool_ids = [
    go_tool.register_evidence_source.id,
    go_tool.submit_claim_cards.id,
    go_tool.finalize_claim_cards.id,
  ]
  tool_call_quota   = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.finalize_claim_cards.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "claims" {
    type      = "file"
    path      = "${block_wd()}/claims.json"
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
      primary_coverage = "Judge whether the newest material primary documents available by the cutoff were retained. Identify every omission that could change the study."
      entailment = "Judge whether every confirmed card is directly entailed by its authoritative source, including named parties, product variant, period, and qualifiers. Treat typed-tool path, URL, date, and quotation matching as authoritative."
      atomicity = "Judge whether each card makes one independently auditable assertion rather than bundling several facts behind one citation."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "brainstorm" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    Define a coverage-complete product and supply-chain scope before judging any
    risk. Keep generic architecture separate from facts about the named target.
    Do not select companies or chokepoints.

    Never use PowerShell, a shell, curl, wget, scripts, or command-line programs
    to search or download content. Only configured source tools may access the web.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Validated primary-source baseline JSON:
    ${research.static.primary_source_baseline.result}
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}

    Use the validated baseline and this workflow's registered snapshots. Write
    "${block_wd()}/brainstorm.md" with the
    focal boundary, product variants, layered components, transformation stages,
    manufacturing, packaging, testing, module integration, downstream system
    qualification, competing dependency hypotheses, and open questions for the
    five tracks.

    Stop upstream decomposition at production equipment and input materials.
    Continue downstream through module components and system qualification.
    Then call ${go_tool.submit_supply_chain_scope.id} with artifact_path
    "${block_wd()}/scope.json". Every expected component and stage must be
    assigned to one or more coverage items and exactly one of the five tracks.
  PROMPT
  collection_tool_ids = local.pplx_tool_ids
  tool_ids            = [go_tool.submit_supply_chain_scope.id]
  tool_call_quota   = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.submit_supply_chain_scope.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

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
      scope = "Judge whether every decision-relevant product branch, component, stage, equipment or material boundary, service dependency, and qualification step is represented."
      separation = "Judge whether generic architecture is clearly separated from target-specific facts and materially different variants are not silently merged."
      uncertainty = "Judge whether competing explanations and material open questions remain explicit."
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
    Validated scope JSON:
    ${research.static.brainstorm.result}
    Validated primary-source baseline JSON:
    ${research.static.primary_source_baseline.result}
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity snapshot_dir: %s/sources", block_wd()) : ""}
    Workspace: "${block_wd()}"

    Research only this track and address every assigned coverage item. Preserve
    product variants and distinguish generic-chain facts from target facts.
    Save every retained source under the snapshots/sources directory and register
    it with ${go_tool.register_evidence_source.id}, using ledger_path
    "${block_wd()}/evidence-ledger.json".

    Submit one independently auditable claim per card with
    ${go_tool.submit_claim_cards.id}, in batches of at most five. Prefix IDs
    with "${each.key}-". Use confirmed, reported, and inferred exactly as
    described by the tool; record unresolved questions in the track narrative,
    not as fake claims.

    Finish with ${go_tool.finalize_claim_cards.id}: workspace_dir
    "${block_wd()}", claims_path "${block_wd()}/claims.json",
    source_registry_path "${block_wd()}/source-registry.json", as_of_date
    "${var.as_of_date}", and allow_empty false.
  PROMPT
  collection_tool_ids = local.pplx_tool_ids
  tool_ids = [
    go_tool.register_evidence_source.id,
    go_tool.submit_claim_cards.id,
    go_tool.finalize_claim_cards.id,
  ]
  tool_call_quota   = local.pplx_tool_call_quota
  terminate_tool_id = go_tool.finalize_claim_cards.id
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "claims" {
    type      = "file"
    path      = "${block_wd()}/claims.json"
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
      track_scope = "Judge whether the cards answer this track's assigned questions and preserve material unknowns without drifting into company selection."
      entailment = "Judge whether each source actually entails its atomic claim with the stated party, period, product branch, and qualifier. Treat typed-tool schema, path, URL, date, and quote matching as authoritative."
      inference = "Judge whether inferred cards follow from their premise cards without silently upgrading correlation, supplier marketing, or general industry structure into a target-specific fact."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "build_supply_chain" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    Build a readable reference supply-chain map from accepted scope and atomic
    claim cards. Do not score, rank, or pre-declare chokepoints. Select only a
    short list of nodes whose continuity risk genuinely warrants assessment.
    Companies are not supply-chain nodes.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Validated scope JSON:
    ${research.static.brainstorm.result}
    Validated baseline and track claim JSON:
    ${research.static.primary_source_baseline.result}
    ${join("\n", [for item in research.static.graph_track : item.result])}

    Use only the validated JSON above. Call ${go_tool.submit_supply_chain_map.id} once with
    workspace_dir "${block_wd()}", artifact_path
    "${block_wd()}/supply-chain.json", the exact topic and scope_path, and
    claim_paths containing the baseline plus all five track claim files.

    Map ordinary nodes and edges across the full declared product boundary.
    Keep each node's stages and product branches explicit. Attach only claim IDs
    that substantively support the node or edge. Put evidence gaps in unknowns.
    assessment_targets is not a chokepoint list: include only nodes for which the
    next stage should separately test actual target dependency, alternatives,
    switching versus buffer time, applicable scenario, and falsification.
  PROMPT
  tool_ids         = [go_tool.submit_supply_chain_map.id]
  terminate_tool_id = go_tool.submit_supply_chain_map.id
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
      completeness = "Judge whether the map covers the decision-relevant product branches and stages defined by scope without becoming an encyclopedia."
      target_selection = "Judge whether every assessment target could plausibly affect continuity and whether important candidates were omitted. A required input or commercially attractive supplier is not automatically a risk node."
      evidence = "Judge whether cited claims semantically support the mapped relationship and target rationale. Treat typed-tool graph references as authoritative."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "dynamic" "assess_nodes" {
  tasks = [
    for index, target in jsondecode(research.static.build_supply_chain.result).assessment_targets : {
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt = <<-PROMPT
        Assess one supply-chain node as a falsifiable buyer continuity-risk
        question. Scope and proof strength are independent dimensions. Do not
        search, score companies, or turn uncertainty into a conclusion.
      PROMPT
      prompt = <<-PROMPT
        Topic: ${var.topic}
        Evidence cutoff: ${var.as_of_date}
        Assessment target: ${jsonencode(target)}
        Validated supply-chain map JSON:
        ${research.static.build_supply_chain.result}
        Validated claim JSON:
        ${research.static.primary_source_baseline.result}
        ${join("\n", [for item in research.static.graph_track : item.result])}

        Use the included map and claims. Answer five questions: which scenario is
        affected (current production, expansion/upgrade, or a product branch);
        whether the named target actually depends on this node; which alternatives
        are already qualified and have usable capacity; whether switching and
        recovery exceed known buffers; and what evidence would falsify the view.

        Call ${go_tool.submit_node_assessment.id} with task workspace_dir
        "${block_wd()}/${format("%03d", index + 1)}", artifact_path
        "${block_wd()}/${format("%03d", index + 1)}/node-assessment.json",
        all claim_paths above, and the target node. risk_scope is global or
        branch; conclusion is independently confirmed, candidate, or not_proven.
        Keep unknowns and falsification_conditions explicit.
      PROMPT
      tool_ids          = [go_tool.submit_node_assessment.id]
      terminate_tool_id = go_tool.submit_node_assessment.id
      disallowed_tools  = local.offline_disallowed_tools
      permission        = "approve_all"
      artifacts = [{
        name      = "node_assessment"
        type      = "file"
        path      = "${block_wd()}/${format("%03d", index + 1)}/node-assessment.json"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          dependency = "Judge whether public evidence actually establishes the target's dependency rather than only an industry-wide possibility."
          alternatives = "Judge whether qualified alternatives, available capacity, switching constraints, and buffers are treated separately and honestly."
          conclusion = "Judge whether confirmed, candidate, or not_proven follows from the evidence for the stated scenario and branch. Not proven means insufficient public evidence, not no risk."
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

research "dynamic" "prioritize_companies" {
  tasks = [
    for index, assessment in research.dynamic.assess_nodes.tasks : {
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = var.research_system_prompt
      prompt = <<-PROMPT
        Topic: ${var.topic}
        Evidence cutoff: ${var.as_of_date}
        Market: ${var.market}
        Validated node assessment JSON:
        ${assessment.result}
        Validated existing claim JSON:
        ${research.static.primary_source_baseline.result}
        ${join("\n", [for item in research.static.graph_track : item.result])}
        Task workspace: "${block_wd()}/${format("%03d", index + 1)}"
        ${local.source_tool_guidance}
        ${var.use_pplx ? format("Perplexity snapshot_dir: %s/%03d/sources", block_wd(), index + 1) : ""}

        This single stage asks whether any public company deserves more research
        because of this exact assessed node. Investigate at most
        ${var.max_candidates_per_chokepoint} companies. For each, distinguish an
        existing supplier, qualified alternative, related-product-only company,
        or unverified lead. Verify the exact legal entity and security.

        Register retained sources with ${go_tool.register_evidence_source.id}
        using workspace_dir above and ledger_path
        "${block_wd()}/${format("%03d", index + 1)}/evidence-ledger.json".
        Submit atomic relationship and economic-exposure cards with
        ${go_tool.submit_claim_cards.id}. Never claim that a related product
        proves adoption, qualification, market share, revenue, or profit impact.

        Finalize cards with ${go_tool.finalize_claim_cards.id}, claims_path
        "${block_wd()}/${format("%03d", index + 1)}/claims.json",
        source_registry_path
        "${block_wd()}/${format("%03d", index + 1)}/source-registry.json",
        cutoff above, and allow_empty true.

        Then call ${go_tool.submit_company_priorities.id} with artifact_path
        "${block_wd()}/${format("%03d", index + 1)}/company-priorities.json",
        the node assessment path, and claim_paths containing the baseline, all
        five track files, and this task's claims.json.

        A means the node matters, the exact company role and relationship are
        confirmed, and economic impact still needs research. B means the node
        matters but relationship, qualification, or benefit mechanism is
        incomplete. C is only an industry or related-product lead.
        do_not_research means the node or company link is too weak. These are
        research priorities, never investment ratings. An empty list is valid.
      PROMPT
      collection_tool_ids = local.pplx_tool_ids
      tool_ids = [
        go_tool.register_evidence_source.id,
        go_tool.submit_claim_cards.id,
        go_tool.finalize_claim_cards.id,
        go_tool.submit_company_priorities.id,
      ]
      tool_call_quota   = local.pplx_tool_call_quota
      terminate_tool_id = go_tool.submit_company_priorities.id
      disallowed_tools  = local.research_disallowed_tools
      permission        = "approve_all"
      artifacts = [
        {
          name      = "claims"
          type      = "file"
          path      = "${block_wd()}/${format("%03d", index + 1)}/claims.json"
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
          name      = "company_priorities"
          type      = "file"
          path      = "${block_wd()}/${format("%03d", index + 1)}/company-priorities.json"
          required  = true
          non_empty = true
        },
      ]
      retry = null
      qc = {
        criteria = {
          company_gate = "Judge whether each priority follows from the assessed node and the exact company's proven role. Industry relevance alone is C at most."
          relationship = "Judge whether claims distinguish product availability, validation, orders, delivery, production use, and primary-supplier status without upgrading one into another."
          economic_boundary = "Judge whether revenue, profit, order, capacity, and competitive significance remain unknown unless directly supported. A/B/C are follow-up priorities, not investment recommendations."
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

research "static" "synthesize" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    Write a concise, company-first research-priority report. Every substantive
    clause must cite one original atomic claim ID. Do not create report-level
    claims, scores, investment ratings, or recommendations. Do not research new
    facts. Preserve not-proven nodes, rejected companies, and unknowns.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Validated scope and supply-chain JSON:
    ${research.static.brainstorm.result}
    ${research.static.build_supply_chain.result}
    Validated node assessments:
    ${join("\n", [for task in research.dynamic.assess_nodes.tasks : task.result])}
    Validated company priorities:
    ${join("\n", [for task in research.dynamic.prioritize_companies.tasks : task.result])}
    Validated baseline and track claims:
    ${research.static.primary_source_baseline.result}
    ${join("\n", [for item in research.static.graph_track : item.result])}

    Use only the validated JSON above and write "${block_wd()}/report.md" in this order:
    1. companies worth further research, showing A/B/C/do-not-research, exact
       node and role, strongest evidence, largest unknown, and next check;
    2. confirmed and candidate global or branch-specific risk nodes;
    3. separate views for current production, expansion/upgrade, and product
       branches;
    4. concise node assessments and falsification conditions;
    5. not-proven nodes and unresolved questions;
    6. the decision-relevant supply-chain map and scope limitations.

    A/B/C are research priorities, never investment ratings. Put
    [[claim:CLAIM-ID]] immediately after every substantive atomic clause. If one
    sentence contains several facts, split it or cite each clause separately.
    Do not cite internal artifact counts or methodology statements as external
    facts. Do not write a URL table or RPT IDs.

    Finish with ${go_tool.finalize_research_report.id}, report_path
    "${block_wd()}/report.md", and claim_paths containing every claim file above.
    The tool replaces claim markers with original URLs and appends the referenced
    evidence cards.
  PROMPT
  tool_ids          = [go_tool.finalize_research_report.id]
  terminate_tool_id = go_tool.finalize_research_report.id
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      decision_usefulness = "Judge whether the first page gives a defensible company research-priority list with the exact node, role, strongest evidence, largest unknown, and next check."
      entailment = "Judge whether each cited atomic claim semantically supports the adjacent report clause without concept, party, period, product-branch, or qualifier substitution. Treat marker, ID, path, URL, and quotation checks as authoritative."
      risk_scope = "Judge whether global versus branch scope and current production versus expansion/upgrade scenarios are separated from proof strength."
      restraint = "Reject investment recommendations, composite scores, false precision, and any company promotion based only on industry relevance or a related product."
      uncertainty = "Judge whether not-proven nodes, rejected companies, missing economic exposure, alternatives, buffers, and falsification conditions remain visible."
    }
    model_provider   = model_provider.primary
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

output "report_path" {
  description = "Final company-first research-priority report with direct source URLs."
  value       = one([for item in research.static.synthesize.artifact : item.path if item.name == "report"])
}

output "scope_path" {
  description = "Machine-readable product boundary and coverage inventory."
  value       = one([for item in research.static.brainstorm.artifact : item.path if item.name == "scope"])
}

output "supply_chain_path" {
  description = "Machine-readable reference supply chain and node-assessment targets."
  value       = one(research.static.build_supply_chain.artifact).path
}

output "node_assessment_paths" {
  description = "Node assessments with independent risk scope and evidence conclusion."
  value       = [for task in research.dynamic.assess_nodes.tasks : one(task.artifacts).path]
}

output "company_priority_paths" {
  description = "Company follow-up research priorities by assessed node."
  value       = [for task in research.dynamic.prioritize_companies.tasks : one([for artifact in task.artifacts : artifact.path if artifact.name == "company_priorities"])]
}
