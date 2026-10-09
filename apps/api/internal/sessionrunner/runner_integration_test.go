package sessionrunner

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
	"github.com/rendr17/dioffice/apps/api/internal/providers"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

const testManifest = `{
	"manifestVersion": 1,
	"workerProfile": "node-22-pnpm-10-playwright",
	"workingDirectory": ".",
	"networkProfile": "none",
	"environment": {"passThrough": []},
	"commands": {
		"install": {"argv": ["true"], "timeoutSeconds": 60},
		"start": {"argv": ["true"], "timeoutSeconds": 60},
		"checks": [{"id": "smoke", "name": "Smoke", "argv": ["true"], "required": true, "timeoutSeconds": 60}]
	},
	"preview": {"port": 3000, "healthPath": "/", "readinessTimeoutSeconds": 30, "routes": ["/"], "viewports": [{"name": "d", "width": 800, "height": 600}]},
	"resources": {"cpu": 1, "memoryMiB": 512, "diskGiB": 1, "attemptTimeoutSeconds": 300}
}
`

// fakeRuntime records calls and can be configured to fail.
type fakeRuntime struct {
	mu           sync.Mutex
	createErr    error
	promptErr    error
	createdDir   string
	createdTitle string
	prompts      map[string]string
	aborted      []string
	nextID       string
}

func (f *fakeRuntime) CreateSession(_ context.Context, directory, title string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.createdDir, f.createdTitle = directory, title
	if f.nextID == "" {
		f.nextID = "rt-session-1"
	}
	return f.nextID, nil
}

func (f *fakeRuntime) SendPrompt(_ context.Context, sessionID, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.promptErr != nil {
		return f.promptErr
	}
	if f.prompts == nil {
		f.prompts = map[string]string{}
	}
	f.prompts[sessionID] = prompt
	return nil
}

func (f *fakeRuntime) Abort(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = append(f.aborted, sessionID)
	return nil
}

func newSessionTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schemaName := "dioffice_sess_test_" + hex.EncodeToString(randomBytes)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
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
	db.SetMaxOpenConns(4)
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
		t.Fatal("could not locate sessionrunner test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

// readyWorkspace drives a task to PROVISIONING through the real task
// service, then marks the workspace READY with a materialized worktree
// directory containing the given manifest bytes — the same end state the
// provisioner produces, without duplicating git setup here.
func readyWorkspace(t *testing.T, ctx context.Context, db *sql.DB, workRoot, manifest string) (taskID, attemptID string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	organizationID := insert(`INSERT INTO organizations (name) VALUES ('Session org') RETURNING id::text`)
	ownerID := insert(`INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner@sess.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID := insert(`INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID := insert(`INSERT INTO projects (organization_id, name) VALUES ($1, 'Session project') RETURNING id::text`, organizationID)
	insert(`INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch) VALUES ($1, $2, 'github', 'acme', 'widgets', 'main') RETURNING id::text`, organizationID, projectID)

	service := tasks.NewService(db)
	draft, _, err := service.CreateDraft(ctx, tasks.CreateDraftInput{
		OrganizationID: organizationID, ProjectID: projectID, ActorUserID: ownerID,
		AssigneeEmployeeID: employeeID, IdempotencyKey: "sess-draft",
		Title: "Build the widget panel", Description: "Add a widget panel to the dashboard",
		AcceptanceCriteria: json.RawMessage(`["Panel renders", "Smoke check passes"]`),
		RequiredChecks:     json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	ready, _, err := service.MarkReady(ctx, tasks.MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "sess-ready", ExpectedVersion: draft.Version,
		ManifestDigest: executionmanifest.Digest([]byte(manifest)),
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	if _, _, err := service.StartExecution(ctx, tasks.StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: "sess-start", ExpectedVersion: ready.Version,
	}); err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM execution_attempts WHERE task_id = $1`, ready.ID).Scan(&attemptID); err != nil {
		t.Fatalf("read attempt id: %v", err)
	}

	worktreeRef := "worktrees/" + ready.ID
	worktreeDir := filepath.Join(workRoot, filepath.FromSlash(worktreeRef))
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".dioffice"), 0o755); err != nil {
		t.Fatalf("create worktree fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)),
		[]byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE workspaces SET state = 'READY', worktree_ref = $2
		WHERE task_id = $1`, ready.ID, worktreeRef); err != nil {
		t.Fatalf("mark workspace ready: %v", err)
	}
	// The provisioner owns the CREATED → PROVISIONING claim; reach the same
	// committed end state here without duplicating git work.
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET state = 'PROVISIONING'
		WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("mark attempt provisioning: %v", err)
	}
	return ready.ID, attemptID
}

func newTestRunner(t *testing.T, db *sql.DB, workRoot string, rt Runtime) *Runner {
	t.Helper()
	runner, err := New(db, Config{WorkRoot: workRoot, OpTimeout: time.Minute}, rt)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runner
}

func TestRunnerStartsSessionAndMarksTaskInProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID := readyWorkspace(t, ctx, db, workRoot, testManifest)
	rt := &fakeRuntime{}
	runner := newTestRunner(t, db, workRoot, rt)

	processed, err := runner.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("RunOnce() processed = %d, want 1", processed)
	}

	var sessionStatus, runtimeID string
	if err := db.QueryRowContext(ctx, `
		SELECT status, COALESCE(runtime_session_id, '') FROM agent_sessions WHERE attempt_id = $1`,
		attemptID).Scan(&sessionStatus, &runtimeID); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if sessionStatus != "RUNNING" || runtimeID != "rt-session-1" {
		t.Fatalf("session = %s runtime %q, want RUNNING rt-session-1", sessionStatus, runtimeID)
	}
	var attemptState, workspaceState, taskStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT a.state, w.state, t.status
		FROM execution_attempts a, workspaces w, tasks t
		WHERE a.task_id = $1 AND w.task_id = $1 AND t.id = $1`, taskID).
		Scan(&attemptState, &workspaceState, &taskStatus); err != nil {
		t.Fatalf("read states: %v", err)
	}
	if attemptState != "RUNNING" || workspaceState != "IN_USE" || taskStatus != "IN_PROGRESS" {
		t.Fatalf("states = attempt:%s workspace:%s task:%s, want RUNNING/IN_USE/IN_PROGRESS",
			attemptState, workspaceState, taskStatus)
	}

	wantDir := filepath.Join(workRoot, "worktrees", taskID)
	if rt.createdDir != wantDir {
		t.Fatalf("runtime directory = %q, want %q", rt.createdDir, wantDir)
	}
	prompt := rt.prompts["rt-session-1"]
	for _, want := range []string{"Deni", "Build the widget panel", "Panel renders", "smoke", "task/" + taskID} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT event_type FROM agent_events WHERE task_id = $1
		AND event_type IN ('session.started', 'execution_attempt.state_changed',
			'workspace.state_changed', 'task.state_changed')
		ORDER BY stream_sequence`, taskID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var et string
		if err := rows.Scan(&et); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		types = append(types, et)
	}
	// Lifecycle facts before the session: task READY, attempt CREATED,
	// workspace PROVISIONING, task PROVISIONING. Then the runner's
	// session.started + attempt RUNNING + workspace IN_USE + task IN_PROGRESS.
	if len(types) != 8 || types[4] != "session.started" ||
		types[5] != "execution_attempt.state_changed" ||
		types[6] != "workspace.state_changed" || types[7] != "task.state_changed" {
		t.Fatalf("session events = %v, want 4 lifecycle facts + session.started + 3 transitions", types)
	}

	if again, err := runner.RunOnce(ctx); err != nil || again != 0 {
		t.Fatalf("second RunOnce() = %d/%v, want 0 claims", again, err)
	}
}

