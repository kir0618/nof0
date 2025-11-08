package marketpersist

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/zeromicro/go-zero/core/logx"
	gocache "github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	cachekeys "nof0-api/internal/cache"
	"nof0-api/internal/model"
	"nof0-api/pkg/market"
)

const (
	assetSQLTimeout   = 30 * time.Second
	assetCacheTimeout = 30 * time.Second
	cacheWorkerLimit  = 32
)

// Service implements market data persistence and caching hooks.
type Service struct {
	sqlConn          sqlx.SqlConn
	assetsModel      model.MarketAssetsModel
	assetCtxModel    model.MarketAssetCtxModel
	priceLatestModel model.PriceLatestModel
	priceTicksModel  model.PriceTicksModel
	cache            gocache.Cache
	redis            *redis.Redis
	ttl              cachekeys.TTLSet
}

// Config enumerates dependencies required to persist market data.
type Config struct {
	SQLConn          sqlx.SqlConn
	AssetsModel      model.MarketAssetsModel
	AssetCtxModel    model.MarketAssetCtxModel
	PriceLatestModel model.PriceLatestModel
	PriceTicksModel  model.PriceTicksModel
	Cache            gocache.Cache
	Redis            *redis.Redis
	TTL              cachekeys.TTLSet
}

// NewService wires a market persistence service. Returns nil when dependencies missing.
func NewService(cfg Config) market.Persistence {
	if cfg.SQLConn == nil {
		return nil
	}
	return &Service{
		sqlConn:          cfg.SQLConn,
		assetsModel:      cfg.AssetsModel,
		assetCtxModel:    cfg.AssetCtxModel,
		priceLatestModel: cfg.PriceLatestModel,
		priceTicksModel:  cfg.PriceTicksModel,
		cache:            cfg.Cache,
		redis:            cfg.Redis,
		ttl:              cfg.TTL,
	}
}

