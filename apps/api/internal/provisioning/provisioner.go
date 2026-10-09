// Package provisioning is the workspace reconciler for the execution layer.
// It claims committed CREATED attempts, materializes the task's isolated git
// worktree from the connected repository, verifies and validates the recorded
// execution manifest, and advances the workspace to READY. It never starts a
// runtime: the OpenCode session starter (agent gateway integration) takes over
// once a workspace is READY, and failures move the task to BLOCKED with durable
// state facts instead of silent retries.
package provisioning

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
)

// Failure reason codes persisted on attempts.error_code and emitted as
// execution_attempt.state_changed reasonCode. They are safe owner-visible
// identifiers, never raw command output.
const (
	ReasonProvisioningStarted   = "provisioning_started"
	ReasonProvisioningReclaim   = "provisioning_reclaim"
	ReasonRepositoryUnreachable = "repository_unreachable"
	ReasonDefaultBranchMissing  = "default_branch_missing"
	ReasonWorktreeFailed        = "worktree_failed"
	ReasonManifestMissing       = "manifest_missing"
	ReasonManifestDigest        = "manifest_digest_mismatch"
	ReasonManifestInvalid       = "manifest_invalid"
	ReasonWorkerProfile         = "worker_profile_mismatch"
	ReasonInternal              = "internal_error"
)

var safeIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// Git executes git with an argv array in a working directory. Command text is
// never interpolated into a shell.
type Git interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// ExecGit shells out to the git binary with bounded output capture.
type ExecGit struct {
	Bin string
}

func (g ExecGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedBuffer{limit: 4096, buffer: &stderr}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return stdout.String(), fmt.Errorf("git %s: %w", args[0], err)
		}
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, detail)
	}
	return stdout.String(), nil
}

type limitedBuffer struct {
	limit  int
	buffer *bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len() < b.limit {
		remaining := b.limit - b.buffer.Len()
		if len(p) > remaining {
			_, _ = b.buffer.Write(p[:remaining])
			return len(p), nil
		}
		_, _ = b.buffer.Write(p)
	}
	return len(p), nil
}

// Config controls where workspaces are materialized and how remotes resolve.
// WorkRoot must be an absolute operator-owned directory; worktree refs stored
// in the database stay relative to it so host paths never reach events or API
// payloads.
type Config struct {
	WorkRoot        string
	GitBin          string
	RemoteBase      string
	OpTimeout       time.Duration
	StaleClaimAfter time.Duration
	PollInterval    time.Duration
	// RemoteURL overrides repository remote resolution; tests use it to point
	// at a local fixture instead of github.com.
	RemoteURL func(owner, name string) string
}

func (c Config) remoteURL(owner, name string) string {
	if c.RemoteURL != nil {
		return c.RemoteURL(owner, name)
	}
	base := c.RemoteBase
	if base == "" {
		base = "https://github.com"
	}
	return strings.TrimSuffix(base, "/") + "/" + owner + "/" + name + ".git"
}

// Provisioner claims and materializes queued workspaces.
type Provisioner struct {
	db  *sql.DB
	cfg Config
	git Git
}

func New(db *sql.DB, cfg Config, git Git) (*Provisioner, error) {
	if db == nil {
		return nil, errors.New("provisioning requires a database")
	}
	if cfg.WorkRoot == "" || !filepath.IsAbs(cfg.WorkRoot) {
		return nil, errors.New("provisioning requires an absolute WorkRoot")
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = 5 * time.Minute
	}
	if cfg.StaleClaimAfter <= 0 {
		cfg.StaleClaimAfter = 10 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if git == nil {
		git = ExecGit{Bin: cfg.GitBin}
	}
	return &Provisioner{db: db, cfg: cfg, git: git}, nil
}

// claimedAttempt is the durable unit of provisioning work.
type claimedAttempt struct {
	AttemptID      string
	AttemptNumber  int64
	AttemptState   string
	WorkspaceID    string
	BranchName     string
	WorkerProfile  string
	TaskVersion    int64
	OrganizationID string
	ProjectID      string
	TaskID         string
	EmployeeID     string
	ManifestDigest string
	RepositoryID   string
	RepoOwner      string
	RepoName       string
	DefaultBranch  string
}

// Run polls for provisionable attempts until ctx is canceled. Each claim is
// processed sequentially; concurrent provisioners are safe because claims use
// FOR UPDATE SKIP LOCKED and the CREATED → PROVISIONING transition is atomic.
func (p *Provisioner) Run(ctx context.Context) error {
	for {
		processed, err := p.RunOnce(ctx)
		if err != nil {
			slog.Error("provisioner pass failed", "error", err)
		}
		if processed == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.cfg.PollInterval):
			}
		}
	}
}

