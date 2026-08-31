variable "edition_date" {
  type        = string
  description = "财经早餐对应的 YYYY-MM-DD 日期；为空时使用运行主机系统时区的当前日期，新闻和行情默认覆盖该日期前一天及当天。"
  default     = null

  validation {
    condition = var.edition_date == null ? true : can(
      regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}$", var.edition_date)
    )
    error_message = "edition_date must use YYYY-MM-DD."
  }
}

variable "cutoff_time" {
  type        = string
  description = "本期早餐的信息截点，必须同时说明时间和时区。"
  default     = "07:30 Asia/Shanghai"

  validation {
    condition     = length(trimspace(var.cutoff_time)) > 0
    error_message = "cutoff_time must not be empty."
  }
}

variable "enforce_cutoff_time" {
  type        = bool
  description = "是否在 edition_date 当天严格拒绝 cutoff_time 之后的信息；前一天始终允许全天信息，关闭时当天也允许全天信息。"
  default     = true
}

variable "news_items_per_keyword" {
  type        = number
  description = "每个新闻或快讯关键词最多保留的返回条数；不足时仅在分页仍有下一页时继续获取，默认 6 条。"
  default     = 6

  validation {
    condition     = var.news_items_per_keyword >= 1 && floor(var.news_items_per_keyword) == var.news_items_per_keyword
    error_message = "news_items_per_keyword must be a positive integer."
  }
}

variable "morning_news_limit" {
  type        = number
  description = "freeze_packet 和 publisher 最多选入财经早餐的新闻条数；候选不足时按实际候选数量写入，默认 15 条。"
  default     = 15

  validation {
    condition     = var.morning_news_limit >= 1 && floor(var.morning_news_limit) == var.morning_news_limit
    error_message = "morning_news_limit must be a positive integer."
  }
}

variable "uncovered_news_limit" {
  type        = number
  description = "每期从开放式增量要闻方向最多选入的、未被观察清单和既有焦点覆盖的新闻条数，默认 2 条。"
  default     = 2

  validation {
    condition     = var.uncovered_news_limit >= 0 && floor(var.uncovered_news_limit) == var.uncovered_news_limit
    error_message = "uncovered_news_limit must be a non-negative integer."
  }
}

variable "jin10_mcp_token_ref" {
  type        = string
  description = "Jin10 MCP Bearer Token 的环境变量名；与 jin10_mcp_token 互斥。"
  default     = "J10_API_KEY"
  nullable    = true
}

variable "jin10_mcp_token" {
  type        = string
  description = "可选的 Jin10 MCP Bearer Token；与 jin10_mcp_token_ref 互斥。不要把真实 Token 写入版本库。"
  default     = null
  nullable    = true
  sensitive   = true
}

