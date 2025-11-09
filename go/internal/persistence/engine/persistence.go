package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/zeromicro/go-zero/core/logx"
	gocache "github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/core/stores/sqlc"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	cachekeys "nof0-api/internal/cache"
	"nof0-api/internal/model"
	"nof0-api/pkg/exchange"
	executorpkg "nof0-api/pkg/executor"
	journal "nof0-api/pkg/journal"
	managerpkg "nof0-api/pkg/manager"
)

var (
	_ managerpkg.PersistenceService    = (*Service)(nil)
	_ executorpkg.ConversationRecorder = (*Service)(nil)
)

// Service wires Postgres + Redis collaborators required by manager persistence hooks.
type Service struct {
	sqlConn                   sqlx.SqlConn
	positionsModel            model.PositionsModel
	tradesModel               model.TradesModel
	snapshotsModel            model.AccountEquitySnapshotsModel
	decisionModel             model.DecisionCyclesModel
	cache                     gocache.Cache
	redis                     *redis.Redis
	ttl                       cachekeys.TTLSet
	conversationsModel        model.ConversationsModel
	conversationMessagesModel model.ConversationMessagesModel
}

// Config enumerates dependencies needed to persist manager events.
type Config struct {
	SQLConn                   sqlx.SqlConn
	PositionsModel            model.PositionsModel
	TradesModel               model.TradesModel
	SnapshotsModel            model.AccountEquitySnapshotsModel
	DecisionModel             model.DecisionCyclesModel
	Cache                     gocache.Cache
	Redis                     *redis.Redis
	TTL                       cachekeys.TTLSet
	ConversationsModel        model.ConversationsModel
	ConversationMessagesModel model.ConversationMessagesModel
}

// NewService returns a concrete persistence service when mandatory dependencies are present.
func NewService(cfg Config) managerpkg.PersistenceService {
	if cfg.SQLConn == nil {
		return nil
	}
	return &Service{
		sqlConn:                   cfg.SQLConn,
		positionsModel:            cfg.PositionsModel,
		tradesModel:               cfg.TradesModel,
		snapshotsModel:            cfg.SnapshotsModel,
		decisionModel:             cfg.DecisionModel,
		cache:                     cfg.Cache,
		redis:                     cfg.Redis,
		ttl:                       cfg.TTL,
		conversationsModel:        cfg.ConversationsModel,
		conversationMessagesModel: cfg.ConversationMessagesModel,
	}
}

// RecordPositionEvent persists basic position lifecycle information.
func (s *Service) RecordPositionEvent(ctx context.Context, event managerpkg.PositionEvent) error {
	if s == nil || s.sqlConn == nil {
		return nil
	}
	modelID := normalizedModelID(event)
	symbol := strings.ToUpper(strings.TrimSpace(event.Decision.Symbol))
	if modelID == "" || symbol == "" {
		return nil
	}
	switch event.Event {
	case managerpkg.PositionEventOpen:
		return s.handleOpenPosition(ctx, modelID, symbol, event)
	case managerpkg.PositionEventClose:
		return s.handleClosePosition(ctx, modelID, symbol, event)
	default:
		return nil
	}
}

// RecordDecisionCycle mirrors journal cycles to Postgres.
func (s *Service) RecordDecisionCycle(ctx context.Context, record managerpkg.DecisionCycleRecord) error {
	if s == nil || s.decisionModel == nil || record.Cycle == nil {
		return nil
	}
	mID := record.TraderID
	if mID == "" {
		mID = record.Cycle.TraderID
	}
	if mID == "" {
		return nil
	}
	row := &model.DecisionCycles{
		ModelId: mID,
		Success: record.Cycle.Success,
		ConfigVersion: func() int64 {
			if record.ConfigVersion > 0 {
				return record.ConfigVersion
			}
			if record.Cycle != nil && record.Cycle.ConfigVersion > 0 {
				return record.Cycle.ConfigVersion
			}
			return 1
		}(),
		ExecutedAt: func() time.Time {
			if record.Cycle.Timestamp.IsZero() {
				return time.Now().UTC()
			}
			return record.Cycle.Timestamp.UTC()
		}(),
	}
	if record.Cycle.CycleNumber > 0 {
		row.CycleNumber = sql.NullInt64{Int64: int64(record.Cycle.CycleNumber), Valid: true}
	}
	if strings.TrimSpace(record.Cycle.PromptDigest) != "" {
		row.PromptDigest = sql.NullString{String: record.Cycle.PromptDigest, Valid: true}
	}
	if strings.TrimSpace(record.Cycle.CoTTrace) != "" {
		row.CotTrace = sql.NullString{String: record.Cycle.CoTTrace, Valid: true}
	}
	if strings.TrimSpace(record.Cycle.DecisionsJSON) != "" {
		row.Decisions = sql.NullString{String: record.Cycle.DecisionsJSON, Valid: true}
	}
	if strings.TrimSpace(record.Cycle.ErrorMessage) != "" {
		row.ErrorMessage = sql.NullString{String: record.Cycle.ErrorMessage, Valid: true}
	}
	_, err := s.decisionModel.Insert(ctx, row)
	if err != nil && isUniqueViolation(err) {
		return nil
	}
	if err != nil {
		return err
	}
	s.cacheDecisionSummary(ctx, mID, record)
	return nil
}

