# Local Development

The local development stack contains only MESGuard dependencies:

- PostgreSQL 16 with pgvector: diagnostic runs, events, knowledge metadata,
  evaluations, and long-term state.
- SQL Server 2022 Developer: synthetic company ERP tickets queried through the
  dedicated `mesguard_case_reader` account.
- Redis 7: short-lived state, locks, and cache. It is not the system of record.
- MinIO: private attachment and knowledge-source objects. PostgreSQL stores the
  immutable object references and lifecycle facts.
- StepFun Step Plan: OpenAI-compatible `step-3.7-flash` ChatModel for Agent runs.
- GitHub MCP: optional read-only code investigation tools.
- `backend`: the `cmd/mesguard-api` executable.

## Start

1. Copy [`../.env.compose.example`](../.env.compose.example) to `.env` if local values
   need to be overridden.
2. Start the stack:

```powershell
docker compose up -d --build
docker compose ps
```

The one-shot `migrate` service applies pending Goose migrations before the API
starts. For a direct Go/GoLand run, start PostgreSQL and apply migrations first:

```powershell
go run ./cmd/mesguard-migrate status
go run ./cmd/mesguard-migrate up
go run ./cmd/mesguard-migrate check
```

创建本地开发账号时，密码只从环境变量读取，不放在命令行参数中：

```powershell
$env:MESGUARD_INITIAL_USER_PASSWORD = "change-this-locally"
go run ./cmd/mesguard-user -username admin01 -display-name "系统管理员" -role admin
```

`mesguard-user` 会创建启用状态、首次登录必须改密的账号；密码不会写入日志或命令输出。登录接口为 `POST /api/v1/auth/login`，当前用户和退出接口分别为 `GET /api/v1/auth/me`、`POST /api/v1/auth/logout`。

3. Verify the API:

```powershell
Invoke-RestMethod http://127.0.0.1:9090/healthz
```

PostgreSQL is the critical fact store checked by `/healthz`. Redis, MinIO, the ERP SQL
Server, StepFun, and GitHub MCP are degradable dependencies: startup continues
when one is down, and only the affected capability fails. The SQL Server
connection pool retries on later requests, so recovery does not require an API
restart. MinIO initialization and bucket creation are retried on later uploads.
The API checks the required PostgreSQL migration version at startup
but never applies migrations itself.

## Agent providers

The ChatModel uses Step Plan's OpenAI-compatible endpoint with model
`step-3.7-flash`. Set `MESGUARD_STEPFUN_API_KEY` only in `.env`, GoLand run
configuration, Docker environment, or a production secret store. Do not put the
key in TOML or logs. When the key is absent, authentication and ticket APIs
remain available while the Agent runtime is disabled.

### Agent Prompt configuration

Agent instructions are stored under `config/prompts/` instead of Go constants:

- `diagnosis-system.md`: production Agent system instruction;
- `evaluation-baseline.md`: paired-evaluation baseline instruction;
- `report-contract.md`: Evidence Gate structured-report contract.

The `[agent]` block declares these three paths and a manually maintained
`promptVersion`. MESGuard reads, trims, validates, and caches each file once
while building the Agent runtime; a missing, empty, or larger-than-32-KiB file
fails Agent initialization. Prompt changes take effect after process restart.

## Knowledge ingestion

The `[knowledge]` block configures the current ingestion contract:

- `pipelineVersion`: persisted with every immutable source version and task;
- `maxAttempts`: bounded transient-failure attempts;
- `maxUploadBytes`: API upload limit, which must not exceed `[minio].maxObjectBytes`.
- `chunkMaxRunes` and `chunkOverlapRunes`: deterministic TXT/Markdown Chunk limits.
- `parserMaxDocumentUnits`: maximum PDF pages, XLSX worksheets, or PPTX slides;
- `parserMaxArchiveEntries`, `parserMaxExpandedBytes`, and `parserMaxXMLBytes`: OOXML
  archive and XML expansion limits;
