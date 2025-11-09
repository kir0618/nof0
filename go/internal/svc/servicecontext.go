package svc

import (
	"database/sql"
	"log"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // register pgx driver
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/cache"
	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/core/stores/sqlc"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
	"github.com/zeromicro/go-zero/core/syncx"

	"nof0-api/internal/config"
	"nof0-api/internal/data"
	"nof0-api/internal/model"
	"nof0-api/pkg/confkit"
	exchangepkg "nof0-api/pkg/exchange"
	_ "nof0-api/pkg/exchange/hyperliquid"
	_ "nof0-api/pkg/exchange/sim"
	executorpkg "nof0-api/pkg/executor"
	llmpkg "nof0-api/pkg/llm"
	managerpkg "nof0-api/pkg/manager"
	marketpkg "nof0-api/pkg/market"
	_ "nof0-api/pkg/market/exchanges/hyperliquid"
	"nof0-api/pkg/repo"
)

type ServiceContext struct {
	Config config.Config

	DataLoader *data.DataLoader

	LLMConfig              *llmpkg.Config
	ExecutorConfig         *executorpkg.Config
	ManagerConfig          *managerpkg.Config
	ManagerPromptRenderers map[string]*managerpkg.PromptRenderer
	ManagerPromptDigests   map[string]string
	ExchangeConfig         *exchangepkg.Config
	ExchangeProviders      map[string]exchangepkg.Provider
	DefaultExchange        exchangepkg.Provider
	MarketConfig           *marketpkg.Config
	MarketProviders        map[string]marketpkg.Provider
	DefaultMarket          marketpkg.Provider
	ManagerTraderExchange  map[string]exchangepkg.Provider
	ManagerTraderMarket    map[string]marketpkg.Provider

	// Optional DB models (injected but unused by handlers/logic for now)
	DBConn                      sqlx.SqlConn
	CachedConn                  *sqlc.CachedConn
	Cache                       cache.Cache
	Redis                       *redis.Redis
	ModelsModel                 model.ModelsModel
	SymbolsModel                model.SymbolsModel
	PriceTicksModel             model.PriceTicksModel
	AccountsModel               model.AccountsModel
	AccountEquitySnapshotsModel model.AccountEquitySnapshotsModel
	PositionsModel              model.PositionsModel
	TradesModel                 model.TradesModel
	ConversationsModel          model.ConversationsModel
	ConversationMessagesModel   model.ConversationMessagesModel
	DecisionCyclesModel         model.DecisionCyclesModel
	MarketAssetsModel           model.MarketAssetsModel
	TraderStateModel            model.TraderStateModel
	TraderConfigModel           model.TraderConfigModel
	TraderConfigHistoryModel    model.TraderConfigHistoryModel
	TraderRuntimeStateModel     model.TraderRuntimeStateModel
	TraderSymbolCooldownsModel  model.TraderSymbolCooldownsModel
	TraderConfigRepo            repo.TraderConfigRepository
	TraderRuntimeRepo           repo.TraderRuntimeRepository
}

