external_tool "pplx_pro_search" {
  description = "Search current web sources with the Perplexity Search API. Call with a concise query."
  program     = ["python", "${path.module}/pplx_external.py", "search"]

  input_type = object({
    query = string
  })

  output_type = object({
    results = list(object({
      title        = string
      url          = string
      snippet      = string
      date         = string
      last_updated = string
      source       = string
    }))
  })
}

external_tool "pplx_fetch" {
  description = "Fetch one absolute HTTP or HTTPS URL through Perplexity and, only on success, save artifact.md in this research block's workspace. If Perplexity cannot retrieve usable content, the tool fails with a clear message and writes no file. Do not register an artifact after that failure; try another source URL."
  program     = ["python", "${path.module}/pplx_external.py", "fetch"]

  input_type = object({
    url = string
  })

  output_type = object({
    title         = string
    url           = string
    artifact_path = string
    fetched_at    = string
  })
}

output "pplx_pro_search_tool_id" {
  description = "Generated ID of the Perplexity search tool."
  value       = external_tool.pplx_pro_search.id
}

output "pplx_fetch_tool_id" {
  description = "Generated ID of the Perplexity fetch tool."
  value       = external_tool.pplx_fetch.id
}
