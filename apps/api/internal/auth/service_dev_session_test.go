package auth

import (
	"context"
	"testing"
	"time"
)

func TestCreateDevelopmentSessionSeedsAndReusesOneOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newAuthTestDatabase(t, ctx)
	service := NewService(db)

	first, err := service.CreateDevelopmentSession(ctx)
	if err != nil {
		t.Fatalf("CreateDevelopmentSession() first call error = %v", err)
	}
	second, err := service.CreateDevelopmentSession(ctx)
	if err != nil {
		t.Fatalf("CreateDevelopmentSession() second call error = %v", err)
	}
	if first.Identity.UserID == "" || first.Identity.Role != "OWNER" || first.Identity.UserID != second.Identity.UserID ||
		first.Identity.OrganizationID != second.Identity.OrganizationID {
		t.Fatalf("development identities differ or are incomplete: first=%+v second=%+v", first.Identity, second.Identity)
	}
	identity, err := service.Authenticate(ctx, first.SessionToken)
	if err != nil {
		t.Fatalf("Authenticate() development session error = %v", err)
	}
	if identity.UserID != first.Identity.UserID || !service.VerifyCSRF(identity, first.CSRFToken, first.CSRFToken) {
		t.Fatalf("development session did not authenticate with valid CSRF: identity=%+v", identity)
	}
	var organizationCount, userCount, employeeCount, projectCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM organizations WHERE name = $1`, developmentOwnerOrgName).Scan(&organizationCount); err != nil {
		t.Fatalf("count development organizations: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = $1 AND password_hash IS NULL`, developmentOwnerEmail).Scan(&userCount); err != nil {
		t.Fatalf("count development Owners: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM employees WHERE organization_id = $1`, first.Identity.OrganizationID).Scan(&employeeCount); err != nil {
		t.Fatalf("count development employees: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE organization_id = $1`, first.Identity.OrganizationID).Scan(&projectCount); err != nil {
		t.Fatalf("count development projects: %v", err)
	}
	if organizationCount != 1 || userCount != 1 || employeeCount != 1 || projectCount != 1 {
		t.Fatalf("development seed counts org/user/employee/project = %d/%d/%d/%d, want 1/1/1/1", organizationCount, userCount, employeeCount, projectCount)
	}
}
