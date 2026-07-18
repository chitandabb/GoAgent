# 002. MESGuard modular-monolith architecture

## Context

The original GopherAI demo separates code into global `controller`, `service`,
`dao`, and `model` directories. It also relies on package-level configuration,
database connections, and AI helper singletons. That layout makes a feature
change cross unrelated directories and couples business tests to Gin, GORM, or
an LLM implementation.

## Decision

MESGuard is a single deployable Go service organised as a modular monolith:

- `cmd/mesguard-api` is the composition root and default executable.
- `internal/diagnosis` owns diagnostic runs, events, use cases, and its ports.
- `internal/transport/http` is the Gin and SSE adapter.
- `internal/adapter/*` contains PostgreSQL and Eino implementations.
- `internal/platform/*` owns configuration, clients, and versioned migrations.
- Legacy GopherAI remains compilable through `cmd/legacy-api` while it is
  progressively retired.

The diagnosis domain does not import Gin, GORM, Redis, SQL Server, or Eino.
Ports are introduced only at these external boundaries, not for every type.

## Consequences

- Application tests use in-memory port implementations and run without Docker.
- PostgreSQL persists `diagnostic_runs` and ordered `diagnostic_events` as the
  source of truth; SSE replays those persisted events.
- `AutoMigrate` is not used for MESGuard tables. Embedded, ordered SQL
  migrations provide repeatable schema evolution during this development phase.
- The first vertical slice creates and retrieves a queued run. Actual MES
  querying and Agent execution are follow-up use cases behind `MESReader` and
  `Agent` ports.
