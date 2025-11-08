# 系统概述

基于AI工作流的Crypto交易系统

## 设计理念（黄金标准）

> 该章节定义所有新增/修改需求的“北极星”。任何实现若偏离下述原则，必须先更新文档，再落地代码。

### 核心原则

1. **控制面与执行面彻底解耦**  
   - internal/ 仅暴露查询与指令入口，不持有业务态。  
   - pkg/ 负责实时决策、风控与状态机，需在无 API 的情况下也能自洽运行。
2. **Trader 即策略容器**  
   - Trader 抽象必须显式声明：讯源（market provider）、交易通道（exchange provider）、提示词、风控、频率、资金域。  
   - 任意新策略都应只修改配置/Prompt，除非违反通用约束。
3. **可回放、可验证、可切换**  
   - 所有交易决策都能通过 Journal + MCP JSON 复现。  
   - Provider、LLM、资金分配要支持热切换，且切换路径在配置层可描述。  
   - 数据一致性遵循“Redis 缓存 → JSON 导出 → API”逐级降级策略。
4. **Fail Closed, Not Fail Silent**  
   - 风控未通过/行情缺失/LLM结构化失败时，必须保持仓位不变并发出告警。  
   - 任何“默认值”都需要显式注释其安全理由。
5. **成本感知的 AI Orchestration**  
   - LLM 调用受限于预算、延迟与成功率：Prompt digest cache、结构化校验与 fallback 模型缺一不可。  
   - Trader 级 KPI（Sharpe、DD、利用率）直接驱动资金再分配与模型选择。

### 设计审阅要点

| 维度 | 评审结论 | 必须遵循的检查项 |
| --- | --- | --- |
| 架构分层 | 控制面/执行面边界清晰，但需持续校验 internal 不引用 pkg/manager 内部状态 | CI 中增加 forbid-import 规则；接口层仅消费 DataSource |
| 状态管理 | Redis+JSON 做权威数据快照合理，但需补齐“快照原子性”方案 | 导出器以 trader_id 维度写临时文件，再原子 rename，上层读取只看稳定文件名 |
| LLM 风控 | Prompt 模板与风控参数同源，便于审核；建议新增 schema version | executor.yaml 中加入 `prompt_schema_version`，Journal 记录版本号 |
| 资金隔离 | 虚拟本金=真实账户子集的假设成立，但需约束 exchange provider 的全局操作 | 禁用全局平仓 API、限制 withdraw 入口仅由运维 CLI 调用 |
| 观测性 | 现有日志足够定位单次决策，但缺运行面板 | Prometheus exporter 必须覆盖：LLM QPS/失败率、trader 决策延迟、Redis/DB RTT |

> 架构评审通过的前提：提交 PR 必须在描述中映射到上述黄金标准项，否则视为欠缺论证。

---

## 架构概览

系统采用**控制面与执行面彻底解耦**的架构：

- **执行引擎（pkg/）**：自主运行的交易决策核心，包含 Manager、Executor、Exchange/Market Provider、LLM 调用。即使无 API 可用，引擎依然能通过配置文件和缓存自洽运行。
- **控制面（internal/）**：通过 REST API 暴露查询与指令入口，不持有业务状态，所有数据来源于 DataLoader（MCP JSON 文件）或 Redis/Postgres 缓存。

### Trader 即策略容器

每个 **Trader** 是一个独立的虚拟交易账户，封装以下要素：

| 配置维度 | 说明 | 可组合性 |
|---------|------|---------|
| 行情源（Market Provider） | 指定 K 线、指标、OI 数据来源 | 支持多源热备、纸面/实盘切换 |
| 交易通道（Exchange Provider） | 订单执行与账户状态同步 | 支持 Sim/Hyperliquid 等自由切换 |
| Prompt 模板 | System/User Prompt 模板路径 | 每个 Trader 可使用不同策略提示词 |
| 风控参数 | 杠杆、置信度、最大仓位数、保证金上限 | Trader 级独立配置 |
| 决策频率与模型 | 调用间隔、LLM 模型选择 | 支持高频/低频混合部署 |
| 资金域（Virtual Capital） | 起始本金、资金分配百分比 | **隔离仓位**：Trader 仓位是 Exchange Provider 账户的逻辑子集，多个 Trader 可共享同一物理账户 |

