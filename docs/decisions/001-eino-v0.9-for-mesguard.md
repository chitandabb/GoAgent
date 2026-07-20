# 001. Eino v0.9 for MESGuard

## Decision

MESGuard uses Eino `v0.9.12` and the official OpenAI-compatible model extension
`v0.1.13`. The adapter is intentionally small: it constructs a
`ToolCallingChatModel` and exposes the project-owned `diagnosis.Agent` port.

## Rationale

Eino provides model and orchestration components. It does not own MESGuard Run
state, SQL safety, cancellation, event ordering, or SSE replay. Keeping Eino
behind an adapter lets the application test diagnostic use cases without an API
key or an LLM process, and it leaves provider changes local to one package.

## Follow-up

Native Eino tool calling will be introduced only with controlled MES tools. The
first tools must be read-only, parameterized, auditable, and bounded by timeout,
row, and cost limits.

## Sources

- https://www.cloudwego.io/zh/docs/eino/
- https://github.com/cloudwego/eino/releases
