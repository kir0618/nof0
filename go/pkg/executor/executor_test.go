package executor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nof0-api/pkg/confkit"
	"nof0-api/pkg/llm"
)

const validDecisionJSON = `{
  "signal":"buy_to_enter",
  "symbol":"BTC",
  "leverage":5,
  "position_size_usd":200,
  "entry_price":100,
  "stop_loss":95,
  "take_profit":115,
  "risk_usd":10,
  "confidence":90,
  "invalidation_condition":"below EMA20",
  "reasoning":"clear uptrend"
}`

// fakeLLM returns a fixed structured decision matching the contract.
type fakeLLM struct {
	payload string
}

func newFakeLLM(payload string) *fakeLLM {
	if payload == "" {
		payload = validDecisionJSON
	}
	return &fakeLLM{payload: payload}
}

func (f *fakeLLM) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, nil
}
func (f *fakeLLM) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamResponse, error) {
	return nil, nil
}

func (f *fakeLLM) ChatStructured(_ context.Context, _ *llm.ChatRequest, target interface{}) (*llm.ChatResponse, error) {
	jsonStr := f.payload
	_ = llm.ParseStructured(jsonStr, target)
	return &llm.ChatResponse{
		Model: "test-model",
		Choices: []llm.Choice{
			{Message: llm.Message{Role: "assistant", Content: jsonStr}},
		},
		Usage: llm.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

func (f *fakeLLM) GetConfig() *llm.Config { return &llm.Config{} }
func (f *fakeLLM) Close() error           { return nil }

func TestExecutor_GetFullDecision(t *testing.T) {
	cfg := &Config{
		MajorCoinLeverage:      20,
		AltcoinLeverage:        10,
		MinConfidence:          75,
		MinRiskReward:          3.0,
		MaxPositions:           4,
		DecisionIntervalRaw:    "3m",
		DecisionTimeoutRaw:     "60s",
		MaxConcurrentDecisions: 1,
		OutputValidation: OutputValidation{
			Enabled:       true,
			SchemaPath:    confkit.MustProjectPath("schemas/decision_output.json"),
			FailOnInvalid: true,
		},
	}
	client := newFakeLLM("")
	templatePath := filepath.Join("..", "..", "etc", "prompts", "executor", "default_prompt.tmpl")

	exec, err := NewExecutor(cfg, client, templatePath, "")
	assert.NoError(t, err, "NewExecutor should not error")
	assert.NotNil(t, exec, "executor should not be nil")

	ctx := &Context{CurrentTime: "2025-01-01T00:00:00Z"}
	out, err := exec.GetFullDecision(ctx)
	assert.NoError(t, err, "GetFullDecision should not error")
	assert.NotNil(t, out, "decision output should not be nil")
	assert.Len(t, out.Decisions, 1, "should have exactly one decision")

	d := out.Decisions[0]
	assert.Equal(t, "open_long", d.Action, "action should be open_long")
	assert.Equal(t, "BTC", d.Symbol, "symbol should be BTC")
	assert.GreaterOrEqual(t, d.Confidence, 75, "confidence should be >= 75")
	assert.NotEmpty(t, out.UserPrompt, "UserPrompt should be populated")
}

func TestExecutorSchemaValidationStrict(t *testing.T) {
	cfg := &Config{
		MajorCoinLeverage:      20,
		AltcoinLeverage:        10,
		MinConfidence:          70,
		MinRiskReward:          2.0,
		MaxPositions:           2,
		DecisionIntervalRaw:    "3m",
		DecisionTimeoutRaw:     "60s",
		MaxConcurrentDecisions: 1,
		OutputValidation: OutputValidation{
			Enabled:       true,
			SchemaPath:    confkit.MustProjectPath("schemas/decision_output.json"),
			FailOnInvalid: true,
		},
	}
	invalidJSON := `{
	  "signal":"buy_to_enter",
	  "symbol":"BTC",
	  "leverage":3,
	  "position_size_usd":150,
	  "entry_price":100,
	  "stop_loss":95,
	  "take_profit":110,
	  "risk_usd":15,
	  "confidence":80,
	  "invalidation_condition":"below EMA",
	  "reasoning":"trend", 
	  "extra_field":"not allowed"
	}`
	client := newFakeLLM(invalidJSON)
	templatePath := filepath.Join("..", "..", "etc", "prompts", "executor", "default_prompt.tmpl")
	exec, err := NewExecutor(cfg, client, templatePath, "")
	require.NoError(t, err)

	_, err = exec.GetFullDecision(&Context{CurrentTime: "2025-01-01T00:00:00Z"})
	require.Error(t, err, "schema violations should bubble when fail_on_invalid is true")
}

func TestExecutorSchemaValidationWarn(t *testing.T) {
	cfg := &Config{
		MajorCoinLeverage:      20,
		AltcoinLeverage:        10,
		MinConfidence:          70,
		MinRiskReward:          2.0,
		MaxPositions:           2,
		DecisionIntervalRaw:    "3m",
		DecisionTimeoutRaw:     "60s",
		MaxConcurrentDecisions: 1,
		OutputValidation: OutputValidation{
			Enabled:       true,
			SchemaPath:    confkit.MustProjectPath("schemas/decision_output.json"),
			FailOnInvalid: false,
		},
	}
	invalidJSON := `{
	  "signal":"buy_to_enter",
	  "symbol":"BTC",
	  "leverage":3,
	  "position_size_usd":150,
	  "entry_price":100,
	  "stop_loss":95,
	  "take_profit":110,
	  "risk_usd":15,
	  "confidence":80,
	  "invalidation_condition":"below EMA",
	  "reasoning":"trend",
	  "extra_field":"warn only"
	}`
	client := newFakeLLM(invalidJSON)
	templatePath := filepath.Join("..", "..", "etc", "prompts", "executor", "default_prompt.tmpl")
	exec, err := NewExecutor(cfg, client, templatePath, "")
	require.NoError(t, err)

	out, err := exec.GetFullDecision(&Context{CurrentTime: "2025-01-01T00:00:00Z"})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, out.Decisions, 1)
}