// RecordAccountSnapshot captures periodic equity metrics.
func (s *Service) RecordAccountSnapshot(ctx context.Context, snapshot managerpkg.AccountSyncSnapshot) error {
	if s == nil || s.snapshotsModel == nil || snapshot.TraderID == "" {
		return nil
	}
	ts := snapshot.SyncedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	metaPayload := map[string]any{
		"available_balance_usd": snapshot.AvailableBalanceUSD,
		"unrealized_pnl_usd":    snapshot.UnrealizedPnLUSD,
	}
	metaBytes, _ := json.Marshal(metaPayload)
	row := &model.AccountEquitySnapshots{
		ModelId:            snapshot.TraderID,
		TsMs:               ts.UTC().UnixMilli(),
		DollarEquity:       snapshot.EquityUSD,
		RealizedPnl:        0,
		TotalUnrealizedPnl: snapshot.UnrealizedPnLUSD,
		Metadata:           string(metaBytes),
	}
	if snapshot.EquityUSD != 0 {
		row.CumPnlPct = sql.NullFloat64{Float64: snapshot.UnrealizedPnLUSD / snapshot.EquityUSD * 100, Valid: true}
	}
	if snapshot.SyncedAt.IsZero() {
		row.SharpeRatio = sql.NullFloat64{}
	}
	_, err := s.snapshotsModel.Insert(ctx, row)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		existing, findErr := s.snapshotsModel.FindOneByModelIdTsMs(ctx, row.ModelId, row.TsMs)
		if findErr != nil {
			return findErr
		}
		row.Id = existing.Id
		return s.snapshotsModel.Update(ctx, row)
	}
	return err
}

// RecordAnalytics persists performance metrics and refreshes related caches.
func (s *Service) RecordAnalytics(ctx context.Context, snapshot managerpkg.AnalyticsSnapshot) error {
	if s == nil || strings.TrimSpace(snapshot.TraderID) == "" {
		return nil
	}
	payload := map[string]any{
		"total_pnl_usd":      snapshot.TotalPnLUSD,
		"total_pnl_pct":      snapshot.TotalPnLPct,
		"sharpe_ratio":       snapshot.SharpeRatio,
		"win_rate":           snapshot.WinRate,
		"total_trades":       snapshot.TotalTrades,
		"max_drawdown_pct":   snapshot.MaxDrawdownPct,
		"updated_at_rfc3339": snapshot.UpdatedAt.UTC().Format(time.RFC3339),
	}
	s.cacheAnalyticsPayload(ctx, snapshot.TraderID, payload)
	s.cacheSinceInception(ctx, snapshot.TraderID, payload)
	s.cacheLeaderboardScore(ctx, snapshot.TraderID, snapshot.TotalPnLPct)
	return nil
}

// HydrateCaches reloads cache state for provided trader IDs. Currently best-effort no-op
// until dedicated cache warmup jobs are implemented.
func (s *Service) HydrateCaches(ctx context.Context, traderIDs []string) error {
	if s == nil || s.cache == nil {
		return nil
	}
	ids := normalizeIDs(traderIDs)
	if len(ids) == 0 {
		return nil
	}
	var errs []error
	if s.positionsModel != nil {
		if err := s.hydratePositions(ctx, ids); err != nil {
			errs = append(errs, err)
		}
	}
	if s.tradesModel != nil {
		if err := s.hydrateTrades(ctx, ids); err != nil {
			errs = append(errs, err)
		}
	}
	if s.decisionModel != nil {
		if err := s.hydrateDecisionCycles(ctx, ids); err != nil {
			errs = append(errs, err)
		}
	}
	return errorsJoin(errs)
}

func (s *Service) hydratePositions(ctx context.Context, traderIDs []string) error {
	data, err := s.positionsModel.ActiveByModels(ctx, traderIDs)
	if err != nil {
		return err
	}
	remaining := make(map[string]struct{}, len(traderIDs))
	for _, id := range traderIDs {
		remaining[id] = struct{}{}
	}
	now := time.Now().UTC().UnixMilli()
	for modelID, records := range data {
		delete(remaining, modelID)
		entries := make(map[string]positionCacheEntry, len(records))
		for _, rec := range records {
			symbol := strings.ToUpper(strings.TrimSpace(rec.Symbol))
			if symbol == "" {
				continue
			}
			entry := positionCacheEntry{
				Symbol:      symbol,
				Side:        strings.ToLower(strings.TrimSpace(rec.Side)),
				Quantity:    rec.Quantity,
				EntryPrice:  rec.EntryPrice,
				Leverage:    floatPtrValue(rec.Leverage),
				Confidence:  floatPtrValue(rec.Confidence),
				RiskUSD:     floatPtrValue(rec.RiskUsd),
				UpdatedAtMs: now,
				Exchange:    strings.TrimSpace(rec.ExchangeProvider),
			}
			if rec.UnrealizedPnl != nil {
				entry.RiskUSD = floatPtrValue(rec.UnrealizedPnl)
			}
			entries[symbol] = entry
		}
		s.persistPositionCache(ctx, modelID, entries)
	}
	// Skip deleting cache for traders with no positions during hydration.
	// Cache entries will naturally expire via TTL, avoiding unnecessary Del operations
	// that can trigger circuit breaker issues during startup.
	return nil
}

func (s *Service) persistPositionCache(ctx context.Context, modelID string, payload map[string]positionCacheEntry) {
	s.writePositionsCache(ctx, modelID, payload)
}

func (s *Service) hydrateTrades(ctx context.Context, traderIDs []string) error {
	for _, modelID := range traderIDs {
		records, err := s.tradesModel.RecentByModel(ctx, modelID, recentTradesLimit)
		if err != nil {
			return err
		}
		// Only persist cache if there are trades to cache.
		// Skip Del operations during hydration to avoid circuit breaker issues.
		if len(records) == 0 {
			continue
		}
		entries := make([]tradeCacheEntry, 0, len(records))
		for _, rec := range records {
			entry := tradeCacheEntry{
				ModelID:      rec.ModelID,
				Symbol:       strings.ToUpper(strings.TrimSpace(rec.Symbol)),
				Side:         strings.ToLower(strings.TrimSpace(rec.Side)),
				Quantity:     floatPtrValue(rec.Quantity),
				EntryPrice:   floatPtrValue(rec.EntryPrice),
				ExitPrice:    floatPtrValue(rec.ExitPrice),
				RealizedPnL:  floatPtrValue(rec.RealizedNetPnl),
				Confidence:   floatPtrValue(rec.Confidence),
				ClosedAtMs:   intPtrValue(rec.ExitTsMs),
				Exchange:     strings.TrimSpace(rec.ExchangeProvider),
				EntryTimeMs:  rec.EntryTsMs,
				Leverage:     floatPtrValue(rec.Leverage),
				PositionSize: floatPtrValue(rec.EntrySz),
			}
			if entry.ClosedAtMs == 0 && rec.EntryTsMs > 0 {
				entry.ClosedAtMs = rec.EntryTsMs
			}
			entries = append(entries, entry)
		}
		s.persistTradeCache(ctx, modelID, entries)
	}
	return nil
}

