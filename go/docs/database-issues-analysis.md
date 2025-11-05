# Database Schema Design Issues Analysis

Date: 2025-11-05
System: nof0 Trading System

## Executive Summary

Based on production logs analysis, identified 7 critical issues in the current database schema design that are causing runtime errors, performance degradation, and data consistency problems.

## Critical Issues

### Issue 1: Schema-Code Mismatch (created_at fields)

**Severity**: HIGH - Causes INSERT failures

**Problem**:
Application code expects `created_at` field in tables where it doesn't exist:
- `market_assets`: Schema has only `updated_at`, code inserts `created_at, updated_at`
- `price_latest`: Schema has only `updated_at`, code inserts `created_at, updated_at`

**Error Log**:
```
ERROR: column "created_at" of relation "market_assets" does not exist (SQLSTATE 42703)
ERROR: column "created_at" of relation "price_latest" does not exist (SQLSTATE 42703)
```

**Root Cause**:
Inconsistent timestamp field design across table types:
- Historical tables (trades, positions): `created_at + updated_at`
- Snapshot tables (price_latest, market_assets): Only `updated_at`
- But code assumes all tables have both fields

**Impact**:
- Market data ingestion failures
- Asset metadata not persisted
- Silent data loss in production

**Recommendation**:
Option A (Quick Fix): Remove `created_at` from INSERT statements for snapshot tables
Option B (Proper Fix): Add `created_at` to all tables for audit trail consistency

---

### Issue 2: Missing UPSERT Logic

**Severity**: HIGH - Causes duplicate key violations

**Problem**:
`model_analytics` table uses plain INSERT instead of UPSERT for records with same `model_id`:

```sql
-- Current (WRONG):
INSERT INTO model_analytics (model_id, payload, ...) VALUES (...)

-- Should be:
INSERT INTO model_analytics (model_id, payload, ...)
VALUES (...)
ON CONFLICT (model_id) DO UPDATE SET
    payload = EXCLUDED.payload,
    server_time_ms = EXCLUDED.server_time_ms,
    updated_at = NOW();
```

**Error Log**:
```
ERROR: duplicate key value violates unique constraint "model_analytics_pkey" (SQLSTATE 23505)
```

**Impact**:
- Analytics updates fail after first insert
- Stale performance metrics in cache
- Error logs pollution

**Recommendation**:
Convert all snapshot table INSERTs to UPSERT pattern using `ON CONFLICT`.

---

### Issue 3: Prepared Statement Conflicts

**Severity**: MEDIUM - Causes intermittent failures

**Problem**:
Supabase Transaction Pooler (port 6543) doesn't support server-side prepared statements properly, causing conflicts when same statement is prepared in different pooled connections.

**Error Log**:
```
ERROR: prepared statement "stmtcache_8e1296b472b519472a7f9d10025d01a8330ed02a3c0eab12"
already exists (SQLSTATE 42P05)
```

**Root Cause**:
- pgx driver caches prepared statements by name
- Supabase Transaction Pooler uses transaction-level connection pooling
- Statements from previous transactions leak into new connections

**Current DSN**:
```
postgres://user:pass@aws-1-us-east-1.pooler.supabase.com:6543/postgres?sslmode=require
```

**Impact**:
- Random INSERT/UPDATE failures
- Price data and market data ingestion errors
- Non-deterministic behavior

**Recommendation**:
Add connection parameter to disable prepared statements:
```
?sslmode=require&default_query_exec_mode=simple_protocol
```

Or switch to Session Pooler (port 5432) for statement caching support.

---

### Issue 4: Performance - Geographic Latency

**Severity**: MEDIUM - Impacts trading decision latency

**Problem**:
High query latency due to geographic distance:
- SQL queries: 250ms-800ms (initial positions query: 796ms)
- Redis operations: 247ms-1342ms
- Supabase DB in AWS us-east-1, likely accessed from Asia/China

**Slow Operations**:
```
[SQL] query: slowcall - SELECT ... FROM positions WHERE status = 'open'
    AND model_id = ANY([...]) duration="796.0ms"

[REDIS] slowcall on executing: del nof0:positions:TRADER_AGGRESSIVE_SHORT
    duration="1342.3ms"
```

**Impact**:
- Trading decision cycle takes 15-30 seconds
- Cannot support high-frequency strategies
- Poor user experience

