# MESGuard Docker Compose

## Target Development Environment

The default Compose environment is the target MESGuard foundation:

- `postgres`: PostgreSQL 16 with pgvector. It will hold Agent runs, events,
  knowledge metadata, long-term memory, and evaluation data.
- `sqlserver`: SQL Server 2022 Developer edition. It is the simulated MES
  business database that future diagnostic tools may query with read-only
  credentials.
- `sqlserver-seed`: an idempotent one-shot initializer that creates
  `SUPPORT_DEMO` and `MES_DEMO` plus a minimal synthetic ticket.
- `redis`: short-lived run state, semantic cache, rate limits, and locks. It
  is not the long-term Agent system of record.

The default `mesguard-api` also starts from this Compose file. It applies
versioned MESGuard migrations, persists diagnostic runs and events in
PostgreSQL, and exposes `/healthz` plus `/api/v1/diagnostic-runs`. SQL Server
remains isolated as the database being diagnosed.

## Start

1. Start Docker Desktop.
2. Optionally copy [`.env.compose.example`](.env.compose.example) to `.env` and
   change ports, passwords, or volume locations.
3. Start the target infrastructure:

```powershell
docker compose up -d
docker compose ps
```

Expected ports are PostgreSQL `5432`, SQL Server `1433`, and Redis `6379`.
`sqlserver-seed` should finish with exit code `0`; it is an initializer, not a
long-running service.

Verify the setup:

```powershell
docker compose exec postgres psql -U mesguard -d mesguard -c "SELECT extname FROM pg_extension WHERE extname = 'vector';"
docker compose exec sqlserver /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P 'MESGuard_Dev!2026' -Q "SELECT name FROM sys.databases WHERE name IN ('SUPPORT_DEMO', 'MES_DEMO');"
docker compose exec redis redis-cli ping
```

Use the values from `.env` instead of the example password when you override
them.

Stop the environment with `docker compose down`. Add `-v` only when you intend
to discard all local PostgreSQL, SQL Server, and Redis data.

## Legacy GopherAI Environment

MySQL, RabbitMQ, and the weather MCP server are retained only for inspecting
legacy modules. The default backend is still `mesguard-api`; the old Vue and
chat routes are not a supported runtime combination with it. These services
stay behind a Compose profile:

```powershell
docker compose --profile legacy up -d --build
```

This profile adds the legacy MySQL, RabbitMQ, MCP demo, and Vue frontend for
source-level comparison. The image-recognition route is disabled in the default
build and requires the explicit `onnx` build tag, the ONNX Runtime library, and
model files; it is not part of the MESGuard baseline.
