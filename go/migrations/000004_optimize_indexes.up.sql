-- Optimize indexes for better query performance

-- Drop suboptimal index on positions
DROP INDEX IF EXISTS idx_positions_model_exchange_open;

-- Create optimized index matching actual query pattern
-- Query: WHERE status = 'open' AND model_id = ANY([...]) ORDER BY model_id, symbol
CREATE INDEX idx_positions_open_model_symbol
ON positions(model_id, symbol)
WHERE status = 'open';

-- Add composite index for status + model_id filtering
CREATE INDEX idx_positions_status_model
ON positions(status, model_id);
