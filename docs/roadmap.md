# MESGuard Current Progress

This file tracks the repository's current implementation state. Target
milestones and product acceptance boundaries are defined in the product,
domain, database, API, and system-architecture design documents. Agent
execution order and acceptance gates are defined in
[`design/agent-implementation-plan.md`](design/agent-implementation-plan.md).

## Current Stage: M2-B1 Advanced RAG Backend Controls Implemented, Quality Expansion Pending

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
`TaskScope`，执行现有 ADK Agent + Evidence Gate。`case/code/sql/knowledge` 能力白名单与 Runtime
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
now stable enough to continue resume item 3. M2-A1 provides `knowledge_documents`, immutable
`knowledge_document_versions`, traceable `knowledge_chunks`, deterministic
Markdown/text chunking, Han-bigram search normalization, and PostgreSQL FTS with
scope filtering in SQL. A live PostgreSQL test verifies current-version switching
and global/personal visibility. The fixed `rag-retrieval-v1` corpus contains 12
industrial documents and 24 literal/paraphrased queries. Two repeated PostgreSQL
FTS runs produced the same hit set: Recall@5 23/24 (`95.83%`) and MRR `0.9028`;
the missed ERP 504 paraphrase is retained for the vector-retrieval comparison.
M2-A2 provides degradable MinIO object storage, immutable source references,
queued knowledge versions, recoverable ingestion task/event state, and atomic
`knowledge.ingest` Outbox creation. M2-A3 adds administrator create/version upload
APIs, UUID `Idempotency-Key` replay/conflict semantics over a source SHA-256 request
fingerprint, bounded temporary-file staging, supported-format signatures, task
status/cancellation APIs, and the ingestion Worker claim/lease/heartbeat/checkpoint/
fencing/retry state machine. Live PostgreSQL tests cover lease contention, expired
takeover, stale-token rejection, retry delay, cancellation, ready publication, and
the `partial_ready` publication gate.

M2-A4 now closes the first runnable ingestion path for UTF-8 TXT and Markdown:
the durable RabbitMQ Consumer uses manual ACK, publisher-confirmed retry/dead copies,
and the existing 30-second/2-minute/10-minute retry schedule; the isolated Knowledge
Worker opens only PostgreSQL, RabbitMQ, and MinIO. It verifies the immutable source
size and SHA-256, routes to a deterministic text parser, persists a versioned JSON
Element Artifact in MinIO, projects Elements into bounded Chunks, and stages the
Artifact reference plus replacement Chunks under the active lease fencing token.
Only after staging succeeds does the Worker enter publishing and atomically switch a
complete version to `ready/current`. Real PostgreSQL, RabbitMQ, and MinIO integration
tests pass, and an API -> Outbox -> Consumer -> Worker smoke produced a ready Markdown
version with eight searchable Chunks before its fixture was cleaned up.

M2-A5 extends that same Element contract with resource-bounded deterministic parsing.
PDF files are read page by page for embedded text and retain page numbers. DOCX extracts
heading-aware paragraphs and tables; XLSX follows workbook relationships and extracts
cell values by worksheet; PPTX follows presentation relationships and extracts slide
paragraphs and tables with slide numbers. The Worker enforces configurable document-unit,
ZIP-entry, expanded-byte, per-XML, extracted-rune, spreadsheet-row, and spreadsheet-column
limits; invalid, encrypted, empty, or over-budget inputs are permanent failures. OOXML
rejects unsafe/duplicate paths and does not follow external relationships. Embedded Office
images are counted and marked for later visual enrichment rather than being described by
the text parser.

An API -> Outbox -> RabbitMQ -> Knowledge Worker smoke verified PDF, DOCX, XLSX, and PPTX
as `succeeded/completed`, `ready/current`, with the expected parser versions, non-empty
Chunks, and persisted Artifact SHA-256 values; all fixture objects and database rows were
then removed. Ingestion timing from these small fixtures is functional evidence, not a
resume throughput metric. At this checkpoint M2-A6 became the active visual-asset slice;
scanned PDF/image interpretation, richer Office semantics, Embedding, hybrid fusion, reranking,
mixed-document throughput evaluation, and the final resume item 3 claim were still incomplete. TaskEvent SSE
reuses the JSON event identity and cursor,
replays PostgreSQL facts, emits heartbeats, closes after terminal events, and is
cancelled by application shutdown without treating browser disconnect as task
cancellation. Worker process-crash drills remain a smaller reliability follow-up
and do not displace the resume-driven RAG target.

