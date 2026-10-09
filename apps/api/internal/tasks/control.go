package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

const (
	retryTaskOperation  = "tasks.retry"
	cancelTaskOperation = "tasks.cancel"
)

// Canonical owner-control transitions: Retry reopens BLOCKED/FAILED work for
// a fresh explicit Start; Cancel terminates a task in any state the contract
// allows. Both quiesce the execution layer atomically — active attempts and
// sessions are CANCELED, workspaces move to CLEANUP_PENDING, and pending
// approvals are resolved CANCELED — so no durable artifact can be mistaken
// for still-running work.
var (
	retriableTaskStates  = []string{"BLOCKED", "FAILED"}
	cancelableTaskStates = []string{"IN_PROGRESS", "WAITING_APPROVAL", "BLOCKED", "IN_REVIEW", "FAILED"}
)

// SessionAborter asks the runtime to terminate a provider session. It is
// satisfied by sessionrunner.OpenCodeRuntime and injected only when the API
// process is configured with OPENCODE_SERVER_URL; without it cancellation is
// still recorded durably and provider cleanup falls to reconciliation.
type SessionAborter interface {
	Abort(ctx context.Context, sessionID string) error
}

// WithSessionAborter configures best-effort provider-session termination
// during Cancel/Retry. Returns the service for chaining.
func (s *Service) WithSessionAborter(aborter SessionAborter) *Service {
	s.aborter = aborter
	return s
}

type TaskControlInput struct {
	OrganizationID  string
	ProjectID       string
	TaskID          string
	ActorUserID     string
	IdempotencyKey  string
	ExpectedVersion int64
	Reason          string
}

func (input TaskControlInput) validate() error {
	if !validUUID(input.OrganizationID) || !validUUID(input.ProjectID) ||
		!validUUID(input.TaskID) || !validUUID(input.ActorUserID) {
		return fmt.Errorf("%w: organizationId, projectId, taskId, and actorUserId must be UUIDs", ErrInvalidInput)
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 {
		return fmt.Errorf("%w: idempotencyKey must contain 1 to 128 characters", ErrInvalidInput)
	}
	if input.ExpectedVersion < 1 {
		return fmt.Errorf("%w: expectedVersion must be a positive integer", ErrInvalidInput)
	}
	if len(input.Reason) > 500 {
		return fmt.Errorf("%w: reason must be at most 500 characters", ErrInvalidInput)
	}
	return nil
}

// RetryTask performs BLOCKED|FAILED -> READY. It never reuses an ambiguous
// worker: any still-active attempt/session is canceled, the workspace is
// marked for cleanup, and a later explicit Start provisions a new attempt.
func (s *Service) RetryTask(ctx context.Context, input TaskControlInput) (Task, bool, error) {
	return s.runControlTransition(ctx, input, retryTaskOperation, retriableTaskStates, "READY", "owner_retry", "task.retry")
}

// CancelTask performs the canonical Owner Cancel from any cancelable state,
// terminating the attempt/session records and marking the workspace for
// cleanup. Provider abort is best-effort after the durable commit.
func (s *Service) CancelTask(ctx context.Context, input TaskControlInput) (Task, bool, error) {
	return s.runControlTransition(ctx, input, cancelTaskOperation, cancelableTaskStates, "CANCELED", "owner_canceled", "task.cancel")
}

func (s *Service) runControlTransition(ctx context.Context, input TaskControlInput,
	operation string, allowedStates []string, toState, reason, auditAction string) (Task, bool, error) {
	if err := input.validate(); err != nil {
		return Task{}, false, err
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}
	requestHash, err := hashTransitionRequest(operation, input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, input.Reason)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash task control request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task control transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimTransitionIdempotency(ctx, tx, input.OrganizationID, input.ActorUserID, operation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent control replay: %w", err)
		}
		return task, true, nil
	}
	task, err = loadTaskForTransition(ctx, tx, input.OrganizationID, input.ProjectID, input.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for control transition: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	allowed := false
	for _, state := range allowedStates {
		if task.Status == state {
			allowed = true
			break
		}
	}
	if !allowed {
		return Task{}, false, ErrInvalidTransition
	}

	correlationID, err := newUUID()
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate control correlation id: %w", err)
	}
	runtimeSessions, err := quiesceExecution(ctx, tx, input, task, correlationID, reason)
	if err != nil {
		return Task{}, false, err
	}

	var taskSequence int64
	if taskSequence, err = nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID); err != nil {
		return Task{}, false, err
	}
	fromState := task.Status
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = $5, task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND task_version = $4
		RETURNING task_version, updated_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, toState).
		Scan(&task.Version, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskVersionConflict
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("update task to %s: %w", toState, err)
	}
	task.Status = toState

	eventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID,
		StreamSequence: taskSequence, Producer: "api",
		Actor: ownerActor(input.ActorUserID), CorrelationID: correlationID,
		EventType: "task.state_changed",
		Data: map[string]any{
			"fromState": fromState, "toState": toState,
			"taskVersion": task.Version, "reason": controlReason(reason, input.Reason),
		},
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("insert task %s event: %w", toState, err)
	}
	if err := insertOutboxRecord(ctx, tx, eventID, input.ProjectID, taskSequence, envelope); err != nil {
		return Task{}, false, err
	}
	if err := insertAuditRecord(ctx, tx, input.OrganizationID, input.ProjectID, input.ActorUserID, auditAction, task.ID); err != nil {
		return Task{}, false, err
	}
	if err := storeTransitionResult(ctx, tx, input.OrganizationID, input.ActorUserID, operation, input.IdempotencyKey, requestHash, task); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit task control transition: %w", err)
	}

	// Provider abort happens after the durable commit: the owner-visible
	// truth is already stored, and a dead runtime must not block cancelation.
	if s.aborter != nil {
		for _, sessionID := range runtimeSessions {
			if err := s.aborter.Abort(ctx, sessionID); err != nil {
				slog.Warn("provider session abort failed after control transition",
					"session", sessionID, "task", task.ID, "error", err)
			}
		}
	}
	return task, false, nil
}

