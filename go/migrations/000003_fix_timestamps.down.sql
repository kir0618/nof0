-- Rollback timestamp columns added in 003

ALTER TABLE trader_state DROP COLUMN created_at;
ALTER TABLE market_asset_ctx DROP COLUMN created_at;
ALTER TABLE price_latest DROP COLUMN created_at;
ALTER TABLE market_assets DROP COLUMN created_at;
