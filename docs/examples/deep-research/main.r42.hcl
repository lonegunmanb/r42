locals {
  supplied_research_tasks = [
    for index, question in coalesce(var.research_plan, []) : {
      id           = format("supplied-%03d", index + 1)
      subquestion  = question
      instructions = "Investigate this subquestion independently. Establish its evidence scope, exclusions, and expected reasoning before submitting atomic claims and exact quotes."
    }
  ]

  deep_dive_system_prompt = <<-PROMPT
    ${var.system_prompt}

    Search discipline is mandatory. Use search and source-reading tools
    selectively. After every search and after reading each source, pause and
    assess whether the evidence already collected is sufficient to answer this
    stage's assigned question and support its conclusion. If it is sufficient,
    stop searching and reading, then organize and submit the result. Continue
    searching only when you can name a specific unresolved evidence gap that
    could change the conclusion. Do not search merely to accumulate more
    sources.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT

  deep_dive_tool_call_quota = {
    web_fetch = var.web_fetch_tool_call_quota
  }
}

research "static" "plan" {
  for_each = length(coalesce(var.research_plan, [])) == 0 ? { default = true } : {}

  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You design evidence-oriented deep-research programs. Decompose one topic
    into a small set of orthogonal, falsifiable subquestions with minimal
    overlap and enough combined coverage to support a final answer.

    Assign every task to exactly one execution group:

    - parallel_tasks run concurrently and must be mutually independent;
    - independent_serial_tasks start at the same time as parallel_tasks but run
      one at a time. They cannot read parallel results or earlier tasks in their
      own group, so each must be independently executable from this plan;
    - final_serial_tasks start only after both earlier groups finish. They may
      ask researchers to compare, audit, or extend those upstream artifacts,
      but final tasks cannot depend on results from earlier tasks in their own
      group because all task prompts are materialized together. When a task
      needs an upstream artifact, say so explicitly in its instructions and
      tell the researcher to inspect the supplied absolute paths with built-in
      file-reading tools such as grep before drawing conclusions; never assume
      an artifact's contents from its filename alone.

    Any individual group may be empty. Across all groups, use globally unique
    lowercase task IDs that are safe as directory names. For every task, write
    a precise subquestion and instructions that define its evidence scope,
    exclusions, and expected reasoning. Do not perform the research yourself.
    Every task must tell the researcher to save complete source material as
    Markdown under its block_wd()/snapshots/ directory and associate every
    quote with the exact saved snapshot path. Tell it to call
    ${go_tool.save_snapshot.id} after every source read. A URL alone is not
    evidence.
    You must finish by calling ${go_tool.submit_research_plan.id}.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt = <<-PROMPT
    Build a research plan for this topic:
    ${var.topic}

    Decide how many tasks belong in each of the three execution groups. Explain
    each task through its subquestion and instructions, then call
    ${go_tool.submit_research_plan.id}. Do not finish with prose or a JSON code
    block; the accepted typed-tool call is the only valid completion.
  PROMPT
  tool_ids          = [go_tool.submit_research_plan.id]
  terminate_tool_id = go_tool.submit_research_plan.id
  permission        = "approve_all"

  qc {
    criteria = {
      coverage = "Verify the combined task set covers the topic's material perspectives, mechanisms, counterarguments, and evidence needs without assuming the answer."
      orthogonality = "Compare every pair of tasks and reject avoidable overlap, duplicate evidence scopes, vague omnibus questions, and decompositions that would produce the same claims twice."
      scheduling = "Verify parallel tasks are mutually independent, independent_serial tasks require no output from either earlier group, and final_serial tasks are meaningful only after both earlier groups have completed."
      executability = "Verify every task has a globally unique safe ID, a falsifiable subquestion, concrete instructions, explicit evidence expectations, and enough context to execute without asking the planner for clarification."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 2
    permission       = "approve_all"
  }
}

research "dynamic" "parallel_deep_dive" {
  serial = false
  tasks = [
    for task in (length(coalesce(var.research_plan, [])) > 0 ? local.supplied_research_tasks : jsondecode(research.static.plan["default"].result).parallel_tasks) : {
      id               = task.id
      subquestion      = task.subquestion
      instructions     = task.instructions
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = local.deep_dive_system_prompt
      prompt = <<-PROMPT
        Overall topic:
        ${var.topic}

        Task ID: ${task.id}
        Assigned subquestion:
        ${task.subquestion}

        Task-specific instructions:
        ${task.instructions}

        If this task reads an artifact produced by an earlier task, use the
        available file/search tools (for example grep) to locate and inspect
        the exact upstream file before relying on it.
        Save the complete material returned by every source read as Markdown
        under "${block_wd()}/snapshots/" before citing it. Call
        ${go_tool.save_snapshot.id} with that path and the complete Markdown
        content. Associate every quote with its exact snapshot_path, URL,
        locator, and verbatim text.
        Research this subquestion independently. Then call
        ${go_tool.submit_knowledge.id} with artifact_path exactly
        "${block_wd()}/${task.id}/knowledge.json", subquestion exactly as
        assigned above, atomic knowledge claims with IDs prefixed
        "${task.id}-kb-", and exact quote records with IDs prefixed
        "${task.id}-quote-". Do not finish with prose or a JSON code block.
      PROMPT
      tool_ids          = [go_tool.save_snapshot.id, go_tool.submit_knowledge.id]
      tool_call_quota   = local.deep_dive_tool_call_quota
      terminate_tool_id = go_tool.submit_knowledge.id
      permission        = "approve_all"
      artifacts = [{
        name      = "knowledge"
        type      = "file"
        path      = "${block_wd()}/${task.id}/knowledge.json"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          knowledge_items = "Parse the candidate and knowledge.json. Check every knowledge item individually: its ID is unique, its claim answers the assigned subquestion, its confidence is justified, and its quote_ids support the complete claim."
          quote_records = "Check every quote record individually. Read its snapshot_path, verify the Markdown snapshot exists under snapshots, locate the passage using locator, and verify exact_quote is verbatim snapshot text rather than a paraphrase or model-generated wording."
          traceability = "Verify both directions of the evidence graph: every knowledge item cites at least one valid quote, every cited quote and snapshot exists, every quote is used, and no source is cited only by title or URL without a checked snapshot."
          artifact_consistency = "Read the declared knowledge.json and verify it is semantically identical to the accepted typed-tool candidate."
        }
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = 3
        permission       = "approve_all"
      }
    }
  ]
}

research "dynamic" "independent_serial_deep_dive" {
  serial = true
  tasks = [
    for task in (length(coalesce(var.research_plan, [])) > 0 ? [] : jsondecode(research.static.plan["default"].result).independent_serial_tasks) : {
      id               = task.id
      subquestion      = task.subquestion
      instructions     = task.instructions
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = local.deep_dive_system_prompt
      prompt = <<-PROMPT
        Overall topic:
        ${var.topic}

        Task ID: ${task.id}
        Assigned subquestion:
        ${task.subquestion}

        Task-specific instructions:
        ${task.instructions}

        If this task reads an artifact produced by an earlier task, use the
        available file/search tools (for example grep) to locate and inspect
        the exact upstream file before relying on it. This group runs one task
        at a time but does not wait for the parallel
        group. Research independently without assuming access to another task's
        result. Save the complete material returned by every source read as
        Markdown under "${block_wd()}/snapshots/" before citing it. Call
        ${go_tool.save_snapshot.id} with that path and the complete Markdown
        content. Associate every quote with its exact snapshot_path, URL,
        locator, and verbatim text. Then call ${go_tool.submit_knowledge.id}
        with artifact_path
        exactly "${block_wd()}/${task.id}/knowledge.json", subquestion exactly
        as assigned above, atomic knowledge claims with IDs prefixed
        "${task.id}-kb-", and exact quote records with IDs prefixed
        "${task.id}-quote-". Do not finish with prose or a JSON code block.
      PROMPT
      tool_ids          = [go_tool.save_snapshot.id, go_tool.submit_knowledge.id]
      tool_call_quota   = local.deep_dive_tool_call_quota
      terminate_tool_id = go_tool.submit_knowledge.id
      permission        = "approve_all"
      artifacts = [{
        name      = "knowledge"
        type      = "file"
        path      = "${block_wd()}/${task.id}/knowledge.json"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          knowledge_items = "Parse the candidate and knowledge.json. Check every knowledge item individually: its ID is unique, its claim answers the assigned subquestion, its confidence is justified, and its quote_ids support the complete claim."
          quote_records = "Check every quote record individually. Read its snapshot_path, verify the Markdown snapshot exists under snapshots, locate the passage using locator, and verify exact_quote is verbatim snapshot text rather than a paraphrase or model-generated wording."
          traceability = "Verify both directions of the evidence graph: every knowledge item cites at least one valid quote, every cited quote and snapshot exists, every quote is used, and no source is cited only by title or URL without a checked snapshot."
          artifact_consistency = "Read the declared knowledge.json and verify it is semantically identical to the accepted typed-tool candidate."
        }
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = 3
        permission       = "approve_all"
      }
    }
  ]
}

research "dynamic" "final_serial_deep_dive" {
  serial = true
  depends_on = [
    research.dynamic.parallel_deep_dive,
    research.dynamic.independent_serial_deep_dive,
  ]
  tasks = [
    for task in (length(coalesce(var.research_plan, [])) > 0 ? [] : jsondecode(research.static.plan["default"].result).final_serial_tasks) : {
      id               = task.id
      subquestion      = task.subquestion
      instructions     = task.instructions
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = local.deep_dive_system_prompt
      prompt = <<-PROMPT
        Overall topic:
        ${var.topic}

        Task ID: ${task.id}
        Assigned subquestion:
        ${task.subquestion}

        Task-specific instructions:
        ${task.instructions}

        Read all validated upstream knowledge artifacts before researching.
        Use available file/search tools (for example grep) to locate and
        inspect each exact path; do not assume that a path string alone means
        the artifact was read:
        ${join("\n", concat(
          [for item in research.dynamic.parallel_deep_dive.tasks : "- ${one(item.artifacts).path}"],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
        ))}

        Save any new source material as Markdown under
        "${block_wd()}/snapshots/" before citing it. Call
        ${go_tool.save_snapshot.id} with that path and the complete Markdown
        content. Associate every quote with its exact snapshot_path, URL,
        locator, and verbatim text. Then call ${go_tool.submit_knowledge.id}
        with artifact_path exactly
        "${block_wd()}/${task.id}/knowledge.json", subquestion exactly as
        assigned above, atomic knowledge claims with IDs prefixed
        "${task.id}-kb-", and exact quote records with IDs prefixed
        "${task.id}-quote-". Do not finish with prose or a JSON code block.
      PROMPT
      tool_ids          = [go_tool.save_snapshot.id, go_tool.submit_knowledge.id]
      tool_call_quota   = local.deep_dive_tool_call_quota
      terminate_tool_id = go_tool.submit_knowledge.id
      permission        = "approve_all"
      artifacts = [{
        name      = "knowledge"
        type      = "file"
        path      = "${block_wd()}/${task.id}/knowledge.json"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          upstream_use = "Read every upstream knowledge artifact listed in the prompt and verify the candidate accurately uses the relevant claims and quotes rather than merely mentioning the files."
          knowledge_items = "Check every new knowledge item individually: its ID is unique, its claim answers the assigned subquestion, its confidence is justified, and its quote_ids support the complete claim."
          quote_records = "Check every new quote record individually. Read its snapshot_path, verify the Markdown snapshot exists under snapshots, locate the cited passage using locator, and verify exact_quote is verbatim snapshot text."
          artifact_consistency = "Read the declared knowledge.json and verify it is semantically identical to the accepted typed-tool candidate."
        }
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = 3
        permission       = "approve_all"
      }
    }
  ]
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

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt = <<-PROMPT
    Overall topic:
    ${var.topic}

    Read every validated knowledge artifact:
    ${join("\n", concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.final_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
    ))}

    Use available file/search tools (for example grep) to locate and inspect
    every listed artifact before comparing its knowledge items and quote
    records. Call
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
      snapshots = "For every source-backed claim used in a conflict decision, read the referenced snapshot_path and verify the snapshot exists under the originating task's snapshots directory."
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

    The validated knowledge artifacts and conflict-resolution record are the
    report's only permitted factual basis. Derive every finding, comparison,
    explanation, and conclusion only from claims and quotes present in those
    artifacts. Do not add information from model training, memory, general
    background knowledge, assumptions, or personal opinions, even when the
    artifacts are incomplete or a source could not be fetched. If the evidence
    does not establish an answer, say that the available evidence is
    insufficient and leave the point unresolved; do not fill the gap with an
    inference presented as fact.

    Do not output a "信源限制说明" or any equivalent disclaimer that says the
    report is based on training data, gives a knowledge cutoff, attributes
    conclusions to model synthesis, or explains paywalls or exhausted web
    quotas. Evidence limitations may be mentioned only when they are stated
    in the validated artifacts and are relevant to the supported conclusion.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.
  PROMPT
  prompt = <<-PROMPT
    Answer this overall topic:
    ${var.topic}

    Validated knowledge artifacts:
    ${join("\n", concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.final_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
    ))}

    Conflict-resolution artifact:
    ${one(research.static.resolve_conflicts.artifact).path}

    Use available file/search tools (for example grep) to locate and inspect
    every listed artifact and the resolution file. Read all files, then write
    the final report to ${block_wd()}/report.md. Include
    an executive summary, findings organized around the planner-produced task groups,
    resolved and unresolved contradictions, limitations, and a source table
    mapping each cited quote ID to its URL. Before writing, remove every
    statement that cannot be traced to a validated knowledge claim, its quote,
    or an explicit conflict-resolution decision. Do not merely concatenate the
    files, and do not use pretrained knowledge or opinion to make the report
    sound complete.
  PROMPT
  disallowed_tools = ["ask_user", "web_search", "web_fetch"]
  permission       = "approve_all"

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      mechanical_audit = "Call ${external_tool.audit_synthesis.id} exactly once in each QC round before judging the current report revision. Pass report_path as the declared report artifact, knowledge_paths as the complete Validated knowledge artifacts list from the task, and resolution_path as the Conflict-resolution artifact. Treat its quote-ID, source-URL, unused-reference, snapshot-existence, and text-equivalence checks as authoritative. Preserve every reported mechanical issue in the QC verdict, but do not repeat those checks with grep or view."
      plan_coverage = "Read the report and the knowledge.json artifacts, but not their snapshots, and verify the report answers every planner-produced subquestion."
      factual_fidelity = "Use each knowledge item's claim, confidence, quote_ids, and exact_quote fields to judge whether every material report statement and conclusion is logically supported without extrapolation. Reject any statement that appears to come from model training, memory, general background knowledge, assumption, or opinion rather than the validated artifacts. This is a semantic judgment; do not repeat the typed tool's text matching."
      conflict_handling = "Read resolution.json and verify all resolved and unresolved conflicts are represented exactly as decided, without hiding residual uncertainty."
      citation_semantics = "For citations that mechanically pass, decide whether the cited exact quote actually supports the surrounding report claim. Do not reopen snapshots unless the mechanical audit itself reports an unreadable or unmatched quote."
      provenance_guard = "Reject a report that includes a 信源限制说明 or equivalent training-data/knowledge-cutoff/paywall/quota disclaimer, or that uses such limitations as a reason to introduce uncited model opinion. Require unsupported conclusions to be removed or explicitly marked as insufficient evidence."
    }
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = 10
    disallowed_tools = ["bash", "powershell", "edit", "task", "ask_user", "web_search", "web_fetch"]
    permission       = "approve_all"
    tool_ids          = [external_tool.audit_synthesis.id]
    tool_call_quota = {
      for tool_id in [external_tool.audit_synthesis.id] : tool_id => 10
    }
  }
}

output "knowledge_paths" {
  description = "Validated knowledge artifacts produced by all planner-selected task groups."
  value = concat(
    [for item in research.dynamic.parallel_deep_dive.tasks : one(item.artifacts).path],
    [for item in research.dynamic.independent_serial_deep_dive.tasks : one(item.artifacts).path],
    [for item in research.dynamic.final_serial_deep_dive.tasks : one(item.artifacts).path],
  )
}

output "conflict_resolution_path" {
  description = "Validated cross-subquestion conflict decisions."
  value       = one(research.static.resolve_conflicts.artifact).path
}

output "report_path" {
  description = "Final deep-research Markdown report."
  value       = one(research.static.synthesize.artifact).path
}
