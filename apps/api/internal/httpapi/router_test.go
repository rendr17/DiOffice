package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/auth"
)

func TestHealthEndpointReturnsServiceIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()

	NewRouter(Dependencies{}).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "ok" || body.Service != "dioffice-api" {
		t.Fatalf("body = %+v, want status=ok service=dioffice-api", body)
	}
}

func TestCorsPreflightAllowsOnlyConfiguredWebOrigin(t *testing.T) {
	router := NewRouter(Dependencies{WebOrigin: "https://web.example.invalid"})
	allowed := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	allowed.Header.Set("Origin", "https://web.example.invalid")
	allowed.Header.Set("Access-Control-Request-Method", http.MethodPost)
	allowed.Header.Set("Access-Control-Request-Headers", "content-type,x-csrf-token")
	allowedResponse := httptest.NewRecorder()
	router.ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusNoContent || allowedResponse.Header().Get("Access-Control-Allow-Origin") != "https://web.example.invalid" || allowedResponse.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("allowed preflight status/headers = %d/%v", allowedResponse.Code, allowedResponse.Header())
	}

	denied := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	denied.Header.Set("Origin", "https://attacker.example.invalid")
	deniedResponse := httptest.NewRecorder()
	router.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || deniedResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted preflight status/allow-origin = %d/%q", deniedResponse.Code, deniedResponse.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestSessionCookiesUseSecureHttpOnlyAndSameSitePolicy(t *testing.T) {
	response := httptest.NewRecorder()
	setSessionCookies(response, auth.LoginSession{
		SessionToken: "test-session-token", CSRFToken: "test-csrf-token",
		ExpiresAt: time.Now().Add(time.Hour),
	}, true)
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookie count = %d, want 2", len(cookies))
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %q has insecure attributes: %+v", cookie.Name, cookie)
		}
		if cookie.Name == sessionCookieName && !cookie.HttpOnly {
			t.Error("session cookie must be HttpOnly")
		}
		if cookie.Name == csrfCookieName && cookie.HttpOnly {
			t.Error("CSRF cookie must be readable by the browser client")
		}
	}
}
