package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"nof0-api/pkg/llm"
	"nof0-api/pkg/market"
)

// Executor defines the decision engine interface.
type Executor interface {
	// GetFullDecision builds prompts from input context, calls LLM and returns a validated decision bundle.
	GetFullDecision(input *Context) (*FullDecision, error)
	// UpdatePerformance refreshes the cached performance view used in prompts.
	UpdatePerformance(view *PerformanceView)
	// GetConfig exposes the immutable executor configuration.
	GetConfig() *Config
}

// BasicExecutor is a minimal implementation wiring configuration, prompt rendering and the LLM client.
type BasicExecutor struct {
	cfg           *Config
	llm           llm.LLMClient
	renderer      *PromptRenderer
	performance   *PerformanceView
	modelAlias    string
	failures      map[string]int
	conversations ConversationRecorder
	schemaChecker *JSONSchemaValidator
}

// NewExecutor constructs a BasicExecutor. The templatePath is the executor prompt template provided by caller.
func NewExecutor(cfg *Config, client llm.LLMClient, templatePath string, modelAlias string, opts ...ExecutorOption) (*BasicExecutor, error) {
	if cfg == nil {
		return nil, errors.New("executor: config is required")
	}
	if client == nil {
		return nil, errors.New("executor: llm client is required")
	}
	renderer, err := NewPromptRenderer(cfg, templatePath)
	if err != nil {
		return nil, err
	}
	var schemaChecker *JSONSchemaValidator
	if cfg.OutputValidation.Enabled {
		validator, err := NewJSONSchemaValidator(cfg.OutputValidation.SchemaPath)
		if err != nil {
			return nil, err
		}
		schemaChecker = validator
	}

	exec := &BasicExecutor{
		cfg:           cfg,
		llm:           client,
		renderer:      renderer,
		modelAlias:    strings.TrimSpace(modelAlias),
		failures:      make(map[string]int),
		conversations: noopConversationRecorder{},
		schemaChecker: schemaChecker,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(exec)
		}
	}
	if exec.conversations == nil {
		exec.conversations = noopConversationRecorder{}
	}
	return exec, nil
}

// GetConfig returns the underlying configuration.
func (e *BasicExecutor) GetConfig() *Config { return e.cfg }

// UpdatePerformance stores the latest performance snapshot.
func (e *BasicExecutor) UpdatePerformance(view *PerformanceView) { e.performance = view }

