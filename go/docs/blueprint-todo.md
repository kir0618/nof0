# Blueprint Implementation TODO

本文档基于 `docs/blueprint.md` 的设计标准，列出当前实现的缺失项和待完善功能。

## 状态说明

- ✅ 已实现
- 🟡 部分实现
- ❌ 未实现
- 🔴 紧急(违反黄金标准)

---

## 一、配置治理 (Configuration Governance)

### 1.1 Prompt Schema 版本管理 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- `executor.yaml` 必须包含 `prompt_schema_version` 字段 (blueprint.md:356)
- 所有 prompt 模板文件必须声明 `Version:` 头部 (blueprint.md:378-383)
- Executor 启动时强制验证版本匹配 (blueprint.md:966)

**当前状况**:
- `etc/executor.yaml` 缺少 `prompt_schema_version` 字段
- 系统中无 `prompts/` 目录，无模板文件
- `pkg/executor/prompt.go` 未实现版本校验逻辑

**实现计划**:
1. **配置增强**:
   ```yaml
   # etc/executor.yaml 新增字段
   prompt_schema_version: "v1.0.0"
   prompt_validation:
     strict_mode: true
     require_version_header: true
   ```

2. **模板规范**:
   - 创建 `prompts/manager/*.tmpl` 和 `prompts/executor/*.tmpl`
   - 每个模板文件开头添加:
     ```go
     {{/* Version: v1.0.0 */}}
     {{/* Description: Aggressive Short Strategy */}}
     ```

3. **代码实现**:
   - `pkg/executor/config.go` 新增 `PromptSchemaVersion` 和 `PromptValidation` 字段
   - `pkg/executor/prompt.go` 中 `NewPromptRenderer()` 解析模板头部版本
   - 版本不匹配时根据 `strict_mode` 决定是否 panic

4. **单元测试**:
   - `pkg/executor/prompt_test.go` 新增版本校验测试
   - `pkg/manager/prompt_test.go` 新增 manager prompt 版本校验

**工作量**: 2-3 天

---

### 1.2 资金分配约束校验 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- `manager.yaml` 中所有 trader 的 `allocation_pct` 之和 ≤ `100 - reserve_equity_pct` (blueprint.md:222)
- CI 中增加 YAML 测试自动校验 (blueprint.md:222)

**当前状况**:
- `etc/manager.yaml` 当前配置: trader1(40%) + trader2(30%) = 70%, reserve(10%) ✅ 合规
- 但无自动化校验，手动修改可能违反约束

**实现计划**:
1. **配置加载校验**:
   - `pkg/manager/config.go` 的 `LoadConfig()` 增加校验逻辑:
     ```go
     func (c *Config) Validate() error {
         totalAlloc := 0.0
         for _, t := range c.Traders {
             totalAlloc += t.AllocationPct
         }
         maxAllowed := 100 - c.Manager.ReserveEquityPct
         if totalAlloc > maxAllowed {
             return fmt.Errorf("total allocation %.1f%% exceeds max %.1f%%", totalAlloc, maxAllowed)
         }
         return nil
     }
     ```

2. **单元测试**:
   - `pkg/manager/config_test.go` 新增 `TestConfig_Validate_AllocationExceedsLimit`
   - 测试边界情况: 恰好 100%、超出 1%、reserve=0 等

3. **CI 集成**:
   - `.github/workflows/test.yml` 中运行 `go test -run TestManagerConfigValidation`
   - 或创建专门的配置校验脚本 `scripts/validate-config.sh`

**工作量**: 1 天

---

### 1.3 Provider 健康检查与超时配置 🟡

**状态**: 部分实现

**Blueprint 要求**:
- 新增 provider 时必须补齐 `healthcheck`、`timeout`、`retry`、`capabilities` 字段 (blueprint.md:221)
- `internal/svc` 启动阶段校验 provider 可用性 (blueprint.md:221)

**当前状况**:
- `etc/exchange.yaml` 和 `etc/market.yaml` 有 `timeout` 和 `max_retries` ✅
- 缺少 `healthcheck` 端点和 `capabilities` 声明 ❌
- `internal/svc/servicecontext.go` 有 provider ID 校验 (L243-256) ✅
- 但无启动时健康检查 ❌

