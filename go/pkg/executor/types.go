package executor

import (
	"time"

	market "nof0-api/pkg/market"
)

// PositionInfo holds a normalized view of an open position.
type PositionInfo struct {
	Symbol           string
	Side             string // "long" or "short"
	EntryPrice       float64
	MarkPrice        float64
	Quantity         float64
	Leverage         int
	UnrealizedPnL    float64
	UnrealizedPnLPct float64
	LiquidationPrice float64
	MarginUsed       float64
	UpdateTime       int64
}

// AccountInfo summarizes account-level state.
type AccountInfo struct {
	TotalEquity      float64
	AvailableBalance float64
	TotalPnL         float64
	TotalPnLPct      float64
	MarginUsed       float64
	MarginUsedPct    float64
	PositionCount    int
}

// CandidateCoin is a pre-filtered candidate symbol with provenance labels.
type CandidateCoin struct {
	Symbol  string
	Sources []string
}

// OpenInterest is a placeholder for optional OI enrichment not covered by market.Snapshot.
type OpenInterest struct {
	Latest  float64
	Average float64
}

// PerformanceView is a read-only summary provided by Manager.
type PerformanceView struct {
	SharpeRatio      float64
	WinRate          float64
	TotalTrades      int
	RecentTradesRate float64
	UpdatedAt        time.Time
}

// AssetMeta captures exchange-specific constraints used for validation/formatting.
type AssetMeta struct {
	MaxLeverage  float64
	Precision    int
	OnlyIsolated bool
}

// Context aggregates all inputs required to form a decision.
type Context struct {
	CurrentTime       string
	RuntimeMinutes    int
	CallCount         int
	Account           AccountInfo
	Positions         []PositionInfo
	CandidateCoins    []CandidateCoin
	MarketDataMap     map[string]*market.Snapshot
	OpenInterestMap   map[string]*OpenInterest
	Performance       *PerformanceView
	MajorCoinLeverage int
	AltcoinLeverage   int
	AssetMeta         map[string]AssetMeta
	// Optional per-trader risk guards injected by Manager.
	MaxRiskPct         float64 // e.g., 3 means 3% of equity per trade
	MaxPositionSizeUSD float64 // hard cap per trade
	// Optional P0 guards (disabled when zero values):
	LiquidityThresholdUSD          float64              // require OI*Price ≥ threshold for new opens
	MaxMarginUsagePct              float64              // after new position margin
	BTCETHPositionValueMinMultiple float64              // min equity multiple for BTC/ETH position value
	BTCETHPositionValueMaxMultiple float64              // max equity multiple for BTC/ETH position value
	AltPositionValueMinMultiple    float64              // min equity multiple for alt position value
	AltPositionValueMaxMultiple    float64              // max equity multiple for alt position value
	RecentlyClosed                 map[string]time.Time // last close time per symbol (cooldown)
	CooldownAfterClose             time.Duration        // disallow new opens until this duration passes
}

// Decision captures a single trading action suggestion.
type Decision struct {
	Symbol                string
	Action                string
	Leverage              int
	PositionSizeUSD       float64
	EntryPrice            float64
	StopLoss              float64
	TakeProfit            float64
	Confidence            int
	RiskUSD               float64
	Reasoning             string
	InvalidationCondition string
}

// FullDecision is the full response produced by the executor.
type FullDecision struct {
	UserPrompt string
	CoTTrace   string
	Decisions  []Decision
	Timestamp  time.Time
}
