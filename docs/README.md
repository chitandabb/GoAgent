# MESGuard Documentation

This directory is the single location for project documentation.

| Document | Purpose |
| --- | --- |
| [Architecture](architecture.md) | Dependency direction, package boundaries, and repository layout |
| [Development](development.md) | Local configuration, Docker, tests, and volume operation |
| [Roadmap](roadmap.md) | Current delivery state and ordered next milestones |
| [Product and Workflow](design/product-and-workflow.md) | Target users, product boundaries, business flows, and staged delivery |
| [Domain and State Machine](design/domain-and-state-machine.md) | Domain objects, lifecycle states, invariants, and event rules |
| [System Architecture](design/system-architecture.md) | Runtime roles, component topology, data flows, deployment, and failure boundaries |
| [Delivery Plan](design/delivery-plan.md) | Milestones, dependencies, acceptance criteria, and implementation order |
| [Database Design](design/database.md) | PostgreSQL tables, constraints, indexes, migrations, and transaction boundaries |
| [Messaging Design](design/messaging.md) | Outbox, RabbitMQ topology, retries, dead letters, acknowledgements, and worker recovery |
| [API Design](design/api.md) | HTTP resources, authentication, authorization, idempotency, errors, and SSE contracts |
| [Diagnostic Tools](design/diagnostic-tools.md) | Remote SQL Server, database evidence, logs, and diagnostic Tool governance |
| [Agent Orchestration](design/agent-orchestration.md) | Single-Agent loop, thin Graph, Skill/Tool boundaries, GitHub MCP, and evaluation metrics |
| [Agent Implementation Plan](design/agent-implementation-plan.md) | Ordered ADK migration, acceptance gates, deletion rules, and resume checklist |
| [Frontend Design](design/frontend.md) | React workbench structure, design-token decisions, state-machine-to-UI mapping, and mock replacement |
| [ADR 001](decisions/001-modular-monolith-architecture.md) | Why MESGuard uses a modular monolith and manual dependency injection |
| [ADR 002](decisions/002-prefer-open-source-components.md) | Prefer mature open-source components; hand-write only thin contract glue |

Documentation rules:

- `README.md` at the repository root is only the project entry point.
- Operational instructions belong in `development.md`.
- Current implementation progress belongs in `roadmap.md`; milestone design and
  acceptance criteria belong in `design/delivery-plan.md`.
- Long-lived design decisions belong in `decisions/`.
- Do not duplicate the same command, status, or design explanation in multiple
  documents.
