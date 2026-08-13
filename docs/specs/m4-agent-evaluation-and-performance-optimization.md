# M4 Agent 评测与性能优化规格

> 状态：规格已确认并进入实现；Ticket 01 评测资产审计与 Evaluation Ledger 最小闭环已完成。
>
> 本规格对应 MESGuard 简历第五点。现有量化数字均为验收目标，不是既成成果；最终简历必须使用当前实现、固定数据集和可复现评测得到的真实结果。

## Problem Statement

MESGuard 已在前四个简历阶段分别建立了 Tool 选择、Text-to-SQL、RAG 检索与回答质量、综合诊断、
文档处理和长会话治理等评测资产，也已经在多个运行路径中记录模型 Usage、工具轨迹、延迟和局部
降级状态。但是这些能力仍然分散在不同领域对象、Observation、评测命令和文档中，失败语义、指标
命名、运行身份和复测边界没有形成统一合同。

从用户视角看，当前系统还缺少两类直接影响生产体验的优化。第一，知识问答即使遇到与历史问题
语义等价、知识版本未变化的问题，仍会重复执行检索和模型生成，增加响应时间与 Token 消耗。第二，
诊断 Agent 即使已经获得足够且可追溯的证据，也缺少经过成对评测证明的 Early Exit 收益口径，无法
确认提前结束是否在不降低质量的前提下减少模型调用。

从工程视角看，系统还需要解决以下问题：

- Provider、Tool、RAG、缓存和观测故障缺少统一的 `strict`、`repair_then_fail`、`best_effort` 语义；
- 现有 Zap 业务日志、Agent Run Observation 和评测记录缺少统一的链路关联字段；
- 旧评测结果可能因实现、Prompt、模型 Profile、数据集或指标合同变化而过期，不能被无条件复用；
- 运行时只记录 Token 和调用信息，不应在业务程序中维护易变化的供应商人民币价格；
- 当前简历中的 `MPR` 应更正为标准的 `MRR`，`P95 200ms` 和 `平均模型调用成本降低 35%+` 只能作为待验证目标；
- 不应为了完成简历第五点自研一个完整评测平台，也不应重新执行前四点所有昂贵的 Provider 测试。

## Solution

MESGuard 将在现有领域评测器和运行 Observation 之上增加统一评测与性能优化层，而不是替换现有
实现。该层由三条主线构成：

1. **统一可靠性与可观测合同**：按业务操作选择 `strict`、`repair_then_fail` 或 `best_effort`，
   所有降级产生标准 Degradation Event；通过 Eino Callbacks 和既有运行记录生成 OpenTelemetry
   Span，并使用统一身份关联 Zap 日志、Agent Run、Model Call、Tool Call、Retrieval、Cache 和评测结果。
2. **Evidence Gate Early Exit**：复用现有 Evidence Gate，由模型提出结束意图，应用侧根据任务类型、
   证据充分性和引用绑定做确定性裁决；通过相同 Case 的 Baseline/Experiment 成对评测证明质量不下降后，
   才报告模型调用、Token 和延迟收益。
3. **企业知识问答语义答案缓存**：只缓存完全由当前 Global 知识支持的最终答案和引用。缓存查询先
   执行保守归一化后的 L1 精确匹配，再执行 L2 Embedding 语义匹配；命中后不进入 ReAct，而是直接
   提交正常 Assistant Message、引用和轻量 Run Observation。

缓存使用 PostgreSQL 中的单一 `Global Knowledge Generation` 保证知识更新后的正确失效。现有逻辑
文档发布新版本、撤回当前文档或改变 Global 可见知识集合时，在同一事务中递增 Generation。缓存记录
必须同时满足 Generation、TTL、结构和 Eligibility Policy，无法确认 Generation 时一律按未命中处理。

语义答案缓存通过稳定接口支持 PostgreSQL + pgvector 与 Redis Stack 两种 Provider，但单次部署只启用
一个后端，不双写。两种后端使用相同固定集分别运行 A/B，比较正确性、P50/P95、资源消耗和运维复杂度。

