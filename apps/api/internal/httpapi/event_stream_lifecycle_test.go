package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventStreamTerminatesOnSessionOrProjectAccessLoss(t *testing.T) {
	for _, reason := range []string{"logout", "expiry", "role", "archive"} {
		t.Run(reason, func(t *testing.T) {
			f := newEventFixture(t)
			f.create(t, "before-access-loss")
			server, done := eventTestServer(t, f, 30*time.Second)
			stream := openTestEventStream(t, f, server, "?after=0", "")
			stream.event(t, 1)
			switch reason {
			case "logout":
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: f.session.CSRFToken})
				req.Header.Set("X-CSRF-Token", f.session.CSRFToken)
				response := httptest.NewRecorder()
				f.router.ServeHTTP(response, req)
				if response.Code != 200 {
					t.Fatal("logout failed")
				}
				var revoked bool
				if err := f.db.QueryRowContext(f.ctx, `SELECT revoked_at IS NOT NULL FROM user_sessions WHERE id = $1`, f.session.Identity.SessionID).Scan(&revoked); err != nil || !revoked {
					t.Fatal("logout revocation was not durable:", err)
				}
			case "expiry":
				if _, err := f.db.ExecContext(f.ctx, `UPDATE user_sessions SET expires_at = created_at + interval '1 microsecond' WHERE id = $1`, f.session.Identity.SessionID); err != nil {
					t.Fatal(err)
				}
			case "role":
				if _, err := f.db.ExecContext(f.ctx, `ALTER TABLE users DROP CONSTRAINT users_role_check`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.ExecContext(f.ctx, `UPDATE users SET role = 'VIEWER' WHERE id = $1`, f.owner.UserID); err != nil {
					t.Fatal(err)
				}
			case "archive":
				if _, err := f.db.ExecContext(f.ctx, `UPDATE projects SET status = 'ARCHIVED' WHERE id = $1`, f.owner.ProjectID); err != nil {
					t.Fatal(err)
				}
			}
			waitEventHandlerDone(t, done)
			remaining, err := io.ReadAll(stream.body.Body)
			if err != nil || len(remaining) != 0 {
				t.Fatal("access loss did not close cleanly without further events")
			}
			if stats := f.db.Stats(); stats.InUse != 0 {
				t.Fatalf("terminated stream holds %d DB connections", stats.InUse)
			}
		})
	}
}

func TestEventStreamRequestCancellationStopsIdlePolling(t *testing.T) {
	f := newEventFixture(t)
	server, done := eventTestServer(t, f, 30*time.Second)
	stream := openTestEventStream(t, f, server, "?after=0", "")
	stream.next(t, time.Second) // Initial retry flush proves the handler is connected.
	stream.cancel()
	waitEventHandlerDone(t, done)
	if stats := f.db.Stats(); stats.InUse != 0 {
		t.Fatal("canceled idle stream retained a database connection")
	}
}

func TestEventStreamSendsHeartbeatDuringIdleConnection(t *testing.T) {
	f := newEventFixture(t)
	server, done := eventTestServer(t, f, 300*time.Millisecond)
	stream := openTestEventStream(t, f, server, "?after=0", "")
	stream.next(t, time.Second)
	started := time.Now()
	frame := stream.next(t, 12*time.Second)
	if len(frame.comments) == 0 || frame.data != "" || frame.id != "" || time.Since(started) < 8*time.Second {
		t.Fatal("expected an idle heartbeat comment at roughly ten seconds")
	}
	f.create(t, "after-heartbeat")
	stream.event(t, 1)
	stream.cancel()
	stream.body.Body.Close()
	waitEventHandlerDone(t, done)
}

func TestEventStreamRedactsLegacyPayloadBeforeBroadcast(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "legacy-stream")
	if _, err := f.db.ExecContext(f.ctx, `UPDATE agent_events SET data = data || jsonb_build_object('description', $1::text, 'objectKey', 'private-storage-key', 'rawProviderOutput', 'private provider output')`, syntheticUnsafeText()); err != nil {
		t.Fatal(err)
	}
	server, done := eventTestServer(t, f, 30*time.Second)
	stream := openTestEventStream(t, f, server, "?after=0", "")
	frame := stream.event(t, 1)
	assertNoEventSecrets(t, []byte(frame.data))
	if strings.Contains(frame.data, "private-storage-key") || strings.Contains(frame.data, "private provider output") {
		t.Fatal("legacy stream leaked raw internal data")
	}
	stream.cancel()
	stream.body.Body.Close()
	waitEventHandlerDone(t, done)
}