**Root Cause Analysis**:
1. Network RTT: ~150-200ms per round trip
2. Cold start overhead: TCP handshake, TLS negotiation
3. Both Postgres and Redis are remote

**Recommendation**:
Short-term:
- Enable connection pooling warmup
- Batch queries where possible
- Aggressive Redis caching with longer TTLs

Long-term:
- Deploy read replica in target region
- Use local Redis for hot data
- Consider edge compute (Cloudflare Workers, Vercel Edge)

---

### Issue 5: Suboptimal Index Design

**Severity**: LOW-MEDIUM - Impacts query performance

**Problem**:
Current index on positions table doesn't match query pattern:

**Query Pattern**:
```sql
SELECT * FROM positions
WHERE status = 'open'
AND model_id = ANY(['TRADER_A', 'TRADER_B'])
ORDER BY model_id, symbol
```

**Current Index**:
```sql
CREATE INDEX idx_positions_model_exchange_open
ON positions(model_id, exchange_provider)
WHERE status = 'open';
```

**Issue**:
- Index includes `exchange_provider` but query doesn't filter on it
- Query uses `model_id = ANY(array)` which may not use index efficiently
- Missing index on `(model_id, symbol)` for ordering

**Recommendation**:
```sql
-- Replace with:
CREATE INDEX idx_positions_open_model_symbol
ON positions(model_id, symbol)
WHERE status = 'open';

-- Or if filtering by status is more selective:
CREATE INDEX idx_positions_status_model
ON positions(status, model_id);
```

---

### Issue 6: Redundant Primary Keys

**Severity**: LOW - Wastes storage

**Problem**:
Many tables have both BIGSERIAL surrogate key and UNIQUE constraint on natural key:

**Examples**:
```sql
CREATE TABLE price_latest (
    id BIGSERIAL PRIMARY KEY,        -- Unused surrogate key
    provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    ...
    UNIQUE (provider, symbol)        -- Actual business key
);

CREATE TABLE market_assets (
    id BIGSERIAL PRIMARY KEY,        -- Unused surrogate key
    provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    ...
    UNIQUE (provider, symbol)        -- Actual business key
);
```

**Impact**:
- Wastes 8 bytes per row (BIGSERIAL)
- Maintains two indexes (primary + unique)
- Foreign key references would use wrong key
- Violates normalization best practices

**Recommendation**:
Use natural keys as primary keys for snapshot tables:
```sql
CREATE TABLE price_latest (
    provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    price DOUBLE PRECISION NOT NULL,
    ts_ms BIGINT NOT NULL,
    raw JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, symbol)
);
```

---

### Issue 7: Inconsistent Timestamp Semantics

**Severity**: LOW - Design clarity issue

**Problem**:
Timestamp field usage is inconsistent across table categories:

**Current State**:
| Table Type | Should Have | Actual | Status |
|------------|-------------|--------|--------|
| Historical (trades) | created_at | created_at + updated_at | OK |
| Snapshot (price_latest) | updated_at | updated_at | OK |
| Config (models) | both | both | OK |
| State (trader_state) | both | updated_at only | Missing |

**Impact**:
- Cannot determine when trader was first created
- Inconsistent audit trail
- Confusion for future developers

**Recommendation**:
Add `created_at` to `trader_state` table for complete lifecycle tracking.

---

## Prioritized Fix Roadmap

### P0 - Critical (Fix Immediately)
1. **Issue 1**: Add `created_at` to `market_assets` and `price_latest` tables
2. **Issue 2**: Convert `model_analytics` INSERT to UPSERT
3. **Issue 3**: Update Postgres DSN to disable prepared statements

### P1 - High (Fix This Sprint)
4. **Issue 4**: Implement connection pooling warmup and query batching
5. **Issue 5**: Optimize positions table index

### P2 - Medium (Fix Next Sprint)
6. **Issue 6**: Refactor primary keys in snapshot tables
7. **Issue 7**: Add missing `created_at` to `trader_state`

### P3 - Long-term (Architectural)
8. Deploy regional read replicas
9. Implement edge caching layer

---

## Proposed Migration

### Migration 003: Fix Schema-Code Mismatch

