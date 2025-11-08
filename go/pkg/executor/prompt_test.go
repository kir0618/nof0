package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPromptRenderer(t *testing.T) {
	templatePath := filepath.Join("..", "..", "etc", "prompts", "executor", "default_prompt.tmpl")
	cfg := &Config{
		MajorCoinLeverage:      20,
		AltcoinLeverage:        8,
		MinConfidence:          75,
		MinRiskReward:          3.2,
		MaxPositions:           3,
		DecisionIntervalRaw:    "3m",
		DecisionTimeoutRaw:     "60s",
		MaxConcurrentDecisions: 1,
		PromptSchemaVersion:    "v1.0.0",
		PromptValidation: PromptValidation{
			StrictMode:           true,
			RequireVersionHeader: true,
		},
	}
	renderer, err := NewPromptRenderer(cfg, templatePath)
	assert.NoError(t, err, "NewPromptRenderer should not error")
	assert.NotNil(t, renderer, "renderer should not be nil")

	out, err := renderer.Render(PromptInputs{
		CurrentTime:     "2025-11-01T08:00:00Z",
		RuntimeMinutes:  120,
		SharpeRatio:     1.45,
		AccountOverview: "Equity: $12000\nBalance: $11800",
		OpenPositions:   "- BTC short 0.1 @ 65000",
		RiskBudget:      "Available risk: $250 (25% of cap)",
		PerformanceView: "WinRate: 60%",
		CandidateCoins:  "- BTC\n- ETH\n- SOL",
		MarketSnapshots: `{"BTC":{"price":64000}}`,
	})
	assert.NoError(t, err, "Render should not error")
	assert.NotEmpty(t, out, "rendered output should not be empty")

	expectations := []string{
		"TIMESTAMP: 2025-11-01T08:00:00Z",
		"UPTIME_MINUTES: 120",
		"ROLLING_SHARPE: 1.45",
		"BTC/ETH default 20x",
		"Minimum reward-to-risk ratio: 3.20",
		"RISK_BUDGET:",
		"Respect per-trader limits defined by Manager",
		"Available risk: $250",
		`"BTC":{"price":64000}`,
		"minimum confidence 75",
	}
	for _, substr := range expectations {
		assert.Contains(t, out, substr, "rendered prompt should contain %q", substr)
	}
}

func TestPromptRendererNilConfig(t *testing.T) {
	_, err := NewPromptRenderer(nil, "")
	assert.Error(t, err, "NewPromptRenderer should error for nil config")
}

func TestPromptRendererEmptyPath(t *testing.T) {
	cfg := &Config{
		MajorCoinLeverage:      20,
		AltcoinLeverage:        8,
		MinConfidence:          75,
		MinRiskReward:          3.2,
		MaxPositions:           3,
		DecisionIntervalRaw:    "3m",
		DecisionTimeoutRaw:     "60s",
		MaxConcurrentDecisions: 1,
	}
	_, err := NewPromptRenderer(cfg, " ")
	assert.Error(t, err, "NewPromptRenderer should error for empty template path")
}

func TestPromptRendererVersionMismatchStrict(t *testing.T) {
	path := writeTempTemplate(t, "{{/* Version: v0.9.0 */}}\nbody")
	cfg := &Config{
		PromptSchemaVersion: "v1.0.0",
		PromptValidation: PromptValidation{
			StrictMode:           true,
			RequireVersionHeader: true,
		},
	}
	_, err := NewPromptRenderer(cfg, path)
	assert.ErrorContains(t, err, "declared version v0.9.0")
}

func TestPromptRendererVersionMismatchNonStrict(t *testing.T) {
	path := writeTempTemplate(t, "{{/* Version: v0.9.0 */}}\nbody")
	cfg := &Config{
		PromptSchemaVersion: "v1.0.0",
		PromptValidation: PromptValidation{
			StrictMode:           false,
			RequireVersionHeader: true,
		},
	}
	_, err := NewPromptRenderer(cfg, path)
	assert.NoError(t, err, "non-strict mode should allow version mismatch")
}

func TestPromptRendererMissingVersionHeader(t *testing.T) {
	path := writeTempTemplate(t, "No header here")
	cfg := &Config{
		PromptSchemaVersion: "v1.0.0",
		PromptValidation: PromptValidation{
			StrictMode:           true,
			RequireVersionHeader: true,
		},
	}
	_, err := NewPromptRenderer(cfg, path)
	assert.ErrorContains(t, err, "missing Version header")
}

func writeTempTemplate(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.tmpl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp template: %v", err)
	}
	return path
}