- `parserMaxExtractedRunes`: per-document extracted-text budget;
- `parserMaxSpreadsheetRows` and `parserMaxSpreadsheetColumns`: per-worksheet limits.
- `parserMaxVisualAssets`, `parserMaxVisualAssetBytes`, and `parserMaxTotalVisualBytes`:
  bounded visual candidates and unique embedded-media bytes;
- `maxVisualEnrichments` and `minVisualPixels`: per-task visual-model budget and the
  decorative-image threshold.
- `[models.embedding]`: independent DashScope `text-embedding-v4` profile, 1024 dimensions,
  query/document input modes, batch size and bounded concurrency. The active profile is created
  by the Knowledge Worker and a different active profile cannot silently replace it.
- `[models.rerank]`: optional DashScope `qwen3-rerank` post-retrieval stage. It is disabled by
  default. `maxCandidates` is capped at 50; provider failure preserves retrieval order and marks
  the Tool result degraded. The fixed-set live probe is available through the evaluator's
  `-retriever rrf-rerank` mode; this run succeeded but the provider response did not expose a
  parseable `usage.total_tokens`, so cost remains unknown rather than estimated.

Administrator uploads use `multipart/form-data`, a UUID `Idempotency-Key`, the
authenticated Session, and CSRF protection. The API stages one bounded file in the
system temporary directory, computes SHA-256, validates its first format boundary,
uploads it to MinIO, and removes the temporary file before returning. A successful
response means the immutable object and PostgreSQL ingestion facts are durable; it
does not yet mean parsing has completed. `mesguard-outbox-relay` publishes the task,
and the independent `mesguard-knowledge-worker` consumes it with manual ACK and
confirmed retry/dead-letter copies. The current Executor supports UTF-8 TXT/Markdown,
embedded-text PDF, deterministic DOCX/XLSX/PPTX extraction, and bounded PNG/JPEG visual
assets. It verifies the immutable source, writes a JSON Element Artifact to MinIO, stages
searchable Chunks under lease fencing, and then publishes `ready/current` or `partial_ready`.
PDF pages and PPTX slides retain page numbers; DOCX headings, paragraphs and tables and
XLSX worksheet cell values use the same Element contract. PDF pages without embedded text
become page-level visual candidates; Office media keeps source-part, relationship, page,
dimensions and SHA-256 metadata, while unreferenced media is recorded but never sent to a
model. Office ZIP paths, entry counts, expanded bytes, XML sizes, visual bytes and visual
occurrences are bounded, and encrypted or malformed inputs fail permanently. OCR/VLM
processing is configuration-driven through `[models.ocr]` and `[models.vision]`. When
unavailable, native text is retained as `partial_ready`, while a visual-only source fails
instead of creating an empty searchable version. Bounded PNG smokes completed both the live
DashScope Vision path and the isolated `qwen-vl-ocr-latest` path. Direct PDF `file_url`
input was separately proven unsupported by the Eino OpenAI adapter and is now classified as
a permanent input-capability error; M2-A7 renders PDF pages locally before image-based
OCR/VLM. No OCR/VLM quality metric is claimed. Formula expressions, speaker notes and hidden
Sheet/Slide handling are not implemented.

Start or rebuild the runnable path with:

```powershell
docker compose up -d --build migrate backend outbox-relay knowledge-worker
docker compose ps backend outbox-relay knowledge-worker
```

Run the opt-in storage and messaging integrations against the local defaults with
`MESGUARD_TEST_POSTGRES_DSN`, `MESGUARD_TEST_RABBITMQ_URL`, and the three
`MESGUARD_TEST_MINIO_*` variables, then execute:

```powershell
go test -tags=integration ./internal/platform/postgres ./internal/platform/rabbitmq ./internal/platform/minio -count=1
```

These tests cover Artifact/Chunk fencing, FTS visibility, Publisher Confirm before
ACK, MinIO Source round trips, PDF page extraction, OOXML ordering and relationship
locations, visual routing, standalone-image validation, and parser resource limits. Local
service smoke tests have covered TXT/Markdown, all four deterministic PDF/Office paths,
and disabled visual enrichment behavior; they are functional proof, not a document-throughput
benchmark. OCR/Vision blocks require the configured API-key environment variable. Rebuild the
Knowledge Worker after profile changes and run a bounded provider smoke before using a new
provider/model combination in ingestion. M2-A7 keeps OCR/VLM cloud-hosted; see
`docs/decisions/003-local-onnx-layout-routing.md`.

