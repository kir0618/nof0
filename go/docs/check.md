# Blueprint Compliance Check (2025-11-08)

The review compares each `etc/` configuration and every `pkg/` module against the contracts captured in `docs/description.md`. Items are called out as ✅ (matches blueprint) or ⚠️ (gap / risk).

## etc/ configs

### etc/nof0.yaml
- ✅ Matches the documented shape (service metadata, Postgres/Cache toggles, TTL tiers, logging knobs) described under “存储与缓存” and “配置治理”.
- ⚠️ Cache nodes rely on `${Cache__0__Tls}` without a default; when the env var is unset go-zero parses an empty string into a bool and panics during `conf.Load`. Consider quoting the placeholder or supplying a default to keep the blueprint’s “JSON remains authoritative even without Redis” promise realistic.

### etc/llm.yaml
- ✅ Mirrors the expected Zenmux/OpenAI routing (base URL, retries, per-model blocks) from the LLM section of the blueprint.
- ⚠️ The comments promise a “LLM_TEST_MODE” override, but neither `pkg/llm/config.go` nor `pkg/llm/client.go` references that env flag, so test-mode switching cannot be achieved without editing YAML.

### etc/executor.yaml
- ✅ Default leverages/confidence/intervals match the executor contract documented in “执行器配置”.
- ⚠️ Fields such as `allowed_trader_ids`, `signing_key`, and `overrides` are parsed (pkg/executor/config.go) but never consumed anywhere else in `pkg/`, meaning operators cannot actually scope executors or apply per-trader overrides even though the blueprint advertises that capability.

### etc/exchange.yaml
- ✅ Provides both the Hyperliquid testnet provider and the `sim` paper venue exactly as described under “交易所配置”. No divergences noted.

### etc/market.yaml
- ✅ Contains the dual Hyperliquid providers with the documented timeout/retry knobs; aligns with “行情配置”.

### etc/manager.yaml
- ✅ Trader definitions and risk params align with the examples in “Manager配置”.
- ⚠️ The blueprint promises per-trader system prompts (“prompt_template”) and journaling knobs, yet `pkg/manager` never reads `PromptTemplate` nor any `journal_*` fields. Supplying new templates in this file has no effect today.

## pkg/ modules

### pkg/manager
- ✅ Core orchestration loop (`pkg/manager/manager.go:286-420`) follows the documented cadence: 1s tick, Sharpe gating, executor call, action fan-out, persistence hooks.
- ⚠️ Manager-level prompt rendering is absent. Although `PromptTemplate` is required in config and `PromptRenderer` exists (`pkg/manager/prompt.go`), nothing invokes it, so blueprint guidance (“ManagerPromptRenderers 与 prompt digest 被缓存”) is unfulfilled; the executor is the only component that ever sees a template.
- ⚠️ Capital policy knobs (`total_equity_usd`, `reserve_equity_pct`, `allocation_strategy`, `rebalance_interval`, `state_storage_*`) are validated but never used beyond `pkg/manager/config.go`. There is no allocator or state persistence, so the documented “ResourceAllocator + state file” contract is missing entirely.
- ⚠️ `SyncTraderPositions` (`pkg/manager/manager.go:631-695`) only fetches `GetAccountState` and never reconciles positions via `GetPositions` as the blueprint’s “账户同步” step specifies. Redis/JSON hydration described in the document can’t happen.
- ⚠️ Cooldown guard does not work: the code records timestamps per symbol (`pkg/manager/manager.go:455-457`) but `buildExecutorContext` never sets `Context.RecentlyClosed` (`pkg/manager/manager.go:950-1114`), so executor-side cooldown checks always see `nil`.
- ⚠️ Performance metrics stay at zero. `t.Performance` only tracks win-rate (`pkg/manager/manager.go:364-379`) and never updates Sharpe or PnL, yet Sharpe gating compares against `ExecGuards.SharpePauseThreshold`. If users set a positive threshold traders will immediately pause after the first cycle because the metric remains 0.0.
- ⚠️ Guard propagation is partial. `buildExecutorContext` forwards liquidity/margin/value-band toggles but omits `MaxPositionSizeUSD` and `MaxRiskPct`, leaving executor-side validation blind even though the blueprint calls for “双重保险 (executor + manager)” on those limits. The manager enforces `MaxPositionSizeUSD` only after receiving a decision (`pkg/manager/manager.go:487-488`), so over-sized actions still burn LLM tokens.

### pkg/executor
- ✅ Prompt rendering/persistence logic lines up with the spec: executor builds rich prompt inputs, logs digests, and records conversations.
- ⚠️ Only one decision per cycle is supported. `decisionContract` maps a single `signal` to exactly one `Decision` (`pkg/executor/utils.go:18-63`), yet `pkg/manager` expects `FullDecision.Decisions` to contain multiple entries to sort close vs open orders. The blueprint’s “关闭→开仓排序” rule cannot be honoured.
- ⚠️ Structured parsing is incomplete: `sanitizeResponse` (`pkg/executor/utils.go:7-15`) and `parseFullDecisionResponse` (`pkg/executor/parser.go`) are stubs, so there is no CoT extraction or JSON repair despite the blueprint’s journaling section calling those artifacts out.
- ⚠️ `RiskUSD` fallback is missing. The document states the engine should derive `risk_usd = position_size / leverage` when the model omits it, but `mapDecisionContract` simply copies the field and validation later depends on it, making the “MaxRiskPct” guard inert.
- ⚠️ Configuration features (`AllowedTraderIDs`, `SigningKey`, `Overrides`) are never applied beyond parsing (`pkg/executor/config.go`), so operators cannot actually scope executors, sign payloads, or tweak per-symbol thresholds as promised.

### pkg/exchange
- ✅ Provider abstraction, Hyperliquid driver, and `sim` paper venue match the blueprint capabilities (real exchange + paper trading). No deviations noted.

### pkg/market
- ✅ Hyperliquid provider builds snapshots with price/indicator/OI/funding data and TTL caches as described. Persistence hooks (`RecordSnapshot`, `RecordPriceSeries`) are wired in (`pkg/market/exchanges/hyperliquid/provider.go:103-140`).

### pkg/llm
- ✅ Client honours base URL/timeout/retry settings, exposes streaming + structured calls, and digests prompts—all consistent with the LLM section of the blueprint.
- ⚠️ The promised “test mode” env overrides are absent (see note under etc/llm), so there is no supported way to switch to the low-cost models the documentation references.

### pkg/journal
- ✅ Standalone JSON writer present, but note that `Manager` never toggles `JournalEnabled` via config, so the journaling guarantees in the blueprint require additional wiring.

### pkg/backtest
- ✅ Engine/portfolio/feeders implement the documented “工具与任务层（pkg/backtest）” features. No mismatches detected.

### pkg/confkit
- ✅ Provides the dotenv + project path helpers assumed throughout the blueprint. No issues found.

## Key remediation steps
1. **Manager:** Implement the missing resource allocator/state persistence pipeline, wire `PromptTemplate` rendering, propagate `MaxPositionSizeUSD`, `MaxRiskPct`, and cooldown metadata into executor contexts, and compute real performance metrics before enabling Sharpe gating.
2. **Executor:** Extend the structured contract to handle multi-action decisions, implement the sanitize/parse/CoT pipeline, and add `RiskUSD` fallback + config override handling so guardrails mirror the documentation.
3. **LLM/Test overrides:** Either remove the comment that advertises `LLM_TEST_MODE` or implement the env-based model substitution so docs and behaviour stay aligned.

