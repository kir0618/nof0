# NOF0 存储架构重构计划 v2

> **重构背景**: 新系统启动,可直接删除旧数据/旧表
> **设计原则**: 基于 blueprint.md 黄金标准 - 可回放、可验证、可切换
> **评审日期**: 2025-01-08
> **执行周期**: 10周 (50.5工作日)

---

## 📋 执行摘要

### 核心问题
1. ❌ Trader配置未持久化,违反"配置即数据"原则
2. ❌ Redis键结构碎片化,500+个String键造成内存浪费
3. ❌ PostgreSQL表字段冗余严重,50%字段无必要
4. ❌ 物化视图与"API读JSON"架构冲突
5. ❌ 实时数据混入Postgres,违反存储分层原则

### 重构策略
- **简化迁移**: 直接创建新表,删除旧表,无需复杂数据迁移
- **快速验证**: 每个Phase独立可回滚,灰度发布
- **架构对齐**: 所有变更严格遵循 blueprint.md 设计理念

---

## 🏗️ 代码架构设计原则

### 数据访问层架构

本次重构遵循**清晰的三层架构**，确保代码可维护、可测试、可复用：

```
┌────────────────────────────────────────────────────────┐
│ pkg/manager, pkg/executor                              │  业务逻辑层
│ - 纯业务逻辑，不直接操作数据库                         │
│ - 依赖 Repository 接口，便于测试和替换                 │
└────────────────────────────────────────────────────────┘
                         ↓ 依赖接口
┌────────────────────────────────────────────────────────┐
│ pkg/repo/                                              │  Repository 层
│ - 复杂查询、业务编排、事务管理                         │
│ - 聚合多个 model，提供领域数据访问接口                 │
│ - 可被多处复用（manager、API、CLI 工具等）             │
└────────────────────────────────────────────────────────┘
                         ↓ 依赖
┌────────────────────────────────────────────────────────┐
│ internal/model/                                        │  表映射层
│ - 1:1 对应数据库表，goctl 自动生成 CRUD                │
│ - *model_gen.go: goctl 生成的基础 CRUD                 │
│ - *model.go: 手写的自定义查询（简单 SQL）              │
│ - 纯数据访问，不包含业务逻辑                           │
└────────────────────────────────────────────────────────┘
```

### 职责划分

#### internal/model/ - 表映射层
- **职责**: 数据库表的直接映射，提供基础 CRUD 操作
- **命名**: `{table_name}model_gen.go` + `{table_name}model.go`
- **生成方式**: 使用 `goctl model pg datasource` 自动生成
- **自定义查询**: 简单的 SQL 查询（单表、简单 JOIN）

**示例**:
```go
// internal/model/traderconfigmodel_gen.go (goctl 生成)
type TraderConfigModel interface {
    Insert(ctx, data) error
    FindOne(ctx, id) (*TraderConfig, error)
    Update(ctx, data) error
    Delete(ctx, id) error
}

// internal/model/traderconfigmodel.go (手写自定义查询)
type TraderConfigModel interface {
    traderConfigModel  // 继承生成的接口
    FindByVersion(ctx, traderID, version) (*TraderConfig, error)  // 简单自定义查询
}
```

#### pkg/repo/ - Repository 层
- **职责**: 复杂查询、业务编排、跨表事务
- **命名**: `{domain}_repo.go` (例如: `trader_config_repo.go`)
- **特点**:
  - 聚合多个 model（例如: config + history）
  - 包含业务编排逻辑（例如: 同步配置 + 记录历史）
  - 管理事务边界
  - 可被多个模块复用

**示例**:
```go
// pkg/repo/trader_config_repo.go
type TraderConfigRepository interface {
    // 基础 CRUD（委托给 model）
    FindOne(ctx, traderID) (*model.TraderConfig, error)

    // 复杂业务逻辑
    SyncFromYAML(ctx, yamlPath) error  // 读取 yaml + 对比差异 + 更新版本 + 记录 history
    FindByVersion(ctx, traderID, version) (*model.TraderConfig, error)
    ListHistory(ctx, traderID, limit) ([]*model.TraderConfigHistory, error)
}

type traderConfigRepo struct {
    configModel  model.TraderConfigModel
    historyModel model.TraderConfigHistoryModel
    db           *sql.DB  // 用于事务
}

// 事务示例：更新配置 + 记录历史
func (r *traderConfigRepo) updateWithHistory(ctx, cfg, version, changedFields) error {
    return r.withTx(ctx, func(tx *sql.Tx) error {
        // 1. 更新 trader_config
        if err := r.configModel.UpdateTx(tx, cfg); err != nil {
            return err
        }
        // 2. 插入 trader_config_history
        history := buildHistory(cfg, version, changedFields)
        return r.historyModel.InsertTx(tx, history)
    })
}
```

