package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"nof0-api/pkg/exchange"
	executorpkg "nof0-api/pkg/executor"
	"nof0-api/pkg/journal"
	"nof0-api/pkg/llm"
	"nof0-api/pkg/market"
)

const (
	positionQuantityTolerance = 1e-6
)

// ExecutorFactory abstracts executor construction so Manager stays decoupled
// from concrete executor wiring (local vs RPC, prompt template selection, etc.).
type ExecutorFactory interface {
	NewExecutor(traderCfg TraderConfig) (executorpkg.Executor, error)
}

// BasicExecutorFactory is a minimal factory that adapts a TraderConfig into
// an executor.Config and constructs a local executor.BasicExecutor.
type BasicExecutorFactory struct {
	llmClient          llm.LLMClient
	conversationLogger executorpkg.ConversationRecorder
}

// NewBasicExecutorFactory returns a factory that builds local executors using
// the provided LLM client.
func NewBasicExecutorFactory(client llm.LLMClient, recorder executorpkg.ConversationRecorder) *BasicExecutorFactory {
	return &BasicExecutorFactory{llmClient: client, conversationLogger: recorder}
}

// NewExecutor implements ExecutorFactory.
func (f *BasicExecutorFactory) NewExecutor(traderCfg TraderConfig) (executorpkg.Executor, error) {
	if f == nil || f.llmClient == nil {
		return nil, errors.New("manager: executor factory requires llm client")
	}

	// Adapt risk/interval fields from TraderConfig → executor.Config.
	// Ensure durations are populated to avoid zero timeout.
	interval := traderCfg.DecisionInterval
	if interval <= 0 {
		if d, err := time.ParseDuration(traderCfg.DecisionIntervalRaw); err == nil && d > 0 {
			interval = d
		} else {
			interval = 3 * time.Minute
		}
	}
	intervalRaw := traderCfg.DecisionIntervalRaw
	if intervalRaw == "" {
		intervalRaw = "3m"
	}
	ec := &executorpkg.Config{
		MajorCoinLeverage:      traderCfg.RiskParams.MajorCoinLeverage,
		AltcoinLeverage:        traderCfg.RiskParams.AltcoinLeverage,
		MinConfidence:          traderCfg.RiskParams.MinConfidence,
		MinRiskReward:          traderCfg.RiskParams.MinRiskRewardRatio,
		MaxPositions:           traderCfg.RiskParams.MaxPositions,
		DecisionIntervalRaw:    intervalRaw,
		DecisionInterval:       interval,
		DecisionTimeoutRaw:     "60s",
		DecisionTimeout:        60 * time.Second,
		MaxConcurrentDecisions: 1,
		AllowedTraderIDs:       []string{traderCfg.ID},
	}
	// executor.NewExecutor validates config.
	ec.TraderID = traderCfg.ID
	var opts []executorpkg.ExecutorOption
	if f.conversationLogger != nil {
		opts = append(opts, executorpkg.WithConversationRecorder(f.conversationLogger))
	}
	exec, err := executorpkg.NewExecutor(ec, f.llmClient, traderCfg.ExecutorTemplate, traderCfg.Model, opts...)
	if err != nil {
		return nil, err
	}
	return exec, nil
}

