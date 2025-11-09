package model

import (
	"context"
	"fmt"

	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ TraderRuntimeStateModel = (*customTraderRuntimeStateModel)(nil)

type (
	// TraderRuntimeStateModel can be customized with additional helpers later.
	TraderRuntimeStateModel interface {
		traderRuntimeStateModel
		Upsert(ctx context.Context, data *TraderRuntimeState) error
	}

	customTraderRuntimeStateModel struct {
		*defaultTraderRuntimeStateModel
	}
)

// NewTraderRuntimeStateModel returns a model for trader_runtime_state table.
func NewTraderRuntimeStateModel(conn sqlx.SqlConn, c cache.CacheConf, opts ...cache.Option) TraderRuntimeStateModel {
	return &customTraderRuntimeStateModel{
		defaultTraderRuntimeStateModel: newTraderRuntimeStateModel(conn, c, opts...),
	}
}

// Upsert inserts or updates a runtime state row atomically using Postgres upsert semantics.
func (m *customTraderRuntimeStateModel) Upsert(ctx context.Context, data *TraderRuntimeState) error {
	if data == nil {
		return fmt.Errorf("trader_runtime_state: nil data")
	}
	query := fmt.Sprintf(`
INSERT INTO %s (trader_id, active_config_version, is_running, detail, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (trader_id) DO UPDATE
SET active_config_version = EXCLUDED.active_config_version,
    is_running = EXCLUDED.is_running,
    detail = EXCLUDED.detail,
    updated_at = NOW()
`, m.tableName())
	_, err := m.ExecNoCacheCtx(ctx, query, data.TraderId, data.ActiveConfigVersion, data.IsRunning, data.Detail)
	return err
}
