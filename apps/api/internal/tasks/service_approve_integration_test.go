package tasks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// driveTaskToReview drives a task to its reviewable state: a SUCCEEDED
// attempt with a recorded candidate_sha, a COMPLETED session, an IN_USE
// workspace, a pull_requests row at that SHA, and the task IN_REVIEW.
func driveTaskToReview(t *testing.T, ctx context.Context, db *sql.DB, service *Service,
	organizationID, projectID, ownerID, employeeID, keyPrefix, candidateSHA string) (task Task, attemptID string) {
	t.Helper()
	started := driveTaskToProvisioning(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, keyPrefix)
	var repositoryID, workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT id::text FROM repositories WHERE organization_id = $1 AND project_id = $2`,
		organizationID, projectID).Scan(&repositoryID); err != nil {
		t.Fatalf("read repository id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM workspaces WHERE organization_id = $1 AND task_id = $2`,
		organizationID, started.ID).Scan(&workspaceID); err != nil {
		t.Fatalf("read workspace id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		UPDATE execution_attempts
		SET state = 'SUCCEEDED', candidate_sha = $3, ended_at = now()
		WHERE organization_id = $1 AND task_id = $2 RETURNING id::text`,
		organizationID, started.ID, candidateSHA).Scan(&attemptID); err != nil {
		t.Fatalf("mark attempt succeeded: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, runtime_session_id, status, started_at, ended_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'opencode', 'rt-done-'||$7, 'COMPLETED', now() - interval '1 minute', now())`,
		organizationID, projectID, started.ID, attemptID, employeeID, workspaceID, attemptID[:8]); err != nil {
		t.Fatalf("insert completed session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE workspaces SET state = 'IN_USE', worktree_ref = 'worktrees/' || $2::text
		WHERE id = $1`, workspaceID, started.ID); err != nil {
		t.Fatalf("mark workspace in use: %v", err)
	}
	var prID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO pull_requests (
			organization_id, project_id, task_id, repository_id, provider,
			external_pr_id, number, url, branch_name, head_sha, base_sha, state)
		VALUES ($1, $2, $3::uuid, $4, 'github', 'PR_' || $3::text, 42,
			'https://github.com/acme/widgets/pull/42', $5, $6,
			'f00f00f00f00f00f00f00f00f00f00f00f00f00f', 'OPEN')
		RETURNING id::text`,
		organizationID, projectID, started.ID, repositoryID, "task/"+started.ID, candidateSHA).
		Scan(&prID); err != nil {
		t.Fatalf("insert pull request: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = 'IN_REVIEW', pull_request_id = $2 WHERE id = $1`,
		started.ID, prID); err != nil {
		t.Fatalf("mark task in review: %v", err)
	}
	started.Status = "IN_REVIEW"
	return started, attemptID
}

func approveInput(organizationID, projectID, taskID, ownerID, key, headSHA string, version int64) TaskApproveInput {
	return TaskApproveInput{
		TaskControlInput: TaskControlInput{
			OrganizationID: organizationID, ProjectID: projectID, TaskID: taskID,
			ActorUserID: ownerID, IdempotencyKey: key, ExpectedVersion: version,
		},
		HeadSHA: headSHA,
	}
}

func TestApproveTaskBindsSHAAndCompletesReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := strings.Repeat("cd", 20)
	task, attemptID := driveTaskToReview(t, ctx, db, service,
		organizationID, projectID, ownerID, employeeID, "approve", candidateSHA)

	version := taskVersion(t, ctx, db, task.ID)
	approved, replayed, err := service.ApproveTask(ctx,
		approveInput(organizationID, projectID, task.ID, ownerID, "approve-001", candidateSHA, version))
	if err != nil {
		t.Fatalf("ApproveTask() error = %v", err)
	}
	if replayed || approved.Status != "DONE" || approved.Version != version+1 {
		t.Fatalf("ApproveTask() = %+v replay %t; want DONE next version", approved, replayed)
	}

	// The approval record binds the attempt, the SHA-derived digest, the
	// policy version, and the resolving owner.
	wantDigest := sha256.Sum256([]byte("review:" + candidateSHA))
	var status, digest, policy, boundAttempt, resolver string
	if err := db.QueryRowContext(ctx, `
		SELECT status, action_digest, policy_version, attempt_id::text, resolved_by_user_id::text
		FROM approvals WHERE organization_id = $1 AND task_id = $2`,
		organizationID, task.ID).Scan(&status, &digest, &policy, &boundAttempt, &resolver); err != nil {
		t.Fatalf("read approval: %v", err)
	}
	if status != "APPROVED" || digest != hex.EncodeToString(wantDigest[:]) ||
		policy != ReviewPolicyVersion || boundAttempt != attemptID || resolver != ownerID {
		t.Fatalf("approval = %s %s %s %s %s", status, digest, policy, boundAttempt, resolver)
	}
	var workspaceState string
	var retained bool
	if err := db.QueryRowContext(ctx, `
		SELECT state, retained_until IS NOT NULL FROM workspaces WHERE task_id = $1`,
		task.ID).Scan(&workspaceState, &retained); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if workspaceState != "RETAINED" || !retained {
		t.Fatalf("workspace = %s retained_until set %t; want RETAINED", workspaceState, retained)
	}

	// The four durable facts share one correlation id.
	var correlations int
	var events int
	if err := db.QueryRowContext(ctx, `
		SELECT count(DISTINCT correlation_id), count(*) FROM agent_events
		WHERE task_id = $1 AND (
			event_type IN ('approval.requested', 'approval.resolved')
			OR (event_type = 'task.state_changed' AND data->>'toState' = 'DONE')
			OR (event_type = 'workspace.state_changed' AND data->>'toState' = 'RETAINED'))`,
		task.ID).Scan(&correlations, &events); err != nil {
		t.Fatalf("read approve events: %v", err)
	}
	if correlations != 1 || events != 4 {
		t.Fatalf("approve facts = %d rows across %d correlations; want 4/1", events, correlations)
	}

	// Idempotent replay returns the stored DONE result without new facts.
	replayedTask, replayed, err := service.ApproveTask(ctx,
		approveInput(organizationID, projectID, task.ID, ownerID, "approve-001", candidateSHA, version))
	if err != nil || !replayed || replayedTask.Status != "DONE" {
		t.Fatalf("approve replay = %+v, %t, %v", replayedTask, replayed, err)
	}
	var approvals int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM approvals WHERE task_id = $1`, task.ID).Scan(&approvals); err != nil {
		t.Fatalf("count approvals: %v", err)
	}
	if approvals != 1 {
		t.Fatalf("approvals = %d, want 1 after replay", approvals)
	}
}

func TestApproveTaskRejectsMismatchedAndUnverifiableSHA(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := strings.Repeat("cd", 20)
	task, _ := driveTaskToReview(t, ctx, db, service,
		organizationID, projectID, ownerID, employeeID, "sha", candidateSHA)
	version := taskVersion(t, ctx, db, task.ID)

	// A SHA the PR does not point at is rejected, even if well-formed.
	if _, _, err := service.ApproveTask(ctx, approveInput(
		organizationID, projectID, task.ID, ownerID, "sha-bad", strings.Repeat("ef", 20), version,
	)); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("ApproveTask(stale sha) = %v, want ErrHeadMismatch", err)
	}
	// A malformed SHA is an input error, not a mismatch.
	if _, _, err := service.ApproveTask(ctx, approveInput(
		organizationID, projectID, task.ID, ownerID, "sha-short", "abcd", version,
	)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ApproveTask(malformed sha) = %v, want ErrInvalidInput", err)
	}
	// Stale expectedVersion is rejected.
	if _, _, err := service.ApproveTask(ctx, approveInput(
		organizationID, projectID, task.ID, ownerID, "sha-stale", candidateSHA, version-1,
	)); !errors.Is(err, ErrTaskVersionConflict) {
		t.Fatalf("ApproveTask(stale version) = %v, want ErrTaskVersionConflict", err)
	}
	// Nothing was written.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM approvals WHERE task_id = $1`, task.ID).Scan(&count); err != nil {
		t.Fatalf("count approvals: %v", err)
	}
	if count != 0 {
		t.Fatalf("approvals = %d after rejected approvals", count)
	}
	if got := taskStatus(t, ctx, db, task.ID); got != "IN_REVIEW" {
		t.Fatalf("task status = %s after rejected approvals", got)
	}
}