// Manager is the orchestration layer that coordinates virtual traders,
// executors and providers.
type Manager struct {
	mu sync.RWMutex

	config *Config

	traders map[string]*VirtualTrader // Trader ID → instance
	// symbol (uppercased) → trader ID
	positionOwners map[string]string

	// Provider registries resolved at startup (see internal/svc for wiring).
	exchangeProviders map[string]exchange.Provider
	marketProviders   map[string]market.Provider

	executorFactory ExecutorFactory
	persistence     PersistenceService

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewManager constructs a Manager with injected dependencies.
func NewManager(
	cfg *Config,
	execFactory ExecutorFactory,
	exch map[string]exchange.Provider,
	mkts map[string]market.Provider,
	persist PersistenceService,
) *Manager {
	if cfg == nil {
		cfg = &Config{}
	}
	if persist == nil {
		persist = newNoopPersistenceService()
	}
	m := &Manager{
		config:            cfg,
		traders:           make(map[string]*VirtualTrader),
		positionOwners:    make(map[string]string),
		exchangeProviders: make(map[string]exchange.Provider),
		marketProviders:   make(map[string]market.Provider),
		executorFactory:   execFactory,
		persistence:       persist,
		stopChan:          make(chan struct{}),
	}
	for k, v := range exch {
		m.exchangeProviders[k] = v
	}
	for k, v := range mkts {
		m.marketProviders[k] = v
	}
	return m
}

// InitializeManager loads configuration and returns a Manager instance.
// Note: provider registries and executor factory can be injected later
// via NewManager or dedicated setters if needed by the application wiring.
func InitializeManager(configPath string) (*Manager, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	return NewManager(cfg, nil, nil, nil, nil), nil
}

// RegisterTrader creates a VirtualTrader from the provided configuration and
// attaches providers and executor instances.
func (m *Manager) RegisterTrader(cfg TraderConfig) (*VirtualTrader, error) {
	if m == nil {
		return nil, errors.New("manager: nil manager")
	}
	tempCfg := &Config{
		Manager:    ManagerConfig{},
		Traders:    []TraderConfig{cfg},
		Monitoring: MonitoringConfig{},
	}
	if m.config != nil {
		tempCfg.Manager = m.config.Manager
		tempCfg.Monitoring = m.config.Monitoring
	}
	if err := tempCfg.Validate(); err != nil {
		// Reuse config validation on a temporary wrapper to validate the trader.
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.traders[cfg.ID]; exists {
		return nil, fmt.Errorf("manager: trader %s already registered", cfg.ID)
	}

	// Resolve providers by ID as declared in manager config.
	ex, ok := m.exchangeProviders[cfg.ExchangeProvider]
	if !ok {
		return nil, fmt.Errorf("manager: unknown exchange provider %q for trader %s", cfg.ExchangeProvider, cfg.ID)
	}
	mk, ok := m.marketProviders[cfg.MarketProvider]
	if !ok {
		return nil, fmt.Errorf("manager: unknown market provider %q for trader %s", cfg.MarketProvider, cfg.ID)
	}
	if m.executorFactory == nil {
		return nil, errors.New("manager: executorFactory is not set")
	}
	exec, err := m.executorFactory.NewExecutor(cfg)
	if err != nil {
		return nil, fmt.Errorf("manager: create executor for trader %s: %w", cfg.ID, err)
	}

	vt := &VirtualTrader{
		ID:                   cfg.ID,
		Name:                 cfg.Name,
		Exchange:             cfg.ExchangeProvider,
		ExchangeProvider:     ex,
		MarketProvider:       mk,
		Executor:             exec,
		PromptTemplate:       cfg.PromptTemplate,
		OrderStyle:           cfg.OrderStyle,
		MarketIOCSlippageBps: cfg.MarketIOCSlippageBps,
		RiskParams:           cfg.RiskParams,
		ExecGuards:           cfg.ExecGuards,
		ResourceAlloc: ResourceAllocation{
			AllocationPct: cfg.AllocationPct,
		},
		State:            TraderStateStopped,
		DecisionInterval: cfg.DecisionInterval,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
		VirtualPositions: make(map[string]VirtualPosition),
		Cooldown:         make(map[string]time.Time),
		JournalEnabled:   cfg.JournalEnabled,
	}
	if cfg.JournalEnabled {
		dir := cfg.JournalDir
		if strings.TrimSpace(dir) == "" {
			dir = fmt.Sprintf("journal/%s", cfg.ID)
		}
		vt.Journal = journal.NewWriter(dir)
		logx.Infof("manager: trader %s journaling enabled dir=%s", cfg.ID, dir)
	}

	m.traders[cfg.ID] = vt
	if cfg.AutoStart {
		_ = vt.Start()
	}
	logx.Infof("manager: registered trader id=%s name=%s allocation=%.2f%% exchange=%s market=%s model=%s order_style=%s auto_start=%t", vt.ID, vt.Name, cfg.AllocationPct, cfg.ExchangeProvider, cfg.MarketProvider, cfg.Model, cfg.OrderStyle, cfg.AutoStart)
	return vt, nil
}

// UnregisterTrader stops and removes a trader from registry.
func (m *Manager) UnregisterTrader(traderID string) error {
	m.mu.Lock()
	t, ok := m.traders[traderID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("manager: trader %s not found", traderID)
	}
	delete(m.traders, traderID)
	m.mu.Unlock()
	_ = t.Stop() // Best-effort stop; ignore error for MVP.
	m.releaseAllVirtualPositions(t)
	logx.Infof("manager: unregistered trader id=%s", traderID)
	return nil
}

// GetActiveTraders returns a stable-ordered slice of currently active traders.
func (m *Manager) GetActiveTraders() []*VirtualTrader {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*VirtualTrader, 0, len(m.traders))
	for _, t := range m.traders {
		if t.IsActive() {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RunTradingLoop executes the main orchestration loop (minimal skeleton).
func (m *Manager) RunTradingLoop(ctx context.Context) error {
	if m == nil {
		return errors.New("manager: nil manager")
	}
	logx.WithContext(ctx).Infof("manager: trading loop starting tick=1s active_traders=%d", len(m.GetActiveTraders()))
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logx.WithContext(ctx).Infof("manager: trading loop stopping (context): %v", ctx.Err())
			return ctx.Err()
		case <-m.stopChan:
			logx.WithContext(ctx).Infof("manager: trading loop stopping (stop signal)")
			return nil
		case <-ticker.C:
			traders := m.GetActiveTraders()
			for _, t := range traders {
				if !t.ShouldMakeDecision() {
					continue
				}
				cycleStart := time.Now()
				// Sharpe gating
				if t.ExecGuards.SharpePauseThreshold != 0 && t.ExecGuards.PauseDurationOnBreach > 0 && t.Performance != nil {
					if t.Performance.SharpeRatio < t.ExecGuards.SharpePauseThreshold {
						t.mu.Lock()
						if t.PauseUntil.Before(time.Now()) {
							t.PauseUntil = time.Now().Add(t.ExecGuards.PauseDurationOnBreach)
						}
						t.mu.Unlock()
						logx.WithContext(ctx).Infof("manager: trader %s paused for Sharpe gating until %s", t.ID, t.PauseUntil.Format(time.RFC3339))
						continue
					}
				}
				// Build richer executor context and refresh performance view.
				perfView := t.Performance.ToExecutorView()
				t.Executor.UpdatePerformance(perfView)

				ectx := m.buildExecutorContext(t)
				out, decisionErr := t.Executor.GetFullDecision(&ectx)
				// NOTE: BasicExecutor will still return a FullDecision even when validation fails (decisionErr != nil),
				// so call sites must treat decisionErr as authoritative and avoid executing the payload until it passes.

				// Prepare journaling containers
				var decisionsJSON string
				var actions []map[string]any
				allOK := true
				decisionCount := 0
				if out != nil {
					// decisionErr can be non-nil here; when wiring retries/guards make sure we don't execute decisions that failed validation.
					decisionCount = len(out.Decisions)
					if b, e := json.Marshal(out.Decisions); e == nil {
						decisionsJSON = string(b)
					}
					// Close actions first, then open actions; cap new opens by remaining slots.
					decisions := sortDecisionsCloseFirst(out.Decisions)
					// remaining slots by max positions
					remaining := t.RiskParams.MaxPositions - len(ectx.Positions)
					if remaining < 0 {
						remaining = 0
					}
					// also enforce per-cycle cap if configured (>0)
					cycleCap := t.ExecGuards.MaxNewPositionsPerCycle
					if cycleCap > 0 && cycleCap < remaining {
						remaining = cycleCap
					}
					decisions = capNewOpenDecisions(decisions, remaining)
					for i := range decisions {
						d := decisions[i]
						execErr := m.ExecuteDecision(t, &d)
						act := map[string]any{
							"symbol":            d.Symbol,
							"action":            d.Action,
							"leverage":          d.Leverage,
							"position_size_usd": d.PositionSizeUSD,
							"entry_price":       d.EntryPrice,
							"stop_loss":         d.StopLoss,
							"take_profit":       d.TakeProfit,
							"confidence":        d.Confidence,
							"result":            "ok",
						}
						if execErr != nil {
							act["result"] = "error"
							act["error"] = execErr.Error()
							allOK = false
							logx.WithContext(ctx).Errorf("manager: trader %s decision action=%s symbol=%s error=%v", t.ID, d.Action, d.Symbol, execErr)
						}
						actions = append(actions, act)
					}
				} else {
					allOK = false
					if decisionErr != nil {
						logx.WithContext(ctx).Errorf("manager: trader %s decision generation failed: %v", t.ID, decisionErr)
					}
				}

				// Update lightweight performance snapshot (success ratio proxy)
				if t.Performance == nil {
					t.Performance = &PerformanceMetrics{}
				}
				succ := 0
				for _, a := range actions {
					if a["result"] == "ok" {
						succ++
					}
				}
				total := len(actions)
				t.Performance.TotalTrades += total
				if total > 0 {
					t.Performance.WinRate = float64(succ) / float64(total)
				}
				t.Performance.UpdatedAt = time.Now()
				m.recordAnalytics(AnalyticsSnapshot{
					TraderID:       t.ID,
					TotalPnLUSD:    t.Performance.TotalPnLUSD,
					TotalPnLPct:    t.Performance.TotalPnLPct,
					SharpeRatio:    t.Performance.SharpeRatio,
					WinRate:        t.Performance.WinRate,
					TotalTrades:    t.Performance.TotalTrades,
					MaxDrawdownPct: t.Performance.MaxDrawdownPct,
					UpdatedAt:      t.Performance.UpdatedAt,
				})

				// Journal the cycle if configured
				if t.Journal != nil && t.JournalEnabled {
					if jErr := m.writeJournalRecord(t, &ectx, out, decisionsJSON, actions, decisionErr, allOK); jErr != nil {
						logx.WithContext(ctx).Errorf("manager: trader %s journal write failed: %v", t.ID, jErr)
					} else {
						logx.WithContext(ctx).Infof("manager: trader %s journal written prompt_digest=%s", t.ID, outPromptDigest(out))
					}
				}
				t.RecordDecision(time.Now())
				if syncErr := m.SyncTraderPositions(t.ID); syncErr != nil {
					logx.WithContext(ctx).Errorf("manager: trader %s sync positions error: %v", t.ID, syncErr)
				}
				logx.WithContext(ctx).Infof("manager: cycle trader=%s decisions=%d actions=%d ok=%t duration=%s", t.ID, decisionCount, len(actions), allOK && decisionErr == nil, time.Since(cycleStart).String())
			}
		}
	}
}

// Stop signals the main loop to exit.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		logx.Info("manager: stop signal emitted")
		close(m.stopChan)
	})
}

