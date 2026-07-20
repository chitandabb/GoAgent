# MESGuard

MESGuard is a Go service for evidence-based MES ticket diagnosis. It treats the
MES database as a read-only evidence source and stores diagnostic state,
execution events, and later evaluations in PostgreSQL.

## Current Scope

The first vertical slice is implemented:

- `GET /healthz`
- `POST /api/v1/diagnostic-runs`
- `GET /api/v1/diagnostic-runs/:runID`
- `GET /api/v1/diagnostic-runs/:runID/events` for persisted SSE replay

Creating a run writes the run and its first `run.created` event in one
PostgreSQL transaction. Agent execution and SQL Server diagnostic tools are the
next use cases, not implied by the current API.

## Architecture

```text
Gin HTTP/SSE adapter
        |
diagnosis application module
        |
ports: RunStore, MESReader, Agent
        |
PostgreSQL / SQL Server / Eino adapters
```

- `cmd/mesguard-api`: composition root and API executable.
- `internal/diagnosis`: business models, use cases, and external-boundary ports.
- `internal/adapter`: implementations for PostgreSQL and Eino.
- `internal/platform`: configuration, connections, and versioned migrations.
- `internal/transport/http`: Gin handlers, request/response DTOs, and SSE.
- `infra`: local PostgreSQL and synthetic SQL Server initialization data.

The business module does not import Gin, GORM, Redis, SQL Server drivers, or
Eino. This keeps the diagnostic use cases testable with in-memory adapters and
makes infrastructure changes local to their adapters.

## Local Development

Use Go `1.25.3` from the repository root. Create a local `.env` from
`.env.compose.example`, then start the dependencies:

```powershell
docker compose up -d postgres sqlserver sqlserver-seed redis
go run ./cmd/mesguard-api
```

Run all tests with:

```powershell
go test ./...
```

For a complete Docker API process, run `docker compose up -d --build backend`.

## Documentation

- [Development environment](README.compose.md)
- [Roadmap](ROADMAP.md)
- [Architecture decision records](docs/decisions)