现有评测资产先经过审计，标记为 `reusable`、`recomputed`、`retest_needed` 或 `obsolete`。本地 Fixture、
历史 Observation 重放和指标重算优先执行；只有实现合同确实变化或缺少关键指标时，才在明确 Case、调用
次数和 Token 上限后选择性调用真实 Provider。

## User Stories

1. As a knowledge assistant user, I want an equivalent question to reuse a validated answer when global knowledge has not changed, so that I receive a faster response without unnecessary model calls.
2. As a knowledge assistant user, I want a cache hit to appear as a normal assistant message with citations, so that the conversation remains complete and understandable.
3. As a knowledge assistant user, I want cache failures to fall back to normal RAG, so that an optimization outage does not make knowledge question answering unavailable.
4. As a knowledge assistant user, I want questions depending on prior conversation, attachments, personal knowledge or current web information to bypass the cache, so that I do not receive an answer from the wrong context.
5. As a knowledge assistant user, I want answers invalidated immediately after relevant global knowledge changes, so that an old policy or technical document is not reused as current truth.
6. As a knowledge assistant user, I want citations from a cached answer to remain visible and traceable, so that I can verify the source in the same way as a newly generated answer.
7. As a diagnosis user, I want the Agent to stop when sufficient evidence already supports a conclusion, so that diagnosis finishes sooner without wasting model calls.
8. As a diagnosis user, I want the application Evidence Gate rather than an unconstrained model decision to authorize Early Exit, so that stopping behavior remains auditable and predictable.
9. As a diagnosis user, I want an evidence-insufficient run to continue gathering allowed evidence within its budget, so that Early Exit does not create shallow reports.
10. As an administrator, I want publishing a new current global document version to invalidate stale cached answers, so that knowledge changes take effect without manually deleting individual cache keys.
11. As an administrator, I want failed or still-processing document versions not to invalidate valid answers, so that an unsuccessful re-index does not disrupt the current knowledge view.
12. As an administrator, I want cache TTL, capacity, timeout and active Provider to be configurable, so that the deployment can be tuned without changing domain code.
13. As an administrator, I want Personal knowledge to remain outside the first semantic-answer-cache release, so that cross-user isolation risks are avoided while Personal remains a placeholder capability.
14. As an operator, I want every degradation to have a stable operation, reason code and trace identity, so that I can distinguish an expected fallback from silent data loss.
15. As an operator, I want authorization, TaskScope, SQL safety checks and state commits to fail closed, so that a reliability feature cannot weaken security or consistency.
16. As an operator, I want Query Rewrite, Rerank, semantic cache and telemetry export to degrade to a safe base path, so that optional enhancements do not become availability dependencies.
17. As an operator, I want structured model outputs to receive only a bounded repair attempt, so that malformed output does not cause unlimited retries or unpredictable cost.
18. As an operator, I want OCR and VLM processing failures to leave a retryable task instead of publishing incomplete knowledge as complete, so that document quality remains explicit.
19. As an operator, I want OpenTelemetry traces to correlate with existing Zap logs through stable identifiers, so that I can investigate a run without replacing the business logging system.
20. As an operator, I want Langfuse to be an optional development and evaluation profile, so that local analysis is available without making it a production fact source.
21. As a privacy-conscious operator, I want telemetry to omit raw prompts, answers and evidence by default, so that observability does not duplicate sensitive business data.
22. As a developer, I want model Profiles to be selected by business operation, so that main chat, summary, query rewrite and offline judge models can be changed independently.
23. As a developer, I want model selection to remain statically configured rather than dynamically price-routed, so that behavior and quality are reproducible.
24. As a developer, I want Token Usage recorded by Provider, model and operation, so that cost can be evaluated externally using the applicable platform pricing.
25. As a developer, I want PostgreSQL and Redis Stack cache implementations behind one interface, so that the active implementation can change without altering conversation or Agent logic.
26. As a developer, I want only one cache Provider active at a time, so that the system avoids dual-write consistency and cache-migration complexity.
27. As a developer, I want the semantic cache to be disposable and rebuildable, so that it does not become a second source of conversational or knowledge truth.
28. As a developer, I want conservative question normalization, so that versions, numbers, dates and negations are not accidentally merged.
29. As a developer, I want cache eligibility determined by application facts rather than another LLM call, so that caching does not reintroduce model latency and cost.
30. As an evaluator, I want existing RAG, Tool, Text-to-SQL, diagnosis, document and context-governance observations inventoried before new tests run, so that previous work is reused honestly.
31. As an evaluator, I want historical results clearly classified as reusable, recomputed, retest-needed or obsolete, so that stale measurements are not presented as current evidence.
32. As an evaluator, I want a versioned Evaluation Ledger to aggregate existing observations, so that metric names and run identities are consistent across domains.
33. As an evaluator, I want Provider-free validation and historical replay to run by default, so that ordinary development does not create unexpected cloud charges.
34. As an evaluator, I want real Provider execution to require an explicit flag and a printed call and Token budget, so that paid tests remain bounded and reviewable.
35. As an evaluator, I want RAG quality measured by Recall@K, MRR, Context Precision and citation correctness, so that retrieval presence, ranking and evidence quality are not conflated.
36. As an evaluator, I want Tool selection measured by required capability coverage, forbidden-tool rate and success rate rather than an exact Tool sequence, so that multiple valid investigation paths remain acceptable.
37. As an evaluator, I want Text-to-SQL measured by normalized execution results on a read-only fixture, so that semantically equivalent SQL is not marked wrong because of textual differences.
38. As an evaluator, I want comprehensive diagnosis measured by completion, conclusion, evidence, unsupported claims, latency, calls and Token, so that a single Judge score cannot hide failures.
39. As an evaluator, I want cache threshold calibration separated from holdout acceptance, so that reported Precision is not the result of tuning on the test set.
40. As an evaluator, I want difficult negative query pairs with overlapping vocabulary, so that cache safety is tested against realistic false-hit risks.
41. As an evaluator, I want human reusable/not-reusable labels to remain the primary cache truth, so that an LLM Judge does not grade its own assumptions.
42. As an evaluator, I want Early Exit evaluated in paired runs on identical diagnosis cases, so that changes in model, Prompt, tools or fixtures do not masquerade as optimization gains.
43. As an evaluator, I want every regressed diagnosis Case listed for human review, so that average quality cannot hide a severe wrong conclusion.
44. As a resume owner, I want only reproducible current results used in the project description, so that interview claims can be explained from the test design to the raw measurements.
45. As a resume owner, I want targets such as 200 ms or 35 percent treated as provisional until measured, so that implementation is not distorted to satisfy an invented number.

