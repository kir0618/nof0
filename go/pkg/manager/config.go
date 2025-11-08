package manager

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"nof0-api/pkg/confkit"
)

// OrderStyle defines how the manager submits opening orders.
type OrderStyle string

const (
	OrderStyleLimitIOC  OrderStyle = "limit_ioc"
	OrderStyleMarketIOC OrderStyle = "market_ioc"

	defaultMarketIOCSlippageBps = 50.0 // 0.50% slippage
)

// Config defines the overall manager configuration schema.
type Config struct {
	Manager    ManagerConfig    `yaml:"manager"`
	Traders    []TraderConfig   `yaml:"traders"`
	Monitoring MonitoringConfig `yaml:"monitoring"`

	baseDir string
}

type ManagerConfig struct {
	TotalEquityUSD      float64       `yaml:"total_equity_usd"`
	ReserveEquityPct    float64       `yaml:"reserve_equity_pct"`
	AllocationStrategy  string        `yaml:"allocation_strategy"`
	RebalanceInterval   time.Duration `yaml:"-"`
	StateStorageBackend string        `yaml:"state_storage_backend"`
	StateStoragePath    string        `yaml:"state_storage_path"`

	RebalanceIntervalRaw string `yaml:"rebalance_interval"`
}

type TraderConfig struct {
	ID                   string         `yaml:"id"`
	Name                 string         `yaml:"name"`
	ExchangeProvider     string         `yaml:"exchange_provider"`
	MarketProvider       string         `yaml:"market_provider"`
	OrderStyle           OrderStyle     `yaml:"order_style"`
	MarketIOCSlippageBps float64        `yaml:"market_ioc_slippage_bps"`
	PromptTemplate       string         `yaml:"prompt_template"`
	ExecutorTemplate     string         `yaml:"executor_prompt_template"`
	Model                string         `yaml:"model"`
	DecisionInterval     time.Duration  `yaml:"-"`
	RiskParams           RiskParameters `yaml:"risk_params"`
	ExecGuards           ExecGuards     `yaml:"exec_guards"`
	AllocationPct        float64        `yaml:"allocation_pct"`
	AutoStart            bool           `yaml:"auto_start"`
	JournalEnabled       bool           `yaml:"journal_enabled"`
	JournalDir           string         `yaml:"journal_dir"`

	DecisionIntervalRaw string `yaml:"decision_interval"`
}

// ExecGuards defines optional hard guards applied at execution/validation time.
type ExecGuards struct {
	MaxNewPositionsPerCycle int     `yaml:"max_new_positions_per_cycle"`
	LiquidityThresholdUSD   float64 `yaml:"liquidity_threshold_usd"`
	MaxMarginUsagePct       float64 `yaml:"max_margin_usage_pct"`

	BTCETHMinEquityMultiple float64 `yaml:"btceth_position_value_min_equity_multiple"`
	BTCETHMaxEquityMultiple float64 `yaml:"btceth_position_value_max_equity_multiple"`
	AltMinEquityMultiple    float64 `yaml:"alt_position_value_min_equity_multiple"`
	AltMaxEquityMultiple    float64 `yaml:"alt_position_value_max_equity_multiple"`

	CooldownAfterClose    time.Duration `yaml:"-"`
	CooldownAfterCloseRaw string        `yaml:"cooldown_after_close"`
	// Feature toggles (default true if omitted)
	EnableLiquidityGuard   *bool `yaml:"enable_liquidity_guard"`
	EnableMarginUsageGuard *bool `yaml:"enable_margin_usage_guard"`
	EnableValueBandGuard   *bool `yaml:"enable_value_band_guard"`
	EnableCooldownGuard    *bool `yaml:"enable_cooldown_guard"`

	// Candidate selection
	CandidateLimit int `yaml:"candidate_limit"`

	// Performance gating
	SharpePauseThreshold     float64       `yaml:"sharpe_pause_threshold"`
	PauseDurationOnBreach    time.Duration `yaml:"-"`
	PauseDurationOnBreachRaw string        `yaml:"pause_duration_on_breach"`
}