// ExecuteDecision executes a single decision using trader's exchange provider.
// Placeholder MVP: perform basic validation and return nil.
func (m *Manager) ExecuteDecision(trader *VirtualTrader, decision *executorpkg.Decision) error {
	if trader == nil || decision == nil {
		return errors.New("manager: execute decision requires trader and decision")
	}
	if decision.Symbol == "" && decision.Action != "hold" && decision.Action != "wait" {
		return errors.New("manager: decision missing symbol")
	}
	if decision.PositionSizeUSD < 0 {
		return errors.New("manager: decision position size must be non-negative")
	}

	isOpen := decision.Action == "open_long" || decision.Action == "open_short"
	isClose := decision.Action == "close_long" || decision.Action == "close_short"
	if (isOpen || isClose) && decision.Symbol == "" {
		return errors.New("manager: symbol required for trade action")
	}
	if isOpen {
		if err := m.ensureSymbolAvailable(trader, decision.Symbol); err != nil {
			return err
		}
		if err := m.SyncTraderPositions(trader.ID); err != nil {
			logx.Errorf("manager: sync trader %s before execution failed: %v", trader.ID, err)
		}
	}
	if isClose {
		if err := m.ensureCloseOwnership(trader, decision.Symbol); err != nil {
			return err
		}
	}

	// Close actions shortcut via provider.
	if isClose {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var closeSnapPrice float64
		if setter, ok := trader.ExchangeProvider.(interface {
			SetMarkPrice(context.Context, string, float64) error
		}); ok {
			if snap, err := trader.MarketProvider.Snapshot(ctx, decision.Symbol); err == nil && snap != nil && snap.Price.Last > 0 {
				closeSnapPrice = snap.Price.Last
				_ = setter.SetMarkPrice(ctx, decision.Symbol, snap.Price.Last)
			}
		}
		// Attempt to cancel resting orders via optional extension
		if p, ok := trader.ExchangeProvider.(interface {
			CancelAllBySymbol(context.Context, string) error
		}); ok {
			_ = p.CancelAllBySymbol(ctx, decision.Symbol)
		}
		orderResp, err := trader.ExchangeProvider.ClosePosition(ctx, decision.Symbol)
		if err != nil {
			return err
		}
		logx.Infof("manager: trader %s closed position symbol=%s action=%s", trader.ID, decision.Symbol, decision.Action)
		// Mark cooldown timestamp on successful close
		trader.mu.Lock()
		trader.Cooldown[decision.Symbol] = time.Now()
		trader.mu.Unlock()
		fillPrice, fillQty, ok := parseOrderFill(orderResp)
		if !ok {
			fillPrice = closeSnapPrice
			if fillPrice <= 0 {
				fillPrice = decision.EntryPrice
			}
		}
		if fillQty <= 0 && fillPrice > 0 && decision.PositionSizeUSD > 0 {
			fillQty = decision.PositionSizeUSD / fillPrice
		}
		m.recordPositionEvent(PositionEvent{
			TraderID:         trader.ID,
			Trader:           trader,
			Decision:         *decision,
			Event:            PositionEventClose,
			ExchangeResponse: orderResp,
			FillPrice:        fillPrice,
			FillSize:         fillQty,
			OccurredAt:       time.Now(),
		})
		m.releaseVirtualPosition(trader.ID, decision.Symbol)
		return nil
	}

	if decision.Action != "open_long" && decision.Action != "open_short" {
		// Ignore non-trade actions (e.g., hold/wait).
		return nil
	}

	// Resolve leverage preference.
	lev := decision.Leverage
	if lev <= 0 {
		if isBTCorETH(decision.Symbol) {
			lev = trader.RiskParams.MajorCoinLeverage
		} else {
			lev = trader.RiskParams.AltcoinLeverage
		}
	}
	decision.Leverage = lev
	if err := m.enforceSecondaryRisk(trader, decision, lev); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	assetIdx, err := trader.ExchangeProvider.GetAssetIndex(ctx, decision.Symbol)
	if err == nil && lev > 0 {
		_ = trader.ExchangeProvider.UpdateLeverage(ctx, assetIdx, true, lev)
	}

	// Determine price: use decision price or query market snapshot.
	price := decision.EntryPrice
	if !(price > 0) {
		snap, err := trader.MarketProvider.Snapshot(ctx, decision.Symbol)
		if err != nil {
			return fmt.Errorf("manager: fetch market snapshot for %s: %w", decision.Symbol, err)
		}
		price = snap.Price.Last
	}
	if !(price > 0) {
		return fmt.Errorf("manager: invalid price resolved for %s", decision.Symbol)
	}

	// Compute size and direction.
	if setter, ok := trader.ExchangeProvider.(interface {
		SetMarkPrice(context.Context, string, float64) error
	}); ok {
		if err := setter.SetMarkPrice(ctx, decision.Symbol, price); err != nil {
			logx.WithContext(ctx).Errorf("manager: set mark price trader=%s symbol=%s err=%v", trader.ID, decision.Symbol, err)
		}
	}

	qty := decision.PositionSizeUSD / price
	if qty <= 0 || math.IsNaN(qty) || math.IsInf(qty, 0) {
		return fmt.Errorf("manager: invalid position size for %s: qty=%.6f", decision.Symbol, qty)
	}
	isBuy := decision.Action == "open_long"
	priceStr := fmt.Sprintf("%.8f", price)
	sizeStr := fmt.Sprintf("%.8f", qty)
	var orderResp *exchange.OrderResponse

	switch trader.OrderStyle {
	case OrderStyleMarketIOC:
		slippage := trader.MarketIOCSlippageBps / 10000.0
		if slippage <= 0 {
			slippage = defaultMarketIOCSlippageBps / 10000.0
		}
		execProvider, ok := trader.ExchangeProvider.(interface {
			IOCMarket(context.Context, string, bool, float64, float64, bool) (*exchange.OrderResponse, error)
		})
		if !ok {
			return fmt.Errorf("manager: trader %s order_style=market_ioc unsupported by exchange provider", trader.ID)
		}
		logx.WithContext(ctx).Infof(
			"manager: trader %s prepared market_ioc order symbol=%s is_buy=%t raw_price=%.8f raw_qty=%.8f asset_idx=%d leverage=%d",
			trader.ID, decision.Symbol, isBuy, price, qty, assetIdx, lev,
		)
		resp, err := execProvider.IOCMarket(ctx, decision.Symbol, isBuy, qty, slippage, false)
		if err != nil {
			return fmt.Errorf("manager: market_ioc order %s %s: %w", decision.Symbol, decision.Action, err)
		}
		orderResp = resp
		summary := summarizeOrderResponse(resp)
		logx.Infof("manager: trader %s submitted market_ioc order symbol=%s notional=%.2f usd qty=%.6f slippage_bps=%.2f response=%s", trader.ID, decision.Symbol, decision.PositionSizeUSD, qty, trader.MarketIOCSlippageBps, summary)
	case OrderStyleLimitIOC, "":
		if p, ok := trader.ExchangeProvider.(interface {
			FormatPrice(context.Context, string, float64) (string, error)
		}); ok {
			if s, err := p.FormatPrice(ctx, decision.Symbol, price); err == nil && s != "" {
				priceStr = s
			} else if err != nil {
				logx.WithContext(ctx).Infof("manager: format price fallback trader=%s symbol=%s price=%.8f err=%v", trader.ID, decision.Symbol, price, err)
			}
		}
		if p, ok := trader.ExchangeProvider.(interface {
			FormatSize(context.Context, string, float64) (string, error)
		}); ok {
			if s, err := p.FormatSize(ctx, decision.Symbol, qty); err == nil && s != "" {
				sizeStr = s
			} else if err != nil {
				logx.WithContext(ctx).Infof("manager: format size fallback trader=%s symbol=%s qty=%.8f err=%v", trader.ID, decision.Symbol, qty, err)
			}
		}

		cloid := buildCloid(trader.ID, decision.Symbol, decision.Action, qty, time.Now())
		order := exchange.Order{
			Asset:      assetIdx,
			IsBuy:      isBuy,
			LimitPx:    priceStr,
			Sz:         sizeStr,
			ReduceOnly: false,
			OrderType:  exchange.OrderType{Limit: &exchange.LimitOrderType{TIF: "Ioc"}},
			Cloid:      cloid,
		}
		logx.WithContext(ctx).Infof(
			"manager: trader %s prepared limit_ioc order symbol=%s is_buy=%t raw_price=%.8f price_str=%s raw_qty=%.8f size_str=%s asset_idx=%d leverage=%d",
			trader.ID, decision.Symbol, isBuy, price, priceStr, qty, sizeStr, assetIdx, lev,
		)
		resp, err := trader.ExchangeProvider.PlaceOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("manager: place order %s %s: %w", decision.Symbol, decision.Action, err)
		}
		orderResp = resp
		summary := summarizeOrderResponse(resp)
		logx.Infof("manager: trader %s submitted limit_ioc order symbol=%s notional=%.2f usd qty=%.6f cloid=%s response=%s", trader.ID, decision.Symbol, decision.PositionSizeUSD, qty, cloid, summary)
	default:
		return fmt.Errorf("manager: trader %s unsupported order_style=%s", trader.ID, trader.OrderStyle)
	}
	// Configure reduce-only SL/TP best-effort
	side := "LONG"
	if !isBuy { // open_short
		side = "SHORT"
	}
	// Best-effort SL/TP via optional provider extension.
	if p, ok := trader.ExchangeProvider.(interface {
		SetStopLoss(context.Context, string, string, float64, float64) error
		SetTakeProfit(context.Context, string, string, float64, float64) error
	}); ok {
		_ = p.SetStopLoss(ctx, decision.Symbol, side, qty, decision.StopLoss)
		_ = p.SetTakeProfit(ctx, decision.Symbol, side, qty, decision.TakeProfit)
	}
	fillPrice := price
	fillQty := qty
	if fp, fq, ok := parseOrderFill(orderResp); ok {
		if fq > 0 {
			fillQty = fq
		}
		if fp > 0 {
			fillPrice = fp
		}
	}
	if fillQty <= 0 {
		fillQty = qty
	}
	if fillPrice <= 0 {
		fillPrice = price
	}
	m.recordPositionEvent(PositionEvent{
		TraderID:         trader.ID,
		Trader:           trader,
		Decision:         *decision,
		Event:            PositionEventOpen,
		ExchangeResponse: orderResp,
		FillPrice:        fillPrice,
		FillSize:         fillQty,
		OccurredAt:       time.Now(),
	})
	virtualSide := "long"
	if !isBuy {
		virtualSide = "short"
	}
	if fillQty > 0 {
		vp := VirtualPosition{
			Symbol:      decision.Symbol,
			Side:        virtualSide,
			Quantity:    fillQty,
			EntryPrice:  fillPrice,
			NotionalUSD: fillQty * fillPrice,
			Leverage:    lev,
		}
		if err := m.assignVirtualPosition(trader, vp); err != nil {
			return err
		}
	}
	return nil
}

