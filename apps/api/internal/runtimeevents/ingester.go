package runtimeevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
)

// EventSource opens the provider's live event stream for a workspace
// directory. sessionrunner.OpenCodeRuntime implements it.
type EventSource interface {
	StreamEvents(ctx context.Context, directory string) (io.ReadCloser, error)
}

// Config controls polling cadence and the workspace root used to resolve
// worktree refs into the directory scope the stream is bound to.
type Config struct {
	WorkRoot     string
	PollInterval time.Duration
	// ReconnectDelay bounds the backoff between dropped stream reconnects.
	ReconnectDelay time.Duration
}

// subscription is one live stream consumer for a RUNNING session.
type subscription struct {
	sessionRowID string
	cancel       context.CancelFunc
	done         chan struct{}
}

// Ingester keeps one directory-scoped SSE subscription per RUNNING session
// and persists normalized facts with deterministic dedupe keys.
type Ingester struct {
	db     *sql.DB
	cfg    Config
	events EventSource

	mu   sync.Mutex
	subs map[string]*subscription
}

func New(db *sql.DB, cfg Config, events EventSource) (*Ingester, error) {
	if db == nil {
		return nil, errors.New("event ingester requires a database")
	}
	if cfg.WorkRoot == "" || !filepath.IsAbs(cfg.WorkRoot) {
		return nil, errors.New("event ingester requires an absolute WorkRoot")
	}
	if events == nil {
		return nil, errors.New("event ingester requires an event source")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = 3 * time.Second
	}
	return &Ingester{db: db, cfg: cfg, events: events, subs: map[string]*subscription{}}, nil
}

// Run reconciles subscriptions with RUNNING sessions until ctx is canceled.
func (i *Ingester) Run(ctx context.Context) error {
	for {
		if err := i.reconcile(ctx); err != nil {
			slog.Error("ingester reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			i.stopAll()
			return ctx.Err()
		case <-time.After(i.cfg.PollInterval):
		}
	}
}

// errSessionDone ends consumption when a terminal fact was persisted.
var errSessionDone = errors.New("session reached a terminal state")

type liveSession struct {
	SessionRowID     string
	RuntimeSessionID string
	OrganizationID   string
	ProjectID        string
	TaskID           string
	EmployeeID       string
	AttemptID        string
	WorkspaceID      string
	WorktreeRef      string
}

// reconcile diffs RUNNING sessions against live subscriptions: starts a
// consumer for each new one and cancels consumers whose row ended.
func (i *Ingester) reconcile(ctx context.Context) error {
	rows, err := i.db.QueryContext(ctx, `
		SELECT s.id::text, s.runtime_session_id, s.organization_id::text,
			s.project_id::text, s.task_id::text, s.employee_id::text,
			s.attempt_id::text, w.id::text, w.worktree_ref
		FROM agent_sessions s
		JOIN workspaces w
			ON w.organization_id = s.organization_id AND w.task_id = s.task_id
			AND w.id = s.workspace_id
		WHERE s.status = 'RUNNING' AND s.runtime_session_id IS NOT NULL
			AND w.worktree_ref IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("list running sessions: %w", err)
	}
	var sessions []liveSession
	for rows.Next() {
		var s liveSession
		if err := rows.Scan(&s.SessionRowID, &s.RuntimeSessionID, &s.OrganizationID,
			&s.ProjectID, &s.TaskID, &s.EmployeeID, &s.AttemptID, &s.WorkspaceID,
			&s.WorktreeRef); err != nil {
			rows.Close()
			return fmt.Errorf("scan running session: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	live := map[string]liveSession{}
	for _, s := range sessions {
		live[s.SessionRowID] = s
	}
	i.mu.Lock()
	for id, sub := range i.subs {
		if _, ok := live[id]; !ok {
			sub.cancel()
			delete(i.subs, id)
		}
	}
	for _, s := range sessions {
		if _, ok := i.subs[s.SessionRowID]; ok {
			continue
		}
		worktreeRef := filepath.Clean(filepath.FromSlash(s.WorktreeRef))
		if !filepath.IsLocal(worktreeRef) {
			slog.Error("unsafe worktree ref on running session", "session", s.SessionRowID)
			continue
		}
		directory := filepath.Join(i.cfg.WorkRoot, worktreeRef)
		subCtx, cancel := context.WithCancel(ctx)
		sub := &subscription{sessionRowID: s.SessionRowID, cancel: cancel, done: make(chan struct{})}
		i.subs[s.SessionRowID] = sub
		go i.consume(subCtx, sub, s, directory)
	}
	i.mu.Unlock()
	return nil
}

func (i *Ingester) stopAll() {
	i.mu.Lock()
	defer i.mu.Unlock()
	for id, sub := range i.subs {
		sub.cancel()
		delete(i.subs, id)
	}
}

// consume reads one session's stream, reconnecting while the session stays
// RUNNING. Terminal facts end the subscription; the next reconcile pass sees
// the row left RUNNING and removes it.
func (i *Ingester) consume(ctx context.Context, sub *subscription, s liveSession, directory string) {
	defer close(sub.done)
	defer func() {
		i.mu.Lock()
		delete(i.subs, s.SessionRowID)
		i.mu.Unlock()
	}()
	for {
		err := i.streamOnce(ctx, s, directory)
		if errors.Is(err, context.Canceled) || errors.Is(err, errSessionDone) {
			return
		}
		if err != nil && !errors.Is(err, io.EOF) {
			slog.Warn("session event stream dropped", "session", s.SessionRowID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(i.cfg.ReconnectDelay):
		}
	}
}

func (i *Ingester) streamOnce(ctx context.Context, s liveSession, directory string) error {
	body, err := i.events.StreamEvents(ctx, directory)
	if err != nil {
		return err
	}
	defer body.Close()
	return readSSEData(ctx, body, func(frame []byte) error {
		var peek struct {
			Properties struct {
				SessionID string `json:"sessionID"`
				Part      struct {
					SessionID string `json:"sessionID"`
				} `json:"part"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(frame, &peek); err == nil {
			sid := peek.Properties.SessionID
			if sid == "" {
				sid = peek.Properties.Part.SessionID
			}
			if sid != "" && sid != s.RuntimeSessionID {
				return nil // another session's activity in the same directory
			}
		}
		for _, fact := range Normalize(frame) {
			terminal, err := i.persist(ctx, s, fact)
			if err != nil {
				slog.Error("persist runtime fact failed", "session", s.SessionRowID,
					"eventType", fact.EventType, "error", err)
				continue
			}
			if terminal {
				return errSessionDone
			}
		}
		return nil
	})
}

