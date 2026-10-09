// Package prrunner publishes verified work: once an attempt is SUCCEEDED
// with a recorded candidate_sha, the runner pushes that exact SHA to the
// task branch on GitHub, opens or adopts the pull request pointing at it,
// persists the pull_requests row, and moves the task IN_PROGRESS →
// IN_REVIEW. The PR's head/base SHAs come from the GitHub API response —
// never inferred locally — so the durable binding reflects what GitHub
// actually serves. Publication retries are bounded; persistent failure
// moves the task to BLOCKED for Owner attention.
package prrunner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
	"github.com/rendr17/dioffice/apps/api/internal/provisioning"
)

// Reason codes surfaced in pr_last_error, audit outcomes, and task
// transition reasons. All are owner-safe; raw API bodies never persist.
const (
	ReasonPublishStarted   = "pr_publish_started"
	ReasonPublishReclaim   = "pr_publish_reclaim"
	ReasonCandidateMissing = "candidate_commit_missing"
	ReasonPushFailed       = "git_push_failed"
	ReasonHeadMismatch     = "pr_head_mismatch"
	ReasonGitHubAPI        = "github_api_error"
	ReasonPublishBlocked   = "pr_publish_failed"
	ReasonInternal         = "internal_error"
)

var (
	safeIDPattern   = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)
	safeSHAPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	safeBranchRe    = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,250}$`)
	safeRepoNameRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	safeOwnerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
)

// Config controls polling, git, and the GitHub credential.
type Config struct {
	WorkRoot        string
	GitBin          string
	PollInterval    time.Duration
	BatchSize       int
	StaleClaimAfter time.Duration
	// RetryBackoff paces retries of FAILED publications.
	RetryBackoff time.Duration
	OpTimeout    time.Duration
	// MaxFailures bounds publication attempts before the task is BLOCKED.
	MaxFailures int
	// Token authenticates both the git push and the GitHub API. It is passed
	// via http.extraheader — never embedded in remote URLs — and redacted
	// from every error string.
	Token string
}

// Runner claims publishable attempts and drives them to a recorded PR.
type Runner struct {
	db     *sql.DB
	cfg    Config
	git    provisioning.Git
	github GitHubClient
}

func New(db *sql.DB, cfg Config, git provisioning.Git, gh GitHubClient) (*Runner, error) {
	if db == nil {
		return nil, errors.New("pr runner requires a database")
	}
	if gh == nil {
		return nil, errors.New("pr runner requires a GitHub client")
	}
	if cfg.WorkRoot == "" || !filepath.IsAbs(cfg.WorkRoot) {
		return nil, errors.New("pr runner requires an absolute WorkRoot")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("pr runner requires a GitHub token (GITHUB_TOKEN)")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 4
	}
	if cfg.StaleClaimAfter <= 0 {
		cfg.StaleClaimAfter = 10 * time.Minute
	}
	if cfg.RetryBackoff < 0 {
		cfg.RetryBackoff = 0
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = 10 * time.Minute
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 6
	}
	if git == nil {
		git = provisioning.ExecGit{Bin: cfg.GitBin}
	}
	return &Runner{db: db, cfg: cfg, git: git, github: gh}, nil
}

// Run polls until ctx is canceled.
func (r *Runner) Run(ctx context.Context) error {
	for {
		processed, err := r.RunOnce(ctx)
		if err != nil {
			slog.Error("pr runner pass failed", "error", err)
		}
		if processed == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.cfg.PollInterval):
			}
		}
	}
}

// RunOnce claims and publishes up to BatchSize attempts.
func (r *Runner) RunOnce(ctx context.Context) (int, error) {
	processed := 0
	for processed < r.cfg.BatchSize {
		claim, err := r.claim(ctx)
		if err != nil {
			return processed, err
		}
		if claim == nil {
			return processed, nil
		}
		processed++
		r.process(ctx, claim)
	}
	return processed, nil
}

// claimablePublish is one durable unit of publication work.
type claimablePublish struct {
	AttemptID      string
	AttemptNumber  int64
	OrganizationID string
	ProjectID      string
	TaskID         string
	EmployeeID     string
	SessionRowID   string
	WorkspaceID    string
	WorktreeRef    string
	BranchName     string
	CandidateSHA   string
	RepositoryID   string
	RepoOwner      string
	RepoName       string
	DefaultBranch  string
	TaskTitle      string
	FailureCount   int
	Reclaim        bool
}

// claim atomically marks one publishable attempt's pr_state RUNNING: the
// attempt must be SUCCEEDED with a recorded candidate SHA, its task still
// IN_PROGRESS, and the claim either fresh, a stale RUNNING reclaim, or a
// FAILED retry past the backoff window and under MaxFailures.
func (r *Runner) claim(ctx context.Context) (*claimablePublish, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pr claim transaction: %w", err)
	}
	defer tx.Rollback()

	var c claimablePublish
	err = tx.QueryRowContext(ctx, `
		SELECT a.id::text, a.organization_id::text, a.project_id::text, a.task_id::text,
			a.employee_id::text, a.attempt_number,
			s.id::text, w.id::text, w.worktree_ref, w.branch_name,
			a.candidate_sha, repo.id::text, repo.owner, repo.repo_name, repo.default_branch,
			t.title, a.pr_failure_count, (a.pr_state = 'RUNNING')
		FROM execution_attempts a
		JOIN agent_sessions s
			ON s.organization_id = a.organization_id AND s.project_id = a.project_id
			AND s.task_id = a.task_id AND s.attempt_id = a.id
			AND s.status = 'COMPLETED'
		JOIN workspaces w
			ON w.organization_id = a.organization_id AND w.project_id = a.project_id
			AND w.task_id = a.task_id AND w.id = s.workspace_id
			AND w.state = 'IN_USE' AND w.worktree_ref IS NOT NULL
		JOIN tasks t
			ON t.organization_id = a.organization_id AND t.project_id = a.project_id
			AND t.id = a.task_id AND t.status = 'IN_PROGRESS'
		JOIN repositories repo
			ON repo.organization_id = a.organization_id AND repo.project_id = a.project_id
		WHERE a.state = 'SUCCEEDED' AND a.candidate_sha IS NOT NULL
			AND (a.pr_state = 'PENDING'
				OR (a.pr_state = 'RUNNING'
					AND a.pr_started_at < now() - ($1::bigint || ' seconds')::interval)
				OR (a.pr_state = 'FAILED' AND a.pr_failure_count < $2
					AND a.pr_started_at < now() - ($3::bigint || ' seconds')::interval))
		ORDER BY a.updated_at
		LIMIT 1
		FOR UPDATE OF a SKIP LOCKED`,
		int64(r.cfg.StaleClaimAfter.Seconds()), r.cfg.MaxFailures,
		int64(r.cfg.RetryBackoff.Seconds())).Scan(
		&c.AttemptID, &c.OrganizationID, &c.ProjectID, &c.TaskID, &c.EmployeeID,
		&c.AttemptNumber, &c.SessionRowID, &c.WorkspaceID, &c.WorktreeRef,
		&c.BranchName, &c.CandidateSHA, &c.RepositoryID, &c.RepoOwner, &c.RepoName,
		&c.DefaultBranch, &c.TaskTitle, &c.FailureCount, &c.Reclaim)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim publishable attempt: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET pr_state = 'RUNNING', pr_started_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'SUCCEEDED'
			AND pr_state IN ('PENDING', 'RUNNING', 'FAILED')`,
		c.OrganizationID, c.AttemptID)
	if err != nil {
		return nil, fmt.Errorf("mark pr running: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("attempt pr claim was modified concurrently")
	}
	reasonCode := ReasonPublishStarted
	if c.Reclaim {
		reasonCode = ReasonPublishReclaim
	}
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("pr-runner"),
		EventType: "audit.action_recorded",
		Data: map[string]any{
			"action": "pull_request.claim", "targetType": "execution_attempt",
			"targetId": c.AttemptID, "outcome": reasonCode,
		},
	}); err != nil {
		return nil, fmt.Errorf("record pr claim audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pr claim: %w", err)
	}
	return &c, nil
}

