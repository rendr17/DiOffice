package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func insertTestRepository(t *testing.T, ctx context.Context, db *sql.DB, organizationID, projectID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch)
		VALUES ($1, $2, 'github', 'octo', 'demo', 'main')`, organizationID, projectID); err != nil {
		t.Fatalf("insert repository fixture: %v", err)
	}
}

func TestMarkReadyRequiresConfirmedDetailsAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	draft, _, err := service.CreateDraft(ctx, testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "ready-create"))
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}

	input := MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "ready-001", ExpectedVersion: draft.Version,
	}
	_, _, err = service.MarkReady(ctx, input)
	var incomplete *IncompleteTaskError
	if !errors.As(err, &incomplete) || !errors.Is(err, ErrTaskIncomplete) {
		t.Fatalf("MarkReady() error = %v, want IncompleteTaskError", err)
	}
	if strings.Join(incomplete.Missing, ",") != "manifestDigest,repository" {
		t.Fatalf("MarkReady() missing = %v, want manifestDigest+repository", incomplete.Missing)
	}

	insertTestRepository(t, ctx, db, organizationID, projectID)
	input.ManifestDigest = strings.Repeat("9f", 32)
	ready, replayed, err := service.MarkReady(ctx, input)
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	if replayed || ready.Status != "READY" || ready.Version != draft.Version+1 || ready.ManifestDigest != input.ManifestDigest {
		t.Fatalf("MarkReady() = task %+v, replay %t; want READY with stored manifest digest", ready, replayed)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT event_type, data::text FROM agent_events
		WHERE task_id = $1 ORDER BY stream_sequence`, draft.ID)
	if err != nil {
		t.Fatalf("read task events: %v", err)
	}
	defer rows.Close()
	types := []string{}
	payloads := []string{}
	for rows.Next() {
		var eventType, data string
		if err := rows.Scan(&eventType, &data); err != nil {
			t.Fatalf("scan task event: %v", err)
		}
		types = append(types, eventType)
		payloads = append(payloads, data)
	}
	if len(types) != 3 || types[0] != "task.created" || types[1] != "task.updated" || types[2] != "task.state_changed" {
		t.Fatalf("task event types = %v, want created+updated+state_changed", types)
	}
	var stateData struct {
		FromState string `json:"fromState"`
		ToState   string `json:"toState"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(payloads[2]), &stateData); err != nil {
		t.Fatalf("decode state-change data: %v", err)
	}
	if stateData.FromState != "DRAFT" || stateData.ToState != "READY" || stateData.Reason != "owner_marked_ready" {
		t.Fatalf("state-change data = %+v, want DRAFT -> READY owner_marked_ready", stateData)
	}

	replayedTask, replayed, err := service.MarkReady(ctx, input)
	if err != nil || !replayed || replayedTask.ID != ready.ID || replayedTask.Version != ready.Version {
		t.Fatalf("idempotent MarkReady() = task %+v, replay %t, error %v; want persisted result", replayedTask, replayed, err)
	}
	var eventCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM agent_events WHERE task_id = $1`, draft.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count task events: %v", err)
	}
	if eventCount != 3 {
		t.Fatalf("agent_events rows = %d, want 3 after idempotent replay", eventCount)
	}
}

func TestMarkReadyRejectsStaleVersionEmptyCriteriaAndActiveStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)

	emptyCriteria := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "ready-empty-criteria")
	emptyCriteria.AcceptanceCriteria = []byte(`[]`)
	emptyCriteria.ManifestDigest = strings.Repeat("ab", 32)
	draft, _, err := service.CreateDraft(ctx, emptyCriteria)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	_, _, err = service.MarkReady(ctx, MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "ready-criteria", ExpectedVersion: draft.Version,
	})
	var incomplete *IncompleteTaskError
	if !errors.As(err, &incomplete) || len(incomplete.Missing) != 1 || incomplete.Missing[0] != "acceptanceCriteria" {
		t.Fatalf("MarkReady() error = %v, want missing acceptanceCriteria only", err)
	}

	complete := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "ready-stale")
	complete.ManifestDigest = strings.Repeat("cd", 32)
	draft, _, err = service.CreateDraft(ctx, complete)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	_, _, err = service.MarkReady(ctx, MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "ready-stale-key", ExpectedVersion: draft.Version + 1,
	})
	if !errors.Is(err, ErrTaskVersionConflict) {
		t.Fatalf("MarkReady() stale version error = %v, want ErrTaskVersionConflict", err)
	}
}

