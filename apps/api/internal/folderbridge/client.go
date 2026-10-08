package folderbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxGatewayResponseBytes = 4096

var (
	ErrBridgeNotConfigured = errors.New("local folder bridge is not configured")
	ErrBridgeUnavailable   = errors.New("local folder bridge is unavailable")
	ErrFolderNotConfigured = errors.New("project folder is not configured")
	ErrFolderOpenFailed    = errors.New("project folder could not be opened")
	ErrInvalidProjectID    = errors.New("invalid project ID")
	ErrInvalidEditor       = errors.New("invalid folder editor")
)

type Editor string

const (
	EditorExplorer Editor = "explorer"
	EditorVSCode   Editor = "vscode"
)

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

func NewClient(rawURL, token string) (*Client, error) {
	client := &Client{
		token: strings.TrimSpace(token),
		httpClient: &http.Client{
			Timeout: 2 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	if strings.TrimSpace(rawURL) == "" {
		return client, nil
	}
	if rawURL != strings.TrimSpace(rawURL) {
		return nil, errors.New("AGENT_GATEWAY_URL must be a loopback HTTP origin")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("AGENT_GATEWAY_URL must be a loopback HTTP origin")
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || ip.To4() == nil || !ip.IsLoopback()) {
		return nil, errors.New("AGENT_GATEWAY_URL must use a loopback host")
	}
	if host == "localhost" {
		if port := parsed.Port(); port != "" {
			parsed.Host = net.JoinHostPort("127.0.0.1", port)
		} else {
			parsed.Host = "127.0.0.1"
		}
	}
	client.baseURL = parsed
	return client, nil
}

func (c *Client) Available(ctx context.Context, projectID string) (bool, error) {
	if c == nil || c.baseURL == nil || len(c.token) < 32 {
		return false, nil
	}
	response, err := c.request(ctx, http.MethodGet, projectID, "/folder", nil)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, ErrBridgeUnavailable
	}
	var result struct {
		Available bool `json:"available"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxGatewayResponseBytes)).Decode(&result); err != nil {
		return false, ErrBridgeUnavailable
	}
	return result.Available, nil
}

func (c *Client) Open(ctx context.Context, projectID string, editor Editor) error {
	if editor != EditorExplorer && editor != EditorVSCode {
		return ErrInvalidEditor
	}
	if c == nil || c.baseURL == nil || len(c.token) < 32 {
		return ErrBridgeNotConfigured
	}
	response, err := c.request(ctx, http.MethodPost, projectID, "/open-folder", struct {
		Editor Editor `json:"editor"`
	}{Editor: editor})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusAccepted {
		return nil
	}
	var result struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, maxGatewayResponseBytes)).Decode(&result)
	switch result.Error {
	case "folder_not_configured":
		return ErrFolderNotConfigured
	case "folder_open_failed":
		return ErrFolderOpenFailed
	case "bridge_not_configured", "unauthorized":
		return ErrBridgeNotConfigured
	default:
		return ErrBridgeUnavailable
	}
}

func (c *Client) request(ctx context.Context, method, projectID, suffix string, body any) (*http.Response, error) {
	if !validProjectID(projectID) {
		return nil, ErrInvalidProjectID
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, ErrBridgeUnavailable
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "/internal/projects/" + projectID + suffix})
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, ErrBridgeUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, ErrBridgeUnavailable
	}
	return response, nil
}

func validProjectID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
