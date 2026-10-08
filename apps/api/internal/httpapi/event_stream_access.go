package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/events"
)

// Access checks continue independently of writes: a slow consumer cannot hold a
// revoked session open. A failed/timed-out revalidation fails closed.
func (deps Dependencies) watchEventAccess(ctx context.Context, cancel context.CancelFunc, controller *http.ResponseController, identity auth.Identity, projectID, sessionToken string, done chan<- struct{}) {
	defer func() { cancel(); _ = controller.SetWriteDeadline(time.Now()); close(done) }()
	ticker := time.NewTicker(eventPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		checkCtx, finish := context.WithTimeout(ctx, 500*time.Millisecond)
		current, err := deps.Auth.Authenticate(checkCtx, sessionToken)
		if err == nil && (current.Role != "OWNER" || current.OrganizationID != identity.OrganizationID || current.UserID != identity.UserID || current.SessionID != identity.SessionID) {
			finish()
			return
		}
		if err == nil {
			err = deps.Events.CheckProject(checkCtx, identity.OrganizationID, projectID)
		}
		finish()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, auth.ErrUnauthenticated) && !errors.Is(err, context.Canceled) && !errors.Is(err, events.ErrProjectNotFound) {
				logInternalError("Event access validation stopped", err)
			}
			return
		}
	}
}