func (s *Service) persistTradeCache(ctx context.Context, modelID string, entries []tradeCacheEntry) {
	s.writeTradesCache(ctx, modelID, entries)
}

func (s *Service) hydrateDecisionCycles(ctx context.Context, traderIDs []string) error {
	if s.sqlConn == nil {
		return nil
	}
	const query = `SELECT success, error_message, decisions, executed_at, config_version FROM public.decision_cycles WHERE model_id = $1 ORDER BY executed_at DESC LIMIT 1`
	for _, modelID := range traderIDs {
		var row struct {
			Success       bool           `db:"success"`
			ErrorMessage  sql.NullString `db:"error_message"`
			Decisions     sql.NullString `db:"decisions"`
			ExecutedAt    time.Time      `db:"executed_at"`
			ConfigVersion sql.NullInt64  `db:"config_version"`
		}
		if err := s.sqlConn.QueryRowCtx(ctx, &row, query, modelID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sqlc.ErrNotFound) {
				continue
			}
			return err
		}
		actions := make([]map[string]any, 0)
		if row.Decisions.Valid && strings.TrimSpace(row.Decisions.String) != "" {
			var raw []map[string]any
			if err := json.Unmarshal([]byte(row.Decisions.String), &raw); err != nil {
				logx.WithContext(ctx).Errorf("enginepersist: hydrate decisions unmarshal model=%s err=%v", modelID, err)
			} else {
				for _, d := range raw {
					action := make(map[string]any)
					if sym, ok := d["symbol"]; ok {
						action["symbol"] = sym
					}
					if act, ok := d["action"]; ok {
						action["action"] = act
					}
					if conf, ok := d["confidence"]; ok {
						action["confidence"] = conf
					}
					actions = append(actions, action)
				}
			}
		}
		rec := &journal.CycleRecord{
			TraderID: modelID,
			ConfigVersion: func() int64 {
				if row.ConfigVersion.Valid {
					return row.ConfigVersion.Int64
				}
				return 0
			}(),
			Timestamp: row.ExecutedAt,
			Success:   row.Success,
			ErrorMessage: func() string {
				if row.ErrorMessage.Valid {
					return row.ErrorMessage.String
				}
				return ""
			}(),
			DecisionsJSON: func() string {
				if row.Decisions.Valid {
					return row.Decisions.String
				}
				return ""
			}(),
			Actions: actions,
		}
		cfgVersion := int64(0)
		if row.ConfigVersion.Valid {
			cfgVersion = row.ConfigVersion.Int64
		}
		s.cacheDecisionSummary(ctx, modelID, managerpkg.DecisionCycleRecord{
			TraderID:      modelID,
			ConfigVersion: cfgVersion,
			Cycle:         rec,
		})
	}
	return nil
}

func normalizeIDs(ids []string) []string {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func numericValue(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
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

func floatPtrValue(ptr *float64) float64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}

func intPtrValue(ptr *int64) int64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}

func errorsJoin(errs []error) error {
	filtered := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			filtered = append(filtered, err)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return errors.Join(filtered...)
}

// RecordConversation stores executor prompt/response pairs for debugging.
func (s *Service) RecordConversation(ctx context.Context, rec executorpkg.ConversationRecord) error {
	if s == nil || s.sqlConn == nil || s.conversationsModel == nil || s.conversationMessagesModel == nil {
		return nil
	}
	modelID := strings.TrimSpace(rec.ModelID)
	if modelID == "" || strings.TrimSpace(rec.Prompt) == "" || strings.TrimSpace(rec.Response) == "" {
		return nil
	}
	ts := rec.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	var conversationID int64
	topic := sql.NullString{}
	if trimmed := strings.TrimSpace(rec.Topic); trimmed != "" {
		topic = sql.NullString{String: trimmed, Valid: true}
	}
	err := s.sqlConn.TransactCtx(ctx, func(ctx context.Context, session sqlx.Session) error {
		insertConv := `
INSERT INTO public.conversations (model_id, topic, created_at)
VALUES ($1, $2, NOW())
RETURNING id`
		if err := session.QueryRowCtx(ctx, &conversationID, insertConv, modelID, topic); err != nil {
			return err
		}
		if err := s.insertConversationMessage(ctx, session, conversationID, "system", rec.Prompt, rec.PromptTokens, ts, map[string]any{
			"model":          rec.ModelName,
			"prompt_tokens":  rec.PromptTokens,
			"total_tokens":   rec.TotalTokens,
			"conversationId": conversationID,
		}); err != nil {
			return err
		}
		return s.insertConversationMessage(ctx, session, conversationID, "assistant", rec.Response, rec.CompletionTokens, ts, map[string]any{
			"model":             rec.ModelName,
			"completion_tokens": rec.CompletionTokens,
			"total_tokens":      rec.TotalTokens,
			"conversationId":    conversationID,
		})
	})
	if err != nil {
		return err
	}
	s.cacheConversationID(ctx, modelID, conversationID)
	return nil
}

