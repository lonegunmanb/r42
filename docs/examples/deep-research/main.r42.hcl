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

  deep_dive_tool_call_quota = var.web_fetch_tool_call_quota == null ? {} : {
    web_fetch = var.web_fetch_tool_call_quota
  }

  offline_disallowed_tools = ["web_search", "web_fetch"]
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
      needs upstream knowledge, say so explicitly in its instructions; r42 will
      inject validated typed-tool JSON results into the task prompt.

    Any individual group may be empty. Across all groups, use globally unique
    lowercase task IDs that are safe as directory names. For every task, write
    a precise subquestion and instructions that define its evidence scope,
    exclusions, and expected reasoning. Do not perform the research yourself.
    Every task must tell the researcher to save complete source material as
    Markdown under its block_wd()/snapshots/ directory and associate every
    quote with the snapshot_id returned by r42_save_snapshot. Tell it to call
    r42_save_snapshot after every source read, passing the source URL in source.
    Tell it to use the returned snapshot_id directly and not call
    r42_register_snapshot afterward. A source identifier alone is not evidence.
    You must finish by calling ${go_tool.submit_research_plan.id}.

    During Collection, do not acquire external evidence; the topic is the
    complete input for planning, so submit an empty collection checkpoint.
    During closed Research, create the plan and call the typed submission tool.

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
  tool_use "submit_plan" {
    tool_id   = go_tool.submit_research_plan.id
    terminate = true
    input = {
      topic = var.topic
    }
    input_from_agent = {
      parallel_tasks = {
        desc = "The independently executable tasks for the parallel group."
        sources = []
      }
      independent_serial_tasks = {
        desc = "The independently executable tasks for the serial group."
        sources = []
      }
      final_serial_tasks = {
        desc = "The tasks that wait for both earlier groups."
        sources = []
      }
    }
  }
  disallowed_tools  = local.offline_disallowed_tools
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
    for index, task in (length(coalesce(var.research_plan, [])) > 0 ? local.supplied_research_tasks : jsondecode(research.static.plan["default"].result).parallel_tasks) : {
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

        Any permitted upstream knowledge is included directly in this prompt.

        During Collection, research this subquestion independently. Save the
        complete material returned by every retained source as Markdown under
        "${one(self.snapshot).path}" by calling r42_save_snapshot with
        source set to the source URL. Use the returned snapshot_id directly;
        do not call r42_register_snapshot for the returned path. Submit a
        collection checkpoint once the evidence is sufficient.

        During closed Research, do not search or fetch. Use the snapshot IDs
        supplied by r42 with r42_read_snapshot to inspect registered evidence. Associate
        every quote with its registered snapshot_id, URL, locator, and verbatim
        text. Then call
        ${go_tool.submit_knowledge.id} with artifact_path exactly
        "${block_wd()}/${index}/${task.id}/knowledge.json", subquestion exactly as
        assigned above, atomic knowledge claims with IDs prefixed
        "${task.id}-kb-", and exact quote records with IDs prefixed
        "${task.id}-quote-". Do not finish with prose or a JSON code block.
      PROMPT
      snapshots = [{
        name        = "sources"
        type        = "directory"
        path        = "${block_wd()}/${index}/snapshots"
        description = "Source material collected for this parallel subquestion."
      }]
      tool_uses = [{
        name      = "submit_knowledge"
        tool_id   = go_tool.submit_knowledge.id
        terminate = true
        input = {
          artifact_path = "${block_wd()}/${index}/${task.id}/knowledge.json"
          subquestion   = task.subquestion
        }
        input_from_agent = {
          knowledge = {
            desc = "Atomic knowledge claims for the assigned subquestion, supported by the collected quotes."
            sources = self.snapshot
          }
          quotes = {
            desc = "Exact quote records read from the authorized snapshots."
            sources = self.snapshot
          }
        }
      }]
      tool_call_quota   = local.deep_dive_tool_call_quota
      permission        = "approve_all"
      artifacts = [{
        name      = "knowledge"
        type      = "file"
        path      = "${block_wd()}/${index}/${task.id}/knowledge.json"
		description = "Validated claims and exact quotes for this parallel subquestion"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          knowledge_items = "Judge whether every knowledge claim answers the assigned subquestion, expresses uncertainty carefully, and is fully supported by its cited quote records."
          quote_records = "Judge whether each accepted quote semantically entails the claim that cites it without changing party, period, scope, causality, or qualifiers. Treat typed-tool snapshot existence and text matching as authoritative."
          traceability = "Judge whether the cited evidence is sufficient and decision-relevant, and whether material contrary evidence or uncertainty is omitted. Treat typed-tool IDs and graph references as authoritative."
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
    for index, task in (length(coalesce(var.research_plan, [])) > 0 ? [] : jsondecode(research.static.plan["default"].result).independent_serial_tasks) : {
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

        Any permitted upstream knowledge is included directly in this prompt.
        This group runs one task
        at a time but does not wait for the parallel
        group. Research independently without assuming access to another task's
        result.

        During Collection, save the complete material returned by every
        retained source as Markdown under "${one(self.snapshot).path}" by calling
        r42_save_snapshot with source set to the source URL. Use the returned
        snapshot_id directly; do not call r42_register_snapshot for the returned
        path. Submit a collection checkpoint once the evidence is sufficient.

        During closed Research, do not search or fetch. Use the snapshot IDs
        supplied by r42 with r42_read_snapshot to inspect registered evidence. Associate
        every quote with its registered snapshot_id, URL, locator, and verbatim
        text. Then call ${go_tool.submit_knowledge.id}
        with artifact_path
        exactly "${block_wd()}/${index}/${task.id}/knowledge.json", subquestion exactly
        as assigned above, atomic knowledge claims with IDs prefixed
        "${task.id}-kb-", and exact quote records with IDs prefixed
        "${task.id}-quote-". Do not finish with prose or a JSON code block.
      PROMPT
      snapshots = [{
        name        = "sources"
        type        = "directory"
        path        = "${block_wd()}/${index}/snapshots"
        description = "Source material collected for this serial subquestion."
      }]
      tool_uses = [{
        name      = "submit_knowledge"
        tool_id   = go_tool.submit_knowledge.id
        terminate = true
        input = {
          artifact_path = "${block_wd()}/${index}/${task.id}/knowledge.json"
          subquestion   = task.subquestion
        }
        input_from_agent = {
          knowledge = {
            desc = "Atomic knowledge claims for the assigned subquestion, supported by the collected quotes."
            sources = self.snapshot
          }
          quotes = {
            desc = "Exact quote records read from the authorized snapshots."
            sources = self.snapshot
          }
        }
      }]
      tool_call_quota   = local.deep_dive_tool_call_quota
      permission        = "approve_all"
      artifacts = [{
        name      = "knowledge"
        type      = "file"
        path      = "${block_wd()}/${index}/${task.id}/knowledge.json"
		description = "Validated claims and exact quotes for this independent serial subquestion"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          knowledge_items = "Judge whether every knowledge claim answers the assigned subquestion, expresses uncertainty carefully, and is fully supported by its cited quote records."
          quote_records = "Judge whether each accepted quote semantically entails the claim that cites it without changing party, period, scope, causality, or qualifiers. Treat typed-tool snapshot existence and text matching as authoritative."
          traceability = "Judge whether the cited evidence is sufficient and decision-relevant, and whether material contrary evidence or uncertainty is omitted. Treat typed-tool IDs and graph references as authoritative."
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
    for index, task in (length(coalesce(var.research_plan, [])) > 0 ? [] : jsondecode(research.static.plan["default"].result).final_serial_tasks) : {
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

        Validated upstream knowledge JSON is included below. Use its claims and
        quotes as context before researching:
        ${join("\n", concat(
          [for item in research.dynamic.parallel_deep_dive.tasks : item.result],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : item.result],
        ))}

        The corresponding artifact paths, used only when submitting the final
        reviewed_artifacts field, are:
        ${join("\n", concat(
          [for item in research.dynamic.parallel_deep_dive.tasks : "- ${one(item.artifacts).path}"],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
        ))}

        During Collection, collect only evidence needed beyond the validated
        upstream JSON. Save each retained source as Markdown under
        "${one(self.snapshot).path}" with r42_save_snapshot, passing the
        source URL in source. Use the returned snapshot_id directly; do not call
        r42_register_snapshot for the returned path. Submit a collection checkpoint.
        If the upstream JSON is sufficient, submit an empty
        collection checkpoint instead of searching.

        During closed Research, do not search or fetch. Use the snapshot IDs
        supplied by r42 with r42_read_snapshot for any newly registered evidence and use the
        validated JSON above directly. Associate every new quote with its exact
        snapshot_id, URL, locator, and verbatim text. Then call
        ${go_tool.submit_knowledge.id}
        with artifact_path exactly
        "${block_wd()}/${index}/${task.id}/knowledge.json", subquestion exactly as
        assigned above, atomic knowledge claims with IDs prefixed
        "${task.id}-kb-", and exact quote records with IDs prefixed
        "${task.id}-quote-". Do not finish with prose or a JSON code block.
      PROMPT
      snapshots = [{
        name        = "sources"
        type        = "directory"
        path        = "${block_wd()}/${index}/snapshots"
        description = "Source material collected for this final subquestion."
      }]
      tool_uses = [{
        name      = "submit_knowledge"
        tool_id   = go_tool.submit_knowledge.id
        terminate = true
        input = {
          artifact_path = "${block_wd()}/${index}/${task.id}/knowledge.json"
          subquestion   = task.subquestion
        }
        input_from_agent = {
          knowledge = {
            desc = "New atomic knowledge claims supported by the authorized snapshots and validated upstream JSON."
            sources = flatten([
              [for item in research.dynamic.parallel_deep_dive.tasks : item.artifacts],
              [for item in research.dynamic.independent_serial_deep_dive.tasks : item.artifacts],
              [for item in research.dynamic.parallel_deep_dive.tasks : item.snapshots],
              [for item in research.dynamic.independent_serial_deep_dive.tasks : item.snapshots],
              self.snapshot,
            ])
          }
          quotes = {
            desc = "Exact quote records for newly collected evidence."
            sources = flatten([
              [for item in research.dynamic.parallel_deep_dive.tasks : item.snapshots],
              [for item in research.dynamic.independent_serial_deep_dive.tasks : item.snapshots],
              self.snapshot,
            ])
          }
        }
      }]
      tool_call_quota   = local.deep_dive_tool_call_quota
      permission        = "approve_all"
      artifacts = [{
        name      = "knowledge"
        type      = "file"
        path      = "${block_wd()}/${index}/${task.id}/knowledge.json"
		description = "Validated claims and exact quotes for this dependent serial subquestion"
        required  = true
        non_empty = true
      }]
      retry = null
      qc = {
        criteria = {
          upstream_use = "Judge whether the candidate accurately uses the relevant claims and quotes from the validated upstream JSON included in the prompt."
          knowledge_items = "Judge whether every new claim answers the assigned subquestion, expresses uncertainty carefully, and is fully supported by its cited quotes."
          quote_records = "Judge whether each accepted quote semantically entails the claim that cites it without changing party, period, scope, causality, or qualifiers. Treat typed-tool snapshot existence and text matching as authoritative."
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
    You are the conflict chair for a deep-research matrix. Use the validated
    upstream knowledge JSON included in the prompt. Detect claims that disagree
    in value, scope, date,
    definition, causality, or source interpretation. Resolve a conflict only
    when the quotes justify a preference; otherwise preserve it as unresolved.
    Never silently drop a minority finding.

    You must finish by calling ${go_tool.submit_conflict_resolution.id}.

    During Collection, do not acquire external evidence; the validated upstream
    JSON is the complete evidence basis, so submit an empty collection checkpoint.
    During closed Research, compare that JSON and submit the resolution.

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

    Validated knowledge JSON:
    ${join("\n", concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : item.result],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : item.result],
      [for item in research.dynamic.final_serial_deep_dive.tasks : item.result],
    ))}

    The corresponding artifact paths, used only for the typed tool's
    reviewed_artifacts field, are:
    ${join("\n", concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.final_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
    ))}

    Compare every included knowledge item and quote record. Call
    ${go_tool.submit_conflict_resolution.id} with artifact_path exactly
    "${block_wd()}/resolution.json", reviewed_artifacts containing every path
    above, all detected conflicts and decisions, and synthesis_guidance for the
    final writer. An empty conflicts list is valid only after an explicit
    cross-file comparison.
  PROMPT
  tool_use "submit_conflict_resolution" {
    tool_id   = go_tool.submit_conflict_resolution.id
    terminate = true
    input = {
      artifact_path = "${block_wd()}/resolution.json"
      topic         = var.topic
      reviewed_artifacts = concat(
        [for item in research.dynamic.parallel_deep_dive.tasks : one(item.artifacts).path],
        [for item in research.dynamic.independent_serial_deep_dive.tasks : one(item.artifacts).path],
        [for item in research.dynamic.final_serial_deep_dive.tasks : one(item.artifacts).path],
      )
    }
    input_from_agent = {
      conflicts = {
        desc = "Every detected cross-subquestion conflict and its evidence-backed decision."
        sources = flatten([
          [for item in research.dynamic.parallel_deep_dive.tasks : item.artifacts],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : item.artifacts],
          [for item in research.dynamic.final_serial_deep_dive.tasks : item.artifacts],
        ])
      }
      synthesis_guidance = {
        desc = "Guidance for the final writer, including unresolved uncertainty."
        sources = flatten([
          [for item in research.dynamic.parallel_deep_dive.tasks : item.artifacts],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : item.artifacts],
          [for item in research.dynamic.final_serial_deep_dive.tasks : item.artifacts],
        ])
      }
    }
  }
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "resolution" {
    type      = "file"
    path      = "${block_wd()}/resolution.json"
	description = "Structured decisions resolving or preserving conflicts across knowledge artifacts"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      coverage = "Judge whether the resolution considers every subquestion represented in the validated upstream JSON. Treat typed-tool reviewed_artifacts validation as authoritative."
      detection = "Compare every knowledge claim across subquestions and verify contradictions in values, dates, scope, definitions, causality, and source interpretation were detected; verify an empty conflict list only when the files are genuinely compatible."
      decisions = "Check every conflict decision against its knowledge IDs and supporting quote IDs. A resolved decision must prefer the stronger evidence; an unresolved decision must preserve the uncertainty for synthesis."
      evidence_semantics = "Judge whether the accepted quotes actually support each conflict decision without changing scope or qualifiers. Treat typed-tool paths, IDs, and text matching as authoritative."
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

    During Collection, do not acquire external evidence; the validated upstream
    JSON is the complete evidence basis, so submit an empty collection checkpoint.
    During closed Research, synthesize only that JSON and write the report.

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

    Validated knowledge JSON:
    ${join("\n", concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : item.result],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : item.result],
      [for item in research.dynamic.final_serial_deep_dive.tasks : item.result],
    ))}

    Validated knowledge artifact paths for Final QC's typed audit:
    ${join("\n", concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
      [for item in research.dynamic.final_serial_deep_dive.tasks : "- ${one(item.artifacts).path}"],
    ))}

    Conflict-resolution JSON:
    ${research.static.resolve_conflicts.result}

    Conflict-resolution artifact path for Final QC's typed audit:
    ${one(research.static.resolve_conflicts.artifact).path}

    Use only the included validated JSON and registered snapshots, then write
    the final report to ${block_wd()}/report.md. Include
    an executive summary, findings organized around the planner-produced task groups,
    resolved and unresolved contradictions, limitations, and a source table
    mapping each cited quote ID to its URL. Before writing, remove every
    statement that cannot be traced to a validated knowledge claim, its quote,
    or an explicit conflict-resolution decision. Do not merely concatenate the
    files, and do not use pretrained knowledge or opinion to make the report
    sound complete.
  PROMPT
  disallowed_tools = concat(["ask_user"], local.offline_disallowed_tools)
  permission       = "approve_all"

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
	description = "Final synthesis report grounded in validated knowledge and conflict decisions"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      mechanical_audit = "Call ${external_tool.audit_synthesis.id} exactly once in each QC round before judging the current report revision. Pass report_path as the declared report artifact, knowledge_paths as the complete Validated knowledge artifacts list from the task, and resolution_path as the Conflict-resolution artifact. Treat its quote-ID, source-URL, unused-reference, and snapshot-ID checks as authoritative; upstream Research typed tools already validated snapshot authorization and exact quote text. Preserve every reported mechanical issue in the QC verdict, but do not repeat those checks with grep or view."
      plan_coverage = "Use the validated knowledge JSON included in the task to judge whether the report answers every planner-produced subquestion."
      factual_fidelity = "Use each knowledge item's claim, confidence, quote_ids, and exact_quote fields to judge whether every material report statement and conclusion is logically supported without extrapolation. Reject any statement that appears to come from model training, memory, general background knowledge, assumption, or opinion rather than the validated artifacts. This is a semantic judgment; do not repeat the typed tool's text matching."
      conflict_handling = "Use the included conflict-resolution JSON to judge whether all resolved and unresolved conflicts are represented faithfully without hiding residual uncertainty."
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
