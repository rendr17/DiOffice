package tasks

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

type recordingAborter struct{ aborted []string }

func (a *recordingAborter) Abort(_ context.Context, sessionID string) error {
	a.aborted = append(a.aborted, sessionID)
	return nil
}

// driveTaskToProvisioning creates a task and runs it through MarkReady +
// StartExecution so a real attempt + workspace pair exists.
func driveTaskToProvisioning(t *testing.T, ctx context.Context, db *sql.DB, service *Service,
	organizationID, projectID, ownerID, employeeID, keyPrefix string) Task {
	t.Helper()
	createInput := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, keyPrefix+"-create")
	createInput.ManifestDigest = strings.Repeat("ab", 32)
	draft, _, err := service.CreateDraft(ctx, createInput)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	ready, _, err := service.MarkReady(ctx, MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: keyPrefix + "-ready", ExpectedVersion: draft.Version,
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	started, _, err := service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: keyPrefix + "-start", ExpectedVersion: ready.Version,
	})
	if err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}
	return started
}

func taskVersion(t *testing.T, ctx context.Context, db *sql.DB, taskID string) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT task_version FROM tasks WHERE id = $1`, taskID).Scan(&version); err != nil {
		t.Fatalf("read task version: %v", err)
	}
	return version
}

func TestRetryTaskReopensBlockedTaskAndQuiescesExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	aborter := &recordingAborter{}
	service := NewService(db).WithSessionAborter(aborter)

	started := driveTaskToProvisioning(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, "retry")

	// Simulate the state after a failed run: task BLOCKED, workspace FAILED,
	// but a session row is still nominally RUNNING (the reconciler missed it).
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'BLOCKED' WHERE id = $1`, started.ID); err != nil {
		t.Fatalf("set task BLOCKED: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET state = 'RUNNING' WHERE organization_id = $1 AND task_id = $2`,
		organizationID, started.ID); err != nil {
		t.Fatalf("set attempt RUNNING: %v", err)
	}
	var attemptID, workspaceID string
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2`, organizationID, started.ID).
		Scan(&attemptID); err != nil {
		t.Fatalf("read attempt id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM workspaces
		WHERE organization_id = $1 AND task_id = $2`, organizationID, started.ID).
		Scan(&workspaceID); err != nil {
		t.Fatalf("read workspace id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_sessions (organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, runtime_session_id, status, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'opencode', 'rt-stuck-1', 'RUNNING', now())`,
		organizationID, projectID, started.ID, attemptID, employeeID, workspaceID); err != nil {
		t.Fatalf("insert RUNNING session: %v", err)
	}

	version := taskVersion(t, ctx, db, started.ID)
	input := TaskControlInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: started.ID,
		ActorUserID: ownerID, IdempotencyKey: "retry-001", ExpectedVersion: version,
	}
	task, replayed, err := service.RetryTask(ctx, input)
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if replayed || task.Status != "READY" || task.Version != version+1 {
		t.Fatalf("RetryTask() = task %+v replay %t; want READY next version", task, replayed)
	}

	var attemptState, sessionStatus, workspaceState string
	if err := db.QueryRowContext(ctx, `
		SELECT state FROM execution_attempts WHERE id = $1`, attemptID).Scan(&attemptState); err != nil {
		t.Fatalf("read attempt state: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT status FROM agent_sessions WHERE runtime_session_id = 'rt-stuck-1'`).Scan(&sessionStatus); err != nil {
		t.Fatalf("read session status: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT state FROM workspaces WHERE id = $1`, workspaceID).Scan(&workspaceState); err != nil {
		t.Fatalf("read workspace state: %v", err)
	}
	if attemptState != "CANCELED" || sessionStatus != "CANCELED" || workspaceState != "CLEANUP_PENDING" {
		t.Fatalf("quiesce = attempt %s, session %s, workspace %s; want CANCELED/CANCELED/CLEANUP_PENDING",
			attemptState, sessionStatus, workspaceState)
	}
	if len(aborter.aborted) != 1 || aborter.aborted[0] != "rt-stuck-1" {
		t.Fatalf("aborted sessions = %v, want [rt-stuck-1]", aborter.aborted)
	}

	// One correlation id groups all events of this transition. Restrict to the
	// quiesce facts so earlier lifecycle events of the fixture don't match.
	var distinctCorrelations int
	if err := db.QueryRowContext(ctx, `
		SELECT count(DISTINCT correlation_id) FROM agent_events
		WHERE task_id = $1 AND (
			event_type = 'session.canceled'
			OR (event_type = 'execution_attempt.state_changed' AND data->>'toState' = 'CANCELED')
			OR (event_type = 'workspace.state_changed' AND data->>'toState' = 'CLEANUP_PENDING')
		)`, started.ID).Scan(&distinctCorrelations); err != nil {
		t.Fatalf("count correlations: %v", err)
	}
	if distinctCorrelations != 1 {
		t.Fatalf("quiesce correlations = %d, want 1", distinctCorrelations)
	}

	replayedTask, replayed, err := service.RetryTask(ctx, input)
	if err != nil || !replayed || replayedTask.Status != "READY" {
		t.Fatalf("idempotent RetryTask() = %+v replay %t err %v; want READY replay", replayedTask, replayed, err)
	}

	// After retry, a fresh explicit Start must work — the old attempt is gone.
	restarted, _, err := service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: task.ID,
		ActorUserID: ownerID, IdempotencyKey: "retry-start-2", ExpectedVersion: task.Version,
	})
	if err != nil || restarted.Status != "PROVISIONING" {
		t.Fatalf("StartExecution() after retry = %+v err %v; want PROVISIONING", restarted, err)
	}
	var attemptCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM execution_attempts WHERE task_id = $1`, started.ID).Scan(&attemptCount); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attemptCount != 2 {
		t.Fatalf("attempts = %d, want original CANCELED + fresh CREATED", attemptCount)
	}
}

func TestCancelTaskTerminatesInProgressRunAndResolvesApprovals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	aborter := &recordingAborter{}
	service := NewService(db).WithSessionAborter(aborter)

	started := driveTaskToProvisioning(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, "cancel")
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'IN_PROGRESS' WHERE id = $1`, started.ID); err != nil {
		t.Fatalf("set task IN_PROGRESS: %v", err)
	}
	var attemptID, workspaceID string
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2`, organizationID, started.ID).
		Scan(&attemptID); err != nil {
		t.Fatalf("read attempt id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM workspaces
		WHERE organization_id = $1 AND task_id = $2`, organizationID, started.ID).
		Scan(&workspaceID); err != nil {
		t.Fatalf("read workspace id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET state = 'RUNNING' WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("set attempt RUNNING: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET state = 'IN_USE' WHERE id = $1`, workspaceID); err != nil {
		t.Fatalf("set workspace IN_USE: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_sessions (organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, runtime_session_id, status, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'opencode', 'rt-live-9', 'RUNNING', now())`,
		organizationID, projectID, started.ID, attemptID, employeeID, workspaceID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO approvals (organization_id, project_id, task_id, attempt_id,
			requested_by_employee_id, action_type, action_digest, policy_version, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'permission_expansion', $6, 'v0.1', now() + interval '1 hour')`,
		organizationID, projectID, started.ID, attemptID, employeeID, strings.Repeat("cd", 32)); err != nil {
		t.Fatalf("insert pending approval: %v", err)
	}

	version := taskVersion(t, ctx, db, started.ID)
	task, replayed, err := service.CancelTask(ctx, TaskControlInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: started.ID,
		ActorUserID: ownerID, IdempotencyKey: "cancel-001", ExpectedVersion: version,
		Reason: "owner stopped the run",
	})
	if err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	if replayed || task.Status != "CANCELED" || task.Version != version+1 {
		t.Fatalf("CancelTask() = task %+v replay %t; want CANCELED", task, replayed)
	}

	var sessionStatus, workspaceState, approvalStatus, attemptState string
	if err := db.QueryRowContext(ctx, `
		SELECT status FROM agent_sessions WHERE runtime_session_id = 'rt-live-9'`).Scan(&sessionStatus); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM workspaces WHERE id = $1`, workspaceID).Scan(&workspaceState); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT status FROM approvals WHERE task_id = $1`, started.ID).Scan(&approvalStatus); err != nil {
		t.Fatalf("read approval: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM execution_attempts WHERE id = $1`, attemptID).Scan(&attemptState); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if sessionStatus != "CANCELED" || workspaceState != "CLEANUP_PENDING" ||
		approvalStatus != "CANCELED" || attemptState != "CANCELED" {
		t.Fatalf("quiesce = session %s workspace %s approval %s attempt %s; all want CANCELED/CLEANUP_PENDING",
			sessionStatus, workspaceState, approvalStatus, attemptState)
	}
	if len(aborter.aborted) != 1 || aborter.aborted[0] != "rt-live-9" {
		t.Fatalf("aborted sessions = %v, want [rt-live-9]", aborter.aborted)
	}

	// A terminal task can no longer be started or retried.
	version = taskVersion(t, ctx, db, started.ID)
	_, _, err = service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: started.ID,
		ActorUserID: ownerID, IdempotencyKey: "cancel-start-again", ExpectedVersion: version,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("StartExecution() on CANCELED = %v, want ErrInvalidTransition", err)
	}
}

func TestControlTransitionsRejectWrongStatesAndStaleVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	draft, _, err := service.CreateDraft(ctx, testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "control-draft"))
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}

	base := TaskControlInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "ctl-1", ExpectedVersion: draft.Version,
	}
	if _, _, err := service.RetryTask(ctx, base); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("RetryTask() on DRAFT = %v, want ErrInvalidTransition", err)
	}
	if _, _, err := service.CancelTask(ctx, base); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CancelTask() on DRAFT = %v, want ErrInvalidTransition", err)
	}
	stale := base
	stale.IdempotencyKey = "ctl-2"
	stale.ExpectedVersion = draft.Version + 9
	if _, _, err := service.RetryTask(ctx, stale); !errors.Is(err, ErrTaskVersionConflict) {
		t.Fatalf("RetryTask() stale version = %v, want ErrTaskVersionConflict", err)
	}
}
