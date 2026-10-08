# DiOffice Agent Runtime Specification v0.1

**Authority:** `PRD.md` defines product behavior; `STATE_MACHINES.md` defines canonical states; event names/payloads are in `EVENT_SCHEMA.md` and `schemas/event-envelope.schema.json`. This document cannot introduce alternate states/events.



## Purpose

The v0.1 build uses OpenCode as the only runtime adapter. Keep employee identity provider-independent; adding a runtime selector/router is later scope.

## Employee vs runtime

Employee is permanent.
Runtime is replaceable.

Example:

Deni -> Runtime Router -> OpenCode

Later:

Deni -> Runtime Router -> Codex

Deni remains the same employee with the same role, task history, permissions, and context.

## v0.1 runtime

OpenCode is the only required implementation for initial end-to-end validation.

## Core execution interface

Conceptual methods (provider implementation is not an external contract):

- startTask(task, workspace)
- sendMessage(sessionId, message)
- pause(sessionId)
- resume(sessionId)
- cancel(sessionId)
- getStatus(sessionId)
- streamEvents(sessionId)

Commands, lifecycle, event envelopes, authorization, idempotency, and approvals are owned by DiOffice; the runtime adapter cannot grant permissions.

## Gateway readiness probe

The agent gateway exposes `GET /healthz` for process liveness and `GET /readyz` for an OpenCode preflight. Readiness requires `OPENCODE_SERVER_URL` to be a local HTTP(S) origin and the upstream `GET /global/health` response to contain `healthy: true`; redirects, remote origins, URL credentials, paths, query strings, and fragments are rejected. The gateway returns only a generic readiness state and never reflects the URL or upstream payload.

This probe confirms only that OpenCode responds. It does not validate a repository manifest, worker image, sandbox, task, or credentials, and it does not create a runtime session or authorize Start. Start remains unavailable until the full execution policy and isolated worker are implemented.

### Local project-folder action

The gateway also exposes an internal, loopback-only project-folder bridge for an authenticated Owner action. The API verifies organization/project scope and CSRF before forwarding a fixed `explorer` or `vscode` choice with its server-side gateway token. The gateway resolves the project UUID through the explicit local `AGENT_GATEWAY_PROJECT_FOLDERS` map, confirms the canonical path is a directory, and launches a fixed executable with `shell: false`. It never accepts a filesystem path from the browser or returns the configured path. This opens a folder only; it is not workspace provisioning, task execution, or permission to access arbitrary host files. The bridge is disabled without a sufficiently long internal token and an explicit project mapping.

## Runtime session

A session is linked to:
- employee_id
- task_id
- project_id
- workspace_id
- runtime_type
- runtime_session_id
- status
- started_at
- ended_at

## Event normalization and state derivation

Normalize provider output only to the registry in `EVENT_SCHEMA.md` and validate it against `schemas/event-envelope.schema.json`. Do not emit legacy aliases (`agent.thinking`, `test.passed`, `browser.screenshot`, `git.pr_created`) or store hidden reasoning. Use `agent.message` only for concise owner-visible progress/result/question summaries. File operations use `file.activity`; checks use `check.*`; screenshots use `preview.screenshot_captured`.

Only backend transition commands own task/employee state. Runtime events may inform derived state but cannot mark a task Done or bypass `STATE_MACHINES.md`. Active checks map to `TESTING`, preview capture to `PREVIEWING`, pending approval to `WAITING_APPROVAL`, PR ready to `WAITING_REVIEW`, and a failed terminal attempt to `ERROR`.

## Context supplied to runtime

Minimum context:
- Employee identity and role
- Task title and description
- Project rules
- Repository path
- Allowed commands/tools
- Relevant owner instructions
- Required completion criteria

Do not blindly send all company memory or all historic events.

## Permissions

Runtime execution must be bounded by DiOffice policy.

Deni v0.1:
- Repository read/write in task workspace: allowed
- Local package install: configurable/approval-sensitive
- Local test/build: allowed
- Commit/push task branch: allowed
- Create PR: allowed
- Merge: owner approval
- Production deploy: denied
- Host filesystem: denied
- Unscoped secrets: denied

## Steering

Owner can send a message to an active employee session.

Example:

> Jangan gunakan gradient di chart.

The instruction is appended to task/session context and sent to the current runtime without creating a separate task.

## Failure handling

Runtime failures must not be disguised as progress.

On failure:
- Persist error
- Mark session failed or blocked
- Update task state if needed
- Notify owner
- Allow Retry (new attempt), Send Instruction, or Cancel. Runtime change is not in v0.1.

## Future routing policy

Not required for v0.1, but the abstraction must support:

- Light -> OpenCode/local
- Medium -> OpenCode
- Heavy -> Codex
- Long autonomous/special -> Devin

Owner override must always be possible.
