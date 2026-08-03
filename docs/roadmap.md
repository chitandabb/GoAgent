# MESGuard Current Progress

This file tracks the repository's current implementation state. Target
milestones and product acceptance boundaries are defined in the product,
domain, database, API, and system-architecture design documents. Agent
execution order and acceptance gates are defined in
[`design/agent-implementation-plan.md`](design/agent-implementation-plan.md).

## Current Stage: P7 Admin Recovery Complete, Failure Drills Next

- [x] `cmd/internal` project layout.
- [x] Typed TOML and `.env` configuration.
- [x] Manual dependency injection in `bootstrap`.
- [x] Gin Router and `/healthz` endpoint.
- [x] Application error-code enum and standard error type.
- [x] Unified success/error response structure.
- [x] Global request ID, error handling, panic recovery, and 404/405 responses.
- [x] Structured Zap logging, request-context fields, and optional file rotation.
- [x] Graceful HTTP, PostgreSQL, and Redis shutdown.
- [x] React workbench prototype in `web/` running on local mock data only; see
      [`design/frontend.md`](design/frontend.md). Authentication is connected to
      the backend; business pages remain Mock-driven even though external-case,
      diagnosis-task, event-history, cancellation, and review APIs now exist.

## M0: Before Business Code

- [x] Request DTO binding and validation conventions.
- [x] Configuration tests and startup failure tests.
- [x] Add the local-account authentication skeleton and analyst/admin roles.
- [x] Define database migration command and transaction conventions.

M0 is complete. PostgreSQL is a critical dependency. Redis, the ERP SQL Server,
and later MinIO are degradable dependencies: their failure disables only the
affected capability without preventing authentication and historical-data APIs
from starting.

## Completed Execution Slice: M1-A1 Backend

This Codex task owns backend work only. The existing uncommitted `web/`
changes belong to a separate frontend task and must not be modified, staged,
or committed here. Notify the user when the backend API is ready for frontend
integration.

### Scope

1. Finish the M0 Bootstrap startup wiring and failure test.
2. Add typed ERP SQL Server configuration, an independent connection pool,
   query timeouts, and a dedicated `case_reader` account.
3. Expand the synthetic ERP schema into tickets, production context, external
   attachment metadata, and a read-only integration view with four fault
   scenarios.
4. Define a stable Go `ExternalCase` model with normalized status/priority,
   customer, product, workpiece, batch, production-line, equipment, safe
   business-database alias, allowlisted attributes, and attachment metadata.
5. Use validated TOML field mapping against explicitly configured relations;
   do not allow arbitrary SQL in configuration.
6. Implement external-case list/detail APIs through Handler -> UseCase ->
   `ExternalCaseReader` -> SQL Server Adapter.
7. Compute `sourceFingerprint` from canonicalized diagnosis input only. It
   includes allowlisted fields and attachment identity/version/hash, but not
   read time, query duration, or display-only formatting.
8. Add PostgreSQL `data_sources` and `external_cases` identity records without
   copying complete ERP ticket content.
9. Keep default tests independent of Docker. Add opt-in SQL Server integration
   tests that prove the reader account cannot execute writes or DDL.

### Confirmed Boundaries

- The company ERP ticket database is the case source. Customer MES databases
  are separate diagnosis evidence sources.
- Production MES databases are strictly read-only.
- The product replica is a delayed copy of production and also provides the
  bounded optimization-test environment. Experiments may create objects only
  in a task-specific area, must not modify original objects, and must be
  cleaned or restored from a recorded baseline.
- Product-replica evidence records `last_synced_at` / `data_as_of`; unknown
  freshness must be reported as a limitation.
- Database/table access is configured globally by administrators, not per
  employee. Bulk schema/prefix/search rules produce a reviewed, explicit,
  immutable Catalog version; runtime wildcard rules must not silently grant
  access to new tables.
- Full Schema Catalog scanning and publishing is deferred to M1-D.
- ERP files are assumed to live in MinIO. Local development uses one MinIO
  instance with isolated ERP and MESGuard buckets. M1-A2 copies ERP objects
  into the MESGuard bucket when diagnosis starts.
- `CaseSnapshot` construction and hashing may be prepared in M1-A1, but
  persistence waits for M1-B so CaseSnapshot, DiagnosisTask, first TaskEvent,
  and OutboxEvent are written in one PostgreSQL transaction.
