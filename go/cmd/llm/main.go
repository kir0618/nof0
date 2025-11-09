package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"nof0-api/internal/cache"
	"nof0-api/internal/cli"
	appconfig "nof0-api/internal/config"
	"nof0-api/internal/ingest"
	enginepersist "nof0-api/internal/persistence/engine"
	marketpersist "nof0-api/internal/persistence/market"
	"nof0-api/internal/svc"
	"nof0-api/pkg/confkit"
	exchangepkg "nof0-api/pkg/exchange"
	_ "nof0-api/pkg/exchange/hyperliquid"
	_ "nof0-api/pkg/exchange/sim"
	executorpkg "nof0-api/pkg/executor"
	llmpkg "nof0-api/pkg/llm"
	managerpkg "nof0-api/pkg/manager"
	marketpkg "nof0-api/pkg/market"
	_ "nof0-api/pkg/market/exchanges/hyperliquid"
)

type filteredMarket struct {
	marketpkg.Provider
	allowed map[string]struct{}
}

func (f *filteredMarket) SetPersistence(persist marketpkg.Persistence) {
	if aware, ok := f.Provider.(marketpkg.PersistenceAware); ok {
		aware.SetPersistence(persist)
	}
}

func newFilteredMarket(base marketpkg.Provider, symbols []string) (*filteredMarket, error) {
	if base == nil {
		return nil, fmt.Errorf("filtered market: base provider is nil")
	}
	set := make(map[string]struct{}, len(symbols))
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		set[strings.ToUpper(sym)] = struct{}{}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("filtered market: allowed symbol list is empty")
	}
	return &filteredMarket{
		Provider: base,
		allowed:  set,
	}, nil
}

func (f *filteredMarket) Snapshot(ctx context.Context, symbol string) (*marketpkg.Snapshot, error) {
	if !f.isAllowed(symbol) {
		return nil, fmt.Errorf("filtered market: symbol %s not allowed", symbol)
	}
	return f.Provider.Snapshot(ctx, symbol)
}

func (f *filteredMarket) ListAssets(ctx context.Context) ([]marketpkg.Asset, error) {
	assets, err := f.Provider.ListAssets(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]marketpkg.Asset, 0, len(f.allowed))
	for _, asset := range assets {
		if f.isAllowed(asset.Symbol) {
			filtered = append(filtered, asset)
		}
	}
	return filtered, nil
}

func (f *filteredMarket) isAllowed(symbol string) bool {
	if symbol == "" {
		return false
	}
	_, ok := f.allowed[strings.ToUpper(symbol)]
	return ok
}

func parseSymbols(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		field = strings.ToUpper(field)
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}

func fatalf(format string, args ...interface{}) {
	logx.Errorf(format, args...)
	os.Exit(1)
}

func adaptManagerConfig(cfg *managerpkg.Config, totalEquity float64, allowed []string) error {
	if cfg == nil {
		return fmt.Errorf("manager config is nil")
	}
	if totalEquity <= 0 {
		return fmt.Errorf("total equity must be positive, got %.2f", totalEquity)
	}
	if len(cfg.Traders) == 0 {
		return fmt.Errorf("manager config has no traders defined")
	}
	if len(allowed) == 0 {
		return fmt.Errorf("allowed symbol list cannot be empty")
	}

	cfg.Manager.TotalEquityUSD = totalEquity
	cfg.Manager.ReserveEquityPct = 0

	perTraderEquity := totalEquity / float64(len(cfg.Traders))
	allocationRemaining := 100.0
	for i := range cfg.Traders {
		tr := &cfg.Traders[i]
		// Force allocation to split evenly across all configured traders; last trader absorbs rounding.
		if i == len(cfg.Traders)-1 {
			tr.AllocationPct = allocationRemaining
		} else {
			share := 100.0 / float64(len(cfg.Traders))
			tr.AllocationPct = share
			allocationRemaining -= share
		}

		// Cap max position size to per-trader equity.
		maxSize := perTraderEquity
		if maxSize <= 0 {
			maxSize = totalEquity
		}
		if tr.RiskParams.MaxPositionSizeUSD > maxSize {
			tr.RiskParams.MaxPositionSizeUSD = maxSize
		}
		if tr.RiskParams.MaxPositions > len(allowed) {
			tr.RiskParams.MaxPositions = len(allowed)
			if tr.RiskParams.MaxPositions == 0 {
				tr.RiskParams.MaxPositions = 1
			}
		}
		if tr.ExecGuards.CandidateLimit <= 0 || tr.ExecGuards.CandidateLimit > len(allowed) {
			tr.ExecGuards.CandidateLimit = len(allowed)
		}
	}
	return cfg.Validate()
}

