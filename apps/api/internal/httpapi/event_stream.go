package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/events"
)

const (
	eventPollInterval      = 250 * time.Millisecond
	eventWriteTimeout      = 750 * time.Millisecond
	eventHeartbeatInterval = 10 * time.Second
)

func (deps Dependencies) streamEvents(w http.ResponseWriter, r *http.Request) {
	identity, ok := r.Context().Value(identityContextKey{}).(auth.Identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	after, err := parseStreamCursor(r.URL.RawQuery, r.Header)
	if err != nil {
		writeEventError(w, err)
		return
	}

	projectID := chi.URLParam(r, "projectID")
	page, err := deps.readEventStreamPage(r.Context(), identity.OrganizationID, projectID, after)
	if err != nil {
		writeEventError(w, err)
		return
	}

	controller := http.NewResponseController(w)
	// Clear only this response's ordinary absolute WriteTimeout. Each frame below
	// has its own short deadline; idle streams are not an unbounded global policy.
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "stream_unavailable")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		cancel()
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	accessDone := make(chan struct{})
	go deps.watchEventAccess(ctx, cancel, controller, identity, projectID, cookie.Value, accessDone)
	defer func() { cancel(); <-accessDone; _ = controller.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := writeEventFrame(ctx, w, controller, "retry: 1000\n\n"); err != nil {
		return
	}

	poll := time.NewTicker(eventPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(eventHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		for _, event := range page.Items {
			data, err := json.Marshal(event)
			if err != nil {
				logInternalError("Event stream encoding failed", err)
				return
			}
			if err := writeEventFrame(ctx, w, controller, fmt.Sprintf("id: %d\ndata: %s\n\n", event.StreamSequence, data)); err != nil {
				return
			}
			after = event.StreamSequence
		}
		if !page.HasMore {
			select {
			case <-ctx.Done():
				return
			case <-poll.C:
			case <-heartbeat.C:
				if err := writeEventFrame(ctx, w, controller, ": heartbeat\n\n"); err != nil {
					return
				}
			}
		}
		page, err = deps.readEventStreamPage(ctx, identity.OrganizationID, projectID, after)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, events.ErrProjectNotFound) {
				logInternalError("Event stream read stopped", err)
			}
			return
		}
	}
}

func (deps Dependencies) readEventStreamPage(ctx context.Context, organizationID, projectID string, after int64) (events.Page, error) {
	readCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return deps.Events.After(readCtx, organizationID, projectID, after, 100)
}

func writeEventFrame(ctx context.Context, w http.ResponseWriter, controller *http.ResponseController, frame string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(eventWriteTimeout)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stopInterrupt() {
			<-interrupted
		}
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, frame); err != nil {
		return err
	}
	return controller.Flush()
}