// A continuation attempt (Owner request-changes) is PROVISIONING while the
// task is already IN_PROGRESS on the preserved READY workspace. The runner
// must claim it, pass the feedback into the prompt, and not emit a redundant
// IN_PROGRESS → IN_PROGRESS task fact.
func TestRunnerClaimsContinuationAttemptWithChangeRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, firstAttemptID := readyWorkspace(t, ctx, db, workRoot, testManifest)

	// Simulate the committed state after tasks.RequestChanges: prior attempt
	// is history, task back IN_PROGRESS, new PROVISIONING attempt carrying
	// the Owner feedback, workspace still READY.
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET state = 'SUCCEEDED', ended_at = now(),
			candidate_sha = 'dddddddddddddddddddddddddddddddddddddddd'
		WHERE id = $1`, firstAttemptID); err != nil {
		t.Fatalf("succeed first attempt: %v", err)
	}
	var organizationID, employeeID string
	if err := db.QueryRowContext(ctx, `
		SELECT organization_id::text, employee_id::text FROM execution_attempts WHERE id = $1`,
		firstAttemptID).Scan(&organizationID, &employeeID); err != nil {
		t.Fatalf("read attempt owner: %v", err)
	}
	var projectID string
	if err := db.QueryRowContext(ctx,
		`SELECT project_id::text FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		t.Fatalf("read project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = 'IN_PROGRESS' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("mark task in progress: %v", err)
	}
	var secondAttemptID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id,
			attempt_number, state, runtime_type, change_request)
		VALUES ($1, $2, $3, $4, 2, 'PROVISIONING', 'opencode',
			'Add input validation and update the tests.')
		RETURNING id::text`,
		organizationID, projectID, taskID, employeeID).Scan(&secondAttemptID); err != nil {
		t.Fatalf("insert continuation attempt: %v", err)
	}

	rt := &fakeRuntime{}
	runner := newTestRunner(t, db, workRoot, rt)
	processed, err := runner.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("RunOnce() processed = %d, want 1", processed)
	}

	var attemptState string
	if err := db.QueryRowContext(ctx,
		`SELECT state FROM execution_attempts WHERE id = $1`, secondAttemptID).Scan(&attemptState); err != nil {
		t.Fatalf("read continuation attempt: %v", err)
	}
	if attemptState != "RUNNING" {
		t.Fatalf("continuation attempt = %s, want RUNNING", attemptState)
	}
	prompt := rt.prompts["rt-session-1"]
	if !strings.Contains(prompt, "Add input validation and update the tests.") ||
		!strings.Contains(prompt, "continuation attempt") {
		t.Fatalf("continuation prompt missing feedback:\n%s", prompt)
	}

	// The task was already IN_PROGRESS — the continuation start must not add
	// a redundant IN_PROGRESS → IN_PROGRESS task.state_changed fact.
	var taskTransitions int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_events
		WHERE task_id = $1 AND event_type = 'task.state_changed'
			AND data->>'toState' = 'IN_PROGRESS'`,
		taskID).Scan(&taskTransitions); err != nil {
		t.Fatalf("count task transitions: %v", err)
	}
	if taskTransitions != 0 {
		t.Fatalf("IN_PROGRESS task.state_changed facts = %d, want 0 (task was already in progress)",
			taskTransitions)
	}
}

