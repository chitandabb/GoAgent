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

### M2-B1 paired Advanced RAG aggregation

After a versioned advanced dataset and its baseline/experiment observations have been generated,
validate and summarize them without making any provider or database call:

```powershell
go run ./cmd/mesguard-rag-paired-eval `
  -dataset <versioned-cases.jsonl> `
  -input <paired-observations.jsonl> `
  -output output/evaluation/rag-advanced.summary.json
```

The dataset labels relevant evidence by stable document key, chunk ordinal and content SHA-256.
Every case must contain exactly one baseline and one experiment with the same retriever, embedding
profile, rerank profile, channels and K. A pair must change exactly one axis in the fixed direction:
`original -> rewrite` with unchanged context, `child -> parent` with unchanged query mode, or
uncompressed Parent -> bounded whole-Chunk compression with unchanged Query/retriever settings and pinned
`maxChunks/maxRunes/minScore`. The summary reports Hit Rate@K, document Recall@K/MRR, Context
Precision/Recall, query amplification, compression input/output Chunk and rune counts, omissions,
context-rune and duration changes, rewrite statuses and provider-reported Token usage.

The domain `AdvancedRetrievalObserver` converts two runtime Search arms into strict observations and
counts non-cancellation search failures instead of dropping them. The offline aggregator does not
create PostgreSQL fixtures or call providers.

Validate the checked-in public-source corpus and its gold Chunk hashes without a database or provider:

```powershell
go run ./cmd/mesguard-rag-paired-observe -validate-only
```

Provider execution is fail-closed and defaults to one Case. Review the printed request budget before
adding `-execute-provider`; context pairs call Embedding, while rewrite pairs call Embedding and the
configured ChatModel. Both arms use the production `BuildKnowledgeSearchService` and all fixture rows
are rolled back in one PostgreSQL transaction. Exact commands and the current observations
are in `docs/evaluations/rag-advanced-v1.md`.

Run the production compression thresholds across the complete five-Case fixture without Rewrite or Rerank:

```powershell
go run ./cmd/mesguard-rag-paired-observe `
  -execute-provider -axis compression -retriever rrf -max-cases 5 -timeout 3m
```

The 2026-08-07 run used at most three document-embedding and ten query-embedding requests. It omitted zero
chunks because the fixture averaged only 2.6 parent neighbors and 575.4 runes per Case, below the production
six-chunk/3000-rune cap. Treat this as a wiring result, not measured savings. Add long-parent pressure Cases
before changing thresholds or reporting a compression rate.

Run the separate official-document pressure fixture with an acceptance gate:

```powershell
go run ./cmd/mesguard-rag-paired-observe `
  -corpus testdata/rag-compression-pressure-v1.corpus.json `
  -dataset testdata/rag-compression-pressure-v1.jsonl `
  -execute-provider -axis compression -require-compression-acceptance `
  -max-cases 1 -timeout 3m
```

The gate rejects zero-omission runs and any Gold Context Recall regression. Three accepted 2026-08-07
runs consistently changed seven neighbors/1507 runes to six neighbors/1438 runes while keeping Context
Recall at 1.0. This single stress Case verifies production-threshold behavior only; do not report its 4.58%
neighbor-rune saving as an aggregate Token reduction.

Evidence Gate re-retrieval is run-scoped. On the second Agent run, evidence/source-binding gaps retain
`search_knowledge` with a one-call limit; format-only gaps remove it from the Tool schema. The same total
Tool, Token and timeout budgets still apply. Diagnosis reports expose `agenticRetrievalAttempted`,
`agenticRetrievalAddedEvidence` and `agenticRetrievalStopReason`; unit tests cover eligibility, the one-call
limit, schema filtering and stable version/Chunk/content-hash detection.

Run the real-model decision fixture only after reviewing the ChatModel budget:

```powershell
go run ./cmd/mesguard-agentic-retrieval-eval `
  -execute-provider -max-cases 3 -timeout 90s
```

The 2026-08-07 three-Case run used `stepfun/step-3.7-flash`: evidence gaps selected one
`search_knowledge` call and added a stable version/Chunk/hash; format-only repair did not receive the Tool;
a valid first pass did not call the provider. Attempt precision/recall and stop-reason accuracy were 1.0 on
this fixture, with 16453 total Tokens. The default 16000 Token budget is per Case. Provider Usage is settled
after each response, so a single completed response can exceed the remaining total budget before cancellation
prevents another action. Treat these three seeded control Cases as wiring evidence, not answer-quality or
general accuracy proof; the observations and full boundary are in `docs/evaluations/rag-advanced-v1.md`.

### M2-C knowledge-ingestion throughput evaluation

Fetch or verify the pinned public corpus under the ignored evaluation directory. The manifest records publisher,
source page, HTTPS download URL, usage boundary, byte length and SHA-256; the script refuses mismatches unless
`-Force` is explicitly supplied:

```powershell
.\scripts\evaluation\fetch_rag_ingestion_corpus.ps1
```

Validate the pinned public corpus without infrastructure or Provider calls:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe -validate-only -max-documents 40
```