type RiskParameters struct {
	MaxPositions       int     `yaml:"max_positions"`
	MaxPositionSizeUSD float64 `yaml:"max_position_size_usd"`
	MaxMarginUsagePct  float64 `yaml:"max_margin_usage_pct"`
	MajorCoinLeverage  int     `yaml:"major_coin_leverage"`
	AltcoinLeverage    int     `yaml:"altcoin_leverage"`
	MinRiskRewardRatio float64 `yaml:"min_risk_reward_ratio"`
	MinConfidence      int     `yaml:"min_confidence"`
	StopLossEnabled    bool    `yaml:"stop_loss_enabled"`
	TakeProfitEnabled  bool    `yaml:"take_profit_enabled"`
}

type MonitoringConfig struct {
	UpdateInterval  time.Duration `yaml:"-"`
	AlertWebhook    string        `yaml:"alert_webhook"`
	MetricsExporter string        `yaml:"metrics_exporter"`

	UpdateIntervalRaw string `yaml:"update_interval"`
}

// LoadConfig reads configuration from disk.
func LoadConfig(path string) (*Config, error) {
	confkit.LoadDotenvOnce()
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manager config: %w", err)
	}
	defer file.Close()
	return LoadConfigFromReader(file, filepath.Dir(path))
}

// MustLoad reads manager configuration from the default project location and panics on error.
func MustLoad() *Config {
	path := confkit.MustProjectPath("etc/manager.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

// LoadConfigFromReader constructs a Config from a reader with the provided base directory.
func LoadConfigFromReader(r io.Reader, baseDir string) (*Config, error) {
	confkit.LoadDotenvOnce()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read manager config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal manager config: %w", err)
	}
	cfg.baseDir = baseDir

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
	if strings.TrimSpace(c.Manager.RebalanceIntervalRaw) == "" {
		c.Manager.RebalanceIntervalRaw = "1h"
	}
	for i := range c.Traders {
		if strings.TrimSpace(c.Traders[i].DecisionIntervalRaw) == "" {
			c.Traders[i].DecisionIntervalRaw = "3m"
		}
		if strings.TrimSpace(c.Traders[i].ExecutorTemplate) == "" {
			c.Traders[i].ExecutorTemplate = "prompts/executor/default_prompt.tmpl"
		}
		c.Traders[i].Model = strings.TrimSpace(c.Traders[i].Model)
		if strings.TrimSpace(string(c.Traders[i].OrderStyle)) == "" {
			c.Traders[i].OrderStyle = OrderStyleLimitIOC
		}
		if c.Traders[i].MarketIOCSlippageBps <= 0 {
			c.Traders[i].MarketIOCSlippageBps = defaultMarketIOCSlippageBps
		}
	}
	if strings.TrimSpace(c.Monitoring.UpdateIntervalRaw) == "" {
		c.Monitoring.UpdateIntervalRaw = "30s"
	}
}

func (c *Config) parseDurations() error {
	var err error
	c.Manager.RebalanceInterval, err = parsePositiveDuration("manager.rebalance_interval", c.Manager.RebalanceIntervalRaw)
	if err != nil {
		return err
	}
	for i := range c.Traders {
		d, err := parsePositiveDuration(fmt.Sprintf("traders[%d].decision_interval", i), c.Traders[i].DecisionIntervalRaw)
		if err != nil {
			return err
		}
		c.Traders[i].DecisionInterval = d
		// ExecGuards cooldown is optional; parse if provided and non-empty.
		raw := strings.TrimSpace(c.Traders[i].ExecGuards.CooldownAfterCloseRaw)
		if raw != "" {
			cd, err := time.ParseDuration(raw)
			if err != nil || cd < 0 {
				return fmt.Errorf("manager config: traders[%d].exec_guards.cooldown_after_close invalid: %v", i, err)
			}
			c.Traders[i].ExecGuards.CooldownAfterClose = cd
		}
		rawPause := strings.TrimSpace(c.Traders[i].ExecGuards.PauseDurationOnBreachRaw)
		if rawPause != "" {
			pd, err := time.ParseDuration(rawPause)
			if err != nil || pd < 0 {
				return fmt.Errorf("manager config: traders[%d].exec_guards.pause_duration_on_breach invalid: %v", i, err)
			}
			c.Traders[i].ExecGuards.PauseDurationOnBreach = pd
		}
	}
	c.Monitoring.UpdateInterval, err = parsePositiveDuration("monitoring.update_interval", c.Monitoring.UpdateIntervalRaw)
	if err != nil {
		return err
	}
	return nil
}

func (c *Config) expandFields() {
	c.Manager.StateStoragePath = c.resolvePath(c.Manager.StateStoragePath)
	c.Manager.AllocationStrategy = strings.TrimSpace(c.Manager.AllocationStrategy)
	c.Manager.StateStorageBackend = strings.TrimSpace(c.Manager.StateStorageBackend)
	for i := range c.Traders {
		c.Traders[i].ID = strings.TrimSpace(c.Traders[i].ID)
		c.Traders[i].Name = strings.TrimSpace(c.Traders[i].Name)
		c.Traders[i].ExchangeProvider = strings.TrimSpace(c.Traders[i].ExchangeProvider)
		c.Traders[i].MarketProvider = strings.TrimSpace(c.Traders[i].MarketProvider)
		c.Traders[i].OrderStyle = OrderStyle(strings.ToLower(strings.TrimSpace(string(c.Traders[i].OrderStyle))))
		c.Traders[i].PromptTemplate = c.resolvePath(c.Traders[i].PromptTemplate)
		c.Traders[i].ExecutorTemplate = c.resolvePath(c.Traders[i].ExecutorTemplate)
		c.Traders[i].JournalDir = c.resolvePath(c.Traders[i].JournalDir)
	}
	c.Monitoring.AlertWebhook = strings.TrimSpace(os.ExpandEnv(c.Monitoring.AlertWebhook))
	c.Monitoring.MetricsExporter = strings.TrimSpace(c.Monitoring.MetricsExporter)
}

func (c *Config) resolvePath(path string) string {
	path = strings.TrimSpace(os.ExpandEnv(path))
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.baseDir, path)
}

