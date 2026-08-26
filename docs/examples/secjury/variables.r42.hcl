variable "target" {
  type        = string
  description = "Company name, ticker, and exchange context for the DCF."

  validation {
    condition     = length(trimspace(var.target)) > 0
    error_message = "target must not be empty."
  }
}

variable "valuation_date" {
  type        = string
  description = "DCF valuation date in YYYY-MM-DD form. Sources must not post-date it."

  validation {
    condition     = can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}$", var.valuation_date))
    error_message = "valuation_date must use YYYY-MM-DD."
  }
}

variable "model" {
  type        = string
  description = "Model used by the DCF builder, jurors, and synthesizer."
  default     = "gpt-5.6-sol"
}

variable "reasoning_effort" {
  type        = string
  description = "Reasoning effort used by all secjury sessions."
  default     = "medium"
}

variable "use_pplx" {
  type        = bool
  description = "Use optional Perplexity finance search, general search, and fetch tools instead of the built-in web tools."
  default     = false
}

variable "pplx_tool_call_quota" {
  type        = number
  description = "Maximum successful Perplexity fetch calls in the DCF Collection session when use_pplx is true. Set null for no limit."
  default     = null
  nullable    = true

  validation {
    condition = var.pplx_tool_call_quota == null ? true : (
      var.pplx_tool_call_quota >= 1 &&
      floor(var.pplx_tool_call_quota) == var.pplx_tool_call_quota
    )
    error_message = "pplx_tool_call_quota must be null or a positive integer."
  }
}