- M1-A1 does not implement MinIO transfer, RabbitMQ, Worker, Eino, Text-to-SQL,
  or stored-procedure experiments.

### Acceptance

- A logged-in user can list and read real synthetic ERP tickets through the
  backend API.
- Mapping, pagination, filtering, normalization, truncation, and fingerprint
  behavior are covered by tests.
- ERP SQL Server downtime degrades ticket endpoints instead of stopping the
  whole API.
- The SQL Server reader account is demonstrably unable to write.
- `go test ./...`, `go vet ./...`, and the opt-in SQL Server integration suite
  pass before frontend handoff.

All acceptance items above were verified on 2026-07-29. The same API process
continued serving authentication while SQL Server was stopped, returned 503
for ticket queries, and recovered ticket queries after SQL Server restarted.

## Resume-Driven Slice: Skill Orchestration Foundation

The project is now prioritizing resume-demonstrable Agent capabilities while
preserving authorization, read-only data access, migrations, critical tests,
recoverability, and structured logs.

Implemented in the current working slice:

- Eino `v0.9.13` ADK `ChatModelAgent`, created independently for every Run;
- native `SKILL.md` packages for `ticket-diagnosis` and `code-investigation`,
  with detailed evidence rules split into on-demand `references/*.md`;
- a data-minimized `read_external_case` Eino Tool;
- official GitHub MCP Streamable HTTP client configuration using read-only mode
  and an exact six-tool code-only allowlist, including repository-tree candidate
  discovery;
- application-side path, query, pagination, repository-identifier, tree prefix,
  ref, and
  commit-SHA validation before GitHub MCP calls, while leaving repository and
  branch scope to the GitHub credential;
- StepFun Step Plan `step-3.7-flash` configuration and Eino OpenAI-compatible
  ToolCallingChatModel factory, including local protocol and usage tests;
- a live StepFun capability probe on 2026-07-30 that returned the required Tool
  Call and provider usage without executing a business tool;
- bootstrap wiring that validates native Skill packages and degrades model/GitHub MCP
  failures without stopping authentication and ticket browsing;
- per-run Eino Callback aggregation across all non-streaming ReAct ChatModel
  calls, including prompt, completion, total, cached, and reasoning Tokens;
- a live synthetic-ticket ReAct run on 2026-07-30 that made two model calls,
  executed only `read_external_case`, and returned aggregated provider usage;
- evaluation aggregation for routing accuracy, first-tool accuracy,
  out-of-allowlist calls, and provider-reported input Token reduction;
- a production Runner path that preloads the context-selected entry Skill and
  progressively loads other Skills in the same ADK loop;
- deterministic production-Runner tests for non-streaming events, Context
  cancellation, maximum iterations, Tool order, and non-duplicated usage;
- live non-streaming and streaming StepFun ADK runs on 2026-07-30. Both made
  three model calls in `skill -> read_external_case` order and reported 4271
  total Tokens; cached Tokens were 2048 and 2944 respectively;
- streaming ChatModel Callback aggregation for provider prompt, completion,
  total, cached, and reasoning Tokens;
- a project-owned read-only Eino filesystem Backend that maps local packages to
  `/skills`, supports Windows slash/backslash paths, and rejects writes, path
  traversal, symbolic links, Windows junction/reparse points, non-regular
  files, and oversized resources;
- a narrow `read_skill_reference` Tool that reads only one-level
  `references/*.md` resources with line and byte caps; no generic filesystem,
  Shell, or Skill script execution is exposed;
- an immutable per-run `TaskScope` covering user, analyst/admin role, diagnosis
  versus knowledge task type, authorized data sources, production/product
  replica safety mode, task-level allowed capabilities, and currently available
  dependencies; task authorization and runtime dependency health are independent;
- a startup-built, read-only `ToolCatalog` that rejects duplicate names and
  invalid policies, then filters Tools by the complete TaskScope without
  mutating shared Agent state;
- an Eino ADK `BeforeAgent` authorization Middleware that replaces static
  business Tools with the current run's authorized set before Tool Schemas are
  passed to the model;