## Implementation Decisions

### Existing Assets and Ownership

- Existing domain evaluators remain owned by their current domains. RAG retrieval, Tool selection, Text-to-SQL,
  conversation answer quality, comprehensive Agent runs, document processing and context governance do not move into
  a new monolithic evaluation package.
- A versioned Evaluation Ledger defines shared run identity, variant, model identity, operation identity, Usage,
  latency, outcome and degradation fields. Domain-specific observations keep their own Gold and scoring fields.
- Existing raw observations and reports are historical evidence. They are never silently rewritten to look like a
  current run.
- Evaluation inventory records one status for every reusable asset: `reusable`, `recomputed`, `retest_needed` or
  `obsolete`, plus the reason and the implementation/configuration fingerprint used for the decision.

### Runtime Identity and Observability

- Runtime records use `traceId`, `runId`, `conversationId`, optional `taskId` and `toolCallId` as correlation fields.
- OpenTelemetry is the portable instrumentation contract. Agent Run, Model Call, Tool Call, Retrieval, Cache and
  Degradation are represented as spans or span events at their existing orchestration boundaries.
- Existing Zap logs remain the business and operational log channel. OTel does not replace normal application logs.
- Langfuse is an optional self-hosted development/evaluation backend connected through an observability deployment
  profile. MESGuard correctness and availability do not depend on Langfuse.
