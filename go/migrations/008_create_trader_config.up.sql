-- Phase 1: Trader configuration persistence tables
-- Creates trader_config, trader_runtime_state, trader_symbol_cooldowns, and trader_config_history.

CREATE TABLE IF NOT EXISTS trader_config (
    id                TEXT PRIMARY KEY,
    version           INT            NOT NULL DEFAULT 1,
    exchange_provider TEXT           NOT NULL,
    market_provider   TEXT           NOT NULL,
    allocation_pct    NUMERIC(5, 2)  NOT NULL,
    detail            JSONB          NOT NULL,
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by        TEXT,
    CHECK (allocation_pct >= 0 AND allocation_pct <= 100)
);

CREATE INDEX IF NOT EXISTS idx_trader_config_providers
    ON trader_config (exchange_provider, market_provider);

CREATE TABLE IF NOT EXISTS trader_runtime_state (
    trader_id             TEXT PRIMARY KEY REFERENCES trader_config (id) ON DELETE CASCADE,
    active_config_version INT         NOT NULL DEFAULT 1,
    is_running            BOOLEAN     NOT NULL DEFAULT FALSE,
    detail                JSONB       NOT NULL DEFAULT '{}'::jsonb,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS trader_symbol_cooldowns (
    trader_id      TEXT        NOT NULL REFERENCES trader_config (id) ON DELETE CASCADE,
    symbol         TEXT        NOT NULL,
    cooldown_until TIMESTAMPTZ NOT NULL,
    detail         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trader_id, symbol)
);

CREATE INDEX IF NOT EXISTS idx_trader_symbol_cooldowns_expire
    ON trader_symbol_cooldowns (cooldown_until)
    WHERE cooldown_until > NOW();

CREATE TABLE IF NOT EXISTS trader_config_history (
    id              BIGSERIAL PRIMARY KEY,
    trader_id       TEXT        NOT NULL REFERENCES trader_config (id) ON DELETE CASCADE,
    version         INT         NOT NULL,
    config_snapshot JSONB       NOT NULL,
    changed_fields  TEXT[],
    change_reason   TEXT,
    changed_by      TEXT,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_trader_config_history_trader_version
    ON trader_config_history (trader_id, version DESC);