- concurrent ADK coverage across 60 isolated per-run Agent instances proving
  that analyst case access, admin bounded-LAB access, and production read-only
  access do not leak Tool visibility between runs; GitHub dependency
  degradation removes only the GitHub Tool;
- a Windows GCC/race run proving the shared Catalog/Middleware path is clean;
  the same run exposed an Eino `v0.9.13` race when one ChatModelAgent instance
  was shared, so the production Runner now creates one Agent per Run;
- deterministic P3 coverage for
  `read_external_case -> skill(code-investigation) -> search_code -> final`,
  GitHub degradation, missing TaskScope, Tool result truncation, provider usage,
  Context cancellation, maximum iterations, and concurrent isolation;
- a live P3 production-Runner smoke run on 2026-07-31 that called only
  `read_external_case`, completed two model calls in 10.37 seconds, and reported
  4397 total Tokens including 1600 cached Tokens;
- removal of the legacy per-Skill ReAct executors, Graph/Handoff Dispatcher,
  `request_code_investigation`, Registry, and compatibility Skill loader;
- a thin Eino Evidence Gate Graph with deterministic structured-report checks,
  at most two Agent runs, Tool/Token/evidence/time budgets, partial-report
  fallback, and a frontend-safe investigation trace;
- file-configured production, evaluation-baseline, and report-contract Prompts
  loaded and cached at Agent startup, with a manually released `promptVersion`
  persisted for report and evaluation traceability;
- deterministic P4 coverage proving that invalid evidence triggers at most one
  supplemental run, budget exhaustion cannot start another Tool/Agent run, and
  cancellation is checked before Tool execution;
- a live P4 StepFun run on 2026-07-31 that completed one Agent run and three
  model calls, executed `read_external_case` and `read_skill_reference`, passed
  three evidence references, and produced an `inconclusive` report with low
  confidence. It reported 10434 Tokens and took 67.82 seconds; this is a protocol
  sample, not an effect metric;
- versioned `EvaluationCase`/`EvaluationObservation` contracts, paired
  baseline/experiment scoring, strict JSONL validation, and a local `dev-v1`
  sample command. The sample only verifies the scorer and is not a resume metric.
- a model-free `mesguard-github-search-eval` command with a bounded JSONL
  dataset for `search_code -> get_repository_tree -> get_file_contents`; it
  records search completeness, expected-path recall, candidate recall, fixed
  SHA file verification, candidate incompleteness, and fallback recovery.
- a native `sql-investigation` Skill plus narrowly scoped
  `get_database_object_definition` Tool for SQL Server procedures, views, and
  functions. The Tool accepts only simple schema/object identifiers, enforces
  administrator-configured `allowedSchemas`, uses a fixed parameterized
  metadata query, truncates returned definitions, and never exposes a database
  connection or arbitrary SQL to the model;
- SQL investigation runtime wiring that registers the Tool only when the SQL
  Server pool is available, requires a matching read-only TaskScope and the
  `sql_server` dependency, and otherwise keeps the Agent runtime available with
  an explicit degradation message;
- a PostgreSQL schema Catalog migration and `search_schema_catalog` Tool that
  only searches active data sources, published Catalog versions, and
  `queryable` entries through parameterized keywords;
- unit, authorization, bootstrap-degradation, race, and opt-in SQL Server
  integration coverage for the object-definition path. A live integration run
  still requires Docker Desktop and `MESGUARD_TEST_SQLSERVER_DSN`;
- a narrow, dependency-free T-SQL QueryGuard POC with token-aware comment,
  string, identifier and statement boundaries; it rejects dangerous or
  ambiguous structures, extracts table/function references, and is covered by
  table-driven, fuzz and live SQL Server CTE/UNION tests;
- a guarded `execute_readonly_query` Tool that rechecks QueryGuard object
  references against the published Catalog before executing, and enforces
  query timeout, maximum rows/result bytes, and a concurrency semaphore;
  unit, authorization, sanitization, and concurrency tests pass, while a real
  SQL Server + published-Catalog integration run now passes against the Docker
  PostgreSQL and SQL Server fixtures;
- a read-only `get_repository_tree` path for narrowing Code Search fallbacks by
  `tree_sha`, `path_filter`, and bounded recursion; the tree is candidate
  metadata only and does not replace fixed-SHA file evidence;
