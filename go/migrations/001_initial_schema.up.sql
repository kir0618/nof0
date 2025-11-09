-- NOF0 Initial Schema
-- This migration represents the final state of the database schema
--
-- Module Structure:
--   1. exchange/account  - Account and equity management
--   2. exchange/market   - Market data and symbols
--   3. executor/llm      - LLM execution and decision making
--   4. manager           - Trading strategy management

-- ============================================================================
-- MODULE: exchange/account
-- Account and equity management
-- ============================================================================

CREATE TABLE accounts (
    provider TEXT PRIMARY KEY, -- exchange provider or trader id
    is_trader BOOLEAN NOT NULL DEFAULT FALSE,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE account_snapshots (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT, -- exchange provider or trader id
    is_trader BOOLEAN NOT NULL DEFAULT FALSE,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, event_at)
);


-- ============================================================================
-- MODULE: exchange/market
-- Market data, symbols, and price ticks
-- ============================================================================

CREATE TABLE symbols (
    id TEXT PRIMARY KEY,  -- Format: <exchange_provider>/<symbol>
    exchange_provider TEXT NOT NULL,
    symbol TEXT NOT NULL,

    -- Trading pair basic info
    base_asset TEXT,
    quote_asset TEXT,

    -- Precision and rules
    base_precision INT,
    quote_precision INT,
    tick_sz DOUBLE PRECISION,
    sz_decimals INT,

    -- Status
    is_delisted BOOLEAN NOT NULL DEFAULT FALSE,

    -- Extended fields
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (exchange_provider, symbol)
);

CREATE TABLE price_ticks (
    id BIGSERIAL PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_price_ticks_symbol_event_at_desc
    ON price_ticks(symbol, event_at DESC);

CREATE INDEX idx_price_ticks_provider_symbol_event_at_desc
    ON price_ticks(exchange_provider, symbol, event_at DESC);

-- Market metrics snapshots for key indicators
CREATE TABLE market_metrics (
    id BIGSERIAL PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    symbol TEXT NOT NULL,

    -- Price metrics
    mark_price DOUBLE PRECISION,
    mid_price DOUBLE PRECISION,
    oracle_price DOUBLE PRECISION,

    -- Derivatives metrics
    funding_rate DOUBLE PRECISION,
    open_interest DOUBLE PRECISION,

    -- Volume metrics
    day_volume DOUBLE PRECISION,        -- 24h base volume
    day_notional_volume DOUBLE PRECISION, -- 24h notional volume

    -- Price changes (fractional, 0.01 = 1%)
    change_1h DOUBLE PRECISION,
    change_4h DOUBLE PRECISION,
    change_24h DOUBLE PRECISION,

    -- Additional metrics
    premium DOUBLE PRECISION,           -- Premium/discount vs oracle
    prev_day_price DOUBLE PRECISION,    -- Previous day close price

    -- Extended fields
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,

    event_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (symbol_id, event_at)
);

CREATE INDEX idx_market_metrics_symbol_event_at_desc
    ON market_metrics(symbol_id, event_at DESC);

CREATE INDEX idx_market_metrics_provider_symbol_event_at_desc
    ON market_metrics(exchange_provider, symbol, event_at DESC);

-- OHLCV candlestick data for backtesting and technical analysis
CREATE TABLE klines (
    id BIGSERIAL PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    symbol TEXT NOT NULL,

    -- Time interval (e.g., '1m', '5m', '15m', '1h', '4h', '1d')
    interval TEXT NOT NULL,

    -- Timestamp range
    open_time TIMESTAMPTZ NOT NULL,
    close_time TIMESTAMPTZ NOT NULL,

    -- OHLC prices
    open_price DOUBLE PRECISION NOT NULL,
    high_price DOUBLE PRECISION NOT NULL,
    low_price DOUBLE PRECISION NOT NULL,
    close_price DOUBLE PRECISION NOT NULL,

    -- Volume
    volume DOUBLE PRECISION,

    -- Extended fields for additional metrics (e.g., trades count, VWAP)
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (symbol_id, interval, open_time)
);

CREATE INDEX idx_klines_symbol_interval_open_time_desc
    ON klines(symbol_id, interval, open_time DESC);

CREATE INDEX idx_klines_provider_symbol_interval_open_time_desc
    ON klines(exchange_provider, symbol, interval, open_time DESC);

-- Index for efficient time range queries
CREATE INDEX idx_klines_symbol_interval_time_range
    ON klines(symbol_id, interval, open_time, close_time);

-- ============================================================================
-- MODULE: executor/llm
-- LLM execution, conversations, and decision cycles
-- ============================================================================

CREATE TABLE models (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    name TEXT NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


CREATE TABLE conversation_messages (
    id BIGSERIAL PRIMARY KEY,
    trader_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant')),
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_conversation_messages_trader
    ON conversation_messages(trader_id);

CREATE TABLE decision_cycles (
    id BIGSERIAL PRIMARY KEY,
    trader_id TEXT NOT NULL,
    cycle_number INT,
    error_message TEXT, -- not empty: error occurred
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_decision_cycles_trader_executed_at_desc
    ON decision_cycles(trader_id, executed_at DESC);

-- ============================================================================
-- MODULE: manager
-- Trading strategy configuration and position management
-- ============================================================================

CREATE TABLE trader_config (
    id                TEXT PRIMARY KEY,
    exchange_provider TEXT           NOT NULL,
    market_provider   TEXT           NOT NULL,
    detail            JSONB          NOT NULL,
    detail_checksum   INT            NOT NULL DEFAULT 1, -- detail checksum
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_trader_config_providers
    ON trader_config (exchange_provider, market_provider);

CREATE TABLE positions (
    id         TEXT PRIMARY KEY,
    trader_id  TEXT NOT NULL,
    symbol_id  TEXT NOT NULL,
    symbol     TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    side       TEXT NOT NULL CHECK(side IN ('long', 'short')),
    status     TEXT NOT NULL CHECK(status IN ('open', 'closed')),
    detail     JSONB NOT NULL,
    event_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_positions_trader_status ON positions(trader_id, status);
CREATE INDEX idx_positions_symbol_open ON positions(symbol) WHERE status = 'open';

CREATE TABLE trades (
    id          TEXT PRIMARY KEY,
    trader_id   TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    side        TEXT NOT NULL CHECK(side IN ('long', 'short')),
    detail      JSONB NOT NULL,
    event_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_trades_trader_close_ts ON trades(trader_id, event_at DESC);