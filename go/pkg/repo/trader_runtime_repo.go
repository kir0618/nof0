package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"nof0-api/internal/model"
)

// RuntimeStateDetail mirrors the JSONB payload described in the blueprint. Fields
// are optional so the repository can store sparse updates.
type RuntimeStateDetail struct {
	Decision    *RuntimeDecisionDetail    `json:"decision,omitempty"`
	Pause       *RuntimePauseDetail       `json:"pause,omitempty"`
	Allocation  *RuntimeAllocationDetail  `json:"allocation,omitempty"`
	Performance *RuntimePerformanceDetail `json:"performance,omitempty"`
}

type RuntimeDecisionDetail struct {
	LastAt *time.Time `json:"last_at,omitempty"`
	NextAt *time.Time `json:"next_at,omitempty"`
}

type RuntimePauseDetail struct {
	Until  *time.Time `json:"until,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

type RuntimeAllocationDetail struct {
	EquityUSD          float64 `json:"equity_usd,omitempty"`
	UsedMarginUSD      float64 `json:"used_margin_usd,omitempty"`
	AvailableMarginUSD float64 `json:"available_margin_usd,omitempty"`
}

type RuntimePerformanceDetail struct {
	SharpeRatio float64 `json:"sharpe_ratio,omitempty"`
	TotalPnLUSD float64 `json:"total_pnl_usd,omitempty"`
}

// RuntimeStateRecord encapsulates an upsert payload for trader_runtime_state.
type RuntimeStateRecord struct {
	TraderID            string
	ActiveConfigVersion int64
	IsRunning           bool
	Detail              RuntimeStateDetail
}

// RuntimeStateSnapshot augments a runtime record with metadata.
type RuntimeStateSnapshot struct {
	RuntimeStateRecord
	UpdatedAt time.Time
}

// SymbolCooldownDetail captures structured metadata for cooldown rows.
type SymbolCooldownDetail struct {
	Reason             string    `json:"reason,omitempty"`
	ConsecutiveLosses  int       `json:"consecutive_losses,omitempty"`
	LastLossPnL        float64   `json:"last_loss_pnl,omitempty"`
	TriggeredAt        time.Time `json:"triggered_at,omitempty"`
	LastPositionClosed time.Time `json:"last_position_closed,omitempty"`
}

type SymbolCooldownRecord struct {
	TraderID string
	Symbol   string
	Until    time.Time
	Detail   SymbolCooldownDetail
}

// TraderRuntimeRepository persists runtime state and cooldowns for traders.
type TraderRuntimeRepository interface {
	UpsertState(ctx context.Context, record RuntimeStateRecord) error
	UpsertCooldown(ctx context.Context, record SymbolCooldownRecord) error
	GetState(ctx context.Context, traderID string) (*RuntimeStateSnapshot, error)
	ListCooldowns(ctx context.Context, traderID string) ([]SymbolCooldownRecord, error)
}

type traderRuntimeRepo struct {
	runtimeModel  model.TraderRuntimeStateModel
	cooldownModel model.TraderSymbolCooldownsModel
}

func NewTraderRuntimeRepository(
	runtimeModel model.TraderRuntimeStateModel,
	cooldownModel model.TraderSymbolCooldownsModel,
) TraderRuntimeRepository {
	if runtimeModel == nil && cooldownModel == nil {
		return nil
	}
	return &traderRuntimeRepo{
		runtimeModel:  runtimeModel,
		cooldownModel: cooldownModel,
	}
}

func (r *traderRuntimeRepo) UpsertState(ctx context.Context, record RuntimeStateRecord) error {
	if r == nil || r.runtimeModel == nil {
		return nil
	}
	if record.TraderID == "" {
		return fmt.Errorf("runtime repo: trader id required")
	}
	detailJSON, err := encodeJSON(record.Detail)
	if err != nil {
		return fmt.Errorf("runtime repo: marshal detail: %w", err)
	}
	row := &model.TraderRuntimeState{
		TraderId:            record.TraderID,
		ActiveConfigVersion: record.ActiveConfigVersion,
		IsRunning:           record.IsRunning,
		Detail:              detailJSON,
	}
	return r.runtimeModel.Upsert(ctx, row)
}

func (r *traderRuntimeRepo) UpsertCooldown(ctx context.Context, record SymbolCooldownRecord) error {
	if r == nil || r.cooldownModel == nil {
		return nil
	}
	if record.TraderID == "" || record.Symbol == "" {
		return fmt.Errorf("runtime repo: trader id and symbol required")
	}
	detailJSON, err := encodeJSON(record.Detail)
	if err != nil {
		return fmt.Errorf("runtime repo: marshal cooldown detail: %w", err)
	}
	row := &model.TraderSymbolCooldowns{
		TraderId:      record.TraderID,
		Symbol:        record.Symbol,
		CooldownUntil: record.Until.UTC(),
		Detail:        detailJSON,
	}
	return r.cooldownModel.Upsert(ctx, row)
}

func (r *traderRuntimeRepo) GetState(ctx context.Context, traderID string) (*RuntimeStateSnapshot, error) {
	if r == nil || r.runtimeModel == nil || strings.TrimSpace(traderID) == "" {
		return nil, nil
	}
	row, err := r.runtimeModel.FindOne(ctx, traderID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	detail, err := decodeRuntimeDetail(row.Detail)
	if err != nil {
		return nil, err
	}
	snap := &RuntimeStateSnapshot{
		RuntimeStateRecord: RuntimeStateRecord{
			TraderID:            row.TraderId,
			ActiveConfigVersion: row.ActiveConfigVersion,
			IsRunning:           row.IsRunning,
			Detail:              detail,
		},
		UpdatedAt: row.UpdatedAt,
	}
	return snap, nil
}

func (r *traderRuntimeRepo) ListCooldowns(ctx context.Context, traderID string) ([]SymbolCooldownRecord, error) {
	if r == nil || r.cooldownModel == nil || strings.TrimSpace(traderID) == "" {
		return nil, nil
	}
	rows, err := r.cooldownModel.ListByTrader(ctx, traderID)
	if err != nil {
		return nil, err
	}
	result := make([]SymbolCooldownRecord, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		detail, err := decodeCooldownDetail(row.Detail)
		if err != nil {
			return nil, err
		}
		record := SymbolCooldownRecord{
			TraderID: row.TraderId,
			Symbol:   row.Symbol,
			Until:    row.CooldownUntil,
			Detail:   detail,
		}
		result = append(result, record)
	}
	return result, nil
}

func encodeJSON(payload any) (string, error) {
	if payload == nil {
		return "{}", nil
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "{}", nil
	}
	return string(trimmed), nil
}

func decodeRuntimeDetail(raw string) (RuntimeStateDetail, error) {
	detail := RuntimeStateDetail{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return detail, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &detail); err != nil {
		return detail, err
	}
	return detail, nil
}

func decodeCooldownDetail(raw string) (SymbolCooldownDetail, error) {
	detail := SymbolCooldownDetail{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return detail, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &detail); err != nil {
		return detail, err
	}
	return detail, nil
}
