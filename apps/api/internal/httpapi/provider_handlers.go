package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/providers"
)

type configureProviderRequest struct {
	Label         string `json:"label"`
	BaseURL       string `json:"baseUrl"`
	CredentialEnv string `json:"credentialEnv"`
	Enabled       bool   `json:"enabled"`
}

func (deps Dependencies) listProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Providers == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	views, err := deps.Providers.ListForOrg(r.Context(), identity.OrganizationID)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (deps Dependencies) configureProvider(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Providers == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body configureProviderRequest
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	view, err := deps.Providers.Configure(r.Context(), providers.ConfigureInput{
		OrganizationID: identity.OrganizationID,
		ProviderKey:    chi.URLParam(r, "providerKey"),
		ActorUserID:    identity.UserID,
		Label:          body.Label,
		BaseURL:        body.BaseURL,
		CredentialEnv:  body.CredentialEnv,
		Enabled:        body.Enabled,
	})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func writeProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, providers.ErrProviderUnknown):
		writeError(w, http.StatusNotFound, "provider_unknown")
	case errors.Is(err, providers.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_provider_config")
	default:
		logInternalError("Provider API request failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
	}
}
