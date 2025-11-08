package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	t.Run("load from valid file", func(t *testing.T) {
		content := `
base_url: "https://api.example.com"
api_key: "test-api-key"
default_model: "gpt-4"
timeout: "30s"
max_retries: 3
log_level: "info"

models:
  gpt-4:
    provider: "openai"
    model_name: "gpt-4-turbo"
    temperature: 0.7
budget:
  daily_token_limit: 10000
  alert_threshold_pct: 75
  strict_enforcement: true
  cost_per_million_tokens:
    gpt-4: 30.5
`
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		err := os.WriteFile(configPath, []byte(content), 0644)
		require.NoError(t, err)

		cfg, err := LoadConfig(configPath)
		require.NoError(t, err)
		require.Equal(t, "https://api.example.com", cfg.BaseURL)
		require.Equal(t, "test-api-key", cfg.APIKey)
		require.Equal(t, "gpt-4", cfg.DefaultModel)
		require.Equal(t, 30*time.Second, cfg.Timeout)
		require.Equal(t, 3, cfg.MaxRetries)
		require.Equal(t, "info", cfg.LogLevel)
		require.NotNil(t, cfg.Budget)
		require.EqualValues(t, 10000, cfg.Budget.DailyTokenLimit)
		require.Equal(t, 75, cfg.Budget.AlertThresholdPct)
		require.True(t, cfg.Budget.StrictEnforcement)
		require.Equal(t, 30.5, cfg.Budget.CostPerMillionTokens["gpt-4"])
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadConfig("/nonexistent/path/config.yaml")
		require.Error(t, err)
		require.Contains(t, err.Error(), "open llm config")
	})

	t.Run("invalid yaml", func(t *testing.T) {
		content := `
base_url: "https://api.example.com"
api_key: test-api-key
  invalid: yaml: structure
`
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "invalid.yaml")
		err := os.WriteFile(configPath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = LoadConfig(configPath)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unmarshal llm config")
	})
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid config",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "test-key",
				DefaultModel: "gpt-4",
				Timeout:      30 * time.Second,
				MaxRetries:   3,
			},
			expectErr: false,
		},
		{
			name: "missing api key",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "",
				DefaultModel: "gpt-4",
				Timeout:      30 * time.Second,
				MaxRetries:   3,
			},
			expectErr: true,
			errMsg:    "api_key is required",
		},
		{
			name: "whitespace api key",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "   ",
				DefaultModel: "gpt-4",
				Timeout:      30 * time.Second,
				MaxRetries:   3,
			},
			expectErr: true,
			errMsg:    "api_key is required",
		},
		{
			name: "missing base url",
			cfg: &Config{
				BaseURL:      "",
				APIKey:       "test-key",
				DefaultModel: "gpt-4",
				Timeout:      30 * time.Second,
				MaxRetries:   3,
			},
			expectErr: true,
			errMsg:    "base_url is required",
		},
		{
			name: "missing default model",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "test-key",
				DefaultModel: "",
				Timeout:      30 * time.Second,
				MaxRetries:   3,
			},
			expectErr: true,
			errMsg:    "default_model is required",
		},
		{
			name: "zero timeout",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "test-key",
				DefaultModel: "gpt-4",
				Timeout:      0,
				MaxRetries:   3,
			},
			expectErr: true,
			errMsg:    "timeout must be positive",
		},
		{
			name: "negative timeout",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "test-key",
				DefaultModel: "gpt-4",
				Timeout:      -1 * time.Second,
				MaxRetries:   3,
			},
			expectErr: true,
			errMsg:    "timeout must be positive",
		},
		{
			name: "negative max retries",
			cfg: &Config{
				BaseURL:      "https://api.example.com",
				APIKey:       "test-key",
				DefaultModel: "gpt-4",
				Timeout:      30 * time.Second,
				MaxRetries:   -1,
			},
			expectErr: true,
			errMsg:    "max_retries cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	require.Equal(t, defaultBaseURL, cfg.BaseURL)
	require.Equal(t, defaultLogLevel, cfg.LogLevel)
	require.Equal(t, defaultMaxRetries, cfg.MaxRetries)
}

func TestConfigParseTimeout(t *testing.T) {
	tests := []struct {
		name        string
		timeoutRaw  string
		expected    time.Duration
		expectError bool
	}{
		{
			name:       "valid duration",
			timeoutRaw: "30s",
			expected:   30 * time.Second,
		},
		{
			name:       "valid duration with minutes",
			timeoutRaw: "2m",
			expected:   2 * time.Minute,
		},
		{
			name:       "empty timeout uses default",
			timeoutRaw: "",
			expected:   defaultTimeout,
		},
		{
			name:       "whitespace timeout uses default",
			timeoutRaw: "   ",
			expected:   defaultTimeout,
		},
		{
			name:        "invalid duration format",
			timeoutRaw:  "invalid",
			expectError: true,
		},
		{
			name:        "zero duration",
			timeoutRaw:  "0s",
			expectError: true,
		},
		{
			name:        "negative duration",
			timeoutRaw:  "-10s",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{timeoutRaw: tt.timeoutRaw}
			err := cfg.parseTimeout()

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, cfg.Timeout)
			}
		})
	}
}

