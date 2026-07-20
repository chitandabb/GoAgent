# MESGuard Roadmap

## Product Goal

MESGuard helps MES implementation, support, and quality engineers turn a ticket
into a traceable diagnosis: collect read-only evidence, reason over it, expose
uncertainty, and produce an actionable report. The repository uses synthetic
MES data only.

## Completed Foundation

- [x] PostgreSQL + pgvector, SQL Server 2022 Developer, and Redis Compose stack.
- [x] `cmd/internal` modular-monolith layout with explicit dependency wiring.
- [x] PostgreSQL-backed diagnostic Run and ordered Event persistence.
- [x] Versioned SQL migration ledger and durable SSE event replay.
- [x] Eino OpenAI-compatible adapter boundary.
- [x] Go 1.25.3 toolchain and Docker build alignment.

## Next: Safe Evidence Collection

- [ ] Implement a SQL Server read-only `MESReader` adapter.
- [ ] Whitelist diagnostic query templates and validate parameters.
- [ ] Record query duration, row limits, and execution-plan evidence as events.
- [ ] Add synthetic ticket cases for delayed work orders, interface failures,
  and slow queries.

## Then: Agent Execution

- [ ] Add Run, Step, ToolCall, and Event state transitions.
- [ ] Execute one Eino-backed diagnostic loop with timeout and cancellation.
- [ ] Persist every model/tool decision before publishing the corresponding SSE
  event.
- [ ] Return a structured diagnosis with evidence, confidence, and next steps.

## Later: Knowledge and Evaluation

- [ ] Add PostgreSQL full-text and vector retrieval for product documents and
  confirmed cases.
- [ ] Build a small offline evaluation set from synthetic MES cases.
- [ ] Add trace timing, prompt/version metadata, and regression checks.
- [ ] Build a dedicated MES diagnostic workbench after the API workflow is
  stable.

## Explicit Non-goals

- No general-purpose assistant, media-processing, or unrelated integration
  features.
- No write-capable SQL tools, DDL, stored procedure execution, or automatic
  remediation.
- No multi-agent collaboration before the single-agent diagnostic loop is
  observable, safe, and evaluated.
