# Final report artifact outputs

## Task

| ID | Goal | Status |
| --- | --- | --- |
| FINAL-REPORT-ARTIFACT | Export final report artifacts from the reporting examples for downstream research. | completed |

## Assumptions

- Reporting examples are `deep-research`, `chokepoint`, `secjury`, and `morning`.
  `basic` has no report; `multi-step` exports fetched source material.
- `final_report_artifact` exports the same Markdown artifact as `report_path`.
  Artifact IDs remain unknown until Apply registers the artifacts.

## Plan

1. Add complete artifact outputs and composition documentation. Verify each
   example's output metadata and agreement with its existing report path.
2. Verify downstream imports through the public runtime, including the module
   dependency and saved-plan Apply reads; repair deferred static artifact
   metadata, import snapshots, and the run-wide artifact registry.
3. Run repository checks and independent review, update this task's status,
   then check the root `break` file.

## TDD Evidence

- Red: `TestExamplesExportFinalReportArtifact` failed in all four examples with
  `final_report_artifact must expose the complete final report artifact`.
- Further Red: deferred static reports had six metadata fields instead of eight;
  downstream imports failed with `sources must contain artifact objects`, then
  `encode import_artifact "previous_report" sources: value must be wholly known`.
- Green: add the four outputs, project deferred artifacts with unknown `id` and
  `kind = "artifact"`, and defer snapshots of imports with unknown IDs while
  retaining validation and the decoded configuration for otherwise known blocks.
- `go test ./docs/examples ./internal/research/spec ./internal/module/spec -count=1`
  passed, including the unchanged `TestResearchBlockPlansArtifactImport`.
- Red: `TestProductionRuntimeImportsModuleReportArtifactFromSavedPlan` reached
  the downstream reader but returned `artifact_read_failed: unknown artifact`.
- Green: child runtime factories share the parent's artifact registry. The
  saved-plan Apply test now verifies the exported run-scoped ID and reads the
  report content through the downstream session's authorized artifact reader.
- Further Red: passing the object to another child module failed with
  `module input "previous_report" must be wholly known during plan`.
- Green: artifact object inputs may retain unknown metadata during Plan; Apply
  resolves them from the saved module expression and upstream results before
  starting the child. The Apply test passes the report between sibling modules
  and verifies both its re-export and authorized content read.

## CI Coverage Self-Check

- Changed categories: Go source, example HCL/tests, documentation.
- `.github/workflows/ci.yml` runs on `pull_request` without path filters.
- `test`: `go vet ./...`, `go test ./... -count=1`, and race tests.
- `lint`: golangci-lint via its official action. CI Gap: none.

## Local Checks

```text
$ go vet ./...
(exit 0; no diagnostics)

$ go test ./... -count=1
(exit 0; all packages passed)

$ golangci-lint run
0 issues.
```

## Independent Code Review

最终 verdict：APPROVED  
总轮次：1

### Final Round Report

Task: FINAL-REPORT-ARTIFACT  
Round: 1 / 10  
Verdict: APPROVED  
Summary: 源码和测试覆盖了最终报告 artifact 从 producer module 导出、经 saved plan 传递并由 consumer 授权读取的链路。未发现可复现的阻塞缺陷。

Active Findings:
  - 无

Held Despite Push-Back:
  - n/a (first round)

Resolved Since Last Round:
  - n/a (first round)

Withdrawn Since Last Round:
  - n/a (first round)

Deferred Since Last Round:
  - n/a (first round)

TDD Evidence Check:
  - 关键行为是否都有覆盖（直接或间接）: yes — `docs/examples/report_artifact_test.go` 验证四个报告示例导出包含八个字段的完整 artifact，并验证 consumer plan 依赖 producer module；`internal/cli/runtime_report_artifact_test.go` 验证 saved plan 序列化/反序列化后 ID 在 Apply 阶段解析、artifact 输出保留且获授权的 reader 能读取报告内容。两个测试均有 arrange / act / assert，并使用 testify。
  - 是否存在被跳过/注释的测试: no — 实现方报告对两份新增测试搜索 `t.Skip(`、`TODO test` 和 `FIXME test` 无匹配，也无注释掉的 `func Test`；两个测试包有 `goleak.VerifyTestMain`。
  - test / lint / vet 是否真的跑过且全绿: yes — `go vet ./...` exit 0；`go test ./... -count=1` exit 0，报告所有包均为 `ok`；`golangci-lint run` 输出 `0 issues.`

Security & Safety:
  - 输入边界: ok — 模块输入仍经过 schema、变量类型、required 和 undeclared input 校验；仅放宽带有精确 `kind = "artifact"` 的 unknown object，测试覆盖 unknown scalar、缺少 kind 和其他 kind。
  - 错误处理: ok — 配置解析与表达式求值错误向上传递；无效模块输入边界仍报错。
  - 并发安全: ok — artifact registry 自身使用 `sync.RWMutex`；模块按 DAG 依赖顺序建立共享 registry，测试包启用 goleak。
  - 敏感数据: ok — 本次改动未新增凭据或敏感数据处理路径。

Out-of-scope Drift:
  - 无。报告示例、README、对应测试和支撑 artifact 导入/模块传递的引擎改动均对应任务目标。

Round Budget:
  - 这是第 1 轮 / 上限 10 轮。
  - 若达到第 10 轮仍有非 BLOCKER 未解，按 AGENTS.md §2.1 规则以实现 agent 视角放行。

Re-review Required: no

### Dialog Log

- Round 1: reviewer 要求补充 deferred import/static-expression 边界测试、unknown module input 的校验路径、goleak TestMain、CI job 和 examples diff；实现方提供现有 invalid/missing/undeclared 输入测试、三项 unknown 非 artifact 边界测试、saved-plan runtime 集成测试、CI workflow 实际配置与完整本地检查结果。Reviewer 核对后给出 `APPROVED`，无 active findings。

## Out of Scope

Creating reports for examples that currently produce no final report.
