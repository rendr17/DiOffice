package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

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

type saveTaskToBacklogRequest struct {
	ExpectedVersion int64 `json:"expectedVersion"`
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
	body, images, err := decodeTaskCreateRequest(w, r)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidReferenceImage):
			writeError(w, http.StatusBadRequest, "invalid_reference_image")
		case errors.Is(err, errInvalidMultipartTaskRequest):
			writeError(w, http.StatusBadRequest, "invalid_task_request")
		default:
			writeRequestDecodeError(w, err)
		}
		return
	}
	if len(images) > 0 && deps.ReferenceImages == nil {
		writeError(w, http.StatusServiceUnavailable, "reference_image_storage_unavailable")
		return
	}
	storedKeys := make([]string, 0, len(images))
	referenceImages := make([]tasks.ReferenceImage, 0, len(images))
	for _, image := range images {
		storedKeys = append(storedKeys, image.metadata.ObjectKey)
		if err := deps.ReferenceImages.Put(r.Context(), image.metadata.ObjectKey, image.metadata.ContentType, image.data); err != nil {
			cleanupReferenceImageObjects(deps.ReferenceImages, storedKeys)
			logInternalError("Reference image upload failed", err)
			writeError(w, http.StatusServiceUnavailable, "reference_image_storage_unavailable")
			return
		}
		referenceImages = append(referenceImages, image.metadata)
	}
	task, replayed, err := deps.Tasks.CreateDraft(r.Context(), tasks.CreateDraftInput{
		OrganizationID:     identity.OrganizationID,
		ProjectID:          chi.URLParam(r, "projectID"),
		ActorUserID:        identity.UserID,
		AssigneeEmployeeID: body.AssigneeEmployeeID,
		IdempotencyKey:     r.Header.Get("Idempotency-Key"),
		Title:              body.Title, Description: body.Description,
		AcceptanceCriteria: body.AcceptanceCriteria, RequiredChecks: body.RequiredChecks,
		ReferenceImages: referenceImages,
		ManifestDigest:  body.ManifestDigest, TaskType: body.TaskType, Priority: body.Priority,
	})
	if err != nil {
		cleanupReferenceImageObjects(deps.ReferenceImages, storedKeys)
		writeTaskError(w, err)
		return
	}
	if replayed {
		cleanupReferenceImageObjects(deps.ReferenceImages, storedKeys)
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, task)
}

func (deps Dependencies) saveTaskToBacklog(w http.ResponseWriter, r *http.Request) {
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
	var body saveTaskToBacklogRequest
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	task, replayed, err := deps.Tasks.SaveToBacklog(r.Context(), tasks.SaveToBacklogInput{
		OrganizationID:  identity.OrganizationID,
		ProjectID:       chi.URLParam(r, "projectID"),
		TaskID:          chi.URLParam(r, "taskID"),
		ActorUserID:     identity.UserID,
		IdempotencyKey:  r.Header.Get("Idempotency-Key"),
		ExpectedVersion: body.ExpectedVersion,
	})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusOK, task)
}

func (deps Dependencies) getTaskReferenceImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if deps.Tasks == nil || deps.ReferenceImages == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	image, err := deps.Tasks.GetReferenceImage(r.Context(), identity.OrganizationID,
		chi.URLParam(r, "projectID"), chi.URLParam(r, "taskID"), chi.URLParam(r, "imageID"))
	if err != nil {
		writeTaskError(w, err)
		return
	}
	object, err := deps.ReferenceImages.Get(r.Context(), image.ObjectKey)
	if err != nil {
		logInternalError("Reference image read failed", err)
		writeError(w, http.StatusServiceUnavailable, "reference_image_storage_unavailable")
		return
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, maxReferenceImageBytes+1))
	if err != nil || int64(len(data)) != image.SizeBytes || len(data) > maxReferenceImageBytes {
		writeError(w, http.StatusServiceUnavailable, "reference_image_storage_unavailable")
		return
	}
	checksum := sha256.Sum256(data)
	if hex.EncodeToString(checksum[:]) != image.SHA256 {
		writeError(w, http.StatusServiceUnavailable, "reference_image_storage_unavailable")
		return
	}
	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": image.FileName}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func cleanupReferenceImageObjects(store ReferenceImageStore, keys []string) {
	if store == nil || len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range keys {
		if err := store.Delete(ctx, key); err != nil {
			logInternalError("Reference image cleanup failed", err)
		}
	}
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
	case errors.Is(err, tasks.ErrTaskNotFound):
		writeError(w, http.StatusNotFound, "task_not_found")
	case errors.Is(err, tasks.ErrReferenceImageNotFound):
		writeError(w, http.StatusNotFound, "reference_image_not_found")
	case errors.Is(err, tasks.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_key_conflict")
	case errors.Is(err, tasks.ErrIdempotencyInProgress):
		writeError(w, http.StatusConflict, "idempotency_request_in_progress")
	case errors.Is(err, tasks.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_state_transition")
	case errors.Is(err, tasks.ErrTaskVersionConflict):
		writeError(w, http.StatusConflict, "stale_task_version")
	default:
		logInternalError("Task API request failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
	}
}