// process does the network/git work outside any transaction, then commits
// the durable outcome (PUBLISHED or FAILED).
func (r *Runner) process(ctx context.Context, c *claimablePublish) {
	opCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.OpTimeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, r.cfg.OpTimeout)
		defer cancel()
	}
	code, detail := r.publish(opCtx, c)
	if code == "" {
		return
	}
	if err := r.fail(ctx, c, code, detail); err != nil {
		slog.Error("record pr failure failed", "error", err, "attempt", c.AttemptID)
	}
}

// remoteURL builds the credential-free https remote for pushes.
func remoteURL(owner, repo string) string {
	return "https://github.com/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repo) + ".git"
}

// publish executes push → find/create PR → persist. It returns a reason
// code only when publication failed; the success path commits its own
// durable facts.
func (r *Runner) publish(ctx context.Context, c *claimablePublish) (string, string) {
	for _, id := range []string{c.AttemptID, c.TaskID, c.SessionRowID, c.RepositoryID} {
		if !safeIDPattern.MatchString(id) {
			return ReasonInternal, "unsafe execution identifier"
		}
	}
	if !safeSHAPattern.MatchString(c.CandidateSHA) ||
		!safeBranchRe.MatchString(c.BranchName) || strings.Contains(c.BranchName, "..") ||
		!safeRepoNameRe.MatchString(c.RepoName) || !safeOwnerNameRe.MatchString(c.RepoOwner) ||
		!safeBranchRe.MatchString(c.DefaultBranch) || strings.Contains(c.DefaultBranch, "..") {
		return ReasonInternal, "unsafe repository or reference data"
	}
	worktreeRef := filepath.Clean(filepath.FromSlash(strings.TrimSpace(c.WorktreeRef)))
	if !filepath.IsLocal(worktreeRef) {
		return ReasonInternal, "unsafe worktree reference"
	}
	worktreeDir := filepath.Join(r.cfg.WorkRoot, worktreeRef)
	if info, err := os.Stat(worktreeDir); err != nil || !info.IsDir() {
		return ReasonInternal, "task worktree is not available for publication"
	}

	// The candidate must exist locally to be pushed; HEAD equality is not
	// required — evidence binds to the recorded SHA, not the branch tip.
	if _, err := r.git.Run(ctx, worktreeDir, "cat-file", "-e", c.CandidateSHA+"^{commit}"); err != nil {
		return ReasonCandidateMissing, "candidate commit is not present in the worktree"
	}

	// Push the exact SHA to the task branch. Credentials ride in
	// http.extraheader so remote URLs and error output stay token-free.
	if _, err := r.git.Run(ctx, worktreeDir,
		"-c", "http.extraheader=AUTHORIZATION: bearer "+r.cfg.Token,
		"push", remoteURL(c.RepoOwner, c.RepoName),
		c.CandidateSHA+":refs/heads/"+c.BranchName); err != nil {
		failureCode := classifyPushError(err)
		if emitErr := r.emit(ctx, c, "git.push_failed", map[string]any{
			"branchName": c.BranchName, "commitSha": c.CandidateSHA,
			"failureCode": failureCode,
		}, fmt.Sprintf("pr:%s:push_failed:%s:%d", c.AttemptID, c.CandidateSHA, c.FailureCount+1)); emitErr != nil {
			return ReasonInternal, "could not record push failure"
		}
		return ReasonPushFailed, failureCode
	}
	if err := r.emit(ctx, c, "git.push_completed", map[string]any{
		"branchName": c.BranchName, "commitSha": c.CandidateSHA,
	}, "pr:"+c.AttemptID+":push_completed:"+c.CandidateSHA); err != nil {
		return ReasonInternal, "could not record push completion"
	}

	// Adopt an existing open PR for the branch, else create one. A 422
	// "already exists" races with adoption — re-query and take that path.
	pr, err := r.github.FindOpenPR(ctx, c.RepoOwner, c.RepoName, c.BranchName)
	if err != nil {
		return apiFailure(err)
	}
	if pr == nil {
		pr, err = r.github.CreatePR(ctx, c.RepoOwner, c.RepoName, CreatePRInput{
			Title: truncate(c.TaskTitle, 200),
			Body:  fmt.Sprintf("DiOffice task work — attempt #%d.\n\nCandidate SHA `%s` passed all required checks. Review the diff and approve exactly this SHA.", c.AttemptNumber, c.CandidateSHA),
			Head:  c.BranchName,
			Base:  c.DefaultBranch,
		})
		switch {
		case err == nil:
		case AlreadyExists(err):
			pr, err = r.github.FindOpenPR(ctx, c.RepoOwner, c.RepoName, c.BranchName)
			if err != nil {
				return apiFailure(err)
			}
			if pr == nil {
				return ReasonGitHubAPI, "github reported an existing pull request but none could be found"
			}
		default:
			return apiFailure(err)
		}
	}
	// The recorded binding must be exactly the tested SHA.
	if pr.HeadSHA != c.CandidateSHA {
		return ReasonHeadMismatch, fmt.Sprintf(
			"pull request #%d head %s does not match candidate %s", pr.Number, pr.HeadSHA, c.CandidateSHA)
	}
	return r.persist(ctx, c, pr)
}