- successful fact-producing read-only Tool calls now produce bounded runtime
  `EvidenceItem` snapshots with a unique `evidenceRef`, source, hash, capture
  time, and truncation state; the Evidence Gate requires report references to
  resolve to those items.

Not yet implemented or verified:

- the complete Agent paired effectiveness evaluation and a larger repeated
  GitHub stability dataset. `agent-real-v1` covers one ticket-only case;
  `agent-real-v2` covers ticket-only, code investigation, and GitHub degraded
  cases. Both variants kept routing/first-tool/evidence coverage at 1/1 with
  zero forbidden calls; the code case stopped at the production 16000 Token
  budget on both sides and is recorded as `token_budget_exhausted`, while the
  degraded case did not call GitHub or SQL. These are boundary observations,
  not resume metrics. The `github-code-search-v2` run covers private C#,
  GoAgent, GoChat, public `Hello-World`, and an `in:file` query: Search complete
  2/6, two real incomplete responses recovered 2/2 through tree candidates and
  fixed-SHA file reads, tree-path recall 6/6, and known-path file verification
  6/6. Two GoChat Search calls hit the GitHub API rate limit while tree/file
  reads succeeded; this is recorded as a search error, not as missing code.
  Automatic local clone/search is intentionally still not implemented;
- Schema Catalog scanning/publishing, Query Store, and estimated-plan Tools;

The repeatable Worker model-failure recovery drill is complete: four exhausted
attempts reach `failed`, an admin requeues the original task, the fifth attempt
succeeds, exactly one report is persisted, and duplicate messages do not call
the model again. The fixed `tool-selection-v1` run is also complete: TaskScope
filtering reached 44/45 (`97.78%`) accuracy with zero invalid or out-of-whitelist
calls; Provider-calibrated Tool Schema prompt tokens fell from 126360 to 68136
(`46.08%`). Total prompt-token reduction was `42.63%` and remains a separate
metric. These artifacts are stored under `testdata/tool-selection-v1.*`.

The fixed SQL safety and Text-to-SQL evaluations for resume item 2 are also
complete. `sql-safety-v1` blocks 40/40 high-risk statements, accepts 12/12 safe
read-only controls, and matches 40/40 expected rejection reason codes.
`text-to-sql-v1` sends 20 fixed industrial-ticket requests through a single
StepFun-generated `execute_readonly_query` Tool Call, the production QueryGuard,
a fixed published-Catalog authorization, and the Docker SQL Server read-only
account. Deterministic column/result comparison reaches 20/20 (`100%`) with an
average duration of `1328.45ms`; no SQL string similarity or model self-scoring
is used. The datasets, observations, and summaries are stored under
`testdata/sql-safety-v1.*` and `testdata/text-to-sql-v1.*`.

The single-Agent inner loop is now the production Runner baseline. See
[`design/agent-implementation-plan.md`](design/agent-implementation-plan.md)
for the ordered migration and [`design/agent-orchestration.md`](design/agent-orchestration.md)
for the target boundaries.

## Next Slice

P0 through P5 的评测契约与统计基础已完成。P5 的 GitHub 工具级分层评测切片和首组四类
Agent baseline/experiment paired 样本也已完成；
P6 当前切片已完成对象定义读取、已发布
Schema Catalog 窄检索、QueryGuard、受限 `execute_readonly_query` Tool、真实跨数据库
联调和运行时 EvidenceItem；仍不等同于通用 Text-to-SQL。GitHub MCP 已完成凭据握手、
六个只读工具加载、私有 C# 仓库按 SHA 文件读取、提交追溯、仓库树候选读取，以及一次
完整 Code Search 返回的真实只读 smoke；Code Search 遇到 `incomplete_results=true` 时
运行时会有限重试并显式降级，已知路径仍可继续走文件/提交证据链。跨仓库/查询形态的
扩展稳定性评测已得到第一组真实结果：`github-code-search-v2` 的两条
`incomplete_results` 均恢复成功，但仍需在限流窗口恢复后重复 GoChat 样本并扩大查询集。
Agent paired CLI 已覆盖工单、代码调查、GitHub 降级、SQL 对象定义以及 SQL Catalog/
只读查询：代码调查在生产 16000 Token 预算下两边均 partial，SQL v3/v4 在临时 32000
Token 评测预算下完整通过；v4 的 Catalog 只存在于事务夹具并在结束时回滚。下一步是
拒答语义人工复核、重复运行和扩大固定数据集；在数据集稳定前不决定本地缓存，也不写入
简历目标值。Catalog 扫描/发布管理仍留到正式管理边界稳定后处理。