func TestRunnerBlocksTaskWhenRuntimeUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID := readyWorkspace(t, ctx, db, workRoot, testManifest)
	rt := &fakeRuntime{createErr: &RuntimeError{Kind: "unreachable", Detail: "connection refused"}}
	runner := newTestRunner(t, db, workRoot, rt)

	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	var sessionStatus, attemptState, attemptError, taskStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT s.status, a.state, a.error_code, t.status
		FROM agent_sessions s, execution_attempts a, tasks t
		WHERE s.attempt_id = $1 AND a.id = $1 AND t.id = a.task_id`, attemptID).
		Scan(&sessionStatus, &attemptState, &attemptError, &taskStatus); err != nil {
		t.Fatalf("read failure state: %v", err)
	}
	if sessionStatus != "FAILED" || attemptState != "FAILED" ||
		attemptError != ReasonRuntimeUnreachable || taskStatus != "BLOCKED" {
		t.Fatalf("failure = session:%s attempt:%s/%s task:%s, want FAILED/FAILED/%s/BLOCKED",
			sessionStatus, attemptState, attemptError, taskStatus, ReasonRuntimeUnreachable)
	}
	// Workspace stays READY for an explicit Owner Retry.
	var workspaceState string
	if err := db.QueryRowContext(ctx,
		`SELECT state FROM workspaces WHERE task_id = $1`, taskID).Scan(&workspaceState); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if workspaceState != "READY" {
		t.Fatalf("workspace = %s, want READY (kept for retry)", workspaceState)
	}
	var failedPayload string
	if err := db.QueryRowContext(ctx, `
		SELECT data::text FROM agent_events
		WHERE task_id = $1 AND event_type = 'session.failed'`, taskID).
		Scan(&failedPayload); err != nil {
		t.Fatalf("read session.failed event: %v", err)
	}
	if !strings.Contains(failedPayload, `"errorCode": "runtime_unreachable"`) ||
		!strings.Contains(failedPayload, `"retryable": true`) {
		t.Fatalf("session.failed payload = %s", failedPayload)
	}
}

func TestRunnerReclaimsInterruptedSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID := readyWorkspace(t, ctx, db, workRoot, testManifest)

	// A previous runner died after creating the provider session.
	var staleSessionID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, status, runtime_session_id, created_at
		)
		SELECT a.organization_id, a.project_id, a.task_id, a.id, a.employee_id,
			w.id, 'opencode', 'STARTING', 'rt-orphan', now() - interval '1 hour'
		FROM execution_attempts a, workspaces w
		WHERE a.id = $1 AND w.task_id = a.task_id
		RETURNING id::text`, attemptID).Scan(&staleSessionID); err != nil {
		t.Fatalf("insert stale session: %v", err)
	}

	rt := &fakeRuntime{nextID: "rt-session-2"}
	runner := newTestRunner(t, db, workRoot, rt)
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	var staleStatus string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM agent_sessions WHERE id = $1`, staleSessionID).Scan(&staleStatus); err != nil {
		t.Fatalf("read stale session: %v", err)
	}
	if staleStatus != "FAILED" {
		t.Fatalf("stale session = %s, want FAILED", staleStatus)
	}
	if len(rt.aborted) != 1 || rt.aborted[0] != "rt-orphan" {
		t.Fatalf("aborted = %v, want [rt-orphan]", rt.aborted)
	}
	var runningID string
	if err := db.QueryRowContext(ctx, `
		SELECT runtime_session_id FROM agent_sessions
		WHERE attempt_id = $1 AND status = 'RUNNING'`, attemptID).Scan(&runningID); err != nil {
		t.Fatalf("read new session: %v", err)
	}
	if runningID != "rt-session-2" {
		t.Fatalf("new runtime session = %q, want rt-session-2", runningID)
	}
	var interrupted string
	if err := db.QueryRowContext(ctx, `
		SELECT data::text FROM agent_events
		WHERE task_id = $1 AND event_type = 'session.failed'`, taskID).Scan(&interrupted); err != nil {
		t.Fatalf("read interrupted event: %v", err)
	}
	if !strings.Contains(interrupted, ReasonSessionInterrupted) {
		t.Fatalf("interrupted payload = %s", interrupted)
	}
}

func TestRunnerRejectsManifestDigestMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID := readyWorkspace(t, ctx, db, workRoot, testManifest)
	// Simulate a manifest edit after provisioning.
	worktreeDir := filepath.Join(workRoot, "worktrees", taskID)
	if err := os.WriteFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)),
		[]byte(strings.Replace(testManifest, `"port": 3000`, `"port": 4000`, 1)), 0o644); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}
	rt := &fakeRuntime{}
	runner := newTestRunner(t, db, workRoot, rt)

	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if rt.createdDir != "" {
		t.Fatal("runtime must not be called when the manifest no longer matches")
	}
	var attemptError, taskStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT a.error_code, t.status FROM execution_attempts a, tasks t
		WHERE a.id = $1 AND t.id = a.task_id`, attemptID).Scan(&attemptError, &taskStatus); err != nil {
		t.Fatalf("read failure: %v", err)
	}
	if attemptError != ReasonManifestDigest || taskStatus != "BLOCKED" {
		t.Fatalf("failure = %s/%s, want %s/BLOCKED", attemptError, taskStatus, ReasonManifestDigest)
	}
}