func applyExecutorPromptProfile(cfg *managerpkg.Config, profile string) error {
	if cfg == nil {
		return fmt.Errorf("manager config is nil")
	}
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" || profile == "default" {
		return nil
	}

	var (
		rel             string
		fastMinRR       = 1.5
		fastMinConf     = 60
		fastMaxPosition = 40.0
	)
	switch profile {
	case "fast", "test", "fast-signal":
		rel = "etc/prompts/executor/fast_signal_prompt.tmpl"
	default:
		return fmt.Errorf("unknown executor prompt profile %q", profile)
	}

	path, err := confkit.ProjectPath(rel)
	if err != nil {
		return fmt.Errorf("resolve prompt profile path: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("prompt profile %q missing template %s: %w", profile, path, err)
	}
	for i := range cfg.Traders {
		cfg.Traders[i].ExecutorTemplate = path
		// Relax risk parameters so fast profile prompts pass validation easier.
		if cfg.Traders[i].RiskParams.MinRiskRewardRatio > fastMinRR {
			cfg.Traders[i].RiskParams.MinRiskRewardRatio = fastMinRR
		}
		if cfg.Traders[i].RiskParams.MinConfidence > fastMinConf {
			cfg.Traders[i].RiskParams.MinConfidence = fastMinConf
		}
		if cfg.Traders[i].RiskParams.MaxPositionSizeUSD <= 0 || cfg.Traders[i].RiskParams.MaxPositionSizeUSD > fastMaxPosition {
			cfg.Traders[i].RiskParams.MaxPositionSizeUSD = fastMaxPosition
		}
	}
	logx.Infof("executor prompt profile override active: %s → %s", profile, path)
	return nil
}

func applyPaperTradingOverride(cfg *managerpkg.Config, providerName string) error {
	if cfg == nil {
		return fmt.Errorf("manager config is nil")
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return fmt.Errorf("paper trading provider name cannot be empty")
	}
	for i := range cfg.Traders {
		cfg.Traders[i].ExchangeProvider = providerName
	}
	return nil
}

func applyPaperMarketOverride(cfg *managerpkg.Config, providerName string) error {
	if cfg == nil {
		return fmt.Errorf("manager config is nil")
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return fmt.Errorf("paper trading market provider name cannot be empty")
	}
	for i := range cfg.Traders {
		cfg.Traders[i].MarketProvider = providerName
	}
	return nil
}

