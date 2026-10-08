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

### Private reference images

The same create endpoint also accepts `multipart/form-data`: one `task` part containing the JSON body above, plus optional `referenceImages` file parts. Let the browser set the multipart boundary; keep the idempotency and CSRF headers. Only PNG/JPEG are accepted after content sniffing and image-header validation: at most 5 files, 8 MiB each, 40 MiB total, 12,000 pixels per dimension, and 50 million pixels per image. Names are sanitized; private object keys and checksums are not returned in task responses.

The create/list response includes `referenceImages` metadata (`id`, `fileName`, `contentType`, `sizeBytes`). Read the actual image through the authenticated, organization/project/task-scoped endpoint:

`GET /api/v1/projects/{projectId}/tasks/{taskId}/reference-images/{imageId}`

Reads use `Cache-Control: private, no-store` and `X-Content-Type-Options: nosniff`. Upload requires API S3 configuration; an unavailable store returns `503 reference_image_storage_unavailable`. Local S3Mock is an integration-test double, not production private storage.

Common task error codes: `401 unauthorized` / `invalid_credentials`, `403 csrf_validation_failed` / `origin_not_allowed`, `400 invalid_task` / `invalid_json` / `invalid_task_request` / `invalid_reference_image`, `404 project_not_found` / `task_not_found`, `409 idempotency_key_conflict` / `stale_task_version` / `invalid_state_transition`, `413 request_too_large`, and `503 service_unavailable` / `reference_image_storage_unavailable`.

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

The web UI supports Owner login, persisted draft creation/listing, the explicit `DRAFT → BACKLOG` transition, private reference-image endpoints, PostgreSQL-backed history/SSE projected into workstation Activity, and an optional local folder opener behind an explicit allowlist. HTTP integration tests cover the durable event path. The implemented product event writers emit `task.created` and `task.state_changed` for that one transition; other canonical runtime/transition producers do not exist yet. Explicit Start, an outbox publisher, Temporal worker, OpenCode execution, GitHub checks/PR integration, approval, and merge flow are not implemented. Directory and resource lists remain capped at 100 entries. Do not expose this local development slice publicly; rate limiting, deployment TLS/origin policy, operator bootstrap lifecycle, and production operational controls still need a separate hardening phase.
