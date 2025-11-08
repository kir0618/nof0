package llm

import (
	"errors"
	"math"
	"strings"
	"sync"
	"time"
)

var ErrBudgetExceeded = errors.New("llm: budget exhausted for current period")

type BudgetSnapshot struct {
	UsedTokens        int64
	Limit             int64
	UsagePct          float64
	UsedCostUSD       float64
	AlertThresholdPct int
	AlertTriggered    bool
}

type BudgetGuard struct {
	cfg         *BudgetConfig
	mu          sync.Mutex
	usedTokens  int64
	usedCostUSD float64
	periodStart time.Time
	now         func() time.Time
}

func NewBudgetGuard(cfg *BudgetConfig) *BudgetGuard {
	if cfg == nil || cfg.DailyTokenLimit <= 0 {
		return nil
	}
	return &BudgetGuard{
		cfg: cfg.Clone(),
		now: time.Now,
	}
}

func (g *BudgetGuard) AllowAttempt() error {
	if g == nil || g.cfg == nil || g.cfg.DailyTokenLimit <= 0 {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetIfNeeded()
	if g.usedTokens >= g.cfg.DailyTokenLimit {
		if g.cfg.StrictEnforcement {
			return ErrBudgetExceeded
		}
		return nil
	}
	return nil
}

func (g *BudgetGuard) RecordUsage(model string, tokens int64) (BudgetSnapshot, error) {
	if g == nil || g.cfg == nil || g.cfg.DailyTokenLimit <= 0 || tokens <= 0 {
		return BudgetSnapshot{}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetIfNeeded()

	limit := g.cfg.DailyTokenLimit
	newTotal := g.usedTokens + tokens
	usagePct := percentage(newTotal, limit)
	costRate := g.costRate(model)
	g.usedTokens = newTotal
	g.usedCostUSD += float64(tokens) / 1_000_000.0 * costRate

	snapshot := BudgetSnapshot{
		UsedTokens:        g.usedTokens,
		Limit:             limit,
		UsagePct:          usagePct,
		UsedCostUSD:       g.usedCostUSD,
		AlertThresholdPct: g.cfg.AlertThresholdPct,
		AlertTriggered:    limit > 0 && g.cfg.AlertThresholdPct > 0 && usagePct >= float64(g.cfg.AlertThresholdPct),
	}

	if limit > 0 && newTotal > limit && g.cfg.StrictEnforcement {
		return snapshot, ErrBudgetExceeded
	}
	return snapshot, nil
}

func (g *BudgetGuard) resetIfNeeded() {
	now := g.nowUTC()
	currentPeriod := truncateDay(now)
	if g.periodStart.IsZero() || !currentPeriod.Equal(g.periodStart) {
		g.periodStart = currentPeriod
		g.usedTokens = 0
		g.usedCostUSD = 0
	}
}

func (g *BudgetGuard) nowUTC() time.Time {
	if g.now == nil {
		return time.Now().UTC()
	}
	return g.now().UTC()
}

func (g *BudgetGuard) costRate(model string) float64 {
	if g.cfg == nil || len(g.cfg.CostPerMillionTokens) == 0 {
		return 0
	}
	if rate, ok := g.cfg.CostPerMillionTokens[model]; ok {
		return rate
	}
	key := strings.ToLower(strings.TrimSpace(model))
	if rate, ok := g.cfg.CostPerMillionTokens[key]; ok {
		return rate
	}
	return 0
}

func percentage(value int64, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return math.Min(100, float64(value)/float64(limit)*100)
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
