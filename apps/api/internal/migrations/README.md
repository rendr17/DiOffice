# API database migrations

Versioned PostgreSQL migrations live in the repository-level `db/migrations/` directory. The current migrations establish tenant-scoped identity/project records, Owner password hashes, and the task/event/outbox persistence foundation; they do not make the execution workflow production-ready.

From `apps/api`, set `DATABASE_URL` to a non-production database and run:

```bash
go run ./cmd/migrate
```

The default migration directory is `../../db/migrations`; `MIGRATIONS_DIR` can override it. The command is explicit and is not run automatically when the API starts. Migrations are transactional and tracked by Goose. `go test ./...` exercises the migration bundle; set `MIGRATION_TEST_DATABASE_URL` to a disposable PostgreSQL database to run its integration checks.

Future migrations still need to add execution attempts, workspaces, runtime sessions, approvals, pull requests, artifacts, and reconciliation state before those workflows are implemented. Keep tenant-consistent foreign keys, canonical state checks, idempotency, event sequence allocation, and outbox writes aligned with `docs/PRD.md`, `docs/STATE_MACHINES.md`, and `docs/EVENT_SCHEMA.md`. Never apply tests or migrations to production data without an explicit, separately reviewed rollout plan.
