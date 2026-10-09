package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidInput         = errors.New("invalid repository input")
	ErrProjectNotFound      = errors.New("project not found")
	ErrRepositoryNotFound   = errors.New("repository not found")
	repositoryOwnerPattern  = regexp.MustCompile(`^[A-Za-z0-9](?:-?[A-Za-z0-9])*$`)
	repositoryNamePattern   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	repositoryBranchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

type Repository struct {
	ID            string `json:"id"`
	ProjectID     string `json:"projectId"`
	Provider      string `json:"provider"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
}

type RegisterInput struct {
	OrganizationID string
	ProjectID      string
	ActorUserID    string
	Owner          string
	Name           string
	DefaultBranch  string
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// Get returns the connected repository for a project within the caller's tenant.
func (s *Service) Get(ctx context.Context, organizationID, projectID string) (Repository, error) {
	if !validUUID(organizationID) || !validUUID(projectID) {
		return Repository{}, fmt.Errorf("%w: organizationId and projectId must be UUIDs", ErrInvalidInput)
	}
	if s == nil || s.db == nil {
		return Repository{}, errors.New("repository service database is not configured")
	}
	var repository Repository
	err := s.db.QueryRowContext(ctx, `
		SELECT r.id::text, r.project_id::text, r.provider, r.owner, r.repo_name, r.default_branch
		FROM repositories r
		WHERE r.organization_id = $1 AND r.project_id = $2`, organizationID, projectID).Scan(
		&repository.ID, &repository.ProjectID, &repository.Provider, &repository.Owner,
		&repository.Name, &repository.DefaultBranch)
	if errors.Is(err, sql.ErrNoRows) {
		return Repository{}, ErrRepositoryNotFound
	}
	if err != nil {
		return Repository{}, fmt.Errorf("query project repository: %w", err)
	}
	return repository, nil
}

// Register connects or replaces the project's single v0.1 repository. The
// upsert is keyed on the project so Owner retries never duplicate rows; it
// records repository identity only and does not verify GitHub access.
func (s *Service) Register(ctx context.Context, input RegisterInput) (Repository, error) {
	input, err := normalizeRegisterInput(input)
	if err != nil {
		return Repository{}, err
	}
	if s == nil || s.db == nil {
		return Repository{}, errors.New("repository service database is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Repository{}, fmt.Errorf("begin repository transaction: %w", err)
	}
	defer tx.Rollback()

	var projectActive bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM projects WHERE organization_id = $1 AND id = $2 AND status = 'ACTIVE'
		)`, input.OrganizationID, input.ProjectID).Scan(&projectActive); err != nil {
		return Repository{}, fmt.Errorf("check project for repository: %w", err)
	}
	if !projectActive {
		return Repository{}, ErrProjectNotFound
	}

	var repository Repository
	err = tx.QueryRowContext(ctx, `
		INSERT INTO repositories (
			organization_id, project_id, provider, owner, repo_name, default_branch
		) VALUES ($1, $2, 'github', $3, $4, $5)
		ON CONFLICT (organization_id, project_id) DO UPDATE
			SET owner = EXCLUDED.owner, repo_name = EXCLUDED.repo_name,
				default_branch = EXCLUDED.default_branch
		RETURNING id::text, project_id::text, provider, owner, repo_name, default_branch`,
		input.OrganizationID, input.ProjectID, input.Owner, input.Name, input.DefaultBranch).Scan(
		&repository.ID, &repository.ProjectID, &repository.Provider, &repository.Owner,
		&repository.Name, &repository.DefaultBranch)
	if err != nil {
		return Repository{}, fmt.Errorf("register project repository: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_records (
			organization_id, project_id, actor_user_id, action, target_type, target_id, outcome
		) VALUES ($1, $2, $3, 'repository.register', 'repository', $4, 'success')`,
		input.OrganizationID, input.ProjectID, input.ActorUserID, repository.ID); err != nil {
		return Repository{}, fmt.Errorf("insert repository audit record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Repository{}, fmt.Errorf("commit repository registration: %w", err)
	}
	return repository, nil
}

func normalizeRegisterInput(input RegisterInput) (RegisterInput, error) {
	if !validUUID(input.OrganizationID) || !validUUID(input.ProjectID) || !validUUID(input.ActorUserID) {
		return RegisterInput{}, fmt.Errorf("%w: organizationId, projectId, and actorUserId must be UUIDs", ErrInvalidInput)
	}
	input.Owner = strings.TrimSpace(input.Owner)
	input.Name = strings.TrimSpace(input.Name)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	if utf8.RuneCountInString(input.Owner) > 39 || !repositoryOwnerPattern.MatchString(input.Owner) {
		return RegisterInput{}, fmt.Errorf("%w: owner must be a valid GitHub owner name", ErrInvalidInput)
	}
	if utf8.RuneCountInString(input.Name) > 100 || !repositoryNamePattern.MatchString(input.Name) ||
		input.Name == "." || input.Name == ".." {
		return RegisterInput{}, fmt.Errorf("%w: name must be a valid GitHub repository name", ErrInvalidInput)
	}
	if !validBranchName(input.DefaultBranch) {
		return RegisterInput{}, fmt.Errorf("%w: defaultBranch must be a valid git branch name", ErrInvalidInput)
	}
	return input, nil
}

func validBranchName(name string) bool {
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 250 ||
		strings.TrimSpace(name) != name || strings.HasPrefix(name, "-") ||
		strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return false
	}
	if !repositoryBranchPattern.MatchString(name) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for _, character := range value {
		if character == '-' {
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}