### M2-A6: Bounded Visual Assets and Configurable OCR/VLM Routing

The backend now extracts a bounded visual contract alongside deterministic text Elements:

- PDF pages with no embedded text become `document_page` candidates that retain the page
  number and source SHA-256; standalone PNG/JPEG uploads become `source_image` candidates;
  DOCX/XLSX/PPTX media is extracted only from package media roots.
- Office relationship XML is resolved only within its package root. Each referenced image
  occurrence retains `sourcePart`, `relationshipId`, and, for PPTX, presentation-order page
  number. Reused images therefore remain separately traceable; orphan media is retained as
  an audit record with `unreferenced_asset` and is skipped before model invocation.
- Per-document media count, per-asset bytes, total unique-media bytes, visual occurrences,
  enrichment count, and minimum pixel thresholds are enforced before provider calls. Tiny
  raster images are skipped as decorative; supported actionable rasters route to OCR+VLM,
  and PDF page candidates route to OCR.
- Element Artifact schema v2 stores visual location, dimensions, media type, SHA-256,
  route/status/reason, provider/model metadata, and output Element indexes. It never stores
  raw visual bytes. Native text plus missing visual enrichment publishes `partial_ready`; a
  visual-only source fails permanently with `invalid_ingestion_input` rather than publishing
  an empty searchable version.
- OCR and Vision endpoints are configured independently through `[models.ocr]` and
  `[models.vision]`, including provider, model, prompt file, prompt version, timeout and
  output budget. The current DashScope adapter accepts only bounded strict JSON output and
  passes the API key to the Knowledge Worker only. Bounded PNG smokes completed both the
  Vision path and the isolated DashScope `qwen-vl-ocr-latest` path through API -> Outbox ->
  RabbitMQ -> Knowledge Worker. OCR returned the expected text Element and persisted
  provider/model/Prompt metadata. Direct PDF `file_url` input was tested separately and is
  unsupported by the current Eino OpenAI adapter; that error is now permanent instead of
  entering the retry queues. No OCR quality or scanned-document metric is claimed yet.

The isolated smoke users, sessions, objects, documents, versions, tasks, events, chunks and
Outbox rows were removed after verification. Targeted tests, full `go test ./...`,
`go vet ./...`, parser/enrichment/ingestion/bootstrap race tests, storage/messaging
integration tests and `docker compose config --quiet` passed after cleanup. M2-A6 is closed.
Embedding, hybrid fusion, reranking, Web Search, and the final resume item 3 metric were
deferred from this checkpoint to later slices.

### M2-A7: Local ONNX Page and Region Routing

The accepted 2026-08-05 implementation target is a focused local layout router, not a
replacement Python document-processing pipeline:

- keep deterministic Go parsing as the fast path and combine native-text quality signals
  with a pre-trained ONNX document-layout detector;
- classify pages as native digital, scanned, or mixed, then retain bounded regions with
  type, bounding box, confidence, model/version, and stable route reason;
- route text regions to native extraction or cloud OCR, scanned tables to table recovery,
  and charts/screenshots/diagrams to cloud VLM; perform retrieval chunking only after region
  recognition, merge, and de-duplication;
- expose an optional Eino Document Transformer/callback adapter without making Eino
  `schema.Document` the persisted Element contract;
- use Docling as a design/reference baseline rather than a required runtime dependency;
- keep DashScope OCR cloud-hosted. Compare StepFun `step-3.7-flash` at low reasoning with
  the current DashScope `qwen3-vl-plus` Vision baseline over identical cropped regions;
  Google `gemini-3.5-flash-lite` is an optional speed reference, not an immediate dependency;
- report routing Macro-F1, high-value visual miss rate, avoided cloud calls, CPU/RAM,
  end-to-end region P50/P95, valid JSON, semantic accuracy, provider failures, and cost per
  successful region. Vendor TPS claims are not project evidence.

