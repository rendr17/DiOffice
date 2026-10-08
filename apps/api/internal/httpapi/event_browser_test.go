package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/directory"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

// Opt-in only: this test never connects the browser to a public API, uses no
// user browser profile, and keeps every fixture mutation in a disposable schema.
// DIOFFICE_BROWSER_OUTPUT is an artifact directory, not a database/config file.
func TestEventBrowserPersistedHistoryAndSSE(t *testing.T) {
	if os.Getenv("DIOFFICE_BROWSER_TEST") != "true" {
		t.Skip("set DIOFFICE_BROWSER_TEST=true to run the isolated real browser test")
	}
	if os.Getenv("MIGRATION_TEST_DATABASE_URL") == "" {
		t.Fatal("the opt-in browser test requires MIGRATION_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate browser fixture source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".."))
	output := os.Getenv("DIOFFICE_BROWSER_OUTPUT")
	if output == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatal("locate user cache for browser artifacts")
		}
		output = filepath.Join(cache, "hermes", "cache", "scratch", fmt.Sprintf("dioffice-event-browser-%d", time.Now().UnixNano()))
	}
	output, err := filepath.Abs(output)
	if err != nil || os.MkdirAll(output, 0700) != nil {
		t.Fatal("create browser artifact directory")
	}
	t.Logf("browser artifacts: %s", output)

	// This connection is used only for schema-qualified, read-only public row
	// fingerprints and the final schema-existence check. Never log its DSN.
	admin, err := sql.Open("pgx", os.Getenv("MIGRATION_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("open public read-only verification connection")
	}
	before, err := eventBrowserPublicRows(ctx, admin)
	if err != nil {
		admin.Close()
		t.Fatal("read public row fingerprint before isolated test")
	}
	var schema string
	var fixture *eventBrowserFixture
	var api, control *httptest.Server
	var vite, browser *eventBrowserProcess
	var webOrigin string
	// Register BEFORE newHTTPAPITestDatabase: LIFO makes this run after the
	// helper has closed its pool and dropped the schema, including on failure.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		after, rowsErr := eventBrowserPublicRows(cleanupCtx, admin)
		unchanged := rowsErr == nil && reflect.DeepEqual(before, after)
		var exists bool
		schemaErr := admin.QueryRowContext(cleanupCtx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists)
		result := map[string]any{
			"schema": schema, "schemaDropped": schema != "" && schemaErr == nil && !exists,
			"publicRowsUnchanged": unchanged, "publicTablesChecked": len(before),
			"browserRunnerStopped": browser == nil || browser.stopped(),
			"viteStopped":          vite == nil || vite.stopped(), "testPassed": !t.Failed(),
		}
		if api != nil {
			result["apiClosed"] = eventBrowserEndpointClosed(api.URL + "/readyz")
		}
		if control != nil {
			result["controllerClosed"] = eventBrowserEndpointClosed(control.URL + "/fixture")
		}
		if webOrigin != "" {
			result["viteEndpointClosed"] = eventBrowserEndpointClosed(webOrigin + "/")
		}
		if fixture != nil {
			fixture.mu.Lock()
			result["activeStreamsAfterCleanup"] = len(fixture.active)
			result["fixtureOnlyMutations"] = append([]string{}, fixture.mutations...)
			fixture.mu.Unlock()
		}
		if !unchanged {
			t.Error("public rows changed or their post-test read failed")
		}
		if schema == "" || schemaErr != nil || exists {
			t.Error("disposable browser schema cleanup was not verified")
		}
		for _, key := range []string{"browserRunnerStopped", "viteStopped", "apiClosed", "controllerClosed", "viteEndpointClosed"} {
			if value, present := result[key]; present && value != true {
				t.Errorf("cleanup check %s failed", key)
			}
		}
		if fixture != nil && result["activeStreamsAfterCleanup"] != 0 {
			t.Error("test-owned streams survived cleanup")
		}
		result["testPassed"] = !t.Failed()
		payload, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(filepath.Join(output, "dioffice-events-fixture.json"), payload, 0600); err != nil {
			t.Error("write fixture verification artifact")
		}
		admin.Close()
	})

	db := newHTTPAPITestDatabase(t, ctx)
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || !strings.HasPrefix(schema, "dioffice_http_test_") {
		t.Fatal("browser test database is not an isolated HTTP test schema")
	}
	fixture = &eventBrowserFixture{db: db, schema: schema, active: make(map[int]context.CancelFunc)}
	// Registered after the schema helper, so all runtimes stop before its drop.
	t.Cleanup(func() {
		if browser != nil {
			browser.stop(t)
		}
		if vite != nil {
			vite.stop(t)
		}
		fixture.disconnect("")
		if control != nil {
			control.CloseClientConnections()
			control.Close()
		}
		if api != nil {
			api.CloseClientConnections()
			api.Close()
		}
	})
	authService := auth.NewService(db)
	session, err := authService.CreateDevelopmentSession(ctx)
	if err != nil {
		t.Fatal("seed passwordless isolated Owner")
	}
	fixture.org, fixture.user = session.Identity.OrganizationID, session.Identity.UserID
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM employees WHERE organization_id = $1 AND slug = 'deni'`, fixture.org).Scan(&fixture.employee); err != nil {
		t.Fatal("find isolated Deni")
	}
	if err := db.QueryRowContext(ctx, `UPDATE projects SET name = 'A / event browser' WHERE organization_id = $1 RETURNING id::text`, fixture.org).Scan(&fixture.projectA); err != nil {
		t.Fatal("name isolated primary project")
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (organization_id, name) VALUES ($1, 'B / event browser') RETURNING id::text`, fixture.org).Scan(&fixture.projectB); err != nil {
		t.Fatal("seed isolated second project")
	}
	taskService := tasks.NewService(db)
	for _, seed := range []struct{ project, title string }{{fixture.projectA, "Browser seed draft"}, {fixture.projectB, "Browser second-project seed"}} {
		task, _, err := taskService.CreateDraft(ctx, tasks.CreateDraftInput{
			OrganizationID: fixture.org, ProjectID: seed.project, ActorUserID: fixture.user,
			AssigneeEmployeeID: fixture.employee, IdempotencyKey: "browser-fixture-" + seed.project,
			Title: seed.title, Description: "Disposable browser fixture; no agent execution.",
			AcceptanceCriteria: json.RawMessage(`[]`), RequiredChecks: json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL",
		})
		if err != nil {
			t.Fatal("seed durable browser draft through real task service")
		}
		if seed.project == fixture.projectA {
			fixture.seedTask = task.ID
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve test-owned Vite port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	webOrigin = "http://127.0.0.1:" + strconv.Itoa(port)
	fixture.webOrigin = webOrigin
	api = httptest.NewServer(fixture.observe(NewRouter(Dependencies{
		DB: db, Auth: authService, Directory: directory.NewService(db), Tasks: taskService,
		DevAuthBypass: true, SecureCookies: false, WebOrigin: webOrigin,
	})))
	fixture.apiOrigin = api.URL
	control = httptest.NewServer(http.HandlerFunc(fixture.serve))
	vitePath := filepath.Join(root, "apps", "web", "node_modules", "vite", "bin", "vite.js")
	if _, err := os.Stat(vitePath); err != nil {
		t.Fatal("installed Vite CLI is required; test does not modify dependencies")
	}
	environment := eventBrowserChildEnvironment(output)
	viteCmd := exec.Command("node", vitePath, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--strictPort")
	viteCmd.Dir = filepath.Join(root, "apps", "web")
	viteCmd.Env = append(environment, "DIOFFICE_API_PROXY_TARGET="+api.URL, "VITE_API_BASE_URL=", "VITE_DEV_AUTH_BYPASS=true")
	viteCmd.Stdout, viteCmd.Stderr = io.Discard, io.Discard
	vite = eventBrowserStart(t, viteCmd)
	readyCtx, readyCancel := context.WithTimeout(ctx, 25*time.Second)
	defer readyCancel()
	if err := eventBrowserReady(readyCtx, api.URL+"/readyz", 200); err != nil {
		t.Fatal("test API did not become ready")
	}
	if err := eventBrowserReady(readyCtx, webOrigin+"/", 200); err != nil {
		t.Fatal("test-owned Vite did not become ready")
	}
	if err := eventBrowserReady(readyCtx, webOrigin+"/api/v1/auth/session", 401); err != nil {
		t.Fatal("test-owned Vite proxy did not reach the isolated API")
	}
	var browserOutput bytes.Buffer
	browserCmd := exec.Command("uv", "run", "--with", "playwright", "python", filepath.Join(root, "tools", "ui-smoke", "check-events.py"), "--fixture-url", control.URL, "--output", output)
	browserCmd.Dir, browserCmd.Env = root, environment
	browserCmd.Stdout, browserCmd.Stderr = &browserOutput, &browserOutput
	browser = eventBrowserStart(t, browserCmd)
	select {
	case <-browser.done:
		t.Logf("browser runner: %s", strings.TrimSpace(browserOutput.String()))
		if browser.err != nil {
			t.Fatal("real browser assertions failed; see browser JSON artifact")
		}
	case <-ctx.Done():
		browser.stop(t)
		t.Fatal("real browser test exceeded its 210-second bound")
	}
	// Exit zero is insufficient: independently verify the runner's acceptance
	// checklist and its own browser/request-context cleanup from the saved report.
	payload, err := os.ReadFile(filepath.Join(output, "dioffice-events-browser.json"))
	if err != nil {
		t.Fatal("browser runner did not produce its report")
	}
	var report struct {
		Passed bool `json:"passed"`
		Checks []struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
		} `json:"checks"`
		Cleanup struct {
			BrowserClosed           bool `json:"browserClosed"`
			RequestContextsDisposed bool `json:"requestContextsDisposed"`
		} `json:"cleanup"`
		UIQA struct {
			Passed bool `json:"passed"`
			Checks []struct {
				Passed bool `json:"passed"`
			} `json:"checks"`
			IsolatedContextClosed bool `json:"isolatedContextClosed"`
		} `json:"uiQA"`
	}
	if json.Unmarshal(payload, &report) != nil || !report.Passed || len(report.Checks) != 13 || !report.Cleanup.BrowserClosed || !report.Cleanup.RequestContextsDisposed {
		t.Fatal("browser report did not verify all 13 checks and browser cleanup")
	}
	for _, check := range report.Checks {
		if !check.Passed {
			t.Errorf("browser check %s failed", check.Name)
		}
	}
	if !report.UIQA.Passed || len(report.UIQA.Checks) != 13 || !report.UIQA.IsolatedContextClosed {
		t.Fatal("read-only UI QA did not verify all 13 checks and isolated context cleanup")
	}
	for _, check := range report.UIQA.Checks {
		if !check.Passed {
			t.Fatal("read-only UI QA contains a failed check")
		}
	}
	t.Logf("verified %d real event checks + %d read-only UI checks; schema/public/runtime cleanup follows", len(report.Checks), len(report.UIQA.Checks))
}

type eventBrowserStream struct {
	ID          int    `json:"id"`
	ProjectID   string `json:"projectId"`
	After       string `json:"after"`
	LastEventID string `json:"lastEventId"`
	Status      int    `json:"status"`
	Closed      bool   `json:"closed"`
}

type eventBrowserFixture struct {
	db                                                                              *sql.DB
	schema, org, user, employee, projectA, projectB, seedTask, webOrigin, apiOrigin string
	mu                                                                              sync.Mutex
	streams                                                                         []*eventBrowserStream
	active                                                                          map[int]context.CancelFunc
	mutations                                                                       []string
}

func (f *eventBrowserFixture) knownProject(id string) bool {
	return id == f.projectA || id == f.projectB
}

func (f *eventBrowserFixture) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 6 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "projects" || parts[4] != "events" || parts[5] != "stream" || !f.knownProject(parts[3]) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		f.mu.Lock()
		record := &eventBrowserStream{ID: len(f.streams), ProjectID: parts[3], After: r.URL.Query().Get("after"), LastEventID: r.Header.Get("Last-Event-ID")}
		f.streams = append(f.streams, record)
		f.active[record.ID] = cancel
		f.mu.Unlock()
		defer func() {
			cancel()
			f.mu.Lock()
			delete(f.active, record.ID)
			record.Closed = true
			f.mu.Unlock()
		}()
		next.ServeHTTP(&eventBrowserWriter{ResponseWriter: w, status: func(code int) { f.mu.Lock(); record.Status = code; f.mu.Unlock() }}, r.WithContext(ctx))
	})
}