- Raw Prompt, answer, Tool payload and evidence content are disabled in telemetry by default. Development-only sampled
  content capture requires explicit configuration. PostgreSQL conversation and diagnosis records remain the fact source.
- Telemetry export is `best_effort`; export failure produces bounded local diagnostics and never changes a business result.

### Model Profiles and Usage Accounting

- Model selection is statically configured per business operation. Main Agent chat, conversation summary, Query Rewrite
  and offline LLM Judge may use different named Profiles.
- The system does not automatically choose a Provider based on live price or inferred task difficulty.
- Runtime Usage is recorded as returned by the Provider and split by Provider, model and operation. Prompt, Completion,
  Cached and Reasoning Token are distinct fields when the Provider reports them.
- The unified runtime and Evaluation Ledger do not maintain model prices or compute CNY cost. Monetary evaluation is an
  external interpretation of recorded Token and request counts using a separately reviewed price snapshot.
- Token from heterogeneous models may be aggregated for request-level operational totals, but reports must preserve the
  per-model and per-operation breakdown.

### Unified Resilience Protocol

- Every guarded operation declares one of three policies:
  - `strict`: failure terminates the operation and cannot be bypassed;
  - `repair_then_fail`: one bounded repair/retry path is allowed, then the operation fails explicitly;
  - `best_effort`: failure emits a Degradation Event and falls back to a defined base path.
- Authorization, TaskScope, SQL safety validation, state commits and external side-effect commands are `strict`.
- Strict structured outputs such as conversation summaries, diagnosis report contracts and critical citation structures
  are `repair_then_fail` unless their existing contract requires immediate failure.
- Query Rewrite, Rerank, semantic answer cache and telemetry export are `best_effort`.
- Embedding failure in online RAG may degrade to FTS. If no remaining channel can provide sufficient evidence, the run
  reports evidence insufficiency rather than fabricating an answer.
- OCR/VLM processing uses bounded retry and a retryable task outcome; it must not publish an incomplete version as complete.
- Read-only Tool failure is returned as a structured Tool error so the Agent may choose another authorized source. Budget
  exhaustion with insufficient evidence produces an explicit partial/evidence-insufficient outcome.
- Each Degradation Event contains operation, configured policy, fallback path, reason code, Provider/model identity when
  applicable, trace/run identity and duration. Errors are not silently swallowed.

### Evidence Gate Early Exit

- The existing Evidence Gate is the only Early Exit decision authority; no second stopping state machine is introduced.
- The model may indicate that it has enough evidence, but the application validates evidence sufficiency, citation binding,
  task-specific report requirements, hard safety conditions and remaining mandatory steps.
- Early Exit is independently configurable and can be disabled for a Baseline run without changing the model, Prompt,
  Tool contract, budget or fixture.
- Early Exit acceptance requires quality non-regression: completion and aggregate conclusion/citation correctness cannot
  decrease, no new high-severity wrong conclusion may appear, and every regressed Case must be listed for review.

### Semantic Answer Cache Eligibility

- The first release serves enterprise Global knowledge only. Personal knowledge remains outside the cache path.
- Diagnosis, Web Search, attachment-dependent requests, conversation-dependent requests, current/latest/version-sensitive
  requests, degraded RAG, evidence-insufficient responses, refusal responses and responses without valid citations are not
  cacheable.
- A response becomes cacheable only after the normal Assistant Message and citations commit successfully.
- Eligibility is determined from runtime facts: the run executed enterprise knowledge RAG, the Evidence Gate accepted the
  evidence, every cited source belongs to current Global knowledge, and no excluded dependency or degraded channel was used.
- Cache writes happen after the business commit and are `best_effort`. A failed cache write cannot roll back a valid answer.
- Cached answers are immutable cache records. A replacement creates a new record or overwrites only through the selected
  cache Provider's normal cache semantics; conversation history remains append-only.

### Cache Lookup and Response Flow

- Cache lookup occurs after the user message is persisted and before the first Agent model call.
- L1 uses conservative normalized text and SHA-256 exact lookup. Normalization performs Unicode normalization, trims and
  folds whitespace, normalizes safe punctuation forms and folds English letter case. It does not delete numbers, dates,
  versions, negations or semantic punctuation.