// RunOnce claims and processes every currently provisionable attempt; it
// returns the number of claims handled.
func (p *Provisioner) RunOnce(ctx context.Context) (int, error) {
	processed := 0
	for {
		claim, err := p.claim(ctx)
		if err != nil {
			return processed, err
		}
		if claim == nil {
			return processed, nil
		}
		processed++
		p.process(ctx, claim)
	}
}

// claim atomically selects one queued attempt and advances it to PROVISIONING
// so the work is owned by this process. Stale PROVISIONING claims older than
// StaleClaimAfter are reclaimed for crash recovery.
func (p *Provisioner) claim(ctx context.Context) (*claimedAttempt, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer tx.Rollback()

	var c claimedAttempt
	err = tx.QueryRowContext(ctx, `
		SELECT a.id::text, a.organization_id::text, a.project_id::text, a.task_id::text,
			a.employee_id::text, a.attempt_number, a.state,
			w.id::text, w.branch_name, w.worker_profile,
			t.task_version, COALESCE(t.manifest_digest, ''),
			r.id::text, r.owner, r.repo_name, r.default_branch
		FROM execution_attempts a
		JOIN workspaces w
			ON w.organization_id = a.organization_id AND w.task_id = a.task_id
			AND w.state = 'PROVISIONING'
		JOIN tasks t
			ON t.organization_id = a.organization_id AND t.id = a.task_id
			AND t.status = 'PROVISIONING'
		JOIN repositories r
			ON r.organization_id = a.organization_id AND r.project_id = a.project_id
		WHERE (a.state = 'CREATED'
			OR (a.state = 'PROVISIONING' AND a.updated_at < now() - ($1::bigint || ' seconds')::interval))
		ORDER BY a.created_at
		LIMIT 1
		FOR UPDATE OF a SKIP LOCKED`,
		int64(p.cfg.StaleClaimAfter.Seconds())).Scan(
		&c.AttemptID, &c.OrganizationID, &c.ProjectID, &c.TaskID, &c.EmployeeID,
		&c.AttemptNumber, &c.AttemptState,
		&c.WorkspaceID, &c.BranchName, &c.WorkerProfile,
		&c.TaskVersion, &c.ManifestDigest,
		&c.RepositoryID, &c.RepoOwner, &c.RepoName, &c.DefaultBranch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim provisionable attempt: %w", err)
	}

	fromState := c.AttemptState
	reasonCode := ReasonProvisioningStarted
	if fromState == "PROVISIONING" {
		reasonCode = ReasonProvisioningReclaim
	} else {
		result, err := tx.ExecContext(ctx, `
			UPDATE execution_attempts
			SET state = 'PROVISIONING', updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND state = 'CREATED'`,
			c.OrganizationID, c.AttemptID)
		if err != nil {
			return nil, fmt.Errorf("advance claimed attempt: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, errors.New("claimed attempt was modified concurrently")
		}
		c.AttemptState = "PROVISIONING"
	}
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		WorkspaceID: c.WorkspaceID, Producer: "reconciler",
		Actor:     eventstore.SystemActor("provisioner"),
		EventType: "execution_attempt.state_changed",
		Data: map[string]any{
			"fromState": fromState, "toState": "PROVISIONING",
			"attemptNumber": c.AttemptNumber, "reasonCode": reasonCode,
		},
	}); err != nil {
		return nil, fmt.Errorf("record claim event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return &c, nil
}

// process performs the filesystem work outside any transaction, then commits
// the resulting durable state.
func (p *Provisioner) process(ctx context.Context, claim *claimedAttempt) {
	worktreeRef, code, detail := p.materialize(ctx, claim)
	if code == "" {
		if err := p.finalize(ctx, claim, worktreeRef); err != nil {
			slog.Error("finalize provisioned workspace failed", "error", err, "attempt", claim.AttemptID)
		}
		return
	}
	slog.Info("provisioning failed", "attempt", claim.AttemptID, "task", claim.TaskID, "code", code)
	if err := p.fail(ctx, claim, code, detail); err != nil {
		slog.Error("record provisioning failure failed", "error", err, "attempt", claim.AttemptID)
	}
}

// materialize performs clone/fetch, branch+worktree creation, and manifest
// verification. It returns the workspace-relative worktree ref, or a failure
// reason code with a short owner-safe detail.
func (p *Provisioner) materialize(ctx context.Context, claim *claimedAttempt) (string, string, string) {
	for _, id := range []string{claim.RepositoryID, claim.TaskID} {
		if !safeIDPattern.MatchString(id) {
			return "", ReasonInternal, "unsafe workspace identifier"
		}
	}
	opCtx := ctx
	var cancel context.CancelFunc
	if p.cfg.OpTimeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, p.cfg.OpTimeout)
		defer cancel()
	}

	mirrorDir := filepath.Join(p.cfg.WorkRoot, "repos", claim.RepositoryID+".git")
	if _, err := os.Stat(mirrorDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", ReasonInternal, "could not inspect repository cache"
		}
		if err := os.MkdirAll(filepath.Dir(mirrorDir), 0o755); err != nil {
			return "", ReasonInternal, "could not create repository cache directory"
		}
		remote := p.cfg.remoteURL(claim.RepoOwner, claim.RepoName)
		if _, err := p.git.Run(opCtx, p.cfg.WorkRoot, "clone", "--mirror", remote, mirrorDir); err != nil {
			return "", ReasonRepositoryUnreachable, "repository clone failed; check repository details and network"
		}
	} else {
		if _, err := p.git.Run(opCtx, mirrorDir, "remote", "update", "--prune"); err != nil {
			return "", ReasonRepositoryUnreachable, "repository fetch failed; check repository details and network"
		}
	}
	// Checkout bytes must match the pushed blob bytes so the recorded manifest
	// digest is verifiable; global autocrlf would mutate them on Windows.
	if _, err := p.git.Run(opCtx, mirrorDir, "config", "core.autocrlf", "false"); err != nil {
		return "", ReasonInternal, "could not pin repository cache config"
	}

	baseRef := "refs/heads/" + claim.DefaultBranch
	if _, err := p.git.Run(opCtx, mirrorDir, "rev-parse", "--verify", "--quiet", baseRef); err != nil {
		return "", ReasonDefaultBranchMissing, "default branch was not found in the connected repository"
	}

	worktreeRef := "worktrees/" + claim.TaskID
	worktreeDir := filepath.Join(p.cfg.WorkRoot, filepath.FromSlash(worktreeRef))
	if info, err := os.Stat(worktreeDir); err == nil && info.IsDir() {
		// Reclaim path: a previous attempt already materialized this worktree.
		// Reuse it only if it still checks out the expected task branch.
		branch, branchErr := p.git.Run(opCtx, worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")
		if branchErr != nil || strings.TrimSpace(branch) != claim.BranchName {
			return "", ReasonWorktreeFailed, "existing task worktree does not match the task branch"
		}
	} else {
		_, branchErr := p.git.Run(opCtx, mirrorDir, "rev-parse", "--verify", "--quiet",
			"refs/heads/"+claim.BranchName)
		var addErr error
		if branchErr == nil {
			_, addErr = p.git.Run(opCtx, mirrorDir, "worktree", "add", worktreeDir, claim.BranchName)
		} else {
			_, addErr = p.git.Run(opCtx, mirrorDir, "worktree", "add", "-b", claim.BranchName, worktreeDir, baseRef)
		}
		if addErr != nil {
			// A stale worktree registration can outlive a deleted directory
			// after a crash; prune metadata and retry once before failing.
			if _, pruneErr := p.git.Run(opCtx, mirrorDir, "worktree", "prune"); pruneErr != nil {
				return "", ReasonWorktreeFailed, "could not prune stale worktree metadata"
			}
			if branchErr == nil {
				_, addErr = p.git.Run(opCtx, mirrorDir, "worktree", "add", worktreeDir, claim.BranchName)
			} else {
				_, addErr = p.git.Run(opCtx, mirrorDir, "worktree", "add", "-b", claim.BranchName, worktreeDir, baseRef)
			}
			if addErr != nil {
				return "", ReasonWorktreeFailed, "could not create the task worktree"
			}
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)))
	if errors.Is(err, os.ErrNotExist) {
		return "", ReasonManifestMissing, "repository has no " + executionmanifest.Path
	}
	if err != nil {
		return "", ReasonInternal, "could not read the execution manifest"
	}
	if got := executionmanifest.Digest(manifestBytes); !strings.EqualFold(got, claim.ManifestDigest) {
		return "", ReasonManifestDigest, "recorded manifest digest does not match the repository file"
	}
	manifest, err := executionmanifest.Parse(manifestBytes)
	if err != nil {
		return "", ReasonManifestInvalid, "execution manifest failed validation"
	}
	if manifest.WorkerProfile != claim.WorkerProfile {
		return "", ReasonWorkerProfile, "manifest workerProfile differs from the workspace profile"
	}
	return worktreeRef, "", ""
}