// Validate ensures configuration sanity.
func (c *Config) Validate() error {
	if c.Manager.TotalEquityUSD < 0 {
		return errors.New("manager config: manager.total_equity_usd cannot be negative")
	}
	if c.Manager.ReserveEquityPct < 0 || c.Manager.ReserveEquityPct > 100 {
		return errors.New("manager config: manager.reserve_equity_pct must be between 0 and 100")
	}
	if strings.TrimSpace(c.Manager.StateStorageBackend) == "" {
		return errors.New("manager config: manager.state_storage_backend is required")
	}
	if strings.TrimSpace(c.Manager.StateStoragePath) == "" {
		return errors.New("manager config: manager.state_storage_path is required")
	}
	if len(c.Traders) == 0 {
		return errors.New("manager config: at least one trader must be defined")
	}

	idSeen := make(map[string]struct{}, len(c.Traders))
	totalAllocation := 0.0
	for i, trader := range c.Traders {
		if trader.ID == "" {
			return fmt.Errorf("manager config: traders[%d].id is required", i)
		}
		if _, ok := idSeen[trader.ID]; ok {
			return fmt.Errorf("manager config: duplicate trader id %q", trader.ID)
		}
		idSeen[trader.ID] = struct{}{}
		if trader.Name == "" {
			return fmt.Errorf("manager config: traders[%d].name is required", i)
		}
		if strings.TrimSpace(trader.ExchangeProvider) == "" {
			return fmt.Errorf("manager config: traders[%d].exchange_provider is required", i)
		}
		if strings.TrimSpace(trader.MarketProvider) == "" {
			return fmt.Errorf("manager config: traders[%d].market_provider is required", i)
		}
		if trader.PromptTemplate == "" {
			return fmt.Errorf("manager config: traders[%d].prompt_template is required", i)
		}
		if _, err := os.Stat(trader.PromptTemplate); err != nil {
			return fmt.Errorf("manager config: traders[%d].prompt_template %q not accessible: %w", i, trader.PromptTemplate, err)
		}
		if strings.TrimSpace(trader.ExecutorTemplate) == "" {
			return fmt.Errorf("manager config: traders[%d].executor_prompt_template is required", i)
		}
		if _, err := os.Stat(trader.ExecutorTemplate); err != nil {
			return fmt.Errorf("manager config: traders[%d].executor_prompt_template %q not accessible: %w", i, trader.ExecutorTemplate, err)
		}
		trader.Model = strings.TrimSpace(trader.Model)
		if trader.AllocationPct < 0 {
			return fmt.Errorf("manager config: traders[%d].allocation_pct cannot be negative", i)
		}
		totalAllocation += trader.AllocationPct
		if err := trader.RiskParams.Validate(i); err != nil {
			return err
		}
		if err := trader.validateOrderStyle(i); err != nil {
			return err
		}
		// ExecGuards validation (optional; non-negative checks)
		if trader.ExecGuards.MaxNewPositionsPerCycle < 0 {
			return fmt.Errorf("manager config: traders[%d].exec_guards.max_new_positions_per_cycle cannot be negative", i)
		}
		if trader.ExecGuards.LiquidityThresholdUSD < 0 {
			return fmt.Errorf("manager config: traders[%d].exec_guards.liquidity_threshold_usd cannot be negative", i)
		}
		if trader.ExecGuards.MaxMarginUsagePct < 0 || trader.ExecGuards.MaxMarginUsagePct > 100 {
			return fmt.Errorf("manager config: traders[%d].exec_guards.max_margin_usage_pct must be 0..100", i)
		}
	}
	if err := c.validateAllocationBudget(totalAllocation); err != nil {
		return err
	}

	if c.Monitoring.MetricsExporter == "" {
		return errors.New("manager config: monitoring.metrics_exporter is required")
	}
	return nil
}

