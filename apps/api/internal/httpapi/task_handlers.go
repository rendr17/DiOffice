package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

type createTaskRequest struct {
	AssigneeEmployeeID string          `json:"assigneeEmployeeId"`
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	AcceptanceCriteria json.RawMessage `json:"acceptanceCriteria"`
	RequiredChecks     json.RawMessage `json:"requiredChecks"`
	ManifestDigest     string          `json:"manifestDigest"`
	TaskType           string          `json:"taskType"`
	Priority           string          `json:"priority"`
}

func (deps Dependencies) createTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Tasks == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body createTaskRequest
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	task, replayed, err := deps.Tasks.CreateDraft(r.Context(), tasks.CreateDraftInput{
		OrganizationID:     identity.OrganizationID,
		ProjectID:          chi.URLParam(r, "projectID"),
		ActorUserID:        identity.UserID,
		AssigneeEmployeeID: body.AssigneeEmployeeID,
		IdempotencyKey:     r.Header.Get("Idempotency-Key"),
		Title:              body.Title, Description: body.Description,
		AcceptanceCriteria: body.AcceptanceCriteria, RequiredChecks: body.RequiredChecks,
		ManifestDigest: body.ManifestDigest, TaskType: body.TaskType, Priority: body.Priority,
	})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, task)
}

func (deps Dependencies) listTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Tasks == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := deps.Tasks.ListByProject(r.Context(), identity.OrganizationID, chi.URLParam(r, "projectID"))
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []tasks.Task `json:"items"`
	}{Items: items})
}

func writeTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tasks.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_task")
	case errors.Is(err, tasks.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project_not_found")
	case errors.Is(err, tasks.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_key_conflict")
	case errors.Is(err, tasks.ErrIdempotencyInProgress):
		writeError(w, http.StatusConflict, "idempotency_request_in_progress")
	default:
		logInternalError("Task API request failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
	}
}
