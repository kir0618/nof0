package backtest

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"nof0-api/pkg/exchange"
	"nof0-api/pkg/exchange/sim"
	executorpkg "nof0-api/pkg/executor"
	"nof0-api/pkg/journal"
	marketpkg "nof0-api/pkg/market"
)

type journalSession struct {
	records []*journal.CycleRecord
	idx     int
	current *journal.CycleRecord
}

func newJournalSession(records []*journal.CycleRecord) *journalSession {
	return &journalSession{records: records}
}

func (s *journalSession) advance() (*journal.CycleRecord, bool) {
	if s.idx >= len(s.records) {
		s.current = nil
		return nil, false
	}
	rec := s.records[s.idx]
	s.idx++
	s.current = rec
	return rec, true
}

func (s *journalSession) currentRecord() *journal.CycleRecord {
	return s.current
}

type journalFeeder struct {
	session *journalSession
	symbol  string
}

func newJournalFeeder(session *journalSession, symbol string) *journalFeeder {
	return &journalFeeder{session: session, symbol: strings.ToUpper(symbol)}
}

func (f *journalFeeder) Next(ctx context.Context, symbol string) (*marketpkg.Snapshot, bool, error) {
	if strings.ToUpper(symbol) != f.symbol {
		return nil, false, fmt.Errorf("journal feeder: symbol mismatch %s != %s", symbol, f.symbol)
	}
	rec, ok := f.session.advance()
	if !ok {
		return nil, false, nil
	}
	snap := snapshotFromDigest(rec, f.symbol)
	return snap, true, nil
}

func snapshotFromDigest(rec *journal.CycleRecord, symbol string) *marketpkg.Snapshot {
	symKey := strings.ToUpper(symbol)
	if rec != nil {
		if raw, ok := rec.MarketDigest[symKey]; ok {
			if mp, ok := raw.(map[string]any); ok {
				snap := &marketpkg.Snapshot{Symbol: symKey}
				if v := toFloat(mp["price"]); v > 0 {
					snap.Price.Last = v
				}
				snap.Change.OneHour = toFloat(mp["chg1h"])
				snap.Change.FourHour = toFloat(mp["chg4h"])
				return snap
			}
		}
	}
	return &marketpkg.Snapshot{Symbol: symKey, Price: marketpkg.PriceInfo{Last: 0}}
}

type journalStrategy struct {
	session    *journalSession
	symbol     string
	provider   exchange.Provider
	assetCache map[string]int
}

func newJournalStrategy(session *journalSession, symbol string, provider exchange.Provider) *journalStrategy {
	return &journalStrategy{
		session:    session,
		symbol:     strings.ToUpper(symbol),
		provider:   provider,
		assetCache: make(map[string]int),
	}
}

func (s *journalStrategy) Decide(ctx context.Context, snap *marketpkg.Snapshot) ([]exchange.Order, error) {
	rec := s.session.currentRecord()
	if rec == nil {
		return nil, nil
	}
	decisions, err := journal.ParseDecisionsJSON(rec.DecisionsJSON)
	if err != nil {
		return nil, err
	}
	if len(decisions) == 0 {
		return nil, nil
	}
	price := snap.Price.Last
	if price <= 0 {
		price = 0
	}
	var orders []exchange.Order
	for _, d := range decisions {
		if !strings.EqualFold(d.Symbol, s.symbol) {
			continue
		}
		ord, ok := s.decisionToOrder(ctx, d, price)
		if ok {
			orders = append(orders, ord)
		}
	}
	return orders, nil
}

func (s *journalStrategy) decisionToOrder(ctx context.Context, d executorpkg.Decision, fallbackPrice float64) (exchange.Order, bool) {
	price := d.EntryPrice
	if fallbackPrice > 0 {
		price = fallbackPrice
	}
	if price <= 0 {
		return exchange.Order{}, false
	}
	notional := d.PositionSizeUSD
	if notional <= 0 {
		return exchange.Order{}, false
	}
	qty := notional / price
	if qty <= 0 {
		return exchange.Order{}, false
	}
	asset, err := s.ensureAsset(ctx, d.Symbol)
	if err != nil {
		return exchange.Order{}, false
	}
	action := strings.ToLower(strings.TrimSpace(d.Action))
	var isBuy bool
	var reduceOnly bool
	switch action {
	case "open_long":
		isBuy = true
	case "open_short":
		isBuy = false
	case "close_long":
		isBuy = false
		reduceOnly = true
	case "close_short":
		isBuy = true
		reduceOnly = true
	default:
		return exchange.Order{}, false
	}
	order := exchange.Order{
		Asset:      asset,
		IsBuy:      isBuy,
		LimitPx:    formatDecimal(price),
		Sz:         formatDecimal(qty),
		ReduceOnly: reduceOnly,
		OrderType:  exchange.OrderType{Limit: &exchange.LimitOrderType{TIF: "Ioc"}},
	}
	return order, true
}

func (s *journalStrategy) ensureAsset(ctx context.Context, symbol string) (int, error) {
	key := strings.ToUpper(symbol)
	if id, ok := s.assetCache[key]; ok {
		return id, nil
	}
	id, err := s.provider.GetAssetIndex(ctx, key)
	if err != nil {
		return 0, err
	}
	s.assetCache[key] = id
	return id, nil
}

// RunJournalReplay replays recorded journal cycles for a single symbol using the backtest engine.
func RunJournalReplay(ctx context.Context, records []*journal.CycleRecord, symbol string, initialEquity float64) (*Result, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("no journal cycles provided")
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required for replay")
	}
	session := newJournalSession(records)
	feeder := newJournalFeeder(session, symbol)
	provider := sim.New()
	strategy := newJournalStrategy(session, symbol, provider)
	engine := &Engine{
		Feeder:        feeder,
		Strategy:      strategy,
		Exch:          provider,
		Symbol:        symbol,
		InitialEquity: initialEquity,
	}
	return engine.Run(ctx)
}

func formatDecimal(v float64) string {
	return fmt.Sprintf("%.8f", v)
}

func toFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err == nil {
			return f
		}
	default:
		return 0
	}
	return 0
}
