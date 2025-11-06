package hyperliquid

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"nof0-api/pkg/market"
)

const defaultProviderTimeout = market.DefaultProviderTimeout

// Provider wraps Hyperliquid client calls behind the generic market.Provider contract.
type Provider struct {
	client      *Client
	timeout     time.Duration
	persistence market.Persistence
	providerID  string
	cacheMu     sync.RWMutex
	snapshots   map[string]cachedSnapshot
	assets      cachedAssets
}

type providerConfig struct {
	timeout      time.Duration
	clientConfig []Option
}

// ProviderOption customises the Hyperliquid provider.
type ProviderOption func(*providerConfig)

// WithTimeout overrides the default per-call timeout.
func WithTimeout(timeout time.Duration) ProviderOption {
	return func(cfg *providerConfig) {
		if timeout > 0 {
			cfg.timeout = timeout
		}
	}
}

// WithClientOptions passes options to the underlying Hyperliquid client.
func WithClientOptions(options ...Option) ProviderOption {
	return func(cfg *providerConfig) {
		cfg.clientConfig = append(cfg.clientConfig, options...)
	}
}

// NewProvider constructs a Hyperliquid market provider.
func NewProvider(opts ...ProviderOption) *Provider {
	cfg := &providerConfig{
		timeout: defaultProviderTimeout,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	client := NewClient(cfg.clientConfig...)
	return &Provider{
		client:    client,
		timeout:   cfg.timeout,
		snapshots: make(map[string]cachedSnapshot),
	}
}

func init() {
	market.RegisterProvider("hyperliquid", func(name string, cfg *market.ProviderConfig) (market.Provider, error) {
		opts := []ProviderOption{}
		clientOptions := []Option{}
		if cfg.Timeout > 0 {
			opts = append(opts, WithTimeout(cfg.Timeout))
		}
		if cfg.HTTPTimeout > 0 {
			clientOptions = append(clientOptions, WithHTTPClient(&http.Client{Timeout: cfg.HTTPTimeout}))
		}
		if cfg.Testnet {
			clientOptions = append(clientOptions, WithBaseURL(testnetBaseURL))
		}
		if cfg.MaxRetries > 0 {
			clientOptions = append(clientOptions, WithMaxRetries(cfg.MaxRetries))
		}
		if len(clientOptions) > 0 {
			opts = append(opts, WithClientOptions(clientOptions...))
		}
		provider := NewProvider(opts...)
		provider.providerID = name
		return provider, nil
	})
}

// Snapshot implements market.Provider by returning an aggregated market snapshot.
func (p *Provider) Snapshot(ctx context.Context, symbol string) (*market.Snapshot, error) {
	ctx, cancel := p.withTimeout(ctx)
	defer cancel()
	if snap, ok := p.loadSnapshot(symbol); ok {
		return snap, nil
	}
	snap, ticks, err := p.client.buildSnapshot(ctx, symbol)
	if err != nil {
		return nil, err
	}
	p.persistSnapshot(ctx, symbol, snap)
	if len(ticks) > 0 && p.persistence != nil {
		if err := p.persistence.RecordPriceSeries(ctx, p.providerName(), symbol, ticks); err != nil {
			logx.WithContext(ctx).Errorf("hyperliquid: persist price series symbol=%s err=%v", symbol, err)
		}
	}
	p.storeSnapshot(symbol, snap)
	return snap, nil
}

// ListAssets implements market.Provider by returning all supported symbols.
func (p *Provider) ListAssets(ctx context.Context) ([]market.Asset, error) {
	ctx, cancel := p.withTimeout(ctx)
	defer cancel()
	if assets, ok := p.loadAssets(); ok {
		return assets, nil
	}

	if err := p.client.refreshSymbolDirectory(ctx); err != nil {
		return nil, err
	}
	assets := p.collectAssets()
	if p.persistence != nil && len(assets) > 0 {
		if err := p.persistence.UpsertAssets(ctx, p.providerName(), assets); err != nil {
			logx.WithContext(ctx).Errorf("hyperliquid: persist assets err=%v", err)
		}
	}
	p.storeAssets(assets)
	return assets, nil
}

func (p *Provider) collectAssets() []market.Asset {
	p.client.symbolsMu.RLock()
	defer p.client.symbolsMu.RUnlock()

	assets := make([]market.Asset, 0, len(p.client.symbolIndex))
	for _, canonical := range p.client.symbolIndex {
		meta := p.client.universeMeta[canonical]
		ctxData := p.client.assetCtxBySymbol[canonical]
		asset := market.Asset{
			Symbol:    canonical,
			Base:      "", // Hyperliquid uses single-coin naming; leave base/quote empty for now.
			Quote:     "",
			Precision: meta.SzDecimals,
			IsActive:  !meta.IsDelisted,
			RawMetadata: map[string]any{
				"maxLeverage":  meta.MaxLeverage,
				"marginTable":  meta.MarginTableID,
				"onlyIsolated": meta.OnlyIsolated,
				"funding":      ctxData.Funding,
				"openInterest": ctxData.OpenInterest,
				"dayBaseVlm":   ctxData.DayBaseVlm,
				"dayNtlVlm":    ctxData.DayNtlVlm,
				"prevDayPx":    ctxData.PrevDayPx,
			},
		}
		assets = append(assets, asset)
	}

	sort.Slice(assets, func(i, j int) bool {
		return assets[i].Symbol < assets[j].Symbol
	})
	return assets
}

func (p *Provider) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, p.timeout)
}

// SetPersistence wires a persistence layer for market data.
func (p *Provider) SetPersistence(persist market.Persistence) {
	p.persistence = persist
}

const (
	snapshotCacheTTL = 15 * time.Second
	assetCacheTTL    = 5 * time.Minute
)

type cachedSnapshot struct {
	Snapshot *market.Snapshot
	Fetched  time.Time
}

type cachedAssets struct {
	Assets  []market.Asset
	Fetched time.Time
}

func (p *Provider) loadSnapshot(symbol string) (*market.Snapshot, bool) {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()
	entry, ok := p.snapshots[strings.ToUpper(symbol)]
	if !ok || time.Since(entry.Fetched) > snapshotCacheTTL || entry.Snapshot == nil {
		return nil, false
	}
	copied := *entry.Snapshot
	return &copied, true
}

func (p *Provider) storeSnapshot(symbol string, snapshot *market.Snapshot) {
	if snapshot == nil {
		return
	}
	copy := *snapshot
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if p.snapshots == nil {
		p.snapshots = make(map[string]cachedSnapshot)
	}
	p.snapshots[strings.ToUpper(symbol)] = cachedSnapshot{Snapshot: &copy, Fetched: time.Now()}
}

func (p *Provider) loadAssets() ([]market.Asset, bool) {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()
	if len(p.assets.Assets) == 0 || time.Since(p.assets.Fetched) > assetCacheTTL {
		return nil, false
	}
	assets := make([]market.Asset, len(p.assets.Assets))
	copy(assets, p.assets.Assets)
	return assets, true
}

func (p *Provider) storeAssets(assets []market.Asset) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	clone := make([]market.Asset, len(assets))
	copy(clone, assets)
	p.assets = cachedAssets{Assets: clone, Fetched: time.Now()}
}

func (p *Provider) providerName() string {
	if strings.TrimSpace(p.providerID) != "" {
		return p.providerID
	}
	return "hyperliquid"
}