#### pkg/manager/ - 业务逻辑层
- **职责**: 核心业务逻辑，编排多个 Repository
- **依赖**: 只依赖 Repository 接口，不直接访问数据库
- **测试**: 可以 mock Repository 进行单元测试

**示例**:
```go
// pkg/manager/manager.go
type Manager struct {
    configRepo repo.TraderConfigRepository  // 依赖接口
    // ...
}

func (m *Manager) Start(ctx context.Context) error {
    // 业务编排：同步配置 → 加载 Trader → 启动
    if err := m.configRepo.SyncFromYAML(ctx, "etc/manager.yaml"); err != nil {
        return err
    }

    traders, err := m.configRepo.LoadAllConfigs(ctx)
    for _, trader := range traders {
        m.startTrader(trader)
    }
    return nil
}
```

### 新旧代码共存策略

**现状**:
- 旧表（positions, trades）使用 `internal/persistence/engine/persistence.go`

**重构策略**:
- ✅ **新表**（trader_config, trader_runtime_state）: 使用 `pkg/repo/`
- ✅ **旧表**: 保持 `internal/persistence/engine/` 不变
- 🔄 **可选**: 未来逐步将旧表迁移到 `pkg/repo/`（非必须）

两套架构可以并存，互不影响。新代码遵循新规范，旧代码保持稳定。

### 目录结构示例

```
internal/model/
  ├─ traderconfigmodel_gen.go          # goctl 生成
  ├─ traderconfigmodel.go              # 手写自定义查询
  ├─ traderconfighistorymodel_gen.go
  ├─ traderconfighistorymodel.go
  ├─ traderruntimestatemodel_gen.go
  └─ traderruntimestatemodel.go

pkg/repo/
  ├─ trader_config_repo.go             # TraderConfig 的复杂逻辑
  ├─ trader_runtime_state_repo.go
  └─ repo.go                           # 公共事务辅助函数

pkg/manager/
  ├─ manager.go
  └─ config_sync.go                    # 调用 repo 接口

internal/persistence/engine/
  └─ persistence.go                    # 旧表保持不变
```

### 优势总结

1. **清晰分层**: model（表映射）→ repo（数据访问）→ 业务逻辑
2. **可测试性**: 业务层依赖接口，可 mock Repository
3. **可复用性**: pkg/repo 可被 manager、API、CLI 等多处使用
4. **易维护性**: 职责单一，修改影响范围小
5. **渐进式**: 新旧代码可并存，无需大规模重构

---

## 🔴 Phase 1: Trader配置持久化 (P0 核心)

**优先级**: 🔴🔴🔴 **最高** (阻塞其他所有重构)
**工期**: 10天
**负责人**: TBD

### 问题描述

**当前架构缺陷**:
- Trader完整配置存储在 `etc/manager.yaml` (18个字段)
- 数据库仅存储部分运行时状态
- 无法追溯历史配置,违反"可回放"原则

**违反的黄金标准**:
```yaml
# blueprint.md 设计理念
- Trader即策略容器: 必须显式声明讯源、交易通道、提示词、风控、频率、资金域
- Prompt版本锁定: Executor仅在版本匹配时加载
- 可回放性: Journal记录决策,需追溯当时的Prompt模板路径、LLM模型、决策间隔
```

### 技术方案

#### 新表结构