// finalize commits workspace READY with its worktree ref and emits the
// workspace.state_changed fact. The attempt stays PROVISIONING until the
// runtime session starter confirms a session (a separate component).
func (p *Provisioner) finalize(ctx context.Context, claim *claimedAttempt, worktreeRef string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize transaction: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE workspaces
		SET state = 'READY', worktree_ref = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'PROVISIONING'`,
		claim.OrganizationID, claim.WorkspaceID, worktreeRef)
	if err != nil {
		return fmt.Errorf("mark workspace ready: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("workspace changed during provisioning; finalize aborted")
	}
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: claim.OrganizationID, ProjectID: claim.ProjectID,
		TaskID: claim.TaskID, EmployeeID: claim.EmployeeID, AttemptID: claim.AttemptID,
		WorkspaceID: claim.WorkspaceID, Producer: "reconciler",
		Actor:     eventstore.SystemActor("provisioner"),
		EventType: "workspace.state_changed",
		Data: map[string]any{
			"fromState": "PROVISIONING", "toState": "READY",
			"branchName": claim.BranchName, "workerProfile": claim.WorkerProfile,
		},
	}); err != nil {
		return fmt.Errorf("record workspace ready event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace ready: %w", err)
	}
	return nil
}

// fail moves the attempt to FAILED, the workspace to FAILED, and the task to
// BLOCKED (when still PROVISIONING) with durable events so the Owner sees an
// actionable state and can Retry explicitly.
func (p *Provisioner) fail(ctx context.Context, claim *claimedAttempt, code, detail string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failure transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET state = 'FAILED', error_code = $3, ended_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2
			AND state IN ('CREATED', 'PROVISIONING', 'RUNNING', 'WAITING_APPROVAL')`,
		claim.OrganizationID, claim.AttemptID, code); err != nil {
		return fmt.Errorf("mark attempt failed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workspaces SET state = 'FAILED', updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'PROVISIONING'`,
		claim.OrganizationID, claim.WorkspaceID); err != nil {
		return fmt.Errorf("mark workspace failed: %w", err)
	}

	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return fmt.Errorf("allocate failure correlation id: %w", err)
	}
	actor := eventstore.SystemActor("provisioner")
	factInput := eventstore.EventInput{
		OrganizationID: claim.OrganizationID, ProjectID: claim.ProjectID,
		TaskID: claim.TaskID, EmployeeID: claim.EmployeeID, AttemptID: claim.AttemptID,
		WorkspaceID: claim.WorkspaceID, Producer: "reconciler", Actor: actor,
		CorrelationID: correlationID,
	}
	attemptEventID, err := eventstore.AppendFact(ctx, tx, withType(factInput, "execution_attempt.state_changed", map[string]any{
		"fromState": "PROVISIONING", "toState": "FAILED",
		"attemptNumber": claim.AttemptNumber, "reasonCode": code,
	}))
	if err != nil {
		return fmt.Errorf("record attempt failure event: %w", err)
	}
	factInput.CausationID = attemptEventID
	if _, err := eventstore.AppendFact(ctx, tx, withType(factInput, "workspace.state_changed", map[string]any{
		"fromState": "PROVISIONING", "toState": "FAILED",
		"branchName": claim.BranchName, "workerProfile": claim.WorkerProfile,
	})); err != nil {
		return fmt.Errorf("record workspace failure event: %w", err)
	}

	var taskVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'BLOCKED', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND status = 'PROVISIONING'
		RETURNING task_version`,
		claim.OrganizationID, claim.ProjectID, claim.TaskID).Scan(&taskVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Task left PROVISIONING during materialization (e.g. future Cancel);
		// the attempt/workspace facts above are still recorded.
	case err != nil:
		return fmt.Errorf("mark task blocked: %w", err)
	default:
		if _, err := eventstore.AppendFact(ctx, tx, withType(factInput, "task.state_changed", map[string]any{
			"fromState": "PROVISIONING", "toState": "BLOCKED",
			"taskVersion": taskVersion, "reason": "provisioning failed: " + code + " — " + detail,
		})); err != nil {
			return fmt.Errorf("record task blocked event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failure records: %w", err)
	}
	return nil
}

func withType(input eventstore.EventInput, eventType string, data map[string]any) eventstore.EventInput {
	input.EventType = eventType
	input.Data = data
	return input
}
