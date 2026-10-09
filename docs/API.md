# REST API v1 (current slice)

Local API defaults to `http://127.0.0.1:8080`. It requires PostgreSQL and an explicitly applied migration bundle; it does not migrate during startup. See the root README for local bootstrap commands.

## Bootstrap and login

From `apps/api`, run `go run ./cmd/bootstrap-owner --organization "..." --project "..." --email "..."`. It prompts twice for a hidden password, then prints the organization, project, and Deni employee IDs. Keep those IDs for the manual API test. The password is stored as a bcrypt hash; do not pass it as a CLI argument or save it in a shared request collection.

`POST /api/v1/auth/login`

```json
{
  "organizationId": "<organization UUID from bootstrap>",
  "email": "<Owner email>",
  "password": "<Owner password>"
}
```

On success the response contains only the safe user identity. The opaque session and CSRF tokens are set as cookies; the session cookie is `HttpOnly`, both cookies use `SameSite=Lax`, and `Secure` follows `COOKIE_SECURE` (true by default; set false only on loopback HTTP for local development). Session tokens and CSRF tokens are persisted only as SHA-256 hashes. Absolute session expiry is 12 hours; logout revokes the server-side session.

For browser clients, send credentials and the exact configured `WEB_ORIGIN`. For mutations, send the value of the `dioffice_csrf` cookie in `X-CSRF-Token`. Login errors intentionally do not reveal whether the organization, email, or password was incorrect.

`GET /api/v1/auth/session` returns the current safe user identity. `POST /api/v1/auth/logout` revokes the current session and clears both cookies; logout also requires the CSRF header.

### Optional local development bypass (never deploy)

Set both `DEV_AUTH_BYPASS=true` for the API and `VITE_DEV_AUTH_BYPASS=true` for the Vite dev server to sign in automatically when no session exists. If startup fails, the login screen offers a retry button. This flag is off by default and the bypass UI is hidden in production builds. API startup refuses the bypass unless the API bind and `WEB_ORIGIN` use literal loopback IP addresses and `COOKIE_SECURE=false`. The endpoint also requires the exact configured Origin and a loopback client.

`POST /api/v1/auth/dev-session` with `{}` creates or reuses a passwordless local Owner, organization, project, and Deni employee, then issues the ordinary 12-hour server-side session and CSRF cookies. Subsequent mutations still require CSRF. The local dev Owner cannot sign in through password login; use `bootstrap-owner` to create a credentialed Owner. Never expose this bypass outside loopback or enable it in a deployed environment.

## Workspace lookups

Both endpoints require an authenticated Owner session and always scope results to the organization in that session; organization IDs are never accepted from the query string.

- `GET /api/v1/projects` returns active projects ordered by name (up to 100).
- `GET /api/v1/employees` returns employee IDs, names, roles, departments, and current statuses (up to 100).

The response shape is `{ "items": [...] }`. Archived projects and other organizations' records are not returned.

## Local project folder

`GET /api/v1/projects/{projectId}/folder` requires an authenticated Owner session and returns only `{ "available": boolean }`. `POST /api/v1/projects/{projectId}/folder/open` requires the same session, a CSRF token, and `{ "editor": "explorer" | "vscode" }`; it returns `202 Accepted` after the local gateway starts the selected application.

Both routes first verify that the project is active and belongs to the signed-in organization. The API calls the gateway over a loopback HTTP origin using a server-side internal token. The gateway accepts only a project UUID mapped in `AGENT_GATEWAY_PROJECT_FOLDERS`, verifies that the configured path currently resolves to a directory, and never accepts a path from the browser or returns the local path. Process launch uses a fixed application name and an argument array without a shell. On Windows, `explorer` launches File Explorer; `vscode` requires the `code` CLI on `PATH`. This action only opens the folder—it does not read files, start a task, or authorize runtime execution.

Configure the same random `AGENT_GATEWAY_INTERNAL_TOKEN` (at least 32 characters) for the API and gateway, and configure the UUID-to-absolute-path JSON map only in the local gateway environment. Keep both values out of source control and do not expose the local bridge outside loopback. With no valid mapping, status is `available: false`; open errors include `folder_not_configured`, `folder_bridge_unavailable`, and `folder_open_failed`.