后端已开始 P7 正式任务链路：新增 `case_snapshots`、`diagnosis_tasks`、
`diagnosis_task_data_sources`、`task_events`、`outbox_events`、`diagnosis_reports` 和
`report_reviews` 迁移，实现了诊断任务创建/安全摘要查询，以及报告反馈 GET/POST 接口。
任务创建会在 PostgreSQL 一个事务内写入脱敏快照、pending 任务、首个 `task_created` 事件和
`diagnosis.execute` Outbox；同一用户同一幂等键支持重放和请求冲突。反馈仍是追加式人工复核
事实：`adopted` 可映射为👍、`rejected` 可映射为👎，管理员可查看但不能代替任务创建者提交。
任务控制面现已增加 TaskEvent JSON 游标查询和幂等取消命令；Worker 接入前的 PostgreSQL
Claim/续租/fencing 契约也已落地，覆盖活跃租约竞争、过期接管和旧 token 失效。Outbox Relay
已使用 `FOR UPDATE SKIP LOCKED`、有限租约、失败退避和 RabbitMQ Publisher Confirm 接通，
Compose 提供持久化 RabbitMQ 主交换机/诊断队列；PostgreSQL 到 RabbitMQ 的真实集成测试已验证
同一 `message_id` 发布并在 Confirm 后写入 `published_at`。Diagnosis Worker 现已接入严格信封
校验、`prefetch=1`、手动 ACK、30 秒/2 分钟/10 分钟 TTL 重试队列和最终死信队列；Worker
领取任务后使用创建时冻结的 CaseSnapshot、数据源和 `requestScope.allowedCapabilities` 构造
`TaskScope`，执行现有 ADK Agent + Evidence Gate。`case/code/sql` 能力白名单与 Runtime
探测的依赖健康状态分离，GitHub MCP 或 SQL Server 在线不会自动扩大任务 Tool 集合；Worker
定时续租，并在 fencing 条件下把 DiagnosisStep、ToolExecution、EvidenceItem、ReportEvidence、
正式 DiagnosisReport、TaskEvent 和 `succeeded` 终态作为一个 PostgreSQL 事务提交。
`cancel_requested -> cancelled`、临时失败释放重试、重试耗尽后的 `failed` 也已接通。
正式报告模型标识已规范为 `model_provider + model_id`，不再把同一配置值同时伪装成
模型名称和模型版本；`00009` 已在本地 PostgreSQL 应用并通过 Worker 完成事务与报告反馈
集成测试。Agent 三类基础 Prompt 已改为启动期文件配置，当前采用重启生效和人工维护的
`promptVersion`，不提前引入 Nacos、热更新或 Prompt 发布平台。
正式报告读取 API 已接入：任务创建者和管理员可读取 Worker 提交的业务/技术摘要、Token
用量、模型与 Prompt 追踪元数据及有序证据声明；接口不返回完整证据、原始 Prompt、模型
推理或原始 SQL。任务存在但尚无报告时返回 `40921`，缺失任务与越权分别返回 `404`/`403`。
领域、HTTP、仓储单测和真实 PostgreSQL Worker 写入后读回集成测试均已通过。管理员失败恢复
入口也已接入：仅允许恢复 `agent_execution_failed`，原子写入 `failed -> pending`、
`task_requeued`、恢复审计并重开原 Outbox；当前仍缺少前端 SSE 消费/复核工作台、
Worker 进程崩溃演练仍待补充；模型重试耗尽、管理员恢复、恢复后成功和重复消息幂等已通过
真实 PostgreSQL 演练。固定 45 条 Tool 选择评测已形成简历指标，单次 Worker smoke 和小样本
端到端 observation 仍不能冒充完整诊断效果指标。

