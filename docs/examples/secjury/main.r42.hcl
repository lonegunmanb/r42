locals {
  offline_tools = ["web_search", "web_fetch", "bash", "powershell", "edit", "task", "ask_user"]
  pplx_tool_ids = var.use_pplx ? [
    module.pplx_tools.pplx_finance_search_tool_id,
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ] : []
  dcf_collection_tool_ids = concat([starlark_tool.calculator.id], local.pplx_tool_ids)
  pplx_tool_call_quota = var.pplx_tool_call_quota == null ? {} : {
    for tool_id in [module.pplx_tools.pplx_fetch_tool_id] : tool_id => var.pplx_tool_call_quota
    if var.use_pplx
  }
  build_dcf_disallowed_tools = concat(
    ["ask_user"],
    var.use_pplx ? ["web_search", "web_fetch"] : [],
  )
  source_tool_guidance = var.use_pplx ? join("\n", [
    "Use pplx_finance_search first (${module.pplx_tools.pplx_finance_search_tool_id}) for quotes, financial statements, valuation data, earnings, estimates, peers, and ownership.",
    "Use ${module.pplx_tools.pplx_pro_search_tool_id} for filings, investor-relations material, other public sources, and fallback discovery.",
    "Use ${module.pplx_tools.pplx_fetch_tool_id} to fetch every retained URL into the evidence artifact_dir stated below.",
    "For every returned artifact_path, call r42_register_artifact; do not call r42_save_artifact for that already-written file.",
    "If fetch returns fetch_failed, it wrote no file: do not register anything; try another source URL or continue with remaining sources.",
    "You may fetch the calculator documentation only to resolve a Starlark usage problem; do not register that operational lookup as DCF evidence.",
    "The built-in web_search and web_fetch tools are disabled while use_pplx is true.",
  ]) : join("\n", [
    "Use the built-in web_search tool to discover public sources and web_fetch to read every source retained as evidence.",
    "Save the complete material returned by every retained source under the evidence directory with r42_save_artifact; that call registers it.",
    "You may fetch the calculator documentation only to resolve a Starlark usage problem; do not save or register that operational lookup as DCF evidence.",
  ])

  jurors = [
    {
      id = "buffett", name = "Warren Buffett", group = "value_quality"
      persona_line = "Price is what you pay; value is what you get."
      method_rules = ["stress durable cash economics", "challenge optimistic terminal assumptions", "require a margin of safety"]
    },
    {
      id = "munger", name = "Charlie Munger", group = "value_quality"
      persona_line = "Invert, always invert."
      method_rules = ["invert the stated assumptions", "identify the most fragile dependency", "penalize unsupported durability"]
    },
    {
      id = "graham", name = "Benjamin Graham", group = "value_quality"
      persona_line = "Insist on margin of safety."
      method_rules = ["judge downside sensitivity", "do not infer value from missing inputs", "prefer conservative adjustments"]
    },
    {
      id = "klarman", name = "Seth Klarman", group = "value_quality"
      persona_line = "Risk first, return second."
      method_rules = ["focus on downside cases", "challenge optimistic scenarios", "reject thin evidence"]
    },
    {
      id = "lynch", name = "Peter Lynch", group = "growth_innovation"
      persona_line = "Buy what you understand, then check the numbers."
      method_rules = ["test whether growth is explainable", "compare growth with cash conversion", "flag unsupported narrative"]
    },
    {
      id = "oneill", name = "William O'Neil", group = "growth_innovation"
      persona_line = "Great stocks show earnings and price strength."
      method_rules = ["stress growth acceleration", "check operating support", "stay within the accepted model packet"]
    },
    {
      id = "thiel", name = "Peter Thiel", group = "growth_innovation"
      persona_line = "Competition is for losers."
      method_rules = ["test pricing power", "challenge scale assumptions", "penalize unsupported terminal optimism"]
    },
    {
      id = "wood", name = "Cathie Wood", group = "growth_innovation"
      persona_line = "We buy the future when the curve is misunderstood."
      method_rules = ["test adoption scenarios", "stress reinvestment needs", "reject unsupported optimism"]
    },
    {
      id = "soros", name = "George Soros", group = "macro_skeptic"
      persona_line = "Watch reflexivity between perception and fundamentals."
      method_rules = ["stress discount rates across regimes", "challenge feedback loops", "state the thesis invalidator"]
    },
    {
      id = "marks", name = "Howard Marks", group = "macro_skeptic"
      persona_line = "The most important thing is the price paid for risk."
      method_rules = ["judge compensation for risk", "stress sensitivities", "separate quality from valuation"]
    },
    {
      id = "burry", name = "Michael Burry", group = "macro_skeptic"
      persona_line = "Find the accounting and consensus blind spots."
      method_rules = ["challenge accounting assumptions", "inspect equity bridge inputs", "find hidden downside"]
    },
    {
      id = "zhang_kun", name = "Zhang Kun", group = "china_fund"
      persona_line = "Long-term cash generation matters more than short-term noise."
      method_rules = ["judge long-term cash durability", "test reinvestment assumptions", "avoid unsupported conviction"]
    },
    {
      id = "minervini", name = "Mark Minervini", group = "technical_quant"
      persona_line = "Only buy strength with risk control."
      method_rules = ["use model downside as risk context", "do not invent technical signals", "define invalidation from accepted inputs"]
    },
    {
      id = "serenity", name = "Serenity", group = "ai_bottleneck"
      persona_line = "Find the scarce bottleneck before relabeling the obvious winner."
      method_rules = ["test bottleneck economics", "stress required investment", "require evidence of mispricing"]
    },
    {
      id = "jensen_huang", name = "Jensen Huang", group = "ai_bottleneck"
      persona_line = "The bottleneck is where accelerated computing cannot scale."
      method_rules = ["test capacity assumptions", "inspect capital intensity", "challenge scalability"]
    },
    {
      id = "altman", name = "Sam Altman", group = "ai_bottleneck"
      persona_line = "Ask what capacity must exist for intelligence to scale."
      method_rules = ["evaluate embedded capacity", "stress second-order cash requirements", "penalize weak causality"]
    },
    {
      id = "musk", name = "Elon Musk", group = "ai_bottleneck"
      persona_line = "Manufacturing and supply chain execution decide the outcome."
      method_rules = ["stress execution", "inspect capital and working capital", "challenge cash conversion"]
    },
    {
      id = "evidence_auditor", name = "Evidence Auditor", group = "valuation_review"
      persona_line = "Every material input needs an auditable basis."
      method_rules = ["trace inputs to collector data or retained snapshots", "flag unsupported inputs", "check dates, units, and security class"]
    },
    {
      id = "assumption_challenger", name = "Assumption Challenger", group = "valuation_review"
      persona_line = "A valuation is only as sound as its most fragile assumption."
      method_rules = ["stress scenario assumptions", "challenge required return and terminal behavior", "identify sensitivity concentration"]
    },
    {
      id = "valuation_reviewer", name = "Valuation Reviewer", group = "valuation_review"
      persona_line = "Reconcile the selected model families before interpreting them."
      method_rules = ["review model applicability", "review equity-value bridges", "review per-share ranges without recalculation"]
    },
  ]
}