```sql
-- 1. trader_config: 核心列 + JSONB详情
CREATE TABLE trader_config (
    -- 核心列（5个）
    id                   TEXT PRIMARY KEY,
    version              INT NOT NULL DEFAULT 1,
    exchange_provider    TEXT NOT NULL,           -- JOIN查询
    market_provider      TEXT NOT NULL,           -- JOIN查询
    allocation_pct       NUMERIC(5,2) NOT NULL,   -- SUM聚合校验

    -- 完整配置详情
    detail               JSONB NOT NULL,

    -- 审计字段
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by           TEXT,

    -- 约束
    CHECK (allocation_pct >= 0 AND allocation_pct <= 100)
);

-- detail JSONB结构示例:
-- {
--   "display_name": "Aggressive Short",
--   "description": "激进做空策略",
--   "auto_start": true,
--   "prompts": {
--     "system_template": "prompts/manager/aggressive_short.tmpl",
--     "system_version": "v1.2.0",
--     "executor_template": "prompts/executor/default_prompt.tmpl",
--     "executor_version": "v1.2.0"
--   },
--   "llm": {
--     "model": "deepseek-chat",
--     "decision_interval_seconds": 180
--   },
--   "order": {
--     "style": "market_ioc",
--     "slippage_bps": 75
--   },
--   "risk": {
--     "max_positions": 3,
--     "max_position_size_usd": 500,
--     "max_margin_usage_pct": 60,
--     "major_coin_leverage": 20,
--     "altcoin_leverage": 10,
--     "min_risk_reward_ratio": 3.0,
--     "min_confidence": 75,
--     "stop_loss_enabled": true,
--     "take_profit_enabled": true
--   }
-- }

CREATE INDEX idx_trader_config_providers
    ON trader_config(exchange_provider, market_provider);

-- 2. trader_runtime_state: 核心列 + JSONB详情
CREATE TABLE trader_runtime_state (
    -- 核心列（3个）
    trader_id            TEXT PRIMARY KEY REFERENCES trader_config(id) ON DELETE CASCADE,
    active_config_version INT NOT NULL DEFAULT 1,
    is_running           BOOLEAN NOT NULL DEFAULT FALSE,

    -- 运行时状态详情
    detail               JSONB NOT NULL DEFAULT '{}'::jsonb,

    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- detail JSONB结构示例:
-- {
--   "decision": {
--     "last_at": "2025-01-08T10:30:00Z",
--     "next_at": "2025-01-08T10:33:00Z"
--   },
--   "pause": {
--     "until": null,
--     "reason": null
--   },
--   "allocation": {
--     "equity_usd": 1000.00,
--     "used_margin_usd": 300.00,
--     "available_margin_usd": 700.00
--   },
--   "performance": {
--     "sharpe_ratio": 1.5,
--     "total_pnl_usd": 150.00
--   }
-- }

-- 3. trader_symbol_cooldowns: 核心列 + JSONB详情
CREATE TABLE trader_symbol_cooldowns (
    -- 核心列（3个）
    trader_id      TEXT NOT NULL REFERENCES trader_config(id) ON DELETE CASCADE,
    symbol         TEXT NOT NULL,
    cooldown_until TIMESTAMPTZ NOT NULL,

    -- 冷却详情
    detail         JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trader_id, symbol)
);

-- detail JSONB结构示例:
-- {
--   "reason": "consecutive_losses",
--   "consecutive_losses": 3,
--   "last_loss_pnl": -50.00,
--   "triggered_at": "2025-01-08T10:00:00Z"
-- }

CREATE INDEX idx_cooldowns_expire
    ON trader_symbol_cooldowns(cooldown_until)
    WHERE cooldown_until > NOW();

-- 4. trader_config_history
CREATE TABLE trader_config_history (
    id              BIGSERIAL PRIMARY KEY,
    trader_id       TEXT NOT NULL REFERENCES trader_config(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    config_snapshot JSONB NOT NULL,
    changed_fields  TEXT[],
    change_reason   TEXT,
    changed_by      TEXT,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_config_history_trader_version
    ON trader_config_history(trader_id, version DESC);
```

### 实施步骤

**Step 1: 创建新表** (Day 1-2)
```bash
migrations/008_create_trader_config.up.sql
migrations/008_create_trader_config.down.sql
migrate -path migrations -database "postgres://..." up
```

