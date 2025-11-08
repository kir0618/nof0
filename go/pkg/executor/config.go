package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"nof0-api/pkg/confkit"
)

// Config controls runtime behaviour for the executor module.
type Config struct {
	MajorCoinLeverage      int                 `yaml:"major_coin_leverage"`
	AltcoinLeverage        int                 `yaml:"altcoin_leverage"`
	MinConfidence          int                 `yaml:"min_confidence"`
	MinRiskReward          float64             `yaml:"min_risk_reward"`
	MaxPositions           int                 `yaml:"max_positions"`
	DecisionInterval       time.Duration       `yaml:"-"`
	DecisionTimeout        time.Duration       `yaml:"-"`
	MaxConcurrentDecisions int                 `yaml:"max_concurrent_decisions"`
	AllowedTraderIDs       []string            `yaml:"allowed_trader_ids"`
	SigningKey             string              `yaml:"signing_key"`
	Overrides              map[string]Override `yaml:"overrides"`
	PromptSchemaVersion    string              `yaml:"prompt_schema_version"`
	PromptValidation       PromptValidation    `yaml:"prompt_validation"`
	TraderID               string              `yaml:"-"` // runtime-only metadata for persistence hooks

	DecisionIntervalRaw string `yaml:"decision_interval"`
	DecisionTimeoutRaw  string `yaml:"decision_timeout"`
	minRiskRewardSet    bool
}

// PromptValidation encapsulates prompt schema validation toggles.
type PromptValidation struct {
	StrictMode           bool `yaml:"strict_mode"`
	RequireVersionHeader bool `yaml:"require_version_header"`
}

// Override allows per-trader or per-symbol overrides of core thresholds.
type Override struct {
	MajorCoinLeverage *int     `yaml:"major_coin_leverage,omitempty"`
	AltcoinLeverage   *int     `yaml:"altcoin_leverage,omitempty"`
	MinConfidence     *int     `yaml:"min_confidence,omitempty"`
	MinRiskReward     *float64 `yaml:"min_risk_reward,omitempty"`
	MaxPositions      *int     `yaml:"max_positions,omitempty"`
}

// LoadConfig reads configuration from disk.
func LoadConfig(path string) (*Config, error) {
	confkit.LoadDotenvOnce()
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open executor config: %w", err)
	}
	defer file.Close()
	return LoadConfigFromReader(file)
}

// MustLoad reads executor configuration from the default project location and panics on error.
func MustLoad() *Config {
	path := confkit.MustProjectPath("etc/executor.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

// LoadConfigFromReader constructs a Config from a reader.
func LoadConfigFromReader(r io.Reader) (*Config, error) {
	confkit.LoadDotenvOnce()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read executor config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal executor config: %w", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err == nil {
		if _, ok := raw["min_risk_reward"]; ok {
			cfg.minRiskRewardSet = true
		}
	}
	cfg.applyDefaults()
	if err := cfg.parseDurations(); err != nil {
		return nil, err
	}
	cfg.expandFields()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.DecisionIntervalRaw) == "" {
		c.DecisionIntervalRaw = "3m"
	}
	if strings.TrimSpace(c.DecisionTimeoutRaw) == "" {
		c.DecisionTimeoutRaw = "60s"
	}
	if c.MaxConcurrentDecisions <= 0 {
		c.MaxConcurrentDecisions = 1
	}
	if !c.minRiskRewardSet && c.MinRiskReward <= 0 {
		c.MinRiskReward = 3.0
	}
	c.PromptSchemaVersion = strings.TrimSpace(c.PromptSchemaVersion)
}

func (c *Config) parseDurations() error {
	interval, err := time.ParseDuration(c.DecisionIntervalRaw)
	if err != nil {
		return fmt.Errorf("executor config: invalid decision_interval %q: %w", c.DecisionIntervalRaw, err)
	}
	if interval <= 0 {
		return fmt.Errorf("executor config: decision_interval must be positive, got %s", interval)
	}
	timeout, err := time.ParseDuration(c.DecisionTimeoutRaw)
	if err != nil {
		return fmt.Errorf("executor config: invalid decision_timeout %q: %w", c.DecisionTimeoutRaw, err)
	}
	if timeout <= 0 {
		return fmt.Errorf("executor config: decision_timeout must be positive, got %s", timeout)
	}
	c.DecisionInterval = interval
	c.DecisionTimeout = timeout
	return nil
}

func (c *Config) expandFields() {
	c.SigningKey = strings.TrimSpace(os.ExpandEnv(c.SigningKey))
	if c.PromptSchemaVersion != "" {
		c.PromptSchemaVersion = strings.TrimSpace(c.PromptSchemaVersion)
	}
	for i, id := range c.AllowedTraderIDs {
		c.AllowedTraderIDs[i] = strings.TrimSpace(id)
	}
}

// Validate ensures configuration sanity.
func (c *Config) Validate() error {
	if c.MajorCoinLeverage <= 0 {
		return errors.New("executor config: major_coin_leverage must be positive")
	}
	if c.AltcoinLeverage <= 0 {
		return errors.New("executor config: altcoin_leverage must be positive")
	}
	if c.MinConfidence < 0 || c.MinConfidence > 100 {
		return errors.New("executor config: min_confidence must be between 0 and 100")
	}
	if c.MinRiskReward <= 0 {
		return errors.New("executor config: min_risk_reward must be positive")
	}
	if c.MaxPositions <= 0 {
		return errors.New("executor config: max_positions must be positive")
	}
	if c.PromptValidation.RequireVersionHeader && strings.TrimSpace(c.PromptSchemaVersion) == "" {
		return errors.New("executor config: prompt_schema_version is required when prompt_validation.require_version_header is true")
	}
	if len(c.AllowedTraderIDs) > 0 {
		seen := make(map[string]struct{}, len(c.AllowedTraderIDs))
		for _, id := range c.AllowedTraderIDs {
			if id == "" {
				return errors.New("executor config: allowed_trader_ids contains empty value")
			}
			if _, ok := seen[id]; ok {
				return fmt.Errorf("executor config: allowed_trader_ids contains duplicate %q", id)
			}
			seen[id] = struct{}{}
		}
	}
	for key, override := range c.Overrides {
		if strings.TrimSpace(key) == "" {
			return errors.New("executor config: overrides cannot contain empty keys")
		}
		if override.MajorCoinLeverage != nil && *override.MajorCoinLeverage <= 0 {
			return fmt.Errorf("executor config: override %s major_coin_leverage must be positive", key)
		}
		if override.AltcoinLeverage != nil && *override.AltcoinLeverage <= 0 {
			return fmt.Errorf("executor config: override %s altcoin_leverage must be positive", key)
		}
		if override.MinConfidence != nil {
			if *override.MinConfidence < 0 || *override.MinConfidence > 100 {
				return fmt.Errorf("executor config: override %s min_confidence must be between 0 and 100", key)
			}
		}
		if override.MinRiskReward != nil && *override.MinRiskReward <= 0 {
			return fmt.Errorf("executor config: override %s min_risk_reward must be positive", key)
		}
		if override.MaxPositions != nil && *override.MaxPositions <= 0 {
			return fmt.Errorf("executor config: override %s max_positions must be positive", key)
		}
	}
	return nil
}
