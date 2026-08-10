# AGENTS.md —工程纪律

本仓库面向自动化编码 agent（GitHub Copilot Cloud Agent / Codex / Cline / Amp / Claude Code 等）。
所有 agent **必须**遵守本文件下列硬性约束，违反任意一条即视为任务未完成。

---

## 1. 写代码必须 TDD（Test-Driven Development）

> 适用范围：任何向 `cmd/`、`internal/`、`pkg/` 写入 Go 代码的任务。
> 例外：纯文档（`docs/`、`*.md`）、prompt 文件（`prompts/*.md`）、Bicep / Dockerfile 等基础设施模板可豁免（但仍鼓励 lint）。

### 1.1 Red-Green-Refactor 循环

对每一个新增的可测试单元（函数、方法、HTTP handler、矩阵/分析师服务方法、纯函数 helper 等）必须严格执行：

1. **Red**：先写一个会失败的测试。提交前必须能演示「测试存在 → 跑 → 看见预期的失败信息」。
2. **Green**：写出**刚好**让测试通过的最小实现。不允许带入未被测试覆盖的额外能力。
3. **Refactor**：在测试保持绿色的前提下整理命名、消除重复、抽象边界。

agent 在 PR 描述里必须给出一句话证据，例如：
- “先写 `TestMatrix_SubmitCard_GateRejects` 跑出 `undefined: SubmitCard`，再实现 handler 让其变绿。”

### 1.2 测试质量基线

- 使用 `stretchr/testify`（`require` 用于前置条件、`assert` 用于多断言）。详见 [.agents/skills/golang-stretchr-testify/SKILL.md](.agents/skills/golang-stretchr-testify/SKILL.md)。
- 默认 table-driven 写法；并发或长跑用例必须配 `t.Parallel()` 与 `goleak`。详见 [.agents/skills/golang-testing/SKILL.md](.agents/skills/golang-testing/SKILL.md)。
- 行为而非实现：测公开 API 的可观察行为，不测私有字段。
- 不允许把已有失败测试 `t.Skip()` 或注释掉来强行通过。
- 不允许只写「打印不断言」的伪测试。
- 覆盖率不是目标，**未被测试覆盖的分支不允许进入 PR**——若分支无法测，必须在 PR 中说明原因（外部依赖、平台限制等）并加 `// note: untested because ...` 单行注释。

### 1.3 必须通过的本地校验

在提交 PR 前 agent 必须按顺序跑通下列命令，并把输出贴到 PR 描述里：

```bash
go vet ./...
go test ./... -count=1
golangci-lint run
```

如果新增模块带集成测试（如 MinIO / Azurite / 其他需要本地副服务的存储），必须提供 `make test-integration` 入口并在 PR 中证明已跑过。

### 1.5 写代码前的行为契约（Karpathy 准则）