### M2-A7 local layout development

The local page/region routing code path is connected to the Knowledge Worker but remains
disabled by default. Prepare verified local artifacts with:

```powershell
.\scripts\models\fetch_and_convert_pp_doclayout.ps1
.\scripts\runtime\fetch_onnxruntime.ps1 -Platform windows-x64
```

The model command verifies source artifacts, builds the pinned Linux converter, checks the
opset-17 ONNX output and rejects a SHA mismatch. The runtime command verifies the official
1.28.0 archive. For a native semantic smoke test:

```powershell
$env:MESGUARD_TEST_ONNX_RUNTIME_LIBRARY = '<absolute path to onnxruntime.dll>'
$env:MESGUARD_TEST_LAYOUT_MODEL = '<absolute path to pp-doclayout-m.onnx>'
$env:MESGUARD_TEST_LAYOUT_IMAGE = '<optional upstream or approved fixture JPEG>'
go test ./internal/platform/onnxlayout -run TestORTIntegration -v -count=1
```

Fetch the pinned public evaluation corpus and run the route/resource benchmark with:

```powershell
.\scripts\evaluation\fetch_layout_routing_corpus.ps1
.\scripts\evaluation\run_layout_routing_eval.ps1
```

The downloaded PDF/DOCX files, observations, summaries, logs, executable and resource samples
stay under ignored `output/evaluation/`. Git tracks the corpus contract in
`testdata/layout-routing-public-v1.corpus.json`, page annotations in
`testdata/layout-routing-public-v1.jsonl`, and the dated result interpretation in
`docs/evaluations/layout-routing-public-v1.md`. The evaluator also accepts
`-max-raster-pixels`, `-render-dpi`, `-intra-op-threads`, and `-inter-op-threads` overrides for
paired resource runs without changing the production profile.

Run a parser-only PPTX compatibility and throughput baseline against an approved local folder:

```powershell
.\scripts\evaluation\run_pptx_parse_eval.ps1 -InputRoot '<approved PPTX folder>'
```

This command performs no provider calls. It records file/slide/Element/table/media counts,
duration and allocation metrics under ignored `output/evaluation/`. For one-page OCR comparison,
the default is also dry-run and only renders the 20M and 8M candidates:

```powershell
go run ./cmd/mesguard-ocr-quality-eval -input '<approved PDF>' -page 8
```

Run the SHA-pinned, manually reviewed PPTX structure set separately:

```powershell
.\scripts\evaluation\run_pptx_element_quality_eval.ps1 `
  -InputRoot '<folder containing the reviewed PPTX files>'
```

It validates page-specific text anchors, DrawingML table counts/content, distinct slide image
relationships and relationship completeness. The source decks and rendered review images are not
committed. Repeated picture uses of the same relationship are counted separately in the review
metadata but intentionally collapse to one visual asset per slide relationship.

Adding `-execute-provider` performs exactly two OCR calls. Review current provider pricing and,
if necessary, override `-input-price-per-million-cny` and
`-output-price-per-million-cny`. Never use this evaluator for an unbounded VLM run.

The reviewed VLM region evaluator is also dry-run by default. It verifies the three source
SHA-256 values, writes only the bounded crops under ignored `output/evaluation/`, and reports a
maximum of six provider calls:

```powershell
.\scripts\evaluation\run_vlm_quality_eval.ps1 `
  -InputRoot '<folder containing the reviewed slide renders>'
```

Only add `-ExecuteProvider` after reviewing current prices and the printed budget. The command
uses the configured Qwen Vision endpoint and StepFun low-reasoning endpoint, the same Prompt and
strict JSON contract, and stops one provider after its first error. Raw provider text remains in
the ignored report. The fixed set and its manual-review caveats are recorded in
`docs/evaluations/knowledge-ingestion-quality-v1.md`.