- L2 embeds the independent user question and performs semantic nearest-neighbor lookup using the active Embedding Profile.
- L2 candidates must pass the calibrated similarity threshold and deterministic conflict checks for entity, number, date,
  version, negation and intent differences.
- Embedding unavailability disables L2 for that request but preserves L1. Cache lookup failure or timeout falls through to
  normal RAG.
- A valid hit directly commits the cached answer and citations as a normal Assistant Message. It also creates a lightweight
  Run Observation with execution path `semantic_cache_hit`, zero model calls, zero Tool calls, cache layer and source run ID.
- Cached answers are returned verbatim with their validated citations; they are not sent through another model for rewriting.
- Cache records are not discovered by scanning conversation history. Conversation history records what happened in one
  Conversation; semantic answer cache is a cross-conversation, disposable performance projection.

### Knowledge Generation and Invalidation

- PostgreSQL stores the authoritative Global Knowledge Generation as a monotonically increasing integer.
- A cache record stores the Generation under which its answer was created. A mismatch is a cache miss.
- Generation increments in the same PostgreSQL transaction that changes the current retrievable Global knowledge set.
- Publishing a new current document version, withdrawing/deleting a current Global document, changing Global visibility,
  or republishing repaired current chunks/indexes increments Generation.
- Uploading, parsing, failed indexing, deleting a draft or changing display metadata does not increment Generation.
- A document update uploads a new version for the same logical document. The old current version remains searchable until
  the new version completes and publishes; files and historical versions are not overwritten in place.
- Cache hit validation reads the authoritative Generation from PostgreSQL. If it cannot be read, the request bypasses the
  cache. No Redis-maintained Generation or distributed transaction is introduced in the first release.
- The first release uses coarse global invalidation. Per-document dependency invalidation is an optional future optimization.

### Cache Provider Contract

- A stable Semantic Answer Cache interface owns lookup, put and bounded maintenance operations. Conversation and knowledge
  application services depend on the interface, not on pgvector or Redis commands.
- Configuration selects exactly one active Provider: PostgreSQL or Redis Stack. The system does not dual-write or migrate
  cache records between Providers.
- PostgreSQL + pgvector is the initial reference implementation because the dependency already exists. Redis Stack is an
  alternative implementation evaluated through the same contract and fixed dataset.
- Cache data is disposable and may be cleared when Provider, schema, Embedding Profile or normalization version changes.
- Experimental defaults are configurable: 24-hour TTL with jitter, 1000 records per isolation scope, 16 KiB answer limit,
  at most 8 citations, 100 ms lookup timeout and 200 ms write timeout. These values are starting points, not resume claims.
- The first release does not implement process-local singleflight, Redis distributed locks or another lease state machine.
  Concurrent duplicate misses are observed. A mitigation is added only if measured duplicate load justifies it.

### Evaluation Dataset and Metrics

- The semantic cache fixed set contains 120 reviewed query pairs:
  - 40 reusable semantic paraphrases;
  - 40 difficult negatives with overlapping terms but different constraints or answers;
  - 20 temporal/version-sensitive negatives;
  - 20 conversation, attachment, Personal or Web-dependent negatives.
- Each pair has a primary human label `reusable=true|false` and an optional rationale. Optional LLM Judge output is auxiliary
  and cannot override the reviewed label.
- The dataset is stratified with a fixed seed into 80 Calibration pairs and 40 Holdout pairs. Calibration selects the
  Embedding-Profile-specific threshold; Holdout is used once for acceptance and resume evidence.
- Threshold selection first requires Cache Precision of at least 98 percent on the applicable acceptance set, then chooses
  the highest Recall among qualifying thresholds. If the threshold cannot meet the precision gate, L2 remains disabled and
  only L1 exact caching is released.
- Cache Precision is correct reusable hits divided by all hits. Cache Recall is reusable pairs successfully hit divided by
  all reusable pairs. Hit rate is reported separately and is never used as a correctness metric.
