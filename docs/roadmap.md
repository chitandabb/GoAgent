# MESGuard Current Progress

This file tracks the repository's current implementation state. Target
milestones, dependencies, and acceptance criteria are defined in
[`design/delivery-plan.md`](design/delivery-plan.md).

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

- Eino `v0.9.13` Graph-based intent branching;
- immutable `SkillDefinition` and registry with Prompt, context budget, Tool
  allowlist, timeout, and maximum ReAct steps;
- versioned `skill.toml` plus `system-prompt.md` packages loaded with strict
  unknown-field, path, duplicate-ID, prompt-size, and definition validation;
- separately compiled ReAct executors so a Skill sees only its allowed tools;
- the `ticket-diagnosis` and `code-investigation` Skill definitions;
- a data-minimized `read_external_case` Eino Tool;
- official GitHub MCP Streamable HTTP client configuration using read-only mode
  and an exact four-tool allowlist;
- application-side repository, ref, path, query, pagination, and commit-SHA
  validation before GitHub MCP calls;
- StepFun Step Plan `step-3.7-flash` configuration and Eino OpenAI-compatible
  ToolCallingChatModel factory, including local protocol and usage tests;
- a live StepFun capability probe on 2026-07-30 that returned the required Tool
  Call and provider usage without executing a business tool;
- bootstrap wiring that loads versioned Skills and degrades model/GitHub MCP
  failures without stopping authentication and ticket browsing;
- per-run Eino Callback aggregation across all non-streaming ReAct ChatModel
  calls, including prompt, completion, total, cached, and reasoning Tokens;
- a live synthetic-ticket ReAct run on 2026-07-30 that made two model calls,
  executed only `read_external_case`, and returned aggregated provider usage;
- evaluation aggregation for routing accuracy, first-tool accuracy,
  out-of-allowlist calls, and provider-reported input Token reduction;
- a transitional structured `ticket-diagnosis -> code-investigation` Handoff
  and generic Dispatcher with loop rejection and a three-handoff limit.

Not yet implemented or verified:

- a live GitHub MCP call with the user's PAT and private demo repository;
- Eino ADK `ChatModelAgent` and Skill Middleware compatibility with StepFun;
- the target single-Agent inner loop and thin Evidence Gate Graph;
- a unified runtime `AgentToolProvider` that filters Tools by task scope;
- Diagnosis Worker/SSE integration;
- the resume target values of 93% tool-selection accuracy and 45% Token
  reduction.

The current per-Skill ReAct/Handoff implementation is a migration baseline,
not the target architecture. See
[`design/agent-implementation-plan.md`](design/agent-implementation-plan.md)
for the ordered migration and [`design/agent-orchestration.md`](design/agent-orchestration.md)
for the target boundaries.

## Next Slice

The immediate slice is P0 of the Agent implementation plan: build an isolated
Eino ADK `ChatModelAgent` POC against StepFun, verify Skill Middleware, Tool
Calling, Callback usage, cancellation, and streaming compatibility, while the
current Runner remains the working baseline. After that, introduce the unified
ToolCatalog/`AgentToolProvider`, migrate Skills to `SKILL.md`, and replace the
per-Skill Executors with one Agent loop.

MinIO attachment work and the DiagnosisTask/Outbox/Worker product chain resume
after the Agent core reaches the plan's P5 reproducible-evaluation checkpoint.

## Target Milestones, Not Yet Implemented

- M1: evidence-based ticket diagnosis;
- M2: knowledge assistant, RAG, and mixed-document ingestion;
- M4: isolated SQL performance laboratory.

Do not mark a target milestone as complete here until its acceptance criteria
in the delivery plan have been verified.
