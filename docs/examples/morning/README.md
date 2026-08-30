# 面向普通读者的财经早餐

这个例子把 `institute-one/workflows/briefing.json` 的“隔夜宏观 → 今日主线 →
编辑汇编”扩展成一条有证据闸门的 r42 工作流。它不是逐条堆新闻，而是先冻结事实，
再由一个封闭的修订阶段直接纠正或删除明显错误，之后做宏观、情绪和策略三种独立复核，
最后生成一篇普通读者能在 5-8 分钟读完的中文财经早餐。

```text
固定覆盖扫描矩阵（每个方向一次）
  ├─ focus_topics       宏观、市场、政策与产业关键词
  └─ watchlist          指数、商品与重点公司的行情/新闻
             │
             ▼
      dynamic scan tasks（只调用一次指定 MCP 工具）
             │
             ▼
       汇总并冻结 breakfast-packet.json
              │
       packet_editor
       （只改正或删除）
              │
      ┌───────┼────────┐
     ▼       ▼        ▼
   宏观复核  情绪复核  策略映射
     └───────┼────────┘
             ▼
    typed tool 校验证据引用与结构
              ▼
             publish
       （生成带出处初稿）
              │
       publisher_editor
       （只改正或删除）
              ▼
        morning-draft.annotated.md + morning-provenance.json + morning.md
```

## 为什么这样拆

- 参考 `deep-research`：外部信息只在 Collection 阶段获取，完整来源保存为 artifact；
  closed Research 只处理已授权扫描结果，原子 claim 与逐字 quote 双向关联。
- 参考 `chokepoint`：可由代码判定的市场覆盖、枚举、ID、日期、重复标题和引用存在性
  全部由 researcher terminate tool 一次校验；`packet_editor` 只在已授权材料上直接
  修订或删除，不再和 Final QC 反复往返。
- 参考 `secjury`：三位复核角色面对同一个冻结事实包并行工作，不能各自搜索补料或改变
  事实底稿，最终编辑必须保留反面解释和证伪条件。

## 报告结构

最终 Markdown 固定采用适合普通读者的阅读顺序：

1. 今早先看三件事；
2. 最新已收盘行情，明确数据日期，避免与晨报发布日期的盘中走势混淆；
3. 5–7 条今晨大事，结构上保留“事实、影响、观察”，正文则写成自然的中文短段落；材料不足时宁可少写，不用重复或次要消息凑数；
4. 今日主线与市场影响，以及盘前观察清单，明确触发条件、传导链、影响方向、确认信号、失效条件、期限和置信度；
5. 机构信息扫描，覆盖市场与资金面、债券利率、商品、航运与运价、公司公告和机构分歧；
6. 今日事件表与上一交易时段的市场信号；
7. 只保留与本期数据直接相关的局限，不生成固定免责声明或泛化投资提醒。

在复核之后，`news_digest` 会从事实包中选出本期要写的新闻。对有来源 URL 的入选新闻，
它逐条调用 `web_fetch`，保存完整正文并提炼最多三句话，Publisher 只使用这份选题和摘要，
不再临时搜索或抓取。

Publisher 直接写一份自然中文 Markdown，并在每句末尾暂时附上
`<!-- r42:claim=<id> evidence=<id> -->` 出处标记。`submit_morning_draft` 会保留带标记的
内部草稿和 `morning-provenance.json`，再自动生成不含标记的读者版 `morning.md`；它不
负责替模型写文章或拼接固定章节。`publisher_editor` 在同一批授权材料上修订初稿，
读者不会看到出处标记。
扫描结果中的 HTTP(S) 来源 URL 会由 `submit_breakfast_packet` 自动汇总到事实包根部的
`source_urls`，并在有明确对应条目时保留在条目的同名字段；`packet_editor` 不会丢弃这些
URL，因此 Publisher 始终能看到原始链接。
扫描任务未生成的可选 `sources` 目录会被 packet 工具跳过；coverage 只需提交
`object_id` 和检查结果，`name`、`kind` 始终由 `required_coverage` 的 canonical 值自动补齐。
专业术语应在第一次出现时解释，避免只有交易员才看得懂的数据堆砌。写作原则是：
**正文讲因果，表格放数字，盘前线索写条件**。盘前观察给交易员提供开盘前需要验证的
假设，但不输出无条件买卖指令。

