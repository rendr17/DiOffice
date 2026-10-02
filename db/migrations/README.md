# Database migrations

The current `docs/DATABASE_SCHEMA.md` is a logical model, not executable DDL. Add versioned SQL migrations here when implementing the first persisted domain slice. Each migration must be reviewed against the canonical lifecycle/event contracts, include the necessary tenant-consistent constraints and indexes, and be exercised against a disposable PostgreSQL instance before merge. Do not place secrets or production data in migrations or fixtures.