**Step 2: 配置同步逻辑** (Day 3-4)
```go
// pkg/repo/trader_config_repo.go
package repo

import (
    "context"
    "database/sql"
    "nof0-api/internal/model"
)

type TraderConfigRepository interface {
    // 基础 CRUD（委托给 model）
    FindOne(ctx context.Context, traderID string) (*model.TraderConfig, error)
    ListAll(ctx context.Context) ([]*model.TraderConfig, error)

    // 复杂业务逻辑
    SyncFromYAML(ctx context.Context, yamlPath string) error
    FindByVersion(ctx context.Context, traderID string, version int) (*model.TraderConfig, error)
    ListHistory(ctx context.Context, traderID string, limit int) ([]*model.TraderConfigHistory, error)
}

type traderConfigRepo struct {
    configModel  model.TraderConfigModel
    historyModel model.TraderConfigHistoryModel
    db           *sql.DB
}

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

// SyncFromYAML 同步 yaml 配置到数据库
func (r *traderConfigRepo) SyncFromYAML(ctx context.Context, yamlPath string) error {
    yamlConfig := loadManagerYAML(yamlPath)

    for _, yamlTrader := range yamlConfig.Traders {
        dbTrader, err := r.configModel.FindOne(ctx, yamlTrader.ID)

        if err == sql.ErrNoRows {
            // 首次部署：事务插入 config + history
            if err := r.insertWithHistory(ctx, yamlTrader, "initial", "system"); err != nil {
                return err
            }
            logx.Infof("Inserted new trader config: %s", yamlTrader.ID)
            continue
        }

        // 检测配置变化
        if hasChanges(dbTrader, yamlTrader) {
            changedFields := diffFields(dbTrader, yamlTrader)
            // 事务更新 config + 插入 history
            if err := r.updateWithHistory(ctx, yamlTrader, dbTrader.Version+1, changedFields); err != nil {
                return err
            }
            logx.Infof("Updated trader config: %s (v%d -> v%d, changed: %v)",
                yamlTrader.ID, dbTrader.Version, dbTrader.Version+1, changedFields)
        }
    }

    return nil
}

// insertWithHistory 事务插入配置和历史记录
func (r *traderConfigRepo) insertWithHistory(ctx context.Context, cfg TraderConfig, reason, changedBy string) error {
    return r.withTx(ctx, func(tx *sql.Tx) error {
        // 1. 插入 trader_config
        if err := r.configModel.InsertTx(tx, cfg); err != nil {
            return err
        }
        // 2. 插入 trader_config_history
        history := &model.TraderConfigHistory{
            TraderID:       cfg.ID,
            Version:        cfg.Version,
            ConfigSnapshot: cfg.Detail,
            ChangeReason:   reason,
            ChangedBy:      changedBy,
        }
        return r.historyModel.InsertTx(tx, history)
    })
}

// updateWithHistory 事务更新配置和记录历史
func (r *traderConfigRepo) updateWithHistory(
    ctx context.Context,
    cfg TraderConfig,
    newVersion int,
    changedFields []string,
) error {
    return r.withTx(ctx, func(tx *sql.Tx) error {
        cfg.Version = newVersion
        // 1. 更新 trader_config
        if err := r.configModel.UpdateTx(tx, cfg); err != nil {
            return err
        }
        // 2. 插入 trader_config_history
        history := &model.TraderConfigHistory{
            TraderID:       cfg.ID,
            Version:        newVersion,
            ConfigSnapshot: cfg.Detail,
            ChangedFields:  changedFields,
            ChangeReason:   "yaml_sync",
            ChangedBy:      "system",
        }
        return r.historyModel.InsertTx(tx, history)
    })
}

// pkg/manager/manager.go
type Manager struct {
    configRepo repo.TraderConfigRepository  // 依赖 Repository 接口
    // ...
}

func (m *Manager) Start(ctx context.Context) error {
    // 启动时自动同步配置
    if err := m.configRepo.SyncFromYAML(ctx, "etc/manager.yaml"); err != nil {
        return fmt.Errorf("sync config from yaml: %w", err)
    }

    // 从数据库加载配置
    traders, err := m.configRepo.ListAll(ctx)
    if err != nil {
        return fmt.Errorf("load traders: %w", err)
    }

    // 启动各个 Trader
    for _, trader := range traders {
        m.startTrader(trader)
    }

    return nil
}
```

**Step 3: 代码适配** (Day 5-7)
```go
// 统一使用 trader_id 替代 model_id
// 影响文件:
internal/persistence/engine/persistence.go
internal/model/*.go
internal/svc/servicecontext.go
```

**Step 4: Journal集成** (Day 8-9)
```go
// pkg/journal/entry.go
type DecisionEntry struct {
    TraderID      string `json:"trader_id"`
    ConfigVersion int    `json:"config_version"`  // 新增
}
```

