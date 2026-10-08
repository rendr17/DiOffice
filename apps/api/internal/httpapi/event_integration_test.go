package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

type eventFixture struct {
	ctx     context.Context
	db      *sql.DB
	auth    *auth.Service
	tasks   *tasks.Service
	owner   auth.BootstrapOwnerResult
	session auth.LoginSession
	router  http.Handler
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	db := newHTTPAPITestDatabase(t, ctx)
	a := auth.NewService(db)
	owner, err := a.BootstrapOwner(ctx, auth.BootstrapOwnerInput{
		OrganizationName: "Isolated event tests", ProjectName: "Persisted history",
		Email: "event-owner@example.invalid", Password: "only-a-synthetic-test-password",
	})
	if err != nil {
		t.Fatal("bootstrap isolated fixture:", err)
	}
	session, err := a.Login(ctx, auth.LoginInput{
		OrganizationID: owner.OrganizationID, Email: "event-owner@example.invalid", Password: "only-a-synthetic-test-password",
	})
	if err != nil {
		t.Fatal("login isolated fixture:", err)
	}
	ts := tasks.NewService(db)
	return &eventFixture{ctx: ctx, db: db, auth: a, tasks: ts, owner: owner, session: session,
		router: NewRouter(Dependencies{DB: db, Auth: a, Tasks: ts})}
}

func (f *eventFixture) create(t *testing.T, key string) tasks.Task {
	t.Helper()
	task, _, err := f.tasks.CreateDraft(f.ctx, tasks.CreateDraftInput{
		OrganizationID: f.owner.OrganizationID, ProjectID: f.owner.ProjectID,
		ActorUserID: f.owner.UserID, AssigneeEmployeeID: f.owner.EmployeeID,
		IdempotencyKey: key, Title: "Draft " + key, Description: "No runtime starts",
		AcceptanceCriteria: json.RawMessage(`["Persist a truthful fact"]`), RequiredChecks: json.RawMessage(`[]`),
		TaskType: "feature", Priority: "NORMAL",
	})
	if err != nil {
		t.Fatal("create isolated draft:", err)
	}
	return task
}

func (f *eventFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, req)
	return response
}

type historyResponse struct {
	Items      []map[string]json.RawMessage `json:"items"`
	NextCursor string                       `json:"nextCursor"`
	HasMore    bool                         `json:"hasMore"`
}

func TestEventHistoryReturnsPersistedCanonicalSnapshot(t *testing.T) {
	f := newEventFixture(t)
	task := f.create(t, "first")
	response := f.get("/api/v1/projects/" + f.owner.ProjectID + "/events")
	if response.Code != http.StatusOK {
		t.Fatalf("history status = %d, want 200; body %s", response.Code, response.Body.String())
	}
	var body historyResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.NextCursor != "1" || body.HasMore {
		t.Fatalf("snapshot has %d items, cursor %q, hasMore %v", len(body.Items), body.NextCursor, body.HasMore)
	}
	event := body.Items[0]
	for _, field := range []string{"eventId", "schemaVersion", "eventType", "organizationId", "projectId", "taskId", "employeeId", "attemptId", "sessionId", "workspaceId", "streamSequence", "occurredAt", "recordedAt", "producer", "actor", "correlationId", "causationId", "data"} {
		if _, ok := event[field]; !ok {
			t.Errorf("missing canonical envelope field %s", field)
		}
	}
	if string(event["eventType"]) != `"task.created"` || string(event["streamSequence"]) != "1" || string(event["taskId"]) != `"`+task.ID+`"` {
		t.Fatal("history did not return persisted task-created fact")
	}
	for _, field := range []string{"attemptId", "sessionId", "workspaceId", "causationId"} {
		if string(event[field]) != "null" {
			t.Errorf("%s = %s, want null", field, event[field])
		}
	}
}

func decodeHistory(t *testing.T, response *httptest.ResponseRecorder) historyResponse {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("history status = %d, want 200; body %s", response.Code, response.Body.String())
	}
	var page historyResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func assertSequences(t *testing.T, page historyResponse, want []int64, cursor string, more bool) {
	t.Helper()
	if len(page.Items) != len(want) || page.NextCursor != cursor || page.HasMore != more {
		t.Fatalf("page = %d items, cursor %q, more %v; want %d/%q/%v", len(page.Items), page.NextCursor, page.HasMore, len(want), cursor, more)
	}
	for i, seq := range want {
		if string(page.Items[i]["streamSequence"]) != strconv.FormatInt(seq, 10) {
			t.Fatalf("event %d sequence = %s, want %d", i, page.Items[i]["streamSequence"], seq)
		}
	}
}

func TestEventHistoryReplaysStrictlyAfterCursorInDurableOrder(t *testing.T) {
	f := newEventFixture(t)
	for i := 1; i <= 3; i++ {
		f.create(t, fmt.Sprintf("replay-%d", i))
	}
	// Provider timestamps are not ordering authority; outbox publication is unrelated.
	if _, err := f.db.ExecContext(f.ctx, `UPDATE agent_events SET occurred_at = now() - stream_sequence * interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	assertSequences(t, decodeHistory(t, f.get(path+"?after=1&limit=1")), []int64{2}, "2", true)
	assertSequences(t, decodeHistory(t, f.get(path+"?after=2&limit=1")), []int64{3}, "3", false)
	assertSequences(t, decodeHistory(t, f.get(path+"?after=3&limit=1")), nil, "3", false)
	assertSequences(t, decodeHistory(t, f.get(path+"?after=0&limit=100")), []int64{1, 2, 3}, "3", false)
	// Replacing all HTTP/service objects still reads the same committed history.
	f.router = NewRouter(Dependencies{DB: f.db, Auth: auth.NewService(f.db), Tasks: tasks.NewService(f.db)})
	assertSequences(t, decodeHistory(t, f.get(path+"?after=1&limit=100")), []int64{2, 3}, "3", false)
	var published int
	if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM event_outbox WHERE published_at IS NOT NULL`).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatal("history reads must not mark the outbox published")
	}
}
