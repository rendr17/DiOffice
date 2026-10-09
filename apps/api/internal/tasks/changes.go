package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
)

const requestChangesOperation = "tasks.request_changes"

// changeRequestMaxLength bounds the Owner feedback stored on the next attempt.
const changeRequestMaxLength = 2000

// ErrWorkspaceUnavailable rejects a change request when the preserved
// task workspace can no longer host a continuation attempt.
var ErrWorkspaceUnavailable = errors.New("task workspace is not available for a continuation attempt")

// TaskChangesInput carries the Owner's request for changes on a reviewable
// task. Reason is required — it is the feedback handed to Deni's next
// session.
type TaskChangesInput struct {
	TaskControlInput
	Reason string
}

func (input TaskChangesInput) validate() error {
	if err := input.TaskControlInput.validate(); err != nil {
		return err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > changeRequestMaxLength {
		return fmt.Errorf("%w: reason is required and must be at most %d characters",
			ErrInvalidInput, changeRequestMaxLength)
	}
	return nil
}

// RequestChanges performs the canonical IN_REVIEW → IN_PROGRESS transition.
// The contract preserves the task branch/workspace: the recorded workspace is
// returned to READY and a new PROVISIONING attempt carrying the change
// request is created, so the existing session runner resumes Deni on the same
// worktree instead of reprovisioning from scratch. The recorded pull request
// stays; a later publish updates it and emits pull_request.updated.
func (s *Service) RequestChanges(ctx context.Context, input TaskChangesInput) (Task, bool, error) {
	if err := input.validate(); err != nil {
		return Task{}, false, err
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}
	reason := strings.TrimSpace(input.Reason)
	requestHash, err := hashTransitionRequest(requestChangesOperation,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, reason)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash request-changes request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin request-changes transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimTransitionIdempotency(ctx, tx, input.OrganizationID,
		input.ActorUserID, requestChangesOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent request-changes replay: %w", err)
		}
		return task, true, nil
	}
	task, err = loadTaskForTransition(ctx, tx, input.OrganizationID, input.ProjectID, input.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for change request: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	if task.Status != "IN_REVIEW" {
		return Task{}, false, ErrInvalidTransition
	}

	// Lock the preserved workspace; only a writable/retained worktree can host
	// the continuation. A cleaned or absent workspace fails closed — the Owner
	// can cancel and create a new task instead of losing the branch silently.
	var workspaceID, workspaceState, branchName, workerProfile string
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, state, branch_name, worker_profile FROM workspaces
		WHERE organization_id = $1 AND task_id = $2
		FOR UPDATE`,
		input.OrganizationID, input.TaskID).Scan(
		&workspaceID, &workspaceState, &branchName, &workerProfile)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrWorkspaceUnavailable
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("lock workspace for change request: %w", err)
	}
	switch workspaceState {
	case "IN_USE", "READY", "RETAINED":
	default:
		return Task{}, false, ErrWorkspaceUnavailable
	}

	// The new attempt inherits the task's runtime and continues the attempt
	// sequence. The one-active-attempt index protects against racing inserts.
	var runtimeType string
	var lastAttempt int64
	err = tx.QueryRowContext(ctx, `
		SELECT runtime_type, attempt_number FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2
		ORDER BY attempt_number DESC LIMIT 1`,
		input.OrganizationID, input.TaskID).Scan(&runtimeType, &lastAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrApprovalPrecondition
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("read latest attempt: %w", err)
	}
	attemptNumber := lastAttempt + 1
	var attemptID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id,
			attempt_number, state, runtime_type, change_request)
		VALUES ($1, $2, $3, $4, $5, 'PROVISIONING', $6, $7)
		RETURNING id::text`,
		input.OrganizationID, input.ProjectID, input.TaskID, task.AssigneeEmployeeID,
		attemptNumber, runtimeType, reason).Scan(&attemptID); err != nil {
		if isUniqueViolation(err) {
			return Task{}, false, ErrActiveAttemptExists
		}
		return Task{}, false, fmt.Errorf("create continuation attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workspaces SET state = 'READY', retained_until = NULL, updated_at = now()
		WHERE organization_id = $1 AND id = $2`,
		input.OrganizationID, workspaceID); err != nil {
		return Task{}, false, fmt.Errorf("return workspace to ready: %w", err)
	}

	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate request-changes correlation id: %w", err)
	}
	base := transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID,
		Producer: "api", Actor: ownerActor(input.ActorUserID),
		CorrelationID: correlationID,
	}
	emit := func(in transitionEventInput) (string, error) {
		sequence, err := nextEventSequence(ctx, tx, in.OrganizationID, in.ProjectID)
		if err != nil {
			return "", err
		}
		in.StreamSequence = sequence
		eventID, envelope, err := insertTransitionEvent(ctx, tx, in)
		if err != nil {
			return "", err
		}
		return eventID, insertOutboxRecord(ctx, tx, eventID, in.ProjectID, sequence, envelope)
	}

	attemptEventID, err := emit(withInput(base, transitionEventInput{
		AttemptID: attemptID, EventType: "execution_attempt.state_changed",
		Data: map[string]any{
			"fromState": "CREATED", "toState": "PROVISIONING",
			"attemptNumber": attemptNumber, "reasonCode": "change_requested",
		},
	}))
	if err != nil {
		return Task{}, false, fmt.Errorf("record continuation attempt event: %w", err)
	}
	if _, err := emit(withInput(base, transitionEventInput{
		WorkspaceID: workspaceID, CausationID: attemptEventID,
		EventType: "workspace.state_changed",
		Data: map[string]any{
			"fromState": workspaceState, "toState": "READY",
			"branchName": branchName, "workerProfile": workerProfile,
		},
	})); err != nil {
		return Task{}, false, fmt.Errorf("record workspace ready event: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'IN_PROGRESS', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND task_version = $4
		RETURNING task_version, updated_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion).
		Scan(&task.Version, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskVersionConflict
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("update task to in progress: %w", err)
	}
	task.Status = "IN_PROGRESS"
	if _, err := emit(withInput(base, transitionEventInput{
		AttemptID: attemptID, EventType: "task.state_changed",
		Data: map[string]any{
			"fromState": "IN_REVIEW", "toState": "IN_PROGRESS",
			"taskVersion": task.Version,
			"reason":      controlReason("owner_requested_changes", reason),
		},
	})); err != nil {
		return Task{}, false, fmt.Errorf("record task in-progress event: %w", err)
	}
	if err := insertAuditRecord(ctx, tx, input.OrganizationID, input.ProjectID,
		input.ActorUserID, "task.request_changes", task.ID); err != nil {
		return Task{}, false, err
	}
	if err := storeTransitionResult(ctx, tx, input.OrganizationID, input.ActorUserID,
		requestChangesOperation, input.IdempotencyKey, requestHash, task); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit request changes: %w", err)
	}
	return task, false, nil
}
