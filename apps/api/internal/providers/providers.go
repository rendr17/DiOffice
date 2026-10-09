// Package providers owns the runtime-provider catalog and each
// organization's provider configuration. The catalog is code, not data:
// only adapters this binary ships can ever produce a live session, so a
// configured-but-unimplemented provider fails closed with
// ErrProviderNotImplemented instead of pretending to run.
//
// Credentials are never stored or returned — `credential_env` is merely the
// NAME of an environment variable expected on the runner host.
package providers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrInvalidInput           = errors.New("invalid provider input")
	ErrProviderUnknown        = errors.New("provider is not in the catalog")
	ErrProviderNotConfigured  = errors.New("provider has no saved configuration")
	ErrProviderDisabled       = errors.New("provider configuration is disabled")
	ErrProviderNotImplemented = errors.New("provider adapter is registered but not implemented")
)

// CatalogEntry describes a runtime provider DiOffice knows about.
type CatalogEntry struct {
	Key             string `json:"key"`
	DisplayName     string `json:"displayName"`
	Kind            string `json:"kind"` // local_server | cli
	AdapterStatus   string `json:"adapterStatus"`
	NeedsBaseURL    bool   `json:"needsBaseUrl"`
	NeedsCredential bool   `json:"needsCredential"`
}

// Catalog is the canonical registry — order is stable for UI display.
var Catalog = []CatalogEntry{
	{Key: "opencode", DisplayName: "OpenCode", Kind: "local_server", AdapterStatus: "implemented", NeedsBaseURL: true},
	{Key: "codex", DisplayName: "Codex", Kind: "cli", AdapterStatus: "registered", NeedsCredential: true},
	{Key: "claude", DisplayName: "Claude", Kind: "cli", AdapterStatus: "registered", NeedsCredential: true},
}