### M2-A8 retrieval evaluation

The fixed retrieval set uses 12 industrial documents, 24 chunks and 24 literal/paraphrased
queries. It runs inside a rolled-back PostgreSQL transaction. Compare all three retrieval paths:

```powershell
go run ./cmd/mesguard-rag-retrieval-eval -retriever fts `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-fts.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-fts.summary.json

go run ./cmd/mesguard-rag-retrieval-eval -retriever vector `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-vector.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-vector.summary.json

go run ./cmd/mesguard-rag-retrieval-eval -retriever rrf `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-rrf.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-rrf.summary.json
```

Vector/RRF call the configured Embedding provider, so keep the corpus bounded and review the
printed/provider-reported Token usage before larger runs. Use
`-embedding-price-cny-per-million` only with the current provider price; without it the summary
does not claim a monetary cost. Methodology and the 2026-08-06 result table are recorded in
`docs/evaluations/rag-retrieval-v1.md`. This fixed set is correctness evidence, not a production
throughput or SLA benchmark.

`[knowledge.layout]` may be enabled only after `modelPath`, `manifestPath` and
`runtimeLibraryPath` resolve inside the Knowledge Worker environment. Artifact schema v5
then records model, renderer, requested/effective DPI, bbox, crop and explicit OCR/VLM routing
provenance plus `element-merge-v1` keep/suppress decisions. The first eight-page public run is
recorded, but its cloud-bound-region avoidance is a routing proxy rather than measured API/Token
savings. One bounded OCR pair, a nine-file PPTX parser benchmark, an eight-slide PPTX structure
set and a three-candidate ONNX thread A/B are recorded in
`docs/evaluations/knowledge-ingestion-quality-v1.md`; remaining work is broader OCR/VLM fixtures,
and cloud-enriched merge quality.

Prepare and build the optional Linux/amd64 layout Worker without placing model assets in Git or
the default backend build context:

```powershell
.\scripts\runtime\fetch_onnxruntime.ps1 -Platform linux-x64
.\scripts\models\fetch_and_convert_pp_doclayout.ps1
.\scripts\runtime\prepare_knowledge_worker_assets.ps1
docker compose -f docker-compose.yml -f docker-compose.layout.yml build knowledge-worker
```

The staging script verifies extracted file lengths and SHA-256 values and copies only the model,
runtime, asset manifest, and license notices into ignored `output/docker/knowledge-worker-assets`.
The Dockerfile verifies the binary hashes again. The overlay uses `linux/amd64`, a non-root user,
read-only root filesystem, dropped capabilities and `no-new-privileges`; it does not change the
default Compose path or turn `[knowledge.layout].enabled` on automatically.

Run the Linux fixed-set/resource gate with no network and 2 CPU/2 GiB limits:

```powershell
.\scripts\evaluation\run_linux_layout_routing_eval.ps1
```

The script writes summary, observations, GNU time output and machine-readable resources only
under ignored `output/evaluation/linux-layout-routing/`.

Increment `promptVersion` whenever a content change must be distinguishable in
persisted diagnosis reports or evaluation observations. The current mechanism
is intentionally file-based and does not provide hot reload or a Prompt release
platform.

Prompt and Skill text cannot grant capabilities. `TaskScope`, `ToolCatalog`,
argument policies, database accounts, and upstream credentials remain the
authorization boundary even if a Prompt file is edited incorrectly.

GitHub code investigation additionally requires `MESGUARD_GITHUB_MCP_TOKEN`.
If GitHub MCP cannot connect, `ticket-diagnosis` remains active and only
`code-investigation` is removed from the compiled Graph.

Public Web Search remains disabled by default. `[webSearch.redaction]` configures the
public-query input/output rune budgets. To add company or internal product names to
the deterministic dictionary, set `sensitiveTermsEnv` to an environment-variable
name and store comma/newline-separated terms in that variable; do not write the
terms themselves into TOML. Current ticket identifiers are added dynamically.
Credentials, connection strings, raw SQL/log/stack/JSON content, and over-redacted
queries are rejected rather than sent. The Firecrawl client and Tool are not wired
yet, so enabling the config at this checkpoint does not perform a public request.

`[knowledge.retrieval]` controls small-to-big context expansion. With
`contextExpansionEnabled=true`, retrieval and optional Rerank still operate on child
chunks; only final hits receive neighboring chunks from the same document version and
section. `contextWindow` is limited to 1-3 and `contextMaxRunes` to 128-8000. These are
server budgets, not Tool arguments. Expansion failure is reported as a degraded
`context` channel and does not discard the original hits.

`[knowledge.retrieval.queryRewrite]` controls the optional LLM Query Plan. It is disabled
by default. When enabled, the service loads `promptFile`, records `promptVersion`, applies
a 1-30 second internal timeout, accepts at most two subqueries, and bounds JSON output by
`maxOutputRunes`. The original query remains available to retrieval; deterministic policy
rejects rewrites that change protected error codes, versions, numbers, time constraints or
explicit negation. Provider failure, malformed JSON, policy rejection, or the rewriter's own
timeout falls back to the original query and marks only `query_rewrite` degraded. A canceled
caller context still stops the whole search. Prompt edits require a version increment.

The real-provider test below performs one billable chat-model request and forces Query Rewrite
on for the test even though production configuration remains disabled:

```powershell
go test -tags=integration ./internal/platform/queryrewrite `
  -run TestStepFunQueryRewritePreservesProtectedSignals -count=1
```

