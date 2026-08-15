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

The `[agent]` block declares the diagnosis paths plus the independent conversation
prompt path and their manually maintained version labels. MESGuard reads, trims, validates, and caches each file once
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
docker compose up -d --build migrate backend outbox-relay knowledge-worker conversation-worker
docker compose ps backend outbox-relay knowledge-worker conversation-worker
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
go run ./tools/evaluation/mesguard-ocr-quality-eval -input '<approved PDF>' -page 8
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
go run ./tools/evaluation/mesguard-rag-retrieval-eval -retriever fts `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-fts.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-fts.summary.json

go run ./tools/evaluation/mesguard-rag-retrieval-eval -retriever vector `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-vector.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-vector.summary.json

go run ./tools/evaluation/mesguard-rag-retrieval-eval -retriever rrf `
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
go run ./tools/evaluation/mesguard-rag-paired-eval `
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
go run ./tools/observation/mesguard-rag-paired-observe -validate-only
```

Provider execution is fail-closed and defaults to one Case. Review the printed request budget before
adding `-execute-provider`; context pairs call Embedding, while rewrite pairs call Embedding and the
configured ChatModel. Both arms use the production `BuildKnowledgeSearchService` and all fixture rows
are rolled back in one PostgreSQL transaction. Exact commands and the current observations
are in `docs/evaluations/rag-advanced-v1.md`.

Run the production compression thresholds across the complete five-Case fixture without Rewrite or Rerank:

```powershell
go run ./tools/observation/mesguard-rag-paired-observe `
  -execute-provider -axis compression -retriever rrf -max-cases 5 -timeout 3m
```

The 2026-08-07 run used at most three document-embedding and ten query-embedding requests. It omitted zero
chunks because the fixture averaged only 2.6 parent neighbors and 575.4 runes per Case, below the production
six-chunk/3000-rune cap. Treat this as a wiring result, not measured savings. Add long-parent pressure Cases
before changing thresholds or reporting a compression rate.

Run the separate official-document pressure fixture with an acceptance gate:

```powershell
go run ./tools/observation/mesguard-rag-paired-observe `
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
go run ./tools/evaluation/mesguard-agentic-retrieval-eval `
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
go run ./tools/observation/mesguard-ingestion-throughput-observe -validate-only -max-documents 40
```

Run the production Parser and Chunking locally, classify text/visual readiness, and write a JSON audit
without infrastructure or Provider calls:

```powershell
go run ./tools/observation/mesguard-ingestion-throughput-observe -audit-only -max-documents 40
```

Parse locally and print the expected baseline/experiment Embedding request counts, conservative Token estimate
and whole-run CNY cost without sending a request:

```powershell
go run ./tools/observation/mesguard-ingestion-throughput-observe -estimate-only -max-documents 40
```

Select a bounded low-cost subset by stable manifest ID instead of relying on manifest order:

```powershell
go run ./tools/observation/mesguard-ingestion-throughput-observe -estimate-only `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster"

go run ./tools/observation/mesguard-ingestion-throughput-observe `
  -estimate-only -document-concurrency-ablation `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster" `
  -repetitions 5
```

Isolate PostgreSQL Chunk/vector staging without any Provider or object-store call:

```powershell
go run ./tools/observation/mesguard-ingestion-throughput-observe `
  -database-ablation -max-documents 3 -repetitions 5 -timeout 15m

go run ./tools/evaluation/mesguard-ingestion-throughput-eval `
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
go run ./tools/observation/mesguard-ingestion-throughput-observe `
  -execute-provider -max-documents 1 -repetitions 1 -timeout 15m `
  -max-provider-cost-cny 0.05 -provider-rpm 900 -provider-tpm 600000

go run ./tools/evaluation/mesguard-ingestion-throughput-eval `
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
go run ./tools/observation/mesguard-ingestion-throughput-observe `
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
was `0|0`. This supports the bounded worker-core 46.48% claim, but remains ineligible for the 40-document/eight-format
full-scale gate because the selected workload contains only two documents and two format classes.

The 2026-08-09 resume-closure pair adds scanned PDF, PPTX, XLSX and PNG under the unchanged default CNY 0.05
hard budget. Run it only after loading the local `.env`; a local CLI also needs the same MinIO development
credentials that Compose expands by default. The observer itself does not load `.env`.

