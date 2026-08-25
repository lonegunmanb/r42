# Collection / Collection QC 改造设计与剩余工作

## 目标

将 Collection 和 Collection QC 从“QC 通过自由文本 issues 要求继续搜集”的开放循环，改成围绕一份固定搜索计划逐项收敛的有限协议。

它要解决两个问题：

1. Collection QC 不能在后续轮次不断引入新的搜索方向，导致 Collection 在错误方向上持续扩张。
2. 对每个搜索方向，系统必须能明确给出：已满足、经过合理搜索仍无解，或达到预算上限但未解决。

这不是让 QC 判断初始选题是否正确。Collection 最初提交的方向和 stop conditions 被无条件接受；QC 只判断这些既定方向在已有材料下是否已足够。

## 完整协议设计

### 1. Information-needs 计划

Collection 在任何非只读工具调用前，必须调用一次 `r42_set_information_needs`：

```json
{
  "information_needs": [
    {
      "question": "需要回答的固定问题",
      "stop_conditions": [
        {"condition": "达到充分证据的客观条件"}
      ]
    }
  ]
}
```

约束：

- 至少一个、最多十个 information needs。
- 每个 need 至少一个、最多五个 stop conditions。
- R42 分配 canonical IDs，例如 `NEED-001` 和 `NEED-001-SC-001`。
- 提交后计划永久冻结：不得增加、删除、编辑、改名或拆分 need / condition。
- 每个 static research block 和每个 dynamic task 各自拥有独立的计划与状态。
- 在计划冻结前，只允许只读 artifact tools 和 `r42_set_information_needs`；所有 acquisition、artifact registration、artifact writing、checkpoint 等非只读工具均拒绝。

Collection QC 的 `criteria` 必须在 Collection 制定计划之前可见，但它只能给出证据质量标准，不能新增问题、条件或搜索范围。

### 2. Collection round

每个 Collection round 对每个 active need 都必须作出真实搜索努力。这个要求是行为契约，而不是 artifact 与 need 的机械绑定；R42 不尝试根据单个 fetch 或 artifact 证明模型实际搜索了某个 need。

每一轮必须恰好成功调用一次 `r42_collection_checkpoint`，并且它是该轮最后一个有效 Collection tool call：

```json
{
  "need_dispositions": [
    {
      "information_need_id": "NEED-001",
      "search_disposition": "continue"
    },
    {
      "information_need_id": "NEED-002",
      "search_disposition": "stalled"
    }
  ],
  "empty_reason": "本轮没有新增 artifact 时的原因"
}
```

约束：

- 每个 active need 必须且只能出现一次；closed need 不得再次出现。
- `continue` 表示仍有合理的下一步搜索动作。
- `stalled` 表示已经进行了真实努力，且没有更多有成效的搜索动作；不是“暂时不想搜”。
- R42 自动从 registry 收集本轮新增 artifact IDs，不要求 artifact 绑定到某个 need。
- 没有新增 artifact 时 `empty_reason` 必填。
- checkpoint 成功后，Collection 的非只读工具被锁定，立即进入 Collection QC。
- 下一次只有在 QC 让仍 active 的 needs 返回 Collection 时，才开启一个新 round。

删除旧的全局 `collection_exhausted` 声明。Collection 的“找不到更多”只能通过逐 need 的 `stalled` 表示。

### 3. Collection QC round

Collection QC 获得：

- 冻结的完整计划和 active needs；
- 每个 need 的 stop conditions；
- 所有既有 evidence 和本轮 checkpoint artifact IDs；
- 已经终止的 outcomes；
- 证据质量 criteria；
- 当前 collection round 数。

QC 可以使用专门的只读 artifact tools 检查材料。QC 不得调用 shell、采集工具或写入工具，也不得发明新的搜索方向。

每轮必须恰好成功调用一次 `r42_collection_qc_verdict`：

```json
{
  "assessments": [
    {
      "information_need_id": "NEED-001",
      "status": "needs_more",
      "unsatisfied_condition_ids": ["NEED-001-SC-002"],
      "evidence_progress": "material"
    }
  ]
}
```

约束：

- 每个 active need 必须且只能出现一次。
- `sufficient` 时 `unsatisfied_condition_ids` 必须为空。
- `needs_more` 时必须至少有一个未满足的既有 condition ID。
- `evidence_progress` 为 `material` 或 `none`。
- 本轮没有新增 artifact 时，`evidence_progress` 只能是 `none`。
- 删除全局 `decision`、自由文本 `issues`、以及将 Collection 全局标记 exhausted 的协议。
- QC 只能引用既有 condition IDs，不能增加条件文本、自由文本搜索建议或新 issues。