通过 Trader 抽象，系统天然支持：
- 行情与交易分离（回测使用历史行情 + Sim 交易所）
- 纸面交易（Paper Trading）：使用实盘行情 + Sim 交易所
- 提示词 A/B 测试：同一账户运行多策略 Trader
- 成本优化：按 Trader KPI 动态分配资金与模型

### 架构层级概览

1. **接口层（internal/handler, internal/logic）**：通过 go-zero 提供 REST API，仅做数据读取与入参校验，所有业务态由下层生成。
2. **服务编排层（internal/svc）**：集中加载配置、构造 DataLoader、LLM/Executor/Manager/Provider，确保跨模块引用在进程启动时即被验证。
3. **执行引擎（pkg/manager, pkg/executor, pkg/exchange, pkg/market, pkg/llm）**：实现交易决策、风控、行情/交易所抽象与大模型调用，是系统稳健性的核心。
4. **数据与持久化层（internal/persistence, internal/cache, docs/migrations 等）**：负责 Postgres/Redis/JSON/Journaling 的一致性写入与回放。
5. **工具与任务层（cmd/*, scripts/, pkg/backtest）**：提供 cron 监控、数据导入、回测等外围能力，用于维持数据新鲜度和快速恢复。

> 该分层保证了“控制面（API）”与“执行面（引擎）”的清晰边界，任何一层故障都可以通过上/下游的缓存与文件导出机制进行降级。

## 执行引擎

### 模块职责

**pkg/manager**（Trader 生命周期管理）

- 初始化与同步 Trader 状态（净值、仓位、指标历史）
- 定时轮询 Trader，触发决策周期
- 执行决策：下单、平仓、风控二次校验
- **失败模式**：Trader 队列阻塞 → Worker 池限流；配置漂移 → 启动时校验 Provider ID

**pkg/executor**（AI 决策引擎）

- 渲染 Prompt（System + User + Market Context）
- 调用 LLM 并验证结构化输出（JSON Schema）
- **执行前风控**：置信度、风险回报比、最大持仓数、杠杆限制
- **失败模式**：Prompt 渲染失败 / LLM 超时 → 保持仓位不变，记录失败计数；连续失败 → 触发降级模型

**pkg/exchange**（交易所抽象层）

- 账户状态查询（资产、保证金、持仓）
- 订单执行（Market/Limit/IOC）与活跃订单管理
- 提供 Sim Provider（`pkg/exchange/sim`）用于纸面交易
- **失败模式**：nonce 冲突 / 订单重复 → `cloid` 去重；网络抖动 → 指数退避重试

**pkg/market**（行情数据层）

- K 线数据获取与指标计算（EMA、MACD、RSI、ATR）
- 实时数据：OI、资金费率、成交量、标记价格
- **失败模式**：K 线延迟 > 3s → 多 Provider 热备 + 指标回填；数据缺失 → 不触发决策（Fail Closed）

**pkg/llm**（大模型接口）

- 统一 OpenAI/Anthropic/DeepSeek API 调用
- Token 统计与成本监控
- **失败模式**：API quota 超限 → 切换备选模型；结构化失败 → 重试 + Fallback

**pkg/backtest**（回测引擎）

- 复用 Manager/Executor/Market/Exchange 抽象
- 通过 Journal Replay 确保回测与实盘一致性

**pkg/journal**（可回放日志）

- 记录每个决策周期的完整上下文：Prompt Digest、LLM 响应、市场快照、执行结果
- **失败模式**：IO 阻塞 → 异步 Writer + 文件切分（按大小/时间）

**模块责任矩阵（Review Checklist）**

| 模块 | 延迟预算 | 关键失败模式 | 措施 |
| --- | --- | --- | --- |
| pkg/manager | ≤10ms/tick（不含 I/O） | Trader 队列阻塞、配置漂移 | Worker 池 + provider 校验 + `context.Context` 超时 |
| pkg/executor | LLM round-trip ≤60s | Prompt 渲染/结构化失败 | Prompt digest cache、JSON Schema 校验、fallback 模型 |
| pkg/exchange | REST/Websocket 抖动 | nonce 不一致、订单重复 | 统一 CbOR/JSON 编码、`cloid` 去重、指数退避重试 |
| pkg/market | K线延迟 > 3s | 缺 tick 时误判信号 | 多 provider 热备 + 指标回填 |
| pkg/llm | API quota / cost | 预算超标 | Token 监控、模型分级（主/备/低成本） |
| pkg/backtest | 回测与实盘偏差 | 数据漂移 | 复用同一 DataLoader + `journal replay` |
| pkg/journal | 写放大 | IO 阻塞主循环 | 异步 writer + 文件切分 (size/time) |

### 运行期生命周期

1. **引导阶段**
   - `internal/svc` 加载主配置以及引用的 LLM/Executor/Manager/Exchange/Market 子配置，解析 BaseDir，验证 provider ID 是否一致。
   - 依据配置是否提供 Postgres/Redis DSN，按需初始化连接池与缓存 TTL 集合，并注入可选的数据库模型。
   - 构造 `data.DataLoader` 指向 `DataPath`，即便数据库暂不可用，API 仍能读取 JSON 数据对外服务。
2. **Trader 装配阶段**
   - `pkg/manager` 遍历 `manager.yaml` 中的 trader，绑定具体的 market/exchange provider，实例化与 trader 风控参数一致的 executor。
   - `ManagerPromptRenderers` 与 prompt digest 被缓存，避免 runtime 模板解析抖动。
3. **主循环阶段**
   - `Manager.RunTradingLoop` 以 1s tick 轮询活跃 trader，根据决策间隔、Sharpe gating、冷却期等条件判断是否触发一次决策。
   - `Executor.GetFullDecision` 渲染 prompt → 调用 LLM → 验证结构化输出；失败记录会增加 symbol 级失败计数，防止抖动。
   - 决策落地后，`PersistenceService` 把仓位事件、决策周期、账户快照、分析指标写入 Postgres/Redis，并触发 Journal 写入。
4. **数据导出阶段**
   - 定时任务或专用 exporter 将 Redis/DB 中的最新状态渲染成 MCP JSON 文件，供 API 与外部系统消费。
   - `cmd/importer` 可以将 JSON 重新写回数据库，形成“可回放”的闭环，支持灾备或重建环境。
5. **再平衡与巡检阶段**
   - `pkg/manager.Rebalance` 在 `rebalance_interval` 触发，依据 trader KPI 与风险限额重新分配资金池。
   - `monitoring` 组件每 15s 采集 LLM/Exchange 延迟、Trader backlog、Redis/DB RTT 并对外暴露指标；若 KPI 超阈值则通过 webhook 告警。
   - `health check CLI` 可离线重放最近 N 条 Journal，确认 prompt schema 与执行结果一致后再恢复实盘。

### 决策执行流程

**主流程**（体现"Fail Closed, Not Fail Silent"原则）

1. **引导阶段**
   - 加载配置，校验 Provider ID 一致性、BaseDir 存在性
   - 初始化数据库连接池与 Redis 缓存
   - 构造 DataLoader 指向 MCP JSON 文件（即便数据库不可用，API 仍可降级服务）

2. **状态同步阶段**
   - 从 Redis/Postgres 加载 Trader 状态（净值、持仓、历史）
   - 调用 `SyncTraderPositions` 从 Exchange Provider 同步最新账户状态
   - 更新 Trader 的 `ResourceAlloc` 和 `Performance` 指标

3. **决策触发阶段**
   - Manager 以 1s tick 轮询活跃 Trader
   - 根据 `decision_interval`、Sharpe gating、冷却期判断是否触发决策
   - **若行情缺失或过期 → 跳过本周期（Fail Closed）**

4. **AI 决策阶段**
   - Executor 渲染 Prompt（System + User + Market Context）
   - 调用 LLM 并验证结构化输出（JSON Schema）
   - **执行前风控**：置信度、风险回报比、最大持仓数、杠杆限制
   - **若风控未通过或 LLM 失败 → 保持仓位不变，记录失败（Fail Closed）**

5. **订单执行阶段**
   - Manager 再次校验 `MaxPositionSizeUSD`、保证金使用率（双重保险）
   - 通过 Exchange Provider 执行订单（带 `cloid` 去重）
   - **若订单失败 → 记录日志，不影响其他 Trader**

6. **持久化与导出阶段**
   - `PersistenceService` 写入 Postgres/Redis（决策周期、账户快照、分析指标）
   - `Journal` 异步写入完整上下文（Prompt、LLM 响应、市场快照、执行结果）
   - 定时导出 MCP JSON 文件供 API 消费
   - **若持久化失败 → 记录告警，不阻塞下一周期**

**失败降级机制**

| 失败场景 | 降级策略 | 告警 |
|---------|---------|------|
| LLM 超时/结构化失败 | 保持仓位不变，增加失败计数；连续失败 → 切换备选模型 | Journal + Webhook |
| 行情数据缺失/延迟 | 跳过本次决策（Fail Closed） | 日志记录 |
| Exchange API 失败 | 指数退避重试；订单去重（`cloid`） | 日志 + 计数 |
| Postgres/Redis 失败 | 仅记录日志，不阻塞决策循环；API 降级读取 JSON | Webhook 告警 |
| 风控校验未通过 | 拒绝订单，保持仓位不变 | Journal 记录原因 |

**数据存储分层**（支持"可回放、可验证、可切换"）

1. trader: 指标参数、历史仓位、历史委托和成交情况等
2. exchange provider: 历史仓位、历史委托和成交
3. market: 币对基础信息、历史行情信息、历史指标信息
4. decision: 决策上下文、LLM对话、决策结果、风控信息

缓存存储:

1. trader: 净值余额、当前仓位
2. exchange provider: 净值余额、当前仓位
3. market：当前行情信息，币对基础信息

### 配置

> **配置治理黄金守则（强制执行）**
> 1. `etc/*` 文件禁止在运行时被写入；所有动态状态写入 `DataPath` / `state_storage_path`。
> 2. 新增 provider 时必须同时补齐：`healthcheck`、`timeout`、`retry` 与 `capabilities` 字段，并在 `internal/svc` 启动阶段校验。
> 3. `manager.yaml` 中 `allocation_pct` 之和 ≤ `100 - reserve_equity_pct`，CI 中以 yaml 测试校验。
> 4. **Prompt 模板文件需声明 `Version:` 头部**，Executor 仅在版本匹配时加载，避免旧模板 silently 生效。
> 5. 配置文件改动默认需要 `docs/blueprint.md` 更新对应章节，方可合并。

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

大语言模型配置（体现"成本感知的 AI Orchestration"）：

```yaml
base_url: "https://zenmux.ai/api/v1"  # LLM服务端点
api_key: "${ZENMUX_API_KEY}"          # API密钥（环境变量）
default_model: "gpt-5"                # 默认模型
timeout: "60s"                        # 请求超时
max_retries: 3                        # 最大重试次数
log_level: "info"

# 成本预算与监控（设计理念第5条）
budget:
  daily_token_limit: 1000000          # 每日 Token 上限
  cost_per_million_tokens:            # 成本配置（美元/百万 tokens）
    gpt-5: 15.0
    claude-sonnet-4.5: 3.0
    deepseek-chat: 0.14
  alert_threshold_pct: 80             # 预算告警阈值（80%）

models:
  gpt-5:
    provider: "openai"
    model_name: "openai/gpt-5"
    temperature: 0.7
    max_completion_tokens: 4096
    priority: 1                       # 模型优先级（1=主模型）
    cost_tier: high                   # 成本分级

  claude-sonnet-4.5:
    provider: "anthropic"
    model_name: "anthropic/claude-sonnet-4.5"
    temperature: 0.7
    max_completion_tokens: 4096
    priority: 2                       # 备选模型
    cost_tier: medium

  deepseek-chat:
    provider: "deepseek"
    model_name: "deepseek/deepseek-chat-v3.1"
    temperature: 0.6
    max_completion_tokens: 4096
    priority: 3                       # 低成本 Fallback
    cost_tier: low

# Prompt Digest Cache（减少重复计算）
cache:
  enabled: true
  ttl: 3600                           # 缓存有效期（秒）
  max_entries: 10000                  # 最大缓存条目数
```

**降级策略**：当主模型（priority=1）连续失败或成本超预算时，自动切换至 priority 更高的模型；恢复条件："成功率 > 99% 且成本 < 预算"双阈值。

#### 3. 执行器配置 (`etc/executor.yaml`)

默认风控参数与 Prompt Schema 版本管理：

```yaml
# Prompt Schema 版本（设计理念：可回放、可验证）
prompt_schema_version: "v1.2.0"       # 当前 Prompt Schema 版本
prompt_validation:
  strict_mode: true                   # 严格模式：不匹配则拒绝加载
  require_version_header: true        # Prompt 模板必须包含 Version 头

# 默认风控参数
major_coin_leverage: 20      # 主流币杠杆（BTC/ETH）
altcoin_leverage: 10         # 山寨币杠杆
min_confidence: 75           # 最低置信度要求
min_risk_reward: 3.0         # 最低风险回报比
max_positions: 4             # 最大持仓数
decision_interval: 3m        # 决策间隔
decision_timeout: 60s        # 决策超时时间
max_concurrent_decisions: 1  # 最大并发决策数

# JSON Schema 校验（结构化输出验证）
output_validation:
  enabled: true
  schema_path: "schemas/decision_output.json"  # JSON Schema 文件路径
  fail_on_invalid: true                        # 校验失败时拒绝决策
```

**Prompt 模板版本管理**：每个 `.tmpl` 文件必须以以下格式声明版本：
```
{{/* Version: v1.2.0 */}}
{{/* Description: Aggressive Short Strategy */}}
...
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
- **版本化 Prompt**：模板文件必须声明版本，确保可回放性
- **成本感知**：LLM 配置包含预算、优先级与降级策略

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
- **状态重建**：
  - `cmd/importer` 可以从 MCP JSON 全量导入 Postgres，结合 Redis Key TTL，可在冷启动/集群扩容时快速回放最近状态。
  - `ManagerTraderExchange/Market` 的映射缓存于 ServiceContext，若配置或 provider 发生漂移，重启即可恢复一致性。

