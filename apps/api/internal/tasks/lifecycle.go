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

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rendr17/dioffice/apps/api/internal/eventstore"
)

const (
	markReadyOperation      = "tasks.mark_ready"
	startExecutionOperation = "tasks.start_execution"
	defaultWorkerProfile    = "node-22-pnpm-10-playwright"
)

var (
	ErrTaskIncomplete       = errors.New("task is not ready for execution")
	ErrActiveAttemptExists  = errors.New("an active execution attempt already exists")
	ErrRepositoryNotFound   = errors.New("project repository is not connected")
	attemptActiveStates     = []string{"CREATED", "PROVISIONING", "RUNNING", "WAITING_APPROVAL"}
	taskReadyRequiredFields = []string{"description", "acceptanceCriteria", "manifestDigest", "repository"}
)

// IncompleteTaskError reports the confirmed details a draft is still missing.
// The Missing values are stable field identifiers, safe to show to the Owner.
type IncompleteTaskError struct {
	Missing []string
}

func (e *IncompleteTaskError) Error() string {
	return "task is not ready: missing " + strings.Join(e.Missing, ", ")
}

func (e *IncompleteTaskError) Is(target error) bool { return target == ErrTaskIncomplete }

type MarkReadyInput struct {
	OrganizationID  string
	ProjectID       string
	TaskID          string
	ActorUserID     string
	IdempotencyKey  string
	ExpectedVersion int64
	ManifestDigest  string
}

type StartExecutionInput struct {
	OrganizationID  string
	ProjectID       string
	TaskID          string
	ActorUserID     string
	IdempotencyKey  string
	ExpectedVersion int64
}

// MarkReady records the explicit DRAFT|BACKLOG -> READY transition. The Owner
// confirms the task's required details; the transition starts no worker.
func (s *Service) MarkReady(ctx context.Context, input MarkReadyInput) (Task, bool, error) {
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
	input.ManifestDigest = strings.ToLower(strings.TrimSpace(input.ManifestDigest))
	if input.ManifestDigest != "" && !isSHA256(input.ManifestDigest) {
		return Task{}, false, fmt.Errorf("%w: manifestDigest must be a SHA-256 digest", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return Task{}, false, errors.New("task service database is not configured")
	}

	requestHash, err := hashTransitionRequest(markReadyOperation, input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, input.ManifestDigest)
	if err != nil {
		return Task{}, false, fmt.Errorf("hash task ready request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task ready transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimTransitionIdempotency(ctx, tx, input.OrganizationID, input.ActorUserID, markReadyOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent ready replay: %w", err)
		}
		return task, true, nil
	}

	task, err = loadTaskForTransition(ctx, tx, input.OrganizationID, input.ProjectID, input.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for ready transition: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	if task.Status != "DRAFT" && task.Status != "BACKLOG" {
		return Task{}, false, ErrInvalidTransition
	}

	digestChanged := input.ManifestDigest != "" && input.ManifestDigest != task.ManifestDigest
	manifestDigest := task.ManifestDigest
	if digestChanged {
		manifestDigest = input.ManifestDigest
	}

	missing, err := taskReadinessGaps(ctx, tx, input.OrganizationID, input.ProjectID, task, manifestDigest)
	if err != nil {
		return Task{}, false, err
	}
	if len(missing) > 0 {
		return Task{}, false, &IncompleteTaskError{Missing: missing}
	}

	sequence, err := nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID)
	if err != nil {
		return Task{}, false, err
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE tasks
		SET status = 'READY', manifest_digest = COALESCE(NULLIF($5, ''), manifest_digest),
			task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND task_version = $4
		RETURNING task_version, updated_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, input.ManifestDigest).
		Scan(&task.Version, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskVersionConflict
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("update task to ready: %w", err)
	}
	fromState := task.Status
	task.Status = "READY"
	task.ManifestDigest = manifestDigest

	causationID := ""
	if digestChanged {
		eventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
			OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
			TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID,
			StreamSequence: sequence, Producer: "api",
			Actor:     ownerActor(input.ActorUserID),
			EventType: "task.updated",
			Data: map[string]any{
				"changedFields": []string{"manifestDigest"}, "newVersion": task.Version,
			},
		})
		if err != nil {
			return Task{}, false, fmt.Errorf("insert manifest-digest update event: %w", err)
		}
		causationID = eventID
		if err := insertOutboxRecord(ctx, tx, eventID, input.ProjectID, sequence, envelope); err != nil {
			return Task{}, false, err
		}
		sequence, err = nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID)
		if err != nil {
			return Task{}, false, err
		}
	}

	eventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID,
		StreamSequence: sequence, Producer: "api",
		Actor:       ownerActor(input.ActorUserID),
		CausationID: causationID,
		EventType:   "task.state_changed",
		Data: map[string]any{
			"fromState": fromState, "toState": "READY",
			"taskVersion": task.Version, "reason": "owner_marked_ready",
		},
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("insert task ready event: %w", err)
	}
	if err := insertOutboxRecord(ctx, tx, eventID, input.ProjectID, sequence, envelope); err != nil {
		return Task{}, false, err
	}
	if err := insertAuditRecord(ctx, tx, input.OrganizationID, input.ProjectID, input.ActorUserID, "task.mark_ready", task.ID); err != nil {
		return Task{}, false, err
	}
	if err := storeTransitionResult(ctx, tx, input.OrganizationID, input.ActorUserID, markReadyOperation, input.IdempotencyKey, requestHash, task); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit task ready transition: %w", err)
	}
	return task, false, nil
}

