package httpapi

import (
	"net/http"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/directory"
)

func (deps Dependencies) listProjects(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Directory == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := deps.Directory.ListProjects(r.Context(), identity.OrganizationID)
	if err != nil {
		logInternalError("Project directory request failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []directory.Project `json:"items"`
	}{Items: items})
}

func (deps Dependencies) listEmployees(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Directory == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := deps.Directory.ListEmployees(r.Context(), identity.OrganizationID)
	if err != nil {
		logInternalError("Employee directory request failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []directory.Employee `json:"items"`
	}{Items: items})
}