## 数据源和研究员权限

- 行情矩阵项先调用受限的 `skills/yahoo-finance`；Yahoo 无可用结果时才回退到 Jin10。
  Jin10 报代码不支持时读取一次 `quote://codes` 重试；仍失败才允许一次 `web_search`
  查代码，找到新代码后必须重新从 Yahoo 开始。整个回退链的每次结果都要保存。
- `overnight-market`、`macro-policy` 和 `china-desk` 可以调用 Jin10 MCP；产业研究员
  仍以完整原文和公司/交易所来源为主，避免所有支线重复刷同一快讯流。
- Yahoo 不能替代 A50 期指、国内期货、官方宏观数据或公司公告；A50 必须使用直接行情源，
  不得用 ETF 代理。行情回退链之外的研究仍优先交易所、央行、统计机构和公司公告。
- 每个矩阵项只执行一次预定的采集工具调用；不翻页、不扩展关键词，也不因 QC 反复补搜。
  Jin10 结果必须先用 `r42_save_artifact` 保存完整 `structuredContent`，再提交扫描状态。

## 运行

安装 r42 并确保本地 GitHub Copilot CLI 已登录。创建一个不会提交到 Git 的
`morning.r42vars`：

```hcl
cutoff_time  = "07:30 Asia/Shanghai"
# 默认使用前一天全天及 edition_date 当天截至 cutoff_time 的信息；设为 false 时当天也允许全天信息。
# enforce_cutoff_time = false
news_items_per_keyword = 6
morning_news_limit     = 15
# 开放式增量要闻最多选入 1-2 条；没有合格事件时可以为 0 条。
uncovered_news_limit   = 2
model        = "openai/gpt-5.5"
qc_model     = "openai/gpt-5.5"
# morning 是短篇财经简报，默认使用 brief；也可改为 strict 或 balanced。
final_qc_strictness = "brief"

model_provider = {
  api_key_ref = "OPENROUTER_API_KEY"
}

qc_model_provider = {
  api_key_ref = "OPENROUTER_QC_API_KEY"
}

# Jin10 默认读取当前环境中的 J10_API_KEY；也可在未提交的变量文件中直接提供 Token。
# jin10_mcp_token_ref = "J10_API_KEY"
# jin10_mcp_token     = null
# 直接提供 Token 时改为：jin10_mcp_token_ref = null，jin10_mcp_token = "<your token>"

# 可选：覆盖默认观察清单。每项都必须检查行情和新闻；没有值得写的新闻时
# 记录 no_material_news，而不是省略该项。
# watchlist = [
#   {
#     id = "csi300"
#     name = "沪深300"
#     kind = "a_share_index"
#     quote_symbols = ["000300.SS"]
#     search_terms = ["沪深300", "CSI 300"]
#   }
# ]

# 可选：覆盖各采集支线的研究方向。id 应保持稳定，track 必须是四条采集支线之一。
# focus_topics = [
#   {
#     id = "fed-decision"
#     name = "美联储议息会议与表态"
#     track = "macro-policy"
#     search_terms = ["美联储", "FOMC", "利率决议"]
#   }
# ]
```

行情快照有报价时，`market_snapshot.as_of` 必须是实际观测日期；明确没有报价时必须写
`direction = "unavailable"` 且 `as_of = "unavailable"`，不得使用 `0001-01-01` 或发布日期
充当占位日期。事件的 `as_of` 仍必须是真实日期。

`edition_date` 默认为 `null`；未显式设置时，示例通过 r42 的 `local_timestamp()`
读取运行主机系统时区下的当前日期。需要补跑或重现历史版本时，可在变量文件中用
`YYYY-MM-DD` 显式覆盖。

新闻扫描默认覆盖 edition_date 的前一天和当天；启用 `enforce_cutoff_time` 时，再从这两天
前一天允许全天信息，当天筛选不晚于 cutoff_time 的结果。每个关键词最多保留 `news_items_per_keyword` 条，
只有服务声明支持分页且返回 `has_more = true`、`next_cursor` 非空时才继续翻页；没有下一页
时不为凑数反复查询。`freeze_packet` 和 Publisher 最多使用 `morning_news_limit` 条不同
新闻，候选不足时按实际数量写入。

