package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionLifetime         = 12 * time.Hour
	developmentOwnerEmail   = "local-dev-owner@example.invalid"
	developmentOwnerOrgName = "DiOffice Local Development"
)

var (
	ErrInvalidBootstrapInput = errors.New("invalid Owner bootstrap input")
	ErrInvalidCredentials    = errors.New("invalid organization, email, or password")
	ErrUnauthenticated       = errors.New("unauthenticated")
	organizationIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	dummyPasswordHash, _     = bcrypt.GenerateFromPassword([]byte("not-a-real-dioffice-account"), bcryptCost)
)

type Service struct {
	db *sql.DB
}

type BootstrapOwnerInput struct {
	OrganizationName string
	ProjectName      string
	Email            string
	Password         string
}

type BootstrapOwnerResult struct {
	OrganizationID string
	ProjectID      string
	UserID         string
	EmployeeID     string
}

type LoginInput struct {
	OrganizationID string
	Email          string
	Password       string
}

type Identity struct {
	OrganizationID string
	UserID         string
	Email          string
	DisplayName    string
	Role           string
	SessionID      string
	csrfTokenHash  []byte
}

type LoginSession struct {
	Identity     Identity
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// BootstrapOwner creates the initial organization, Owner, Deni employee, and project.
// The password is only stored as a bcrypt hash; callers must never log the input.
func (s *Service) BootstrapOwner(ctx context.Context, input BootstrapOwnerInput) (BootstrapOwnerResult, error) {
	if s == nil || s.db == nil {
		return BootstrapOwnerResult{}, errors.New("auth service database is not configured")
	}
	organizationName := strings.TrimSpace(input.OrganizationName)
	projectName := strings.TrimSpace(input.ProjectName)
	email, err := normalizeEmail(input.Email)
	if err != nil || !validName(organizationName, 120) || !validName(projectName, 120) {
		return BootstrapOwnerResult{}, ErrInvalidBootstrapInput
	}
	passwordHash, err := HashPassword(input.Password)
	if err != nil {
		return BootstrapOwnerResult{}, ErrInvalidBootstrapInput
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("begin Owner bootstrap: %w", err)
	}
	defer tx.Rollback()

	var result BootstrapOwnerResult
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO organizations (name) VALUES ($1) RETURNING id::text`, organizationName).Scan(&result.OrganizationID); err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("create bootstrap organization: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO users (organization_id, email, display_name, password_hash)
		VALUES ($1, $2, 'Owner', $3)
		RETURNING id::text`, result.OrganizationID, email, passwordHash).Scan(&result.UserID); err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("create bootstrap Owner: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO employees (organization_id, name, slug, role, department)
		VALUES ($1, 'Deni', 'deni', 'Frontend Engineer', 'Engineering')
		RETURNING id::text`, result.OrganizationID).Scan(&result.EmployeeID); err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("create bootstrap employee: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO projects (organization_id, name)
		VALUES ($1, $2) RETURNING id::text`, result.OrganizationID, projectName).Scan(&result.ProjectID); err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("create bootstrap project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("commit Owner bootstrap: %w", err)
	}
	return result, nil
}

func (s *Service) Login(ctx context.Context, input LoginInput) (LoginSession, error) {
	if s == nil || s.db == nil {
		return LoginSession{}, errors.New("auth service database is not configured")
	}
	email, emailErr := normalizeEmail(input.Email)
	if emailErr != nil || !organizationIDPattern.MatchString(input.OrganizationID) || len(input.Password) > bcryptMaxPasswordBytes {
		s.compareDummyPassword(input.Password)
		return LoginSession{}, ErrInvalidCredentials
	}

	var identity Identity
	var passwordHash sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, organization_id::text, email, display_name, role, password_hash
		FROM users
		WHERE organization_id = $1 AND lower(email) = $2`,
		input.OrganizationID, email).Scan(
		&identity.UserID, &identity.OrganizationID, &identity.Email, &identity.DisplayName,
		&identity.Role, &passwordHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.compareDummyPassword(input.Password)
			return LoginSession{}, ErrInvalidCredentials
		}
		return LoginSession{}, fmt.Errorf("look up Owner credentials: %w", err)
	}
	if !passwordHash.Valid {
		s.compareDummyPassword(input.Password)
		return LoginSession{}, ErrInvalidCredentials
	}
	if !VerifyPassword(passwordHash.String, input.Password) {
		return LoginSession{}, ErrInvalidCredentials
	}

	return createSession(ctx, s.db, identity)
}

// CreateDevelopmentSession creates or reuses a passwordless local Owner and
// issues a normal server-side session. Its HTTP route is gated by a loopback-only
// development flag and must never be exposed in a deployed environment.
func (s *Service) CreateDevelopmentSession(ctx context.Context) (LoginSession, error) {
	if s == nil || s.db == nil {
		return LoginSession{}, errors.New("auth service database is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LoginSession{}, fmt.Errorf("begin development Owner session: %w", err)
	}
	defer tx.Rollback()

	var lockAcquired bool
	if err := tx.QueryRowContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('dioffice-local-dev-owner', 0)) IS NOT NULL`).Scan(&lockAcquired); err != nil {
		return LoginSession{}, fmt.Errorf("lock development Owner seed: %w", err)
	}
	if !lockAcquired {
		return LoginSession{}, errors.New("development Owner seed lock was not acquired")
	}

	var identity Identity
	err = tx.QueryRowContext(ctx, `
		SELECT u.id::text, u.organization_id::text, u.email, u.display_name, u.role
		FROM users AS u
		JOIN organizations AS o ON o.id = u.organization_id
		WHERE o.name = $1 AND lower(u.email) = $2
			AND u.display_name = 'Local Development Owner'
			AND u.role = 'OWNER' AND u.password_hash IS NULL
		ORDER BY u.created_at
		LIMIT 1`, developmentOwnerOrgName, developmentOwnerEmail).Scan(
		&identity.UserID, &identity.OrganizationID, &identity.Email, &identity.DisplayName, &identity.Role)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO organizations (name) VALUES ($1) RETURNING id::text`, developmentOwnerOrgName).Scan(&identity.OrganizationID); err != nil {
			return LoginSession{}, fmt.Errorf("create development organization: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO users (organization_id, email, display_name, password_hash)
			VALUES ($1, $2, 'Local Development Owner', NULL)
			RETURNING id::text, email, display_name, role`,
			identity.OrganizationID, developmentOwnerEmail).Scan(
			&identity.UserID, &identity.Email, &identity.DisplayName, &identity.Role); err != nil {
			return LoginSession{}, fmt.Errorf("create development Owner: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO employees (organization_id, name, slug, role, department)
			VALUES ($1, 'Deni', 'deni', 'Frontend Engineer', 'Engineering')`, identity.OrganizationID); err != nil {
			return LoginSession{}, fmt.Errorf("create development employee: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (organization_id, name) VALUES ($1, 'Local Manual Test')`, identity.OrganizationID); err != nil {
			return LoginSession{}, fmt.Errorf("create development project: %w", err)
		}
	} else if err != nil {
		return LoginSession{}, fmt.Errorf("look up development Owner: %w", err)
	}

	session, err := createSession(ctx, tx, identity)
	if err != nil {
		return LoginSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return LoginSession{}, fmt.Errorf("commit development Owner session: %w", err)
	}
	return session, nil
}