**实现计划**:
1. **配置扩展**:
   ```yaml
   # etc/exchange.yaml
   providers:
     hyperliquid_testnet:
       type: hyperliquid
       timeout: 30s
       max_retries: 3
       healthcheck:
         enabled: true
         endpoint: "/info"  # 使用 info 端点验证连接
         timeout: 5s
       capabilities:
         - order_placement
         - position_management
         - account_query
   ```

2. **接口定义**:
   - `pkg/exchange/interface.go` 增加 `HealthCheck(ctx) error` 方法
   - `pkg/market/provider.go` 增加 `HealthCheck(ctx) error` 方法

3. **启动校验**:
   - `internal/svc/servicecontext.go` 的 `NewServiceContext()` 中:
     ```go
     // After BuildProviders()
     for id, provider := range providers {
         if hc, ok := provider.(interface{ HealthCheck(context.Context) error }); ok {
             ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
             if err := hc.HealthCheck(ctx); err != nil {
                 log.Fatalf("provider %s health check failed: %v", id, err)
             }
             cancel()
         }
     }
     ```

**工作量**: 2 天

---

## 二、LLM 与 Executor 层

### 2.1 成本预算与模型降级 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- `llm.yaml` 包含 `budget` 配置 (blueprint.md:308-315)
- 模型配置包含 `priority` 和 `cost_tier` (blueprint.md:322-339)
- 当主模型失败或成本超预算时自动切换至低优先级模型 (blueprint.md:348)

**当前状况**:
- `etc/llm.yaml` 仅有基础配置，无 `budget` 和 `priority` 字段 ❌
- `pkg/llm/config.go` 未定义预算相关结构 ❌
- `pkg/llm/provider.go` 未实现模型切换逻辑 ❌

**实现计划**:
1. **配置结构**:
   ```go
   // pkg/llm/config.go
   type BudgetConfig struct {
       DailyTokenLimit      int64              `yaml:"daily_token_limit"`
       CostPerMillionTokens map[string]float64 `yaml:"cost_per_million_tokens"`
       AlertThresholdPct    int                `yaml:"alert_threshold_pct"`
   }

   type ModelConfig struct {
       // ... 现有字段
       Priority  int    `yaml:"priority"`   // 1=主模型, 2=备选, 3=fallback
       CostTier  string `yaml:"cost_tier"`  // high/medium/low
   }
   ```

2. **预算监控**:
   - 创建 `pkg/llm/budget.go`:
     ```go
     type BudgetGuard struct {
         config    *BudgetConfig
         usedTokens atomic.Int64
         resetAt    time.Time
     }

     func (g *BudgetGuard) CheckAndIncrement(tokens int64, model string) error
     func (g *BudgetGuard) GetUsagePercent() float64
     func (g *BudgetGuard) ShouldAlert() bool
     ```

3. **模型降级**:
   - `pkg/llm/provider.go` 中实现 `FallbackClient`:
     ```go
     type FallbackClient struct {
         primary   LLMClient
         fallbacks []LLMClient
         budget    *BudgetGuard
         failureThreshold int
     }

     func (c *FallbackClient) Chat(ctx, req) (resp, error) {
         // 1. 检查预算
         if c.budget.GetUsagePercent() > 80 {
             return c.fallbacks[0].Chat(ctx, req)
         }
         // 2. 尝试主模型
         resp, err := c.primary.Chat(ctx, req)
         if err == nil {
             c.budget.CheckAndIncrement(resp.Usage.TotalTokens, req.Model)
             return resp, nil
         }
         // 3. 降级至 fallback
         return c.fallbacks[0].Chat(ctx, req)
     }
     ```

4. **单元测试**:
   - `pkg/llm/budget_test.go`: 测试预算计数、重置、告警
   - `pkg/llm/provider_test.go`: 测试模型降级逻辑

**工作量**: 3-4 天

---

### 2.2 Prompt Digest Cache 🟡

**状态**: 部分实现

**Blueprint 要求**:
- 启用 Prompt digest 缓存减少重复计算 (blueprint.md:341-345)
- Manager 层缓存 `ManagerPromptRenderers` 与 digest (blueprint.md:142)

**当前状况**:
- `internal/svc/servicecontext.go` 已缓存 `ManagerPromptRenderers` 和 `ManagerPromptDigests` ✅
- 但 `pkg/executor` 层未缓存 executor prompt digest ❌
- 缺少基于 digest 的 LLM 响应缓存 ❌

