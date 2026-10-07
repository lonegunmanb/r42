# S3 Run ID Subfolder

## Task

| ID | Goal | Status |
| --- | --- | --- |
| P1-T27 / S3-RUN-ID | Add an opt-in run ID subfolder to `s3_folder` uploads. | complete |

Requested in the current conversation; no issue link was supplied.

## Assumptions

- The option is named `use_run_id_subfolder` and defaults to `false`.
- When enabled, the effective prefix is `<prefix>/<run-id>` or just `<run-id>`
  when the configured prefix is empty.
- The ID comes from the existing active run, including saved-plan and resumed
  execution. This task does not add a new HCL function.
- Uploads, rollback diagnostics, and result prefix/root use the effective prefix.

## Plan

1. Add failing configuration, saved-plan, upload, no-op, and rollback tests.
2. Carry the option through schema/snapshots and resolve the prefix once at Apply.
3. Document the option, run local checks, and obtain independent review.

## TDD Evidence

- Red: `TestRuntimeS3FolderRunIDSubfolder` failed with `Unsupported argument;
  An argument named "use_run_id_subfolder" is not expected here.` The snapshot
  and value tests also failed to compile with `unknown field UseRunIDSubfolder`.
- Green: add the optional bool to HCL/config/value and saved-plan snapshots;
  resolve the effective prefix once when constructing the Apply node.
- Covered cases: omitted/false/true, nested file paths, empty prefix, empty
  source, saved-plan round trips, legacy snapshots, upload failure, and rollback.
- Refactor: gofmt only; reuse existing upload, rollback, and result code.

## Local Checks

Mandatory checks all exited 0, in the required order, on base `2a23f44`:

```text
$ go vet ./...
$ go test ./... -count=1
ok  	github.com/lonegunmanb/r42/cmd/r42	0.632s
ok  	github.com/lonegunmanb/r42/docs/examples	6.971s
ok  	github.com/lonegunmanb/r42/docs/examples/chokepoint	41.635s
ok  	github.com/lonegunmanb/r42/docs/examples/deep-research	6.061s
ok  	github.com/lonegunmanb/r42/docs/examples/morning	15.204s
ok  	github.com/lonegunmanb/r42/docs/examples/secjury	21.157s
ok  	github.com/lonegunmanb/r42/internal/artifact	1.246s
ok  	github.com/lonegunmanb/r42/internal/checkpoint	1.331s
ok  	github.com/lonegunmanb/r42/internal/cli	34.194s
ok  	github.com/lonegunmanb/r42/internal/collection	1.041s
ok  	github.com/lonegunmanb/r42/internal/collectionqc	0.913s
ok  	github.com/lonegunmanb/r42/internal/concurrency	0.997s
ok  	github.com/lonegunmanb/r42/internal/config	1.044s
ok  	github.com/lonegunmanb/r42/internal/coordinator	0.670s
ok  	github.com/lonegunmanb/r42/internal/copilot	0.888s
ok  	github.com/lonegunmanb/r42/internal/copilotprobe	2.155s
ok  	github.com/lonegunmanb/r42/internal/debuglog	1.008s
ok  	github.com/lonegunmanb/r42/internal/e2e	6.289s
ok  	github.com/lonegunmanb/r42/internal/evidence	0.861s
ok  	github.com/lonegunmanb/r42/internal/executor	1.326s
ok  	github.com/lonegunmanb/r42/internal/goldenprobe	0.910s
ok  	github.com/lonegunmanb/r42/internal/mcp	0.980s
ok  	github.com/lonegunmanb/r42/internal/mcp/spec	0.844s
ok  	github.com/lonegunmanb/r42/internal/module	0.884s
ok  	github.com/lonegunmanb/r42/internal/module/spec	3.000s
ok  	github.com/lonegunmanb/r42/internal/plan	1.331s
ok  	github.com/lonegunmanb/r42/internal/progress	1.375s
ok  	github.com/lonegunmanb/r42/internal/project	0.726s
ok  	github.com/lonegunmanb/r42/internal/provider	1.025s
ok  	github.com/lonegunmanb/r42/internal/qc	1.154s
ok  	github.com/lonegunmanb/r42/internal/research/runtime	1.206s
ok  	github.com/lonegunmanb/r42/internal/research/spec	1.207s
ok  	github.com/lonegunmanb/r42/internal/run	1.355s
ok  	github.com/lonegunmanb/r42/internal/s3	1.378s
ok  	github.com/lonegunmanb/r42/internal/s3/spec	0.959s
ok  	github.com/lonegunmanb/r42/internal/scaffold	0.949s
ok  	github.com/lonegunmanb/r42/internal/spec	1.101s
ok  	github.com/lonegunmanb/r42/internal/tool/document	1.368s
ok  	github.com/lonegunmanb/r42/internal/tool/external	1.927s
ok  	github.com/lonegunmanb/r42/internal/tool/gotool	2.879s
ok  	github.com/lonegunmanb/r42/internal/tool/spec	1.216s
ok  	github.com/lonegunmanb/r42/internal/tool/starlarktool	3.222s
ok  	github.com/lonegunmanb/r42/internal/ui	0.867s
ok  	github.com/lonegunmanb/r42/internal/variableschema	0.417s
ok  	github.com/lonegunmanb/r42/internal/workflow	0.640s
$ golangci-lint run
0 issues.
```

