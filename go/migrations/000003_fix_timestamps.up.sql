-- Add missing created_at columns to snapshot tables for consistency with application code

ALTER TABLE market_assets
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE price_latest
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE market_asset_ctx
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE trader_state
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Set created_at to match updated_at for existing rows
UPDATE market_assets SET created_at = updated_at WHERE created_at IS NULL;
UPDATE price_latest SET created_at = updated_at WHERE created_at IS NULL;
UPDATE market_asset_ctx SET created_at = updated_at WHERE created_at IS NULL;
UPDATE trader_state SET created_at = updated_at WHERE created_at IS NULL;
