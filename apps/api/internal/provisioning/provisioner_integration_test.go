package provisioning

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
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
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

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is required for provisioning tests")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, output)
	}
	return string(output)
}

// newRemoteRepository builds a bare git remote fixture containing the given
// files on branch main and returns its local path.
func newRemoteRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	git(t, source, "init", "-b", "main")
	// Keep fixture bytes byte-identical so the recorded manifest digest
	// matches exactly what a clone checks out on any platform.
	git(t, source, "config", "core.autocrlf", "false")
	git(t, source, "config", "core.eol", "lf")
	for name, content := range files {
		full := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture file: %v", err)
		}
	}
	git(t, source, "add", "-A")
	git(t, source, "-c", "user.name=DiOffice Test", "-c", "user.email=test@dioffice.invalid",
		"commit", "-m", "fixture commit")
	remote := filepath.Join(root, "remote.git")
	git(t, root, "clone", "--bare", source, remote)
	return remote
}

func newProvisioningTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
	schemaName := "dioffice_prov_test_" + hex.EncodeToString(randomBytes)
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
		t.Fatal("could not locate provisioning test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

func provisioningFixtures(t *testing.T, ctx context.Context, db *sql.DB) (organizationID, projectID, ownerID, employeeID string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	organizationID = insert(`INSERT INTO organizations (name) VALUES ('Provisioning org') RETURNING id::text`)
	ownerID = insert(`INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'owner@prov.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID = insert(`INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID = insert(`INSERT INTO projects (organization_id, name) VALUES ($1, 'Provisioning project') RETURNING id::text`, organizationID)
	insert(`INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch) VALUES ($1, $2, 'github', 'acme', 'widgets', 'main') RETURNING id::text`, organizationID, projectID)
	return organizationID, projectID, ownerID, employeeID
}

// startProvisionedTask drives a task through DRAFT→READY→PROVISIONING so the
// provisioner has a committed attempt + workspace claim to pick up.
func startProvisionedTask(t *testing.T, ctx context.Context, db *sql.DB, manifestDigest string) (taskID, attemptID string) {
	t.Helper()
	organizationID, projectID, ownerID, employeeID := provisioningFixtures(t, ctx, db)
	service := tasks.NewService(db)
	draft, _, err := service.CreateDraft(ctx, tasks.CreateDraftInput{
		OrganizationID: organizationID, ProjectID: projectID, ActorUserID: ownerID,
		AssigneeEmployeeID: employeeID, IdempotencyKey: "prov-draft",
		Title: "Provision me", Description: "Exercise the workspace provisioner",
		AcceptanceCriteria: json.RawMessage(`["Workspace becomes READY"]`),
		RequiredChecks:     json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	ready, _, err := service.MarkReady(ctx, tasks.MarkReadyInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: draft.ID,
		ActorUserID: ownerID, IdempotencyKey: "prov-ready", ExpectedVersion: draft.Version,
		ManifestDigest: manifestDigest,
	})
	if err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	if _, _, err := service.StartExecution(ctx, tasks.StartExecutionInput{
		OrganizationID: organizationID, ProjectID: projectID, TaskID: ready.ID,
		ActorUserID: ownerID, IdempotencyKey: "prov-start", ExpectedVersion: ready.Version,
	}); err != nil {
		t.Fatalf("StartExecution() error = %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM execution_attempts WHERE task_id = $1`, ready.ID).Scan(&attemptID); err != nil {
		t.Fatalf("read attempt id: %v", err)
	}
	return ready.ID, attemptID
}

func newTestProvisioner(t *testing.T, db *sql.DB, workRoot, remotePath string) *Provisioner {
	t.Helper()
	provisioner, err := New(db, Config{
		WorkRoot:  workRoot,
		RemoteURL: func(owner, name string) string { return remotePath },
		OpTimeout: time.Minute,
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provisioner
}

func TestProvisionerMaterializesWorktreeAndMarksWorkspaceReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	remote := newRemoteRepository(t, map[string]string{
		executionmanifest.Path: testManifest,
		"README.md":            "fixture\n",
	})
	db := newProvisioningTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID := startProvisionedTask(t, ctx, db, executionmanifest.Digest([]byte(testManifest)))
	provisioner := newTestProvisioner(t, db, workRoot, remote)

	processed, err := provisioner.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("RunOnce() processed = %d, want 1", processed)
	}

	var workspaceState, worktreeRef string
	if err := db.QueryRowContext(ctx, `
		SELECT state, worktree_ref FROM workspaces WHERE task_id = $1`, taskID).
		Scan(&workspaceState, &worktreeRef); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if workspaceState != "READY" || worktreeRef != "worktrees/"+taskID {
		t.Fatalf("workspace = %s ref %q, want READY worktrees/<task>", workspaceState, worktreeRef)
	}
	worktreeDir := filepath.Join(workRoot, filepath.FromSlash(worktreeRef))
	manifestPath := filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path))
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("worktree is missing the manifest: %v", err)
	}
	if head := strings.TrimSpace(git(t, worktreeDir, "rev-parse", "HEAD")); len(head) != 40 {
		t.Fatalf("worktree HEAD = %q, want a commit SHA", head)
	}
	if branch := strings.TrimSpace(git(t, worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")); branch != "task/"+taskID {
		t.Fatalf("worktree branch = %q, want task/<id>", branch)
	}

	var attemptState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM execution_attempts WHERE id = $1`, attemptID).
		Scan(&attemptState); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attemptState != "PROVISIONING" {
		t.Fatalf("attempt state = %s, want PROVISIONING until a runtime session starts", attemptState)
	}
	var taskStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if taskStatus != "PROVISIONING" {
		t.Fatalf("task status = %s, want PROVISIONING until session start", taskStatus)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT event_type, data::text FROM agent_events WHERE task_id = $1
		AND event_type IN ('execution_attempt.state_changed', 'workspace.state_changed')
		ORDER BY stream_sequence`, taskID)
	if err != nil {
		t.Fatalf("read execution events: %v", err)
	}
	defer rows.Close()
	type row struct{ eventType, data string }
	var events []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.eventType, &r.data); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		events = append(events, r)
	}
	if len(events) != 4 ||
		!strings.Contains(events[2].data, `"toState": "PROVISIONING"`) ||
		!strings.Contains(events[3].data, `"toState": "READY"`) {
		t.Fatalf("provisioner events = %+v, want Start facts + claim + workspace READY", events)
	}

	// A second pass must find nothing left to claim.
	if again, err := provisioner.RunOnce(ctx); err != nil || again != 0 {
		t.Fatalf("second RunOnce() = %d/%v, want 0 claims", again, err)
	}
}