func catalogEntry(key string) (CatalogEntry, bool) {
	for _, entry := range Catalog {
		if entry.Key == key {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

// View merges the catalog entry with the org's saved configuration for the
// API/UI. Secret material is never present — CredentialEnv is a name only.
type View struct {
	CatalogEntry
	Configured    bool   `json:"configured"`
	Enabled       bool   `json:"enabled"`
	Label         string `json:"label"`
	BaseURL       string `json:"baseUrl,omitempty"`
	CredentialEnv string `json:"credentialEnv,omitempty"`
}

// Endpoint is the resolved, non-secret connection material a runner needs.
type Endpoint struct {
	BaseURL       string
	CredentialEnv string
}

var credentialEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// ValidateBaseURL enforces the same rule as the OpenCode readiness probe:
// http is allowed only on a loopback literal; other hosts must be https.
// No credentials, path, query, or fragment are permitted.
func ValidateBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed != raw {
		return fmt.Errorf("%w: baseUrl is required for this provider", ErrInvalidInput)
	}
	server, err := url.Parse(trimmed)
	if err != nil || (server.Scheme != "http" && server.Scheme != "https") {
		return fmt.Errorf("%w: baseUrl must be an http(s) origin", ErrInvalidInput)
	}
	if server.User != nil || server.Path != "" && server.Path != "/" ||
		server.RawQuery != "" || server.Fragment != "" {
		return fmt.Errorf("%w: baseUrl must be a bare origin without path, query, or credentials", ErrInvalidInput)
	}
	host := server.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasPrefix(host, "127.")
	if server.Scheme == "http" && !loopback {
		return fmt.Errorf("%w: baseUrl over http is only allowed on loopback", ErrInvalidInput)
	}
	if host == "" {
		return fmt.Errorf("%w: baseUrl must include a host", ErrInvalidInput)
	}
	return nil
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// ListForOrg returns every catalog provider merged with the org's saved
// configuration, in stable catalog order.
func (s *Service) ListForOrg(ctx context.Context, organizationID string) ([]View, error) {
	if !isUUID(organizationID) {
		return nil, fmt.Errorf("%w: organizationId must be a UUID", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return nil, errors.New("provider service database is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_key, label, COALESCE(base_url, ''), COALESCE(credential_env, ''), enabled
		FROM provider_configs WHERE organization_id = $1`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list provider configs: %w", err)
	}
	defer rows.Close()
	saved := map[string]View{}
	for rows.Next() {
		var view View
		if err := rows.Scan(&view.Key, &view.Label, &view.BaseURL, &view.CredentialEnv, &view.Enabled); err != nil {
			return nil, fmt.Errorf("scan provider config: %w", err)
		}
		view.Configured = true
		saved[view.Key] = view
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	views := make([]View, 0, len(Catalog))
	for _, entry := range Catalog {
		view := saved[entry.Key]
		view.CatalogEntry = entry
		if view.Label == "" {
			view.Label = entry.DisplayName
		}
		views = append(views, view)
	}
	return views, nil
}

type ConfigureInput struct {
	OrganizationID string
	ProviderKey    string
	ActorUserID    string
	Label          string
	BaseURL        string
	CredentialEnv  string
	Enabled        bool
}

// Configure upserts the org's configuration for one catalog provider and
// writes an org-scoped audit record (provider config is org-level, so
// audit_records.project_id stays NULL).
func (s *Service) Configure(ctx context.Context, input ConfigureInput) (View, error) {
	entry, ok := catalogEntry(input.ProviderKey)
	if !ok {
		return View{}, ErrProviderUnknown
	}
	if !isUUID(input.OrganizationID) || !isUUID(input.ActorUserID) {
		return View{}, fmt.Errorf("%w: organizationId and actorUserId must be UUIDs", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return View{}, errors.New("provider service database is not configured")
	}
	label := strings.TrimSpace(input.Label)
	if label == "" {
		label = entry.DisplayName
	}
	if len(label) > 80 {
		return View{}, fmt.Errorf("%w: label must be at most 80 characters", ErrInvalidInput)
	}
	baseURL := strings.TrimSpace(input.BaseURL)
	if entry.NeedsBaseURL {
		if err := ValidateBaseURL(baseURL); err != nil {
			return View{}, err
		}
	} else if baseURL != "" {
		if err := ValidateBaseURL(baseURL); err != nil {
			return View{}, err
		}
	}
	credentialEnv := strings.TrimSpace(input.CredentialEnv)
	if credentialEnv != "" && !credentialEnvPattern.MatchString(credentialEnv) {
		return View{}, fmt.Errorf("%w: credentialEnv must be an environment variable name like OPENAI_API_KEY", ErrInvalidInput)
	}
	if entry.NeedsCredential && input.Enabled && credentialEnv == "" {
		return View{}, fmt.Errorf("%w: %s requires credentialEnv before it can be enabled", ErrInvalidInput, entry.DisplayName)
	}
	// CLI providers carry no endpoint; ignore stray base URLs.
	if entry.Kind == "cli" {
		baseURL = ""
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return View{}, fmt.Errorf("begin provider configure transaction: %w", err)
	}
	defer tx.Rollback()

	var view View
	err = tx.QueryRowContext(ctx, `
		INSERT INTO provider_configs (organization_id, provider_key, label, base_url, credential_env, enabled)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6)
		ON CONFLICT (organization_id, provider_key) DO UPDATE SET
			label = EXCLUDED.label, base_url = EXCLUDED.base_url,
			credential_env = EXCLUDED.credential_env, enabled = EXCLUDED.enabled,
			updated_at = now()
		WHERE provider_configs.enabled IS DISTINCT FROM EXCLUDED.enabled
			OR provider_configs.label IS DISTINCT FROM EXCLUDED.label
			OR provider_configs.base_url IS DISTINCT FROM EXCLUDED.base_url
			OR provider_configs.credential_env IS DISTINCT FROM EXCLUDED.credential_env
		RETURNING provider_key, label, COALESCE(base_url, ''), COALESCE(credential_env, ''), enabled`,
		input.OrganizationID, input.ProviderKey, label, baseURL, credentialEnv, input.Enabled).
		Scan(&view.Key, &view.Label, &view.BaseURL, &view.CredentialEnv, &view.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		// UPDATE was skipped because nothing changed — read the stored row.
		err = tx.QueryRowContext(ctx, `
			SELECT provider_key, label, COALESCE(base_url, ''), COALESCE(credential_env, ''), enabled
			FROM provider_configs WHERE organization_id = $1 AND provider_key = $2`,
			input.OrganizationID, input.ProviderKey).
			Scan(&view.Key, &view.Label, &view.BaseURL, &view.CredentialEnv, &view.Enabled)
	}
	if err != nil {
		return View{}, fmt.Errorf("upsert provider config: %w", err)
	}
	view.CatalogEntry = entry
	view.Configured = true

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_records (organization_id, project_id, actor_user_id, action, target_type, target_id, outcome)
		VALUES ($1, NULL, $2, 'provider.configure', 'provider_config',
			(SELECT id FROM provider_configs WHERE organization_id = $1 AND provider_key = $3), 'success')`,
		input.OrganizationID, input.ActorUserID, input.ProviderKey); err != nil {
		return View{}, fmt.Errorf("insert provider audit record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return View{}, fmt.Errorf("commit provider configure: %w", err)
	}
	return view, nil
}

// ResolveEndpoint returns the non-secret connection material a runner needs
// for an org's provider, or a typed error (unknown/not configured/disabled/
// registered-but-unimplemented). Resolving is deliberately not the same as
// reaching the provider — liveness stays with the runtime adapter.
func (s *Service) ResolveEndpoint(ctx context.Context, organizationID, providerKey string) (Endpoint, error) {
	entry, ok := catalogEntry(providerKey)
	if !ok {
		return Endpoint{}, ErrProviderUnknown
	}
	if entry.AdapterStatus != "implemented" {
		return Endpoint{}, ErrProviderNotImplemented
	}
	if s == nil || s.db == nil {
		return Endpoint{}, errors.New("provider service database is not configured")
	}
	var endpoint Endpoint
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(base_url, ''), COALESCE(credential_env, ''), enabled
		FROM provider_configs WHERE organization_id = $1 AND provider_key = $2`,
		organizationID, providerKey).Scan(&endpoint.BaseURL, &endpoint.CredentialEnv, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Endpoint{}, ErrProviderNotConfigured
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("resolve provider endpoint: %w", err)
	}
	if !enabled {
		return Endpoint{}, ErrProviderDisabled
	}
	return endpoint, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(value string) bool {
	return uuidPattern.MatchString(value)
}