#### 6. 性能优化措施

- **批量插入**：交易记录批量写入数据库
- **连接池**：数据库连接池复用（MaxOpen: 25）
- **并发worker**：市场数据并发拉取（参考recent commit: "optimize asset caching with concurrent workers"）
- **哈希缓存**：Redis Hash存储资产价格，减少键数量
- **超时控制**：所有操作设置合理超时（见配置章节）

## 稳健性与运维

### 依赖矩阵（体现"可回放、可验证、可切换"）

| 组件 | 强依赖 | 弱依赖 | 降级策略 |
| --- | --- | --- | --- |
| API 进程 | JSON 文件、配置 | Postgres/Redis | JSON 作为权威数据源；DB/Cache 可选，仅在提供 DSN 时启用 |
| 执行引擎 | Redis、Exchange/Market API、LLM | Postgres | Redis + provider 决定实时性；Postgres 失败 → 日志告警但不阻塞决策 |
| Cron/Importer | Exchange/Market、Postgres | JSON 文件 | 监控与数据导入程序可在独立节点运行；支持 JSON → DB 的灾备重建 |

### 监控与告警（体现"成本感知"与"Fail Closed"）

**关键指标（Prometheus）**：
- **LLM 层**：QPS、失败率、平均延迟、Token 用量、成本累计
- **Trader 层**：决策延迟、风控拒绝率、持仓数、保证金使用率、Sharpe Ratio
- **Exchange 层**：API 延迟、订单成功率、nonce 冲突次数
- **Market 层**：K 线延迟、数据缺失率、Provider 切换次数
- **存储层**：Redis/DB RTT、慢查询计数、连接池使用率