func (s *Service) Authenticate(ctx context.Context, sessionToken string) (Identity, error) {
	if s == nil || s.db == nil {
		return Identity{}, errors.New("auth service database is not configured")
	}
	tokenBytes, err := base64.RawURLEncoding.DecodeString(sessionToken)
	if err != nil || len(tokenBytes) != 32 {
		return Identity{}, ErrUnauthenticated
	}
	tokenHash := sha256.Sum256([]byte(sessionToken))
	var identity Identity
	err = s.db.QueryRowContext(ctx, `
		SELECT s.id::text, s.organization_id::text, u.id::text, u.email,
			u.display_name, u.role, s.csrf_token_hash
		FROM user_sessions AS s
		JOIN users AS u ON u.organization_id = s.organization_id AND u.id = s.user_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`,
		tokenHash[:]).Scan(
		&identity.SessionID, &identity.OrganizationID, &identity.UserID, &identity.Email,
		&identity.DisplayName, &identity.Role, &identity.csrfTokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Identity{}, ErrUnauthenticated
		}
		return Identity{}, fmt.Errorf("validate Owner session: %w", err)
	}
	return identity, nil
}

func (s *Service) VerifyCSRF(identity Identity, cookieToken, headerToken string) bool {
	if cookieToken == "" || headerToken == "" ||
		subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
		return false
	}
	hash := sha256.Sum256([]byte(cookieToken))
	return len(identity.csrfTokenHash) == len(hash) &&
		subtle.ConstantTimeCompare(identity.csrfTokenHash, hash[:]) == 1
}

func (s *Service) RevokeSession(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return errors.New("auth service database is not configured")
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE user_sessions SET revoked_at = now()
		WHERE id = $1 AND revoked_at IS NULL`, sessionID); err != nil {
		return fmt.Errorf("revoke Owner session: %w", err)
	}
	return nil
}

func (s *Service) compareDummyPassword(password string) {
	if len(dummyPasswordHash) > 0 && len(password) <= bcryptMaxPasswordBytes {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
	}
}

type sessionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func createSession(ctx context.Context, querier sessionQuerier, identity Identity) (LoginSession, error) {
	token, err := randomToken()
	if err != nil {
		return LoginSession{}, fmt.Errorf("generate session token: %w", err)
	}
	csrfToken, err := randomToken()
	if err != nil {
		return LoginSession{}, fmt.Errorf("generate CSRF token: %w", err)
	}
	tokenHash := sha256.Sum256([]byte(token))
	csrfHash := sha256.Sum256([]byte(csrfToken))
	identity.csrfTokenHash = csrfHash[:]
	var expiresAt time.Time
	if err := querier.QueryRowContext(ctx, `
		INSERT INTO user_sessions (
			organization_id, user_id, token_hash, csrf_token_hash, expires_at
		) VALUES ($1, $2, $3, $4, now() + interval '12 hours')
		RETURNING id::text, expires_at`,
		identity.OrganizationID, identity.UserID, tokenHash[:], csrfHash[:]).Scan(&identity.SessionID, &expiresAt); err != nil {
		return LoginSession{}, fmt.Errorf("create Owner session: %w", err)
	}
	return LoginSession{
		Identity: identity, SessionToken: token, CSRFToken: csrfToken, ExpiresAt: expiresAt,
	}, nil
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func normalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > 254 {
		return "", errors.New("email must contain 1 to 254 characters")
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil || parsed.Address != trimmed || strings.ContainsAny(trimmed, "\r\n\t ") {
		return "", errors.New("email must be a single mailbox address")
	}
	return strings.ToLower(trimmed), nil
}

func validName(value string, maxLength int) bool {
	return value != "" && len([]rune(value)) <= maxLength
}