```powershell
go run ./tools/observation/mesguard-ingestion-throughput-observe `
  -audit-only `
  -document-ids "uspto-us4575330,icsarw-shikhaliyev-poster,nist-sp800-171r2-security-requirements,wikimedia-cnc-lathe" `
  -audit-output output/evaluation/rag-ingestion-corpus-audit-resume-closure-v1.json

go run ./tools/observation/mesguard-ingestion-throughput-observe `
  -estimate-only -document-concurrency-ablation `
  -document-ids "uspto-us4575330,icsarw-shikhaliyev-poster,nist-sp800-171r2-security-requirements,wikimedia-cnc-lathe" `
  -repetitions 1 -max-provider-cost-cny 0.05

go run ./tools/observation/mesguard-ingestion-throughput-observe `
  -execute-provider -document-concurrency-ablation `
  -document-ids "uspto-us4575330,icsarw-shikhaliyev-poster,nist-sp800-171r2-security-requirements,wikimedia-cnc-lathe" `
  -repetitions 1 -timeout 15m -max-provider-cost-cny 0.05 `
  -output output/evaluation/rag-ingestion-resume-closure-v1.observations.jsonl
```

Preflight estimated 46 requests, 97,306 Tokens and CNY 0.0487. Actual usage was 46 requests, 50,838 Tokens and
approximately CNY 0.025419. Both arms retained the same `1 succeeded / 1 partial / 2 failed` set, 36 searchable
Elements and 223 Chunks; the two failed inputs are the expected scanned-PDF/PNG fail-closed result while visual
processors are absent. `IntegrityPreserved=true`; duration changed from 6492 ms to 5498 ms and document throughput
increased 18.08%. This one pair closes representative cross-format integrity only. It does not satisfy or replace
the 40-document/eight-format/five-pair production-scale gate, and it made no OCR/VLM calls.

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

Docker Compose must pass `DASHSCOPE_API_KEY` to `knowledge-worker`, `diagnosis-worker` and
`conversation-worker`. The first creates document vectors during ingestion; the latter two create query vectors
during diagnosis or conversational knowledge retrieval. If only the Knowledge Worker receives the key, ingestion
can succeed while Agent retrieval silently degrades to FTS. Recreate the affected application container after
changing environment variables; no database or object-store volume reset is required.

`[knowledge.layout]` may be enabled only after `modelPath`, `manifestPath` and
`runtimeLibraryPath` resolve inside the Knowledge Worker environment. Artifact schema v6
then records model, renderer, requested/effective DPI, bbox, crop, explicit OCR/VLM/Table routing,
structured table cells and `element-merge-v1` keep/suppress decisions. The first eight-page public run is
recorded, but its cloud-bound-region avoidance is a routing proxy rather than measured API/Token
savings. One bounded OCR pair, a nine-file PPTX parser benchmark, an eight-slide PPTX structure
set and a three-candidate ONNX thread A/B are recorded in
`docs/evaluations/knowledge-ingestion-quality-v1.md`; remaining work is broader OCR/VLM fixtures,
and cloud-enriched merge quality.

Structured table recovery uses the independent `[models.table]` profile and
`config/prompts/table-recovery.md`. It currently shares the configured DashScope API credential but
does not share the Vision Prompt or response schema. `table_recovery` regions produce Markdown plus
bounded cell coordinates/spans/header flags in Element metadata; only Markdown is projected into
PostgreSQL Chunk content. If the table processor is unavailable, generic OCR/VLM text is allowed only
as a `partial` fallback. Provider-free verification is:

```powershell
go test ./internal/knowledgetable ./internal/platform/tablemodel `
  ./internal/knowledgeenrichment ./internal/knowledgeingestion ./internal/knowledge -count=1
```

These tests include a mixed page with native text, a table, a picture and a decorative region. They
do not call DashScope and are not table-quality evidence. A paid observation must first validate the
crop and estimate cost:

```powershell
go run ./tools/evaluation/mesguard-table-quality-eval `
  -mode validate `
  -input output/evaluation/layout-routing-preview/nist-8107-page-15.png `
  -bbox 0.10691961899302364,0.11920245006831005,0.9089730455984477,0.30269819317442
```

