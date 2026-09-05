module "pplx_tools" {
  source = "./modules/pplx_tools"
}

starlark_tool "calculator" {
  description = "Perform isolated, resource-bounded numerical calculations for deep research. Use this tool for every exact or derived numeric result."
  max_steps   = 1000000
  timeout     = "5s"
}

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

    ${local.calculator_guidance}

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

  pplx_tool_ids = var.use_pplx ? [
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ] : []

  deep_dive_tool_call_quota = var.web_fetch_tool_call_quota == null ? {} : (
    var.use_pplx ? {
      (module.pplx_tools.pplx_fetch_tool_id) = var.web_fetch_tool_call_quota
      } : {
      web_fetch = var.web_fetch_tool_call_quota
    }
  )

  deep_dive_disallowed_tools = var.use_pplx ? ["web_search", "web_fetch"] : []

  source_tool_guidance = var.use_pplx ? join("\n", [
    "Use ${module.pplx_tools.pplx_pro_search_tool_id} to discover current sources",
    "and ${module.pplx_tools.pplx_fetch_tool_id} to fetch every source retained",
    "as evidence. Every fetch call must include url and the artifact_dir stated",
    "in the task prompt. For every successful artifact_path, call",
    "r42_register_artifact to obtain its artifact_id; do not call",
    "r42_save_artifact for that file. If fetch returns fetch_failed, it wrote",
    "no file: do not register anything; try another source URL or continue.",
    ]) : join("\n", [
    "Use the built-in web_search tool to discover current sources and web_fetch",
    "to read every source retained as evidence. Save the complete returned",
    "material as Markdown under the source artifact directory stated in the",
    "task prompt by calling r42_save_artifact with source set to the source URL. Use the returned",
    "artifact_id directly; do not call r42_register_artifact for that path.",
  ])

  offline_disallowed_tools = ["web_search", "web_fetch"]

  calculator_guidance = <<-PROMPT
    All exact or derived numerical work must be calculated by calling ${starlark_tool.calculator.id}. Pass raw inputs through data_json and use result_json. Do not perform arithmetic, ratios, percentages, unit conversions, averages, ranges, interpolation, or other derived numerical calculations mentally or outside the calculator. If calculation is not needed, do not call it.
  PROMPT

  plan_design_guidance = <<-PROMPT
    Your job is to design the research plan, not to answer the research topic.
    Treat the topic as a research problem before decomposing it into tasks.
    Infer the underlying question, then identify the central tensions,
    paradoxes, competing explanations, trade-offs, bottlenecks, time lags,
    incentive mismatches, supply-demand mismatches, accounting problems, and
    possible nonlinearities.

    The plan should collectively investigate, as applicable:
    - What is happening? Establish definitions, scope, baselines, timelines,
      magnitudes,
      actors, and institutional or technical context;
    - Why is it happening? Identify causal mechanisms, incentives, constraints,
      dependencies, feedback loops, and transmission channels;
    - What could make the obvious interpretation wrong? Seek contrary evidence,
      alternative mechanisms, measurement problems, hidden assumptions,
      double counting, selection effects, accounting mismatches, and historical
      counterexamples;
    - What does it imply? Trace consequences across the relevant systems;
    - How will we know whether the conclusions remain true? Identify measurable
      indicators, leading signals, scenario triggers, uncertainties, and
      falsification conditions.

    Where applicable, reconstruct an explicit causal chain such as
    input -> intermediate state -> observable output -> consequence. Identify
    conversion gates where one state does not automatically become the next;
    for example, announcement is not commitment, commitment is not financing,
    financing is not delivery, and capacity is not utilization. At least one
    task must test the full causal chain and its likely failure points when the
    topic contains such a chain.

    Identify relevant actors and distinguish resource, money, risk,
    information, and value flows. Determine who controls scarce resources,
    pricing or bargaining power, financing, downside risk, and the ability to
    transfer costs. Separate short-term and long-term clocks such as
    technology, construction, financing, regulation, adoption, depreciation,
    and workforce development when they differ.

    Enforce evidence discipline. Distinguish Fact (directly supported),
    Inference (derived from supported facts), and Hypothesis (still to be
    tested). Prefer government and regulatory sources, official statistics,
    filings and original disclosures, international organizations,
    peer-reviewed research, original technical documentation, and high-quality
    datasets. Use secondary media mainly for discovery and context. Important
    quantitative claims should be independently cross-checked where practical.

    For every major candidate thesis, require evidence that supports it,
    contradicts it, or offers an alternative explanation. Include at least one
    task explicitly focused on counter-evidence, competing explanations, or
    falsification whenever the topic permits it. At least one task should
    explicitly focus on counter-evidence, competing explanations, or
    falsification.

    Guard against inconsistent definitions, duplicated values, stock-versus-
    flow confusion, nominal-versus-real values, gross-versus-net values,
    announced-versus-committed-versus-completed activity, capacity-versus-
    utilization confusion, and correlation-versus-causation errors. When
    datasets describe different stages of the same activity, require
    reconciliation instead of mechanical addition.

    When useful, combine top-down system evidence with bottom-up cases. Choose
    cases because they test an important mechanism, not merely because they are
    famous. For evolving or uncertain systems, construct multiple plausible
    scenarios that differ by important uncertain variables and identify their
    triggers, leading indicators, monitoring variables, and conditions that
    would change the conclusion.

    Every task must be complementary and minimally redundant. Every task must
    have a narrow but consequential subquestion and
    instructions that state why it matters, what mechanism or hypothesis it
    tests, what evidence to collect, what distinctions and measurement pitfalls
    to preserve, which primary sources to prioritize, what counter-evidence to
    seek, and what conclusions, uncertainty, source conflicts, and unresolved
    questions to return. Build the smallest set of complementary tasks that
    achieves comprehensive coverage; do not create tasks merely to increase
    task count.

    Before submitting, verify that important causal claims can be tested, major
    assumptions can be challenged, relevant actors and bottlenecks are covered,
    quantitative claims can be reconciled without double counting, primary
    evidence is prioritized, at least one meaningful competing explanation and
    falsification path exist, and downstream synthesis can distinguish Fact,
    Inference, and Hypothesis.
  PROMPT
}

