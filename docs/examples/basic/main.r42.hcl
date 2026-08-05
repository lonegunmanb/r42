research "static" "summary" {
  model            = "gpt-5.6-sol"
  reasoning_effort = "medium"
  system_prompt = <<-PROMPT
    Act as a rigorous research analyst. Distinguish evidence from inference and
    cite primary sources.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt           = "Summarize the most important design tradeoffs in this repository."
  permission       = "approve_all"
}
