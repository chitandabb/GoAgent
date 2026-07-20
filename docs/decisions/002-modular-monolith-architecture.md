# 002. Modular Monolith With Explicit Adapters

## Decision

MESGuard is one Go deployable organized as a modular monolith:

- `cmd/mesguard-api` is the composition root.
- `internal/diagnosis` owns diagnostic terminology, use cases, and ports.
- `internal/transport/http` adapts Gin HTTP and SSE to those use cases.
- `internal/adapter/*` implements external ports.
- `internal/platform/*` owns operational concerns such as configuration,
  connections, and migrations.

## Rationale

Gin, GORM, Redis, SQL Server, and Eino are useful frameworks and clients, but
they are external details. A diagnostic use case should depend on a `RunStore`,
`MESReader`, or `Agent` purpose, rather than a concrete HTTP or database API.
This makes tests independent of infrastructure and prevents framework tags from
becoming the domain model.

## Consequences

New work is added as a vertical business slice. HTTP DTOs, PostgreSQL records,
and domain models remain separate when their requirements differ. Interfaces
are introduced only at replaceable external boundaries; the project does not
create interfaces merely to mirror every implementation.
