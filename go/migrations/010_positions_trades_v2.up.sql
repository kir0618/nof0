-- Phase 3: compact positions & trades schema

DROP TABLE IF EXISTS trades CASCADE;
DROP TABLE IF EXISTS positions CASCADE;

CREATE TABLE positions (
    id         TEXT PRIMARY KEY,
    trader_id  TEXT NOT NULL REFERENCES trader_config(id) ON DELETE CASCADE,
    symbol     TEXT NOT NULL,
    side       TEXT NOT NULL CHECK(side IN ('long', 'short')),
    status     TEXT NOT NULL CHECK(status IN ('open', 'closed')),
    detail     JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_positions_trader_status ON positions(trader_id, status);
CREATE INDEX idx_positions_symbol_open ON positions(symbol) WHERE status = 'open';

CREATE TABLE trades (
    id          TEXT PRIMARY KEY,
    trader_id   TEXT NOT NULL REFERENCES trader_config(id) ON DELETE CASCADE,
    symbol      TEXT NOT NULL,
    side        TEXT NOT NULL CHECK(side IN ('long', 'short')),
    close_ts_ms BIGINT NOT NULL,
    detail      JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_trades_trader_close_ts ON trades(trader_id, close_ts_ms DESC);
