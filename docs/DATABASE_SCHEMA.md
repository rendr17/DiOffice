# DiOffice Database Schema v0.1

Database: PostgreSQL

## Logical model only

This is a conceptual domain model, not executable DDL or a complete implementation-ready schema. Before persisting production data, define tenant-consistent foreign keys, types, unique/check constraints, indexes, audit/history/outbox tables, and versioned migrations. Use state, event, and execution contracts linked from `PRD.md` as the source of truth.


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
- created_at

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

## execution_attempts

- id, organization_id, project_id, task_id, employee_id, attempt_number
- state, runtime_type, runtime_session_id nullable
- started_at, ended_at nullable, error_code nullable, retryable
- unique (task_id, attempt_number); enforce at most one active attempt per task

## workspaces

- id, organization_id, project_id, task_id, branch_name, worktree_ref
- state, container_id nullable, created_at, retained_until nullable, cleaned_at nullable
- unique writable workspace per task; never persist a host path in owner-visible events

## agent_sessions

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

## approvals

- id, organization_id, project_id, task_id, attempt_id nullable
- requested_by_employee_id nullable, action_type, action_digest, policy_version, expires_at
- status, requested_at, resolved_at nullable, resolved_by_user_id nullable, reason nullable
- approval is single-use and only valid for the exact action digest/policy version and before expiry

## event_outbox

- id, event_id, project_id, stream_sequence, payload_json, created_at, published_at nullable
- unique (event_id); durable retry and idempotent publication


## Pull Requests

- id, organization_id, project_id, task_id, repository_id
- provider, external_pr_id, url, branch_name, head_sha, base_sha, state
- created_at, merged_at nullable
- unique (provider, repository_id, external_pr_id); approval tied to current head_sha



## Artifacts

- id, organization_id, project_id, task_id, attempt_id nullable, session_id nullable
- kind, storage_key (private server-side only), mime_type, size_bytes, sha256
- created_at, expires_at nullable
- storage key is never exposed in events or API payloads; authorize each download against tenant/project/task

## Key constraints

- Unique (project_id, stream_sequence); unique provider repository ID and external PR identity.
- Enforce one active attempt and one writable workspace per task in the database.
- A workspace belongs to one task; a PR belongs to one task for v0.1.
- Required check runs and Owner approvals bind to the exact immutable commit/head SHA.
- Employee current status is a projection; task/attempt/session/workspace/event history is authoritative.
- Organization/project foreign keys must prevent cross-tenant references; state changes, history, events, and outbox commit atomically.
