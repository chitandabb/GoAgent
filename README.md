# MESGuard

MESGuard is a Go service for evidence-based MES ticket diagnosis. It treats the
MES database as a read-only evidence source and stores diagnostic runs and
ordered execution events in PostgreSQL.

## Current Status

The first vertical slice is available:

- `GET /healthz`
- `POST /api/v1/diagnostic-runs`
- `GET /api/v1/diagnostic-runs/:runID`
- `GET /api/v1/diagnostic-runs/:runID/events`

Creating a run atomically persists the run and its first `run.created` event.
SQL Server evidence collection and Agent execution are the next milestones.

## Quick Start

Use Go `1.25.3`, create a local `.env` from `.env.compose.example`, and run from
the repository root:

```powershell
docker compose up -d postgres sqlserver sqlserver-seed redis
go run ./cmd/mesguard-api
```

Run the test suite with `go test ./...`.

## Documentation

All project documentation is indexed from [docs/README.md](docs/README.md):

- architecture and directory responsibilities;
- local development and Docker operation;
- product roadmap;
- architecture decision records.

The root directory intentionally contains only the project entry document.
