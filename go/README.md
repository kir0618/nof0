# NOF0 Backend - Go API Server

> 高性能Go语言实现的NOF0 Alpha Arena后端API，提供AI交易模型的实时数据、分析和排行榜服务

**[返回项目主页](../README.md)** | **状态**: 生产就绪 | **版本**: v1.1.0

---

## 概述

NOF0后端是基于Go-Zero框架的微服务API，为前端提供7个REST端点。支持双模式数据源：
- **文件模式**: 从JSON文件快速加载（开发/演示）
- **数据库模式**: Postgres + Redis（生产环境）

### 核心特性

| 特性 | 说明 | 指标 |
|------|------|------|
| **高性能** | 优化的数据加载 | 响应时间 <10ms (90%) |
| **类型安全** | 完整Go类型系统 | 27字段Trade, 11+字段Account |
| **全面测试** | 单元+集成测试 | 数据层88%覆盖率 |
| **双模式数据源** | 文件/数据库自动切换 | Postgres+Redis 可选 |
| **生产就绪** | CORS + 日志 + 监控 | 单二进制部署 |

### 技术栈 {#tech-stack}

<table>
<tr>
<td width="50%">

**后端框架**
- [Go-Zero](https://go-zero.dev/) - 微服务框架
- [pgx/v5](https://github.com/jackc/pgx) - Postgres驱动
- [go-redis](https://github.com/redis/go-redis) - Redis客户端

</td>
<td width="50%">

**前端技术** *(web/目录)*
- React 18 + TypeScript
- Recharts 图表库
- TanStack Query 数据管理

</td>
</tr>
</table>

---

## 快速开始

### 方式一: 使用文件数据源 (推荐入门)

```bash
# 1. 克隆仓库并进入后端目录
cd go

# 2. 安装依赖
go mod download

# 3. 构建并运行
go build -o nof0-api ./nof0.go
./nof0-api -f etc/nof0.yaml
```

服务启动在 `http://localhost:8888`

**测试 API**:
```bash
# 实时价格
curl http://localhost:8888/api/crypto-prices

# AI排行榜
curl http://localhost:8888/api/leaderboard

# 交易历史
curl http://localhost:8888/api/trades
```

---

**配置指南（重要变更）**

- 管理器 Manager 仅通过 Provider ID 引用外部依赖：
  - 交易所凭证统一放在 `etc/exchange.yaml`
  - 行情数据源统一放在 `etc/market.yaml`
  - 每个 Trader 在 `etc/manager.yaml` 中使用 `exchange_provider` 与 `market_provider` 指向上述 Provider ID
- 执行器 Executor 配置不再包含模型或 Prompt：
  - 已移除 `model_alias`、`prompt_template` 字段
  - 模板路径由 Trader 侧注入（`prompt_template` 位于 `etc/prompts/manager/*.tmpl`）

最小可运行示例

1) 设置必要环境变量（示例使用 Hyperliquid 与默认 LLM 网关）：

```bash
export ZENMUX_API_KEY=your_llm_api_key
export HYPERLIQUID_PRIVATE_KEY=0000000000000000000000000000000000000000000000000000000000000001
```

2) 核对配置文件（仓库已提供样例，可按需调整）：

- `etc/nof0.yaml`
  - 指向各模块子配置：`LLM.File`、`Executor.File`、`Manager.File`、`Exchange.File`、`Market.File`

- `etc/manager.yaml`（节选）

```yaml
traders:
  - id: trader_aggressive_short
    name: Aggressive Short
    exchange_provider: hyperliquid_main   # 来自 etc/exchange.yaml 的 providers 键名
    market_provider: hyperliquid          # 来自 etc/market.yaml 的 providers 键名
    prompt_template: prompts/manager/aggressive_short.tmpl
    decision_interval: 3m
    allocation_pct: 40
    auto_start: true
    risk_params:
      max_positions: 3
      max_position_size_usd: 500
      max_margin_usage_pct: 60
      major_coin_leverage: 20
      altcoin_leverage: 10
      min_risk_reward_ratio: 3.0
      min_confidence: 75
      stop_loss_enabled: true
      take_profit_enabled: true
```

- `etc/executor.yaml`（节选：仅执行参数，无模型/Prompt）

```yaml
major_coin_leverage: 20
altcoin_leverage: 10
min_confidence: 75
min_risk_reward: 3.0
max_positions: 4
decision_interval: 3m
decision_timeout: 60s
max_concurrent_decisions: 1
allowed_trader_ids: []
signing_key: ""
overrides: {}
```

- `etc/exchange.yaml`（节选：统一管理交易所凭证）

```yaml
default: hyperliquid_main
providers:
  hyperliquid_main:
    type: hyperliquid
    private_key: ${HYPERLIQUID_PRIVATE_KEY}
    testnet: false
    timeout: 30s
```

- `etc/market.yaml`（节选：统一管理行情数据源）

```yaml
default: hyperliquid
providers:
  hyperliquid:
    type: hyperliquid
    base_url: https://api.hyperliquid.xyz/info
    timeout: 8s
    http_timeout: 10s
    max_retries: 3
```

3) 启动服务：