Run the production Parser and Chunking locally, classify text/visual readiness, and write a JSON audit
without infrastructure or Provider calls:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe -audit-only -max-documents 40
```

Parse locally and print the expected baseline/experiment Embedding request counts, conservative Token estimate
and whole-run CNY cost without sending a request:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe -estimate-only -max-documents 40
```

Select a bounded low-cost subset by stable manifest ID instead of relying on manifest order:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe -estimate-only `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster"

go run ./cmd/mesguard-ingestion-throughput-observe `
  -estimate-only -document-concurrency-ablation `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster" `
  -repetitions 5
```

Isolate PostgreSQL Chunk/vector staging without any Provider or object-store call:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe `
  -database-ablation -max-documents 3 -repetitions 5 -timeout 15m

go run ./cmd/mesguard-ingestion-throughput-eval `
  -input output/evaluation/rag-ingestion-db-ablation-v1.observations.jsonl `
  -output output/evaluation/rag-ingestion-db-ablation-v1.summary.json `
  -target-increase-percent 40
```

This mode parses the pinned real files before timing, uses deterministic normalized vectors only on non-current
staging versions, and times `SaveParsedResult` with write batch 1 versus the configured production batch. It
never calls the configured Embedding endpoint. The 2026-08-07 five-pair run over 3 documents/743 Chunks measured
1752 ms versus 406 ms median staging duration; this database-only result remains outside full-chain acceptance.

Real execution requires PostgreSQL, MinIO, the configured Embedding key and explicit authorization. Before
opening those dependencies, the command performs the same local Parser/Chunk estimate. The default whole-command
budget is CNY 0.05 at CNY 0.5 per million Embedding input Tokens; exceeding it fails before any paid request.
Provider starts are smoothed to 900 RPM and 600,000 estimated TPM by default. These are evaluation safety defaults,
not account quota claims, and can be changed only with explicit flags:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe `
  -execute-provider -max-documents 1 -repetitions 1 -timeout 15m `
  -max-provider-cost-cny 0.05 -provider-rpm 900 -provider-tpm 600000

go run ./cmd/mesguard-ingestion-throughput-eval `
  -input output/evaluation/rag-ingestion-throughput-v1.observations.jsonl `
  -output output/evaluation/rag-ingestion-throughput-v1.summary.json `
  -target-increase-percent 40
```

The observer creates fresh MinIO/database facts for each arm and cleans them after measurement, but it does
not clear provider, operating-system or PostgreSQL caches. It excludes RabbitMQ delivery, OCR/VLM and layout
routing. The experiment combines the existing Embedding batch/concurrency with batched Chunk/vector INSERTs;
use a separate staging benchmark before attributing a gain to database batching. Acceptance requires 40 real
documents, all eight declared `formatClass` values and five integrity-preserving pairs. The current bounded
five-pair concurrency result remains ineligible for that full-scale gate even though its observed increase exceeds 40%; see
`docs/evaluations/rag-ingestion-throughput-v1.md`.

The worker-core observer atomically removes its own `knowledge.ingest` Outbox row in the queue transaction.
This is deliberate isolation: a live Outbox Relay/Knowledge Worker must not claim a task that the observer will
execute directly. Per-document task status, Worker action/reason, Elements, Chunks and Embedding Tokens are stored
in every new observation.

Run the provider-backed document-concurrency ablation with identical Embedding/database batching on both arms:

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe `
  -execute-provider -document-concurrency-ablation `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster" `
  -repetitions 5 -timeout 15m `
  -max-provider-cost-cny 0.06 -provider-rpm 900 -provider-tpm 600000 `
  -output output/evaluation/rag-ingestion-document-concurrency-budgeted-v1.observations.jsonl
