package sessionrunner

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rendr17/dioffice/apps/api/internal/providers"
)

// Resolver is the production RuntimeResolver: it reads the org's provider
// configuration for the attempt's recorded runtime key and builds the
// matching adapter. OpenCode keeps a development fallback — when no config
// row exists, OPENCODE_SERVER_URL is used so existing local setups keep
// working; an explicitly disabled config wins over the fallback.
type Resolver struct {
	DB                 *sql.DB
	OpenCodeEnvURL     string
	OpenCodeHTTPClient *http.Client
}

func (r Resolver) Resolve(ctx context.Context, organizationID, providerKey string) (Runtime, error) {
	svc := providers.NewService(r.DB)
	endpoint, err := svc.ResolveEndpoint(ctx, organizationID, providerKey)
	switch {
	case err == nil:
		// Configured, enabled, and the adapter ships with this binary.
	case errors.Is(err, providers.ErrProviderNotConfigured) && providerKey == "opencode":
		if strings.TrimSpace(r.OpenCodeEnvURL) == "" {
			return nil, providers.ErrProviderNotConfigured
		}
		endpoint.BaseURL = strings.TrimSpace(r.OpenCodeEnvURL)
	default:
		return nil, err
	}

	switch providerKey {
	case "opencode":
		client := r.OpenCodeHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 2 * time.Minute}
		}
		return NewOpenCodeRuntime(OpenCodeConfig{ServerURL: endpoint.BaseURL, Client: client})
	default:
		// Registered catalog providers reach ResolveEndpoint already; any
		// key that slips past the DB constraint fails closed here.
		return nil, providers.ErrProviderNotImplemented
	}
}
