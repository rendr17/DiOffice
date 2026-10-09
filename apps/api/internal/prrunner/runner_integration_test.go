package prrunner

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

const testCandidateSHA = "0123456789abcdef0123456789abcdef01234567"

// scriptedGit satisfies provisioning.Git with canned results per subcommand.
type scriptedGit struct {
	calls []string
	err   func(args []string) error
}

func (g *scriptedGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	g.calls = append(g.calls, strings.Join(args, " "))
	if g.err != nil {
		if err := g.err(args); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (g *scriptedGit) joined() string { return strings.Join(g.calls, "\n") }

// fakeGitHub satisfies GitHubClient for the runner tests.
type fakeGitHub struct {
	openPR      *PullRequest
	created     *PullRequest
	byNumber    map[int]*PullRequest
	findErr     error
	createErr   error
	mergeErr    error
	mergeResult *PullRequest
	findCalls   int
	creates     []CreatePRInput
	merges      []mergeCall
}

func (f *fakeGitHub) FindOpenPR(_ context.Context, _, _, _ string) (*PullRequest, error) {
	f.findCalls++
	return f.openPR, f.findErr
}

func (f *fakeGitHub) CreatePR(_ context.Context, _, _ string, in CreatePRInput) (*PullRequest, error) {
	f.creates = append(f.creates, in)
	return f.created, f.createErr
}

func (f *fakeGitHub) GetPR(_ context.Context, _, _ string, number int) (*PullRequest, error) {
	if f.byNumber != nil {
		return f.byNumber[number], nil
	}
	return f.openPR, f.findErr
}

func (f *fakeGitHub) MergePR(_ context.Context, _, _ string, number int,
	in MergePRInput) (*PullRequest, error) {
	f.merges = append(f.merges, mergeCall{number: number, in: in})
	return f.mergeResult, f.mergeErr
}

type mergeCall struct {
	number int
	in     MergePRInput
}

func newPRTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
	schemaName := "dioffice_pr_test_" + hex.EncodeToString(randomBytes)
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
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated test schema: %v", err)
	}
	_, sourceFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

// prFixture drives a task to its publishable state directly: attempt
// SUCCEEDED with a recorded candidate_sha, COMPLETED session, IN_USE
// workspace, IN_PROGRESS task, and a repository row.
func prFixture(t *testing.T, ctx context.Context, db *sql.DB, workRoot string) (taskID, attemptID, worktreeDir string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	organizationID := insert(`INSERT INTO organizations (name) VALUES ('PR org') RETURNING id::text`)
	ownerID := insert(`INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'o@pr.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID := insert(`INSERT INTO employees (organization_id, name, slug, role, department) VALUES ($1, 'Deni', 'deni', 'Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID := insert(`INSERT INTO projects (organization_id, name) VALUES ($1, 'PR project') RETURNING id::text`, organizationID)
	insert(`INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch)
		VALUES ($1, $2, 'github', 'acme', 'widgets', 'main') RETURNING id::text`, organizationID, projectID)
	taskID = insert(`INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			task_type, status, created_by_user_id)
		VALUES ($1, $2, $3, 'Build the widget', 'desc', 'feature', 'IN_PROGRESS', $4)
		RETURNING id::text`, organizationID, projectID, employeeID, ownerID)

	worktreeRef := "worktrees/" + taskID
	worktreeDir = filepath.Join(workRoot, filepath.FromSlash(worktreeRef))
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("create worktree fixture: %v", err)
	}
	workspaceID := insert(`INSERT INTO workspaces (
			organization_id, project_id, task_id, branch_name, worktree_ref,
			worker_profile, state)
		VALUES ($1, $2, $3, $4, $5, 'node-22-pnpm-10-playwright', 'IN_USE')
		RETURNING id::text`, organizationID, projectID, taskID, "task/"+taskID, worktreeRef)
	attemptID = insert(`INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id, attempt_number,
			state, runtime_type, candidate_sha, started_at, ended_at)
		VALUES ($1, $2, $3, $4, 1, 'SUCCEEDED', 'opencode', $5, now() - interval '5 minutes', now())
		RETURNING id::text`, organizationID, projectID, taskID, employeeID, testCandidateSHA)
	insert(`INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, status, runtime_session_id, started_at, ended_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'opencode', 'COMPLETED', 'rt-done',
			now() - interval '4 minutes', now()) RETURNING id::text`,
		organizationID, projectID, taskID, attemptID, employeeID, workspaceID)
	return taskID, attemptID, worktreeDir
}

func newTestRunner(t *testing.T, db *sql.DB, workRoot string, git *scriptedGit, gh *fakeGitHub) *Runner {
	t.Helper()
	runner, err := New(db, Config{
		WorkRoot: workRoot, Token: "test-token-fixture",
		RetryBackoff: 0, OpTimeout: time.Minute, MaxFailures: 3,
	}, git, gh)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runner
}

func taskStatus(t *testing.T, ctx context.Context, db *sql.DB, taskID string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status
}

func eventTypesFor(t *testing.T, ctx context.Context, db *sql.DB, taskID string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT event_type FROM agent_events WHERE task_id = $1 ORDER BY stream_sequence`, taskID)
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

func TestPublishCreatesPRAndMovesToReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newPRTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, _ := prFixture(t, ctx, db, workRoot)
	git := &scriptedGit{}
	gh := &fakeGitHub{created: &PullRequest{
		NodeID: "PR_node1", Number: 42, URL: "https://github.com/acme/widgets/pull/42",
		State: "OPEN", HeadSHA: testCandidateSHA, BaseSHA: "f00f00f00f00f00f00f00f00f00f00f00f00f00f",
	}}
	runner := newTestRunner(t, db, workRoot, git, gh)

	processed, err := runner.RunOnce(ctx)
	if err != nil || processed != 1 {
		t.Fatalf("RunOnce() = %d, %v; want 1 claim", processed, err)
	}
	if len(gh.creates) != 1 || gh.creates[0].Head != "task/"+taskID || gh.creates[0].Base != "main" {
		t.Fatalf("CreatePR inputs = %+v", gh.creates)
	}
	if !strings.Contains(git.joined(), "push https://github.com/acme/widgets.git "+
		testCandidateSHA+":refs/heads/task/"+taskID) {
		t.Fatalf("git calls missing SHA-bound push:\n%s", git.joined())
	}
	if !strings.Contains(git.joined(), "http.extraheader=AUTHORIZATION: bearer test-token-fixture") {
		t.Fatalf("push did not use header auth:\n%s", git.joined())
	}

	var headSHA, prState, linkedPR string
	if err := db.QueryRowContext(ctx, `
		SELECT p.head_sha, a.pr_state, t.pull_request_id::text
		FROM tasks t
		JOIN pull_requests p ON p.id = t.pull_request_id
		JOIN execution_attempts a ON a.id = $2
		WHERE t.id = $1`, taskID, attemptID).Scan(&headSHA, &prState, &linkedPR); err != nil {
		t.Fatalf("read publication result: %v", err)
	}
	if headSHA != testCandidateSHA || prState != "PUBLISHED" || linkedPR == "" {
		t.Fatalf("head=%s pr_state=%s pr=%s", headSHA, prState, linkedPR)
	}
	if status := taskStatus(t, ctx, db, taskID); status != "IN_REVIEW" {
		t.Fatalf("task status = %s, want IN_REVIEW", status)
	}
	want := []string{"audit.action_recorded", "git.push_completed", "pull_request.created", "task.state_changed"}
	got := eventTypesFor(t, ctx, db, taskID)
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
	if processed, err := runner.RunOnce(ctx); err != nil || processed != 0 {
		t.Fatalf("second RunOnce() = %d, %v; want no claim", processed, err)
	}
}

func TestPublishAdoptsAndUpdatesExistingPR(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newPRTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, _ := prFixture(t, ctx, db, workRoot)
	git := &scriptedGit{}
	oldSHA := "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeee"
	gh := &fakeGitHub{openPR: &PullRequest{
		NodeID: "PR_node7", Number: 7, URL: "https://github.com/acme/widgets/pull/7",
		State: "OPEN", HeadSHA: testCandidateSHA, BaseSHA: "f00f00f00f00f00f00f00f00f00f00f00f00f00f",
	}}
	// A prior attempt recorded a stale PR row; the publisher must update it,
	// not insert a second row (one PR per task).
	if _, err := db.ExecContext(ctx, `
		INSERT INTO pull_requests (
			organization_id, project_id, task_id, repository_id, provider,
			external_pr_id, number, url, branch_name, head_sha, base_sha, state)
		SELECT a.organization_id, a.project_id, a.task_id, r.id, 'github',
			'PR_old', 7, 'https://github.com/acme/widgets/pull/7', 'task/old',
			$2, 'f00f00f00f00f00f00f00f00f00f00f00f00f00f', 'OPEN'
		FROM execution_attempts a
		JOIN repositories r
			ON r.organization_id = a.organization_id AND r.project_id = a.project_id
		WHERE a.id = $1`, attemptID, oldSHA); err != nil {
		t.Fatalf("insert stale pull request: %v", err)
	}
	runner := newTestRunner(t, db, workRoot, git, gh)

	if processed, err := runner.RunOnce(ctx); err != nil || processed != 1 {
		t.Fatalf("RunOnce() = %d, %v; want 1 claim", processed, err)
	}
	if len(gh.creates) != 0 {
		t.Fatalf("CreatePR called %d times; existing PR should be adopted", len(gh.creates))
	}
	var count int
	var headSHA string
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), max(head_sha) FROM pull_requests WHERE task_id = $1`,
		taskID).Scan(&count, &headSHA); err != nil {
		t.Fatalf("count pull requests: %v", err)
	}
	if count != 1 || headSHA != testCandidateSHA {
		t.Fatalf("pull_requests rows=%d head=%s; want 1 row at candidate", count, headSHA)
	}
	if status := taskStatus(t, ctx, db, taskID); status != "IN_REVIEW" {
		t.Fatalf("task status = %s, want IN_REVIEW", status)
	}
	found := false
	for _, et := range eventTypesFor(t, ctx, db, taskID) {
		if et == "pull_request.updated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing pull_request.updated; events = %v", eventTypesFor(t, ctx, db, taskID))
	}
}

func TestPublishPushFailureRetriesThenBlocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newPRTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, _ := prFixture(t, ctx, db, workRoot)
	git := &scriptedGit{err: func(args []string) error {
		if len(args) > 0 && args[0] == "cat-file" {
			return nil
		}
		for _, a := range args {
			if a == "push" {
				return errors.New("git push: exit status 128: remote: Invalid username or password")
			}
		}
		return nil
	}}
	gh := &fakeGitHub{}
	runner := newTestRunner(t, db, workRoot, git, gh)

	// RetryBackoff=0 makes every FAILED claim immediately retryable inside a
	// single pass; the runner must stop at MaxFailures and block the task.
	processed, err := runner.RunOnce(ctx)
	if err != nil || processed != 3 {
		t.Fatalf("RunOnce() = %d, %v; want 3 claims (max failures)", processed, err)
	}
	var prState, lastError string
	var failures int
	if err := db.QueryRowContext(ctx, `
		SELECT pr_state, pr_failure_count, pr_last_error FROM execution_attempts WHERE id = $1`,
		attemptID).Scan(&prState, &failures, &lastError); err != nil {
		t.Fatalf("read attempt pr state: %v", err)
	}
	if prState != "FAILED" || failures != 3 || lastError != ReasonPushFailed {
		t.Fatalf("pr_state=%s failures=%d err=%s", prState, failures, lastError)
	}
	if status := taskStatus(t, ctx, db, taskID); status != "BLOCKED" {
		t.Fatalf("task status = %s, want BLOCKED after MaxFailures", status)
	}
	var pushFailures int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_events WHERE task_id = $1 AND event_type = 'git.push_failed'`,
		taskID).Scan(&pushFailures); err != nil {
		t.Fatalf("count push_failed events: %v", err)
	}
	if pushFailures != 3 {
		t.Fatalf("git.push_failed events = %d, want 3", pushFailures)
	}
	// A blocked task is no longer claimable.
	if processed, err := runner.RunOnce(ctx); err != nil || processed != 0 {
		t.Fatalf("post-block RunOnce() = %d, %v; want no claim", processed, err)
	}
}

func TestPublishHeadMismatchFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newPRTestDatabase(t, ctx)
	workRoot := t.TempDir()
	taskID, attemptID, _ := prFixture(t, ctx, db, workRoot)
	git := &scriptedGit{}
	gh := &fakeGitHub{openPR: &PullRequest{
		NodeID: "PR_node9", Number: 9, URL: "https://github.com/acme/widgets/pull/9",
		State: "OPEN", HeadSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		BaseSHA: "f00f00f00f00f00f00f00f00f00f00f00f00f00f",
	}}
	runner := newTestRunner(t, db, workRoot, git, gh)

	// The mismatch is deterministic, so all retries exhaust and the task
	// blocks — publication can never bind a PR at the wrong SHA.
	processed, err := runner.RunOnce(ctx)
	if err != nil || processed != 3 {
		t.Fatalf("RunOnce() = %d, %v; want 3 claims (max failures)", processed, err)
	}
	var prState, lastError string
	if err := db.QueryRowContext(ctx, `
		SELECT pr_state, pr_last_error FROM execution_attempts WHERE id = $1`,
		attemptID).Scan(&prState, &lastError); err != nil {
		t.Fatalf("read attempt pr state: %v", err)
	}
	if prState != "FAILED" || lastError != ReasonHeadMismatch {
		t.Fatalf("pr_state=%s err=%s; want FAILED/%s", prState, lastError, ReasonHeadMismatch)
	}
	if status := taskStatus(t, ctx, db, taskID); status != "BLOCKED" {
		t.Fatalf("task status = %s; exhausted publication retries must block", status)
	}
	var prCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pull_requests WHERE task_id = $1`, taskID).Scan(&prCount); err != nil {
		t.Fatalf("count pull requests: %v", err)
	}
	if prCount != 0 {
		t.Fatalf("pull_requests rows = %d; mismatched head must never be recorded", prCount)
	}
}

func TestPublishCandidateMissingFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newPRTestDatabase(t, ctx)
	workRoot := t.TempDir()
	_, attemptID, _ := prFixture(t, ctx, db, workRoot)
	git := &scriptedGit{err: func(args []string) error {
		if len(args) > 0 && args[0] == "cat-file" {
			return errors.New("git cat-file: fatal: Not a valid object name")
		}
		return nil
	}}
	gh := &fakeGitHub{}
	runner := newTestRunner(t, db, workRoot, git, gh)

	if processed, err := runner.RunOnce(ctx); err != nil || processed != 3 {
		t.Fatalf("RunOnce() = %d, %v; want 3 claims (max failures)", processed, err)
	}
	if gh.findCalls != 0 || len(gh.creates) != 0 {
		t.Fatalf("github contacted despite missing candidate (find=%d create=%d)", gh.findCalls, len(gh.creates))
	}
	var lastError string
	if err := db.QueryRowContext(ctx, `
		SELECT pr_last_error FROM execution_attempts WHERE id = $1`,
		attemptID).Scan(&lastError); err != nil {
		t.Fatalf("read attempt error: %v", err)
	}
	if lastError != ReasonCandidateMissing {
		t.Fatalf("pr_last_error = %s, want %s", lastError, ReasonCandidateMissing)
	}
}