research "static" "plan" {
  for_each = length(coalesce(var.research_plan, [])) == 0 ? { default = true } : {}

  phase_mode       = "research_only"
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    ${local.plan_design_guidance}

    ${local.calculator_guidance}

    The workflow structure is fixed. Allocate every task into exactly one of
    the three existing R42 execution groups. Do not alter the workflow or
    introduce additional execution groups. Do not reinterpret these groups as
    three strictly sequential research phases.

    - parallel_tasks run concurrently and must be mutually independent. Use
      them for baseline, measurement, actor, mechanism, or counter-evidence
      tasks that do not read one another.
    - independent_serial_tasks start at the same time as parallel_tasks but run
      one at a time. They cannot read parallel results or earlier tasks in their
      own group, so each must be independently executable. Use them for
      independent methodology, validation, or alternative-framework work.
    - final_serial_tasks start only after both earlier groups finish. They may
      compare, audit, synthesize, or extend upstream artifacts. State their
      upstream inputs explicitly; r42 injects validated typed-tool JSON results.
      Final tasks cannot depend on results from earlier tasks in their own group
      because all task prompts are materialized together.

    Any individual group may be empty. Across all groups, use globally unique
    lowercase task IDs that are safe as directory names. For every task, write
    a precise subquestion and instructions that define its evidence scope,
    exclusions, and expected reasoning. Do not perform the research yourself.
    Every task must tell the researcher to save complete source material as
    Markdown under its block_wd()/artifacts/ directory and associate every
    quote with the artifact_id returned by r42_save_artifact. Tell it to call
    r42_save_artifact after every source read, passing the source URL in source.
    Tell it to use the returned artifact_id directly and not call
    r42_register_artifact afterward. A source identifier alone is not evidence.
    Do not perform the research yourself. You must finish by calling
    ${go_tool.submit_research_plan.id}.

    The topic is the complete input for planning. Do not acquire external
    evidence. Create the plan in this closed Research session and call the
    typed submission tool.

    Never use PowerShell, a shell, curl, wget, or scripts and command-line
    programs to search the web or download remote content. Do not use them as
    a workaround when a search or source-reading tool reaches its call quota
    or returns an error. Only the search and source-reading tools configured
    for this task may access remote sources. When their quotas are exhausted,
    continue with the evidence already collected.

    Decide how many tasks belong in each of the three execution groups. Explain
    each task through its subquestion and instructions, then call
    ${go_tool.submit_research_plan.id}. Do not finish with prose or a JSON code
    block; the accepted typed-tool call is the only valid completion.
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
  }
  tool_use "calculate" {
    tool_id = starlark_tool.calculator.id
    input_from_agent = {
      code = {
        desc    = "A Starlark program for exact or derived numerical calculations needed by the planning session."
        sources = []
      }
      data_json = {
        desc    = "A JSON string containing raw inputs for the Starlark calculation."
        sources = []
      }
    }
  }
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

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

        During Collection, research this subquestion independently.
        ${local.source_tool_guidance}
        Source artifact directory: "${artifact("sources").path}"
        Submit a collection checkpoint once the evidence is sufficient.

        During closed Research, do not search or fetch. Use r42_search_artifact
        or r42_search_artifacts to get a quote_ref for supporting text; call
        r42_capture_quote when surrounding lines are needed. Pass only those
        quote_ref values in each claim's citations; do not copy quote text or
        source metadata. Then call
        ${go_tool.submit_knowledge.id}; r42 binds its declared knowledge
        artifact_id and the subquestion exactly as assigned above, with atomic
        knowledge claim IDs prefixed "${task.id}-kb-". Do not finish with prose
        or a JSON code block.
      PROMPT
      tool_use = {
        calculate = {
          tool_id = starlark_tool.calculator.id
          input_from_agent = {
            code = {
              desc    = "A Starlark program for exact or derived numerical calculations in this research task."
              sources = []
            }
            data_json = {
              desc    = "A JSON string containing raw inputs for the Starlark calculation."
              sources = []
            }
          }
        }
        submit_knowledge = {
          tool_id   = go_tool.submit_knowledge.id
          terminate = true
          input = {
            artifact_id            = artifact("knowledge").id
            _r42_artifact_path     = ""
            subquestion            = task.subquestion
            quote_id_prefix        = "${task.id}-quote-"
          }
        }
      }
      collection_tool_ids = local.pplx_tool_ids
      tool_call_quota     = local.deep_dive_tool_call_quota
      disallowed_tools    = local.deep_dive_disallowed_tools
      permission          = "approve_all"
      artifact = {
        sources = {
          type        = "directory"
          path        = "${block_wd()}/${index}/artifacts"
          description = "Source material collected for this parallel subquestion."
        }
        knowledge = {
        type      = "file"
        path      = "${block_wd()}/${index}/${task.id}/knowledge.json"
		description = "Validated claims and exact quotes for this parallel subquestion"
        required  = true
        non_empty = true
        }
      }
      retry = null
      collection_qc = {
        model_provider = model_provider.qc
      }
      qc = {
        criteria = {
          knowledge_items = "Judge whether every knowledge claim answers the assigned subquestion, expresses uncertainty carefully, and is fully supported by its cited quote records."
          quote_records = "Judge whether each accepted quote semantically entails the claim that cites it without changing party, period, scope, causality, or qualifiers. Treat the typed-tool trusted quote registry and canonical quote fields as authoritative."
          traceability = "Judge whether the cited evidence is sufficient and decision-relevant, and whether material contrary evidence or uncertainty is omitted. Treat typed-tool IDs and graph references as authoritative."
        }
        model_provider   = model_provider.qc
        reasoning_effort = var.reasoning_effort
        permission       = "approve_all"
        tool_ids         = [starlark_tool.calculator.id]
        tool_call_quota  = { (starlark_tool.calculator.id) = 20 }
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

        During Collection, research this subquestion independently.
        ${local.source_tool_guidance}
        Source artifact directory: "${artifact("sources").path}"
        Submit a collection checkpoint once the evidence is sufficient.

        During closed Research, do not search or fetch. Use r42_search_artifact
        or r42_search_artifacts to get a quote_ref for supporting text; call
        r42_capture_quote when surrounding lines are needed. Pass only those
        quote_ref values in each claim's citations; do not copy quote text or
        source metadata. Then call ${go_tool.submit_knowledge.id}
        with its declared knowledge artifact_id and subquestion exactly
        as assigned above, with atomic knowledge claim IDs prefixed
        "${task.id}-kb-". Do not finish with prose or a JSON code block.
      PROMPT
      tool_use = {
        calculate = {
          tool_id = starlark_tool.calculator.id
          input_from_agent = {
            code = {
              desc    = "A Starlark program for exact or derived numerical calculations in this research task."
              sources = []
            }
            data_json = {
              desc    = "A JSON string containing raw inputs for the Starlark calculation."
              sources = []
            }
          }
        }
        submit_knowledge = {
          tool_id   = go_tool.submit_knowledge.id
          terminate = true
          input = {
            artifact_id            = artifact("knowledge").id
            _r42_artifact_path     = ""
            subquestion            = task.subquestion
            quote_id_prefix        = "${task.id}-quote-"
          }
        }
      }
      collection_tool_ids = local.pplx_tool_ids
      tool_call_quota     = local.deep_dive_tool_call_quota
      disallowed_tools    = local.deep_dive_disallowed_tools
      permission          = "approve_all"
      artifact = {
        sources = {
          type        = "directory"
          path        = "${block_wd()}/${index}/artifacts"
          description = "Source material collected for this serial subquestion."
        }
        knowledge = {
        type      = "file"
        path      = "${block_wd()}/${index}/${task.id}/knowledge.json"
		description = "Validated claims and exact quotes for this independent serial subquestion"
        required  = true
        non_empty = true
        }
      }
      retry = null
      collection_qc = {
        model_provider = model_provider.qc
      }
      qc = {
        criteria = {
          knowledge_items = "Judge whether every knowledge claim answers the assigned subquestion, expresses uncertainty carefully, and is fully supported by its cited quote records."
          quote_records = "Judge whether each accepted quote semantically entails the claim that cites it without changing party, period, scope, causality, or qualifiers. Treat the typed-tool trusted quote registry and canonical quote fields as authoritative."
          traceability = "Judge whether the cited evidence is sufficient and decision-relevant, and whether material contrary evidence or uncertainty is omitted. Treat typed-tool IDs and graph references as authoritative."
        }
        model_provider   = model_provider.qc
        reasoning_effort = var.reasoning_effort
        permission       = "approve_all"
        tool_ids         = [starlark_tool.calculator.id]
        tool_call_quota  = { (starlark_tool.calculator.id) = 20 }
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
      import_artifact = {
        parallel_knowledge = {
          desc    = "Validated knowledge artifacts from parallel deep-dive tasks."
          sources = flatten([for item in research.dynamic.parallel_deep_dive.tasks : values(item.artifact)])
        }
        independent_knowledge = {
          desc    = "Validated knowledge artifacts from independent serial deep-dive tasks."
          sources = flatten([for item in research.dynamic.independent_serial_deep_dive.tasks : values(item.artifact)])
        }
      }
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
          [for item in research.dynamic.parallel_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
        ))}

        During Collection, collect only evidence needed beyond the validated
        upstream JSON.
        ${local.source_tool_guidance}
        Source artifact directory: "${artifact("sources").path}"
        Submit a collection checkpoint.
        If the upstream JSON is sufficient, submit an empty
        collection checkpoint instead of searching.

        During closed Research, do not search or fetch. Use the validated JSON
        above directly. Reuse its quote_ref values, or call
        r42_search_artifact / r42_search_artifacts and optionally
        r42_capture_quote for newly registered evidence. Pass only quote_ref
        values in each claim's citations; do not copy quote text or source
        metadata. Then call
        ${go_tool.submit_knowledge.id}
        with its declared knowledge artifact_id, subquestion exactly as
        assigned above, with atomic knowledge claim IDs prefixed
        "${task.id}-kb-". Do not finish with prose or a JSON code block.
      PROMPT
      tool_use = {
        calculate = {
          tool_id = starlark_tool.calculator.id
          input_from_agent = {
            code = {
              desc    = "A Starlark program for exact or derived numerical calculations in this research task."
              sources = []
            }
            data_json = {
              desc    = "A JSON string containing raw inputs for the Starlark calculation."
              sources = []
            }
          }
        }
        submit_knowledge = {
          tool_id   = go_tool.submit_knowledge.id
          terminate = true
          input = {
            artifact_id            = artifact("knowledge").id
            _r42_artifact_path     = ""
            subquestion            = task.subquestion
            quote_id_prefix        = "${task.id}-quote-"
          }
          input_from_agent = {
            knowledge = {
              desc = "New atomic knowledge claims supported by the authorized artifacts and validated upstream JSON."
              sources = flatten([
                [for item in research.dynamic.parallel_deep_dive.tasks : values(item.artifact)],
                [for item in research.dynamic.independent_serial_deep_dive.tasks : values(item.artifact)],
              ])
            }
          }
        }
      }
      collection_tool_ids = local.pplx_tool_ids
      tool_call_quota     = local.deep_dive_tool_call_quota
      disallowed_tools    = local.deep_dive_disallowed_tools
      permission          = "approve_all"
      artifact = {
        sources = {
          type        = "directory"
          path        = "${block_wd()}/${index}/artifacts"
          description = "Source material collected for this final subquestion."
        }
        knowledge = {
        type      = "file"
        path      = "${block_wd()}/${index}/${task.id}/knowledge.json"
		description = "Validated claims and exact quotes for this dependent serial subquestion"
        required  = true
        non_empty = true
        }
      }
      retry = null
      collection_qc = {
        model_provider = model_provider.qc
      }
      qc = {
        criteria = {
          upstream_use = "Judge whether the candidate accurately uses the relevant claims and quotes from the validated upstream JSON included in the prompt."
          knowledge_items = "Judge whether every new claim answers the assigned subquestion, expresses uncertainty carefully, and is fully supported by its cited quotes."
          quote_records = "Judge whether each accepted quote semantically entails the claim that cites it without changing party, period, scope, causality, or qualifiers. Treat the typed-tool trusted quote registry and canonical quote fields as authoritative."
        }
        model_provider   = model_provider.qc
        reasoning_effort = var.reasoning_effort
        permission       = "approve_all"
        tool_ids         = [starlark_tool.calculator.id]
        tool_call_quota  = { (starlark_tool.calculator.id) = 20 }
      }
    }
  ]
}

