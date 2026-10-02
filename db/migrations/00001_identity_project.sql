-- +goose Up
CREATE TABLE organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id)
);

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id),
    email text NOT NULL CHECK (length(btrim(email)) > 0),
    display_name text NOT NULL CHECK (length(btrim(display_name)) > 0),
    role text NOT NULL DEFAULT 'OWNER' CHECK (role = 'OWNER'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX users_organization_email_lower_unique
    ON users (organization_id, lower(email));

CREATE TABLE user_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    csrf_token_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    FOREIGN KEY (organization_id, user_id)
        REFERENCES users (organization_id, id),
    CHECK (expires_at > created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX user_sessions_live_by_user
    ON user_sessions (organization_id, user_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE employees (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    role text NOT NULL CHECK (length(btrim(role)) > 0),
    department text NOT NULL CHECK (length(btrim(department)) > 0),
    status text NOT NULL DEFAULT 'IDLE' CHECK (status IN (
        'IDLE', 'PLANNING', 'RESEARCHING', 'CODING', 'TESTING',
        'PREVIEWING', 'WAITING_APPROVAL', 'BLOCKED', 'WAITING_REVIEW',
        'ERROR', 'DONE'
    )),
    default_runtime text NOT NULL DEFAULT 'opencode',
    heavy_runtime text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, slug)
);

CREATE TABLE employee_skills (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    employee_id uuid NOT NULL,
    skill_key text NOT NULL CHECK (length(btrim(skill_key)) > 0),
    level integer,
    FOREIGN KEY (organization_id, employee_id)
        REFERENCES employees (organization_id, id),
    UNIQUE (organization_id, employee_id, skill_key)
);

CREATE TABLE employee_permissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    employee_id uuid NOT NULL,
    permission_key text NOT NULL CHECK (length(btrim(permission_key)) > 0),
    policy jsonb NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
    FOREIGN KEY (organization_id, employee_id)
        REFERENCES employees (organization_id, id),
    UNIQUE (organization_id, employee_id, permission_key)
);

CREATE TABLE projects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    last_event_sequence bigint NOT NULL DEFAULT 0 CHECK (last_event_sequence >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE TABLE repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider = 'github'),
    owner text NOT NULL CHECK (length(btrim(owner)) > 0),
    repo_name text NOT NULL CHECK (length(btrim(repo_name)) > 0),
    default_branch text NOT NULL CHECK (length(btrim(default_branch)) > 0),
    external_repo_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, project_id)
);

CREATE UNIQUE INDEX repositories_provider_external_id_unique
    ON repositories (provider, external_repo_id)
    WHERE external_repo_id IS NOT NULL;

-- +goose Down
DROP TABLE repositories;
DROP TABLE projects;
DROP TABLE employee_permissions;
DROP TABLE employee_skills;
DROP TABLE employees;
DROP TABLE user_sessions;
DROP TABLE users;
DROP TABLE organizations;
