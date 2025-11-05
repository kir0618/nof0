-- Rollback index optimizations

DROP INDEX IF EXISTS idx_positions_status_model;
DROP INDEX IF EXISTS idx_positions_open_model_symbol;

-- Restore original index
CREATE INDEX idx_positions_model_exchange_open
ON positions(model_id, exchange_provider)
WHERE status = 'open';
