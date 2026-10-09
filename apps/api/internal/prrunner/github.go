package prrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PullRequest is the subset of the GitHub PR object DiOffice records. Head
// and base SHAs come from the API response — never inferred from local git —
// so the persisted binding reflects what GitHub actually points at.
type PullRequest struct {
	NodeID   string
	Number   int
	URL      string
	State    string // OPEN | DRAFT | MERGED | CLOSED
	HeadSHA  string
	BaseSHA  string
	MergedAt *time.Time
}

// CreatePRInput describes the pull request to open.
type CreatePRInput struct {
	Title string
	Body  string
	Head  string // head branch name (same-repo branch)
	Base  string
}

// GitHubClient is the provider API boundary — faked in integration tests.
type GitHubClient interface {
	// FindOpenPR returns the open pull request whose head is headBranch, or
	// nil when none exists.
	FindOpenPR(ctx context.Context, owner, repo, headBranch string) (*PullRequest, error)
	CreatePR(ctx context.Context, owner, repo string, in CreatePRInput) (*PullRequest, error)
	// GetPR returns the pull request by number — the reconciler's read of the
	// authoritative head/base/state.
	GetPR(ctx context.Context, owner, repo string, number int) (*PullRequest, error)
	// MergePR merges the pull request only when the live head equals
	// in.HeadSHA (the GitHub merge API's sha precondition).
	MergePR(ctx context.Context, owner, repo string, number int, in MergePRInput) (*PullRequest, error)
}

// MergePRInput describes one Owner-requested merge.
type MergePRInput struct {
	HeadSHA string // required — GitHub refuses the merge when the live head differs
	Method  string // merge | squash | rebase; empty defaults to merge
}

// ErrorCode classifies a GitHub failure into an owner-safe reason code.
type ErrorCode string

const (
	ErrAuth        ErrorCode = "github_auth_failed"
	ErrRepoMissing ErrorCode = "github_repo_unavailable"
	ErrValidation  ErrorCode = "github_validation_failed"
	ErrTransient   ErrorCode = "github_transient"
	ErrUnexpected  ErrorCode = "github_unexpected_response"
)

// APIError is a classified GitHub API failure.
type APIError struct {
	Code    ErrorCode
	Message string
}

func (e *APIError) Error() string { return string(e.Code) + ": " + e.Message }

// HTTPGitHub calls the GitHub REST API. Token is a bearer credential
// (GITHUB_TOKEN); it is set only on Authorization headers and never logged.
type HTTPGitHub struct {
	APIURL string // defaults to https://api.github.com
	Token  string
	Client *http.Client
}

func (g *HTTPGitHub) http() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *HTTPGitHub) apiURL() string {
	base := strings.TrimRight(strings.TrimSpace(g.APIURL), "/")
	if base == "" {
		return "https://api.github.com"
	}
	return base
}

func (g *HTTPGitHub) do(ctx context.Context, method, path string,
	query url.Values, body any, out any) error {
	endpoint := g.apiURL() + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build GitHub request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.http().Do(req)
	if err != nil {
		return &APIError{Code: ErrTransient, Message: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &APIError{Code: ErrTransient, Message: "read response failed"}
	}
	if resp.StatusCode >= 400 {
		return classifyError(resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return &APIError{Code: ErrUnexpected, Message: "could not decode response"}
		}
	}
	return nil
}

func classifyError(status int, body []byte) error {
	message := http.StatusText(status)
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		message = parsed.Message
	}
	code := ErrTransient
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code = ErrAuth
	case status == http.StatusNotFound:
		code = ErrRepoMissing
	case status == http.StatusUnprocessableEntity:
		code = ErrValidation
	case status >= 500:
		code = ErrTransient
	}
	return &APIError{Code: code, Message: message}
}