func NewServiceContext(c config.Config, mainConfigPath string) *ServiceContext {
	configureLogging(c.Logging)

	svc := &ServiceContext{
		Config:     c,
		DataLoader: data.NewDataLoader(c.DataPath),
	}

	cacheNodes := filterCacheNodes(c.Cache)
	hasCache := len(cacheNodes) > 0
	var cacheOpts []cache.Option
	if c.TTL.Medium > 0 {
		cacheOpts = append(cacheOpts, cache.WithExpiry(time.Duration(c.TTL.Medium)*time.Second))
	}
	if hasCache {
		svc.Cache = cache.New(cacheNodes, syncx.NewSingleFlight(), cache.NewStat("nof0-cache"), sql.ErrNoRows, cacheOpts...)
		// Also create direct Redis client for Hash operations
		if len(cacheNodes) > 0 {
			redisConf := cacheNodes[0].RedisConf // keep TLS/user/etc so secure Redis works
			var err error
			svc.Redis, err = redis.NewRedis(redisConf)
			if err != nil {
				logx.Errorf("failed to create redis client: %v (Hash operations will be disabled)", err)
			}
		}
	}
	var rawDB *sql.DB
	if strings.TrimSpace(c.Postgres.DataSource) != "" {
		if !hasCache {
			log.Fatalf("cache configuration required when postgres is enabled")
		}
		conn := sqlx.NewSqlConn("pgx", c.Postgres.DataSource)
		raw, err := conn.RawDB()
		if err != nil {
			log.Fatalf("failed to init postgres raw db: %v", err)
		}
		applyPostgresPool(raw, c.Postgres)
		rawDB = raw
		svc.DBConn = conn
		if svc.Cache != nil {
			cached := sqlc.NewConnWithCache(conn, svc.Cache)
			svc.CachedConn = &cached
		}
	}

	baseDir := c.BaseDir()
	if baseDir == "" && mainConfigPath != "" {
		baseDir = confkit.BaseDir(mainConfigPath)
	}

	// Load LLM config if specified (hydrated by config.Load or fallback)
	llmCfg := c.LLM.Value
	if llmCfg == nil && c.LLM.File != "" {
		if baseDir == "" {
			log.Fatalf("failed to resolve base dir for llm config %s", c.LLM.File)
		}
		var err error
		llmCfg, err = llmpkg.LoadConfig(confkit.ResolvePath(baseDir, c.LLM.File))
		if err != nil {
			log.Fatalf("failed to load llm config: %v", err)
		}
	}
	if llmCfg != nil {
		if c.IsTestEnv() {
			llmCfg = llmCfg.Clone()
			llmCfg.DefaultModel = "google/gemini-2.5-flash-lite"
		}
		svc.LLMConfig = llmCfg
	}

	// Load Executor config if specified
	executorCfg := c.Executor.Value
	if executorCfg == nil && c.Executor.File != "" {
		if baseDir == "" {
			log.Fatalf("failed to resolve base dir for executor config %s", c.Executor.File)
		}
		var err error
		executorCfg, err = executorpkg.LoadConfig(confkit.ResolvePath(baseDir, c.Executor.File))
		if err != nil {
			log.Fatalf("failed to load executor config: %v", err)
		}
	}
	if executorCfg != nil {
		svc.ExecutorConfig = executorCfg
	}

	var managerPromptGuard *llmpkg.TemplateVersionGuard
	if executorCfg != nil {
		managerPromptGuard = &llmpkg.TemplateVersionGuard{
			Component:            "manager.prompt",
			ExpectedVersion:      executorCfg.PromptSchemaVersion,
			RequireVersionHeader: executorCfg.PromptValidation.RequireVersionHeader,
			StrictMode:           executorCfg.PromptValidation.StrictMode,
		}
	}

	// Load Manager config if specified
	managerCfg := c.Manager.Value
	if managerCfg == nil && c.Manager.File != "" {
		if baseDir == "" {
			log.Fatalf("failed to resolve base dir for manager config %s", c.Manager.File)
		}
		var err error
		managerCfg, err = managerpkg.LoadConfig(confkit.ResolvePath(baseDir, c.Manager.File))
		if err != nil {
			log.Fatalf("failed to load manager config: %v", err)
		}
	}
	if managerCfg != nil {
		renderers := make(map[string]*managerpkg.PromptRenderer, len(managerCfg.Traders))
		digests := make(map[string]string, len(managerCfg.Traders))
		for i := range managerCfg.Traders {
			tr := &managerCfg.Traders[i]
			renderer, err := managerpkg.NewPromptRenderer(tr.PromptTemplate, managerPromptGuard)
			if err != nil {
				log.Fatalf("failed to init manager prompt renderer for trader %s: %v", tr.ID, err)
			}
			renderers[tr.ID] = renderer
			digests[tr.ID] = renderer.Digest()
		}
		svc.ManagerConfig = managerCfg
		svc.ManagerPromptRenderers = renderers
		svc.ManagerPromptDigests = digests
	}

	// Load Exchange config if specified
	exchangeCfg := c.Exchange.Value
	if exchangeCfg == nil && c.Exchange.File != "" {
		if baseDir == "" {
			log.Fatalf("failed to resolve base dir for exchange config %s", c.Exchange.File)
		}
		var err error
		exchangeCfg, err = exchangepkg.LoadConfig(confkit.ResolvePath(baseDir, c.Exchange.File))
		if err != nil {
			log.Fatalf("failed to load exchange config: %v", err)
		}
	}
	if exchangeCfg != nil {
		if c.IsTestEnv() {
			for _, provider := range exchangeCfg.Providers {
				provider.Testnet = true
			}
		}
		providers, err := exchangeCfg.BuildProviders()
		if err != nil {
			log.Fatalf("failed to build exchange providers: %v", err)
		}
		svc.ExchangeConfig = exchangeCfg
		svc.ExchangeProviders = providers
		if exchangeCfg.Default != "" {
			svc.DefaultExchange = providers[exchangeCfg.Default]
		}
	}

	// Load Market config if specified
	marketCfg := c.Market.Value
	if marketCfg == nil && c.Market.File != "" {
		if baseDir == "" {
			log.Fatalf("failed to resolve base dir for market config %s", c.Market.File)
		}
		var err error
		marketCfg, err = marketpkg.LoadConfig(confkit.ResolvePath(baseDir, c.Market.File))
		if err != nil {
			log.Fatalf("failed to load market config: %v", err)
		}
	}
	if marketCfg != nil {
		providers, err := marketCfg.BuildProviders()
		if err != nil {
			log.Fatalf("failed to build market providers: %v", err)
		}
		svc.MarketConfig = marketCfg
		svc.MarketProviders = providers
		if marketCfg.Default != "" {
			svc.DefaultMarket = providers[marketCfg.Default]
		}
	}

	// Validate cross-module references: manager trader -> exchange/market providers
	if svc.ManagerConfig != nil {
		svc.ManagerTraderExchange = make(map[string]exchangepkg.Provider, len(svc.ManagerConfig.Traders))
		svc.ManagerTraderMarket = make(map[string]marketpkg.Provider, len(svc.ManagerConfig.Traders))
		for i := range svc.ManagerConfig.Traders {
			trader := &svc.ManagerConfig.Traders[i]
			// Strict mapping: manager config requires explicit provider IDs
			exProvider, ok := svc.ExchangeProviders[trader.ExchangeProvider]
			if !ok {
				log.Fatalf("manager trader %s references unknown exchange provider %s", trader.ID, trader.ExchangeProvider)
			}
			svc.ManagerTraderExchange[trader.ID] = exProvider

			mktProvider, ok := svc.MarketProviders[trader.MarketProvider]
			if !ok {
				log.Fatalf("manager trader %s references unknown market provider %s", trader.ID, trader.MarketProvider)
			}
			svc.ManagerTraderMarket[trader.ID] = mktProvider
		}
	}

	// Only inject DB models when DSN provided; business logic still uses DataLoader.
	if svc.DBConn != nil {
		conn := svc.DBConn
		svc.ModelsModel = model.NewModelsModel(conn, cacheNodes, cacheOpts...)
		svc.SymbolsModel = model.NewSymbolsModel(conn, cacheNodes, cacheOpts...)
		svc.PriceTicksModel = model.NewPriceTicksModel(conn, cacheNodes, cacheOpts...)
		svc.AccountsModel = model.NewAccountsModel(conn, cacheNodes, cacheOpts...)
		svc.AccountEquitySnapshotsModel = model.NewAccountEquitySnapshotsModel(conn, cacheNodes, cacheOpts...)
		svc.PositionsModel = model.NewPositionsModel(conn, cacheNodes, cacheOpts...)
		svc.TradesModel = model.NewTradesModel(conn, cacheNodes, cacheOpts...)
		svc.ConversationsModel = model.NewConversationsModel(conn, cacheNodes, cacheOpts...)
		svc.ConversationMessagesModel = model.NewConversationMessagesModel(conn, cacheNodes, cacheOpts...)
		svc.DecisionCyclesModel = model.NewDecisionCyclesModel(conn, cacheNodes, cacheOpts...)
		svc.MarketAssetsModel = model.NewMarketAssetsModel(conn, cacheNodes, cacheOpts...)
		svc.TraderStateModel = model.NewTraderStateModel(conn, cacheNodes, cacheOpts...)
		svc.TraderConfigModel = model.NewTraderConfigModel(conn, cacheNodes, cacheOpts...)
		svc.TraderConfigHistoryModel = model.NewTraderConfigHistoryModel(conn, cacheNodes, cacheOpts...)
		svc.TraderRuntimeStateModel = model.NewTraderRuntimeStateModel(conn, cacheNodes, cacheOpts...)
		svc.TraderSymbolCooldownsModel = model.NewTraderSymbolCooldownsModel(conn, cacheNodes, cacheOpts...)
		if rawDB != nil {
			svc.TraderConfigRepo = repo.NewTraderConfigRepository(
				svc.TraderConfigModel,
				svc.TraderConfigHistoryModel,
				rawDB,
			)
		}
		svc.TraderRuntimeRepo = repo.NewTraderRuntimeRepository(
			svc.TraderRuntimeStateModel,
			svc.TraderSymbolCooldownsModel,
		)
	}

	return svc
}

