package repo

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"nof0-api/internal/model"
)

// TraderConfigRecord represents a single trader configuration payload that
// should be persisted to Postgres. Detail must be valid JSON representing the
// full manager.TraderConfig payload as described in the blueprint docs.
type TraderConfigRecord struct {
	ID               string
	ExchangeProvider string
	MarketProvider   string
	AllocationPct    float64
	Detail           json.RawMessage
	CreatedBy        string
	ChangeReason     string
}

// ConfigSyncResult captures the outcome of a sync run so callers can log or
// assert behavior in tests.
type ConfigSyncResult struct {
	Inserted  []string
	Updated   []string
	Unchanged []string
}

func (r *ConfigSyncResult) recordInserted(id string) {
	r.Inserted = append(r.Inserted, id)
}

func (r *ConfigSyncResult) recordUpdated(id string) {
	r.Updated = append(r.Updated, id)
}

func (r *ConfigSyncResult) recordUnchanged(id string) {
	r.Unchanged = append(r.Unchanged, id)
}

// TraderConfigRepository coordinates trader_config + trader_config_history writes.
type TraderConfigRepository interface {
	Sync(ctx context.Context, records []TraderConfigRecord) (*ConfigSyncResult, error)
	FindOne(ctx context.Context, traderID string) (*model.TraderConfig, error)
	ListAll(ctx context.Context) ([]*model.TraderConfig, error)
	FindByVersion(ctx context.Context, traderID string, version int64) (*model.TraderConfig, error)
	ListHistory(ctx context.Context, traderID string, limit int) ([]*model.TraderConfigHistory, error)
}

type traderConfigRepo struct {
	configModel  model.TraderConfigModel
	historyModel model.TraderConfigHistoryModel
	db           *sql.DB
}

// NewTraderConfigRepository wires the repo with table models and a raw DB handle.
func NewTraderConfigRepository(
	configModel model.TraderConfigModel,
	historyModel model.TraderConfigHistoryModel,
	db *sql.DB,
) TraderConfigRepository {
	return &traderConfigRepo{
		configModel:  configModel,
		historyModel: historyModel,
		db:           db,
	}
}

func (r *traderConfigRepo) Sync(ctx context.Context, records []TraderConfigRecord) (*ConfigSyncResult, error) {
	summary := &ConfigSyncResult{}
	if len(records) == 0 {
		return summary, nil
	}
	for _, rec := range records {
		rec := rec
		action, err := r.syncOne(ctx, rec)
		if err != nil {
			return summary, err
		}
		switch action {
		case configInserted:
			summary.recordInserted(rec.ID)
		case configUpdated:
			summary.recordUpdated(rec.ID)
		default:
			summary.recordUnchanged(rec.ID)
		}
	}
	return summary, nil
}

func (r *traderConfigRepo) FindOne(ctx context.Context, traderID string) (*model.TraderConfig, error) {
	return r.configModel.FindOne(ctx, traderID)
}

func (r *traderConfigRepo) ListAll(ctx context.Context) ([]*model.TraderConfig, error) {
	return r.configModel.ListAll(ctx)
}

func (r *traderConfigRepo) FindByVersion(ctx context.Context, traderID string, version int64) (*model.TraderConfig, error) {
	return r.configModel.FindByVersion(ctx, traderID, version)
}

func (r *traderConfigRepo) ListHistory(ctx context.Context, traderID string, limit int) ([]*model.TraderConfigHistory, error) {
	return r.historyModel.ListByTrader(ctx, traderID, limit)
}

type configSyncAction string

const (
	configUnchanged configSyncAction = "unchanged"
	configInserted  configSyncAction = "inserted"
	configUpdated   configSyncAction = "updated"
)

func (r *traderConfigRepo) syncOne(ctx context.Context, rec TraderConfigRecord) (configSyncAction, error) {
	if strings.TrimSpace(rec.ID) == "" {
		return configUnchanged, errors.New("trader config record missing id")
	}
	detail, err := normalizeDetail(rec.Detail)
	if err != nil {
		return configUnchanged, fmt.Errorf("normalize detail for %s: %w", rec.ID, err)
	}
	existing, err := r.configModel.FindOne(ctx, rec.ID)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return configUnchanged, err
	}
	if errors.Is(err, model.ErrNotFound) {
		row := buildConfigRow(rec, detail, 1)
		history := buildHistoryRow(row, []string{"exchange_provider", "market_provider", "allocation_pct", "detail"}, rec)
		if err := withTx(ctx, r.db, func(tx *sql.Tx) error {
			if err := r.configModel.InsertTx(ctx, tx, row); err != nil {
				return err
			}
			return r.historyModel.InsertTx(ctx, tx, history)
		}); err != nil {
			return configUnchanged, err
		}
		return configInserted, nil
	}

	changes := computeChangedFields(existing, rec, detail)
	if len(changes) == 0 {
		return configUnchanged, nil
	}
	row := buildConfigRow(rec, detail, existing.Version+1)
	history := buildHistoryRow(row, changes, rec)
	if err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := r.configModel.UpdateTx(ctx, tx, row); err != nil {
			return err
		}
		return r.historyModel.InsertTx(ctx, tx, history)
	}); err != nil {
		return configUnchanged, err
	}
	return configUpdated, nil
}

func buildConfigRow(rec TraderConfigRecord, detail string, version int64) *model.TraderConfig {
	row := &model.TraderConfig{
		Id:               rec.ID,
		Version:          version,
		ExchangeProvider: rec.ExchangeProvider,
		MarketProvider:   rec.MarketProvider,
		AllocationPct:    rec.AllocationPct,
		Detail:           detail,
	}
	if strings.TrimSpace(rec.CreatedBy) != "" {
		row.CreatedBy = sql.NullString{String: rec.CreatedBy, Valid: true}
	}
	return row
}

func buildHistoryRow(row *model.TraderConfig, changedFields []string, rec TraderConfigRecord) *model.TraderConfigHistory {
	history := &model.TraderConfigHistory{
		TraderId:       row.Id,
		Version:        row.Version,
		ConfigSnapshot: row.Detail,
		ChangedAt:      time.Now().UTC(),
	}
	if len(changedFields) > 0 {
		history.ChangedFields = pq.StringArray(changedFields)
	}
	if reason := strings.TrimSpace(rec.ChangeReason); reason != "" {
		history.ChangeReason = sql.NullString{String: reason, Valid: true}
	}
	if by := strings.TrimSpace(rec.CreatedBy); by != "" {
		history.ChangedBy = sql.NullString{String: by, Valid: true}
	}
	return history
}

func computeChangedFields(existing *model.TraderConfig, rec TraderConfigRecord, detail string) []string {
	var changed []string
	if existing.ExchangeProvider != rec.ExchangeProvider {
		changed = append(changed, "exchange_provider")
	}
	if existing.MarketProvider != rec.MarketProvider {
		changed = append(changed, "market_provider")
	}
	if existing.AllocationPct != rec.AllocationPct {
		changed = append(changed, "allocation_pct")
	}
	if strings.TrimSpace(existing.Detail) != detail {
		changed = append(changed, "detail")
	}
	if len(changed) == 0 {
		return nil
	}
	sort.Strings(changed)
	// dedupe
	uniq := changed[:0]
	last := ""
	for _, field := range changed {
		if field == last {
			continue
		}
		uniq = append(uniq, field)
		last = field
	}
	return uniq
}

func normalizeDetail(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "{}", nil
	}
	var payload any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}