// handleOpenPosition upserts a lightweight open position row.
func (s *Service) handleOpenPosition(ctx context.Context, modelID, symbol string, event managerpkg.PositionEvent) error {
	price := effectivePrice(event)
	qty := effectiveQuantity(event, price)
	side := "long"
	if strings.EqualFold(event.Decision.Action, "open_short") {
		side = "short"
	}
	entryTime := event.OccurredAt
	if entryTime.IsZero() {
		entryTime = time.Now()
	}
	detail, err := buildPositionDetail(positionDetailInput{
		Price:       price,
		Quantity:    qty,
		TimeMs:      entryTime.UTC().UnixMilli(),
		Leverage:    float64(event.Decision.Leverage),
		Confidence:  float64(event.Decision.Confidence),
		RiskUSD:     event.Decision.RiskUSD,
		Exchange:    traderExchange(event),
		Orientation: side,
	})
	if err != nil {
		return err
	}
	statement := `
INSERT INTO public.positions (
    id, trader_id, symbol, side, status, detail, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, 'open', $5, NOW(), NOW()
)
ON CONFLICT (id) DO UPDATE SET
    side = EXCLUDED.side,
    status = 'open',
    detail = EXCLUDED.detail,
    updated_at = NOW();
`
	_, err = s.sqlConn.ExecCtx(
		ctx,
		statement,
		positionID(modelID, symbol),
		modelID,
		symbol,
		side,
		detail,
	)
	if err != nil {
		return err
	}
	s.cacheOpenPosition(ctx, modelID, symbol, &positionCacheEntry{
		Symbol:      symbol,
		Side:        side,
		Quantity:    qty,
		EntryPrice:  price,
		Leverage:    float64(event.Decision.Leverage),
		Confidence:  float64(event.Decision.Confidence),
		RiskUSD:     event.Decision.RiskUSD,
		UpdatedAtMs: time.Now().UTC().UnixMilli(),
		Exchange:    traderExchange(event),
	})
	return nil
}

// handleClosePosition transitions the row to closed status and attempts PnL computation.
func (s *Service) handleClosePosition(ctx context.Context, modelID, symbol string, event managerpkg.PositionEvent) error {
	var existing *model.Positions
	var existingDetail positionDetail
	if s.positionsModel != nil {
		if pos, err := s.positionsModel.FindOne(ctx, positionID(modelID, symbol)); err == nil {
			existing = pos
			existingDetail = decodePositionDetail(pos.Detail)
		} else if err != nil && err != model.ErrNotFound {
			return err
		}
	}
	closePrice := effectivePrice(event)
	if closePrice <= 0 && existing != nil && existingDetail.Entry.Price > 0 {
		closePrice = existingDetail.Entry.Price
	}
	closeTime := event.OccurredAt
	if closeTime.IsZero() {
		closeTime = time.Now()
	}
	qty := effectiveQuantity(event, closePrice)
	if qty <= 0 && existing != nil && existingDetail.Entry.Quantity > 0 {
		qty = existingDetail.Entry.Quantity
	}
	qtyForPnl := qty
	if qtyForPnl <= 0 && existing != nil {
		qtyForPnl = existingDetail.Entry.Quantity
	}
	var pnl sql.NullFloat64
	if existing != nil && closePrice > 0 && existingDetail.Entry.Price > 0 && qtyForPnl > 0 {
		sign := 1.0
		if strings.EqualFold(existing.Side, "short") {
			sign = -1.0
		}
		value := sign * (closePrice - existingDetail.Entry.Price) * qtyForPnl
		pnl = sql.NullFloat64{Float64: value, Valid: true}
	}
	var detailJSON string
	var detailErr error
	if existing != nil {
		detailJSON, detailErr = updatePositionDetailOnClose(existing.Detail, positionCloseDetail{
			Price:    closePrice,
			TimeMs:   closeTime.UTC().UnixMilli(),
			Quantity: qty,
			PnL: func() float64 {
				if pnl.Valid {
					return pnl.Float64
				}
				return 0
			}(),
		})
	} else {
		detailJSON, detailErr = updatePositionDetailOnClose("", positionCloseDetail{
			Price:    closePrice,
			TimeMs:   closeTime.UTC().UnixMilli(),
			Quantity: qty,
			PnL: func() float64 {
				if pnl.Valid {
					return pnl.Float64
				}
				return 0
			}(),
		})
	}
	if detailErr != nil {
		return detailErr
	}
	statement := `
UPDATE public.positions
SET status = 'closed',
    detail = $2,
    updated_at = NOW()
WHERE id = $1;
`
	if _, err := s.sqlConn.ExecCtx(ctx, statement, positionID(modelID, symbol), detailJSON); err != nil {
		return err
	}
	summary, err := s.insertTrade(ctx, existing, modelID, symbol, closePrice, qty, pnl, closeTime, event)
	if err != nil {
		return err
	}
	s.cacheOpenPosition(ctx, modelID, symbol, nil)
	if summary != nil {
		s.appendRecentTrade(ctx, modelID, *summary)
	}
	return nil
}

