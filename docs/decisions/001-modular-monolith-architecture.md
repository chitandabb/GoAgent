# 001. Modular Monolith and Manual Dependency Injection

## Decision

MESGuard is one Go deployable organized as a modular monolith. The current Web
foundation uses:

- `cmd/mesguard-api` as the executable entry;
- `internal/bootstrap` as the manual dependency-injection location;
- `internal/apperror` for framework-independent error semantics;
- `internal/transport/http` for Gin, middleware, and response formatting;
- `internal/platform` for configuration and infrastructure clients.

Future business modules will be added under `internal/<module>` only when their
use cases are implemented.

## Rationale

Go does not require a dependency-injection container. Constructor calls in one
composition root make object ownership, startup failures, and shutdown order
visible in ordinary code. Keeping application errors independent from Gin also
allows future HTTP, CLI, worker, and test entry points to share the same error
semantics.

## Consequences

- There is no component scanning or annotation-driven injection.
- Global middleware owns HTTP error serialization.
- Business code will not write `gin.Context` responses directly.
- Interfaces will be introduced at real external boundaries, not mechanically
  for every struct.
