# 系统概述

基于AI工作流的Crypto交易系统

## 设计理念

采用执行引擎和API分离的架构：

* 执行引擎：位于pkg/
* API：位于internal/

基于抽象的trader，一系列配置信息和 trader绑定:

* 资源实体由trader自由配置（参考`etc/manager.yaml`），如行情provider、交易所provider
* system prompt 和 user prompt
* 风控参数
* 决策频率
* 使用哪家大模型
* 起始本金

通过抽象的 trader，可自由实现：

* 行情provider和交易所provider分离，自由搭配
* 纸面交易（paper trading）
* 回测（backtesting）
* 提示词变体
* 指定大模型
* 虚拟本金账户（一个交易所provider可同时运行多个trader）

为了让虚拟本金账户正常运作，做出如下约定:

* 默认禁用一键全部平仓功能，只能针对trader拥有的虚拟仓位进行平仓操作

## 执行引擎

### 模块

pkg/manager:

* 管理并初始化trader的状态参数（净值余额、当前仓位、指标参数、历史仓位等）
* 管理行情
* 定时调用executor模块进行决策
* 基于决策结果进行操作

pkg/executor:

* 基于上下文信息，调用LLM，给出交易决策
* 执行前风控

pkg/exchange:

* 获取账户级别的资产、仓位情况
* 获取活跃订单
* 开平仓操作
* 提供paper trading交易所（pkg/exchange/sim）

pkg/llm:

* 大语言模型交互

pkg/market:

* K线指标
* OI、资金费率、成交量、标记价格、市场价格
* 计算出来的指标：EMA、MACD、RSI、ATR

pkg/backtest:

* 回测

pkg/journal:

* 独立的日志器，记录交易周期

### 流程

主流程：

1. 加载配置、初始化数据库连接
2. 从数据库加载状态信息：trader 净值余额、当前仓位、指标参数、历史仓位等
3. 获取历史行情，更新状态信息，如trader的净值余额，当前仓位盈亏
4. 定时获取并执行最新交易决策

数据表存储：

1. trader: 指标参数、历史仓位、历史委托和成交情况等
2. exchange provider: 历史仓位、历史委托和成交
3. market: 币对基础信息、历史行情信息、历史指标信息
4. decision: 决策上下文、LLM对话、决策结果、风控信息

缓存存储:

1. trader: 净值余额、当前仓位
2. exchange provider: 净值余额、当前仓位
3. market：当前行情信息，币对基础信息

### 配置

系统采用分层配置架构，主配置文件为`etc/nof0.yaml`，引用其他模块配置：

#### 1. 主配置 (`etc/nof0.yaml`)

基础服务配置：

```yaml
Name: nof0
Env: test | dev | prod         # 运行环境
Host: 0.0.0.0
Port: 8888
DataPath: ../mcp/data          # MCP数据文件路径
```

数据库配置（通过环境变量注入）：

```yaml
Postgres:
  DataSource: "${Postgres__DataSource}"  # postgres://user:pass@host:port/dbname
  MaxOpen: 25
  MaxIdle: 10
  MaxLifetime: 5m
```

缓存配置（Redis，通过环境变量注入）：

```yaml
Cache:
  - Host: "${Cache__0__Host}"
    Type: node
    Pass: "${Cache__0__Pass}"
    Tls: ${Cache__0__Tls}
```

TTL配置（数据缓存时间）：

```yaml
TTL:
  Short: 10      # 快速变化数据（如价格）
  Medium: 60     # 列表数据（如交易）
  Long: 300      # 大型聚合数据
```

日志配置：

```yaml
Logging:
  SlowThreshold:
    SQL: 10000      # SQL慢查询阈值（毫秒）
    Redis: 5000     # Redis慢操作阈值（毫秒）
  VerboseSQL: false  # 是否打印SQL详情
  VerboseLLM: false  # 是否打印LLM完整提示词
```

模块配置文件引用：

```yaml
LLM:
  File: llm.yaml
Executor:
  File: executor.yaml
Manager:
  File: manager.yaml
Exchange:
  File: exchange.yaml
Market:
  File: market.yaml
```

#### 2. LLM配置 (`etc/llm.yaml`)

