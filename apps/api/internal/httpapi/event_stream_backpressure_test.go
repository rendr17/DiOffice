package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
)

// The network boundary alone is controlled; handlers, sessions and event reads
// still use the real disposable PostgreSQL schema.
type stalledEventWriter struct {
	header    http.Header
	mu        sync.Mutex
	deadline  time.Time
	updates   chan struct{}
	blocked   chan struct{}
	once      sync.Once
	frames    int
	unbounded bool
}

func newStalledEventWriter() *stalledEventWriter {
	return &stalledEventWriter{header: make(http.Header), updates: make(chan struct{}, 1), blocked: make(chan struct{})}
}
func (w *stalledEventWriter) Header() http.Header { return w.header }
func (w *stalledEventWriter) WriteHeader(int)     {}
func (w *stalledEventWriter) FlushError() error   { return nil }
func (w *stalledEventWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	select {
	case w.updates <- struct{}{}:
	default:
	}
	return nil
}
func (w *stalledEventWriter) Write(raw []byte) (int, error) {
	if strings.HasPrefix(string(raw), "retry:") {
		return len(raw), nil
	}
	w.mu.Lock()
	w.frames++
	if w.deadline.IsZero() || time.Until(w.deadline) > time.Second {
		w.unbounded = true
	}
	w.mu.Unlock()
	w.once.Do(func() { close(w.blocked) })
	for {
		w.mu.Lock()
		deadline := w.deadline
		w.mu.Unlock()
		if deadline.IsZero() {
			<-w.updates
			continue
		}
		if !time.Now().Before(deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-timer.C:
			return 0, os.ErrDeadlineExceeded
		case <-w.updates:
			timer.Stop()
		}
	}
}

func TestWriteEventFrameHonorsEarlierContextDeadline(t *testing.T) {
	writer := newStalledEventWriter()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := writeEventFrame(ctx, writer, http.NewResponseController(writer), "data: {}\n\n")
	if err == nil || time.Since(started) > 200*time.Millisecond {
		t.Fatal("frame write ignored the shorter request deadline")
	}
	writer.mu.Lock()
	cleared := writer.deadline.IsZero()
	writer.mu.Unlock()
	if !cleared {
		t.Fatal("frame did not reset the response deadline")
	}
}

func TestWriteEventFrameCancellationInterruptsBackpressure(t *testing.T) {
	writer := newStalledEventWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- writeEventFrame(ctx, writer, http.NewResponseController(writer), "data: {}\n\n") }()
	<-writer.blocked
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled stalled write succeeded")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("request cancellation failed to interrupt stalled frame")
	}
}

func TestEventStreamBackpressureIsBoundedWithoutBufferingFurtherEvents(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "one")
	f.create(t, "two")
	writer := newStalledEventWriter()
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+f.owner.ProjectID+"/events/stream?after=0", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
	done := make(chan struct{})
	go func() { f.router.ServeHTTP(writer, req); close(done) }()
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("stalled reader did not reach committed event")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("slow consumer held stream beyond bounded write timeout")
	}
	writer.mu.Lock()
	frames, unbounded, cleared := writer.frames, writer.unbounded, writer.deadline.IsZero()
	writer.mu.Unlock()
	if frames != 1 || unbounded || !cleared {
		t.Fatal("slow stream buffered further frames or used an unbounded deadline")
	}
	if stats := f.db.Stats(); stats.InUse != 0 {
		t.Fatal("slow stream held a database transaction")
	}
}

func TestEventStreamRevocationInterruptsBlockedWriter(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "blocked-before-revocation")
	writer := newStalledEventWriter()
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+f.owner.ProjectID+"/events/stream?after=0", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session.SessionToken})
	done := make(chan struct{})
	go func() { f.router.ServeHTTP(writer, req); close(done) }()
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("no blocked frame")
	}
	if err := f.auth.RevokeSession(f.ctx, f.session.Identity.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.Authenticate(f.ctx, f.session.SessionToken); err == nil || !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("session was not revoked")
	}
	select {
	case <-done:
	case <-time.After(600 * time.Millisecond):
		cancel()
		t.Fatal("access checks were stalled behind backpressure")
	}
}
