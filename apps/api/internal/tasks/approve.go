package tasks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const approveTaskOperation = "tasks.approve"

// ReviewPolicyVersion identifies the single approval policy v0.1 applies:
// the Owner approves exactly the recorded pull-request head SHA. Any newer
// head makes the recorded approval stale (DONE → IN_REVIEW on head change).
const ReviewPolicyVersion = "review-sha.v1"

// reviewApprovalExpiry bounds the validity window of the approval request
// record. The approval is resolved in the same transaction, so the value is
// nominal but contract-required.
const reviewApprovalExpiry = 15 * time.Minute

// workspaceRetention keeps a DONE task's worktree for possible reopening or
// evidence review before cleanup sweeps it.
const workspaceRetention = 7 * 24 * time.Hour

var (
	// ErrHeadMismatch rejects an approval submitted against a SHA that is not
	// the recorded pull-request head — the Owner's display was stale.
	ErrHeadMismatch = errors.New("approved head sha does not match the recorded pull request head")
	// ErrApprovalPrecondition rejects approval when the durable evidence a
	// review requires (recorded PR, succeeded attempt at that SHA) is absent.
	ErrApprovalPrecondition = errors.New("task lacks the durable review evidence required for approval")
	// ErrPullRequestMissing means no pull_requests row exists for the task.
	ErrPullRequestMissing = errors.New("task has no recorded pull request")

	headSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// TaskApproveInput carries the Owner's explicit approval of one exact SHA.
type TaskApproveInput struct {
	TaskControlInput
	HeadSHA string
}

func (input TaskApproveInput) validate() error {
	if err := input.TaskControlInput.validate(); err != nil {
		return err
	}
	if !headSHAPattern.MatchString(input.HeadSHA) {
		return fmt.Errorf("%w: headSha must be a 40-character lowercase hex SHA", ErrInvalidInput)
	}
	return nil
}

// PullRequest is the owner-visible pull-request evidence for a task.
type PullRequest struct {
	ID         string  `json:"id"`
	Number     int     `json:"number"`
	URL        string  `json:"url"`
	BranchName string  `json:"branchName"`
	HeadSHA    string  `json:"headSha"`
	BaseSHA    string  `json:"baseSha"`
	State      string  `json:"state"`
	MergedAt   *string `json:"mergedAt"`
}

// GetPullRequest returns the recorded pull request for a task.
func (s *Service) GetPullRequest(ctx context.Context, organizationID, projectID, taskID string) (PullRequest, error) {
	if !validUUID(organizationID) || !validUUID(projectID) || !validUUID(taskID) {
		return PullRequest{}, fmt.Errorf("%w: organizationId, projectId, and taskId must be UUIDs", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return PullRequest{}, errors.New("task service database is not configured")
	}
	var pr PullRequest
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, number, url, branch_name, head_sha, base_sha, state,
			to_char(merged_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM pull_requests
		WHERE organization_id = $1 AND project_id = $2 AND task_id = $3`,
		organizationID, projectID, taskID).Scan(
		&pr.ID, &pr.Number, &pr.URL, &pr.BranchName, &pr.HeadSHA, &pr.BaseSHA, &pr.State, &pr.MergedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequest{}, ErrPullRequestMissing
	}
	if err != nil {
		return PullRequest{}, fmt.Errorf("read pull request: %w", err)
	}
	return pr, nil
}

// ApproveTask performs the canonical IN_REVIEW → DONE transition. The Owner
// submits the SHA they saw; it must equal the recorded pull-request head and
// a SUCCEEDED attempt's candidate_sha, so approval can never bind a SHA the
// checks runner did not verify. The approval row is recorded against the
// action digest of that SHA and the review policy version. Merge stays a
// separate Owner action — DONE only means the SHA was accepted.
func (s *Service) ApproveTask(ctx context.Context, input TaskApproveInput) (Task, bool, error) {
	if err := input.validate(); err != nil {
		return Task{}, false, err
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}
	requestHash, err := hashTransitionRequest(approveTaskOperation,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion,
		input.HeadSHA)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash task approve request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task approve transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimTransitionIdempotency(ctx, tx, input.OrganizationID,
		input.ActorUserID, approveTaskOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent approve replay: %w", err)
		}
		return task, true, nil
	}
	task, err = loadTaskForTransition(ctx, tx, input.OrganizationID, input.ProjectID, input.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for approval: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	if task.Status != "IN_REVIEW" {
		return Task{}, false, ErrInvalidTransition
	}

	// The approved SHA must equal the recorded PR head — and that head must
	// be a verified candidate from a SUCCEEDED attempt, so approval can only
	// ever bind the exact SHA evidence was produced for.
	var prID, recordedHead string
	err = tx.QueryRowContext(ctx, `
		SELECT p.id::text, p.head_sha FROM pull_requests p
		JOIN tasks t ON t.pull_request_id = p.id
		WHERE t.organization_id = $1 AND t.project_id = $2 AND t.id = $3`,
		input.OrganizationID, input.ProjectID, input.TaskID).Scan(&prID, &recordedHead)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrApprovalPrecondition
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("read pull request for approval: %w", err)
	}
	if input.HeadSHA != recordedHead {
		return Task{}, false, ErrHeadMismatch
	}
	var attemptID string
	err = tx.QueryRowContext(ctx, `
		SELECT id::text FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2
			AND state = 'SUCCEEDED' AND candidate_sha = $3
		ORDER BY ended_at DESC LIMIT 1`,
		input.OrganizationID, input.TaskID, input.HeadSHA).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrApprovalPrecondition
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("read verified attempt for approval: %w", err)
	}

	correlationID, err := newUUID()
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate approve correlation id: %w", err)
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

	// The approval record binds {task, attempt, action_type, action_digest,
	// policy_version, expires_at} — single-use and stale on any head change.
	actionDigest := sha256.Sum256([]byte("review:" + input.HeadSHA))
	expiresAt := time.Now().Add(reviewApprovalExpiry).UTC()
	var approvalID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO approvals (
			organization_id, project_id, task_id, attempt_id,
			requested_by_employee_id, action_type, action_digest, policy_version,
			status, expires_at, resolved_at, resolved_by_user_id, reason)
		VALUES ($1, $2, $3, $4, $5, 'review_approve', $6, $7,
			'APPROVED', $8, now(), $9, NULLIF($10, ''))
		RETURNING id::text`,
		input.OrganizationID, input.ProjectID, task.ID, attemptID,
		task.AssigneeEmployeeID, hex.EncodeToString(actionDigest[:]),
		ReviewPolicyVersion, expiresAt, input.ActorUserID,
		strings.TrimSpace(input.Reason)).Scan(&approvalID); err != nil {
		return Task{}, false, fmt.Errorf("record approval: %w", err)
	}
	requestedEventID, err := emit(withInput(base, transitionEventInput{
		AttemptID: attemptID, EventType: "approval.requested",
		Data: map[string]any{
			"approvalId": approvalID, "actionType": "review_approve",
			"actionDigest":  hex.EncodeToString(actionDigest[:]),
			"policyVersion": ReviewPolicyVersion,
			"expiresAt":     expiresAt.Format(time.RFC3339),
		},
	}))
	if err != nil {
		return Task{}, false, fmt.Errorf("record approval request: %w", err)
	}
	if _, err := emit(withInput(base, transitionEventInput{
		AttemptID: attemptID, CausationID: requestedEventID, EventType: "approval.resolved",
		Data: map[string]any{
			"approvalId": approvalID, "outcome": "APPROVED",
			"reason": strings.TrimSpace(input.Reason),
		},
	})); err != nil {
		return Task{}, false, fmt.Errorf("record approval resolution: %w", err)
	}

	// An IN_USE workspace still holds the verified worktree; retain it for
	// evidence/reopen before cleanup rather than leaving it writable forever.
	var workspaceID, branchName, workerProfile string
	err = tx.QueryRowContext(ctx, `
		UPDATE workspaces SET state = 'RETAINED', retained_until = now() + $3::interval,
			updated_at = now()
		WHERE organization_id = $1 AND task_id = $2 AND state = 'IN_USE'
		RETURNING id::text, branch_name, worker_profile`,
		input.OrganizationID, task.ID,
		fmt.Sprintf("%d seconds", int64(workspaceRetention.Seconds()))).
		Scan(&workspaceID, &branchName, &workerProfile)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Task{}, false, fmt.Errorf("retain workspace on approval: %w", err)
	default:
		if _, err := emit(withInput(base, transitionEventInput{
			WorkspaceID: workspaceID, EventType: "workspace.state_changed",
			Data: map[string]any{
				"fromState": "IN_USE", "toState": "RETAINED",
				"branchName": branchName, "workerProfile": workerProfile,
			},
		})); err != nil {
			return Task{}, false, fmt.Errorf("record workspace retention: %w", err)
		}
	}

	var taskSequence int64
	if taskSequence, err = nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID); err != nil {
		return Task{}, false, err
	}
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'DONE', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND task_version = $4
		RETURNING task_version, updated_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion).
		Scan(&task.Version, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskVersionConflict
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("update task to DONE: %w", err)
	}
	task.Status = "DONE"

	eventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID,
		AttemptID: attemptID, StreamSequence: taskSequence, Producer: "api",
		Actor: ownerActor(input.ActorUserID), CorrelationID: correlationID,
		EventType: "task.state_changed",
		Data: map[string]any{
			"fromState": "IN_REVIEW", "toState": "DONE",
			"taskVersion": task.Version,
			"reason": controlReason(
				fmt.Sprintf("owner_approved head %s (policy %s)", input.HeadSHA, ReviewPolicyVersion),
				input.Reason),
		},
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("insert task DONE event: %w", err)
	}
	if err := insertOutboxRecord(ctx, tx, eventID, input.ProjectID, taskSequence, envelope); err != nil {
		return Task{}, false, err
	}
	if err := insertAuditRecord(ctx, tx, input.OrganizationID, input.ProjectID,
		input.ActorUserID, "task.approve", task.ID); err != nil {
		return Task{}, false, err
	}
	if err := storeTransitionResult(ctx, tx, input.OrganizationID, input.ActorUserID,
		approveTaskOperation, input.IdempotencyKey, requestHash, task); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit task approval: %w", err)
	}
	return task, false, nil
}