## Task draft flow

All task endpoints require an authenticated Owner session. Browser clients select a project and assignee from the scoped lookup endpoints; the organization scope and actor ID always come from the authenticated session, never from the request body.

`POST /api/v1/projects/{projectId}/tasks`

Required headers for a JSON draft:

- `Content-Type: application/json`
- `Idempotency-Key: <unique key for this create command>`
- `X-CSRF-Token: <value of dioffice_csrf cookie>`

Body:

```json
{
  "assigneeEmployeeId": "<Deni employee UUID from bootstrap>",
  "title": "First manual API task",
  "description": "Verify the persisted draft flow",
  "acceptanceCriteria": ["Task appears in the project list"],
  "requiredChecks": [],
  "taskType": "feature",
  "priority": "NORMAL"
}
```

A successful call returns `201 Created` and a `DRAFT` task. Replaying the same idempotency key and same payload returns the original task with `Idempotency-Replayed: true`; reusing the key with a different payload returns `409 Conflict`. Creation writes the task, `task.created` event, outbox record, audit record, and idempotency result in one transaction. It does **not** start an agent.

`GET /api/v1/projects/{projectId}/tasks` returns the newest 100 tasks in that organization/project. A cross-organization project lookup returns `404`.

### Save a draft to backlog

`POST /api/v1/projects/{projectId}/tasks/{taskId}/backlog` requires an authenticated Owner session, CSRF token, `Idempotency-Key`, and a JSON body containing the current `expectedVersion`. It performs only the canonical `DRAFT → BACKLOG` transition; it does not provision a branch, worker, or runtime. A successful response returns `200 OK` with the updated task. Replaying the same key and command returns the stored response with `Idempotency-Replayed: true`; a stale version or illegal transition returns `409`. The task update, `task.state_changed` event, outbox record, audit record, and idempotency result commit atomically.

The task contract accepts a title of 1–200 Unicode code points, a description of up to 20,000, and up to 100 acceptance criteria of 1–2,000 code points each. Draft creation saves the composer’s first line as title and the complete instruction as description; the task remains `DRAFT` until the separate backlog command is submitted.

### Project repository registration

`GET /api/v1/projects/{projectId}/repository` returns the repository connected to the project, or `404 repository_not_found` when none is registered. `PUT` on the same path registers or replaces it:

```json
{ "owner": "acme", "name": "widgets", "defaultBranch": "main" }
```

The PUT requires an authenticated Owner session and CSRF token. Owner/name are validated against GitHub naming rules and `defaultBranch` against git ref rules; invalid values return `400 invalid_repository`, and a cross-organization or archived project returns `404 project_not_found`. Registration records repository identity (`github` provider only for v0.1) and an audit record — it does **not** call GitHub, verify access, or store credentials. One repository per project; the upsert is keyed on the project so retries never duplicate rows.

### Mark a task ready

