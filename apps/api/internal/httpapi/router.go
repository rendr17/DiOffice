package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

type Dependencies struct {
	DB            *sql.DB
	Auth          *auth.Service
	Tasks         *tasks.Service
	SecureCookies bool
	WebOrigin     string
}

const (
	sessionCookieName = "dioffice_session"
	csrfCookieName    = "dioffice_csrf"
)

func NewRouter(deps Dependencies) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer, corsMiddleware(deps.WebOrigin))
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Service string `json:"service"`
		}{Status: "ok", Service: "dioffice-api"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if deps.DB == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := deps.DB.PingContext(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	router.Post("/api/v1/auth/login", deps.login)
	router.With(deps.requireIdentity).Get("/api/v1/auth/session", deps.currentSession)
	router.With(deps.requireIdentity, deps.requireCSRF).Post("/api/v1/auth/logout", deps.logout)
	router.With(deps.requireIdentity).Get("/api/v1/projects/{projectID}/tasks", deps.listTasks)
	router.With(deps.requireIdentity, deps.requireCSRF).Post("/api/v1/projects/{projectID}/tasks", deps.createTask)
	return router
}

func corsMiddleware(webOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if webOrigin == "" || origin != webOrigin {
				writeError(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", webOrigin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-CSRF-Token")
			w.Header().Set("Access-Control-Expose-Headers", "Idempotency-Replayed")
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func logInternalError(message string, err error) {
	if err != nil {
		slog.Error(message, "error_type", fmt.Sprintf("%T", err))
	}
}
