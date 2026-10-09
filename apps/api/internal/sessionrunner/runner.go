// Package sessionrunner is the runtime session starter for the execution
// layer. It claims PROVISIONING attempts whose workspace is READY, verifies
// the materialized manifest still matches the recorded digest, asks the
// OpenCode runtime for a session bound to the task worktree, sends the
// initial task instruction, and then advances attempt/workspace/task to
// RUNNING/IN_USE/IN_PROGRESS with durable events. Runtime failures leave a
// durable FAILED attempt/session and a BLOCKED task — never silent retries
// and never a fake RUNNING state.
package sessionrunner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
	"github.com/rendr17/dioffice/apps/api/internal/providers"
)

// Failure reason codes persisted on attempts.error_code and sessions, and
// emitted as execution_attempt.state_changed / session.failed reasonCode or
// errorCode. They are safe owner-visible identifiers, never raw upstream
// responses or host paths.
const (
	ReasonSessionStarted      = "session_started"
	ReasonSessionInterrupted  = "session_start_interrupted"
	ReasonRuntimeUnreachable  = "runtime_unreachable"
	ReasonRuntimeRejected     = "runtime_rejected"
	ReasonRuntimeInvalidReply = "runtime_invalid_response"
	ReasonManifestMissing     = "manifest_missing"
	ReasonManifestDigest      = "manifest_digest_mismatch"
	ReasonManifestInvalid     = "manifest_invalid"
	ReasonWorkerProfile       = "worker_profile_mismatch"
	ReasonProviderUnknown     = "provider_unknown"
	ReasonProviderConfig      = "provider_not_configured"
	ReasonProviderDisabled    = "provider_disabled"
	ReasonProviderUnbuilt     = "provider_not_implemented"
	ReasonInternal            = "internal_error"
)

// Runtime is the provider boundary for session lifecycle calls. DiOffice
// keeps ownership of state; the runtime only accepts/rejects operations.
type Runtime interface {
	// CreateSession returns the provider session id bound to a worktree
	// directory.
	CreateSession(ctx context.Context, directory, title string) (string, error)
	// SendPrompt submits the initial task instruction without blocking on
	// the agent's turn.
	SendPrompt(ctx context.Context, sessionID, prompt string) error
	// Abort requests best-effort cancellation of a provider session. A
	// missing session is a successful abort.
	Abort(ctx context.Context, sessionID string) error
}

// RuntimeError classifies runtime call failures for reason-code mapping.
// Kind is "unreachable", "rejected", or "invalid_response".
type RuntimeError struct {
	Kind   string
	Detail string
}

func (e *RuntimeError) Error() string { return "runtime " + e.Kind + ": " + e.Detail }

func runtimeKind(err error) string {
	var re *RuntimeError
	if errors.As(err, &re) {
		return re.Kind
	}
	return "unreachable"
}

// providerReason maps provider-registry failures to owner-safe reason codes.
// Every code here is deterministic — the attempt is not retryable as-is.
func providerReason(err error) string {
	switch {
	case errors.Is(err, providers.ErrProviderDisabled):
		return ReasonProviderDisabled
	case errors.Is(err, providers.ErrProviderNotConfigured):
		return ReasonProviderConfig
	case errors.Is(err, providers.ErrProviderNotImplemented):
		return ReasonProviderUnbuilt
	case errors.Is(err, providers.ErrProviderUnknown):
		return ReasonProviderUnknown
	default:
		return ReasonInternal
	}
}

// Config controls polling cadence and where materialized worktrees live.
// WorkRoot must match the provisioner's so worktree_ref resolves to the same
// directory the runtime session is bound to.
type Config struct {
	WorkRoot        string
	OpTimeout       time.Duration
	StaleClaimAfter time.Duration
	PollInterval    time.Duration
}

// RuntimeResolver builds the runtime adapter for one claimed attempt's
// provider key and organization. A resolver must fail closed for provider
// keys whose adapter is not implemented — never fake a session.
type RuntimeResolver func(ctx context.Context, organizationID, providerKey string) (Runtime, error)

// Runner claims READY workspaces and starts runtime sessions for them.
type Runner struct {
	db      *sql.DB
	cfg     Config
	runtime Runtime
	resolve RuntimeResolver
}