func controlReason(code, detail string) string {
	if strings.TrimSpace(detail) == "" {
		return code
	}
	return code + ": " + strings.TrimSpace(detail)
}

// quiesceExecution cancels every still-active attempt/session row for the
// task, marks writable or failed workspaces for cleanup, and resolves pending
// approvals — each with its own durable event under one correlation id. It
// returns provider session ids that were live so the caller can abort them
// after commit.
func quiesceExecution(ctx context.Context, tx *sql.Tx, input TaskControlInput,
	task Task, correlationID, reason string) ([]string, error) {
	base := transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID,
		Producer: "api", Actor: ownerActor(input.ActorUserID),
		CorrelationID: correlationID,
	}
	emit := func(input transitionEventInput) (string, error) {
		sequence, err := nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID)
		if err != nil {
			return "", err
		}
		input.StreamSequence = sequence
		eventID, envelope, err := insertTransitionEvent(ctx, tx, input)
		if err != nil {
			return "", err
		}
		return eventID, insertOutboxRecord(ctx, tx, eventID, input.ProjectID, sequence, envelope)
	}

	// Cancel live sessions first so their events precede the attempt's.
	rows, err := tx.QueryContext(ctx, `
		UPDATE agent_sessions SET status = 'CANCELED', ended_at = now()
		WHERE organization_id = $1 AND task_id = $2
			AND status IN ('STARTING', 'RUNNING', 'PAUSED')
		RETURNING id::text, COALESCE(runtime_session_id, '')`,
		input.OrganizationID, task.ID)
	if err != nil {
		return nil, fmt.Errorf("cancel live sessions: %w", err)
	}
	var canceled []struct{ id, runtimeID string }
	for rows.Next() {
		var row struct{ id, runtimeID string }
		if err := rows.Scan(&row.id, &row.runtimeID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan canceled session: %w", err)
		}
		canceled = append(canceled, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var runtimeIDs []string
	for _, session := range canceled {
		if _, err := emit(withInput(base, transitionEventInput{
			SessionID: session.id, EventType: "session.canceled",
			Data: map[string]any{"reason": controlReason(reason, input.Reason)},
		})); err != nil {
			return nil, fmt.Errorf("record session cancellation: %w", err)
		}
		if session.runtimeID != "" {
			runtimeIDs = append(runtimeIDs, session.runtimeID)
		}
	}
	return runtimeIDs, quiesceAttemptsAndWorkspaces(ctx, tx, input, task, base, emit)
}

// quiesceAttemptsAndWorkspaces cancels active attempts and moves writable or
// failed workspaces to CLEANUP_PENDING, emitting their state facts.
func quiesceAttemptsAndWorkspaces(ctx context.Context, tx *sql.Tx,
	input TaskControlInput, task Task, base transitionEventInput,
	emit func(transitionEventInput) (string, error)) error {
	type attemptRow struct {
		id, state     string
		attemptNumber int64
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, state, attempt_number FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2 AND state = ANY($3)
		FOR UPDATE`, input.OrganizationID, task.ID, attemptActiveStates)
	if err != nil {
		return fmt.Errorf("list active attempts: %w", err)
	}
	var attempts []attemptRow
	for rows.Next() {
		var row attemptRow
		if err := rows.Scan(&row.id, &row.state, &row.attemptNumber); err != nil {
			rows.Close()
			return fmt.Errorf("scan active attempt: %w", err)
		}
		attempts = append(attempts, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, attempt := range attempts {
		if _, err := tx.ExecContext(ctx, `
			UPDATE execution_attempts SET state = 'CANCELED', ended_at = now(), updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND state = $3`,
			input.OrganizationID, attempt.id, attempt.state); err != nil {
			return fmt.Errorf("cancel attempt %s: %w", attempt.id, err)
		}
		if _, err := emit(withInput(base, transitionEventInput{
			AttemptID: attempt.id, EventType: "execution_attempt.state_changed",
			Data: map[string]any{
				"fromState": attempt.state, "toState": "CANCELED",
				"attemptNumber": attempt.attemptNumber, "reasonCode": "owner_control",
			},
		})); err != nil {
			return fmt.Errorf("record attempt cancellation: %w", err)
		}
	}

	type workspaceRow struct{ id, state, branchName, workerProfile string }
	wsRows, err := tx.QueryContext(ctx, `
		SELECT id::text, state, branch_name, worker_profile FROM workspaces
		WHERE organization_id = $1 AND task_id = $2
			AND state IN ('PROVISIONING', 'READY', 'IN_USE', 'FAILED')
		FOR UPDATE`, input.OrganizationID, task.ID)
	if err != nil {
		return fmt.Errorf("list open workspaces: %w", err)
	}
	var workspaces []workspaceRow
	for wsRows.Next() {
		var row workspaceRow
		if err := wsRows.Scan(&row.id, &row.state, &row.branchName, &row.workerProfile); err != nil {
			wsRows.Close()
			return fmt.Errorf("scan workspace: %w", err)
		}
		workspaces = append(workspaces, row)
	}
	wsRows.Close()
	if err := wsRows.Err(); err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if _, err := tx.ExecContext(ctx, `
			UPDATE workspaces SET state = 'CLEANUP_PENDING', updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND state = $3`,
			input.OrganizationID, workspace.id, workspace.state); err != nil {
			return fmt.Errorf("mark workspace cleanup pending: %w", err)
		}
		if _, err := emit(withInput(base, transitionEventInput{
			WorkspaceID: workspace.id, EventType: "workspace.state_changed",
			Data: map[string]any{
				"fromState": workspace.state, "toState": "CLEANUP_PENDING",
				"branchName": workspace.branchName, "workerProfile": workspace.workerProfile,
			},
		})); err != nil {
			return fmt.Errorf("record workspace cleanup: %w", err)
		}
	}

	// Resolve pending approvals so a stale gate can never be acted on later.
	type approvalRow struct{ id string }
	apprRows, err := tx.QueryContext(ctx, `
		UPDATE approvals SET status = 'CANCELED', resolved_at = now(),
			resolved_by_user_id = $3, reason = 'task control transition'
		WHERE organization_id = $1 AND task_id = $2 AND status = 'PENDING'
		RETURNING id::text`, input.OrganizationID, task.ID, input.ActorUserID)
	if err != nil {
		return fmt.Errorf("cancel pending approvals: %w", err)
	}
	var approvals []approvalRow
	for apprRows.Next() {
		var row approvalRow
		if err := apprRows.Scan(&row.id); err != nil {
			apprRows.Close()
			return fmt.Errorf("scan canceled approval: %w", err)
		}
		approvals = append(approvals, row)
	}
	apprRows.Close()
	if err := apprRows.Err(); err != nil {
		return err
	}
	for _, approval := range approvals {
		if _, err := emit(withInput(base, transitionEventInput{
			EventType: "approval.resolved",
			Data: map[string]any{
				"approvalId": approval.id, "outcome": "CANCELED", "reason": "task control transition",
			},
		})); err != nil {
			return fmt.Errorf("record approval cancellation: %w", err)
		}
	}
	return nil
}

func withInput(base, overlay transitionEventInput) transitionEventInput {
	if overlay.SessionID != "" {
		base.SessionID = overlay.SessionID
	}
	if overlay.AttemptID != "" {
		base.AttemptID = overlay.AttemptID
	}
	if overlay.WorkspaceID != "" {
		base.WorkspaceID = overlay.WorkspaceID
	}
	if overlay.CausationID != "" {
		base.CausationID = overlay.CausationID
	}
	base.EventType = overlay.EventType
	base.Data = overlay.Data
	return base
}
