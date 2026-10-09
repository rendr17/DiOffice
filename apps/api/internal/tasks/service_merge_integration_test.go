package tasks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

// mergeFakeGitHub satisfies GitMergeClient for merge tests.
type mergeFakeGitHub struct {
	merged   *MergedPullRequest
	mergeErr error
	got      *MergedPullRequest
	getErr   error
	merges   []string
}

func (f *mergeFakeGitHub) GetPR(context.Context, string, string, int) (*MergedPullRequest, error) {
	return f.got, f.getErr
}

func (f *mergeFakeGitHub) MergePR(_ context.Context, owner, repo string, _ int, headSHA string) (*MergedPullRequest, error) {
	f.merges = append(f.merges, headSHA)
	if owner != "octo" || repo != "demo" {
		return nil, errors.New("merge targeted wrong repository")
	}
	return f.merged, f.mergeErr
}

func mergeInput(organizationID, projectID, taskID, ownerID, key string, version int64, reason string) TaskControlInput {
	return TaskControlInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: taskID,
		ActorUserID: ownerID, IdempotencyKey: key, ExpectedVersion: version, Reason: reason,
	}
}

// driveTaskToDone drives a task through review approval so it is DONE with a
// recorded pull request and a review approval bound to the candidate SHA.
func driveTaskToDone(t *testing.T, ctx context.Context, db *sql.DB, service *Service,
	organizationID, projectID, ownerID, employeeID, keyPrefix, candidateSHA string) Task {
	t.Helper()
	task, _ := driveTaskToReview(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, keyPrefix, candidateSHA)
	done, _, err := service.ApproveTask(ctx, approveInput(organizationID, projectID, task.ID, ownerID,
		keyPrefix+"-approve", candidateSHA, task.Version))
	if err != nil {
		t.Fatalf("ApproveTask() error = %v", err)
	}
	return done
}