research "static" "resolve_conflicts" {
  phase_mode       = "research_only"
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

    ${local.calculator_guidance}

    You must finish by calling ${go_tool.submit_conflict_resolution.id}.

    This is a closed Research phase. Do not acquire external evidence; the
    validated upstream JSON is the complete evidence basis. Compare that JSON
    and submit the resolution.

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
      [for item in research.dynamic.parallel_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
      [for item in research.dynamic.final_serial_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
    ))}

    Compare every included knowledge item and quote record. Call
    ${go_tool.submit_conflict_resolution.id}; r42 binds the declared resolution
    artifact_id and reviewed_artifacts containing every path
    above, all detected conflicts and decisions, and synthesis_guidance for the
    final writer. An empty conflicts list is valid only after an explicit
    cross-file comparison.
  PROMPT
  import_artifact "parallel_knowledge" {
    desc    = "Validated knowledge artifacts from parallel deep-dive tasks."
    sources = flatten([for item in research.dynamic.parallel_deep_dive.tasks : values(item.artifact)])
  }
  import_artifact "independent_knowledge" {
    desc    = "Validated knowledge artifacts from independent serial deep-dive tasks."
    sources = flatten([for item in research.dynamic.independent_serial_deep_dive.tasks : values(item.artifact)])
  }
  import_artifact "final_knowledge" {
    desc    = "Validated knowledge artifacts from dependent serial deep-dive tasks."
    sources = flatten([for item in research.dynamic.final_serial_deep_dive.tasks : values(item.artifact)])
  }
  tool_use "submit_conflict_resolution" {
    tool_id   = go_tool.submit_conflict_resolution.id
    terminate = true
    input = {
      artifact_id           = artifact("resolution").id
      _r42_artifact_path    = ""
      topic                 = var.topic
      reviewed_artifacts = concat(
        [for item in research.dynamic.parallel_deep_dive.tasks : item.artifact.knowledge.path],
        [for item in research.dynamic.independent_serial_deep_dive.tasks : item.artifact.knowledge.path],
        [for item in research.dynamic.final_serial_deep_dive.tasks : item.artifact.knowledge.path],
      )
    }
    input_from_agent = {
      conflicts = {
        desc = "Every detected cross-subquestion conflict and its evidence-backed decision."
        sources = flatten([
          [for item in research.dynamic.parallel_deep_dive.tasks : values(item.artifact)],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : values(item.artifact)],
          [for item in research.dynamic.final_serial_deep_dive.tasks : values(item.artifact)],
        ])
      }
      synthesis_guidance = {
        desc = "Guidance for the final writer, including unresolved uncertainty."
        sources = flatten([
          [for item in research.dynamic.parallel_deep_dive.tasks : values(item.artifact)],
          [for item in research.dynamic.independent_serial_deep_dive.tasks : values(item.artifact)],
          [for item in research.dynamic.final_serial_deep_dive.tasks : values(item.artifact)],
        ])
      }
    }
  }
  tool_use "calculate" {
    tool_id = starlark_tool.calculator.id
    input_from_agent = {
      code = {
        desc    = "A Starlark program for exact or derived numerical calculations needed for conflict analysis."
        sources = []
      }
      data_json = {
        desc    = "A JSON string containing raw inputs for the Starlark calculation."
        sources = []
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
      evidence_semantics = "Judge whether the accepted quotes actually support each conflict decision without changing scope or qualifiers. Treat typed-tool paths, IDs, trusted quote references, and canonical quote fields as authoritative."
    }
    model_provider   = model_provider.qc
    reasoning_effort = var.reasoning_effort
    permission       = "approve_all"
    tool_ids         = [starlark_tool.calculator.id]
    tool_call_quota  = { (starlark_tool.calculator.id) = 20 }
  }
}