// GetFullDecision implements the end-to-end flow (MVP skeleton).
func (e *BasicExecutor) GetFullDecision(input *Context) (*FullDecision, error) {
	if e == nil || e.renderer == nil {
		return nil, errors.New("executor: not initialised")
	}
	if input == nil {
		return nil, errors.New("executor: input context is required")
	}

	e.logInputWarnings(input)

	// Render prompt from template with dynamic sections.
	inputs := buildPromptInputs(e.cfg, &Context{
		CurrentTime:       input.CurrentTime,
		RuntimeMinutes:    input.RuntimeMinutes,
		CallCount:         input.CallCount,
		Account:           input.Account,
		Positions:         input.Positions,
		CandidateCoins:    input.CandidateCoins,
		MarketDataMap:     input.MarketDataMap,
		OpenInterestMap:   input.OpenInterestMap,
		Performance:       e.performance,
		MajorCoinLeverage: e.cfg.MajorCoinLeverage,
		AltcoinLeverage:   e.cfg.AltcoinLeverage,
	})

	promptStr, err := e.renderer.Render(inputs)
	if err != nil {
		return nil, err
	}
	promptDigest := llm.DigestString(promptStr)
	if e.modelAlias != "" {
		logx.Infof("executor: prompt rendered digest=%s candidates=%d positions=%d runtime_minutes=%d model=%s", promptDigest, len(input.CandidateCoins), len(input.Positions), input.RuntimeMinutes, e.modelAlias)
	} else {
		logx.Infof("executor: prompt rendered digest=%s candidates=%d positions=%d runtime_minutes=%d", promptDigest, len(input.CandidateCoins), len(input.Positions), input.RuntimeMinutes)
	}

	// Phase 2: Call LLM with structured output request.
	req := &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: promptStr},
		},
	}
	if e.modelAlias != "" {
		req.Model = e.modelAlias
	}

	// Use package-level contract type for structured response.
	var out decisionContract
	callCtx, cancel := context.WithTimeout(context.Background(), e.cfg.DecisionTimeout)
	defer cancel()
	callStart := time.Now()
	resp, err := e.llm.ChatStructured(callCtx, req, &out)
	if err != nil {
		logx.WithContext(callCtx).Errorf("executor: chat failed digest=%s duration=%s error=%v", promptDigest, time.Since(callStart), err)
		return &FullDecision{UserPrompt: promptStr, CoTTrace: "", Decisions: nil, Timestamp: time.Now()}, err
	}
	logx.WithContext(callCtx).Infof("executor: chat completed digest=%s duration=%s", promptDigest, time.Since(callStart))
	e.recordConversation(callCtx, promptStr, resp)

	// Phase 3: Schema validation (optional) and logical validation.
	if err := e.validateSchema(resp, out); err != nil {
		if e.cfg.OutputValidation.FailOnInvalid {
			return &FullDecision{UserPrompt: promptStr, CoTTrace: "", Decisions: nil, Timestamp: time.Now()}, err
		}
		logx.Slowf("executor: schema validation warning digest=%s err=%v", promptDigest, err)
	}

	// Map & validate execution constraints.
	mapped := mapDecisionContract(out, input.Positions)
	if err := ValidateDecisions(e.cfg, input, []Decision{mapped}); err != nil {
		e.trackFailure(mapped.Symbol, err)
		return &FullDecision{UserPrompt: promptStr, CoTTrace: "", Decisions: []Decision{mapped}, Timestamp: time.Now()}, err
	}
	e.resetFailure(mapped.Symbol)
	logx.Infof("executor: decision validated digest=%s symbol=%s action=%s notional=%.2f confidence=%d", promptDigest, mapped.Symbol, mapped.Action, mapped.PositionSizeUSD, mapped.Confidence)

	return &FullDecision{
		UserPrompt: promptStr,
		CoTTrace:   "",
		Decisions:  []Decision{mapped},
		Timestamp:  time.Now(),
	}, nil
}

func condPerf(p *PerformanceView) *PerformanceView {
	if p != nil {
		return p
	}
	return &PerformanceView{}
}

func (e *BasicExecutor) validateSchema(resp *llm.ChatResponse, contract decisionContract) error {
	if e == nil || e.schemaChecker == nil {
		return nil
	}
	var raw []byte
	if resp != nil && len(resp.Choices) > 0 {
		content := strings.TrimSpace(resp.Choices[0].Message.Content)
		if content != "" {
			raw = []byte(content)
		}
	}
	if len(raw) == 0 {
		payload, err := json.Marshal(contract)
		if err != nil {
			return fmt.Errorf("executor: marshal decision for schema validation: %w", err)
		}
		raw = payload
	}
	return e.schemaChecker.ValidateBytes(raw)
}

