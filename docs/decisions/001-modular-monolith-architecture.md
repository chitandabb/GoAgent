# 001. Modular Monolith and Manual Dependency Injection

## Decision

MESGuard is one modular Go codebase with shared domain modules and explicit
composition roots. The current Web foundation has one executable; the target
architecture adds API and background Worker runtime roles without splitting
the system into independently owned microservices.

The current foundation uses:

- `cmd/mesguard-api` as the executable entry;
- `internal/bootstrap` as the manual dependency-injection location;
- `internal/apperror` for framework-independent error semantics;
- `internal/transport/http` for Gin, middleware, and response formatting;
- `internal/platform` for configuration and infrastructure clients.

Future business modules will be added under `internal/<module>` only when their
use cases are implemented.

The target runtime uses:

- an API role for HTTP, SSE, authentication, queries, and streaming knowledge chat;
- a Diagnosis Worker role for Outbox relay, RabbitMQ consumption, and Eino diagnosis;
- an Ingestion Worker role, introduced in M2, for document parsing, OCR, ONNX,
  multimodal descriptions, and indexing.

API and Diagnosis Worker are built from the same base backend image. The
Ingestion Worker comes from the same repository but may use an enhanced image
containing native document-processing and ONNX Runtime dependencies. All roles
share domain code and PostgreSQL facts; they do not communicate through a new
internal service API.

## Rationale

Go does not require a dependency-injection container. Constructor calls in one
composition root make object ownership, startup failures, and shutdown order
visible in ordinary code. Keeping application errors independent from Gin also
allows future HTTP, CLI, worker, and test entry points to share the same error
semantics.

## Consequences

- There is no component scanning or annotation-driven injection.
- Each runtime role has its own manual composition root and shutdown order.
- API and Workers can restart or scale independently while sharing one codebase.
- Adding a runtime role does not grant it an independent database ownership boundary.
- Global middleware owns HTTP error serialization.
- Business code will not write `gin.Context` responses directly.
- Interfaces will be introduced at real external boundaries, not mechanically
  for every struct.