variable "watchlist" {
  description = "必须逐项检查行情与新闻的观察清单。没有值得写的要闻时提交 no_material_news；用户可完整覆盖此默认列表。"
  type = list(object({
    id            = string
    name          = string
    kind          = string
    quote_symbols = list(string)
    search_terms  = list(string)
  }))
  default = [
    {
      id = "csi300"
      name = "沪深300"
      kind = "a_share_index"
      quote_symbols = ["000300.SS"]
      search_terms = ["沪深300", "CSI 300"]
    },
    {
      id = "star50"
      name = "科创50"
      kind = "a_share_index"
      quote_symbols = ["000688.SS"]
      search_terms = ["科创50", "科创板"]
    },
    {
      id = "chinext"
      name = "创业板指数"
      kind = "a_share_index"
      quote_symbols = ["399006.SZ"]
      search_terms = ["创业板指", "创业板指数"]
    },
    {
      id = "hsi"
      name = "恒生指数"
      kind = "hk_index"
      quote_symbols = ["^HSI"]
      search_terms = ["恒生指数", "恒生"]
    },
    {
      id = "hstech"
      name = "恒生科技指数"
      kind = "hk_index"
      quote_symbols = ["HSTECH.HK"]
      search_terms = ["恒生科技", "恒生科技指数"]
    },
    {
      id = "tencent"
      name = "腾讯控股"
      kind = "hk_internet_company"
      quote_symbols = ["0700.HK"]
      search_terms = ["腾讯", "腾讯控股"]
    },
    {
      id = "alibaba"
      name = "阿里巴巴"
      kind = "hk_internet_company"
      quote_symbols = ["9988.HK"]
      search_terms = ["阿里巴巴", "阿里"]
    },
    {
      id = "meituan"
      name = "美团"
      kind = "hk_internet_company"
      quote_symbols = ["3690.HK"]
      search_terms = ["美团"]
    },
    {
      id = "jd"
      name = "京东集团"
      kind = "hk_internet_company"
      quote_symbols = ["9618.HK"]
      search_terms = ["京东", "京东集团"]
    },
    {
      id = "xiaomi"
      name = "小米集团"
      kind = "hk_internet_company"
      quote_symbols = ["1810.HK"]
      search_terms = ["小米", "小米集团"]
    },
    {
      id = "netease"
      name = "网易"
      kind = "hk_internet_company"
      quote_symbols = ["9999.HK"]
      search_terms = ["网易"]
    },
    {
      id = "usdjpy"
      name = "美元/日元"
      kind = "fx"
      quote_symbols = ["JPY=X"]
      search_terms = ["美元/日元", "USD/JPY", "日元"]
    },
    {
      id = "dow-jones"
      name = "道琼斯工业平均指数"
      kind = "overseas_index"
      quote_symbols = ["^DJI"]
      search_terms = ["道琼斯", "道琼斯工业指数", "Dow Jones"]
    },
    {
      id = "nikkei"
      name = "日经225"
      kind = "overseas_index"
      quote_symbols = ["^N225"]
      search_terms = ["日经225", "日经指数", "日本股市"]
    },
    {
      id = "dax"
      name = "德国DAX"
      kind = "europe_index"
      quote_symbols = ["^GDAXI"]
      search_terms = ["德国DAX", "德国股市"]
    },
    {
      id = "cac40"
      name = "法国CAC 40"
      kind = "europe_index"
      quote_symbols = ["^FCHI"]
      search_terms = ["法国CAC 40", "法国股市"]
    },
    {
      id = "ftse100"
      name = "英国富时100"
      kind = "europe_index"
      quote_symbols = ["^FTSE"]
      search_terms = ["英国富时100", "英国股市"]
    },
    {
      id = "euro-stoxx50"
      name = "欧洲斯托克50"
      kind = "europe_index"
      quote_symbols = ["^STOXX50E"]
      search_terms = ["欧洲斯托克50", "Euro Stoxx 50", "欧洲股市"]
    },
    {
      id = "baltic-dry"
      name = "波罗的海干散货指数"
      kind = "shipping_index"
      quote_symbols = ["^BDI"]
      search_terms = ["波罗的海干散货指数", "BDI", "干散货运价"]
    },
  ]
}

variable "focus_topics" {
  description = "各采集支线要主动检查的研究方向。用户可增删或替换；每项使用稳定 id，并填写所属 track 和搜索词。"
  type = list(object({
    id           = string
    name         = string
    track        = string
    search_terms = list(string)
  }))
  default = [
    {
      id = "sp500"
      name = "S&P 500"
      track = "overnight-market"
      search_terms = ["S&P 500", "标普500"]
    },
    {
      id = "nasdaq"
      name = "Nasdaq"
      track = "overnight-market"
      search_terms = ["Nasdaq", "纳斯达克"]
    },
    {
      id = "china-adr"
      name = "中概股整体表现"
      track = "overnight-market"
      search_terms = ["中概股", "中国资产"]
    },
    {
      id = "a50"
      name = "A50 期指"
      track = "overnight-market"
      search_terms = ["A50 期指", "富时中国A50"]
    },
    {
      id = "usdcnh"
      name = "离岸人民币 USD/CNH"
      track = "overnight-market"
      search_terms = ["USD/CNH", "离岸人民币"]
    },
    {
      id = "gold"
      name = "黄金"
      track = "overnight-market"
      search_terms = ["现货黄金", "黄金期货"]
    },
    {
      id = "crude"
      name = "原油"
      track = "overnight-market"
      search_terms = ["WTI 原油", "布伦特原油"]
    },
    {
      id = "global-commodities"
      name = "全球大宗商品"
      track = "overnight-market"
      search_terms = ["黄金", "白银", "原油", "铜", "天然气", "铁矿石", "铝"]
    },
    {
      id = "shipping-freight"
      name = "航运与运价"
      track = "overnight-market"
      search_terms = ["航运", "集装箱运价", "波罗的海干散货指数", "BDI", "红海航运"]
    },
    {
      id = "fed-decision"
      name = "美联储议息会议与表态"
      track = "macro-policy"
      search_terms = ["美联储", "FOMC", "利率决议"]
    },
    {
      id = "us-nonfarm"
      name = "美国非农指标"
      track = "macro-policy"
      search_terms = ["非农", "非农就业"]
    },
    {
      id = "china-mof"
      name = "中国财政部"
      track = "macro-policy"
      search_terms = ["中国财政部", "财政政策"]
    },
    {
      id = "china-pboc"
      name = "中国人民银行"
      track = "macro-policy"
      search_terms = ["中国人民银行", "央行", "货币政策"]
    },
    {
      id = "major-central-banks"
      name = "其他主要央行"
      track = "macro-policy"
      search_terms = ["欧洲央行", "日本央行", "英国央行"]
    },
    {
      id = "institutional-bull-bear"
      name = "大型机构和投行公开多空观点"
      track = "macro-policy"
      search_terms = ["投行 看多", "投行 看空", "机构观点"]
    },
    {
      id = "financial-voices"
      name = "金融学者与市场大咖公开言论"
      track = "macro-policy"
      search_terms = ["金融学者", "市场大咖", "专家观点"]
    },
    {
      id = "open-discovery"
      name = "开放式增量要闻"
      track = "macro-policy"
      search_terms = ["全球财经要闻", "突发财经", "市场焦点"]
    },
    {
      id = "market-liquidity"
      name = "市场与资金面"
      track = "china-desk"
      search_terms = ["A股 资金面", "北向资金", "融资融券"]
    },
    {
      id = "bonds-rates"
      name = "债券与利率"
      track = "china-desk"
      search_terms = ["国债收益率", "债券", "利率"]
    },
    {
      id = "domestic-commodities"
      name = "国内大宗商品"
      track = "china-desk"
      search_terms = ["国内商品", "商品期货"]
    },
    {
      id = "company-announcements"
      name = "重要公司公告"
      track = "china-desk"
      search_terms = ["公司公告", "业绩预告", "重大事项"]
    },
    {
      id = "sell-side-consensus"
      name = "卖方一致预期与分歧"
      track = "china-desk"
      search_terms = ["卖方 一致预期", "分析师 分歧"]
    },
    {
      id = "ai-data-centers"
      name = "人工智能与数据中心"
      track = "industry-themes"
      search_terms = ["人工智能", "数据中心", "AI"]
    },
    {
      id = "chips"
      name = "芯片"
      track = "industry-themes"
      search_terms = ["芯片", "半导体"]
    },
    {
      id = "power-compute-infrastructure"
      name = "能耗与算力基础设施"
      track = "industry-themes"
      search_terms = ["算力基础设施", "电力", "能耗"]
    },
  ]
}

