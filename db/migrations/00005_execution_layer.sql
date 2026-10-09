-- +goose Up
ALTER TABLE repositories
    ADD CONSTRAINT repositories_organization_project_id_key
    UNIQUE (organization_id, project_id, id);

CREATE TABLE workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    branch_name text NOT NULL
        CHECK (length(btrim(branch_name)) BETWEEN 1 AND 250),
    worktree_ref text
        CHECK (worktree_ref IS NULL OR length(btrim(worktree_ref)) > 0),
    worker_profile text NOT NULL
        CHECK (worker_profile = 'node-22-pnpm-10-playwright'),
    state text NOT NULL DEFAULT 'PROVISIONING' CHECK (state IN (
        'PROVISIONING', 'READY', 'IN_USE', 'RETAINED',
        'CLEANUP_PENDING', 'CLEANED', 'FAILED'
    )),
    container_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    retained_until timestamptz,
    cleaned_at timestamptz,
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    CHECK ((cleaned_at IS NULL) = (state <> 'CLEANED')),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id, id),
    UNIQUE (organization_id, project_id, task_id, id)
);

CREATE UNIQUE INDEX workspaces_one_writable_per_task_idx
    ON workspaces (organization_id, task_id)
    WHERE state IN ('PROVISIONING', 'READY', 'IN_USE');

CREATE INDEX workspaces_task_state_idx
    ON workspaces (organization_id, task_id, state);

CREATE TABLE execution_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    employee_id uuid NOT NULL,
    attempt_number bigint NOT NULL CHECK (attempt_number > 0),
    state text NOT NULL DEFAULT 'CREATED' CHECK (state IN (
        'CREATED', 'PROVISIONING', 'RUNNING', 'WAITING_APPROVAL',
        'SUCCEEDED', 'FAILED', 'CANCELED', 'ABORTED'
    )),
    runtime_type text NOT NULL CHECK (runtime_type = 'opencode'),
    runtime_session_id text,
    error_code text
        CHECK (error_code IS NULL OR length(btrim(error_code)) > 0),
    retryable boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    ended_at timestamptz,
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    FOREIGN KEY (organization_id, employee_id)
        REFERENCES employees (organization_id, id),
    CHECK (error_code IS NULL OR state IN ('FAILED', 'ABORTED')),
    CHECK ((ended_at IS NULL) = (state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED', 'ABORTED'))),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id, id),
    UNIQUE (organization_id, project_id, task_id, id),
    UNIQUE (organization_id, task_id, attempt_number)
);

CREATE UNIQUE INDEX execution_attempts_one_active_per_task_idx
    ON execution_attempts (organization_id, task_id)
    WHERE state IN ('CREATED', 'PROVISIONING', 'RUNNING', 'WAITING_APPROVAL');

CREATE UNIQUE INDEX execution_attempts_one_active_per_employee_idx
    ON execution_attempts (organization_id, employee_id)
    WHERE state IN ('CREATED', 'PROVISIONING', 'RUNNING', 'WAITING_APPROVAL');

CREATE TABLE agent_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    employee_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    runtime_type text NOT NULL CHECK (runtime_type = 'opencode'),
    runtime_session_id text,
    status text NOT NULL DEFAULT 'STARTING' CHECK (status IN (
        'STARTING', 'RUNNING', 'PAUSED', 'COMPLETED', 'FAILED', 'CANCELED'
    )),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    ended_at timestamptz,
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, task_id, attempt_id)
        REFERENCES execution_attempts (organization_id, project_id, task_id, id),
    FOREIGN KEY (organization_id, project_id, task_id, workspace_id)
        REFERENCES workspaces (organization_id, project_id, task_id, id),
    FOREIGN KEY (organization_id, employee_id)
        REFERENCES employees (organization_id, id),
    CHECK ((ended_at IS NULL) = (status NOT IN ('COMPLETED', 'FAILED', 'CANCELED'))),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id, id),
    UNIQUE (organization_id, project_id, task_id, id)
);

CREATE UNIQUE INDEX agent_sessions_runtime_session_unique_idx
    ON agent_sessions (runtime_type, runtime_session_id)
    WHERE runtime_session_id IS NOT NULL;

CREATE INDEX agent_sessions_attempt_created_idx
    ON agent_sessions (organization_id, attempt_id, created_at);

