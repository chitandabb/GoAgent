# MESGuard Current Progress

This file tracks the repository's current implementation state. Target
milestones, dependencies, and acceptance criteria are defined in
[`design/delivery-plan.md`](design/delivery-plan.md).

## Current Stage: Web Foundation

- [x] `cmd/internal` project layout.
- [x] Typed TOML and `.env` configuration.
- [x] Manual dependency injection in `bootstrap`.
- [x] Gin Router and `/healthz` endpoint.
- [x] Application error-code enum and standard error type.
- [x] Unified success/error response structure.
- [x] Global request ID, error handling, panic recovery, and 404/405 responses.
- [x] Structured Zap logging, request-context fields, and optional file rotation.
- [x] Graceful HTTP, PostgreSQL, and Redis shutdown.
- [x] React workbench prototype in `web/` running on local mock data only; see
      [`design/frontend.md`](design/frontend.md). No backend business API is
      implemented yet.

## M0: Before Business Code

- [x] Request DTO binding and validation conventions.
- [ ] Configuration tests and startup failure tests.
- [ ] Add the local-account authentication skeleton and analyst/admin roles.
- [x] Define database migration command and transaction conventions.

## Target Milestones, Not Yet Implemented

- M1: evidence-based ticket diagnosis;
- M2: knowledge assistant, RAG, and mixed-document ingestion;
- M3: restricted code investigation;
- M4: isolated SQL performance laboratory.

Do not mark a target milestone as complete here until its acceptance criteria
in the delivery plan have been verified.