// SyncAllPositions updates cached account/position state for all traders (stub).
func (m *Manager) SyncAllPositions() error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.traders))
	for id := range m.traders {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		_ = m.SyncTraderPositions(id)
	}
	return nil
}

// SyncTraderPositions updates a single trader's cached state (stub).
func (m *Manager) SyncTraderPositions(traderID string) error {
	m.mu.RLock()
	t := m.traders[traderID]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("manager: sync positions: trader %s not found", traderID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acct, err := t.ExchangeProvider.GetAccountState(ctx)
	if err != nil {
		return err
	}
	// Parse commonly used fields from strings.
	acctVal := parseFloat(acct.MarginSummary.AccountValue)
	marginUsed := parseFloat(acct.MarginSummary.TotalMarginUsed)
	var unreal float64
	for i := range acct.AssetPositions {
		unreal += parseFloat(acct.AssetPositions[i].UnrealizedPnl)
	}

	t.mu.Lock()
	t.ResourceAlloc.CurrentEquityUSD = acctVal
	t.ResourceAlloc.MarginUsedUSD = marginUsed
	t.ResourceAlloc.UnrealizedPnLUSD = unreal
	t.ResourceAlloc.AvailableBalanceUSD = math.Max(0, acctVal-marginUsed)
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
	logx.Infof("manager: trader %s equity=%.2f usd margin_used=%.2f usd avail=%.2f usd unreal_pnl=%.2f usd", traderID, acctVal, marginUsed, t.ResourceAlloc.AvailableBalanceUSD, unreal)
	m.recordAccountSnapshot(AccountSyncSnapshot{
		TraderID:            traderID,
		EquityUSD:           acctVal,
		MarginUsedUSD:       marginUsed,
		AvailableBalanceUSD: t.ResourceAlloc.AvailableBalanceUSD,
		UnrealizedPnLUSD:    unreal,
		SyncedAt:            time.Now(),
	})
	if t.Performance != nil {
		m.recordAnalytics(AnalyticsSnapshot{
			TraderID:       traderID,
			TotalPnLUSD:    t.Performance.TotalPnLUSD,
			TotalPnLPct:    t.Performance.TotalPnLPct,
			SharpeRatio:    t.Performance.SharpeRatio,
			WinRate:        t.Performance.WinRate,
			TotalTrades:    t.Performance.TotalTrades,
			MaxDrawdownPct: t.Performance.MaxDrawdownPct,
			UpdatedAt:      t.Performance.UpdatedAt,
		})
	}
	return m.ReconcileTraderPositions(ctx, traderID)
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return 0
}

func outPromptDigest(out *executorpkg.FullDecision) string {
	if out == nil {
		return ""
	}
	if strings.TrimSpace(out.UserPrompt) == "" {
		return ""
	}
	return llm.DigestString(out.UserPrompt)
}

func parsePtrFloat(ps *string) float64 {
	if ps == nil {
		return 0
	}
	return parseFloat(*ps)
}

func isBTCorETH(symbol string) bool {
	switch symbol {
	case "BTC", "ETH", "BTCUSDT", "ETHUSDT":
		return true
	default:
		return false
	}
}

func normalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func (m *Manager) ensureSymbolAvailable(trader *VirtualTrader, symbol string) error {
	if trader == nil {
		return errors.New("manager: trader is nil")
	}
	key := normalizeSymbol(symbol)
	if key == "" {
		return errors.New("manager: symbol is required for open action")
	}
	owner := m.getPositionOwner(key)
	if owner == "" {
		return nil
	}
	if owner == trader.ID {
		return fmt.Errorf("manager: trader %s already controls %s", trader.ID, symbol)
	}
	return fmt.Errorf("manager: symbol %s currently assigned to trader %s", symbol, owner)
}

func (m *Manager) ensureCloseOwnership(trader *VirtualTrader, symbol string) error {
	if trader == nil {
		return errors.New("manager: trader is nil")
	}
	key := normalizeSymbol(symbol)
	if key == "" {
		return errors.New("manager: symbol is required for close action")
	}
	owner := m.getPositionOwner(key)
	if owner == "" {
		logx.Slowf("manager: trader %s closing unassigned symbol %s", trader.ID, symbol)
		return nil
	}
	if owner != trader.ID {
		return fmt.Errorf("manager: trader %s cannot close %s owned by trader %s", trader.ID, symbol, owner)
	}
	return nil
}

func (m *Manager) getPositionOwner(symbol string) string {
	if m == nil {
		return ""
	}
	key := normalizeSymbol(symbol)
	if key == "" {
		return ""
	}
	m.mu.RLock()
	owner := m.positionOwners[key]
	m.mu.RUnlock()
	return owner
}

func (m *Manager) assignVirtualPosition(trader *VirtualTrader, pos VirtualPosition) error {
	if m == nil || trader == nil {
		return errors.New("manager: assign virtual position missing trader")
	}
	key := normalizeSymbol(pos.Symbol)
	if key == "" {
		return errors.New("manager: assign virtual position requires symbol")
	}
	now := time.Now()
	if pos.OpenedAt.IsZero() {
		pos.OpenedAt = now
	}
	pos.UpdatedAt = now
	m.mu.Lock()
	defer m.mu.Unlock()
	if owner, ok := m.positionOwners[key]; ok && owner != trader.ID {
		return fmt.Errorf("manager: symbol %s already assigned to trader %s", pos.Symbol, owner)
	}
	trader.mu.Lock()
	if trader.VirtualPositions == nil {
		trader.VirtualPositions = make(map[string]VirtualPosition)
	}
	trader.VirtualPositions[key] = pos
	trader.mu.Unlock()
	m.positionOwners[key] = trader.ID
	return nil
}

func (m *Manager) releaseVirtualPosition(traderID, symbol string) {
	if m == nil {
		return
	}
	key := normalizeSymbol(symbol)
	if key == "" {
		return
	}
	m.mu.Lock()
	trader := m.traders[traderID]
	owner := m.positionOwners[key]
	if owner != "" && owner != traderID {
		m.mu.Unlock()
		return
	}
	delete(m.positionOwners, key)
	m.mu.Unlock()
	if trader == nil {
		return
	}
	trader.mu.Lock()
	delete(trader.VirtualPositions, key)
	trader.mu.Unlock()
}

func (m *Manager) releaseAllVirtualPositions(trader *VirtualTrader) {
	if trader == nil {
		return
	}
	trader.mu.RLock()
	symbols := make([]string, 0, len(trader.VirtualPositions))
	for sym := range trader.VirtualPositions {
		symbols = append(symbols, sym)
	}
	trader.mu.RUnlock()
	for _, sym := range symbols {
		m.releaseVirtualPosition(trader.ID, sym)
	}
}

func (m *Manager) snapshotVirtualPositions(trader *VirtualTrader) map[string]VirtualPosition {
	if trader == nil {
		return nil
	}
	trader.mu.RLock()
	defer trader.mu.RUnlock()
	out := make(map[string]VirtualPosition, len(trader.VirtualPositions))
	for k, v := range trader.VirtualPositions {
		out[k] = v
	}
	return out
}

func (m *Manager) filterPositionsForTrader(traderID string, positions []exchange.Position) []exchange.Position {
	if m == nil {
		return positions
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.positionOwners) == 0 {
		return positions
	}
	filtered := make([]exchange.Position, 0, len(positions))
	for _, p := range positions {
		key := normalizeSymbol(p.Coin)
		owner := m.positionOwners[key]
		if owner == "" || owner == traderID {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func summarizeOrderResponse(resp *exchange.OrderResponse) string {
	if resp == nil {
		return "status=<nil>"
	}
	details := make([]string, 0, len(resp.Response.Data.Statuses)+1)
	for i, st := range resp.Response.Data.Statuses {
		switch {
		case st.Error != "":
			details = append(details, fmt.Sprintf("[%d]error=%s", i, st.Error))
		case st.Filled != nil:
			details = append(details, fmt.Sprintf("[%d]filled oid=%d total_sz=%s avg_px=%s", i, st.Filled.Oid, st.Filled.TotalSz, st.Filled.AvgPx))
		case st.Resting != nil:
			details = append(details, fmt.Sprintf("[%d]resting oid=%d", i, st.Resting.Oid))
		}
	}
	if resp.ErrorMessage != "" {
		details = append(details, fmt.Sprintf("message=%s", resp.ErrorMessage))
	}
	summary := strings.Join(details, "; ")
	if strings.TrimSpace(summary) == "" {
		summary = "no_status"
	}
	status := strings.TrimSpace(resp.Status)
	if status == "" {
		return summary
	}
	return fmt.Sprintf("status=%s %s", status, summary)
}

func parseOrderFill(resp *exchange.OrderResponse) (price float64, qty float64, ok bool) {
	if resp == nil {
		return 0, 0, false
	}
	for _, st := range resp.Response.Data.Statuses {
		if st.Filled == nil {
			continue
		}
		if px, err := strconv.ParseFloat(strings.TrimSpace(st.Filled.AvgPx), 64); err == nil && px > 0 {
			price = px
		}
		if sz, err := strconv.ParseFloat(strings.TrimSpace(st.Filled.TotalSz), 64); err == nil && sz > 0 {
			qty = sz
		}
		if price > 0 || qty > 0 {
			return price, qty, true
		}
	}
	return 0, 0, false
}

// ReconcileTraderPositions ensures the manager's virtual book matches the actual
// exchange positions for a trader. It also auto-assigns unowned positions to the
// trader that detects them, emitting warnings for manual review.
func (m *Manager) ReconcileTraderPositions(ctx context.Context, traderID string) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	trader := m.traders[traderID]
	m.mu.RUnlock()
	if trader == nil {
		return fmt.Errorf("manager: reconcile trader %s not found", traderID)
	}
	var cancel context.CancelFunc
	if ctx == nil {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	positionsRaw, err := trader.ExchangeProvider.GetPositions(ctx)
	if err != nil {
		return fmt.Errorf("manager: fetch positions for trader %s: %w", traderID, err)
	}
	actual := make(map[string]exchange.Position, len(positionsRaw))
	for _, p := range positionsRaw {
		if qty := parseFloat(p.Szi); qty != 0 {
			actual[normalizeSymbol(p.Coin)] = p
		}
	}
	virtual := m.snapshotVirtualPositions(trader)
	for sym, v := range virtual {
		p, ok := actual[sym]
		if !ok {
			logx.Slowf("manager: trader %s virtual position %s missing on exchange; releasing", trader.ID, sym)
			m.releaseVirtualPosition(trader.ID, sym)
			continue
		}
		vp, _ := exchangePositionToVirtual(p)
		if vp.Symbol == "" {
			continue
		}
		if v.Side != vp.Side || math.Abs(v.Quantity-vp.Quantity) > positionQuantityTolerance {
			return fmt.Errorf("manager: position mismatch %s (virtual %.6f %s vs exchange %.6f %s)", sym, v.Quantity, v.Side, vp.Quantity, vp.Side)
		}
	}
	for sym, p := range actual {
		owner := m.getPositionOwner(sym)
		vp, ok := exchangePositionToVirtual(p)
		if !ok {
			continue
		}
		if owner != "" && owner != trader.ID {
			// Another trader owns this symbol; they will reconcile it during their sync.
			continue
		}
		if owner == "" {
			logx.Slowf("manager: assigning unowned position %s to trader %s during reconciliation", sym, trader.ID)
			if err := m.assignVirtualPosition(trader, vp); err != nil {
				return err
			}
			continue
		}
		if _, ok := virtual[sym]; !ok {
			if err := m.assignVirtualPosition(trader, vp); err != nil {
				return err
			}
		}
	}
	return nil
}

func exchangePositionToVirtual(p exchange.Position) (VirtualPosition, bool) {
	qtySigned := parseFloat(p.Szi)
	if qtySigned == 0 {
		return VirtualPosition{}, false
	}
	side := "long"
	if qtySigned < 0 {
		side = "short"
	}
	qty := math.Abs(qtySigned)
	entry := parsePtrFloat(p.EntryPx)
	if entry <= 0 && qty > 0 {
		if pv := parseFloat(p.PositionValue); pv > 0 {
			entry = pv / qty
		}
	}
	vp := VirtualPosition{
		Symbol:     p.Coin,
		Side:       side,
		Quantity:   qty,
		EntryPrice: entry,
		Leverage:   p.Leverage.Value,
	}
	if entry > 0 {
		vp.NotionalUSD = entry * qty
	} else {
		vp.NotionalUSD = parseFloat(p.PositionValue)
	}
	return vp, true
}

func (m *Manager) enforceSecondaryRisk(trader *VirtualTrader, decision *executorpkg.Decision, leverage int) error {
	if trader == nil || decision == nil {
		return errors.New("manager: missing inputs for risk check")
	}
	rp := trader.RiskParams
	if rp.MaxPositionSizeUSD > 0 && decision.PositionSizeUSD > rp.MaxPositionSizeUSD+1e-6 {
		return fmt.Errorf("manager: decision size %.2f exceeds max_position_size_usd %.2f", decision.PositionSizeUSD, rp.MaxPositionSizeUSD)
	}
	if rp.MaxMarginUsagePct > 0 && leverage > 0 {
		trader.mu.RLock()
		alloc := trader.ResourceAlloc
		trader.mu.RUnlock()
		equity := alloc.CurrentEquityUSD
		if equity > 0 {
			projected := alloc.MarginUsedUSD + decision.PositionSizeUSD/float64(leverage)
			usagePct := 100 * (projected / equity)
			if usagePct > rp.MaxMarginUsagePct+1e-6 {
				return fmt.Errorf("manager: projected margin usage %.2f%% exceeds cap %.2f%%", usagePct, rp.MaxMarginUsagePct)
			}
		}
	}
	return nil
}

func (m *Manager) recordPositionEvent(event PositionEvent) {
	if m == nil || m.persistence == nil {
		return
	}
	if event.TraderID == "" && event.Trader != nil {
		event.TraderID = event.Trader.ID
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := m.persistence.RecordPositionEvent(ctx, event)
	logPersistenceError(err, "position event persistence failed", map[string]any{
		"trader_id": event.TraderID,
		"symbol":    event.Decision.Symbol,
		"event":     event.Event,
	})
}

func (m *Manager) recordDecisionCycle(record DecisionCycleRecord) {
	if m == nil || m.persistence == nil || record.Cycle == nil {
		return
	}
	if record.TraderID == "" && record.Cycle != nil {
		record.TraderID = record.Cycle.TraderID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := m.persistence.RecordDecisionCycle(ctx, record)
	logPersistenceError(err, "decision cycle persistence failed", map[string]any{
		"trader_id": record.TraderID,
		"success":   record.Cycle.Success,
	})
}

func (m *Manager) recordAccountSnapshot(snapshot AccountSyncSnapshot) {
	if m == nil || m.persistence == nil {
		return
	}
	if snapshot.TraderID == "" {
		return
	}
	if snapshot.SyncedAt.IsZero() {
		snapshot.SyncedAt = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := m.persistence.RecordAccountSnapshot(ctx, snapshot)
	logPersistenceError(err, "account snapshot persistence failed", map[string]any{
		"trader_id": snapshot.TraderID,
		"equity":    snapshot.EquityUSD,
		"margin":    snapshot.MarginUsedUSD,
	})
}

func (m *Manager) recordAnalytics(snapshot AnalyticsSnapshot) {
	if m == nil || m.persistence == nil || strings.TrimSpace(snapshot.TraderID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := m.persistence.RecordAnalytics(ctx, snapshot)
	logPersistenceError(err, "analytics persistence failed", map[string]any{
		"trader_id": snapshot.TraderID,
		"win_rate":  snapshot.WinRate,
		"pnl_pct":   snapshot.TotalPnLPct,
	})
}

func (m *Manager) writeJournalRecord(t *VirtualTrader, ectx *executorpkg.Context, out *executorpkg.FullDecision, decisionsJSON string, actions []map[string]any, callErr error, allOK bool) error {
	if t == nil || ectx == nil {
		return nil
	}
	if !t.JournalEnabled || t.Journal == nil {
		return nil
	}
	acc := map[string]any{
		"equity":      ectx.Account.TotalEquity,
		"available":   ectx.Account.AvailableBalance,
		"used_margin": ectx.Account.MarginUsed,
		"used_pct":    ectx.Account.MarginUsedPct,
		"positions":   ectx.Account.PositionCount,
	}
	pos := make([]map[string]any, 0, len(ectx.Positions))
	for _, p := range ectx.Positions {
		pos = append(pos, map[string]any{
			"symbol": p.Symbol,
			"side":   p.Side,
			"qty":    p.Quantity,
			"lev":    p.Leverage,
			"entry":  p.EntryPrice,
			"mark":   p.MarkPrice,
			"upnl":   p.UnrealizedPnL,
			"liq":    p.LiquidationPrice,
		})
	}
	marketDigest := make(map[string]any, len(ectx.MarketDataMap))
	for sym, s := range ectx.MarketDataMap {
		if s == nil {
			continue
		}
		md := map[string]any{
			"price": s.Price.Last,
			"chg1h": s.Change.OneHour,
			"chg4h": s.Change.FourHour,
		}
		if s.OpenInterest != nil {
			md["oi_latest"] = s.OpenInterest.Latest
		}
		if s.Funding != nil {
			md["funding"] = s.Funding.Rate
		}
		marketDigest[sym] = md
	}

	cot := ""
	promptDigest := ""
	if out != nil {
		cot = out.CoTTrace
		if s := strings.TrimSpace(out.UserPrompt); s != "" {
			promptDigest = llm.DigestString(s)
		}
	}
	// candidates list as strings for compactness
	var cand []string
	for _, c := range ectx.CandidateCoins {
		cand = append(cand, c.Symbol)
	}
	rec := &journal.CycleRecord{
		TraderID:      t.ID,
		PromptDigest:  promptDigest,
		CoTTrace:      cot,
		DecisionsJSON: decisionsJSON,
		Account:       acc,
		Positions:     pos,
		Candidates:    cand,
		MarketDigest:  marketDigest,
		Actions:       actions,
		Success:       allOK && callErr == nil,
	}
	if callErr != nil {
		rec.ErrorMessage = callErr.Error()
	}
	var err error
	if t.Journal != nil {
		_, err = t.Journal.WriteCycle(rec)
	}
	m.recordDecisionCycle(DecisionCycleRecord{
		TraderID: t.ID,
		Cycle:    rec,
	})
	return err
}

// buildCloid creates a stable client order id for idempotent intent submission.
func buildCloid(traderID, symbol, action string, qty float64, now time.Time) string {
	// Bucket time to minute to avoid collision across cycles; include rounded qty to 6 dp.
	ts := now.UTC().Format("20060102T1504")
	raw := fmt.Sprintf("%s|%s|%s|%.6f|%s", traderID, strings.ToUpper(symbol), action, qty, ts)
	sum := sha256.Sum256([]byte(raw))
	// Hyperliquid requires CLOIDs to be 0x-prefixed 32 hex chars (16 bytes). Feeding human-readable
	// strings back into the API triggers HTTP 422s, so we hash and truncate to stay deterministic yet compliant.
	return "0x" + hex.EncodeToString(sum[:16])
}

// buildExecutorContext collects a richer snapshot for the executor prompt and validation.
func (m *Manager) buildExecutorContext(t *VirtualTrader) executorpkg.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1) Account and positions
	acctState, _ := t.ExchangeProvider.GetAccountState(ctx)
	positionsRaw, _ := t.ExchangeProvider.GetPositions(ctx)
	positionsRaw = m.filterPositionsForTrader(t.ID, positionsRaw)

	// Normalize account info
	account := executorpkg.AccountInfo{}
	if acctState != nil {
		account.TotalEquity = parseFloat(acctState.MarginSummary.AccountValue)
		account.MarginUsed = parseFloat(acctState.MarginSummary.TotalMarginUsed)
		account.AvailableBalance = account.TotalEquity - account.MarginUsed
		// Aggregate unrealized PnL
		for i := range acctState.AssetPositions {
			account.TotalPnL += parseFloat(acctState.AssetPositions[i].UnrealizedPnl)
		}
		if account.TotalEquity != 0 {
			account.MarginUsedPct = 100 * (account.MarginUsed / account.TotalEquity)
			account.TotalPnLPct = 100 * (account.TotalPnL / account.TotalEquity)
		}
	}

	// Normalize positions (first pass: collect symbols and static fields)
	positions := make([]executorpkg.PositionInfo, 0, len(positionsRaw))
	symbols := make(map[string]struct{})
	for i := range positionsRaw {
		p := positionsRaw[i]
		side := "long"
		qty := parseFloat(p.Szi)
		if qty < 0 {
			side = "short"
			qty = -qty
		}
		positions = append(positions, executorpkg.PositionInfo{
			Symbol:           p.Coin,
			Side:             side,
			EntryPrice:       parsePtrFloat(p.EntryPx),
			MarkPrice:        0,
			Quantity:         qty,
			Leverage:         p.Leverage.Value,
			UnrealizedPnL:    parseFloat(p.UnrealizedPnl),
			LiquidationPrice: parsePtrFloat(p.LiquidationPx),
		})
		symbols[p.Coin] = struct{}{}
	}
	account.PositionCount = len(positions)

	// 2) Candidate set (basic Top-N by |1h change|) and market snapshots
	candidates := m.selectCandidates(ctx, t, 0)
	snaps := map[string]*market.Snapshot{}
	for sym := range symbols {
		if s, err := t.MarketProvider.Snapshot(ctx, sym); err == nil && s != nil {
			snaps[sym] = s
		}
	}
	// Snapshots for candidates
	for _, c := range candidates {
		if _, ok := snaps[c.Symbol]; ok { // already fetched
			continue
		}
		if s, err := t.MarketProvider.Snapshot(ctx, c.Symbol); err == nil && s != nil {
			snaps[c.Symbol] = s
		}
	}

	// Second pass: enrich mark price and pnl pct from snapshots
	for i := range positions {
		pi := &positions[i]
		if pi.EntryPrice > 0 {
			if s, ok := snaps[pi.Symbol]; ok && s != nil && s.Price.Last > 0 {
				pi.MarkPrice = s.Price.Last
				pi.UnrealizedPnLPct = 100 * (pi.MarkPrice - pi.EntryPrice) / pi.EntryPrice
			}
		}
	}

	// 3) Asset meta (max leverage, precision) for present symbols
	assetMeta := map[string]executorpkg.AssetMeta{}
	if assets, err := t.MarketProvider.ListAssets(ctx); err == nil {
		for _, a := range assets {
			if _, want := symbols[a.Symbol]; !want {
				continue
			}
			ml := 0.0
			onlyIso := false
			if a.RawMetadata != nil {
				if v, ok := a.RawMetadata["maxLeverage"]; ok {
					switch x := v.(type) {
					case float64:
						ml = x
					case int:
						ml = float64(x)
					}
				}
				if v, ok := a.RawMetadata["onlyIsolated"]; ok {
					if b, ok := v.(bool); ok {
						onlyIso = b
					}
				}
			}
			assetMeta[a.Symbol] = executorpkg.AssetMeta{MaxLeverage: ml, Precision: a.Precision, OnlyIsolated: onlyIso}
		}
	}

	// 4) Compose executor context
	return executorpkg.Context{
		CurrentTime:       time.Now().UTC().Format(time.RFC3339),
		RuntimeMinutes:    0,
		CallCount:         0,
		Account:           account,
		Positions:         positions,
		CandidateCoins:    candidates,
		MarketDataMap:     snaps,
		OpenInterestMap:   nil,
		Performance:       t.Performance.ToExecutorView(),
		MajorCoinLeverage: t.RiskParams.MajorCoinLeverage,
		AltcoinLeverage:   t.RiskParams.AltcoinLeverage,
		AssetMeta:         assetMeta,
		// Optional guards sourced from trader risk params when enabled
		MaxMarginUsagePct: func() float64 {
			if t.ExecGuards.EnableMarginUsageGuard == nil || *t.ExecGuards.EnableMarginUsageGuard {
				return t.RiskParams.MaxMarginUsagePct
			}
			return 0
		}(),
		LiquidityThresholdUSD: func() float64 {
			if t.ExecGuards.EnableLiquidityGuard == nil || *t.ExecGuards.EnableLiquidityGuard {
				return t.ExecGuards.LiquidityThresholdUSD
			}
			return 0
		}(),
		BTCETHPositionValueMinMultiple: func() float64 {
			if t.ExecGuards.EnableValueBandGuard == nil || *t.ExecGuards.EnableValueBandGuard {
				return t.ExecGuards.BTCETHMinEquityMultiple
			}
			return 0
		}(),
		BTCETHPositionValueMaxMultiple: func() float64 {
			if t.ExecGuards.EnableValueBandGuard == nil || *t.ExecGuards.EnableValueBandGuard {
				return t.ExecGuards.BTCETHMaxEquityMultiple
			}
			return 0
		}(),
		AltPositionValueMinMultiple: func() float64 {
			if t.ExecGuards.EnableValueBandGuard == nil || *t.ExecGuards.EnableValueBandGuard {
				return t.ExecGuards.AltMinEquityMultiple
			}
			return 0
		}(),
		AltPositionValueMaxMultiple: func() float64 {
			if t.ExecGuards.EnableValueBandGuard == nil || *t.ExecGuards.EnableValueBandGuard {
				return t.ExecGuards.AltMaxEquityMultiple
			}
			return 0
		}(),
		CooldownAfterClose: func() time.Duration {
			if t.ExecGuards.EnableCooldownGuard == nil || *t.ExecGuards.EnableCooldownGuard {
				return t.ExecGuards.CooldownAfterClose
			}
			return 0
		}(),
	}
}

// selectCandidates picks up to limit candidates using a simple heuristic (|1h change| ranking).
// If limit == 0, uses ExecGuards.CandidateLimit (defaults to 10 when <=0). Applies liquidity threshold when enabled.
func (m *Manager) selectCandidates(ctx context.Context, t *VirtualTrader, limit int) []executorpkg.CandidateCoin {
	if limit <= 0 {
		limit = t.ExecGuards.CandidateLimit
		if limit <= 0 {
			limit = 10
		}
	}
	assets, err := t.MarketProvider.ListAssets(ctx)
	if err != nil || len(assets) == 0 {
		return nil
	}
	// Fetch snapshots (keep to first 200 assets to bound cost)
	type item struct {
		sym   string
		score float64
	}
	ranked := make([]item, 0, limit*3)
	count := 0
	for _, a := range assets {
		if !a.IsActive {
			continue
		}
		s, err := t.MarketProvider.Snapshot(ctx, a.Symbol)
		if err != nil || s == nil {
			continue
		}
		// Liquidity threshold if enabled
		if (t.ExecGuards.EnableLiquidityGuard == nil || *t.ExecGuards.EnableLiquidityGuard) && t.ExecGuards.LiquidityThresholdUSD > 0 {
			if s.OpenInterest != nil {
				if s.OpenInterest.Latest*s.Price.Last+1e-9 < t.ExecGuards.LiquidityThresholdUSD {
					continue
				}
			}
		}
		score := s.Change.OneHour
		if score < 0 {
			score = -score
		}
		ranked = append(ranked, item{sym: a.Symbol, score: score})
		count++
		if count >= 200 {
			break
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]executorpkg.CandidateCoin, 0, len(ranked))
	for _, it := range ranked {
		out = append(out, executorpkg.CandidateCoin{Symbol: it.sym, Sources: []string{"rank_1h_abs"}})
	}
	return out
}

// sortDecisionsCloseFirst returns decisions ordered by priority: close_* first, then open_*.
func sortDecisionsCloseFirst(ds []executorpkg.Decision) []executorpkg.Decision {
	out := make([]executorpkg.Decision, len(ds))
	copy(out, ds)
	sort.SliceStable(out, func(i, j int) bool {
		pri := priority(out[i].Action)
		prj := priority(out[j].Action)
		if pri != prj {
			return pri < prj
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

func priority(action string) int {
	switch action {
	case "close_long", "close_short":
		return 0
	case "open_long", "open_short":
		return 1
	default:
		return 2
	}
}

// capNewOpenDecisions limits the number of new open actions to remainingSlots; non-open actions are kept.
func capNewOpenDecisions(ds []executorpkg.Decision, remainingSlots int) []executorpkg.Decision {
	if remainingSlots <= 0 {
		// Keep only non-open actions (e.g., closes/hold/wait)
		out := make([]executorpkg.Decision, 0, len(ds))
		for _, d := range ds {
			if d.Action != "open_long" && d.Action != "open_short" {
				out = append(out, d)
			}
		}
		return out
	}
	opens := 0
	out := make([]executorpkg.Decision, 0, len(ds))
	for _, d := range ds {
		if d.Action == "open_long" || d.Action == "open_short" {
			if opens >= remainingSlots {
				continue
			}
			opens++
		}
		out = append(out, d)
	}
	return out
}
