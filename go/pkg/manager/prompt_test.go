package manager

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"nof0-api/pkg/llm"
)

func TestManagerPromptRenderer(t *testing.T) {
	templatePath := filepath.Join("..", "..", "etc", "prompts", "manager", "aggressive_short.tmpl")
	renderer, err := NewPromptRenderer(templatePath, nil)
	assert.NoError(t, err, "NewPromptRenderer should not error")
	assert.NotNil(t, renderer, "renderer should not be nil")

	trader := &TraderConfig{
		ID:               "trader_aggressive_short",
		Name:             "Aggressive Short",
		ExchangeProvider: "hyperliquid",
		MarketProvider:   "hl_market",
		PromptTemplate:   templatePath,
		DecisionInterval: 3 * time.Minute,
		AllocationPct:    40,
		AutoStart:        true,
		RiskParams: RiskParameters{
			MaxPositions:       3,
			MaxPositionSizeUSD: 500,
			MaxMarginUsagePct:  60,
			MajorCoinLeverage:  20,
			AltcoinLeverage:    10,
			MinRiskRewardRatio: 3.0,
			MinConfidence:      75,
			StopLossEnabled:    true,
			TakeProfitEnabled:  true,
		},
	}

	out, err := renderer.Render(ManagerPromptInputs{
		Trader:      trader,
		ContextJSON: `{"market":"bearish"}`,
	})
	assert.NoError(t, err, "Render should not error")
	assert.NotEmpty(t, out, "rendered output should not be empty")

	expectations := []string{
		"Trader ID: trader_aggressive_short",
		"Decision Interval: 3m0s",
		"Allocation %: 40.00",
		"max_positions=3",
		"Min Confidence: 75",
		`{"market":"bearish"}`,
	}
	for _, substr := range expectations {
		assert.Contains(t, out, substr, "rendered prompt should contain %q", substr)
	}
}

func TestManagerPromptRendererMissingTrader(t *testing.T) {
	templatePath := filepath.Join("..", "..", "etc", "prompts", "manager", "aggressive_short.tmpl")
	renderer, err := NewPromptRenderer(templatePath, nil)
	assert.NoError(t, err, "NewPromptRenderer should not error")
	assert.NotNil(t, renderer, "renderer should not be nil")

	_, err = renderer.Render(ManagerPromptInputs{})
	assert.Error(t, err, "Render should error for missing trader data")
}

func TestManagerPromptRendererVersionMismatchStrict(t *testing.T) {
	path := writeTempManagerTemplate(t, "{{/* Version: v0.9.0 */}}\nbody")
	guard := &llm.TemplateVersionGuard{
		ExpectedVersion:      "v1.0.0",
		RequireVersionHeader: true,
		StrictMode:           true,
	}
	_, err := NewPromptRenderer(path, guard)
	assert.ErrorContains(t, err, "declared version v0.9.0")
}

func TestManagerPromptRendererVersionMismatchNonStrict(t *testing.T) {
	path := writeTempManagerTemplate(t, "{{/* Version: v0.9.0 */}}\nbody")
	guard := &llm.TemplateVersionGuard{
		ExpectedVersion:      "v1.0.0",
		RequireVersionHeader: true,
		StrictMode:           false,
	}
	_, err := NewPromptRenderer(path, guard)
	assert.NoError(t, err)
}

func TestManagerPromptRendererMissingHeader(t *testing.T) {
	path := writeTempManagerTemplate(t, "no metadata")
	guard := &llm.TemplateVersionGuard{
		ExpectedVersion:      "v1.0.0",
		RequireVersionHeader: true,
		StrictMode:           true,
	}
	_, err := NewPromptRenderer(path, guard)
	assert.ErrorContains(t, err, "missing Version header")
}

func writeTempManagerTemplate(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manager_prompt.tmpl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp manager template: %v", err)
	}
	return path
}