The DiagnosisTask/Outbox/Worker contract and resume item 2 SQL evaluation are
now stable enough to start resume item 3. The next active backend slice is a
minimal verifiable ingestion/retrieval path for mixed documents, MinIO,
PostgreSQL FTS, and pgvector. M2-A1 now provides `knowledge_documents`, immutable
`knowledge_document_versions`, traceable `knowledge_chunks`, deterministic
Markdown/text chunking, Han-bigram search normalization, and PostgreSQL FTS with
scope filtering in SQL. A live PostgreSQL test verifies current-version switching
and global/personal visibility. The fixed `rag-retrieval-v1` corpus contains 12
industrial documents and 24 literal/paraphrased queries. Two repeated PostgreSQL
FTS runs produced the same hit set: Recall@5 23/24 (`95.83%`) and MRR `0.9028`;
the missed ERP 504 paraphrase is retained for the vector-retrieval comparison.
Ingestion timing is recorded but the small local run is not a resume throughput
metric. The next slice is Embedding plus hybrid fusion on this same dataset;
MinIO, OCR/VLM, reranking, mixed-document throughput improvement, and the final
resume item 3 claim remain incomplete. TaskEvent SSE
reuses the JSON event identity and cursor,
replays PostgreSQL facts, emits heartbeats, closes after terminal events, and is
cancelled by application shutdown without treating browser disconnect as task
cancellation. Worker process-crash drills remain a smaller reliability follow-up
and do not displace the resume-driven RAG target.

### Completed Backend Checkpoint: Formal Diagnosis Report Read API

`GET /api/v1/diagnosis-tasks/{taskId}/report` has been implemented with these boundaries:

1. add a typed, immutable report read model and PostgreSQL repository query;
2. authorize through the existing task owner/admin boundary;
3. return business and technical summaries, partial/missing-evidence state,
   model/Prompt trace metadata, timestamps, and ordered report evidence claims;
4. expose evidence identity, source type, source reference, support type, and
   truncation/validity metadata, but keep raw evidence content behind the later
   dedicated evidence endpoint;
5. return `409` when the task exists but no report can currently be read, while
   preserving `404` for a missing task and `403` for an unauthorized actor;
6. update `api/openapi.yaml`, `docs/design/openapi.json`, repository integration
   tests, service authorization tests, and HTTP contract tests together.

Verified acceptance: a task owner and admin can read the same committed report generated
by the Worker; another analyst cannot; pending/running/failed/cancelled tasks do
not fabricate an empty report; malformed stored summary JSON fails explicitly;
the endpoint performs a bounded query without returning Prompt text, raw model
reasoning, raw SQL, credentials, or complete evidence snapshots.

### Completed Backend Checkpoint: TaskEvent SSE

`text/event-stream` content negotiation is now available on the existing task-events route.
SSE replays PostgreSQL TaskEvents from
`Last-Event-ID`/`afterSeq`, emits heartbeats, preserves the existing JSON history
API, and never treats browser disconnect as task cancellation. Redis may wake a
stream but cannot become the event source of truth. Task lifecycle event names
now use the domain `TaskEventType`; one status/event mapping defines the
`succeeded`, `failed`, and `cancelled` terminal transitions shared by SSE,
Worker, and PostgreSQL adapters. These state-machine values are compile-time
protocol constants rather than runtime configuration.

### Completed Backend Checkpoint: Administrator Failed-Task Recovery

`POST /api/v1/admin/diagnosis-tasks/{taskId}/recover` now restores only allowlisted
`agent_execution_failed` tasks. The PostgreSQL transaction preserves the prior
attempt count, clears execution/error state, appends one `task_requeued`, reopens
the original Outbox/message ID, and writes an immutable admin/reason/error audit.
The same admin/key/reason replays without duplicate events; a changed reason
conflicts. Tasks with cancellation, reports, active leases, inactive creators or
data sources, or permanent failures are rejected. A later Worker Claim increments
the attempt and stale leases remain fenced.

The next backend checkpoint is repeatable process interruption, temporary model
failure, retry/dead-letter and recovered-success drills, followed by fixed-dataset
evaluation without fabricating duplicate reports or overwriting fenced results.

## Target Milestones, Not Yet Implemented

- M1: evidence-based ticket diagnosis;
- M2: knowledge assistant, RAG, and mixed-document ingestion;
- M4: isolated SQL performance laboratory.

Do not mark a target milestone as complete here until its acceptance criteria
in the linked product/domain/API and implementation-plan documents have been verified.
