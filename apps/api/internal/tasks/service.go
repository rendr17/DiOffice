package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	createTaskOperation   = "tasks.create"
	createdResponseStatus = 201
)

var (
	ErrInvalidInput          = errors.New("invalid task input")
	ErrProjectNotFound       = errors.New("project not found")
	ErrIdempotencyConflict   = errors.New("idempotency key reused with different request")
	ErrIdempotencyInProgress = errors.New("idempotent request has no stored result")
)

type CreateDraftInput struct {
	OrganizationID     string
	ProjectID          string
	ActorUserID        string
	AssigneeEmployeeID string
	IdempotencyKey     string
	Title              string
	Description        string
	AcceptanceCriteria json.RawMessage
	RequiredChecks     json.RawMessage
	ManifestDigest     string
	TaskType           string
	Priority           string
}

type Task struct {
	ID                 string          `json:"id"`
	OrganizationID     string          `json:"organizationId"`
	ProjectID          string          `json:"projectId"`
	AssigneeEmployeeID string          `json:"assigneeEmployeeId"`
	CreatedByUserID    string          `json:"createdByUserId"`
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	AcceptanceCriteria json.RawMessage `json:"acceptanceCriteria"`
	RequiredChecks     json.RawMessage `json:"requiredChecks"`
	ManifestDigest     string          `json:"manifestDigest,omitempty"`
	TaskType           string          `json:"taskType"`
	Priority           string          `json:"priority"`
	Status             string          `json:"status"`
	Version            int64           `json:"version"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// ListByProject returns the newest 100 tasks visible to the supplied organization.
func (s *Service) ListByProject(ctx context.Context, organizationID, projectID string) ([]Task, error) {
	if !validUUID(organizationID) || !validUUID(projectID) {
		return nil, fmt.Errorf("%w: organizationId and projectId must be UUIDs", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return nil, errors.New("task service database is not configured")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM projects WHERE organization_id = $1 AND id = $2
		)`, organizationID, projectID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check project for task list: %w", err)
	}
	if !exists {
		return nil, ErrProjectNotFound
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, organization_id::text, project_id::text,
			assignee_employee_id::text, created_by_user_id::text, title, description,
			acceptance_criteria, required_checks, manifest_digest, task_type, priority,
			status, task_version, created_at, updated_at
		FROM tasks
		WHERE organization_id = $1 AND project_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 100`, organizationID, projectID)
	if err != nil {
		return nil, fmt.Errorf("query tasks for project: %w", err)
	}
	defer rows.Close()

	tasks := make([]Task, 0)
	for rows.Next() {
		var task Task
		var manifestDigest sql.NullString
		if err := rows.Scan(
			&task.ID, &task.OrganizationID, &task.ProjectID, &task.AssigneeEmployeeID,
			&task.CreatedByUserID, &task.Title, &task.Description,
			&task.AcceptanceCriteria, &task.RequiredChecks, &manifestDigest,
			&task.TaskType, &task.Priority, &task.Status, &task.Version,
			&task.CreatedAt, &task.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan project task: %w", err)
		}
		if manifestDigest.Valid {
			task.ManifestDigest = manifestDigest.String
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project tasks: %w", err)
	}
	return tasks, nil
}

// CreateDraft persists a DRAFT task and its event, outbox, audit, and idempotency
// result in one transaction. ActorUserID must come from authenticated server context.
func (s *Service) CreateDraft(ctx context.Context, input CreateDraftInput) (Task, bool, error) {
	input, err := normalizeAndValidate(input)
	if err != nil {
		return Task{}, false, err
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}

	requestHash, err := hashRequest(input)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash task request: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task transaction: %w", err)
	}
	defer tx.Rollback()

	replayedTask, replayed, err := claimIdempotency(ctx, tx, input, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent task replay: %w", err)
		}
		return replayedTask, true, nil
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
		return Task{}, false, fmt.Errorf("allocate project event sequence: %w", err)
	}

	task, err := insertTask(ctx, tx, input)
	if err != nil {
		return Task{}, false, fmt.Errorf("insert draft task: %w", err)
	}

	eventID, envelope, err := insertTaskCreatedEvent(ctx, tx, input, task, sequence)
	if err != nil {
		return Task{}, false, fmt.Errorf("insert task-created event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_outbox (event_id, project_id, stream_sequence, payload_json)
		VALUES ($1, $2, $3, $4::jsonb)`, eventID, input.ProjectID, sequence, envelope); err != nil {
		return Task{}, false, fmt.Errorf("insert task event outbox record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_records (
			organization_id, project_id, actor_user_id, action, target_type, target_id, outcome
		) VALUES ($1, $2, $3, 'task.create', 'task', $4, 'success')`,
		input.OrganizationID, input.ProjectID, input.ActorUserID, task.ID); err != nil {
		return Task{}, false, fmt.Errorf("insert task audit record: %w", err)
	}

	responseJSON, err := json.Marshal(task)
	if err != nil {
		return Task{}, false, fmt.Errorf("encode task response: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET response_status = $1, response_json = $2::jsonb
		WHERE organization_id = $3 AND actor_user_id = $4
			AND operation = $5 AND idempotency_key = $6 AND request_hash = $7`,
		createdResponseStatus, responseJSON, input.OrganizationID, input.ActorUserID,
		createTaskOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, fmt.Errorf("store idempotent task response: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Task{}, false, fmt.Errorf("check idempotency response update: %w", err)
	} else if affected != 1 {
		return Task{}, false, errors.New("idempotency record disappeared before commit")
	}

	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit draft task: %w", err)
	}
	return task, false, nil
}

