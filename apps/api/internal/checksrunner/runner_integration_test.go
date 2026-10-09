package checksrunner

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

const checkManifestPass = `{
	"manifestVersion": 1,
	"workerProfile": "node-22-pnpm-10-playwright",
	"workingDirectory": ".",
	"networkProfile": "none",
	"environment": {"passThrough": []},
	"commands": {
		"install": {"argv": ["git", "--version"], "timeoutSeconds": 60},
		"start": {"argv": ["git", "--version"], "timeoutSeconds": 60},
		"checks": [{"id": "head", "name": "HEAD resolves", "argv": ["git", "rev-parse", "HEAD"], "required": true, "timeoutSeconds": 60}]
	},
	"preview": {"port": 3000, "healthPath": "/", "readinessTimeoutSeconds": 30, "routes": ["/"], "viewports": [{"name": "d", "width": 800, "height": 600}]},
	"resources": {"cpu": 1, "memoryMiB": 512, "diskGiB": 1, "attemptTimeoutSeconds": 3600}
}
`

const checkManifestFail = `{
	"manifestVersion": 1,
	"workerProfile": "node-22-pnpm-10-playwright",
	"workingDirectory": ".",
	"networkProfile": "none",
	"environment": {"passThrough": []},
	"commands": {
		"install": {"argv": ["git", "--version"], "timeoutSeconds": 60},
		"start": {"argv": ["git", "--version"], "timeoutSeconds": 60},
		"checks": [{"id": "absent", "name": "Absent ref", "argv": ["git", "rev-parse", "--verify", "refs/heads/definitely-absent"], "required": true, "timeoutSeconds": 60}]
	},
	"preview": {"port": 3000, "healthPath": "/", "readinessTimeoutSeconds": 30, "routes": ["/"], "viewports": [{"name": "d", "width": 800, "height": 600}]},
	"resources": {"cpu": 1, "memoryMiB": 512, "diskGiB": 1, "attemptTimeoutSeconds": 3600}
}
`

type fakeArtifactStore struct {
	mu   sync.Mutex
	puts map[string][]byte
}

func (f *fakeArtifactStore) PutArtifact(_ context.Context, key, contentType string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts == nil {
		f.puts = map[string][]byte{}
	}
	if contentType != "text/plain" {
		return errUnexpectedType
	}
	f.puts[key] = append([]byte(nil), data...)
	return nil
}

var errUnexpectedType = errorString("unexpected content type")

type errorString string

func (e errorString) Error() string { return string(e) }

func newChecksTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
	schemaName := "dioffice_chk_test_" + hex.EncodeToString(randomBytes)
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
		t.Fatal("could not locate checksrunner test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// completedSession drives a task to PROVISIONING through the real task
// service, materializes a real git repository as the task worktree, and moves
// the rows to the post-session end state: attempt RUNNING, session COMPLETED,
// workspace IN_USE — exactly what the checks runner claims.
func completedSession(t *testing.T, ctx context.Context, db *sql.DB,
	workRoot, manifest string) (taskID, attemptID, worktreeDir string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	organizationID := insert(`INSERT INTO organizations (name) VALUES ('Checks org') RETURNING id::text`)
	ownerID := insert(`INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner@chk.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID := insert(`INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID := insert(`INSERT INTO projects (organization_id, name) VALUES ($1, 'Checks project') RETURNING id::text`, organizationID)
	insert(`INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch) VALUES ($1, $2, 'github', 'acme', 'widgets', 'main') RETURNING id::text`, organizationID, projectID)

	service := tasks.NewService(db)
	draft, _, err := service.CreateDraft(ctx, tasks.CreateDraftInput{
		OrganizationID: organizationID, ProjectID: projectID, ActorUserID: ownerID,
		AssigneeEmployeeID: employeeID, IdempotencyKey: "chk-draft",
		Title: "Build the widget panel", Description: "Add a widget panel to the dashboard",
		AcceptanceCriteria: json.RawMessage(`["Panel renders"]`),
		RequiredChecks:     json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	ready, _, err := service.MarkReady(ctx, tasks.MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "chk-ready", ExpectedVersion: draft.Version,
		ManifestDigest: executionmanifest.Digest([]byte(manifest)),
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	if _, _, err := service.StartExecution(ctx, tasks.StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: "chk-start", ExpectedVersion: ready.Version,
	}); err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM execution_attempts WHERE task_id = $1`, ready.ID).Scan(&attemptID); err != nil {
		t.Fatalf("read attempt id: %v", err)
	}

	// A real git repository stands in for the provisioner-managed worktree:
	// the checks runner only needs status/commit/rev-parse to work.
	worktreeRef := "worktrees/" + ready.ID
	worktreeDir = filepath.Join(workRoot, filepath.FromSlash(worktreeRef))
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".dioffice"), 0o755); err != nil {
		t.Fatalf("create worktree fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)),
		[]byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, "panel.ts"), []byte("export const panel = true\n"), 0o644); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	git(t, worktreeDir, "init", "-b", "task/"+ready.ID)
	git(t, worktreeDir, "config", "core.autocrlf", "false")
	git(t, worktreeDir, "-c", "user.name=Deni", "-c", "user.email=deni@dioffice.local", "add", "-A")
	git(t, worktreeDir, "-c", "user.name=Deni", "-c", "user.email=deni@dioffice.local",
		"commit", "-m", "feat: widget panel")

	var workspaceID string
	if err := db.QueryRowContext(ctx, `
		UPDATE workspaces SET state = 'IN_USE', worktree_ref = $2
		WHERE task_id = $1 RETURNING id::text`, ready.ID, worktreeRef).Scan(&workspaceID); err != nil {
		t.Fatalf("mark workspace in use: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET state = 'RUNNING', started_at = now() - interval '2 minutes'
		WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("mark attempt running: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, status, runtime_session_id, started_at, ended_at)
		SELECT a.organization_id, a.project_id, a.task_id, a.id, a.employee_id,
			$2::uuid, 'opencode', 'COMPLETED', 'rt-done', now() - interval '1 minute', now()
		FROM execution_attempts a WHERE a.id = $1`, attemptID, workspaceID); err != nil {
		t.Fatalf("insert completed session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = 'IN_PROGRESS' WHERE id = $1`, ready.ID); err != nil {
		t.Fatalf("mark task in progress: %v", err)
	}
	return ready.ID, attemptID, worktreeDir
}

func newTestRunner(t *testing.T, db *sql.DB, workRoot string, store ArtifactStore) *Runner {
	t.Helper()
	runner, err := New(db, Config{WorkRoot: workRoot, OpTimeout: 5 * time.Minute}, nil, store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runner
}

func eventTypes(t *testing.T, ctx context.Context, db *sql.DB, taskID string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT event_type FROM agent_events WHERE task_id = $1
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
	return types
}

func TestRunnerPassesChecksAndSucceedsAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newChecksTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, worktreeDir := completedSession(t, ctx, db, workRoot, checkManifestPass)
	wantSHA := strings.ToLower(git(t, worktreeDir, "rev-parse", "HEAD"))
	store := &fakeArtifactStore{}
	runner := newTestRunner(t, db, workRoot, store)

	processed, err := runner.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("RunOnce() processed = %d, want 1", processed)
	}

	var attemptState, checksState, candidateSHA, taskStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT a.state, a.checks_state, COALESCE(a.candidate_sha, ''), t.status
		FROM execution_attempts a, tasks t WHERE a.id = $1 AND t.id = a.task_id`,
		attemptID).Scan(&attemptState, &checksState, &candidateSHA, &taskStatus); err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if attemptState != "SUCCEEDED" || checksState != "PASSED" {
		t.Fatalf("attempt = %s checks %s, want SUCCEEDED/PASSED", attemptState, checksState)
	}
	if candidateSHA != wantSHA {
		t.Fatalf("candidate_sha = %q, want tested HEAD %q", candidateSHA, wantSHA)
	}
	// The task stays IN_PROGRESS: IN_REVIEW additionally requires a pull
	// request pointing at this SHA, which is a later pipeline step.
	if taskStatus != "IN_PROGRESS" {
		t.Fatalf("task = %s, want IN_PROGRESS (awaiting PR step)", taskStatus)
	}

	types := eventTypes(t, ctx, db, taskID)
	var started, passed, artifacts, succeeded int
	for _, et := range types {
		switch et {
		case "check.started":
			started++
		case "check.passed":
			passed++
		case "artifact.created":
			artifacts++
		case "execution_attempt.state_changed":
			succeeded++
		}
	}
	if started != 1 || passed != 1 || artifacts != 1 {
		t.Fatalf("check events = started:%d passed:%d artifacts:%d, want 1/1/1 (types %v)",
			started, passed, artifacts, types)
	}
	var payload string
	if err := db.QueryRowContext(ctx, `
		SELECT data::text FROM agent_events WHERE task_id = $1 AND event_type = 'check.passed'`,
		taskID).Scan(&payload); err != nil {
		t.Fatalf("read check.passed payload: %v", err)
	}
	if !strings.Contains(payload, `"commitSha": "`+wantSHA) || !strings.Contains(payload, `"checkId": "head"`) {
		t.Fatalf("check.passed payload = %s", payload)
	}
	var artifactKey string
	if err := db.QueryRowContext(ctx, `
		SELECT storage_key FROM artifacts WHERE task_id = $1`, taskID).Scan(&artifactKey); err != nil {
		t.Fatalf("read artifact row: %v", err)
	}
	if _, ok := store.puts[artifactKey]; !ok {
		t.Fatalf("artifact %q not written to the store", artifactKey)
	}
	if again, err := runner.RunOnce(ctx); err != nil || again != 0 {
		t.Fatalf("second RunOnce() = %d/%v, want 0 claims", again, err)
	}
}

func TestRunnerFailsRequiredCheckAndTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newChecksTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, _ := completedSession(t, ctx, db, workRoot, checkManifestFail)
	runner := newTestRunner(t, db, workRoot, &fakeArtifactStore{})

	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	var attemptState, errorCode, taskStatus string
	var retryable bool
	if err := db.QueryRowContext(ctx, `
		SELECT a.state, a.error_code, a.retryable, t.status
		FROM execution_attempts a, tasks t WHERE a.id = $1 AND t.id = a.task_id`,
		attemptID).Scan(&attemptState, &errorCode, &retryable, &taskStatus); err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if attemptState != "FAILED" || errorCode != ReasonCheckFailed || !retryable {
		t.Fatalf("attempt = %s/%s retryable=%v, want FAILED/%s/true",
			attemptState, errorCode, retryable, ReasonCheckFailed)
	}
	if taskStatus != "FAILED" {
		t.Fatalf("task = %s, want FAILED", taskStatus)
	}
	var failureCode string
	if err := db.QueryRowContext(ctx, `
		SELECT data->>'failureCode' FROM agent_events
		WHERE task_id = $1 AND event_type = 'check.failed'`, taskID).Scan(&failureCode); err != nil {
		t.Fatalf("read check.failed: %v", err)
	}
	if failureCode != "nonzero_exit" {
		t.Fatalf("failureCode = %q, want nonzero_exit", failureCode)
	}
	var artifactCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM artifacts WHERE task_id = $1`, taskID).Scan(&artifactCount); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if artifactCount != 1 {
		t.Fatalf("artifacts = %d, want 1 failed-check log", artifactCount)
	}
}

func TestRunnerCommitsResidualWorktreeChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newChecksTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, worktreeDir := completedSession(t, ctx, db, workRoot, checkManifestPass)
	beforeSHA := git(t, worktreeDir, "rev-parse", "HEAD")

	// The agent left uncommitted work behind; evidence must bind to the SHA
	// that was actually verified, so the runner checkpoints it first.
	if err := os.WriteFile(filepath.Join(worktreeDir, "extra.ts"), []byte("export const extra = 1\n"), 0o644); err != nil {
		t.Fatalf("write residual file: %v", err)
	}
	runner := newTestRunner(t, db, workRoot, nil)
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	headSHA := git(t, worktreeDir, "rev-parse", "HEAD")
	if headSHA == beforeSHA {
		t.Fatal("residual changes were not committed")
	}
	var candidateSHA string
	if err := db.QueryRowContext(ctx,
		`SELECT candidate_sha FROM execution_attempts WHERE id = $1`, attemptID).Scan(&candidateSHA); err != nil {
		t.Fatalf("read candidate_sha: %v", err)
	}
	if candidateSHA != headSHA {
		t.Fatalf("candidate_sha = %q, want checkpoint HEAD %q", candidateSHA, headSHA)
	}
	var commitEvent string
	if err := db.QueryRowContext(ctx, `
		SELECT data::text FROM agent_events
		WHERE task_id = $1 AND event_type = 'git.commit_created'`, taskID).Scan(&commitEvent); err != nil {
		t.Fatalf("read commit event: %v", err)
	}
	if !strings.Contains(commitEvent, `"commitSha": "`+headSHA) ||
		!strings.Contains(commitEvent, `"branchName": "task/`+taskID) {
		t.Fatalf("git.commit_created payload = %s", commitEvent)
	}
}

func TestRunnerRejectsTamperedManifest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newChecksTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, worktreeDir := completedSession(t, ctx, db, workRoot, checkManifestPass)
	// Deni (or anything with worktree write access) edited the manifest after
	// provisioning; the recorded digest must still gate execution.
	if err := os.WriteFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)),
		[]byte(strings.Replace(checkManifestPass, `"required": true`, `"required": false`, 1)), 0o644); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}
	runner := newTestRunner(t, db, workRoot, nil)

	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	var errorCode, taskStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT a.error_code, t.status FROM execution_attempts a, tasks t
		WHERE a.id = $1 AND t.id = a.task_id`, attemptID).Scan(&errorCode, &taskStatus); err != nil {
		t.Fatalf("read failure: %v", err)
	}
	if errorCode != ReasonManifestDigest || taskStatus != "FAILED" {
		t.Fatalf("failure = %s/%s, want %s/FAILED", errorCode, taskStatus, ReasonManifestDigest)
	}
	for _, et := range eventTypes(t, ctx, db, taskID) {
		if strings.HasPrefix(et, "check.") {
			t.Fatalf("no check events must be emitted on tamper, got %s", et)
		}
	}
}

func TestRunnerReclaimsStaleCheckRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newChecksTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, _ := completedSession(t, ctx, db, workRoot, checkManifestPass)

	// A previous runner died mid-verification; its stale claim is reclaimed.
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts
		SET checks_state = 'RUNNING', checks_started_at = now() - interval '2 hours'
		WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("mark stale checks claim: %v", err)
	}
	runner := newTestRunner(t, db, workRoot, nil)
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	var checksState string
	if err := db.QueryRowContext(ctx,
		`SELECT checks_state FROM execution_attempts WHERE id = $1`, attemptID).Scan(&checksState); err != nil {
		t.Fatalf("read checks_state: %v", err)
	}
	if checksState != "PASSED" {
		t.Fatalf("checks_state = %s, want PASSED after reclaim", checksState)
	}
	var audit string
	if err := db.QueryRowContext(ctx, `
		SELECT data::text FROM agent_events
		WHERE task_id = $1 AND event_type = 'audit.action_recorded'`, taskID).Scan(&audit); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if !strings.Contains(audit, ReasonChecksReclaim) {
		t.Fatalf("audit payload = %s, want reclaim", audit)
	}
}
