package folderbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testProjectID = "01234567-89ab-4cde-8fab-0123456789ab"

func TestClientUsesAuthenticatedLocalGatewayForStatusAndOpen(t *testing.T) {
	token := "test-only-" + strings.Repeat("x", 48)
	var statusCalls, openCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("Authorization header missing or incorrect")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/projects/" + testProjectID + "/folder":
			statusCalls++
			_, _ = io.WriteString(w, `{"available":true}`)
		case "POST /internal/projects/" + testProjectID + "/open-folder":
			openCalls++
			var body struct {
				Editor Editor `json:"editor"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode open request: %v", err)
			}
			if body.Editor != EditorVSCode {
				t.Errorf("editor = %q, want vscode", body.Editor)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"opening","editor":"vscode"}`)
		default:
			t.Errorf("unexpected gateway request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	available, err := client.Available(context.Background(), testProjectID)
	if err != nil || !available {
		t.Fatalf("Available() = %t, error %v; want true", available, err)
	}
	if err := client.Open(context.Background(), testProjectID, EditorVSCode); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if statusCalls != 1 || openCalls != 1 {
		t.Fatalf("gateway calls = status %d/open %d; want one each", statusCalls, openCalls)
	}
}

func TestClientFailsClosedWithoutCredentialsAndRejectsRemoteGateways(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:4100", "")
	if err != nil {
		t.Fatalf("NewClient() without token error = %v", err)
	}
	available, err := client.Available(context.Background(), testProjectID)
	if err != nil || available {
		t.Fatalf("unconfigured Available() = %t, error %v; want false without error", available, err)
	}
	if err := client.Open(context.Background(), testProjectID, EditorExplorer); !errors.Is(err, ErrBridgeNotConfigured) {
		t.Fatalf("unconfigured Open() error = %v; want ErrBridgeNotConfigured", err)
	}

	if _, err := NewClient("https://example.com", "test-only-"+strings.Repeat("x", 48)); err == nil {
		t.Fatal("NewClient() accepted a remote gateway URL")
	}
}

func TestClientMapsUnconfiguredFolderAndRejectsInvalidInputs(t *testing.T) {
	token := "test-only-" + strings.Repeat("x", 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"folder_not_configured"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Open(context.Background(), testProjectID, EditorExplorer); !errors.Is(err, ErrFolderNotConfigured) {
		t.Fatalf("Open() error = %v; want ErrFolderNotConfigured", err)
	}
	if _, err := client.Available(context.Background(), "../../etc"); !errors.Is(err, ErrInvalidProjectID) {
		t.Fatalf("Available() with invalid project ID error = %v; want ErrInvalidProjectID", err)
	}
	if err := client.Open(context.Background(), testProjectID, Editor("shell")); !errors.Is(err, ErrInvalidEditor) {
		t.Fatalf("Open() with invalid editor error = %v; want ErrInvalidEditor", err)
	}
}
