model_provider "primary" {
  type             = var.model_provider.type
  endpoint         = var.model_provider.endpoint
  wire_api         = var.model_provider.wire_api
  transport        = var.model_provider.transport
  headers          = var.model_provider.headers
  api_key          = var.model_provider.api_key
  api_key_ref      = var.model_provider.api_key_ref
  bearer_token     = var.model_provider.bearer_token
  bearer_token_ref = var.model_provider.bearer_token_ref

  retry {
    lifecycle_retries    = var.model_provider.retry.lifecycle_retries
    model_call_retries   = var.model_provider.retry.model_call_retries
    interval_seconds     = var.model_provider.retry.interval_seconds
    max_interval_seconds = var.model_provider.retry.max_interval_seconds
    error_message_regex  = var.model_provider.retry.error_message_regex
  }
}

model_provider "qc" {
  type             = var.qc_model_provider.type
  endpoint         = var.qc_model_provider.endpoint
  wire_api         = var.qc_model_provider.wire_api
  transport        = var.qc_model_provider.transport
  headers          = var.qc_model_provider.headers
  api_key          = var.qc_model_provider.api_key
  api_key_ref      = var.qc_model_provider.api_key_ref
  bearer_token     = var.qc_model_provider.bearer_token
  bearer_token_ref = var.qc_model_provider.bearer_token_ref

  retry {
    lifecycle_retries    = var.qc_model_provider.retry.lifecycle_retries
    model_call_retries   = var.qc_model_provider.retry.model_call_retries
    interval_seconds     = var.qc_model_provider.retry.interval_seconds
    max_interval_seconds = var.qc_model_provider.retry.max_interval_seconds
    error_message_regex  = var.qc_model_provider.retry.error_message_regex
  }
}
