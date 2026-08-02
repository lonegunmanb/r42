research "static" "deep_dive" {
  for_each = {
    for index, question in var.research_plan : format("%03d", index + 1) => question
  }

  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt    = var.system_prompt
  prompt = <<-PROMPT
    Overall topic:
    ${var.topic}

    Assigned subquestion ${each.key}:
    ${each.value}

    Research the subquestion thoroughly. Then call
    ${go_tool.submit_knowledge.id} with:

    - artifact_path exactly "${block_wd()}/knowledge.json";
    - subquestion exactly as assigned above;
    - knowledge: atomic, falsifiable claims with stable IDs such as
      "${each.key}-kb-001", confidence high/medium/low, and quote_ids;
    - quotes: stable IDs such as "${each.key}-quote-001", source title,
      absolute URL, a precise locator, and exact verbatim source text.

    Do not finish with prose or a JSON code block. The accepted typed-tool call
    is the only valid completion.
  PROMPT
  tool_ids         = [go_tool.submit_knowledge.id]
  terminate_tool_id = go_tool.submit_knowledge.id
  permission        = "approve_all"

  artifact "knowledge" {
    type      = "file"
    path      = "${block_wd()}/knowledge.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      knowledge_items = "Parse the candidate and knowledge.json. Check every knowledge item individually: its ID is unique, its claim answers the assigned subquestion, its confidence is justified, and its quote_ids support the complete claim."
      quote_records = "Check every quote record individually. Open its URL, locate the cited passage using locator, and verify exact_quote is verbatim source text rather than a paraphrase or model-generated wording."
      traceability = "Verify both directions of the evidence graph: every knowledge item cites at least one valid quote, every cited quote exists, every quote is used, and no source is cited only by title or URL without a checked quotation."
      artifact_consistency = "Read knowledge.json and verify it is semantically identical to the accepted typed-tool candidate, including the assigned subquestion, every knowledge item, and every quote."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
    permission       = "approve_all"
  }
}

research "static" "resolve_conflicts" {
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the conflict chair for a deep-research matrix. Read every upstream
    knowledge.json file. Detect claims that disagree in value, scope, date,
    definition, causality, or source interpretation. Resolve a conflict only
    when the quotes justify a preference; otherwise preserve it as unresolved.
    Never silently drop a minority finding.

    You must finish by calling ${go_tool.submit_conflict_resolution.id}.
  PROMPT
  prompt = <<-PROMPT
    Overall topic:
    ${var.topic}

    Read every validated knowledge artifact:
    ${join("\n", [for item in research.static.deep_dive : "- ${one(item.artifact).path}"])}

    Compare all knowledge items and quote records across files. Call
    ${go_tool.submit_conflict_resolution.id} with artifact_path exactly
    "${block_wd()}/resolution.json", reviewed_artifacts containing every path
    above, all detected conflicts and decisions, and synthesis_guidance for the
    final writer. An empty conflicts list is valid only after an explicit
    cross-file comparison.
  PROMPT
  tool_ids          = [go_tool.submit_conflict_resolution.id]
  terminate_tool_id = go_tool.submit_conflict_resolution.id
  permission        = "approve_all"

  artifact "resolution" {
    type      = "file"
    path      = "${block_wd()}/resolution.json"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      coverage = "Read every upstream knowledge.json path listed in the task and verify reviewed_artifacts includes each one exactly once."
      detection = "Compare every knowledge claim across subquestions and verify contradictions in values, dates, scope, definitions, causality, and source interpretation were detected; verify an empty conflict list only when the files are genuinely compatible."
      decisions = "Check every conflict decision against its knowledge IDs and supporting quote IDs. A resolved decision must prefer the stronger evidence; an unresolved decision must preserve the uncertainty for synthesis."
      artifact_consistency = "Read resolution.json and verify it is semantically identical to the accepted typed-tool candidate."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
    permission       = "approve_all"
  }
}

research "static" "synthesize" {
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the senior editor. Produce one evidence-dense Markdown report from
    the validated knowledge artifacts and conflict-resolution record. Every
    material factual statement must cite the source URL and quote ID that
    supports it. Clearly label unresolved conflicts and limitations.
  PROMPT
  prompt = <<-PROMPT
    Answer this overall topic:
    ${var.topic}

    Validated knowledge artifacts:
    ${join("\n", [for item in research.static.deep_dive : "- ${one(item.artifact).path}"])}

    Conflict-resolution artifact:
    ${one(research.static.resolve_conflicts.artifact).path}

    Read all files. Write the final report to ${block_wd()}/report.md. Include
    an executive summary, findings organized around the research plan,
    resolved and unresolved contradictions, limitations, and a source table
    mapping each cited quote ID to its URL. Do not merely concatenate the files.
  PROMPT
  permission = "approve_all"

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      plan_coverage = "Read every knowledge.json artifact and verify the report answers every subquestion in the supplied research plan."
      factual_fidelity = "Check every material statement against its referenced knowledge item and exact quote; reject unsupported extrapolation."
      conflict_handling = "Read resolution.json and verify all resolved and unresolved conflicts are represented exactly as decided, without hiding residual uncertainty."
      citations = "Verify each quote ID cited in the report maps to the correct source URL and that the source table contains no unused or invented references."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 3
    permission       = "approve_all"
  }
}

output "knowledge_paths" {
  description = "Validated knowledge artifacts produced by the parallel subquestion stage."
  value       = [for item in research.static.deep_dive : one(item.artifact).path]
}

output "conflict_resolution_path" {
  description = "Validated cross-subquestion conflict decisions."
  value       = one(research.static.resolve_conflicts.artifact).path
}

output "report_path" {
  description = "Final deep-research Markdown report."
  value       = one(research.static.synthesize.artifact).path
}
