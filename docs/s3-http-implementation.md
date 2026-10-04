# HTTP S3 Endpoints

## Task

| ID | Goal | Status |
| --- | --- | --- |
| P1-T26 / S3-HTTP | Allow HTTP S3 endpoints for local compatible services. | completed |

A configured `http://127.0.0.1:9000` endpoint previously failed during Plan
with `s3 provider endpoint must be an HTTPS URL`. HTTP and HTTPS URLs with a
host are now accepted; the AWS SDK preserves the configured scheme.

## Assumptions

- The user explicitly requested HTTP support, including local S3 services.
  Both schemes are supported for all hosts; there is no localhost-only rule.
- Empty endpoints continue to use the AWS SDK default. Unsupported schemes,
  missing hosts, and malformed URLs remain errors.
- This fixes endpoint validation; it does not automatically change endpoint
  schemes or client transport settings.

## Plan

1. Add validation, SDK request, and example Plan regression tests; observe the
   existing HTTPS-only failure before modifying implementation.
2. Allow HTTP in the existing endpoint condition and update documentation;
   verify HTTP and HTTPS request construction without network access.
3. Run vet, full tests, and lint in order, obtain independent review, update
   task status, and check the root `break` file before commit.

## TDD Evidence

- Red: `TestS3ProviderConfigValidate/http_localhost_endpoint` failed with
  `Received unexpected error: s3 provider endpoint must be an HTTPS URL`.
  IPv4, IPv6, and remote HTTP cases failed with the same error.
- Red: `TestNewClientPreservesEndpointSchemeInRequests/http` and every example's
  `TestExamplesOptionalS3RunUpload/<example>/local_http` also failed with the
  HTTPS-only validation error.
- Green: the existing URL validation now accepts either `http` or `https`
  and still requires a parsed host. The diagnostic names both allowed schemes.
  All regression cases pass.
- Coverage: validation tests cover default/HTTPS/HTTP endpoints and rejected
  schemes, missing scheme/host, and malformed URLs. Actual AWS SDK request
  construction verifies scheme, host, and path for HTTP and HTTPS. All six
  example Plan cases preserve the HTTP provider endpoint in the folder plan.
- Refactor: the SDK test checks its client type assertion explicitly to satisfy
  `forcetypeassert`; no additional production behavior was introduced.

## Local Checks

Final checks after the last Go edit, executed in this order:

```text
$ go vet ./...
(no output; exit 0)
$ go test ./... -count=1
ok  	github.com/lonegunmanb/r42/cmd/r42	1.253s
ok  	github.com/lonegunmanb/r42/docs/examples	9.040s
ok  	github.com/lonegunmanb/r42/docs/examples/chokepoint	48.302s
ok  	github.com/lonegunmanb/r42/docs/examples/deep-research	8.665s
ok  	github.com/lonegunmanb/r42/docs/examples/morning	17.169s
ok  	github.com/lonegunmanb/r42/docs/examples/secjury	25.202s
ok  	github.com/lonegunmanb/r42/internal/artifact	1.558s
ok  	github.com/lonegunmanb/r42/internal/checkpoint	1.420s
ok  	github.com/lonegunmanb/r42/internal/cli	40.830s
ok  	github.com/lonegunmanb/r42/internal/collection	1.966s
ok  	github.com/lonegunmanb/r42/internal/collectionqc	1.457s
ok  	github.com/lonegunmanb/r42/internal/concurrency	1.427s
ok  	github.com/lonegunmanb/r42/internal/config	1.017s
ok  	github.com/lonegunmanb/r42/internal/coordinator	1.083s
ok  	github.com/lonegunmanb/r42/internal/copilot	1.007s
ok  	github.com/lonegunmanb/r42/internal/copilotprobe	2.350s
ok  	github.com/lonegunmanb/r42/internal/debuglog	2.035s
ok  	github.com/lonegunmanb/r42/internal/e2e	7.215s
ok  	github.com/lonegunmanb/r42/internal/evidence	1.486s
ok  	github.com/lonegunmanb/r42/internal/executor	2.222s
ok  	github.com/lonegunmanb/r42/internal/goldenprobe	1.410s
ok  	github.com/lonegunmanb/r42/internal/mcp	1.550s
ok  	github.com/lonegunmanb/r42/internal/mcp/spec	1.372s
ok  	github.com/lonegunmanb/r42/internal/module	1.410s
ok  	github.com/lonegunmanb/r42/internal/module/spec	3.481s
ok  	github.com/lonegunmanb/r42/internal/plan	1.770s
ok  	github.com/lonegunmanb/r42/internal/progress	1.609s
ok  	github.com/lonegunmanb/r42/internal/project	1.270s
ok  	github.com/lonegunmanb/r42/internal/provider	1.029s
ok  	github.com/lonegunmanb/r42/internal/qc	1.024s
ok  	github.com/lonegunmanb/r42/internal/research/runtime	1.228s
ok  	github.com/lonegunmanb/r42/internal/research/spec	1.177s
ok  	github.com/lonegunmanb/r42/internal/run	1.329s
ok  	github.com/lonegunmanb/r42/internal/s3	0.907s
ok  	github.com/lonegunmanb/r42/internal/s3/spec	0.929s
ok  	github.com/lonegunmanb/r42/internal/scaffold	0.949s
ok  	github.com/lonegunmanb/r42/internal/spec	1.028s
ok  	github.com/lonegunmanb/r42/internal/tool/external	1.634s
ok  	github.com/lonegunmanb/r42/internal/tool/gotool	3.847s
ok  	github.com/lonegunmanb/r42/internal/tool/spec	0.971s
ok  	github.com/lonegunmanb/r42/internal/tool/starlarktool	3.764s
ok  	github.com/lonegunmanb/r42/internal/ui	1.622s
ok  	github.com/lonegunmanb/r42/internal/variableschema	0.715s
ok  	github.com/lonegunmanb/r42/internal/workflow	1.331s
$ golangci-lint run
0 issues.
```

