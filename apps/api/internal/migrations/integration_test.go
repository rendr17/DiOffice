package migrations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationCommandIsIdempotentAndEnforcesTenantScope(t *testing.T) {
	baseURL := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	adminDB, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open migration test database: %v", err)
	}
	if err := adminDB.PingContext(ctx); err != nil {
		adminDB.Close()
		t.Fatalf("connect to migration test database: %v", err)
	}

	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		adminDB.Close()
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schemaName := "dioffice_migration_test_" + hex.EncodeToString(randomBytes)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		adminDB.Close()
		t.Fatalf("create isolated migration schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE"); err != nil {
			t.Errorf("drop isolated migration schema: %v", err)
		}
		if err := adminDB.Close(); err != nil {
			t.Errorf("close migration test database: %v", err)
		}
	})

	scopedURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse migration test database URL: %v", err)
	}
	query := scopedURL.Query()
	query.Set("search_path", schemaName)
	scopedURL.RawQuery = query.Encode()
	testURL := scopedURL.String()

	scopedDB, err := sql.Open("pgx", testURL)
	if err != nil {
		t.Fatalf("open isolated migration schema: %v", err)
	}
	scopedDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := scopedDB.Close(); err != nil {
			t.Errorf("close isolated migration schema: %v", err)
		}
	})
	if err := scopedDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated migration schema: %v", err)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate migration test source")
	}
	apiDir := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	migrationsDir := filepath.Clean(filepath.Join(apiDir, "..", "..", "db", "migrations"))

	for attempt := 1; attempt <= 2; attempt++ {
		command := exec.CommandContext(ctx, "go", "run", "./cmd/migrate")
		command.Dir = apiDir
		command.Env = replaceEnvironment(os.Environ(), "DATABASE_URL", testURL)
		command.Env = replaceEnvironment(command.Env, "MIGRATIONS_DIR", migrationsDir)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("migration command attempt %d failed: %v\n%s", attempt, err, output)
		}
	}

	tables := []string{
		"organizations", "users", "user_sessions", "employees", "employee_skills",
		"employee_permissions", "projects", "repositories", "tasks", "agent_events",
		"event_outbox", "idempotency_keys", "audit_records", "goose_db_version",
	}
	for _, table := range tables {
		var exists bool
		if err := scopedDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Errorf("migration did not create table %q", table)
		}
	}

	organizationA := insertID(t, ctx, scopedDB, `INSERT INTO organizations (name) VALUES ('Org A') RETURNING id::text`)
	organizationB := insertID(t, ctx, scopedDB, `INSERT INTO organizations (name) VALUES ('Org B') RETURNING id::text`)
	ownerA := insertID(t, ctx, scopedDB, `INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner-a@example.invalid', 'Owner A') RETURNING id::text`, organizationA)
	ownerB := insertID(t, ctx, scopedDB, `INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner-b@example.invalid', 'Owner B') RETURNING id::text`, organizationB)
	employeeA := insertID(t, ctx, scopedDB, `INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Frontend Engineer', 'Engineering') RETURNING id::text`, organizationA)
	projectA := insertID(t, ctx, scopedDB, `INSERT INTO projects (organization_id, name) VALUES ($1, 'Project A') RETURNING id::text`, organizationA)
	projectB := insertID(t, ctx, scopedDB, `INSERT INTO projects (organization_id, name) VALUES ($1, 'Project B') RETURNING id::text`, organizationA)
	taskA := insertID(t, ctx, scopedDB, `
		INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			acceptance_criteria, required_checks, task_type, priority, status, created_by_user_id
		) VALUES ($1, $2, $3, 'Persist task', 'Create a durable draft', '[]', '[]', 'feature', 'NORMAL', 'DRAFT', $4)
		RETURNING id::text`, organizationA, projectA, employeeA, ownerA)

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO agent_events (
			schema_version, event_type, organization_id, project_id, task_id, employee_id,
			stream_sequence, occurred_at, producer, actor, correlation_id, data
		) VALUES ('1.0.0', 'task.updated', $1, $2, $3, $4, 1, now(), 'api', '{"type":"owner"}', gen_random_uuid(), '{}')`,
		organizationA, projectB, taskA, employeeA)
	if err == nil {
		t.Fatal("same-tenant task event was allowed on a different project stream")
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			acceptance_criteria, required_checks, task_type, created_by_user_id
		) VALUES ($1, $2, $3, 'Cross-tenant task', 'Must be rejected', '[]', '[]', 'feature', $4)`,
		organizationB, projectA, employeeA, ownerB)
	if err == nil {
		t.Fatal("cross-tenant project/employee references were accepted")
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			acceptance_criteria, required_checks, task_type, status, created_by_user_id
		) VALUES ($1, $2, $3, 'Invalid state', 'Must be rejected', '[]', '[]', 'feature', 'RUNNING', $4)`,
		organizationA, projectA, employeeA, ownerA)
	if err == nil {
		t.Fatal("non-canonical task state was accepted")
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			acceptance_criteria, required_checks, task_type, priority, created_by_user_id
		) VALUES ($1, $2, $3, 'Invalid priority', 'Must be rejected', '[]', '[]', 'feature', 'URGENTLY', $4)`,
		organizationA, projectA, employeeA, ownerA)
	if err == nil {
		t.Fatal("non-canonical task priority was accepted")
	}

	data := fmt.Sprintf(`{"title":"Persist task","description":"Create a durable draft","assigneeEmployeeId":"%s","priority":"NORMAL","initialState":"DRAFT","acceptanceCriteria":[]}`, employeeA)
	actor := fmt.Sprintf(`{"type":"owner","id":"%s"}`, ownerA)
	eventID := insertID(t, ctx, scopedDB, `
		INSERT INTO agent_events (
			schema_version, event_type, organization_id, project_id, task_id, employee_id,
			stream_sequence, occurred_at, producer, actor, correlation_id, data
		) VALUES ('1.0.0', 'task.created', $1, $2, $3, $4, 1, now(), 'api', $5, gen_random_uuid(), $6)
		RETURNING event_id::text`, organizationA, projectA, taskA, employeeA, actor, data)
	if _, err := scopedDB.ExecContext(ctx, `
		INSERT INTO event_outbox (event_id, project_id, stream_sequence, payload_json)
		VALUES ($1, $2, 1, '{"eventType":"task.created"}')`, eventID, projectA); err != nil {
		t.Fatalf("insert matching outbox record: %v", err)
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO agent_events (
			schema_version, event_type, organization_id, project_id, task_id, employee_id,
			stream_sequence, occurred_at, producer, actor, correlation_id, data
		) VALUES ('1.0.0', 'task.updated', $1, $2, $3, $4, 1, now(), 'api', $5, gen_random_uuid(), '{}')`,
		organizationA, projectA, taskA, employeeA, actor)
	if err == nil {
		t.Fatal("duplicate project event sequence was accepted")
	}
}

func insertID(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
		t.Fatalf("insert fixture row: %v", err)
	}
	return id
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	for index, item := range environment {
		if strings.HasPrefix(item, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}
