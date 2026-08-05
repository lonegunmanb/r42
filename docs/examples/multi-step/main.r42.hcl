model_provider "primary" {
  type        = "openai"
  endpoint    = "https://api.deepseek.com"
  wire_api    = "responses"
  api_key_ref = "DEEPSEEK_KEY"
}

module "pplx_tools" {
  source = "./modules/pplx_tools"
}

research "static" "external_tool_snapshot" {
  model_provider   = model_provider.primary
  model            = "deepseek-v4-flash"
  reasoning_effort = "low"
  system_prompt = <<-PROMPT
    You are testing two external research tools. You must call both tools in order:
    first ${module.pplx_tools.pplx_pro_search_tool_id}, then ${module.pplx_tools.pplx_fetch_tool_id}.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt = <<-PROMPT
    Search for the latest stable Python release announcement on python.org.
    Select one python.org result and call ${module.pplx_tools.pplx_fetch_tool_id} with its URL.
    The fetch tool writes ${block_wd()}/snapshot.md itself.
    Do not finish until ${module.pplx_tools.pplx_fetch_tool_id} succeeds and reports that exact snapshot path.
  PROMPT
  tool_ids = [
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ]
  permission = "approve_all"

  artifact "snapshot" {
    type      = "file"
    path      = "${block_wd()}/snapshot.md"
    required  = true
    non_empty = true
  }
}

output "snapshot_path" {
  description = "Absolute path to the Markdown snapshot written by the external fetch process."
  value       = one(research.static.external_tool_snapshot.artifact).path
}
