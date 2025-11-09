package manager

import (
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"nof0-api/pkg/exchange"
	executorpkg "nof0-api/pkg/executor"
	"nof0-api/pkg/journal"
	"nof0-api/pkg/market"
)

// TraderState captures a trader's lifecycle state.
type TraderState string

const (
	TraderStateRunning TraderState = "running"
	TraderStatePaused  TraderState = "paused"
	TraderStateStopped TraderState = "stopped"
	TraderStateError   TraderState = "error"
)

// ResourceAllocation tracks funds and margin usage assigned to a trader.
type ResourceAllocation struct {
	AllocatedEquityUSD  float64 // Assigned equity slice in USD
	AllocationPct       float64 // Share of total equity [0..100]
	CurrentEquityUSD    float64 // Live equity (updated via sync)
	AvailableBalanceUSD float64 // Available balance (updated via sync)
	MarginUsedUSD       float64 // Margin currently used
	UnrealizedPnLUSD    float64 // Unrealized PnL
}

// IsOverAllocated reports whether live usage exceeds the assigned slice.
func (r ResourceAllocation) IsOverAllocated() bool {
	if r.AllocatedEquityUSD <= 0 {
		return false
	}
	return r.MarginUsedUSD > r.AllocatedEquityUSD
}

// PerformanceMetrics aggregates trader-level KPIs.
type PerformanceMetrics struct {
	TotalPnLUSD        float64
	TotalPnLPct        float64
	SharpeRatio        float64
	WinRate            float64
	TotalTrades        int
	WinningTrades      int
	LosingTrades       int
	AvgWinUSD          float64
	AvgLossUSD         float64
	MaxDrawdownPct     float64
	CurrentDrawdownPct float64
	UpdatedAt          time.Time
}

// VirtualPosition mirrors the notional exposure a trader believes it owns on the
// shared exchange account. This is reconciled against physical positions to
// guarantee isolation across virtual traders.
type VirtualPosition struct {
	Symbol      string
	Side        string
	Quantity    float64
	NotionalUSD float64
	EntryPrice  float64
	Leverage    int
	OpenedAt    time.Time
	UpdatedAt   time.Time
}

// ToExecutorView converts to the compact view used by the executor prompts.
func (p *PerformanceMetrics) ToExecutorView() *executorpkg.PerformanceView {
	if p == nil {
		return nil
	}
	return &executorpkg.PerformanceView{
		SharpeRatio:      p.SharpeRatio,
		WinRate:          p.WinRate,
		TotalTrades:      p.TotalTrades,
		RecentTradesRate: 0, // TODO: compute based on recent execution history
		UpdatedAt:        p.UpdatedAt,
	}
}

// VirtualTrader models a strategy instance bound to providers and an executor.
type VirtualTrader struct {
	mu sync.RWMutex

	ID                   string
	Name                 string
	Exchange             string
	ExchangeProvider     exchange.Provider
	MarketProvider       market.Provider
	Executor             executorpkg.Executor
	PromptTemplate       string
	OrderStyle           OrderStyle
	MarketIOCSlippageBps float64
	RiskParams           RiskParameters
	ExecGuards           ExecGuards
	ResourceAlloc        ResourceAllocation
	ConfigVersion        int64
	State                TraderState
	Performance          *PerformanceMetrics
	LastDecisionAt       time.Time
	DecisionInterval     time.Duration
	CreatedAt            time.Time
	UpdatedAt            time.Time
	VirtualPositions     map[string]VirtualPosition
	// Cooldown map tracks last successful close time per symbol
	Cooldown map[string]time.Time
	// Decision journal writer (per trader)
	Journal *journal.Writer
	// Journal flags
	JournalEnabled bool
	// Pause window for Sharpe gating
	PauseUntil time.Time
}

// Start transitions the trader into running state.
func (t *VirtualTrader) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State == TraderStateRunning {
		return nil
	}
	t.State = TraderStateRunning
	t.UpdatedAt = time.Now()
	logx.Infof("trader %s started", t.ID)
	return nil
}

// Pause moves the trader into paused state.
func (t *VirtualTrader) Pause() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State == TraderStateStopped {
		return nil
	}
	t.State = TraderStatePaused
	t.UpdatedAt = time.Now()
	logx.Infof("trader %s paused", t.ID)
	return nil
}

// Resume sets the state back to running.
func (t *VirtualTrader) Resume() error { return t.Start() }

// Stop transitions the trader into stopped state.
func (t *VirtualTrader) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = TraderStateStopped
	t.UpdatedAt = time.Now()
	logx.Infof("trader %s stopped", t.ID)
	return nil
}

// IsActive returns true when trader should participate in scheduling.
func (t *VirtualTrader) IsActive() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.State == TraderStateRunning
}

// ShouldMakeDecision determines whether a decision should be requested now.
func (t *VirtualTrader) ShouldMakeDecision() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.State != TraderStateRunning {
		return false
	}
	if !t.PauseUntil.IsZero() && time.Now().Before(t.PauseUntil) {
		return false
	}
	if t.DecisionInterval <= 0 {
		return true
	}
	if t.LastDecisionAt.IsZero() {
		return true
	}
	return time.Since(t.LastDecisionAt) >= t.DecisionInterval
}

// RecordDecision updates timestamps after a decision round completes.
func (t *VirtualTrader) RecordDecision(ts time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ts.IsZero() {
		ts = time.Now()
	}
	t.LastDecisionAt = ts
	t.UpdatedAt = ts
}