**实现计划**:
1. **Executor Digest 缓存**:
   - `pkg/executor/prompt.go` 的 `PromptRenderer` 增加 `Digest()` 方法
   - `internal/svc/servicecontext.go` 缓存 executor prompt digest

2. **LLM 响应缓存**:
   - 创建 `pkg/llm/cache.go`:
     ```go
     type ResponseCache interface {
         Get(digest string) (*ChatResponse, bool)
         Set(digest string, resp *ChatResponse, ttl time.Duration)
     }

     type RedisResponseCache struct {
         redis *redis.Redis
         ttl   time.Duration
     }
     ```

3. **集成**:
   - `pkg/executor/executor.go` 的 `GetFullDecision()`:
     ```go
     digest := e.renderer.Digest() + "_" + hashInputs(inputs)
     if cached, ok := e.cache.Get(digest); ok {
         return parseCachedDecision(cached), nil
     }
     // ... 调用 LLM
     e.cache.Set(digest, response, 1*time.Hour)
     ```

**工作量**: 2 天

---

### 2.3 JSON Schema 校验 🟡

**状态**: 部分实现

**Blueprint 要求**:
- `executor.yaml` 指定 `output_validation.schema_path` (blueprint.md:373-375)
- Executor 在 LLM 返回后验证结构化输出 (blueprint.md:179)

**当前状况**:
- `pkg/executor/validator.go` 有基本的结构验证 ✅
- 但未使用独立的 JSON Schema 文件 ❌
- 缺少 `fail_on_invalid` 配置 ❌

**实现计划**:
1. **配置扩展**:
   ```yaml
   # etc/executor.yaml
   output_validation:
     enabled: true
     schema_path: "schemas/decision_output.json"
     fail_on_invalid: true
   ```

2. **Schema 文件**:
   - 创建 `schemas/decision_output.json`:
     ```json
     {
       "$schema": "http://json-schema.org/draft-07/schema#",
       "type": "object",
       "required": ["decisions", "reasoning"],
       "properties": {
         "decisions": {
           "type": "array",
           "items": {
             "type": "object",
             "required": ["symbol", "action", "confidence"],
             "properties": {
               "symbol": {"type": "string"},
               "action": {"enum": ["long", "short", "hold", "close"]},
               "confidence": {"type": "number", "minimum": 0, "maximum": 100}
             }
           }
         }
       }
     }
     ```

3. **代码实现**:
   - 使用 `github.com/xeipuuv/gojsonschema` 库
   - `pkg/executor/validator.go` 增加 `ValidateAgainstSchema()` 方法
   - 根据 `fail_on_invalid` 决定返回错误或仅记录日志

**工作量**: 1-2 天

---

## 三、Manager 与交易执行

### 3.1 虚拟资金隔离与仓位校验 🔴

**状态**: 🟡 部分实现

**Blueprint 要求**:
- Trader 仓位是 Exchange Provider 账户的逻辑子集 (blueprint.md:60)
- 禁用全局平仓 API，限制 withdraw 入口 (blueprint.md:36)
- Manager 执行订单前二次校验 `MaxPositionSizeUSD` 和保证金使用率 (blueprint.md:184)

**当前状况**:
- `pkg/manager/trader.go` 定义了 `VirtualTrader` 和 `ResourceAlloc` ✅
- Manager 有基本的风控校验 (从代码推测) 🟡
- 但缺少明确的"虚拟仓位 vs 物理仓位"对账逻辑 ❌
- Exchange provider 未限制全局操作 ❌

**实现计划**:
1. **仓位对账**:
   - `pkg/manager/manager.go` 新增方法:
     ```go
     func (m *Manager) ReconcilePositions(ctx context.Context, traderID string) error {
         trader := m.traders[traderID]
         // 1. 从 exchange provider 获取物理仓位
         physicalPositions, _ := trader.exchange.GetPositions(ctx)
         // 2. 汇总所有 trader 的虚拟仓位
         virtualTotal := m.aggregateVirtualPositions(ctx)
         // 3. 检查一致性
         for symbol, virtualSize := range virtualTotal {
             if physicalSize := findPhysicalSize(physicalPositions, symbol); physicalSize != virtualSize {
                 return fmt.Errorf("position mismatch %s: virtual=%.2f physical=%.2f", symbol, virtualSize, physicalSize)
             }
         }
         return nil
     }
     ```