大语言模型配置：

```yaml
base_url: "https://zenmux.ai/api/v1"  # LLM服务端点
api_key: "${ZENMUX_API_KEY}"          # API密钥（环境变量）
default_model: "gpt-5"                # 默认模型
timeout: "60s"                        # 请求超时
max_retries: 3                        # 最大重试次数
log_level: "info"

models:
  gpt-5:
    provider: "openai"
    model_name: "openai/gpt-5"
    temperature: 0.7
    max_completion_tokens: 4096
  claude-sonnet-4.5:
    provider: "anthropic"
    model_name: "anthropic/claude-sonnet-4.5"
    temperature: 0.7
    max_completion_tokens: 4096
  deepseek-chat:
    provider: "deepseek"
    model_name: "deepseek/deepseek-chat-v3.1"
    temperature: 0.6
    max_completion_tokens: 4096
```

#### 3. 执行器配置 (`etc/executor.yaml`)

默认风控参数：

```yaml
major_coin_leverage: 20      # 主流币杠杆（BTC/ETH）
altcoin_leverage: 10         # 山寨币杠杆
min_confidence: 75           # 最低置信度要求
min_risk_reward: 3.0         # 最低风险回报比
max_positions: 4             # 最大持仓数
decision_interval: 3m        # 决策间隔
decision_timeout: 60s        # 决策超时时间
max_concurrent_decisions: 1  # 最大并发决策数
```

#### 4. 交易所配置 (`etc/exchange.yaml`)

交易所Provider配置：

```yaml
default: hyperliquid_testnet

providers:
  hyperliquid_testnet:
    type: hyperliquid
    private_key: ${HYPERLIQUID_PRIVATE_KEY}    # 私钥（环境变量）
    main_address: ${HYPERLIQUID_MAIN_ADDRESS}  # 主账户地址
    testnet: true                               # 测试网模式
    timeout: 30s
    vault_address: ${HYPERLIQUID_VAULT_ADDRESS} # 可选：金库地址

  paper_trading:
    type: sim  # 纸面交易模拟器
```

#### 5. 行情配置 (`etc/market.yaml`)

行情数据Provider配置：

```yaml
default: hyperliquid_testnet

providers:
  hyperliquid:
    type: hyperliquid
    testnet: false
    timeout: 8s
    http_timeout: 10s
    max_retries: 3

  hyperliquid_testnet:
    type: hyperliquid
    testnet: true
    timeout: 8s
    http_timeout: 10s
    max_retries: 3
```

#### 6. Manager配置 (`etc/manager.yaml`)

Manager全局配置：

```yaml
manager:
  total_equity_usd: 10000           # 总资金（美元）
  reserve_equity_pct: 10            # 保留金百分比
  allocation_strategy: performance_based  # 分配策略
  rebalance_interval: 1h            # 再平衡间隔
  state_storage_backend: file       # 状态存储后端
  state_storage_path: ../data/manager_state.json
```

Trader配置（每个trader是一个独立的虚拟交易账户）：

```yaml
traders:
  - id: trader_aggressive_short
    name: Aggressive Short
    exchange_provider: hyperliquid_testnet  # 交易所provider
    market_provider: hyperliquid_testnet    # 行情provider
    order_style: market_ioc                 # 订单类型
    market_ioc_slippage_bps: 75            # 滑点（基点）
    prompt_template: prompts/manager/aggressive_short.tmpl  # 系统提示词模板
    executor_prompt_template: prompts/executor/default_prompt.tmpl  # 执行器提示词
    model: deepseek-chat                    # 使用的LLM模型
    decision_interval: 3m                   # 决策间隔
    allocation_pct: 40                      # 资金分配百分比
    auto_start: true                        # 自动启动
    risk_params:                            # 风控参数
      max_positions: 3                      # 最大持仓数
      max_position_size_usd: 500           # 单仓最大金额
      max_margin_usage_pct: 60             # 最大保证金使用率
      major_coin_leverage: 20              # 主流币杠杆
      altcoin_leverage: 10                 # 山寨币杠杆
      min_risk_reward_ratio: 3.0           # 最小风险回报比
      min_confidence: 75                    # 最小置信度
      stop_loss_enabled: true              # 启用止损
      take_profit_enabled: true            # 启用止盈
```

