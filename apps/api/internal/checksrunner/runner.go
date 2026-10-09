// Package checksrunner verifies a completed agent session's work: it claims
// RUNNING attempts whose session ended, pins the candidate commit SHA in the
// task worktree (committing residual changes first so evidence always binds
// to an immutable SHA), executes the manifest's declared checks as argv
// processes with bounded output, and persists canonical check.* facts. All
// required checks passing marks the attempt SUCCEEDED; a required failure or
// verification error marks it FAILED and the task FAILED for an explicit
// Owner Retry. The runner never executes shell strings and never marks a
// task DONE — review, PR creation, and merge stay separate owner-gated steps.
package checksrunner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
	"github.com/rendr17/dioffice/apps/api/internal/provisioning"
)

// Failure reason codes persisted on attempts.error_code and emitted as
// execution_attempt.state_changed reasonCode / task.state_changed context.
const (
	ReasonWorktreeMissing = "worktree_missing"
	ReasonManifestMissing = "manifest_missing"
	ReasonManifestDigest  = "manifest_digest_mismatch"
	ReasonManifestInvalid = "manifest_invalid"
	ReasonAttemptTimeout  = "attempt_timeout"
	ReasonCommitFailed    = "commit_failed"
	ReasonCheckFailed     = "required_check_failed"
	ReasonInternal        = "internal_error"
	ReasonChecksStarted   = "checks_started"
	ReasonChecksReclaim   = "checks_reclaim"
	ReasonChecksPassed    = "checks_passed"
)

var safeIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// ArtifactStore writes a private evidence object and is satisfied by
// objectstore's artifact writer. When nil, check logs are simply not stored
// as artifacts — the check.* facts still record the measured outcome.
type ArtifactStore interface {
	PutArtifact(ctx context.Context, key, contentType string, data []byte) error
}

// Config controls polling and execution bounds.
type Config struct {
	WorkRoot        string
	GitBin          string
	PollInterval    time.Duration
	BatchSize       int
	StaleClaimAfter time.Duration
	OpTimeout       time.Duration
	// OutputLimit bounds each check's retained output tail (default 256 KiB).
	OutputLimit int
}

// Runner claims verifiable attempts and executes manifest checks.
type Runner struct {
	db    *sql.DB
	cfg   Config
	git   provisioning.Git
	store ArtifactStore
}

func New(db *sql.DB, cfg Config, git provisioning.Git, store ArtifactStore) (*Runner, error) {
	if db == nil {
		return nil, errors.New("checks runner requires a database")
	}
	if cfg.WorkRoot == "" || !filepath.IsAbs(cfg.WorkRoot) {
		return nil, errors.New("checks runner requires an absolute WorkRoot")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 4
	}
	if cfg.StaleClaimAfter <= 0 {
		cfg.StaleClaimAfter = 30 * time.Minute
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = 30 * time.Minute
	}
	if cfg.OutputLimit <= 0 {
		cfg.OutputLimit = 256 << 10
	}
	if git == nil {
		git = provisioning.ExecGit{Bin: cfg.GitBin}
	}
	return &Runner{db: db, cfg: cfg, git: git, store: store}, nil
}

