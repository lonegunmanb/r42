variable "topic" {
  type        = string
  description = "The overall question answered by the final report."

  validation {
    condition     = length(trimspace(var.topic)) > 0
    error_message = "topic must not be empty."
  }
}

variable "research_plan" {
  type        = list(string)
  description = "Optional caller-provided subquestions. A non-empty list skips the planner and runs in parallel."
  default     = []
  nullable    = true

  validation {
    condition = var.research_plan == null ? true : alltrue([
      for question in var.research_plan : length(trimspace(question)) > 0
    ])
    error_message = "research_plan must contain only non-empty subquestions."
  }
}

variable "system_prompt" {
  type        = string
  description = "System prompt shared by every independent deep-research task."
  default = <<-PROMPT
    You are one independent analyst in a deep-research matrix. Investigate only
    the assigned subquestion, but interpret it in the context of the overall
    topic. Use web search and source-reading tools. Prefer primary sources and
    preserve disagreements instead of averaging them away.

    Do not overuse search or source-reading tools. After every search and every
    source read, assess whether the information and evidence already collected
    are sufficient to answer the assigned subquestion. If they are sufficient,
    stop searching and reading, then organize and submit the result. Continue
    gathering sources only when you can identify a concrete remaining evidence
    gap that matters to the answer.

    You must finish by calling the configured terminate tool. The call must
    submit atomic knowledge claims and separate verbatim quote records. Save
    the complete material returned by every source read as Markdown under the
    current block workspace's snapshots/ directory before citing it. Every quote must include the
    exact snapshot_path, locator, URL, and verbatim text from that snapshot.
    Every claim must reference at least one quote ID, and every quote must be
    used. Before calling the tool, be ready for it to write the accepted
    payload to the declared knowledge.json artifact.
  PROMPT
}

variable "model_provider" {
  description = "BYOK model provider and retry configuration shared by every session."
  type = object({
    type             = optional(string, "openai")
    endpoint         = optional(string, "https://openrouter.ai/api/v1")
    wire_api         = optional(string, "completions")
    transport        = optional(string)
    headers          = optional(map(string))
    api_key          = optional(string)
    api_key_ref      = optional(string)
    bearer_token     = optional(string)
    bearer_token_ref = optional(string)
    retry = optional(object({
      lifecycle_retries    = optional(number, 3)
      model_call_retries   = optional(number, 6)
      interval_seconds     = optional(number, 2)
      max_interval_seconds = optional(number, 30)
      error_message_regex  = optional(list(string), [])
    }), {})
  })
}

variable "model" {
  type        = string
  description = "Copilot model used by research, QC, conflict resolution, and synthesis sessions."
  default     = "openai/gpt-5.5"
}

variable "reasoning_effort" {
  type        = string
  description = "Reasoning effort passed to every session."
  default     = "medium"
}