func TestRunnerFailsClosedWhenProviderIsUnimplemented(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)
	workRoot := t.TempDir()
	_, attemptID := readyWorkspace(t, ctx, db, workRoot, testManifest)

	// Employee.default_runtime flows into the attempt at Start; switching the
	// recorded key exercises the resolver's closed failure path.
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET runtime_type = 'codex' WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("set attempt provider: %v", err)
	}
	runner, err := NewWithResolver(db, Config{WorkRoot: workRoot, OpTimeout: time.Minute},
		func(context.Context, string, string) (Runtime, error) {
			return nil, providers.ErrProviderNotImplemented
		})
	if err != nil {
		t.Fatalf("NewWithResolver() error = %v", err)
	}

	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	var attemptError, sessionStatus, sessionRuntime, taskStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT a.error_code, s.status, s.runtime_type, t.status
		FROM execution_attempts a
		JOIN agent_sessions s ON s.attempt_id = a.id
		JOIN tasks t ON t.id = a.task_id
		WHERE a.id = $1`, attemptID).Scan(&attemptError, &sessionStatus, &sessionRuntime, &taskStatus); err != nil {
		t.Fatalf("read failure state: %v", err)
	}
	if attemptError != ReasonProviderUnbuilt || sessionStatus != "FAILED" ||
		sessionRuntime != "codex" || taskStatus != "BLOCKED" {
		t.Fatalf("failure = %s/%s/%s/%s, want %s/FAILED/codex/BLOCKED",
			attemptError, sessionStatus, sessionRuntime, taskStatus, ReasonProviderUnbuilt)
	}
	var retryable bool
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(retryable, true) FROM execution_attempts WHERE id = $1`, attemptID).
		Scan(&retryable); err != nil {
		t.Fatalf("read retryable: %v", err)
	}
	if retryable {
		t.Fatal("provider_not_implemented must not be marked retryable")
	}
}

func TestResolverHonorsOrgProviderConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newSessionTestDatabase(t, ctx)

	var organizationID string
	if err := db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('Resolver org') RETURNING id::text`).
		Scan(&organizationID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	resolver := Resolver{DB: db, OpenCodeEnvURL: "http://127.0.0.1:4096"}

	// No config row → dev env fallback builds the OpenCode adapter.
	runtime, err := resolver.Resolve(ctx, organizationID, "opencode")
	if err != nil || runtime == nil {
		t.Fatalf("Resolve(opencode fallback) = %v, %v", runtime, err)
	}
	// Unimplemented provider keys fail closed regardless of env config.
	if _, err := resolver.Resolve(ctx, organizationID, "codex"); !errors.Is(err, providers.ErrProviderNotImplemented) {
		t.Fatalf("Resolve(codex) = %v, want ErrProviderNotImplemented", err)
	}
	// An explicitly disabled config wins over the env fallback.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO provider_configs (organization_id, provider_key, label, base_url, enabled)
		VALUES ($1, 'opencode', 'Office OpenCode', 'http://127.0.0.1:5000', false)`, organizationID); err != nil {
		t.Fatalf("insert disabled config: %v", err)
	}
	if _, err := resolver.Resolve(ctx, organizationID, "opencode"); !errors.Is(err, providers.ErrProviderDisabled) {
		t.Fatalf("Resolve(disabled opencode) = %v, want ErrProviderDisabled", err)
	}
	// Enable it → the saved base_url drives the adapter, not the env fallback.
	if _, err := db.ExecContext(ctx, `
		UPDATE provider_configs SET enabled = true
		WHERE organization_id = $1 AND provider_key = 'opencode'`, organizationID); err != nil {
		t.Fatalf("enable config: %v", err)
	}
	enabled, err := resolver.Resolve(ctx, organizationID, "opencode")
	if err != nil || enabled == nil {
		t.Fatalf("Resolve(enabled opencode) = %v, %v", enabled, err)
	}
}
