package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
)

func TestCreateDraftRejectsAcceptanceCriteriaOutsideEventContract(t *testing.T) {
	input := CreateDraftInput{
		OrganizationID:     "00000000-0000-4000-8000-000000000001",
		ProjectID:          "00000000-0000-4000-8000-000000000002",
		ActorUserID:        "00000000-0000-4000-8000-000000000003",
		AssigneeEmployeeID: "00000000-0000-4000-8000-000000000004",
		IdempotencyKey:     "invalid-criteria", Title: "Valid title", TaskType: "feature", Priority: "NORMAL",
		AcceptanceCriteria: []byte(`[{"text":"criteria must be a string"}]`),
		RequiredChecks:     []byte(`[]`),
	}
	if _, _, err := NewService(nil).CreateDraft(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateDraft() error = %v, want ErrInvalidInput for non-string acceptance criterion", err)
	}
}

func TestCreateDraftAcceptsUppercaseManifestDigestAllowedByEventContract(t *testing.T) {
	input := testCreateDraftInput(
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000004",
		"uppercase-digest",
	)
	input.ManifestDigest = strings.Repeat("A", 64)
	if _, err := normalizeAndValidate(input); err != nil {
		t.Fatalf("normalizeAndValidate() rejected schema-valid uppercase SHA-256 digest: %v", err)
	}
}

