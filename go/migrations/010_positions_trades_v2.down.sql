DROP TABLE IF EXISTS trades CASCADE;
DROP TABLE IF EXISTS positions CASCADE;

CREATE TABLE IF NOT EXISTS positions (
    id TEXT PRIMARY KEY,
    model_id TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    side TEXT NOT NULL DEFAULT 'long' CHECK (side IN ('long', 'short', 'flat')),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    entry_oid BIGINT,
    risk_usd DOUBLE PRECISION,
    confidence DOUBLE PRECISION,
    index_col JSONB,
    exit_plan JSONB,
    entry_time_ms BIGINT NOT NULL,
    entry_price DOUBLE PRECISION NOT NULL,
    tp_oid BIGINT,
    margin DOUBLE PRECISION,
    wait_for_fill BOOLEAN NOT NULL DEFAULT FALSE,
    sl_oid BIGINT,
    current_price DOUBLE PRECISION,
    closed_pnl DOUBLE PRECISION,
    liquidation_price DOUBLE PRECISION,
    commission DOUBLE PRECISION,
    leverage DOUBLE PRECISION,
    slippage DOUBLE PRECISION,
    quantity DOUBLE PRECISION NOT NULL,
    unrealized_pnl DOUBLE PRECISION,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_positions_model ON positions(model_id);
CREATE INDEX idx_positions_symbol ON positions(symbol);
CREATE INDEX idx_positions_open_model_symbol ON positions(model_id, symbol);
CREATE INDEX idx_positions_status_model ON positions(status, model_id);

CREATE TABLE IF NOT EXISTS trades (
    id TEXT PRIMARY KEY,
    model_id TEXT NOT NULL,
    exchange_provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    side TEXT NOT NULL,
    trade_type TEXT,
    trade_id TEXT,
    quantity DOUBLE PRECISION,
    leverage DOUBLE PRECISION,
    confidence DOUBLE PRECISION,
    entry_price DOUBLE PRECISION,
    entry_ts_ms BIGINT NOT NULL,
    entry_human_time TEXT,
    entry_sz DOUBLE PRECISION,
    entry_tid BIGINT,
    entry_oid BIGINT,
    entry_crossed BOOLEAN NOT NULL DEFAULT FALSE,
    entry_liquidation JSONB,
    entry_commission_dollars DOUBLE PRECISION,
    entry_closed_pnl DOUBLE PRECISION,
    exit_price DOUBLE PRECISION,
    exit_ts_ms BIGINT,
    exit_human_time TEXT,
    exit_sz DOUBLE PRECISION,
    exit_tid BIGINT,
    exit_oid BIGINT,
    exit_crossed BOOLEAN,
    exit_liquidation JSONB,
    exit_commission_dollars DOUBLE PRECISION,
    exit_closed_pnl DOUBLE PRECISION,
    exit_plan JSONB,
    realized_gross_pnl DOUBLE PRECISION,
    realized_net_pnl DOUBLE PRECISION,
    total_commission_dollars DOUBLE PRECISION,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_trades_model_entry_ts_desc
    ON trades(model_id, entry_ts_ms DESC);
CREATE INDEX idx_trades_exit_oid
    ON trades(exit_oid);
