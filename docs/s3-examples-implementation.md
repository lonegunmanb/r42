# Optional S3 Upload in Examples

## Task

| ID | Goal | Status |
| --- | --- | --- |
| S3-EXAMPLES | Add optional S3 upload and explicit `run_wd()` to every top-level example. | completed |

## Assumptions

- The user confirmed that "runs directory" means the current
  `.r42/runs/<run-id>/`, consistent with existing S3 source confinement;
  historical runs are outside this task.
- The six runnable top-level examples own the upload. Their child acquisition
  modules do not independently upload the shared run.
- `s3 = null` disables both blocks. A configured object requires region,
  bucket, and prefix; credentials may use the AWS default chain or environment
  references. Missing or invalid enabled configuration must fail explicitly.
- "After completion" means after all example research/module DAG nodes succeed.
  It does not mean after final process teardown or after an unsuccessful run.

## Plan

1. Verify indexed and empty S3 block references, then fix their value export.
   Validation: public runtime planning and saved-plan Apply tests.
2. Add `run_wd()` and `s3.r42.hcl` to all six examples with current-run source and full DAG
   dependencies. Validation: disabled/null, AWS/OSS, invalid-config planning
   tests and a simulated basic-example Apply.
3. Run repository checks and obtain independent review, then update task status
   and check the root `break` file.

## TDD Evidence

- Red: `TestRuntimeS3ProviderForEachReferences` failed with `Invalid index` for
  enabled instances and `Unsupported attribute` for the empty collection.
  `TestRuntimeAppliesSavedS3FolderForEach` failed when iterating folder results.
- Green: both blocks now export `golden.Valuable` fields so Golden aggregates
  instances by key; `ResearchConfig.EvalContext` exposes their single-label
  namespace without the internal empty-type level. Runtime tests then passed.
- Red: `TestExamplesOptionalS3RunUpload` failed in all six examples because
  `s3.r42.hcl` did not exist. After adding the files, its planning cases passed.
- Apply evidence: `TestBasicExampleOptionalS3UploadAfterSuccess` verifies current
  run nested files, saved plan, and checkpoints are uploaded on success, and no
  S3 client is created for disabled configuration or failed research.
- Red: `TestRuntimeRunWorkingDirectoryIsSharedAndPersisted` and
  `TestRuntimeRunWorkingDirectoryRejectsArguments` failed with
  `There is no function named "run_wd"` before implementation.
- Green: run-bound functions are registered for planning, Apply-time outputs,
  and deferred research expressions. Tests cover default/custom run roots,
  root/child-module agreement, slash normalization, no filesystem creation
  during Plan, saved-plan Apply, deferred locals/dynamic tasks, and rejection
  of arguments. All example sources now explicitly call `run_wd()`.

## CI Coverage Self-Check

- Changed categories: Go source/tests, example HCL, documentation.
- `.github/workflows/ci.yml` runs on `pull_request` without path filters.
- `test` runs `go vet ./...` and `go test ./... -count=1`, plus race tests.
- `lint` runs golangci-lint via its official action; `actionlint` checks workflows.
- CI Gap: none. This task does not change workflows or add dependencies.

## Local Checks

```text
$ go vet ./...
(no output; exit 0)
$ go test ./... -count=1
ok github.com/lonegunmanb/r42/cmd/r42 0.677s
ok github.com/lonegunmanb/r42/docs/examples 6.051s
ok github.com/lonegunmanb/r42/docs/examples/chokepoint 43.246s
ok github.com/lonegunmanb/r42/docs/examples/deep-research 7.010s
ok github.com/lonegunmanb/r42/docs/examples/morning 13.578s
ok github.com/lonegunmanb/r42/docs/examples/secjury 21.388s
ok github.com/lonegunmanb/r42/internal/artifact 0.625s
ok github.com/lonegunmanb/r42/internal/checkpoint 1.021s
ok github.com/lonegunmanb/r42/internal/cli 36.411s
ok github.com/lonegunmanb/r42/internal/collection 1.077s
ok github.com/lonegunmanb/r42/internal/collectionqc 0.625s
ok github.com/lonegunmanb/r42/internal/concurrency 1.204s
ok github.com/lonegunmanb/r42/internal/config 0.631s
ok github.com/lonegunmanb/r42/internal/coordinator 0.640s
ok github.com/lonegunmanb/r42/internal/copilot 0.966s
ok github.com/lonegunmanb/r42/internal/copilotprobe 2.427s
ok github.com/lonegunmanb/r42/internal/debuglog 2.378s
ok github.com/lonegunmanb/r42/internal/e2e 6.154s
ok github.com/lonegunmanb/r42/internal/evidence 1.903s
ok github.com/lonegunmanb/r42/internal/executor 1.276s
ok github.com/lonegunmanb/r42/internal/goldenprobe 0.833s
ok github.com/lonegunmanb/r42/internal/mcp 1.432s
ok github.com/lonegunmanb/r42/internal/mcp/spec 0.768s
ok github.com/lonegunmanb/r42/internal/module 0.959s
ok github.com/lonegunmanb/r42/internal/module/spec 3.165s
ok github.com/lonegunmanb/r42/internal/plan 1.648s
ok github.com/lonegunmanb/r42/internal/progress 1.764s
ok github.com/lonegunmanb/r42/internal/project 1.036s
ok github.com/lonegunmanb/r42/internal/provider 1.426s
ok github.com/lonegunmanb/r42/internal/qc 1.484s
ok github.com/lonegunmanb/r42/internal/research/runtime 1.326s
ok github.com/lonegunmanb/r42/internal/research/spec 1.506s
ok github.com/lonegunmanb/r42/internal/run 1.294s
ok github.com/lonegunmanb/r42/internal/s3 1.027s
ok github.com/lonegunmanb/r42/internal/s3/spec 1.028s
ok github.com/lonegunmanb/r42/internal/scaffold 0.988s
ok github.com/lonegunmanb/r42/internal/spec 1.020s
ok github.com/lonegunmanb/r42/internal/tool/external 1.626s
ok github.com/lonegunmanb/r42/internal/tool/gotool 3.670s
ok github.com/lonegunmanb/r42/internal/tool/spec 0.800s
ok github.com/lonegunmanb/r42/internal/tool/starlarktool 2.880s
ok github.com/lonegunmanb/r42/internal/ui 0.900s
ok github.com/lonegunmanb/r42/internal/variableschema 0.346s
ok github.com/lonegunmanb/r42/internal/workflow 0.841s
$ golangci-lint run
0 issues.
```