```

The budget-protected 2026-08-07 five-pair run changed only document concurrency `1 -> 2`. Preflight estimated
100,830 Tokens/CNY 0.0504 and the manually reviewed cap was CNY 0.06; actual usage was 80 requests, 96,060 Tokens
and approximately CNY 0.04803, with no OCR/VLM calls. Median duration changed from 2124 ms to 1450 ms and median
document throughput increased 46.48%, while every arm retained 42 searchable Elements, 70 Chunks, 8 Embedding
requests, 9606 Tokens and 2+2 Chunk/vector batches. `IntegrityPreserved=true` and temporary actor/document residue
was `0|0`. This supports the bounded worker-core 40%+ claim, but remains ineligible for the 40-document/eight-format
full-scale gate because the selected workload contains only two documents and two format classes.

The zero-cost audit now covers 40 public documents and all 8 format classes with 5,946 raw Elements,
5,854 searchable Elements, 12,864 Chunks, 139 visual candidates and zero parser failures. It applies the production
Element merge before Chunking and records 92 suppressed duplicate/nonsemantic Elements. It counts only materialized visual bytes; PDF
page candidates reference the immutable source and are not charged the whole PDF once per page. The 40-document
corpus requires 12,864 Embedding requests with batch 1 or 1,306 with batch 10. Document and format coverage are
complete, but five full-chain pairs are still missing, so the acceptance gate remains false. NIST AMS 100-32 was
removed from the positive corpus after its page text extraction failed to terminate within 40 seconds; treat it as
a parser-isolation fixture instead of silently increasing the worker timeout.

The first 40-document provider pair is invalid. The concurrency-1 arm completed 12,864 Chunks in 280,459 ms with
1,306 requests and 2,585,532 Tokens. The concurrency-2 arm hit DashScope `429 Throttling.AllocationQuota` on 11
documents and produced only 10,923 Chunks, so the apparent +141.57% throughput is rejected by
`IntegrityPreserved=false`. Both arms issued 2,412 HTTP requests and consumed 4,668,907 Tokens, approximately
CNY 2.3345 at the Beijing synchronous price. The experiment averaged 571.6 RPM but 1,076,707 TPM; the official
limits are 1,800 RPM and 1,200,000 TPM, so rolling Token bursts, not a permanently exhausted key quota, explain
the 429. A first 429 now cancels the evaluation, and the 40-document corpus remains provider-free unless its
preflight cost receives explicit approval.

Docker Compose must pass `DASHSCOPE_API_KEY` to both `knowledge-worker` and `diagnosis-worker`. The first
creates document vectors during ingestion; the second creates query vectors during diagnosis retrieval.
If only the Knowledge Worker receives the key, ingestion can succeed while diagnosis silently logs
`knowledge vector search unavailable` and degrades to FTS. Recreate the affected application container after
changing environment variables; no database or object-store volume reset is required.

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

Public Web Search is enabled in the tracked runtime profiles but remains an optional,
fail-closed dependency. Its search and content providers are configured independently
with `searchProvider` and `contentProvider` under `[webSearch]`. The legacy single
`provider`, `baseURL` and `apiKeyEnv` fields remain accepted for migration. A missing
or rejected provider credential hides `web_search`/`fetch_public_page`; diagnosis
continues with its other evidence channels. New diagnosis tasks receive the
backend-managed `web_search` capability together with `knowledge`; users do not select
either Tool.

`[webSearch.redaction]` configures public-query input/output rune budgets. To add
company or internal product names to the deterministic dictionary, set
`sensitiveTermsEnv` to an environment-variable name and store comma/newline-separated
terms in that variable; do not write the terms themselves into TOML. Current ticket
identifiers are added dynamically. Credentials, connection strings, raw
SQL/log/stack/JSON content, and over-redacted queries are rejected rather than sent.

Search and page extraction are separate Provider contracts. A run may perform at most
two searches, receive five candidates per search, and fetch three pages.
`fetch_public_page` accepts only the opaque `resultId` returned by the same run, not a
URL. Targets and final provider-reported URLs are restricted to HTTP/HTTPS on public
DNS/IP addresses and normal ports; local, private, link-local, reserved, mixed
public/private DNS and credential-bearing URLs are rejected. Responses are capped at
2 MiB, and page text is capped at 20,000 characters.

The Firecrawl adapter calls `/v2/search` and `/v2/scrape`, returns provider Markdown
with `onlyMainContent=true`, and relies on Firecrawl for redirects that are not
observable to MESGuard. The SearXNG adapter calls its local JSON `/search` endpoint
and does not require a SaaS API key; its upstream engines can still rate-limit or
challenge the self-hosted instance. The Direct content adapter accepts HTML, XHTML,
plain text and JSON, extracts bounded visible text without executing scripts, and
re-validates every redirect through the MESGuard URL policy. JavaScript-heavy pages,
PDFs and unsupported content types should use a configured managed content Provider.
All snippets/pages remain untrusted.

All contract and security tests are offline:

```powershell
go test ./internal/webresearch ./internal/platform/firecrawl ./internal/platform/directweb ./internal/platform/searxng ./internal/agent ./internal/bootstrap
```

The opt-in live smoke spends exactly one Search and one Scrape request and performs no
automatic retry. It loads `FIRECRAWL_API_KEY` from the environment or project `.env`:

```powershell
$env:MESGUARD_TEST_FIRECRAWL_LIVE="1"
go test ./internal/platform/firecrawl -run TestFirecrawlLiveSearchAndScrape -count=1 -v
```

`[knowledge.retrieval]` controls small-to-big context expansion. With
`contextExpansionEnabled=true`, retrieval and optional Rerank still operate on child
chunks; only final hits receive neighboring chunks from the same document version and
section. `contextWindow` is limited to 1-3 and `contextMaxRunes` to 128-8000. These are
server budgets, not Tool arguments. Expansion failure is reported as a degraded
`context` channel and does not discard the original hits.

`[knowledge.retrieval.contextCompression]` runs after expansion. With `enabled=true`, it selects only
complete neighbor chunks under `maxChunks` (1-40), `maxRunes` (128-32000) and `minScore` (0-1); the
checked-in production values are 6, 3000 and 0.05. Matched child chunks are outside this added-context
budget. The Tool cannot override these settings, and compression never rewrites evidence text or hashes.

`[knowledge.retrieval.queryRewrite]` controls the optional LLM Query Plan. It is disabled
by default. `modelProfile` selects a named `[models.chat.profiles.<name>]` independently of
the main Agent `activeProfile`. When enabled, the service loads `promptFile`, records
`promptVersion`, applies a 1-30 second internal timeout, accepts at most two subqueries, and
bounds JSON output by `maxOutputRunes`. The checked-in `qwen-rewrite` candidate disables
thinking, uses temperature 0, a 3-second provider/rewrite timeout, 256 output tokens, and one
subquery. The original query remains available to retrieval; deterministic policy
rejects rewrites that change protected error codes, versions, numbers, time constraints or
explicit negation. Provider failure, malformed JSON, policy rejection, or the rewriter's own
timeout falls back to the original query and marks only `query_rewrite` degraded. A canceled
caller context still stops the whole search. Prompt edits require a version increment.

The real-provider test below performs one billable chat-model request and forces Query Rewrite
on for the test even though production configuration remains disabled:

```powershell
go test -tags=integration ./internal/platform/queryrewrite `
  -run TestConfiguredQueryRewriteProfilePreservesProtectedSignals -count=1
```