It verifies the strict JSON/protected-signal contract, not Recall, ranking quality, latency SLA,
or cost reduction. See `docs/evaluations/query-rewrite-v1.md` for the current smoke observations.

Model configuration is not currently a universal hot-plug layer. `[models.chat]` accepts only
`stepfun`; Judge, Embedding, Rerank, OCR and Vision accept only their registered DashScope path.
The domain interfaces are replaceable, but adding another provider still requires a Bootstrap
adapter and contract tests. In particular, do not point the current Chat config at DeepSeek and
assume `reasoningEffort` has the same meaning. Provider adapters must explicitly map or omit
reasoning controls and verify Tool Calling plus structured output.

Token accounting consumes provider Usage normalized by Eino. Prompt, completion and total values
are portable only when the provider returns them. Cached and reasoning values may remain zero when
the provider omits those details; zero is not yet an availability indicator. Embedding model changes
also require a new persisted profile and reindex rather than an in-place configuration swap. The
full role matrix and acceptance boundary are documented in
`docs/design/rag-ingestion-and-retrieval.md` under "模型 Provider 可替换性边界".

After configuring the StepFun key, run the provider smoke test once:

```powershell
go run ./cmd/mesguard-model-smoke
```

The command asks the model to return one harmless Tool Call and prints only the
model name, Tool name, and provider-reported Token usage. It does not execute
database, GitHub, or write-capable tools.

To verify the complete non-streaming ReAct loop and multi-call Token
aggregation against a fixed synthetic ticket, run:

```powershell
go run ./cmd/mesguard-agent-smoke
```

This command executes only the local read-only `read_external_case` Tool. It
does not connect to ERP, PostgreSQL, Redis, or GitHub. The printed report is a
local smoke result and is not an evaluation metric.

To aggregate a reproducible baseline/experiment pair, keep the versioned case
labels separate from each real run observation:

```powershell
go run ./cmd/mesguard-agent-eval `
  -dataset testdata/agent-evaluation.dataset.sample.jsonl `
  -input testdata/agent-evaluation.sample.jsonl
```

The command rejects mixed dataset versions, duplicate case/variant runs,
unknown JSON fields, and mismatched model/reasoning settings in a pair. The
checked-in `sample` files only verify the scoring program; they must not be
reported as production accuracy or Token reduction.

