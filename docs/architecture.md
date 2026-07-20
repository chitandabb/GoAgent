# Architecture

MESGuard currently contains only the reusable Web foundation. Business modules
will be added after the Web request lifecycle is understood and accepted.

## Current Request Flow

```text
cmd/mesguard-api/main.go
        |
        v
internal/bootstrap/app.go          manual dependency injection
        |
        v
Gin Router
        |
        +-- RequestID middleware
        +-- Structured request logger
        +-- Recovery middleware
        +-- ErrorHandler middleware
        |
        v
GET /healthz
        |
        v
unified JSON Response
```

## Spring Boot Mapping

| Spring Boot concept | Current Go location |
| --- | --- |
| `SpringApplication.run` | `cmd/mesguard-api/main.go` |
| `@Configuration` / `@Bean` | `internal/bootstrap/app.go` |
| `@ConfigurationProperties` | `internal/platform/config/config.go` |
| `@RestControllerAdvice` | `internal/transport/http/middleware.go` |
| SLF4J/Logback configuration | `internal/platform/logger` and `[log]` TOML configuration |
| Common response object | `internal/transport/http/response.go` |
| Business exception and error enum | `internal/apperror` |
| Controller and route registration | `internal/transport/http/router.go` |
| DataSource/Redis client creation | `internal/platform/postgres` and `redis` |

## Repository Layout

```text
.
|-- cmd/
|   `-- mesguard-api/                 executable entry
|-- config/                           local and Docker TOML configuration
|-- docs/                             all project documentation
|-- infra/                            local database initialization data
|-- internal/
|   |-- apperror/                     error-code enum and application errors
|   |-- bootstrap/                    manual dependency construction
|   |-- platform/
|   |   |-- config/                   typed configuration loading
|   |   |-- logger/                   Zap construction and request context
|   |   |-- postgres/                 PostgreSQL connection lifecycle
|   |   `-- redis/                    Redis connection lifecycle
|   `-- transport/http/
|       |-- middleware.go             request ID, errors, panic recovery
|       |-- response.go               unified JSON responses
|       `-- router.go                 Gin creation and basic routes
|-- docker-compose.yml
|-- Dockerfile.backend
|-- go.mod
`-- README.md
```

## Dependency Rules

1. `main` loads configuration, creates the process-wide Logger, asks `bootstrap`
   to construct the app, and starts it.
2. `main` creates the shared Logger first; `bootstrap` explicitly injects it
   into HTTP and database infrastructure.
3. Handlers report errors with `c.Error`; the global middleware writes the
   response.
4. Application errors do not import Gin or `net/http`.
5. Future business modules will live under `internal/<module>` and will not
   import Gin, GORM, or concrete external clients directly.

The rationale is recorded in
[ADR 001](decisions/001-modular-monolith-architecture.md).