variable "model" {
  type        = string
  description = "采集、事实整理、复核角色和成稿使用的模型。"
  default     = "openai/gpt-5.5"
}

variable "qc_model" {
  type        = string
  description = "Collection QC 与 Final QC 使用的模型。"
  default     = "openai/gpt-5.5"
}

variable "final_qc_strictness" {
  type        = string
  description = "Final QC 严格度；morning 默认 `brief`，适合短篇财经简报，也可改为 `strict` 或 `balanced`。"
  default     = "brief"

  validation {
    condition     = contains(["strict", "balanced", "brief"], var.final_qc_strictness)
    error_message = "final_qc_strictness must be strict, balanced, or brief."
  }
}

variable "reasoning_effort" {
  type        = string
  description = "所有 Research 与 QC session 的 reasoning effort。"
  default     = "medium"
}

variable "use_pplx" {
  type        = bool
  description = "使用可选的 Perplexity 搜索/抓取工具代替内置 web_search/web_fetch。"
  default     = false
}

variable "fetch_tool_call_quota" {
  type        = number
  description = "每条采集支线允许的成功抓取次数；null 表示不设置工具配额。"
  default     = 12
  nullable    = true

  validation {
    condition = var.fetch_tool_call_quota == null ? true : (
      var.fetch_tool_call_quota >= 1 &&
      floor(var.fetch_tool_call_quota) == var.fetch_tool_call_quota
    )
    error_message = "fetch_tool_call_quota must be null or a positive integer."
  }
}

variable "model_provider" {
  description = "Research session 使用的 BYOK provider。"
  type = object({
    type             = optional(string, "openai")
    endpoint         = optional(string, "https://openrouter.ai/api/v1")
    wire_api         = optional(string, "completions")
    transport        = optional(string)
    headers          = optional(map(string))
    api_key          = optional(string)
    api_key_ref      = optional(string)
    bearer_token     = optional(string)
    bearer_token_ref = optional(string)
    retry = optional(object({
      lifecycle_retries    = optional(number, 3)
      model_call_retries   = optional(number, 6)
      interval_seconds     = optional(number, 2)
      max_interval_seconds = optional(number, 30)
      error_message_regex  = optional(list(string), [])
    }), {})
  })
}

variable "qc_model_provider" {
  description = "Collection QC 与 Final QC 使用的 BYOK provider。"
  type = object({
    type             = optional(string, "openai")
    endpoint         = optional(string, "https://openrouter.ai/api/v1")
    wire_api         = optional(string, "completions")
    transport        = optional(string)
    headers          = optional(map(string))
    api_key          = optional(string)
    api_key_ref      = optional(string)
    bearer_token     = optional(string)
    bearer_token_ref = optional(string)
    retry = optional(object({
      lifecycle_retries    = optional(number, 3)
      model_call_retries   = optional(number, 6)
      interval_seconds     = optional(number, 2)
      max_interval_seconds = optional(number, 30)
      error_message_regex  = optional(list(string), [])
    }), {})
  })
}
