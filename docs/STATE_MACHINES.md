# DiOffice v0.1 — Canonical State Machines

**Authority:** These are the only canonical persisted lifecycle states. UI labels and Phaser animations are projections, not additional states. State changes are validated server-side and recorded as durable events/history.

## 1. Task

### States

| State | Meaning | Active worker? | Terminal? |
|---|---|---:|---:|
| `DRAFT` | Owner is editing task details; no execution has been authorized. | No | No |
| `BACKLOG` | Saved but not ready to start. | No | No |
| `READY` | Complete enough to execute; Owner must still explicitly Start. | No | No |
| `PROVISIONING` | Start was accepted; branch/worktree/worker/runtime are being created. | Maybe | No |
| `IN_PROGRESS` | A live execution attempt is working. | Yes | No |
| `WAITING_APPROVAL` | Execution is paused at a specific pending, operation-scoped approval. | Paused | No |
| `BLOCKED` | Cannot safely continue until an Owner resolves an external/policy/configuration blocker. | No | No |
| `IN_REVIEW` | PR/evidence/checks are ready for Owner review. | No | No |
| `DONE` | Owner accepted the exact candidate SHA. This does **not** mean merged. | No | Yes* |
| `FAILED` | Attempt exhausted/failed; Owner must Retry or Cancel. | No | No** |
| `CANCELED` | Owner canceled the task; no further execution is allowed. | No | Yes |

`DONE` can return to `IN_REVIEW` if the PR head SHA changes after approval or the Owner explicitly reopens the task. `FAILED` can return to `READY` only through an explicit Owner Retry. `CANCELED` is terminal; create a new task to restart canceled work.

### Allowed transitions

| From | To | Trigger / guard |
|---|---|---|
| — | `DRAFT` | Owner creates task; generate stable task ID before emitting events. |
| `DRAFT` | `BACKLOG` | Owner saves draft without scheduling it. |
| `DRAFT` | `READY` | Owner confirms title, description, acceptance criteria, repository, and execution manifest. |
| `BACKLOG` | `READY` | Owner confirms all required task details. |
| `READY` | `PROVISIONING` | Explicit Owner Start; idempotency key required; no other active Deni attempt. |
| `PROVISIONING` | `IN_PROGRESS` | Workspace and runtime health checks pass and runtime confirms session started. |
| `PROVISIONING` | `WAITING_APPROVAL` | A specific policy gate requires Owner approval before an operation proceeds. |
| `PROVISIONING` | `BLOCKED` | Recoverable setup/configuration or external dependency blocker. |
| `PROVISIONING` | `FAILED` | Non-retryable error or retry budget exhausted. |
| `IN_PROGRESS` | `WAITING_APPROVAL` | An operation is paused before execution pending scoped approval. |
| `IN_PROGRESS` | `BLOCKED` | Explicit blocker, lost prerequisite, or recoverable infrastructure failure. |
| `IN_PROGRESS` | `IN_REVIEW` | Required checks pass for candidate SHA, PR exists and points at that SHA, and review evidence is persisted. |
| `IN_PROGRESS` | `FAILED` | Non-retryable error or retry budget exhausted. |
| `IN_PROGRESS` | `CANCELED` | Owner Cancel; terminate runtime/container and record outcome. |
| `WAITING_APPROVAL` | `IN_PROGRESS` or `PROVISIONING` | Approval granted for the exact action digest before expiry; resume prior phase. |
| `WAITING_APPROVAL` | `BLOCKED` | Approval denied, expired, or canceled; the gated operation must not execute. |
| `WAITING_APPROVAL` | `CANCELED` | Owner cancels task. |
| `BLOCKED` | `READY` | Owner resolves blocker and explicitly Retry; create new execution attempt. |
| `BLOCKED` | `CANCELED` | Owner cancels. |
| `IN_REVIEW` | `IN_PROGRESS` | Owner requests changes; preserve task branch/workspace and create a new attempt/session if resume is unavailable. |
| `IN_REVIEW` | `DONE` | Owner approves the displayed PR head SHA and evidence. Approval is recorded against SHA and policy version. |
| `IN_REVIEW` | `BLOCKED` | Review cannot safely proceed (e.g. repository/PR unavailable); retain evidence. |
| `IN_REVIEW` | `CANCELED` | Owner cancels; preserve PR/branch state and record that it remains externally open if applicable. |
| `DONE` | `IN_REVIEW` | PR head changed after approval or Owner explicitly reopens; previous approval becomes stale. |
| `FAILED` | `READY` | Explicit Owner Retry; create a new attempt, never silently resume an ambiguous worker. |
| `FAILED` | `CANCELED` | Owner cancels. |

All other transitions are rejected with a stable `INVALID_STATE_TRANSITION` error. Mutating commands carry an idempotency key and expected aggregate version. State changes, history, and outbox events commit atomically.

### Task completion and merge semantics