func main() {
	var (
		exchangePath  = flag.String("exchange-config", "etc/exchange.yaml", "path to exchange provider configuration")
		marketPath    = flag.String("market-config", "etc/market.yaml", "path to market provider configuration")
		llmPath       = flag.String("llm-config", "etc/llm.yaml", "path to llm client configuration")
		managerPath   = flag.String("manager-config", "etc/manager.yaml", "path to manager configuration")
		appConfig     = flag.String("app-config", "etc/nof0.yaml", "path to application config for summary logging")
		allowedRaw    = flag.String("symbols", "BTC,ETH", "comma-separated list of tradable symbols")
		totalEquity   = flag.Float64("equity", 100.0, "total deployable equity in USD")
		promptProfile = flag.String("executor-prompt-profile", "default", "executor prompt profile (default|fast)")
		paperTrading  = flag.Bool("paper-trading", false, "route trades to the in-memory simulator instead of live exchanges")
		paperExchange = flag.String("paper-exchange-provider", "paper_trading", "exchange provider id to use when --paper-trading is enabled")
	)
	flag.Parse()
	logx.MustSetup(logx.LogConf{})
	logx.DisableStat()

	var runtimeCfg *appconfig.Config
	if strings.TrimSpace(*appConfig) != "" {
		if cfg, err := appconfig.Load(*appConfig); err != nil {
			logx.Errorf("load app config %s: %v", *appConfig, err)
		} else {
			cli.LogConfigSummary(cfg)
			runtimeCfg = cfg
		}
	}

	allowedSymbols := parseSymbols(*allowedRaw)
	if len(allowedSymbols) == 0 {
		fatalf("no tradable symbols provided; use --symbols to specify at least one")
	}

	confkit.LoadDotenvOnce()

	exchangeCfg, err := exchangepkg.LoadConfig(*exchangePath)
	if err != nil {
		fatalf("load exchange config: %v", err)
	}
	exchangeProviders, err := exchangeCfg.BuildProviders()
	if err != nil {
		fatalf("build exchange providers: %v", err)
	}

	marketCfg, err := marketpkg.LoadConfig(*marketPath)
	if err != nil {
		fatalf("load market config: %v", err)
	}
	marketProviders, err := marketCfg.BuildProviders()
	if err != nil {
		fatalf("build market providers: %v", err)
	}
	filteredMarkets := make(map[string]marketpkg.Provider, len(marketProviders))
	for name, provider := range marketProviders {
		wrapped, wrapErr := newFilteredMarket(provider, allowedSymbols)
		if wrapErr != nil {
			fatalf("wrap market provider %s: %v", name, wrapErr)
		}
		filteredMarkets[name] = wrapped
	}

	llmCfg, err := llmpkg.LoadConfig(*llmPath)
	if err != nil {
		fatalf("load llm config: %v", err)
	}
	llmClient, err := llmpkg.NewClient(llmCfg)
	if err != nil {
		fatalf("initialise llm client: %v", err)
	}
	defer func() {
		_ = llmClient.Close()
	}()

	managerCfg, err := managerpkg.LoadConfig(*managerPath)
	if err != nil {
		fatalf("load manager config: %v", err)
	}
	if err := applyExecutorPromptProfile(managerCfg, *promptProfile); err != nil {
		fatalf("apply executor prompt profile: %v", err)
	}
	if *paperTrading {
		name := strings.TrimSpace(*paperExchange)
		if name == "" {
			fatalf("paper trading requested but --paper-exchange-provider is empty")
		}
		if _, ok := exchangeProviders[name]; !ok {
			fatalf("paper trading requested but exchange provider %s not found; update %s", name, *exchangePath)
		}
		if err := applyPaperTradingOverride(managerCfg, name); err != nil {
			fatalf("apply paper trading override: %v", err)
		}
		marketName := "hyperliquid"
		if _, ok := filteredMarkets[marketName]; !ok {
			fatalf("paper trading requested but market provider %s not found; update %s", marketName, *marketPath)
		}
		if err := applyPaperMarketOverride(managerCfg, marketName); err != nil {
			fatalf("apply paper trading market override: %v", err)
		}
		logx.Infof("paper trading enabled: exchange=%s market=%s", name, marketName)
	}
	if err := adaptManagerConfig(managerCfg, *totalEquity, allowedSymbols); err != nil {
		fatalf("adapt manager config: %v", err)
	}

	// Validate trader-level model assignments against LLM config.
	for _, trader := range managerCfg.Traders {
		if trader.Model == "" {
			continue
		}
		if _, ok := llmCfg.Model(trader.Model); ok {
			continue
		}
		if strings.Contains(trader.Model, "/") {
			// Allow fully qualified model identifiers.
			continue
		}
		fatalf("manager trader %s references unknown model %s", trader.ID, trader.Model)
	}

	var (
		persistService managerpkg.PersistenceService
		marketPersist  marketpkg.Persistence
		svcCtx         *svc.ServiceContext
	)
	if runtimeCfg != nil {
		svcCtx = svc.NewServiceContext(*runtimeCfg, runtimeCfg.MainPath())
		ttlSet := cache.NewTTLSet(runtimeCfg.TTL)
		if runtimeCfg.TTL.Short == 0 && runtimeCfg.TTL.Medium == 0 && runtimeCfg.TTL.Long == 0 {
			logx.Slowf("cache ttl config missing; using defaults short=%s medium=%s long=%s", ttlSet.Short, ttlSet.Medium, ttlSet.Long)
		}
		persistService = enginepersist.NewService(enginepersist.Config{
			SQLConn:                   svcCtx.DBConn,
			PositionsModel:            svcCtx.PositionsModel,
			TradesModel:               svcCtx.TradesModel,
			SnapshotsModel:            svcCtx.AccountEquitySnapshotsModel,
			DecisionModel:             svcCtx.DecisionCyclesModel,
			Cache:                     svcCtx.Cache,
			Redis:                     svcCtx.Redis,
			TTL:                       ttlSet,
			ConversationsModel:        svcCtx.ConversationsModel,
			ConversationMessagesModel: svcCtx.ConversationMessagesModel,
		})
		marketPersist = marketpersist.NewService(marketpersist.Config{
			SQLConn:         svcCtx.DBConn,
			AssetsModel:     svcCtx.MarketAssetsModel,
			PriceTicksModel: svcCtx.PriceTicksModel,
			Cache:           svcCtx.Cache,
			Redis:           svcCtx.Redis,
			TTL:             ttlSet,
		})
		if persistService == nil {
			logx.Slowf("manager persistence disabled: postgres/cache not configured in %s", *appConfig)
		} else {
			logx.Infof("manager persistence enabled via %s", *appConfig)
		}
	}
	if marketPersist != nil {
		for name, provider := range marketProviders {
			if aware, ok := provider.(marketpkg.PersistenceAware); ok {
				aware.SetPersistence(marketPersist)
			}
			if wrapped, ok := filteredMarkets[name].(marketpkg.PersistenceAware); ok {
				wrapped.SetPersistence(marketPersist)
			}
		}
	}
	ingestor := ingest.NewMarketIngestor(filteredMarkets, allowedSymbols, 45*time.Second, 30*time.Minute, 150*time.Millisecond)
	var conversationRecorder executorpkg.ConversationRecorder
	if rec, ok := persistService.(executorpkg.ConversationRecorder); ok {
		conversationRecorder = rec
	}
	execFactory := managerpkg.NewBasicExecutorFactory(llmClient, conversationRecorder)

	traderSources := managerCfg.Traders
	if svcCtx != nil && svcCtx.TraderConfigRepo != nil && len(managerCfg.Traders) > 0 {
		records, err := managerpkg.TraderConfigsToRecords(managerCfg.Traders, "system", "yaml_sync")
		if err != nil {
			logx.Errorf("manager: serialize trader configs for sync: %v", err)
		} else {
			syncCtx := context.Background()
			if summary, syncErr := svcCtx.TraderConfigRepo.Sync(syncCtx, records); syncErr != nil {
				logx.Errorf("manager: sync trader configs: %v", syncErr)
			} else {
				logx.Infof("manager: trader config sync inserted=%d updated=%d unchanged=%d", len(summary.Inserted), len(summary.Updated), len(summary.Unchanged))
				if rows, listErr := svcCtx.TraderConfigRepo.ListAll(syncCtx); listErr != nil {
					logx.Errorf("manager: list trader configs: %v", listErr)
				} else if len(rows) > 0 {
					dbTraders := make([]managerpkg.TraderConfig, 0, len(rows))
					for _, row := range rows {
						cfg, decodeErr := managerpkg.TraderConfigFromModel(row)
						if decodeErr != nil {
							logx.Errorf("manager: decode trader config %s: %v", row.Id, decodeErr)
							continue
						}
						dbTraders = append(dbTraders, *cfg)
					}
					if len(dbTraders) > 0 {
						traderSources = dbTraders
					}
				}
			}
		}
	}

	var managerOpts []managerpkg.Option
	if svcCtx != nil {
		if svcCtx.TraderConfigRepo != nil {
			managerOpts = append(managerOpts, managerpkg.WithConfigRepo(svcCtx.TraderConfigRepo))
		}
		if svcCtx.TraderRuntimeRepo != nil {
			managerOpts = append(managerOpts, managerpkg.WithRuntimeRepo(svcCtx.TraderRuntimeRepo))
		}
	}

	mgr := managerpkg.NewManager(managerCfg, execFactory, exchangeProviders, filteredMarkets, persistService, managerOpts...)

	traderIDs := make([]string, 0, len(traderSources))
	for _, traderCfg := range traderSources {
		vt, regErr := mgr.RegisterTrader(context.Background(), traderCfg)
		if regErr != nil {
			fatalf("register trader %s: %v", traderCfg.ID, regErr)
		}
		logx.Infof("registered trader %s (%s) using exchange=%s market=%s model=%s", vt.ID, vt.Name, traderCfg.ExchangeProvider, traderCfg.MarketProvider, traderCfg.Model)
		traderIDs = append(traderIDs, vt.ID)
	}

	if persistService != nil && len(traderIDs) > 0 {
		hydrateCtx, hydrateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := persistService.HydrateCaches(hydrateCtx, traderIDs); err != nil {
			panic(fmt.Sprintf("manager: hydrate caches err=%v", err))
		}
		hydrateCancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if ingestor != nil {
		go ingestor.Run(ctx)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logx.Infof("received signal %s, shutting down manager loop", sig)
		cancel()
		mgr.Stop()
	}()

	logx.Infof("starting manager loop with equity=%.2f USD, symbols=%s", *totalEquity, strings.Join(allowedSymbols, ","))
	if err := mgr.RunTradingLoop(ctx); err != nil && err != context.Canceled {
		fatalf("manager loop exited with error: %v", err)
	}
	logx.Info("manager loop stopped")
}