**003_fix_timestamps.up.sql**:
```sql
-- Add created_at to snapshot tables for consistency
ALTER TABLE market_assets
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE price_latest
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE market_asset_ctx
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE trader_state
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Update existing rows
UPDATE market_assets SET created_at = updated_at WHERE created_at IS NULL;
UPDATE price_latest SET created_at = updated_at WHERE created_at IS NULL;
UPDATE market_asset_ctx SET created_at = updated_at WHERE created_at IS NULL;
UPDATE trader_state SET created_at = updated_at WHERE created_at IS NULL;
```

**003_fix_timestamps.down.sql**:
```sql
ALTER TABLE trader_state DROP COLUMN created_at;
ALTER TABLE market_asset_ctx DROP COLUMN created_at;
ALTER TABLE price_latest DROP COLUMN created_at;
ALTER TABLE market_assets DROP COLUMN created_at;
```

### Migration 004: Optimize Indexes

**004_optimize_indexes.up.sql**:
```sql
-- Drop suboptimal index
DROP INDEX IF EXISTS idx_positions_model_exchange_open;

-- Create optimized index for positions query pattern
CREATE INDEX idx_positions_open_model_symbol
ON positions(model_id, symbol)
WHERE status = 'open';

-- Add composite index for better query performance
CREATE INDEX idx_positions_status_model
ON positions(status, model_id);
```

**004_optimize_indexes.down.sql**:
```sql
DROP INDEX IF EXISTS idx_positions_status_model;
DROP INDEX IF EXISTS idx_positions_open_model_symbol;

CREATE INDEX idx_positions_model_exchange_open
ON positions(model_id, exchange_provider)
WHERE status = 'open';
```

---

## Configuration Changes

### Update Postgres DSN

**File**: `etc/nof0.yaml`

**Before**:
```yaml
Postgres:
  DataSource: "postgres://user:pass@aws-1-us-east-1.pooler.supabase.com:6543/postgres?sslmode=require&supa=base-pooler.x"
```

**After**:
```yaml
Postgres:
  DataSource: "postgres://user:pass@aws-1-us-east-1.pooler.supabase.com:6543/postgres?sslmode=require&default_query_exec_mode=simple_protocol&supa=base-pooler.x"
  # Or switch to session pooler:
  # DataSource: "postgres://user:pass@aws-1-us-east-1.pooler.supabase.com:5432/postgres?sslmode=require"
```

---

## Code Changes Required

### Fix UPSERT in model_analytics

**File**: `internal/persistence/engine/persistence.go`

**Before**:
```go
_, err := s.modelAnalyticsModel.Insert(ctx, &model.ModelAnalytics{
    ModelId:       traderID,
    Payload:       string(jsonData),
    ServerTimeMs:  time.Now().UnixMilli(),
    Metadata:      string(metaJSON),
})
```

**After**:
```go
// Use Upsert method or raw SQL with ON CONFLICT
query := `
INSERT INTO model_analytics (model_id, payload, server_time_ms, metadata, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (model_id) DO UPDATE SET
    payload = EXCLUDED.payload,
    server_time_ms = EXCLUDED.server_time_ms,
    metadata = EXCLUDED.metadata,
    updated_at = NOW()
`
_, err := s.sqlConn.ExecCtx(ctx, query, traderID, string(jsonData), time.Now().UnixMilli(), string(metaJSON))
```

---

## Testing Plan

1. **Schema Migration Testing**:
   - Test on staging with production data snapshot
   - Verify no data loss during migration
   - Confirm backward compatibility

2. **Performance Testing**:
   - Benchmark positions query before/after index changes
   - Measure prepared statement overhead with/without simple protocol
   - Load test with realistic trading volume

3. **Integration Testing**:
   - Run full trading cycle with new schema
   - Verify all INSERT/UPSERT operations succeed
   - Check analytics persistence

---

## Monitoring & Alerts

Add alerting for:
- SQL errors with SQLSTATE 42703 (column not found)
- SQL errors with SQLSTATE 23505 (duplicate key)
- SQL errors with SQLSTATE 42P05 (prepared statement exists)
- Query latency > 500ms for critical paths
- Failed market data ingestion

---

## References

- [Supabase Connection Pooling](https://supabase.com/docs/guides/database/connecting-to-postgres#connection-pool)
- [pgx Prepared Statement Modes](https://github.com/jackc/pgx/wiki/Prepared-Statements)
- [PostgreSQL Index Design Best Practices](https://www.postgresql.org/docs/current/indexes.html)
- Project schema: `docs/data-store.md`
- Migration guide: `migrations/README.md`