**告警规则**：
- LLM 成本超预算 80% → Webhook 告警 + 自动降级至低成本模型
- Trader 决策失败率 > 10% → 暂停自动交易，等待人工介入
- Exchange API 失败率 > 5% → 触发 Provider 切换（如 Hyperliquid → Sim）
- Redis 不可用 → API 降级至纯 JSON 模式

### 故障应对策略

| 故障场景 | 应对措施 | 恢复路径 |
|---------|---------|---------|
| **LLM 异常** | Executor 重试（`RetryHandler`）→ 切换备选模型 → 仍失败则 Fail Closed | 成功率 > 99% 且成本 < 预算时切回主模型 |
| **Exchange API 故障** | 指数退避重试 → 快速迁移到 `sim` provider（保留决策链） | Provider 健康检查恢复后重新绑定 |
| **行情数据缺失/延迟** | 跳过本次决策（Fail Closed）→ 多 Provider 热备 | 数据恢复后自动继续 |
| **Postgres/Redis 中断** | PersistenceService 仅记录日志 → API 消费 JSON | 数据库恢复后通过 Journal Replay 重建状态 |
| **配置漂移** | 启动时强校验 Provider ID、分配比率 → Panic 阻止上线 | 修复配置后重启 |

### 部署与启动顺序

1. **执行引擎优先部署**：确保 Redis/JSON 中存在最新数据，再启动 API 进程
2. **单进程 all-in-one 模式**：`nof0.go` 启动 API + Manager CLI 运行 Trader 循环
3. **分布式部署**：执行引擎、API、Cron/Importer 可拆分为独立容器
4. **启动自检**：ServiceContext 加载时立即校验 Provider、BaseDir、配置一致性