**Step 5: 删除旧表** (Day 10)
```sql
DROP TABLE IF EXISTS models CASCADE;
DROP TABLE IF EXISTS trader_state CASCADE;
```

### 验收标准

- [ ] `trader_config` 表包含所有 `etc/manager.yaml` 字段
- [ ] Journal 记录包含 `config_version`
- [ ] 可回放历史决策时恢复当时配置
- [ ] 所有代码中 `model_id` 已替换为 `trader_id`

### 收益

- ✅ 完整可回放: Journal + config_version 可精确重建历史环境
- ✅ 审计能力: 查询任意时间点的Trader配置
- ✅ 支持热切换: 更新配置无需重启进程
- ✅ A/B测试支持: 同一策略可运行多个配置版本

---

## 🔴 Phase 2: Redis键结构优化 (P0 性能)

**优先级**: 🔴🔴🔴 **最高**
**工期**: 8天
**依赖**: 无

### 问题描述

**当前架构缺陷**:
- 100个trader × 5种数据类型 = 500+ 个 String 键
- 每个Redis String键元数据开销: ~96 bytes
- 内存碎片化: 实测1000个小String vs 1个Hash → 内存差异 30%

### 技术方案

#### 新Redis键设计

```
数据类型           新键模式                    Redis类型
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Price:
  nof0:price:latest                Hash

Market:
  nof0:market:asset:{provider}     Hash
  nof0:market:ctx                  Hash

Trader数据:
  nof0:trader:positions            Hash
  nof0:trader:trades_recent        Hash
  nof0:trader:analytics            Hash
  nof0:trader:since_inception      Hash
  nof0:trader:decision_last        Hash

Leaderboard:
  nof0:leaderboard                 ZSet

Conversations:
  nof0:conversations:{trader_id}   List
```

### 实施步骤

**Step 1: 创建Hash Adapter** (Day 1-2)
**Step 2: 双写模式** (Day 3-5)
**Step 3: 灰度验证** (Day 6-7)
**Step 4: 完全切换** (Day 8)

### 验收标准

- [ ] Redis键数量: 500+ → 10
- [ ] 内存使用减少 25-30%
- [ ] 查询模式统一
- [ ] 无性能回退

### 收益

- ✅ 内存节省 25-30%
- ✅ 键数量减少: 500+ → 10
- ✅ 查询模式统一
- ✅ 语义清晰

---

## 🟡 Phase 3: PostgreSQL表结构规范化 (P1 数据完整性)

**优先级**: 🟡🟡 **高**
**工期**: 10天
**依赖**: Phase 1

### 3.1 positions表精简 (6天)

#### 问题描述

**字段冗余**:
- 总字段数: 26个
- 冗余字段: 13个 (50%)
- 单行大小: ~450 bytes

#### 技术方案

```sql
-- positions: 核心列 + JSONB详情
CREATE TABLE positions (
    -- 核心列（5个）
    id         TEXT PRIMARY KEY,                    -- {trader_id}|{symbol}
    trader_id  TEXT NOT NULL,
    symbol     TEXT NOT NULL,
    side       TEXT NOT NULL CHECK(side IN ('long', 'short')),
    status     TEXT NOT NULL CHECK(status IN ('open', 'closed')),

    -- 持仓详情
    detail     JSONB NOT NULL,

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),

    FOREIGN KEY (trader_id) REFERENCES trader_config(id) ON DELETE CASCADE
);

-- detail JSONB结构示例:
-- {
--   "entry": {
--     "price": 45000.50,
--     "quantity": 0.01,
--     "time_ms": 1704678000000,
--     "leverage": 10
--   },
--   "exchange": {
--     "provider": "hyperliquid_testnet",
--     "position_id": "12345"
--   },
--   "orders": {
--     "entry_oid": 67890,
--     "tp_oid": 67891,
--     "sl_oid": 67892
--   },
--   "exit_plan": {
--     "tp_price": 46000.00,
--     "sl_price": 44500.00,
--     "trailing_stop_pct": null
--   },
--   "risk": {
--     "confidence": 85,
--     "risk_usd": 50.00
--   }
-- }

CREATE INDEX idx_positions_trader_status
    ON positions(trader_id, status);

CREATE INDEX idx_positions_symbol_open
    ON positions(symbol) WHERE status = 'open';
```