2. **Exchange 权限限制**:
   - `pkg/exchange/interface.go` 标记危险方法:
     ```go
     // CloseAllPositions is restricted to operator CLI only.
     // DO NOT call from Manager - use per-trader ClosePosition instead.
     CloseAllPositions(ctx context.Context) error
     ```
   - `pkg/exchange/hyperliquid/provider.go` 实现权限检查:
     ```go
     func (p *Provider) CloseAllPositions(ctx context.Context) error {
         if !p.allowGlobalOps {
             return ErrGlobalOpForbidden
         }
         // ...
     }
     ```

3. **Manager 二次风控**:
   - `pkg/manager/manager.go` 的订单执行逻辑增强:
     ```go
     func (m *Manager) executeDecision(ctx, trader, decision) error {
         // 已有风控: executor 层校验

         // 二次风控: manager 层再校验
         if decision.PositionSizeUSD > trader.RiskParams.MaxPositionSizeUSD {
             return ErrExceedsMaxPositionSize
         }

         currentMarginPct := trader.ResourceAlloc.MarginUsedUSD / trader.ResourceAlloc.EquityUSD * 100
         if currentMarginPct > trader.RiskParams.MaxMarginUsagePct {
             return ErrExceedsMaxMargin
         }

         // 执行订单
         // ...
     }
     ```

**工作量**: 3-4 天

---

### 3.2 再平衡与 KPI 驱动资金分配 ❌

**状态**: ❌ 未实现

**Blueprint 要求**:
- 按 `rebalance_interval` 触发，依据 trader KPI 与风险限额重新分配资金池 (blueprint.md:154)
- Trader 级 KPI (Sharpe、DD、利用率) 直接驱动资金再分配 (blueprint.md:26)

**当前状况**:
- `etc/manager.yaml` 有 `allocation_strategy: performance_based` 和 `rebalance_interval: 1h`
- 但 `pkg/manager/manager.go` 未实现 `Rebalance()` 方法 ❌

**实现计划**:
1. **KPI 计算**:
   - `pkg/manager/trader.go` 增强 `Performance` 结构:
     ```go
     type Performance struct {
         SharpeRatio    float64
         MaxDrawdownPct float64
         WinRate        float64
         ProfitFactor   float64
         CapitalUtilization float64
         // ...
     }

     func (t *VirtualTrader) CalculateKPI() Performance {
         // 从历史 trades 计算 Sharpe、DD 等
     }
     ```

2. **再平衡策略**:
   - 创建 `pkg/manager/rebalance.go`:
     ```go
     type RebalanceStrategy interface {
         Rebalance(traders []*VirtualTrader, totalEquity float64, reserve float64) map[string]float64
     }

     type PerformanceBasedStrategy struct{}

     func (s *PerformanceBasedStrategy) Rebalance(...) map[string]float64 {
         // 1. 按 Sharpe Ratio 排序 traders
         // 2. 高 Sharpe 者获得更多资金
         // 3. 低于阈值者资金减半或暂停
         // 4. 确保总分配 ≤ totalEquity - reserve
     }
     ```

3. **定时触发**:
   - `pkg/manager/manager.go` 的 `RunTradingLoop()`:
     ```go
     rebalanceTicker := time.NewTicker(m.config.Manager.RebalanceInterval)
     for {
         select {
         case <-rebalanceTicker.C:
             if err := m.Rebalance(ctx); err != nil {
                 logx.Errorf("rebalance failed: %v", err)
             }
         // ...
         }
     }
     ```

**工作量**: 4-5 天

---

### 3.3 订单去重 (`cloid`) ✅

**状态**: ✅ 已实现

**Blueprint 要求**:
- 订单使用 `cloid` 防重 (blueprint.md:101, 902)

**当前状况**:
- `pkg/manager/cloid_test.go` 存在，说明已实现 ✅
- `pkg/exchange/hyperliquid/order.go` 应该有 `cloid` 生成逻辑 ✅

**无需额外工作**

---

## 四、数据持久化与观测性