func New(db *sql.DB, cfg Config, runtime Runtime) (*Runner, error) {
	r, err := newRunner(db, cfg)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, errors.New("session runner requires a runtime adapter")
	}
	r.runtime = runtime
	return r, nil
}

// NewWithResolver builds a runner that resolves the adapter per attempt from
// the attempt's recorded provider key (execution_attempts.runtime_type).
func NewWithResolver(db *sql.DB, cfg Config, resolve RuntimeResolver) (*Runner, error) {
	r, err := newRunner(db, cfg)
	if err != nil {
		return nil, err
	}
	if resolve == nil {
		return nil, errors.New("session runner requires a runtime resolver")
	}
	r.resolve = resolve
	return r, nil
}

func newRunner(db *sql.DB, cfg Config) (*Runner, error) {
	if db == nil {
		return nil, errors.New("session runner requires a database")
	}
	if cfg.WorkRoot == "" || !filepath.IsAbs(cfg.WorkRoot) {
		return nil, errors.New("session runner requires an absolute WorkRoot")
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = 2 * time.Minute
	}
	if cfg.StaleClaimAfter <= 0 {
		cfg.StaleClaimAfter = 10 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	return &Runner{db: db, cfg: cfg}, nil
}

// runtimeFor returns the adapter for the claim's provider: the resolver when
// configured, otherwise the single injected runtime (tests/legacy wiring).
func (r *Runner) runtimeFor(ctx context.Context, c *claimedWork) (Runtime, error) {
	if r.resolve != nil {
		return r.resolve(ctx, c.OrganizationID, c.RuntimeType)
	}
	if r.runtime == nil {
		return nil, errors.New("no runtime adapter is configured")
	}
	return r.runtime, nil
}

// claimedWork is one committed attempt ready for a runtime session.
type claimedWork struct {
	AttemptID          string
	AttemptNumber      int64
	SessionID          string
	RuntimeType        string
	OrganizationID     string
	ProjectID          string
	TaskID             string
	EmployeeID         string
	WorkspaceID        string
	WorktreeRef        string
	BranchName         string
	WorkerProfile      string
	TaskVersion        int64
	ManifestDigest     string
	TaskTitle          string
	TaskDescription    string
	AcceptanceCriteria json.RawMessage
	EmployeeName       string
	EmployeeRole       string
	TaskStatus         string
	ChangeRequest      string
}

// Run polls for sessionable attempts until ctx is canceled. Claims are
// storage-level (a committed STARTING agent_sessions row), so concurrent
// runners cannot start the same session twice.
func (r *Runner) Run(ctx context.Context) error {
	for {
		processed, err := r.RunOnce(ctx)
		if err != nil {
			slog.Error("session runner pass failed", "error", err)
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

// RunOnce claims and processes every currently sessionable attempt and
// returns the number of claims handled.
func (r *Runner) RunOnce(ctx context.Context) (int, error) {
	processed := 0
	for {
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
}

// claim locks one PROVISIONING attempt with a READY workspace, fails any
// stale STARTING session left by a crashed runner, and inserts the new
// STARTING session row that marks the work as owned.
func (r *Runner) claim(ctx context.Context) (*claimedWork, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer tx.Rollback()

	var c claimedWork
	err = tx.QueryRowContext(ctx, `
		SELECT a.id::text, a.runtime_type, a.organization_id::text, a.project_id::text, a.task_id::text,
			a.employee_id::text, a.attempt_number,
			w.id::text, w.worktree_ref, w.branch_name, w.worker_profile,
			t.task_version, COALESCE(t.manifest_digest, ''), t.title, t.description,
			t.acceptance_criteria, e.name, e.role,
			t.status, COALESCE(a.change_request, '')
		FROM execution_attempts a
		JOIN workspaces w
			ON w.organization_id = a.organization_id AND w.task_id = a.task_id
			AND w.state = 'READY' AND w.worktree_ref IS NOT NULL
		JOIN tasks t
			ON t.organization_id = a.organization_id AND t.id = a.task_id
			AND (t.status = 'PROVISIONING'
				OR (t.status = 'IN_PROGRESS' AND a.change_request IS NOT NULL))
		JOIN employees e
			ON e.organization_id = a.organization_id AND e.id = a.employee_id
		WHERE a.state = 'PROVISIONING'
		ORDER BY a.created_at
		LIMIT 1
		FOR UPDATE OF a SKIP LOCKED`).Scan(
		&c.AttemptID, &c.RuntimeType, &c.OrganizationID, &c.ProjectID, &c.TaskID, &c.EmployeeID,
		&c.AttemptNumber, &c.WorkspaceID, &c.WorktreeRef, &c.BranchName,
		&c.WorkerProfile, &c.TaskVersion, &c.ManifestDigest, &c.TaskTitle,
		&c.TaskDescription, &c.AcceptanceCriteria, &c.EmployeeName, &c.EmployeeRole,
		&c.TaskStatus, &c.ChangeRequest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim sessionable attempt: %w", err)
	}

	// Under the attempt lock, reconcile in-flight session state.
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, status, COALESCE(runtime_session_id, ''), created_at
		FROM agent_sessions
		WHERE organization_id = $1 AND attempt_id = $2
			AND status IN ('STARTING', 'RUNNING', 'PAUSED')
		ORDER BY created_at`,
		c.OrganizationID, c.AttemptID)
	if err != nil {
		return nil, fmt.Errorf("inspect attempt sessions: %w", err)
	}
	type openSession struct {
		id, status, runtimeID string
		createdAt             time.Time
	}
	var open []openSession
	for rows.Next() {
		var s openSession
		if err := rows.Scan(&s.id, &s.status, &s.runtimeID, &s.createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan attempt session: %w", err)
		}
		open = append(open, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read attempt sessions: %w", err)
	}
	rows.Close()
	for _, s := range open {
		if s.status != "STARTING" {
			// RUNNING/PAUSED already owns the attempt; nothing to start.
			return nil, nil
		}
		if time.Since(s.createdAt) < r.cfg.StaleClaimAfter {
			// Another runner is actively starting this session.
			return nil, nil
		}
		// A previous runner died mid-start. Cancel the provider session
		// best-effort when its id was already recorded, then fail the row.
		// Resolution errors only skip the abort — the durable failure row
		// must still commit so the attempt can be re-claimed.
		if s.runtimeID != "" {
			if runtime, err := r.runtimeFor(ctx, &c); err != nil {
				slog.Warn("cannot abort interrupted session: provider unresolved",
					"session", s.id, "provider", c.RuntimeType, "error", err)
			} else {
				abortCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if err := runtime.Abort(abortCtx, s.runtimeID); err != nil {
					slog.Warn("could not abort interrupted runtime session",
						"session", s.id, "error", err)
				}
				cancel()
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_sessions SET status = 'FAILED', ended_at = now()
			WHERE organization_id = $1 AND id = $2 AND status = 'STARTING'`,
			c.OrganizationID, s.id); err != nil {
			return nil, fmt.Errorf("fail interrupted session: %w", err)
		}
		if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
			OrganizationID: c.OrganizationID, ProjectID: c.ProjectID,
			TaskID: c.TaskID, EmployeeID: c.EmployeeID, AttemptID: c.AttemptID,
			SessionID: s.id, WorkspaceID: c.WorkspaceID, Producer: "runtime_adapter",
			Actor:     eventstore.SystemActor("sessionrunner"),
			EventType: "session.failed",
			Data: map[string]any{
				"errorCode": ReasonSessionInterrupted, "retryable": true,
			},
		}); err != nil {
			return nil, fmt.Errorf("record interrupted session event: %w", err)
		}
	}

	var sessionID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_sessions (
			organization_id, project_id, task_id, attempt_id, employee_id,
			workspace_id, runtime_type, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'STARTING')
		RETURNING id::text`,
		c.OrganizationID, c.ProjectID, c.TaskID, c.AttemptID, c.EmployeeID,
		c.WorkspaceID, c.RuntimeType).Scan(&sessionID); err != nil {
		return nil, fmt.Errorf("insert starting session: %w", err)
	}
	c.SessionID = sessionID
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session claim: %w", err)
	}
	return &c, nil
}

// process performs runtime calls outside any transaction, then commits the
// resulting durable state.
func (r *Runner) process(ctx context.Context, claim *claimedWork) {
	opCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.OpTimeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, r.cfg.OpTimeout)
		defer cancel()
	}

	runtimeSessionID, code, detail := r.start(opCtx, claim)
	if code != "" {
		slog.Info("session start failed", "attempt", claim.AttemptID,
			"session", claim.SessionID, "task", claim.TaskID, "code", code)
		retryable := code == ReasonRuntimeUnreachable ||
			code == ReasonRuntimeInvalidReply || code == ReasonSessionInterrupted
		if err := r.fail(ctx, claim, code, detail, retryable); err != nil {
			slog.Error("record session start failure failed", "error", err,
				"attempt", claim.AttemptID)
		}
		return
	}
	if err := r.finalize(ctx, claim, runtimeSessionID); err != nil {
		slog.Error("finalize started session failed", "error", err,
			"attempt", claim.AttemptID)
	}
}

// start verifies the persisted manifest is still the checked-out contract,
// creates the runtime session bound to the worktree, records the provider id
// promptly (shrinking the orphan window after a crash), and sends the
// initial instruction. The worktree ref is workspace-relative; host paths
// leave this process only inside the runtime call, never in events or the DB.
func (r *Runner) start(ctx context.Context, claim *claimedWork) (string, string, string) {
	worktreeRef := filepath.Clean(filepath.FromSlash(claim.WorktreeRef))
	if !filepath.IsLocal(worktreeRef) {
		return "", ReasonInternal, "unsafe worktree reference"
	}
	worktreeDir := filepath.Join(r.cfg.WorkRoot, worktreeRef)

	manifestBytes, err := os.ReadFile(filepath.Join(worktreeDir, filepath.FromSlash(executionmanifest.Path)))
	if errors.Is(err, os.ErrNotExist) {
		return "", ReasonManifestMissing, "repository has no " + executionmanifest.Path
	}
	if err != nil {
		return "", ReasonInternal, "could not read the execution manifest"
	}
	if got := executionmanifest.Digest(manifestBytes); !strings.EqualFold(got, claim.ManifestDigest) {
		return "", ReasonManifestDigest, "checked-out manifest no longer matches the recorded digest"
	}
	manifest, err := executionmanifest.Parse(manifestBytes)
	if err != nil {
		return "", ReasonManifestInvalid, "execution manifest failed validation"
	}
	if manifest.WorkerProfile != claim.WorkerProfile {
		return "", ReasonWorkerProfile, "manifest workerProfile differs from the workspace profile"
	}

	runtime, err := r.runtimeFor(ctx, claim)
	if err != nil {
		return "", providerReason(err), "provider could not be resolved for this attempt"
	}

	runtimeSessionID, err := runtime.CreateSession(ctx, worktreeDir, sessionTitle(claim))
	if err != nil {
		switch runtimeKind(err) {
		case "rejected":
			return "", ReasonRuntimeRejected, "runtime refused the session request"
		case "invalid_response":
			return "", ReasonRuntimeInvalidReply, "runtime returned an unexpected response"
		default:
			return "", ReasonRuntimeUnreachable, "runtime is not reachable or healthy"
		}
	}
	if strings.TrimSpace(runtimeSessionID) == "" {
		return "", ReasonRuntimeInvalidReply, "runtime returned an empty session id"
	}

	// Attach the provider id as soon as it exists so crash reclaim can abort
	// the right upstream session instead of orphaning it.
	attached, err := func() (bool, error) {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_sessions SET runtime_session_id = $3
			WHERE organization_id = $1 AND id = $2 AND status = 'STARTING'`,
			claim.OrganizationID, claim.SessionID, runtimeSessionID)
		if err != nil {
			return false, err
		}
		affected, _ := result.RowsAffected()
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return affected == 1, nil
	}()
	if err != nil {
		return "", ReasonInternal, "could not persist the runtime session id"
	}
	if !attached {
		return "", ReasonInternal, "session claim changed while starting"
	}

	prompt, err := BuildInitialPrompt(claim.TaskTitle, claim.TaskDescription,
		claim.AcceptanceCriteria, claim.EmployeeName, claim.EmployeeRole,
		claim.BranchName, claim.ChangeRequest, &manifest)
	if err != nil {
		return "", ReasonInternal, "could not compose the task instruction"
	}
	if err := runtime.SendPrompt(ctx, runtimeSessionID, prompt); err != nil {
		// The provider session already exists; cancel it best-effort so a
		// failed start does not leave a live unprompted session behind.
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if abortErr := runtime.Abort(abortCtx, runtimeSessionID); abortErr != nil {
			slog.Warn("could not abort provider session after prompt failure",
				"session", claim.SessionID, "runtimeSession", runtimeSessionID,
				"error", abortErr)
		}
		abortCancel()
		switch runtimeKind(err) {
		case "rejected":
			return "", ReasonRuntimeRejected, "runtime refused the task instruction"
		case "invalid_response":
			return "", ReasonRuntimeInvalidReply, "runtime returned an unexpected response"
		default:
			return "", ReasonRuntimeUnreachable, "runtime is not reachable or healthy"
		}
	}
	return runtimeSessionID, "", ""
}