#### 验收标准

- [ ] positions表字段: 26个 → **5个核心列 + 1个JSONB**
- [ ] Schema极简稳定，配置演化无需ALTER TABLE
- [ ] 外键约束防止数据孤岛
- [ ] 索引支持高频查询（trader_id, status, symbol）

### 3.2 trades表拆分 (4天)

#### 技术方案

```sql
-- trades: 核心列 + JSONB详情
CREATE TABLE trades (
    -- 核心列（5个）
    id          TEXT PRIMARY KEY,                -- {trader_id}|{symbol}|{close_ts_nano}
    trader_id   TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    side        TEXT NOT NULL CHECK(side IN ('long', 'short')),
    close_ts_ms BIGINT NOT NULL,                 -- 用于排序查询

    -- 交易详情
    detail      JSONB NOT NULL,

    created_at  TIMESTAMPTZ DEFAULT NOW(),

    FOREIGN KEY (trader_id) REFERENCES trader_config(id)
);

-- detail JSONB结构示例:
-- {
--   "time": {
--     "open_ts_ms": 1704678000000,
--     "close_ts_ms": 1704678300000,
--     "duration_seconds": 300
--   },
--   "prices": {
--     "avg_entry": 45000.50,
--     "avg_exit": 45500.00
--   },
--   "quantity": {
--     "total": 0.01
--   },
--   "pnl": {
--     "gross": 5.00,
--     "commission": 0.50,
--     "net": 4.50
--   },
--   "risk": {
--     "leverage": 10,
--     "confidence": 85
--   },
--   "exchange": {
--     "provider": "hyperliquid_testnet"
--   },
--   "fills": [
--     {
--       "type": "entry",
--       "order_id": 67890,
--       "fill_id": 12345,
--       "price": 45000.50,
--       "quantity": 0.01,
--       "commission": 0.25,
--       "is_crossed": false,
--       "ts_ms": 1704678000000
--     },
--     {
--       "type": "exit",
--       "order_id": 67891,
--       "fill_id": 12346,
--       "price": 45500.00,
--       "quantity": 0.01,
--       "commission": 0.25,
--       "is_crossed": true,
--       "ts_ms": 1704678300000
--     }
--   ]
-- }

CREATE INDEX idx_trades_trader_time
    ON trades(trader_id, close_ts_ms DESC);
```

#### 验收标准

- [ ] trades表字段: 28个 → **5个核心列 + 1个JSONB**
- [ ] fills数据存储在detail.fills数组中
- [ ] Schema极简稳定，无需单独的fills表
- [ ] 索引支持时间范围查询

---

## 🟠 Phase 4: JSONB字段结构化 (P2 代码可维护性)

**优先级**: 🟠🟠 **中**
**工期**: 5天
**依赖**: Phase 3

### 技术方案

```sql
-- 删除未使用字段
ALTER TABLE positions DROP COLUMN IF EXISTS index_col;
ALTER TABLE trades
    DROP COLUMN IF EXISTS entry_liquidation,
    DROP COLUMN IF EXISTS exit_liquidation;
```

### 验收标准

- [ ] 删除3个未使用JSONB字段
- [ ] JSONB字段数: 18个 → 12个 (减少33%)

---

## 🟢 Phase 5: 删除冗余Postgres表 (P3 架构优化)

**优先级**: 🟢 **低**
**工期**: 4天
**依赖**: Phase 1

### 技术方案

#### 5.1 删除 price_latest 表 (Day 1)
```sql
DROP TABLE IF EXISTS price_latest CASCADE;
```

#### 5.2 删除 market_asset_ctx 表 (Day 2)
```sql
DROP TABLE IF EXISTS market_asset_ctx CASCADE;
```

#### 5.3 删除 model_analytics 表 (Day 3-4)
```sql
DROP TABLE IF EXISTS model_analytics CASCADE;
```

### 验收标准

- [ ] 删除3张冗余表
- [ ] Postgres写入压力降低 60%+
- [ ] Redis缓存覆盖率 > 99%

---

## 🟢 Phase 6: 删除物化视图 (P3 架构对齐)

**优先级**: 🟢 **低**
**工期**: 5.5天
**依赖**: 无

### 技术方案

