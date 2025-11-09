package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ TraderConfigHistoryModel = (*customTraderConfigHistoryModel)(nil)

type (
	// TraderConfigHistoryModel exposes helpers for trader_config_history table.
	TraderConfigHistoryModel interface {
		traderConfigHistoryModel
		ListByTrader(ctx context.Context, traderID string, limit int) ([]*TraderConfigHistory, error)
		InsertTx(ctx context.Context, tx *sql.Tx, data *TraderConfigHistory) error
	}

	customTraderConfigHistoryModel struct {
		*defaultTraderConfigHistoryModel
	}
)

// NewTraderConfigHistoryModel returns a model for trader_config_history table.
func NewTraderConfigHistoryModel(conn sqlx.SqlConn, c cache.CacheConf, opts ...cache.Option) TraderConfigHistoryModel {
	return &customTraderConfigHistoryModel{
		defaultTraderConfigHistoryModel: newTraderConfigHistoryModel(conn, c, opts...),
	}
}

func (m *customTraderConfigHistoryModel) ListByTrader(ctx context.Context, traderID string, limit int) ([]*TraderConfigHistory, error) {
	query := fmt.Sprintf("select %s from %s where trader_id = $1 order by version desc limit $2", traderConfigHistoryRows, m.tableName())
	var rows []*TraderConfigHistory
	if limit <= 0 {
		limit = 50
	}
	if err := m.QueryRowsNoCacheCtx(ctx, &rows, query, traderID, limit); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *customTraderConfigHistoryModel) InsertTx(ctx context.Context, tx *sql.Tx, data *TraderConfigHistory) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	query := fmt.Sprintf("insert into %s (%s) values ($1, $2, $3, $4, $5, $6, $7)", m.tableName(), traderConfigHistoryRowsExpectAutoSet)
	_, err := tx.ExecContext(ctx, query, data.TraderId, data.Version, data.ConfigSnapshot, data.ChangedFields, data.ChangeReason, data.ChangedBy, data.ChangedAt)
	return err
}