func TestApproveTaskRejectsWrongStateAndMissingEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := strings.Repeat("cd", 20)
	task, _ := driveTaskToReview(t, ctx, db, service,
		organizationID, projectID, ownerID, employeeID, "state", candidateSHA)
	version := taskVersion(t, ctx, db, task.ID)

	// IN_REVIEW without the durable PR row cannot be approved.
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET pull_request_id = NULL WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("unlink pull request: %v", err)
	}
	if _, _, err := service.ApproveTask(ctx, approveInput(
		organizationID, projectID, task.ID, ownerID, "state-nopr", candidateSHA, version,
	)); !errors.Is(err, ErrApprovalPrecondition) {
		t.Fatalf("ApproveTask(no pr row) = %v, want ErrApprovalPrecondition", err)
	}

	// A task outside IN_REVIEW cannot be approved at all.
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET status = 'IN_PROGRESS' WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("set task in progress: %v", err)
	}
	if _, _, err := service.ApproveTask(ctx, approveInput(
		organizationID, projectID, task.ID, ownerID, "state-wrong", candidateSHA, version,
	)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ApproveTask(IN_PROGRESS) = %v, want ErrInvalidTransition", err)
	}
}

func TestGetPullRequestReturnsRecordedEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := strings.Repeat("cd", 20)
	task, _ := driveTaskToReview(t, ctx, db, service,
		organizationID, projectID, ownerID, employeeID, "getpr", candidateSHA)

	pr, err := service.GetPullRequest(ctx, organizationID, projectID, task.ID)
	if err != nil {
		t.Fatalf("GetPullRequest() error = %v", err)
	}
	if pr.Number != 42 || pr.HeadSHA != candidateSHA || pr.State != "OPEN" || pr.URL == "" {
		t.Fatalf("GetPullRequest() = %+v", pr)
	}

	// A task with no recorded PR reports the evidence as missing.
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET pull_request_id = NULL WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("unlink pull request: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM pull_requests WHERE task_id = $1`, task.ID); err != nil {
		t.Fatalf("delete pull request: %v", err)
	}
	if _, err := service.GetPullRequest(ctx, organizationID, projectID, task.ID); !errors.Is(err, ErrPullRequestMissing) {
		t.Fatalf("GetPullRequest(missing) = %v, want ErrPullRequestMissing", err)
	}
}

func taskStatus(t *testing.T, ctx context.Context, db *sql.DB, taskID string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status
}
