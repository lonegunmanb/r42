# Optional MarkItDown Tool

## Task

| ID | Goal | Status |
| --- | --- | --- |
| DOC-MARKITDOWN | Detect MarkItDown once at Apply startup and expose its CLI options as a Collection typed tool. | complete |

This task comes from the current user request; no issue link was supplied.

## Assumptions

- Startup means the start of each Apply execution. The resolved executable and
  availability are shared with child modules for that run.
- Detect the `markitdown` CLI on the current process PATH. Do not install it,
  poll, hot-load tools, or require a separately running MCP server.
- Expose the upstream CLI's current canonical options, including help/version,
  output files, stdin text, plugins, data URIs, and Azure conversion settings.
  Older installations can reject options their version does not support.
- File input/output stays within the task workspace. Cloud modes and plugins
  are explicit opt-ins. Format support is provided by the installed CLI.

## Plan

1. Write startup detection, complete typed-schema, flag forwarding, filesystem,
   bounds, cancellation, and runtime phase tests; observe Red failures.
2. Implement a startup-resolved CLI runner and conditionally attach the typed
   conversion tool only to Collection. Verify root and child module execution.
3. Run vet, full tests, and lint in order, obtain independent review, update
   this task status, and check the root `break` file.

## TDD Evidence

- Red: `TestRunnerDetectsMarkItDownOnlyAtStartup` and
  `TestRunnerPassesAllMarkItDownOptions` failed to compile with
  `undefined: newRunner` and `undefined: Input` before the new contract existed.
- Red: `TestDocumentToolRegistersEveryMarkItDownArgument` failed because the
  description lacked `Convert`, `PDF`, `Word`, `Excel`, `PowerPoint`, `HTML`,
  and `CSV`; the new description explicitly identifies multi-format Markdown
  conversion and its dependency limits.
- Red: `TestDocumentToolHonorsInvocationCancellation` failed because the
  conversion received a live context after its invocation was canceled.
- Red: the Content Understanding stdin case failed with
  `cloud conversion requires filename`; only Document Intelligence now has
  that requirement, matching upstream CLI behavior.
- Red: the conversion-failure case could not find `oops.OopsError` in the
  returned error chain; failures are now wrapped with document context as
  required by AGENTS.md.
- Green: targeted runner and runtime tests pass using subprocess fixtures;
  every canonical CLI argument is forwarded without a shell, startup detection
  is cached, root/child Collection receives the optional tool, and QC/Research
  does not.
- Refactor: removed the superseded polling, live RPC updates, conditional
  session state, and their tests. No Copilot session changes remain.

## Local Checks

