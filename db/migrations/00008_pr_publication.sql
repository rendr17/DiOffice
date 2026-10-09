-- +goose Up
-- Pull-request publication tracking. After an attempt SUCCEEDS with a
-- recorded candidate_sha, a separate publisher pushes that SHA to the task
-- branch and opens/adopts the GitHub pull request. `pr_state` is the
-- restart-safe claim marker; `pr_started_at` bounds stale RUNNING claims and
-- paces FAILED retries; `pr_failure_count` stops publication from looping
-- forever against a persistent GitHub failure.
ALTER TABLE execution_attempts
    ADD COLUMN pr_state text NOT NULL DEFAULT 'PENDING'
        CHECK (pr_state IN ('PENDING', 'RUNNING', 'PUBLISHED', 'FAILED')),
    ADD COLUMN pr_started_at timestamptz,
    ADD COLUMN pr_failure_count integer NOT NULL DEFAULT 0
        CHECK (pr_failure_count >= 0),
    ADD COLUMN pr_last_error text;

-- +goose Down
ALTER TABLE execution_attempts
    DROP COLUMN pr_last_error,
    DROP COLUMN pr_failure_count,
    DROP COLUMN pr_started_at,
    DROP COLUMN pr_state;