func TestProvisionerBlocksTaskWhenManifestMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	remote := newRemoteRepository(t, map[string]string{"README.md": "no manifest here\n"})
	db := newProvisioningTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, _ := startProvisionedTask(t, ctx, db, executionmanifest.Digest([]byte(testManifest)))
	provisioner := newTestProvisioner(t, db, workRoot, remote)

	if _, err := provisioner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	var taskStatus, attemptState, attemptError, workspaceState string
	if err := db.QueryRowContext(ctx, `
		SELECT t.status, a.state, COALESCE(a.error_code, ''), w.state
		FROM tasks t, execution_attempts a, workspaces w
		WHERE t.id = $1 AND a.task_id = t.id AND w.task_id = t.id`, taskID).
		Scan(&taskStatus, &attemptState, &attemptError, &workspaceState); err != nil {
		t.Fatalf("read failure state: %v", err)
	}
	if taskStatus != "BLOCKED" || attemptState != "FAILED" || attemptError != ReasonManifestMissing || workspaceState != "FAILED" {
		t.Fatalf("failure states = task %s / attempt %s (%s) / workspace %s, want BLOCKED/FAILED manifest_missing/FAILED",
			taskStatus, attemptState, attemptError, workspaceState)
	}
}

func TestProvisionerRejectsManifestDigestMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	remote := newRemoteRepository(t, map[string]string{
		executionmanifest.Path: testManifest,
	})
	db := newProvisioningTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, _ := startProvisionedTask(t, ctx, db, strings.Repeat("00", 32))
	provisioner := newTestProvisioner(t, db, workRoot, remote)

	if _, err := provisioner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	var taskStatus, attemptError string
	if err := db.QueryRowContext(ctx, `
		SELECT t.status, COALESCE(a.error_code, '')
		FROM tasks t, execution_attempts a WHERE t.id = $1 AND a.task_id = t.id`, taskID).
		Scan(&taskStatus, &attemptError); err != nil {
		t.Fatalf("read failure state: %v", err)
	}
	if taskStatus != "BLOCKED" || attemptError != ReasonManifestDigest {
		t.Fatalf("failure states = %s/%s, want BLOCKED with %s", taskStatus, attemptError, ReasonManifestDigest)
	}
}
