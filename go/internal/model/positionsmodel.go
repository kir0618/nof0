package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ PositionsModel = (*customPositionsModel)(nil)

// PositionRecord provides a nullable-safe representation of a row in the
// positions table. Nullable numeric fields become pointers so callers can
// easily detect unset values while working with idiomatic Go types.
type PositionRecord struct {
	ID               string
	TraderID         string
	ExchangeProvider string
	Symbol           string
	Side             string
	Status           string
	EntryTimeMs      int64
	EntryPrice       float64
	Quantity         float64
	Leverage         *float64
	Confidence       *float64
	RiskUsd          *float64
	UnrealizedPnl    *float64
}

type (
	// PositionsModel is an interface to be customized, add more methods here,
	// and implement the added methods in customPositionsModel.
	PositionsModel interface {
		positionsModel
		ActiveByModels(ctx context.Context, modelIDs []string) (map[string][]PositionRecord, error)
	}

	customPositionsModel struct {
		*defaultPositionsModel
	}
)

// NewPositionsModel returns a model for the database table.
func NewPositionsModel(conn sqlx.SqlConn, c cache.CacheConf, opts ...cache.Option) PositionsModel {
	return &customPositionsModel{
		defaultPositionsModel: newPositionsModel(conn, c, opts...),
	}
}

// ActiveByModels returns all open positions grouped by trader ID. When modelIDs
// is empty, it returns every open position.
func (m *customPositionsModel) ActiveByModels(ctx context.Context, modelIDs []string) (map[string][]PositionRecord, error) {
	const baseQuery = `
SELECT
    id,
    trader_id,
    symbol,
    side,
    status,
    detail
FROM public.positions
WHERE status = 'open'
%s
ORDER BY trader_id, symbol`

	var (
		args   []any
		clause string
	)
	if len(modelIDs) > 0 {
		clause = "AND trader_id = ANY($1)"
		args = append(args, pq.Array(modelIDs))
	}

	finalQuery := fmt.Sprintf(baseQuery, clause)

	var rows []Positions
	if err := m.QueryRowsNoCacheCtx(ctx, &rows, finalQuery, args...); err != nil {
		return nil, fmt.Errorf("positions.ActiveByModels query: %w", err)
	}

	result := make(map[string][]PositionRecord)
	for i := range rows {
		rec := buildPositionRecord(&rows[i])
		result[rows[i].TraderId] = append(result[rows[i].TraderId], rec)
	}
	return result, nil
}

func buildPositionRecord(row *Positions) PositionRecord {
	detail := decodePositionDetail(row.Detail)
	rec := PositionRecord{
		ID:               row.Id,
		TraderID:         row.TraderId,
		ExchangeProvider: detail.Exchange.Provider,
		Symbol:           row.Symbol,
		Side:             row.Side,
		Status:           row.Status,
		EntryTimeMs:      detail.Entry.TimeMs,
		EntryPrice:       detail.Entry.Price,
		Quantity:         detail.Entry.Quantity,
	}
	if detail.Entry.Leverage != 0 {
		value := detail.Entry.Leverage
		rec.Leverage = &value
	}
	if detail.Risk.Confidence != 0 {
		value := detail.Risk.Confidence
		rec.Confidence = &value
	}
	if detail.Risk.RiskUSD != 0 {
		value := detail.Risk.RiskUSD
		rec.RiskUsd = &value
	}
	if detail.Metrics.UnrealizedPnL != 0 {
		value := detail.Metrics.UnrealizedPnL
		rec.UnrealizedPnl = &value
	}
	return rec
}

type positionDetail struct {
	Entry    positionEntryDetail    `json:"entry"`
	Exchange positionExchangeDetail `json:"exchange"`
	Risk     positionRiskDetail     `json:"risk"`
	Metrics  positionMetricsDetail  `json:"metrics"`
}

type positionEntryDetail struct {
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
	TimeMs   int64   `json:"time_ms"`
	Leverage float64 `json:"leverage"`
}

type positionExchangeDetail struct {
	Provider string `json:"provider"`
}

type positionRiskDetail struct {
	Confidence float64 `json:"confidence"`
	RiskUSD    float64 `json:"risk_usd"`
}

type positionMetricsDetail struct {
	UnrealizedPnL float64 `json:"unrealized_pnl"`
}

func decodePositionDetail(raw string) positionDetail {
	if strings.TrimSpace(raw) == "" {
		return positionDetail{}
	}
	var detail positionDetail
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return positionDetail{}
	}
	return detail
}
