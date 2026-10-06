variable "s3" {
  description = "Optional upload of the current run directory after all workflow blocks succeed. Null disables S3."
  type = object({
    region            = string
    bucket            = string
    prefix            = string
    endpoint          = optional(string, "")
    force_path_style  = optional(bool, false)
    access_key_ref    = optional(string)
    secret_key_ref    = optional(string)
    session_token_ref = optional(string)
    exclude           = optional(list(string), [])
  })
  default  = null
  nullable = true
}

s3_provider "runs" {
  for_each = var.s3 == null ? {} : { current = var.s3 }

  region            = each.value.region
  endpoint          = each.value.endpoint
  force_path_style  = each.value.force_path_style
  access_key_ref    = each.value.access_key_ref
  secret_key_ref    = each.value.secret_key_ref
  session_token_ref = each.value.session_token_ref
}

s3_folder "runs" {
  for_each = var.s3 == null ? {} : { current = var.s3 }

  provider = s3_provider.runs[each.key]
  bucket   = each.value.bucket
  prefix   = each.value.prefix
  source   = run_wd()
  exclude  = each.value.exclude

  depends_on = [
    module.pplx_tools,
    research.static.external_tool_artifact,
  ]
}

output "report_s3_prefix" {
  description = "Actual uploaded S3 prefix for this run, or null when S3 is disabled."
  value       = var.s3 == null ? null : s3_folder.runs["current"].result.prefix
}
