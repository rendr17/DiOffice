package runtimeevents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

// fakeSource serves one canned SSE payload per StreamEvents call.
type fakeSource struct {
	payload string
	calls   *atomic.Int32
	lastDir *atomic.Value
}

func (f *fakeSource) StreamEvents(ctx context.Context, directory string) (io.ReadCloser, error) {
	f.calls.Add(1)
	f.lastDir.Store(directory)
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

func sse(frames ...string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return b.String()
}

func newIngesterTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
	schemaName := "dioffice_ing_test_" + hex.EncodeToString(randomBytes)
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
		t.Fatal("could not locate runtimeevents test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

const ingestManifest = `{
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

// runningSession drives a task to PROVISIONING via the real service, then
// advances the rows to the IN_PROGRESS end state (attempt RUNNING, workspace
// IN_USE, session RUNNING with a provider id) — the same state the session
// runner commits — without an actual OpenCode server.
func runningSession(t *testing.T, ctx context.Context, db *sql.DB, workRoot string) (taskID, sessionRowID string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	organizationID := insert(`INSERT INTO organizations (name) VALUES ('Ingest org') RETURNING id::text`)
	ownerID := insert(`INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner@ing.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID := insert(`INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID := insert(`INSERT INTO projects (organization_id, name) VALUES ($1, 'Ingest project') RETURNING id::text`, organizationID)
	insert(`INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch) VALUES ($1, $2, 'github', 'acme', 'widgets', 'main') RETURNING id::text`, organizationID, projectID)

	service := tasks.NewService(db)
	draft, _, err := service.CreateDraft(ctx, tasks.CreateDraftInput{
		OrganizationID: organizationID, ProjectID: projectID, ActorUserID: ownerID,
		AssigneeEmployeeID: employeeID, IdempotencyKey: "ing-draft",
		Title: "Ingest me", Description: "Exercise the event ingester",
		AcceptanceCriteria: json.RawMessage(`["Facts are persisted"]`),
		RequiredChecks:     json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	ready, _, err := service.MarkReady(ctx, tasks.MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "ing-ready", ExpectedVersion: draft.Version,
		ManifestDigest: executionmanifest.Digest([]byte(ingestManifest)),
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	if _, _, err := service.StartExecution(ctx, tasks.StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: "ing-start", ExpectedVersion: ready.Version,
	}); err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}
	var attemptID, workspaceID string
	if err := db.QueryRowContext(ctx, `
		SELECT a.id::text, w.id::text FROM execution_attempts a, workspaces w
		WHERE a.task_id = $1 AND w.task_id = $1`, ready.ID).Scan(&attemptID, &workspaceID); err != nil {
		t.Fatalf("read attempt/workspace: %v", err)
	}
	worktreeRef := "worktrees/" + ready.ID
	if err := os.MkdirAll(filepath.Join(workRoot, filepath.FromSlash(worktreeRef)), 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE workspaces SET state = 'IN_USE', worktree_ref = $2 WHERE id = $1`,
		workspaceID, worktreeRef); err != nil {
		t.Fatalf("mark workspace in use: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE execution_attempts SET state = 'RUNNING', runtime_session_id = 'rt-live'
		WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("mark attempt running: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET status = 'IN_PROGRESS' WHERE id = $1`, ready.ID); err != nil {
		t.Fatalf("mark task in progress: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, runtime_session_id, status, started_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'opencode', 'rt-live', 'RUNNING', now())
		RETURNING id::text`,
		organizationID, projectID, ready.ID, attemptID, employeeID, workspaceID).
		Scan(&sessionRowID); err != nil {
		t.Fatalf("insert running session: %v", err)
	}
	return ready.ID, sessionRowID
}

func waitFor(t *testing.T, ctx context.Context, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context done waiting for %s", what)
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func countFacts(t *testing.T, ctx context.Context, db *sql.DB, taskID string) map[string]int {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT event_type, count(*) FROM agent_events WHERE task_id = $1
		GROUP BY event_type`, taskID)
	if err != nil {
		t.Fatalf("count facts: %v", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var et string
		var n int
		if err := rows.Scan(&et, &n); err != nil {
			t.Fatalf("scan count: %v", err)
		}
		counts[et] = n
	}
	return counts
}

func TestIngesterPersistsFactsAndCompletesSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newIngesterTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, _ := runningSession(t, ctx, db, workRoot)

	calls := &atomic.Int32{}
	lastDir := &atomic.Value{}
	src := &fakeSource{calls: calls, lastDir: lastDir, payload: sse(
		`{"type":"message.part.updated","properties":{"part":{"id":"p1","sessionID":"rt-live","type":"text","text":"Panel implemented.","time":{"start":1,"end":2}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"t1","sessionID":"rt-live","type":"tool","tool":"bash","state":{"status":"running","input":{"command":"pnpm test"}}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"t1","sessionID":"rt-live","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"pnpm test"},"metadata":{"exit":0},"time":{"start":10,"end":11}}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"f1","sessionID":"rt-live","type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"src/Panel.tsx"}}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"other","sessionID":"rt-other","type":"text","text":"different session","time":{"start":1,"end":2}}}}`,
		`{"type":"session.idle","properties":{"sessionID":"rt-live"}}`,
	)}
	ingester, err := New(db, Config{WorkRoot: workRoot, ReconnectDelay: time.Hour}, src)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := ingester.reconcile(ctx); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	var sessionStatus string
	waitFor(t, ctx, "session completion", func() bool {
		return db.QueryRowContext(ctx,
			`SELECT status FROM agent_sessions WHERE task_id = $1`, taskID).
			Scan(&sessionStatus) == nil && sessionStatus == "COMPLETED"
	})

	counts := countFacts(t, ctx, db, taskID)
	for et, want := range map[string]int{
		"agent.message":     1,
		"command.started":   1,
		"command.completed": 1,
		"file.activity":     1,
		"session.completed": 1,
	} {
		if counts[et] != want {
			var keys string
			_ = db.QueryRowContext(ctx, `
				SELECT string_agg(COALESCE(dedupe_key, '-'), ', ')
				FROM agent_events WHERE task_id = $1`, taskID).Scan(&keys)
			t.Fatalf("%s count = %d, want %d (all: %v, dedupe keys: %s)", et, counts[et], want, counts, keys)
		}
	}
	if dir, _ := lastDir.Load().(string); dir != filepath.Join(workRoot, "worktrees", taskID) {
		t.Fatalf("stream directory = %q", dir)
	}
	// Terminal fact ended the subscription; no reconnect happens.
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("stream calls = %d, want 1 (no reconnect after terminal)", calls.Load())
	}
}

func TestIngesterFailsSessionOnProviderError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newIngesterTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, _ := runningSession(t, ctx, db, workRoot)

	calls := &atomic.Int32{}
	src := &fakeSource{calls: calls, lastDir: &atomic.Value{}, payload: sse(
		`{"type":"session.error","properties":{"sessionID":"rt-live","error":{"message":"provider died"}}}`,
	)}
	ingester, err := New(db, Config{WorkRoot: workRoot, ReconnectDelay: time.Hour}, src)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := ingester.reconcile(ctx); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	var sessionStatus, attemptState, taskStatus string
	waitFor(t, ctx, "failure propagation", func() bool {
		err := db.QueryRowContext(ctx, `
			SELECT s.status, a.state, t.status
			FROM agent_sessions s, execution_attempts a, tasks t
			WHERE s.task_id = $1 AND a.task_id = $1 AND t.id = $1`, taskID).
			Scan(&sessionStatus, &attemptState, &taskStatus)
		return err == nil && sessionStatus == "FAILED" &&
			attemptState == "FAILED" && taskStatus == "FAILED"
	})
	counts := countFacts(t, ctx, db, taskID)
	if counts["session.failed"] != 1 ||
		counts["execution_attempt.state_changed"] != 2 /* Start + this failure */ {
		t.Fatalf("counts = %v", counts)
	}
}

func TestIngesterDedupesReplayedFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newIngesterTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, sessionRowID := runningSession(t, ctx, db, workRoot)

	ingester, err := New(db, Config{WorkRoot: workRoot}, &fakeSource{calls: &atomic.Int32{}, lastDir: &atomic.Value{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sess := liveSession{SessionRowID: sessionRowID}
	if err := db.QueryRowContext(ctx, `
		SELECT organization_id::text, project_id::text, task_id::text,
			employee_id::text, attempt_id::text, workspace_id::text
		FROM agent_sessions WHERE id = $1`, sessionRowID).Scan(
		&sess.OrganizationID, &sess.ProjectID, &sess.TaskID,
		&sess.EmployeeID, &sess.AttemptID, &sess.WorkspaceID); err != nil {
		t.Fatalf("load session: %v", err)
	}
	fact := Fact{
		EventType: "agent.message",
		Data:      map[string]any{"kind": "progress", "summary": "hello"},
		DedupeKey: "part:p1:text",
	}
	for i := 0; i < 3; i++ {
		terminal, err := ingester.persist(ctx, sess, fact)
		if err != nil || terminal {
			t.Fatalf("persist #%d = terminal:%v err:%v", i, terminal, err)
		}
	}
	if n := countFacts(t, ctx, db, taskID)["agent.message"]; n != 1 {
		t.Fatalf("agent.message count = %d, want 1 after replays", n)
	}
}