func (s *Service) insertTrade(ctx context.Context, pos *model.Positions, modelID, symbol string, closePrice, qty float64, pnl sql.NullFloat64, closeTime time.Time, event managerpkg.PositionEvent) (*tradeCacheEntry, error) {
	if s == nil || s.tradesModel == nil || pos == nil {
		return nil, nil
	}
	posDetail := decodePositionDetail(pos.Detail)
	tradeQty := qty
	if tradeQty <= 0 {
		tradeQty = posDetail.Entry.Quantity
	}
	entryTs := posDetail.Entry.TimeMs
	tradeDetail, err := buildTradeDetail(tradeDetailInput{
		EntryPrice:  posDetail.Entry.Price,
		ExitPrice:   closePrice,
		EntryTimeMs: entryTs,
		CloseTimeMs: closeTime.UTC().UnixMilli(),
		Quantity:    tradeQty,
		Confidence:  float64(event.Decision.Confidence),
		Leverage:    posDetail.Entry.Leverage,
		Exchange:    posDetail.Exchange.Provider,
		PnL: func() float64 {
			if pnl.Valid {
				return pnl.Float64
			}
			return 0
		}(),
	})
	trade := &model.Trades{
		Id:        buildTradeID(modelID, symbol, closeTime),
		TraderId:  pos.TraderId,
		Symbol:    symbol,
		Side:      pos.Side,
		CloseTsMs: closeTime.UTC().UnixMilli(),
		Detail:    tradeDetail,
	}
	if err != nil {
		return nil, err
	}
	_, err = s.tradesModel.Insert(ctx, trade)
	if isUniqueViolation(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	realized := 0.0
	if pnl.Valid {
		realized = pnl.Float64
	}
	summary := &tradeCacheEntry{
		ModelID:      pos.TraderId,
		Symbol:       symbol,
		Side:         pos.Side,
		Quantity:     tradeQty,
		EntryPrice:   posDetail.Entry.Price,
		ExitPrice:    closePrice,
		RealizedPnL:  realized,
		Confidence:   float64(event.Decision.Confidence),
		ClosedAtMs:   closeTime.UTC().UnixMilli(),
		Exchange:     posDetail.Exchange.Provider,
		EntryTimeMs:  entryTs,
		Leverage:     posDetail.Entry.Leverage,
		PositionSize: posDetail.Entry.Quantity,
	}
	return summary, nil
}

func normalizedModelID(event managerpkg.PositionEvent) string {
	if strings.TrimSpace(event.TraderID) != "" {
		return event.TraderID
	}
	if event.Trader != nil {
		return event.Trader.ID
	}
	return ""
}

func traderExchange(event managerpkg.PositionEvent) string {
	if event.Trader != nil && strings.TrimSpace(event.Trader.Exchange) != "" {
		return event.Trader.Exchange
	}
	return "unknown"
}

func positionID(modelID, symbol string) string {
	return fmt.Sprintf("%s|%s", strings.TrimSpace(modelID), strings.ToUpper(strings.TrimSpace(symbol)))
}

func effectivePrice(event managerpkg.PositionEvent) float64 {
	if event.FillPrice > 0 {
		return event.FillPrice
	}
	if event.Decision.EntryPrice > 0 {
		return event.Decision.EntryPrice
	}
	if px, _, ok := extractFill(event.ExchangeResponse); ok && px > 0 {
		return px
	}
	return 0
}

func effectiveQuantity(event managerpkg.PositionEvent, price float64) float64 {
	if event.FillSize > 0 {
		return event.FillSize
	}
	if price > 0 && event.Decision.PositionSizeUSD > 0 {
		qty := event.Decision.PositionSizeUSD / price
		if qty > 0 && !math.IsInf(qty, 0) && !math.IsNaN(qty) {
			return qty
		}
	}
	if _, sz, ok := extractFill(event.ExchangeResponse); ok && sz > 0 {
		return sz
	}
	if event.Decision.PositionSizeUSD > 0 {
		return event.Decision.PositionSizeUSD
	}
	return 0
}

func extractFill(resp *exchange.OrderResponse) (price float64, qty float64, ok bool) {
	if resp == nil {
		return 0, 0, false
	}
	for _, status := range resp.Response.Data.Statuses {
		if status.Filled != nil {
			if px, err := strconv.ParseFloat(status.Filled.AvgPx, 64); err == nil {
				price = px
			}
			if sz, err := strconv.ParseFloat(status.Filled.TotalSz, 64); err == nil {
				qty = sz
			}
			return price, qty, true
		}
	}
	return 0, 0, false
}

func isUniqueViolation(err error) bool {
	pgErr, ok := err.(*pq.Error)
	return ok && pgErr.Code == "23505"
}

func nullFloatValue(value sql.NullFloat64) interface{} {
	if value.Valid {
		return value.Float64
	}
	return nil
}

func toNullFloat(value float64, valid bool) sql.NullFloat64 {
	if !valid {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: value, Valid: true}
}

func buildTradeID(modelID, symbol string, closeTime time.Time) string {
	return fmt.Sprintf("%s|%s|%d", modelID, strings.ToUpper(strings.TrimSpace(symbol)), closeTime.UTC().UnixNano())
}

func (s *Service) insertConversationMessage(ctx context.Context, session sqlx.Session, conversationID int64, role string, content string, tokens int, ts time.Time, metadata map[string]any) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	tsMs := sql.NullInt64{}
	if !ts.IsZero() {
		tsMs = sql.NullInt64{Int64: ts.UTC().UnixMilli(), Valid: true}
	}
	metaStr := ""
	if len(metadata) > 0 {
		if data, err := json.Marshal(metadata); err == nil {
			metaStr = string(data)
		}
	}
	_, err := session.ExecCtx(ctx, `
INSERT INTO public.conversation_messages (conversation_id, role, content, ts_ms, metadata, created_at)
VALUES ($1, $2, $3, $4, $5, NOW())`,
		conversationID,
		role,
		content,
		tsMs,
		metaStr,
	)
	return err
}

const (
	recentTradesLimit       = 100
	defaultCacheTTL         = time.Minute
	conversationsCacheLimit = 20
)

type positionCacheEntry struct {
	Symbol      string  `json:"symbol"`
	Side        string  `json:"side"`
	Quantity    float64 `json:"quantity"`
	EntryPrice  float64 `json:"entry_price"`
	Leverage    float64 `json:"leverage"`
	Confidence  float64 `json:"confidence,omitempty"`
	RiskUSD     float64 `json:"risk_usd,omitempty"`
	UpdatedAtMs int64   `json:"updated_at_ms"`
	Exchange    string  `json:"exchange,omitempty"`
}

