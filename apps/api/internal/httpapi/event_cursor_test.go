package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func assertCursorRecovery(t *testing.T, response *httptest.ResponseRecorder, status int, code string, sequences []int64, cursor string) {
	t.Helper()
	assertEventError(t, response, status, code)
	var body struct {
		Snapshot historyResponse `json:"snapshot"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertSequences(t, body.Snapshot, sequences, cursor, false)
}

func TestEventHistoryRejectsAheadCursorWithAuthorizedSnapshot(t *testing.T) {
	f := newEventFixture(t)
	for _, key := range []string{"a", "b", "c"} {
		f.create(t, key)
	}
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	for _, after := range []string{"4", "9223372036854775807"} {
		t.Run(after, func(t *testing.T) {
			assertCursorRecovery(t, f.get(path+"?after="+after), 409, "event_cursor_ahead", []int64{1, 2, 3}, "3")
		})
	}
}

func TestEventHistoryRejectsExpiredCursorWithoutSilentlySkipping(t *testing.T) {
	f := newEventFixture(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		f.create(t, key)
	}
	// Model retention only in an isolated schema; no retention feature or user rows are touched.
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM event_outbox WHERE stream_sequence < 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM agent_events WHERE stream_sequence < 3`); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	assertCursorRecovery(t, f.get(path+"?after=1"), 410, "event_cursor_expired", []int64{3, 4}, "4")
	assertSequences(t, decodeHistory(t, f.get(path+"?after=2")), []int64{3, 4}, "4", false)
	assertSequences(t, decodeHistory(t, f.get(path+"?after=0")), []int64{3, 4}, "4", false)
	assertSequences(t, decodeHistory(t, f.get(path)), []int64{3, 4}, "4", false)
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM event_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM agent_events`); err != nil {
		t.Fatal(err)
	}
	assertCursorRecovery(t, f.get(path+"?after=1"), 410, "event_cursor_expired", nil, "4")
	assertSequences(t, decodeHistory(t, f.get(path+"?after=4")), nil, "4", false)
}
