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

{{ 参考etc/完善 }}

## API

### API路由

{{ 参考internal/handler/routes.go完善 }}

### 计算逻辑

直接从执行引擎的数据表和缓存计算得来，不与执行引擎直接交互

{{ 请补充 }}

### 存储

{{ 请补充 }}