// StartExecution records the explicit Owner Start: READY -> PROVISIONING. It
// commits the task transition, a CREATED execution attempt, and a PROVISIONING
// workspace in one transaction. No worker or runtime exists yet; a later
// provisioner advances the committed attempt.
func (s *Service) StartExecution(ctx context.Context, input StartExecutionInput) (Task, bool, error) {
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

	requestHash, err := hashTransitionRequest(startExecutionOperation, input.OrganizationID, input.ProjectID, input.TaskID, input.ExpectedVersion, "")
	if err != nil {
		return Task{}, false, fmt.Errorf("hash task start request: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task start transaction: %w", err)
	}
	defer tx.Rollback()

	task, replayed, err := claimTransitionIdempotency(ctx, tx, input.OrganizationID, input.ActorUserID, startExecutionOperation, input.IdempotencyKey, requestHash)
	if err != nil {
		return Task{}, false, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit idempotent start replay: %w", err)
		}
		return task, true, nil
	}

	task, err = loadTaskForTransition(ctx, tx, input.OrganizationID, input.ProjectID, input.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("load task for start: %w", err)
	}
	if task.Version != input.ExpectedVersion {
		return Task{}, false, ErrTaskVersionConflict
	}
	if task.Status != "READY" {
		return Task{}, false, ErrInvalidTransition
	}

	var runtimeType string
	err = tx.QueryRowContext(ctx, `
		SELECT e.default_runtime FROM employees e
		WHERE e.organization_id = $1 AND e.id = $2`,
		input.OrganizationID, task.AssigneeEmployeeID).Scan(&runtimeType)
	if err != nil {
		return Task{}, false, fmt.Errorf("load assignee runtime: %w", err)
	}
	var attemptBusy bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM execution_attempts
			WHERE organization_id = $1
				AND (task_id = $2 OR employee_id = $3)
				AND state = ANY($4)
		)`, input.OrganizationID, input.TaskID, task.AssigneeEmployeeID, attemptActiveStates).Scan(&attemptBusy)
	if err != nil {
		return Task{}, false, fmt.Errorf("check active attempts: %w", err)
	}
	if attemptBusy {
		return Task{}, false, ErrActiveAttemptExists
	}

	var attemptNumber int64
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(attempt_number), 0) + 1
		FROM execution_attempts
		WHERE organization_id = $1 AND task_id = $2`,
		input.OrganizationID, input.TaskID).Scan(&attemptNumber)
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate attempt number: %w", err)
	}

	branchName := "task/" + input.TaskID
	var workspaceID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO workspaces (
			organization_id, project_id, task_id, branch_name, worker_profile, state
		) VALUES ($1, $2, $3, $4, $5, 'PROVISIONING')
		RETURNING id::text`,
		input.OrganizationID, input.ProjectID, input.TaskID, branchName, defaultWorkerProfile).Scan(&workspaceID)
	if err != nil {
		if isUniqueViolation(err) {
			return Task{}, false, ErrActiveAttemptExists
		}
		return Task{}, false, fmt.Errorf("insert task workspace: %w", err)
	}
	var attemptID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO execution_attempts (
			organization_id, project_id, task_id, employee_id, attempt_number,
			state, runtime_type, started_at
		) VALUES ($1, $2, $3, $4, $5, 'CREATED', $6, now())
		RETURNING id::text`,
		input.OrganizationID, input.ProjectID, input.TaskID, task.AssigneeEmployeeID,
		attemptNumber, runtimeType).Scan(&attemptID)
	if err != nil {
		if isUniqueViolation(err) {
			return Task{}, false, ErrActiveAttemptExists
		}
		return Task{}, false, fmt.Errorf("insert execution attempt: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE tasks
		SET status = 'PROVISIONING', branch_name = $4, task_version = task_version + 1, updated_at = now()
		WHERE organization_id = $1 AND project_id = $2 AND id = $3 AND task_version = $5
		RETURNING task_version, updated_at`,
		input.OrganizationID, input.ProjectID, input.TaskID, branchName, input.ExpectedVersion).
		Scan(&task.Version, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrTaskVersionConflict
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("update task to provisioning: %w", err)
	}
	task.Status = "PROVISIONING"

	correlationID, err := newUUID()
	if err != nil {
		return Task{}, false, fmt.Errorf("allocate start correlation id: %w", err)
	}
	actor := ownerActor(input.ActorUserID)

	attemptSequence, err := nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID)
	if err != nil {
		return Task{}, false, err
	}
	attemptEventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID, AttemptID: attemptID,
		WorkspaceID: workspaceID, StreamSequence: attemptSequence, Producer: "api",
		Actor: actor, CorrelationID: correlationID,
		EventType: "execution_attempt.state_changed",
		Data: map[string]any{
			"fromState": nil, "toState": "CREATED",
			"attemptNumber": attemptNumber, "reasonCode": "owner_start",
		},
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("insert attempt-created event: %w", err)
	}
	if err := insertOutboxRecord(ctx, tx, attemptEventID, input.ProjectID, attemptSequence, envelope); err != nil {
		return Task{}, false, err
	}

	workspaceSequence, err := nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID)
	if err != nil {
		return Task{}, false, err
	}
	workspaceEventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID, AttemptID: attemptID,
		WorkspaceID: workspaceID, StreamSequence: workspaceSequence, Producer: "api",
		Actor: actor, CorrelationID: correlationID, CausationID: attemptEventID,
		EventType: "workspace.state_changed",
		Data: map[string]any{
			"fromState": nil, "toState": "PROVISIONING",
			"branchName": branchName, "workerProfile": defaultWorkerProfile,
		},
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("insert workspace-provisioning event: %w", err)
	}
	if err := insertOutboxRecord(ctx, tx, workspaceEventID, input.ProjectID, workspaceSequence, envelope); err != nil {
		return Task{}, false, err
	}

	taskSequence, err := nextEventSequence(ctx, tx, input.OrganizationID, input.ProjectID)
	if err != nil {
		return Task{}, false, err
	}
	taskEventID, envelope, err := insertTransitionEvent(ctx, tx, transitionEventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: task.ID, EmployeeID: task.AssigneeEmployeeID, AttemptID: attemptID,
		WorkspaceID: workspaceID, StreamSequence: taskSequence, Producer: "api",
		Actor: actor, CorrelationID: correlationID, CausationID: attemptEventID,
		EventType: "task.state_changed",
		Data: map[string]any{
			"fromState": "READY", "toState": "PROVISIONING",
			"taskVersion": task.Version, "reason": "owner_started",
		},
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("insert task-provisioning event: %w", err)
	}
	if err := insertOutboxRecord(ctx, tx, taskEventID, input.ProjectID, taskSequence, envelope); err != nil {
		return Task{}, false, err
	}
	if err := insertAuditRecord(ctx, tx, input.OrganizationID, input.ProjectID, input.ActorUserID, "task.start", task.ID); err != nil {
		return Task{}, false, err
	}
	if err := storeTransitionResult(ctx, tx, input.OrganizationID, input.ActorUserID, startExecutionOperation, input.IdempotencyKey, requestHash, task); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit task start: %w", err)
	}
	return task, false, nil
}

// taskReadinessGaps lists the confirmed details still missing before READY.
func taskReadinessGaps(ctx context.Context, tx *sql.Tx, organizationID, projectID string, task Task, manifestDigest string) ([]string, error) {
	missing := make([]string, 0, len(taskReadyRequiredFields))
	if strings.TrimSpace(task.Description) == "" {
		missing = append(missing, "description")
	}
	var criteria []json.RawMessage
	if err := json.Unmarshal(task.AcceptanceCriteria, &criteria); err != nil || len(criteria) == 0 {
		missing = append(missing, "acceptanceCriteria")
	}
	if manifestDigest == "" {
		missing = append(missing, "manifestDigest")
	}
	var repositoryConnected bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM repositories WHERE organization_id = $1 AND project_id = $2
		)`, organizationID, projectID).Scan(&repositoryConnected); err != nil {
		return nil, fmt.Errorf("check project repository: %w", err)
	}
	if !repositoryConnected {
		missing = append(missing, "repository")
	}
	return missing, nil
}

