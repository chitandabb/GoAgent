# 003. Use a Local ONNX Layout Router before Cloud OCR and VLM

## Status

Accepted on 2026-08-04. M2-A7 contract implementation started on 2026-08-05.

## Context

M2-A6 can discover bounded visual assets and route obvious cases by media type,
native-text presence, image dimensions, Office relationships, and per-task budgets. It
cannot yet distinguish native text, scanned text, scanned tables, screenshots, charts,
or mixed regions on the same page. Calling a cloud VLM for every page would hide this
gap behind a costly and non-deterministic full-document pass.

Three implementation choices were considered:

1. run the complete Python-based Docling pipeline;
2. call a cloud multimodal model for page classification and processing;
3. keep the Go ingestion pipeline and execute a focused pre-trained layout model through
   ONNX Runtime locally.

The routing decision is MESGuard domain policy, while layout inference is a reusable
model capability. Re-training a layout model or rebuilding OCR is outside the project
scope.

## Decision

MESGuard will implement a Go-owned `LayoutRouter` domain port and route planner. A
pre-trained document-layout detector will run locally through ONNX Runtime in the
Knowledge Worker. The selected model, preprocessing, labels, confidence thresholds,
SHA-256, and runtime version must be configuration-backed and recorded in the Element
Artifact. Model licensing must be approved before its weight is distributed.

Routing has two levels:

```text
page: native digital / scanned / mixed
region: text / table / picture / caption / formula / decorative
```

The planner combines deterministic signals with model output. Obvious native text stays
on the current Go fast path. Ambiguous, scanned, and mixed pages are rendered for local
layout inference. Region routing then selects native parsing, cloud OCR, table recovery,
or cloud VLM. Region segmentation happens before recognition; retrieval Chunk creation
happens only after all recognized Elements have been merged and de-duplicated.

Eino may expose this capability through a custom Document Transformer and callbacks, but
Eino `schema.Document` is not the persisted source of truth. Page number, bounding box,
region type, confidence, route reason, provider/model identity, and source reference remain
in MESGuard's domain Element and versioned Artifact contracts.

The complete Docling runtime will not be a required production dependency for M2-A7.
Docling remains a design reference, an offline comparison baseline, and a possible
low-confidence fallback only if measurements justify its operational cost.

OCR and VLM remain cloud services in this checkpoint:

- OCR baseline: DashScope `qwen-vl-ocr-latest`;
- Vision quality baseline: DashScope `qwen3-vl-plus`;
- Vision production candidate: StepFun `step-3.7-flash` with low reasoning;
- optional speed reference: Google `gemini-3.5-flash-lite` after credentials are available.

The active Vision profile is selected only after a paired run over the same cropped
regions, Prompt, output schema, timeout, and output-token limit. Vendor TPS claims do not
determine the result. The evaluation records end-to-end P50/P95 latency, valid-JSON rate,
chart/table semantic accuracy, provider errors and rate limits, cost per successful region,
and document-level throughput. The ONNX router separately records route Macro-F1,
high-value visual miss rate, CPU/RAM, and avoided OCR/VLM calls.

## Consequences

- The Knowledge Worker image must package the approved model and ONNX Runtime native
  library without adding those dependencies to the API or Diagnosis Worker images.
- Windows and Linux packaging, warm-session startup, bounded inference threads, and model
  checksum verification become explicit release concerns.
- Artifact schema v5 stores page class, bounding boxes, route reasons, model, renderer and provider usage
  provenance, requested/effective DPI, crop hashes, the generated visual-asset index, and
  deterministic element-merge decisions. M2-A6 file/page routing remains the fallback for
  formats without page rasters.
- Cloud OCR/VLM credentials stay isolated to the Knowledge Worker. Users never select a
  parser or model; the backend route planner owns the decision.
- Local OCR remains an optional future adapter. It must not block layout routing,
  Embedding, hybrid retrieval, or the resume-driven M2 schedule.

## Current Implementation Status

The production code path is implemented but the feature switch remains disabled by default:

- `config/models/pp-doclayout-m.json` pins Apache-2.0 PP-DocLayout-M commit
  `7dbfcce3154a55776dc71ca026a4a2a8388dad8d`, all source hashes, 23 labels, domain mapping,
  fixed `640x640` RGB/NCHW preprocessing and NMS contract.
- `scripts/models/fetch_and_convert_pp_doclayout.ps1` verifies the Paddle artifacts and runs
  a pinned Linux conversion image (`Paddle2ONNX 2.1.0`, Paddle
  `3.4.0.dev20260407`, ONNX opset 17). Repeated conversion produced SHA-256
  `b237c7e4aef235de8f45778ff2dd96dc21480cade40f01435f640b0ff68ee010`.
- `scripts/runtime/fetch_onnxruntime.ps1` verifies ONNX Runtime 1.28.0 archives for Windows
  x64 and Linux x64. The Go adapter uses `onnxruntime_go v1.32.0`, a process-owned,
  reference-counted environment, bounded threads/concurrency and per-run cancellation.
