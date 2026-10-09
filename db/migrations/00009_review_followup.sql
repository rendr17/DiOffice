-- +goose Up
-- Review follow-up: Owner change requests ride on the new execution attempt
-- so the session runner can hand the feedback to Deni, and approvals get a
-- dedicated review action_type instead of 'other'.
ALTER TABLE execution_attempts
    ADD COLUMN change_request text,
    ADD CONSTRAINT execution_attempts_change_request_len
        CHECK (change_request IS NULL OR length(btrim(change_request)) BETWEEN 1 AND 2000);

ALTER TABLE approvals DROP CONSTRAINT approvals_action_type_check;
ALTER TABLE approvals ADD CONSTRAINT approvals_action_type_check
    CHECK (action_type IN (
        'dependency_change', 'destructive_action', 'permission_expansion',
        'check_override', 'merge', 'review_approve', 'other'
    ));

-- +goose Down
ALTER TABLE approvals DROP CONSTRAINT approvals_action_type_check;
ALTER TABLE approvals ADD CONSTRAINT approvals_action_type_check
    CHECK (action_type IN (
        'dependency_change', 'destructive_action', 'permission_expansion',
        'check_override', 'merge', 'other'
    ));

ALTER TABLE execution_attempts
    DROP CONSTRAINT execution_attempts_change_request_len,
    DROP COLUMN change_request;