// finalize commits session RUNNING, attempt RUNNING, workspace IN_USE, and
// task IN_PROGRESS with their transition facts under one correlation id.
func (r *Runner) finalize(ctx context.Context, claim *claimedWork, runtimeSessionID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE agent_sessions
		SET status = 'RUNNING', runtime_session_id = $3, started_at = now()
		WHERE organization_id = $1 AND id = $2 AND status = 'STARTING'`,
		claim.OrganizationID, claim.SessionID, runtimeSessionID)
	if err != nil {
		return fmt.Errorf("mark session running: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("session claim changed during start; finalize aborted")
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET state = 'RUNNING', runtime_session_id = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'PROVISIONING'`,
		claim.OrganizationID, claim.AttemptID, runtimeSessionID)
	if err != nil {
		return fmt.Errorf("mark attempt running: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("attempt left PROVISIONING during session start; finalize aborted")
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE workspaces SET state = 'IN_USE', updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND state = 'READY'`,
		claim.OrganizationID, claim.WorkspaceID)
	if err != nil {
		return fmt.Errorf("mark workspace in use: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("workspace left READY during session start; finalize aborted")
	}
	var taskVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'IN_PROGRESS', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3
			AND status IN ('PROVISIONING', 'IN_PROGRESS')
		RETURNING task_version`,
		claim.OrganizationID, claim.ProjectID, claim.TaskID).Scan(&taskVersion)
	if err != nil {
		return fmt.Errorf("mark task in progress: %w", err)
	}

	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return fmt.Errorf("allocate finalize correlation id: %w", err)
	}
	actor := eventstore.SystemActor("sessionrunner")
	fact := eventstore.EventInput{
		OrganizationID: claim.OrganizationID, ProjectID: claim.ProjectID,
		TaskID: claim.TaskID, EmployeeID: claim.EmployeeID, AttemptID: claim.AttemptID,
		SessionID: claim.SessionID, WorkspaceID: claim.WorkspaceID,
		Producer: "runtime_adapter", Actor: actor, CorrelationID: correlationID,
	}
	capabilities := []string{"workspace.write", "checks.run", "preview.capture"}
	sessionEventID, err := eventstore.AppendFact(ctx, tx, withType(fact, "session.started", map[string]any{
		"runtimeType": "opencode", "capabilities": capabilities,
	}))
	if err != nil {
		return fmt.Errorf("record session started event: %w", err)
	}
	fact.CausationID = sessionEventID
	if _, err := eventstore.AppendFact(ctx, tx, withType(fact, "execution_attempt.state_changed", map[string]any{
		"fromState": "PROVISIONING", "toState": "RUNNING",
		"attemptNumber": claim.AttemptNumber, "reasonCode": ReasonSessionStarted,
	})); err != nil {
		return fmt.Errorf("record attempt running event: %w", err)
	}
	if _, err := eventstore.AppendFact(ctx, tx, withType(fact, "workspace.state_changed", map[string]any{
		"fromState": "READY", "toState": "IN_USE",
		"branchName": claim.BranchName, "workerProfile": claim.WorkerProfile,
	})); err != nil {
		return fmt.Errorf("record workspace in-use event: %w", err)
	}
	// A continuation attempt finds the task already IN_PROGRESS (the Owner's
	// request-changes command moved it there); only a real state move emits a
	// task.state_changed fact.
	if claim.TaskStatus != "IN_PROGRESS" {
		if _, err := eventstore.AppendFact(ctx, tx, withType(fact, "task.state_changed", map[string]any{
			"fromState": claim.TaskStatus, "toState": "IN_PROGRESS",
			"taskVersion": taskVersion, "reason": "runtime session started",
		})); err != nil {
			return fmt.Errorf("record task in-progress event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session start: %w", err)
	}
	return nil
}