`POST /api/v1/projects/{projectId}/tasks/{taskId}/ready` requires an authenticated Owner session, CSRF token, `Idempotency-Key`, and a JSON body with `expectedVersion` plus an optional `manifestDigest` (SHA-256 hex of the task's `.dioffice/execution.json`). It performs `DRAFT → READY` or `BACKLOG → READY` only when every READY requirement is confirmed: non-empty description, at least one acceptance criterion, a recorded manifest digest, and a connected project repository. When requirements are missing it returns `409` with `{ "error": "task_incomplete", "missing": [...] }` listing stable field identifiers. A supplied digest updates the task and emits `task.updated` before the `task.state_changed` event; transition, events, outbox, audit, and idempotency result commit atomically. It starts no worker.

### Explicit Start

`POST /api/v1/projects/{projectId}/tasks/{taskId}/start` requires an authenticated Owner session, CSRF token, `Idempotency-Key`, and `expectedVersion`. It performs the canonical `READY → PROVISIONING` transition and, in the same transaction, creates a `CREATED` execution attempt (immutable `attempt_number` per task) and a `PROVISIONING` workspace (`task/{taskId}` branch name, `node-22-pnpm-10-playwright` worker profile), then emits `execution_attempt.state_changed`, `workspace.state_changed`, and `task.state_changed` under one correlation ID. A task that is not `READY` returns `409 invalid_state_transition`; if the task or its assignee already has an active attempt (`CREATED`/`PROVISIONING`/`RUNNING`/`WAITING_APPROVAL`), it returns `409 active_attempt_exists`. No worker, git worktree, or OpenCode session is created by the API — the provisioner process below advances the committed attempt.

### Owner control: Retry and Cancel

`POST /api/v1/projects/{projectId}/tasks/{taskId}/retry` performs the canonical `BLOCKED|FAILED → READY` transition. `POST /api/v1/projects/{projectId}/tasks/{taskId}/cancel` performs the canonical Owner cancel from `IN_PROGRESS|WAITING_APPROVAL|BLOCKED|IN_REVIEW|FAILED` to `CANCELED` (`reason` in the JSON body is optional, ≤500 chars). Both require an authenticated Owner session, CSRF token, `Idempotency-Key`, and `expectedVersion`; both are idempotent and return `409` on a stale version or non-canonical source state.

Either transition first quiesces the execution layer inside the same transaction: live sessions become `CANCELED` (`session.canceled`), active attempts become `CANCELED` (`execution_attempt.state_changed`, reason `owner_control`), open workspaces move to `CLEANUP_PENDING` (`workspace.state_changed`), and pending approvals resolve `CANCELED` (`approval.resolved`) — all under one correlation id. Retry never reuses an ambiguous worker: a later explicit Start provisions a brand-new attempt and workspace. When the API is configured with `OPENCODE_SERVER_URL`, recorded provider sessions are aborted best-effort *after* the durable commit; a dead runtime can never block cancellation, and leftover provider sessions fall to the runner's stale-session reconciliation.

### Workspace provisioning

`go run ./cmd/provisioner` is a separate process that polls `execution_attempts` for claimable `CREATED` rows (`PROVISIONER_POLL_INTERVAL`, `PROVISIONER_OP_TIMEOUT`, `PROVISIONER_CLAIM_STALE_AFTER`). For each claim it clones/fetches the registered repository into a bare mirror under `WORK_ROOT`, pins `core.autocrlf=false`, creates the `task/{taskId}` branch from the configured default branch, and materializes an isolated `git worktree` — all git invocations are argv-based with bounded timeouts; no shell strings are executed. A stale worktree registration triggers `git worktree prune` and one retry.

After materialization the provisioner reads `.dioffice/execution.json` from the worktree, validates it with `internal/executionmanifest`, and compares its SHA-256 digest to the digest recorded on the task at READY. On success the workspace becomes `READY` (attempt stays `PROVISIONING`, task stays `PROVISIONING`) and the transition events commit atomically. On failure the attempt is marked `FAILED` with an owner-safe reason code (`repository_unreachable`, `default_branch_missing`, `worktree_failed`, `manifest_missing`, `manifest_invalid`, `manifest_digest_mismatch`, `worker_profile_mismatch`, `internal_error`), the workspace becomes `FAILED`, and the task moves to `BLOCKED`. A second poll never reclaims a completed attempt.

### Runtime providers

`GET /api/v1/providers` returns the runtime catalog merged with the organization's saved configuration: `key`, `displayName`, `kind`, `adapterStatus` (`implemented`|`registered`), `needsBaseUrl`/`needsCredential`, plus `configured`, `enabled`, `label`, `baseUrl`, `credentialEnv`.

`PUT /api/v1/providers/{providerKey}` (Owner session + CSRF) upserts `{label, baseUrl, credentialEnv, enabled}` for one catalog key (`opencode`, `codex`, `claude`). `baseUrl` must be a bare http(s) origin — http only on loopback; `credentialEnv` stores only the NAME of a runner-host environment variable (`^[A-Z][A-Z0-9_]+$`) — secret values never enter the database, events, or responses. Writes are audited org-scoped (`provider.configure`, `project_id` NULL). Unknown keys return `404 provider_unknown`; validation failures return `400 invalid_provider_config`.

The session runner resolves each attempt's `runtime_type` through this registry: an enabled config's `base_url` drives the OpenCode adapter, while `OPENCODE_SERVER_URL` remains the development fallback when no config row exists. Registered-but-unimplemented providers (codex, claude today) or disabled/unconfigured providers fail the attempt closed with `provider_not_implemented`/`provider_disabled`/`provider_not_configured` — never a fake RUNNING session. Selecting a runtime per employee comes from `employees.default_runtime`; an assignment UI is still pending.

### Runtime session start

`go run ./cmd/session-runner` polls `execution_attempts` for `PROVISIONING` attempts whose workspace is `READY` (same `PROVISIONER_WORK_ROOT`; provider endpoints resolve via `provider_configs` with `OPENCODE_SERVER_URL` as the unconfigured fallback — loopback http(s) only). The claim is a committed `agent_sessions` `STARTING` row under the attempt lock, so concurrent runners cannot double-start. A stale `STARTING` row from a crashed runner is failed with `session_start_interrupted` — aborting its recorded provider session best-effort — before a new session is claimed.

The runner re-verifies the checked-out manifest digest, creates a directory-scoped OpenCode session (`POST /session?directory=`, also sent as `x-opencode-directory`), persists the provider session id immediately, and submits the initial task instruction (`POST /session/{id}/prompt_async`) built from the employee identity, task title/description/acceptance criteria, branch, and manifest rules. On success one transaction marks the session `RUNNING`, the attempt `RUNNING`, the workspace `IN_USE`, and the task `IN_PROGRESS`, emitting `session.started` plus the three `*.state_changed` facts under one correlation id. Runtime or manifest failures mark the session and attempt `FAILED` (`session.failed` with `errorCode`/`retryable`), keep the workspace `READY` for an explicit Owner Retry, and move the task to `BLOCKED` with an owner-safe reason (`runtime_unreachable`, `runtime_rejected`, `runtime_invalid_response`, `manifest_*`, `provider_unknown`/`provider_not_configured`/`provider_disabled`/`provider_not_implemented`, `session_start_interrupted`, `internal_error`).

### Runtime event ingestion

`go run ./cmd/event-ingester` is a separate process (same `PROVISIONER_WORK_ROOT` and loopback-only `OPENCODE_SERVER_URL`; `EVENT_INGESTER_POLL_INTERVAL` reconciles subscriptions, `EVENT_INGESTER_RECONNECT_DELAY` bounds reconnect backoff). It reconciles `RUNNING` agent sessions against live subscriptions, opens one directory-scoped `GET /event` SSE stream per session, and normalizes recognized provider frames into canonical durable facts through `internal/runtimeevents`: text-part completions → `agent.message`, bash/shell tool start/finish → `command.started`/`command.completed` (or `command.failed`), file tool activity → `file.activity`, provider idle/error → `session.completed`/`session.failed`.

Every normalized fact carries a deterministic `dedupe_key` (`opencode:{session}:{provider id}`) enforced by a partial unique index on `agent_events`, so at-least-once provider delivery and process restarts never double-record a fact. Frames carrying another `sessionID` on the same directory are dropped, frames that are not valid JSON or recognized shapes are ignored (fail-closed), and unsafe absolute/traversal paths are rejected at normalization. A terminal fact commits the session transition in the same transaction — `session.failed` additionally marks the attempt `FAILED` (retryable) and moves an `IN_PROGRESS` task to `FAILED` with the attempt/task `*.state_changed` events under the same causation. The consumer goroutine exits on a terminal fact or context cancel; dropped streams reconnect after the backoff delay, and sessions that left `RUNNING` are unsubscribed on the next reconcile pass.

### Checks verification

`go run ./cmd/checks-runner` claims `RUNNING` attempts whose `agent_sessions` row is `COMPLETED` (`CHECKS_RUNNER_POLL_INTERVAL`, `CHECKS_RUNNER_CLAIM_STALE_AFTER` for crash reclaim, `PROVISIONER_WORK_ROOT` shared with the provisioner, optional `OBJECTSTORE_*` for log artifacts). The claim flips `execution_attempts.checks_state` `PENDING → RUNNING` under `FOR UPDATE SKIP LOCKED` and records a `checks.claim`/`checks_reclaim` audit fact. Because the agent has write access to the worktree, the runner re-verifies the checked-out `.dioffice/execution.json` digest against the task's recorded digest before trusting any declared command.

Residual working-tree changes are checkpointed onto the task branch (`git.commit_created`) so evidence always binds to an immutable `candidate_sha` (`rev-parse HEAD`). Each manifest check runs as an argv process in `workingDirectory` — never a shell string — with an allowlisted child environment (host toolchain vars plus manifest `passThrough` names only), per-check `timeoutSeconds`, and a bounded output tail. Canonical `check.started` / `check.passed` / `check.failed` (`failureCode` ∈ `nonzero_exit`/`timeout`/`spawn_error`) facts carry `checkRunId`, `checkId`, and `commitSha`; when object storage is configured the output tail is stored as a private `log_archive` artifact (`artifacts/{id}` key, sha256-checked) with an `artifact.created` fact.

All required checks passing moves the attempt `RUNNING → SUCCEEDED` (`checks_state=PASSED`, `candidate_sha` recorded). Any required failure — or a verification error such as a tampered/missing manifest, missing worktree, or `resources.attemptTimeoutSeconds` exceeded — marks the attempt `FAILED` (retryable, except `attempt_timeout`/`manifest_digest_mismatch`) and moves an `IN_PROGRESS` task to `FAILED` for an explicit Owner Retry.

### Pull request publication

`go run ./cmd/pr-runner` claims `SUCCEEDED` attempts whose `candidate_sha` is recorded (`PR_RUNNER_POLL_INTERVAL`, `PR_RUNNER_CLAIM_STALE_AFTER` for crash reclaim, `PR_RUNNER_RETRY_BACKOFF` pacing FAILED retries, `PR_RUNNER_MAX_FAILURES` bounding them; `PROVISIONER_WORK_ROOT` shared, `GITHUB_TOKEN` required, `GITHUB_API_URL` override for tests). The claim flips `execution_attempts.pr_state` `PENDING → RUNNING` under `FOR UPDATE SKIP LOCKED` and records a `pull_request.claim` audit fact.

The runner verifies the candidate commit exists locally (`git cat-file -e {sha}^{commit}`), then pushes exactly that SHA — not the branch tip — to `refs/heads/task/{taskId}` on `https://github.com/{owner}/{repo}.git`. The token rides in `http.extraheader`, never in remote URLs, error output, or events; `GIT_TERMINAL_PROMPT=0` guarantees no credential prompt can hang the runner. Push outcomes emit `git.push_completed` or `git.push_failed` with bounded failure codes (`push_auth_failed`/`push_repo_unavailable`/`push_rejected`/`push_failed`).

The runner then finds or creates the pull request via the GitHub REST API (`GET /pulls?head=owner:branch&state=open`, else `POST /pulls`; a 422 "already exists" re-queries and adopts). The recorded `pull_requests` row takes `head_sha`/`base_sha`/`state`/`number`/`url`/`external_pr_id` (node_id) **from the API response** — never inferred locally — and the head must equal `candidate_sha` or publication fails with `pr_head_mismatch`. One PR per task (`UNIQUE(organization_id, task_id)`): a later attempt updates the existing row and emits `pull_request.updated` instead of a second `pull_request.created`.

Success commits one transaction: the `pull_requests` row (insert or update), `tasks.pull_request_id`, `task.state_changed` `IN_PROGRESS → IN_REVIEW` ("review ready: pull request #N points at candidate {sha}"), and `pr_state=PUBLISHED`. Each `FAILED` outcome increments `pr_failure_count`; once `PR_RUNNER_MAX_FAILURES` (default 6) is exhausted the task moves to `BLOCKED` with reason `pr_publish_failed` — publication can never loop forever and never binds a PR at the wrong SHA. The GitHub credential resolves through `internal/secrets` (env-var `GITHUB_TOKEN` locally; secrets-manager resolver for production).

### Review evidence and Owner approval

`GET /api/v1/projects/{projectId}/tasks/{taskId}/pull-request` returns the recorded PR evidence (`number`, `url`, `branchName`, `headSha`, `baseSha`, `state`, `mergedAt`) for a task; it answers `404 pull_request_missing` when nothing was recorded. This is read evidence for the review UI — not an approval.

`POST /api/v1/projects/{projectId}/tasks/{taskId}/approve` performs the canonical `IN_REVIEW → DONE` transition. It requires an authenticated Owner session, CSRF token, `Idempotency-Key`, `expectedVersion`, and the exact `headSha` shown in the review UI. The body may carry an optional `reason` (≤500 chars).

Approval fails closed on every mismatch: a task outside `IN_REVIEW` or a stale version → `409`, no recorded PR → `409 approval_precondition_missing`, a missing/dangling `pull_request_id`, an unexpected `external_pr_id`/`repository_id`, a `SUCCEEDED`-attempt `candidate_sha` that disagrees with the recorded PR head, or a submitted `headSha` that differs from the recorded head → `409 head_sha_mismatch` (malformed SHAs → `400 invalid_input`). The approval binds exactly the verified candidate: one transaction inserts the `approvals` row (`action_type=review_approve`, `action_digest=sha256("review:"+headSha)`, `policy_version`, bound `attempt_id`, `resolved_by`, `expires_at` — single-use per `(organization_id, action_digest)`), retains the workspace (`RETAINED` + `retained_until`), and emits `approval.requested`, `approval.resolved`, `workspace.state_changed`, and `task.state_changed` under one correlation id. Replays of the same `Idempotency-Key` return the stored `DONE` result without new facts. Approval completes the review only; merging the PR remains a separate Owner action.

`POST /api/v1/projects/{projectId}/tasks/{taskId}/request-changes` performs the canonical `IN_REVIEW → IN_PROGRESS` transition with a required Owner `reason` (≤2000 chars). One transaction resolves any pending approvals `CANCELED`, keeps the retained workspace and the recorded pull request bound to the task, records a new `execution_attempts` row (`change_request` carries the feedback), queues a fresh session on that workspace, emits `task.state_changed` (`IN_PROGRESS`, reason `owner_requested_changes`) — then the normal runner chain resumes: the session runner's continuation prompt includes the feedback verbatim, checks re-run on revised commits, and the PR runner updates the same pull request. Replays under the same `Idempotency-Key` return the stored result.

### Pull-request reconciliation

`go run ./cmd/pr-reconciler` is a polling worker (webhooks are a later integration) that re-reads every recorded `OPEN`/`DRAFT` pull request — plus terminal rows still attached to a non-terminal task — through `GET /repos/{owner}/{repo}/pulls/{number}`. Drift is persisted transactionally: `head_sha`/`base_sha`/`state`/`merged_at` updates emit `pull_request.updated` (dedupe key `prreconcile:{prId}:{change}:{head}:{state}` so repeated scans never duplicate facts). A head change on a `DONE` task moves it `DONE → IN_REVIEW` — the earlier approval is stale because its digest binds the old SHA — while a `CLOSED`/`MERGED` or vanished PR on an `IN_REVIEW` task moves it to `BLOCKED` with the evidence preserved. GitHub API responses are authoritative; PR state is never inferred from push events.

### Owner merge

`POST /api/v1/projects/{projectId}/tasks/{taskId}/merge` is the distinct Owner merge action (`Idempotency-Key`, `expectedVersion`, optional `reason`). Preconditions are verified inside the transaction — task `DONE`, recorded `OPEN`/`DRAFT` pull request on the registered repository, and a non-stale `APPROVED` approval whose digest binds the recorded head (`409 stale_approval` when the head moved after approval). The GitHub `PUT /pulls/{n}/merge` call then runs with the API's own `sha` precondition, so a head change between review and merge is rejected by GitHub rather than merged. When GitHub refuses, the service re-reads the PR first — a merge that already landed (e.g. after a crashed commit) is persisted as `MERGED` instead of reported as `409 merge_rejected`; transport/auth failures answer `503 merge_unavailable`. Success marks the pull request `MERGED` with `merged_at`, records a single-use `merge`-action approval bound to `sha256("merge:"+headSha)`, and emits `pull_request.updated`, `approval.requested`, and `approval.resolved` under one correlation id. The task stays `DONE` — merge is recorded on the pull request, and replays are idempotent.

GitHub credentials resolve through `internal/secrets` — an environment-backed resolver (`GITHUB_TOKEN`) for local development, designed so a production deployment swaps in a secrets-manager resolver without touching the runners. Resolved values never appear in logs, event payloads, or persisted errors.

### Private reference images

The same create endpoint also accepts `multipart/form-data`: one `task` part containing the JSON body above, plus optional `referenceImages` file parts. Let the browser set the multipart boundary; keep the idempotency and CSRF headers. Only PNG/JPEG are accepted after content sniffing and image-header validation: at most 5 files, 8 MiB each, 40 MiB total, 12,000 pixels per dimension, and 50 million pixels per image. Names are sanitized; private object keys and checksums are not returned in task responses.

The create/list response includes `referenceImages` metadata (`id`, `fileName`, `contentType`, `sizeBytes`). Read the actual image through the authenticated, organization/project/task-scoped endpoint:

`GET /api/v1/projects/{projectId}/tasks/{taskId}/reference-images/{imageId}`

Reads use `Cache-Control: private, no-store` and `X-Content-Type-Options: nosniff`. Upload requires API S3 configuration; an unavailable store returns `503 reference_image_storage_unavailable`. Local S3Mock is an integration-test double, not production private storage.

Common task error codes: `401 unauthorized` / `invalid_credentials`, `403 csrf_validation_failed` / `origin_not_allowed`, `400 invalid_task` / `invalid_json` / `invalid_task_request` / `invalid_reference_image` / `invalid_repository`, `404 project_not_found` / `task_not_found` / `repository_not_found`, `409 idempotency_key_conflict` / `idempotency_request_in_progress` / `stale_task_version` / `invalid_state_transition` / `task_incomplete` / `active_attempt_exists` / `head_sha_mismatch` / `approval_precondition_missing` / `stale_approval` / `merge_rejected`, `413 request_too_large`, and `503 service_unavailable` / `merge_unavailable` / `reference_image_storage_unavailable`.

## Persisted project history and SSE

Both endpoints require an authenticated Owner session, scope the organization from that session, and check that the project is active. Cross-organization or archived projects return `404 project_not_found`. A project UUID or cursor is never an access grant. Envelope and payload definitions remain authoritative in [`EVENT_SCHEMA.md`](EVENT_SCHEMA.md) and [`schemas/event-envelope.schema.json`](schemas/event-envelope.schema.json).

### History

`GET /api/v1/projects/{projectId}/events`

With no `after`, this returns the **latest 100 or fewer committed events**, ordered by ascending `streamSequence`, plus the durable project head as `nextCursor`. This is a recent, nonpaginated snapshot, not the complete project history; `hasMore` is false.

`GET /api/v1/projects/{projectId}/events?after=<cursor>&limit=100`

With `after`, this returns a bounded ascending replay page **strictly after** that sequence. `limit` defaults to 100 and accepts 1–100. `nextCursor` is the last delivered sequence, or the supplied cursor when the page is empty; follow it while `hasMore` is true.

Example response for a project with no persisted events:

```json
{ "items": [], "nextCursor": "0", "hasMore": false }
```

For nonempty pages, `items` contains canonical envelope objects. Cursors are canonical nonnegative decimal strings in the signed-64-bit range: `0` is valid; signs, leading zeroes, whitespace, duplicate query keys, overflow, and unknown query parameters are rejected. Event ordering never uses provider timestamps or outbox publication status.

### Stream

`GET /api/v1/projects/{projectId}/events/stream?after=<cursor>`

Use ordinary session cookies (`EventSource` with credentials), not tokens in the URL. `Last-Event-ID`, when present, takes precedence over `after` and must be one valid decimal cursor. The stream accepts no other query keys. Initial access and cursor errors are JSON HTTP errors returned before SSE headers.

Successful responses use `text/event-stream`, `private, no-store`, and `X-Accel-Buffering: no`. A message has `id: <streamSequence>` and `data: <canonical JSON envelope>`; it uses the default `message` event, not a provider-specific name. The server suggests a 1-second reconnect delay and sends idle heartbeat comments every 10 seconds.

This slice polls **committed PostgreSQL `agent_events`** in bounded batches every 250 ms; it does not publish or mark outbox rows as delivered. It revalidates the session and active project independently of writes, terminates on access loss/cancellation, and bounds each frame write/flush to 750 ms or an earlier request deadline. A slow consumer must reconnect and replay rather than relying on an unbounded server buffer.

### Cursor recovery and errors

An already-authorized cursor ahead of the durable head returns `409 event_cursor_ahead`; a positive cursor preceding retained history returns `410 event_cursor_expired`. Both responses include `snapshot` with the same recent-history page shape and a new cursor. Unauthorized callers do not receive that snapshot. Clients must explicitly reset history and refresh authoritative resource snapshots; they must not silently skip to the new head.

Other errors: `400 invalid_event_cursor` / `invalid_event_query`, `401 unauthorized`, `403 origin_not_allowed`, `404 project_not_found`, and `503 service_unavailable` / `stream_unavailable`. Unknown/unsafe stored envelopes fail closed instead of being skipped. Newly created event/outbox payloads are projected onto the versioned contract and recognizable credential/host-path/object-reference patterns are redacted before persistence. Legacy records are sanitized again at delivery; arbitrary unlabelled secrets cannot be reliably detected, so never put credentials or raw internal reasoning in a task brief or event payload.

### Browser projection

Activity shows employee-scoped facts within the latest 100 project events. The client checks envelope/version/type/scope/cursor, deduplicates by event ID and sequence, preserves received facts during reconnect, and disposes the subscription on project switch/logout. Invalid frames stop the subscription with an explicit retry state. Cursor recovery shows a history-reset warning. Task/employee facts and successful reconnects trigger coalesced API snapshot refetches; they do **not** write optimistic task/employee statuses. The timeline does not render raw instruction, terminal, or provider payloads.

A transient employee **refresh** failure is not an initial directory-load failure or loss of project access. The client keeps the last successfully loaded employee snapshot, marks it as not yet reconfirmed in the workspace/workstation, and offers `Refresh employee` without disposing Activity/SSE. A successful authoritative employee refetch clears that warning; an unauthorized response still clears the session. Failed initial directory loads continue to show the workspace-unavailable state, rather than an empty office. Test commands, fixture-only interventions, and cleanup guarantees are documented in [`tools/ui-smoke/README.md`](../tools/ui-smoke/README.md).

## Current boundary

The web UI supports Owner login, persisted draft creation/listing, the explicit `DRAFT → BACKLOG`, `→ READY`, and `READY → PROVISIONING` transitions, repository registration, private reference-image endpoints, PR review evidence with SHA-bound Owner approval, PostgreSQL-backed history/SSE projected into workstation Activity, and an optional local folder opener behind an explicit allowlist. HTTP integration tests cover the durable event path. The implemented product event writers emit `task.created`, `task.updated`, `task.state_changed`, `execution_attempt.state_changed`, `workspace.state_changed`, `session.started`, `session.canceled`, `session.failed`, `git.commit_created`, `git.push_completed`, `git.push_failed`, `check.started`, `check.completed`, `check.failed`, `pull_request.created`, `pull_request.updated`, `approval.requested`, `approval.resolved`, `artifact.created`, and `audit.action_recorded` for those transitions, and the event ingester additionally persists normalized `agent.message`, `file.activity`, `command.started`/`command.completed`, `session.completed`, and `session.failed` facts from the live OpenCode stream. Start records the attempt/workspace, the provisioner materializes the isolated worktree, the session runner starts a directory-scoped OpenCode session that moves the task to `IN_PROGRESS`, the checks runner binds a candidate SHA to required-check evidence, the PR runner publishes it to GitHub and moves the task to `IN_REVIEW`, and Owner approval completes `IN_REVIEW → DONE`. Follow-up flows are live too: request-changes resumes revision on the preserved workspace (`IN_REVIEW → IN_PROGRESS`), `cmd/pr-reconciler` re-polls recorded PRs so a post-approval head change reopens the task (`DONE → IN_REVIEW`, approval stale by digest binding) and terminal/vanished PRs block review, and `POST .../merge` performs the distinct Owner merge against a non-stale approval with GitHub's own sha precondition. GitHub credentials resolve through `internal/secrets`. Preview screenshot execution, an outbox publisher, Temporal worker orchestration, GitHub webhook ingestion (reconciliation is polling-based), a production secrets-manager resolver, and real Codex/Claude adapters remain unimplemented. Directory and resource lists remain capped at 100 entries. Do not expose this local development slice publicly; rate limiting, deployment TLS/origin policy, operator bootstrap lifecycle, secrets-manager credential storage (`GITHUB_TOKEN` is a plain env var today), and production operational controls still need a separate hardening phase.