// UpsertAssets persists static metadata and refreshes Redis cache.
func (s *Service) UpsertAssets(ctx context.Context, provider string, assets []market.Asset) error {
	if s == nil || s.sqlConn == nil || len(assets) == 0 {
		return nil
	}

	start := time.Now()

	// Prepare batch INSERT with multiple VALUES clauses
	var valueClauses []string
	var args []interface{}
	validAssets := make([]market.Asset, 0, len(assets))
	argIndex := 1

	for _, asset := range assets {
		if strings.TrimSpace(asset.Symbol) == "" {
			continue
		}
		name := asset.Symbol
		if base := strings.TrimSpace(asset.Base); base != "" {
			name = base
		}
		metadata := asset.RawMetadata
		maxLev := nullFloatFromMeta(metadata, "maxLeverage")
		marginTbl := nullIntFromMeta(metadata, "marginTable", "margin_table_id")
		onlyIso := nullBoolFromMeta(metadata, "onlyIsolated", "only_isolated")
		precision := sql.NullInt64{}
		if asset.Precision > 0 {
			precision = sql.NullInt64{Int64: int64(asset.Precision), Valid: true}
		}
		isDelisted := !asset.IsActive

		// Build placeholders for this row: ($1, $2, $3, ..., $8)
		placeholders := make([]string, 8)
		for i := 0; i < 8; i++ {
			placeholders[i] = fmt.Sprintf("$%d", argIndex)
			argIndex++
		}
		valueClauses = append(valueClauses, fmt.Sprintf("(%s, NOW(), NOW())", strings.Join(placeholders, ", ")))

		// Append arguments in order
		args = append(args,
			provider,
			asset.Symbol,
			sql.NullString{String: name, Valid: name != ""},
			precision,
			maxLev,
			onlyIso,
			marginTbl,
			isDelisted,
		)

		validAssets = append(validAssets, asset)
	}

	if len(valueClauses) == 0 {
		logx.WithContext(ctx).Infow("marketpersist: no valid assets to upsert", logx.Field("provider", provider))
		return nil
	}

	// Build single batch INSERT statement
	stmt := fmt.Sprintf(`
INSERT INTO public.market_assets (
    provider, symbol, name, sz_decimals, max_leverage, only_isolated, margin_table_id, is_delisted, created_at, updated_at
) VALUES %s
ON CONFLICT (provider, symbol) DO UPDATE SET
    name = EXCLUDED.name,
    sz_decimals = EXCLUDED.sz_decimals,
    max_leverage = EXCLUDED.max_leverage,
    only_isolated = EXCLUDED.only_isolated,
    margin_table_id = EXCLUDED.margin_table_id,
    is_delisted = EXCLUDED.is_delisted,
    updated_at = NOW()`, strings.Join(valueClauses, ", "))

	sqlCtx, sqlCancel := context.WithTimeout(context.Background(), assetSQLTimeout)
	defer sqlCancel()
	queryStart := time.Now()
	if _, err := s.sqlConn.ExecCtx(sqlCtx, stmt, args...); err != nil {
		logx.WithContext(sqlCtx).Errorf("marketpersist: batch upsert failed provider=%s count=%d err=%v", provider, len(validAssets), err)
		return err
	}
	sqlDuration := time.Since(queryStart)

	cacheCtx, cacheCancel := context.WithTimeout(context.Background(), assetCacheTimeout)
	defer cacheCancel()
	cacheStart := time.Now()
	if err := s.cacheAssets(cacheCtx, provider, validAssets); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logx.WithContext(cacheCtx).Errorf("marketpersist: cache assets timed out provider=%s count=%d err=%v", provider, len(validAssets), err)
		}
		return err
	}
	cacheDuration := time.Since(cacheStart)

	logx.WithContext(ctx).Infof("marketpersist: batch upserted assets provider=%s count=%d sql_duration=%dms cache_duration=%dms total_duration=%dms",
		provider, len(validAssets), sqlDuration.Milliseconds(), cacheDuration.Milliseconds(), time.Since(start).Milliseconds())
	return nil
}

// RecordSnapshot persists latest price/context data to Postgres + Redis.
func (s *Service) RecordSnapshot(ctx context.Context, provider string, snapshot *market.Snapshot) error {
	if s == nil || s.sqlConn == nil || snapshot == nil || strings.TrimSpace(snapshot.Symbol) == "" {
		return nil
	}
	now := time.Now().UTC()
	price := snapshot.Price.Last
	raw, _ := json.Marshal(snapshot)
	priceStmt := `
INSERT INTO public.price_latest (provider, symbol, price, ts_ms, raw, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
ON CONFLICT (provider, symbol) DO UPDATE SET
    price = EXCLUDED.price,
    ts_ms = EXCLUDED.ts_ms,
    raw = EXCLUDED.raw,
    updated_at = NOW();`
	if _, err := s.sqlConn.ExecCtx(ctx, priceStmt, provider, snapshot.Symbol, price, now.UnixMilli(), string(raw)); err != nil {
		return err
	}

	ctxStmt := `
INSERT INTO public.market_asset_ctx (
    provider, symbol, funding, open_interest, oracle_px, mark_px, mid_px, impact_pxs, prev_day_px, day_ntl_vlm, day_base_vlm, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, NULL, $5, NULL, NULL, NULL, NULL, NULL, NOW(), NOW()
)
ON CONFLICT (provider, symbol) DO UPDATE SET
    funding = EXCLUDED.funding,
    open_interest = EXCLUDED.open_interest,
    mark_px = EXCLUDED.mark_px,
    updated_at = NOW();`
	funding := sql.NullFloat64{}
	if snapshot.Funding != nil {
		funding = sql.NullFloat64{Float64: snapshot.Funding.Rate, Valid: true}
	}
	openInterest := sql.NullFloat64{}
	if snapshot.OpenInterest != nil {
		openInterest = sql.NullFloat64{Float64: snapshot.OpenInterest.Latest, Valid: true}
	}
	if _, err := s.sqlConn.ExecCtx(ctx, ctxStmt, provider, snapshot.Symbol, funding, openInterest, price); err != nil {
		return err
	}

	s.cachePrice(ctx, provider, snapshot.Symbol, price, now)
	s.cacheMarketCtx(ctx, provider, snapshot)
	s.updateCryptoPrices(ctx, provider, snapshot.Symbol, price)
	return nil
}