监控配置：

```yaml
monitoring:
  update_interval: 15s               # 更新间隔
  alert_webhook: ""                  # 告警webhook
  metrics_exporter: prometheus       # 指标导出器
```

配置特性：

- **分离架构**：执行引擎和API各自独立配置
- **环境变量注入**：敏感信息通过`${VAR_NAME}`注入
- **Provider抽象**：行情和交易所可自由组合
- **多Trader支持**：每个trader独立配置和运行
- **虚拟资金账户**：多个trader共享物理账户，各自管理虚拟资金

## API

### API路由

所有API路由均挂载在 `/api` 前缀下（参见 `internal/handler/routes.go:14-64`）：

#### 1. 账户汇总 - Account Totals

- **路由**: `GET /api/account-totals`
- **Handler**: `AccountTotalsHandler`
- **功能**: 获取所有trader的账户汇总信息
- **返回数据**:
  - `accountTotals[]`: 账户汇总数组
    - `id`: trader标识
    - `model_id`: 模型ID
    - `dollar_equity`: 美元净值
    - `realized_pnl`: 已实现盈亏
    - `total_unrealized_pnl`: 总未实现盈亏
    - `cum_pnl_pct`: 累计盈亏百分比
    - `sharpe_ratio`: 夏普比率
    - `positions{}`: 当前持仓映射
  - `serverTime`: 服务器时间戳

#### 2. 综合分析 - Analytics

- **路由**: `GET /api/analytics`
- **Handler**: `AnalyticsHandler`
- **功能**: 获取所有模型的综合分析数据
- **返回数据**:
  - `analytics[]`: 分析数据数组
    - `model_id`: 模型ID
    - `fee_pnl_moves_breakdown_table`: 手续费和盈亏分解表
    - `winners_losers_breakdown_table`: 盈亏交易分解表
    - `signals_breakdown_table`: 信号分解表（做多/做空/持有/平仓比例）
    - `longs_shorts_breakdown_table`: 多空交易分解表
    - `overall_trades_overview_table`: 交易总览表
    - `invocation_breakdown_table`: 调用统计表
  - `serverTime`: 服务器时间戳

#### 3. 单模型分析 - Model Analytics

- **路由**: `GET /api/analytics/:modelId`
- **Handler**: `ModelAnalyticsHandler`
- **功能**: 获取指定模型的详细分析数据
- **参数**: `modelId` - 模型/trader的ID
- **返回数据**: 与 Analytics 相同结构的单个模型数据

#### 4. 加密货币价格 - Crypto Prices

- **路由**: `GET /api/crypto-prices`
- **Handler**: `CryptoPricesHandler`
- **功能**: 获取当前加密货币价格快照
- **返回数据**:
  - `prices{}`: 价格映射（symbol → price对象）
    - `symbol`: 币对符号
    - `price`: 当前价格
    - `timestamp`: 价格时间戳
  - `serverTime`: 服务器时间戳

#### 5. 排行榜 - Leaderboard

- **路由**: `GET /api/leaderboard`
- **Handler**: `LeaderboardHandler`
- **功能**: 获取trader绩效排行榜
- **返回数据**:
  - `leaderboard[]`: 排行榜数组
    - `id`: trader ID
    - `num_trades`: 交易次数
    - `sharpe`: 夏普比率
    - `win_dollars`: 盈利金额
    - `lose_dollars`: 亏损金额
    - `return_pct`: 收益率百分比
    - `equity`: 当前净值
    - `num_wins`: 盈利次数
    - `num_losses`: 亏损次数

#### 6. 启动以来表现 - Since Inception

- **路由**: `GET /api/since-inception-values`
- **Handler**: `SinceInceptionHandler`
- **功能**: 获取从启动以来的绩效数据
- **返回数据**:
  - `sinceInceptionValues[]`: 启动以来数据数组
    - `id`: trader ID
    - `model_id`: 模型ID
    - `nav_since_inception`: 启动以来净值变化
    - `inception_date`: 启动日期
    - `num_invocations`: 调用次数
  - `serverTime`: 服务器时间戳

