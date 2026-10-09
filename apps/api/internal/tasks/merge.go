package tasks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
)

const mergeTaskOperation = "tasks.merge"

// MergePolicyVersion identifies the merge precondition policy: a recorded
// non-stale review approval for the exact pull-request head being merged.
const MergePolicyVersion = "merge-sha.v1"

const mergeApprovalExpiry = 15 * time.Minute

var (
	// ErrStaleApproval rejects a merge when no APPROVED approval exists whose
	// action_digest binds the pull request's current recorded head SHA —
	// the canonical "current, non-stale approval" precondition.
	ErrStaleApproval = errors.New("no current approval binds the recorded pull request head")
	// ErrMergeRejected means GitHub refused the merge and the pull request is
	// not already merged — head moved, not mergeable, or branch protection.
	// Nothing was persisted.
	ErrMergeRejected = errors.New("github rejected the merge")
	// ErrMergeUnavailable means the merge could not reach GitHub reliably
	// (auth, missing repo, transient transport). Nothing was persisted.
	ErrMergeUnavailable = errors.New("github merge is unavailable")
)

// MergedPullRequest is the narrow slice of provider state MergeTask needs.
type MergedPullRequest struct {
	State    string
	HeadSHA  string
	MergedAt *time.Time
}

// GitMergeClient is the minimal provider surface for merging — defined here
// so the tasks package does not depend on the runner package (import
// cycles). cmd/api adapts the real GitHub client to it.
type GitMergeClient interface {
	// MergePR merges the pull request with the provider's own sha
	// precondition. Rejection reasons are signalled with MergeRefusal so
	// the service can distinguish a refused merge from an unavailable one.
	MergePR(ctx context.Context, owner, repo string, number int, headSHA string) (*MergedPullRequest, error)
	// GetPR re-reads authoritative pull-request state.
	GetPR(ctx context.Context, owner, repo string, number int) (*MergedPullRequest, error)
}

// MergeRefusal marks a provider rejection of the merge itself (head
// precondition failed, not mergeable, protection rules) — as opposed to
// transport/auth failures.
var MergeRefusal = errors.New("merge refused by provider")

