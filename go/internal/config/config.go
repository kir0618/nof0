package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/rest"

	"nof0-api/pkg/confkit"
	exchangepkg "nof0-api/pkg/exchange"
	executorpkg "nof0-api/pkg/executor"
	llmpkg "nof0-api/pkg/llm"
	managerpkg "nof0-api/pkg/manager"
	marketpkg "nof0-api/pkg/market"
)

type CacheTTL struct {
	Short  int `json:",default=10"` // seconds
	Medium int `json:",default=60"`
	Long   int `json:",default=300"`
}

// PostgresConf mirrors goctl style database settings while allowing pool tuning.
type PostgresConf struct {
	DataSource  string        `json:",optional"`
	MaxOpen     int           `json:",default=10"`
	MaxIdle     int           `json:",default=5"`
	MaxLifetime time.Duration `json:",default=5m"`
}

// LoggingConf configures logging behavior and thresholds
type LoggingConf struct {
	SlowThreshold SlowThresholdConf `json:",optional"`
	VerboseSQL    bool              `json:",default=false"`
	VerboseLLM    bool              `json:",default=false"`
}

// SlowThresholdConf defines millisecond thresholds for slow operation logging
type SlowThresholdConf struct {
	SQL   int `json:",default=1000"` // milliseconds
	Redis int `json:",default=500"`  // milliseconds
}

type Config struct {
	rest.RestConf
	// Env indicates the running environment: test | dev | prod
	// Defaults to test. In test mode we prefer low-cost LLM routing.
	Env      string          `json:",default=test"`
	DataPath string          `json:",default=../../mcp/data"`
	Postgres PostgresConf    `json:",optional"`
	Cache    cache.CacheConf `json:",optional"`
	TTL      CacheTTL        `json:",optional"`
	Logging  LoggingConf     `json:",optional"`

	LLM      confkit.Section[llmpkg.Config]      `json:",optional"`
	Executor confkit.Section[executorpkg.Config] `json:",optional"`
	Manager  confkit.Section[managerpkg.Config]  `json:",optional"`
	Exchange confkit.Section[exchangepkg.Config] `json:",optional"`
	Market   confkit.Section[marketpkg.Config]   `json:",optional"`

	mainPath string
	baseDir  string
}

const defaultConfigRelativePath = "etc/nof0.yaml"

var (
	configFileFlag = flag.String("f", defaultConfigRelativePath, "the config file")
)

func init() {
	confkit.LoadDotenvOnce()
}

func ConfigFile() string {
	candidate := defaultConfigRelativePath
	if configFileFlag != nil {
		if trimmed := strings.TrimSpace(*configFileFlag); trimmed != "" {
			candidate = trimmed
		}
	}

	if resolved, ok := resolveConfigPath(candidate); ok {
		return resolved
	}
	return candidate
}

func OverrideConfigFile(path string) (restore func()) {
	prev := ConfigFile()
	if configFileFlag != nil {
		*configFileFlag = path
	}
	return func() {
		if configFileFlag != nil {
			*configFileFlag = prev
		}
	}
}

func (c *Config) IsTestEnv() bool {
	return c.Env == "test" || c.Env == ""
}

func resolveConfigPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if filepath.IsAbs(path) {
		if fileExists(path) {
			return path, true
		}
		return "", false
	}

	startDirs := make([]string, 0, 3)
	if cwd, err := os.Getwd(); err == nil {
		startDirs = append(startDirs, cwd)
	}
	if exePath, err := os.Executable(); err == nil {
		startDirs = append(startDirs, filepath.Dir(exePath))
	}

	seen := make(map[string]struct{}, len(startDirs))
	for _, dir := range startDirs {
		dir = filepath.Clean(dir)
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		if resolved, ok := searchUpwards(dir, path); ok {
			return resolved, true
		}
	}

	return "", false
}

func searchUpwards(start, rel string) (string, bool) {
	dir := filepath.Clean(start)
	for {
		candidate := filepath.Join(dir, rel)
		if fileExists(candidate) {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func MustLoad() *Config {
	path := ConfigFile()
	cfg, err := Load(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

func Load(path string) (*Config, error) {
	confkit.LoadDotenvOnce()

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %s: %w", path, err)
	}

	var cfg Config
	if err := conf.Load(absPath, &cfg, conf.UseEnv()); err != nil {
		return nil, fmt.Errorf("load config %s: %w", absPath, err)
	}

	cfg.mainPath = absPath
	cfg.baseDir = filepath.Dir(absPath)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.hydrateSections(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.Env)) {
	case "", "test", "dev", "prod":
		if strings.TrimSpace(c.Env) == "" {
			c.Env = "test"
		}
	default:
		return errors.New("config: env must be one of test|dev|prod")
	}
	if strings.TrimSpace(c.DataPath) == "" {
		return errors.New("config: dataPath is required")
	}
	return c.validateTTL()
}

func (c *Config) validateTTL() error {
	if c.TTL.Short <= 0 {
		return errors.New("config: ttl.short must be positive")
	}
	if c.TTL.Medium <= 0 {
		return errors.New("config: ttl.medium must be positive")
	}
	if c.TTL.Long <= 0 {
		return errors.New("config: ttl.long must be positive")
	}
	return nil
}

func (c *Config) hydrateSections() error {
	base := c.baseDir

	if err := c.LLM.Hydrate(base, llmpkg.LoadConfig); err != nil {
		return fmt.Errorf("load llm config: %w", err)
	}
	if err := c.Executor.Hydrate(base, executorpkg.LoadConfig); err != nil {
		return fmt.Errorf("load executor config: %w", err)
	}
	if err := c.Manager.Hydrate(base, managerpkg.LoadConfig); err != nil {
		return fmt.Errorf("load manager config: %w", err)
	}
	if err := c.Exchange.Hydrate(base, exchangepkg.LoadConfig); err != nil {
		return fmt.Errorf("load exchange config: %w", err)
	}
	if err := c.Market.Hydrate(base, marketpkg.LoadConfig); err != nil {
		return fmt.Errorf("load market config: %w", err)
	}

	return nil
}

func (c *Config) MainPath() string {
	return c.mainPath
}

func (c *Config) BaseDir() string {
	return c.baseDir
}
