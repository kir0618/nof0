package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "decision_schema.json")
	assert.NoError(t, os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o600))

	configYAML := `
major_coin_leverage: 20
altcoin_leverage: 10
min_confidence: 75
min_risk_reward: 3.5
max_positions: 4
decision_interval: 2m
decision_timeout: 45s
max_concurrent_decisions: 2
allowed_trader_ids:
  - trader_alpha
  - trader_beta
signing_key: ${EXEC_SIGNING_KEY}
overrides:
  trader_alpha:
    min_confidence: 80
output_validation:
  enabled: true
  schema_path: decision_schema.json
  fail_on_invalid: true
`
	path := filepath.Join(dir, "executor.yaml")
	err := os.WriteFile(path, []byte(configYAML), 0o600)
	assert.NoError(t, err, "should write config file successfully")

	t.Setenv("EXEC_SIGNING_KEY", " secret ")
	cfg, err := LoadConfig(path)
	assert.NoError(t, err, "LoadConfig should not error")
	assert.NotNil(t, cfg, "config should not be nil")

	assert.Equal(t, "2m0s", cfg.DecisionInterval.String(), "DecisionInterval should be parsed correctly")
	assert.Equal(t, "45s", cfg.DecisionTimeout.String(), "DecisionTimeout should be parsed correctly")
	assert.Equal(t, 2, cfg.MaxConcurrentDecisions, "MaxConcurrentDecisions should be 2")
	assert.Equal(t, "secret", cfg.SigningKey, "SigningKey should be trimmed and expanded")

	assert.NotNil(t, cfg.Overrides["trader_alpha"].MinConfidence, "Override MinConfidence should not be nil")
	assert.Equal(t, 80, *cfg.Overrides["trader_alpha"].MinConfidence, "Override MinConfidence should be 80")

	expectedIDs := []string{"trader_alpha", "trader_beta"}
	assert.Equal(t, expectedIDs, cfg.AllowedTraderIDs, "AllowedTraderIDs should match expected list")
	assert.True(t, cfg.OutputValidation.Enabled)
	assert.True(t, cfg.OutputValidation.FailOnInvalid)
	assert.Equal(t, schemaPath, cfg.OutputValidation.SchemaPath)
}

func TestLoadConfigInvalidMinRiskReward(t *testing.T) {
	dir := t.TempDir()
	configYAML := `
major_coin_leverage: 20
altcoin_leverage: 10
min_confidence: 75
min_risk_reward: -1
max_positions: 4
`
	path := filepath.Join(dir, "executor.yaml")
	err := os.WriteFile(path, []byte(configYAML), 0o600)
	assert.NoError(t, err, "should write config file successfully")

	_, err = LoadConfig(path)
	assert.Error(t, err, "LoadConfig should error for invalid min_risk_reward")
	assert.Contains(t, err.Error(), "min_risk_reward", "error should mention min_risk_reward")
}

func TestLoadConfigDuplicateTraderIDs(t *testing.T) {
	dir := t.TempDir()
	configYAML := `
major_coin_leverage: 20
altcoin_leverage: 10
min_confidence: 75
min_risk_reward: 3.5
max_positions: 4
allowed_trader_ids:
  - trader_alpha
  - trader_alpha
`
	path := filepath.Join(dir, "executor.yaml")
	err := os.WriteFile(path, []byte(configYAML), 0o600)
	assert.NoError(t, err, "should write config file successfully")

	_, err = LoadConfig(path)
	assert.Error(t, err, "LoadConfig should error for duplicate trader IDs")
	assert.Contains(t, err.Error(), "duplicate", "error should mention duplicate")
}

func TestValidateFails(t *testing.T) {
	dir := t.TempDir()
	configYAML := `
major_coin_leverage: 0
altcoin_leverage: -1
min_confidence: 150
min_risk_reward: -2
max_positions: 0
decision_interval: 1s
decision_timeout: 1s
`
	path := filepath.Join(dir, "bad.yaml")
	err := os.WriteFile(path, []byte(configYAML), 0o600)
	assert.NoError(t, err, "should write config file successfully")

	_, err = LoadConfig(path)
	assert.Error(t, err, "LoadConfig should error for invalid config")
	assert.Contains(t, err.Error(), "major_coin_leverage", "error should mention major_coin_leverage")
}
