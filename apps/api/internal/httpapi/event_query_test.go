package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func assertEventError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error    string          `json:"error"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON event error status %d", response.Code)
	}
	if response.Code != status || body.Error != code {
		t.Fatalf("error = %d/%q, want %d/%q", response.Code, body.Error, status, code)
	}
	if status != 409 && status != 410 && len(body.Snapshot) > 0 {
		t.Fatal("error disclosed an unauthorized snapshot")
	}
}

func TestEventHistoryRejectsMalformedQueries(t *testing.T) {
	f := newEventFixture(t)
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	for _, raw := range []string{"", "-1", "%2B1", "1.5", "1e2", "%201", "1%20", "01", "9223372036854775808", "x", "1%0A"} {
		t.Run("cursor-"+raw, func(t *testing.T) { assertEventError(t, f.get(path+"?after="+raw), 400, "invalid_event_cursor") })
	}
	t.Run("duplicate-cursor", func(t *testing.T) { assertEventError(t, f.get(path+"?after=0&after=1"), 400, "invalid_event_cursor") })
	for _, raw := range []string{"", "0", "-1", "101", "1.5", "%2B1", "01", "invalid", "999999999999999999999"} {
		t.Run("limit-"+raw, func(t *testing.T) { assertEventError(t, f.get(path+"?after=0&limit="+raw), 400, "invalid_event_query") })
	}
	for _, query := range []string{"limit=0", "limit=1&limit=2", "tenant=ignored", "token=synthetic", "after=0;limit=2", "after=%GG"} {
		t.Run(query, func(t *testing.T) { assertEventError(t, f.get(path+"?"+query), 400, "invalid_event_query") })
	}
	for _, id := range []string{"not-a-uuid", "00000000000000000000000000000000", "00000000-0000-0000-0000-00000000000x"} {
		t.Run(id, func(t *testing.T) {
			assertEventError(t, f.get("/api/v1/projects/"+id+"/events"), 400, "invalid_event_query")
		})
	}
}

func TestEventHistoryAuthorizesBeforeCursorDisclosure(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "authorized-only")
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	anonymous := httptest.NewRecorder()
	f.router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, path+"?after=9223372036854775807", nil))
	assertEventError(t, anonymous, 401, "unauthorized")
	var org, otherProject, archived string
	if err := f.db.QueryRowContext(f.ctx, `INSERT INTO organizations (name) VALUES ('Other event tenant') RETURNING id::text`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(f.ctx, `INSERT INTO projects (organization_id, name, last_event_sequence) VALUES ($1, 'Private other project', 99) RETURNING id::text`, org).Scan(&otherProject); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(f.ctx, `INSERT INTO projects (organization_id, name, status) VALUES ($1, 'Archived event project', 'ARCHIVED') RETURNING id::text`, f.owner.OrganizationID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{otherProject, archived, "00000000-0000-4000-8000-000000000001"} {
		assertEventError(t, f.get("/api/v1/projects/"+id+"/events?after=9223372036854775807"), 404, "project_not_found")
	}
	// Role loss is modeled only in this disposable schema: production v0.1 permits OWNER alone.
	if _, err := f.db.ExecContext(f.ctx, `ALTER TABLE users DROP CONSTRAINT users_role_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `UPDATE users SET role = 'VIEWER' WHERE id = $1`, f.owner.UserID); err != nil {
		t.Fatal(err)
	}
	assertEventError(t, f.get(path+"?after=999"), 403, "forbidden")
}

func TestEventHistorySnapshotIsRecentBoundedAndNonpaginated(t *testing.T) {
	f := newEventFixture(t)
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	page := decodeHistory(t, f.get(path))
	assertSequences(t, page, nil, "0", false)
	if strings.Contains(f.get(path).Body.String(), `"items":null`) {
		t.Fatal("empty history must be []")
	}
	for i := 1; i <= 103; i++ {
		f.create(t, fmt.Sprintf("bounded-%d", i))
	}
	sequences := make([]int64, 100)
	for i := range sequences {
		sequences[i] = int64(i + 4)
	}
	assertSequences(t, decodeHistory(t, f.get(path)), sequences, "103", false)
}