func TestStartExecutionCommitsAttemptWorkspaceAndEventsIdempotently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)
	createInput := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "start-create")
	createInput.ManifestDigest = strings.Repeat("ef", 32)
	draft, _, err := service.CreateDraft(ctx, createInput)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	ready, _, err := service.MarkReady(ctx, MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-ready", ExpectedVersion: draft.Version,
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}

	input := StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-001", ExpectedVersion: ready.Version,
	}
	started, replayed, err := service.StartExecution(ctx, input)
	if err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}
	if replayed || started.Status != "PROVISIONING" || started.Version != ready.Version+1 {
		t.Fatalf("StartExecution() = task %+v, replay %t; want PROVISIONING at next version", started, replayed)
	}

	var workspaceState, branchName string
	if err := db.QueryRowContext(ctx, `
		SELECT state, branch_name FROM workspaces WHERE organization_id = $1 AND task_id = $2`,
		organizationID, ready.ID).Scan(&workspaceState, &branchName); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if workspaceState != "PROVISIONING" || branchName != "task/"+ready.ID {
		t.Fatalf("workspace = %s on %s, want PROVISIONING task/<id>", workspaceState, branchName)
	}

	var attemptState, runtimeType string
	var attemptNumber int64
	if err := db.QueryRowContext(ctx, `
		SELECT state, runtime_type, attempt_number FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2`,
		organizationID, ready.ID).Scan(&attemptState, &runtimeType, &attemptNumber); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attemptState != "CREATED" || runtimeType != "opencode" || attemptNumber != 1 {
		t.Fatalf("attempt = %s/%s #%d, want CREATED opencode #1", attemptState, runtimeType, attemptNumber)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT event_type, data::text, attempt_id::text, workspace_id::text
		FROM agent_events WHERE task_id = $1 ORDER BY stream_sequence`, ready.ID)
	if err != nil {
		t.Fatalf("read task events: %v", err)
	}
	defer rows.Close()
	types := []string{}
	for rows.Next() {
		var eventType, data string
		var attemptID, workspaceID *string
		if err := rows.Scan(&eventType, &data, &attemptID, &workspaceID); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		types = append(types, eventType)
		if strings.HasPrefix(eventType, "execution_attempt") || strings.HasPrefix(eventType, "workspace") {
			if attemptID == nil || workspaceID == nil {
				t.Fatalf("%s event is missing attempt/workspace ids", eventType)
			}
		}
	}
	wantTypes := []string{"task.created", "task.state_changed", "execution_attempt.state_changed", "workspace.state_changed", "task.state_changed"}
	if strings.Join(types, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("event order = %v, want %v", types, wantTypes)
	}
	var outboxCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM event_outbox`).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if outboxCount != 5 {
		t.Fatalf("outbox rows = %d, want one per committed event", outboxCount)
	}

	replayedTask, replayed, err := service.StartExecution(ctx, input)
	if err != nil || !replayed || replayedTask.Version != started.Version || replayedTask.Status != "PROVISIONING" {
		t.Fatalf("idempotent StartExecution() = task %+v, replay %t, error %v; want persisted result", replayedTask, replayed, err)
	}
	var attemptCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM execution_attempts`).Scan(&attemptCount); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Fatalf("execution_attempts rows = %d, want 1 after idempotent replay", attemptCount)
	}
}

func TestStartExecutionRejectsNonReadyAndConcurrentActiveAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	insertTestRepository(t, ctx, db, organizationID, projectID)
	service := NewService(db)

	createInput := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "start-draft")
	createInput.ManifestDigest = strings.Repeat("01", 32)
	draft, _, err := service.CreateDraft(ctx, createInput)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	_, _, err = service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-non-ready", ExpectedVersion: draft.Version,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("StartExecution() on DRAFT error = %v, want ErrInvalidTransition", err)
	}

	ready, _, err := service.MarkReady(ctx, MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-ready-1", ExpectedVersion: draft.Version,
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	started, _, err := service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-a", ExpectedVersion: ready.Version,
	})
	if err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}

	second := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "start-second")
	second.ManifestDigest = strings.Repeat("02", 32)
	draftB, _, err := service.CreateDraft(ctx, second)
	if err != nil {
		t.Fatalf("CreateDraft() second error = %v", err)
	}
	readyB, _, err := service.MarkReady(ctx, MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draftB.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-ready-2", ExpectedVersion: draftB.Version,
	})
	if err != nil {
		t.Fatalf("MarkReady() second error = %v", err)
	}
	_, _, err = service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: readyB.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-b", ExpectedVersion: readyB.Version,
	})
	if !errors.Is(err, ErrActiveAttemptExists) {
		t.Fatalf("StartExecution() while employee busy error = %v, want ErrActiveAttemptExists", err)
	}

	_, _, err = service.StartExecution(ctx, StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: started.ID,
		ActorUserID: ownerID, IdempotencyKey: "start-again", ExpectedVersion: started.Version,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("StartExecution() on PROVISIONING error = %v, want ErrInvalidTransition", err)
	}
}