func applyPostgresPool(db *sql.DB, cfg config.PostgresConf) {
	if cfg.MaxIdle > 0 {
		db.SetMaxIdleConns(cfg.MaxIdle)
	}
	if cfg.MaxOpen > 0 {
		db.SetMaxOpenConns(cfg.MaxOpen)
	}
	if cfg.MaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.MaxLifetime)
	} else {
		db.SetConnMaxLifetime(5 * time.Minute)
	}
}

func filterCacheNodes(conf cache.CacheConf) cache.CacheConf {
	nodes := make(cache.CacheConf, 0, len(conf))
	for _, node := range conf {
		host := strings.TrimSpace(node.Host)
		if host == "" {
			continue
		}
		if strings.TrimSpace(node.Type) == "" {
			node.Type = "node"
		}
		node.Host = host
		nodes = append(nodes, node)
	}
	return nodes
}

func configureLogging(cfg config.LoggingConf) {
	if cfg.SlowThreshold.SQL > 0 {
		sqlx.SetSlowThreshold(time.Duration(cfg.SlowThreshold.SQL) * time.Millisecond)
	}
	if cfg.SlowThreshold.Redis > 0 {
		redis.SetSlowThreshold(time.Duration(cfg.SlowThreshold.Redis) * time.Millisecond)
	}
	if !cfg.VerboseSQL {
		sqlx.DisableStmtLog()
	}
	llmpkg.SetVerboseLogging(cfg.VerboseLLM)
}
