# DiOffice v0.1 — Event Schema and Delivery Contract

The envelope and event enum, plus safety-critical payloads, are machine-validated in `schemas/event-envelope.schema.json` (JSON Schema Draft 2020-12, schema version `1.0.0`). The required fields for every event below are normative; implementation conformance tests must cover all event types. State transitions are defined in `STATE_MACHINES.md`. This document specifies persistence, delivery, and security semantics; it must not create alternate event names.

## 1. Event envelope

Every normalized event has these required fields:

| Field | Contract |
|---|---|
| `eventId` | Globally unique UUID generated at the trusted ingestion boundary; deduplicates provider retries. |
| `schemaVersion` | Exact event-contract version, currently `1.0.0`. |
| `eventType` | One enum value in the JSON Schema; never expose provider-specific raw event names. |
| `organizationId`, `projectId` | Tenant/project scope derived and checked server-side. |
| `taskId`, `employeeId`, `attemptId`, `sessionId`, `workspaceId` | Relevant stable resource IDs; nullable only where no resource exists for that lifecycle fact. |
| `streamSequence` | Positive, strictly increasing per project; unique with `projectId`, assigned transactionally by ingestion. |
| `occurredAt` | Source occurrence time in RFC 3339 UTC; informational, not ordering authority. |
| `recordedAt` | Trusted backend persistence time in RFC 3339 UTC. |
| `producer` | Trusted component class (API, workflow, gateway, worker, adapter, GitHub webhook, reconciler). |
| `actor` | Validated owner/employee/system/integration identity that caused the fact. |
| `correlationId`, `causationId` | Trace/cause linkage; causation may be null for a root fact. |
| `data` | Typed payload validated against the event-specific definition in the JSON Schema. |

The project-scoped `streamSequence` is the replay cursor. Do not sort the timeline by provider timestamp or session-local sequence.

## 2. Canonical event registry

The registry lists canonical event names. Machine validation covers the envelope and safety-critical payloads; required data fields for every registered type are listed in the contract table below.

| Family | Event types |
|---|---|
| Task | `task.created`, `task.updated`, `task.state_changed`, `task.instruction_added` |
| Employee/attempt/workspace | `employee.state_changed`, `execution_attempt.state_changed`, `workspace.state_changed` |
| Runtime session | `session.started`, `session.paused`, `session.resumed`, `session.completed`, `session.failed`, `session.canceled` |
| Safe agent summary | `agent.message` |
| Repository activity | `file.activity` |
| Commands | `command.started`, `command.output`, `command.completed`, `command.failed` |
| Checks | `check.started`, `check.passed`, `check.failed`, `check.overridden` |
| Preview | `preview.started`, `preview.screenshot_captured`, `preview.failed` |
| Git/PR | `git.commit_created`, `git.push_completed`, `git.push_failed`, `pull_request.created`, `pull_request.updated` |
| Approval/artifact/audit | `approval.requested`, `approval.resolved`, `artifact.created`, `audit.action_recorded` |

Do not emit legacy aliases such as `agent.started`, `agent.stopped`, `test.passed`, `browser.screenshot`, `git.pr_created`, or `task.completed`. Use the canonical type replacements described above; task transitions use `task.state_changed`, checks use `check.*`, and screenshots use `preview.screenshot_captured`.

### Required payload fields

The following required payload fields are normative for all event types. The JSON Schema validates the envelope, registry, and required payload shapes/fields for every registered event type; application validators additionally enforce cross-field/state-transition rules, redaction, byte limits, URL policy, and tenant/resource relationships. Optional fields must be versioned and documented before use.

