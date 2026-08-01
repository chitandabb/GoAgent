# Local Development

The local development stack contains only MESGuard dependencies:

- PostgreSQL 16 with pgvector: diagnostic runs, events, knowledge metadata,
  evaluations, and long-term state.
- SQL Server 2022 Developer: synthetic company ERP tickets queried through the
  dedicated `mesguard_case_reader` account.
- Redis 7: short-lived state, locks, and cache. It is not the system of record.
- StepFun Step Plan: OpenAI-compatible `step-3.7-flash` ChatModel for Agent runs.
- GitHub MCP: optional read-only code investigation tools.
- `backend`: the `cmd/mesguard-api` executable.

## Start

1. Copy [`../.env.compose.example`](../.env.compose.example) to `.env` if local values
   need to be overridden.
2. Start the stack:

```powershell
docker compose up -d --build
docker compose ps
```

The one-shot `migrate` service applies pending Goose migrations before the API
starts. For a direct Go/GoLand run, start PostgreSQL and apply migrations first:

```powershell
go run ./cmd/mesguard-migrate status
go run ./cmd/mesguard-migrate up
go run ./cmd/mesguard-migrate check
```

创建本地开发账号时，密码只从环境变量读取，不放在命令行参数中：

```powershell
$env:MESGUARD_INITIAL_USER_PASSWORD = "change-this-locally"
go run ./cmd/mesguard-user -username admin01 -display-name "系统管理员" -role admin
```

`mesguard-user` 会创建启用状态、首次登录必须改密的账号；密码不会写入日志或命令输出。登录接口为 `POST /api/v1/auth/login`，当前用户和退出接口分别为 `GET /api/v1/auth/me`、`POST /api/v1/auth/logout`。

3. Verify the API:

```powershell
Invoke-RestMethod http://127.0.0.1:9090/healthz
```

PostgreSQL is the critical fact store checked by `/healthz`. Redis, the ERP SQL
Server, StepFun, and GitHub MCP are degradable dependencies: startup continues
when one is down, and only the affected capability fails. The SQL Server
connection pool retries on later requests, so recovery does not require an API
restart. The API checks the required PostgreSQL migration version at startup
but never applies migrations itself.

## Agent providers

The ChatModel uses Step Plan's OpenAI-compatible endpoint with model
`step-3.7-flash`. Set `MESGUARD_STEPFUN_API_KEY` only in `.env`, GoLand run
configuration, Docker environment, or a production secret store. Do not put the
key in TOML or logs. When the key is absent, authentication and ticket APIs
remain available while the Agent runtime is disabled.

GitHub code investigation additionally requires `MESGUARD_GITHUB_MCP_TOKEN`.
If GitHub MCP cannot connect, `ticket-diagnosis` remains active and only
`code-investigation` is removed from the compiled Graph.

After configuring the StepFun key, run the provider smoke test once:

```powershell
go run ./cmd/mesguard-model-smoke
```

The command asks the model to return one harmless Tool Call and prints only the
model name, Tool name, and provider-reported Token usage. It does not execute
database, GitHub, or write-capable tools.

To verify the complete non-streaming ReAct loop and multi-call Token
aggregation against a fixed synthetic ticket, run:

```powershell
go run ./cmd/mesguard-agent-smoke
```

This command executes only the local read-only `read_external_case` Tool. It
does not connect to ERP, PostgreSQL, Redis, or GitHub. The printed report is a
local smoke result and is not an evaluation metric.

To aggregate a reproducible baseline/experiment pair, keep the versioned case
labels separate from each real run observation:

```powershell
go run ./cmd/mesguard-agent-eval `
  -dataset testdata/agent-evaluation.dataset.sample.jsonl `
  -input testdata/agent-evaluation.sample.jsonl
```

The command rejects mixed dataset versions, duplicate case/variant runs,
unknown JSON fields, and mismatched model/reasoning settings in a pair. The
checked-in `sample` files only verify the scoring program; they must not be
reported as production accuracy or Token reduction.

