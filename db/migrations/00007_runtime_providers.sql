-- +goose Up
-- Runtime provider registry. `provider_configs` holds per-organization
-- configuration for each supported agent runtime; the provider catalog itself
-- (which keys exist, which adapters are implemented) lives in code so a
-- misconfigured row can never invent a runtime the binary cannot execute.
--
-- Secrets are never stored: `credential_env` records only the NAME of an
-- environment variable that must exist on the runner host. The value stays
-- out of the database, events, and API responses.

CREATE TABLE provider_configs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id),
    provider_key text NOT NULL
        CHECK (provider_key IN ('opencode', 'codex', 'claude')),
    label text NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 80),
    -- http(s) endpoint for server-based providers (opencode serve). CLI
    -- providers leave this NULL.
    base_url text
        CHECK (base_url IS NULL OR length(btrim(base_url)) BETWEEN 8 AND 300),
    -- NAME of the environment variable that carries the provider credential
    -- on the runner host. Never the secret value itself.
    credential_env text
        CHECK (credential_env IS NULL OR credential_env ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, provider_key),
    UNIQUE (organization_id, id)
);

CREATE INDEX provider_configs_org_idx
    ON provider_configs (organization_id, provider_key);

-- Widen the runtime allowlists: provider keys beyond 'opencode' may now be
-- recorded, and the session runner fails them closed with
-- provider_not_implemented until a matching adapter ships.
ALTER TABLE execution_attempts
    DROP CONSTRAINT execution_attempts_runtime_type_check,
    ADD CONSTRAINT execution_attempts_runtime_type_check
        CHECK (runtime_type IN ('opencode', 'codex', 'claude'));

ALTER TABLE agent_sessions
    DROP CONSTRAINT agent_sessions_runtime_type_check,
    ADD CONSTRAINT agent_sessions_runtime_type_check
        CHECK (runtime_type IN ('opencode', 'codex', 'claude'));

-- +goose Down
ALTER TABLE execution_attempts
    DROP CONSTRAINT execution_attempts_runtime_type_check,
    ADD CONSTRAINT execution_attempts_runtime_type_check
        CHECK (runtime_type = 'opencode');
ALTER TABLE agent_sessions
    DROP CONSTRAINT agent_sessions_runtime_type_check,
    ADD CONSTRAINT agent_sessions_runtime_type_check
        CHECK (runtime_type = 'opencode');
DROP TABLE provider_configs;