type tradeCacheEntry struct {
	ModelID      string  `json:"model_id"`
	Symbol       string  `json:"symbol"`
	Side         string  `json:"side"`
	Quantity     float64 `json:"quantity"`
	EntryPrice   float64 `json:"entry_price"`
	ExitPrice    float64 `json:"exit_price"`
	RealizedPnL  float64 `json:"realized_pnl"`
	Confidence   float64 `json:"confidence,omitempty"`
	ClosedAtMs   int64   `json:"closed_at_ms"`
	Exchange     string  `json:"exchange,omitempty"`
	EntryTimeMs  int64   `json:"entry_time_ms,omitempty"`
	Leverage     float64 `json:"leverage,omitempty"`
	PositionSize float64 `json:"position_size,omitempty"`
}

type decisionCacheEntry struct {
	ModelID       string `json:"model_id"`
	ConfigVersion int64  `json:"config_version,omitempty"`
	Success       bool   `json:"success"`
	Timestamp     int64  `json:"timestamp_ms"`
	Symbol        string `json:"symbol,omitempty"`
	Action        string `json:"action,omitempty"`
	Confidence    int    `json:"confidence,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (s *Service) cacheOpenPosition(ctx context.Context, modelID, symbol string, entry *positionCacheEntry) {
	if s == nil || (s.redis == nil && s.cache == nil) {
		return
	}
	payload, err := s.loadPositionsCache(ctx, modelID)
	if err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: load positions cache trader=%s err=%v", modelID, err)
		return
	}
	upSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	if upSymbol == "" {
		return
	}
	if entry == nil {
		delete(payload, upSymbol)
	} else {
		if entry.Symbol == "" {
			entry.Symbol = upSymbol
		}
		entry.Exchange = strings.TrimSpace(entry.Exchange)
		payload[upSymbol] = *entry
	}
	if len(payload) == 0 {
		payload = nil
	}
	s.writePositionsCache(ctx, modelID, payload)
}

func (s *Service) appendRecentTrade(ctx context.Context, modelID string, entry tradeCacheEntry) {
	if s == nil || (s.redis == nil && s.cache == nil) {
		return
	}
	payload, err := s.loadTradesCache(ctx, modelID)
	if err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: load trades cache trader=%s err=%v", modelID, err)
		return
	}
	payload = append([]tradeCacheEntry{entry}, payload...)
	if len(payload) > recentTradesLimit {
		payload = payload[:recentTradesLimit]
	}
	s.writeTradesCache(ctx, modelID, payload)
}

func (s *Service) cacheDecisionSummary(ctx context.Context, modelID string, record managerpkg.DecisionCycleRecord) {
	if s == nil || record.Cycle == nil {
		return
	}
	key := cachekeys.TraderDecisionLastHashKey()
	field := cachekeys.TraderHashField(modelID)
	ts := record.Cycle.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	entry := decisionCacheEntry{
		ModelID:       modelID,
		ConfigVersion: record.ConfigVersion,
		Success:       record.Cycle.Success,
		Timestamp:     ts.UnixMilli(),
		Error:         record.Cycle.ErrorMessage,
	}
	if len(record.Cycle.Actions) > 0 {
		first := record.Cycle.Actions[0]
		if sym, ok := first["symbol"].(string); ok {
			entry.Symbol = sym
		}
		if act, ok := first["action"].(string); ok {
			entry.Action = act
		}
		switch c := first["confidence"].(type) {
		case int:
			entry.Confidence = c
		case float64:
			entry.Confidence = int(math.Round(c))
		}
	}
	ttl := s.ttlDuration(cachekeys.DecisionLastTTL(s.ttl))
	if ttl <= 0 {
		return
	}
	if s.redis != nil {
		if err := s.hashSetJSON(ctx, key, field, ttl, entry); err != nil {
			logx.WithContext(ctx).Errorf("enginepersist: set decision hash key=%s field=%s err=%v", key, field, err)
		}
		return
	}
	if s.cache == nil {
		return
	}
	legacyKey := cachekeys.DecisionLastKey(modelID)
	if err := s.cache.SetWithExpireCtx(ctx, legacyKey, entry, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set decision cache key=%s err=%v", legacyKey, err)
	}
}

func (s *Service) loadPositionsCache(ctx context.Context, traderID string) (map[string]positionCacheEntry, error) {
	if s.redis != nil {
		var payload map[string]positionCacheEntry
		found, err := s.hashGetJSON(ctx, cachekeys.TraderPositionsHashKey(), cachekeys.TraderHashField(traderID), &payload)
		if err != nil {
			return nil, err
		}
		if !found || payload == nil {
			return make(map[string]positionCacheEntry), nil
		}
		return payload, nil
	}
	if s.cache == nil {
		return make(map[string]positionCacheEntry), nil
	}
	key := cachekeys.PositionsHashKey(traderID)
	payload := make(map[string]positionCacheEntry)
	if err := s.cache.GetCtx(ctx, key, &payload); err != nil {
		if s.cache.IsNotFound(err) {
			return make(map[string]positionCacheEntry), nil
		}
		return nil, err
	}
	if payload == nil {
		payload = make(map[string]positionCacheEntry)
	}
	return payload, nil
}

func (s *Service) writePositionsCache(ctx context.Context, traderID string, payload map[string]positionCacheEntry) {
	ttl := s.ttlDuration(cachekeys.PositionsTTL(s.ttl))
	if s.redis != nil {
		key := cachekeys.TraderPositionsHashKey()
		field := cachekeys.TraderHashField(traderID)
		if len(payload) == 0 || payload == nil {
			if err := s.hashDelField(ctx, key, field); err != nil && err != redis.Nil {
				logx.WithContext(ctx).Errorf("enginepersist: del positions hash key=%s field=%s err=%v", key, field, err)
			}
		} else if err := s.hashSetJSON(ctx, key, field, ttl, payload); err != nil {
			logx.WithContext(ctx).Errorf("enginepersist: set positions hash key=%s field=%s err=%v", key, field, err)
		}
		return
	}
	if s.cache == nil {
		return
	}
	key := cachekeys.PositionsHashKey(traderID)
	if len(payload) == 0 || payload == nil {
		if err := s.cache.DelCtx(ctx, key); err != nil && !s.cache.IsNotFound(err) {
			logx.WithContext(ctx).Errorf("enginepersist: del positions cache key=%s err=%v", key, err)
		}
		return
	}
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set positions cache key=%s err=%v", key, err)
	}
}

func (s *Service) loadTradesCache(ctx context.Context, traderID string) ([]tradeCacheEntry, error) {
	if s.redis != nil {
		var payload []tradeCacheEntry
		found, err := s.hashGetJSON(ctx, cachekeys.TraderTradesRecentHashKey(), cachekeys.TraderHashField(traderID), &payload)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, nil
		}
		return payload, nil
	}
	if s.cache == nil {
		return nil, nil
	}
	key := cachekeys.TradesRecentKey(traderID)
	var payload []tradeCacheEntry
	if err := s.cache.GetCtx(ctx, key, &payload); err != nil {
		if s.cache.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return payload, nil
}

func (s *Service) writeTradesCache(ctx context.Context, traderID string, payload []tradeCacheEntry) {
	ttl := s.ttlDuration(cachekeys.TradesRecentTTL(s.ttl))
	if s.redis != nil {
		key := cachekeys.TraderTradesRecentHashKey()
		field := cachekeys.TraderHashField(traderID)
		if len(payload) == 0 {
			if err := s.hashDelField(ctx, key, field); err != nil && err != redis.Nil {
				logx.WithContext(ctx).Errorf("enginepersist: del trades hash key=%s field=%s err=%v", key, field, err)
			}
		} else if err := s.hashSetJSON(ctx, key, field, ttl, payload); err != nil {
			logx.WithContext(ctx).Errorf("enginepersist: set trades hash key=%s field=%s err=%v", key, field, err)
		}
		return
	}
	if s.cache == nil {
		return
	}
	key := cachekeys.TradesRecentKey(traderID)
	if len(payload) == 0 {
		if err := s.cache.DelCtx(ctx, key); err != nil && !s.cache.IsNotFound(err) {
			logx.WithContext(ctx).Errorf("enginepersist: del trades cache key=%s err=%v", key, err)
		}
		return
	}
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set trades cache key=%s err=%v", key, err)
	}
}

func (s *Service) writeAnalyticsCache(ctx context.Context, traderID string, payload map[string]any) {
	ttl := s.ttlDuration(cachekeys.AnalyticsTTL(s.ttl))
	if s.redis != nil {
		key := cachekeys.TraderAnalyticsHashKey()
		field := cachekeys.TraderHashField(traderID)
		if len(payload) == 0 || payload == nil {
			if err := s.hashDelField(ctx, key, field); err != nil && err != redis.Nil {
				logx.WithContext(ctx).Errorf("enginepersist: del analytics hash key=%s field=%s err=%v", key, field, err)
			}
		} else if err := s.hashSetJSON(ctx, key, field, ttl, payload); err != nil {
			logx.WithContext(ctx).Errorf("enginepersist: set analytics hash key=%s field=%s err=%v", key, field, err)
		}
		return
	}
	if s.cache == nil {
		return
	}
	key := cachekeys.AnalyticsKey(traderID)
	if len(payload) == 0 || payload == nil {
		if err := s.cache.DelCtx(ctx, key); err != nil && !s.cache.IsNotFound(err) {
			logx.WithContext(ctx).Errorf("enginepersist: del analytics cache key=%s err=%v", key, err)
		}
		return
	}
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set analytics cache key=%s err=%v", key, err)
	}
}

func (s *Service) writeSinceInceptionCache(ctx context.Context, traderID string, payload map[string]any) {
	ttl := s.ttlDuration(cachekeys.SinceInceptionTTL(s.ttl))
	if s.redis != nil {
		key := cachekeys.TraderSinceInceptionHashKey()
		field := cachekeys.TraderHashField(traderID)
		if len(payload) == 0 || payload == nil {
			if err := s.hashDelField(ctx, key, field); err != nil && err != redis.Nil {
				logx.WithContext(ctx).Errorf("enginepersist: del since inception hash key=%s field=%s err=%v", key, field, err)
			}
		} else if err := s.hashSetJSON(ctx, key, field, ttl, payload); err != nil {
			logx.WithContext(ctx).Errorf("enginepersist: set since inception hash key=%s field=%s err=%v", key, field, err)
		}
		return
	}
	if s.cache == nil {
		return
	}
	key := cachekeys.SinceInceptionKey(traderID)
	if len(payload) == 0 || payload == nil {
		if err := s.cache.DelCtx(ctx, key); err != nil && !s.cache.IsNotFound(err) {
			logx.WithContext(ctx).Errorf("enginepersist: del since inception cache key=%s err=%v", key, err)
		}
		return
	}
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, payload, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set since inception cache key=%s err=%v", key, err)
	}
}

func (s *Service) hashSetJSON(ctx context.Context, key, field string, ttl time.Duration, payload any) error {
	if s.redis == nil || strings.TrimSpace(field) == "" {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := s.redis.HsetCtx(ctx, key, field, string(data)); err != nil {
		return err
	}
	s.hashExpire(ctx, key, ttl)
	return nil
}

func (s *Service) hashGetJSON(ctx context.Context, key, field string, dest any) (bool, error) {
	if s.redis == nil || strings.TrimSpace(field) == "" {
		return false, nil
	}
	val, err := s.redis.HgetCtx(ctx, key, field)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	if strings.TrimSpace(val) == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(val), dest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) hashDelField(ctx context.Context, key, field string) error {
	if s.redis == nil || strings.TrimSpace(field) == "" {
		return nil
	}
	_, err := s.redis.HdelCtx(ctx, key, field)
	return err
}

func (s *Service) hashExpire(ctx context.Context, key string, ttl time.Duration) {
	if s.redis == nil || ttl <= 0 {
		return
	}
	if err := s.redis.ExpireCtx(ctx, key, durationToSeconds(ttl)); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: expire hash key=%s err=%v", key, err)
	}
}

func (s *Service) ttlDuration(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultCacheTTL
	}
	return value
}

func durationToSeconds(ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	return int(math.Ceil(ttl.Seconds()))
}

type positionDetailInput struct {
	Price       float64
	Quantity    float64
	TimeMs      int64
	Leverage    float64
	Confidence  float64
	RiskUSD     float64
	Exchange    string
	Orientation string
}

type positionDetail struct {
	Entry    positionEntryDetail    `json:"entry"`
	Exchange positionExchangeDetail `json:"exchange"`
	Risk     positionRiskDetail     `json:"risk"`
	Close    *positionCloseDetail   `json:"close,omitempty"`
	Metrics  positionMetricsDetail  `json:"metrics,omitempty"`
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

type positionCloseDetail struct {
	Price    float64 `json:"price,omitempty"`
	TimeMs   int64   `json:"time_ms,omitempty"`
	Quantity float64 `json:"quantity,omitempty"`
	PnL      float64 `json:"pnl,omitempty"`
}

type positionMetricsDetail struct {
	UnrealizedPnL float64 `json:"unrealized_pnl,omitempty"`
}

func buildPositionDetail(input positionDetailInput) (string, error) {
	detail := positionDetail{
		Entry: positionEntryDetail{
			Price:    input.Price,
			Quantity: input.Quantity,
			TimeMs:   input.TimeMs,
			Leverage: input.Leverage,
		},
		Exchange: positionExchangeDetail{
			Provider: strings.TrimSpace(input.Exchange),
		},
		Risk: positionRiskDetail{
			Confidence: input.Confidence,
			RiskUSD:    input.RiskUSD,
		},
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func updatePositionDetailOnClose(raw string, closeInfo positionCloseDetail) (string, error) {
	detail := decodePositionDetail(raw)
	detail.Close = &closeInfo
	payload, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	return string(payload), nil
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

type tradeDetailInput struct {
	EntryPrice  float64
	ExitPrice   float64
	EntryTimeMs int64
	CloseTimeMs int64
	Quantity    float64
	Confidence  float64
	Leverage    float64
	Exchange    string
	PnL         float64
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

func buildTradeDetail(input tradeDetailInput) (string, error) {
	detail := tradeDetail{}
	detail.Time.OpenTsMs = input.EntryTimeMs
	detail.Time.CloseTsMs = input.CloseTimeMs
	if input.CloseTimeMs > 0 && input.EntryTimeMs > 0 {
		duration := input.CloseTimeMs - input.EntryTimeMs
		if duration > 0 {
			detail.Time.DurationSeconds = duration / 1000
		}
	}
	detail.Prices.Entry = input.EntryPrice
	detail.Prices.Exit = input.ExitPrice
	detail.Quantity.Total = input.Quantity
	detail.Risk.Confidence = input.Confidence
	detail.Risk.Leverage = input.Leverage
	detail.Exchange.Provider = strings.TrimSpace(input.Exchange)
	detail.PnL.Net = input.PnL
	payload, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (s *Service) cacheConversationID(ctx context.Context, modelID string, conversationID int64) {
	if s == nil || s.cache == nil || conversationID <= 0 || strings.TrimSpace(modelID) == "" {
		return
	}
	key := cachekeys.ConversationsKey(modelID)
	var ids []int64
	if err := s.cache.GetCtx(ctx, key, &ids); err != nil && !s.cache.IsNotFound(err) {
		logx.WithContext(ctx).Errorf("enginepersist: load conversations cache key=%s err=%v", key, err)
		return
	}
	ids = append([]int64{conversationID}, ids...)
	if len(ids) > conversationsCacheLimit {
		ids = ids[:conversationsCacheLimit]
	}
	ttl := s.ttlDuration(cachekeys.ConversationsTTL(s.ttl))
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, ids, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set conversations cache key=%s err=%v", key, err)
	}
}

func (s *Service) cacheAnalyticsPayload(ctx context.Context, modelID string, payload map[string]any) {
	s.writeAnalyticsCache(ctx, modelID, payload)
}

func (s *Service) cacheSinceInception(ctx context.Context, modelID string, payload map[string]any) {
	data := map[string]any{
		"total_pnl_usd": payload["total_pnl_usd"],
		"total_pnl_pct": payload["total_pnl_pct"],
		"sharpe_ratio":  payload["sharpe_ratio"],
	}
	s.writeSinceInceptionCache(ctx, modelID, data)
}

func (s *Service) cacheLeaderboardScore(ctx context.Context, modelID string, score float64) {
	if s == nil {
		return
	}
	if s.redis != nil {
		key := cachekeys.LeaderboardZSetKey()
		if _, err := s.redis.ZaddFloatCtx(ctx, key, score, modelID); err != nil {
			logx.WithContext(ctx).Errorf("enginepersist: zadd leaderboard key=%s trader=%s err=%v", key, modelID, err)
		}
		return
	}
	if s.cache == nil {
		return
	}
	key := cachekeys.LeaderboardCacheKey()
	entry := map[string]any{
		"model_id":   modelID,
		"score":      score,
		"updated_at": time.Now().UTC().UnixMilli(),
	}
	ttl := s.ttlDuration(cachekeys.LeaderboardTTL(s.ttl))
	if ttl <= 0 {
		return
	}
	if err := s.cache.SetWithExpireCtx(ctx, key, entry, ttl); err != nil {
		logx.WithContext(ctx).Errorf("enginepersist: set leaderboard cache key=%s err=%v", key, err)
	}
}