func TestCreateDraftCommitsTaskEventOutboxAndAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)

	service := NewService(db)
	input := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "create-draft-001")
	task, replayed, err := service.CreateDraft(ctx, input)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if replayed {
		t.Fatal("first CreateDraft() call was reported as an idempotent replay")
	}
	if task.ID == "" || task.Status != "DRAFT" || task.Title != "Persist a task" {
		t.Fatalf("CreateDraft() task = %+v, want persisted DRAFT task", task)
	}

	var sequence int64
	if err := db.QueryRowContext(ctx, `
		SELECT last_event_sequence FROM projects
		WHERE organization_id = $1 AND id = $2`, organizationID, projectID).Scan(&sequence); err != nil {
		t.Fatalf("read project event sequence: %v", err)
	}
	if sequence != 1 {
		t.Fatalf("project last_event_sequence = %d, want 1", sequence)
	}

	var eventType, eventTaskID, initialState string
	var eventSequence int64
	if err := db.QueryRowContext(ctx, `
		SELECT event_type, task_id::text, stream_sequence, data->>'initialState'
		FROM agent_events WHERE organization_id = $1 AND project_id = $2`,
		organizationID, projectID).Scan(&eventType, &eventTaskID, &eventSequence, &initialState); err != nil {
		t.Fatalf("read persisted task event: %v", err)
	}
	if eventType != "task.created" || eventTaskID != task.ID || eventSequence != 1 || initialState != "DRAFT" {
		t.Fatalf("event = type %q task %q sequence %d state %q, want task.created for the new DRAFT task at sequence 1", eventType, eventTaskID, eventSequence, initialState)
	}

	var outboxCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM event_outbox
		WHERE project_id = $1 AND stream_sequence = $2`, projectID, eventSequence).Scan(&outboxCount); err != nil {
		t.Fatalf("count event outbox rows: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("matching outbox rows = %d, want 1", outboxCount)
	}

	var auditCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_records
		WHERE organization_id = $1 AND actor_user_id = $2
			AND action = 'task.create' AND target_id = $3`,
		organizationID, ownerID, task.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count task audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("matching task audit rows = %d, want 1", auditCount)
	}

	listed, err := service.ListByProject(ctx, organizationID, projectID)
	if err != nil {
		t.Fatalf("ListByProject() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != task.ID || listed[0].Status != "DRAFT" {
		t.Fatalf("ListByProject() = %+v, want the created DRAFT task", listed)
	}
	var otherOrganizationID string
	if err := db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('Other task test org') RETURNING id::text`).Scan(&otherOrganizationID); err != nil {
		t.Fatalf("create other organization fixture: %v", err)
	}
	if _, err := service.ListByProject(ctx, otherOrganizationID, projectID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("ListByProject() across organizations error = %v, want ErrProjectNotFound", err)
	}
}

func TestCreateDraftIdempotencyReplayReturnsOriginalTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	input := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "create-draft-replay")

	first, firstReplay, err := service.CreateDraft(ctx, input)
	if err != nil || firstReplay {
		t.Fatalf("first CreateDraft() = task %+v, replay %t, error %v", first, firstReplay, err)
	}
	second, secondReplay, err := service.CreateDraft(ctx, input)
	if err != nil {
		t.Fatalf("replayed CreateDraft() error = %v", err)
	}
	if !secondReplay || second.ID != first.ID {
		t.Fatalf("replayed task = %+v, replay %t; want original task %q and replay=true", second, secondReplay, first.ID)
	}
	for _, table := range []string{"tasks", "agent_events", "event_outbox", "audit_records"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s rows = %d, want 1 after replay", table, count)
		}
	}
	var sequence int64
	if err := db.QueryRowContext(ctx, `SELECT last_event_sequence FROM projects WHERE id = $1`, projectID).Scan(&sequence); err != nil {
		t.Fatalf("read replay project sequence: %v", err)
	}
	if sequence != 1 {
		t.Fatalf("last_event_sequence after replay = %d, want 1", sequence)
	}
}

func TestCreateDraftRejectsIdempotencyKeyWithDifferentRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	input := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "create-draft-conflict")
	if _, _, err := service.CreateDraft(ctx, input); err != nil {
		t.Fatalf("first CreateDraft() error = %v", err)
	}
	input.Title = "Different task"
	if _, _, err := service.CreateDraft(ctx, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("CreateDraft() error = %v, want ErrIdempotencyConflict", err)
	}
	var taskCount, eventCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM agent_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if taskCount != 1 || eventCount != 1 {
		t.Fatalf("tasks/events after idempotency conflict = %d/%d, want 1/1", taskCount, eventCount)
	}
}

func newTaskTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	baseURL := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}

	adminDB, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	if err := adminDB.PingContext(ctx); err != nil {
		adminDB.Close()
		t.Fatalf("connect to PostgreSQL test database: %v", err)
	}

	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		adminDB.Close()
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schemaName := "dioffice_tasks_test_" + hex.EncodeToString(randomBytes)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		adminDB.Close()
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE"); err != nil {
			t.Errorf("drop isolated test schema: %v", err)
		}
		if err := adminDB.Close(); err != nil {
			t.Errorf("close PostgreSQL test database: %v", err)
		}
	})

	testURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL test database URL: %v", err)
	}
	query := testURL.Query()
	query.Set("search_path", schemaName)
	testURL.RawQuery = query.Encode()

	db, err := sql.Open("pgx", testURL.String())
	if err != nil {
		t.Fatalf("open isolated test schema: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close isolated test schema: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated test schema: %v", err)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate task test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	migrationDir := filepath.Join(repoRoot, "db", "migrations")
	if err := migrations.Up(ctx, db, migrationDir); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

func createTaskFixtures(t *testing.T, ctx context.Context, db *sql.DB) (organizationID, projectID, ownerID, employeeID string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert task fixture: %v", err)
		}
		return id
	}
	organizationID = insert(`INSERT INTO organizations (name) VALUES ('Task test org') RETURNING id::text`)
	ownerID = insert(`INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner@example.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID = insert(`INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Frontend Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID = insert(`INSERT INTO projects (organization_id, name) VALUES ($1, 'Task test project') RETURNING id::text`, organizationID)
	return organizationID, projectID, ownerID, employeeID
}

func testCreateDraftInput(organizationID, projectID, ownerID, employeeID, idempotencyKey string) CreateDraftInput {
	return CreateDraftInput{
		OrganizationID: organizationID, ProjectID: projectID, ActorUserID: ownerID,
		AssigneeEmployeeID: employeeID, IdempotencyKey: idempotencyKey,
		Title: "Persist a task", Description: "Create a draft without starting a worker",
		AcceptanceCriteria: []byte(`["Task and event persist"]`),
		RequiredChecks:     []byte(`[]`), TaskType: "feature", Priority: "NORMAL",
	}
}
