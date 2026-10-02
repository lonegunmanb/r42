# Checkpoint Recovery Implementation

Status: P1-T24 complete

Windows checkpoint directory-sync compatibility fix: complete.

## Assumptions

- A registered artifact is modified by at most one session at a time, and a
  subsequent session does not begin until that session reaches a handoff.
- Recovery runs on the original machine and from the original run directory.
- Configuration and prompt changes remain compatible with the saved plan.
- A checkpoint is the transaction boundary for a static research block or one
  materialized dynamic research task; a Copilot session and workflow phase are
  implementation details and never correctness boundaries.

## P1-T24 - Durable Research Checkpoints

Persist a `started` snapshot before the first session and a `completed` snapshot
after the last required phase. Each snapshot contains the artifact registry,
workspace contents, and (for completion) the block/task result. Session files,
phase state, and quotas are not recovery boundaries.

Success criteria:

1. A partially written checkpoint is never selected for recovery.
2. Resume restores completed units and skips them; an unfinished unit restores
   its pre-unit snapshot and starts fresh from its first phase.
3. Registered artifact metadata and filesystem content match the checkpoint
   after recovery, including additions, changes, deletions, and renames.
4. The resumed block timeout begins again at zero.
5. Static and materialized dynamic workflows resume without re-evaluating an
   already materialized dynamic task list. A dynamic block that had not begun
   at interruption evaluates normally on resume.

## Limits

- Recovery restores each block/task checkpoint in the original run. Completed
  units are skipped; interrupted units are rolled back and rerun. This does not
  provide cross-run reuse.
- `collection_only` follows the same whole-unit transaction boundary, even
  though it has no internal workflow handoff.
- The checkpoint snapshot only controls local files and host state. Calls to
  external services must be independently idempotent.
- Windows syncs checkpoint files before publication but skips directory sync:
  the read-only directory handles opened by Go cannot be flushed. Directory
  metadata therefore has no explicit sync guarantee against power loss.

## Block/task recovery protocol

The host records completion only after a block or materialized task has
committed its outputs and final artifact state. Intermediate workflow handoffs
do not complete a unit. Changes after unit start are rolled back on resume.

Dynamic materialization has a separately versioned completed checkpoint before
the dynamic block is scheduled. On resume its saved task list is loaded rather
than evaluated again, including when no generated task had started yet.

Resume may change operational settings such as timeout, parallelism, and
provider credentials. Block addresses, dependencies, task identities, artifact
declarations, and prompt/configuration identity must remain compatible with the
saved plan; incompatible changes fail with a plan compatibility error.

Independent units can complete concurrently. Each unit snapshot covers only
its own workspace and registry entries, so resuming one does not overwrite a
different completed dynamic task.