`git diff --check` also passed. Tests construct SDK requests and do not send
network traffic or require a local S3 service. No live S3 integration test was
performed. Local race testing remains unavailable on this Windows host because
CGO and a C compiler are unavailable; the existing Linux CI race gate applies.

## CI Coverage Self-Check

- Changed categories: Go source/tests, example tests, documentation.
- `.github/workflows/ci.yml` uses `pull_request` with no path filters.
- `test` runs `go vet ./...`, `go test ./... -count=1`, and full race tests.
- `lint` runs the official golangci-lint action; actionlint also runs.
- CI Gap: none. No workflows or dependencies were changed.

## Independent Code Review

Final verdict: APPROVED. Total rounds: 1.

The isolated reviewer followed `.github/agents/code-reviewer.agent.md` using
explicitly supplied source snapshots. It did not execute commands, modify
files, or access the implementation conversation. The report below identifies
the limits of this snapshot review.

### Final Round Report

```text
Task: P1-T26 / S3-HTTP
Round: 1 / 10
Verdict: APPROVED
Summary: 提供的源码快照中未发现阻塞问题：HTTP endpoint 获准通过，HTTPS 和 AWS 默认 endpoint 保持原行为，无效 URL 拒绝路径已有覆盖。本次仅审阅调用方提供的快照和真实校验输出，未直接访问工作树；未验证真实 S3 网络请求或本地 race 检查。

Active Findings:
  - none

Held Despite Push-Back:
  - n/a (first round)

Resolved Since Last Round:
  - n/a (first round)

Withdrawn Since Last Round:
  - n/a (first round)

Deferred Since Last Round:
  - n/a (first round)

TDD Evidence Check:
  - 关键行为是否都有覆盖（直接或间接）: yes。internal/s3/spec/schema_test.go:12 表驱动覆盖 HTTP localhost、IPv4、IPv6、远程 endpoint、HTTPS、默认 endpoint、非支持协议、缺失 host 和解析错误；internal/s3/client_test.go:44 验证真实 AWS SDK 构建请求时保留 HTTP/HTTPS scheme、host 和 path；docs/examples/s3_test.go:46 的新增配置进入现有六组示例执行测试。前两个测试均包含 arrange、act、testify assert/require。Red 输出展示新增 HTTP 用例先被 HTTPS 限制拒绝，Green 仅放宽允许的 scheme。
  - 是否存在被跳过/注释的测试: no。提供的测试快照无此情况；调用方对三个变更测试文件的搜索退出 1、无匹配。
  - test / lint / vet 是否真的跑过且全绿: yes。最终顺序为 go vet ./...（exit 0，无输出）、go test ./... -count=1（全部包通过，exit 0）、golangci-lint run（0 issues，exit 0）。

Security & Safety:
  - 输入边界: ok。仅接受 http/https 且 Host 非空，url.Parse 错误仍被拒绝；空 endpoint 继续使用 AWS SDK 默认配置。
  - 错误处理: ok。校验失败由既有 NewClient 路径返回，错误消息准确反映允许协议。
  - 并发安全: ok。生产变更为无共享状态的校验条件；新增测试使用 t.Parallel，SDK request.Build 不发送网络请求。未新增后台 goroutine；示例包已有 goleak TestMain。本地未跑 race，CI test job 保留 race 检查。
  - 敏感数据: ok。测试使用固定虚构凭据，未新增敏感日志。HTTP 为用户显式选择，未引入自动协议降级。

Out-of-scope Drift:
  - 无。生产代码、测试和说明均对应 HTTP endpoint 支持；计划新增的实施记录文档尚未提供快照，不包含在本次源码审阅范围内。
  - CI 覆盖符合要求：提供的 .github/workflows/ci.yml 快照包含无路径过滤的 pull_request 触发，test job 执行 vet 和完整测试，lint job 执行 golangci-lint。CI Gap: none。

Round Budget:
  - 这是第 1 轮 / 上限 10 轮
  - 未触发 10 轮上限规则。

Re-review Required: no
```

### Dialog Log

- Round 1: supplied task context, changed-source snapshots, Red/Green evidence,
  and CI configuration. Reviewer acknowledged receipt and waited for final
  local checks. Supplied the final ordered vet/test/lint outputs and corrected
  an accidental duplicate row in the copied reviewer instructions; no review
  rules were changed. Reviewer returned APPROVED with no findings.

## Out of Scope

No automatic HTTPS-to-HTTP fallback, live S3 deployment, or upload scheduling
changes. No follow-up issue is required for this agreed scope.