module "pplx_tools" {
  source = "./modules/pplx_tools"
}

starlark_tool "calculator" {
  description = "Perform isolated numerical calculations for the DCF model. Use this tool for every derived numeric result."
  max_steps   = 1000000
  timeout     = "5s"
}

research "static" "build_dcf" {
  phase_mode       = "collection_only"
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt    = "You are the sole DCF modeling agent for secjury."
  prompt = <<-PROMPT
    Research and build a defensible DCF for target ${jsonencode(var.target)} as of ${var.valuation_date}. Follow the supplied DCF process, but return only one JSON object and do not create any spreadsheet or xlsx artifact.
    Use the registered yahoo-finance skill when it can efficiently provide market prices, share data, capital structure, fundamentals, or price history. Prefer its machine-readable JSON output.

    This is one persistent Collection session. Use yahoo-finance together with the source tools described below to obtain the public evidence required by the dcf-model skill. Prefer machine-readable market data, primary filings, and investor-relations sources.
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity artifact_dir: %s", artifact("evidence").path) : format("Evidence directory: %s", artifact("evidence").path)}
    All retained sources must have been public on or before ${var.valuation_date}. If calculation exposes a missing raw input, resume acquisition in this same session, then calculate again. Stop acquiring only when the model can be built without inventing unavailable facts.

    Before substantive modeling, call ${go_tool.update_dcf_progress.id} with a concise, company-specific ordered DCF execution plan whose steps all have status pending. Work through one step at a time. Before changing a step to completed, call ${go_tool.update_dcf_progress.id} with the complete current plan and record exact values, units, periods, source IDs, URLs, artifact references or precise locators, formulas, derivations, assumptions, and unresolved questions in that step. After any interruption or context compaction, read ${artifact("progress").path} before continuing. Do not repeat completed work unless its recorded evidence is missing or contradicted, and document the reason before revisiting it. Call ${go_tool.update_dcf_progress.id} once more with every step completed before submitting the model.

    All derived numeric values must be calculated by calling ${starlark_tool.calculator.id}. Write a Starlark program for the data available at that moment, pass raw inputs through data_json, and use its result_json in the model and progress record. Do not perform arithmetic, discounting, interpolation, averaging, tax, cash-flow, terminal-value, equity-bridge, per-share, implied-return, or sensitivity calculations mentally or outside the calculator. Raw source retrieval and extraction are not derived numeric work. If the calculator rejects a program, inspect the returned issue, correct the program or inputs, and retry in this same session.

    The JSON must have exactly two top-level fields: model and sources.
    The model object must use schema_version "dcf-model.v2" and exactly these fields:
    schema_version, company, valuation_date, assumptions, historical, projections, valuation, sensitivity.
    Use decimal rates (0.10 means 10%). Include 3-5 historical periods, 5-10 projection periods, and an odd square WACC/terminal-growth sensitivity grid with its base case at the center. Every material raw input must be traceable to a source entry with a stable id, title, URL, published_date when known, and accessed_date. Never fabricate unavailable data; use an explicitly conservative estimate only when the process permits it.

    Required nested fields:
    company={name,ticker,exchange,currency}; assumptions={wacc,terminal_growth};
    historical/projections items={period,revenue,revenue_growth,ebit,ebit_margin,tax_rate,nopat,da,capex,change_nwc,ufcf,discount_period,discount_factor,pv_ufcf};
    valuation={pv_explicit_fcf,terminal_fcf,terminal_value,pv_terminal_value,enterprise_value,net_debt,equity_value,diluted_shares,implied_value_per_share,current_price,implied_return};
    sensitivity items={wacc,terminal_growth,implied_value_per_share}.
    sources is an array whose items={id,title,url,published_date,accessed_date}.

    Finish by calling ${go_tool.submit_dcf_model.id} with the complete JSON object. Do not finish with prose or a JSON code block; the accepted typed-tool call is the only valid completion.
  PROMPT
  collection_skill_directories = ["${path.module}/skills"]
  collection_skills            = ["dcf-model", "yahoo-finance"]
  collection_tool_ids          = local.dcf_collection_tool_ids
  tool_call_quota              = local.pplx_tool_call_quota
  disallowed_tools             = local.build_dcf_disallowed_tools
  permission                   = "approve_all"

  artifact "evidence" {
    type        = "directory"
    path        = "${block_wd()}/artifacts"
    description = "Public source material retained for the DCF model."
    required    = true
    non_empty   = true
  }

  artifact "progress" {
    type        = "file"
    path        = "${block_wd()}/progress.json"
    description = "Monotonic DCF execution checkpoint maintained only by update_dcf_progress."
    required    = true
    non_empty   = true
  }

  artifact "combined" {
    type        = "file"
    path        = "${block_wd()}/modeling/dcf-output.json"
    description = "Complete dcf-model.v2 payload containing model and sources."
    required    = true
    non_empty   = true
  }

  artifact "model" {
    type        = "file"
    path        = "${block_wd()}/modeling/dcf-model.json"
    description = "Frozen DCF model reviewed by every juror."
    required    = true
    non_empty   = true
  }

  artifact "sources" {
    type        = "file"
    path        = "${block_wd()}/modeling/dcf-sources.json"
    description = "Source declarations returned with the frozen DCF model."
    required    = true
    non_empty   = true
  }

  tool_use "update_progress" {
    tool_id = go_tool.update_dcf_progress.id
    input = {
      progress_path  = artifact("progress").path
      target         = var.target
      valuation_date = var.valuation_date
    }
    input_from_agent = {
      steps = {
        desc    = "The complete ordered DCF plan with current status and all accumulated evidence."
        sources = [artifact("evidence")]
      }
    }
  }

  tool_use "submit_model" {
    tool_id   = go_tool.submit_dcf_model.id
    terminate = true
    input = {
      combined_path  = artifact("combined").path
      model_path     = artifact("model").path
      sources_path   = artifact("sources").path
      progress_path  = artifact("progress").path
      target         = var.target
      valuation_date = var.valuation_date
    }
    input_from_agent = {
      model = {
        desc    = "The complete dcf-model.v2 model object produced by the copied DCF process."
        sources = [artifact("evidence"), artifact("progress")]
      }
      sources = {
        desc    = "Stable source records supporting every material raw input in the DCF."
        sources = [artifact("evidence"), artifact("progress")]
      }
    }
  }
}

