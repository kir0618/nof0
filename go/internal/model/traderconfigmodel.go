package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/sqlc"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ TraderConfigModel = (*customTraderConfigModel)(nil)

type (
	// TraderConfigModel defines custom behavior for trader_config table.
	TraderConfigModel interface {
		traderConfigModel
		FindByVersion(ctx context.Context, traderID string, version int64) (*TraderConfig, error)
		ListAll(ctx context.Context) ([]*TraderConfig, error)
		InsertTx(ctx context.Context, tx *sql.Tx, data *TraderConfig) error
		UpdateTx(ctx context.Context, tx *sql.Tx, data *TraderConfig) error
	}

	customTraderConfigModel struct {
		*defaultTraderConfigModel
	}
)

// NewTraderConfigModel returns a model for the trader_config table.
func NewTraderConfigModel(conn sqlx.SqlConn, c cache.CacheConf, opts ...cache.Option) TraderConfigModel {
	return &customTraderConfigModel{
		defaultTraderConfigModel: newTraderConfigModel(conn, c, opts...),
	}
}

func (m *customTraderConfigModel) FindByVersion(ctx context.Context, traderID string, version int64) (*TraderConfig, error) {
	query := fmt.Sprintf("select %s from %s where id = $1 and version = $2 limit 1", traderConfigRows, m.tableName())
	var resp TraderConfig
	if err := m.QueryRowNoCacheCtx(ctx, &resp, query, traderID, version); err != nil {
		if err == sqlc.ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &resp, nil
}

func (m *customTraderConfigModel) ListAll(ctx context.Context) ([]*TraderConfig, error) {
	query := fmt.Sprintf("select %s from %s order by id", traderConfigRows, m.tableName())
	var rows []*TraderConfig
	if err := m.QueryRowsNoCacheCtx(ctx, &rows, query); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *customTraderConfigModel) InsertTx(ctx context.Context, tx *sql.Tx, data *TraderConfig) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	query := fmt.Sprintf("insert into %s (%s) values ($1, $2, $3, $4, $5, $6, $7)", m.tableName(), traderConfigRowsExpectAutoSet)
	if _, err := tx.ExecContext(ctx, query, data.Id, data.Version, data.ExchangeProvider, data.MarketProvider, data.AllocationPct, data.Detail, data.CreatedBy); err != nil {
		return err
	}
	return m.DelCacheCtx(ctx, fmt.Sprintf("%s%v", cacheTraderConfigIdPrefix, data.Id))
}

func (m *customTraderConfigModel) UpdateTx(ctx context.Context, tx *sql.Tx, data *TraderConfig) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	query := fmt.Sprintf(`update %s
set version = $2,
    exchange_provider = $3,
    market_provider = $4,
    allocation_pct = $5,
    detail = $6,
    updated_at = NOW()
where id = $1`, m.tableName())
	if _, err := tx.ExecContext(ctx, query, data.Id, data.Version, data.ExchangeProvider, data.MarketProvider, data.AllocationPct, data.Detail); err != nil {
		return err
	}
	return m.DelCacheCtx(ctx, fmt.Sprintf("%s%v", cacheTraderConfigIdPrefix, data.Id))
}
