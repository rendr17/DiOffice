package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

type sseFrame struct {
	id, data, event string
	comments        []string
}

type testEventStream struct {
	body    *http.Response
	scanner *bufio.Scanner
	cancel  context.CancelFunc
}

func eventTestServer(t *testing.T, f *eventFixture, writeTimeout time.Duration) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{}, 16)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.router.ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/events/stream") {
			done <- struct{}{}
		}
	}))
	server.Config.WriteTimeout = writeTimeout
	server.Start()
	t.Cleanup(server.Close)
	return server, done
}

func openTestEventStream(t *testing.T, f *eventFixture, server *httptest.Server, query, lastID string) *testEventStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/projects/"+f.owner.ProjectID+"/events/stream"+query, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal("open event stream:", err)
	}
	t.Cleanup(func() { cancel(); response.Body.Close() })
	if response.StatusCode != 200 {
		t.Fatalf("stream status = %d, want 200", response.StatusCode)
	}
	if response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("missing SSE buffering/privacy headers")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	return &testEventStream{body: response, scanner: scanner, cancel: cancel}
}

func (s *testEventStream) frame() (sseFrame, error) {
	frame := sseFrame{}
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			return frame, nil
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			frame.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			frame.data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "event:"):
			frame.event = line
		case strings.HasPrefix(line, ":"):
			frame.comments = append(frame.comments, line)
		}
	}
	if err := s.scanner.Err(); err != nil {
		return frame, err
	}
	return frame, fmt.Errorf("stream closed")
}

func (s *testEventStream) next(t *testing.T, timeout time.Duration) sseFrame {
	t.Helper()
	type result struct {
		frame sseFrame
		err   error
	}
	ch := make(chan result, 1)
	go func() { frame, err := s.frame(); ch <- result{frame, err} }()
	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatal("read event stream:", result.err)
		}
		return result.frame
	case <-time.After(timeout):
		s.cancel()
		t.Fatal("event stream read timed out")
	}
	return sseFrame{}
}

func (s *testEventStream) event(t *testing.T, want int64) sseFrame {
	t.Helper()
	for {
		frame := s.next(t, 3*time.Second)
		if frame.data == "" {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(frame.data), &envelope); err != nil {
			t.Fatal(err)
		}
		if frame.id != fmt.Sprint(want) || string(envelope["streamSequence"]) != fmt.Sprint(want) || frame.event != "" {
			t.Fatalf("SSE id/sequence/type = %q/%s/%q, want default message %d", frame.id, envelope["streamSequence"], frame.event, want)
		}
		return frame
	}
}

func waitEventHandlerDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event handler did not stop within one second")
	}
}

func createDraftDuringStream(t *testing.T, f *eventFixture, server *httptest.Server, key string, replayed bool) tasks.Task {
	t.Helper()
	body, _ := json.Marshal(createTaskRequest{AssigneeEmployeeID: f.owner.EmployeeID, Title: "Live HTTP draft " + key, Description: "No runtime starts", AcceptanceCriteria: json.RawMessage(`[]`), RequiredChecks: json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL"})
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, server.URL+"/api/v1/projects/"+f.owner.ProjectID+"/tasks", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-CSRF-Token", f.session.CSRFToken)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: f.session.CSRFToken})
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 201 || (response.Header.Get("Idempotency-Replayed") == "true") != replayed {
		t.Fatal("live HTTP draft did not respect creation/idempotency contract")
	}
	var task tasks.Task
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestEventStreamDeliversLiveCommittedAppendWithoutDuplicateCreate(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "initial")
	server, done := eventTestServer(t, f, 300*time.Millisecond)
	stream := openTestEventStream(t, f, server, "?after=0", "")
	frame := stream.event(t, 1)
	assertNoEventSecrets(t, []byte(frame.data))
	// Leave the connection idle past the server's ordinary absolute write timeout.
	time.Sleep(650 * time.Millisecond)
	live := createDraftDuringStream(t, f, server, "live", false)
	frame = stream.event(t, 2)
	if !strings.Contains(frame.data, live.ID) {
		t.Fatal("live stream did not carry newly committed task")
	}
	duplicate := createDraftDuringStream(t, f, server, "live", true)
	if duplicate.ID != live.ID {
		t.Fatal("duplicate create did not replay the same task")
	}
	createDraftDuringStream(t, f, server, "next-distinct", false)
	stream.event(t, 3)
	var count int
	if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM agent_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("duplicate command emitted %d facts, want 3 total", count)
	}
	stream.cancel()
	stream.body.Body.Close()
	waitEventHandlerDone(t, done)
}