research "dynamic" "review_dcf" {
  serial = false
  tasks = [
    for index, juror in local.jurors : {
      id               = juror.id
      phase_mode       = "research_only"
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = "You are one isolated valuation juror. Review only the frozen DCF through the supplied persona."
      prompt = <<-PROMPT
        Juror: ${juror.name} (${juror.id})
        Persona: ${juror.persona_line}
        Method challenges:
        - ${join("\n- ", juror.method_rules)}

        Frozen DCF model JSON:
        ${research.static.build_dcf.result}

        Evaluate only this frozen DCF. Challenge evidence quality, assumptions, formula interpretation, reinvestment, terminal dependence, sensitivities, and margin of safety. Every finding must cite exact JSON paths in model_paths. Do not edit or recalculate the model, simulate another persona, or introduce external facts.

        Finish by calling ${go_tool.submit_dcf_juror_opinion.id}. Do not finish with prose or a JSON code block.
      PROMPT
      import_artifact = {
        frozen_dcf = {
          desc    = "The immutable model and source declarations shared by every juror."
          sources = [research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources]
        }
      }
      disallowed_tools = local.offline_tools
      permission       = "approve_all"
      artifact = {
        opinion = {
          type        = "file"
          path        = "${block_wd()}/${index}/opinion.json"
          description = "Structured opinion from ${juror.name}."
          required    = true
          non_empty   = true
        }
      }
      tool_use = {
        submit_opinion = {
          tool_id   = go_tool.submit_dcf_juror_opinion.id
          terminate = true
          input = {
            opinion_path = artifact("opinion").path
            juror_id     = juror.id
          }
          input_from_agent = {
            verdict = {
              desc    = "One of accept, revise, reject, or abstain."
              sources = [research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources]
            }
            confidence = {
              desc    = "Juror confidence from 0 to 1; this is persona judgment, not statistical probability."
              sources = [research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources]
            }
            summary = {
              desc    = "Concise persona-specific assessment of the frozen DCF."
              sources = [research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources]
            }
            findings = {
              desc    = "Specific findings with severity, category, message, and exact model_paths."
              sources = [research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources]
            }
          }
        }
      }
      retry = null
      qc    = null
    }
  ]
}