The 2026-08-09 bounded NIST observation stopped after two `qwen3-vl-plus` calls. It used 2,507
Tokens and approximately CNY 0.014432 in total. Both responses preserved searchable text but
collapsed three visible rows beneath a vertically merged cell. A Prompt-only retry did not repair
the structure. The adapter therefore treats multiline cell content or `<br>` as
`multiline_cell_structure_ambiguous`, forces `partial`, caps confidence at 0.8 and emits a stable
warning. This is a truthful degradation gate, not a general table-accuracy result. Raw execution
details and the current enhancement boundary are in `docs/evaluations/table-recovery-v1.md`.

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

Prompt and Skill text cannot grant capabilities. The Tool Profile (model-visible
Schema) is the startup assembly snapshot of one process-start/deployment Epoch:
its content is fixed by which Adapters complete construction at startup, and
after the Runtime starts, current message references, `TaskScope`/`RunAccess`
narrowing and temporary dependency health never remove Schema. A restart that
fails to construct an Adapter is a new startup Profile/Epoch with a new Tool
Schema fingerprint; do not mix its evaluation data with the old Epoch. The
execution-time Permission Guard is wired; the unified `ResourceGrant`
projection and Tool-internal checks land in the `turn_context` + Conversation
Text-to-SQL slice, and existing attachment/task Tools keep their
`CommandContext`/owner checks until then. `TaskScope`, `ToolCatalog`, argument
policies, database accounts, and upstream credentials remain the authorization
boundary even if a Prompt file is edited incorrectly.

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
Agent model, while `conversationMemoryProfile` independently selects the future fast conversation
compaction model without duplicating Provider credentials. Other roles resolve their own profile. The Factory currently registers StepFun,
DeepSeek and DashScope adapters and returns normalized profile/provider/model/capability identity.
All configured profiles are statically validated, but only a selected profile reads its API key.
StepFun maps `reasoningEffort`; DeepSeek requires explicit thinking mode and rejects effort while
thinking is disabled; DashScope maps thinking mode to `enable_thinking`. Unsupported combinations
fail during construction instead of being silently ignored.

Structured output is capability-negotiated per Provider. StepFun and DashScope declare
`json_object` plus strict `json_schema`; DeepSeek declares only the officially documented
`json_object` capability. The checked-in `stepfun-conversation-memory` Profile requires
`responseFormat = "json_schema"` and `responseSchema = "conversation_memory_v2"`. A Provider
without strict Schema support is rejected during model construction instead of silently weakening
the memory contract. Provider-native formatting is still followed by strict Go decoding and domain
validation; valid JSON alone is not sufficient to activate a memory Snapshot.

Use the dedicated bounded protocol Smoke before any long-input Conversation Memory Pilot:

```powershell
go run ./tools/smoke/mesguard-conversation-memory-smoke
go run ./tools/smoke/mesguard-conversation-memory-smoke -execute-provider -timeout 60s
```

The first command is Provider-free and prints the conservative input bound. The second permits exactly
one StepFun call, never invokes the main model, never writes or activates a Snapshot, and validates the
same strict Schema, decoder, and domain contract used by production. Its hard conservative input limit
is 3,000 Tokens after accounting for the complete field-level Schema; changing that limit is not part of
ordinary debugging.

The bounded Smoke passed on 2026-08-12 after separating Provider-supported Schema keywords from Go
domain validation: one StepFun call, 224 prompt Tokens, 1,837 completion Tokens, 2,061 total Tokens,
14.8 seconds, and `domainValidated=true`. This is a protocol/short-input acceptance, not evidence for
long-summary latency or the 60%+ Context Governance metric. Smoke failure output is content-free and
uses stable stages/codes such as `provider_http_400`, `provider_timeout`, `entry_entry_id`, and
`entry_status`.

Every enabled named Chat profile also declares `contextWindowTokens`, `maxOutputTokens`, absolute
and ratio prompt safety margins, and a `tokenizerStrategy`. Validation requires output plus the
effective safety margin to leave positive input capacity. `toolExposureStrategy` defaults to
`static_frozen` and can represent `native_deferred`, `epoch_rebind`, or `gateway` for later Provider
adapters; `providerNativeCompactionEnabled` is capability configuration only.

`[agent.contextMemory]` enables preflight observation with configurable soft/hard thresholds and a
Tool-growth reserve. `continuousTailEnabled=true` activates Token-aware history selection, while
`tailMaxRatio` caps the Tail at 20% of the model window (the checked-in value is 15%). The
current User Message is always retained and counted; earlier messages are selected backwards as one
continuous sequence, and selection stops at the first message that does not fit. Case, diagnosis-task
and attachment references use bounded model-visible descriptions and stable identifiers, so their
Token cost is included without copying object payloads into history. Turning the Feature Flag off
returns to `conversationMaxContextRunes` for staged rollback.