```bash
go build -o nof0-api ./nof0.go
./nof0-api -f etc/nof0.yaml
```

常见问题

- 报错 “manager trader <id> references unknown exchange provider …”
  - 检查 `etc/manager.yaml` 的 `exchange_provider` 是否与 `etc/exchange.yaml` 的 `providers` 键名一致
- 报错 “market provider … not defined/unknown”
  - 同理检查 `market_provider` 与 `etc/market.yaml` 的 `providers` 键名
- LLM 相关 401/鉴权错误
  - 确认已设置 `ZENMUX_API_KEY`，可通过 `env | grep ZENMUX` 验证

### 方式二: 使用 Docker Compose (含数据库)

```bash
# 启动 Postgres + Redis + API
docker-compose up -d

# 运行数据迁移
make migrate-up

# 导入历史数据 (可选)
go run cmd/importer/main.go -dsn "postgres://nof0:nof0@localhost:5432/nof0?sslmode=disable"
```

### 前置要求

- **最小配置**: Go 1.22+
- **完整配置**: Go 1.22+ + Docker + Postgres 16 + Redis 7

---

## API 端点

### 核心接口

<table>
<tr><th>端点</th><th>说明</th><th>响应时间</th><th>示例</th></tr>
<tr>
  <td><code>/api/crypto-prices</code></td>
  <td>实时加密货币价格</td>
  <td>~2ms</td>
  <td>

```json
{
  "prices": {
    "BTCUSDT": {"price": 68234.5, "timestamp": 1735228800000}
  }
}
```
  </td>
</tr>
<tr>
  <td><code>/api/leaderboard</code></td>
  <td>AI模型排行榜</td>
  <td>~1ms</td>
  <td>

```json
{
  "leaderboard": [
    {"model_id": "qwen3-max", "equity": 12456.78, "sharpe": 1.23}
  ]
}
```
  </td>
</tr>
<tr>
  <td><code>/api/trades</code></td>
  <td>完整交易历史</td>
  <td>~10ms</td>
  <td>27字段Trade数组</td>
</tr>
<tr>
  <td><code>/api/account-totals</code></td>
  <td>账户+持仓详情</td>
  <td>~150ms</td>
  <td>含positions map</td>
</tr>
<tr>
  <td><code>/api/analytics/:id</code></td>
  <td>模型分析数据</td>
  <td>~2ms</td>
  <td>模型级别统计</td>
</tr>
</table>

**完整文档**: [API端点规范](../mcp/data/api-endpoints.json)

---

## 测试

```bash
# 运行所有单元测试
./scripts/run-tests.sh

# 运行集成测试
./scripts/run-integration-tests.sh

# 查看覆盖率
go test -cover ./internal/data/
```

**测试指标**:
- 数据层覆盖率: 88%
- 集成测试: 100% API端点
- 详细文档: [TEST_README.md](TEST_README.md)

### 系统环境与测试模型选择

`etc/nof0.yaml` 新增 `Env` 字段（test|dev|prod，默认 test）。当 `Env=test` 时：
- LLM 默认模型自动切换为低成本模型，避免测试期间产生高额费用。
- 当前使用 `google/gemini-2.5-flash-lite`（性价比高，成本低）。
- 可在 `etc/nof0.yaml` 中设置 `Env: prod` 以使用生产模型配置。

**注意**: Zenmux 的自动路由功能（`zenmux/auto`）目前不稳定，待官方修复后将迁移到智能路由模式

---

## 后端目录结构