### 配置与运维治理

- **12-Factor 兼容**：所有 `etc/*.yaml` 支持 `${ENV_VAR}` 注入
- **Provider 强校验**：`manager.yaml` 的 provider id 在启动阶段强制匹配
- **TTL 集中管理**：缓存过期、日志阈值统一在 `etc/nof0.yaml` 配置，可跨环境覆盖
- **Prompt 版本锁定**：Executor 启动时校验模板 Version 头，不匹配则拒绝加载（见配置章节）
- **资金分配校验**：`allocation_pct` 之和 ≤ `100 - reserve_equity_pct`（CI 自动化测试）

通过上述分层、流程与运行保障，文档与仓库实现之间形成一套可验证的契约，能够指导新成员在开发、部署、扩容、故障恢复等环节保持同样的稳健性假设。

## 架构Review结论与行动项

- **Golden Test 套件**：补充 `cmd/archtest`，在 CI 中静态验证 provider id、配置比率、Prompt schema 版本，防止偏离黄金标准。
- **观测性落地**：以 Prometheus 指标为基线，新增 Dashboards（LLM/Exchange/TPS/VWAP 偏差），并把慢查询阈值暴露为 runtime config。
- **LLM 成本 Budget Guard**：实现 `pkg/llm/budget.go`，周期性读取 token 用量，触发资金池再平衡时同步参考成本。
- **Replay-first 回归流程**：发布前必须跑 `journal replay` 覆盖主策略，确保 Prompt、Executor、Persistence 在最新代码下仍可重建相同决策。
- **安全上线流程**：运维 checklist 包含“禁用全局平仓”“确认 Reserve ≥10%”“启用 degradation webhook”，上线记录附带审计链路。