type prResponse struct {
	NodeID   string     `json:"node_id"`
	Number   int        `json:"number"`
	HTMLURL  string     `json:"html_url"`
	State    string     `json:"state"`
	Draft    bool       `json:"draft"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

func (p prResponse) normalize() (*PullRequest, error) {
	headSHA := strings.ToLower(strings.TrimSpace(p.Head.SHA))
	baseSHA := strings.ToLower(strings.TrimSpace(p.Base.SHA))
	if p.Number <= 0 || p.NodeID == "" || len(headSHA) != 40 || len(baseSHA) != 40 {
		return nil, &APIError{Code: ErrUnexpected, Message: "pull request response is missing identifiers"}
	}
	url := strings.TrimSpace(p.HTMLURL)
	if !strings.HasPrefix(url, "https://") {
		return nil, &APIError{Code: ErrUnexpected, Message: "pull request URL is not an https URL"}
	}
	state := "CLOSED"
	switch {
	case p.Merged:
		state = "MERGED"
	case p.State == "open" && p.Draft:
		state = "DRAFT"
	case p.State == "open":
		state = "OPEN"
	}
	return &PullRequest{
		NodeID: p.NodeID, Number: p.Number, URL: url,
		State: state, HeadSHA: headSHA, BaseSHA: baseSHA, MergedAt: p.MergedAt,
	}, nil
}

// FindOpenPR lists open PRs filtered by head branch (`head=owner:branch`).
func (g *HTTPGitHub) FindOpenPR(ctx context.Context, owner, repo,
	headBranch string) (*PullRequest, error) {
	var list []prResponse
	query := url.Values{
		"head":     {owner + ":" + headBranch},
		"state":    {"open"},
		"per_page": {"5"},
	}
	if err := g.do(ctx, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls",
		query, nil, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list[0].normalize()
}

// CreatePR opens a pull request. A 422 "already exists" response is not
// mapped to success — callers re-query FindOpenPR to adopt the existing PR.
func (g *HTTPGitHub) CreatePR(ctx context.Context, owner, repo string,
	in CreatePRInput) (*PullRequest, error) {
	var created prResponse
	err := g.do(ctx, http.MethodPost,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls",
		nil, map[string]any{
			"title":                 in.Title,
			"body":                  in.Body,
			"head":                  in.Head,
			"base":                  in.Base,
			"maintainer_can_modify": true,
		}, &created)
	if err != nil {
		return nil, err
	}
	return created.normalize()
}

// GetPR fetches one pull request by number — the authoritative head/base/state
// read for reconciliation and merge preconditions.
func (g *HTTPGitHub) GetPR(ctx context.Context, owner, repo string,
	number int) (*PullRequest, error) {
	var pr prResponse
	if err := g.do(ctx, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+
			"/pulls/"+strconv.Itoa(number),
		nil, nil, &pr); err != nil {
		return nil, err
	}
	return pr.normalize()
}

// MergePR merges the pull request via `PUT /pulls/{n}/merge`. The GitHub sha
// precondition makes the merge atomic with the recorded head — a head change
// between review and merge is rejected by the API, never silently merged.
func (g *HTTPGitHub) MergePR(ctx context.Context, owner, repo string,
	number int, in MergePRInput) (*PullRequest, error) {
	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = "merge"
	}
	body := map[string]any{"merge_method": method}
	if in.HeadSHA != "" {
		body["sha"] = in.HeadSHA
	}
	var merged struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if err := g.do(ctx, http.MethodPut,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+
			"/pulls/"+strconv.Itoa(number)+"/merge",
		nil, body, &merged); err != nil {
		return nil, err
	}
	if !merged.Merged {
		return nil, &APIError{Code: ErrValidation,
			Message: "merge not completed: " + merged.Message}
	}
	// The merge response carries only the merge commit SHA; return the
	// recorded identifiers with the terminal state so callers persist a
	// coherent row without a second API read.
	return &PullRequest{Number: number, State: "MERGED", HeadSHA: in.HeadSHA}, nil
}

// AlreadyExists reports whether err is the GitHub "a pull request already
// exists" validation failure.
func AlreadyExists(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == ErrValidation &&
		strings.Contains(strings.ToLower(apiErr.Message), "pull request already exists")
}