// persist upserts the pull_requests row (one PR per task), links it on the
// task, and moves the task to IN_REVIEW — all facts in one transaction.
func (r *Runner) persist(ctx context.Context, c *claimablePublish, pr *PullRequest) (string, string) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ReasonInternal, "could not begin publish transaction"
	}
	defer tx.Rollback()

	base := eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("pr-runner"),
	}

	var prRowID string
	var existingNumber int
	var existingHead string
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, number, head_sha FROM pull_requests
		WHERE organization_id = $1 AND task_id = $2 FOR UPDATE`,
		c.OrganizationID, c.TaskID).Scan(&prRowID, &existingNumber, &existingHead)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// pull_request.created's state enum is OPEN|DRAFT — a terminal PR can
		// never be "created" as a durable fact.
		if pr.State != "OPEN" && pr.State != "DRAFT" {
			return ReasonInternal, "pull request is not open and cannot be recorded as created"
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO pull_requests (
				organization_id, project_id, task_id, repository_id,
				provider, external_pr_id, number, url, branch_name,
				head_sha, base_sha, state)
			VALUES ($1, $2, $3, $4, 'github', $5, $6, $7, $8, $9, $10, $11)
			RETURNING id::text`,
			c.OrganizationID, c.ProjectID, c.TaskID, c.RepositoryID,
			pr.NodeID, pr.Number, pr.URL, c.BranchName,
			pr.HeadSHA, pr.BaseSHA, pr.State).Scan(&prRowID); err != nil {
			return ReasonInternal, "could not record the pull request"
		}
		input := withInput(base, eventstore.EventInput{
			EventType: "pull_request.created",
			DedupeKey: fmt.Sprintf("pr:%s:created:%d", c.AttemptID, pr.Number),
			Data: map[string]any{
				"provider": "github", "number": pr.Number, "url": pr.URL,
				"headSha": pr.HeadSHA, "baseSha": pr.BaseSHA, "state": pr.State,
			},
		})
		if _, err := eventstore.AppendFact(ctx, tx, input); err != nil {
			return ReasonInternal, "could not record pull request creation"
		}
	case err != nil:
		return ReasonInternal, "could not read the existing pull request"
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE pull_requests
			SET external_pr_id = $3, number = $4, url = $5, branch_name = $6,
				head_sha = $7, base_sha = $8, state = $9, updated_at = now()
			WHERE organization_id = $1 AND id = $2`,
			c.OrganizationID, prRowID,
			pr.NodeID, pr.Number, pr.URL, c.BranchName,
			pr.HeadSHA, pr.BaseSHA, pr.State); err != nil {
			return ReasonInternal, "could not update the pull request record"
		}
		if existingHead != pr.HeadSHA || existingNumber != pr.Number {
			input := withInput(base, eventstore.EventInput{
				EventType: "pull_request.updated",
				DedupeKey: fmt.Sprintf("pr:%s:updated:%d:%s", c.AttemptID, pr.Number, pr.HeadSHA),
				Data: map[string]any{
					"number": pr.Number, "change": "head",
					"headSha": pr.HeadSHA, "state": pr.State,
				},
			})
			if _, err := eventstore.AppendFact(ctx, tx, input); err != nil {
				return ReasonInternal, "could not record pull request update"
			}
		}
	}

	var taskVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks
		SET status = 'IN_REVIEW', pull_request_id = $4,
			task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND status = 'IN_PROGRESS'
		RETURNING task_version`,
		c.OrganizationID, c.ProjectID, c.TaskID, prRowID).Scan(&taskVersion)
	if errors.Is(err, sql.ErrNoRows) {
		// Task left IN_PROGRESS concurrently (e.g. Owner Cancel) — the PR
		// exists on GitHub but no durable binding is recorded.
		return ReasonInternal, "task left IN_PROGRESS before the pull request could be linked"
	}
	if err != nil {
		return ReasonInternal, "could not move the task to IN_REVIEW"
	}
	input := withInput(base, eventstore.EventInput{
		EventType: "task.state_changed",
		Data: map[string]any{
			"fromState": "IN_PROGRESS", "toState": "IN_REVIEW",
			"taskVersion": taskVersion,
			"reason":      fmt.Sprintf("review ready: pull request #%d points at candidate %s", pr.Number, pr.HeadSHA),
		},
	})
	if _, err := eventstore.AppendFact(ctx, tx, input); err != nil {
		return ReasonInternal, "could not record the task transition"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET pr_state = 'PUBLISHED', pr_last_error = NULL, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'SUCCEEDED'`,
		c.OrganizationID, c.AttemptID); err != nil {
		return ReasonInternal, "could not mark publication complete"
	}
	if err := tx.Commit(); err != nil {
		return ReasonInternal, "could not commit pull request facts"
	}
	return "", ""
}

// fail marks pr_state FAILED, counts the failure, and blocks the task once
// MaxFailures is exhausted so persistent GitHub trouble cannot loop forever.
func (r *Runner) fail(ctx context.Context, c *claimablePublish, code, detail string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pr failure transaction: %w", err)
	}
	defer tx.Rollback()

	var count int
	err = tx.QueryRowContext(ctx, `
		UPDATE execution_attempts
		SET pr_state = 'FAILED', pr_failure_count = pr_failure_count + 1,
			pr_last_error = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'SUCCEEDED'
			AND pr_state = 'RUNNING'
		RETURNING pr_failure_count`,
		c.OrganizationID, c.AttemptID, code).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("attempt changed during pr failure recording")
	}
	if err != nil {
		return fmt.Errorf("mark pr failed: %w", err)
	}
	if count < r.cfg.MaxFailures {
		return tx.Commit()
	}

	var taskVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'BLOCKED', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND status = 'IN_PROGRESS'
		RETURNING task_version`,
		c.OrganizationID, c.ProjectID, c.TaskID).Scan(&taskVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("mark task blocked: %w", err)
	default:
		if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
			OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
			TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
			SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
			Producer: "worker", Actor: eventstore.SystemActor("pr-runner"),
			EventType: "task.state_changed",
			Data: map[string]any{
				"fromState": "IN_PROGRESS", "toState": "BLOCKED",
				"taskVersion": taskVersion,
				"reason":      "pull request publication failed repeatedly: " + code + " — " + detail,
			},
		}); err != nil {
			return fmt.Errorf("record task blocked event: %w", err)
		}
	}
	return tx.Commit()
}

