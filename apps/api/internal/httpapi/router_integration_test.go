package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

func TestAuthenticatedOwnerCanCreateAndListDraftTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newHTTPAPITestDatabase(t, ctx)
	authService := auth.NewService(db)
	owner, err := authService.BootstrapOwner(ctx, auth.BootstrapOwnerInput{
		OrganizationName: "HTTP integration org",
		ProjectName:      "Task API test",
		Email:            "owner@example.invalid",
		Password:         "correct-horse-battery-staple",
	})
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	router := NewRouter(Dependencies{
		DB: db, Auth: authService, Tasks: tasks.NewService(db), SecureCookies: false,
	})
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("readiness with PostgreSQL status = %d, body %q; want %d", ready.Code, ready.Body.String(), http.StatusOK)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/"+owner.ProjectID+"/tasks", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	loginBody, _ := json.Marshal(map[string]string{
		"organizationId": owner.OrganizationID,
		"email":          "owner@example.invalid",
		"password":       "correct-horse-battery-staple",
	})
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d, body %q; want %d", loginResponse.Code, loginResponse.Body.String(), http.StatusOK)
	}
	cookies := loginResponse.Result().Cookies()
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case sessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil || !sessionCookie.HttpOnly || csrfCookie.HttpOnly ||
		sessionCookie.Secure || csrfCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login cookies do not match local development security settings: session=%+v csrf=%+v", sessionCookie, csrfCookie)
	}
	if strings.Contains(loginResponse.Body.String(), sessionCookie.Value) || strings.Contains(loginResponse.Body.String(), csrfCookie.Value) {
		t.Fatal("login response body exposed a cookie token")
	}

	taskBody := []byte(`{"assigneeEmployeeId":"` + owner.EmployeeID + `","title":"First API task","description":"Verify persistence through HTTP","acceptanceCriteria":["Task is visible in the list"],"requiredChecks":[],"taskType":"feature","priority":"NORMAL"}`)
	createRequest := func(csrfHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+owner.ProjectID+"/tasks", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "http-task-create-001")
		req.AddCookie(sessionCookie)
		req.AddCookie(csrfCookie)
		if csrfHeader != "" {
			req.Header.Set("X-CSRF-Token", csrfHeader)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	if response := createRequest("wrong-token"); response.Code != http.StatusForbidden {
		t.Fatalf("create with invalid CSRF status = %d, want %d", response.Code, http.StatusForbidden)
	}
	created := createRequest(csrfCookie.Value)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %q; want %d", created.Code, created.Body.String(), http.StatusCreated)
	}
	var createdTask tasks.Task
	if err := json.Unmarshal(created.Body.Bytes(), &createdTask); err != nil {
		t.Fatalf("decode created task: %v", err)
	}
	if createdTask.ID == "" || createdTask.Status != "DRAFT" || createdTask.CreatedByUserID != owner.UserID {
		t.Fatalf("created task = %+v, want authenticated DRAFT task", createdTask)
	}
	if replay := createRequest(csrfCookie.Value); replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("idempotent replay status/header = %d/%q, want 201/true", replay.Code, replay.Header().Get("Idempotency-Replayed"))
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+owner.ProjectID+"/tasks", nil)
	listRequest.AddCookie(sessionCookie)
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %q; want %d", listResponse.Code, listResponse.Body.String(), http.StatusOK)
	}
	var listBody struct {
		Items []tasks.Task `json:"items"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(listBody.Items) != 1 || listBody.Items[0].ID != createdTask.ID {
		t.Fatalf("task list = %+v, want exactly the created task", listBody.Items)
	}
}

func TestHealthAndReadinessAreSeparate(t *testing.T) {
	router := NewRouter(Dependencies{})
	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", health.Code, http.StatusOK)
	}
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness without database status = %d, want %d", ready.Code, http.StatusServiceUnavailable)
	}
}

func newHTTPAPITestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	baseURL := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	adminDB, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	if err := adminDB.PingContext(ctx); err != nil {
		adminDB.Close()
		t.Fatalf("connect to PostgreSQL test database: %v", err)
	}
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schemaName := "dioffice_http_test_" + hex.EncodeToString(randomBytes)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE"); err != nil {
			t.Errorf("drop isolated test schema: %v", err)
		}
		if err := adminDB.Close(); err != nil {
			t.Errorf("close PostgreSQL test database: %v", err)
		}
	})
	testURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL test database URL: %v", err)
	}
	query := testURL.Query()
	query.Set("search_path", schemaName)
	testURL.RawQuery = query.Encode()
	db, err := sql.Open("pgx", testURL.String())
	if err != nil {
		t.Fatalf("open isolated test schema: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close isolated test schema: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated test schema: %v", err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate HTTP API test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}
