package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/folderbridge"
)

type openProjectFolderRequest struct {
	Editor folderbridge.Editor `json:"editor"`
}

func (deps Dependencies) projectFolderStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	projectID, ok := deps.authorizedActiveProject(w, r)
	if !ok {
		return
	}
	if deps.FolderBridge == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"available": false})
		return
	}
	available, err := deps.FolderBridge.Available(r.Context(), projectID)
	if err != nil {
		logInternalError("Local project folder status failed", err)
		writeError(w, http.StatusServiceUnavailable, "folder_bridge_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"available": available})
}

func (deps Dependencies) openProjectFolder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body openProjectFolderRequest
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	if body.Editor != folderbridge.EditorExplorer && body.Editor != folderbridge.EditorVSCode {
		writeError(w, http.StatusBadRequest, "invalid_folder_request")
		return
	}
	projectID, ok := deps.authorizedActiveProject(w, r)
	if !ok {
		return
	}
	if deps.FolderBridge == nil {
		writeError(w, http.StatusServiceUnavailable, "folder_bridge_unavailable")
		return
	}
	if err := deps.FolderBridge.Open(r.Context(), projectID, body.Editor); err != nil {
		switch {
		case errors.Is(err, folderbridge.ErrFolderNotConfigured):
			writeError(w, http.StatusConflict, "folder_not_configured")
		case errors.Is(err, folderbridge.ErrInvalidEditor):
			writeError(w, http.StatusBadRequest, "invalid_folder_request")
		case errors.Is(err, folderbridge.ErrBridgeNotConfigured), errors.Is(err, folderbridge.ErrBridgeUnavailable):
			writeError(w, http.StatusServiceUnavailable, "folder_bridge_unavailable")
		case errors.Is(err, folderbridge.ErrFolderOpenFailed):
			writeError(w, http.StatusServiceUnavailable, "folder_open_failed")
		default:
			logInternalError("Local project folder open failed", err)
			writeError(w, http.StatusServiceUnavailable, "folder_bridge_unavailable")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "opening", "editor": string(body.Editor)})
}

func (deps Dependencies) authorizedActiveProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	if deps.Directory == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return "", false
	}
	projects, err := deps.Directory.ListProjects(r.Context(), identity.OrganizationID)
	if err != nil {
		logInternalError("Local project folder scope check failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return "", false
	}
	projectID := chi.URLParam(r, "projectID")
	for _, project := range projects {
		if project.ID == projectID {
			return projectID, true
		}
	}
	writeError(w, http.StatusNotFound, "project_not_found")
	return "", false
}
