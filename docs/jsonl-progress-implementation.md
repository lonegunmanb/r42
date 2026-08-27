# JSONL Progress UI Implementation Plan

Status: approved design; P1-T15 through P1-T23 implemented

This plan decomposes the `r42 apply --ui=jsonl` contract in
[design.md](design.md#151-jsonl-machine-progress-ui) into single-purpose pull
requests. Each task must follow the repository TDD, local-check, CI-coverage,
and independent-review requirements in `AGENTS.md`.

## Completed

- **P1-T15** — Versioned protocol types and schema-1 encoder, with golden
  fixtures, delivered in `internal/progress` (frames, envelope, records, enum
  validation, line-oriented `Encoder`).
- **P1-T16** — Bidirectional `hello`/`select`/`ready` handshake with the fixed
  5-second pre-Plan timeout, injectable I/O and time, and a returned encoder.
- **P1-T17** — CLI `--ui=jsonl` mode: stdout reserved for the protocol,
  pretty Plan/output suppressed, stderr diagnostics retained, `--debug` does
  not alter stdout privacy, invalid UI values remain usage errors.
- **P1-T18** — Sanitized current-state projector in `internal/progress`:
  builds the initial expanded DAG snapshot (including nested modules), tracks
  per-node phase/status/activity/safe tool name and deduplicated aggregate
  token usage, strips terminal control characters, and bounds every summary
  to 4096 UTF-8 bytes. The projector is the JSONL privacy boundary: prompts,
  variables, tool arguments/results, process streams, and SDK payloads never
  reach a projection.

  Deferred to P1-T20 (reviewer-approved): `Phase` is currently written from
  `event.Session` on every observed event; `block.apply` lifecycle events do
  not carry `Session`, so phase is populated by `assistant.*`/`tool.*` events
  (matching the TUI projector). The publisher wiring in P1-T20 consumes
  `session.send` events that do carry `Session`.
- **P1-T19** — Dynamic nodes and best-effort timeline in `internal/progress`:
  the projector materializes `dynamic.tasks.materialized` into canonical
  dynamic task addresses (`parent.tasks[i]`) without duplicating re-announced
  task sets, produces complete-replacement node upserts (never deltas), and
  exposes per-block activity summaries via `Timeline`. Timeline history is not
  retained: `Timeline` derives the latest record from the projection and
  returns `nil` before a block has any assistant-derived summary, and the node
  map stays `O(number of blocks)` as timeline volume grows.
- **P1-T20** — Bounded asynchronous publisher in `internal/progress`: a
  `Publisher` subscribes to the synchronous event bus and writes records to
  the negotiated encoder behind a bounded worker. `Observe` never blocks a
  stalled consumer; under pressure it coalesces `node_upsert` per address
  (keeping the newest pending state), drops oldest `timeline_append` records
  first, and gives structural/terminal records priority over timeline records.
  Writes are serialized by the single worker, `Close` is bounded by a drain
  timeout, and a write failure disables further publication with at most one
  warning (never affecting research).
- **P1-T21** — Terminal projection: the publisher emits exactly one best-effort
  completed, failed, or canceled record after Apply; failure summaries are
  sanitized, and stdout failures remain stderr-only diagnostics.
- **P1-T22** — Compatibility enforcement: every advertised major has a
  distinct encoder and complete fixture set; a new major must retain the
  immediately preceding major.
- **P1-T23** — Worker-facing coverage and documentation: subprocess tests
  exercise negotiation, static/dynamic progress, cancellation, closed/slow
  stdout, timeline capping, and exit-status reconciliation; README documents
  the worker contract.

## Assumptions

- r42 and its supervising worker communicate through local process pipes; the
  transport is reliable enough that retransmission and acknowledgements are not
  required.
- Progress is observational and best effort. Missing, reordered, or dropped
  events do not change research execution or its result.
- The first released event schema major is 1. The requirement to retain one
  previous major starts when schema major 2 is introduced; schema 0 is not
  invented for the initial release.
- Report identification, report upload, S3 credentials, and S3 lifecycle are
  owned by a future root block and are outside every task below.

## Delivery Rules

- Implement tasks in dependency order. Each row is one PR and uses the listed
  `P1-T##` ID in its issue, branch name, TDD evidence, and reviewer hand-off.
- Do not serialize `debuglog.Event` directly. New output must pass through the
  allowlisted projection and the encoder for the negotiated schema major.
- Keep queue sizes and post-negotiation flush bounds as internal, tested
  constants. Do not add user-facing tuning flags in this sequence.
- Before each PR, run `go vet ./...`, `go test ./... -count=1`, and
  `golangci-lint run`, verify the Go PR workflow covers the changed paths, and
  obtain the required independent `code-reviewer` verdict.

## Tasks

| ID | Deliverable | In scope | Depends on | Required Red test / evidence | Acceptance criteria |
| --- | --- | --- | --- | --- | --- |
| P1-T15 | Versioned protocol types and schema-1 encoder | Define handshake frames, common event envelope, schema-1 node/timeline/terminal records, enum validation, and line-oriented encoding behind a schema-specific encoder registry. Add golden contract fixtures. | None | Encoder tests fail because protocol types and schema-1 fixtures do not exist; malformed or unsupported versions are not rejected. | Every advertised frame has a deterministic NDJSON fixture; unknown optional fields can be ignored by a fixture consumer; invalid required fields and unsupported majors fail; raw debug event types are not accepted by the encoder API. |
| P1-T16 | Bidirectional handshake | Implement `hello`/`select`/`ready`, highest-common selection by the worker, exact advertised-version validation by r42, flushes, and the fixed 5-second pre-Plan timeout using injectable I/O and time in tests. | P1-T15 | Table tests fail for successful selection, malformed input, unsupported selection, EOF, timeout, and write/flush failures. | Plan/Apply is not entered before `ready`; all failure paths return nonzero without starting research; stdin is not used after negotiation; the selected encoder is passed to the run. |
| P1-T17 | CLI mode and stdout contract | Accept `--ui=jsonl`, keep existing mode resolution unchanged, route stdout exclusively to the protocol, retain stderr diagnostics, and suppress pretty Plan/output documents only in JSONL mode. | P1-T16 | CLI tests show `jsonl` is rejected or that Plan/output JSON contaminates protocol stdout. | `auto`, `tui`, and `repl` behavior is unchanged; every JSONL stdout line decodes as a protocol frame; `--debug` does not alter stdout privacy; invalid UI values remain usage errors. |
| P1-T18 | Sanitized current-state projector | Build the initial sanitized DAG snapshot and self-contained per-node projection with phase, status, activity, safe tool name, and deduplicated aggregate token usage. Implement deterministic summaries and the 4096-byte UTF-8 bound. | P1-T15 | Projector tests fail for nested modules, status/phase changes, usage deduplication, terminal-control stripping, truncation, and sensitive-field exclusion. | The node map can reproduce the TUI's DAG/detail selection without the complete Plan; projection state is linear in known blocks; prompts, variables, arguments/results, process streams, SDK payloads, and raw debug content cannot reach encoded records. |
| P1-T19 | Dynamic nodes and best-effort timeline | Project dynamic materialization, canonical dynamic addresses, node upserts, and per-block activity summaries without retaining timeline history in r42. | P1-T18 | Tests fail because dynamic members are absent, addresses are ambiguous, or timeline records accumulate in projector memory. | A consumer can switch among static blocks and dynamic tasks and see current phase/activity plus per-block timeline events; upserts are complete replacements; projector memory remains `O(number of blocks)` as timeline volume grows. |
| P1-T20 | Bounded asynchronous publisher | Subscribe without blocking the synchronous event bus; add bounded buffering, per-address `node_upsert` coalescing, oldest-timeline dropping, structural/terminal priority, serialized writes, and bounded close/flush behavior. | P1-T18, P1-T19 | Deterministic saturation tests block the event publisher, retain stale upserts, or lose priority records before timeline records. | A stalled writer cannot stall research; the newest pending state per node survives pressure; timeline loss and sequence gaps are permitted; shutdown is bounded and race/leak tests pass. |
| P1-T21 | Terminal, failure, and cancellation projection | Emit best-effort `run_completed`, `run_failed`, and `run_canceled`; sanitize failure summaries; retain process-signal cancellation and make process exit authoritative when a terminal frame is absent. | P1-T17, P1-T20 | Lifecycle tests fail for success, Apply failure, worker cancellation, missing terminal writes, and stdout failure after `ready`. | Post-negotiation stdout failure warns at most once and never changes research outcome; cancellation adds no stdin command; a bounded terminal flush cannot hang exit; no terminal record contains outputs or report paths. |
| P1-T22 | Compatibility enforcement | Add advertised-major registry checks and fixture-driven consumer contract tests that require a distinct encoder for every supported major and prevent removal of the immediately previous major when a new major is added. | P1-T15, P1-T21 | Registry tests allow an advertised version without an encoder/fixtures or allow a synthetic next-major change to remove its predecessor. | Initially only schema 1 is advertised; adding schema 2 requires schema-1 and schema-2 encoders and fixtures; additive fields remain compatible; unknown non-critical events are ignored and unknown critical events mark progress incomplete. |
| P1-T23 | Worker-facing end-to-end coverage and documentation | Add subprocess tests for negotiation followed by static and dynamic runs, concurrent blocks, stderr separation, cancellation, slow/closed stdout, and terminal-frame loss. Update CLI help and user-facing command examples. | P1-T17, P1-T19, P1-T20, P1-T21, P1-T22 | E2E tests fail because no complete worker can negotiate, ingest progress, and reconcile the process exit. | A reference test worker reconstructs node state and per-block timelines, caps its simulated browser view at 200 entries per block, and derives final outcome from exit status; all repository checks pass; docs contain no S3/report-output contract. |

## Dependency Sequence

```text
P1-T15* -> P1-T16* -> P1-T17* -> P1-T18* -> P1-T19* -> P1-T20* -> P1-T23
    |          |
    +-> P1-T21 -> P1-T22 --+
```

`*` = implemented (P1-T15 through P1-T23).

P1-T18 may start after P1-T15 while P1-T16 is in progress. P1-T20 waits for both
projection tasks so queue policy is tested against real record categories rather
than a second temporary event model.

## Out of Scope

- TCP listeners, network authentication, TLS, acknowledgements, replay, or event
  retransmission.
- Pause, resume, persisted checkpoints, or restarting an interrupted Apply.
- Browser/backend APIs, database selection, deployment topology, and retention
  beyond the agreed 200-entry browser window.
- Report discovery, final-report fields in terminal events, and S3 upload.