research "static" "synthesize" {
  phase_mode       = "research_only"
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the senior editor. Produce one evidence-dense Markdown report from
    the validated knowledge artifacts and conflict-resolution record. Every
    material factual statement must cite the quote ID that supports it. Use
    only quote IDs from `quotes[].id` as report citations, never
    `knowledge[].id` values. Write each Markdown citation exactly as
    `[QUOTE_ID]`; do not wrap quote IDs in backticks. Do not add or copy URLs;
    ${go_tool.generate_source_table.id} adds canonical URLs automatically.
    Clearly label unresolved conflicts and limitations. After writing or
    revising the report, call ${go_tool.generate_source_table.id} with the
    report artifact ID before finalizing; do not write or copy source URLs
    yourself.

    ${local.calculator_guidance}

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

    This is a closed Research phase. Do not acquire external evidence; the
    validated upstream JSON is the complete evidence basis. Synthesize only
    that JSON and write the report.

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
      [for item in research.dynamic.parallel_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
      [for item in research.dynamic.final_serial_deep_dive.tasks : "- ${item.artifact.knowledge.path}"],
    ))}

    Conflict-resolution JSON:
    ${research.static.resolve_conflicts.result}

    Conflict-resolution artifact path for Final QC's typed audit:
    ${research.static.resolve_conflicts.artifact.resolution.path}

    Use only the included validated JSON and registered artifacts, then write
    the final report to ${artifact("report").path}. Include
    an executive summary, findings organized around the planner-produced task groups,
    resolved and unresolved contradictions, and limitations. Before writing,
    remove every
    statement that cannot be traced to a validated knowledge claim, its quote,
    or an explicit conflict-resolution decision. Do not merely concatenate the
    files, and do not use pretrained knowledge or opinion to make the report
    sound complete. Then call ${go_tool.generate_source_table.id} with the
    report artifact ID before calling the Research terminal tool.
  PROMPT
  import_artifact "parallel_knowledge" {
    desc    = "Validated knowledge artifacts from parallel deep-dive tasks."
    sources = flatten([for item in research.dynamic.parallel_deep_dive.tasks : values(item.artifact)])
  }
  import_artifact "independent_knowledge" {
    desc    = "Validated knowledge artifacts from independent serial deep-dive tasks."
    sources = flatten([for item in research.dynamic.independent_serial_deep_dive.tasks : values(item.artifact)])
  }
  import_artifact "final_knowledge" {
    desc    = "Validated knowledge artifacts from dependent serial deep-dive tasks."
    sources = flatten([for item in research.dynamic.final_serial_deep_dive.tasks : values(item.artifact)])
  }
  import_artifact "conflict_resolution" {
    desc    = "Validated cross-subquestion conflict-resolution record."
    sources = values(research.static.resolve_conflicts.artifact)
  }
  tool_use "generate_source_table" {
    tool_id = go_tool.generate_source_table.id
    input = {
      _r42_report_path = ""
      knowledge_artifact_ids = concat(
        [for item in research.dynamic.parallel_deep_dive.tasks : item.artifact.knowledge.id],
        [for item in research.dynamic.independent_serial_deep_dive.tasks : item.artifact.knowledge.id],
        [for item in research.dynamic.final_serial_deep_dive.tasks : item.artifact.knowledge.id],
      )
      _r42_knowledge_paths = []
    }
    input_from_agent = {
      report_artifact_id = {
        desc    = "The declared final report artifact ID."
        sources = []
      }
    }
  }
  tool_use "calculate" {
    tool_id = starlark_tool.calculator.id
    input_from_agent = {
      code = {
        desc    = "A Starlark program for exact or derived numerical calculations used in the synthesis."
        sources = []
      }
      data_json = {
        desc    = "A JSON string containing raw inputs for the Starlark calculation."
        sources = []
      }
    }
  }
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
      mechanical_audit = "Call ${external_tool.audit_synthesis.id} exactly once in each QC round before judging the current report revision. Pass report_path as the declared report artifact, knowledge_paths as the complete Validated knowledge artifacts list from the task, and resolution_path as the Conflict-resolution artifact. Treat only its path, readability, schema, and artifact-ID checks as authoritative. After completing all Final QC repairs in the current round, call ${go_tool.generate_source_table.id} exactly once with the declared report artifact ID, before calling r42_qc_complete. Do not inspect or reproduce source-table, quote-ID, quote-reference, or URL validation with grep, view, shell, or another tool."
      plan_coverage = "Use the validated knowledge JSON included in the task to judge whether the report answers every planner-produced subquestion."
      factual_fidelity = "Use each knowledge item's claim, confidence, quote_ids, and exact_quote fields to judge whether every material report statement and conclusion is logically supported without extrapolation. Reject any statement that appears to come from model training, memory, general background knowledge, assumption, or opinion rather than the validated artifacts. This is a semantic judgment; do not compare canonical quote text with artifacts."
      conflict_handling = "Use the included conflict-resolution JSON to judge whether all resolved and unresolved conflicts are represented faithfully without hiding residual uncertainty."
      citation_semantics = "For citations in the report, decide whether the cited exact quote semantically supports the surrounding claim. Do not reopen artifacts to compare canonical quote text."
      provenance_guard = "Apply the configured final_qc_strictness to provenance wording. Reject material claims that rely on uncited model opinion or on a source limitation as a substitute for evidence. Do not turn a harmless, evidence-grounded limitation statement into a separate issue when it does not affect a conclusion."
    }
    model_provider   = model_provider.qc
    reasoning_effort = var.reasoning_effort
    disallowed_tools = ["bash", "powershell", "edit", "task", "ask_user", "web_search", "web_fetch"]
    permission       = "approve_all"
    tool_ids          = [starlark_tool.calculator.id, external_tool.audit_synthesis.id, go_tool.generate_source_table.id]
    tool_call_quota = {
      (starlark_tool.calculator.id) = 20
      (external_tool.audit_synthesis.id) = 10
      (go_tool.generate_source_table.id) = 10
    }
  }
}

output "knowledge_paths" {
  description = "Validated knowledge artifacts produced by all planner-selected task groups."
  value = concat(
      [for item in research.dynamic.parallel_deep_dive.tasks : item.artifact.knowledge.path],
      [for item in research.dynamic.independent_serial_deep_dive.tasks : item.artifact.knowledge.path],
      [for item in research.dynamic.final_serial_deep_dive.tasks : item.artifact.knowledge.path],
  )
}

output "conflict_resolution_path" {
  description = "Validated cross-subquestion conflict decisions."
  value       = research.static.resolve_conflicts.artifact.resolution.path
}

output "report_path" {
  description = "Final deep-research Markdown report."
  value       = research.static.synthesize.artifact.report.path
}