func (c *Config) validateAllocationBudget(totalAllocation float64) error {
	if totalAllocation > 100+1e-6 {
		return fmt.Errorf("manager config: trader allocation sum %.2f exceeds 100", totalAllocation)
	}
	maxAllowed := 100 - c.Manager.ReserveEquityPct
	if totalAllocation > maxAllowed+1e-6 {
		return fmt.Errorf("manager config: trader allocation %.2f exceeds available equity after reserve %.2f", totalAllocation, maxAllowed)
	}
	return nil
}

func (t TraderConfig) validateOrderStyle(index int) error {
	switch t.OrderStyle {
	case OrderStyleLimitIOC, OrderStyleMarketIOC:
	default:
		return fmt.Errorf("manager config: traders[%d].order_style %q unsupported", index, t.OrderStyle)
	}
	if t.OrderStyle == OrderStyleMarketIOC && t.MarketIOCSlippageBps <= 0 {
		return fmt.Errorf("manager config: traders[%d].market_ioc_slippage_bps must be positive", index)
	}
	return nil
}

// Validate ensures risk parameters are within expected ranges.
func (r RiskParameters) Validate(index int) error {
	if r.MaxPositions <= 0 {
		return fmt.Errorf("manager config: traders[%d].risk_params.max_positions must be positive", index)
	}
	if r.MaxPositionSizeUSD <= 0 {
		return fmt.Errorf("manager config: traders[%d].risk_params.max_position_size_usd must be positive", index)
	}
	if r.MaxMarginUsagePct < 0 || r.MaxMarginUsagePct > 100 {
		return fmt.Errorf("manager config: traders[%d].risk_params.max_margin_usage_pct must be between 0 and 100", index)
	}
	if r.MajorCoinLeverage <= 0 {
		return fmt.Errorf("manager config: traders[%d].risk_params.major_coin_leverage must be positive", index)
	}
	if r.AltcoinLeverage <= 0 {
		return fmt.Errorf("manager config: traders[%d].risk_params.altcoin_leverage must be positive", index)
	}
	if r.MinRiskRewardRatio <= 0 {
		return fmt.Errorf("manager config: traders[%d].risk_params.min_risk_reward_ratio must be positive", index)
	}
	if r.MinConfidence < 0 || r.MinConfidence > 100 {
		return fmt.Errorf("manager config: traders[%d].risk_params.min_confidence must be between 0 and 100", index)
	}
	return nil
}

func parsePositiveDuration(field, value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("manager config: %s is required", field)
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("manager config: invalid %s %q: %w", field, value, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("manager config: %s must be positive, got %s", field, d)
	}
	return d, nil
}

// TraderIDs returns a stable ordered list of trader IDs.
func (c *Config) TraderIDs() []string {
	ids := make([]string, 0, len(c.Traders))
	for _, t := range c.Traders {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	return ids
}
