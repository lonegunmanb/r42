variable "topic" {
  type        = string
  description = "The focal technology or industrial system whose supply-chain chokepoints will be researched."

  validation {
    condition     = length(trimspace(var.topic)) > 0
    error_message = "topic must not be empty."
  }
}

variable "as_of_date" {
  type        = string
  description = "Fixed YYYY-MM-DD evidence cutoff for the complete run. Sources published after this date are rejected."

  validation {
    condition     = can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}$", var.as_of_date))
    error_message = "as_of_date must use YYYY-MM-DD."
  }
}

variable "market" {
  type        = string
  description = "Public market universe used when discovering candidate companies."
  default     = "global"

  validation {
    condition     = contains(["global", "a-share", "hong-kong", "us"], var.market)
    error_message = "market must be global, a-share, hong-kong, or us."
  }
}

variable "max_candidates_per_chokepoint" {
  type        = number
  description = "Maximum number of public companies screened for each assessed supply-chain node. The legacy name is retained for variable-file compatibility."
  default     = 3

  validation {
    condition = (
      var.max_candidates_per_chokepoint >= 1 &&
      var.max_candidates_per_chokepoint <= 5 &&
      floor(var.max_candidates_per_chokepoint) == var.max_candidates_per_chokepoint
    )
    error_message = "max_candidates_per_chokepoint must be an integer from 1 through 5."
  }
}

variable "max_qc_rounds" {
  type        = number
  description = "Maximum Final-QC evaluations allowed for each QC-enabled research workflow."
  default     = 10

  validation {
    condition = (
      var.max_qc_rounds >= 1 &&
      floor(var.max_qc_rounds) == var.max_qc_rounds
    )
    error_message = "max_qc_rounds must be a positive integer."
  }
}

variable "use_pplx" {
  type        = bool
  description = "Use the optional Perplexity search and fetch typed tools instead of the built-in web tools."
  default     = false
}

variable "pplx_tool_call_quota" {
  type        = number
  description = "Maximum successful Perplexity fetch calls allowed per Collection session when use_pplx is true."
  default     = 10

  validation {
    condition = (
      var.pplx_tool_call_quota >= 1 &&
      floor(var.pplx_tool_call_quota) == var.pplx_tool_call_quota
    )
    error_message = "pplx_tool_call_quota must be a positive integer."
  }
}

variable "model_provider" {
  description = "BYOK model provider and retry configuration shared by every workflow phase session."
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
  description = "Model used by all workflow phase sessions unless overridden."
  default     = "openai/gpt-5.5"
}

variable "high_impact_model" {
  type        = string
  description = "Optional stronger model for the primary baseline, scope, supply-chain map, and synthesis. Empty uses model."
  default     = ""
}

variable "qc_model" {
  type        = string
  description = "Optional independent model for QC sessions. Empty uses high_impact_model, then model."
  default     = ""
}

variable "reasoning_effort" {
  type        = string
  description = "Reasoning effort passed to all workflow phase sessions."
  default     = "medium"
}

variable "research_system_prompt" {
  type        = string
  description = "Shared evidence discipline for the five tracks and all dynamically materialized tasks."
  default = <<-PROMPT
    You are an evidence-first supply-chain researcher. Distinguish an industry
    function, process, equipment category, or material from the companies that
    participate in it. Prefer primary sources and exact quotations. Preserve
    contradictions and label missing evidence as unknown.

    Do not overuse search or source-reading tools. After every search and every
    source read, assess whether the evidence already collected is sufficient
    for the assigned task. Stop searching when it is sufficient; continue only
    when you can name a remaining gap that could change the conclusion.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.

    When a terminate tool is configured, its accepted call is the only valid
    completion. Repair every schema or validation issue returned by the tool.
  PROMPT
}
