-- Rollback NOF0 Initial Schema
-- Drop tables in reverse order of creation

-- ============================================================================
-- MODULE: manager (reverse order)
-- ============================================================================

DROP TABLE IF EXISTS trades CASCADE;
DROP TABLE IF EXISTS positions CASCADE;
DROP TABLE IF EXISTS trader_config CASCADE;

-- ============================================================================
-- MODULE: executor/llm (reverse order)
-- ============================================================================

DROP TABLE IF EXISTS decision_cycles CASCADE;
DROP TABLE IF EXISTS conversation_messages CASCADE;
DROP TABLE IF EXISTS models CASCADE;

-- ============================================================================
-- MODULE: exchange/market (reverse order)
-- ============================================================================

DROP TABLE IF EXISTS klines CASCADE;
DROP TABLE IF EXISTS market_metrics CASCADE;
DROP TABLE IF EXISTS price_ticks CASCADE;
DROP TABLE IF EXISTS symbols CASCADE;

-- ============================================================================
-- MODULE: exchange/account (reverse order)
-- ============================================================================

DROP TABLE IF EXISTS account_snapshots CASCADE;
DROP TABLE IF EXISTS accounts CASCADE;