### 4. 单调收敛

第一次 QC assessment 为每个 need 建立未满足条件集合。

之后每一轮必须满足：

```text
current.unsatisfied_condition_ids
⊆
previous.unsatisfied_condition_ids
```

因此：

- 已满足 condition 永不重开。
- 已 `sufficient` 的 need 永不重开。
- 已终止的 need 永不重开。
- QC 无法在后续轮次偷偷扩大搜索空间。

ID 合法性、覆盖完整性、枚举、唯一性、状态流转、集合子集关系和调用次数都必须由 typed tools 机械校验；QC 只做语义判断。

### 5. 每个 need 的终止状态

每个 need 单独维护生命周期：

```text
ACTIVE
  -> SATISFIED
  -> UNRESOLVED / SEARCH_STALLED
  -> UNRESOLVED / BUDGET_EXHAUSTED
```

所有 terminal states 都不可逆。

每个 need 的 stall streak 只在同时满足以下条件时加一：

```text
Collection disposition = stalled
AND QC status = needs_more
AND QC evidence_progress = none
```

以下任一情况将该 need 的 streak 清零：

- Collection 返回 `continue`；
- QC 返回 `material`；
- 未满足条件集合缩小。

连续两轮满足停滞条件后，该 need 终止为：

```json
{
  "resolution": "unresolved",
  "termination_reason": "search_stalled"
}
```

已 `search_stalled` 的 need 后续不再搜索。

### 6. 轮次预算

默认 hard cap 为十个 Collection rounds。每一轮对每个 active need 都要求真实搜索努力。

第十轮 QC 结束后，尚 active 的 need 自动成为：

```json
{
  "resolution": "unresolved",
  "termination_reason": "budget_exhausted"
}
```

同一轮的终止优先级：

1. `sufficient`
2. `search_stalled`
3. `budget_exhausted`

### 7. 后续 Research 与 Final QC

Collection 是唯一允许 acquisition 的循环。其结束后 R42 形成完整的 `information_need_outcomes`，并只传给 Research。

Research 必须把 unresolved outcomes 表示为不确定性，不能把“没有找到足够证据”升级为“该事实不存在”。

Final QC 只能：

- pass；
- return to Research。

Final QC 不得看到 information need、stop condition 或 outcomes，也不得重新判断信息是否充分。它只检查候选产物中实际存在的论述是否被证据语义支持；不支持的已有论述只能删除或收窄，不得因缺少论述而要求补充，也不得重新打开 Collection。

## 已完成的实现工作

当前工作树已完成以下核心部分：

- `r42_set_information_needs` 的输入、限额校验、canonical ID 分配和冻结。
- production Collection context 对计划先行的工具门控。
- checkpoint 的 `need_dispositions` 结构、active need 覆盖和一次一轮门控。
- `assessments` 结构和 QC 逐 need 的 schema。
- condition ID 合法性、重复 ID、sufficient / needs_more 形状、已满足 condition 不可重开等机械校验。
- 两轮 stall streak 和 `search_stalled` outcome。
- Collection QC prompt 注入 frozen plan、active needs 和 outcomes。
- Collection 与 Collection QC 的 focused tests 已通过；全量 `go test ./... -count=1`、`go vet ./...` 和 `golangci-lint run` 曾在当前改动期间执行并通过，后续修改后仍需在最终提交前重新完整执行。

## 尚未完成的开发任务

以下工作必须完成后才能称为该改造完整落地。

### P0：移除临时兼容协议

当前 `collection.Context` / `collectionqc.Runner` 仍保留旧的 `CollectionExhausted`、全局 `Decision`、自由文本 `Issues` 等兼容字段和分支，以避免旧单元测试和调用方立即失效。

需要：

- 删除 `CheckpointArgs.CollectionExhausted`、`CheckpointOutput.CollectionExhausted`、`Config.CollectionExhausted` 及相关旧测试。
- 删除 `collectionqc.Verdict.Decision`、`collectionqc.Verdict.Issues` 和 legacy runner 分支。
- 删除 workflow 中与 Collection exhaustion / `EventCollectionLimitExhausted` 相关的旧语义，改为 outcomes 驱动。
- 让 `r42_set_information_needs` 的门控成为 Context 本身的正式不变量，不再依靠 `EnforceInformationNeeds` 的 production-only 开关。