// RecordPriceSeries persists historical ticks (typically OHLCV candles).
func (s *Service) RecordPriceSeries(ctx context.Context, provider string, symbol string, ticks []market.PriceTick) error {
	if s == nil || s.priceTicksModel == nil {
		return nil
	}
	provider = strings.TrimSpace(provider)
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if provider == "" || symbol == "" || len(ticks) == 0 {
		return nil
	}
	for _, tick := range ticks {
		if tick.Timestamp.IsZero() || !(tick.Price > 0) {
			continue
		}
		row := &model.PriceTicks{
			Provider: provider,
			Symbol:   symbol,
			Price:    tick.Price,
			TsMs:     tick.Timestamp.UTC().UnixMilli(),
		}
		if tick.HasVolume {
			row.Volume = sql.NullFloat64{Float64: tick.Volume, Valid: true}
		}
		if raw := buildTickRaw(tick); raw.Valid {
			row.Raw = raw
		}
		if _, err := s.priceTicksModel.Insert(ctx, row); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Service) cacheAssets(ctx context.Context, provider string, assets []market.Asset) error {
	if s.redis == nil || len(assets) == 0 {
		return nil
	}

	key := cachekeys.MarketAssetKey(provider)
	ttl := s.ttl.Duration(cachekeys.TTLLong)
	if ttl <= 0 {
		ttl = cachekeys.MarketAssetTTL(s.ttl)
	}

	// Build hash fields map: symbol -> JSON payload
	fields := make(map[string]string, len(assets))
	for _, asset := range assets {
		if strings.TrimSpace(asset.Symbol) == "" {
			continue
		}

		// Build asset payload
		payload := map[string]any{
			"symbol":     asset.Symbol,
			"base":       asset.Base,
			"quote":      asset.Quote,
			"precision":  asset.Precision,
			"is_active":  asset.IsActive,
			"updated_at": time.Now().UTC().UnixMilli(),
		}
		if asset.RawMetadata != nil && len(asset.RawMetadata) > 0 {
			payload["metadata"] = asset.RawMetadata
		}

		// Convert to JSON
		data, err := json.Marshal(payload)
		if err != nil {
			logx.WithContext(ctx).Errorf("marketpersist: marshal asset failed symbol=%s err=%v", asset.Symbol, err)
			continue
		}
		fields[asset.Symbol] = string(data)
	}

	if len(fields) == 0 {
		return nil
	}

	// Use HMSET to set all fields at once
	if err := s.redis.HmsetCtx(ctx, key, fields); err != nil {
		logx.WithContext(ctx).Errorf("marketpersist: cache assets hash key=%s count=%d err=%v", key, len(fields), err)
		return err
	}

	// Set TTL on the hash key
	if err := s.redis.ExpireCtx(ctx, key, int(ttl.Seconds())); err != nil {
		logx.WithContext(ctx).Errorf("marketpersist: set ttl on assets hash key=%s err=%v", key, err)
		return err
	}

	return nil
}

func (s *Service) cachePrice(ctx context.Context, provider, symbol string, price float64, ts time.Time) {
	if s.cache == nil {
		return
	}
	ttl := cachekeys.PriceTTL(s.ttl)
	if ttl <= 0 {
		return
	}
	// Provider scoped key
	key := cachekeys.PriceLatestByProviderKey(provider, symbol)
	payload := map[string]any{
		"price": price,
		"ts":    ts.UnixMilli(),
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("marketpersist: cache price key=%s err=%v", key, err)
	}
	// Global key
	global := cachekeys.PriceLatestKey(symbol)
	if err := s.cache.SetWithExpireCtx(ctx, global, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("marketpersist: cache price key=%s err=%v", global, err)
	}
}

func (s *Service) cacheMarketCtx(ctx context.Context, provider string, snapshot *market.Snapshot) {
	if s.cache == nil {
		return
	}
	ttl := cachekeys.MarketAssetCtxTTL(s.ttl)
	if ttl <= 0 {
		return
	}
	key := cachekeys.MarketAssetCtxKey(provider, snapshot.Symbol)
	ctxPayload := map[string]any{
		"price":        snapshot.Price.Last,
		"change":       snapshot.Change,
		"funding":      snapshot.Funding,
		"oi":           snapshot.OpenInterest,
		"indicators":   snapshot.Indicators,
		"timestamp_ms": time.Now().UTC().UnixMilli(),
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, ctxPayload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("marketpersist: cache ctx key=%s err=%v", key, err)
	}
}

func (s *Service) updateCryptoPrices(ctx context.Context, provider, symbol string, price float64) {
	if s.cache == nil {
		return
	}
	key := cachekeys.CryptoPricesKey()
	var payload map[string]float64
	if err := s.cache.GetCtx(ctx, key, &payload); err != nil && !s.cache.IsNotFound(err) {
		logx.WithContext(ctx).Errorf("marketpersist: load crypto prices key=%s err=%v", key, err)
		return
	}
	if payload == nil {
		payload = make(map[string]float64)
	}
	field := fmt.Sprintf("%s:%s", provider, symbol)
	payload[field] = price
	ttl := cachekeys.CryptoPricesTTL(s.ttl)
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("marketpersist: cache crypto prices key=%s err=%v", key, err)
	}
}

