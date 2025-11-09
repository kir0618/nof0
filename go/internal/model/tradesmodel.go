package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ TradesModel = (*customTradesModel)(nil)

// TradeRecord provides a nullable-safe representation of a trade row.
type TradeRecord struct {
	ID               string
	ModelID          string
	ExchangeProvider string
	Symbol           string
	Side             string
	Quantity         *float64
	Leverage         *float64
	Confidence       *float64
	EntryPrice       *float64
	EntryTsMs        int64
	EntrySz          *float64
	ExitPrice        *float64
	ExitTsMs         *int64
	RealizedNetPnl   *float64
}

type (
	// TradesModel is an interface to be customized, add more methods here,
	// and implement the added methods in customTradesModel.
	TradesModel interface {
		tradesModel
		RecentByModel(ctx context.Context, modelID string, limit int) ([]TradeRecord, error)
	}

	customTradesModel struct {
		*defaultTradesModel
	}
)

// NewTradesModel returns a model for the database table.
func NewTradesModel(conn sqlx.SqlConn, c cache.CacheConf, opts ...cache.Option) TradesModel {
	return &customTradesModel{
		defaultTradesModel: newTradesModel(conn, c, opts...),
	}
}

// RecentByModel returns trades for the given model ordered by entry timestamp
// descending. Limit defaults to 200 when non-positive.
func (m *customTradesModel) RecentByModel(ctx context.Context, modelID string, limit int) ([]TradeRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	const query = `
SELECT
    id,
    trader_id,
    symbol,
    side,
    close_ts_ms,
    detail
FROM public.trades
WHERE trader_id = $1
ORDER BY close_ts_ms DESC
LIMIT $2`

	var rows []Trades
	if err := m.QueryRowsNoCacheCtx(ctx, &rows, query, modelID, limit); err != nil {
		return nil, fmt.Errorf("trades.RecentByModel query: %w", err)
	}

	result := make([]TradeRecord, 0, len(rows))
	for i := range rows {
		result = append(result, buildTradeRecord(&rows[i]))
	}
	return result, nil
}

func buildTradeRecord(row *Trades) TradeRecord {
	rec := TradeRecord{
		ID:        row.Id,
		ModelID:   row.TraderId,
		Symbol:    row.Symbol,
		Side:      row.Side,
		EntryTsMs: 0,
	}
	if row.CloseTsMs > 0 {
		value := row.CloseTsMs
		rec.ExitTsMs = &value
	}
	detail := decodeTradeDetail(row.Detail)
	rec.ExchangeProvider = detail.Exchange.Provider
	rec.EntryTsMs = detail.Time.OpenTsMs
	rec.EntryPrice = floatPtr(detail.Prices.Entry)
	rec.ExitPrice = floatPtr(detail.Prices.Exit)
	rec.Quantity = floatPtr(detail.Quantity.Total)
	rec.EntrySz = floatPtr(detail.Quantity.Total)
	rec.Leverage = floatPtr(detail.Risk.Leverage)
	rec.Confidence = floatPtr(detail.Risk.Confidence)
	rec.RealizedNetPnl = floatPtr(detail.PnL.Net)
	return rec
}

func floatPtr(value float64) *float64 {
	if value == 0 {
		return nil
	}
	return &value
}

type tradeDetail struct {
	Time struct {
		OpenTsMs        int64 `json:"open_ts_ms"`
		CloseTsMs       int64 `json:"close_ts_ms"`
		DurationSeconds int64 `json:"duration_seconds"`
	} `json:"time"`
	Prices struct {
		Entry float64 `json:"entry"`
		Exit  float64 `json:"exit"`
	} `json:"prices"`
	Quantity struct {
		Total float64 `json:"total"`
	} `json:"quantity"`
	Risk struct {
		Confidence float64 `json:"confidence"`
		Leverage   float64 `json:"leverage"`
	} `json:"risk"`
	Exchange struct {
		Provider string `json:"provider"`
	} `json:"exchange"`
	PnL struct {
		Net float64 `json:"net"`
	} `json:"pnl"`
}

func decodeTradeDetail(raw string) tradeDetail {
	detail := tradeDetail{}
	if strings.TrimSpace(raw) == "" {
		return detail
	}
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return tradeDetail{}
	}
	return detail
}