- PDF pages are rendered through sandboxed PDFium-WASM with no filesystem mount. Parser page
  observations, deterministic native-text fast paths, normalized region routing and bounded
  PNG crops are connected to the Knowledge Worker Executor when layout is enabled. Oversized
  pages now reduce effective DPI instead of failing the whole ingestion task; requested and
  effective DPI are retained in Artifact provenance.
- The same PDFium-WASM worker now recovers native page text when the primary pure-Go parser
  returns no usable text. It checks `FPDFText_CountChars` before extraction, shares the parser's
  document-level rune budget, records recovered Element provenance, and still renders the page
  for mixed-content detection because PDF visual-candidate state remains unknown.
- Low-confidence text, caption and table detections no longer escalate to VLM. They fall back
  to native text when available or OCR otherwise; low-confidence decorative regions are skipped.
  Picture and formula regions retain Vision routing to protect high-value recall on the scanned
  engineering-drawing fixture. Per-class thresholds remain an evaluation-driven follow-up.
- Small decorative/picture duplicates use a configurable cross-label arbitration. It requires
  near-identical boxes, a small page-area ratio, and a confidence margin, so the fixed-set run
  removes the duplicate NASA logo without filtering low-confidence USPTO engineering drawings.
- Actionable crops become `layout_region` assets with explicit OCR or OCR+VLM plans. Whole
  image/page assets are marked `superseded_by_layout_regions`, preventing duplicate cloud
  calls. Document-level region-count and total-crop-byte budgets suppress excess crops before
  cloud calls while retaining the suppression reason. Prompt v2 requests Markdown table
  structure and formula transcription.
- Artifact schema v5 persists layout/model/render/crop provenance, provider Token usage, and every raw element before
  Chunk creation. `element-merge-v1` suppresses only explainable same-page duplicates from the
  searchable projection: exact normalized duplicates, OCR fully covered by native text, and
  at least 85% contained overlapping OCR. Structured tables/native text win; VLM descriptions
  are not fuzzy-deduplicated. Suppressed elements and their winning element indexes remain in
  the Artifact. A real upstream fixture returned 2 table, 4 caption and 10 text regions through the Go ONNX
  adapter; this is a semantic wiring check, not a quality benchmark.
- `layout-routing-public-v1` now pins seven public files by source, size, and SHA-256. Eight
  manually reviewed pages cover native text, mixed pages, dense tables, industrial diagrams,
  equipment photos, scanned prose, and scanned engineering drawings. The first Windows paired
  post-fallback run produced page-class Macro-F1 1.0000, actionable-route Macro-F1 1.0000,
  and 0/7 high-value misses. Its 73.08% cloud-bound-region avoidance is defined against an all-regions-cloud routing
  baseline and is not a measured Token/cost reduction.
- A 20M versus 8M raster-pixel paired run kept those routing labels unchanged while reducing
  P95 from 6587.09 ms to 2617.51 ms and peak working set from 786.5 MiB to 584.1 MiB. The
  oversized scan pages reached 72 DPI under 8M. A subsequent two-call OCR comparison on one
  scanned prose page returned strict JSON for both candidates, 99.54% paired character
  similarity and about 30.6% lower provider latency at 72 DPI. One page is insufficient to
  promote 8M, so 20M remains the production setting. Full methods and caveats are recorded in
  `docs/evaluations/layout-routing-public-v1.md` and `knowledge-ingestion-quality-v1.md`.

Nine substantial local PPTX files now provide parser compatibility and throughput evidence. An
independently reviewed eight-slide structure set also verifies page anchors, nine DrawingML
tables, and 14 distinct slide relationships; one repeated relationship use is intentionally
deduplicated. A three-run Windows thread A/B kept two intra-op threads as the conservative default
and retained four as a throughput candidate. A three-crop cloud pair then compared Qwen and
StepFun under the same Prompt/schema and 2,048 output-Token limit. Both completed strict JSON for
3/3 and retained all text anchors, but manual review found one relation error per provider. Qwen
used 53.5% fewer total Tokens and had 32.1% lower mean latency, so it remains the production Vision
profile while StepFun remains a candidate. The generated model, Linux x64 runtime and license
notices are now staged through an ignored SHA-verified BuildKit context and copied only into an
optional non-root Knowledge Worker image. A no-network 2 CPU/2 GiB Linux run retained fixed-set
quality with 1.18-second average page latency, 2.59-second P95 and 638.06 MiB peak RSS. Remaining
enablement gates are broader chart/screenshot/scanned-table routing fixtures and cloud-enriched
merge-quality evidence. The
v1 route metrics above may be quoted only with their eight-page sample and baseline definition;
the single OCR pair and local parser benchmark must retain their dataset and stage boundaries;
no absolute OCR accuracy or end-to-end production throughput is claimed.

## M2-A7 Completion Checklist

1. [x] Approve one ONNX layout model and its label/license contract.
2. [x] Define `LayoutRouter`, page/region inputs, decisions, bounding boxes, confidence, and
   stable reason codes in Go.
3. [x] Add deterministic fast-path, fallback and real native-runtime tests.
4. [x] Package ONNX Runtime and the pinned model only in the Knowledge Worker role.
5. [x] Extend Artifact provenance and connect region routes to existing OCR/VLM ports.
6. [x] Run the complete fixed fixture and Windows/Linux resource benchmark before enabling by default.