```text
$ go vet ./...
(no output; exit 0)
$ go test ./... -count=1
ok  	github.com/lonegunmanb/r42/cmd/r42	0.391s
ok  	github.com/lonegunmanb/r42/docs/examples	5.508s
ok  	github.com/lonegunmanb/r42/docs/examples/chokepoint	49.686s
ok  	github.com/lonegunmanb/r42/docs/examples/deep-research	6.656s
ok  	github.com/lonegunmanb/r42/docs/examples/morning	16.312s
ok  	github.com/lonegunmanb/r42/docs/examples/secjury	26.624s
ok  	github.com/lonegunmanb/r42/internal/artifact	1.234s
ok  	github.com/lonegunmanb/r42/internal/checkpoint	1.233s
ok  	github.com/lonegunmanb/r42/internal/cli	43.588s
ok  	github.com/lonegunmanb/r42/internal/collection	1.247s
ok  	github.com/lonegunmanb/r42/internal/collectionqc	1.109s
ok  	github.com/lonegunmanb/r42/internal/concurrency	1.525s
ok  	github.com/lonegunmanb/r42/internal/config	0.988s
ok  	github.com/lonegunmanb/r42/internal/coordinator	1.187s
ok  	github.com/lonegunmanb/r42/internal/copilot	1.107s
ok  	github.com/lonegunmanb/r42/internal/copilotprobe	1.872s
ok  	github.com/lonegunmanb/r42/internal/debuglog	1.895s
ok  	github.com/lonegunmanb/r42/internal/e2e	6.913s
ok  	github.com/lonegunmanb/r42/internal/evidence	1.230s
ok  	github.com/lonegunmanb/r42/internal/executor	1.887s
ok  	github.com/lonegunmanb/r42/internal/goldenprobe	1.287s
ok  	github.com/lonegunmanb/r42/internal/mcp	1.509s
ok  	github.com/lonegunmanb/r42/internal/mcp/spec	0.875s
ok  	github.com/lonegunmanb/r42/internal/module	1.478s
ok  	github.com/lonegunmanb/r42/internal/module/spec	3.368s
ok  	github.com/lonegunmanb/r42/internal/plan	1.559s
ok  	github.com/lonegunmanb/r42/internal/progress	1.537s
ok  	github.com/lonegunmanb/r42/internal/project	1.006s
ok  	github.com/lonegunmanb/r42/internal/provider	0.677s
ok  	github.com/lonegunmanb/r42/internal/qc	0.589s
ok  	github.com/lonegunmanb/r42/internal/research/runtime	0.799s
ok  	github.com/lonegunmanb/r42/internal/research/spec	0.722s
ok  	github.com/lonegunmanb/r42/internal/run	1.194s
ok  	github.com/lonegunmanb/r42/internal/s3	0.784s
ok  	github.com/lonegunmanb/r42/internal/s3/spec	0.702s
ok  	github.com/lonegunmanb/r42/internal/scaffold	0.655s
ok  	github.com/lonegunmanb/r42/internal/spec	1.024s
ok  	github.com/lonegunmanb/r42/internal/tool/document	2.431s
ok  	github.com/lonegunmanb/r42/internal/tool/external	1.867s
ok  	github.com/lonegunmanb/r42/internal/tool/gotool	3.840s
ok  	github.com/lonegunmanb/r42/internal/tool/spec	1.328s
ok  	github.com/lonegunmanb/r42/internal/tool/starlarktool	4.075s
ok  	github.com/lonegunmanb/r42/internal/ui	1.595s
ok  	github.com/lonegunmanb/r42/internal/variableschema	0.541s
ok  	github.com/lonegunmanb/r42/internal/workflow	1.489s
$ golangci-lint run
0 issues.
```

All three final checks exited 0 and ran in the order above.

An earlier full test run failed the existing
`TestProductionRuntimeCollectionOnlyStopsSessionOnCancellationAndTimeout/timeout`
when inline Go-tool compilation exceeded its five-second block timeout before
opening a session. The isolated retry passed (`internal/cli 5.294s`), and the
final full run above passed without changing that test. An intermediate lint
failure required using `require.NoError` for the new live-owner precondition;
the final code includes that correction.

## CI Coverage Self-Check

- Diff categories: Go code, Go dependencies, documentation.
- `.github/workflows/ci.yml` runs on `pull_request` with no path exclusions.
  `test` runs `go vet ./...` and `go test ./... -count=1`; `lint` runs
  `golangci-lint run` through the official lint action.
- CI Gap: none.

## Independent Code Review

Final verdict: APPROVED. Total rounds: 2.

Reviewers received explicit source snapshots without conversation inheritance.
They used no terminal or mutation tools; this environment has no standalone
read/search tools. The review completed before committing or opening a PR.

### Final Round Report

```text
Task: DOC-MARKITDOWN
Round: 2 / 10
Verdict: APPROVED
Summary: Both previous findings are resolved by focused tests. The supplied final vet, test, and lint results are green.

Active Findings:
- none

Held Despite Push-Back:
- none

Resolved Since Last Round:
- Oversized stdin and temporary output coverage -> verified at internal/tool/document/runner_test.go:138. Both cases assert the expected limit error, an empty result, and no published workspace output.
- Owner-context cancellation with a live invocation context -> verified at internal/cli/runtime_document_test.go:97. Tests cover both a pre-canceled owner and cancellation during invocation, including propagation to the tool result.

Withdrawn Since Last Round:
- none

Deferred Since Last Round:
- none

TDD Evidence Check:
- Critical behavior coverage: yes. The new tests close the previously identified gaps; unchanged behavior retains the Round 1 coverage assessment.
- At least two tests inspected for arrange/act/assert and testify usage: yes, both new test functions.
- Skipped or commented tests: none in the supplied new diff.
- Vet/test/lint actually run: yes, according to supplied execution evidence. go vet ./... exited 0; retained go test ./... -count=1 output shows all packages passing; golangci-lint run exited 0 with 0 issues.
- These additions exercise existing behavior and introduce no production implementation requiring a new Red step. Original Red evidence was supplied.

Security & Safety:
- No production changes this round. The new tests verify cancellation propagation and prevent oversized output from being published.
- Parallel tests use separate temporary workspaces and invocation contexts; package TestMain uses goleak.
- No new input, error-handling, concurrency, or sensitive-data concern found in the supplied diff.

Out-of-scope Drift:
- none. Changes directly address the previous findings.
- The supplied CI configuration retains PR vet, test, and lint gates without path exclusions.

Round Budget:
- This is round 2 / maximum 10.

Re-review Required: no
```

