# Database migrations

Versioned PostgreSQL migrations in this directory implement the initial identity/project and task/event/outbox persistence foundation. The broader model in `docs/DATABASE_SCHEMA.md` remains logical for runtime attempts, workspaces, approvals, pull requests, and artifacts; those tables are not yet implemented.

The API migration runner is `apps/api/cmd/migrate`. From `apps/api`, set `DATABASE_URL` to a non-production database and run `go run ./cmd/migrate`. `MIGRATIONS_DIR` overrides the default `../../db/migrations` path. The API does not apply migrations automatically at startup. Run `go test ./...`; set `MIGRATION_TEST_DATABASE_URL` to a disposable PostgreSQL database to exercise migrations and tenant constraints. Never run migration tests against production data.