The detailed boundary and completion checklist are recorded in
`docs/decisions/003-local-onnx-layout-routing.md`.

Current M2-A7 implementation state:

- `[knowledge.layout]` now holds the disabled-by-default model/runtime identity, rendering,
  inference, concurrency, native-text, confidence, crop-padding and resource limits.
- parsers emit ordered page observations. PDF visual-candidate state is explicitly `unknown`
  because embedded-text extraction cannot prove a page has no vector table/chart; standalone
  PNG/JPEG state is known and contains one visual candidate.
- `LayoutRouter`, `RoutePlanner`, `PageRenderer`, `PageAnalyzer`, bounded region cropper and
  Parse-output `LayoutStage` now execute inside the Knowledge Worker when enabled. PDFium-WASM
  rendering, ONNX Runtime 1.28.0, PP-DocLayout-M conversion/manifest checks, explicit region
  OCR/VLM plans, document-level crop budgets, duplicate whole-page suppression and Artifact
  schema v5 provenance with provider Token usage are implemented. Confirmed text-only pages skip rendering; only
  actionable routes within the count/byte budgets retain crops.
- pinned scripts reproduce the ONNX model and Windows/Linux runtime. A real upstream fixture
  passed the Go adapter with table/caption/text detections; no route-quality metric is inferred.
- the versioned `layout-routing-public-v1` manifest pins seven real public PDF/DOCX files by
  URL, usage basis, byte length and SHA-256; eight reviewed PDF pages now have page/route and
  high-value-region annotations. Bounded PDFium native-text recovery and class-aware
  low-confidence fallback corrected the NIST native page and scanned-prose VLM false route.
  The current Windows run records page-class Macro-F1 1.0000, actionable-route Macro-F1
  1.0000, 0/7 high-value misses and 74.03% cloud-bound-region avoidance against the explicitly
  documented all-regions-cloud routing baseline. No cloud provider was called in this run.
- real scanned patent pages exposed oversized-raster task failure. PDFium now adaptively lowers
  effective DPI and persists requested/effective DPI. A 20M/8M pixel paired run kept route
  quality unchanged while cutting P95 from 6.59 s to 2.62 s and peak working set from 786.5 MiB
  to 584.1 MiB. One 72-DPI prose page now has paired OCR evidence, but 8M is not promoted until
  small-font, scanned-table and degraded-scan quality is measured.
- `element-merge-v1` now runs before Chunk creation. It keeps all raw Elements in Artifact v5
  but removes explainable same-page duplicates from the searchable projection: exact normalized
  duplicates, OCR fully covered by native text, and highly contained overlapping OCR. It does
  not fuzzy-deduplicate VLM descriptions.
- configurable small decorative/picture arbitration removed one duplicate NASA-logo cloud
  candidate without changing actionable-route Macro-F1 or the 0/7 high-value miss result;
- nine real local PPTX files (752 slides) now pass the production-limit parser. Three fresh
  process runs have median 9.55 MiB/s and 378.72 slides/s. This is parser-only throughput,
  not upload-to-publish throughput;
- an independently reviewed eight-slide PPTX structure set now verifies 21 page anchors, nine
  DrawingML tables, 15 picture uses and 14 distinct slide relationships at 100% on the fixed set.
  It does not yet prove SmartArt/chart semantics or cloud-enriched Element merge quality;
- a three-run Windows ONNX thread A/B kept route quality unchanged. Intra-op 1/2/4 produced
  median average page times of 1457.09/1389.57/1283.88 ms at the 8M raster limit; two threads
  retain the best median P95 and remain the default while four is a throughput candidate;
- one explicitly enabled two-call OCR pair compared USPTO page 8 at 113 and 72 DPI. Both passed
  strict JSON; paired character similarity was 99.54%, 72-DPI provider latency was 30.6% lower,
  and total measured cost was about CNY 0.00631. One clean prose page does not promote 8M.
- a bounded three-region VLM pair compared `qwen3-vl-plus` with `step-3.7-flash` low reasoning
  using identical crops, Prompt, strict JSON and a 2,048 output-Token limit. Both completed 3/3,
  reached 100% text-anchor recall and 8/9 reviewed relation-fact recall, but manual review found
  one relation error per provider, so each had only 2/3 fully correct cases. Qwen averaged
  5.35 seconds and 2,206 total Tokens versus StepFun's 7.87 seconds and 4,740 Tokens; its three
  calls cost about CNY 0.00684. The current production Vision profile remains Qwen, while StepFun
  stays a candidate rather than being rejected from one small set.
