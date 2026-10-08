package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSaveToBacklogPersistsStateEventOutboxAuditAndIdempotentResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	createInput := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "backlog-create")
	createInput.ReferenceImages = []ReferenceImage{validReferenceImage()}
	draft, replayed, err := service.CreateDraft(ctx, createInput)
	if err != nil || replayed {
		t.Fatalf("CreateDraft() = task %+v, replay %t, error %v; want a new DRAFT", draft, replayed, err)
	}

	input := SaveToBacklogInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "save-to-backlog-001", ExpectedVersion: draft.Version,
	}
	queued, replayed, err := service.SaveToBacklog(ctx, input)
	if err != nil {
		t.Fatalf("SaveToBacklog() error = %v", err)
	}
	if replayed || queued.Status != "BACKLOG" || queued.Version != draft.Version+1 || len(queued.ReferenceImages) != 1 {
		t.Fatalf("SaveToBacklog() = task %+v, replay %t; want BACKLOG, incremented version, retained image metadata", queued, replayed)
	}

	var eventType, eventData string
	var sequence int64
	if err := db.QueryRowContext(ctx, `
		SELECT event_type, stream_sequence, data::text
		FROM agent_events WHERE task_id = $1 ORDER BY stream_sequence DESC LIMIT 1`, draft.ID).
		Scan(&eventType, &sequence, &eventData); err != nil {
		t.Fatalf("read state-change event: %v", err)
	}
	var data struct {
		FromState   string `json:"fromState"`
		ToState     string `json:"toState"`
		TaskVersion int64  `json:"taskVersion"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(eventData), &data); err != nil {
		t.Fatalf("decode state-change event data: %v", err)
	}
	if eventType != "task.state_changed" || sequence != 2 || data.FromState != "DRAFT" ||
		data.ToState != "BACKLOG" || data.TaskVersion != queued.Version || data.Reason != "owner_saved_to_backlog" {
		t.Fatalf("state-change event = %q/%d/%+v; want task.state_changed sequence 2 and DRAFT -> BACKLOG at version %d", eventType, sequence, data, queued.Version)
	}

	replayedTask, replayed, err := service.SaveToBacklog(ctx, input)
	if err != nil || !replayed || replayedTask.ID != queued.ID || replayedTask.Version != queued.Version || len(replayedTask.ReferenceImages) != 1 {
		t.Fatalf("idempotent SaveToBacklog() = task %+v, replay %t, error %v; want original persisted result", replayedTask, replayed, err)
	}
	for table, want := range map[string]int{"tasks": 1, "agent_events": 2, "event_outbox": 2, "audit_records": 2} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		if count != want {
			t.Fatalf("%s rows = %d, want %d after idempotent replay", table, count, want)
		}
	}
}

func TestSaveToBacklogRejectsStaleVersionInvalidTransitionAndChangedReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	draft, _, err := service.CreateDraft(ctx, testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "backlog-create-guards"))
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	input := SaveToBacklogInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "save-to-backlog-guards", ExpectedVersion: draft.Version + 1,
	}
	if _, _, err := service.SaveToBacklog(ctx, input); !errors.Is(err, ErrTaskVersionConflict) {
		t.Fatalf("SaveToBacklog() stale version error = %v, want ErrTaskVersionConflict", err)
	}
	var sequence int64
	if err := db.QueryRowContext(ctx, `SELECT last_event_sequence FROM projects WHERE id = $1`, projectID).Scan(&sequence); err != nil {
		t.Fatalf("read project sequence after stale command: %v", err)
	}
	if sequence != 1 {
		t.Fatalf("project sequence after stale command = %d, want 1", sequence)
	}

	input.ExpectedVersion = draft.Version
	queued, _, err := service.SaveToBacklog(ctx, input)
	if err != nil {
		t.Fatalf("SaveToBacklog() valid transition error = %v", err)
	}
	changedReplay := input
	changedReplay.ExpectedVersion = queued.Version
	if _, _, err := service.SaveToBacklog(ctx, changedReplay); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("SaveToBacklog() changed idempotent replay error = %v, want ErrIdempotencyConflict", err)
	}

	illegalTransition := input
	illegalTransition.IdempotencyKey = "save-to-backlog-illegal-transition"
	illegalTransition.ExpectedVersion = queued.Version
	if _, _, err := service.SaveToBacklog(ctx, illegalTransition); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("SaveToBacklog() from BACKLOG error = %v, want ErrInvalidTransition", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT last_event_sequence FROM projects WHERE id = $1`, projectID).Scan(&sequence); err != nil {
		t.Fatalf("read project sequence after rejected transition: %v", err)
	}
	if sequence != 2 {
		t.Fatalf("project sequence after rejected transition = %d, want 2", sequence)
	}
}