```
go/
├── nof0.go                   # 主入口文件
├── etc/nof0.yaml             # 配置文件
├── internal/
│   ├── handler/              # HTTP路由处理器
│   ├── logic/                # 业务逻辑层
│   ├── data/                 # 文件数据源 (JSON)
│   ├── repo/                 # DB数据源 (Postgres+Redis)
│   ├── model/                # 数据库Model层（自动生成）
│   ├── types/                # API类型定义
│   ├── config/               # 配置结构
│   └── svc/                  # 服务上下文
├── cmd/importer/             # 数据导入CLI工具
├── migrations/               # 数据库迁移脚本
├── test/                     # 集成测试套件
└── scripts/                  # 自动化脚本
    ├── run-tests.sh          # 单元测试
    └── run-integration-tests.sh
```

---

## 配置

### 基础配置 (`etc/nof0.yaml`)

```yaml
Name: nof0-api
Host: 0.0.0.0
Port: 8888
DataPath: ../mcp/data  # 文件数据源路径

Cors:
  AllowOrigins: ['*']
```

### 数据库配置 (可选)

启用Postgres + Redis数据源:

```yaml
Postgres:
  DSN: postgres://nof0:nof0@localhost:5432/nof0?sslmode=disable
  MaxOpen: 10
  MaxIdle: 5

Redis:
  Host: localhost:6379
  Type: node

TTL:
  Short: 10    # 快速变化数据 (价格)
  Medium: 60   # 列表数据 (交易)
  Long: 300    # 聚合数据 (排行榜)
```

**初始化数据库**:
```bash
# 运行迁移
make migrate-up

# 导入历史数据
go run cmd/importer/main.go -dsn "$POSTGRES_DSN" -data ../mcp/data
```

**架构设计**: 查看 [docs/data-architecture.md](docs/data-architecture.md) 了解完整数据层设计

---

## 开发指南

### 添加新API端点

1. 定义类型: `internal/types/types.go`
2. 实现数据加载: `internal/data/loader.go` (文件源) 或 `internal/repo/` (DB源)
3. 业务逻辑: `internal/logic/xxx_logic.go`
4. 路由注册: `internal/handler/routes.go`
5. 编写测试: `internal/logic/xxx_logic_test.go`

### 代码质量

```bash
go fmt ./...              # 格式化
golangci-lint run         # 静态检查
go test ./... -cover      # 测试+覆盖率
```

---

## 部署

<table>
<tr>
<td width="50%">

**Docker 部署** (推荐)
```bash
docker-compose up -d
```

</td>
<td width="50%">

**二进制部署**
```bash
go build -o nof0-api
./nof0-api -f etc/nof0.yaml
```

</td>
</tr>
</table>

---

## 故障排查

| 问题 | 解决方案 |
|------|---------|
| 端口8888被占用 | `kill $(lsof -ti:8888)` |
| 数据文件未找到 | 检查 `DataPath` 配置是否指向 `mcp/data` |
| 数据库连接失败 | 验证 `Postgres.DSN` 格式和权限 |
| 测试失败 | 确保 `mcp/data` 目录存在且有权限 |

更多问题: [TEST_README.md#troubleshooting](TEST_README.md#troubleshooting)

---

## 更新日志

### v1.1.0 (2025-10-26) - 数据层升级
- 新增 Postgres + Redis 数据源支持
- 数据库迁移脚本和导入工具
- DataSource 抽象层，支持文件/DB自动切换
- 物化视图和缓存策略设计

### v1.0.0 (2025-10-26) - 初始版本
- 7个REST API端点完整实现
- 文件数据源 (JSON) 完整支持
- 88%数据层测试覆盖 + 100%集成测试
- Docker部署 + 自动化测试脚本

---

## 相关资源

**项目文档**:
- [项目主页](../README.md) - 完整项目愿景和路线图
- [测试指南](TEST_README.md) - 单元测试和集成测试
- [数据架构](docs/data-architecture.md) - 数据库设计文档
- [API规范](../mcp/data/README.md) - 数据格式说明

**技术参考**:
- [Go-Zero 框架](https://go-zero.dev/)
- [NOF1 官方网站](https://nof1.ai/)

---

<div align="center">

**NOF0 Backend API**

**版本**: v1.1.0 | **状态**: 生产就绪 | **更新**: 2025-10-26

[返回项目主页](../README.md)

</div>