- an optional enhanced Knowledge Worker image now packages only the SHA-pinned Linux x64 ONNX
  Runtime, PP-DocLayout-M model and license notices through an ignored BuildKit context. The
  default Compose build remains lightweight; the layout overlay runs non-root with a read-only
  root filesystem and no capabilities. A no-network 2 CPU/2 GiB Linux fixed-set run retained the
  quality metrics, averaged 1.18 seconds per page, had 2.59-second P95 and 638.06 MiB peak RSS.

The production profile still keeps layout disabled. Broader chart/screenshot/scanned-table
fixtures and cloud-enriched merge-quality evidence remain. M2-A7 must not be described as fully evaluated
operational routing, and the current region-avoidance metric must not be presented as measured
Token/cost reduction. See `docs/evaluations/layout-routing-public-v1.md` and
`docs/evaluations/knowledge-ingestion-quality-v1.md`.

### M2-A8: Embedding, Vector Search and RRF Baseline

M2-A8 now connects the parsed Chunk projection to a versioned vector index:

- `[models.embedding]` is independent from ChatModel and uses the validated DashScope
  `text-embedding-v4` profile with query/document input types, 1024 dimensions, normalization,
  batch size and bounded concurrency;
- `00016_create_knowledge_embeddings.sql` creates `knowledge_embedding_profiles` and
  `knowledge_chunk_embeddings`, with one active profile, stable fingerprint identity, content
  SHA-256 consistency and Chunk cascade cleanup;
- Knowledge Worker computes bounded batch Embeddings after parsing/chunking and writes Artifact,
  Chunk and vectors under the same fenced publication transaction. An Embedding failure blocks
  publication and follows the existing retry/dead-letter path;
- PostgreSQL Vector Search applies active profile, current/ready, deleted and global/personal scope
  filters in SQL. Hybrid retrieval runs FTS and Vector candidates concurrently, fuses them with
  `RRF k=60`, deduplicates by content hash and reports degraded channels when one path fails;
- the `rag-retrieval-v1` fixed set now compares FTS, Vector and RRF. FTS reached 23/24 Recall@5,
  Vector 24/24 with MRR 1.0, and RRF 24/24 with MRR 0.9792. Vector/RRF consumed 796 provider
  Embedding Tokens on this 24-query set. This is a bounded correctness baseline, not a production
  throughput or cost claim; full methodology is in `docs/evaluations/rag-retrieval-v1.md`.

M2-A8 is complete at the persistence/retrieval layer and the knowledge-qa and diagnosis Runners now receive a
single backend-owned `search_knowledge` Tool. The Tool accepts only a bounded business query and
result limit; it injects the actor identity, hides FTS/Vector/RRF selection, and returns source
location plus `degraded/missingChannels` when Embedding is unavailable. A disabled-by-default
`[models.rerank]` profile, `qwen3-rerank` native HTTP adapter, candidate-to-result reordering,
Token propagation and retrieval-order fallback are also implemented and covered by contract tests.
The live 24-case `qwen3-rerank` run succeeded with Recall@5 24/24 and MRR 0.9792, matching the current
RRF quality baseline; the provider response did not expose usable rerank token usage, so cost remains
unknown. New diagnosis tasks automatically receive the backend-managed knowledge capability and the
frontend does not choose this Tool. Knowledge results now pass a deterministic citation gate: empty or
malformed results do not become EvidenceItems, and valid results are tagged as `knowledge_chunk` with
document/version/Chunk/hash location metadata. Logical Parent context expansion and controlled Query Plan
are now implemented as the first M2-B1 slices; Parent and Rewrite each have one real paired Case, while the
Compression axis has all five. Deterministic whole-Chunk compression and Evidence Gate-driven bounded
Agentic re-retrieval are now implemented. The five quality Cases did not reach the production budget; a
separate long-parent pressure Case repeatedly reduced 7/1507 neighbor chunks/runes to 6/1438 while preserving
Gold Context Recall. This is a threshold check, not an aggregate Token claim.

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

