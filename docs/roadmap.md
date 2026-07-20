# MESGuard Roadmap

## Current Stage: Web Foundation

- [x] `cmd/internal` project layout.
- [x] Typed TOML and `.env` configuration.
- [x] Manual dependency injection in `bootstrap`.
- [x] Gin Router and `/healthz` endpoint.
- [x] Application error-code enum and standard error type.
- [x] Unified success/error response structure.
- [x] Global request ID, error handling, panic recovery, and 404/405 responses.
- [x] Graceful HTTP, PostgreSQL, and Redis shutdown.

## Before Business Code

- [ ] Request DTO binding and validation conventions.
- [ ] Structured application logging and request-context fields.
- [ ] Configuration tests and startup failure tests.
- [ ] Decide authentication requirements for the diagnostic workbench.
- [ ] Define database migration command and transaction conventions.

## Business Work, Deliberately Deferred

- Diagnostic Run, Step, ToolCall, and Event models.
- SQL Server read-only evidence collection.
- Eino Agent execution and controlled tool calling.
- Retrieval, evaluation, and the diagnostic workbench UI.

These capabilities will be introduced one vertical slice at a time after the
Web foundation is understood and reviewed.