// Run polls until ctx is canceled.
func (r *Runner) Run(ctx context.Context) error {
	for {
		processed, err := r.RunOnce(ctx)
		if err != nil {
			slog.Error("checks runner pass failed", "error", err)
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

// RunOnce claims and verifies up to BatchSize attempts.
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
		r.verify(ctx, claim)
	}
	return processed, nil
}

// claimableAttempt is the durable unit of verification work.
type claimableAttempt struct {
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
	ManifestDigest string
	StartedAt      time.Time
}

// claim atomically marks one verifiable attempt's checks as RUNNING: the
// attempt must still be RUNNING with a COMPLETED session, an IN_USE
// worktree, and no fresh checks claim. A stale RUNNING checks_state is
// reclaimable so a crashed runner never wedges an attempt forever.
func (r *Runner) claim(ctx context.Context) (*claimableAttempt, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin checks claim transaction: %w", err)
	}
	defer tx.Rollback()

	var c claimableAttempt
	var reclaim bool
	err = tx.QueryRowContext(ctx, `
		SELECT a.id::text, a.organization_id::text, a.project_id::text, a.task_id::text,
			a.employee_id::text, a.attempt_number,
			s.id::text, w.id::text, w.worktree_ref, w.branch_name,
			COALESCE(t.manifest_digest, ''), a.started_at,
			(a.checks_state = 'RUNNING')
		FROM execution_attempts a
		JOIN agent_sessions s
			ON s.organization_id = a.organization_id AND s.project_id = a.project_id
			AND s.task_id = a.task_id AND s.attempt_id = a.id
			AND s.status = 'COMPLETED'
		JOIN workspaces w
			ON w.organization_id = a.organization_id AND w.task_id = a.task_id
			AND w.id = s.workspace_id AND w.state = 'IN_USE'
			AND w.worktree_ref IS NOT NULL
		JOIN tasks t
			ON t.organization_id = a.organization_id AND t.project_id = a.project_id
			AND t.id = a.task_id
		WHERE a.state = 'RUNNING'
			AND (a.checks_state = 'PENDING'
				OR (a.checks_state = 'RUNNING'
					AND a.checks_started_at < now() - ($1::bigint || ' seconds')::interval))
		ORDER BY a.updated_at
		LIMIT 1
		FOR UPDATE OF a SKIP LOCKED`,
		int64(r.cfg.StaleClaimAfter.Seconds())).Scan(
		&c.AttemptID, &c.OrganizationID, &c.ProjectID, &c.TaskID, &c.EmployeeID,
		&c.AttemptNumber, &c.SessionRowID, &c.WorkspaceID, &c.WorktreeRef,
		&c.BranchName, &c.ManifestDigest, &c.StartedAt, &reclaim)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim verifiable attempt: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET checks_state = 'RUNNING', checks_started_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'RUNNING'
			AND checks_state IN ('PENDING', 'RUNNING')`,
		c.OrganizationID, c.AttemptID)
	if err != nil {
		return nil, fmt.Errorf("mark checks running: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("attempt checks claim was modified concurrently")
	}
	reasonCode := ReasonChecksStarted
	if reclaim {
		reasonCode = ReasonChecksReclaim
	}
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("checks-runner"),
		EventType: "audit.action_recorded",
		Data: map[string]any{
			"action": "checks.claim", "targetType": "execution_attempt",
			"targetId": c.AttemptID, "outcome": reasonCode,
		},
	}); err != nil {
		return nil, fmt.Errorf("record checks claim audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit checks claim: %w", err)
	}
	return &c, nil
}

// verify performs filesystem/process work outside any transaction, then
// commits the durable outcome.
func (r *Runner) verify(ctx context.Context, c *claimableAttempt) {
	opCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.OpTimeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, r.cfg.OpTimeout)
		defer cancel()
	}
	code, detail := r.executeChecks(opCtx, c)
	if code == "" {
		return
	}
	if err := r.fail(ctx, c, code, detail); err != nil {
		slog.Error("record checks failure failed", "error", err, "attempt", c.AttemptID)
	}
}

// executeChecks validates the worktree state, pins the candidate SHA, runs
// every declared check, and commits the outcome. It returns a reason code
// only when the attempt must fail; the check-pass and check-fail paths emit
// their own durable facts.
func (r *Runner) executeChecks(ctx context.Context, c *claimableAttempt) (string, string) {
	for _, id := range []string{c.AttemptID, c.TaskID, c.SessionRowID} {
		if !safeIDPattern.MatchString(id) {
			return ReasonInternal, "unsafe execution identifier"
		}
	}
	worktreeRef := filepath.Clean(filepath.FromSlash(strings.TrimSpace(c.WorktreeRef)))
	if !filepath.IsLocal(worktreeRef) {
		return ReasonInternal, "unsafe worktree reference"
	}
	worktreeDir := filepath.Join(r.cfg.WorkRoot, worktreeRef)
	if info, err := os.Stat(worktreeDir); err != nil || !info.IsDir() {
		return ReasonWorktreeMissing, "task worktree is not available for verification"
	}

	manifestBytes, err := os.ReadFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)))
	if errors.Is(err, os.ErrNotExist) {
		return ReasonManifestMissing, "repository has no " + executionmanifest.Path
	}
	if err != nil {
		return ReasonInternal, "could not read the execution manifest"
	}
	// Deni has write access to the worktree, so the checked-out manifest is
	// untrusted and must still match the digest recorded at READY.
	if got := executionmanifest.Digest(manifestBytes); !strings.EqualFold(got, c.ManifestDigest) {
		return ReasonManifestDigest, "checked-out manifest digest differs from the recorded digest"
	}
	manifest, err := executionmanifest.Parse(manifestBytes)
	if err != nil {
		return ReasonManifestInvalid, "execution manifest failed validation"
	}
	if !c.StartedAt.IsZero() && time.Since(c.StartedAt) >
		time.Duration(manifest.Resources.AttemptTimeoutSeconds)*time.Second {
		return ReasonAttemptTimeout, "attempt exceeded the manifest attempt timeout"
	}

	// Evidence must bind to an immutable commit: checkpoint any residual
	// working-tree changes on the task branch before measuring.
	if out, err := r.git.Run(ctx, worktreeDir, "status", "--porcelain"); err != nil {
		return ReasonInternal, "could not inspect worktree state"
	} else if strings.TrimSpace(out) != "" {
		if _, err := r.git.Run(ctx, worktreeDir, "add", "-A"); err != nil {
			return ReasonCommitFailed, "could not stage residual task work"
		}
		if _, err := r.git.Run(ctx, worktreeDir,
			"-c", "user.name=Deni (DiOffice)", "-c", "user.email=deni@dioffice.local",
			"commit", "-m", "checkpoint: residual task work (automated)"); err != nil {
			return ReasonCommitFailed, "could not commit residual task work"
		}
		if err := r.emitCommitFact(ctx, c, worktreeDir); err != nil {
			return ReasonInternal, "could not record the checkpoint commit"
		}
	}

	commitSha, err := r.git.Run(ctx, worktreeDir, "rev-parse", "HEAD")
	if err != nil {
		return ReasonInternal, "could not resolve the candidate commit"
	}
	candidateSHA := strings.ToLower(strings.TrimSpace(commitSha))
	checkDir := filepath.Join(worktreeDir, filepath.FromSlash(manifest.WorkingDirectory))
	if info, err := os.Stat(checkDir); err != nil || !info.IsDir() {
		return ReasonManifestInvalid, "manifest workingDirectory does not exist in the worktree"
	}

	checkRunID, err := eventstore.NewUUID()
	if err != nil {
		return ReasonInternal, "could not allocate a check run id"
	}
	passThrough := []string{}
	if manifest.Environment.PassThrough != nil {
		passThrough = *manifest.Environment.PassThrough
	}
	env := checkEnv(passThrough, nil)

	requiredFailed := []string{}
	for _, check := range manifest.Commands.Checks {
		if _, err := r.emit(ctx, c, "check.started", map[string]any{
			"checkRunId": checkRunID, "checkId": check.ID, "name": check.Name,
			"commitSha": candidateSHA, "required": check.Required,
		}, "checkrun:"+c.AttemptID+":"+checkRunID+":"+check.ID+":started"); err != nil {
			return ReasonInternal, "could not record check start"
		}
		result := runCheck(ctx, checkDir, check.Argv, env,
			time.Duration(check.TimeoutSeconds)*time.Second, r.cfg.OutputLimit)
		data := map[string]any{
			"checkRunId": checkRunID, "checkId": check.ID, "name": check.Name,
			"commitSha": candidateSHA, "durationMs": result.duration.Milliseconds(),
		}
		eventType := "check.passed"
		if result.failureCode != "" {
			eventType = "check.failed"
			data["failureCode"] = result.failureCode
			if check.Required {
				requiredFailed = append(requiredFailed, check.ID)
			}
		}
		outcomeID, err := r.emit(ctx, c, eventType, data,
			"checkrun:"+c.AttemptID+":"+checkRunID+":"+check.ID+":outcome")
		if err != nil {
			return ReasonInternal, "could not record check outcome"
		}
		if err := r.storeLog(ctx, c, checkRunID, check.ID, outcomeID, candidateSHA, result); err != nil {
			slog.Warn("check log artifact store failed", "check", check.ID, "error", err)
		}
	}

	if len(requiredFailed) > 0 {
		return r.failWithChecks(ctx, c, requiredFailed)
	}
	return r.succeed(ctx, c, candidateSHA)
}

// emit persists one canonical fact with a deterministic dedupe key so a
// reclaimed run's events cannot duplicate.
func (r *Runner) emit(ctx context.Context, c *claimableAttempt, eventType string,
	data map[string]any, dedupe string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin fact transaction: %w", err)
	}
	defer tx.Rollback()
	eventID, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("checks-runner"),
		EventType: eventType, DedupeKey: dedupe, Data: data,
	})
	if err != nil {
		return "", fmt.Errorf("append %s fact: %w", eventType, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit %s fact: %w", eventType, err)
	}
	return eventID, nil
}

// emitCommitFact records git.commit_created for the checkpoint commit.
func (r *Runner) emitCommitFact(ctx context.Context, c *claimableAttempt, worktreeDir string) error {
	commitSha, err := r.git.Run(ctx, worktreeDir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve checkpoint commit: %w", err)
	}
	treeSha, err := r.git.Run(ctx, worktreeDir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return fmt.Errorf("resolve checkpoint tree: %w", err)
	}
	_, err = r.emit(ctx, c, "git.commit_created", map[string]any{
		"commitSha":  strings.ToLower(strings.TrimSpace(commitSha)),
		"treeSha":    strings.ToLower(strings.TrimSpace(treeSha)),
		"branchName": c.BranchName,
		"message":    "checkpoint: residual task work (automated)",
	}, "checkrun:"+c.AttemptID+":commit:"+strings.ToLower(strings.TrimSpace(commitSha)))
	return err
}

// storeLog persists a check's bounded output tail as a private artifact and
// emits artifact.created causally linked to the check's outcome event.
func (r *Runner) storeLog(ctx context.Context, c *claimableAttempt, checkRunID, checkID,
	causationID, commitSHA string, result checkResult) error {
	if r.store == nil || len(result.output) == 0 {
		return nil
	}
	artifactID, err := eventstore.NewUUID()
	if err != nil {
		return err
	}
	key := "artifacts/" + artifactID
	header := fmt.Sprintf("check %s (%s) on %s\nexit_code=%d truncated=%v\n---\n",
		checkID, checkRunID, commitSHA, result.exitCode, result.truncated)
	body := append([]byte(header), result.output...)
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])
	if err := r.store.PutArtifact(ctx, key, "text/plain", body); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts (
			id, organization_id, project_id, task_id, attempt_id, session_id,
			kind, storage_key, mime_type, size_bytes, sha256)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, 'log_archive', $7, 'text/plain', $8, $9)`,
		artifactID, c.OrganizationID, c.ProjectID, c.TaskID, c.AttemptID,
		c.SessionRowID, key, int64(len(body)), sumHex); err != nil {
		return fmt.Errorf("insert artifact row: %w", err)
	}
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("checks-runner"),
		CausationID: causationID,
		EventType:   "artifact.created",
		DedupeKey:   "checkrun:" + c.AttemptID + ":" + checkRunID + ":" + checkID + ":log",
		Data: map[string]any{
			"artifactId": artifactID, "kind": "log_archive", "mimeType": "text/plain",
			"sizeBytes": int64(len(body)), "sha256": sumHex,
		},
	}); err != nil {
		return fmt.Errorf("record artifact event: %w", err)
	}
	return tx.Commit()
}

