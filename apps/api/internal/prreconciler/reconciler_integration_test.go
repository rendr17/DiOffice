package prreconciler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/rendr17/dioffice/apps/api/internal/migrations"
	"github.com/rendr17/dioffice/apps/api/internal/prrunner"
)

const (
	headOld = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headNew = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	baseSHA = "f00f00f00f00f00f00f00f00f00f00f00f00f00f"
)

// fakeGH serves scripted GitHub reads keyed by PR number.
type fakeGH struct {
	prs  map[int]*prrunner.PullRequest
	errs map[int]error
	gets int
}

func (f *fakeGH) FindOpenPR(context.Context, string, string, string) (*prrunner.PullRequest, error) {
	return nil, errors.New("unused")
}
func (f *fakeGH) CreatePR(context.Context, string, string, prrunner.CreatePRInput) (*prrunner.PullRequest, error) {
	return nil, errors.New("unused")
}
func (f *fakeGH) MergePR(context.Context, string, string, int, prrunner.MergePRInput) (*prrunner.PullRequest, error) {
	return nil, errors.New("unused")
}
func (f *fakeGH) GetPR(_ context.Context, _, _ string, number int) (*prrunner.PullRequest, error) {
	f.gets++
	if err := f.errs[number]; err != nil {
		return nil, err
	}
	return f.prs[number], nil
}

func newReconTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	baseURL := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	db, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	// Disposable schema per test, mirroring the other integration suites.
	conn, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer conn.Close(ctx)
	schema := "recon_" + strings.ReplaceAll(t.Name(), "/", "_")
	if len(schema) > 50 {
		schema = schema[:50]
	}
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	_, sourceFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// fixture drives a task to a reviewable/done state with a recorded PR.
func reconFixture(t *testing.T, ctx context.Context, db *sql.DB,
	taskStatus, prState, head string) (taskID, prID string) {
	t.Helper()
	insert := func(query string, args ...any) string {
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	organizationID := insert(`INSERT INTO organizations (name) VALUES ('Recon org') RETURNING id::text`)
	ownerID := insert(`INSERT INTO users (organization_id, email, display_name)
		VALUES ($1, 'o@recon.invalid', 'Owner') RETURNING id::text`, organizationID)
	employeeID := insert(`INSERT INTO employees (organization_id, name, slug, role, department)
		VALUES ($1, 'Deni', 'deni', 'Engineer', 'Engineering') RETURNING id::text`, organizationID)
	projectID := insert(`INSERT INTO projects (organization_id, name)
		VALUES ($1, 'Recon project') RETURNING id::text`, organizationID)
	repositoryID := insert(`INSERT INTO repositories (organization_id, project_id, provider, owner, repo_name, default_branch)
		VALUES ($1, $2, 'github', 'acme', 'widgets', 'main') RETURNING id::text`, organizationID, projectID)
	taskID = insert(`INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			task_type, status, created_by_user_id)
		VALUES ($1, $2, $3, 'Build the widget', 'desc', 'feature', $4, $5)
		RETURNING id::text`, organizationID, projectID, employeeID, taskStatus, ownerID)
	prID = insert(`INSERT INTO pull_requests (
			organization_id, project_id, task_id, repository_id, provider,
			external_pr_id, number, url, branch_name, head_sha, base_sha, state)
		VALUES ($1, $2, $3, $4, 'github', 'PR_node42', 42,
			'https://github.com/acme/widgets/pull/42', $5, $6, $7, $8)
		RETURNING id::text`,
		organizationID, projectID, taskID, repositoryID, "task/"+taskID, head, baseSHA, prState)
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET pull_request_id = $2 WHERE id = $1`, taskID, prID); err != nil {
		t.Fatalf("link pull request: %v", err)
	}
	return taskID, prID
}

func newTestReconciler(t *testing.T, db *sql.DB, gh prrunner.GitHubClient) *Reconciler {
	t.Helper()
	r, err := New(db, gh, Config{OpTimeout: time.Minute})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return r
}

func prUpdatedFacts(t *testing.T, ctx context.Context, db *sql.DB, taskID string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT data->>'change' FROM agent_events
		WHERE task_id = $1 AND event_type = 'pull_request.updated'
		ORDER BY stream_sequence`, taskID)
	if err != nil {
		t.Fatalf("read pr facts: %v", err)
	}
	defer rows.Close()
	var changes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		changes = append(changes, c)
	}
	return changes
}

func taskStatusOf(t *testing.T, ctx context.Context, db *sql.DB, taskID string) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&s); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return s
}

