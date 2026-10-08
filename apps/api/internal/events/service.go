// Package events reads committed, project-scoped facts; it does not publish the outbox.
package events

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrProjectNotFound = errors.New("project not found")
	ErrInvalidQuery    = errors.New("invalid event query")
	ErrInvalidCursor   = errors.New("invalid event cursor")
	ErrCursorAhead     = errors.New("event cursor ahead")
	ErrCursorExpired   = errors.New("event cursor expired")
)

type Envelope struct {
	EventID        string          `json:"eventId"`
	SchemaVersion  string          `json:"schemaVersion"`
	EventType      string          `json:"eventType"`
	OrganizationID string          `json:"organizationId"`
	ProjectID      string          `json:"projectId"`
	TaskID         *string         `json:"taskId"`
	EmployeeID     *string         `json:"employeeId"`
	AttemptID      *string         `json:"attemptId"`
	SessionID      *string         `json:"sessionId"`
	WorkspaceID    *string         `json:"workspaceId"`
	StreamSequence int64           `json:"streamSequence"`
	OccurredAt     time.Time       `json:"occurredAt"`
	RecordedAt     time.Time       `json:"recordedAt"`
	Producer       string          `json:"producer"`
	Actor          json.RawMessage `json:"actor"`
	CorrelationID  string          `json:"correlationId"`
	CausationID    *string         `json:"causationId"`
	Data           json.RawMessage `json:"data"`
}

type Page struct {
	Items      []Envelope `json:"items"`
	NextCursor string     `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
}

// CursorError includes only a snapshot of an already-authorized active project.
type CursorError struct {
	Cause    error
	Snapshot Page
}

func (e *CursorError) Error() string { return e.Cause.Error() }
func (e *CursorError) Unwrap() error { return e.Cause }

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// CheckProject revalidates active tenant membership without returning a head or payload.
func (s *Service) CheckProject(ctx context.Context, organizationID, projectID string) error {
	if !validUUID(organizationID) || !validUUID(projectID) {
		return ErrInvalidQuery
	}
	if s == nil || s.db == nil {
		return errors.New("event database unavailable")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM projects WHERE organization_id = $1 AND id = $2 AND status = 'ACTIVE'
	)`, organizationID, projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrProjectNotFound
	}
	return nil
}

// Recent returns one consistent latest-100 snapshot, ordered by durable sequence.
func (s *Service) Recent(ctx context.Context, organizationID, projectID string) (Page, error) {
	return s.read(ctx, organizationID, projectID, nil, 100)
}

// After reads a bounded ascending batch strictly after the supplied durable cursor.
func (s *Service) After(ctx context.Context, organizationID, projectID string, after int64, limit int) (Page, error) {
	return s.read(ctx, organizationID, projectID, &after, limit)
}

func (s *Service) read(ctx context.Context, organizationID, projectID string, after *int64, limit int) (Page, error) {
	if !validUUID(organizationID) || !validUUID(projectID) || limit < 1 || limit > 100 {
		return Page{}, ErrInvalidQuery
	}
	if after != nil && *after < 0 {
		return Page{}, ErrInvalidCursor
	}
	if s == nil || s.db == nil {
		return Page{}, errors.New("event database unavailable")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	var head int64
	err = tx.QueryRowContext(ctx, `SELECT last_event_sequence FROM projects
		WHERE organization_id = $1 AND id = $2 AND status = 'ACTIVE'`, organizationID, projectID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, ErrProjectNotFound
	}
	if err != nil {
		return Page{}, err
	}
	page := Page{NextCursor: strconv.FormatInt(head, 10)}
	var cursorErr error
	if after != nil {
		if *after > head {
			cursorErr = ErrCursorAhead
		} else if *after > 0 {
			var retainedMinimum sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT min(stream_sequence) FROM agent_events
				WHERE organization_id = $1 AND project_id = $2 AND stream_sequence <= $3`, organizationID, projectID, head).Scan(&retainedMinimum); err != nil {
				return Page{}, err
			}
			// Subtract from the positive minimum rather than overflowing after+1.
			if (retainedMinimum.Valid && *after < retainedMinimum.Int64-1) || (!retainedMinimum.Valid && *after < head) {
				cursorErr = ErrCursorExpired
			}
		}
	}
	if cursorErr != nil {
		page.Items, err = readRecent(ctx, tx, organizationID, projectID, head)
		if err != nil {
			return Page{}, err
		}
		if err := tx.Commit(); err != nil {
			return Page{}, err
		}
		return Page{}, &CursorError{Cause: cursorErr, Snapshot: page}
	}
	if after == nil {
		page.Items, err = readRecent(ctx, tx, organizationID, projectID, head)
	} else {
		rows, queryErr := tx.QueryContext(ctx, `SELECT `+eventColumns+` FROM agent_events
			WHERE organization_id = $1 AND project_id = $2 AND stream_sequence > $3 AND stream_sequence <= $4
			ORDER BY stream_sequence ASC LIMIT $5`, organizationID, projectID, *after, head, limit+1)
		if queryErr != nil {
			return Page{}, queryErr
		}
		page.Items, err = scanEvents(rows)
		if err != nil {
			return Page{}, err
		}
		page.HasMore = len(page.Items) > limit
		if page.HasMore {
			page.Items = page.Items[:limit]
		}
		page.NextCursor = strconv.FormatInt(*after, 10)
		if len(page.Items) > 0 {
			page.NextCursor = strconv.FormatInt(page.Items[len(page.Items)-1].StreamSequence, 10)
		}
	}
	if err != nil {
		return Page{}, err
	}
	if err := tx.Commit(); err != nil {
		return Page{}, err
	}
	return page, nil
}

const eventColumns = `event_id::text, schema_version, event_type, organization_id::text, project_id::text,
	task_id::text, employee_id::text, attempt_id::text, session_id::text, workspace_id::text,
	stream_sequence, occurred_at, recorded_at, producer,
	CASE WHEN octet_length(actor::text) <= 1024 THEN actor ELSE NULL END,
	correlation_id::text, causation_id::text,
	CASE WHEN octet_length(data::text) <= 2097152 THEN data ELSE NULL END`

func validUUID(raw string) bool {
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(raw, "-", ""))
	return err == nil && len(decoded) == 16
}

func readRecent(ctx context.Context, tx *sql.Tx, organizationID, projectID string, head int64) ([]Envelope, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+eventColumns+` FROM agent_events
		WHERE organization_id = $1 AND project_id = $2 AND stream_sequence <= $3
		ORDER BY stream_sequence DESC LIMIT 100`, organizationID, projectID, head)
	if err != nil {
		return nil, err
	}
	items, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

func scanEvents(rows *sql.Rows) ([]Envelope, error) {
	defer rows.Close()
	items := make([]Envelope, 0)
	for rows.Next() {
		var e Envelope
		if err := rows.Scan(&e.EventID, &e.SchemaVersion, &e.EventType, &e.OrganizationID, &e.ProjectID,
			&e.TaskID, &e.EmployeeID, &e.AttemptID, &e.SessionID, &e.WorkspaceID, &e.StreamSequence,
			&e.OccurredAt, &e.RecordedAt, &e.Producer, &e.Actor, &e.CorrelationID, &e.CausationID, &e.Data); err != nil {
			return nil, err
		}
		e.OccurredAt, e.RecordedAt = e.OccurredAt.UTC(), e.RecordedAt.UTC()
		if err := sanitizeEnvelope(&e); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}
