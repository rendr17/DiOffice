package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/repositories"
)

type saveRepositoryRequest struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
}

func (deps Dependencies) getProjectRepository(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Repositories == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	repository, err := deps.Repositories.Get(r.Context(), identity.OrganizationID, chi.URLParam(r, "projectID"))
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (deps Dependencies) saveProjectRepository(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Repositories == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body saveRepositoryRequest
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	repository, err := deps.Repositories.Register(r.Context(), repositories.RegisterInput{
		OrganizationID: identity.OrganizationID,
		ProjectID:      chi.URLParam(r, "projectID"),
		ActorUserID:    identity.UserID,
		Owner:          body.Owner,
		Name:           body.Name,
		DefaultBranch:  body.DefaultBranch,
	})
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repositories.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_repository")
	case errors.Is(err, repositories.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project_not_found")
	case errors.Is(err, repositories.ErrRepositoryNotFound):
		writeError(w, http.StatusNotFound, "repository_not_found")
	default:
		logInternalError("Repository API request failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
	}
}
