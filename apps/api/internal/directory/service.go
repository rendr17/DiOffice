package directory

import (
	"context"
	"database/sql"
	"fmt"
)

const maxDirectoryItems = 100

type Project struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Employee struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Department string `json:"department"`
	Status     string `json:"status"`
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

func (s *Service) ListProjects(ctx context.Context, organizationID string) ([]Project, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("directory service database is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, name, status
		FROM projects
		WHERE organization_id = $1 AND status = 'ACTIVE'
		ORDER BY lower(name), id
		LIMIT $2`, organizationID, maxDirectoryItems)
	if err != nil {
		return nil, fmt.Errorf("query organization projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0)
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.Name, &project.Status); err != nil {
			return nil, fmt.Errorf("scan organization project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organization projects: %w", err)
	}
	return projects, nil
}

func (s *Service) ListEmployees(ctx context.Context, organizationID string) ([]Employee, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("directory service database is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, name, role, department, status
		FROM employees
		WHERE organization_id = $1
		ORDER BY lower(name), id
		LIMIT $2`, organizationID, maxDirectoryItems)
	if err != nil {
		return nil, fmt.Errorf("query organization employees: %w", err)
	}
	defer rows.Close()

	employees := make([]Employee, 0)
	for rows.Next() {
		var employee Employee
		if err := rows.Scan(&employee.ID, &employee.Name, &employee.Role, &employee.Department, &employee.Status); err != nil {
			return nil, fmt.Errorf("scan organization employee: %w", err)
		}
		employees = append(employees, employee)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organization employees: %w", err)
	}
	return employees, nil
}
