package market

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"nof0-api/pkg/confkit"
)

// Config describes the set of market data providers available to the application.
type Config struct {
	Default   string                     `yaml:"default"`
	Providers map[string]*ProviderConfig `yaml:"providers"`
}

const (
	// DefaultProviderTimeout defines the per-call timeout applied when none is supplied in configuration.
	DefaultProviderTimeout = 8 * time.Second
	// DefaultProviderHTTPTimeout defines the HTTP client timeout used when none is supplied in configuration.
	DefaultProviderHTTPTimeout = 10 * time.Second
)

// ProviderConfig represents configuration for a single market provider.
type ProviderConfig struct {
	Type    string `yaml:"type"`
	Testnet bool   `yaml:"testnet"`
	Mode    string `yaml:"mode"`

	TimeoutRaw     string        `yaml:"timeout"`
	Timeout        time.Duration `yaml:"-"`
	HTTPTimeoutRaw string        `yaml:"http_timeout"`
	HTTPTimeout    time.Duration `yaml:"-"`
	MaxRetries     int           `yaml:"max_retries"`
}

// ProviderBuilder constructs a Provider from configuration.
type ProviderBuilder func(name string, cfg *ProviderConfig) (Provider, error)

var (
	providerRegistry   = make(map[string]ProviderBuilder)
	providerRegistryMu sync.RWMutex
)

// RegisterProvider registers a market provider constructor.
func RegisterProvider(typeName string, builder ProviderBuilder) {
	providerRegistryMu.Lock()
	defer providerRegistryMu.Unlock()
	providerRegistry[strings.ToLower(strings.TrimSpace(typeName))] = builder
}

func lookupProviderBuilder(typeName string) (ProviderBuilder, bool) {
	providerRegistryMu.RLock()
	defer providerRegistryMu.RUnlock()
	builder, ok := providerRegistry[strings.ToLower(strings.TrimSpace(typeName))]
	return builder, ok
}

// LoadConfig reads configuration from disk.
func LoadConfig(path string) (*Config, error) {
	confkit.LoadDotenvOnce()
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open market config: %w", err)
	}
	defer file.Close()
	return LoadConfigFromReader(file)
}

// MustLoad reads market configuration from the default project location and panics on error.
func MustLoad() *Config {
	path := confkit.MustProjectPath("etc/market.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

// LoadConfigFromReader constructs a Config from an io.Reader.
func LoadConfigFromReader(r io.Reader) (*Config, error) {
	confkit.LoadDotenvOnce()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read market config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal market config: %w", err)
	}
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) normalise() error {
	if c.Providers == nil {
		c.Providers = make(map[string]*ProviderConfig)
	}
	for name, provider := range c.Providers {
		if provider == nil {
			provider = &ProviderConfig{}
			c.Providers[name] = provider
		}
		provider.expandEnv()
		if err := provider.parseDurations(name); err != nil {
			return err
		}
	}
	return nil
}

func (p *ProviderConfig) expandEnv() {
	p.Type = strings.TrimSpace(os.ExpandEnv(p.Type))
	p.Mode = strings.TrimSpace(os.ExpandEnv(p.Mode))
	p.TimeoutRaw = strings.TrimSpace(os.ExpandEnv(p.TimeoutRaw))
	p.HTTPTimeoutRaw = strings.TrimSpace(os.ExpandEnv(p.HTTPTimeoutRaw))
}

func (p *ProviderConfig) parseDurations(name string) error {
	if p.TimeoutRaw != "" {
		d, err := time.ParseDuration(p.TimeoutRaw)
		if err != nil {
			return fmt.Errorf("market provider %s: invalid timeout %q: %w", name, p.TimeoutRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("market provider %s: timeout must be positive, got %s", name, d)
		}
		p.Timeout = d
	}
	if p.Timeout <= 0 {
		p.Timeout = DefaultProviderTimeout
	}
	if p.HTTPTimeoutRaw != "" {
		d, err := time.ParseDuration(p.HTTPTimeoutRaw)
		if err != nil {
			return fmt.Errorf("market provider %s: invalid http_timeout %q: %w", name, p.HTTPTimeoutRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("market provider %s: http_timeout must be positive, got %s", name, d)
		}
		p.HTTPTimeout = d
	}
	if p.HTTPTimeout <= 0 {
		p.HTTPTimeout = DefaultProviderHTTPTimeout
	}
	return nil
}

// Validate ensures the configuration is structurally sound.
func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("market config: providers cannot be empty")
	}
	if c.Default != "" {
		if _, ok := c.Providers[c.Default]; !ok {
			return fmt.Errorf("market config: default provider %q not defined", c.Default)
		}
	}
	for name, provider := range c.Providers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("market config: provider name cannot be empty")
		}
		if err := provider.validate(name); err != nil {
			return err
		}
	}
	return nil
}

func (p *ProviderConfig) validate(name string) error {
	if p == nil {
		return fmt.Errorf("market config: provider %s is nil", name)
	}
	if strings.TrimSpace(p.Type) == "" {
		return fmt.Errorf("market config: provider %s must specify type", name)
	}
	if _, ok := lookupProviderBuilder(p.Type); !ok {
		return fmt.Errorf("market config: provider %s has unsupported type %q", name, p.Type)
	}
	return nil
}

// BuildProviders instantiates market data providers according to configuration.
func (c *Config) BuildProviders() (map[string]Provider, error) {
	result := make(map[string]Provider, len(c.Providers))
	for name, providerCfg := range c.Providers {
		builder, ok := lookupProviderBuilder(providerCfg.Type)
		if !ok {
			return nil, fmt.Errorf("market provider %s: unsupported type %q", name, providerCfg.Type)
		}
		provider, err := builder(name, providerCfg)
		if err != nil {
			return nil, fmt.Errorf("market provider %s: %w", name, err)
		}
		result[name] = provider
	}
	return result, nil
}