// fail moves the session and attempt to FAILED and the task to BLOCKED (when
// still PROVISIONING) with durable events. The READY workspace is kept so an
// Owner Retry can reuse the materialized worktree.
func (r *Runner) fail(ctx context.Context, claim *claimedWork, code, detail string, retryable bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failure transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_sessions SET status = 'FAILED', ended_at = now()
		WHERE organization_id = $1 AND id = $2 AND status IN ('STARTING', 'RUNNING', 'PAUSED')`,
		claim.OrganizationID, claim.SessionID); err != nil {
		return fmt.Errorf("mark session failed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET state = 'FAILED', error_code = $3, retryable = $4, ended_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2
			AND state IN ('CREATED', 'PROVISIONING', 'RUNNING', 'WAITING_APPROVAL')`,
		claim.OrganizationID, claim.AttemptID, code, retryable); err != nil {
		return fmt.Errorf("mark attempt failed: %w", err)
	}

	correlationID, err := eventstore.NewUUID()
	if err != nil {
		return fmt.Errorf("allocate failure correlation id: %w", err)
	}
	fact := eventstore.EventInput{
		OrganizationID: claim.OrganizationID, ProjectID: claim.ProjectID,
		TaskID: claim.TaskID, EmployeeID: claim.EmployeeID, AttemptID: claim.AttemptID,
		SessionID: claim.SessionID, WorkspaceID: claim.WorkspaceID,
		Producer: "runtime_adapter", Actor: eventstore.SystemActor("sessionrunner"),
		CorrelationID: correlationID,
	}
	sessionEventID, err := eventstore.AppendFact(ctx, tx, withType(fact, "session.failed", map[string]any{
		"errorCode": code, "retryable": retryable,
	}))
	if err != nil {
		return fmt.Errorf("record session failed event: %w", err)
	}
	fact.CausationID = sessionEventID
	if _, err := eventstore.AppendFact(ctx, tx, withType(fact, "execution_attempt.state_changed", map[string]any{
		"fromState": "PROVISIONING", "toState": "FAILED",
		"attemptNumber": claim.AttemptNumber, "reasonCode": code,
	})); err != nil {
		return fmt.Errorf("record attempt failure event: %w", err)
	}

	var taskVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = 'BLOCKED', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3
			AND status IN ('PROVISIONING', 'IN_PROGRESS')
		RETURNING task_version`,
		claim.OrganizationID, claim.ProjectID, claim.TaskID).Scan(&taskVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Task left a sessionable state during session start (e.g. Cancel);
		// the session/attempt facts above are still recorded.
	case err != nil:
		return fmt.Errorf("mark task blocked: %w", err)
	default:
		if _, err := eventstore.AppendFact(ctx, tx, withType(fact, "task.state_changed", map[string]any{
			"fromState": claim.TaskStatus, "toState": "BLOCKED",
			"taskVersion": taskVersion, "reason": "session start failed: " + code + " — " + detail,
		})); err != nil {
			return fmt.Errorf("record task blocked event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session failure records: %w", err)
	}
	return nil
}

func sessionTitle(claim *claimedWork) string {
	title := "DiOffice " + claim.TaskID + " " + claim.TaskTitle
	if len(title) > 120 {
		title = title[:120]
	}
	return title
}

func withType(input eventstore.EventInput, eventType string, data map[string]any) eventstore.EventInput {
	input.EventType = eventType
	input.Data = data
	return input
}
