module "pplx_tools" {
  source = "./modules/pplx_tools"
}

mcp_server "jin10" {
  tools = [
    "get_quote",
    "get_kline",
    "list_flash",
    "search_flash",
    "list_news",
    "search_news",
    "get_news",
    "list_calendar",
  ]
  resources = ["quote://codes"]

  timeout = "30s"

  http {
    url              = "https://mcp.jin10.com/mcp"
    bearer_token_ref = var.jin10_mcp_token_ref
    bearer_token     = var.jin10_mcp_token
  }
}

research "dynamic" "scan" {
  serial = false
  tasks = [
    for index, action in local.scan_matrix : {
      id               = action.id
      phase_mode       = "collection_only"
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      final_qc_strictness = var.final_qc_strictness
      system_prompt = <<-PROMPT
        You are one deterministic coverage-matrix collector for a Chinese-language
        financial breakfast. Each non-quote task has exactly one assigned keyword
        and acquisition action: make one initial call to its configured tool. Quote tasks use
        the bounded Yahoo-first fallback below instead; do not interpret the
        one-call rule as forbidding that listed fallback. Do not broaden a query
        or repeat a failed call. For a news or flash query, preserve at most
        ${var.news_items_per_keyword} returned items for this keyword. If a tool
        declares cursor pagination and the response has fewer items than the
        target, inspect has_more and next_cursor; request another page only when
        has_more is true and next_cursor is non-empty. Stop at the target or when
        the service reports no next page; do not blindly paginate to pad a short result.
        Quote tasks use the bounded Yahoo-first fallback and must begin with
        yahoo-finance, even though their configured Jin10 tool is the fallback.
        Do not call the configured Jin10 tool before the Yahoo step.
        ${action.scan_type == "get_quote" ? local.quote_scan_guidance : "For non-quote scans, no fallback search or retry is allowed."}
        ${local.date_scope_guidance}

        Save the complete structured result from every retained page and any useful source context with
        r42_save_artifact under the declared sources directory before finishing.
        For an MCP result, preserve structuredContent as the machine-readable
        body and keep result.content only as readable supplementation. If the
        result is empty, unavailable, or an error, do not invent facts: use
        no_material_news, unavailable, or check_failed and explain why.
        Calling r42_read_mcp_resource is allowed only in the quote recovery path,
        and its complete result must also be saved. If no matching code is found,
        continue to the single web_search code lookup or finish unavailable; do
        not guess a code.
        After saving, call the configured
        submit_morning_scan termination tool exactly once. Never call
        r42_collection_checkpoint or submit_morning_evidence.
      PROMPT
      prompt = <<-PROMPT
        财经早餐日期：${local.edition_date}
        信息范围：${local.cutoff_label}
        Action ID: ${action.id}
        Track ID: ${action.track_id}
        Target: ${action.target_name} (${action.target_id})
        Scan type: ${action.scan_type}
        Query: ${action.query}
        ${action.quote_symbol == "" ? "" : "Quote code: ${action.quote_symbol}"}
        Call arguments (JSON; use exactly these values): ${action.arguments_json}
        Assigned question: ${action.question}

        ${action.scan_type == "get_quote" ? local.quote_scan_guidance : (action.use_jin10 ? local.jin10_source_guidance : (action.use_pplx ? format("Call %s once with the assigned query and save its complete result.", action.tool_id) : "Use web_search for this one discovery call; do not call web_fetch."))}
        For this fixed scan, the assigned response (and, for a quote recovery,
        the explicitly permitted fallback responses) is the complete snapshot.
        Save every response directly. For non-quote scans, do not call web_fetch;
        quote recovery may use it only for the single code-discovery result.
        Source artifact directory: "${artifact("sources").path}"
        ${action.scan_type == "get_quote" ? format("The configured acquisition tool ID is the Jin10 fallback after Yahoo (and after any web_search code rediscovery): %s", action.tool_id) : format("Use the configured acquisition tool ID for this single initial call: %s", action.tool_id)}
      PROMPT
      collection_tool_ids = action.use_pplx ? [action.tool_id] : []
      collection_mcp_tool_ids = action.use_jin10 ? [action.tool_id] : []
      collection_mcp_resource_ids = action.use_jin10 ? [mcp_server.jin10.resource_ids["quote://codes"]] : []
      collection_skill_directories = ["${path.module}/skills"]
      collection_skills = ["yahoo-finance"]
      collection_allowed_builtin_tools = ["bash", "powershell", "shell", "web_search", "web_fetch"]
      disallowed_tools = [
        for tool in local.gather_disallowed_tools : tool
        if !contains(["bash", "powershell", "shell", "web_search", "web_fetch"], tool)
      ]
      permission = "approve_all"

      artifact = {
        sources = {
          type        = "directory"
          path        = "${block_wd()}/${index}/sources"
          description = "完整保留的 ${action.id} 扫描结果。"
        }
        scan = {
          type        = "file"
          path        = "${block_wd()}/${index}/scan.json"
          description = "${action.id} 的单次覆盖扫描状态与 artifact 引用。"
          required    = true
          non_empty   = true
        }
      }

      tool_use = {
        submit_scan = {
          tool_id   = go_tool.submit_morning_scan.id
          terminate = true
          input = {
            artifact_id        = artifact("scan").id
            _r42_artifact_path = artifact("scan").path
            action_id          = action.id
            track_id           = action.track_id
            target_id          = action.target_id
            scan_type          = action.scan_type
          }
          input_from_agent = {
            status = {
              desc    = "completed, no_material_news, check_failed, or unavailable."
              sources = [artifact("sources")]
            }
            summary = {
              desc    = "What the one assigned call returned and why it is or is not material."
              sources = [artifact("sources")]
            }
            source_artifact_ids = {
              desc    = "IDs returned by r42_save_artifact or r42_register_artifact for this scan."
              sources = []
            }
          }
        }
      }
    }
  ]
}