// emit persists one canonical fact with a deterministic dedupe key so a
// reclaimed publication cannot duplicate facts.
func (r *Runner) emit(ctx context.Context, c *claimablePublish, eventType string,
	data map[string]any, dedupe string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fact transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("pr-runner"),
		EventType: eventType, DedupeKey: dedupe, Data: data,
	}); err != nil {
		if errors.Is(err, eventstore.ErrDuplicateEvent) {
			// A reclaim re-emitting a fact the first pass already committed
			// is the dedupe key doing its job — not a failure.
			return nil
		}
		return fmt.Errorf("append %s fact: %w", eventType, err)
	}
	return tx.Commit()
}

// classifyPushError maps a git push failure to a bounded owner-safe code.
func classifyPushError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "authentication") || strings.Contains(msg, "403") ||
		strings.Contains(msg, "401") || strings.Contains(msg, "permission denied"):
		return "push_auth_failed"
	case strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist"):
		return "push_repo_unavailable"
	case strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "rejected") ||
		strings.Contains(msg, "stale info"):
		return "push_rejected"
	default:
		return "push_failed"
	}
}

// apiFailure converts a classified GitHub API error into a reason pair.
func apiFailure(err error) (string, string) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return ReasonGitHubAPI, string(apiErr.Code) + " — " + apiErr.Message
	}
	return ReasonGitHubAPI, "unexpected github client error"
}

// withInput overlays fields onto a copied base input (correlation/actor
// context stays identical across a transaction's facts).
func withInput(base, overlay eventstore.EventInput) eventstore.EventInput {
	base.EventType = overlay.EventType
	base.Data = overlay.Data
	base.DedupeKey = overlay.DedupeKey
	base.CausationID = overlay.CausationID
	return base
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
