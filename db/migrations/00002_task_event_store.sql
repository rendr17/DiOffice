-- +goose Up
CREATE TABLE tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    assignee_employee_id uuid NOT NULL,
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    description text NOT NULL,
    acceptance_criteria jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(acceptance_criteria) = 'array'),
    required_checks jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(required_checks) = 'array'),
    manifest_digest text,
    task_version bigint NOT NULL DEFAULT 1 CHECK (task_version > 0),
    task_type text NOT NULL CHECK (length(btrim(task_type)) > 0),
    priority text NOT NULL DEFAULT 'NORMAL'
        CHECK (priority IN ('LOW', 'NORMAL', 'HIGH', 'URGENT')),
    status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN (
        'DRAFT', 'BACKLOG', 'READY', 'PROVISIONING', 'IN_PROGRESS',
        'WAITING_APPROVAL', 'BLOCKED', 'IN_REVIEW', 'DONE', 'FAILED', 'CANCELED'
    )),
    branch_name text,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, assignee_employee_id)
        REFERENCES employees (organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES users (organization_id, id),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id, id)
);

CREATE INDEX tasks_project_status_created_idx
    ON tasks (organization_id, project_id, status, created_at DESC);
CREATE INDEX tasks_assignee_status_created_idx
    ON tasks (organization_id, assignee_employee_id, status, created_at DESC);

CREATE TABLE agent_events (
    event_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    schema_version text NOT NULL CHECK (length(btrim(schema_version)) > 0),
    event_type text NOT NULL CHECK (length(btrim(event_type)) > 0),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid,
    employee_id uuid,
    attempt_id uuid,
    session_id uuid,
    workspace_id uuid,
    stream_sequence bigint NOT NULL CHECK (stream_sequence > 0),
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    producer text NOT NULL CHECK (length(btrim(producer)) > 0),
    actor jsonb NOT NULL CHECK (jsonb_typeof(actor) = 'object'),
    correlation_id uuid NOT NULL,
    causation_id uuid,
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, project_id, task_id)
        REFERENCES tasks (organization_id, project_id, id),
    FOREIGN KEY (organization_id, employee_id)
        REFERENCES employees (organization_id, id),
    UNIQUE (project_id, stream_sequence),
    UNIQUE (event_id, project_id, stream_sequence)
);

CREATE INDEX agent_events_task_sequence_idx
    ON agent_events (organization_id, task_id, stream_sequence)
    WHERE task_id IS NOT NULL;

CREATE TABLE event_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL UNIQUE,
    project_id uuid NOT NULL,
    stream_sequence bigint NOT NULL,
    payload_json jsonb NOT NULL CHECK (jsonb_typeof(payload_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error_code text,
    FOREIGN KEY (event_id, project_id, stream_sequence)
        REFERENCES agent_events (event_id, project_id, stream_sequence)
);

CREATE INDEX event_outbox_unpublished_created_idx
    ON event_outbox (created_at)
    WHERE published_at IS NULL;

CREATE TABLE idempotency_keys (
    organization_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    operation text NOT NULL CHECK (length(btrim(operation)) > 0),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response_status integer,
    response_json jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, actor_user_id, operation, idempotency_key),
    FOREIGN KEY (organization_id, actor_user_id)
        REFERENCES users (organization_id, id),
    CHECK (expires_at > created_at),
    CHECK ((response_status IS NULL) = (response_json IS NULL))
);

CREATE INDEX idempotency_keys_expiration_idx ON idempotency_keys (expires_at);

CREATE TABLE audit_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid,
    actor_user_id uuid NOT NULL,
    action text NOT NULL CHECK (length(btrim(action)) > 0),
    target_type text NOT NULL CHECK (length(btrim(target_type)) > 0),
    target_id uuid NOT NULL,
    outcome text NOT NULL CHECK (length(btrim(outcome)) > 0),
    reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    FOREIGN KEY (organization_id, actor_user_id)
        REFERENCES users (organization_id, id)
);

CREATE INDEX audit_records_project_created_idx
    ON audit_records (organization_id, project_id, created_at DESC)
    WHERE project_id IS NOT NULL;

-- +goose Down
DROP TABLE audit_records;
DROP TABLE idempotency_keys;
DROP TABLE event_outbox;
DROP TABLE agent_events;
DROP TABLE tasks;
