# MESGuard Current Progress

This file tracks the repository's current implementation state. Target
milestones and product acceptance boundaries are defined in the product,
domain, database, API, and system-architecture design documents. Agent
execution order and acceptance gates are defined in
[`design/agent-implementation-plan.md`](design/agent-implementation-plan.md).

## Current Stage: M1-A1 Backend Complete, Agent Foundation In Progress

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
      [`design/frontend.md`](design/frontend.md). Authentication API is now
      implemented; ticket and diagnosis APIs are still not implemented.

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
  and an exact four-tool allowlist;
- application-side repository, ref, path, query, pagination, and commit-SHA
  validation before GitHub MCP calls;
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
  replica safety mode, and currently available dependencies;
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
- successful fact-producing read-only Tool calls now produce bounded runtime
  `EvidenceItem` snapshots with a unique `evidenceRef`, source, hash, capture
  time, and truncation state; the Evidence Gate requires report references to
  resolve to those items.

Not yet implemented or verified:

- a live GitHub MCP call with the user's PAT and private demo repository;
- Schema Catalog scanning/publishing, Query Store, and estimated-plan Tools;
- formal EvidenceItem persistence in the DiagnosisTask/Worker chain;
- Diagnosis Worker/SSE integration;
- the resume target values of 93% tool-selection accuracy and 45% Token
  reduction.

The single-Agent inner loop is now the production Runner baseline. See
[`design/agent-implementation-plan.md`](design/agent-implementation-plan.md)
for the ordered migration and [`design/agent-orchestration.md`](design/agent-orchestration.md)
for the target boundaries.

## Next Slice

P0 through P5 的评测契约与统计基础已完成。P6 当前切片已完成对象定义读取、已发布
Schema Catalog 窄检索、QueryGuard、受限 `execute_readonly_query` Tool、真实跨数据库
联调和运行时 EvidenceItem；仍不等同于通用 Text-to-SQL。下一步回到第一条简历能力的
GitHub PAT 与 baseline/experiment 评测闭环。正式 EvidenceItem 持久化留到
DiagnosisTask/Worker 链路稳定后处理。

MinIO attachment work and the DiagnosisTask/Outbox/Worker product chain resume
after the Agent core reaches the plan's P5 reproducible-evaluation checkpoint.

## Target Milestones, Not Yet Implemented

- M1: evidence-based ticket diagnosis;
- M2: knowledge assistant, RAG, and mixed-document ingestion;
- M4: isolated SQL performance laboratory.

Do not mark a target milestone as complete here until its acceptance criteria
in the linked product/domain/API and implementation-plan documents have been verified.