Preflight has a separate short timeout that defaults to 250 ms; the model-run timeout starts after this
observation, so an estimator timeout cannot cancel or consume the model execution window. A failed
observation stays non-blocking but produces a bounded degraded Manifest with a stable machine reason
instead of disappearing. Explicit identity/estimate availability flags prevent an unknown Tool
contract or failed estimate from being counted as an empty Epoch or zero-Token sample. The local-first
estimator and Provider-independent planner observe the selected messages, model-visible canonical Tool
contract, system prompt, optional Skill/Summary segments, dynamic references and current user message.
They persist one bounded
`conversation_prompt_manifests` record with the Prompt Epoch and stable fingerprints, visible-prompt
estimate, reserve-inclusive upper bound, first Provider call Usage/Cache tokens, signed estimation
error and latency. If a later ReAct call is blocked, that same turn Manifest is updated to the blocked
runtime estimate, marked `react_prompt_blocked`, and leaves actual usage unavailable for the unsent call;
the Run observation still carries aggregate usage from earlier successful calls. The reserve is
intentionally excluded from calibration error, and Cache Hit tokens
never reduce window occupancy. When Continuous Tail is active and the conservative initial-prompt bound
exceeds the hard model window, MESGuard persists the blocked Manifest and does not call the Provider.
The per-turn model boundary then re-runs the same conservative Planner before every subsequent ReAct
call after Tool results have been appended. A failed estimator is fail-closed only after Continuous Tail
activation; observation-only rollout remains fail-open. Oversized Tool output is kept only in a bounded,
in-memory store owned by the current Agent Run. The model receives a preview, stable `sha256` reference
and original byte count, then can page the exact result through `read_conversation_tool_result`. The Tool
is always present in the frozen Conversation Tool Schema, is authorized by TaskScope, limits each read,
cannot search the store, and cannot resolve a reference after the Run ends. Results above the bounded
store capacity fail safely instead of creating an unresolvable handle. Diagnosis now reuses the same
Provider-independent preflight and persists bounded high-water/block observations without adding
conversation memory to diagnosis tasks.

The M3 Context Governance Pilot has a checked-in provider-free fixture and separate evaluation/observation
commands. Validate the 4-scenario/12-checkpoint fixture or print the conservative plan without loading model
configuration:

```powershell
go run ./tools/evaluation/mesguard-context-governance-pilot `
  -validate-only `
  -fixture testdata/context-governance-pilot-v1.json

go run ./tools/observation/mesguard-context-governance-pilot-observe
```

The default provider-free observer prints the full 36-main/12-Summary capacity plan and does not read the
config or call a Provider. Provider execution has separate fail-closed defaults: one main call, one Summary
call, 130K cumulative estimated prompt Tokens for each class, `0.50 CNY`, and concurrency 1. Summary retries
are planned from configured `maxAttempts`; every attempt reserves class-specific calls, prompt Tokens, and
cost before Provider access.

Only after explicitly approving one bounded probe and loading the required key into the current shell, execute
a single selected Experiment checkpoint:

```powershell
go run ./tools/observation/mesguard-context-governance-pilot-observe `
  -execute-provider `
  -scenario-id incident-correction `
  -checkpoint-id incident-cp2 `
  -arm experiment `
  -output output/evaluation/context-governance-pilot-v1.single-probe.jsonl
