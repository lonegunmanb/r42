locals {
  high_impact_model = trimspace(var.high_impact_model) != "" ? var.high_impact_model : var.model
  qc_model = (
    trimspace(var.qc_model) != "" ? var.qc_model : local.high_impact_model
  )
  pplx_tool_ids = var.use_pplx ? [
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ] : []
  pplx_tool_call_quota = {
    for tool_id in [module.pplx_tools.pplx_fetch_tool_id] : tool_id => var.pplx_tool_call_quota
    if var.use_pplx
  }
  research_disallowed_tools = concat(
    ["ask_user"],
    var.use_pplx ? ["web_search", "web_fetch"] : [],
  )
  offline_disallowed_tools = distinct(concat(
    local.research_disallowed_tools,
    ["web_search", "web_fetch"],
  ))
  semantic_qc_disallowed_tools = distinct(concat(
    local.offline_disallowed_tools,
    ["powershell", "bash", "shell", "edit", "task"],
  ))
  evidence_tool_ids = [
    go_tool.register_evidence_source.id,
    go_tool.stage_evidence_claims.id,
    go_tool.stage_claim_freshness_checks.id,
    go_tool.stage_evidence_gaps.id,
    go_tool.finalize_evidence_ledger.id,
  ]
  source_tool_guidance = var.use_pplx ? join("\n", [
    "Use ${module.pplx_tools.pplx_pro_search_tool_id} to discover current sources",
    "and ${module.pplx_tools.pplx_fetch_tool_id} to fetch every source retained",
    "as evidence. Every fetch call must include url and the absolute snapshot_dir",
    "stated below. Record the final snapshot_path returned by each fetch.",
    ]) : join("\n", [
    "Use the built-in web_search tool to discover current sources and web_fetch",
    "to read every source retained as evidence.",
  ])
  evidence_registration_guidance = <<-GUIDANCE
    Register every retained snapshot with ${go_tool.register_evidence_source.id}.
    Set url to the fetched page and canonical_url to the original publication URL;
    they may be identical. Classify sources broadly rather than repeatedly guessing
    at marginal categories: authoritative_primary/official_filing/official_product/
    official_statement/regulator; qualified_media/credible_media/named_media/
    peer_reviewed/industry_research; other_published; or lead_only/self_media/
    forum/aggregator. An unfamiliar source_type is valid and is conservatively
    normalized to unknown. Use reporting_basis public_document, named_source,
    anonymous_sources, direct_observation, published_methodology, or unspecified,
    and provenance original, syndication, aggregation, or unknown.

    When staging a claim, set authority_for_claim on each evidence edge only when
    that source is authoritative for that exact assertion. One direct qualified
    media origin with a named, document-backed, observed, or published-methodology
    basis may confirm a claim; anonymous reporting requires two independent
    qualified-media origins. Syndications and aggregations are not independent.
    Self-media, forums, and aggregators are discovery leads only and must not
    directly support a substantive final claim. Record explicit inference in the
    claim's inference field; inference remains inferred regardless of source rank.
  GUIDANCE

  graph_tracks = {
    product_structure = {
      title = "Product structure and BOM"
      question = "Verify product, assembly, component, subcomponent, and BOM branches. Identify where several critical paths converge."
    }
    manufacturing_testing = {
      title = "Manufacturing, packaging, and testing"
      question = "Map manufacturing, packaging, testing, inspection, yield-control, and rework steps that can constrain delivery."
    }
    equipment = {
      title = "Specialized equipment and tooling"
      question = "Identify equipment and tooling that constrain capacity, process generation, yield, qualification, or recovery time."
    }
    materials_chemicals = {
      title = "Materials, chemicals, and consumables"
      question = "Map specialized materials, chemicals, substrates, consumables, and the point where each branch becomes a liquid commodity."
    }
    qualification_integration = {
      title = "Qualification and integration lock-in"
      question = "Investigate customer qualification, certification, firmware or design integration, supplier switching, and time-to-recover dependencies."
    }
  }
}