- `cache_lookup_duration` measures cache lookup only. `cache_hit_response_duration` measures server receipt through cache
  validation and Assistant Message/citation commit. Resume P95 refers to the latter and excludes browser network/rendering.
- RAG metrics use Recall@K, MRR, Context Precision@K and Citation Correctness. `MPR` is not a supported metric name.
- Tool metrics use Required Capability Coverage, Forbidden Tool Rate, Tool Call Success Rate and average Tool Calls; exact
  Tool sequence is not a correctness contract.
- Text-to-SQL uses SQL Execution Accuracy on a fixed read-only SQL Server fixture after result-set normalization. SQL Exact
  Match is diagnostic only. QueryGuard safety and high-risk operation blocking are reported separately.
- Comprehensive diagnosis reports Task Completion, Conclusion Correctness, Citation/Evidence Correctness, Unsupported Claim
  Rate, Forbidden Tool Rate, model/Tool calls, P50/P95 latency, per-model Token Usage and Degradation Rate.
- Early Exit initially targets 30 paired diagnosis Cases when enough reviewed Cases exist. It uses all available Cases rather
  than a training split because Early Exit has no learned threshold; skipped and failed pairs remain visible.

### Evaluation Execution and Cost Guard

- Local validation, Fixture tests, raw Observation validation, historical replay and deterministic recomputation are the
  default execution paths and make zero Provider calls.
- Provider-backed runs require an explicit execution flag. Before execution they print Case count, maximum model calls,
  maximum Embedding/Rerank requests and Token upper bounds.
- Paid evaluations run with bounded concurrency, timeout and circuit breaking. A failed Provider does not trigger unbounded
  retries or silently expand the dataset.
- Baseline and Experiment must share model identity, reasoning settings, Prompt contract, Tool contract, fixture, knowledge
  Generation and execution budgets except for the single variable under test.
- Reports preserve raw per-Case observations, failed/skipped reasons, aggregate metrics and configuration fingerprints.
- Existing monetary estimates in historical evaluation documents remain historical. The new unified runtime ledger reports
  Token and request counts, not CNY.

## Testing Decisions

### Testing Principles

- Tests assert externally visible behavior and durable observations rather than private helper calls or exact internal order.
- Existing high-level seams are preferred. New Provider adapters conform to shared contracts instead of creating separate
  feature test frameworks.
- Production safety and cache correctness are tested before latency optimization.
- Provider-free tests are the default. Real Provider tests are bounded acceptance checks, not ordinary unit-test dependencies.
- Fixed datasets, Gold labels, Prompt/model identity, Knowledge Generation and configuration fingerprints are versioned so
  that a result can be reproduced and compared honestly.

### Conversation and Agent Run Seam

- Test from a persisted user message through answer commit.
- Verify a cache miss executes normal RAG and a valid cache hit commits a normal Assistant Message and citations with zero
  model and Tool calls.
- Verify exact and semantic hit paths, TTL expiration, Generation mismatch, non-cacheable context, corrupted cache records,
  cache timeout, Embedding failure and Generation-read failure.
- Verify cache hit observations contain the execution path, cache layer and source run identity without fabricating a ReAct trace.
- Use an asynchronous conversation integration test for the complete API/Worker persistence path and focused application-level
  tests for cache policy and Provider contracts.

### Evidence Orchestrator Seam

- Run identical diagnosis Cases with Early Exit disabled and enabled.
- Assert task/report outcomes, evidence and citation quality, model calls, Token and latency from the final Run Observation.
- Verify evidence-insufficient Cases continue within budget and evidence-sufficient Cases may terminate.
- Verify security gates, required task steps and high-severity regressions cannot be hidden by aggregate gains.
- Reuse the existing Evidence Gate, Agent paired evaluation and diagnosis fixtures as prior art.

### Versioned Evaluation Ledger Seam