### Backend Checkpoint: Controlled Public Web Research

The M2-B2 backend chain now exists in `internal/webresearch`. A provider query is
created only after deterministic removal of direct identifiers, private network/file
locations, business IDs, hashes, administrator terms, and current-ticket terms.
Credentials, connection strings, raw SQL/log/stack/JSON content, invalid input, and
queries with too little technical signal fail closed. Findings retain category/count
only, and the future provider boundary accepts `PublicQuery` rather than arbitrary
text. `[webSearch.redaction]` controls rune budgets and an optional environment-backed
dictionary. Firecrawl `/v2/search` discovery and `/v2/scrape` extraction remain
separate. Search emits run-scoped opaque result IDs; page fetch accepts no arbitrary
URL, enforces two-search/three-page budgets, caches repeated fetches, and validates
both candidate and final provider-reported targets against protocol, port, DNS and
public-address rules. JSON responses are byte-bounded and page Markdown is
character-bounded and marked untrusted.

Only a fetched page can become a `web` EvidenceItem. Its URL, title, source tier,
available page time, fetched-at time, truncation state and content SHA-256 are retained,
and the Evidence boundary recomputes the hash. Source tiers are configured by domain;
unlisted sources are conservatively C. System/Skill instructions forbid treating page
text as commands. New diagnosis tasks receive backend-managed web-search authorization,
while dependency failure hides the Tools without failing the runtime. Offline provider,
SSRF, budget and tamper tests pass. One real Search + one Scrape smoke remains pending
because the current project environment has no `FIRECRAWL_API_KEY`; no public-provider
request or cost was produced at this checkpoint. Firecrawl's unobservable intermediate
redirect chain remains a provider-side SSRF boundary.

### Partial Backend Checkpoint: Small-to-Big Context, Compression, Query Plan and Agentic Re-retrieval

The first M2-B1 retrieval-context slice is implemented without adding a duplicate
parent vector index. Child chunks remain the FTS/vector/RRF/rerank unit. After final
top-K selection, PostgreSQL batch-loads a bounded neighboring window from the same
current document version and `section_path`, under the same actor scope filters.
`[knowledge.retrieval]` owns the window and per-parent rune budget. Tool output keeps
matched chunks separate from context groups, preserves IDs and hashes for every
chunk, and degrades only the `context` channel on expansion failure.

Expanded context then passes through a deterministic whole-Chunk compressor owned by
`[knowledge.retrieval.contextCompression]`. Production defaults cap added context at six chunks and
3000 runes with a 0.05 score floor. Query coverage, protected signals, hit rank and ordinal distance
affect selection; text is never summarized or partially truncated, so content hashes remain valid.
Tool and Evidence boundaries verify input/output counts and runes. Compression failure reports
`context_compression` degradation and keeps the uncompressed parent context.

This is a logical parent reconstructed from section structure, not a materialized
parent index. A configurable Query Rewriter now produces a strict lexical/semantic/
subquery plan while always retaining the original query. Deterministic protected-signal
validation rejects changed error codes, versions, numbers, time constraints and explicit
negation. FTS and Vector merge query variants by rank and original-query priority before
RRF; provider failure or internal timeout degrades only `query_rewrite`. The Tool exposes
the plan, four stable statuses, prompt version and provider token usage.

Query Rewrite remains disabled by default. The strict paired evaluation contract, offline
`mesguard-rag-paired-eval` aggregator and real `mesguard-rag-paired-observe` fixture command now
separate original/rewrite, child/parent and parent/compression arms, pin public corpus and gold chunks by URL/date/hash,
reject mixed retrieval profiles, and report quality, query amplification, context growth, latency,
Embedding, Rewrite and Rerank usage. The real command uses the production SearchService inside a
rolled-back PostgreSQL transaction, defaults to one Case and requires explicit provider execution.

Chat model assembly now uses `activeProfile` plus named profiles. The Provider Factory has offline
request-shape contracts for StepFun, DeepSeek and DashScope: provider-specific reasoning/thinking
fields are mapped or rejected explicitly, and only the selected profile reads its API key. Query
Rewrite resolves its own fast profile (`qwen-rewrite`) instead of inheriting the main Agent's
reasoning and output budget. This closes the configuration and budget-isolation gap, but it does
not complete live DeepSeek/Qwen Tool Calling or quality acceptance.