research "static" "synthesize" {
  phase_mode       = "research_only"
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt    = "Synthesize the frozen DCF and submitted persona opinions. Never change model values or invent consensus."
  prompt = <<-PROMPT
    Frozen DCF: ${research.static.build_dcf.result}

    Submitted juror opinions:
    ${join("\n", [for task in research.dynamic.review_dcf.tasks : task.result])}

    Return a decision and report grounded only in the model, sources, and opinions. Clearly distinguish model output from juror judgment. The decision must be accept, revise, or reject. Finish by calling ${go_tool.submit_dcf_report.id}; it renders the original SecJury report structure to report.md without changing model values. Do not finish with prose or a JSON code block.
  PROMPT
  import_artifact "frozen_dcf" {
    desc    = "Frozen DCF model and sources."
    sources = [research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources]
  }
  import_artifact "jury_opinions" {
    desc    = "All independently generated persona opinions in roster order."
    sources = [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion]
  }
  disallowed_tools = local.offline_tools
  permission       = "approve_all"

  artifact "report_json" {
    type        = "file"
    path        = "${block_wd()}/report.json"
    description = "Structured synthesis decision."
    required    = true
    non_empty   = true
  }

  artifact "report" {
    type        = "file"
    path        = "${block_wd()}/report.md"
    description = "Final SecJury DCF report with model, sources, and jury opinions."
    required    = true
    non_empty   = true
  }

  tool_use "submit_report" {
    tool_id   = go_tool.submit_dcf_report.id
    terminate = true
    input = {
      report_json_path = artifact("report_json").path
      report_path      = artifact("report").path
      model_path       = research.static.build_dcf.artifact.model.path
      sources_path     = research.static.build_dcf.artifact.sources.path
      opinion_paths    = [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion.path]
      jurors           = local.jurors
    }
    input_from_agent = {
      decision = {
        desc    = "Final decision: accept, revise, or reject."
        sources = concat([research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      headline = {
        desc    = "Short headline grounded in the frozen DCF and juror opinions."
        sources = concat([research.static.build_dcf.artifact.model], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      summary = {
        desc    = "Synthesis that distinguishes model output from juror judgment."
        sources = concat([research.static.build_dcf.artifact.model], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      key_findings = {
        desc    = "Decision-relevant findings supported by the frozen DCF or submitted opinions."
        sources = concat([research.static.build_dcf.artifact.model], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      limitations = {
        desc    = "Material limitations preserved from the model and jury review."
        sources = concat([research.static.build_dcf.artifact.model, research.static.build_dcf.artifact.sources], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
    }
  }
}

output "dcf_model_path" {
  description = "Frozen DCF model reviewed by all jurors."
  value       = research.static.build_dcf.artifact.model.path
}

output "jury_opinion_paths" {
  description = "Structured opinions produced by the parallel dynamic tasks."
  value       = [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion.path]
}

output "report_path" {
  description = "Final synthesized Markdown report."
  value       = research.static.synthesize.artifact.report.path
}