CREATE TABLE approvals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    attempt_id uuid,
    requested_by_employee_id uuid,
    action_type text NOT NULL CHECK (action_type IN (
        'dependency_change', 'destructive_action', 'permission_expansion',
        'check_override', 'merge', 'other'
    )),
    action_digest text NOT NULL CHECK (action_digest ~ '^[a-f0-9]{64}$'),
    policy_version text NOT NULL
        CHECK (length(btrim(policy_version)) BETWEEN 1 AND 64),
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN (
        'PENDING', 'APPROVED', 'DENIED', 'EXPIRED', 'CANCELED'
    )),
    requested_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    resolved_at timestamptz,
    resolved_by_user_id uuid,
    reason text,
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, task_id, attempt_id)
        REFERENCES execution_attempts (organization_id, project_id, task_id, id),
    FOREIGN KEY (organization_id, requested_by_employee_id)
        REFERENCES employees (organization_id, id),
    FOREIGN KEY (organization_id, resolved_by_user_id)
        REFERENCES users (organization_id, id),
    CHECK (expires_at > requested_at),
    CHECK ((resolved_at IS NULL) = (status = 'PENDING')),
    CHECK (status IN ('PENDING', 'EXPIRED') OR resolved_by_user_id IS NOT NULL),
    CHECK (status <> 'EXPIRED' OR resolved_by_user_id IS NULL),
    UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX approvals_one_pending_per_task_idx
    ON approvals (organization_id, task_id)
    WHERE status = 'PENDING';

CREATE INDEX approvals_task_requested_idx
    ON approvals (organization_id, task_id, requested_at DESC);

CREATE TABLE pull_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider = 'github'),
    external_pr_id text NOT NULL CHECK (length(btrim(external_pr_id)) > 0),
    number integer NOT NULL CHECK (number > 0),
    url text NOT NULL
        CHECK (length(url) BETWEEN 1 AND 2048 AND url ~ '^https://'),
    branch_name text NOT NULL
        CHECK (length(btrim(branch_name)) BETWEEN 1 AND 250),
    head_sha text NOT NULL
        CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    base_sha text NOT NULL
        CHECK (base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    state text NOT NULL CHECK (state IN ('OPEN', 'DRAFT', 'MERGED', 'CLOSED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    merged_at timestamptz,
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, repository_id)
        REFERENCES repositories (organization_id, project_id, id),
    CHECK ((state = 'MERGED') = (merged_at IS NOT NULL)),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id, id),
    UNIQUE (provider, repository_id, external_pr_id),
    UNIQUE (organization_id, task_id)
);

CREATE TABLE artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    attempt_id uuid,
    session_id uuid,
    kind text NOT NULL CHECK (kind IN (
        'screenshot', 'browser_capture', 'log_archive', 'diff',
        'terminal_capture', 'generated_asset', 'other'
    )),
    storage_key text NOT NULL
        CHECK (storage_key = 'artifacts/' || id::text),
    mime_type text NOT NULL
        CHECK (length(btrim(mime_type)) BETWEEN 1 AND 127),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, task_id, attempt_id)
        REFERENCES execution_attempts (organization_id, project_id, task_id, id),
    FOREIGN KEY (organization_id, project_id, task_id, session_id)
        REFERENCES agent_sessions (organization_id, project_id, task_id, id),
    CHECK (expires_at IS NULL OR expires_at > created_at),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id, task_id, id),
    UNIQUE (storage_key)
);

CREATE INDEX artifacts_task_created_idx
    ON artifacts (organization_id, task_id, created_at DESC, id);

ALTER TABLE tasks ADD COLUMN pull_request_id uuid;
ALTER TABLE tasks ADD CONSTRAINT tasks_pull_request_fk
    FOREIGN KEY (organization_id, project_id, pull_request_id)
    REFERENCES pull_requests (organization_id, project_id, id);

ALTER TABLE agent_events
    ADD CONSTRAINT agent_events_attempt_fk
    FOREIGN KEY (organization_id, project_id, attempt_id)
    REFERENCES execution_attempts (organization_id, project_id, id),
    ADD CONSTRAINT agent_events_session_fk
    FOREIGN KEY (organization_id, project_id, session_id)
    REFERENCES agent_sessions (organization_id, project_id, id),
    ADD CONSTRAINT agent_events_workspace_fk
    FOREIGN KEY (organization_id, project_id, workspace_id)
    REFERENCES workspaces (organization_id, project_id, id);

-- Runtime ingestion is at-least-once: provider callbacks are deduplicated by
-- a deterministic key derived from the provider event id scoped to the
-- session, so a restarted ingester never writes a second fact.
ALTER TABLE agent_events ADD COLUMN dedupe_key text;
CREATE UNIQUE INDEX agent_events_dedupe_idx
    ON agent_events (organization_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL;

-- +goose Down
DROP INDEX agent_events_dedupe_idx;
ALTER TABLE agent_events
    DROP COLUMN dedupe_key,
    DROP CONSTRAINT agent_events_workspace_fk,
    DROP CONSTRAINT agent_events_session_fk,
    DROP CONSTRAINT agent_events_attempt_fk;
ALTER TABLE tasks DROP CONSTRAINT tasks_pull_request_fk;
ALTER TABLE tasks DROP COLUMN pull_request_id;
DROP TABLE artifacts;
DROP TABLE pull_requests;
DROP TABLE approvals;
DROP TABLE agent_sessions;
DROP TABLE execution_attempts;
DROP TABLE workspaces;
ALTER TABLE repositories
    DROP CONSTRAINT repositories_organization_project_id_key;
