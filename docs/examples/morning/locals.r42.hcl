locals {
  edition_date = var.edition_date == null ? formatdate(
    "YYYY-MM-DD",
    local_timestamp(),
  ) : var.edition_date

  previous_date = formatdate(
    "YYYY-MM-DD",
    timeadd("${local.edition_date}T00:00:00Z", "-24h"),
  )

  cutoff_label = var.enforce_cutoff_time ? "${local.previous_date} 至 ${local.edition_date}；当日截止 ${var.cutoff_time}" : "${local.previous_date} 至 ${local.edition_date} 全天（未启用 cutoff_time）"

  date_scope_guidance = var.enforce_cutoff_time ? format(
    "信息日期范围固定为 %s 全天及 %s 当天截至 %s；前一天允许全天信息，当天只使用不晚于 cutoff_time 的信息，cutoff_time 的时区规则适用于 %s；不得使用其他日期的信息。",
    local.previous_date,
    local.edition_date,
    var.cutoff_time,
    local.edition_date,
  ) : format(
    "信息日期范围固定为 %s 和 %s；不启用 cutoff_time 时允许使用这两天任意时间发布或观测的信息，但不得使用其他日期的信息。",
    local.previous_date,
    local.edition_date,
  )

  gather_tracks = [
    {
      id        = "overnight-market"
      use_jin10 = true
      question = "隔夜海外市场、外汇、商品和航运如何变化，它们对中国投资者今天的开盘环境意味着什么？"
      instructions = <<-PROMPT
        按下面的 focus_topics 逐项检查隔夜市场方向，并记录数值、涨跌、数据时间、
        交易日及原始来源：
        ${local.focus_topic_guidance["overnight-market"]}
        另外逐项检查观察清单中的 A 股指数、港股指数、港股中国互联网公司、
        美元/日元、道琼斯工业平均指数、日经225、德国 DAX、法国 CAC 40、
        英国富时100、欧洲斯托克50和波罗的海干散货指数：分别查询行情与
        ${var.enforce_cutoff_time ? "前一天至 edition_date 当日截止时间内" : "前一天和 edition_date 当天任意时间"}
        的新闻。商品和航运方向要同时关注价格、运价和供需/扰动消息；没有值得写的
        要闻时记录 no_material_news 和已检查范围；查询失败时记录 check_failed，不能
        伪装成没有新闻。发现符合标准的新闻必须进入候选池；最终由 freeze_packet 按
        配置的新闻上限选择。新增海外指数、外汇和 BDI 的行情即使不入选头条，也要在
        对应 coverage 与扫描 artifact 中如实保留，供事实包判断是否可用。
        行情扫描必须先使用 yahoo-finance；只有 Yahoo 无结果、报错或价格为空时，
        才按 quote_scan_guidance 回退到 Jin10。不要用新闻标题代替行情数据。
        Yahoo 结果必须保留观察时间、交易日和市场状态；A50 期指必须使用直接行情源，
        不得拿 ETF 代理冒充。金十报价和 K 线作为回退时应保留完整 structuredContent，
        并如实记录每次尝试的结果。
      PROMPT
    },
    {
      id        = "macro-policy"
      use_jin10 = true
      question = "哪些宏观数据、央行动态和政策事件可能改变今天的风险偏好？"
      instructions = <<-PROMPT
        按下面的 focus_topics 逐项检查宏观、政策和公开观点：
        ${local.focus_topic_guidance["macro-policy"]}
        对机构和个人观点必须标明发言人、机构、发布时间和原文，不把单一观点写成市场共识。严格区分
        已经发生、已经宣布和市场预期；记录公布时间、前值/预期/实际值（可得时）
        以及政策原文或统计机构来源。
        用金十财经日历建立事件候选，但重要数据仍优先回到统计机构或央行原文。
        `open-discovery` 是一次有限的增量搜索：只保留既不属于观察清单、也不属于
        其他 focus_topics 的重大财经事件。它用于让每期早餐有 0-2 条真正的新线索，
        不得把已有主题换个关键词重复提交，也不得为了凑数编造或重复头条；没有合格
        增量时提交 no_material_news，并写清已检查范围。edition_date 是运行主机本地今天
        时，系统还会提供一次 `list_news({})` 最新资讯页；把这一页当作开放发现的
        补充候选，按日期和重要性筛选后交给 freeze_packet，不要继续翻页。
      PROMPT
    },
    {
      id        = "china-desk"
      use_jin10 = true
      question = "A 股开盘前，国内资金面、债券商品、公司公告和机构观点中有哪些真正需要交易台注意的变化？"
      instructions = <<-PROMPT
        按下面的 focus_topics 逐项检查国内市场、资金和公司信息：
        ${local.focus_topic_guidance["china-desk"]}
        金十快讯和资讯只用于发现；需要文章内容时先搜索拿到 id，再调用
        get_news 获取完整正文并注册 artifact。区分事实、机构判断与市场传言，不把
        单家机构观点写成市场共识，也不把新闻热度当成资金流。
      PROMPT
    },
    {
      id        = "industry-themes"
      use_jin10 = false
      question = "过去 24 小时有哪些真正可能形成交易线索的产业与公司事件？"
      instructions = <<-PROMPT
        按下面的 focus_topics 检查产业方向，同时保持开放，不为预设主题制造新闻：
        ${local.focus_topic_guidance["industry-themes"]}
        合并转载和重复通稿，区分事实、公司
        宣传和分析师推断。在原文明确支持、且引用包含足够上下文时，说明事件影响的
        是收入、成本、需求、供给或估值情绪；材料不足时只写已证实事实，不要强行
        补充影响维度。
        引用可以比最小支持片段更长，优先保留完整句或必要的相邻句，以保留主体、
        归属、时间、条件、单位和限定语，但不要把不相关段落塞进同一条 quote。
      PROMPT
    },
  ]

  watchlist_guidance = join("\n", [
    for item in var.watchlist : format(
      "- %s | %s | %s | quote symbols: %s | search terms: %s",
      item.id,
      item.name,
      item.kind,
      join(", ", item.quote_symbols),
      join(", ", item.search_terms),
    )
  ])

  focus_topic_guidance = {
    for track_id in ["overnight-market", "macro-policy", "china-desk", "industry-themes"] : track_id => join("\n", [
      for item in var.focus_topics : format(
        "- %s | %s | search terms: %s",
        item.id,
        item.name,
        join(", ", item.search_terms),
      ) if item.track == track_id
    ])
  }

  jin10_scan_tool_ids = {
    get_quote    = mcp_server.jin10.tool_ids["get_quote"]
    search_flash = mcp_server.jin10.tool_ids["search_flash"]
    list_news    = mcp_server.jin10.tool_ids["list_news"]
    search_news  = mcp_server.jin10.tool_ids["search_news"]
  }

  # The scan matrix is intentionally finite: one task and one initial acquisition
  # call per row, plus only the bounded cursor pagination described above.
  scan_matrix = flatten([
    for track in local.gather_tracks : concat(
      flatten([
        for topic in var.focus_topics : [
          for term_index, term in topic.search_terms : {
            id            = "${track.id}.${topic.id}.news.${term_index}"
            track_id      = track.id
            target_id     = topic.id
            target_name   = topic.name
            scan_type     = track.use_jin10 ? "search_news" : (var.use_pplx ? "pplx_search" : "web_search")
            tool_id       = track.use_jin10 ? local.jin10_scan_tool_ids.search_news : (var.use_pplx ? module.pplx_tools.pplx_pro_search_tool_id : "web_search")
            query         = term
            query_terms   = topic.search_terms
            arguments_json = track.use_jin10 ? jsonencode({ keyword = term }) : jsonencode({ query = term })
            use_jin10     = track.use_jin10
            use_pplx      = !track.use_jin10 && var.use_pplx
            quote_symbol  = ""
            question      = track.question
          }
        ] if topic.track == track.id
      ]),
      # On the live local-system date, take one latest-news page as a bounded
      # open-discovery supplement. Historical reruns remain deterministic and
      # use only their date-scoped keyword scans.
      track.id == "macro-policy" && local.edition_date == formatdate(
        "YYYY-MM-DD",
        local_timestamp(),
      ) && length([for topic in var.focus_topics : topic.id if topic.id == "open-discovery"]) > 0 ? [{
        id            = "${track.id}.open-discovery.latest-news"
        track_id      = track.id
        target_id     = "open-discovery"
        target_name   = "开放式增量要闻（最新资讯页）"
        scan_type     = "list_news"
        tool_id       = local.jin10_scan_tool_ids.list_news
        query         = "Jin10 latest news page"
        query_terms   = []
        arguments_json = jsonencode({})
        use_jin10     = true
        use_pplx      = false
        quote_symbol  = ""
        question      = track.question
      }] : [],
      track.id == "overnight-market" ? flatten([
        for item in var.watchlist : concat(
          [{
            id            = "${track.id}.${item.id}.quote"
            track_id      = track.id
            target_id     = item.id
            target_name   = item.name
            scan_type     = "get_quote"
            tool_id       = local.jin10_scan_tool_ids.get_quote
            query         = item.name
            query_terms   = item.search_terms
            arguments_json = jsonencode({ code = length(item.quote_symbols) > 0 ? item.quote_symbols[0] : "" })
            use_jin10     = true
            use_pplx      = false
            quote_symbol  = length(item.quote_symbols) > 0 ? item.quote_symbols[0] : ""
            question      = track.question
          }],
          [for term_index, term in item.search_terms : {
            id            = "${track.id}.${item.id}.news.${term_index}"
            track_id      = track.id
            target_id     = item.id
            target_name   = item.name
            scan_type     = "search_flash"
            tool_id       = local.jin10_scan_tool_ids.search_flash
            query         = term
            query_terms   = item.search_terms
            arguments_json = jsonencode({ keyword = term })
            use_jin10     = true
            use_pplx      = false
            quote_symbol  = ""
            question      = track.question
          }],
        )
      ]) : [],
    )
  ])

  review_roles = [
    {
      id    = "macro"
      title = "宏观校准员"
      remit = "检查宏观传导链、时间顺序和已发生/已宣布/预期的边界。"
    },
    {
      id    = "sentiment"
      title = "情绪观察员"
      remit = "根据已冻结的跨资产表现判断 risk-on、risk-off 或 mixed，并说明样本限制；不得编造恐慌贪婪指数。"
    },
    {
      id    = "strategy"
      title = "策略映射员"
      remit = "把事件映射为可能受益/承压方向、逻辑链和证伪条件，不给个性化买卖指令。"
    },
  ]

  pplx_tool_ids = var.use_pplx ? [
    module.pplx_tools.pplx_pro_search_tool_id,
    module.pplx_tools.pplx_fetch_tool_id,
  ] : []

  jin10_mcp_tool_ids = [
    mcp_server.jin10.tool_ids["get_quote"],
    mcp_server.jin10.tool_ids["get_kline"],
    mcp_server.jin10.tool_ids["list_flash"],
    mcp_server.jin10.tool_ids["search_flash"],
    mcp_server.jin10.tool_ids["list_news"],
    mcp_server.jin10.tool_ids["search_news"],
    mcp_server.jin10.tool_ids["get_news"],
    mcp_server.jin10.tool_ids["list_calendar"],
  ]

  jin10_source_guidance = join("\n", [
    "Use the native Jin10 MCP tools only for the assigned calls.",
    "Read structuredContent as the machine-readable result; content is supplementary.",
    "Every successful Jin10 query must be saved immediately as a separate Markdown",
    "source artifact under the declared Source artifact directory, before another",
    "acquisition call. Use a unique descriptive filename and a stable source identifier.",
    "Store get_quote/get_kline's complete data object (including code, name, time,",
    "open, close, high, low, volume, and ups fields; for K-lines include all klines)",
    "as a market-data artifact. Store list_flash/search_flash's complete data.items",
    "and pagination fields as a flash artifact. Store list_news/search_news's complete",
    "data.items and pagination fields as a news-index artifact.",
    format("For each keyword, retain at most the first %d returned news items in the frozen packet; do not treat this bounded sample as exhaustive.", var.news_items_per_keyword),
    "If one page has fewer than the target, inspect has_more and next_cursor.",
    "Only request the next page when has_more is true and next_cursor is non-empty;",
    "stop when the target is reached or when has_more is false/absent. Do not",
    "paginate merely to pad a short result.",
    "If an article is selected, store get_news's complete data including content as a news article artifact.",
    "Store list_calendar's complete data array as a calendar artifact. Store the",
    "complete result of r42_read_mcp_resource (such as quote://codes) as a resource",
    "snapshot artifact. The later evidence.json contains only atomic claims and exact",
    "quotes that point back to these registered source artifacts; never use an MCP",
    "transcript result as evidence without saving and registering it first.",
    "For list pagination use cursor, data.next_cursor, and data.has_more. Treat isError",
    "and JSON-RPC errors as failures; do not present a failed call as no material news.",
    "If get_quote rejects a code as unsupported, call r42_read_mcp_resource once",
    "with the quote://codes resource_id listed in that tool's schema, choose a",
    "matching supported code, and retry get_quote once. Do not retry any other",
    "error or call the resource reader for search/news failures.",
  ])

  quote_scan_guidance = join("\n", [
    "行情扫描必须按以下顺序执行，并保存每次尝试的完整 JSON 结果：",
    "1) 先用 yahoo-finance skill 的 yf quote 命令查询原始代码；Yahoo 返回有效价格时立即采用，不再调用其他行情源。",
    "2) Yahoo 无结果、报错或价格为 null 时，调用 Jin10 get_quote 查询同一代码。",
    "3) 若 Jin10 明确报告代码不支持，调用 r42_read_mcp_resource 一次读取 quote://codes；找到匹配代码后用 Jin10 get_quote 重试一次。",
    "4) 若仍没有可用结果，调用 web_search 一次搜索该品种的官方或行情代码；不得搜索新闻代替代码查询。",
    "5) 若 web_search 找到新代码，从第 1 步重新开始：先用 Yahoo 查询新代码；Yahoo 失败后再用 Jin10 查询；若 Jin10 再次报告代码不支持，只允许再读取一次 quote://codes 并重试一次 Jin10。",
    "所有步骤达到上限仍失败时提交 unavailable；不得猜测代码、调用其他行情工具或继续搜索。",
  ])

  fetch_tool_quota = var.fetch_tool_call_quota == null ? {} : (
    var.use_pplx ? {
      (module.pplx_tools.pplx_fetch_tool_id) = var.fetch_tool_call_quota
      } : {
      web_fetch = var.fetch_tool_call_quota
    }
  )

  source_tool_guidance = var.use_pplx ? join("\n", [
    "Use ${module.pplx_tools.pplx_pro_search_tool_id} for discovery and",
    "${module.pplx_tools.pplx_fetch_tool_id} to retain selected sources.",
    "Pass the declared source artifact directory to every fetch, then register",
    "each returned artifact_path with r42_register_artifact. Do not save it again.",
    ]) : join("\n", [
    "Use web_search for discovery and web_fetch to read selected sources.",
    "Save each complete retained source with r42_save_artifact under the declared",
    "source artifact directory, passing its URL as source; use the returned artifact_id.",
  ])

  gather_disallowed_tools = var.use_pplx ? ["web_search", "web_fetch", "bash", "powershell", "shell"] : ["bash", "powershell", "shell"]
  closed_disallowed_tools = ["web_search", "web_fetch", "bash", "powershell", "shell", "edit", "task", "ask_user"]
}
