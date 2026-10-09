// Package prreconciler re-polls recorded pull requests against the GitHub
// API and persists drift as canonical facts. A head change emits
// pull_request.updated and moves an approved-but-unmerged task back to
// IN_REVIEW (the recorded approval is bound to the old SHA digest and is
// thereby stale); a terminal PR state under review blocks the task with an
// owner-safe reason. PR state is never inferred from local data — every fact
// comes from the API response.
package prreconciler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
	"github.com/rendr17/dioffice/apps/api/internal/prrunner"
)

// Config tunes the reconciliation loop.
type Config struct {
	PollInterval time.Duration // default 60s
	BatchSize    int           // default 50 pull requests per pass
	OpTimeout    time.Duration // default 30s per GitHub read
}

func (c Config) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return 60 * time.Second
}

func (c Config) batchSize() int {
	if c.BatchSize > 0 {
		return c.BatchSize
	}
	return 50
}

func (c Config) opTimeout() time.Duration {
	if c.OpTimeout > 0 {
		return c.OpTimeout
	}
	return 30 * time.Second
}

// Reconciler polls open recorded pull requests and persists drift facts.
type Reconciler struct {
	db     *sql.DB
	github prrunner.GitHubClient
	cfg    Config
}

// New builds a reconciler against a real or faked GitHub client.
func New(db *sql.DB, github prrunner.GitHubClient, cfg Config) (*Reconciler, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if github == nil {
		return nil, errors.New("github client is required")
	}
	return &Reconciler{db: db, github: github, cfg: cfg}, nil
}

// candidate is the recorded join of pull request, task, and repository.
type candidate struct {
	PRID           string
	OrganizationID string
	ProjectID      string
	TaskID         string
	EmployeeID     string
	Number         int
	HeadSHA        string
	BaseSHA        string
	State          string
	Owner          string
	Repo           string
	TaskStatus     string
}