| Event type | Required `data` fields |
|---|---|
| `task.created` | `title`, `description`, `assigneeEmployeeId`, `priority`, `initialState=DRAFT`, `acceptanceCriteria`; optional `manifestDigest` |
| `task.updated` | `changedFields[]`, `newVersion` |
| `task.state_changed` | `fromState` (null only for initial state), `toState`, `taskVersion`, `reason` |
| `task.instruction_added` | `instructionId`, `kind` (`initial\|steering\|revision`), sanitized `summary` |
| `employee.state_changed` | `fromState`, `toState`, `reason` |
| `execution_attempt.state_changed` | `fromState`, `toState`, `attemptNumber`, `reasonCode` |
| `workspace.state_changed` | `fromState`, `toState`, `branchName`, `workerProfile` |
| `session.started` | `runtimeType`, `capabilities[]` |
| `session.paused`, `session.resumed`, `session.canceled` | `reason` |
| `session.completed` | `result` (`handoff_ready\|stopped\|no_changes`) |
| `session.failed` | `errorCode`, `retryable` |
| `agent.message` | `kind` (`progress\|result\|question`), `summary` |
| `file.activity` | `operation` (`read\|created\|modified\|deleted`), workspace-relative `path`; optional additions/deletions |
| `command.started` | `commandId`, redacted `displayCommand`, `classification`; optional `approvalId` |
| `command.output` | `commandId`, `stream`, `chunkSequence`, `text`, `redacted=true`; max 32 KiB |
| `command.completed` | `commandId`, `exitCode`, `durationMs` |
| `command.failed` | `commandId`, `errorCode`, `durationMs` |
| `check.started` | `checkRunId`, `checkId`, `name`, `commitSha`, `required` |
| `check.passed` | `checkRunId`, `checkId`, `name`, `commitSha`, `durationMs` |
| `check.failed` | Same as passed plus `failureCode` |
| `check.overridden` | `checkRunId`, `commitSha`, `ownerId`, `reason` |
| `preview.started` | `port`, relative `route`, `viewports[]` |
| `preview.screenshot_captured` | `artifactId`, `commitSha`, relative `route`, `viewport`, `capturedAt` |
| `preview.failed` | relative `route`, `failureCode` |
| `git.commit_created` | `commitSha`, `treeSha`, `branchName`, sanitized `message` |
| `git.push_completed` | `branchName`, `commitSha` |
| `git.push_failed` | `branchName`, `commitSha`, `failureCode` |
| `pull_request.created` | `provider=github`, `number`, `url`, `headSha`, `baseSha`, `state` |
| `pull_request.updated` | `number`, `change`, `headSha`, `state` |
| `approval.requested` | `approvalId`, `actionType`, `actionDigest`, `policyVersion`, `expiresAt` |
| `approval.resolved` | `approvalId`, `outcome`, `reason` |
| `artifact.created` | `artifactId`, `kind`, `mimeType`, `sizeBytes`, `sha256` |
| `audit.action_recorded` | `action`, `targetType`, `targetId`, `outcome`; optional `reason` |

All payloads reject unknown fields at the versioned contract boundary. IDs, enums, SHA formats, path constraints, URI policy, and sizes must be checked by validators in addition to basic JSON parsing. Never place storage keys, signed URLs, secret values, host paths, or raw reasoning in `data`.

## 3. Persistence, idempotency, and replay

1. In one database transaction, persist the normalized event, any resulting aggregate state/history changes, and an outbox record. Publish only after commit.
2. Ingestion is at-least-once. Deduplicate provider callbacks using a stable provider event ID scoped to provider/session; if the provider lacks one, derive a deterministic operation/event key. Never allocate a second event for a recognized duplicate.
3. Allocate a unique monotonic project sequence inside the same transaction as event persistence. A gap is allowed only if the sequence source can prove a rolled-back allocation; consumers use cursor replay, not contiguous-number assumptions.
4. REST carries Owner commands; one-way project activity is SSE. SSE `id:` is the decimal `streamSequence`; `Last-Event-ID` resumes strictly after that sequence within the authorized project.
5. A slow client may reconnect and replay. If its cursor predates retained events, return an authorized state snapshot plus a new cursor; never silently skip a gap.
6. Clients deduplicate by `eventId` and `(projectId, streamSequence)`. The UI treats an event as a fact, then converges to an authoritative snapshot on version/cursor mismatch.
7. Event streams and artifact lookups are authorized using the authenticated tenant/project; never trust IDs in a cursor or URL as access grants.

## 4. Payload and privacy rules

- Redact secrets before event persistence, logging, broadcast, and artifact storage—not only in the UI.
- `command.output` is chunked, bounded to 32 KiB per event (byte-count enforced by ingestion, in addition to the schema character limit), carries `redacted: true`, and must be filtered for credentials/known sensitive patterns. Apply backpressure and an output quota; summarize/drop excess with an explicit truncation fact rather than buffering without limit.
- `agent.message` is a concise owner-visible progress, result, or question. Never ingest or expose raw chain-of-thought/internal reasoning.
- File paths are workspace-relative, normalized, and contain no absolute host path or traversal. Do not include full file content in `file.activity`; use authorized diff/artifact endpoints.
- PR URLs and artifact metadata are owner-visible; object-store keys, signed URLs, provider tokens, secret values, and host paths are never event payloads.
- Store large terminal captures, diffs, and screenshots as private artifacts with tenant/task authorization, content type/size limits, checksum, and retention policy. Events reference artifact IDs only.
- Reject unknown schema versions/event types. During upgrades, deploy readers before writers and retain compatibility until old writers are retired.

## 5. State authority and UI projection

Only backend transition commands write task/employee/attempt/workspace/approval/PR state. An incoming runtime event may request a derived employee-state update, but it cannot bypass the legal transition table or mark a task Done. `task.state_changed`, `employee.state_changed`, `execution_attempt.state_changed`, `workspace.state_changed`, and `approval.*` are persisted state facts. Phaser and React consume those facts and the snapshot API; they do not infer transitions from animation timing or terminal text.

## 6. Minimum verification

- Schema-valid example for every event type and invalid payload rejection.
- Duplicate provider delivery produces one persisted fact and one sequence.
- Concurrent ingestion yields unique ordered project sequences.
- Event and outbox commit atomically; publisher restart does not lose events.
- SSE reconnect/replay from cursor, duplicate delivery, cursor expiry/snapshot, and tenant authorization tests.
- Secret and path traversal fixtures are redacted/rejected before persistence.