func (e *BasicExecutor) logInputWarnings(input *Context) {
	if input == nil {
		return
	}
	const (
		changeOneHourAnomalyPct  = 0.05 // fraction move (~5%) within 1 hour that triggers a warning
		changeFourHourAnomalyPct = 0.10 // fraction move (~10%) within 4 hours that triggers a warning
		fundingAnomalyThreshold  = 0.01 // funding rate (decimal form) threshold for alerting
	)
	for sym, snap := range input.MarketDataMap {
		if snap == nil {
			continue
		}
		if math.Abs(snap.Change.OneHour) > changeOneHourAnomalyPct {
			logx.Slowf("executor: market change anomaly symbol=%s change_1h=%.4f change_4h=%.4f", sym, snap.Change.OneHour, snap.Change.FourHour)
		}
		if math.Abs(snap.Change.FourHour) > changeFourHourAnomalyPct {
			logx.Slowf("executor: market 4h change anomaly symbol=%s change_4h=%.4f", sym, snap.Change.FourHour)
		}
		if snap.Price.Last <= 0 {
			logx.Slowf("executor: non-positive price symbol=%s price=%f", sym, snap.Price.Last)
		}
		if snap.Funding != nil && math.Abs(snap.Funding.Rate) > fundingAnomalyThreshold {
			logx.Slowf("executor: funding anomaly symbol=%s funding=%.6f", sym, snap.Funding.Rate)
		}
		checkIndicators(sym, snap)
	}

	if input.Account.TotalEquity <= 0 {
		logx.Slowf("executor: account equity non-positive equity=%.2f", input.Account.TotalEquity)
	}
	symbolSeen := make(map[string]struct{}, len(input.Positions))
	for _, pos := range input.Positions {
		if _, exists := symbolSeen[pos.Symbol]; exists {
			logx.Slowf("executor: duplicate position detected symbol=%s", pos.Symbol)
		}
		symbolSeen[pos.Symbol] = struct{}{}
	}
	if len(input.CandidateCoins) == 0 && len(input.Positions) > 0 {
		logx.Slowf("executor: no candidates provided while %d positions open", len(input.Positions))
	}
}

func (e *BasicExecutor) recordConversation(ctx context.Context, prompt string, resp *llm.ChatResponse) {
	if e == nil || e.conversations == nil || resp == nil || e.cfg == nil || strings.TrimSpace(e.cfg.TraderID) == "" {
		return
	}
	if len(resp.Choices) == 0 {
		return
	}
	ts := time.Now()
	rec := ConversationRecord{
		ModelID:          e.cfg.TraderID,
		Prompt:           prompt,
		PromptTokens:     resp.Usage.PromptTokens,
		Response:         strings.TrimSpace(resp.Choices[0].Message.Content),
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		ModelName:        resp.Model,
		Timestamp:        ts,
	}
	if err := e.conversations.RecordConversation(ctx, rec); err != nil {
		logx.WithContext(ctx).Errorf("executor: record conversation failed trader=%s err=%v", e.cfg.TraderID, err)
	}
}

func checkIndicators(symbol string, snap *market.Snapshot) {
	if snap == nil {
		return
	}
	if len(snap.Indicators.EMA) == 0 && len(snap.Indicators.RSI) == 0 && snap.Indicators.MACD == 0 {
		logx.Slowf("executor: indicators missing for symbol=%s", symbol)
	}
	if snap.Indicators.RSI != nil {
		for key, value := range snap.Indicators.RSI {
			if value < 0 || value > 100 {
				logx.Slowf("executor: RSI anomaly symbol=%s interval=%s value=%.2f", symbol, key, value)
			}
		}
	}
}

func (e *BasicExecutor) trackFailure(symbol string, err error) {
	if e.failures == nil {
		e.failures = make(map[string]int)
	}
	key := normalizeFailureKey(symbol, err)
	if key == "" {
		return
	}
	e.failures[key]++
	count := e.failures[key]
	logx.Errorf("executor: decision validation failed key=%s symbol=%s error=%v count=%d", key, symbol, err, count)
	if count >= 3 {
		logx.Slowf("executor: repeated validation failures key=%s count=%d last_error=%v", key, count, err)
	}
}

func (e *BasicExecutor) resetFailure(symbol string) {
	if e.failures == nil {
		return
	}
	key := normalizeFailureKey(symbol, nil)
	if key == "" {
		return
	}
	delete(e.failures, key)
}

func normalizeFailureKey(symbol string, err error) string {
	key := strings.ToUpper(strings.TrimSpace(symbol))
	if key != "" {
		return key
	}
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 64 {
		msg = msg[:64]
	}
	if msg == "" {
		return ""
	}
	return "ERR:" + msg
}