// succeed marks the attempt SUCCEEDED once every required check passed for
// the pinned candidate SHA. The task stays IN_PROGRESS: it reaches IN_REVIEW
// only when a pull request exists and points at this SHA (separate step).
func (r *Runner) succeed(ctx context.Context, c *claimableAttempt, candidateSHA string) (string, string) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ReasonInternal, "could not begin success transaction"
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET state = 'SUCCEEDED', checks_state = 'PASSED', candidate_sha = $3,
			ended_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'RUNNING'`,
		c.OrganizationID, c.AttemptID, candidateSHA)
	if err != nil {
		return ReasonInternal, "could not mark attempt succeeded"
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		slog.Warn("attempt left RUNNING during checks finalize", "attempt", c.AttemptID)
		return "", ""
	}
	if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("checks-runner"),
		EventType: "execution_attempt.state_changed",
		Data: map[string]any{
			"fromState": "RUNNING", "toState": "SUCCEEDED",
			"attemptNumber": c.AttemptNumber, "reasonCode": ReasonChecksPassed,
		},
	}); err != nil {
		return ReasonInternal, "could not record attempt success"
	}
	if err := tx.Commit(); err != nil {
		return ReasonInternal, "could not commit checks success"
	}
	return "", ""
}

// failWithChecks fails the attempt and task when a required check measured a
// failure — the evidence and check.failed facts are already durable.
func (r *Runner) failWithChecks(ctx context.Context, c *claimableAttempt,
	failed []string) (string, string) {
	detailBytes, _ := json.Marshal(failed)
	detail := "required check failed: " + string(detailBytes)
	if err := r.fail(ctx, c, ReasonCheckFailed, detail); err != nil {
		return ReasonInternal, "could not record check failure outcome"
	}
	return "", ""
}

// fail records the durable failure outcome: attempt FAILED, task FAILED
// (from IN_PROGRESS) with an owner-safe reason. Workspace/session rows keep
// their terminal states — a Retried task provisions a fresh workspace.
func (r *Runner) fail(ctx context.Context, c *claimableAttempt, code, detail string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failure transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET state = 'FAILED', checks_state = 'FAILED', error_code = $3,
			retryable = $4, ended_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'RUNNING'`,
		c.OrganizationID, c.AttemptID, code,
		code != ReasonAttemptTimeout && code != ReasonManifestDigest)
	if err != nil {
		return fmt.Errorf("mark attempt failed: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("attempt changed during checks failure recording")
	}
	attemptEventID, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
		OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
		TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
		SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
		Producer: "worker", Actor: eventstore.SystemActor("checks-runner"),
		EventType: "execution_attempt.state_changed",
		Data: map[string]any{
			"fromState": "RUNNING", "toState": "FAILED",
			"attemptNumber": c.AttemptNumber, "reasonCode": code,
		},
	})
	if err != nil {
		return fmt.Errorf("record attempt failure event: %w", err)
	}

	var taskVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'FAILED', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND status = 'IN_PROGRESS'
		RETURNING task_version`,
		c.OrganizationID, c.ProjectID, c.TaskID).Scan(&taskVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("mark task failed: %w", err)
	default:
		if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
			OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
			TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
			SessionID: c.SessionRowID, WorkspaceID: c.WorkspaceID,
			Producer: "worker", Actor: eventstore.SystemActor("checks-runner"),
			CausationID: attemptEventID,
			EventType:   "task.state_changed",
			Data: map[string]any{
				"fromState": "IN_PROGRESS", "toState": "FAILED",
				"taskVersion": taskVersion, "reason": "verification failed: " + code + " — " + detail,
			},
		}); err != nil {
			return fmt.Errorf("record task failure event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failure records: %w", err)
	}
	return nil
}