// Run reconciles until ctx is canceled.
func (r *Reconciler) Run(ctx context.Context) error {
	for {
		if _, err := r.RunOnce(ctx); err != nil {
			slog.Error("pr reconcile pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.cfg.pollInterval()):
		}
	}
}

// RunOnce reconciles one bounded batch of open recorded pull requests and
// returns the number of rows examined.
func (r *Reconciler) RunOnce(ctx context.Context) (int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id::text, p.organization_id::text, p.project_id::text, p.task_id::text,
			COALESCE(t.assignee_employee_id::text, ''),
			p.number, p.head_sha, p.base_sha, p.state,
			r.owner, r.repo_name, t.status
		FROM pull_requests p
		JOIN tasks t
			ON t.organization_id = p.organization_id
			AND t.project_id = p.project_id AND t.id = p.task_id
		JOIN repositories r
			ON r.organization_id = p.organization_id
			AND r.project_id = p.project_id AND r.id = p.repository_id
		WHERE p.state IN ('OPEN', 'DRAFT') AND t.status IN ('IN_REVIEW', 'DONE')
		ORDER BY p.updated_at
		LIMIT $1`, r.cfg.batchSize())
	if err != nil {
		return 0, fmt.Errorf("list pull request candidates: %w", err)
	}
	var batch []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.PRID, &c.OrganizationID, &c.ProjectID, &c.TaskID,
			&c.EmployeeID, &c.Number, &c.HeadSHA, &c.BaseSHA, &c.State,
			&c.Owner, &c.Repo, &c.TaskStatus); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan pull request candidate: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close pull request candidates: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate pull request candidates: %w", err)
	}

	for i := range batch {
		r.reconcile(ctx, &batch[i])
	}
	return len(batch), nil
}

// reconcile fetches the authoritative PR state and persists any drift.
// GitHub read failures are transient: the row is skipped this pass.
func (r *Reconciler) reconcile(ctx context.Context, c *candidate) {
	opCtx, cancel := context.WithTimeout(ctx, r.cfg.opTimeout())
	pr, err := r.github.GetPR(opCtx, c.Owner, c.Repo, c.Number)
	cancel()
	if err != nil {
		var apiErr *prrunner.APIError
		if errors.As(err, &apiErr) && apiErr.Code == prrunner.ErrRepoMissing {
			// The recorded PR no longer exists upstream. A task under review
			// cannot be reviewed — block it with an owner-safe reason; a DONE
			// task's historical approval stays untouched.
			if c.TaskStatus == "IN_REVIEW" {
				if perr := r.persistBlocked(ctx, c, apiErr.Code); perr != nil {
					slog.Error("persist pull request unavailability failed",
						"task", c.TaskID, "error", perr)
				}
			}
			return
		}
		slog.Warn("pull request reconcile fetch failed",
			"task", c.TaskID, "number", c.Number, "error", err)
		return
	}
	if pr == nil {
		return
	}
	if pr.HeadSHA == c.HeadSHA && pr.State == c.State && pr.BaseSHA == c.BaseSHA {
		return
	}
	if err := r.persistDrift(ctx, c, pr); err != nil {
		slog.Error("persist pull request drift failed", "task", c.TaskID, "error", err)
	}
}

// persistDrift records the API-observed head/base/state under the PR row
// lock, emits pull_request.updated, and performs the canonical task fallout:
// DONE → IN_REVIEW on head change (approval bound to the old SHA is stale),
// IN_REVIEW → BLOCKED when the PR is no longer reviewable.
func (r *Reconciler) persistDrift(ctx context.Context, c *candidate, pr *prrunner.PullRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reconcile transaction: %w", err)
	}
	defer tx.Rollback()

	var curHead, curBase, curState, taskStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT p.head_sha, p.base_sha, p.state, t.status
		FROM pull_requests p
		JOIN tasks t
			ON t.organization_id = p.organization_id
			AND t.project_id = p.project_id AND t.id = p.task_id
		WHERE p.organization_id = $1 AND p.id = $2
		FOR UPDATE OF p, t`,
		c.OrganizationID, c.PRID).Scan(&curHead, &curBase, &curState, &taskStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock pull request row: %w", err)
	}
	// Another writer already recorded this drift.
	if curHead == pr.HeadSHA && curState == pr.State && curBase == pr.BaseSHA {
		return nil
	}

	headChanged := curHead != pr.HeadSHA
	baseChanged := curBase != pr.BaseSHA
	change := "state"
	switch {
	case headChanged:
		change = "head"
	case baseChanged:
		change = "base"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE pull_requests
		SET head_sha = $3, base_sha = $4, state = $5,
			merged_at = CASE WHEN $5 = 'MERGED' THEN COALESCE($6, now()) ELSE NULL END,
			updated_at = now()
		WHERE organization_id = $1 AND id = $2`,
		c.OrganizationID, c.PRID, pr.HeadSHA, pr.BaseSHA, pr.State, pr.MergedAt); err != nil {
		return fmt.Errorf("update pull request row: %w", err)
	}

	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return fmt.Errorf("allocate reconcile correlation id: %w", err)
	}
	fact := eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID, TaskID: c.TaskID,
		EmployeeID: c.EmployeeID, Producer: "reconciler",
		Actor: eventstore.SystemActor("prreconciler"), CorrelationID: correlationID,
		DedupeKey: "prreconcile:" + c.PRID + ":" + change + ":" + pr.HeadSHA + ":" + pr.State,
	}
	prEventID, err := eventstore.AppendFact(ctx, tx, withType(fact, "pull_request.updated", map[string]any{
		"number": pr.Number, "change": change,
		"headSha": pr.HeadSHA, "state": pr.State,
	}))
	if err != nil {
		if errors.Is(err, eventstore.ErrDuplicateEvent) {
			return tx.Commit()
		}
		return fmt.Errorf("record pull request update: %w", err)
	}
	fact.CausationID = prEventID
	// The dedupe key above identifies this specific drift fact; the follow-up
	// task transition is guarded by the row's status predicate instead.
	fact.DedupeKey = ""

	terminal := pr.State == "MERGED" || pr.State == "CLOSED"
	switch {
	case headChanged && taskStatus == "DONE":
		// The approved head moved after approval: the recorded approval is
		// bound to the old SHA digest — stale by construction — and the task
		// must be reviewed again before anything else happens to it.
		if err := r.moveTask(ctx, tx, c, fact, taskStatus, "IN_REVIEW",
			"pull request head changed after approval; previous approval is stale"); err != nil {
			return err
		}
	case terminal && taskStatus == "IN_REVIEW":
		// A closed or externally merged PR is not reviewable; retain the
		// evidence and stop the review with an owner-safe reason.
		if err := r.moveTask(ctx, tx, c, fact, taskStatus, "BLOCKED",
			"pull request "+pr.State+" upstream; review cannot proceed"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// persistBlocked blocks an IN_REVIEW task whose recorded PR vanished
// upstream, emitting the audit trail without inventing a PR state.
func (r *Reconciler) persistBlocked(ctx context.Context, c *candidate, code prrunner.ErrorCode) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin unavailable transaction: %w", err)
	}
	defer tx.Rollback()
	var taskStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM tasks
		WHERE organization_id = $1 AND project_id = $2 AND id = $3
		FOR UPDATE`, c.OrganizationID, c.ProjectID, c.TaskID).Scan(&taskStatus); err != nil {
		return fmt.Errorf("lock task row: %w", err)
	}
	if taskStatus != "IN_REVIEW" {
		return nil
	}
	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return fmt.Errorf("allocate unavailable correlation id: %w", err)
	}
	fact := eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID, TaskID: c.TaskID,
		EmployeeID: c.EmployeeID, Producer: "reconciler",
		Actor: eventstore.SystemActor("prreconciler"), CorrelationID: correlationID,
	}
	if err := r.moveTask(ctx, tx, c, fact, taskStatus, "BLOCKED",
		"pull request unavailable upstream ("+string(code)+"); review cannot proceed"); err != nil {
		return err
	}
	return tx.Commit()
}

// moveTask performs a guarded task transition — the task row is already
// locked by the caller (or locked here for the unavailable path) — and emits
// the task.state_changed fact under the shared correlation id.
func (r *Reconciler) moveTask(ctx context.Context, tx *sql.Tx, c *candidate,
	fact eventstore.EventInput, fromState, toState, reason string) error {
	var taskVersion int64
	err := tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = $4, task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND status = $5
		RETURNING task_version`,
		c.OrganizationID, c.ProjectID, c.TaskID, toState, fromState).Scan(&taskVersion)
	if errors.Is(err, sql.ErrNoRows) {
		// The task moved on between the candidate read and this lock — a
		// concurrent Owner command wins; the PR update fact still stands.
		return nil
	}
	if err != nil {
		return fmt.Errorf("move task to %s: %w", toState, err)
	}
	if _, err := eventstore.AppendFact(ctx, tx, withType(fact, "task.state_changed", map[string]any{
		"fromState": fromState, "toState": toState,
		"taskVersion": taskVersion, "reason": reason,
	})); err != nil {
		return fmt.Errorf("record task %s transition: %w", toState, err)
	}
	return nil
}

func withType(input eventstore.EventInput, eventType string, data map[string]any) eventstore.EventInput {
	input.EventType = eventType
	input.Data = data
	return input
}
