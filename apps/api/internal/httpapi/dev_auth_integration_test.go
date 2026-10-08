package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/directory"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

func TestDevelopmentSessionIsOptInLoopbackOnlyAndSeedsOneOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newHTTPAPITestDatabase(t, ctx)
	authService := auth.NewService(db)
	dependencies := Dependencies{
		DB: db, Auth: authService, Directory: directory.NewService(db), Tasks: tasks.NewService(db),
		SecureCookies: false, WebOrigin: "http://127.0.0.1:5174",
	}

	requestDevSession := func(router http.Handler, origin, remoteAddress string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/dev-session", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		request.RemoteAddr = remoteAddress
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	if response := requestDevSession(NewRouter(dependencies), dependencies.WebOrigin, "127.0.0.1:1234"); response.Code != http.StatusNotFound {
		t.Fatalf("development session without opt-in status = %d, want %d", response.Code, http.StatusNotFound)
	}

	dependencies.DevAuthBypass = true
	router := NewRouter(dependencies)
	if response := requestDevSession(router, "https://attacker.example", "127.0.0.1:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("development session from untrusted origin status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if response := requestDevSession(router, dependencies.WebOrigin, "192.0.2.10:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("development session from non-loopback client status = %d, want %d", response.Code, http.StatusForbidden)
	}

	response := requestDevSession(router, dependencies.WebOrigin, "127.0.0.1:1234")
	if response.Code != http.StatusOK {
		t.Fatalf("development session status = %d, body %q; want %d", response.Code, response.Body.String(), http.StatusOK)
	}
	var sessionBody struct {
		User safeUser `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &sessionBody); err != nil {
		t.Fatalf("decode development session response: %v", err)
	}
	if sessionBody.User.ID == "" || sessionBody.User.OrganizationID == "" || sessionBody.User.Role != "OWNER" {
		t.Fatalf("development user = %+v, want persisted local Owner", sessionBody.User)
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil || !sessionCookie.HttpOnly || csrfCookie.HttpOnly {
		t.Fatalf("development session cookies are missing or unsafe: session=%+v csrf=%+v", sessionCookie, csrfCookie)
	}

	projectsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	projectsRequest.AddCookie(sessionCookie)
	projectsResponse := httptest.NewRecorder()
	router.ServeHTTP(projectsResponse, projectsRequest)
	if projectsResponse.Code != http.StatusOK {
		t.Fatalf("authenticated project directory status = %d, body %q; want %d", projectsResponse.Code, projectsResponse.Body.String(), http.StatusOK)
	}
	var projectsBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(projectsResponse.Body.Bytes(), &projectsBody); err != nil {
		t.Fatalf("decode development projects: %v", err)
	}
	if len(projectsBody.Items) != 1 || projectsBody.Items[0].ID == "" {
		t.Fatalf("development projects = %+v, want one seeded local project", projectsBody.Items)
	}
	employeesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/employees", nil)
	employeesRequest.AddCookie(sessionCookie)
	employeesResponse := httptest.NewRecorder()
	router.ServeHTTP(employeesResponse, employeesRequest)
	if employeesResponse.Code != http.StatusOK {
		t.Fatalf("authenticated employee directory status = %d, body %q; want %d", employeesResponse.Code, employeesResponse.Body.String(), http.StatusOK)
	}
	var employeesBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(employeesResponse.Body.Bytes(), &employeesBody); err != nil {
		t.Fatalf("decode development employees: %v", err)
	}
	if len(employeesBody.Items) != 1 || employeesBody.Items[0].ID == "" {
		t.Fatalf("development employees = %+v, want one seeded Deni", employeesBody.Items)
	}
	taskBody, err := json.Marshal(map[string]any{
		"assigneeEmployeeId": employeesBody.Items[0].ID,
		"title":              "Local dev bypass task", "description": "Verify the local Owner can create a draft.",
		"acceptanceCriteria": []string{"Draft is persisted"}, "requiredChecks": []string{},
		"taskType": "feature", "priority": "NORMAL",
	})
	if err != nil {
		t.Fatalf("encode development task: %v", err)
	}
	taskRequest := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+projectsBody.Items[0].ID+"/tasks", bytes.NewReader(taskBody))
	taskRequest.Header.Set("Content-Type", "application/json")
	taskRequest.Header.Set("Idempotency-Key", "dev-bypass-task-001")
	taskRequest.Header.Set("X-CSRF-Token", csrfCookie.Value)
	taskRequest.AddCookie(sessionCookie)
	taskRequest.AddCookie(csrfCookie)
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("development task create status = %d, body %q; want %d", taskResponse.Code, taskResponse.Body.String(), http.StatusCreated)
	}

	repeated := requestDevSession(router, dependencies.WebOrigin, "127.0.0.1:1234")
	if repeated.Code != http.StatusOK {
		t.Fatalf("repeated development session status = %d, want %d", repeated.Code, http.StatusOK)
	}
	var repeatedBody struct {
		User safeUser `json:"user"`
	}
	if err := json.Unmarshal(repeated.Body.Bytes(), &repeatedBody); err != nil {
		t.Fatalf("decode repeated development session response: %v", err)
	}
	if repeatedBody.User.ID != sessionBody.User.ID || repeatedBody.User.OrganizationID != sessionBody.User.OrganizationID {
		t.Fatalf("repeated development session user = %+v, want the same seeded Owner %+v", repeatedBody.User, sessionBody.User)
	}
}