func TestMergeTaskVerifiesApprovalAndMerges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := "aaaabbbbccccddddeeeeffff0000111122223333"

	// Not DONE — merge is rejected without any GitHub call.
	notDone, _ := driveTaskToReview(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, "mr-not-done", candidateSHA)
	gh := &mergeFakeGitHub{merged: &MergedPullRequest{State: "MERGED"}}
	if _, _, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, notDone.ID, ownerID, "mr-n1", notDone.Version, ""), gh); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("MergeTask(IN_REVIEW) = %v, want ErrInvalidTransition", err)
	}
	if len(gh.merges) != 0 {
		t.Fatalf("GitHub was called for a non-DONE task")
	}

	task := driveTaskToDone(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, "mr", candidateSHA)
	mergedAt := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	gh = &mergeFakeGitHub{merged: &MergedPullRequest{
		HeadSHA: candidateSHA, State: "MERGED", MergedAt: &mergedAt,
	}}
	done, replayed, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, task.ID, ownerID, "mr-1", task.Version, "ship it"), gh)
	if err != nil {
		t.Fatalf("MergeTask() error = %v", err)
	}
	if replayed || done.Status != "DONE" {
		t.Fatalf("MergeTask() = %+v replayed=%v, want DONE non-replayed", done, replayed)
	}
	if len(gh.merges) != 1 || gh.merges[0] != candidateSHA {
		t.Fatalf("MergePR calls = %+v, want one call with head=%s", gh.merges, candidateSHA)
	}
	var prState string
	var prMergedAt *time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT p.state, p.merged_at FROM pull_requests p
		JOIN tasks t ON t.pull_request_id = p.id
		WHERE t.organization_id = $1 AND t.id = $2`,
		organizationID, task.ID).Scan(&prState, &prMergedAt); err != nil {
		t.Fatalf("read pull request: %v", err)
	}
	if prState != "MERGED" || prMergedAt == nil {
		t.Fatalf("pull request = %s merged_at=%v, want MERGED with timestamp", prState, prMergedAt)
	}
	// A merge approval row bound to merge:<head> and the three durable facts.
	var mergeDigest string
	digest := sha256.Sum256([]byte("merge:" + candidateSHA))
	mergeDigest = hex.EncodeToString(digest[:])
	var grantCount, eventCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM approvals
		WHERE organization_id = $1 AND task_id = $2 AND action_type = 'merge' AND action_digest = $3`,
		organizationID, task.ID, mergeDigest).Scan(&grantCount); err != nil {
		t.Fatalf("count merge approvals: %v", err)
	}
	if grantCount != 1 {
		t.Fatalf("merge approvals = %d, want 1", grantCount)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_events
		WHERE organization_id = $1 AND project_id = $2
			AND ((event_type = 'pull_request.updated' AND data->>'change' = 'state' AND data->>'state' = 'MERGED')
				OR (event_type = 'approval.requested' AND data->>'actionType' = 'merge')
				OR (event_type = 'approval.resolved' AND data->>'reason' LIKE 'merged via%'))`,
		organizationID, projectID).Scan(&eventCount); err != nil {
		t.Fatalf("count merge events: %v", err)
	}
	if eventCount != 3 {
		t.Fatalf("merge facts = %d, want pull_request.updated + approval.requested + approval.resolved", eventCount)
	}

	// Idempotent replay — same key, no second GitHub call, no new facts.
	replay, replayed, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, task.ID, ownerID, "mr-1", task.Version, "ship it"), gh)
	if err != nil || !replayed || replay.ID != task.ID {
		t.Fatalf("MergeTask() replay = %+v replayed=%v err=%v", replay, replayed, err)
	}
	if len(gh.merges) != 1 {
		t.Fatalf("replay hit GitHub again: %d merges", len(gh.merges))
	}
	// Second merge on the already-MERGED PR is an idempotent no-op success.
	again, replayed, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, task.ID, ownerID, "mr-2", task.Version, ""), gh)
	if err != nil || replayed || again.Status != "DONE" {
		t.Fatalf("MergeTask(already merged) = %+v replayed=%v err=%v", again, replayed, err)
	}
	if len(gh.merges) != 1 {
		t.Fatalf("already-merged merge hit GitHub: %d merges", len(gh.merges))
	}
}

func TestMergeTaskRejectsStaleApprovalAndFailedMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := "bbbbccccddddeeeeffff0000111122223333aaaa"

	// DONE, then the recorded head moved (reconciler wrote a new SHA) — the
	// review approval bound to the old head no longer applies.
	task := driveTaskToDone(t, ctx, db, service, organizationID, projectID, ownerID, employeeID, "ms-stale", candidateSHA)
	newHead := "ccccddddeeeeffff0000111122223333aaaabbbb"
	if _, err := db.ExecContext(ctx, `
		UPDATE pull_requests SET head_sha = $2 WHERE organization_id = $1 AND task_id = $3`,
		organizationID, newHead, task.ID); err != nil {
		t.Fatalf("move pull request head: %v", err)
	}
	gh := &mergeFakeGitHub{merged: &MergedPullRequest{State: "MERGED"}}
	if _, _, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, task.ID, ownerID, "ms-1", task.Version, ""), gh); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("MergeTask(stale head) = %v, want ErrStaleApproval", err)
	}
	if len(gh.merges) != 0 {
		t.Fatalf("GitHub was called without a current approval")
	}

	// Fresh approval bound to the new head — the row shape ApproveTask
	// writes, recorded directly since the task is already DONE.
	digest := sha256.Sum256([]byte("review:" + newHead))
	if _, err := db.ExecContext(ctx, `
		INSERT INTO approvals (
			organization_id, project_id, task_id, requested_by_employee_id,
			action_type, action_digest, policy_version, status, expires_at,
			resolved_at, resolved_by_user_id)
		VALUES ($1, $2, $3, $4, 'review_approve', $5, $6, 'APPROVED',
			now() + interval '1 hour', now(), $7)`,
		organizationID, projectID, task.ID, employeeID,
		hex.EncodeToString(digest[:]), ReviewPolicyVersion, ownerID); err != nil {
		t.Fatalf("insert fresh approval: %v", err)
	}
	gh.mergeErr = fmt.Errorf("%w: not mergeable", MergeRefusal)
	gh.merged = nil
	if _, _, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, task.ID, ownerID, "ms-2", task.Version, ""), gh); !errors.Is(err, ErrMergeRejected) {
		t.Fatalf("MergeTask(github 405) = %v, want ErrMergeRejected", err)
	}
	if len(gh.merges) != 1 {
		t.Fatalf("MergePR not attempted with fresh approval: %d merges", len(gh.merges))
	}
	// Nothing persisted — the pull request is still OPEN.
	var prState string
	if err := db.QueryRowContext(ctx, `
		SELECT state FROM pull_requests WHERE organization_id = $1 AND task_id = $2`,
		organizationID, task.ID).Scan(&prState); err != nil {
		t.Fatalf("read pull request state: %v", err)
	}
	if prState != "OPEN" {
		t.Fatalf("pull request = %s after rejected merge, want OPEN", prState)
	}

	// GitHub rejected the merge call but the merge actually landed (e.g. the
	// service's own commit crashed after GitHub succeeded) — re-read heals it.
	landed := time.Now().UTC()
	gh.got = &MergedPullRequest{HeadSHA: newHead, State: "MERGED", MergedAt: &landed}
	if _, _, err := service.MergeTask(ctx,
		mergeInput(organizationID, projectID, task.ID, ownerID, "ms-3", task.Version, ""), gh); err != nil {
		t.Fatalf("MergeTask(landed despite refusal) error = %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT state FROM pull_requests WHERE organization_id = $1 AND task_id = $2`,
		organizationID, task.ID).Scan(&prState); err != nil {
		t.Fatalf("read healed pull request: %v", err)
	}
	if prState != "MERGED" {
		t.Fatalf("pull request = %s after landed merge, want MERGED", prState)
	}
}
