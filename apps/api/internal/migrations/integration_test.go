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
		"event_outbox", "idempotency_keys", "audit_records", "workspaces",
		"execution_attempts", "agent_sessions", "approvals", "pull_requests",
		"artifacts", "goose_db_version",
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

	workspaceA := insertID(t, ctx, scopedDB, `
		INSERT INTO workspaces (
			organization_id, project_id, task_id, branch_name, worktree_ref,
			worker_profile, state
		) VALUES ($1, $2, $3, 'task/demo', 'worktree-ref-1', 'node-22-pnpm-10-playwright', 'IN_USE')
		RETURNING id::text`, organizationA, projectA, taskA)

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO workspaces (
			organization_id, project_id, task_id, branch_name, worktree_ref,
			worker_profile, state
		) VALUES ($1, $2, $3, 'task/demo-2', 'worktree-ref-2', 'node-22-pnpm-10-playwright', 'READY')`,
		organizationA, projectA, taskA)
	if err == nil {
		t.Fatal("second writable workspace for the same task was accepted")
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id, attempt_number,
			state, runtime_type
		) VALUES ($1, $2, $3, $4, 1, 'RUNNING', 'opencode')`,
		organizationA, projectB, taskA, employeeA)
	if err == nil {
		t.Fatal("attempt was allowed on a different project than its task")
	}

	attemptA := insertID(t, ctx, scopedDB, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id, attempt_number,
			state, runtime_type, started_at
		) VALUES ($1, $2, $3, $4, 1, 'RUNNING', 'opencode', now())
		RETURNING id::text`, organizationA, projectA, taskA, employeeA)

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id, attempt_number,
			state, runtime_type
		) VALUES ($1, $2, $3, $4, 2, 'CREATED', 'opencode')`,
		organizationA, projectA, taskA, employeeA)
	if err == nil {
		t.Fatal("second active attempt for the same task was accepted")
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id, attempt_number,
			state, runtime_type
		) VALUES ($1, $2, $3, $4, 2, 'STUCK', 'opencode')`,
		organizationA, projectA, taskA, employeeA)
	if err == nil {
		t.Fatal("non-canonical attempt state was accepted")
	}

	sessionA := insertID(t, ctx, scopedDB, `
		INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, status, started_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'opencode', 'RUNNING', now())
		RETURNING id::text`, organizationA, projectA, taskA, attemptA, employeeA, workspaceA)

	approvalA := insertID(t, ctx, scopedDB, `
		INSERT INTO approvals (
			organization_id, project_id, task_id, attempt_id, requested_by_employee_id,
			action_type, action_digest, policy_version, expires_at
		) VALUES ($1, $2, $3, $4, $5, 'merge', $6, 'v1', now() + interval '1 hour')
		RETURNING id::text`,
		organizationA, projectA, taskA, attemptA, employeeA,
		strings.Repeat("a", 64))

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO approvals (
			organization_id, project_id, task_id, attempt_id,
			action_type, action_digest, policy_version, expires_at
		) VALUES ($1, $2, $3, $4, 'check_override', $5, 'v1', now() + interval '1 hour')`,
		organizationA, projectA, taskA, attemptA, strings.Repeat("b", 64))
	if err == nil {
		t.Fatal("second pending approval for the same task was accepted")
	}

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO approvals (
			organization_id, project_id, task_id, attempt_id,
			action_type, action_digest, policy_version, expires_at
		) VALUES ($1, $2, $3, $4, 'merge', 'not-a-digest', 'v1', now() + interval '1 hour')`,
		organizationA, projectA, taskA, attemptA)
	if err == nil {
		t.Fatal("approval with a non-SHA-256 action digest was accepted")
	}

	repositoryA := insertID(t, ctx, scopedDB, `
		INSERT INTO repositories (
			organization_id, project_id, provider, owner, repo_name, default_branch
		) VALUES ($1, $2, 'github', 'octo', 'demo', 'main')
		RETURNING id::text`, organizationA, projectA)

	_, err = scopedDB.ExecContext(ctx, `
		INSERT INTO pull_requests (
			organization_id, project_id, task_id, repository_id, provider,
			external_pr_id, number, url, branch_name, head_sha, base_sha, state
		) VALUES ($1, $2, $3, $4, 'github', 'pr-1', 1, 'https://example.invalid/pr/1',
			'task/demo', 'zzzz', $5, 'OPEN')`,
		organizationA, projectA, taskA, repositoryA, strings.Repeat("c", 64))
	if err == nil {
		t.Fatal("pull request with a non-SHA head was accepted")
	}

	pullRequestA := insertID(t, ctx, scopedDB, `
		INSERT INTO pull_requests (
			organization_id, project_id, task_id, repository_id, provider,
			external_pr_id, number, url, branch_name, head_sha, base_sha, state
		) VALUES ($1, $2, $3, $4, 'github', 'pr-1', 1, 'https://example.invalid/pr/1',
			'task/demo', $5, $5, 'OPEN')
		RETURNING id::text`, organizationA, projectA, taskA, repositoryA, strings.Repeat("c", 64))

	_, err = scopedDB.ExecContext(ctx, `
		UPDATE tasks SET pull_request_id = $1 WHERE organization_id = $2 AND project_id = $3 AND id = $4`,
		pullRequestA, organizationA, projectA, taskA)
	if err != nil {
		t.Fatalf("task pull request link was rejected: %v", err)
	}

	artifactID := insertID(t, ctx, scopedDB, `
		INSERT INTO artifacts (
			id, organization_id, project_id, task_id, attempt_id, session_id, kind,
			storage_key, mime_type, size_bytes, sha256
		) VALUES (
			'0f8fad5b-d9cb-469f-a165-70867728950e', $1, $2, $3, $4, $5, 'screenshot',
			'artifacts/0f8fad5b-d9cb-469f-a165-70867728950e', 'image/png', 128, $6
		)
		RETURNING id::text`,
		organizationA, projectA, taskA, attemptA, sessionA, strings.Repeat("d", 64))
	var storageKey string
	if err := scopedDB.QueryRowContext(ctx, `
		SELECT storage_key FROM artifacts WHERE organization_id = $1 AND id = $2`,
		organizationA, artifactID).Scan(&storageKey); err != nil {
		t.Fatalf("read artifact storage key: %v", err)
	}
	if storageKey != "artifacts/"+artifactID {
		t.Fatalf("artifact storage key %q did not derive from its id", storageKey)
	}

	_, err = scopedDB.ExecContext(ctx, `
		UPDATE agent_events SET attempt_id = $1
		WHERE event_id = $2`, approvalA, eventID)
	if err == nil {
		t.Fatal("agent event accepted a non-attempt UUID as attempt_id")
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