The SQL investigation runtime exposes `execute_readonly_query` only when both
the SQL Server pool and PostgreSQL Schema Catalog are available. Its query
policy is configured under `[sqlserver.investigation]`:
`maxQueryBytes`, `maxRows`, `maxResultBytes`, and `maxConcurrentQueries`.
The default unit suite covers the guard, Catalog authorization, result limits,
concurrency, and runtime EvidenceItem capture. The opt-in cross-database test
uses a temporary published Catalog row in PostgreSQL and the synthetic SQL
Server data; it also verifies that an uncatalogued base table is rejected before
SQL Server execution.

## ERP SQL Server

`sqlserver-seed` restores four deterministic ERP fault scenarios and creates a
reader login that can select only the two MESGuard integration views. The API
password is read from `MESGUARD_SQLSERVER_PASSWORD`; the `sa` password is used
only by the seed container.

Run the real SQL Server integration suite explicitly:

```powershell
$env:MESGUARD_TEST_SQLSERVER_DSN = "sqlserver://mesguard_case_reader:...@127.0.0.1:1433?database=SUPPORT_DEMO&encrypt=disable&TrustServerCertificate=true"
$env:MESGUARD_TEST_POSTGRES_DSN = "postgres://..."
go test -tags=integration ./internal/platform/sqlserver ./internal/platform/postgres -count=1 -v
```

The suite verifies mapping and also proves that `INSERT`, `UPDATE`, `DELETE`,
and `CREATE TABLE` are rejected by SQL Server. Default `go test ./...` does not
require Docker.

After login, discover the configured source before listing tickets:

```text
GET /api/v1/data-sources
GET /api/v1/external-cases?dataSourceId=<id>&page=1&pageSize=20
GET /api/v1/external-cases/<externalCaseId>
```

The complete implemented contract is in [`../api/openapi.yaml`](../api/openapi.yaml).

## Logging

Local configuration writes readable console logs and rotated JSON files under
`logs/`. Docker configuration writes JSON to stdout so the container runtime can
collect and rotate it. Every HTTP completion log includes `request_id`, method,
route, status, latency, response size, client IP, and error count.

## Repository and transactions

Business use cases depend on `internal/repository.TxManager` instead of GORM.
The PostgreSQL implementation is created with `postgres.NewTxManager(db)` and
passes the active transaction through `context.Context`:

```go
err := txManager.WithinTx(ctx, func(txCtx context.Context) error {
    // Every Repository call in this callback must receive txCtx.
    return userRepository.Save(txCtx, user)
})
```

PostgreSQL Repository adapters call `postgres.ResolveDB(ctx, db)` before each
query. Nested `WithinTx` calls join the existing transaction; they do not create
an independent commit or savepoint. Returning an error rolls the outer
transaction back, while returning `nil` commits it.

Transactions must contain database work only. SQL Server queries, MinIO,
RabbitMQ, Redis notifications, model calls, and other network operations run
before or after the transaction so database locks are not held during slow or
unreliable external calls.

Repository adapters translate framework-specific errors with
`postgres.TranslateError`. Use `errors.Is` with `repository.ErrNotFound` or
`repository.ErrConflict` in the application layer, then map them to the
appropriate application error code. Constraint-name-specific rules remain in
the owning Repository rather than in the generic translator.

Real transaction tests require an isolated or disposable PostgreSQL database:

```powershell
$env:MESGUARD_TEST_POSTGRES_DSN = "postgres://..."
go test ./internal/platform/postgres -run TestTxManagerAgainstPostgres -v
```

Stop the stack with `docker compose down`. Do not add `-v` unless the local
PostgreSQL, SQL Server, Redis, and MinIO data should be discarded deliberately.

## Volumes

Compose uses named volumes by default. To use host directories, set one or more
of these variables in `.env`:

```dotenv
MESGUARD_POSTGRES_DATA=D:/develop/docker_workspace/mesguard/postgres
MESGUARD_SQLSERVER_DATA=D:/develop/docker_workspace/mesguard/sqlserver
MESGUARD_REDIS_DATA=D:/develop/docker_workspace/mesguard/redis
MESGUARD_MINIO_DATA=D:/develop/docker_workspace/mesguard/minio
```
