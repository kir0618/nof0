package cache

import (
	"fmt"
	"strings"
	"time"

	"nof0-api/internal/config"
)

// Namespace is the Redis key prefix for the NOF0 application.
const Namespace = "nof0"

// TTLClass represents a config-driven TTL bucket.
type TTLClass string

const (
	TTLShort  TTLClass = "short"
	TTLMedium TTLClass = "medium"
	TTLLong   TTLClass = "long"
)

// TTLSet normalises cache TTLs from config into time.Duration values.
type TTLSet struct {
	Short  time.Duration
	Medium time.Duration
	Long   time.Duration
}

// NewTTLSet converts config TTLs (in seconds) into durations.
func NewTTLSet(cfg config.CacheTTL) TTLSet {
	return TTLSet{
		Short:  durationOrDefault(cfg.Short, 10*time.Second),
		Medium: durationOrDefault(cfg.Medium, time.Minute),
		Long:   durationOrDefault(cfg.Long, 5*time.Minute),
	}
}

func durationOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds < 0 {
		return 0
	}
	if seconds == 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// Duration returns the configured duration for the given TTL class.
func (t TTLSet) Duration(class TTLClass) time.Duration {
	switch class {
	case TTLShort:
		return t.Short
	case TTLMedium:
		return t.Medium
	case TTLLong:
		return t.Long
	default:
		return 0
	}
}

// Scaled applies a multiplier to a TTL class, useful for half/double TTL variants.
func (t TTLSet) Scaled(class TTLClass, factor float64) time.Duration {
	base := t.Duration(class)
	if base <= 0 || factor <= 0 {
		return base
	}
	return time.Duration(float64(base) * factor)
}

func formatKey(parts ...string) string {
	values := make([]string, 0, len(parts)+1)
	values = append(values, Namespace)
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if clean == "" {
			continue
		}
		values = append(values, clean)
	}
	return strings.Join(values, ":")
}

// --- Price & Market Keys ----------------------------------------------------

// PriceLatestKey returns the default latest price key without provider scoping.
func PriceLatestKey(symbol string) string {
	return formatKey("price", "latest", symbol)
}

// PriceLatestByProviderKey returns the latest price key scoped by provider.
func PriceLatestByProviderKey(provider, symbol string) string {
	return formatKey("price", "latest", provider, symbol)
}

// CryptoPricesKey holds the aggregated prices map payload.
func CryptoPricesKey() string {
	return formatKey("crypto_prices")
}

// MarketAssetKey stores static metadata (max leverage, isolation flags).
// Returns hash key in format: nof0:market:asset:{provider}
// The symbol should be used as the hash field name.
func MarketAssetKey(provider string) string {
	return formatKey("market", "asset", provider)
}

// MarketAssetCtxKey stores volatile market context (funding, OI, etc.).
func MarketAssetCtxKey(provider, symbol string) string {
	return formatKey("market", "ctx", provider, symbol)
}

// --- Positions Keys ---------------------------------------------------------

func PositionsHashKey(modelID string) string {
	return formatKey("positions", modelID)
}

// TraderPositionsHashKey is the consolidated hash for all trader positions.
func TraderPositionsHashKey() string {
	return formatKey("trader", "positions")
}

// TraderTradesRecentHashKey is the consolidated hash for recent trades.
func TraderTradesRecentHashKey() string {
	return formatKey("trader", "trades_recent")
}

// TraderAnalyticsHashKey stores analytics snapshots per trader.
func TraderAnalyticsHashKey() string {
	return formatKey("trader", "analytics")
}

// TraderSinceInceptionHashKey stores lifetime analytics per trader.
func TraderSinceInceptionHashKey() string {
	return formatKey("trader", "since_inception")
}

// TraderDecisionLastHashKey stores the latest decision summary per trader.
func TraderDecisionLastHashKey() string {
	return formatKey("trader", "decision_last")
}

// TraderHashField normalizes trader ids for hash access.
func TraderHashField(traderID string) string {
	return strings.ToLower(strings.TrimSpace(traderID))
}

// PositionsLockKey is used as a short-lived recompute lock.
func PositionsLockKey(modelID string) string {
	return formatKey("lock", "positions", modelID)
}

// --- Trades Keys ------------------------------------------------------------

func TradesRecentKey(modelID string) string {
	return formatKey("trades", "recent", modelID)
}