#### 7. 交易历史 - Trades

- **路由**: `GET /api/trades`
- **Handler**: `TradesHandler`
- **功能**: 获取历史交易记录
- **返回数据**:
  - `trades[]`: 交易数组
    - `id`: 交易ID
    - `model_id`: 模型ID
    - `symbol`: 交易币对
    - `side`: 方向（long/short）
    - `trade_type`: 交易类型
    - `quantity`: 数量
    - `leverage`: 杠杆
    - `confidence`: 置信度
    - `entry_price/entry_time`: 入场价格/时间
    - `exit_price/exit_time`: 出场价格/时间
    - `realized_gross_pnl`: 已实现总盈亏
    - `realized_net_pnl`: 已实现净盈亏
    - `total_commission_dollars`: 总手续费
  - `serverTime`: 服务器时间戳

#### 8. 持仓信息 - Positions

- **路由**: `GET /api/positions`
- **Handler**: `PositionsHandler`
- **功能**: 获取当前持仓信息
- **返回数据**:
  - `accountTotals[]`: 按模型分组的持仓
    - `model_id`: 模型ID
    - `positions{}`: 持仓映射（symbol → position对象）
      - `symbol`: 币对
      - `entry_price`: 入场价格
      - `current_price`: 当前价格
      - `quantity`: 数量
      - `leverage`: 杠杆
      - `margin`: 保证金
      - `unrealized_pnl`: 未实现盈亏
      - `liquidation_price`: 爆仓价格
  - `serverTime`: 服务器时间戳

#### 9. 对话历史 - Conversations

- **路由**: `GET /api/conversations`
- **Handler**: `ConversationsHandler`
- **功能**: 获取LLM对话历史记录
- **返回数据**:
  - `conversations[]`: 对话数组
    - `model_id`: 模型ID
    - `messages[]`: 消息数组
      - `role`: 角色（system/user/assistant）
      - `content`: 内容
      - `timestamp`: 时间戳
  - `serverTime`: 服务器时间戳

### 计算逻辑

API层与执行引擎完全分离，通过数据源抽象（`DataSource`接口）获取数据。目前支持两种数据源实现：

#### 1. 数据源抽象 (`internal/data/datasource.go`)

定义统一的数据加载接口：

```go
type DataSource interface {
    LoadCryptoPrices() (*types.CryptoPricesResponse, error)
    LoadAccountTotals() (*types.AccountTotalsResponse, error)
    LoadTrades() (*types.TradesResponse, error)
    LoadSinceInception() (*types.SinceInceptionResponse, error)
    LoadLeaderboard() (*types.LeaderboardResponse, error)
    LoadAnalytics() (*types.AnalyticsResponse, error)
    LoadModelAnalytics(modelId string) (*types.ModelAnalyticsResponse, error)
    LoadPositions() (*types.PositionsResponse, error)
    LoadConversations() (*types.ConversationsResponse, error)
}
```

#### 2. JSON文件数据源 (`internal/data/loader.go`)

当前实现基于JSON文件读取（MCP模式）：

**DataLoader结构**：
- 从 `DataPath` 配置的目录读取预生成的JSON文件
- 文件命名规范：
  - `crypto-prices.json` - 价格数据
  - `account-totals.json` - 账户汇总
  - `trades.json` - 交易历史
  - `positions.json` - 持仓信息
  - `analytics.json` - 综合分析
  - `analytics-{modelId}.json` - 单模型分析
  - `leaderboard.json` - 排行榜
  - `since-inception-values.json` - 启动以来数据
  - `conversations.json` - 对话历史

**特点**：
- 即时响应：无需数据库查询，直接读取预计算结果
- 服务端时间戳注入：自动添加 `serverTime` 字段
- 降级处理：模型分析不存在时返回空数据而非错误

#### 3. 业务逻辑层 (`internal/logic/`)

每个Handler对应一个Logic层，负责调用DataLoader：

**模式**：
```go
type XxxLogic struct {
    ctx    context.Context
    svcCtx *svc.ServiceContext  // 包含DataLoader实例
}

func (l *XxxLogic) Method() (*types.Response, error) {
    return l.svcCtx.DataLoader.LoadXxx()
}
```