Additional focused race check could not run on this Windows host:

```text
$ go test -race ./internal/cli ./internal/s3/spec -run 'TestRuntimeS3FolderRunIDSubfolder|TestFolderPlan.*RunIDSubfolder|TestS3FolderValueExposesRunIDSubfolderOption' -count=1
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```

CGO is disabled and neither GCC nor Clang is installed. CI's Linux `test` job
runs `go test ./... -race -shuffle=on -count=1`. No test was skipped or disabled.

## CI Coverage Self-Check

- Diff categories: Go code and documentation.
- `.github/workflows/ci.yml`: `test` runs vet, tests, and race tests;
  `lint` runs golangci-lint. Both run on `pull_request` without path exclusions.
- CI Gap: none.

## Independent Code Review

Final verdict: APPROVED. Total rounds: 1.

The reviewer ran independently without implementation conversation history and
with no execute or write tools. Because native read/search tools were unavailable,
the hand-off supplied repository source snapshots, the production diff, changed
tests, workflow source, and actual validation output for read-only inspection.

### Final Round Report

```text
Task: S3-RUN-ID
Round: 1 / 10
Verdict: APPROVED
Summary: The supplied source and tests cover the requested option, consistent upload/result/error prefixes, and saved-plan preservation of the original run identity. No blocking defects found within the task diff.

Active Findings:
- none

TDD Evidence Check:
- Key behavior coverage: yes. `TestRuntimeS3FolderRunIDSubfolder` covers omitted/false/true options, empty prefix, empty source, upload failure and rollback, and save/load before application. Snapshot tests cover true/false round trips and legacy defaults; schema tests verify the exposed bool.
- Skipped/commented tests: no, according to the supplied changed-source snapshots and search evidence.
- Actual green vet/test/lint: yes. Supplied final outputs show `go vet ./...`, `go test ./... -count=1`, and `golangci-lint run` passing in the required order. Red evidence demonstrates unsupported HCL arguments and missing Go fields before implementation.
- CI coverage: existing unrestricted `pull_request` jobs cover vet, tests, race tests and lint. No CI gap identified.

Security & Safety:
- The effective prefix is computed once from the validated prefix and active run ID, then shared by uploads, failure diagnostics and published results.
- Saved plans retain the option; application reopens the recorded run directory, preserving its identity.
- No new credential exposure, external-input execution, shared production state, or error-handling changes identified.
- Parallel tests use isolated fixtures and clients; mock mutations are synchronized. Local race execution was unavailable because CGO was disabled; existing Linux PR CI supplies race coverage.

Out-of-scope Drift:
- none. Documentation describes the implemented behavior; changes remain within S3 option propagation, runtime prefix construction and focused verification.

Round Budget:
- This is round 1 / maximum 10 rounds.

Re-review Required: no
```

### Dialog Log

- Round 1: reviewer returned APPROVED with no active findings. Implementer
  checked the report against the prefix construction, saved-plan propagation,
  upload/rollback/no-op tests, and CI gates; no corrective changes were needed.

## Out of Scope

- Changing example defaults or adding a new HCL `run_id()` function.
- New storage services, upload scheduling, credential behavior, or recovery rules.
