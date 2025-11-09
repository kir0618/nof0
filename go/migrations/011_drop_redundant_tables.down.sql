CREATE TABLE IF NOT EXISTS price_latest (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    price DOUBLE PRECISION NOT NULL,
    ts_ms BIGINT NOT NULL,
    raw JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, symbol)
);

CREATE TABLE IF NOT EXISTS market_asset_ctx (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    symbol TEXT NOT NULL,
    funding DOUBLE PRECISION,
    open_interest DOUBLE PRECISION,
    oracle_px DOUBLE PRECISION,
    mark_px DOUBLE PRECISION,
    mid_px DOUBLE PRECISION,
    impact_pxs JSONB,
    prev_day_px DOUBLE PRECISION,
    day_ntl_vlm DOUBLE PRECISION,
    day_base_vlm DOUBLE PRECISION,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, symbol)
);

CREATE TABLE IF NOT EXISTS model_analytics (
    model_id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    server_time_ms BIGINT NOT NULL,
    metadata JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
