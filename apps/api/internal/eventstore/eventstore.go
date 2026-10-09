// Package eventstore is the single durable-fact writer used by API commands
// and background reconcilers. It persists contract-checked agent_events rows
// together with their outbox payload so state changes and normalized events
// commit atomically in the caller's transaction.
package eventstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rendr17/dioffice/apps/api/internal/events"
)

// ErrProjectSequence indicates the project scope does not exist or is not
// ACTIVE, so a durable stream sequence cannot be allocated for it.
var ErrProjectSequence = errors.New("project sequence scope is not available")

// Actor identifies who caused a persisted fact. Owners use {"type":"owner"};
// backend reconcilers use {"type":"system"} with a component id.
type Actor map[string]string

// OwnerActor returns the canonical owner actor for a user id.
func OwnerActor(userID string) Actor { return Actor{"type": "owner", "id": userID} }

// SystemActor returns the canonical actor for backend reconcilers/workers.
func SystemActor(component string) Actor { return Actor{"type": "system", "id": component} }

// EventInput is one normalized fact. IDs may be empty when not applicable;
// they are persisted as NULL. Sequence must come from NextProjectSequence in
// the same transaction.
type EventInput struct {
	OrganizationID string
	ProjectID      string
	TaskID         string
	EmployeeID     string
	AttemptID      string
	SessionID      string
	WorkspaceID    string
	StreamSequence int64
	Producer       string
	Actor          Actor
	CorrelationID  string
	CausationID    string
	EventType      string
	// DedupeKey is an optional deterministic provider-event key; when set,
	// a replayed provider callback is a no-op (ErrDuplicateEvent) instead of
	// a second persisted fact.
	DedupeKey string
	Data      map[string]any
}

// ErrDuplicateEvent is returned when a persisted event already carries the
// same dedupe key in this organization scope.
var ErrDuplicateEvent = errors.New("event with this dedupe key already exists")

// AppendEvent persists one contract-checked fact and returns its id and the
// canonical outbox/SSE envelope. Event data is projected onto the registered
// payload schema and redacted before persistence. Producer and actor type are
// validated against the canonical registry so a writer cannot persist an
// envelope that delivery would later fail closed on.
func AppendEvent(ctx context.Context, tx *sql.Tx, input EventInput) (string, []byte, error) {
	switch input.Producer {
	case "api", "workflow", "gateway", "worker", "runtime_adapter",
		"github_webhook", "reconciler":
	default:
		return "", nil, fmt.Errorf("producer %q is not in the canonical registry", input.Producer)
	}
	switch input.Actor["type"] {
	case "owner", "employee", "system", "integration":
	default:
		return "", nil, fmt.Errorf("actor type %q is not in the canonical registry", input.Actor["type"])
	}
	if input.Actor["id"] == "" {
		return "", nil, errors.New("actor id is required")
	}
	actorJSON, err := json.Marshal(input.Actor)
	if err != nil {
		return "", nil, err
	}
	dataJSON, err := json.Marshal(input.Data)
	if err != nil {
		return "", nil, err
	}
	dataJSON, err = events.SanitizeData(input.EventType, dataJSON)
	if err != nil {
		return "", nil, err
	}
	correlationID := input.CorrelationID
	if correlationID == "" {
		correlationID, err = NewUUID()
		if err != nil {
			return "", nil, err
		}
	}
	var eventID string
	var occurredAt, recordedAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_events (
			schema_version, event_type, organization_id, project_id, task_id, employee_id,
			attempt_id, session_id, workspace_id, stream_sequence, occurred_at,
			producer, actor, correlation_id, causation_id, data, dedupe_key
		) VALUES ('1.0.0', $8, $1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid,
			NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, NULLIF($7, '')::uuid, $9, now(),
			$10, $11::jsonb, $12::uuid, NULLIF($13, '')::uuid, $14::jsonb,
			NULLIF($15, ''))
		RETURNING event_id::text, occurred_at, recorded_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, input.EmployeeID,
		input.AttemptID, input.SessionID, input.WorkspaceID, input.EventType,
		input.StreamSequence, input.Producer, actorJSON, correlationID, input.CausationID,
		dataJSON, input.DedupeKey).Scan(&eventID, &occurredAt, &recordedAt)
	if err != nil {
		if input.DedupeKey != "" && isUniqueViolation(err) {
			return "", nil, ErrDuplicateEvent
		}
		return "", nil, err
	}
	data := map[string]any{}
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		return "", nil, err
	}
	envelope := struct {
		EventID        string            `json:"eventId"`
		SchemaVersion  string            `json:"schemaVersion"`
		EventType      string            `json:"eventType"`
		OrganizationID string            `json:"organizationId"`
		ProjectID      string            `json:"projectId"`
		TaskID         *string           `json:"taskId"`
		EmployeeID     *string           `json:"employeeId"`
		AttemptID      *string           `json:"attemptId"`
		SessionID      *string           `json:"sessionId"`
		WorkspaceID    *string           `json:"workspaceId"`
		StreamSequence int64             `json:"streamSequence"`
		OccurredAt     time.Time         `json:"occurredAt"`
		RecordedAt     time.Time         `json:"recordedAt"`
		Producer       string            `json:"producer"`
		Actor          map[string]string `json:"actor"`
		CorrelationID  string            `json:"correlationId"`
		CausationID    *string           `json:"causationId"`
		Data           map[string]any    `json:"data"`
	}{
		EventID: eventID, SchemaVersion: "1.0.0", EventType: input.EventType,
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID:         optionalString(input.TaskID),
		EmployeeID:     optionalString(input.EmployeeID),
		AttemptID:      optionalString(input.AttemptID),
		SessionID:      optionalString(input.SessionID),
		WorkspaceID:    optionalString(input.WorkspaceID),
		StreamSequence: input.StreamSequence, OccurredAt: occurredAt, RecordedAt: recordedAt,
		Producer: input.Producer, Actor: input.Actor, CorrelationID: correlationID,
		CausationID: optionalString(input.CausationID), Data: data,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", nil, err
	}
	return eventID, payload, nil
}

