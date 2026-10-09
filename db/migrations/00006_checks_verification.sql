-- +goose Up
-- Checks-verification bookkeeping on execution attempts. checks_state is the
-- claim marker for the checks runner: PENDING until claimed, RUNNING while a
-- runner verifies the candidate SHA, then PASSED or FAILED as the durable
-- outcome. A RUNNING claim older than the runner's stale window is
-- reclaimable after a crash. candidate_sha pins the exact commit the checks
-- ran against so review evidence is bound to one immutable SHA.
ALTER TABLE execution_attempts
    ADD COLUMN checks_state text NOT NULL DEFAULT 'PENDING'
        CHECK (checks_state IN ('PENDING', 'RUNNING', 'PASSED', 'FAILED')),
    ADD COLUMN checks_started_at timestamptz,
    ADD COLUMN candidate_sha text
        CHECK (candidate_sha IS NULL OR candidate_sha ~ '^[0-9a-fA-F]{40}$');

CREATE INDEX execution_attempts_checks_claim_idx
    ON execution_attempts (checks_state, checks_started_at)
    WHERE state = 'RUNNING' AND checks_state IN ('PENDING', 'RUNNING');

-- +goose Down
DROP INDEX IF EXISTS execution_attempts_checks_claim_idx;

ALTER TABLE execution_attempts
    DROP COLUMN candidate_sha,
    DROP COLUMN checks_started_at,
    DROP COLUMN checks_state;