```sql
DROP MATERIALIZED VIEW IF EXISTS v_since_inception;
DROP MATERIALIZED VIEW IF EXISTS v_leaderboard;
DROP MATERIALIZED VIEW IF EXISTS v_crypto_prices_latest;
DROP FUNCTION IF EXISTS refresh_views_nof0();
```

### 验收标准

- [ ] 删除3个物化视图
- [ ] JSON导出器每60s执行正常
- [ ] 导出耗时 < 5s

---

## 🟢 Phase 7: 索引优化 (P3 性能)

**优先级**: 🟢 **低**
**工期**: 2天
**依赖**: 无

### 技术方案

```sql
-- 覆盖索引
CREATE INDEX idx_trades_recent_covering
    ON trades(trader_id, entry_ts_ms DESC)
    INCLUDE (symbol, side, realized_net_pnl, confidence, leverage, quantity);

CREATE INDEX idx_positions_open_trader_symbol
    ON positions(trader_id, symbol)
    WHERE status = 'open'
    INCLUDE (side, quantity, entry_price, leverage);
```

### 验收标准

- [ ] 慢查询 < 100ms
- [ ] 查询性能提升 > 50%

---

## 📊 总体进度与工时

| Phase | 优先级 | 问题描述              | 工期   |
|-------|--------|----------------------|--------|
| 1     | 🔴     | Trader配置持久化      | 10天   |
| 2     | 🔴     | Redis Hash重构       | 8天    |
| 3.1   | 🟡     | positions表规范化    | 6天    |
| 3.2   | 🟡     | trades表拆分         | 4天    |
| 4     | 🟠     | JSONB结构化          | 5天    |
| 5     | 🟢     | 删除冗余Postgres表   | 4天    |
| 6     | 🟢     | 删除物化视图         | 5.5天  |
| 7     | 🟢     | 索引优化             | 2天    |

**总计**: **44.5天** (约 9周)

### 执行时间线

```
Week 1-2:  Phase 1 (Trader配置持久化)
Week 3-4:  Phase 2 (Redis Hash重构)
Week 5:    Phase 3.1 (positions表规范化)
Week 6:    Phase 3.2 (trades表拆分) + Phase 4
Week 7:    Phase 5 (删除冗余表)
Week 8:    Phase 6 (删除物化视图)
Week 9:    Phase 7 (索引优化) + 最终验收
```

---

## ✅ 验收清单

### 最终验收

**架构对齐**:
- [ ] 符合 blueprint.md "可回放、可验证、可切换" 原则
- [ ] 符合"API读JSON"架构
- [ ] 符合存储分层原则

**性能指标**:
- [ ] Redis内存减少 25-30%
- [ ] Postgres写入压力降低 60%+
- [ ] 慢查询 < 100ms

**代码质量**:
- [ ] 统一使用 `trader_id`
- [ ] 消除JSONB滥用 (18个 → 12个)
- [ ] 消除表字段冗余 (50% → 0%)

---

## 🔄 回滚策略

### 每个Phase的回滚

```sql
-- 执行 down 迁移
migrate -path migrations -database "..." down 1

-- 恢复旧代码
git revert <commit-hash>
```

### 灰度发布策略

**Phase 2 (Redis重构)**:
- Week 1: 双写模式
- Week 2: 验证数据一致性
- Week 3: 完全切换
- Week 4: 清理旧键

---

## 📝 相关文件清单

### 配置文件
- `etc/nof0.yaml`
- `etc/manager.yaml`
- `migrations/*.sql`

### 核心代码
- `internal/cache/keys.go`
- `internal/persistence/engine/persistence.go`
- `internal/model/*.go`

### 新增文件
- `pkg/repo/trader_config_repo.go`
- `pkg/repo/trader_runtime_state_repo.go`
- `pkg/repo/repo.go` (公共事务辅助)
- `internal/cache/hash_adapter.go`

---

## 🎯 优先级说明

- 🔴 **P0 最高优先级**: 必须立即执行,阻塞其他重构,影响核心架构
- 🟡 **P1 高优先级**: 建议1个月内完成,影响数据完整性
- 🟠 **P2 中优先级**: 建议2个月内完成,影响代码可维护性
- 🟢 **P3 低优先级**: 可根据资源情况安排,锦上添花

---

**最后更新**: 2025-01-08
**文档版本**: v2.0
**状态**: 待审核
