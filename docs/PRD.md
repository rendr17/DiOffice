# DiOffice — Product Requirements Document

- **Version:** v0.1 canonical baseline
- **Status:** Implementation baseline
- **Authority:** Sole authoritative product requirements document. Specific contracts live in the linked state-machine, event-schema, and execution-manifest documents.

> `README_PRD.md` and `docs/DiOffice_PRD_MVP_v0.1.md` are superseded legacy copies. Do not implement from them. Update this document first when product behavior changes.

## 1. Product vision

DiOffice is an employee-first operating system for a virtual software company. An Owner delegates work to persistent AI employees with stable identities, roles, permissions, work history, and workstations. A runtime such as OpenCode is a replaceable tool used by an employee; it is not the employee identity.

The product combines company management (projects, tasks, review, approvals, audit), real coding execution (isolated Git worktree and worker), and a pixel office that reflects real persisted state/events rather than simulated progress.

The v0.1 MVP proves one trustworthy workflow with one Owner, one project/repository, and Deni (Frontend Engineer).

## 2. Product principles

- **Employee-first:** Owners assign work to Deni, not a provider session.
- **Truthful observability:** Visible activity is backed by persisted state or normalized event.
- **Human authority:** Creating a task never starts compute. Owner explicitly starts work; protected operations require action-specific approval.
- **Safe by default:** Repository code is untrusted. Docker plus worktree alone is not a sufficient security boundary.
- **Runtime-agnostic domain:** OpenCode is the only v0.1 adapter; product contracts do not depend on it.
- **Backend truth:** React and Phaser are replayable projections, not sources of state.
- **Small vertical slice:** Multi-employee concurrency, meetings, autonomous delegation, and runtime routing are later scope.

## 3. v0.1 limits

- One Owner organization, one active project/repository, one persistent employee: Deni.
- At most one active Deni execution. Other tasks may queue FIFO in `READY`, but never auto-start.
- One GitHub repository with a valid, reviewed `.dioffice/execution.json` and an operator-allowlisted worker profile.
- OpenCode only; no Codex/Devin selection or automatic runtime routing.
- Browser experience is static Playwright screenshots, not an interactive remote browser.

## 4. Core task workflow

1. **Connect and verify:** Owner signs in, connects GitHub repository, selects default branch, configures OpenCode, and verifies repository/manifest/worker/runtime health.
2. **Draft:** Owner targets Deni and enters an instruction. DiOffice creates `DRAFT`; Owner reviews title, description, acceptance criteria, project manifest, and required checks. No branch, worker, session, or compute starts.
3. **Queue:** Owner saves as `BACKLOG` or confirms as `READY`. Start is a separate explicit action.
4. **Start:** Owner explicitly starts a `READY` task. A durable idempotent command provisions branch/worktree/worker/runtime. Task enters `PROVISIONING`, then `IN_PROGRESS` only after runtime confirms session start.
5. **Work and observe:** Persist normalized events before delivery. Workstation shows sanitized activity/terminal, changed files/diff, check results, screenshot artifacts, and Git/PR state. Employee animation comes from canonical backend state.
6. **Check gate:** Required checks must pass on the exact candidate commit SHA. Failure blocks review. Owner override is explicit, reasoned, audited, and bound to the same SHA.
7. **Review:** Deni pushes the branch and creates/updates one PR idempotently. Task enters `IN_REVIEW` only when PR and check evidence refer to the same candidate SHA.
8. **Revise:** Owner requests changes; task returns to `IN_PROGRESS` on the same branch/workspace. Resume the same runtime session when supported; otherwise create a new attempt/session on the same workspace.
9. **Approve:** Owner accepts the displayed PR head SHA and evidence. Task becomes `DONE`; approval never merges. A later PR head change invalidates approval and reopens review.
10. **Merge separately:** Merge is an explicit, separately authorized Owner action. A task may be `DONE` while its PR is still open; `DONE` means accepted work, not merged code.
11. **Retain/clean:** Stop containers after the attempt. Retain branch/worktree/artifacts while review or an open PR needs them; cleanup is durable, retryable, and observable. See `EXECUTION_MANIFEST.md`.

Legal transitions are defined only in `STATE_MACHINES.md`.

## 5. P0 functional requirements

### Identity, project, and access

- Owner session authentication with expiry/revocation, secure cookies, CSRF protection for mutations, and authorization on APIs, SSE subscriptions, and artifact downloads.
- Derive organization/project scope from authenticated session; enforce tenant-consistent relationships for every tenant-owned record.
- Connect one GitHub repository through a GitHub App; persist immutable provider repository ID and installation identity.
- Health-check GitHub access, default branch, manifest/profile, OpenCode, and worker before enabling Start.
- Use short-lived, repository-scoped GitHub installation tokens. Never expose the App private key to a task container.

### Deni and task management

- Stable Deni employee identity, role, skills, permission policy, runtime default, status, current task, and activity history.
- Draft task from free-form Owner instruction; capture structured acceptance criteria and required checks before Start.
- Board, task detail, FIFO Ready queue, one active Deni execution, explicit Start/Retry/Send Instruction/Request Changes/Approve/Cancel and separate Merge.
- Mutations are authenticated, idempotent, audit-logged, and guarded by expected aggregate version.
- Board labels reflect canonical task states in `STATE_MACHINES.md`; do not introduce UI-only lifecycle states.

### Runtime and safe execution