```

The command does not load `.env` itself. A wider selection is rejected before model creation unless all
relevant limits are explicitly raised. Do not raise them during debugging; inspect the single observation
first. `60%+` is the original target, not a pass-at-all-costs gate. A valid, explicitly approved fixed set must
report the measured end-to-end result including Summary usage. Also report main-model prompt reduction,
over-window continuation, duplicate-compaction suppression, latency, cost and quality; replace the resume
number when the measured result differs.

The bounded `cp2 Experiment` probe after stable error classification made one real Summary call: 61,759
prompt Tokens, 6,144 completion Tokens and 47.861 seconds, with zero main-model calls. The first attempt was
`output_truncated`; the second attempt was stopped locally because two conservative 88,727-Token reservations
were assumed to exceed the 130,000 Summary prompt limit. That wording was imprecise: 88,727 is the
conversation Prompt preflight estimate, while the Summary request reservation is separately estimated and was
observed to be above 100,000 and at most 130,000 Tokens. Local admission failures now use the non-retryable
`local_budget_exceeded` code instead of being reported as Provider failures. Do not raise the budget merely to
retry this artifact. The `conversation-memory-v3` prompt instead bounds the model's first-pass information
selection and drops repeated timeline boilerplate. This result is diagnostic evidence, not a Token-reduction
Pair.

The v3 capacity probe reduced Summary completion from the 6,144 limit to 2,896 Tokens, but exposed an
unclassified stable-reference validation failure. Fixture v4 now carries report references as structured
message metadata and maps them to real message sequences. `conversation-memory-v4` states that reference-like
text is never authority; only structured task, citation, and known-report inputs may produce Reference entries.
Production persistence of conversation report references remains a separate migration/repository slice and is
not implied by the Pilot fixture.

The Fixture v4 + Prompt v4 probe used 62,074 Summary prompt Tokens, 3,033 completion Tokens and 704 cached
Tokens in 30.872 seconds. Capacity and report authorization passed, but the model inferred a task reference
from `TKT-2048` in free text and was correctly rejected as `task_reference_id_unknown`. Prompt v5 adds a
deterministic retry instruction for evidence/task/report ID, identity and source failures: restore only exact
structured whitelist values, otherwise delete the invalid Reference entry. No second remote attempt was made
in this slice.

The invalid 2026-08-12 exploratory runs recorded 89 DashScope Summary attempts. Provider-reported Summary
usage was about 6.77M prompt Tokens and 42K completion Tokens, with three additional failed attempts lacking
usage. The runs mixed a 65K-100K prompt with up to three in-process retries and repeated diagnostic batches;
they are cost evidence, not a resume metric. The configured conversation-memory Profile now uses StepFun
Step Plan with low reasoning. Production retains three bounded attempts; the observer must reserve the
worst-case attempts before Provider access, so evaluation cost control never weakens the production retry
contract. No Provider-backed retest is authorized by this configuration change alone.

The evaluator does not treat a failed Provider-backed run as a zero-token success. A Baseline/Experiment
checkpoint enters comparable Token, cost, and first-token-latency metrics only when both observations are
within the hard window and have no error. In-window Provider or Runner failures are counted as `failedRuns`
and fail the `run_failure` quality gate.

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
go run ./tools/smoke/mesguard-model-smoke
```

The command asks the model to return one harmless Tool Call and prints only the
model name, Tool name, and provider-reported Token usage. It does not execute
database, GitHub, or write-capable tools.

To verify the complete non-streaming ReAct loop and multi-call Token
aggregation against a fixed synthetic ticket, run:

```powershell
go run ./tools/smoke/mesguard-agent-smoke
```

This command executes only the local read-only `read_external_case` Tool. It
does not connect to ERP, PostgreSQL, Redis, or GitHub. The printed report is a
local smoke result and is not an evaluation metric.

To aggregate a reproducible baseline/experiment pair, keep the versioned case
labels separate from each real run observation:

```powershell
go run ./tools/evaluation/mesguard-agent-eval `
  -dataset testdata/agent-evaluation.dataset.sample.jsonl `
  -input testdata/agent-evaluation.sample.jsonl
```

The command rejects mixed dataset versions, duplicate case/variant runs,
unknown JSON fields, and mismatched model/reasoning settings in a pair. The
checked-in `sample` files only verify the scoring program; they must not be
reported as production accuracy or Token reduction.

To verify the unified Conversation answer-quality contract without calling a
Provider, run:

```powershell
go run ./tools/evaluation/mesguard-conversation-quality-eval `
  -dataset testdata/conversation-quality-v1.jsonl `
  -input testdata/conversation-quality-v1.seeded.observations.jsonl
```

This command separates retrieval, citation, preview, degradation, latency,
Token, cost, and optional human/LLM Judge metrics. The checked-in observations
are `seeded_contract` fixtures and must not be reported as model quality. They
cannot be mixed with exported `recorded_run` observations. See
`docs/evaluations/conversation-quality-v1.md` for formulas, run-ledger export and
remaining real-sample work.

To prepare and validate an independent claim-level Judge input without calling
any model, use:

```powershell
go run ./tools/export/mesguard-conversation-quality-judge-export -overwrite
go run ./tools/evaluation/mesguard-rag-judge `
  -input output/evaluation/conversation-quality-recorded-v1.judge-inputs.jsonl `
  -validate-only
go run ./tools/evaluation/mesguard-rag-judge `
  -input output/evaluation/conversation-quality-recorded-v1.judge-inputs.jsonl `
  -estimate-only
```