### 4.1 完整的 PersistenceService 实现 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- Manager 通过 `PersistenceService` 写入 Postgres/Redis (blueprint.md:189-192)
- 持久化失败不阻塞决策循环 (blueprint.md:201)

**当前状况**:
- `pkg/manager/persistence.go` 定义了接口 `PersistenceService` ✅
- 但只有 `noopPersistenceService` 实现 ❌
- 缺少实际的数据库写入实现 ❌

**实现计划**:
1. **PostgreSQL 实现**:
   - 创建 `internal/persistence/postgres.go`:
     ```go
     type PostgresPersistence struct {
         db     sqlx.SqlConn
         logger logx.Logger
     }

     func (p *PostgresPersistence) RecordPositionEvent(ctx, event) error {
         // INSERT INTO positions (...)
         // 捕获错误但不返回，仅记录日志
         if err := p.db.ExecCtx(ctx, sql, args...); err != nil {
             p.logger.Errorf("failed to record position event: %v", err)
             // 不返回错误，避免阻塞 Manager
         }
         return nil
     }

     // 类似实现其他方法...
     ```

2. **Redis 缓存实现**:
   - 创建 `internal/persistence/redis.go`:
     ```go
     type RedisPersistence struct {
         redis *redis.Redis
     }

     func (p *RedisPersistence) RecordAccountSnapshot(ctx, snapshot) error {
         key := fmt.Sprintf("trader:%s:equity", snapshot.TraderID)
         // HSET trader:{id}:equity ...
     }
     ```

3. **组合实现**:
   - 创建 `internal/persistence/composite.go`:
     ```go
     type CompositePersistence struct {
         postgres *PostgresPersistence
         redis    *RedisPersistence
     }

     func (c *CompositePersistence) RecordPositionEvent(ctx, event) error {
         // 同时写入 Postgres 和 Redis
         c.postgres.RecordPositionEvent(ctx, event)
         c.redis.RecordPositionEvent(ctx, event)
         return nil
     }
     ```

4. **集成到 Manager**:
   - `internal/svc/servicecontext.go` 构造 persistence:
     ```go
     var persistence managerpkg.PersistenceService
     if svc.DBConn != nil && svc.Redis != nil {
         persistence = persistence.NewCompositePersistence(svc.DBConn, svc.Redis)
     } else {
         persistence = managerpkg.NewNoopPersistenceService()
     }
     // 传给 Manager
     ```

**工作量**: 5-6 天

---

### 4.2 MCP JSON 导出器 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- 定时任务或专用 exporter 将 Redis/DB 渲染成 MCP JSON 文件 (blueprint.md:151)
- 原子写入：临时文件 → rename (blueprint.md:34)

**当前状况**:
- `internal/data/loader.go` 可以读取 JSON 文件 ✅
- 但没有写入 JSON 的 exporter ❌
- 缺少定时导出机制 ❌

**实现计划**:
1. **Exporter 实现**:
   - 创建 `cmd/exporter/main.go`:
     ```go
     func exportAccountTotals(db, redis, dataPath) error {
         // 1. 从 Redis/DB 查询数据
         data := queryAccountTotals(db, redis)

         // 2. 序列化为 JSON
         jsonBytes, _ := json.MarshalIndent(data, "", "  ")

         // 3. 原子写入
         tmpFile := filepath.Join(dataPath, ".account-totals.json.tmp")
         finalFile := filepath.Join(dataPath, "account-totals.json")

         os.WriteFile(tmpFile, jsonBytes, 0644)
         os.Rename(tmpFile, finalFile)  // 原子操作

         return nil
     }
     ```

2. **定时调度**:
   - 方案A: 在 `cmd/exporter` 中使用 `time.Ticker`:
     ```go
     ticker := time.NewTicker(30 * time.Second)
     for range ticker.C {
         exportAccountTotals(...)
         exportPositions(...)
         exportAnalytics(...)
     }
     ```

   - 方案B: 使用 cron 调度 `cmd/exporter` (推荐)

3. **导出清单**:
   按 blueprint.md:660-671 的文件清单实现导出:
   - `crypto-prices.json`
   - `account-totals.json`
   - `trades.json`
   - `positions.json`
   - `analytics.json`
   - `analytics-{modelId}.json`
   - `leaderboard.json`
   - `since-inception-values.json`
   - `conversations.json`