- Feed existing domain observations into the shared ledger and assert deterministic aggregation without Provider calls.
- Verify legacy metric names are mapped explicitly, invalid records fail validation and no missing Usage is fabricated as zero.
- Verify inventory classification and recomputation preserve raw observations and distinguish historical from current runs.
- Verify Trace/Run correlation connects runtime spans, Zap fields and evaluation records without requiring Langfuse availability.
- Reuse existing RAG retrieval, advanced retrieval, Tool selection, Text-to-SQL, conversation quality, Agent paired and context
  governance evaluators as prior art.

### Cache Provider Contract and Integration Tests

- Run the same contract suite against PostgreSQL and Redis Stack implementations: exact lookup, semantic ranking, Generation,
  TTL, capacity, size limits, malformed records and cancellation/timeouts.
- Use real PostgreSQL/pgvector and Redis Stack only in tagged integration tests. Unit tests use deterministic fakes at the
  Semantic Answer Cache interface.
- A/B compares Providers in separate runs against the same 120-pair dataset; no dual-write consistency test is required.

### Resilience and Observability Tests

- Inject representative failures at the operation boundary and assert the selected policy, final business outcome and one
  standard Degradation Event.
- Verify `strict` operations cannot continue, `repair_then_fail` is bounded, and `best_effort` follows its documented base path.
- Verify telemetry exporter failure does not change the answer or report and does not log raw sensitive content by default.
- Verify operation, Provider/model and trace identities are present where applicable.

### Acceptance Gates

- L2 cache release requires the reviewed Precision gate; otherwise the system ships with L1 only.
- Cache-hit P95 and model-call reduction are reported from Holdout or a separately versioned fixed traffic replay, not from
  Calibration data.
- Early Exit gains are accepted only when completion, conclusion and citation quality do not regress and no new high-severity
  wrong conclusion appears.
- Every resume metric must identify dataset version, sample count, Baseline, Experiment, model Profile and measurement boundary.
- Full repository unit tests, relevant race tests, database/cache integration tests and configuration validation must pass
  before the feature is marked complete.

## Out of Scope

- Building a new full-featured evaluation or observability platform;
- Replacing Zap business logs with OpenTelemetry or Langfuse;
- Dynamic price-aware or difficulty-aware model routing;
- Runtime Provider hot switching without restart and acceptance testing;
- Calculating or persisting CNY model cost in the unified runtime ledger;
- Caching diagnosis reports, Web Search answers, attachment-dependent answers or conversation-dependent transformations;
- Personal-knowledge semantic answer caching or global/personal two-level cache;
- Using an online LLM Judge on every cache hit;
- Redis distributed locks, distributed singleflight, cache leases or a new coordination state machine;
- Dual-writing PostgreSQL and Redis Stack cache Providers or migrating cache entries between them;
- Per-document dependency invalidation in the first release;
- Scanning all conversation history to discover reusable answers;
- Reimplementing existing RAG, Tool, Text-to-SQL, diagnosis, ingestion or context-governance evaluators;
- Re-running all historical Provider tests merely to populate a unified dashboard;
- Training a new Embedding, Rerank or classification model;
- Guaranteeing the provisional 200 ms P95 or 35 percent cost-reduction targets before measurement;
- Frontend dashboards beyond the data contracts needed for later integration.

## Further Notes

### Existing Evaluation Baseline

The repository already contains fixed or paired evaluation paths for Tool selection, comprehensive Agent runs,
Text-to-SQL and SQL safety, FTS/Vector/RRF retrieval, Advanced RAG variants, conversation answer/citation quality,
document ingestion and visual processing, and context governance. M4 treats these as prior art and input assets.
Their historical results remain scoped to the implementation, model, Prompt, data and date recorded at the time.

Ticket 01 已建立 `evaluation_inventory_v1`，逐项登记当前 19 个评测入口，并使用
`reusable / recomputed / retest_needed / obsolete` 标记历史资产。首个 Tool Selection tracer bullet
以零 Provider 调用重放 45 Case、90 条历史 Observation，生成绑定 Dataset/Observation SHA-256 的
`evaluation_ledger_v1`；领域 Summary 保持原始 wide `95.56%`、filtered `97.78%` 和 45 个 paired Case。
该结果只证明历史数据可审计重放，不代表当前 Tool Contract 已复测，清单仍将其标为 `retest_needed`。