The SQL investigation runtime exposes `execute_readonly_query` only when both
the SQL Server pool and PostgreSQL Schema Catalog are available. Its query
policy is configured under `[sqlserver.investigation]`:
`maxQueryBytes`, `maxRows`, `maxResultBytes`, and `maxConcurrentQueries`.
The default unit suite covers the guard, Catalog authorization, result limits,
concurrency, and runtime EvidenceItem capture. The opt-in cross-database test
uses a temporary published Catalog row in PostgreSQL and the synthetic SQL
Server data; it also verifies that an uncatalogued base table is rejected before
SQL Server execution.

## ERP SQL Server

`sqlserver-seed` restores four deterministic ERP fault scenarios and creates a
reader login that can select only the two MESGuard integration views. The API
password is read from `MESGUARD_SQLSERVER_PASSWORD`; the `sa` password is used
only by the seed container.

Run the real SQL Server integration suite explicitly:

```powershell
$env:MESGUARD_TEST_SQLSERVER_DSN = "sqlserver://mesguard_case_reader:...@127.0.0.1:1433?database=SUPPORT_DEMO&encrypt=disable&TrustServerCertificate=true"
$env:MESGUARD_TEST_POSTGRES_DSN = "postgres://..."
go test -tags=integration ./internal/platform/sqlserver ./internal/platform/postgres -count=1 -v
```

The suite verifies mapping and also proves that `INSERT`, `UPDATE`, `DELETE`,
and `CREATE TABLE` are rejected by SQL Server. Default `go test ./...` does not
require Docker.

After login, discover the configured source before listing tickets:

```text
GET /api/v1/data-sources
GET /api/v1/external-cases?dataSourceId=<id>&page=1&pageSize=20
GET /api/v1/external-cases/<externalCaseId>
```

The complete implemented contract is in [`../api/openapi.yaml`](../api/openapi.yaml).

## Logging

Local configuration writes readable console logs and rotated JSON files under
`logs/`. Docker configuration writes JSON to stdout so the container runtime can
collect and rotate it. Every HTTP completion log includes `request_id`, method,
route, status, latency, response size, client IP, and error count.

## Repository and transactions

Business use cases depend on `internal/repository.TxManager` instead of GORM.
The PostgreSQL implementation is created with `postgres.NewTxManager(db)` and
passes the active transaction through `context.Context`:

```go
err := txManager.WithinTx(ctx, func(txCtx context.Context) error {
    // Every Repository call in this callback must receive txCtx.
    return userRepository.Save(txCtx, user)
})
```

PostgreSQL Repository adapters call `postgres.ResolveDB(ctx, db)` before each
query. Nested `WithinTx` calls join the existing transaction; they do not create
an independent commit or savepoint. Returning an error rolls the outer
transaction back, while returning `nil` commits it.

Transactions must contain database work only. SQL Server queries, MinIO,
RabbitMQ, Redis notifications, model calls, and other network operations run
before or after the transaction so database locks are not held during slow or
unreliable external calls.

Repository adapters translate framework-specific errors with
`postgres.TranslateError`. Use `errors.Is` with `repository.ErrNotFound` or
`repository.ErrConflict` in the application layer, then map them to the
appropriate application error code. Constraint-name-specific rules remain in
the owning Repository rather than in the generic translator.

Real transaction tests require an isolated or disposable PostgreSQL database:

```powershell
$env:MESGUARD_TEST_POSTGRES_DSN = "postgres://..."
go test ./internal/platform/postgres -run TestTxManagerAgainstPostgres -v
```

Stop the stack with `docker compose down`. Do not add `-v` unless the local
PostgreSQL, SQL Server, and Redis data should be discarded deliberately.

## Volumes

Compose uses named volumes by default. To use host directories, set one or more
of these variables in `.env`:

```dotenv
MESGUARD_POSTGRES_DATA=D:/develop/docker_workspace/mesguard/postgres
MESGUARD_SQLSERVER_DATA=D:/develop/docker_workspace/mesguard/sqlserver
MESGUARD_REDIS_DATA=D:/develop/docker_workspace/mesguard/redis
```