- `DONE` means the Owner accepted the current PR head SHA; `DONE` is not a claim that GitHub merged the PR.
- Approve and Merge are separate actions. Merge is only available to an authorized Owner, after approval is still valid, and requires a separate confirmation.
- Any new head SHA invalidates approval and returns a completed-but-open task to `IN_REVIEW` for review. Do not auto-approve a later commit.
- PR status (`OPEN`, `MERGED`, `CLOSED`, `DRAFT`) is a separate lifecycle from task status.

## 2. Employee

Employee state is a backend-owned projection of the active task/attempt and recent normalized facts. It is not written by Phaser or inferred from elapsed time.

| State | Meaning / deterministic source |
|---|---|
| `IDLE` | No active task/attempt. |
| `PLANNING` | Attempt is active; runtime is planning or inspecting, with no edit/test/preview fact yet. |
| `RESEARCHING` | Read/search activity or browser investigation is active. |
| `CODING` | Workspace file creation/modification/deletion is active. |
| `TESTING` | A required or configured check is running. |
| `PREVIEWING` | Preview server/readiness/screenshot operation is running. |
| `WAITING_APPROVAL` | Task has a pending operation-scoped approval; runtime is paused. |
| `BLOCKED` | Task is blocked pending Owner/system resolution. |
| `WAITING_REVIEW` | Task is `IN_REVIEW`; Deni is not actively running. |
| `ERROR` | Most recent execution attempt failed and task is `FAILED`; this is visible until Owner retries/cancels. |
| `DONE` | Task has just transitioned to `DONE`; short success presentation, then `IDLE`. Persisted task remains `DONE`. |

Transitions are derived from task/attempt/approval state plus normalized events. Precedence when multiple facts exist: `WAITING_APPROVAL` > `BLOCKED`/`ERROR` > `WAITING_REVIEW` > active operation (`TESTING`, `PREVIEWING`, `CODING`, `RESEARCHING`, `PLANNING`) > `DONE` presentation > `IDLE`. Unknown runtime activity never creates a guessed state; retain the last valid state and surface an unknown-activity indicator.

## 3. Execution attempt

Every Start/Retry creates a new immutable `attempt_id`. A same-workspace revision may reuse the workspace and branch, but never overwrites attempt/session history.

| State | Meaning |
|---|---|
| `CREATED` | Attempt record committed. |
| `PROVISIONING` | Workspace/container/runtime setup is underway. |
| `RUNNING` | Runtime session is confirmed active. |
| `WAITING_APPROVAL` | Paused at a specific approval gate. |
| `SUCCEEDED` | Required execution work and evidence reached the requested handoff point. |
| `FAILED` | Attempt ended with a durable error. |
| `CANCELED` | Owner cancellation was acknowledged; cleanup follows. |
| `ABORTED` | Reconciler stopped a duplicate/orphan attempt; never report success. |

Only one active (`CREATED`, `PROVISIONING`, `RUNNING`, `WAITING_APPROVAL`) attempt per task/employee is allowed. Enforce this in storage, not only in application logic.

## 4. Workspace

`PROVISIONING → READY → IN_USE → RETAINED → CLEANUP_PENDING → CLEANED`. Any provisioning/cleanup operation may enter `FAILED` and be retried by a reconciler. A revision reuses the task branch/worktree; containers are per active attempt. Never destroy the retained worktree while the PR is open or review evidence is still required. Cleanup policy is defined in `docs/EXECUTION_MANIFEST.md`.

## 5. Approval

`PENDING → APPROVED | DENIED | EXPIRED | CANCELED`. An approval is single-use and bound to `{task_id, attempt_id, action_type, action_digest, policy_version, expires_at}`. Replays, a changed action digest, expiry, or a changed PR head cannot reuse approval. A denied/expired operation does not execute and leaves the task `BLOCKED` for Owner resolution.

## 6. Pull request

`NOT_CREATED → OPEN/DRAFT → MERGED | CLOSED`. `OPEN ↔ DRAFT` may change through GitHub. A PR head/base update emits `pull_request.updated`; a head update invalidates the matching approval and may move task `DONE → IN_REVIEW`. Merge requires a distinct Owner action and current, non-stale approval. The PR's state is not inferred from a `git.push` event; verify through GitHub API/webhook and reconcile.

## 7. Required invariants

1. Draft creation never allocates a worker or starts a runtime.
2. One task has at most one active attempt and one writable worktree.
3. Only the authenticated Owner can start, retry, cancel, override a failed check, approve, or merge.
4. A required check is valid only for the candidate commit SHA it tested.
5. Review approval is valid only for the currently displayed PR head SHA and policy version.
6. Start/Retry/Cancel/Approve/Merge are idempotent; stale expected versions are rejected.
7. Worker/session failure is never mapped to `DONE` or `IDLE` without a durable outcome.
8. Task state and event history are backend truth; UI and office are replayable projections.