**特点**：
- 薄逻辑层：仅做数据转发，不做复杂计算
- 统一错误处理：通过go-zero框架统一处理
- 可扩展：未来可替换为数据库实现

#### 4. 数据计算流程

虽然API层不直接计算，但执行引擎会持续更新JSON文件：

**执行引擎计算**（`pkg/manager/manager.go:266-407`）：
1. **实时状态更新**：
   - 每个交易周期同步账户状态（`SyncTraderPositions`）
   - 从交易所获取账户净值、保证金使用、未实现盈亏
   - 更新trader的 `ResourceAlloc` 和 `Performance` 指标

2. **绩效指标计算**（`pkg/manager/trader.go`）：
   - 夏普比率（Sharpe Ratio）
   - 胜率（Win Rate）
   - 最大回撤（Max Drawdown）
   - 总盈亏（Total PnL）

3. **持久化记录**（`pkg/manager/persistence.go`）：
   - `RecordPositionEvent`: 开仓/平仓事件
   - `RecordDecisionCycle`: 决策周期完整记录
   - `RecordAccountSnapshot`: 账户快照
   - `RecordAnalytics`: 分析数据快照

4. **数据导出**：
   - 定期（或触发）将缓存数据导出为JSON文件
   - JSON文件供API层读取

#### 5. 计算指标说明

**账户级指标**：
- `dollar_equity`: 账户总净值（美元）
- `realized_pnl`: 已实现盈亏（已平仓）
- `total_unrealized_pnl`: 未实现盈亏（持仓中）
- `cum_pnl_pct`: 累计盈亏百分比 = (realized + unrealized) / initial_equity × 100
- `sharpe_ratio`: 夏普比率 = (平均收益 - 无风险收益) / 收益标准差
- `margin_used_pct`: 保证金使用率 = margin_used / total_equity × 100

**交易级指标**：
- `realized_gross_pnl`: 总盈亏（未扣手续费）
- `realized_net_pnl`: 净盈亏（扣除手续费）
- `avg_holding_period_mins`: 平均持仓时间（分钟）
- `win_rate`: 胜率 = 盈利交易数 / 总交易数 × 100
- `long_short_ratio`: 多空比例 = 做多次数 / 做空次数

**信号级指标**：
- `long_signal_pct`: 做多信号百分比
- `short_signal_pct`: 做空信号百分比
- `hold_signal_pct`: 持有信号百分比
- `close_signal_pct`: 平仓信号百分比
- `avg_confidence`: 平均置信度
- `avg_leverage`: 平均杠杆

### 存储

系统采用多层存储架构，结合数据库、缓存和文件系统：

#### 1. PostgreSQL 数据库存储

**主要用途**：持久化核心业务数据

**数据表设计**（按文档描述）：

1. **Trader相关表**：
   - `traders`: trader配置和元信息
     - trader_id, name, config, risk_params
   - `trader_positions`: 历史持仓记录
     - trader_id, symbol, side, entry_price, exit_price, pnl
   - `trader_orders`: 历史委托记录
     - trader_id, order_id, symbol, side, price, size, status
   - `trader_fills`: 历史成交记录
     - trader_id, order_id, fill_price, fill_size, commission
   - `trader_performance`: 绩效指标快照
     - trader_id, timestamp, equity, pnl, sharpe_ratio, win_rate

2. **Exchange Provider相关表**：
   - `exchange_positions`: 交易所级别持仓记录
     - provider_id, symbol, side, size, entry_price
   - `exchange_orders`: 交易所级别委托记录
     - provider_id, order_id, symbol, status, timestamps

3. **Market数据表**：
   - `assets`: 币对基础信息
     - symbol, name, precision, max_leverage, is_active
   - `klines`: K线历史数据
     - symbol, interval, timestamp, open, high, low, close, volume
   - `indicators`: 指标历史数据
     - symbol, timestamp, ema, macd, rsi, atr

4. **Decision决策表**：
   - `decision_cycles`: 决策周期记录
     - trader_id, timestamp, prompt_digest, decisions_json
   - `llm_conversations`: LLM对话记录
     - trader_id, timestamp, prompt, response, tokens, model
   - `decision_validations`: 风控验证记录
     - trader_id, symbol, action, validation_result, error_msg

