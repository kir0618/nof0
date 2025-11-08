package journal

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	executorpkg "nof0-api/pkg/executor"
	marketpkg "nof0-api/pkg/market"
)

// ParseDecisionsJSON converts the stored decisions JSON into executor.Decision objects.
func ParseDecisionsJSON(payload string) ([]executorpkg.Decision, error) {
	if strings.TrimSpace(payload) == "" {
		return nil, nil
	}
	var decisions []executorpkg.Decision
	if err := json.Unmarshal([]byte(payload), &decisions); err != nil {
		return nil, err
	}
	return decisions, nil
}

// BuildExecutorContext reconstructs an executor.Context from a stored cycle record.
func BuildExecutorContext(cfg *executorpkg.Config, rec *CycleRecord) executorpkg.Context {
	ctx := executorpkg.Context{}
	if rec == nil {
		return ctx
	}
	if !rec.Timestamp.IsZero() {
		ctx.CurrentTime = rec.Timestamp.UTC().Format(time.RFC3339)
	}
	if cfg != nil {
		ctx.MajorCoinLeverage = cfg.MajorCoinLeverage
		ctx.AltcoinLeverage = cfg.AltcoinLeverage
	}
	ctx.Account = mapAccountInfo(rec.Account)
	ctx.Positions = mapPositions(rec.Positions)
	ctx.Account.PositionCount = len(ctx.Positions)
	ctx.CandidateCoins = mapCandidates(rec.Candidates)
	ctx.MarketDataMap = mapMarketDigest(rec.MarketDigest)
	return ctx
}

func mapAccountInfo(data map[string]any) executorpkg.AccountInfo {
	if data == nil {
		return executorpkg.AccountInfo{}
	}
	return executorpkg.AccountInfo{
		TotalEquity:      asFloat(data["equity"]),
		AvailableBalance: asFloat(data["available"]),
		MarginUsed:       asFloat(data["used_margin"]),
		MarginUsedPct:    asFloat(data["used_pct"]),
		PositionCount:    int(asFloat(data["positions"])),
	}
}

func mapPositions(raw []map[string]any) []executorpkg.PositionInfo {
	if len(raw) == 0 {
		return nil
	}
	out := make([]executorpkg.PositionInfo, 0, len(raw))
	for _, item := range raw {
		if item == nil {
			continue
		}
		out = append(out, executorpkg.PositionInfo{
			Symbol:           asString(item["symbol"]),
			Side:             strings.ToLower(asString(item["side"])),
			Quantity:         asFloat(item["qty"]),
			Leverage:         int(asFloat(item["lev"])),
			EntryPrice:       asFloat(item["entry"]),
			MarkPrice:        asFloat(item["mark"]),
			UnrealizedPnL:    asFloat(item["upnl"]),
			LiquidationPrice: asFloat(item["liq"]),
		})
	}
	return out
}

func mapCandidates(raw []string) []executorpkg.CandidateCoin {
	if len(raw) == 0 {
		return nil
	}
	out := make([]executorpkg.CandidateCoin, 0, len(raw))
	for _, sym := range raw {
		sym = strings.ToUpper(strings.TrimSpace(sym))
		if sym == "" {
			continue
		}
		out = append(out, executorpkg.CandidateCoin{Symbol: sym})
	}
	return out
}

func mapMarketDigest(raw map[string]any) map[string]*marketpkg.Snapshot {
	if len(raw) == 0 {
		return map[string]*marketpkg.Snapshot{}
	}
	out := make(map[string]*marketpkg.Snapshot, len(raw))
	for sym, payload := range raw {
		mp, _ := payload.(map[string]any)
		snap := &marketpkg.Snapshot{
			Symbol: strings.ToUpper(sym),
			Price: marketpkg.PriceInfo{
				Last: asFloat(mp["price"]),
			},
			Change: marketpkg.ChangeInfo{
				OneHour:  asFloat(mp["chg1h"]),
				FourHour: asFloat(mp["chg4h"]),
			},
		}
		if oi := asFloat(mp["oi_latest"]); oi != 0 {
			snap.OpenInterest = &marketpkg.OpenInterestInfo{Latest: oi}
		}
		if funding := asFloat(mp["funding"]); funding != 0 {
			snap.Funding = &marketpkg.FundingInfo{Rate: funding}
		}
		out[strings.ToUpper(sym)] = snap
	}
	return out
}

func asFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	case string:
		f, _ := parseStringFloat(val)
		return f
	default:
		return 0
	}
}

func parseStringFloat(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return strconv.ParseFloat(s, 64)
}

func asString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		return fmt.Sprint(val)
	}
}