### P0：完成 workflow 状态机的 need-aware 驱动

当前 workflow phase 仍主要依据旧的全局 `sufficient / needs_more` transition 工作；Collection QC 的新 outcomes 尚未完全成为 transition 的唯一依据。

需要：

- QC 后按 active / terminal outcomes 判断：有 active need 则回 Collection；全部 terminal 则进 Research。
- 到第十轮时将每个 remaining active need 标记 `budget_exhausted`，而不是沿用全局 collection budget shortcut。
- 在 workflow state 中保存 / 提供 outcomes，并保证 dynamic tasks 彼此隔离。
- 移除全局 `lastCollectionQCIssues` 作为 Collection 下一轮 prompt 的驱动，改为 active needs、剩余 condition IDs 和 outcomes。

### P0：把 outcomes 注入 Research

目前 Collection QC context 可以看到 outcomes，但 Research 尚未被完整注入同一份 `information_need_outcomes`。

需要：

- 向 Research phase 的初始及后续 prompt / context 注入完整 outcomes。
- 更新 Research prompt，使 unresolved outcome 必须被写成 uncertainty，而不是反向事实。
- 为 outcome 的 artifact / task 作用域补测试，尤其是 static block 与 dynamic task 的隔离。

### P0：禁止 Final QC reopen Collection

当前 workflow 仍定义 `EventReopenCollection`，coordinator 的旧路径和部分 Final QC 测试仍承认 `qc.DecisionReopenCollection`。

需要：

- 从 Final QC schema、typed verdict tool、workflow state、coordinator 和 prompts 中删除 `reopen_collection`。
- Final QC 只允许 `pass` 或 `revise_research`。
- 调整所有 e2e / coordinator tests，证明 Final QC 不会把流程返回 Collection。

### P1：设定并验证默认十轮 cap

设计要求默认最大 Collection round 为 10；当前 research config 仍允许 `MaxCollectionRounds == nil` 表示无限。

需要：

- 在 research spec 默认值、HCL schema、runtime 和 workflow config 中设为 10。
- 保留显式配置更小正数的能力；是否允许更大值需要另行产品决策，不能默默改变。
- 为第十轮 `budget_exhausted` 逐 need outcome 写状态机和 e2e 测试。

### P1：更新 chokepoint 示例与文档

示例中的 Collection prompt 和多处 checkpoint 说明仍有旧的“空 checkpoint / `collection_exhausted=true`”语言。

需要：

- 更新 `docs/examples/chokepoint/main.r42.hcl` 的每个 Collection / Collection QC block。
- 更新 `README.md` 中 Collection round、终止条件和 `max_collection_rounds` 的说明。
- 让示例的 tool_use 描述只要求 agent 提供能构造的新 typed-tool 字段；不要错误地将 artifact 绑定到 need。
- 将 example tests 改为断言冻结计划、逐项 assessment、无 `collection_exhausted` 和无 `reopen_collection`。

### P1：补齐测试与最终工程闭环

需要新增或迁移的测试至少包括：

- 计划未冻结时，所有非只读 Collection typed tools 被拒绝。
- checkpoint 每轮恰好一次，成功后全部 Collection non-read-only tools 被锁定。
- closed need 不得进入后续 checkpoint 或 QC verdict。
- 每个 active need 在 checkpoint / verdict 中必须各出现一次。
- `unsatisfied_condition_ids` 只能是前轮的子集。
- `none` 与无新 artifact 的一致性。
- 两轮 stalled 与第十轮 budget exhaust 的逐 need outcome。
- outcomes 只传给 Research；Final QC 看不到 stop conditions，且无法 reopen Collection。
- static block、dynamic task 和 artifact scope 的隔离。

最终按仓库纪律执行：

```text
go vet ./...
go test ./... -count=1
golangci-lint run
```

然后进行 CI coverage self-check 和独立只读 code review，修复或明确回应 findings 后才可提交。

## 明确接受的限制

- R42 不会机械证明 Collection 对每个 active need 都进行了真实搜索，因为不做 tool-call / artifact-to-need 绑定。
- 初始 information needs 和 stop conditions 可能选错；这由 Collection 负责，QC 不纠正方向。
- `evidence_progress = material` 是 QC 的语义判断，仍可能过宽。
- 第一轮可能在错误方向上浪费工作。

这些限制由计划冻结、condition 集合单调递减、两轮停滞终止和十轮硬上限共同限制，而不是被假装成可由代码完全证明的事实。
