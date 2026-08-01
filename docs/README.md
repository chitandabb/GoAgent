# MESGuard Documentation

This directory is the single location for project documentation.

## Active Documents

| Document | Purpose |
| --- | --- |
| [Resume](简历.md) | Resume source of truth; only verified project outcomes may be written as metrics |
| [Development](development.md) | Local configuration, Docker, tests, and volume operation |
| [Roadmap](roadmap.md) | Current delivery state and ordered next milestones |
| [Agent Orchestration](design/agent-orchestration.md) | Single-Agent loop, thin Graph, Skill/Tool boundaries, GitHub MCP, and evaluation metrics |
| [Agent Implementation Plan](design/agent-implementation-plan.md) | Ordered ADK migration, acceptance gates, deletion rules, and resume checklist |

## Engineering References

| Document | Purpose |
| --- | --- |
| [Product and Workflow](design/product-and-workflow.md) | Target users, product boundaries, business flows, and staged delivery |
| [System Architecture](design/system-architecture.md) | Runtime roles, component topology, data flows, deployment, and failure boundaries |
| [Domain and State Machine](design/domain-and-state-machine.md) | Domain objects, lifecycle states, invariants, and event rules |
| [Database Design](design/database.md) | PostgreSQL tables, constraints, indexes, migrations, and transaction boundaries |
| [Messaging Design](design/messaging.md) | Outbox, RabbitMQ topology, retries, dead letters, acknowledgements, and worker recovery |
| [API Design](design/api.md) | HTTP resources, authentication, authorization, idempotency, errors, and SSE contracts |
| [Diagnostic Tools](design/diagnostic-tools.md) | Remote SQL Server, database evidence, logs, and diagnostic Tool governance |
| [Frontend Design](design/frontend.md) | React workbench structure, design-token decisions, state-machine-to-UI mapping, and mock replacement |
| [Target OpenAPI](design/openapi.json) | Target machine-readable contract; implemented contract remains `api/openapi.yaml` |

## Decisions

| Document | Purpose |
| --- | --- |
| [ADR 001](decisions/001-modular-monolith-architecture.md) | Why MESGuard uses a modular monolith and manual dependency injection |
| [ADR 002](decisions/002-prefer-open-source-components.md) | Prefer mature open-source components; hand-write only thin contract glue |

Documentation rules:

- `README.md` at the repository root is only the project entry point.
- Operational instructions belong in `development.md`.
- Current implementation progress belongs in `roadmap.md`; Agent execution order
  belongs in `design/agent-implementation-plan.md`.
- Stable product and engineering boundaries belong in the reference documents;
  they must not be used to claim unimplemented features.
- `api/openapi.yaml` describes implemented HTTP endpoints. The target OpenAPI
  draft under `design/openapi.json` is not an implementation claim.
- Long-lived design decisions belong in `decisions/`.
- Do not duplicate the same command, status, or design explanation in multiple
  documents.
