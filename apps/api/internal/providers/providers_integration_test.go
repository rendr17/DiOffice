package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
)

func newProvidersTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	baseURL := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	adminDB, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	if err := adminDB.PingContext(ctx); err != nil {
		adminDB.Close()
		t.Fatalf("connect to PostgreSQL test database: %v", err)
	}
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		adminDB.Close()
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schemaName := "dioffice_providers_test_" + hex.EncodeToString(randomBytes)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		adminDB.Close()
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE"); err != nil {
			t.Errorf("drop isolated test schema: %v", err)
		}
		if err := adminDB.Close(); err != nil {
			t.Errorf("close PostgreSQL test database: %v", err)
		}
	})
	testURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL test database URL: %v", err)
	}
	query := testURL.Query()
	query.Set("search_path", schemaName)
	testURL.RawQuery = query.Encode()
	db, err := sql.Open("pgx", testURL.String())
	if err != nil {
		t.Fatalf("open isolated test schema: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated test schema: %v", err)
	}
	_, sourceFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}

func providerFixtures(t *testing.T, ctx context.Context, db *sql.DB) (organizationID, ownerID string) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('Provider org') RETURNING id::text`).Scan(&organizationID); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users (organization_id, email, display_name) VALUES ($1, 'o@example.invalid', 'Owner')
		RETURNING id::text`, organizationID).Scan(&ownerID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	return organizationID, ownerID
}

func TestConfigureValidatesInputsAndPersists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newProvidersTestDatabase(t, ctx)
	organizationID, ownerID := providerFixtures(t, ctx, db)
	service := NewService(db)

	// Catalog list with nothing configured.
	views, err := service.ListForOrg(ctx, organizationID)
	if err != nil || len(views) != len(Catalog) {
		t.Fatalf("ListForOrg() = %v views, %v; want catalog-sized list", len(views), err)
	}
	for _, view := range views {
		if view.Configured || view.Enabled {
			t.Fatalf("unconfigured provider %s shows configured/enabled", view.Key)
		}
	}

	// Unknown key is rejected; enabling a credential provider without an env
	// name is rejected; a non-loopback http base URL is rejected.
	if _, err := service.Configure(ctx, ConfigureInput{
		OrganizationID: organizationID, ProviderKey: "gpt-9", ActorUserID: ownerID,
	}); !errors.Is(err, ErrProviderUnknown) {
		t.Fatalf("Configure(unknown) = %v, want ErrProviderUnknown", err)
	}
	if _, err := service.Configure(ctx, ConfigureInput{
		OrganizationID: organizationID, ProviderKey: "codex", ActorUserID: ownerID,
		Enabled: true,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Configure(codex without credentialEnv) = %v, want ErrInvalidInput", err)
	}
	if _, err := service.Configure(ctx, ConfigureInput{
		OrganizationID: organizationID, ProviderKey: "opencode", ActorUserID: ownerID,
		BaseURL: "http://example.com",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Configure(http non-loopback) = %v, want ErrInvalidInput", err)
	}

	// Save an enabled opencode config; secrets only appear as env NAMES.
	saved, err := service.Configure(ctx, ConfigureInput{
		OrganizationID: organizationID, ProviderKey: "opencode", ActorUserID: ownerID,
		Label: "Office OpenCode", BaseURL: "http://127.0.0.1:4096", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Configure(opencode) error = %v", err)
	}
	if !saved.Configured || !saved.Enabled || saved.BaseURL != "http://127.0.0.1:4096" {
		t.Fatalf("saved view = %+v", saved)
	}

	endpoint, err := service.ResolveEndpoint(ctx, organizationID, "opencode")
	if err != nil || endpoint.BaseURL != "http://127.0.0.1:4096" {
		t.Fatalf("ResolveEndpoint(opencode) = %+v, %v", endpoint, err)
	}

	// Registered-but-unimplemented providers resolve to a closed failure even
	// when fully configured.
	if _, err := service.Configure(ctx, ConfigureInput{
		OrganizationID: organizationID, ProviderKey: "claude", ActorUserID: ownerID,
		CredentialEnv: "ANTHROPIC_API_KEY", Enabled: true,
	}); err != nil {
		t.Fatalf("Configure(claude) error = %v", err)
	}
	if _, err := service.ResolveEndpoint(ctx, organizationID, "claude"); !errors.Is(err, ErrProviderNotImplemented) {
		t.Fatalf("ResolveEndpoint(claude) = %v, want ErrProviderNotImplemented", err)
	}

	// Disable opencode → resolve refuses.
	if _, err := service.Configure(ctx, ConfigureInput{
		OrganizationID: organizationID, ProviderKey: "opencode", ActorUserID: ownerID,
		Label: "Office OpenCode", BaseURL: "http://127.0.0.1:4096",
	}); err != nil {
		t.Fatalf("Configure(opencode disabled) error = %v", err)
	}
	if _, err := service.ResolveEndpoint(ctx, organizationID, "opencode"); !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("ResolveEndpoint(disabled) = %v, want ErrProviderDisabled", err)
	}

	// A second org sees none of this org's configuration.
	var otherOrg string
	if err := db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('Other org') RETURNING id::text`).Scan(&otherOrg); err != nil {
		t.Fatalf("insert second org: %v", err)
	}
	if _, err := service.ResolveEndpoint(ctx, otherOrg, "opencode"); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("ResolveEndpoint(other org) = %v, want ErrProviderNotConfigured", err)
	}

	var auditCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_records
		WHERE organization_id = $1 AND action = 'provider.configure' AND project_id IS NULL`,
		organizationID).Scan(&auditCount); err != nil {
		t.Fatalf("count provider audits: %v", err)
	}
	if auditCount != 3 {
		t.Fatalf("provider audit rows = %d, want 3 successful configures", auditCount)
	}
}
