package sessionrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenCodeConfig locates the local OpenCode server. ServerURL must be a
// loopback http(s) origin with no credentials, path, query, or fragment —
// the same restriction the agent gateway readiness probe enforces.
type OpenCodeConfig struct {
	ServerURL string
	Client    *http.Client
}

// OpenCodeRuntime talks to a local `opencode serve` instance. Every request
// is directory-scoped so sessions bind to the task worktree, never to the
// server's own working directory.
type OpenCodeRuntime struct {
	base   *url.URL
	client *http.Client
	// streamClient has no total request timeout: SSE streams are long-lived
	// and bounded by context cancellation instead.
	streamClient *http.Client
}

// NewOpenCodeRuntime validates the configured server URL and returns a
// runtime adapter. It performs no network call; readiness is the gateway's
// /readyz responsibility and call failures map to runtime_unreachable.
func NewOpenCodeRuntime(cfg OpenCodeConfig) (*OpenCodeRuntime, error) {
	raw := strings.TrimSpace(cfg.ServerURL)
	if raw == "" || raw != cfg.ServerURL {
		return nil, errors.New("OPENCODE_SERVER_URL is required")
	}
	server, err := url.Parse(raw)
	if err != nil || (server.Scheme != "http" && server.Scheme != "https") {
		return nil, errors.New("OPENCODE_SERVER_URL must be an http(s) URL")
	}
	if server.User != nil || (server.Path != "" && server.Path != "/") ||
		server.RawQuery != "" || server.Fragment != "" {
		return nil, errors.New("OPENCODE_SERVER_URL must be a bare origin")
	}
	host := server.Hostname()
	loopback := host == "localhost" ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if !loopback {
		return nil, errors.New("OPENCODE_SERVER_URL must resolve to loopback")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if client.Timeout <= 0 || client.Timeout > 2*time.Minute {
		client.Timeout = 60 * time.Second
	}
	return &OpenCodeRuntime{base: server, client: client, streamClient: &http.Client{}}, nil
}

// StreamEvents opens the provider's GET /event SSE stream scoped to the
// workspace directory. The caller owns cancellation via ctx and must close
// the returned body.
func (r *OpenCodeRuntime) StreamEvents(ctx context.Context, directory string) (io.ReadCloser, error) {
	target := r.base.ResolveReference(&url.URL{Path: "/event"})
	query := target.Query()
	query.Set("directory", directory)
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, &RuntimeError{Kind: "invalid_response", Detail: "could not build stream request"}
	}
	request.Header.Set("accept", "text/event-stream")
	request.Header.Set("x-opencode-directory", directory)
	response, err := r.streamClient.Do(request)
	if err != nil {
		return nil, &RuntimeError{Kind: "unreachable", Detail: "stream request failed"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &RuntimeError{
			Kind:   "unreachable",
			Detail: fmt.Sprintf("event stream returned status %d", response.StatusCode),
		}
	}
	return response.Body, nil
}

func (r *OpenCodeRuntime) CreateSession(ctx context.Context, directory, title string) (string, error) {
	body, err := json.Marshal(map[string]any{"title": title})
	if err != nil {
		return "", &RuntimeError{Kind: "invalid_response", Detail: "could not encode session request"}
	}
	payload, err := r.call(ctx, http.MethodPost, "/session", directory, body)
	if err != nil {
		return "", err
	}
	var session struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(payload, &session); err != nil {
		return "", &RuntimeError{Kind: "invalid_response", Detail: "session response is not JSON"}
	}
	id := session.ID
	if id == "" {
		id = session.SessionID
	}
	if id == "" {
		return "", &RuntimeError{Kind: "invalid_response", Detail: "session response has no id"}
	}
	return id, nil
}

func (r *OpenCodeRuntime) SendPrompt(ctx context.Context, sessionID, prompt string) error {
	body, err := json.Marshal(map[string]any{
		"parts": []map[string]string{{"type": "text", "text": prompt}},
	})
	if err != nil {
		return &RuntimeError{Kind: "invalid_response", Detail: "could not encode prompt request"}
	}
	if _, err := r.call(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/prompt_async", "", body); err != nil {
		return err
	}
	return nil
}

func (r *OpenCodeRuntime) Abort(ctx context.Context, sessionID string) error {
	_, err := r.call(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/abort", "", []byte("{}"))
	var re *RuntimeError
	if errors.As(err, &re) && re.Kind == "rejected" {
		// Session already gone/aborted or the endpoint is unavailable;
		// either way there is nothing left to cancel.
		return nil
	}
	return err
}

// call performs one bounded JSON request. The directory scope is sent both
// as a query parameter and as the x-opencode-directory header so the call
// works across server versions; it never forwards provider payloads.
func (r *OpenCodeRuntime) call(ctx context.Context, method, path, directory string, body []byte) ([]byte, error) {
	target := r.base.ResolveReference(&url.URL{Path: path})
	if directory != "" {
		query := target.Query()
		query.Set("directory", directory)
		target.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &RuntimeError{Kind: "invalid_response", Detail: "could not build request"}
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "application/json")
	if directory != "" {
		request.Header.Set("x-opencode-directory", directory)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, &RuntimeError{Kind: "unreachable", Detail: "request failed"}
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, &RuntimeError{Kind: "unreachable", Detail: "could not read response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		kind := "rejected"
		if response.StatusCode >= 500 {
			kind = "unreachable"
		}
		return nil, &RuntimeError{
			Kind:   kind,
			Detail: fmt.Sprintf("runtime returned status %d", response.StatusCode),
		}
	}
	return payload, nil
}
