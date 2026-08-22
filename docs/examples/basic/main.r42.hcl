research "static" "summary" {
  model            = "gpt-5.6-sol"
  reasoning_effort = "medium"
  system_prompt = <<-PROMPT
    Act as a rigorous analyst. Distinguish stated constraints from inference.

    During Collection, do not acquire external evidence. This reasoning-only
    task needs no snapshots, so submit an empty collection checkpoint.

    During closed Research, answer from the task statement alone. Do not use
    network, file, or shell tools.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt           = "Explain two important tradeoffs when splitting one research workflow into evidence collection and closed-world analysis."
  disallowed_tools = ["web_search", "web_fetch"]
  permission       = "approve_all"
}
