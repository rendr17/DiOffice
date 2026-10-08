package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/directory"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

func TestAuthenticatedOwnerCanCreateAndReadPrivateReferenceImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newHTTPAPITestDatabase(t, ctx)
	authService := auth.NewService(db)
	owner, err := authService.BootstrapOwner(ctx, auth.BootstrapOwnerInput{
		OrganizationName: "Reference image integration org",
		ProjectName:      "Reference image task",
		Email:            "image-owner@example.invalid",
		Password:         "test-only-password-not-a-credential",
	})
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	var otherProjectID string
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (organization_id, name) VALUES ($1, 'Other project') RETURNING id::text`, owner.OrganizationID).Scan(&otherProjectID); err != nil {
		t.Fatalf("create same-organization project: %v", err)
	}
	store := newMemoryReferenceImageStore()
	router := NewRouter(Dependencies{
		DB: db, Auth: authService, Directory: directory.NewService(db), Tasks: tasks.NewService(db),
		ReferenceImages: store, SecureCookies: false,
	})

	loginBody, _ := json.Marshal(map[string]string{
		"organizationId": owner.OrganizationID,
		"email":          "image-owner@example.invalid",
		"password":       "test-only-password-not-a-credential",
	})
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d, body %q", loginResponse.Code, loginResponse.Body.String())
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
		t.Fatal("login response did not set both session and CSRF cookies")
	}

	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	create := func() *httptest.ResponseRecorder {
		request, err := newMultipartTaskRequest(t, imageBytes.Bytes(), "reference.jpg", owner.EmployeeID)
		if err != nil {
			t.Fatalf("build multipart task request: %v", err)
		}
		request.URL.Path = "/api/v1/projects/" + owner.ProjectID + "/tasks"
		request.Header.Set("Idempotency-Key", "reference-image-task-create-1")
		request.AddCookie(sessionCookie)
		request.AddCookie(csrfCookie)
		request.Header.Set("X-CSRF-Token", csrfCookie.Value)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	created := create()
	if created.Code != http.StatusCreated {
		t.Fatalf("create task status = %d, body %q", created.Code, created.Body.String())
	}
	var createdTask tasks.Task
	if err := json.Unmarshal(created.Body.Bytes(), &createdTask); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	if string(createdTask.AcceptanceCriteria) != "[]" || string(createdTask.RequiredChecks) != "[]" {
		t.Fatalf("task arrays = %s/%s, want []/[] for null optional fields", createdTask.AcceptanceCriteria, createdTask.RequiredChecks)
	}
	if len(createdTask.ReferenceImages) != 1 {
		t.Fatalf("task reference images = %+v, want one", createdTask.ReferenceImages)
	}
	imageMetadata := createdTask.ReferenceImages[0]
	if imageMetadata.FileName != "reference.png" || imageMetadata.ContentType != "image/png" || imageMetadata.SizeBytes != int64(imageBytes.Len()) {
		t.Fatalf("task image metadata = %+v, want validated PNG metadata", imageMetadata)
	}
	if strings.Contains(created.Body.String(), "task-reference-images/") || strings.Contains(created.Body.String(), "SHA256") {
		t.Fatal("task response exposed private object location or checksum")
	}
	if store.count() != 1 {
		t.Fatalf("private object count after task creation = %d, want 1", store.count())
	}

	imagePath := "/api/v1/projects/" + owner.ProjectID + "/tasks/" + createdTask.ID + "/reference-images/" + imageMetadata.ID
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, imagePath, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated image read status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	readImage := httptest.NewRequest(http.MethodGet, imagePath, nil)
	readImage.AddCookie(sessionCookie)
	imageResponse := httptest.NewRecorder()
	router.ServeHTTP(imageResponse, readImage)
	if imageResponse.Code != http.StatusOK || !bytes.Equal(imageResponse.Body.Bytes(), imageBytes.Bytes()) {
		t.Fatalf("image read status/body = %d/%v, want authorized original PNG", imageResponse.Code, bytes.Equal(imageResponse.Body.Bytes(), imageBytes.Bytes()))
	}
	if imageResponse.Header().Get("Content-Type") != "image/png" || imageResponse.Header().Get("X-Content-Type-Options") != "nosniff" || imageResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private image security headers = %v", imageResponse.Header())
	}

	wrongProject := httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/"+otherProjectID+"/tasks/"+createdTask.ID+"/reference-images/"+imageMetadata.ID, nil)
	wrongProject.AddCookie(sessionCookie)
	wrongProjectResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongProjectResponse, wrongProject)
	if wrongProjectResponse.Code != http.StatusNotFound {
		t.Fatalf("image read through another project status = %d, want %d", wrongProjectResponse.Code, http.StatusNotFound)
	}

	replay := create()
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("image task replay status/header = %d/%q, want 201/true", replay.Code, replay.Header().Get("Idempotency-Replayed"))
	}
	if store.count() != 1 {
		t.Fatalf("private object count after idempotent replay = %d, want 1 (new upload should be cleaned)", store.count())
	}
}