func loadTaskForTransition(ctx context.Context, tx *sql.Tx, organizationID, projectID, taskID string) (Task, error) {
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
		FOR UPDATE OF t`, organizationID, projectID, taskID).Scan(
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

func nextEventSequence(ctx context.Context, tx *sql.Tx, organizationID, projectID string) (int64, error) {
	sequence, err := eventstore.NextProjectSequence(ctx, tx, organizationID, projectID)
	if errors.Is(err, eventstore.ErrProjectSequence) {
		return 0, ErrProjectNotFound
	}
	return sequence, err
}

type transitionEventInput struct {
	OrganizationID string
	ProjectID      string
	TaskID         string
	EmployeeID     string
	AttemptID      string
	SessionID      string
	WorkspaceID    string
	StreamSequence int64
	Producer       string
	Actor          map[string]string
	CorrelationID  string
	CausationID    string
	EventType      string
	Data           map[string]any
}

func ownerActor(userID string) map[string]string {
	return map[string]string{"type": "owner", "id": userID}
}

// insertTransitionEvent persists one contract-checked fact and returns its id
// and outbox payload. Event data is sanitized against the registered schema.
func insertTransitionEvent(ctx context.Context, tx *sql.Tx, input transitionEventInput) (string, []byte, error) {
	return eventstore.AppendEvent(ctx, tx, eventstore.EventInput{
		OrganizationID: input.OrganizationID, ProjectID: input.ProjectID,
		TaskID: input.TaskID, EmployeeID: input.EmployeeID,
		AttemptID: input.AttemptID, SessionID: input.SessionID,
		WorkspaceID: input.WorkspaceID, StreamSequence: input.StreamSequence,
		Producer: input.Producer, Actor: input.Actor,
		CorrelationID: input.CorrelationID, CausationID: input.CausationID,
		EventType: input.EventType, Data: input.Data,
	})
}

func insertOutboxRecord(ctx context.Context, tx *sql.Tx, eventID, projectID string, sequence int64, envelope []byte) error {
	return eventstore.EnqueueOutbox(ctx, tx, eventID, projectID, sequence, envelope)
}

func insertAuditRecord(ctx context.Context, tx *sql.Tx, organizationID, projectID, actorUserID, action, targetID string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_records (
			organization_id, project_id, actor_user_id, action, target_type, target_id, outcome
		) VALUES ($1, $2, $3, $4, 'task', $5, 'success')`,
		organizationID, projectID, actorUserID, action, targetID); err != nil {
		return fmt.Errorf("insert task audit record: %w", err)
	}
	return nil
}

