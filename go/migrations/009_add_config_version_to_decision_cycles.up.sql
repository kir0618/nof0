ALTER TABLE decision_cycles
    ADD COLUMN IF NOT EXISTS config_version INT NOT NULL DEFAULT 1;
