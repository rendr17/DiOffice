package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

func TestEventStreamReconnectUsesLastEventIDBeforeQuery(t *testing.T) {
	f := newEventFixture(t)
	for _, key := range []string{"one", "two", "three"} {
		f.create(t, key)
	}
	server, done := eventTestServer(t, f, 30*time.Second)
	stream := openTestEventStream(t, f, server, "?after=0", "")
	stream.event(t, 1)
	stream.cancel()
	stream.body.Body.Close()
	waitEventHandlerDone(t, done)
	for _, query := range []string{"?after=0", "?after=malformed", "?after=9223372036854775807"} {
		stream = openTestEventStream(t, f, server, query, "1")
		stream.event(t, 2)
		stream.event(t, 3)
		stream.cancel()
		stream.body.Body.Close()
		waitEventHandlerDone(t, done)
	}
}

func TestEventStreamRejectsCursorAndAccessErrorsBeforeSSEHeaders(t *testing.T) {
	f := newEventFixture(t)
	for _, key := range []string{"one", "two", "three"} {
		f.create(t, key)
	}
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events/stream"
	for _, query := range []string{"?after=", "?after=-1", "?after=%2B1", "?after=1&after=2", "?after=9223372036854775808"} {
		t.Run(query, func(t *testing.T) {
			response := f.get(path + query)
			assertEventError(t, response, 400, "invalid_event_cursor")
			if response.Header().Get("Content-Type") == "text/event-stream" {
				t.Fatal("invalid stream committed SSE headers")
			}
		})
	}
	assertEventError(t, f.get(path+"?after=0&limit=1"), 400, "invalid_event_query")
	assertEventError(t, f.get(path+"?token=synthetic"), 400, "invalid_event_query")
	for _, values := range [][]string{{""}, {"-1"}, {"1", "2"}} {
		req := httptest.NewRequest(http.MethodGet, path+"?after=0", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
		for _, value := range values {
			req.Header.Add("Last-Event-ID", value)
		}
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, req)
		assertEventError(t, response, 400, "invalid_event_cursor")
	}
	assertCursorRecovery(t, f.get(path+"?after=4"), 409, "event_cursor_ahead", []int64{1, 2, 3}, "3")
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM event_outbox WHERE stream_sequence < 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM agent_events WHERE stream_sequence < 3`); err != nil {
		t.Fatal(err)
	}
	assertCursorRecovery(t, f.get(path+"?after=1"), 410, "event_cursor_expired", []int64{3}, "3")
	anonymous := httptest.NewRecorder()
	f.router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, path+"?after=99", nil))
	assertEventError(t, anonymous, 401, "unauthorized")
	var org, other, archived string
	if err := f.db.QueryRowContext(f.ctx, `INSERT INTO organizations (name) VALUES ('Other stream tenant') RETURNING id::text`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(f.ctx, `INSERT INTO projects (organization_id, name) VALUES ($1, 'Private stream project') RETURNING id::text`, org).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(f.ctx, `INSERT INTO projects (organization_id, name, status) VALUES ($1, 'Archived stream project', 'ARCHIVED') RETURNING id::text`, f.owner.OrganizationID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{other, archived, "00000000-0000-4000-8000-000000000001"} {
		assertEventError(t, f.get("/api/v1/projects/"+id+"/events/stream?after=99"), 404, "project_not_found")
	}
	assertEventError(t, f.get("/api/v1/projects/not-a-uuid/events/stream"), 400, "invalid_event_query")
	if _, err := f.db.ExecContext(f.ctx, `ALTER TABLE users DROP CONSTRAINT users_role_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `UPDATE users SET role = 'VIEWER' WHERE id = $1`, f.owner.UserID); err != nil {
		t.Fatal(err)
	}
	assertEventError(t, f.get(path+"?after=99"), 403, "forbidden")
}

func TestEventStreamAllowsReconnectHeaderInCredentialedCORS(t *testing.T) {
	router := NewRouter(Dependencies{WebOrigin: "https://owner.example.invalid"})
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/projects/fixture/events/stream", nil)
	req.Header.Set("Origin", "https://owner.example.invalid")
	req.Header.Set("Access-Control-Request-Headers", "Last-Event-ID")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 204 || !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "Last-Event-ID") || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("credentialed SSE reconnect CORS header is not allowed")
	}
}

func TestEventStreamReplaySurvivesRouterRestartAcrossBoundedPages(t *testing.T) {
	f := newEventFixture(t)
	for i := 1; i <= 103; i++ {
		f.create(t, "page-"+time.Duration(i).String())
	}
	f.router = NewRouter(Dependencies{DB: f.db, Auth: auth.NewService(f.db), Tasks: tasks.NewService(f.db)})
	server, done := eventTestServer(t, f, 30*time.Second)
	stream := openTestEventStream(t, f, server, "?after=0", "")
	for i := int64(1); i <= 103; i++ {
		stream.event(t, i)
	}
	stream.cancel()
	stream.body.Body.Close()
	waitEventHandlerDone(t, done)
}
