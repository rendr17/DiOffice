package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/events"
)

func (deps Dependencies) listEvents(w http.ResponseWriter, r *http.Request) {
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	projectID := chi.URLParam(r, "projectID")
	query, err := parseHistoryQuery(r.URL.RawQuery)
	if err != nil {
		writeEventError(w, err)
		return
	}
	page := events.Page{}
	if query.after != nil {
		page, err = deps.Events.After(r.Context(), identity.OrganizationID, projectID, *query.after, query.limit)
	} else {
		page, err = deps.Events.Recent(r.Context(), identity.OrganizationID, projectID)
	}
	if err != nil {
		writeEventError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func writeEventError(w http.ResponseWriter, err error) {
	var cursorErr *events.CursorError
	if errors.As(err, &cursorErr) {
		status, code := http.StatusConflict, "event_cursor_ahead"
		if errors.Is(err, events.ErrCursorExpired) {
			status, code = http.StatusGone, "event_cursor_expired"
		}
		writeJSON(w, status, struct {
			Error    string      `json:"error"`
			Snapshot events.Page `json:"snapshot"`
		}{Error: code, Snapshot: cursorErr.Snapshot})
		return
	}
	if errors.Is(err, events.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_event_cursor")
		return
	}
	if errors.Is(err, events.ErrInvalidQuery) {
		writeError(w, http.StatusBadRequest, "invalid_event_query")
		return
	}
	if errors.Is(err, events.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, "project_not_found")
		return
	}
	logInternalError("Event read failed", err)
	writeError(w, http.StatusServiceUnavailable, "service_unavailable")
}
