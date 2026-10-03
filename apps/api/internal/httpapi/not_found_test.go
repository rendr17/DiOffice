package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnknownRouteReturnsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/not-a-route", nil)
	res := httptest.NewRecorder()

	NewRouter(Dependencies{}).ServeHTTP(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
}
