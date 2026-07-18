# Eino v0.9 Upgrade And Learning Map

## Decision

Use the latest stable Eino release, `v0.9.12`, rather than the current
`v0.10.0-alpha.*` prereleases.

The official v0.9 migration notes state that unlisted new capabilities normally
do not affect existing `*schema.Message` paths. This project uses that path and
does not yet use ADK middleware, Agent Transfer, Workflow Agent, Supervisor, or
summarization middleware. The upgrade is therefore limited to core and the
official component modules already used by the project.

## Version Set

| Module | Before | After |
| --- | --- | --- |
| `github.com/cloudwego/eino` | `v0.5.14` | `v0.9.12` |
| OpenAI chat model component | `v0.1.4` | `v0.1.13` |
| Ollama chat model component | `v0.1.5` | `v0.1.9` |
| Ark embedding component | `v0.1.0` | `v0.1.2` |
| Redis indexer/retriever | 2025 pseudo-version | 2026 pseudo-version |

## Current Code Mapping

| Eino concept | Current implementation | What it does |
| --- | --- | --- |
| `schema.Message` | `common/aihelper/model.go` | Common message representation passed to models. |
| `ToolCallingChatModel` | `OpenAIModel`, `OllamaModel`, `AliRAGModel`, `MCPModel` | Model interface for synchronous and streaming generation. |
| Chat model component | `openai.NewChatModel`, `ollama.NewChatModel` | Provider-specific client construction. |
| `Embedder` | `common/rag/rag.go` | Turns document content and queries into vectors. |
| `Indexer` / `Retriever` | `common/rag/rag.go` | Writes to and queries the legacy Redis vector store. |

The current MCP path is not Eino Tool Calling. It asks the model for a JSON
payload, parses it in application code, calls MCP, and then asks the model for a
second answer. It remains a legacy compatibility path.

## Learning Order

1. Read the official quick start and identify `ChatModel`, `schema.Message`,
   `Generate`, and `Stream` in the current `OpenAIModel` implementation.
2. Learn `Tool` and native Tool Calling. Implement MESGuard `get_ticket` as the
   first controlled tool; do not use prompt-JSON parsing for new features.
3. Learn `compose.Graph` for deterministic nodes and conditional branches.
4. Learn ADK `ChatModelAgent` only after Run/Step/ToolCall/Event persistence
   exists. The framework may drive a ReAct loop, but PostgreSQL remains the
   application system of record.
5. Add Eino callbacks around model and tool calls for trace timing. Keep
   cancellation, approval, SQL safety, event sequencing, and idempotency in the
   application layer.

## Boundaries

Eino provides reusable components and orchestration. It does not replace the
MESGuard domain model, SQL Server least-privilege connections, approval state,
SSE event replay, or the evaluation dataset.

Do not adopt `AgenticMessage` just because it is newer. Official v0.9 migration
guidance recommends retaining the default `*schema.Message` path unless the
selected model provider specifically needs the native Agentic protocol.

## Verification

Run from the repository root with the selected project Go toolchain:

```powershell
go test ./...
```

The upgrade was verified with Go `1.25.3` and the local PostgreSQL development
service. The database integration test creates, reads, and cleans up a user,
session, and message through the migrated DAO path.

## Official Sources

- https://www.cloudwego.io/zh/docs/eino/
- https://www.cloudwego.io/zh/docs/eino/release_notes_and_migration/eino_v0.9._agentic-runtime/eino_v0.9_migration_notes/
- https://github.com/cloudwego/eino/releases