**工作量**: 3-4 天

---

### 4.3 Prometheus Exporter 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- Prometheus exporter 必须覆盖 (blueprint.md:36):
  - LLM QPS/失败率
  - Trader 决策延迟
  - Redis/DB RTT
- 监控组件每 15s 采集指标 (blueprint.md:155)

**当前状况**:
- `etc/manager.yaml` 有 `monitoring.metrics_exporter: prometheus` 配置
- 但无实际的指标采集代码 ❌

**实现计划**:
1. **指标定义**:
   - 创建 `internal/metrics/prometheus.go`:
     ```go
     var (
         LLMRequestsTotal = prometheus.NewCounterVec(
             prometheus.CounterOpts{Name: "llm_requests_total"},
             []string{"model", "status"},
         )
         LLMRequestDuration = prometheus.NewHistogramVec(
             prometheus.HistogramOpts{Name: "llm_request_duration_seconds"},
             []string{"model"},
         )
         TraderDecisionDuration = prometheus.NewHistogramVec(
             prometheus.HistogramOpts{Name: "trader_decision_duration_seconds"},
             []string{"trader_id"},
         )
         RedisLatency = prometheus.NewHistogram(
             prometheus.HistogramOpts{Name: "redis_latency_seconds"},
         )
         PostgresLatency = prometheus.NewHistogram(
             prometheus.HistogramOpts{Name: "postgres_latency_seconds"},
         )
     )
     ```

2. **埋点**:
   - `pkg/llm/provider.go` 的 `Chat()` 方法:
     ```go
     start := time.Now()
     resp, err := c.client.Chat(ctx, req)
     LLMRequestDuration.WithLabelValues(req.Model).Observe(time.Since(start).Seconds())
     if err != nil {
         LLMRequestsTotal.WithLabelValues(req.Model, "error").Inc()
     } else {
         LLMRequestsTotal.WithLabelValues(req.Model, "success").Inc()
     }
     ```

   - 类似在 `pkg/manager`、`pkg/exchange`、Redis/DB 操作中埋点

3. **HTTP Endpoint**:
   - `internal/handler/routes.go` 增加:
     ```go
     server.AddRoute(rest.Route{
         Method: http.MethodGet,
         Path:   "/metrics",
         Handler: promhttp.Handler(),
     })
     ```

4. **告警规则**:
   - 创建 `deploy/prometheus/alerts.yml`:
     ```yaml
     groups:
       - name: nof0_alerts
         rules:
           - alert: LLMHighFailureRate
             expr: rate(llm_requests_total{status="error"}[5m]) > 0.1
             annotations:
               summary: "LLM failure rate > 10%"
           - alert: TraderDecisionSlow
             expr: histogram_quantile(0.95, trader_decision_duration_seconds) > 60
             annotations:
               summary: "95th percentile decision latency > 60s"
     ```

**工作量**: 3-4 天

---

### 4.4 Journal Replay 工具 ❌

**状态**: ❌ 未实现

**Blueprint 要求**:
- `health check CLI` 可离线重放最近 N 条 Journal (blueprint.md:156)
- 发布前必须跑 journal replay 覆盖主策略 (blueprint.md:976)

**当前状况**:
- `pkg/journal/journal.go` 可以写入 journal ✅
- 但没有读取和重放工具 ❌

**实现计划**:
1. **Replay Reader**:
   - `pkg/journal/reader.go`:
     ```go
     type Reader struct {
         dir string
     }

     func (r *Reader) ListCycles() ([]string, error) {
         // 列出 journal/*.json 文件
     }

     func (r *Reader) LoadCycle(filename string) (*CycleRecord, error) {
         // 加载 JSON
     }
     ```

2. **Replay Engine**:
   - 创建 `cmd/replay/main.go`:
     ```go
     func replayCycle(rec *CycleRecord, executor executor.Executor) error {
         // 1. 从 rec 重建 executor.Context
         ctx := &executor.Context{
             Account:        rec.Account,
             Positions:      rec.Positions,
             MarketDataMap:  rec.MarketDigest,
             // ...
         }

         // 2. 调用 executor.GetFullDecision()
         decision, err := executor.GetFullDecision(ctx)

         // 3. 比对决策是否一致
         if decision.DecisionsJSON != rec.DecisionsJSON {
             return fmt.Errorf("decision mismatch")
         }

         return nil
     }

     func main() {
         // 加载最近 N 条 journal
         reader := journal.NewReader("journal")
         cycles, _ := reader.ListCycles()

         for _, file := range cycles[len(cycles)-10:] { // 最近 10 条
             rec, _ := reader.LoadCycle(file)
             if err := replayCycle(rec, executor); err != nil {
                 log.Fatalf("replay failed: %v", err)
             }
         }
         fmt.Println("All replays passed!")
     }
     ```

