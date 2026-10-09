# DiOffice Database Schema v0.1

Database: PostgreSQL

## Logical model and implementation status

This document includes the target logical model. The executable subset today covers identity/project, Owner password, task/event/outbox, task reference images, the execution layer (workspaces, execution attempts, agent sessions, approvals, pull requests, artifacts), checks verification columns, and the runtime-provider registry (`provider_configs`). Those migrations live in `db/migrations/` and are not a complete production schema; `task_comments` remains logical-only. Use state, event, and execution contracts linked from `PRD.md` as the source of truth.


## organizations

- id
- name
- created_at

## users

- id
- organization_id
- email
- display_name
- role
- password_hash nullable (bcrypt; password is never stored in plain text)
- created_at

## user_sessions (implemented)

- id, organization_id, user_id
- SHA-256 hashes of the opaque session and CSRF tokens (raw tokens are returned only at login)
- created_at, last_seen_at, expires_at, revoked_at
- tenant-scoped foreign key to users; expired or revoked sessions cannot authenticate

## employees

- id
- organization_id
- name
- slug
- role
- department
- status
- default_runtime
- heavy_runtime nullable
- current_task_id nullable
- created_at
- updated_at

## employee_skills

- id
- employee_id
- skill_key
- level nullable

## employee_permissions

- id
- employee_id
- permission_key
- policy

## projects

- id
- organization_id
- name
- status
- created_at

## repositories

- id
- project_id
- provider
- owner
- repo_name
- default_branch
- external_repo_id nullable
- created_at

## tasks

- id
- project_id
- assignee_employee_id (required in v0.1; `task.created` requires an assignee)
- title
- description
- acceptance_criteria
- required_checks
- manifest_digest
- task_version
- type
- priority
- status
- branch_name nullable
- pull_request_id nullable
- created_by_user_id
- created_at
- updated_at

## task_comments

- id
- task_id
- author_type
- author_id
- body
- created_at

## execution_attempts (implemented)

- id, organization_id, project_id, task_id, employee_id, attempt_number
- state, runtime_type, runtime_session_id nullable
- checks_state, checks_started_at nullable, candidate_sha nullable (checks verification claim marker + tested SHA)
- pr_state, pr_started_at nullable, pr_failure_count, pr_last_error nullable (PR publication claim marker + bounded retry)
- started_at, ended_at nullable, error_code nullable, retryable
- unique (task_id, attempt_number); enforce at most one active attempt per task

## workspaces (implemented)

- id, organization_id, project_id, task_id, branch_name, worktree_ref nullable until provisioning allocates the worktree
- state, container_id nullable, created_at, retained_until nullable, cleaned_at nullable
- unique writable workspace per task; never persist a host path in owner-visible events

## agent_sessions (implemented)

- id, task_id, attempt_id, employee_id, workspace_id
- runtime_type, runtime_session_id nullable, status, started_at, ended_at nullable



## agent_events

- event_id, organization_id, project_id, nullable task/employee/attempt/session/workspace IDs
- project-scoped stream_sequence, schema_version, event_type, typed payload_json
- occurred_at, recorded_at, producer, actor, correlation_id, causation_id

Indexes/constraints:
- event_id unique
- (project_id, stream_sequence) unique and indexed for SSE replay
- task_id + stream_sequence for task timeline
- Persist event/state/history/outbox atomically; assign sequence transactionally at trusted ingestion.
- Payload contract is in `EVENT_SCHEMA.md` and `schemas/event-envelope.schema.json`.

## approvals (implemented)

- id, organization_id, project_id, task_id, attempt_id nullable
- requested_by_employee_id nullable, action_type, action_digest, policy_version, expires_at
- status, requested_at, resolved_at nullable, resolved_by_user_id nullable, reason nullable
- approval is single-use and only valid for the exact action digest/policy version and before expiry

## event_outbox

- id, event_id, project_id, stream_sequence, payload_json, created_at, published_at nullable
- unique (event_id); durable retry and idempotent publication


## Pull Requests (implemented)

- id, organization_id, project_id, task_id, repository_id
- provider, external_pr_id, url, branch_name, head_sha, base_sha, state
- created_at, merged_at nullable
- unique (provider, repository_id, external_pr_id); approval tied to current head_sha



## Artifacts (implemented)

- id, organization_id, project_id, task_id, attempt_id nullable, session_id nullable
- kind, storage_key (private server-side only), mime_type, size_bytes, sha256
- created_at, expires_at nullable
- storage key is never exposed in events or API payloads; authorize each download against tenant/project/task

## provider_configs (implemented)

- id, organization_id, provider_key (`opencode|codex|claude` catalog keys), label
- base_url nullable (bare http(s) origin; http only on loopback — enforced by the service)
- credential_env nullable — NAME of a runner-host env var only; secret values are never stored
- enabled, created_at, updated_at
- unique (organization_id, provider_key); configuration writes are audited org-scoped
- the session runner resolves each attempt's runtime_type through this registry; `OPENCODE_SERVER_URL` is the dev fallback for unconfigured orgs

## Key constraints

- Unique (project_id, stream_sequence); unique provider repository ID and external PR identity.
- Enforce one active attempt and one writable workspace per task in the database.
- A workspace belongs to one task; a PR belongs to one task for v0.1.
- Required check runs and Owner approvals bind to the exact immutable commit/head SHA.
- Employee current status is a projection; task/attempt/session/workspace/event history is authoritative.
- Organization/project foreign keys must prevent cross-tenant references; state changes, history, events, and outbox commit atomically.
