package model

import (
	"context"
	"fmt"

	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ TraderSymbolCooldownsModel = (*customTraderSymbolCooldownsModel)(nil)

type (
	// TraderSymbolCooldownsModel can be extended with additional helpers later.
	TraderSymbolCooldownsModel interface {
		traderSymbolCooldownsModel
		Upsert(ctx context.Context, data *TraderSymbolCooldowns) error
		ListByTrader(ctx context.Context, traderId string) ([]*TraderSymbolCooldowns, error)
	}

	customTraderSymbolCooldownsModel struct {
		*defaultTraderSymbolCooldownsModel
	}
)

// NewTraderSymbolCooldownsModel returns a model for trader_symbol_cooldowns table.
func NewTraderSymbolCooldownsModel(conn sqlx.SqlConn, c cache.CacheConf, opts ...cache.Option) TraderSymbolCooldownsModel {
	return &customTraderSymbolCooldownsModel{
		defaultTraderSymbolCooldownsModel: newTraderSymbolCooldownsModel(conn, c, opts...),
	}
}

// Upsert writes the cooldown entry, replacing existing state for the trader/symbol pair.
func (m *customTraderSymbolCooldownsModel) Upsert(ctx context.Context, data *TraderSymbolCooldowns) error {
	if data == nil {
		return fmt.Errorf("trader_symbol_cooldowns: nil data")
	}
	query := fmt.Sprintf(`
INSERT INTO %s (trader_id, symbol, cooldown_until, detail)
VALUES ($1, $2, $3, $4)
ON CONFLICT (trader_id, symbol) DO UPDATE
SET cooldown_until = EXCLUDED.cooldown_until,
    detail = EXCLUDED.detail
`, m.tableName())
	_, err := m.ExecNoCacheCtx(ctx, query, data.TraderId, data.Symbol, data.CooldownUntil, data.Detail)
	return err
}

// ListByTrader returns all cooldown rows for a trader.
func (m *customTraderSymbolCooldownsModel) ListByTrader(ctx context.Context, traderId string) ([]*TraderSymbolCooldowns, error) {
	query := fmt.Sprintf("select %s from %s where trader_id = $1", traderSymbolCooldownsRows, m.tableName())
	var rows []*TraderSymbolCooldowns
	if err := m.QueryRowsNoCacheCtx(ctx, &rows, query, traderId); err != nil {
		return nil, err
	}
	return rows, nil
}
