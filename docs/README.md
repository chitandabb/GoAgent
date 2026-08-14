# MESGuard Documentation

This directory is the single location for project documentation.

## Active Documents

| Document | Purpose |
| --- | --- |
| [Resume](简历.md) | Resume source of truth; only verified project outcomes may be written as metrics |
| [Resume Project Points 1-2](resume-project-points-1-2.md) | Business, architecture, implementation, evidence, and interview narrative for the first two MESGuard resume items |
| [Resume Project Point 3](resume-project-point-3.md) | Business, architecture, implementation, evidence, and interview boundary for mixed-document parsing and Agentic RAG |
| [Development](development.md) | Local configuration, Docker, tests, and volume operation |
| [Roadmap](roadmap.md) | Current delivery state and ordered next milestones |
| [M3 Context Governance Spec](specs/m3-context-governance-and-layered-memory.md) | Implementation-ready scope, user stories, decisions, seams, and acceptance gates for resume point 4 |
| [Agent Orchestration](design/agent-orchestration.md) | Single-Agent loop, thin Graph, Skill/Tool boundaries, GitHub MCP, and evaluation metrics |

## Engineering References

| Document | Purpose |
| --- | --- |
| [Product and Workflow](design/product-and-workflow.md) | Target users, product boundaries, business flows, and staged delivery |
| [Code Organization](design/code-organization.md) | Production/tool directory roles, package dependency direction, and deferred structural debt |
| [System Architecture](design/system-architecture.md) | Runtime roles, component topology, data flows, deployment, and failure boundaries |
| [Domain and State Machine](design/domain-and-state-machine.md) | Domain objects, lifecycle states, invariants, and event rules |
| [Database Design](design/database.md) | PostgreSQL tables, constraints, indexes, migrations, and transaction boundaries |
| [Messaging Design](design/messaging.md) | Outbox, RabbitMQ topology, retries, dead letters, acknowledgements, and worker recovery |
| [API Design](design/api.md) | HTTP resources, authentication, authorization, idempotency, errors, and SSE contracts |
| [Diagnostic Tools](design/diagnostic-tools.md) | Remote SQL Server, database evidence, logs, and diagnostic Tool governance |
| [RAG Ingestion and Retrieval](design/rag-ingestion-and-retrieval.md) | Mixed-document ingestion, local layout routing, retrieval, Agentic RAG, Web Search boundary, and evaluation contract |
| [Context Governance and Memory](design/context-governance-and-memory.md) | Token budgeting, Summary + Tail memory, Prompt Epochs, Tool/Skill cache boundaries, persistence, and evaluation contract |
| [Frontend Design](design/frontend.md) | React workbench structure, design-token decisions, state-machine-to-UI mapping, and mock replacement |

## Decisions

| Document | Purpose |
| --- | --- |
| [ADR 001](decisions/001-modular-monolith-architecture.md) | Why MESGuard uses a modular monolith and manual dependency injection |
| [ADR 002](decisions/002-prefer-open-source-components.md) | Prefer mature open-source components; hand-write only thin contract glue |
| [ADR 003](decisions/003-local-onnx-layout-routing.md) | Why local ONNX handles bounded page/region routing while OCR/VLM remain configurable cloud capabilities |
| [ADR 004](decisions/004-conversation-driven-diagnosis-commands.md) | Why conversations stay independent while the Agent creates durable diagnosis tasks through a guarded command Tool |

## Evaluation Records

| Document | Purpose |
| --- | --- |
| [Evaluations](evaluations/) | Reproducible fixed-set methods and measured observations; raw provider output stays ignored |

Documentation rules:

- `README.md` at the repository root is only the project entry point.
- `简历.md` is the only resume fact source. Supporting resume documents explain evidence and
  boundaries but do not silently change resume metrics.
- `resume-project-points-1-2.md` and `resume-project-point-3.md` are interview/engineering
  explainers; measured results must link to an evaluation record and state dataset scope.
- Operational instructions belong in `development.md`.
- Current implementation progress and ordered next slices belong in `roadmap.md`.
- Directory responsibilities and package dependency direction belong in
  `design/code-organization.md`.
- Stable product and engineering boundaries belong in the reference documents;
  they must not be used to claim unimplemented features.
- `api/openapi.yaml` is the only machine-readable HTTP/SSE contract and only follows implemented Handlers.
- Long-lived design decisions belong in `decisions/`.
- Do not duplicate the same command, status, or design explanation in multiple
  documents.