Ticket 02 已建立最小统一 Resilience 合同和企业知识检索纵向样板。`strict`、
`repair_then_fail`、`best_effort` 分别固定为一次后失败、最多两次后失败、一次后进入调用方声明的
基础路径；未引入没有真实调用方的通用执行器或重试状态机。本纵向样板只在知识检索真实接入
`best_effort`，另外两种策略将在 Ticket 03 的具体安全校验和结构化输出边界接入。Query Rewrite、Embedding/Vector、FTS 和 Rerank 的实际回退会
产生一条带 operation、policy、fallback、reason code、Run/Trace、Provider/Model 和耗时的标准
Degradation Event，并进入 `search_knowledge` Tool 结果和 Zap Observer。双路正常零命中不产生事件，
部分子查询失败产生 `partial_failure -> available_results` 事件；双路基础设施全部失败则向 Agent 返回
不含底层错误的结构化 `all_channels_failed`，不能伪装成空检索结果。

Ticket 03 已把统一语义接入真实 Agent 边界。Tool 注册必须显式声明失败策略：创建诊断任务等副作用命令为
`strict`，普通只读 Tool 为 `best_effort`；TaskScope、授权和 SQL Query Guard 仍在回退之前 fail closed。
只读 Tool 的失败会先分类：安全/授权错误严格传播，参数错误结构化为不可重试拒绝，只有明确标记的暂时依赖故障
才返回脱敏 Tool Error 并产生一次 `best_effort -> agent_selects_alternative_source` Degradation Event；诊断任务 ID
或会话 User Message ID 作为 Run ID。SQL Query Guard、Catalog 授权和数据源授权保持 strict，数据库暂时不可用
才允许降级。Evidence Orchestrator 和会话 Citation Repairer 必须显式配置 `repair_then_fail`；完整诊断报告合同最多
初次生成加一次修复，JSON、字段或 Evidence 绑定任一仍失败时只能输出 `partial/inconclusive`，引用修复耗尽后保持
`evidence_insufficient`，都不会接受未经校验的自由文本。知识 Worker 沿用已有 lease/attempt 状态机：OCR/VLM
暂时失败进入 retry，且不会调用 Parsed Result/Complete 发布路径；没有为统一协议另建业务状态机。

### Resume Update Rule

The final fifth resume point should describe the unified protocol and the two measured optimizations. A valid final form is:

> 基于 Eino Callbacks 与 OpenTelemetry 统一采集模型调用、工具轨迹、Token、延迟及降级事件，整合
> RAG、Tool 选择、Text-to-SQL 与综合诊断固定评测集；面向企业知识问答实现 Generation 驱动的
> 语义答案缓存，并结合 Evidence Gate Early Exit，在 Holdout 集上达到 X% 缓存复用准确率，使命中
> 请求 P95 降至 X ms，综合流量平均模型调用次数下降 X%。

`X` 只能由本规格的当前固定集和接受门禁产生。若 Redis Stack、Early Exit 或 L2 语义匹配没有表现出
足够净收益，应如实保留 PostgreSQL/L1/基础 Evidence Gate 路径，并将未采用方案记录为消融结论。

### Delivery Order

1. 盘点并分类现有评测资产，定义统一 Evaluation Ledger 和指标词汇；
2. 落地统一 Resilience Policy 与 Degradation Event；
3. 接入 OTel，并提供可选 Langfuse 部署 Profile；
4. 为现有 Evidence Gate 增加 Early Exit Baseline/Experiment 评测开关；
5. 定义 Semantic Answer Cache、Eligibility Policy、Generation 和 PostgreSQL Provider；
6. 增加 Redis Stack Provider，执行相同合同与 A/B；
7. 建立并人工复核 120 组缓存数据，完成 Calibration/Holdout；
8. 选择性复测受实现变化影响的既有评测，汇总真实指标；
9. 更新 Roadmap、设计文档和简历第五点。