The exporter joins the pinned corpus, raw/resolved Cases, human gold facts and
recorded observations. It includes all actually cited evidence but never turns
an extra citation into an allowed gold source. The Judge command requires
exactly one of `-validate-only`, `-estimate-only`, or `-execute-provider`,
defaults to one Case and a `0.05 CNY` guard, and rejects the same provider/model
that generated the answer. Current config uses `rag-judge-v2` with
`[models.judge] enabled=false`; the existing one-Case input validates locally
and preflights at about `0.034864 CNY`. One explicitly authorized `qwen3-max`
run used `2512/713/3225` Prompt/Completion/Total Tokens, took `16595 ms`, and
cost an estimated `0.018604 CNY`; config was then restored to disabled. Human
review agreed with its two unsupported-claim findings, extra-citation finding,
and no-missing-fact result. Passing that JSONL to
`mesguard-conversation-quality-eval -judge <path>` yields auxiliary
Faithfulness/Relevance/Citation Alignment `0.50/0.50/0.25` without changing the
deterministic failed result.

The transaction-scoped real-observation command uses a pinned public corpus and
defaults to provider-free validation or planning:

```powershell
go run ./tools/observation/mesguard-conversation-quality-observe -validate-only -max-cases 5
go run ./tools/observation/mesguard-conversation-quality-observe -estimate-only -max-cases 1 -chat-profile qwen-qa-eval
```

The checked-in set contains five knowledge Cases over four documents and 21
Chunks. Real execution is opt-in with `-execute-provider`, defaults to one Case,
disables Query Rewrite and Rerank, and rolls all fixture database facts back.
The observer pins `maxResults=3` for each Case (the raw dataset can override it
within the normal 1..20 boundary), so model-generated Tool arguments cannot
silently change the Context Precision denominator. Production search defaults
remain unchanged.
The CNY guard is an admission and post-Case estimate, not a provider-side hard
limit: usage is settled only after an in-flight call returns. Review the printed
plan and `docs/evaluations/conversation-quality-v1.md` before executing it.

To avoid charging for repeated retrieval during this bounded evaluation, the
observer forces the sole `search_knowledge` Tool on the first model call, executes
it once per user message, and returns that same
validated result with a stop notice on subsequent attempts. Production
Conversation behavior is unchanged; Tool-selection quality is measured by the
independent `tool-selection-v1` set. A real probe showed that a prompt-only stop
notice can still be ignored, even though compact cache replay reduced total
Tokens from 10,188 to 8,349. After the first Tool result the observer keeps the
`search_knowledge` schema for protocol continuity; removing it breaks providers
that validate prior Tool Call/Result history. It sends `ToolChoiceForbidden` to reject a new
Tool Call. A scripted model verifies that only
one search decision and one final cited answer remain. Two post-fix StepFun
probes still retrieved both gold Chunks and used about 2,000 Chat Tokens each,
but produced no Eino-valid final answer. Do not repeat the same paid probe or
report it as model quality. The observer now accepts `-chat-profile` and prints
content-free protocol diagnostics: Tool names, role, content presence,
ToolChoice, finish reason and stable error type only. A first `qwen-qa-eval`
probe used one actual model call and returned `assistant + content + stop`
without searching, so it failed Context/Citation Recall. It also exposed that
the outer Agent node and inner OpenAI-compatible Client could count the same
usage twice. The Wrapper now isolates inner callbacks and forces the first
knowledge retrieval. The latest bounded Qwen rerun completed two actual model
calls, returned a source-bound answer, recalled both required citations and matched
both previews, but failed strict Context/Citation Precision (`0.5/0.6667`). With
fixed `K=3`, three hits plus one Parent context source were exposed.
`conversation-v4` removed the over-specific parameter claim, yet the answer still
added plausible risks not directly supported by the two required chunks. This is
retained as a failed quality observation; do not expand gold labels or repeat
Prompt-only paid probes to make it pass. A different transaction Case then returned a correct draft but no
markers under both `conversation-v4` and `conversation-v5`. `conversation-v6` adds a failure-triggered citation
repair: valid original answers have zero extra calls; zero-citation source-backed drafts get at most one Tool-free,
temperature-zero, strict-JSON call using the same model. The repair input is bounded to 64 KiB of same-run evidence,
the configured output limit is 768 Tokens, and every returned marker is revalidated against the same source/hash
allowlist. Invalid, truncated, timed-out, unknown-marker or zero-marker repairs keep `insufficient_evidence`.