func TestBudgetConfigValidate(t *testing.T) {
	valid := &BudgetConfig{
		DailyTokenLimit:   1000,
		AlertThresholdPct: 50,
		CostPerMillionTokens: map[string]float64{
			"gpt-4": 25.5,
		},
	}
	require.NoError(t, valid.Validate())

	invalidLimit := &BudgetConfig{}
	require.Error(t, invalidLimit.Validate())

	invalidThreshold := &BudgetConfig{DailyTokenLimit: 100, AlertThresholdPct: 150}
	require.Error(t, invalidThreshold.Validate())

	invalidCost := &BudgetConfig{DailyTokenLimit: 100, AlertThresholdPct: 50, CostPerMillionTokens: map[string]float64{"gpt-4": -1}}
	require.Error(t, invalidCost.Validate())

	cfg := &BudgetConfig{DailyTokenLimit: 100, AlertThresholdPct: 0}
	cfg.applyDefaults()
	require.Equal(t, 80, cfg.AlertThresholdPct)
}

func TestConfigModel(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"gpt-4": {
				Provider:  "openai",
				ModelName: "gpt-4-turbo",
			},
		},
	}

	t.Run("existing model", func(t *testing.T) {
		model, ok := cfg.Model("gpt-4")
		require.True(t, ok)
		require.Equal(t, "openai", model.Provider)
		require.Equal(t, "gpt-4-turbo", model.ModelName)
	})

	t.Run("non-existing model", func(t *testing.T) {
		_, ok := cfg.Model("non-existent")
		require.False(t, ok)
	})

	t.Run("nil models map", func(t *testing.T) {
		cfg := &Config{Models: nil}
		_, ok := cfg.Model("gpt-4")
		require.False(t, ok)
	})
}

func TestConfigClone(t *testing.T) {
	temp := 0.7
	maxCompletionTokens := 1024

	original := &Config{
		BaseURL:      "https://api.example.com",
		APIKey:       "test-key",
		DefaultModel: "gpt-4",
		Timeout:      30 * time.Second,
		MaxRetries:   3,
		LogLevel:     "info",
		Models: map[string]ModelConfig{
			"gpt-4": {
				Provider:            "openai",
				ModelName:           "gpt-4-turbo",
				Temperature:         &temp,
				MaxCompletionTokens: &maxCompletionTokens,
			},
		},
		timeoutRaw: "30s",
	}

	cloned := original.Clone()
	require.NotNil(t, cloned)
	require.Equal(t, original.BaseURL, cloned.BaseURL)
	require.Equal(t, original.APIKey, cloned.APIKey)
	require.Equal(t, original.DefaultModel, cloned.DefaultModel)
	require.Equal(t, original.Timeout, cloned.Timeout)
	require.Equal(t, original.MaxRetries, cloned.MaxRetries)
	require.Equal(t, original.LogLevel, cloned.LogLevel)
	require.Equal(t, original.timeoutRaw, cloned.timeoutRaw)

	// Verify deep copy of models map
	require.NotNil(t, cloned.Models)
	require.Equal(t, len(original.Models), len(cloned.Models))
	model, ok := cloned.Model("gpt-4")
	require.True(t, ok)
	require.Equal(t, "openai", model.Provider)
	require.NotNil(t, model.MaxCompletionTokens)
	require.Equal(t, maxCompletionTokens, *model.MaxCompletionTokens)

	// Modify cloned models map to ensure it's a separate copy
	cloned.Models["gpt-5"] = ModelConfig{Provider: "openai"}
	_, ok = original.Model("gpt-5")
	require.False(t, ok, "original should not be affected by changes to clone")
}

func TestConfigCloneNil(t *testing.T) {
	var cfg *Config
	cloned := cfg.Clone()
	require.Nil(t, cloned)
}

func TestLoadConfigFromReaderWithEnvExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "expanded-key")
	t.Setenv("TEST_BASE_URL", "https://expanded.com")

	data := `
base_url: "${TEST_BASE_URL}"
api_key: "${TEST_API_KEY}"
default_model: "gpt-4"
timeout: "30s"
`

	cfg, err := LoadConfigFromReader(strings.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, "https://expanded.com", cfg.BaseURL)
	require.Equal(t, "expanded-key", cfg.APIKey)
}