`rag-advanced-v1` currently contains 4 official documents, 21 chunks and 5 cases. Parent and Rewrite
each have one bounded observation: logical Parent recovered the second gold context chunk but doubled
context runes; accepted Query Rewrite did not change quality and added 1152 rewrite Tokens plus substantial
latency. The compression axis has run all five Cases with pinned production limits; all 13 parent neighbors
remained below the limit, so quality stayed unchanged and zero chunks were omitted. These results prove the
measurement path, not a general gain or Token reduction.

The separate `rag-compression-pressure-v1` fixture pins two PostgreSQL official documents and one `K=6`
multi-fact long-section Case. A CLI acceptance gate now fails unless at least one neighbor is omitted without
reducing Gold Context Recall. Three repeated accepted runs consistently reduced seven neighbors to six and
1507 runes to 1438 (-4.58%) while preserving Context Recall at 1.0. This is a threshold/traceability check,
not an aggregate Token claim; latency from the sequential provider pair is not attributed to compression.

The existing EvidenceOrchestrator now owns bounded Agentic re-retrieval. Only evidence/source-binding gaps
keep `search_knowledge` visible in run two; format-only repair hides it. A run-scoped policy permits at most
one second-run knowledge call while retaining the original total Tool, Token and timeout budgets. The result,
stored report and report API expose whether retrieval was attempted, whether it added a new stable
version/Chunk/content-hash identity and why
it stopped; the investigation timeline gets a redacted `agentic_retrieval` step. Real-model trigger precision
is now covered by a three-Case fixed set: evidence gaps selected knowledge search and added stable evidence,
format-only repair hid the Tool, and a valid first pass skipped the second run. All expected decisions and stop
reasons matched, using 16453 total Tokens. Answer-quality gains and broad stability are not yet measured.

ADR 004 has accepted the next conversation boundary: the right-side dossier supplies structured case
references to an independent conversation, and the conversation Agent may create a durable diagnosis
task through a guarded command Tool. Conversations do not own task lifecycle. This is a target decision,
not current behavior; the implemented frontend still calls `POST /api/v1/diagnosis-tasks` directly and
the backend has no conversation/message persistence or `create_diagnosis_task` Tool yet.

### Partial Backend Checkpoint: Knowledge Ingestion Throughput Baseline

The Knowledge Worker repository now accepts a validated `[knowledge].chunkWriteBatchSize` and stages
Chunks plus pgvector rows with GORM `CreateInBatches` under the existing fenced transaction. Batch size
one preserves the serial reference path; PostgreSQL integration tests cover multi-batch Chunk/vector
staging and ready publication.

The paired observer exercises real MinIO, PDF/Office parsing, DashScope Embedding, Worker checkpoints and
PostgreSQL publication while cleaning all temporary facts and objects. Its corpus manifest now requires
one of eight stable `formatClass` values and includes that class in the corpus fingerprint, so native and
scanned PDFs are not collapsed by their shared MIME.

The first NIST IR 8108 pair reduced 32 Embedding requests to 4 and 32+32 Chunk/vector INSERT batches to
1+1. Total duration changed from 6686 ms to 1130 ms with equal 7904 Embedding Tokens and equal partial
outcomes/7 Elements/32 Chunks. The observed throughput increase is 491.68%, but the acceptance gate remains
false because this is only one document, one format class and one pair. It also combines Embedding batching
with database batching and excludes RabbitMQ, OCR/VLM and layout routing. Full scope is recorded in
`docs/evaluations/rag-ingestion-throughput-v1.md`.

Database isolation is now complete on the three currently pinned public documents (two native PDFs and one
DOCX), producing 743 Chunks. Five order-alternated pairs changed median `SaveParsedResult` time from 1752 ms
to 406 ms and per-run Chunk/vector INSERT batches from 743+743 to 9+9, with zero Provider requests or Tokens.
Median paired staging throughput increased 319.21% and temporary actor/document residue was 0|0. This proves
an independent database-round-trip gain, but remains ineligible at three documents/two classes. The first DOCX
run also exposed and fixed an OPC compatibility bug: same-package `word -> customXml` relationships are now
allowed while traversal beyond the virtual ZIP root remains rejected.