// TradesStreamKey is the Redis Stream name for trade fan-out.
func TradesStreamKey() string {
	return formatKey("trades", "stream")
}

// TradeIngestGuardKey prevents duplicate ingestion of the same trade ID.
func TradeIngestGuardKey(tradeID string) string {
	return formatKey("ingest", "trade", tradeID)
}

// --- Leaderboard & Analytics Keys ------------------------------------------

func LeaderboardZSetKey() string {
	return formatKey("leaderboard")
}

// LeaderboardCacheKey stores a pre-rendered leaderboard payload.
func LeaderboardCacheKey() string {
	return formatKey("leaderboard", "cache")
}

func SinceInceptionKey(modelID string) string {
	return formatKey("since_inception", modelID)
}

func AnalyticsKey(modelID string) string {
	return formatKey("analytics", modelID)
}

// AnalyticsAllKey caches the aggregated analytics response.
func AnalyticsAllKey() string {
	return formatKey("analytics", "all")
}

// --- Conversations & Decisions ---------------------------------------------

func ConversationsKey(modelID string) string {
	return formatKey("conversations", modelID)
}

// DecisionLastKey caches a summary of the latest decision cycle.
func DecisionLastKey(modelID string) string {
	return formatKey("decision", "last", modelID)
}

// --- Trader State / Simulator ----------------------------------------------

func TraderStateKey(traderID string) string {
	return formatKey("trader", traderID, "state")
}

// SimOrdersKey holds simulator order snapshots.
func SimOrdersKey(traderID string) string {
	return formatKey("sim", "orders", traderID)
}

// SimBalancesKey holds simulator balances.
func SimBalancesKey(traderID string) string {
	return formatKey("sim", "balances", traderID)
}

// --- TTL Helpers ------------------------------------------------------------

// PriceTTL returns short-lived TTL for individual price keys.
func PriceTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLShort)
}

// CryptoPricesTTL returns the TTL for bundled prices.
func CryptoPricesTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLShort)
}

// MarketAssetTTL returns the TTL for static market metadata.
func MarketAssetTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLLong)
}

// MarketAssetCtxTTL returns the TTL for volatile market context payloads.
func MarketAssetCtxTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLMedium)
}

// PositionsTTL returns the TTL for positions hash payloads.
func PositionsTTL(ttl TTLSet) time.Duration {
	return ttl.Scaled(TTLMedium, 0.5) // target ~30s when medium=60s
}

// PositionsLockTTL returns the TTL for recompute locks.
func PositionsLockTTL(ttl TTLSet) time.Duration {
	return ttl.Scaled(TTLShort, 0.5) // target ~5s when short=10s
}

// TradesRecentTTL returns the TTL for recent trades lists.
func TradesRecentTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLMedium)
}

// TradeIngestGuardTTL returns the TTL for trade idempotency guards.
func TradeIngestGuardTTL() time.Duration {
	return 24 * time.Hour
}

// LeaderboardTTL returns the TTL for leaderboard caches.
func LeaderboardTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLMedium)
}

// SinceInceptionTTL returns the TTL for since inception caches.
func SinceInceptionTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLLong)
}

// AnalyticsTTL returns the TTL for analytics payloads.
func AnalyticsTTL(ttl TTLSet) time.Duration {
	return ttl.Scaled(TTLLong, 2) // target ~600s when long=300s
}

// ConversationsTTL returns the TTL for conversation lists.
func ConversationsTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLLong)
}

// DecisionLastTTL returns the TTL for last decision snapshots.
func DecisionLastTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLMedium)
}

// TraderStateTTL returns the TTL for cached trader state.
func TraderStateTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLMedium)
}

// SimulatorTTL returns the TTL for simulator artefacts.
func SimulatorTTL(ttl TTLSet) time.Duration {
	return ttl.Duration(TTLMedium)
}

// FormatCacheKey is exported for dynamic key construction when patterns
// are not covered by helpers (e.g. segmented analytics keys).
func FormatCacheKey(parts ...string) string {
	return formatKey(parts...)
}

// BuildKeyWithSuffix appends an arbitrary suffix to an existing key.
func BuildKeyWithSuffix(baseKey, suffix string) string {
	if strings.TrimSpace(suffix) == "" {
		return baseKey
	}
	return fmt.Sprintf("%s:%s", baseKey, strings.TrimSpace(suffix))
}