Focused race checks could not start on this Windows host:

```text
$ go test -race ./internal/cli ./docs/examples -run '<S3 regression tests>' -count=1
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```

`CGO_ENABLED=0` and no C compiler is installed. CI's existing Linux race gate
runs `go test ./... -race -shuffle=on -count=1`. Tests use `t.Parallel()` and
package-level goleak verification; local uploads use a simulated client, not a
live AWS/OSS bucket.

## Independent Code Review

Final verdict: APPROVED. Total rounds: 1.

The isolated reviewer read explicitly supplied source snapshots and actual
check outputs. It did not execute commands, access implementation conversation
history, or modify files.

### Final Round Report

```text
Task: S3-EXAMPLES
Round: 1 / 10
Verdict: APPROVED
Summary: 根据提供的只读源码快照、Red/Green 证据及实际校验输出，未发现阻塞问题。六个示例均以条件 for_each 启用 S3，并在全部研究/module 节点成功后上传 run_wd() 指向的当前运行目录。

Active Findings:
  - none

Held Despite Push-Back:
  - n/a

Resolved Since Last Round:
  - n/a

Withdrawn Since Last Round:
  - n/a

Deferred Since Last Round:
  - n/a

TDD Evidence Check:
  - 关键行为是否都有覆盖（直接或间接）: yes。runtime_s3_test.go 覆盖空/单/多实例 provider 引用、folder 保存及加载后的 Apply、成功上传、禁用及研究失败时不创建客户端；docs/examples/s3_test.go 覆盖全部六个示例的启用、禁用、配置错误和完整依赖集合。
  - run_wd() 覆盖: internal/cli/runtime_s3_test.go:28 覆盖绝对路径、根/子 module 共享、自定义运行根目录、保存计划、延迟 locals/outputs、dynamic tasks；另有参数拒绝测试。
  - Red/Green: 已提供 for_each 的 Unsupported attribute/Invalid index、六个缺失 s3.r42.hcl、run_wd() unknown function 的失败输出及后续通过输出。
  - 是否存在被跳过/注释的测试: no，提供的新增测试中未见。
  - test / lint / vet 是否真的跑过且全绿: yes，最终实际输出为 vet exit 0、全量 test exit 0、lint “0 issues.”。

Security & Safety:
  - 输入边界: ok；既有 provider/folder 校验及源路径限制保持适用。
  - 错误处理: ok；研究失败阻止上传，错误配置在 Plan 阶段报告。
  - 并发安全: ok；新增并行测试使用 t.Parallel 和包级 goleak，共享 fake 状态使用 mutex/atomic。
  - 敏感数据: ok；示例使用凭据引用，文档说明运行制品可能含敏感信息及 exclude 配置。
  - 验证限制: 本次为快照评审；本地 race 因 CGO/编译器缺失未能启动，未访问真实 S3。两项均未被误报为通过。

Out-of-scope Drift: 无；run_wd() 属于用户明确扩展的任务范围。

CI Coverage Self-Check:
  - ci.yml 在 pull_request 上运行全量 vet/test/race 和 golangci-lint，无路径排除或 job 条件。
  - CI Gap: none。

Round Budget:
  - 这是第 1 轮 / 上限 10 轮。
  - 达到 10 轮仍非 BLOCKER 按 AGENTS 规则实现视角放行。

Re-review Required: no
```

### Dialog Log

- Round 1: reviewer requested the original S3 Red output to complete its TDD
  check. The implementation agent supplied the actual failures and subsequent
  passing output; no active code findings were raised. Final verdict: APPROVED.
