# Architecture

MESGuard is a modular monolith with one deployable API process. Business use
cases depend on project-owned ports; Gin, GORM, Redis, SQL Server, and Eino stay
at the outer boundary.

```text
HTTP request / SSE client
          |
internal/transport/http
          |
internal/diagnosis
          |
RunStore | MESReader | Agent
          |
PostgreSQL | SQL Server | Eino adapters
```

## Repository Layout

```text
.
|-- cmd/
|   `-- mesguard-api/                 API executable and composition entry
|-- config/                           Local and Docker TOML configuration
|-- docs/                             Project documentation
|   |-- decisions/                    Architecture decision records
|   |-- architecture.md
|   |-- development.md
|   `-- roadmap.md
|-- infra/
|   |-- postgres/init/                Local PostgreSQL initialization
|   `-- sqlserver/init/               Synthetic MES schema and seed data
|-- internal/
|   |-- adapter/
|   |   |-- eino/                     diagnosis.Agent implementation
|   |   `-- postgres/diagnosis/       diagnosis.RunStore implementation
|   |-- bootstrap/                    Explicit dependency construction
|   |-- diagnosis/                    Domain models, use cases, and ports
|   |-- platform/
|   |   |-- config/                   Typed configuration loading
|   |   |-- migrate/                  Embedded versioned SQL migrations
|   |   |-- postgres/                 PostgreSQL connection lifecycle
|   |   `-- redis/                    Redis connection lifecycle
|   `-- transport/http/               Gin handlers, DTOs, health, and SSE
|-- docker-compose.yml
|-- Dockerfile.backend
|-- go.mod
`-- README.md
```

## Dependency Rules

1. `internal/diagnosis` must not import Gin, GORM, Redis, SQL Server drivers, or
   Eino.
2. HTTP DTOs, domain models, and persistence records are separate types when
   their requirements differ.
3. Interfaces are defined for external capabilities such as `RunStore`,
   `MESReader`, and `Agent`, not for every concrete type.
4. `cmd/mesguard-api` and `internal/bootstrap` are the only dependency assembly
   locations.
5. PostgreSQL is the durable system of record; Redis is never the sole source
   of diagnostic state.

The rationale and consequences are recorded in
[ADR 002](decisions/002-modular-monolith-architecture.md).
