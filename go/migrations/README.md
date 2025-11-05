# Database Migrations Guide

## Overview

This directory contains SQL migration files managed by [golang-migrate](https://github.com/golang-migrate/migrate). The migration system provides version control for database schema changes.

## Prerequisites

Install golang-migrate CLI:

```bash
# macOS
brew install golang-migrate

# Linux/macOS (Go install)
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Verify installation
migrate -version
```

## Migration File Structure

Migration files follow the naming pattern: `{version}_{description}.up.sql` and `{version}_{description}.down.sql`

```
migrations/
  001_domain.up.sql                    # Core tables and indexes (forward)
  001_domain.down.sql                  # Core tables rollback (backward)
  002_refresh_helpers.up.sql           # Materialized views (forward)
  002_refresh_helpers.down.sql         # Materialized views rollback (backward)
  003_add_notifications.up.sql         # Example new migration (forward)
  003_add_notifications.down.sql       # Example rollback (backward)
```

Each `.up.sql` file contains the forward migration (creating/altering schema).
Each `.down.sql` file contains the rollback migration (reversing the changes).

## Basic Commands

### Environment Setup

Set the database connection string:

```bash
export POSTGRES_DSN="postgres://user:password@localhost:5432/dbname?sslmode=disable"
```

### Running Migrations

```bash
# Apply all pending migrations
make migrate-up

# Rollback last migration
make migrate-down

# Check current migration version
make migrate-status

# Create new migration file
make migrate-create NAME=add_user_preferences
```

### Direct CLI Usage

```bash
# Apply all migrations
migrate -path migrations -database "$POSTGRES_DSN" up

# Rollback N migrations
migrate -path migrations -database "$POSTGRES_DSN" down 2

# Go to specific version
migrate -path migrations -database "$POSTGRES_DSN" goto 3

# Force version (use cautiously)
migrate -path migrations -database "$POSTGRES_DSN" force 2
```

## Best Practices

### 1. Migration File Guidelines

**DO:**
- Write both Up and Down migrations
- Use transactions where appropriate
- Add indexes in separate migrations from table creation if they are slow
- Use `IF NOT EXISTS` for idempotent operations when needed
- Include descriptive comments explaining complex changes
- Test migrations on a copy of production data

**DON'T:**
- Modify existing migration files after they've been applied
- Delete migration files from version control
- Use database-specific features without documentation
- Mix DDL and data changes in the same migration
- Perform destructive operations without backups

### 2. Creating New Migrations

```bash
# Create migration file pair
make migrate-create NAME=add_user_notifications

# This creates two files:
# migrations/003_add_user_notifications.up.sql
# migrations/003_add_user_notifications.down.sql
```

Example migration structure:

**migrations/003_add_user_notifications.up.sql:**
```sql
CREATE TABLE user_notifications (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    message TEXT NOT NULL,
    read_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_user_notifications_user_id ON user_notifications(user_id);
CREATE INDEX idx_user_notifications_created_at ON user_notifications(created_at DESC);
```

**migrations/003_add_user_notifications.down.sql:**
```sql
DROP TABLE user_notifications;
```

### 3. Testing Migrations

Before committing:

```bash
# Apply migration
make migrate-up

# Verify schema
psql $POSTGRES_DSN -c "\d+ table_name"

# Test rollback
make migrate-down

# Verify rollback worked
psql $POSTGRES_DSN -c "\d+ table_name"

# Re-apply for final verification
make migrate-up
```

### 4. Handling Migration Conflicts

When multiple developers create migrations simultaneously:

**Scenario:** Two developers create `003_xxx.sql` independently

**Resolution:**
1. Merge the later migration into a new sequential number
2. Rename: `003_feature_b.sql` -> `004_feature_b.sql`
3. Update version number in git

**Prevention:** Use timestamp-based naming (optional):
```bash
# Add timestamp prefix
20250105120000_add_notifications.sql
20250105130000_add_user_preferences.sql
```

### 5. Production Deployment

**Pre-deployment checklist:**
- Backup database before migration
- Test migration on staging with production data snapshot
- Review migration for blocking operations
- Plan rollback strategy
- Monitor migration execution time

**Deployment steps:**
```bash
# 1. Check current version
make migrate-status

# 2. Apply migrations
make migrate-up

# 3. Verify application still works
# 4. If issues occur, rollback:
make migrate-down
```

### 6. Common Patterns

**Adding a column:**
```sql
-- +migrate Up
ALTER TABLE positions ADD COLUMN notes TEXT;

-- +migrate Down
ALTER TABLE positions DROP COLUMN notes;
```

**Adding an index:**
```sql
-- +migrate Up
CREATE INDEX CONCURRENTLY idx_positions_status
ON positions(status) WHERE status = 'open';

-- +migrate Down
DROP INDEX CONCURRENTLY idx_positions_status;
```

**Modifying a column (safe pattern):**
```sql
-- +migrate Up
-- Add new column
ALTER TABLE accounts ADD COLUMN balance_usd NUMERIC(20, 8);

-- Backfill data
UPDATE accounts SET balance_usd = balance::NUMERIC WHERE balance_usd IS NULL;

-- Make it non-nullable
ALTER TABLE accounts ALTER COLUMN balance_usd SET NOT NULL;

-- +migrate Down
ALTER TABLE accounts DROP COLUMN balance_usd;
```

**Creating a materialized view:**
```sql
-- +migrate Up
CREATE MATERIALIZED VIEW v_daily_pnl AS
SELECT
    model_id,
    DATE(created_at) as date,
    SUM(closed_pnl) as total_pnl
FROM trades
GROUP BY model_id, DATE(created_at);

CREATE UNIQUE INDEX idx_v_daily_pnl_model_date
ON v_daily_pnl(model_id, date);

-- +migrate Down
DROP MATERIALIZED VIEW v_daily_pnl;
```

### 7. Troubleshooting

**Error: "Dirty database version"**
```bash
# Check current state
make migrate-status

# If a migration failed mid-way, force to last known good version
make migrate-force VERSION=2

# Then fix the migration and retry
make migrate-up
```

**Error: "no change" when expecting migration**
```bash
# Verify migration files exist
ls migrations/

# Check current version
make migrate-status

# Verify database connection
psql $POSTGRES_DSN -c "SELECT version FROM schema_migrations;"
```

**Rolling back multiple migrations**
```bash
# Rollback 3 migrations
migrate -path migrations -database "$POSTGRES_DSN" down 3
```

## Integration with go-zero

After applying migrations, regenerate Go models:

```bash
# Regenerate all models
make model-gen

# Or manually:
goctl model pg datasource \
  --url "$POSTGRES_DSN" \
  --dir internal/model \
  --cache \
  --table "*"
```

This generates `*_gen.go` files. Custom business logic should go in separate `*model.go` files.

## CI/CD Integration

Example GitHub Actions workflow:

```yaml
- name: Run migrations
  run: make migrate-up
  env:
    POSTGRES_DSN: postgres://test:test@localhost:5432/test_db

- name: Verify models are up to date
  run: |
    make model-gen
    git diff --exit-code internal/model/
```

## References

- [golang-migrate documentation](https://github.com/golang-migrate/migrate)
- [go-zero model generation](https://go-zero.dev/docs/tutorials/cli/model)
- Project schema design: `../docs/data-store.md`

## Migration History

| Version | Description | Date Applied |
|---------|-------------|--------------|
| 001 | Core domain tables (positions, trades, accounts, etc.) | 2025-11-05 |
| 002 | Materialized views and refresh helpers | 2025-11-05 |
| 003 | Fix missing created_at columns in snapshot tables | 2025-11-05 |
| 004 | Optimize indexes for positions query patterns | 2025-11-05 |