**配置**（`etc/nof0.yaml`）：
```yaml
Postgres:
  DataSource: "${Postgres__DataSource}"
  MaxOpen: 25      # 最大连接数
  MaxIdle: 10      # 最大空闲连接
  MaxLifetime: 5m  # 连接最大生命周期
```

#### 2. Redis 缓存存储

**主要用途**：高频访问数据的缓存层

**缓存键设计**：

1. **Trader状态缓存**（实时）：
   - `trader:{trader_id}:equity` → 当前净值
   - `trader:{trader_id}:positions` → 当前持仓哈希
   - `trader:{trader_id}:margin_used` → 已用保证金

2. **Exchange Provider缓存**：
   - `exchange:{provider_id}:account` → 账户状态
   - `exchange:{provider_id}:positions` → 当前持仓列表

3. **Market数据缓存**：
   - `market:prices` → 价格哈希（所有币对）
   - `market:snapshot:{symbol}` → 币对快照（价格、OI、资金费率、指标）
   - `market:assets` → 资产列表

4. **API结果缓存**（见计算逻辑章节）：
   - `api:crypto-prices` → TTL: Short (10s)
   - `api:trades` → TTL: Medium (60s)
   - `api:analytics` → TTL: Long (300s)

**TTL配置**：
```yaml
TTL:
  Short: 10      # 快速变化数据（价格）
  Medium: 60     # 列表数据（交易）
  Long: 300      # 聚合数据（分析）
```

**缓存特性**：
- 自动过期：基于TTL配置
- 哈希优化：使用Redis Hash存储资产价格（`internal/data/loader.go`提到的优化）
- 并发访问：支持多trader并发读写

#### 3. 文件系统存储

**主要用途**：数据导出、日志、Journal

1. **Manager状态文件**（`etc/manager.yaml`）：
   ```yaml
   state_storage_backend: file
   state_storage_path: ../data/manager_state.json
   ```
   - 存储trader分配状态、再平衡历史

2. **MCP数据文件**（`etc/nof0.yaml`）：
   ```yaml
   DataPath: ../mcp/data
   ```
   - 用于API层读取的JSON文件
   - 由执行引擎定期生成/更新

3. **Journal日志**（`pkg/journal`）：
   - 每个trader可配置独立的journal目录
   - 记录每个交易周期的完整上下文：
     - 提示词摘要（digest）
     - CoT推理轨迹
     - 决策结果JSON
     - 市场快照
     - 账户状态
     - 执行结果
   - 用于回测、审计、调试

4. **系统日志**：
   - go-zero框架统一日志
   - 慢查询日志（SQL > 10s, Redis > 5s）
   - 决策日志（执行引擎）

#### 4. 数据流向

```
执行引擎 → Postgres (持久化)
         ↓
       Redis (缓存)
         ↓
      JSON文件 (导出)
         ↓
       API层 (读取) → 前端
```

**写入路径**（执行引擎）：
1. Manager交易决策 → Postgres + Redis
2. 定期刷新 → JSON文件（MCP数据）
3. Journal记录 → 文件系统

**读取路径**（API）：
1. HTTP请求 → Handler → Logic
2. Logic → DataLoader → 读取JSON文件
3. 自动注入 `serverTime` → 返回

#### 5. 存储一致性保证

- **最终一致性模型**：执行引擎和API层最终一致
- **数据延迟**：JSON文件更新周期内可能有延迟
- **幂等性保证**：
  - 订单使用 `cloid`（client order id）防重（`pkg/manager/manager.go:940-948`）
  - 决策使用 `uuid` 参数去重
- **失败处理**：
  - 持久化失败仅记录日志，不阻塞交易
  - 超时机制：数据库3s，Redis 5s

#### 6. 性能优化措施

- **批量插入**：交易记录批量写入数据库
- **连接池**：数据库连接池复用（MaxOpen: 25）
- **并发worker**：市场数据并发拉取（参考recent commit: "optimize asset caching with concurrent workers"）
- **哈希缓存**：Redis Hash存储资产价格，减少键数量
- **超时控制**：所有操作设置合理超时（见配置章节）