research "static" "freeze_packet" {
  phase_mode      = "research_only"
  model_provider  = model_provider.primary
  model           = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the breakfast desk editor. Convert validated evidence into one
    frozen packet before any narrative or strategy review begins. Deduplicate
    repeated headlines and syndicated copies. Preserve unavailable fields as
    unavailable; never guess a quote, index move, timestamp, or macro value.
    For every market_snapshot item with an observed quote, as_of is the date of
    the observed market data. When a quote is unavailable, use direction and
    as_of = unavailable explicitly. Never substitute the edition date,
    retrieval date, or 0001-01-01 for the actual quote date.
  PROMPT
  prompt = <<-PROMPT
    财经早餐日期：${local.edition_date}
    信息范围：${local.cutoff_label}

    Fixed coverage scan JSON:
    ${join("\n", [for item in research.dynamic.scan.tasks : item.result])}

    Scan artifact paths:
    ${join("\n", [for item in research.dynamic.scan.tasks : item.artifact.scan.path])}

    Required watchlist coverage:
    ${local.watchlist_guidance}

    Build a compact packet with exactly these market keys: sp500, nasdaq,
    china_adr, a50, usdcnh, gold, crude. A value may be unavailable, but its
    key must remain present with a sourced explanation. Events must use macro,
    policy, industry, or company categories and occurred, announced, or
    expected status. Deduplicate repeated headlines and select at most ${var.morning_news_limit}
    distinct, decision-relevant news events for the packet. If the supported
    candidate set is smaller, keep the smaller set rather than inventing filler.
    For every
    required watchlist object, preserve exactly one coverage row with its
    quote_status, news_status, checked_until, and summary. A no_material_news
    row is valid and must not be converted into a claim. A material_news_found
    row must retain all related events; do not discard them due to a headline
    count. Also retain
    an institutional_scan across market_liquidity, bonds_rates, commodities,
    company_announcements, and sell_side when evidence exists, plus
    calendar_events with previous / consensus / actual values left empty when
    unavailable rather than guessed. In each event summary, preserve a
    source-supported impact dimension (revenue, costs, demand, supply, or
    valuation/sentiment) when relevant; omit it when the evidence does not
    establish one.

    Whenever an imported scan result or evidence entry contains one or more
    source URLs, copy every source URL verbatim into that packet item's
    `source_urls` array. Preserve all URLs when deduplicating or merging
    headlines; never drop, rewrite, or invent a URL. These URLs remain in the
    frozen packet for the Publisher. The packet tool also maintains a root
    `source_urls` index from the host-supplied scan paths, which the Publisher
    can use as a complete source list.

    Call ${go_tool.submit_breakfast_packet.id}; r42 binds the packet artifact.
    Create evidence_catalog entries only for claims supported by the imported
    scan snapshots. Preserve exact source wording where available, mark your
    own interpretation as analysis or mixed, and never invent a claim when a
    scan is unavailable. Do not finish with prose or a code block.
  PROMPT
  import_artifact "coverage_scans" {
    desc    = "固定覆盖矩阵各项的一次性扫描结果与完整来源。"
    sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
  }
  disallowed_tools = local.closed_disallowed_tools
  permission       = "approve_all"

  artifact "packet" {
    type        = "file"
    path        = "${block_wd()}/breakfast-packet.json"
    description = "降噪、去重并冻结后的财经早餐事实包。"
    required    = true
    non_empty   = true
  }

  tool_use "submit_packet" {
    tool_id   = go_tool.submit_breakfast_packet.id
    terminate = true
    input = {
      artifact_id            = artifact("packet").id
      _r42_artifact_path     = ""
      edition_date           = local.edition_date
      cutoff_time            = local.cutoff_label
      # Scan manifests are not evidence-claim artifacts; semantic claims are
      # created here from the imported source snapshots.
      reviewed_artifacts     = []
      required_coverage      = var.watchlist
      source_paths            = flatten([for item in research.dynamic.scan.tasks : [for source in values(item.artifact) : source.path]])
    }
      input_from_agent = {
      market_snapshot = {
        desc    = "Seven required latest closed-market readings. Observed quotes use their actual date; unavailable quotes use direction=unavailable and as_of=unavailable. Copy every source URL into source_urls when present."
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      coverage = {
        desc    = "One preserved coverage row for every configured watchlist object; submit object_id, statuses, checked_until, summary, event_ids, and source_urls only. The typed tool fills name and kind from required_coverage; do not guess or override them. no_material_news is a valid checked result. Preserve every source URL in source_urls when present."
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      events = {
        desc    = "Deduplicated macro, policy, industry, and company events. Copy every source URL into source_urls when present and preserve all URLs across merged events."
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      noise_notes = {
        desc    = "What was discarded or merged during information-noise filtering."
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      institutional_scan = {
        desc    = "Evidence-linked market/liquidity, rates, commodities, announcements, and sell-side signals. Preserve every source URL in source_urls when present."
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      calendar_events = {
        desc    = "Scheduled events with time, importance, previous, consensus, actual, expected effect, and evidence IDs. Preserve every source URL in source_urls when present."
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
    evidence_catalog = {
      desc    = "Every upstream claim retained for review and publication. Copy every source URL into source_urls when present."
      sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
    }
    }
  }
}

research "static" "packet_editor" {
  phase_mode      = "research_only"
  model_provider  = model_provider.primary
  model           = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the breakfast packet correction editor. Read the frozen packet and
    its imported source artifacts. This is a closed correction pass: do not
    search, fetch, or acquire new evidence. Correct or delete only fields that
    are plainly inconsistent with the supplied evidence or the packet schema.
    Never invent a value, date, quote, event, or evidence ID. Preserve explicit
    unavailable values when the sources do not establish a value. Do not return
    an issue list; submit the corrected complete packet.
    Preserve every existing `source_urls` value from the frozen packet. If an
    imported source has a URL, keep that URL verbatim in the corresponding
    packet item; do not remove, rewrite, or invent URLs during correction.
  PROMPT
  prompt = <<-PROMPT
    财经早餐日期：${local.edition_date}
    信息范围：${local.cutoff_label}

    The previous freeze pass produced this candidate packet JSON:
    ${research.static.freeze_packet.result}

    Inspect the candidate and imported scan artifacts with the read-only r42 tools. Repair only
    clear mechanical or factual inconsistencies. In particular, an unavailable
    market reading must use direction=unavailable and as_of=unavailable; never
    use 0001-01-01 or the edition date as a placeholder. Remove a field rather
    than guessing when the evidence cannot support it.

    Call ${go_tool.submit_breakfast_packet.id} to write the corrected packet.
    The typed tool performs only structural and reference validation. Do not
    finish with prose or a code block.
  PROMPT
  import_artifact "coverage_scans" {
    desc    = "用于核对事实的固定覆盖扫描源。"
    sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
  }
  disallowed_tools = local.closed_disallowed_tools
  permission       = "approve_all"

  artifact "packet" {
    type        = "file"
    path        = "${block_wd()}/breakfast-packet.json"
    description = "修订后的财经早餐事实包。"
    required    = true
    non_empty   = true
  }

  tool_use "submit_packet" {
    tool_id   = go_tool.submit_breakfast_packet.id
    terminate = true
    input = {
      artifact_id        = artifact("packet").id
      _r42_artifact_path  = ""
      edition_date       = local.edition_date
      cutoff_time        = local.cutoff_label
      reviewed_artifacts = []
      required_coverage  = var.watchlist
      source_paths       = concat(
        [research.static.freeze_packet.artifact.packet.path],
        flatten([for item in research.dynamic.scan.tasks : [for source in values(item.artifact) : source.path]])
      )
    }
    input_from_agent = {
      market_snapshot = {
        desc    = "修订后的七个市场行情条目；不可用行情的 as_of 必须为 unavailable。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      coverage = {
        desc    = "每个观察清单对象一条 coverage 记录；只提交 object_id、状态、检查时间、摘要、event_ids 和 source_urls。name/kind 由 typed tool 根据 required_coverage 自动补齐，不要自行推断或修改。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      events = {
        desc    = "与证据一致的去重事件；删除无法证实的事件。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      noise_notes = {
        desc    = "修订后的降噪说明。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      institutional_scan = {
        desc    = "与证据一致的机构和资金面条目。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      calendar_events = {
        desc    = "与证据一致的日历事件；无法确认的值留空。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
      evidence_catalog = {
        desc    = "只保留有来源支持的 evidence catalog 条目。"
        sources = flatten([for item in research.dynamic.scan.tasks : values(item.artifact)])
      }
    }
  }
}

research "dynamic" "review" {
  serial = false
  tasks = [
    for index, role in local.review_roles : {
      id               = role.id
      phase_mode       = "research_only"
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      final_qc_strictness = var.final_qc_strictness
      system_prompt = <<-PROMPT
        You are the ${role.title}. Review one frozen financial-breakfast packet
        from your assigned perspective. You cannot fetch new facts, edit the
        packet, or recalculate missing data. Preserve uncertainty and identify
        a counterpoint for each material interpretation.
      PROMPT
      prompt = <<-PROMPT
        财经早餐日期：${local.edition_date}
        Role: ${role.id}
        Remit: ${role.remit}

        Frozen packet JSON:
        ${research.static.packet_editor.result}

        Produce two to five focused findings. Explain each finding in language
        an ordinary reader can understand. Reference only metric keys, event
        IDs, or evidence IDs present in the packet. For strategy findings,
        describe a logic chain, possible beneficiaries or pressure points, a
        confidence level, and a falsification condition. This is not personalized investment advice.

        Call ${go_tool.submit_breakfast_review.id}; r42 binds this review's
        artifact path and role. Do not finish with prose.
      PROMPT
      import_artifact = {
        packet = {
          desc    = "所有复核角色共享的不可变事实包。"
          sources = values(research.static.packet_editor.artifact)
        }
      }
      disallowed_tools = local.closed_disallowed_tools
      permission       = "approve_all"

      artifact = {
        review = {
          type        = "file"
          path        = "${block_wd()}/${index}/${role.id}/review.json"
          description = "${role.title}的结构化复核意见。"
          required    = true
          non_empty   = true
        }
      }

      tool_use = {
        submit_review = {
          tool_id   = go_tool.submit_breakfast_review.id
          terminate = true
          input = {
            artifact_id            = artifact("review").id
            _r42_artifact_path     = ""
            packet_path            = research.static.packet_editor.artifact.packet.path
            role                   = role.id
          }
          input_from_agent = {
            headline = {
              desc    = "One concise conclusion for this review role."
              sources = values(research.static.packet_editor.artifact)
            }
            findings = {
              desc    = "Evidence-linked findings, plain-language explanations, counterpoints, and falsification conditions."
              sources = values(research.static.packet_editor.artifact)
            }
          }
        }
      }

      qc = {
        model_provider   = model_provider.qc
        model            = var.qc_model
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = 3
        disallowed_tools = local.closed_disallowed_tools
        permission       = "approve_all"
        criteria = {
          role_discipline = "Judge whether the review stays within its assigned role and does not introduce facts outside the frozen packet."
          reasoning = "Check only material unsupported interpretations or deterministic market claims. A concise counterpoint or falsification condition is sufficient; do not require a deep-investment-research treatment of every finding."
        }
      }
    }
  ]
}

research "static" "news_digest" {
  phase_mode      = "collection_only"
  model_provider  = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the morning brief's news selection and source-reading desk. Work
    only from the frozen packet and validated reviews supplied below. Select
    the most useful packet events for the Publisher, up to the configured
    limit; do not search for new stories or add events outside the packet.
    For every selected event that has a source URL, call the built-in
    `web_fetch` once for each URL, save the complete result under the declared
    fetched-artifact directory with `r42_save_artifact`, and write a natural
    summary of at most three sentences. A failed fetch still requires the
    saved error/result artifact and a `fetch_failed` status. Events without a
    URL use `no_url` and must not trigger a fabricated link or fetch.
    Finish by calling the typed digest tool exactly once. This is a bounded
    collection pass without collection QC; the typed tool performs mechanical
    checks only.
  PROMPT
  prompt = <<-PROMPT
    财经早餐日期：${local.edition_date}
    信息范围：${local.cutoff_label}
    最多入选新闻数：${var.morning_news_limit}

    Frozen packet JSON:
    ${research.static.packet_editor.result}

    Validated review JSON:
    ${join("\n", [for item in research.dynamic.review.tasks : item.result])}

    Choose at most ${var.morning_news_limit} packet events that are genuinely
    useful for today's newspaper. Prefer material events with clear impact and
    source URLs; do not fill the limit when fewer events are useful. For every
    selected event, copy its event_id, headline, and all packet source_urls.
    If it has URLs, call web_fetch once per URL, save each complete response
    under "${artifact("fetched").path}", and put the returned artifact IDs in
    fetch_artifact_ids. Set status to fetched when the source was read, or
    fetch_failed when the fetch returned an error. If it has no URL, set
    status to no_url and leave source_urls and fetch_artifact_ids empty.
    Summaries must be no more than three sentences and must use only the
    selected packet event and fetched source material.

    Call ${go_tool.submit_morning_news_digest.id}. The host supplies the packet
    path, output artifact path, and item limit; submit only the selected items.
    Do not finish with prose or a code block.
  PROMPT
  import_artifact "packet" {
    desc    = "冻结后的财经早餐事实包，新闻选择只能来自这里。"
    sources = values(research.static.packet_editor.artifact)
  }
  import_artifact "reviews" {
    desc    = "已验证的三类复核结果，仅用于判断新闻重要性。"
    sources = flatten([for item in research.dynamic.review.tasks : values(item.artifact)])
  }
  collection_allowed_builtin_tools = ["web_fetch"]
  disallowed_tools = [
    for tool in local.closed_disallowed_tools : tool
    if tool != "web_fetch"
  ]
  permission = "approve_all"

  artifact "fetched" {
    type        = "directory"
    path        = "${block_wd()}/fetched"
    description = "入选新闻的完整 web_fetch 结果。"
  }
  artifact "digest" {
    type        = "file"
    path        = "${block_wd()}/news-digest.json"
    description = "供 Publisher 使用的入选新闻和正文摘要。"
    required    = true
    non_empty   = true
  }

  tool_use "submit_digest" {
    tool_id   = go_tool.submit_morning_news_digest.id
    terminate = true
    input = {
      artifact_id        = artifact("digest").id
      _r42_artifact_path = artifact("digest").path
      packet_path        = research.static.packet_editor.artifact.packet.path
      max_items          = var.morning_news_limit
    }
    input_from_agent = {
      items = {
        desc    = "入选 packet 新闻、来源 URL、抓取 artifact ID、状态和最多三句话摘要。"
        sources = concat(values(research.static.packet_editor.artifact), [artifact("fetched")])
      }
    }
  }
}

research "static" "publish" {
  phase_mode       = "research_only"
  model_provider   = model_provider.primary
  model            = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are a financial editor writing for an ordinary reader, not a terminal
    user or professional trader. Use short paragraphs and familiar words.
    Explain unavoidable terms at first use. Lead with the useful conclusion,
    then answer: what happened, why it matters, and what to watch.

    Use only the frozen packet and validated reviews. Do not add model-memory
    facts. Do not predict certainty, recommend a security, or imply guaranteed
    returns. Do not pad the report with generic disclaimers or boilerplate
    portfolio advice. If the evidence does not support a useful observation,
    omit it instead of inventing one. Write like a concise Bloomberg, Reuters,
    or Mitrade morning newspaper: prioritize the news, explain why it matters,
    and give a few conditional premarket observations in fluent Chinese.

    Do not write disclaimer-style sentences that distance the article from its
    own reporting. In particular, do not use formulations such as
    “不能替代……”, “不代表已经……”, “尚未确认/尚未证实……”,
    “更准确的说法是……”, or “只能提供……线索”, nor equivalent
    self-protective caveats. State the reported fact, current status, and
    practical market implication directly; when a qualification is not needed
    to understand the fact, omit it rather than explaining why the report is
    limited.
  PROMPT
  prompt = <<-PROMPT
    财经早餐日期：${local.edition_date}
    信息范围：${local.cutoff_label}

    Frozen packet JSON:
    ${research.static.packet_editor.result}

    The packet includes a `source_urls` array on data items when the imported
    material supplied URLs, plus a root `source_urls` index maintained by the
    packet tool. Treat those URLs as part of the source data visible to the
    Publisher; do not discard, rewrite, or invent them while selecting news or
    writing the article.

    A separate news digest has selected the stories for this edition and, when
    URLs were available, supplied fetched source text and a short summary:
    ${research.static.news_digest.result}
    Fetched source artifacts are under
    ${research.static.news_digest.artifact.fetched.path}. Use this digest as
    the news selection boundary. Do not search or fetch additional stories.

    Validated review JSON:
    ${join("\n", [for item in research.dynamic.review.tasks : item.result])}

    Write a complete 5-8 minute natural Chinese morning newspaper, not a form or a
    checklist. You may choose the exact headings, but normally include a short
    lead, overnight market numbers with their observation date, the most useful
    news, and a final section of conditional signals to watch before and after
    the open. Select and combine duplicate news; choose at most ${var.morning_news_limit}
    news items for the reader-facing article. If the packet contains fewer useful
    candidates, use fewer rather than adding filler. Do not list scan tasks.
    Every factual or analytical sentence must end with one provenance marker:
    <!-- r42:claim=<id> evidence=<id> -->. Use IDs that exist in the frozen
    packet or validated reviews. A sentence may cite multiple IDs by repeating
    claim= or evidence= fields. Headings and table separator lines need no
    marker, but all prose, bullets, table data, and captions do. Analysis may
    be natural and concise; cite the facts it is based on and do not turn a
    possibility into a certainty. Do not print a separate evidence list.

    The finished article must not contain disclaimer-style wording, including
    “不能替代……”, “不代表已经……”, “尚未确认/尚未证实……”,
    “更准确的说法是……”, “只能提供……线索”, “不构成投资建议”,
    or equivalent boilerplate. Write the underlying fact or market state
    directly and then, if supported, its concrete implication. Do not add a
    paragraph whose purpose is to limit the publisher's responsibility.

    Call ${go_tool.submit_morning_draft.id}. The host supplies all artifact
    paths; pass only the complete annotated Markdown as the markdown argument.
    The tool stores the annotated draft and provenance sidecar, then removes
    the private markers for the reader-facing morning.md. Do not provide
    artifact IDs or filesystem paths and do not call a deterministic renderer.
  PROMPT
  import_artifact "packet" {
    desc    = "最终报告唯一事实底稿。"
    sources = values(research.static.packet_editor.artifact)
  }
  import_artifact "reviews" {
    desc    = "宏观、情绪和策略三种独立复核。"
    sources = flatten([for item in research.dynamic.review.tasks : values(item.artifact)])
  }
  import_artifact "news_digest" {
    desc    = "入选新闻、三句话摘要和完整抓取来源。"
    sources = values(research.static.news_digest.artifact)
  }
  disallowed_tools = local.closed_disallowed_tools
  permission       = "approve_all"

  artifact "report_annotated" {
    type        = "file"
    path        = "${block_wd()}/morning-draft.annotated.md"
    description = "供 publisher_editor 修订的带出处财经早餐草稿。"
    required    = true
    non_empty   = true
  }
  artifact "report_provenance" {
    type        = "file"
    path        = "${block_wd()}/morning-provenance.json"
    description = "带出处句子的内部索引，不展示给读者。"
    required    = true
    non_empty   = true
  }
  artifact "report_markdown" {
    type        = "file"
    path        = "${block_wd()}/morning.md"
    description = "面向普通读者的财经早餐 Markdown。"
    required    = true
    non_empty   = true
  }

  tool_use "submit_report" {
    tool_id   = go_tool.submit_morning_draft.id
    terminate = true
    input = {
      annotated_artifact_id  = artifact("report_annotated").id
      provenance_artifact_id = artifact("report_provenance").id
      markdown_artifact_id   = artifact("report_markdown").id
      source_paths            = concat(
        [research.static.packet_editor.artifact.packet.path],
        [for item in research.dynamic.review.tasks : item.artifact.review.path],
      )
      _r42_annotated_path     = ""
      _r42_provenance_path    = ""
      _r42_markdown_path      = ""
      edition_date            = local.edition_date
    }
    input_from_agent = {
      markdown = {
        desc    = "完整的自然中文早报，所有事实、分析、表格数据和项目符号句末都要有 r42 provenance marker。"
        sources = concat(values(research.static.packet_editor.artifact), flatten([for item in research.dynamic.review.tasks : values(item.artifact)]))
      }
    }
  }

}

research "static" "publisher_editor" {
  phase_mode      = "research_only"
  model_provider  = model_provider.primary
  model           = var.model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You are the final public-edition correction editor for a short financial
    morning brief. Read the annotated draft, provenance sidecar, frozen packet,
    and validated reviews at the paths supplied in the task prompt. Do not
    search or acquire new evidence. Keep
    the author's natural newspaper style. Correct or delete only material
    numeric, date, unit, percentage, sign, market-direction, or provenance
    errors that are clear from the supplied sources. Never add a new fact or
     turn a plausible conditional analysis into a certainty. Preserve valid
     provenance markers on every factual or analytical sentence. Submit the
     corrected annotated Markdown; do not return an issue list.

    Remove or directly rewrite disclaimer-style sentences in the draft. Do not
    preserve formulations such as “不能替代……”, “不代表已经……”,
    “尚未确认/尚未证实……”, “更准确的说法是……”, or
    “只能提供……线索”, and do not introduce equivalent boilerplate,
    “不构成投资建议”, or self-protective caveats. Keep the underlying fact,
    status, and supported implication when they are useful; otherwise delete
    the sentence. This rule applies even when the draft already contains the
    wording.
  PROMPT
  prompt = <<-PROMPT
    财经早餐日期：${local.edition_date}
    信息范围：${local.cutoff_label}

    Read the publish draft at
    ${research.static.publish.artifact.report_annotated.path} and its provenance
    sidecar at ${research.static.publish.artifact.report_provenance.path} with
    the read-only r42 tools, then compare material statements with the corrected
    packet at ${research.static.packet_editor.artifact.packet.path} and the
    validated review files:
    ${join("\n", [for item in research.dynamic.review.tasks : item.artifact.review.path])}
    Delete a sentence when it cannot be repaired from those sources. Do not add
    免责声明、泛化投资建议或新的市场事实；对草稿中的免责声明式句子，
    必须直接改写为事实/状态/影响，或在无法保留原意时删除。禁止保留
    “不能替代……”“不代表已经……”“尚未确认/尚未证实……”“更准确的说法是……”
    “只能提供……线索”等措辞及其同义表达。

    The packet is the Publisher's source of record. Publisher receives
    `source_urls` from the packet; keep every URL available for provenance when
    editing, and never remove, rewrite, or invent one. The final reader-facing
    Markdown may continue to hide private provenance markers according to the
    existing output rules.

    Call ${go_tool.submit_morning_draft.id}. Pass the complete corrected
    annotated Markdown as `markdown`; the host supplies all artifact IDs and
    paths. The tool writes the final reader-facing morning.md after removing
    private provenance markers. Do not finish with prose or a code block.
  PROMPT
  disallowed_tools = local.closed_disallowed_tools
  permission       = "approve_all"

  artifact "report_annotated" {
    type        = "file"
    path        = "${block_wd()}/morning-draft.annotated.md"
    description = "经 publisher_editor 修订的带出处财经早餐。"
    required    = true
    non_empty   = true
  }
  artifact "report_provenance" {
    type        = "file"
    path        = "${block_wd()}/morning-provenance.json"
    description = "最终带出处句子的内部索引。"
    required    = true
    non_empty   = true
  }
  artifact "report_markdown" {
    type        = "file"
    path        = "${block_wd()}/morning.md"
    description = "最终面向普通读者的财经早餐 Markdown。"
    required    = true
    non_empty   = true
  }

  tool_use "submit_report" {
    tool_id   = go_tool.submit_morning_draft.id
    terminate = true
    input = {
      annotated_artifact_id  = artifact("report_annotated").id
      provenance_artifact_id = artifact("report_provenance").id
      markdown_artifact_id   = artifact("report_markdown").id
      source_paths           = concat(
        [research.static.packet_editor.artifact.packet.path],
        [for item in research.dynamic.review.tasks : item.artifact.review.path],
      )
      _r42_annotated_path  = ""
      _r42_provenance_path = ""
      _r42_markdown_path   = ""
      edition_date         = local.edition_date
    }
    input_from_agent = {
      markdown = {
        desc = "修订后的完整自然中文早报；所有事实、分析、表格数据和项目符号句末都要有 r42 provenance marker。"
        sources = []
      }
    }
  }
}

output "packet_path" {
  description = "降噪并冻结的财经早餐事实包。"
  value       = research.static.packet_editor.artifact.packet.path
}

output "review_paths" {
  description = "宏观、情绪、策略三种复核结果。"
  value       = [for item in research.dynamic.review.tasks : item.artifact.review.path]
}

output "news_digest_path" {
  description = "Publisher 使用的入选新闻和抓取摘要。"
  value       = research.static.news_digest.artifact.digest.path
}

output "report_path" {
  description = "最终面向普通读者的财经早餐 Markdown。"
  value       = research.static.publisher_editor.artifact.report_markdown.path
}