// EnqueueOutbox stores the canonical payload for a committed event so a
// publisher can deliver it at least once.
func EnqueueOutbox(ctx context.Context, tx *sql.Tx, eventID, projectID string, sequence int64, envelope []byte) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_outbox (event_id, project_id, stream_sequence, payload_json)
		VALUES ($1, $2, $3, $4::jsonb)`, eventID, projectID, sequence, envelope); err != nil {
		return fmt.Errorf("insert event outbox record: %w", err)
	}
	return nil
}

// AppendFact allocates the project stream sequence, persists the event, and
// enqueues its outbox record in one helper for non-idempotent reconciler
// writes that already run inside a transaction.
func AppendFact(ctx context.Context, tx *sql.Tx, input EventInput) (string, error) {
	sequence, err := NextProjectSequence(ctx, tx, input.OrganizationID, input.ProjectID)
	if err != nil {
		return "", err
	}
	input.StreamSequence = sequence
	eventID, envelope, err := AppendEvent(ctx, tx, input)
	if err != nil {
		return "", err
	}
	if err := EnqueueOutbox(ctx, tx, eventID, input.ProjectID, sequence, envelope); err != nil {
		return "", err
	}
	return eventID, nil
}

// NextProjectSequence allocates the next durable stream sequence for an
// ACTIVE project inside the caller's transaction.
func NextProjectSequence(ctx context.Context, tx *sql.Tx, organizationID, projectID string) (int64, error) {
	var sequence int64
	err := tx.QueryRowContext(ctx, `
		UPDATE projects
		SET last_event_sequence = last_event_sequence + 1, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND status = 'ACTIVE'
		RETURNING last_event_sequence`, organizationID, projectID).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrProjectSequence
	}
	if err != nil {
		return 0, fmt.Errorf("allocate project event sequence: %w", err)
	}
	return sequence, nil
}

// NewUUID returns a random RFC 4122 version-4 UUID string.
func NewUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