The final `transaction-commit-failure` run passed with Context Recall `1.0`, Citation Precision/Recall `1.0`, preview
consistency `1.0`, answer-term recall `1.0`, 7,025 Chat Tokens, 3,869 ms, and estimated online cost `0.008141 CNY`.
It used three calls because repair was triggered; human review found only the two directly supported claims. Context
Precision remains `0.5`, so retrieval candidate compression is still visible. The claim-level human/LLM Judge input,
strict execution and aggregation path remains available; the first historical Judge call and human calibration are
complete. No OCR, VLM, Web
Search, Query Rewrite, or Rerank call is part of this command.

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

The first independent conversation backend slice is also available after login:

```text
POST /api/v1/conversations
GET /api/v1/conversations?page=1&pageSize=20
GET /api/v1/conversations/<conversationId>
GET /api/v1/conversations/<conversationId>/messages?afterSeq=0&limit=20
POST /api/v1/conversations/<conversationId>/messages
POST /api/v1/conversations/<conversationId>/turns
GET /api/v1/conversations/<conversationId>/turns/<turnId>
GET /api/v1/conversations/<conversationId>/turns/<turnId>/events?afterSeq=0&limit=100
GET /api/v1/conversations/<conversationId>/turns/<turnId>/events  (Accept: text/event-stream)
POST /api/v1/conversations/<conversationId>/attachments
GET /api/v1/conversations/<conversationId>/attachments/<attachmentId>/preview
GET /api/v1/knowledge-citations/<chunkId>
```

Message writes require `X-CSRF-Token`. The message request may carry `caseReferences` and
`taskReferences`; the server verifies referenced records and writes them with the message in one
PostgreSQL transaction. Selecting a case alone does not create a diagnosis task. The guarded
`create_diagnosis_task` command service and internal Tool contract are invoked by the independent
Conversation Agent through `/turns`. The Tool stays model-visible in the fixed Conversation Profile;
a unique `selected` case reference plus explicit diagnosis
intent is required before execution passes the `diagnosis.create` permission and command Guard. The command only creates a durable diagnosis task;
the Diagnosis Worker remains asynchronous and independent from conversation lifecycle. `/turns`
requires a client-generated UUID `Idempotency-Key`. PostgreSQL atomically writes the user message,
the `queued` turn, and a `conversation.turn.execute` Outbox event; the API returns `202` without
calling a model. The independent `mesguard-conversation-worker` claims and renews
`lease_owner + lease_expires_at`, executes the Agent, and transactionally persists the final
assistant message. Failed retries reuse the same user message; completed retries return the original
assistant message with `200/replayed=true`. Turn status is independently queryable, and turn events can
be replayed from PostgreSQL with `afterSeq` or `Last-Event-ID` over JSON/SSE. A changed request with the same key, or another message
while a turn is queued/running, returns `409`; the same queued/running request returns its current
state with `202`. The internal `get_diagnosis_task_status` Tool stays model-visible in the fixed
Conversation Profile; a verified `taskReferences` entry grants the execution-time `task.read`
permission. The Tool rechecks the latest message reference and
owner/admin authorization, then returns persisted task status and report availability without an
invented progress percentage.

Attachment upload also requires `X-CSRF-Token`, a UUID `Idempotency-Key`, and exactly one multipart
`file` field. The route fixes the scope to the authenticated user's current conversation. The supported
formats match the existing parser boundary: UTF-8 text/Markdown/log/JSON/CSV/SQL/XML/YAML, PDF,
DOCX/XLSX/PPTX, PNG and JPEG. A user message may carry up to eight attachment references;
the server validates owner and conversation before writing the message and links all references in the
same transaction. Upload does not itself authorize the Agent. `read_attachment` stays model-visible
in the fixed Conversation Profile; only a current user message carrying an attachment grants the
execution-time `attachment.read` permission, and the Tool rechecks user, conversation, message and attachment
IDs before parsing at most 12,000 runes. The preview route returns at most 2,000 runes. Neither route
returns MinIO bucket/object coordinates, ETags, credentials, permanent URLs or Base64. Images and
scan-only pages report visual content but do not trigger paid OCR/VLM calls.