### Dialog Log

Round 1 reviewer report:

```text
Task: DOC-MARKITDOWN
Round: 1 / 10
Verdict: CHANGES_REQUESTED
Summary: Startup detection, conditional Collection registration, CLI option forwarding, and workspace confinement match the requested behavior. Readily testable size-limit branches lack the coverage required by AGENTS.md section 1.2.

Active Findings:
- [MAJOR] (confidence=high) internal/tool/document/runner.go:100 - The oversized stdin rejection and oversized temporary output-file rejection at line 157 are untested. Neither has a permitted platform limitation, so these branches violate the repository's explicit test gate. Add cases invoking Input{Stdin: strings.Repeat("x", 64<<20+1)} and helperRunner("large") with Output set; assert the appropriate limit error, empty result, and absence of a published workspace output file.
- [MINOR] (confidence=high) internal/cli/runtime_document.go:64 - Owner-context cancellation while an invocation supplies TraceContext lacks coverage. The current cancellation test exercises only a canceled invocation context. Add coverage for a pre-canceled owner and cancellation of a live owner during invocation to verify the context-combination behavior.

TDD Evidence Check:
- Critical behaviors covered: partially. Startup lookup caching, all canonical arguments, metadata operations, conversion errors, input-file limits, stdout limits, typed schema, cancellation, and root/child phase registration have direct tests. The missing cases are identified above.
- At least two tests inspected for arrange/act/assert and testify usage: yes, TestRunnerPassesAllMarkItDownOptions and TestProductionRuntimeRegistersStartupDocumentsInCollection.
- Skipped or commented tests: none in the supplied changed test sources.
- Vet/test/lint run green: yes, based on the supplied execution evidence. Full test stdout was not retained; the reported package excerpts and exit status support the result.

Security & Safety:
- Arguments use exec.CommandContext without a shell. Workspace paths use filepath.IsLocal and os.Root; staged inputs require regular files.
- Output and diagnostic buffers are bounded; temporary resources are cleaned up. Errors retain their causes and receive oops context.
- No shared mutable invocation state or apparent concurrency defect found.
- Canonical upstream source confirms all current CLI options and confirms conversion completes before the output file opens, resolving the temporary input/output basename concern.
- Real installed-CLI and cloud conversion remain untested, explicitly documented.

Out-of-scope Drift:
- No material drift found. Dependencies implement the mandated error convention; documentation and wiring support this task.
- Supplied CI source provides PR vet, test, and lint gates without path exclusions.

Round Budget:
- This is round 1 / maximum 10.

Re-review Required: yes.
```

Implementer responses:

- MAJOR: fix. The explicit repository test requirement applies to both
  readily reproducible limits. Added
  `TestRunnerRejectsOversizedStreamsWithoutPublishingOutput`; it checks the
  limit errors, empty results, and absence of published output files.
- MINOR: fix. Cancellation belongs to this tool's behavior, and the cases
  require only focused tests. Added
  `TestDocumentToolHonorsOwnerCancellationWithInvocationContext` for both
  pre-canceled and live-owner cancellation. These changes cover existing
  implementation and introduce no new production behavior or Red claim.
- Round 2: new independent reviewer verified those source changes and the
  complete final check output, resolved both findings, and returned APPROVED
  (full report above).

## Out of Scope

- Installing MarkItDown or Python, starting an MCP server, polling, and loading
  capabilities into existing sessions after startup.
- Implementing document parsers, OCR, Azure services, or plugin behavior in Go.
- Automated real-service conversion tests: this host has no MarkItDown CLI;
  tests exercise the process contract using isolated executable fixtures.
- OS disk failures and a source growing after its initial stat cannot be
  deterministically reproduced here. Those defensive branches carry
  `// note: untested because ...` explanations.