- Provider-neutral adapter contract and one OpenCode adapter.
- Validate `.dioffice/execution.json` against `schemas/execution-manifest.schema.json` before task Start.
- One task branch and writable worktree; one worker container per active attempt.
- Hardened non-root worker: no privileged mode, no Docker socket, dropped capabilities, read-only root, task-only mounts, resource/time/disk limits, controlled network, and command policy enforced outside the prompt.
- Manifest chooses only an operator-allowlisted worker profile and project commands. It cannot grant host mounts, secrets, privileged access, arbitrary egress, or larger resource budget.
- No production secrets in prompts/workers. Inject only scoped short-lived credentials required for an operation; GitHub credentials stay in control plane.
- Dependency/lockfile changes, destructive actions, permission expansions, exceptional operations, and merge require explicit approval. Production deployment, production DB writes, force-push, repository deletion, and unrestricted host filesystem are disabled.

### Events, evidence, and review

- Use `EVENT_SCHEMA.md` and `schemas/event-envelope.schema.json`; no alternate event names.
- Commit state/history/event/outbox atomically before publication. Use durable project-scoped sequence and SSE `Last-Event-ID`; at-least-once delivery is deduplicated by event ID/sequence.
- Persist required check runs against exact commit SHA. Approval binds to PR head SHA, evidence, and policy version; a changed head invalidates it.
- Workstation tabs: Activity, Diff, Terminal, Browser screenshots, Checks, Git/PR, and Approvals.
- Redact secrets before persistence, logs, artifact creation, and broadcast. Store large output/screenshots privately; authorize every artifact fetch by tenant/task.
- Do not store/display raw chain-of-thought. `agent.message` is only a concise user-visible progress/result/question summary.

### Office, audit, and reliability

- One Phaser room with Deni, desk/computer, nameplate, status indicator. Critical task/review controls work without Phaser.
- Append-only audit records for auth/security and Owner actions: task edit/start/retry/cancel, policy/credential change, approval/override, merge, and cleanup result.
- Every async execution has attempt ID, heartbeat/lease, bounded retries, and startup reconciliation for orphaned containers/worktrees/runtime sessions.
- Cleanup failure remains visible and retryable; it is never silently reported successful.

## 6. Definition of Done

Acceptance tests on a real non-production fixture repository must prove:

1. Draft creation starts no worker/runtime; only explicit Start does.
2. Duplicate Start/Retry cannot create multiple active attempts/worktrees.
3. Deni changes real files in an isolated hardened worker and emits persisted normalized events.
4. API restart and SSE reconnect replay events without state loss or duplicate UI entries.
5. Required checks bind to commit SHA; failure blocks review; override requires Owner/reason and audit record.
6. One PR is created idempotently; revisions reuse the same task branch/workspace and rerun required checks.
7. Approval binds to exact PR head SHA; later head changes invalidate it.
8. Approve never merges; separate Merge is separately authorized and recorded.
9. Runtime/provision/push/PR failure, approval denial/expiry, cancellation, and cleanup failure are visible and recoverable.
10. Secret redaction is tested before persistence/broadcast.
11. Cross-tenant task, event, PR, and artifact reads are denied.
12. UI/office state derives only from persisted facts; no hard-coded activity or fake progress.

## 7. P1 and P2

**P1:** Nabil and Raka; bounded multi-employee concurrency; dependencies/handoffs; Codex adapter/runtime selector; cost/usage view; interactive preview; notifications/approval inbox/dashboard.

**P2:** Devin/automatic routing; meetings; roadmap; company memory; project rooms; autonomous CTO; asset generation; performance scoring; incident/War Room workflows.

## 8. Non-goals for v0.1

No concurrent employees, autonomous delegation, meeting/roadmap/company-memory system, production deployment or DB writes, full remote desktop, interactive browser proxy, unrestricted egress/host access, force-push, repository deletion, Kubernetes, Kafka, or custom game engine.

## 9. Initial non-functional targets

- Persisted event to connected UI p95 ≤ 1 second at 100 events/second for one active project and one Owner client.
- Reconnect resumes from event cursor; cursor expiry returns authorized snapshot plus a new cursor; duplicate delivery is deduplicated.
- Default worker budget per attempt: 2 vCPU, 4 GiB RAM, 10 GiB writable workspace, 30 minutes. Deployment policy may lower limits; any higher cap must be explicit and tested before production.
- Preview viewports: desktop 1440×900 and mobile 375×812 by default; maximum four per run.
- Status uses text/icon as well as color; critical actions are keyboard-accessible and usable without Phaser.

## 10. Canonical document map

| Contract | Authoritative location |
|---|---|
| Product scope and behavior | `docs/PRD.md` |
| Lifecycle states/transitions | `docs/STATE_MACHINES.md` |
| Event names/payload/delivery/redaction | `docs/EVENT_SCHEMA.md` + `docs/schemas/event-envelope.schema.json` |
| Project execution config | `docs/EXECUTION_MANIFEST.md` + `docs/schemas/execution-manifest.schema.json` |
| User journey | `docs/USER_FLOW.md` (summary only) |
| Runtime adapter | `docs/AGENT_RUNTIME_SPEC.md` (summary only) |
| Data model | `docs/DATABASE_SCHEMA.md` (logical draft; not executable DDL) |

If a summary conflicts, the linked contract wins and the summary must be corrected. Schema changes require a version bump and compatibility plan.

## 11. v0.1 build order

1. Freeze PRD and machine-readable contracts; validate examples automatically.
2. Prove a CLI execution spike on a disposable fixture repo: worktree → hardened worker → OpenCode edit → checks → screenshots → commit/push/PR → revision.
3. Implement state/event/attempt/approval/audit persistence, idempotent commands, outbox, replay cursor, and reconciliation.
4. Implement setup/health, repository/runtime connections, security gates, and cleanup before enabling execution.
5. Build API/React task loop and workstation evidence/review flow.
6. Add Phaser only after canonical state reducers pass tests.
7. Run all Definition of Done and failure/recovery/security tests before release.