A provider-free corpus audit now runs the production Parser and Chunking before any infrastructure call. The
pinned set contains 40 public documents across all eight declared format classes, producing 5,946 raw Elements,
5,854 searchable Elements, 12,864 Chunks and 139 visual candidates with zero parser failures. Production
`element-merge-v1` suppresses 92 duplicate/nonsemantic Elements before Chunking. Twenty-seven documents are
text-ready, ten retain searchable text while awaiting visual enrichment, and three require visual enrichment. Only
materialized image bytes are summed; PDF page references no longer multiply the source-file size. This completes
the document-count and format-coverage gates; five full-chain Provider pairs remain before acceptance can pass.
The manifest also pins publisher, source page, HTTPS download URL and usage boundary; a guarded fetch script writes
only below the ignored evaluation root and accepts a file only after byte-length and SHA-256 verification.
Corpus admission rejected one 83.85 MB PDF above the 50 MiB upload limit and removed NIST AMS 100-32 after its
in-process page text extraction exceeded 40 seconds without honoring cancellation. A passing NIST document replaced
it in the positive corpus; a terminable subprocess or equivalent isolation boundary remains a parser-hardening task.
The first 40-document provider pair is also excluded: its concurrency-2 arm hit DashScope
`Throttling.AllocationQuota` on 11 documents and returned 10,923 rather than 12,864 Chunks. The integrity gate
correctly rejected the apparent +141.57% throughput. The two arms issued 2,412 HTTP requests and consumed
4,668,907 Tokens, approximately CNY 2.3345. The experiment averaged 571.6 RPM and 1,076,707 TPM against the
Beijing limits of 1,800 RPM and 1,200,000 TPM, identifying rolling Token bursts rather than a permanently
exhausted key quota. Provider runs now perform a local cost preflight, default to a CNY 0.05 command budget and
half-limit smoothing, and abort the whole evaluation on the first 429. The 40-document set remains provider-free
unless a full-run estimate receives explicit cost approval.

The live two-document worker-core run exposed an evaluation isolation bug: its production-style Outbox row could
be claimed by the running Relay/Worker before the embedded evaluation Worker, yielding `lease is held` and a false
performance delta. Evaluation queueing now removes that Outbox row inside the same PostgreSQL transaction and
persists per-document outcome diagnostics. After adding cost preflight and rate smoothing, a five-pair
single-variable ablation changed only document concurrency `1 -> 2`; median duration fell from 2124 ms to 1450 ms
and median throughput increased 46.48%, with identical 42 Elements/70 Chunks/requests/Tokens/batches. The whole
run used 80 requests, 96,060 Tokens and approximately CNY 0.04803, with zero temporary actor/document residue.
This supports the bounded worker-core 40%+ claim; the two-document/two-class scope remains explicit and is not the
40-document mixed-visual acceptance result.

## Target Milestones, Not Yet Implemented

- M2-B1: expand the checked-in pressure dataset beyond one long-parent Case, add failed/repeated/no-selection second-retrieval and answer-quality cases, complete the remaining Parent/Rewrite pairs, and measure aggregate compression rate and repeated-run stability; implementation is present but broad sample quality is unproven;
- M2-B2: run the bounded one-Search/one-Scrape Firecrawl smoke when a Key is available, then add a small public-answer/citation quality set; backend Provider, authorization, safety, citation and dependency-degradation paths are implemented;
- M2-B3: independent conversation/message API, structured case/task references, guarded Agent task-creation command, knowledge-qa streaming, citation preview and attachment content access;
- M2-C: retain the completed 40-document/eight-class corpus as the provider-free parser/chunk compatibility gate;
  use a 4-8 document representative set for budgeted full-chain smoke and controlled pairs, then decide whether
  the full 40-document/five-pair synchronous cost is justified before adding RabbitMQ delivery to the
  upload-to-publish baseline;
- M4: isolated SQL performance laboratory.

Do not mark a target milestone as complete here until its acceptance criteria
in the linked product/domain/API and implementation-plan documents have been verified.