func hashTransitionRequest(operation, organizationID, projectID, taskID string, expectedVersion int64, extra string) (string, error) {
	canonical, err := json.Marshal(struct {
		Operation       string `json:"operation"`
		OrganizationID  string `json:"organizationId"`
		ProjectID       string `json:"projectId"`
		TaskID          string `json:"taskId"`
		ExpectedVersion int64  `json:"expectedVersion"`
		Extra           string `json:"extra,omitempty"`
	}{operation, organizationID, projectID, taskID, expectedVersion, extra})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func claimTransitionIdempotency(ctx context.Context, tx *sql.Tx, organizationID, actorUserID, operation, idempotencyKey, requestHash string) (Task, bool, error) {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM idempotency_keys
		WHERE organization_id = $1 AND actor_user_id = $2 AND operation = $3
			AND idempotency_key = $4 AND expires_at <= now()`,
		organizationID, actorUserID, operation, idempotencyKey); err != nil {
		return Task{}, false, fmt.Errorf("remove expired idempotency record: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_keys (
			organization_id, actor_user_id, operation, idempotency_key, request_hash, expires_at
		) VALUES ($1, $2, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, actor_user_id, operation, idempotency_key) DO NOTHING`,
		organizationID, actorUserID, operation, idempotencyKey, requestHash)
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
		FOR UPDATE`, organizationID, actorUserID, operation, idempotencyKey).
		Scan(&storedHash, &responseStatus, &responseJSON)
	if err != nil {
		return Task{}, false, fmt.Errorf("read idempotency result: %w", err)
	}
	if storedHash != requestHash {
		return Task{}, false, ErrIdempotencyConflict
	}
	if !responseStatus.Valid || responseStatus.Int64 != transitionResponseStatus || len(responseJSON) == 0 {
		return Task{}, false, ErrIdempotencyInProgress
	}
	var task Task
	if err := json.Unmarshal(responseJSON, &task); err != nil {
		return Task{}, false, fmt.Errorf("decode idempotency response: %w", err)
	}
	return task, true, nil
}

func storeTransitionResult(ctx context.Context, tx *sql.Tx, organizationID, actorUserID, operation, idempotencyKey, requestHash string, task Task) error {
	responseJSON, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode task response: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET response_status = $1, response_json = $2::jsonb
		WHERE organization_id = $3 AND actor_user_id = $4
			AND operation = $5 AND idempotency_key = $6 AND request_hash = $7`,
		transitionResponseStatus, responseJSON, organizationID, actorUserID, operation, idempotencyKey, requestHash)
	if err != nil {
		return fmt.Errorf("store idempotent task response: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check idempotency response update: %w", err)
	} else if affected != 1 {
		return errors.New("idempotency record disappeared before commit")
	}
	return nil
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

func newUUID() (string, error) {
	return eventstore.NewUUID()
}
