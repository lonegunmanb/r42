model_provider "primary" {
  type        = "openai"
  endpoint    = "https://api.deepseek.com"
  wire_api    = "responses"
  api_key_ref = "DEEPSEEK_KEY"
}

module "pplx_tools" {
  source = "./modules/pplx_tools"
}

research "static" "external_tool_artifact" {
  model_provider   = model_provider.primary
  model            = "deepseek-v4-flash"
  reasoning_effort = "low"
  system_prompt = <<-PROMPT
    You are testing phased research with two external acquisition tools.

    During Collection, call ${module.pplx_tools.pplx_pro_search_tool_id}, then
    ${module.pplx_tools.pplx_fetch_tool_id}. Register the fetched Markdown path
    with r42_register_artifact and submit the collection checkpoint.

    During closed Research, do not call acquisition tools. Use
    r42_list_artifacts and r42_read_artifact to verify the registered artifact,
    then finish with a concise confirmation of the Python release found.

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
    The fetch tool writes ${self.artifact.artifact.path} itself.
    During Collection, do not checkpoint until the fetch succeeds, reports that
    exact path, and r42_register_artifact accepts it. During closed Research,
    verify the artifact through the r42 artifact readers before responding.
  PROMPT
  collection_tool_ids = [
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ]
  disallowed_tools = ["web_search", "web_fetch"]
  permission        = "approve_all"

  artifact "artifact" {
    type      = "file"
    path      = "${block_wd()}/artifact.md"
	description = "Markdown artifact returned by the configured external fetch process"
    required  = true
    non_empty = true
  }
}

output "artifact_path" {
  description = "Absolute path to the Markdown artifact written by the external fetch process."
  value       = research.static.external_tool_artifact.artifact.artifact.path
}