func buildTickRaw(tick market.PriceTick) sql.NullString {
	payload := map[string]any{
		"interval": tick.Interval,
		"open":     tick.Open,
		"high":     tick.High,
		"low":      tick.Low,
		"close":    tick.Close,
	}
	if tick.HasVolume {
		payload["volume"] = tick.Volume
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(data), Valid: true}
}

func nullFloatFromMeta(meta map[string]any, keys ...string) sql.NullFloat64 {
	for _, key := range keys {
		if v, ok := meta[key]; ok {
			if f, conv := toFloat64(v); conv {
				return sql.NullFloat64{Float64: f, Valid: true}
			}
		}
	}
	return sql.NullFloat64{}
}

func nullIntFromMeta(meta map[string]any, keys ...string) sql.NullInt64 {
	for _, key := range keys {
		if v, ok := meta[key]; ok {
			switch t := v.(type) {
			case int:
				return sql.NullInt64{Int64: int64(t), Valid: true}
			case int64:
				return sql.NullInt64{Int64: t, Valid: true}
			case float64:
				return sql.NullInt64{Int64: int64(t), Valid: true}
			case json.Number:
				if val, err := t.Int64(); err == nil {
					return sql.NullInt64{Int64: val, Valid: true}
				}
			}
		}
	}
	return sql.NullInt64{}
}

func nullBoolFromMeta(meta map[string]any, keys ...string) sql.NullBool {
	for _, key := range keys {
		if v, ok := meta[key]; ok {
			switch t := v.(type) {
			case bool:
				return sql.NullBool{Bool: t, Valid: true}
			case string:
				if strings.EqualFold(t, "true") {
					return sql.NullBool{Bool: true, Valid: true}
				}
				if strings.EqualFold(t, "false") {
					return sql.NullBool{Bool: false, Valid: true}
				}
			}
		}
	}
	return sql.NullBool{}
}

func toFloat64(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	pgErr, ok := err.(*pq.Error)
	return ok && pgErr.Code == "23505"
}
