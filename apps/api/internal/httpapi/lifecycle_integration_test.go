package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/directory"
	"github.com/rendr17/dioffice/apps/api/internal/repositories"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

// TestRepositoryReadyAndStartLifecycleOverHTTP drives the Owner-facing slice of
// the execution lifecycle: repository registration, the READY gate, and the
// explicit Start that records an attempt and workspace without a worker.
func TestRepositoryReadyAndStartLifecycleOverHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newHTTPAPITestDatabase(t, ctx)
	authService := auth.NewService(db)
	owner, err := authService.BootstrapOwner(ctx, auth.BootstrapOwnerInput{
		OrganizationName: "Lifecycle HTTP org",
		ProjectName:      "Lifecycle project",
		Email:            "owner@lifecycle.invalid",
		Password:         "correct-horse-battery-staple",
	})
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	var otherProjectID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO organizations (name) VALUES ('Other lifecycle tenant') RETURNING id::text
	`).Scan(new(string)); err != nil {
		t.Fatalf("create other tenant: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO projects (organization_id, name)
		SELECT id, 'Other tenant project' FROM organizations WHERE name = 'Other lifecycle tenant'
		RETURNING id::text
	`).Scan(&otherProjectID); err != nil {
		t.Fatalf("create other tenant project: %v", err)
	}

	tasksService := tasks.NewService(db)
	router := NewRouter(Dependencies{
		DB: db, Auth: authService, Directory: directory.NewService(db),
		Tasks: tasksService, Repositories: repositories.NewService(db), SecureCookies: false,
	})

	loginBody, _ := json.Marshal(map[string]string{
		"organizationId": owner.OrganizationID,
		"email":          "owner@lifecycle.invalid",
		"password":       "correct-horse-battery-staple",
	})
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d, body %q; want %d", loginResponse.Code, loginResponse.Body.String(), http.StatusOK)
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatal("login did not issue session and CSRF cookies")
	}

	repositoryPath := "/api/v1/projects/" + owner.ProjectID + "/repository"
	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, repositoryPath, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated repository read status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	getRepository := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, repositoryPath, nil)
		request.AddCookie(sessionCookie)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := getRepository(); response.Code != http.StatusNotFound {
		t.Fatalf("repository read before connect status = %d, body %q; want 404", response.Code, response.Body.String())
	}
	foreignRepository := httptest.NewRecorder()
	foreignRequest := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+otherProjectID+"/repository", nil)
	foreignRequest.AddCookie(sessionCookie)
	router.ServeHTTP(foreignRepository, foreignRequest)
	if foreignRepository.Code != http.StatusNotFound {
		t.Fatalf("foreign project repository read status = %d, want 404", foreignRepository.Code)
	}

	putRepository := func(csrfHeader, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, repositoryPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(sessionCookie)
		request.AddCookie(csrfCookie)
		if csrfHeader != "" {
			request.Header.Set("X-CSRF-Token", csrfHeader)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := putRepository("wrong-token", `{"owner":"acme","name":"widgets","defaultBranch":"main"}`); response.Code != http.StatusForbidden {
		t.Fatalf("repository connect with invalid CSRF status = %d, want 403", response.Code)
	}
	if response := putRepository(csrfCookie.Value, `{"owner":"bad owner!","name":"widgets","defaultBranch":"main"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("repository connect with invalid owner status = %d, want 400", response.Code)
	}
	connected := putRepository(csrfCookie.Value, `{"owner":"acme","name":"widgets","defaultBranch":"main"}`)
	if connected.Code != http.StatusOK {
		t.Fatalf("repository connect status = %d, body %q; want 200", connected.Code, connected.Body.String())
	}
	var repository repositories.Repository
	if err := json.Unmarshal(connected.Body.Bytes(), &repository); err != nil {
		t.Fatalf("decode repository response: %v", err)
	}
	if repository.Provider != "github" || repository.Owner != "acme" || repository.Name != "widgets" || repository.DefaultBranch != "main" {
		t.Fatalf("repository = %+v, want acme/widgets@main on github", repository)
	}
	if response := getRepository(); response.Code != http.StatusOK {
		t.Fatalf("repository read after connect status = %d, want 200", response.Code)
	}

	draft, _, err := tasksService.CreateDraft(ctx, tasks.CreateDraftInput{
		OrganizationID:     owner.OrganizationID,
		ProjectID:          owner.ProjectID,
		ActorUserID:        owner.UserID,
		AssigneeEmployeeID: owner.EmployeeID,
		IdempotencyKey:     "lifecycle-draft-001",
		Title:              "Lifecycle task",
		Description:        "Verify READY gate and explicit Start over HTTP.",
		AcceptanceCriteria: json.RawMessage(`["Task reaches PROVISIONING"]`),
		RequiredChecks:     json.RawMessage(`[]`),
		TaskType:           "feature",
		Priority:           "NORMAL",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}

	readyPath := "/api/v1/projects/" + owner.ProjectID + "/tasks/" + draft.ID + "/ready"
	postTaskCommand := func(path, idempotencyKey, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", idempotencyKey)
		request.AddCookie(sessionCookie)
		request.AddCookie(csrfCookie)
		request.Header.Set("X-CSRF-Token", csrfCookie.Value)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	incomplete := postTaskCommand(readyPath, "lifecycle-ready-incomplete", `{"expectedVersion":1}`)
	if incomplete.Code != http.StatusConflict {
		t.Fatalf("mark ready without manifest digest status = %d, body %q; want 409", incomplete.Code, incomplete.Body.String())
	}
	var incompleteBody struct {
		Error   string   `json:"error"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(incomplete.Body.Bytes(), &incompleteBody); err != nil {
		t.Fatalf("decode task_incomplete response: %v", err)
	}
	if incompleteBody.Error != "task_incomplete" || len(incompleteBody.Missing) != 1 || incompleteBody.Missing[0] != "manifestDigest" {
		t.Fatalf("task_incomplete body = %+v, want missing=[manifestDigest]", incompleteBody)
	}

	manifestDigest := strings.Repeat("ab", 32)
	ready := postTaskCommand(readyPath, "lifecycle-ready-001", `{"expectedVersion":1,"manifestDigest":"`+manifestDigest+`"}`)
	if ready.Code != http.StatusOK {
		t.Fatalf("mark ready status = %d, body %q; want 200", ready.Code, ready.Body.String())
	}
	var readyTask tasks.Task
	if err := json.Unmarshal(ready.Body.Bytes(), &readyTask); err != nil {
		t.Fatalf("decode ready task: %v", err)
	}
	if readyTask.Status != "READY" || readyTask.Version != draft.Version+1 || readyTask.ManifestDigest != manifestDigest {
		t.Fatalf("ready task = %+v, want READY v%d with recorded digest", readyTask, draft.Version+1)
	}
	if replay := postTaskCommand(readyPath, "lifecycle-ready-001", `{"expectedVersion":1,"manifestDigest":"`+manifestDigest+`"}`); replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("ready replay status/header = %d/%q, want 200/true", replay.Code, replay.Header().Get("Idempotency-Replayed"))
	}

	startPath := "/api/v1/projects/" + owner.ProjectID + "/tasks/" + draft.ID + "/start"
	if stale := postTaskCommand(startPath, "lifecycle-start-stale", `{"expectedVersion":1}`); stale.Code != http.StatusConflict {
		t.Fatalf("start with stale version status = %d, body %q; want 409", stale.Code, stale.Body.String())
	}
	started := postTaskCommand(startPath, "lifecycle-start-001", `{"expectedVersion":`+itoa(readyTask.Version)+`}`)
	if started.Code != http.StatusOK {
		t.Fatalf("start status = %d, body %q; want 200", started.Code, started.Body.String())
	}
	var startedTask tasks.Task
	if err := json.Unmarshal(started.Body.Bytes(), &startedTask); err != nil {
		t.Fatalf("decode started task: %v", err)
	}
	if startedTask.Status != "PROVISIONING" || startedTask.Version != readyTask.Version+1 {
		t.Fatalf("started task = %+v, want PROVISIONING at next version", startedTask)
	}
	if replay := postTaskCommand(startPath, "lifecycle-start-001", `{"expectedVersion":`+itoa(readyTask.Version)+`}`); replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("start replay status/header = %d/%q, want 200/true", replay.Code, replay.Header().Get("Idempotency-Replayed"))
	}
	if again := postTaskCommand(startPath, "lifecycle-start-002", `{"expectedVersion":`+itoa(startedTask.Version)+`}`); again.Code != http.StatusConflict {
		t.Fatalf("second start status = %d, body %q; want 409", again.Code, again.Body.String())
	}

	var attemptCount, workspaceCount int
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM execution_attempts WHERE task_id = $1 AND state = 'CREATED'),
			(SELECT COUNT(*) FROM workspaces WHERE task_id = $1 AND state = 'PROVISIONING')`,
		draft.ID).Scan(&attemptCount, &workspaceCount); err != nil {
		t.Fatalf("count execution rows: %v", err)
	}
	if attemptCount != 1 || workspaceCount != 1 {
		t.Fatalf("execution rows = %d attempts/%d workspaces, want exactly 1 each", attemptCount, workspaceCount)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
