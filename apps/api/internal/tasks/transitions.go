package tasks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/events"
)

const (
	saveToBacklogOperation   = "tasks.save_to_backlog"
	transitionResponseStatus = 200
)

var (
	ErrTaskNotFound        = errors.New("task not found")
	ErrInvalidTransition   = errors.New("invalid task state transition")
	ErrTaskVersionConflict = errors.New("task version conflict")
)

type SaveToBacklogInput struct {
	OrganizationID  string
	ProjectID       string
	TaskID          string
	ActorUserID     string
	IdempotencyKey  string
	ExpectedVersion int64
}

// SaveToBacklog records the explicit DRAFT -> BACKLOG transition without
// provisioning a worker or starting a runtime.
func (s *Service) SaveToBacklog(ctx context.Context, input SaveToBacklogInput) (Task, bool, error) {
	if !validUUID(input.OrganizationID) || !validUUID(input.ProjectID) ||
		!validUUID(input.TaskID) || !validUUID(input.ActorUserID) {
		return Task{}, false, fmt.Errorf("%w: organizationId, projectId, taskId, and actorUserId must be UUIDs", ErrInvalidInput)
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 {
		return Task{}, false, fmt.Errorf("%w: idempotencyKey must contain 1 to 128 characters", ErrInvalidInput)
	}
	if input.ExpectedVersion < 1 {
		return Task{}, false, fmt.Errorf("%w: expectedVersion must be a positive integer", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}

	requestHash, err := hashSaveToBacklogRequest(input)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash task backlog request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task backlog transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimSaveToBacklogIdempotency(ctx, tx, input, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent backlog replay: %w", err)
		}
		return task, true, nil
	}

	task, err = loadTaskForBacklogTransition(ctx, tx, input)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for backlog transition: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	if task.Status != "DRAFT" {
		return Task{}, false, ErrInvalidTransition
	}

	var sequence int64
	err = tx.QueryRowContext(ctx, `
		UPDATE projects
		SET last_event_sequence = last_event_sequence + 1, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND status = 'ACTIVE'
		RETURNING last_event_sequence`, input.OrganizationID, input.ProjectID).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrProjectNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate backlog event sequence: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE tasks
		SET status = 'BACKLOG', task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND task_version = $4
		RETURNING task_version, updated_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion).
		Scan(&task.Version, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskVersionConflict
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("update task to backlog: %w", err)
	}
	task.Status = "BACKLOG"

	eventID, envelope, err := insertTaskStateChangedEvent(ctx, tx, input, task, "DRAFT", "BACKLOG", sequence)
	if err != nil {
		return Task{}, false, fmt.Errorf("insert task state event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_outbox (event_id, project_id, stream_sequence, payload_json)
		VALUES ($1, $2, $3, $4::jsonb)`, eventID, input.ProjectID, sequence, envelope); err != nil {
		return Task{}, false, fmt.Errorf("insert task state event outbox record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_records (
			organization_id, project_id, actor_user_id, action, target_type, target_id, outcome
		) VALUES ($1, $2, $3, 'task.state_change', 'task', $4, 'success')`,
		input.OrganizationID, input.ProjectID, input.ActorUserID, input.TaskID); err != nil {
		return Task{}, false, fmt.Errorf("insert task state audit record: %w", err)
	}

	responseJSON, err := json.Marshal(task)
	if err != nil {
		return Task{}, false, fmt.Errorf("encode backlog task response: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET response_status = $1, response_json = $2::jsonb
		WHERE organization_id = $3 AND actor_user_id = $4
			AND operation = $5 AND idempotency_key = $6 AND request_hash = $7`,
		transitionResponseStatus, responseJSON, input.OrganizationID, input.ActorUserID,
		saveToBacklogOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, fmt.Errorf("store idempotent backlog response: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Task{}, false, fmt.Errorf("check backlog idempotency response update: %w", err)
	} else if affected != 1 {
		return Task{}, false, errors.New("backlog idempotency record disappeared before commit")
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit task backlog transition: %w", err)
	}
	return task, false, nil
}

func hashSaveToBacklogRequest(input SaveToBacklogInput) (string, error) {
	canonical, err := json.Marshal(struct {
		OrganizationID  string `json:"organizationId"`
		ProjectID       string `json:"projectId"`
		TaskID          string `json:"taskId"`
		ExpectedVersion int64  `json:"expectedVersion"`
	}{input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func claimSaveToBacklogIdempotency(ctx context.Context, tx *sql.Tx, input SaveToBacklogInput, requestHash string) (Task, bool, error) {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM idempotency_keys
		WHERE organization_id = $1 AND actor_user_id = $2 AND operation = $3
			AND idempotency_key = $4 AND expires_at <= now()`,
		input.OrganizationID, input.ActorUserID, saveToBacklogOperation, input.IdempotencyKey); err != nil {
		return Task{}, false, fmt.Errorf("remove expired backlog idempotency record: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_keys (
			organization_id, actor_user_id, operation, idempotency_key, request_hash, expires_at
		) VALUES ($1, $2, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, actor_user_id, operation, idempotency_key) DO NOTHING`,
		input.OrganizationID, input.ActorUserID, saveToBacklogOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, fmt.Errorf("claim backlog idempotency key: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Task{}, false, fmt.Errorf("check backlog idempotency claim: %w", err)
	}
	if inserted == 1 {
		return Task{}, false, nil
	}

	var storedHash string
	var responseStatus sql.NullInt64
	var responseJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT request_hash, response_status, response_json
		FROM idempotency_keys
		WHERE organization_id = $1 AND actor_user_id = $2 AND operation = $3 AND idempotency_key = $4
		FOR UPDATE`, input.OrganizationID, input.ActorUserID, saveToBacklogOperation, input.IdempotencyKey).
		Scan(&storedHash, &responseStatus, &responseJSON)
	if err != nil {
		return Task{}, false, fmt.Errorf("read backlog idempotency result: %w", err)
	}
	if storedHash != requestHash {
		return Task{}, false, ErrIdempotencyConflict
	}
	if !responseStatus.Valid || responseStatus.Int64 != transitionResponseStatus || len(responseJSON) == 0 {
		return Task{}, false, ErrIdempotencyInProgress
	}
	var task Task
	if err := json.Unmarshal(responseJSON, &task); err != nil {
		return Task{}, false, fmt.Errorf("decode backlog idempotency response: %w", err)
	}
	return task, true, nil
}

func loadTaskForBacklogTransition(ctx context.Context, tx *sql.Tx, input SaveToBacklogInput) (Task, error) {
	var task Task
	var manifestDigest sql.NullString
	var referenceImagesJSON []byte
	err := tx.QueryRowContext(ctx, `
		SELECT t.id::text, t.organization_id::text, t.project_id::text,
			t.assignee_employee_id::text, t.created_by_user_id::text, t.title, t.description,
			t.acceptance_criteria, t.required_checks, t.manifest_digest, t.task_type, t.priority,
			t.status, t.task_version, t.created_at, t.updated_at,
			COALESCE((
				SELECT jsonb_agg(jsonb_build_object(
					'id', image.id::text, 'fileName', image.file_name,
					'contentType', image.content_type, 'sizeBytes', image.size_bytes
				) ORDER BY image.created_at, image.id)
				FROM task_reference_images image
				WHERE image.organization_id = t.organization_id AND image.task_id = t.id
			), '[]'::jsonb)
		FROM tasks t
		WHERE t.organization_id = $1 AND t.project_id = $2 AND t.id = $3
		FOR UPDATE OF t`, input.OrganizationID, input.ProjectID, input.TaskID).Scan(
		&task.ID, &task.OrganizationID, &task.ProjectID, &task.AssigneeEmployeeID,
		&task.CreatedByUserID, &task.Title, &task.Description, &task.AcceptanceCriteria,
		&task.RequiredChecks, &manifestDigest, &task.TaskType, &task.Priority,
		&task.Status, &task.Version, &task.CreatedAt, &task.UpdatedAt, &referenceImagesJSON)
	if err != nil {
		return Task{}, err
	}
	if manifestDigest.Valid {
		task.ManifestDigest = manifestDigest.String
	}
	if err := json.Unmarshal(referenceImagesJSON, &task.ReferenceImages); err != nil {
		return Task{}, fmt.Errorf("decode task reference images for transition: %w", err)
	}
	return task, nil
}

func insertTaskStateChangedEvent(ctx context.Context, tx *sql.Tx, input SaveToBacklogInput, task Task, fromState, toState string, sequence int64) (string, []byte, error) {
	actor := map[string]string{"type": "owner", "id": input.ActorUserID}
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return "", nil, err
	}
	dataJSON, err := json.Marshal(map[string]any{
		"fromState": fromState, "toState": toState,
		"taskVersion": task.Version, "reason": "owner_saved_to_backlog",
	})
	if err != nil {
		return "", nil, err
	}
	dataJSON, err = events.SanitizeData("task.state_changed", dataJSON)
	if err != nil {
		return "", nil, err
	}
	var eventID, correlationID string
	var occurredAt, recordedAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_events (
			schema_version, event_type, organization_id, project_id, task_id, employee_id,
			stream_sequence, occurred_at, producer, actor, correlation_id, data
		) VALUES ('1.0.0', 'task.state_changed', $1, $2, $3, $4, $5, now(), 'api', $6::jsonb, gen_random_uuid(), $7::jsonb)
		RETURNING event_id::text, correlation_id::text, occurred_at, recorded_at`,
		input.OrganizationID, input.ProjectID, task.ID, task.AssigneeEmployeeID,
		sequence, actorJSON, dataJSON).Scan(&eventID, &correlationID, &occurredAt, &recordedAt)
	if err != nil {
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
		EventID: eventID, SchemaVersion: "1.0.0", EventType: "task.state_changed",
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: stringPointer(task.ID), EmployeeID: stringPointer(task.AssigneeEmployeeID),
		StreamSequence: sequence, OccurredAt: occurredAt, RecordedAt: recordedAt,
		Producer: "api", Actor: actor, CorrelationID: correlationID,
	}
	if err := json.Unmarshal(dataJSON, &envelope.Data); err != nil {
		return "", nil, err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", nil, err
	}
	return eventID, payload, nil
}
