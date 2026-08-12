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
  source_tool_guidance = var.use_pplx ? join("\n", [
    "Use ${module.pplx_tools.pplx_pro_search_tool_id} to discover current sources",
    "and ${module.pplx_tools.pplx_fetch_tool_id} to fetch every source retained",
    "as evidence. Every fetch call must include url and the absolute snapshot_dir",
    "stated below. Record the final snapshot_path returned by each fetch.",
    ]) : join("\n", [
    "Use the built-in web_search tool to discover current sources and web_fetch",
    "to read every source retained as evidence.",
  ])
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
