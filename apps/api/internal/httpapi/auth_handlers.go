package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
)

type identityContextKey struct{}

type safeUser struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	Email          string `json:"email"`
	DisplayName    string `json:"displayName"`
	Role           string `json:"role"`
}

type loginRequest struct {
	OrganizationID string `json:"organizationId"`
	Email          string `json:"email"`
	Password       string `json:"password"`
}

func (deps Dependencies) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	var body loginRequest
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	session, err := deps.Auth.Login(r.Context(), auth.LoginInput{
		OrganizationID: body.OrganizationID,
		Email:          body.Email,
		Password:       body.Password,
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		logInternalError("Owner login failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	setSessionCookies(w, session, deps.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]safeUser{"user": toSafeUser(session.Identity)})
}

func (deps Dependencies) developmentSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if deps.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	if deps.WebOrigin == "" || r.Header.Get("Origin") != deps.WebOrigin || !isLoopbackRemote(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "dev_auth_bypass_not_allowed")
		return
	}
	var body struct{}
	if err := decodeJSONRequest(w, r, &body); err != nil {
		writeRequestDecodeError(w, err)
		return
	}
	session, err := deps.Auth.CreateDevelopmentSession(r.Context())
	if err != nil {
		logInternalError("Development Owner session failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	setSessionCookies(w, session, deps.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]safeUser{"user": toSafeUser(session.Identity)})
}

func (deps Dependencies) requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if deps.Auth == nil {
			writeError(w, http.StatusServiceUnavailable, "service_unavailable")
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		identity, err := deps.Auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthenticated) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			logInternalError("Owner session validation failed", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable")
			return
		}
		if identity.Role != "OWNER" {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		ctx := context.WithValue(r.Context(), identityContextKey{}, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (deps Dependencies) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
		if !ok || deps.Auth == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || !deps.Auth.VerifyCSRF(identity, cookie.Value, r.Header.Get("X-CSRF-Token")) {
			writeError(w, http.StatusForbidden, "csrf_validation_failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (deps Dependencies) currentSession(w http.ResponseWriter, r *http.Request) {
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]safeUser{"user": toSafeUser(identity)})
}

func (deps Dependencies) logout(w http.ResponseWriter, r *http.Request) {
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok || deps.Auth == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := deps.Auth.RevokeSession(r.Context(), identity.SessionID); err != nil {
		logInternalError("Owner logout failed", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	clearSessionCookies(w, deps.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed_out"})
}

func setSessionCookies(w http.ResponseWriter, session auth.LoginSession, secure bool) {
	maxAge := int(time.Until(session.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	cookieBase := http.Cookie{
		Path: "/", Secure: secure, SameSite: http.SameSiteLaxMode,
		Expires: session.ExpiresAt, MaxAge: maxAge,
	}
	sessionCookie := cookieBase
	sessionCookie.Name = sessionCookieName
	sessionCookie.Value = session.SessionToken
	sessionCookie.HttpOnly = true
	http.SetCookie(w, &sessionCookie)
	csrfCookie := cookieBase
	csrfCookie.Name = csrfCookieName
	csrfCookie.Value = session.CSRFToken
	csrfCookie.HttpOnly = false
	http.SetCookie(w, &csrfCookie)
}

func clearSessionCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", HttpOnly: name == sessionCookieName,
			Secure: secure, SameSite: http.SameSiteLaxMode,
			Expires: time.Unix(1, 0), MaxAge: -1,
		})
	}
}

func toSafeUser(identity auth.Identity) safeUser {
	return safeUser{
		ID: identity.UserID, OrganizationID: identity.OrganizationID,
		Email: identity.Email, DisplayName: identity.DisplayName, Role: identity.Role,
	}
}

func isLoopbackRemote(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