func TestReconcilerReopensDoneTaskOnHeadChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newReconTestDatabase(t, ctx)
	taskID, prID := reconFixture(t, ctx, db, "DONE", "OPEN", headOld)

	gh := &fakeGH{prs: map[int]*prrunner.PullRequest{42: {
		Number: 42, NodeID: "PR_node42", URL: "https://github.com/acme/widgets/pull/42",
		State: "OPEN", HeadSHA: headNew, BaseSHA: baseSHA,
	}}}
	r := newTestReconciler(t, db, gh)
	if n, err := r.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce() = %d/%v", n, err)
	}

	var head, state string
	if err := db.QueryRowContext(ctx,
		`SELECT head_sha, state FROM pull_requests WHERE id = $1`, prID).Scan(&head, &state); err != nil {
		t.Fatalf("read pr: %v", err)
	}
	if head != headNew || state != "OPEN" {
		t.Fatalf("pr = %s/%s, want %s/OPEN", head, state, headNew)
	}
	if got := taskStatusOf(t, ctx, db, taskID); got != "IN_REVIEW" {
		t.Fatalf("task = %s, want IN_REVIEW after post-approval head change", got)
	}
	changes := prUpdatedFacts(t, ctx, db, taskID)
	if len(changes) != 1 || changes[0] != "head" {
		t.Fatalf("pr.updated changes = %v, want [head]", changes)
	}
	// The head-change and task transitions share one correlation id.
	var correlations int
	if err := db.QueryRowContext(ctx, `
		SELECT count(DISTINCT correlation_id) FROM agent_events
		WHERE task_id = $1 AND event_type IN ('pull_request.updated', 'task.state_changed')`,
		taskID).Scan(&correlations); err != nil {
		t.Fatalf("count correlations: %v", err)
	}
	if correlations != 1 {
		t.Fatalf("correlations = %d, want 1", correlations)
	}
	// The open PR stays a candidate every pass (that is the reconciler's
	// job), but drift already recorded produces no new facts.
	if n, err := r.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("second RunOnce() = %d/%v", n, err)
	}
	if got := prUpdatedFacts(t, ctx, db, taskID); len(got) != 1 {
		t.Fatalf("pr facts after rescan = %v, want still 1", got)
	}
}

func TestReconcilerUpdatesHeadUnderReviewWithoutTransition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newReconTestDatabase(t, ctx)
	taskID, prID := reconFixture(t, ctx, db, "IN_REVIEW", "OPEN", headOld)

	gh := &fakeGH{prs: map[int]*prrunner.PullRequest{42: {
		Number: 42, NodeID: "PR_node42", URL: "https://github.com/acme/widgets/pull/42",
		State: "OPEN", HeadSHA: headNew, BaseSHA: baseSHA,
	}}}
	r := newTestReconciler(t, db, gh)
	if n, err := r.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce() = %d/%v", n, err)
	}
	var head string
	if err := db.QueryRowContext(ctx,
		`SELECT head_sha FROM pull_requests WHERE id = $1`, prID).Scan(&head); err != nil {
		t.Fatalf("read pr: %v", err)
	}
	if head != headNew {
		t.Fatalf("head = %s, want %s", head, headNew)
	}
	if got := taskStatusOf(t, ctx, db, taskID); got != "IN_REVIEW" {
		t.Fatalf("task = %s, want IN_REVIEW", got)
	}
	if got := prUpdatedFacts(t, ctx, db, taskID); len(got) != 1 || got[0] != "head" {
		t.Fatalf("changes = %v, want [head]", got)
	}
}

func TestReconcilerBlocksReviewOnTerminalPullRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newReconTestDatabase(t, ctx)
	taskID, prID := reconFixture(t, ctx, db, "IN_REVIEW", "OPEN", headOld)

	gh := &fakeGH{prs: map[int]*prrunner.PullRequest{42: {
		Number: 42, NodeID: "PR_node42", URL: "https://github.com/acme/widgets/pull/42",
		State: "MERGED", HeadSHA: headOld, BaseSHA: baseSHA,
	}}}
	r := newTestReconciler(t, db, gh)
	if n, err := r.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce() = %d/%v", n, err)
	}
	var state string
	if err := db.QueryRowContext(ctx,
		`SELECT state FROM pull_requests WHERE id = $1`, prID).Scan(&state); err != nil {
		t.Fatalf("read pr: %v", err)
	}
	if state != "MERGED" {
		t.Fatalf("pr state = %s, want MERGED", state)
	}
	if got := taskStatusOf(t, ctx, db, taskID); got != "BLOCKED" {
		t.Fatalf("task = %s, want BLOCKED", got)
	}
	if got := prUpdatedFacts(t, ctx, db, taskID); len(got) != 1 || got[0] != "state" {
		t.Fatalf("changes = %v, want [state]", got)
	}
}

func TestReconcilerSkipsUndriftedAndUnavailablePullRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newReconTestDatabase(t, ctx)
	taskID, prID := reconFixture(t, ctx, db, "IN_REVIEW", "OPEN", headOld)

	gh := &fakeGH{prs: map[int]*prrunner.PullRequest{42: {
		Number: 42, NodeID: "PR_node42", URL: "https://github.com/acme/widgets/pull/42",
		State: "OPEN", HeadSHA: headOld, BaseSHA: baseSHA,
	}}}
	r := newTestReconciler(t, db, gh)
	if n, err := r.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce() = %d/%v", n, err)
	}
	if got := prUpdatedFacts(t, ctx, db, taskID); len(got) != 0 {
		t.Fatalf("undrifted pr wrote facts %v", got)
	}
	if got := taskStatusOf(t, ctx, db, taskID); got != "IN_REVIEW" {
		t.Fatalf("task = %s, want IN_REVIEW", got)
	}

	// A vanished PR under review blocks the task without mutating the row.
	gh.errs = map[int]error{42: &prrunner.APIError{Code: prrunner.ErrRepoMissing, Message: "not found"}}
	gh.prs = nil
	if n, err := r.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce() = %d/%v", n, err)
	}
	if got := taskStatusOf(t, ctx, db, taskID); got != "BLOCKED" {
		t.Fatalf("task = %s, want BLOCKED on missing PR", got)
	}
	var head, state string
	if err := db.QueryRowContext(ctx,
		`SELECT head_sha, state FROM pull_requests WHERE id = $1`, prID).Scan(&head, &state); err != nil {
		t.Fatalf("read pr: %v", err)
	}
	if head != headOld || state != "OPEN" {
		t.Fatalf("pr row mutated on missing upstream: %s/%s", head, state)
	}
}