Knowledge citation preview accepts a Chunk UUID, permits global scope or a personal document owned by
the current user, and only reads ready/retired versions. Missing, deleted, processing and unauthorized
personal content all return not found. The frontend still needs to adopt turn SSE, attachment upload/Tool
traces and both citation preview paths. The Conversation Agent can now freeze all or a selected subset of
the current user message attachments into a new task; the transaction rechecks the latest message, owner,
conversation and attachment state. Diagnosis Worker metadata and `read_attachment` are then task-scoped,
and successful reads become `attachment` evidence. Direct HTTP diagnosis creation still rejects attachments
because it has no message authorization context. Personal attachment upload, failed-upload leases and orphan
cleanup remain later backend slices.

Assistant citations are not accepted as arbitrary model JSON. Valid knowledge, attachment, and fetched-page
Tool results receive backend-owned `citationSources` inside the same Conversation run. Each entry includes a
backend-formatted full marker; the final answer may refer to one by copying it verbatim, for example
`[source:knowledge:11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222]`.
Angle brackets, quotes, and backticks are not part of the marker. The
Runner rejects any marker that was not exposed in that run.
`[agent].conversationCitationRepair*` controls the optional zero-citation repair Prompt, version, timeout and output
budget. The repair uses the same configured main model identity and contributes usage to the original turn budget;
it is not a second hidden Provider profile.
Only the actually cited source type/ref/SHA-256/position is written with the assistant message and returned by
message reads or completed-turn replay. Retrieved-but-unused sources are not persisted as citations. The
database/API never exposes MinIO coordinates or raw Tool payloads, and preview authorization is evaluated again
when a user opens a knowledge or attachment citation.

Completed Conversation runs persist a separate bounded run ledger with provider/model/prompt identity, provider
usage, duration, validated retrieved source identities, and stable degraded channels. To export real evaluation
observations, prepare one strict JSONL row per dataset Case:

```json
{"caseId":"knowledge-answer","turnId":"<completed-turn-uuid>","estimatedCostCny":0.001,"previewContentSha256ByRef":{"knowledge:<version>/<chunk>":"<preview-sha256>"}}
```

Validate the mapping without database or provider access, then export to a new mode-0600 file:

```powershell
go run ./tools/export/mesguard-conversation-quality-export `
  -dataset testdata/conversation-quality-v1.jsonl `
  -selections <recorded-run-selections.jsonl> `
  -validate-only

go run ./tools/export/mesguard-conversation-quality-export `
  -dataset testdata/conversation-quality-v1.jsonl `
  -selections <recorded-run-selections.jsonl> `
  -output output/evaluation/conversation-quality-v1.recorded.observations.jsonl
```

The exporter performs read-only PostgreSQL access and no model/provider calls. It refuses query/case mismatches,
turn reuse, unknown JSON fields, non-cited preview hashes, and an existing output path. `estimatedCostCny` comes
from the reviewed provider price/bill; Token counts come only from persisted provider usage. Completed Turns export
their assistant answer and citations. Terminal failed Turns can be exported without an assistant message and carry
only the final safe run facts plus a stable `errorType`; zero usage is accepted only when the Provider returned no
usage and must not be interpreted as a free request. Explicitly requeueing the same failed Turn clears its previous
run ledger in the requeue transaction, so selections must target the current terminal state.

To verify the real attachment boundary without calling any model/provider, run the bounded PostgreSQL + MinIO
HTTP smoke against local development services:

```powershell
$env:MESGUARD_TEST_POSTGRES_DSN = "postgres://..."
$env:MESGUARD_TEST_MINIO_ENDPOINT = "127.0.0.1:9000"
$env:MESGUARD_TEST_MINIO_ACCESS_KEY = "..."
$env:MESGUARD_TEST_MINIO_SECRET_KEY = "..."
go test -tags=integration ./internal/transport/http `
  -run TestAttachmentHTTPMinIOSmoke -count=1 -v
```

The test uploads a 49-byte UTF-8 TXT through the real Gin multipart route and real MinIO, then verifies upload
idempotency, denial before message association, exact-message `read_attachment` authorization, citation preview,
cross-user 404 behavior, and object-store coordinate/credential non-disclosure. PostgreSQL facts are wrapped in a
rollback transaction and dedicated `mesguard-http-*` test buckets are removed. The test does not call OCR, VLM,
Embedding, Rerank, Web Search, or a chat model.

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
