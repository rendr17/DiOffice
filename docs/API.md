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

## Task draft flow

All task endpoints require an authenticated Owner session. `projectId` is obtained from bootstrap for this first slice; the organization scope and actor ID always come from the authenticated session, never from the request body.

`POST /api/v1/projects/{projectId}/tasks`

Required headers:

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

Common error codes: `401 unauthorized` / `invalid_credentials`, `403 csrf_validation_failed` / `origin_not_allowed`, `400 invalid_task` / `invalid_json`, `404 project_not_found`, `409 idempotency_key_conflict`, `413 request_too_large`, and `503 service_unavailable`.

## Current boundary

This API slice is manually exercisable with an HTTP client and is covered by a PostgreSQL-backed HTTP integration test. There is not yet a web login/task UI, project-list API, task transition or explicit Start command, outbox publisher, durable SSE replay, Temporal worker, OpenCode execution, GitHub checks/PR integration, approval, or merge flow. Do not expose this local development slice publicly; rate limiting, deployment TLS/origin policy, operator bootstrap lifecycle, and production operational controls still need a separate hardening phase.
