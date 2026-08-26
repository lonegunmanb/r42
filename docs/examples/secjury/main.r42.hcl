locals {
  offline_tools = ["web_search", "web_fetch", "bash", "powershell", "edit", "task", "ask_user"]
  pplx_tool_ids = [
    module.pplx_tools.pplx_finance_search_tool_id,
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ]
  pplx_tool_call_quota = var.pplx_tool_call_quota == null ? {} : {
    for tool_id in [module.pplx_tools.pplx_fetch_tool_id] : tool_id => var.pplx_tool_call_quota
    if var.use_pplx
  }
  build_dcf_disallowed_tools = concat(
    ["ask_user"],
    var.use_pplx ? ["web_search", "web_fetch"] : [],
    var.use_pplx ? [] : local.pplx_tool_ids,
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
      lens_name = "Durable cash generation"
      plain_question = "Can this business reliably turn its advantages into cash?"
      mandate = "Judge whether the base case reflects durable cash economics and whether the quoted price leaves a margin of safety."
      required_tests = ["compare historical and projected cash conversion", "test whether terminal cash economics follow from durable business advantages", "compare implied value and current price without relaxing the base case"]
      out_of_scope = ["failure-mode inversion assigned to Munger", "accounting and equity-bridge forensics assigned to Burry", "macro-regime feedback assigned to Soros"]
      decision_rule = "Support the valuation only when conservative cash generation still produces a meaningful margin of safety."
    },
    {
      id = "munger", name = "Charlie Munger", group = "value_quality"
      lens_name = "Failure-mode inversion"
      plain_question = "What must go wrong for this valuation to fail?"
      mandate = "Start from failure, identify the smallest plausible set of broken assumptions, and expose dependencies hidden by the base case."
      required_tests = ["identify the single most fragile dependency", "test correlated rather than isolated failures", "separate recoverable forecast misses from irreversible thesis breaks"]
      out_of_scope = ["base-case cash quality assigned to Buffett", "downside valuation floor assigned to Graham", "source traceability assigned to Evidence Auditor"]
      decision_rule = "Require revision when one plausible failure or a realistic cluster of failures destroys the investment case."
    },
    {
      id = "graham", name = "Benjamin Graham", group = "value_quality"
      lens_name = "Downside valuation floor"
      plain_question = "What is the defensible valuation floor under conservative assumptions?"
      mandate = "Find the capital-preservation floor visible in the frozen model and distinguish it from a hopeful base case."
      required_tests = ["inspect the conservative sensitivity outcomes", "test how much value survives weaker growth and margins", "measure dependence on terminal value and uncertain recovery"]
      out_of_scope = ["business-quality durability assigned to Buffett", "failure causality assigned to Munger", "macro-regime mechanics assigned to Soros"]
      decision_rule = "Require revision or rejection when downside can erase principal and the model shows no defensible floor."
    },
    {
      id = "klarman", name = "Seth Klarman", group = "value_quality"
      lens_name = "Uncertainty and payoff asymmetry"
      plain_question = "Are investors paid enough for uncertainty and hard-to-measure risks?"
      mandate = "Treat uncertainty itself as a cost and judge whether the range of outcomes is favorably asymmetric."
      required_tests = ["identify assumptions with wide but understated uncertainty", "compare plausible loss severity with modeled upside", "check whether limitations are reflected in the valuation conclusion"]
      out_of_scope = ["downside floor assigned to Graham", "cycle and risk pricing assigned to Marks", "accounting forensics assigned to Burry"]
      decision_rule = "Support the case only when the discount compensates for both known downside and material unknowns."
    },
    {
      id = "lynch", name = "Peter Lynch", group = "growth_innovation"
      lens_name = "Operational story plausibility"
      plain_question = "Can an ordinary reader connect the forecast to how the company sells, grows, and earns?"
      mandate = "Translate the projection into an understandable operating story and find narrative leaps that the numbers do not support."
      required_tests = ["connect revenue growth to observable business activity", "check that margins, capex, working capital, and cash conversion tell one coherent story", "flag unexplained forecast inflections"]
      out_of_scope = ["growth momentum assigned to O'Neil", "competitive protection assigned to Thiel", "source traceability assigned to Evidence Auditor"]
      decision_rule = "Require revision when the forecast cannot be explained in simple operating terms without inventing a story."
    },
    {
      id = "oneill", name = "William O'Neil", group = "growth_innovation"
      lens_name = "Growth momentum consistency"
      plain_question = "Does the model show genuine, sustained earnings momentum rather than a one-off spike?"
      mandate = "Inspect the direction, breadth, and persistence of modeled growth while refusing to invent unavailable market signals."
      required_tests = ["compare revenue, EBIT, and UFCF trend direction", "identify one-period spikes or abrupt unsupported inflections", "check whether acceleration persists long enough to support the valuation"]
      out_of_scope = ["business-story explainability assigned to Lynch", "upside optionality assigned to Wood", "technical price or volume signals absent from the frozen packet"]
      decision_rule = "Support the growth case only when acceleration is broad, internally supported, and persistent."
    },
    {
      id = "thiel", name = "Peter Thiel", group = "growth_innovation"
      lens_name = "Competitive advantage and pricing power"
      plain_question = "What protects growth and margins from competition?"
      mandate = "Test the durable competitive advantage implicitly required by the forecast and terminal economics."
      required_tests = ["compare projected and terminal margins with the implied competitive environment", "test whether scaling strengthens or dilutes economics", "identify where competitive decay would break the valuation"]
      out_of_scope = ["operating-story plausibility assigned to Lynch", "capacity execution assigned to Musk and Jensen Huang", "source traceability assigned to Evidence Auditor"]
      decision_rule = "Require revision when terminal economics depend on durable market power that the frozen packet does not support."
    },
    {
      id = "wood", name = "Cathie Wood", group = "growth_innovation"
      lens_name = "Upside optionality and reinvestment"
      plain_question = "Could the model miss a credible upside path, and what must be reinvested to reach it?"
      mandate = "Identify model-supported upside optionality while charging the full capital, timing, and cash requirements needed to realize it."
      required_tests = ["locate credible adoption or scaling upside visible in the packet", "test the capex, working-capital, and timing burden before payoff", "distinguish valuable optionality from unsupported optimism"]
      out_of_scope = ["accounting and equity-bridge inputs assigned to Burry", "base-case margin of safety assigned to Buffett", "macro financing regimes assigned to Soros"]
      decision_rule = "Recognize upside only when a visible path earns enough to justify its reinvestment and financing burden."
    },
    {
      id = "soros", name = "George Soros", group = "macro_skeptic"
      lens_name = "Macro regimes and reflexivity"
      plain_question = "How could rates, financing conditions, or market expectations feed back into this valuation?"
      mandate = "Stress regime changes and feedback loops between valuation, access to capital, operating outcomes, and investor expectations."
      required_tests = ["inspect discount-rate sensitivity across plausible regimes", "trace financing pressure into operations and back into valuation", "state the macro or reflexive condition that invalidates the thesis"]
      out_of_scope = ["accounting bridge inputs assigned to Burry", "company operating story assigned to Lynch", "source traceability assigned to Evidence Auditor"]
      decision_rule = "Require revision when the thesis needs one benign regime or ignores a self-reinforcing financing loop."
    },
    {
      id = "marks", name = "Howard Marks", group = "macro_skeptic"
      lens_name = "Cycle risk and price compensation"
      plain_question = "At this price, is the investor compensated for cycle risk and uncertainty?"
      mandate = "Separate company quality from the price paid and judge whether expected return compensates for adverse outcomes."
      required_tests = ["compare implied return with the severity of modeled downside", "inspect sensitivity dispersion and terminal-value concentration", "distinguish cyclical recovery from permanent economics"]
      out_of_scope = ["macro feedback mechanics assigned to Soros", "downside valuation floor assigned to Graham", "durable business quality assigned to Buffett"]
      decision_rule = "Support the valuation only when reward clearly compensates for adverse-cycle severity and uncertainty."
    },
    {
      id = "burry", name = "Michael Burry", group = "macro_skeptic"
      lens_name = "Accounting and equity-bridge forensics"
      plain_question = "Do accounting choices, cash, debt, or share count make the per-share value misleading?"
      mandate = "Find accounting, sign, classification, snapshot, and equity-bridge defects that can distort UFCF or per-share value."
      required_tests = ["inspect tax treatment and working-capital definitions and signs", "reconcile cash, restricted cash, debt, leases, and other debt-like items", "check diluted shares, security class, dates, units, and possible double counting"]
      out_of_scope = ["source retrieval and traceability assigned to Evidence Auditor", "macro regimes assigned to Soros", "operating growth story assigned to Lynch"]
      decision_rule = "Require revision or rejection for any material accounting or bridge ambiguity that can distort per-share value."
    },
    {
      id = "zhang_kun", name = "Zhang Kun", group = "china_fund"
      lens_name = "Long-duration reinvestment quality"
      plain_question = "Can the company compound cash for years without continuously lowering returns?"
      mandate = "Judge whether long-duration growth is supported by productive reinvestment rather than ever-increasing capital consumption."
      required_tests = ["compare growth with capex and working-capital needs over time", "inspect whether margins and cash conversion remain durable through reinvestment", "test the transition from reinvestment to terminal steady state"]
      out_of_scope = ["current margin of safety assigned to Buffett", "upside optionality assigned to Wood", "shorter-term operating narrative assigned to Lynch"]
      decision_rule = "Support the long-term case only when reinvestment plausibly sustains growth without steadily degrading cash economics."
    },
    {
      id = "minervini", name = "Mark Minervini", group = "technical_quant"
      lens_name = "Risk control and early invalidation"
      plain_question = "What model-observable threshold would tell us the thesis is failing?"
      mandate = "Turn the frozen forecast into explicit early warning conditions and a disciplined loss-control interpretation."
      required_tests = ["identify the earliest projection inflection that would invalidate the case", "connect modeled downside with current price risk", "state concise thresholds observable in the supplied model"]
      out_of_scope = ["technical price or volume signals absent from the frozen packet", "macro regime analysis assigned to Soros", "accounting forensics assigned to Burry"]
      decision_rule = "Support the case only when it has clear model-based invalidation conditions and tolerable downside."
    },
    {
      id = "serenity", name = "Serenity", group = "ai_bottleneck"
      lens_name = "Bottleneck ownership and economics"
      plain_question = "Which scarce input constrains growth, and who captures the economics?"
      mandate = "Identify the binding constraint implied by the model and test whether the company actually owns the resulting economics."
      required_tests = ["locate the constraint implied by growth, capex, and working capital", "distinguish owning a bottleneck from merely paying for it", "test valuation sensitivity to bottleneck relief or migration"]
      out_of_scope = ["physical capacity scalability assigned to Jensen Huang", "ecosystem financing assigned to Altman", "execution cadence assigned to Musk"]
      decision_rule = "Abstain or require revision when the packet cannot identify both the bottleneck and the beneficiary of its economics."
    },
    {
      id = "jensen_huang", name = "Jensen Huang", group = "ai_bottleneck"
      lens_name = "Capacity scalability"
      plain_question = "Can physical and technical capacity scale fast enough without destroying margins?"
      mandate = "Stress the throughput, capacity ramp, and capital intensity required to deliver the modeled growth."
      required_tests = ["check growth against capex and D&A trajectories", "inspect capacity-ramp timing against margin expansion", "test whether terminal economics preserve realistic capacity intensity"]
      out_of_scope = ["bottleneck ownership assigned to Serenity", "ecosystem financing assigned to Altman", "management execution cadence assigned to Musk"]
      decision_rule = "Support the forecast only when the capacity ramp is internally consistent, timely, and cash-efficient."
    },
    {
      id = "altman", name = "Sam Altman", group = "ai_bottleneck"
      lens_name = "Ecosystem capacity and financing"
      plain_question = "What surrounding infrastructure and financing must exist for demand to become revenue?"
      mandate = "Test second-order infrastructure, timing, and funding dependencies implicit in the growth case."
      required_tests = ["identify growth that requires surrounding capacity not represented in direct capex", "inspect timing gaps between ecosystem investment and company cash generation", "test whether financing needs can weaken the modeled adoption loop"]
      out_of_scope = ["direct physical capacity assigned to Jensen Huang", "supply-chain execution assigned to Musk", "macro regime feedback assigned to Soros"]
      decision_rule = "Require revision when growth depends on material ecosystem investment or financing that the model leaves invisible."
    },
    {
      id = "musk", name = "Elon Musk", group = "ai_bottleneck"
      lens_name = "Execution, capex, and cash burn"
      plain_question = "Can management deliver the forecast on time and within the modeled cash budget?"
      mandate = "Stress execution cadence, build-out requirements, working capital, and cash consumption behind aggressive ramps."
      required_tests = ["inspect abrupt projection step changes and schedule compression", "connect revenue ramps with capex, D&A, and working capital", "identify cash-burn periods that threaten delivery before payoff"]
      out_of_scope = ["technical capacity limits assigned to Jensen Huang", "upside optionality assigned to Wood", "accounting classifications assigned to Burry"]
      decision_rule = "Require revision when the ramp lacks enough time, capital, working capital, or cash runway to be executable."
    },
    {
      id = "evidence_auditor", name = "Evidence Auditor", group = "valuation_review"
      lens_name = "Point-in-time source traceability"
      plain_question = "Can every material input be traced to the correct dated source, unit, and security?"
      mandate = "Audit whether the frozen packet semantically supports each material raw input as of the valuation date."
      required_tests = ["check issuer, security class, publication date, and valuation-date availability", "check currency, units, periods, and statement-line meaning", "distinguish retained raw sources from unsupported declarations or transient tool output"]
      out_of_scope = ["formula mechanics assigned to Valuation Reviewer", "assumption attractiveness assigned to Assumption Challenger", "investment recommendation assigned to the synthesis"]
      decision_rule = "Reject or abstain when a material raw input cannot be traced to an appropriate point-in-time source."
    },
    {
      id = "assumption_challenger", name = "Assumption Challenger", group = "valuation_review"
      lens_name = "Assumption coherence and concentration"
      plain_question = "Which few assumptions drive most of the valuation, and are they mutually consistent?"
      mandate = "Find valuation concentration and contradictions across growth, margins, reinvestment, taxes, discount rate, and terminal behavior."
      required_tests = ["inspect concentration in the WACC and terminal-growth grid", "check growth, margin, tax, capex, and working-capital assumptions together", "test whether the base case is coherent with its own transition to steady state"]
      out_of_scope = ["formula mechanics assigned to Valuation Reviewer", "source traceability assigned to Evidence Auditor", "macro causal narrative assigned to Soros"]
      decision_rule = "Require revision when value is concentrated in a fragile assumption or assumptions cannot all be true together."
    },
    {
      id = "valuation_reviewer", name = "Valuation Reviewer", group = "valuation_review"
      lens_name = "DCF mechanics and market-implied expectations"
      plain_question = "Are the DCF mechanics, terminal value, and market-implied expectations interpreted correctly?"
      mandate = "Review valuation-method applicability, formula relationships, terminal treatment, sensitivity behavior, and reverse-DCF interpretation."
      required_tests = ["inspect discounting, terminal-value, enterprise-to-equity, and per-share relationships", "check sensitivity direction and base-case placement", "compare the forecast with expectations implied by current price without inventing new inputs"]
      out_of_scope = ["accounting and bridge input quality assigned to Burry", "source traceability assigned to Evidence Auditor", "macro feedback assigned to Soros"]
      decision_rule = "Require revision or rejection for a material mechanical inconsistency or an unsupported interpretation of market-implied expectations."
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
  model_provider   = model_provider.primary
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
  collection_allowed_builtin_tools = ["bash", "powershell", "shell"]
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

  tool_use "calculate" {
    tool_id = starlark_tool.calculator.id
    input_from_agent = {
      code = {
        desc    = "A Starlark program that calculates the current derived DCF values."
        sources = []
      }
      data_json = {
        desc    = "A JSON string containing the raw inputs used by the Starlark program."
        sources = []
      }
    }
  }

  tool_use "pplx_finance_search" {
    tool_id = module.pplx_tools.pplx_finance_search_tool_id
    input_from_agent = {
      query = {
        desc    = "One concise financial-data question for the target company."
        sources = []
      }
    }
  }

  tool_use "pplx_pro_search" {
    tool_id = module.pplx_tools.pplx_pro_search_tool_id
    input_from_agent = {
      query = {
        desc    = "One concise public-source discovery query for the target company."
        sources = []
      }
    }
  }

  tool_use "pplx_fetch" {
    tool_id = module.pplx_tools.pplx_fetch_tool_id
    input = {
      artifact_dir = artifact("evidence").path
    }
    input_from_agent = {
      url = {
        desc    = "One HTTP or HTTPS source URL to fetch into the configured evidence directory."
        sources = []
      }
    }
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

research "static" "audit_dcf" {
  phase_mode       = "collection_only"
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt    = "You are the independent DCF auditor and repairer for secjury. Treat the upstream DCF as an untrusted candidate, not as established truth."
  prompt = <<-PROMPT
    Audit and, where necessary, repair the candidate DCF for target ${jsonencode(var.target)} as of ${var.valuation_date}. Do not merely describe defects: produce the corrected canonical dcf-model.v2 payload that the jurors will review. If a candidate value is already correct and adequately supported, preserve it exactly.

    Candidate DCF JSON:
    ${research.static.build_dcf.result}

    Treat the candidate's source declarations as leads, not as verified material. Independently retrieve and register every material raw source used by the canonical model. Use the registered yahoo-finance skill and the source tools below to verify existing inputs and to acquire missing or stale inputs. Prefer machine-readable market data and official exchange, regulator, issuer, and investor-relations documents.
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity artifact_dir for newly acquired audit material: %s", artifact("evidence").path) : format("Evidence directory for newly acquired audit material: %s", artifact("evidence").path)}
    All retained sources must have been public on or before ${var.valuation_date}. Save and register every newly used raw document or machine-readable response as an artifact; a transient tool or shell result is not an auditable input.

    Before auditing, call ${go_tool.update_dcf_progress.id} with a concise, company-specific audit plan whose steps all have status pending. Keep the complete plan current and record exact paths, values, units, periods, source IDs, URLs, artifact references or locators, formulas, calculations, assumptions, defects found, and repairs made. After any interruption or context compaction, read ${artifact("progress").path} before continuing. Submit only after every audit step is completed.

    Inspect at least these recurring DCF failure modes:
    - point-in-time source completeness: verify the issuer and security class, then check official annual reports, quarterly or interim reports, earnings announcements, exchange inquiries and responses, and other valuation-date disclosures instead of assuming an unavailable filing does not exist;
    - source traceability and normalization: verify dates, currency and units, statement line mappings, signs, period alignment, and registration of every raw input, especially machine-readable finance data;
    - unsupported smooth forecast paths: replace arbitrary revenue, margin, D&A, capex, and working-capital glide paths with operating drivers or explicitly justified assumptions, and do not disguise missing support by choosing a different smooth sequence;
    - immediate tax benefits in loss years: recognize tax shields only when loss carryforwards, taxable income, timing, and realizability support them;
    - terminal value before a demonstrated steady state: do not enter perpetuity immediately after the first positive UFCF year; demonstrate sustainable margins, reinvestment, growth, and cash conversion or extend the explicit transition;
    - working-capital balance versus period change: confirm that change_nwc is an incremental cash investment with the correct sign, not a working-capital balance or a revenue ratio mislabeled as a change;
    - WACC component build-up: reconcile the risk-free rate, beta, equity-risk and country/size/liquidity premiums, cost of debt, tax treatment, and capital-structure weights rather than inserting an unexplained scalar;
    - equity-value bridge: reconcile restricted cash, debt-like items, and diluted shares together with snapshot dates and units for cash, debt, leases, non-operating assets, forecast cash consumption, options, and other dilution; do not double-count cash burn already reflected in UFCF;
    - operating-driver stress tests: challenge recovery timing, revenue and margins, reinvestment, tax realization, terminal assumptions, cash runway, and dilution rather than relying only on a WACC/terminal-growth grid.

    A separate reverse-dcf.v1 artifact is mandatory. Do not change the evidence-supported base case merely to match the market price. Reverse DCF is a diagnostic of what the market price requires, not evidence that those expectations will occur.
    Use the same audited diluted shares, net debt, PV of explicit cash flows, WACC, terminal growth, and terminal discount period as the canonical model. With ${starlark_tool.calculator.id}, calculate market capitalization, market-implied enterprise value, the gap from base-model enterprise value, implied terminal FCF, implied final-year FCF, and implied FCF as a percentage of modeled final-year revenue. Then calculate at least three sustainable FCF-margin scenarios and the revenue scale each scenario requires relative to modeled final-year revenue.
    Investigate the optionality gap using point-in-time public sources. Separate candidate products, pipelines, capacity, or other drivers supported by sources from the commercial scale, margins, timing, financing, dilution, approvals, or market size that remain unproven. A source showing that a product exists or that a stock price moved does not prove it can fill the valuation gap.
    Call ${go_tool.submit_reverse_dcf.id} before submitting the canonical model. Its accepted artifact must state the market-implied enterprise value, implied terminal FCF, sustainable FCF-margin scenarios, and optionality gap in plain language.

    All derived numeric values must be calculated by calling ${starlark_tool.calculator.id}. Pass raw inputs through data_json and use result_json. Do not perform arithmetic, discounting, interpolation, averaging, tax, cash-flow, terminal-value, equity-bridge, per-share, implied-return, reverse-DCF, scenario, or sensitivity calculations mentally or outside the calculator. Recalculate every dependent value after a repaired input. If the calculator rejects a program, inspect the returned issue, correct the program or inputs, and retry.

    Preserve the exact candidate output contract: one object with exactly model and sources at the top level; model schema_version "dcf-model.v2" with exactly schema_version, company, valuation_date, assumptions, historical, projections, valuation, sensitivity; 3-5 historical periods; 5-10 projection periods; and an odd square WACC/terminal-growth sensitivity grid with the base case at its center. Every material raw input must be traceable to a stable source record. Never fabricate unavailable facts. When estimation is permitted, label the assumption explicitly and make its rationale auditable.

    Finish by calling ${go_tool.submit_dcf_model.id} with the complete corrected JSON object. Do not finish with an audit memo, prose, or a JSON code block; the accepted typed-tool call is the only valid completion.
  PROMPT
  collection_skill_directories     = ["${path.module}/skills"]
  collection_skills                = ["dcf-model", "yahoo-finance"]
  collection_allowed_builtin_tools = ["bash", "powershell", "shell"]
  tool_call_quota                  = local.pplx_tool_call_quota
  disallowed_tools                 = local.build_dcf_disallowed_tools
  permission                       = "approve_all"

  artifact "evidence" {
    type        = "directory"
    path        = "${block_wd()}/artifacts"
    description = "Independently retained public source material supporting the audited DCF."
    required    = true
    non_empty   = true
  }

  artifact "progress" {
    type        = "file"
    path        = "${block_wd()}/progress.json"
    description = "Monotonic DCF audit and repair checkpoint maintained only by update_dcf_progress."
    required    = true
    non_empty   = true
  }

  artifact "combined" {
    type        = "file"
    path        = "${block_wd()}/modeling/dcf-output.json"
    description = "Canonical audited dcf-model.v2 payload containing model and sources."
    required    = true
    non_empty   = true
  }

  artifact "model" {
    type        = "file"
    path        = "${block_wd()}/modeling/dcf-model.json"
    description = "Canonical audited DCF model reviewed by every juror."
    required    = true
    non_empty   = true
  }

  artifact "sources" {
    type        = "file"
    path        = "${block_wd()}/modeling/dcf-sources.json"
    description = "Canonical source declarations after DCF audit and repair."
    required    = true
    non_empty   = true
  }

  artifact "reverse_dcf" {
    type        = "file"
    path        = "${block_wd()}/modeling/reverse-dcf.json"
    description = "Structured market-implied expectations and optionality gap calculated from the audited DCF."
    required    = true
    non_empty   = true
  }

  tool_use "calculate" {
    tool_id = starlark_tool.calculator.id
    input_from_agent = {
      code = {
        desc    = "A Starlark program that audits or recalculates the current DCF values."
        sources = []
      }
      data_json = {
        desc = "A JSON string containing the candidate values and raw inputs used by the Starlark program."
        sources = [
          artifact("evidence"),
          artifact("progress"),
        ]
      }
    }
  }

  tool_use "pplx_finance_search" {
    tool_id = module.pplx_tools.pplx_finance_search_tool_id
    input_from_agent = {
      query = {
        desc    = "One concise financial-data verification question for the target company."
        sources = []
      }
    }
  }

  tool_use "pplx_pro_search" {
    tool_id = module.pplx_tools.pplx_pro_search_tool_id
    input_from_agent = {
      query = {
        desc    = "One concise public-source verification or discovery query for the target company."
        sources = []
      }
    }
  }

  tool_use "pplx_fetch" {
    tool_id = module.pplx_tools.pplx_fetch_tool_id
    input = {
      artifact_dir = artifact("evidence").path
    }
    input_from_agent = {
      url = {
        desc    = "One HTTP or HTTPS source URL to fetch into the audit evidence directory."
        sources = []
      }
    }
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
        desc = "The complete ordered DCF audit plan with findings, repairs, calculations, and accumulated source references."
        sources = [
          artifact("evidence"),
        ]
      }
    }
  }

  tool_use "submit_reverse_dcf" {
    tool_id = go_tool.submit_reverse_dcf.id
    input = {
      reverse_dcf_path = artifact("reverse_dcf").path
      schema_version   = "reverse-dcf.v1"
      valuation_date   = var.valuation_date
    }
    input_from_agent = {
      currency = {
        desc    = "Currency of the audited DCF and market price."
        sources = [artifact("evidence"), artifact("progress")]
      }
      monetary_unit = {
        desc    = "Scale for monetary values, such as millions; price remains currency per share."
        sources = [artifact("progress")]
      }
      market_snapshot = {
        desc    = "Point-in-time price, diluted shares, market cap, net debt, market-implied EV, and price source IDs. Net cash is negative net debt."
        sources = [artifact("evidence"), artifact("progress")]
      }
      base_case = {
        desc    = "Audited base-model EV, PV of explicit cash flows, per-share value, and final projection period revenue and UFCF, preserved without fitting them to price."
        sources = [artifact("progress")]
      }
      fixed_assumptions = {
        desc    = "Audited WACC, terminal growth, and terminal discount period held fixed for reverse DCF."
        sources = [artifact("progress")]
      }
      implied_expectations = {
        desc    = "Calculator-derived terminal FCF, final-year FCF, FCF versus modeled revenue, and market-to-base EV gap."
        sources = [artifact("progress")]
      }
      revenue_scenarios = {
        desc    = "At least three calculator-derived sustainable FCF-margin scenarios with required revenue, multiple of modeled revenue, and plain-language interpretation."
        sources = [artifact("progress")]
      }
      optionality = {
        desc    = "Unexplained EV plus structured candidate drivers. For each driver provide current evidence, current revenue contribution or not-disclosed status, commercial or development stage, source IDs, required scale or milestones, and an assessment against the valuation gap."
        sources = [artifact("evidence"), artifact("progress")]
      }
      conclusion = {
        desc    = "Plain-language answer to what operating future the current price requires and whether the frozen packet supports it."
        sources = [artifact("evidence"), artifact("progress")]
      }
      limitations = {
        desc    = "Limits of the reverse DCF, including that implied expectations are not forecasts or proof."
        sources = [artifact("evidence"), artifact("progress")]
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
      reverse_dcf_path = artifact("reverse_dcf").path
      target         = var.target
      valuation_date = var.valuation_date
    }
    input_from_agent = {
      model = {
        desc = "The complete canonical dcf-model.v2 model after independent audit and any required repair."
        sources = [
          artifact("evidence"),
          artifact("progress"),
        ]
      }
      sources = {
        desc = "Stable source records supporting every material raw input in the audited DCF."
        sources = [
          artifact("evidence"),
          artifact("progress"),
        ]
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
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = "You are one isolated valuation juror. Apply only the assigned analytical lens to the frozen DCF."
      prompt = <<-PROMPT
        Juror: ${juror.name} (${juror.id})
        Lens: ${juror.lens_name}
        Question this role represents: ${juror.plain_question}
        Mandate: ${juror.mandate}
        Required tests:
        - ${join("\n- ", juror.required_tests)}
        Out of scope for this role:
        - ${join("\n- ", juror.out_of_scope)}
        Decision rule: ${juror.decision_rule}

        Frozen DCF model JSON:
        ${research.static.audit_dcf.result}

        Reverse DCF artifact:
        ${research.static.audit_dcf.artifact.reverse_dcf.path}

        Shared jury protocol:
        - Stay within the assigned lens. Do not repeat categories assigned to another juror unless the overlap directly changes your verdict; when it does, state the dependency and leave the specialist's category to that role.
        - Apply every required test that the frozen packet supports. If the packet lacks information needed for a required test, make that limitation explicit and abstain where appropriate instead of inventing facts.
        - Every finding must cite exact JSON paths in model_paths. Use $.model for the audited DCF and $.reverse_dcf for market-implied expectations. Do not edit or recalculate either artifact, simulate another role, or introduce external facts.
        - Write the summary in plain language for a non-specialist reader. Explain the practical consequence instead of merely naming a finance concept.
        - Celebrity names are explanatory mnemonics only. Use the stated mandate, not attributed quotes or presumed beliefs, and do not claim that the real person participated, endorsed, or supplied facts.

        Finish by calling ${go_tool.submit_dcf_juror_opinion.id}. Do not finish with prose or a JSON code block.
      PROMPT
      import_artifact = {
        frozen_dcf = {
          desc    = "The immutable model, source declarations, and reverse DCF shared by every juror."
          sources = [research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf]
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
              sources = [research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf]
            }
            confidence = {
              desc    = "Juror confidence from 0 to 1; this is persona judgment, not statistical probability."
              sources = [research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf]
            }
            summary = {
              desc    = "Plain-language conclusion for a non-specialist reader that explains the practical consequence of this lens."
              sources = [research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf]
            }
            findings = {
              desc    = "Specific findings with severity, category, message, and exact model_paths."
              sources = [research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf]
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
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt    = "Synthesize the frozen DCF and submitted persona opinions. Never change model values or invent consensus."
  prompt = <<-PROMPT
    Frozen DCF: ${research.static.audit_dcf.result}
    Reverse DCF: ${research.static.audit_dcf.artifact.reverse_dcf.path}

    Submitted juror opinions:
    ${join("\n", [for task in research.dynamic.review_dcf.tasks : task.result])}

    Return a decision and report grounded only in the model, sources, reverse DCF, and opinions. Treat market-implied expectations as requirements embedded in price, not as a forecast or proof. Clearly distinguish model output from juror judgment. The decision must be accept, revise, or reject. Finish by calling ${go_tool.submit_dcf_report.id}; it renders the SecJury report structure to report.md without changing model values. Do not finish with prose or a JSON code block.
  PROMPT
  import_artifact "frozen_dcf" {
    desc    = "Frozen DCF model and sources."
    sources = [research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources]
  }
  import_artifact "reverse_dcf" {
    desc    = "Audited market-implied expectations, required revenue scale, and optionality gap."
    sources = [research.static.audit_dcf.artifact.reverse_dcf]
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
      model_path       = research.static.audit_dcf.artifact.model.path
      sources_path     = research.static.audit_dcf.artifact.sources.path
      reverse_dcf_path = research.static.audit_dcf.artifact.reverse_dcf.path
      opinion_paths    = [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion.path]
      jurors           = local.jurors
    }
    input_from_agent = {
      decision = {
        desc    = "Final decision: accept, revise, or reject."
        sources = concat([research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      headline = {
        desc    = "Short headline grounded in the frozen DCF and juror opinions."
        sources = concat([research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.reverse_dcf], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      summary = {
        desc    = "Synthesis that distinguishes model output from juror judgment."
        sources = concat([research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.reverse_dcf], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      key_findings = {
        desc    = "Decision-relevant findings supported by the frozen DCF or submitted opinions."
        sources = concat([research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.reverse_dcf], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
      limitations = {
        desc    = "Material limitations preserved from the model and jury review."
        sources = concat([research.static.audit_dcf.artifact.model, research.static.audit_dcf.artifact.sources, research.static.audit_dcf.artifact.reverse_dcf], [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion])
      }
    }
  }
}

output "dcf_model_path" {
  description = "Frozen DCF model reviewed by all jurors."
  value       = research.static.audit_dcf.artifact.model.path
}

output "jury_opinion_paths" {
  description = "Structured opinions produced by the parallel dynamic tasks."
  value       = [for task in research.dynamic.review_dcf.tasks : task.artifact.opinion.path]
}

output "report_path" {
  description = "Final synthesized Markdown report."
  value       = research.static.synthesize.artifact.report.path
}