> 出处：[karpathy-guidelines](https://github.com/multica-ai/andrej-karpathy-skills/blob/main/skills/karpathy-guidelines/SKILL.md)（MIT）。
> 本节是**认知层**纪律，§1.1 是**产物层**纪律，两者必须同时遵守。

#### a. 写代码前先想清楚（Think Before Coding）

- 把**隐含假设**写在 PR 描述里（用 "## Assumptions" 小节，哪怕 1 行）
- 任务有多种合理解读时，**先在 issue / PR 讨论里列出选项**让人类选，**不要默默选一个**
- 如果有更简方案，**点出来**，必要时反对原方案
- 不清楚就 **stop**，明确指出哪里不清楚，再 ask

#### b. 最小代码先行（Simplicity First）

- 只写问题需要的最少代码；不做用户没要求的"扩展点"
- 单次使用的代码不要包抽象层 / interface / option struct
- 不要为不可能的场景写错误处理
- 200 行能 50 行写完，就重写
- 自查：「资深工程师会说这段过度设计吗？」如果会，简化

#### c. 外科手术式修改（Surgical Changes）

- 只动必须动的；不要"顺手"改邻近代码 / 注释 / 格式
- 不要 refactor 没坏的东西
- **匹配仓库现有风格**，即使你个人会另写
- 看见无关 dead code，**在 PR 描述里提一句**，不要顺手删
- 你自己改动产生的孤儿 import / 变量 / 函数 → **必须**删
- 测试：**每一行变更都必须能追溯回 issue / 任务**

#### d. 目标驱动执行（Goal-Driven Execution）

- 把任务翻译成**可验证的成功标准**——这正是 §1.1 TDD Red 步骤
- 多步任务要在 PR 描述列出最简计划：
  ```
  1. <步骤> → 验证：<怎么检查>
  2. <步骤> → 验证：<怎么检查>
  ```
- 强成功标准让 agent 能独立闭环；弱标准（"让它能跑"）会反复 ping 用户

#### Tradeoff 提示

Karpathy 原文：「these guidelines bias toward caution over speed. For trivial tasks, use judgment.」

本仓库的判定：

- **任何**改动 `cmd/` / `internal/` / `pkg/` / `*.bicep` / `Dockerfile` 的 PR 都按全套准则走
- 纯文档 / `prompts/` / `.agents/skills/**` 改动可放宽 b 和 c（文档的"简单"和"外科手术"是另一套标准）
- 「trivial」由独立评审 sub-agent 判定，不由实现 sub-agent 自己说了算

### 1.4 CI 工作流覆盖自检（每次 `git commit` 前必做）

本地校验只能证明「在我这台机器上跑得过」。要让团队的合并门禁可信，**CI 必须替我们守门**。所以 agent 在每次 `git commit` 之前必须做一次结构化核对：

1. 列出本次 staged diff 涉及的路径，按下表分类：

   | diff 命中的路径                                     | CI 必须存在的 PR 检查                                                                                          |
   | --------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
  | `cmd/**`、`internal/**`、`pkg/**`、`go.mod`、`go.sum` | 至少一个 workflow 在 `pull_request` 事件上跑 `go test ./... -count=1` + `go vet ./...` + `golangci-lint run`，且 `paths` / `paths-ignore` 过滤不能把本次改动目录排除掉 |
   | `Dockerfile`、`deploy/docker/**`、`build/**`         | 至少一个 workflow 在 `pull_request` 上跑 `docker build`（或 `docker buildx build`），并跑 `trivy`（或等价 SCA）扫描，HIGH+ 漏洞必须 fail |
   | `deploy/azure/**`、`*.bicep`                         | 至少一个 workflow 跑 `bicep build` + `bicep lint`（或 `az bicep build`）                                       |
   | `.github/workflows/**`                              | actionlint                                                                                                     |

2. 打开 `.github/workflows/*.yml` 实际确认上述 job 存在且 `on: pull_request` 配置正确（不能只在 `push: main` 上跑）。
3. 处理结论：
   - **缺失但可在本 PR 内补齐** → 直接补齐对应 workflow，这属于「让 CI 真的能替我们守门」的最小必要改动，**不算超出本任务范围**。
   - **当前 PR 无法补齐**（例如需要新增 secret、需要审批等） → 必须在 PR 描述 `## CI Coverage Self-Check` 段落显式写出「CI Gap」，列出缺失 job、原因、跟进任务 ID。
4. 独立评审子 agent 会照搬上表核对：若发现 diff 命中的路径在 CI 上没有对应 PR 门禁，且 PR 描述里也没有 explicit `CI Gap` 说明，**直接判 `MAJOR`**，等同于 `CHANGES_REQUESTED`。

> 例外：纯 `docs/**`、`*.md`、`prompts/**`、`.agents/skills/**`、`README.md` 类改动可以只跑 markdown lint / 链接检查；这些 workflow 若缺失，建议补但不阻塞。

---

## 2. 提交 PR 之前必须经过独立子 agent 代码评审

> 自评不算。必须由 [.github/agents/code-reviewer.agent.md](.github/agents/code-reviewer.agent.md) 这个**只读**子 agent 独立审阅。
> 评审是**协商式**的，不是单向裁决：实现 agent 必须批判性评估每条 finding，三选一回应；reviewer 也接受合理反驳。最终拿到 `APPROVED` 或触发 10 轮上限规则才能开 PR。

### 2.1 强制流程

1. 实现 agent 完成 TDD 循环，本地校验全绿。
2. **必须**通过子 agent 调用机制（`runSubagent` 或等价的 hand-off）调用 `code-reviewer` 子 agent，传入：
   - 本次任务 ID（如 `P1-T12`）+ 一句话目标 + 关联 issue 链接
   - 已修改 / 新增的文件清单 + 大致行数
  - 本地校验命令的实际输出（go vet / go test / golangci-lint）
   - TDD 证据（Red 测试名、Green 实现摘要）
   - **第 2 轮起**：当前轮次号、上一轮 reviewer 报告、实现 agent 对上一轮每条 finding 的 `fix` / `defer` / `push-back` 回应
3. `code-reviewer` 子 agent 会输出一份结构化评审报告（见 §2.3），必须包含明确 verdict：
   - `APPROVED` — 可以提交 PR
   - `CHANGES_REQUESTED` — 不允许提交 PR，必须先回应或修
4. 若 verdict 为 `CHANGES_REQUESTED`，实现 agent **对每条 Active Finding 必须三选一**：
   - **fix**：修复并附 commit/diff 证据。
   - **defer**：承认问题但开 follow-up issue 留到后续 PR；必须给 issue 链接 + owner，并证明当前 PR 不依赖它。
   - **push-back**：反对该 finding。必须给论据，例如：
     - 「out-of-scope，本 issue 只要求 X，不要求 Y」
     - 「该行为已被 `file:line` 的现有测试间接覆盖」
     - 「fix 会引入 N 行额外复杂度 + 一个新抽象，cost 大于 benefit」
     - 「reviewer 的描述与源码不符，见 `file:line`」
5. 实现 agent **不允许无条件接受**每条 finding。reviewer 也会 hallucinate / over-engineer / out-of-scope，必须自己做 in-scope 判断 + cost-benefit 评估再决定回应类型。
6. 整理回应后**重新触发** reviewer，进入下一轮（不允许跳过）。直到拿到 `APPROVED` 或触发 §2.4 的 10 轮上限。
7. PR 描述里必须粘贴最后一轮报告全文 + 完整多轮对话日志（见 §3）。

### 2.2 评审隔离硬约束

- 评审子 agent **只能读，不能写**。tools 已被限制为 `read, search`，禁止 `edit` 与 `execute`。
- 评审子 agent **每轮独立运行**，不直接访问实现 agent 的对话历史；上一轮的报告 + 实现 agent 的回应作为本轮**显式输入**传给它，而不是 context 自动继承，避免 confirmation bias。
- 实现 agent 不允许「修改评审子 agent 的 prompt 或裁剪其评审清单」来换取 `APPROVED`。授权过的多轮对话模型调整除外（即本文件 §2.1 + §2.4 + reviewer agent 文件本身）。

### 2.3 评审报告必须字段

```
Task: P1-T##
Round: <N> / 10
Verdict: APPROVED | CHANGES_REQUESTED
Summary: <1-2 句>

Active Findings:
  - [BLOCKER|MAJOR|MINOR|NIT|SCOPE] (confidence=high|medium|low) <file:line> — <问题> — <建议>

Held Despite Push-Back:
  - <finding 简述> — 实现 agent 论据 — 不接受原因

Resolved Since Last Round:
  - <finding 简述> → verified at <file:line>

Withdrawn Since Last Round:
  - <finding 简述> → reason: <接受反驳的理由>

Deferred Since Last Round:
  - <finding 简述> → follow-up: <issue 链接>

TDD Evidence Check:
  - 关键行为是否都有覆盖（直接或间接）: yes/no + 证据
  - 是否存在被跳过/注释的测试: yes/no
  - test / lint / vet 是否真的跑过: yes/no
Security & Safety:
  - 输入边界、错误处理、并发安全、敏感数据是否经过检查
Out-of-scope Drift:
  - 是否引入了任务范围外的修改

Round Budget:
  - 这是第 <N> 轮 / 上限 10 轮
Re-review Required: yes/no
```

第 1 轮可省略 `Resolved/Withdrawn/Deferred/Held` 四节。

verdict 计算规则：

- **第 1 轮**：Active Findings 里有 `BLOCKER` 或 confidence ≥ medium 的 `MAJOR` → `CHANGES_REQUESTED`。
- **第 2-9 轮**：仅 Active Findings 里仍有 `BLOCKER` 或未被合理反驳的 `MAJOR` → `CHANGES_REQUESTED`；否则 `APPROVED`。
- **第 10 轮**：见 §2.4。

### 2.4 10 轮上限规则（avoid infinite loop）

- 同一 PR 与 reviewer 累计对话上限 **10 轮**。
- 第 10 轮起 reviewer 只能保留 `BLOCKER` 级别的 `HELD`；所有 `MAJOR` / `MINOR` / `NIT` / `SCOPE` 自动按 `WITHDRAWN` 处理，verdict 必须 `APPROVED`（除非仍有 BLOCKER）。
- 触发 10 轮上限时实现 agent 必须在 PR 描述 `## Independent Code Review` 段落顶部显式声明：
  ```
  10-round cap reached. 实现 agent 视角为准。
  未达成共识的 finding 清单：
    - <finding 1> — reviewer 立场 — 实现 agent 立场 — 为何坚持
    - <finding 2> — ...
  ```
- `BLOCKER` 永远不被 10 轮规则放行：仍未消除的 BLOCKER 必须由人类评审介入或撤回 PR。

---

## 3. PR 提交规范

- 分支命名：`task/P1-T##-<slug>`，对应《实施路线图.md》§2.8 的任务 ID。
- 单 PR 单任务，禁止跨任务夹带。
- PR 描述必须按下面模板填写：

```
## Task
P1-T## — <标题>

## TDD Evidence
- Red: <第一个失败测试名 + 失败信息节选>
- Green: <最小实现摘要>
- Refactor: <若有，描述重构>

## Local Checks
$ go vet ./...
<output>
$ go test ./... -count=1
<output>
$ golangci-lint run
<output>

## CI Coverage Self-Check
- diff 命中类别：<Go 代码 | Dockerfile | Bicep | 文档 | …>
- 对应 PR 门禁 workflow：<workflow 文件名 + job 名 + 触发事件>
- CI Gap（如有）：<缺失的 job + 原因 + 跟进任务 ID>；无 gap 写 `none`

## Independent Code Review
<!-- 若触发 10 轮上限规则，本节顶部必须先写一段 "10-round cap reached" 声明，列出未达成共识的 finding 与实现 agent 立场，详见 §2.4。 -->

最终 verdict：APPROVED（或 10-round cap 放行）
总轮次：<N>

### Final Round Report
<粘贴最后一轮 reviewer 报告全文>

### Dialog Log
- Round 1: <reviewer 关键 finding 摘要> → 实现 agent 回应（fix/defer/push-back 概要）
- Round 2: ...
- ...
- Round N: APPROVED（或 10-round cap）

## Out of Scope
<本 PR 显式不做的事 + 关联 follow-up issue 链接>
```

- 任何 PR 缺少上述五个 section 都会被视为不合规，必须补齐再请求合并。

---

## 4. 其他始终适用的工程纪律

- 配置 / CLI：见 [.agents/skills/golang-cli/SKILL.md](.agents/skills/golang-cli/SKILL.md)、[.agents/skills/golang-spf13-cobra/SKILL.md](.agents/skills/golang-spf13-cobra/SKILL.md)、[.agents/skills/golang-spf13-viper/SKILL.md](.agents/skills/golang-spf13-viper/SKILL.md)。
- 日志 / 错误：使用 `log/slog` + `samber/oops`，见 [.agents/skills/golang-error-handling/SKILL.md](.agents/skills/golang-error-handling/SKILL.md)、[.agents/skills/golang-samber-oops/SKILL.md](.agents/skills/golang-samber-oops/SKILL.md)。
- 项目布局与命名：见 [.agents/skills/golang-project-layout/SKILL.md](.agents/skills/golang-project-layout/SKILL.md)、[.agents/skills/golang-naming/SKILL.md](.agents/skills/golang-naming/SKILL.md)。
- 安全：见 [.agents/skills/golang-security/SKILL.md](.agents/skills/golang-security/SKILL.md)。
- 证据校验 typed tool：比较自然语言证据字符串时，默认先规范化 Unicode 空白，忽略首尾空白、连续空格、换行和 Tab 等纯排版差异；不得借此忽略文字、标点、顺序等实质差异，也不得放宽 ID、URL、枚举或证据状态等结构化字段的精确校验。
- Researcher / QC 职责边界：schema、必填字段、枚举、ID 唯一性、引用存在性、路径、状态流转、图约束、精确或规范化文本匹配等可由代码确定的机械性检查，**必须**在 researcher session 提交阶段使用的 typed tool（尤其 terminate / finalize tool）中完成。工具应一次返回全部已发现的问题，让 researcher 在结束本阶段前修正；不得把这些检查推迟给 QC，也不得让 QC 用 grep、PowerShell、shell 或临时脚本重复实现第二套 validator。
- QC 只做语义检查：QC 仅判断代码无法可靠决定的语义问题，例如证据是否真正蕴含结论、推论是否越界、是否混淆相关概念、不同 variant / 时期是否被错误合并，以及不确定性是否表达审慎。QC 必须把 researcher typed tool 已通过的机械校验视为权威结果。
- 大体积 QC 数据访问：当 QC 需要检查大型 JSON、ledger、快照集合或跨文件关系时，必须为其配置专用的**只读** typed tool，按 ID 或检查对象投影最小必要数据。该工具只负责读取和筛选，不修改候选制品、不输出 QC pass/fail、不复制机械校验逻辑；配置完成后应禁用 QC 的 PowerShell、Bash 和通用 shell 工具，避免任意脚本、上下文浪费和被质检数据被修改。
- 评审视角清单（实现 agent 自查也建议读）：见 [.agents/skills/code-review-excellence/SKILL.md](.agents/skills/code-review-excellence/SKILL.md)。

## 5. 不要做的事

- 不要在没有失败测试的情况下提交实现代码。
- 不要绕过独立子 agent 评审直接开 PR。
- 不要在一个 PR 里夹带多个任务的改动；不要顺手重构任务范围外的文件。
- 不要默默替用户做歧义选择——有多种合理解读时停下来在 issue / PR 评论里列选项。
- 不要在任务范围外"顺手"重构、改格式、调注释；out-of-scope drift 会被独立评审打回。
- **不要无条件接受 reviewer 的每一条 finding**。reviewer 也会 hallucinate / over-engineer / out-of-scope，必须自己做 in-scope 判断 + cost-benefit 评估，再决定 `fix` / `defer` / `push-back`（见 §2.1）。
- **不要把 reviewer 的 push-back 反应当对抗**。reviewer 接受合理反驳是设计的一部分；你需要用论据说服它而不是机械修复。
- 不要修改 [`.github/agents/code-reviewer.agent.md`](.github/agents/code-reviewer.agent.md) 来「让评审更宽松」。允许的调整只有 §2.1 + §2.4 + reviewer agent 文件本身这三处由人类授权的更新。
- 不要把已有失败测试 `t.Skip()` 掉换通过；也不要把测试改成只断言「不 panic」。
- 直接使用官方上游 `github.com/github/copilot-sdk/go`，不要引入第三方 fork 或 adapter。
- 不要在 CI workflow 里用 `paths-ignore` 把本次 diff 影响的路径排除掉以闪过单测门禁；也不要把关键 job 只绑在 `push: main` 上、不走 `pull_request`。
- **不要重新引入"并行 broadcast / 多个 goroutine 同时召唤 analyst"** —— W3 已经把这条路径完全删除，详见 [docs/matrix-flow.md §1 设计目标 + §4 为什么不并行](docs/matrix-flow.md)。
- **不要把 `forecaster_final_predictor` 当成 analyst 放进 analyst 队列**；它由矩阵在 `analyst_phase.completed` 时（或 deadline 兜底时）显式召唤，走 `roleForcedPredictor` 路径。

---

## 7. 一句话总结

> **No tests, no code. No independent review, no PR. No CI gate, no commit.**