// Preserve ResponseController traversal and real SSE writes/flushes. This
// observer records only safe cursor/status metadata, never cookies or payloads.
type eventBrowserWriter struct {
	http.ResponseWriter
	status  func(int)
	written bool
}

func (w *eventBrowserWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *eventBrowserWriter) WriteHeader(code int) {
	if !w.written {
		w.written = true
		w.status(code)
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *eventBrowserWriter) Write(p []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(p)
}
func (w *eventBrowserWriter) Flush() {
	if !w.written {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (f *eventBrowserFixture) disconnect(project string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, cancel := range f.active {
		if project == "" || f.streams[id].ProjectID == project {
			cancel()
		}
	}
}

func (f *eventBrowserFixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !isLoopbackRemote(r.RemoteAddr) {
		writeError(w, 403, "fixture_loopback_only")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.Method == "GET" && r.URL.Path == "/fixture" {
		writeJSON(w, 200, map[string]any{"protocol": "dioffice-event-browser-v1", "isolated": true, "schema": f.schema, "webOrigin": f.webOrigin, "apiOrigin": f.apiOrigin, "organizationId": f.org, "employeeId": f.employee, "projectA": f.projectA, "projectB": f.projectB, "seedTaskId": f.seedTask})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/fixture/state" {
		projects := make(map[string]any)
		for _, project := range []string{f.projectA, f.projectB} {
			var head, taskCount, eventCount, outboxCount, auditCount, idempotencyCount int64
			err := f.db.QueryRowContext(ctx, `SELECT last_event_sequence,
				(SELECT count(*) FROM tasks WHERE organization_id = $1 AND project_id = $2),
				(SELECT count(*) FROM agent_events WHERE organization_id = $1 AND project_id = $2),
				(SELECT count(*) FROM event_outbox WHERE project_id = $2),
				(SELECT count(*) FROM audit_records WHERE organization_id = $1 AND project_id = $2),
				(SELECT count(*) FROM idempotency_keys WHERE organization_id = $1 AND response_json->>'projectId' = $2::text)
				FROM projects WHERE organization_id = $1 AND id = $2`, f.org, project).Scan(&head, &taskCount, &eventCount, &outboxCount, &auditCount, &idempotencyCount)
			if err != nil {
				writeError(w, 500, "fixture_state_failed")
				return
			}
			rows, err := f.db.QueryContext(ctx, `SELECT event_id::text, task_id::text, employee_id::text, stream_sequence FROM agent_events WHERE organization_id = $1 AND project_id = $2 ORDER BY stream_sequence`, f.org, project)
			if err != nil {
				writeError(w, 500, "fixture_events_failed")
				return
			}
			items := []map[string]any{}
			for rows.Next() {
				var eventID string
				var taskID, employeeID sql.NullString
				var sequence int64
				if err := rows.Scan(&eventID, &taskID, &employeeID, &sequence); err != nil {
					rows.Close()
					writeError(w, 500, "fixture_event_scan_failed")
					return
				}
				items = append(items, map[string]any{"eventId": eventID, "taskId": taskID.String, "employeeId": employeeID.String, "streamSequence": sequence})
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				writeError(w, 500, "fixture_events_failed")
				return
			}
			projects[project] = map[string]any{"head": head, "taskCount": taskCount, "eventCount": eventCount, "outboxCount": outboxCount, "auditCount": auditCount, "idempotencyCount": idempotencyCount, "events": items}
		}
		f.mu.Lock()
		streams := make([]eventBrowserStream, len(f.streams))
		active := make(map[string]int)
		for i, stream := range f.streams {
			streams[i] = *stream
			if _, exists := f.active[stream.ID]; exists {
				active[stream.ProjectID]++
			}
		}
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"projects": projects, "streams": streams, "activeStreams": active})
		return
	}
	if r.Method != "POST" {
		writeError(w, 404, "fixture_route_not_found")
		return
	}
	switch r.URL.Path {
	case "/fixture/disconnect":
		var body struct {
			ProjectID string `json:"projectId"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body) != nil || !f.knownProject(body.ProjectID) {
			writeError(w, 400, "fixture_invalid_project")
			return
		}
		f.disconnect(body.ProjectID)
		f.mu.Lock()
		f.mutations = append(f.mutations, "disconnect_test_owned_stream")
		f.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"disconnected": true})
	case "/fixture/task-snapshot":
		result, err := f.db.ExecContext(ctx, `UPDATE tasks SET title = 'Browser authoritative snapshot', description = 'Authoritative DB-only snapshot, not the task.created payload.', status = 'BLOCKED', priority = 'HIGH', task_version = task_version + 1, updated_at = now() WHERE organization_id = $1 AND project_id = $2 AND id = $3`, f.org, f.projectA, f.seedTask)
		if err != nil {
			writeError(w, 500, "fixture_task_update_failed")
			return
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			writeError(w, 500, "fixture_task_update_failed")
			return
		}
		f.mu.Lock()
		f.mutations = append(f.mutations, "update_disposable_seed_task_snapshot_without_event")
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"updated": true, "taskId": f.seedTask, "emittedEvent": false})
	case "/fixture/expire-cursor":
		var body struct {
			ProjectID string `json:"projectId"`
			After     int64  `json:"after"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body) != nil || !f.knownProject(body.ProjectID) || body.After < 1 {
			writeError(w, 400, "fixture_invalid_cursor")
			return
		}
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			writeError(w, 500, "fixture_expiry_failed")
			return
		}
		defer tx.Rollback()
		var head int64
		if tx.QueryRowContext(ctx, `SELECT last_event_sequence FROM projects WHERE organization_id = $1 AND id = $2 FOR UPDATE`, f.org, body.ProjectID).Scan(&head) != nil || head-body.After < 2 {
			writeError(w, 409, "fixture_requires_committed_cursor_gap")
			return
		}
		// FK order matters; preserve the durable head and latest real event.
		if _, err := tx.ExecContext(ctx, `DELETE FROM event_outbox WHERE project_id = $1 AND stream_sequence < $2`, body.ProjectID, head); err != nil {
			writeError(w, 500, "fixture_expiry_failed")
			return
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_events WHERE organization_id = $1 AND project_id = $2 AND stream_sequence < $3`, f.org, body.ProjectID, head); err != nil {
			writeError(w, 500, "fixture_expiry_failed")
			return
		}
		if tx.Commit() != nil {
			writeError(w, 500, "fixture_expiry_failed")
			return
		}
		f.mu.Lock()
		f.mutations = append(f.mutations, "delete_disposable_retained_event_rows_for_cursor_expiry")
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"head": head, "retainedMinimum": head, "expiredAfter": body.After})
	default:
		writeError(w, 404, "fixture_route_not_found")
	}
}

type eventBrowserProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func eventBrowserStart(t *testing.T, cmd *exec.Cmd) *eventBrowserProcess {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start test-owned %s process: %v", filepath.Base(cmd.Path), err)
	}
	p := &eventBrowserProcess{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p
}
func (p *eventBrowserProcess) stopped() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
func (p *eventBrowserProcess) stop(t *testing.T) {
	t.Helper()
	if p.stopped() {
		return
	}
	if runtime.GOOS == "windows" {
		// Only a PID we started, never an image name, port owner, or user runtime.
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(p.cmd.Process.Pid), "/T", "/F").Run()
	} else {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		t.Error("test-owned process cleanup timed out")
	}
}

func eventBrowserChildEnvironment(scratch string) []string {
	// Allowlist rather than pass-through: DB, object store, tokens, secrets,
	// proxy credentials, and unrelated runtime configuration cannot reach Node
	// or Playwright. Browser contexts are ephemeral and never serialize cookies.
	allowed := map[string]bool{"PATH": true, "SYSTEMROOT": true, "WINDIR": true, "USERPROFILE": true, "HOME": true, "LOCALAPPDATA": true, "APPDATA": true, "PROGRAMDATA": true, "PROGRAMFILES": true, "PROGRAMFILES(X86)": true, "COMSPEC": true, "PATHEXT": true, "PROCESSOR_ARCHITECTURE": true, "NUMBER_OF_PROCESSORS": true, "UV_CACHE_DIR": true}
	env := []string{}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if allowed[strings.ToUpper(key)] {
			env = append(env, item)
		}
	}
	return append(env, "TMP="+scratch, "TEMP="+scratch, "TMPDIR="+scratch, "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "UV_NO_PROGRESS=1", "UV_NO_CONFIG=1")
}

func eventBrowserReady(ctx context.Context, target string, status int) error {
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == status {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func eventBrowserEndpointClosed(target string) bool {
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(target)
	if err == nil {
		response.Body.Close()
	}
	return err != nil
}

func eventBrowserPublicRows(ctx context.Context, db *sql.DB) (map[string]string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	tables := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var fingerprint string
		// Only aggregate opaque hashes in memory; never return or log row data.
		if err := tx.QueryRowContext(ctx, `SELECT count(*)::text || ':' || md5(COALESCE(string_agg(row_hash, '' ORDER BY row_hash), '')) FROM (SELECT md5(to_jsonb(t)::text) AS row_hash FROM public.`+quoted+` AS t) hashes`).Scan(&fingerprint); err != nil {
			return nil, err
		}
		result[table] = fingerprint
	}
	return result, tx.Commit()
}