// MergeTask performs the distinct Owner merge action. Everything — the
// durable preconditions (task DONE, recorded open PR, current non-stale
// approval bound to the recorded head), the GitHub merge call, and the
// outcome writes — lives in one transaction so the idempotency claim can
// never outlive its result. The GitHub API's own sha precondition keeps the
// merge atomic with the recorded head: a head change between review and
// merge is rejected by GitHub, never silently merged. When GitHub reports a
// refusal the service re-reads the PR — a merge that already landed (e.g.
// the commit after a crashed persist) is persisted instead of rejected, and
// the drift reconciler converges any remaining divergence. The task stays
// DONE — merge is recorded on the pull request, not the task.
func (s *Service) MergeTask(ctx context.Context, input TaskControlInput, gh GitMergeClient) (Task, bool, error) {
	if err := input.validate(); err != nil {
		return Task{}, false, err
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}
	if gh == nil {
		return Task{}, false, errors.New("github client is not configured")
	}
	requestHash, err := hashTransitionRequest(mergeTaskOperation,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, input.Reason)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash merge request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin merge transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimTransitionIdempotency(ctx, tx, input.OrganizationID,
		input.ActorUserID, mergeTaskOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent merge replay: %w", err)
		}
		return task, true, nil
	}
	task, err = loadTaskForTransition(ctx, tx, input.OrganizationID, input.ProjectID, input.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for merge: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	if task.Status != "DONE" {
		return Task{}, false, ErrInvalidTransition
	}

	var prID, headSHA, prState, owner, repo string
	var number int
	err = tx.QueryRowContext(ctx, `
		SELECT p.id::text, p.head_sha, p.state, p.number, r.owner, r.repo_name
		FROM pull_requests p
		JOIN tasks t ON t.pull_request_id = p.id
		JOIN repositories r
			ON r.organization_id = p.organization_id
			AND r.project_id = p.project_id AND r.id = p.repository_id
		WHERE t.organization_id = $1 AND t.project_id = $2 AND t.id = $3
		FOR UPDATE OF p, t`,
		input.OrganizationID, input.ProjectID, input.TaskID).Scan(
		&prID, &headSHA, &prState, &number, &owner, &repo)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrApprovalPrecondition
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("read pull request for merge: %w", err)
	}

	var mergedAt *time.Time
	var boundApprovalID, boundAttemptID string
	switch {
	case prState == "MERGED":
		// Already merged — record the idempotent result, no GitHub call.
	case prState == "OPEN" || prState == "DRAFT":
		// Current, non-stale approval: an APPROVED approval whose digest
		// binds exactly the recorded head.
		reviewDigest := sha256.Sum256([]byte("review:" + headSHA))
		err = tx.QueryRowContext(ctx, `
			SELECT id::text, COALESCE(attempt_id::text, '') FROM approvals
			WHERE organization_id = $1 AND task_id = $2
				AND action_type = 'review_approve' AND action_digest = $3
				AND status = 'APPROVED'
			ORDER BY resolved_at DESC LIMIT 1`,
			input.OrganizationID, input.TaskID, hex.EncodeToString(reviewDigest[:])).
			Scan(&boundApprovalID, &boundAttemptID)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false, ErrStaleApproval
		}
		if err != nil {
			return Task{}, false, fmt.Errorf("read review approval for merge: %w", err)
		}
		merged, mergeErr := gh.MergePR(ctx, owner, repo, number, headSHA)
		switch {
		case mergeErr == nil && merged != nil && merged.State == "MERGED":
			mergedAt = merged.MergedAt
		case mergeErr != nil && errors.Is(mergeErr, MergeRefusal):
			// Refused — but the merge may have landed before our own
			// crash/rollback. Re-read before declaring rejection.
			fresh, getErr := gh.GetPR(ctx, owner, repo, number)
			if getErr != nil || fresh == nil || fresh.State != "MERGED" || fresh.HeadSHA != headSHA {
				return Task{}, false, ErrMergeRejected
			}
			mergedAt = fresh.MergedAt
		case mergeErr != nil:
			return Task{}, false, ErrMergeUnavailable
		default:
			return Task{}, false, ErrMergeRejected
		}
	default:
		return Task{}, false, ErrMergeRejected
	}

	if prState != "MERGED" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE pull_requests
			SET state = 'MERGED', merged_at = COALESCE($3, now()), updated_at = now()
			WHERE organization_id = $1 AND id = $2`,
			input.OrganizationID, prID, mergedAt); err != nil {
			return Task{}, false, fmt.Errorf("mark pull request merged: %w", err)
		}
	}

	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate merge correlation id: %w", err)
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

	// The merge grant is recorded as its own single-use approval bound to
	// the merged head — distinct from the review approval it consumed.
	mergeDigest := sha256.Sum256([]byte("merge:" + headSHA))
	expiresAt := time.Now().Add(mergeApprovalExpiry).UTC()
	var mergeApprovalID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO approvals (
			organization_id, project_id, task_id, attempt_id,
			requested_by_employee_id, action_type, action_digest, policy_version,
			status, expires_at, resolved_at, resolved_by_user_id, reason)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, 'merge', $6, $7,
			'APPROVED', $8, now(), $9, NULLIF($10, ''))
		RETURNING id::text`,
		input.OrganizationID, input.ProjectID, input.TaskID, boundAttemptID,
		task.AssigneeEmployeeID, hex.EncodeToString(mergeDigest[:]),
		MergePolicyVersion, expiresAt, input.ActorUserID, input.Reason).Scan(&mergeApprovalID)
	if err != nil {
		return Task{}, false, fmt.Errorf("record merge approval: %w", err)
	}
	if prState != "MERGED" {
		if _, err := emit(withInput(base, transitionEventInput{
			EventType: "pull_request.updated",
			Data: map[string]any{
				"number": number, "change": "state",
				"headSha": headSHA, "state": "MERGED",
			},
		})); err != nil {
			return Task{}, false, fmt.Errorf("record pull request merge: %w", err)
		}
	}
	requestedEventID, err := emit(withInput(base, transitionEventInput{
		EventType: "approval.requested",
		Data: map[string]any{
			"approvalId": mergeApprovalID, "actionType": "merge",
			"actionDigest":  hex.EncodeToString(mergeDigest[:]),
			"policyVersion": MergePolicyVersion,
			"expiresAt":     expiresAt.Format(time.RFC3339),
		},
	}))
	if err != nil {
		return Task{}, false, fmt.Errorf("record merge approval request: %w", err)
	}
	if _, err := emit(withInput(base, transitionEventInput{
		CausationID: requestedEventID, EventType: "approval.resolved",
		Data: map[string]any{
			"approvalId": mergeApprovalID, "outcome": "APPROVED",
			"reason": "merged via owner command (bound to review approval " + boundApprovalID + ")",
		},
	})); err != nil {
		return Task{}, false, fmt.Errorf("record merge approval resolution: %w", err)
	}
	if err := insertAuditRecord(ctx, tx, input.OrganizationID, input.ProjectID,
		input.ActorUserID, "pull_request.merge", prID); err != nil {
		return Task{}, false, err
	}
	if err := storeTransitionResult(ctx, tx, input.OrganizationID, input.ActorUserID,
		mergeTaskOperation, input.IdempotencyKey, requestHash, task); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit merge outcome: %w", err)
	}
	return task, false, nil
}