Final QC 在本例中保持收敛且宽松：只核对带出处草稿里的数字、日期、单位、百分比、
涨跌方向等是否与材料一致，以及每句正文是否有合法出处。它不评判新闻覆盖是否充分，
不挑剔文风，也不要求分析写成严格演绎证明；只要分析指向相关材料且没有明显矛盾即可。
首次 QC 会一次列出全部问题并分配稳定 ID，后续轮次只能复查这些 ID，不得不断新增问题。
`final_qc_strictness` 仍可由通用研究配置覆盖，但不要把更严格的自定义标准用于本例。

然后从仓库根目录规划和运行：

```powershell
r42 init ./docs/examples/morning
r42 plan `
  -var-file ./docs/examples/morning/morning.r42vars `
  --out ./morning.r42plan
r42 apply ./morning.r42plan --parallelism 6
```

默认使用 r42 内置的 `web_search` / `web_fetch`。要改用示例内复制的 Perplexity
搜索和抓取工具，在变量文件加入 `use_pplx = true`，并设置 `PPLX_API_KEY`。密钥只放
环境变量，不写进 HCL 或 plan。

Jin10 通过原生 `mcp_server` 接入，由 GitHub Copilot SDK 负责协议协商和工具调用。
示例同时声明了 `quote://codes` MCP resource；使用 Jin10 的 Collection task 会自动获得
受限 typed tool `r42_read_mcp_resource`。如果 `get_quote` 明确返回代码不支持，扫描员只能
读取一次官方代码表，换用匹配代码重试一次；其他错误不重试。
采集阶段优先读取结构化结果，文本 `content` 只作补充。运行前在当前 shell 设置
`J10_API_KEY`；HCL 中只保存环境变量名。

认证参数也可以通过变量覆盖：`jin10_mcp_token_ref` 默认值为 `J10_API_KEY`，
`jin10_mcp_token` 是标记为 sensitive 的可选直接 Token。通常只配置环境变量引用即可；
如果运行环境不能提供环境变量，可在未提交的变量文件中设置 `jin10_mcp_token`，不要把
它写入版本库或共享 plan 文件。两个变量是互斥的，不能同时设置。