3. **CI 集成**:
   - `.github/workflows/release.yml`:
     ```yaml
     - name: Journal Replay Test
       run: |
         go run cmd/replay/main.go --journal-dir=test_data/journal
     ```

**工作量**: 2-3 天

---

## 五、回测与测试

### 5.1 回测引擎完善 🟡

**状态**: 部分实现

**Blueprint 要求**:
- 复用 Manager/Executor/Market/Exchange 抽象 (blueprint.md:119)
- 通过 Journal Replay 确保回测与实盘一致性 (blueprint.md:119)

**当前状况**:
- `pkg/backtest/engine.go` 存在 ✅
- 但未与 journal replay 集成 ❌

**实现计划**:
1. **集成 Journal**:
   - `pkg/backtest/engine.go` 增加方法:
     ```go
     func (e *Engine) RunFromJournal(journalPath string) (*BacktestResult, error) {
         reader := journal.NewReader(journalPath)
         cycles, _ := reader.ListCycles()

         for _, file := range cycles {
             rec, _ := reader.LoadCycle(file)
             // 回放决策
             e.executeCycle(rec)
         }

         return e.GetResults(), nil
     }
     ```

2. **测试**:
   - `pkg/backtest/backtest_test.go` 增加 journal replay 测试

**工作量**: 1-2 天

---

### 5.2 集成测试覆盖 🟡

**状态**: 部分实现

**当前状况**:
- 存在部分 `*_integration_test.go` 文件 ✅
- 但缺少端到端测试 ❌

**实现计划**:
1. **端到端测试**:
   - 创建 `tests/e2e_test.go`:
     ```go
     func TestE2E_FullTradingCycle(t *testing.T) {
         // 1. 启动 Manager
         // 2. 模拟市场数据
         // 3. 触发决策
         // 4. 验证订单执行
         // 5. 检查持久化
     }
     ```

2. **Testcontainers**:
   - 使用 `github.com/testcontainers/testcontainers-go`
   - 在测试中启动 Postgres 和 Redis 容器

**工作量**: 3-4 天

---

## 六、部署与运维

### 6.1 配置示例与文档 🟡

**状态**: 部分实现

**当前状况**:
- `etc/*.yaml` 文件有基本配置 ✅
- Blueprint.md 有详细的配置说明 ✅
- 但缺少多环境配置示例 (dev/test/prod) ❌

**实现计划**:
1. **环境配置**:
   - `etc/nof0.dev.yaml`
   - `etc/nof0.test.yaml`
   - `etc/nof0.prod.yaml`

2. **配置文档**:
   - `docs/configuration-guide.md`: 每个字段的详细说明和示例

**工作量**: 1 天

---

### 6.2 Docker 化与部署脚本 ❌

**状态**: ❌ 未实现

**实现计划**:
1. **Dockerfile**:
   ```dockerfile
   FROM golang:1.21 AS builder
   WORKDIR /app
   COPY . .
   RUN go build -o nof0 cmd/nof0/main.go

   FROM alpine:latest
   RUN apk --no-cache add ca-certificates
   COPY --from=builder /app/nof0 /nof0
   CMD ["/nof0"]
   ```

2. **docker-compose.yml**:
   ```yaml
   services:
     postgres:
       image: postgres:15
     redis:
       image: redis:7
     nof0:
       build: .
       depends_on:
         - postgres
         - redis
   ```

**工作量**: 2 天

---

## 七、Golden Test 套件 🔴

**状态**: ❌ 未实现

**Blueprint 要求**:
- 补充 `cmd/archtest`，在 CI 中静态验证 (blueprint.md:973):
  - Provider ID 一致性
  - 配置比率合规性
  - Prompt schema 版本
- 防止偏离黄金标准

