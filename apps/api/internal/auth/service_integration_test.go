package auth

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

func TestBootstrapOwnerLoginCSRFAndSessionRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newAuthTestDatabase(t, ctx)
	service := NewService(db)
	password := "correct-horse-battery-staple"

	owner, err := service.BootstrapOwner(ctx, BootstrapOwnerInput{
		OrganizationName: "Auth integration org",
		ProjectName:      "Manual API test",
		Email:            "owner@example.invalid",
		Password:         password,
	})
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	var storedHash string
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, owner.UserID).Scan(&storedHash); err != nil {
		t.Fatalf("read stored password hash: %v", err)
	}
	if storedHash == password || !VerifyPassword(storedHash, password) {
		t.Fatal("bootstrap stored the password directly or stored an invalid hash")
	}

	login, err := service.Login(ctx, LoginInput{
		OrganizationID: owner.OrganizationID,
		Email:          " OWNER@example.invalid ",
		Password:       password,
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if login.SessionToken == "" || login.CSRFToken == "" || login.Identity.UserID != owner.UserID {
		t.Fatalf("Login() returned incomplete session: %+v", login)
	}
	identity, err := service.Authenticate(ctx, login.SessionToken)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if identity.OrganizationID != owner.OrganizationID || identity.UserID != owner.UserID || identity.Role != "OWNER" {
		t.Fatalf("Authenticate() identity = %+v, want the bootstrapped Owner", identity)
	}
	if !service.VerifyCSRF(identity, login.CSRFToken, login.CSRFToken) {
		t.Fatal("VerifyCSRF() rejected matching double-submit token")
	}
	if service.VerifyCSRF(identity, login.CSRFToken, "different-token") {
		t.Fatal("VerifyCSRF() accepted mismatched double-submit token")
	}

	if _, err := service.Login(ctx, LoginInput{
		OrganizationID: owner.OrganizationID,
		Email:          "owner@example.invalid",
		Password:       "incorrect-password",
	}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() with wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if err := service.RevokeSession(ctx, identity.SessionID); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, login.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate() after revocation error = %v, want ErrUnauthenticated", err)
	}
}

func newAuthTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schemaName := "dioffice_auth_test_" + hex.EncodeToString(randomBytes)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
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
	db.SetMaxOpenConns(8)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close isolated test schema: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated test schema: %v", err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate auth test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	if err := migrations.Up(ctx, db, filepath.Join(repoRoot, "db", "migrations")); err != nil {
		t.Fatalf("apply migrations to isolated test schema: %v", err)
	}
	return db
}
