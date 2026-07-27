# MESGuard

MESGuard will become a Go service for evidence-based MES ticket diagnosis. The
project is currently at the Web foundation stage so that each Go engineering
concept can be introduced and reviewed independently.

## Current Status

The current API exposes only:

- `GET /healthz`

The Web shell already provides request IDs, unified success/error responses,
application error codes, structured Zap logging, global error handling, panic
recovery, and graceful shutdown. No MES business API is registered yet.

Successful responses use this shape:

```json
{
  "code": 0,
  "message": "success",
  "data": {},
  "requestId": "..."
}
```

## Quick Start

Use Go `1.25.3`, create a local `.env` from `.env.compose.example`, and run from
the repository root:

```powershell
docker compose up -d postgres sqlserver sqlserver-seed redis
go run ./cmd/mesguard-migrate up
go run ./cmd/mesguard-api
```

Run the Go test suite with:

```powershell
go test ./cmd/... ./db/... ./internal/...
```

## Documentation

All project documentation is indexed from [docs/README.md](docs/README.md):

- Web request flow, dependency injection, and directory responsibilities;
- local development and Docker operation;
- product roadmap;
- architecture decision records.

The root directory intentionally contains only the project entry document.
