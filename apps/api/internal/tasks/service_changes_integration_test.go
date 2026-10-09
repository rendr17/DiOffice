package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func changesInput(organizationID, projectID, taskID, ownerID, key, reason string, version int64) TaskChangesInput {
	return TaskChangesInput{
		TaskControlInput: TaskControlInput{
			OrganizationID: organizationID, ProjectID: projectID, TaskID: taskID,
			ActorUserID: ownerID, IdempotencyKey: key, ExpectedVersion: version,
		},
		Reason: reason,
	}
}

func TestRequestChangesPreservesWorkspaceAndQueuesContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := strings.Repeat("cd", 20)
	task, firstAttempt := driveTaskToReview(t, ctx, db, service,
		organizationID, projectID, ownerID, employeeID, "chg", candidateSHA)

	version := taskVersion(t, ctx, db, task.ID)
	updated, replayed, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chg-001",
		"Please add input validation and update the tests.", version))
	if err != nil {
		t.Fatalf("RequestChanges() error = %v", err)
	}
	if replayed || updated.Status != "IN_PROGRESS" || updated.Version != version+1 {
		t.Fatalf("RequestChanges() = %+v replay %t; want IN_PROGRESS next version", updated, replayed)
	}

	// A new PROVISIONING attempt carrying the feedback exists on the preserved
	// (back to READY) workspace; the first attempt and PR evidence are intact.
	var attemptState, workspaceState, storedReason, runtimeType string
	var attemptNumber int64
	if err := db.QueryRowContext(ctx, `
		SELECT a.state, a.attempt_number, a.change_request, a.runtime_type, w.state
		FROM execution_attempts a JOIN workspaces w
			ON w.organization_id = a.organization_id AND w.task_id = a.task_id
		WHERE a.organization_id = $1 AND a.task_id = $2 AND a.attempt_number = 2`,
		organizationID, task.ID).Scan(
		&attemptState, &attemptNumber, &storedReason, &runtimeType, &workspaceState); err != nil {
		t.Fatalf("read continuation attempt: %v", err)
	}
	if attemptState != "PROVISIONING" || storedReason != "Please add input validation and update the tests." ||
		runtimeType != "opencode" || workspaceState != "READY" {
		t.Fatalf("continuation = %s #%d req %q rt %s ws %s",
			attemptState, attemptNumber, storedReason, runtimeType, workspaceState)
	}
	var priorState string
	if err := db.QueryRowContext(ctx,
		`SELECT state FROM execution_attempts WHERE id = $1`, firstAttempt).Scan(&priorState); err != nil {
		t.Fatalf("read prior attempt: %v", err)
	}
	if priorState != "SUCCEEDED" {
		t.Fatalf("prior attempt = %s, want SUCCEEDED history", priorState)
	}
	var prHead string
	if err := db.QueryRowContext(ctx,
		`SELECT head_sha FROM pull_requests WHERE task_id = $1`, task.ID).Scan(&prHead); err != nil {
		t.Fatalf("read pull request: %v", err)
	}
	if prHead != candidateSHA {
		t.Fatalf("pull request head = %s, want preserved %s", prHead, candidateSHA)
	}

	// The three durable facts share one correlation id.
	var correlations, events int
	if err := db.QueryRowContext(ctx, `
		SELECT count(DISTINCT correlation_id), count(*) FROM agent_events
		WHERE task_id = $1 AND (
			(event_type = 'execution_attempt.state_changed' AND data->>'toState' = 'PROVISIONING')
			OR (event_type = 'workspace.state_changed' AND data->>'toState' = 'READY')
			OR (event_type = 'task.state_changed' AND data->>'toState' = 'IN_PROGRESS'
				AND data->>'fromState' = 'IN_REVIEW'))`,
		task.ID).Scan(&correlations, &events); err != nil {
		t.Fatalf("read request-changes events: %v", err)
	}
	if correlations != 1 || events != 3 {
		t.Fatalf("request-changes facts = %d rows across %d correlations; want 3/1", events, correlations)
	}

	// Idempotent replay returns the stored result without a second attempt.
	replayTask, replayed, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chg-001",
		"Please add input validation and update the tests.", version))
	if err != nil || !replayed || replayTask.Status != "IN_PROGRESS" {
		t.Fatalf("request-changes replay = %+v, %t, %v", replayTask, replayed, err)
	}
	var attempts int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM execution_attempts WHERE task_id = $1`, task.ID).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d after replay, want 2", attempts)
	}
}

func TestRequestChangesRejectsInvalidInputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	candidateSHA := strings.Repeat("cd", 20)
	task, _ := driveTaskToReview(t, ctx, db, service,
		organizationID, projectID, ownerID, employeeID, "chgbad", candidateSHA)
	version := taskVersion(t, ctx, db, task.ID)

	if _, _, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chgbad-empty", "   ", version,
	)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RequestChanges(empty reason) = %v, want ErrInvalidInput", err)
	}
	if _, _, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chgbad-long",
		strings.Repeat("x", changeRequestMaxLength+1), version,
	)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RequestChanges(long reason) = %v, want ErrInvalidInput", err)
	}
	if _, _, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chgbad-ver", "fix", version-1,
	)); !errors.Is(err, ErrTaskVersionConflict) {
		t.Fatalf("RequestChanges(stale version) = %v, want ErrTaskVersionConflict", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET status = 'IN_PROGRESS' WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("set task in progress: %v", err)
	}
	if _, _, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chgbad-state", "fix", version,
	)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("RequestChanges(IN_PROGRESS) = %v, want ErrInvalidTransition", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET status = 'IN_REVIEW' WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("restore task review: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE workspaces SET state = 'CLEANED', cleaned_at = now() WHERE task_id = $1`, task.ID); err != nil {
		t.Fatalf("clean workspace: %v", err)
	}
	if _, _, err := service.RequestChanges(ctx, changesInput(
		organizationID, projectID, task.ID, ownerID, "chgbad-ws", "fix", version,
	)); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("RequestChanges(cleaned workspace) = %v, want ErrWorkspaceUnavailable", err)
	}
}