It verifies the strict JSON/protected-signal contract, not Recall, ranking quality, latency SLA,
or cost reduction. See `docs/evaluations/query-rewrite-v1.md` for the current smoke observations.

`[models.chat]` is a named-profile model assembly layer. `activeProfile` selects the diagnosis
Agent model; other roles resolve their own profile. The Factory currently registers StepFun,
DeepSeek and DashScope adapters and returns normalized profile/provider/model/capability identity.
All configured profiles are statically validated, but only a selected profile reads its API key.
StepFun maps `reasoningEffort`; DeepSeek requires explicit thinking mode and rejects effort while
thinking is disabled; DashScope maps thinking mode to `enable_thinking`. Unsupported combinations
fail during construction instead of being silently ignored.

This is configuration-level replaceability, not runtime hot reload or production acceptance for
every model. Config/key changes require restart. Judge, Embedding, Rerank, OCR and Vision still use
their role-specific adapters. A new Chat Provider still requires an Adapter, offline request-shape
tests and live capability/quality acceptance.

Token accounting consumes provider Usage normalized by Eino. Prompt, completion and total values
are portable only when the provider returns them. Cached and reasoning values may remain zero when
the provider omits those details; zero is not yet an availability indicator. Embedding model changes
also require a new persisted profile and reindex rather than an in-place configuration swap. The
full role matrix and acceptance boundary are documented in
`docs/design/rag-ingestion-and-retrieval.md` under "模型 Provider 可替换性边界".

The 2026-08-07 DeepSeek adapter maps the documented OpenAI-compatible
`thinking={type: enabled|disabled}` request and optional thinking effort. Thinking remains explicit
because the provider default is enabled. The pinned Eino OpenAI adapter has the fields needed to
carry `reasoning_content`, but no live DeepSeek Tool loop has been run. Do not promote a DeepSeek
profile to `activeProfile` until non-streaming/streaming Tool loops, strict JSON, Usage, cancellation,
Evidence Gate and paired quality probes pass. The exact acceptance matrix and official links are in
the same design section.

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