按照[金十数据智能开放平台接入指南](https://mcp.jin10.com/app/doc.html)获取 Token：

1. 在金十 MCP 首页点击“立即体验”，登录金十数据账号。登录成功后，该按钮会变成
   “管理TOKEN”。
2. 进入 Token 管理页面，点击“激活”申请 MCP Token。
3. 激活成功后复制 Token。金十文档称它为 MCP Token，本例将它保存在
   `J10_API_KEY` 环境变量中。

只在当前 PowerShell 窗口运行本例时，可以临时设置：

```powershell
$env:J10_API_KEY = '<your Jin10 token>'
```

要保存到当前 Windows 用户，且避免把 Token 明文写进 PowerShell 命令历史，可以使用
隐藏输入；下面的命令也会同步设置当前 PowerShell 进程，因此无需重开终端：

```powershell
$secureToken = Read-Host 'Paste Jin10 MCP Token' -AsSecureString
$plainToken = [System.Net.NetworkCredential]::new('', $secureToken).Password
[Environment]::SetEnvironmentVariable('J10_API_KEY', $plainToken, 'User')
$env:J10_API_KEY = $plainToken
Remove-Variable secureToken, plainToken
```

运行前可检查变量是否存在，而不打印 Token：

```powershell
if ([string]::IsNullOrWhiteSpace($env:J10_API_KEY)) {
  throw 'J10_API_KEY is not configured'
}
```

随后在同一个 PowerShell 窗口执行本节前面的 `r42 init`、`r42 plan` 和
`r42 apply` 命令。新开的终端会自动读取用户级 `J10_API_KEY`。

列表翻页统一使用请求字段 `cursor`，并读取响应里的 `data.next_cursor` 与
`data.has_more`。选入证据的结构化结果必须通过 `r42_save_artifact` 持久化；服务端仍会
执行北京时间自然日的额度限制。

### 观察清单与“没有要闻”

默认观察清单包括沪深 300、科创 50、创业板指数、恒生指数、恒生科技指数，以及腾讯、
阿里巴巴、美团、京东、小米、网易；还包括美元/日元、道琼斯工业平均指数、日经225、
德国 DAX、法国 CAC 40、英国富时100、欧洲斯托克50和波罗的海干散货指数（BDI）。
它是 `var.watchlist`，用户可以在变量文件中完整替换，例如加入行业 ETF、其他指数或删去
不需要的公司。每个清单对象都要完成行情检查和新闻检查，但不要求每天都有可写内容：

宏观支线另外固定检查美联储议息会议、非农、中国财政部和中国人民银行的公开表态，
并搜索大型机构/投行的公开多空观点及金融大咖言论。这些主题不是每天都必须产生事件，
但研究员必须完成搜索并在证据中说明“没有达到晨报标准”或“检查失败”。

- `material_news_found`：发现值得纳入早餐的消息，必须关联 claim（在 packet 中关联 event），
  不能因为头条数量限制丢弃。
- `no_material_news`：已完成搜索但没有达到晨报标准的要闻，可以没有 claim/event；必须在
  `summary` 写明检查范围和截点。
- `check_failed`：工具、代码或来源不可用，必须说明原因，不能冒充“没有新闻”。

扫描任务的次数和范围在 plan 阶段由 `local.scan_matrix` 固定；扫描完成后不会再要求补充搜索。
每个新闻/快讯关键词独立搜索，默认最多保留前 6 条（由 `news_items_per_keyword` 调整），
商品方向覆盖黄金、白银、原油、铜、天然气、铁矿石和铝，航运方向覆盖集装箱运价、BDI、
干散货和红海航运。另有 `open-discovery` 有限搜索，用来发现不在观察清单和既有焦点中的
增量要闻；如果 `edition_date` 是运行主机本地今天，还会额外读取 Jin10 `list_news` 的一页
最新资讯作为开放发现候选，但不翻页。freeze packet、news digest 和 Publisher 会优先保留
最多 `uncovered_news_limit` 条（默认 2 条），没有真正新增且有证据的事件时允许为 0 条，
不会为了凑数重复已有主题。
没有证据不会被伪造成 claim，也不会要求给
`unavailable` 的指标挂无关证据。

### 可调整的研究方向

`var.focus_topics` 控制四条采集支线主动搜索的内容点。默认值覆盖隔夜的 S&P 500、Nasdaq、
中概股、A50、USD/CNH、黄金、原油、全球大宗商品和航运/运价；宏观的美联储、非农、中国
财政部、中国人民银行、主要央行、机构/投行多空观点、金融大咖言论，以及一次开放式增量
要闻发现；国内资金面、债券利率、商品、公司公告和卖方分歧；以及人工智能、数据中心、
芯片、能耗和算力基础设施。每项带有稳定 `id`、显示名称、
所属 `track` 和搜索词。用户可以完整替换列表，研究员会按支线自动获得对应的 `join` 后提示，
而不需要修改 workflow 结构。

Apply 完成后暴露三个输出：

 - `packet_path`：经 `packet_editor` 修订后的事实包；原始冻结包仅作为中间 artifact；
- `review_paths`：宏观、情绪、策略三个结构化复核；
- `report_path`：最终的 `morning.md`。

## 风险边界

这份财经早餐是信息整理工具，**仅供信息参考，非投资建议，也不构成个性化投资建议**。模型可能漏报、误读或放大某个
叙事，短线情绪也不能代替个人的期限、现金流和风险承受能力判断。

- 不依据单一 AI 信号满仓、集中下注或使用高杠杆；杠杆可能让一次判断错误造成不可逆
  损失。
- 多元化是首要防御。稳健底仓和长期资产配置应优先于早餐中的短线主题。
- 专业行情、金融 MCP、模型和新闻订阅都可能产生隐性成本，评估策略收益时要扣除这些
  费用。
- “可能受益”只表示逻辑方向，不表示某只证券一定上涨；每条映射必须同时给出反面解释
  和证伪条件。