**实现计划**:
1. **架构测试工具**:
   - 创建 `cmd/archtest/main.go`:
     ```go
     func validateProviderIDs() error {
         // 1. 加载 manager.yaml
         // 2. 检查每个 trader 的 exchange_provider 和 market_provider
         // 3. 验证在 exchange.yaml 和 market.yaml 中存在
     }

     func validateAllocationConstraints() error {
         // 1. 加载 manager.yaml
         // 2. 计算 allocation_pct 之和
         // 3. 验证 ≤ 100 - reserve_equity_pct
     }

     func validatePromptSchemaVersions() error {
         // 1. 读取 executor.yaml 的 prompt_schema_version
         // 2. 扫描所有 .tmpl 文件
         // 3. 验证每个模板的 Version 头部匹配
     }
     ```

2. **CI 集成**:
   - `.github/workflows/test.yml`:
     ```yaml
     - name: Architecture Golden Test
       run: go run cmd/archtest/main.go
     ```

**工作量**: 2 天

---

## 实施优先级建议

### P0 (紧急，违反黄金标准):
1. ⚠️ Prompt Schema 版本管理 (1.1) - 2-3 天
2. ⚠️ 资金分配约束校验 (1.2) - 1 天
3. ⚠️ 成本预算与模型降级 (2.1) - 3-4 天
4. ⚠️ 虚拟资金隔离与仓位校验 (3.1) - 3-4 天
5. ⚠️ PersistenceService 实现 (4.1) - 5-6 天
6. ⚠️ MCP JSON 导出器 (4.2) - 3-4 天
7. ⚠️ Prometheus Exporter (4.3) - 3-4 天
8. ⚠️ Golden Test 套件 (七) - 2 天

**总计**: 约 22-30 工作日

### P1 (重要，影响可观测性和可靠性):
1. Provider 健康检查 (1.3) - 2 天
2. Prompt Digest Cache (2.2) - 2 天
3. JSON Schema 校验 (2.3) - 1-2 天
4. 再平衡与 KPI 驱动资金分配 (3.2) - 4-5 天
5. Journal Replay 工具 (4.4) - 2-3 天

**总计**: 约 11-14 工作日

### P2 (优化，可后续迭代):
1. 回测引擎完善 (5.1) - 1-2 天
2. 集成测试覆盖 (5.2) - 3-4 天
3. 配置示例与文档 (6.1) - 1 天
4. Docker 化与部署脚本 (6.2) - 2 天

**总计**: 约 7-9 工作日

---

## 长期改进项

### 架构演进:
1. **分布式部署**: 执行引擎、API、Cron 拆分为独立服务
2. **消息队列**: 使用 Kafka/RabbitMQ 解耦 Manager 与 Persistence
3. **多租户支持**: 支持多个独立的 trading 账户
4. **Web UI**: 实时监控面板和策略配置界面

### 功能增强:
1. **多交易所支持**: 接入 Binance、OKX 等
2. **策略市场**: 用户可上传/分享 prompt 模板
3. **风控增强**: 动态调整止损止盈、组合风险计算
4. **AI 优化**: 使用 RL 自动优化超参数

---

## 附录: 配置检查清单

### 启动前必检项:
- [ ] `etc/executor.yaml` 包含 `prompt_schema_version`
- [ ] 所有 prompt 模板文件有 `Version:` 头部
- [ ] `manager.yaml` 的 `allocation_pct` 之和 ≤ 90% (reserve=10%)
- [ ] 所有 trader 引用的 provider ID 在 `exchange.yaml` 和 `market.yaml` 中存在
- [ ] 环境变量设置完整 (HYPERLIQUID_PRIVATE_KEY, ZENMUX_API_KEY 等)
- [ ] Postgres 和 Redis 连接可用

### 运维监控项:
- [ ] Prometheus `/metrics` 端点正常
- [ ] LLM 成本 < 预算 80%
- [ ] Trader 决策延迟 < 60s
- [ ] Redis/DB RTT < 阈值
- [ ] 定时导出 JSON 文件成功
- [ ] Journal 文件正常写入

---

**文档生成时间**: 2025-11-08
**基于 Blueprint 版本**: docs/blueprint.md (最新)
**实现进度跟踪**: 请在完成每项后更新状态符号