func normalizeAndValidate(input CreateDraftInput) (CreateDraftInput, error) {
	for name, value := range map[string]string{
		"organizationId":     input.OrganizationID,
		"projectId":          input.ProjectID,
		"actorUserId":        input.ActorUserID,
		"assigneeEmployeeId": input.AssigneeEmployeeID,
	} {
		if !validUUID(value) {
			return CreateDraftInput{}, fmt.Errorf("%w: %s must be a UUID", ErrInvalidInput, name)
		}
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 {
		return CreateDraftInput{}, fmt.Errorf("%w: idempotencyKey must contain 1 to 128 characters", ErrInvalidInput)
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 200 {
		return CreateDraftInput{}, fmt.Errorf("%w: title must contain 1 to 200 characters", ErrInvalidInput)
	}
	if utf8.RuneCountInString(input.Description) > 20000 {
		return CreateDraftInput{}, fmt.Errorf("%w: description must not exceed 20000 characters", ErrInvalidInput)
	}
	input.TaskType = strings.TrimSpace(input.TaskType)
	if input.TaskType == "" {
		return CreateDraftInput{}, fmt.Errorf("%w: taskType is required", ErrInvalidInput)
	}
	switch input.Priority {
	case "LOW", "NORMAL", "HIGH", "URGENT":
	default:
		return CreateDraftInput{}, fmt.Errorf("%w: priority is not canonical", ErrInvalidInput)
	}
	var err error
	input.AcceptanceCriteria, err = canonicalAcceptanceCriteria(input.AcceptanceCriteria)
	if err != nil {
		return CreateDraftInput{}, fmt.Errorf("%w: acceptanceCriteria does not match the event contract: %v", ErrInvalidInput, err)
	}
	input.RequiredChecks, err = canonicalJSONArray(input.RequiredChecks)
	if err != nil {
		return CreateDraftInput{}, fmt.Errorf("%w: requiredChecks must be a JSON array: %v", ErrInvalidInput, err)
	}
	if input.ManifestDigest != "" && !isSHA256(input.ManifestDigest) {
		return CreateDraftInput{}, fmt.Errorf("%w: manifestDigest must be a SHA-256 digest", ErrInvalidInput)
	}
	return input, nil
}

func canonicalJSONArray(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`[]`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values []any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	canonical, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func canonicalAcceptanceCriteria(raw json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicalJSONArray(raw)
	if err != nil {
		return nil, err
	}
	var criteria []string
	if err := json.Unmarshal(canonical, &criteria); err != nil {
		return nil, err
	}
	if len(criteria) > 100 {
		return nil, errors.New("must contain at most 100 criteria")
	}
	for index, criterion := range criteria {
		length := utf8.RuneCountInString(criterion)
		if length < 1 || length > 2000 {
			return nil, fmt.Errorf("criterion %d must contain 1 to 2000 characters", index)
		}
	}
	return json.Marshal(criteria)
}

func hashRequest(input CreateDraftInput) (string, error) {
	canonical, err := json.Marshal(struct {
		OrganizationID     string          `json:"organizationId"`
		ProjectID          string          `json:"projectId"`
		AssigneeEmployeeID string          `json:"assigneeEmployeeId"`
		Title              string          `json:"title"`
		Description        string          `json:"description"`
		AcceptanceCriteria json.RawMessage `json:"acceptanceCriteria"`
		RequiredChecks     json.RawMessage `json:"requiredChecks"`
		ManifestDigest     string          `json:"manifestDigest,omitempty"`
		TaskType           string          `json:"taskType"`
		Priority           string          `json:"priority"`
	}{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		AssigneeEmployeeID: input.AssigneeEmployeeID, Title: input.Title,
		Description: input.Description, AcceptanceCriteria: input.AcceptanceCriteria,
		RequiredChecks: input.RequiredChecks, ManifestDigest: input.ManifestDigest,
		TaskType: input.TaskType, Priority: input.Priority,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func claimIdempotency(ctx context.Context, tx *sql.Tx, input CreateDraftInput, requestHash string) (Task, bool, error) {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM idempotency_keys
		WHERE organization_id = $1 AND actor_user_id = $2 AND operation = $3
			AND idempotency_key = $4 AND expires_at <= now()`,
		input.OrganizationID, input.ActorUserID, createTaskOperation, input.IdempotencyKey); err != nil {
		return Task{}, false, fmt.Errorf("remove expired idempotency record: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_keys (
			organization_id, actor_user_id, operation, idempotency_key, request_hash, expires_at
		) VALUES ($1, $2, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, actor_user_id, operation, idempotency_key) DO NOTHING`,
		input.OrganizationID, input.ActorUserID, createTaskOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Task{}, false, fmt.Errorf("check idempotency claim: %w", err)
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
		FOR UPDATE`, input.OrganizationID, input.ActorUserID, createTaskOperation, input.IdempotencyKey).
		Scan(&storedHash, &responseStatus, &responseJSON)
	if err != nil {
		return Task{}, false, fmt.Errorf("read idempotency result: %w", err)
	}
	if storedHash != requestHash {
		return Task{}, false, ErrIdempotencyConflict
	}
	if !responseStatus.Valid || responseStatus.Int64 != createdResponseStatus || len(responseJSON) == 0 {
		return Task{}, false, ErrIdempotencyInProgress
	}
	var task Task
	if err := json.Unmarshal(responseJSON, &task); err != nil {
		return Task{}, false, fmt.Errorf("decode idempotency response: %w", err)
	}
	return task, true, nil
}

func insertTask(ctx context.Context, tx *sql.Tx, input CreateDraftInput) (Task, error) {
	var task Task
	var manifestDigest sql.NullString
	err := tx.QueryRowContext(ctx, `
		INSERT INTO tasks (
			organization_id, project_id, assignee_employee_id, title, description,
			acceptance_criteria, required_checks, manifest_digest, task_type, priority,
			status, created_by_user_id
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, NULLIF($8, ''), $9, $10, 'DRAFT', $11)
		RETURNING id::text, organization_id::text, project_id::text, assignee_employee_id::text,
			created_by_user_id::text, title, description, acceptance_criteria, required_checks,
			manifest_digest, task_type, priority, status, task_version, created_at, updated_at`,
		input.OrganizationID, input.ProjectID, input.AssigneeEmployeeID, input.Title,
		input.Description, []byte(input.AcceptanceCriteria), []byte(input.RequiredChecks),
		input.ManifestDigest, input.TaskType, input.Priority, input.ActorUserID).Scan(
		&task.ID, &task.OrganizationID, &task.ProjectID, &task.AssigneeEmployeeID,
		&task.CreatedByUserID, &task.Title, &task.Description, &task.AcceptanceCriteria,
		&task.RequiredChecks, &manifestDigest, &task.TaskType, &task.Priority,
		&task.Status, &task.Version, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return Task{}, err
	}
	if manifestDigest.Valid {
		task.ManifestDigest = manifestDigest.String
	}
	return task, nil
}

func insertTaskCreatedEvent(ctx context.Context, tx *sql.Tx, input CreateDraftInput, task Task, sequence int64) (string, []byte, error) {
	actor, err := json.Marshal(map[string]string{"type": "owner", "id": input.ActorUserID})
	if err != nil {
		return "", nil, err
	}
	data := map[string]any{
		"title":              task.Title,
		"description":        task.Description,
		"assigneeEmployeeId": task.AssigneeEmployeeID,
		"priority":           task.Priority,
		"initialState":       "DRAFT",
		"acceptanceCriteria": task.AcceptanceCriteria,
	}
	if task.ManifestDigest != "" {
		data["manifestDigest"] = task.ManifestDigest
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return "", nil, err
	}

	var eventID, correlationID string
	var occurredAt, recordedAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_events (
			schema_version, event_type, organization_id, project_id, task_id, employee_id,
			stream_sequence, occurred_at, producer, actor, correlation_id, data
		) VALUES ('1.0.0', 'task.created', $1, $2, $3, $4, $5, now(), 'api', $6::jsonb, gen_random_uuid(), $7::jsonb)
		RETURNING event_id::text, correlation_id::text, occurred_at, recorded_at`,
		input.OrganizationID, input.ProjectID, task.ID, input.AssigneeEmployeeID,
		sequence, actor, dataJSON).Scan(&eventID, &correlationID, &occurredAt, &recordedAt)
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
		EventID: eventID, SchemaVersion: "1.0.0", EventType: "task.created",
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: stringPointer(task.ID), EmployeeID: stringPointer(input.AssigneeEmployeeID),
		StreamSequence: sequence, OccurredAt: occurredAt, RecordedAt: recordedAt,
		Producer: "api", Actor: map[string]string{"type": "owner", "id": input.ActorUserID},
		CorrelationID: correlationID, Data: data,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", nil, err
	}
	return eventID, payload, nil
}

func stringPointer(value string) *string {
	return &value
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