// persist writes one normalized fact with its dedupe key, applying terminal
// session transitions in the same transaction. Returns true when the session
// reached a terminal state and consumption should stop.
func (i *Ingester) persist(ctx context.Context, s liveSession, f Fact) (bool, error) {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin fact transaction: %w", err)
	}
	defer tx.Rollback()

	fact := eventstore.EventInput{
		OrganizationID: s.OrganizationID, ProjectID: s.ProjectID,
		TaskID: s.TaskID, EmployeeID: s.EmployeeID, AttemptID: s.AttemptID,
		SessionID: s.SessionRowID, WorkspaceID: s.WorkspaceID,
		Producer: "runtime_adapter", Actor: eventstore.SystemActor("runtime-ingester"),
		EventType: f.EventType,
		DedupeKey: "opencode:" + s.SessionRowID + ":" + f.DedupeKey,
		Data:      f.Data,
	}
	eventID, err := eventstore.AppendFact(ctx, tx, fact)
	if errors.Is(err, eventstore.ErrDuplicateEvent) {
		return false, nil // replayed provider callback: no second fact
	}
	if err != nil {
		return false, fmt.Errorf("append runtime fact: %w", err)
	}

	terminal := false
	switch {
	case f.Complete:
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_sessions SET status = 'COMPLETED', ended_at = now()
			WHERE organization_id = $1 AND id = $2 AND status IN ('STARTING', 'RUNNING', 'PAUSED')`,
			s.OrganizationID, s.SessionRowID); err != nil {
			return false, fmt.Errorf("mark session completed: %w", err)
		}
		terminal = true
	case f.Failed != "":
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_sessions SET status = 'FAILED', ended_at = now()
			WHERE organization_id = $1 AND id = $2 AND status IN ('STARTING', 'RUNNING', 'PAUSED')`,
			s.OrganizationID, s.SessionRowID); err != nil {
			return false, fmt.Errorf("mark session failed: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE execution_attempts
			SET state = 'FAILED', error_code = $3, retryable = true, ended_at = now(), updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND state IN ('RUNNING', 'WAITING_APPROVAL')`,
			s.OrganizationID, s.AttemptID, f.Failed); err != nil {
			return false, fmt.Errorf("mark attempt failed: %w", err)
		}
		var attemptNumber int64
		_ = tx.QueryRowContext(ctx,
			`SELECT attempt_number FROM execution_attempts WHERE id = $1`, s.AttemptID).Scan(&attemptNumber)
		if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
			OrganizationID: s.OrganizationID, ProjectID: s.ProjectID,
			TaskID: s.TaskID, EmployeeID: s.EmployeeID, AttemptID: s.AttemptID,
			SessionID: s.SessionRowID, WorkspaceID: s.WorkspaceID,
			Producer: "runtime_adapter", Actor: eventstore.SystemActor("runtime-ingester"),
			CorrelationID: "", CausationID: eventID,
			EventType: "execution_attempt.state_changed",
			Data: map[string]any{
				"fromState": "RUNNING", "toState": "FAILED",
				"attemptNumber": attemptNumber, "reasonCode": f.Failed,
			},
		}); err != nil {
			return false, fmt.Errorf("record attempt failure event: %w", err)
		}
		var taskVersion int64
		err = tx.QueryRowContext(ctx, `
			UPDATE tasks SET status = 'FAILED', task_version = task_version + 1, updated_at = now()
			WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND status = 'IN_PROGRESS'
			RETURNING task_version`,
			s.OrganizationID, s.ProjectID, s.TaskID).Scan(&taskVersion)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return false, fmt.Errorf("mark task failed: %w", err)
		default:
			if _, err := eventstore.AppendFact(ctx, tx, eventstore.EventInput{
				OrganizationID: s.OrganizationID, ProjectID: s.ProjectID,
				TaskID: s.TaskID, EmployeeID: s.EmployeeID, AttemptID: s.AttemptID,
				SessionID: s.SessionRowID, WorkspaceID: s.WorkspaceID,
				Producer: "runtime_adapter", Actor: eventstore.SystemActor("runtime-ingester"),
				CausationID: eventID,
				EventType:   "task.state_changed",
				Data: map[string]any{
					"fromState": "IN_PROGRESS", "toState": "FAILED",
					"taskVersion": taskVersion, "reason": "runtime session error",
				},
			}); err != nil {
				return false, fmt.Errorf("record task failure event: %w", err)
			}
		}
		terminal = true
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit runtime fact: %w", err)
	}
	return terminal, nil
}
