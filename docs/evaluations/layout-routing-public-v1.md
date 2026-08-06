# M2-A7 Public Layout Routing Evaluation v1

## Purpose

This evaluation checks the local PP-DocLayout-M routing decision before cloud OCR/VLM.
It does not evaluate final OCR text, VLM descriptions, retrieval quality, Token usage, or
provider cost. Raw public documents and run output stay under ignored `output/evaluation/`;
Git stores only the source manifest, human annotations, evaluator, scripts, and this result
record.

## Corpus

`testdata/layout-routing-public-v1.corpus.json` pins seven real public files by URL, publisher,
usage basis, media type, byte length, and SHA-256:

- five PDFs with 357 pages in total: native text, dense manufacturing tables, industrial
  control/network diagrams, equipment photos, mixed presentation pages, and a 16-page
  image-only USPTO patent;
- two Microsoft SQL Server DOCX guides with 79 embedded media objects for later parser and
  merge evaluation.

The routing subset in `testdata/layout-routing-public-v1.jsonl` contains eight manually
reviewed PDF pages and seven high-value table/picture regions. This is a fixed engineering
set, not a statistically representative industrial-document benchmark. The one public PPTX
found during the initial search was a single-slide poster and was deliberately excluded. A
later local-only compatibility corpus uses nine course PPTX files; aggregate results are in
`knowledge-ingestion-quality-v1.md`, while the original files remain outside Git.

Reproduce downloads and verify hashes with:

```powershell
.\scripts\evaluation\fetch_layout_routing_corpus.ps1
```

## Metric Contract

- Page-class Macro-F1 compares `native_digital`, `scanned`, and `mixed` labels.
- Route Macro-F1 compares page-level presence of `native_text`, `cloud_ocr`,
  `table_recovery`, `cloud_vision`, and `skip`. Actionable Route Macro-F1 separately
  excludes advisory `skip`, because the v1 annotations label required processing paths
  and high-value regions rather than every decorative region.
- A high-value visual is found only when region type and route match and bounding-box IoU is
  at least `0.3`.
- The cloud-bound region baseline sends every detected region to a cloud recognizer. The
  candidate sends only OCR, table-recovery, and VLM routes. The reported avoidance rate is a
  routing-decision proxy, not measured API-call, Token, latency, or cost reduction.
- Go heap/allocations are emitted by the evaluator. The Windows wrapper separately samples
  process CPU, working set, and private memory; build time is excluded.

## 2026-08-05 Post-fallback Paired Run

Environment: Windows x64, 16 logical processors, ONNX Runtime 1.28.0, PP-DocLayout-M pinned
revision, render request 144 DPI, one page at a time.

| Metric | 20M pixel limit | 8M pixel candidate |
|---|---:|---:|
| Page-class Macro-F1 | 1.0000 | 1.0000 |
| Route Macro-F1, including advisory `skip` | 0.8000 | 0.8000 |
| Actionable Route Macro-F1 | 1.0000 | 1.0000 |
| High-value visual miss | 0/7 | 0/7 |
| Cloud-bound page avoidance | 25.00% | 25.00% |
| Cloud-bound region avoidance | 73.08% | 73.08% |
| Page P50 | 793.15 ms | 796.55 ms |
| Page P95 | 6386.53 ms | 2510.88 ms |
| Average page latency | 2129.94 ms | 1166.12 ms |
| Peak Go heap | 403.8 MiB | 319.9 MiB |
| Peak process working set | 829.0 MiB | 580.8 MiB |
| Complete process duration | 21.72 s | 14.01 s |

The 8M candidate reduced P95 by about 60.7%, average page latency by 45.3%, peak Go heap by
20.8%, and peak process working set by 29.9% on this paired run without changing routing labels.
The two oversized patent pages were adaptively rendered at 72 DPI instead of the requested
144 DPI. Because OCR/VLM recognition quality at that DPI has not been measured, 8M remains a
candidate and the production profile keeps 20M for now.

No cloud OCR or VLM provider was called in either run. The improvement from 46.15% to 73.08%
cloud-bound-region avoidance came from two deterministic policy changes, not from a provider
benchmark: PDFium-WASM recovered native text when the primary Go parser returned an empty page,
and low-confidence text/table detections now fall back to native text or OCR instead of being
promoted to VLM. Candidate cloud-bound regions fell from 42 to 21 against the same 78-region
all-cloud baseline. This is still not a measured Token, latency, quality, or money saving.

## Findings And Next Gate

The public corpus exposed and fixed two production issues before benchmark completion:

1. PDFium previously failed the whole task when a scanned page exceeded the pixel limit.
   It now lowers effective DPI while retaining requested/effective DPI in the artifact.
2. The planner previously labeled every detector-assisted native page `mixed`. It now uses
   final routes, so detected text-only pages remain `native_digital`.

NIST IR 8108 page 14 now recovers its embedded text through bounded PDFium text APIs and routes
to `native_text`; the USPTO scanned-prose page routes only to OCR. The all-route score remains
0.8000 solely because PP-DocLayout-M reports one low-confidence NASA logo as decorative and the
planner records its cost-free `skip`, while the v1 expected-route annotation does not enumerate
decorative regions. Actionable routes and all seven high-value regions match the annotations.

Remaining bottlenecks are now narrower:

1. `github.com/ledongthuc/pdf` still prints repeated `pdf.keyword(def). Skip dict` diagnostics
   and cannot extract NIST IR 8108 page 14. PDFium is a bounded fallback, but the primary parser
   remains a maintenance and hostile-input risk inside the isolated Worker.
2. PP-DocLayout-M can emit overlapping labels. A bounded cross-label arbitration now removes
   only small pictures that overlap a higher-confidence decorative region by at least 0.85 IoU,
   occupy at most 2% of the page, and trail by at least 0.15 confidence. In the fixed-set A/B it
   removed only the duplicate NASA logo picture: candidate cloud-bound regions changed 21 to
   20, avoidance changed 73.08% to 74.03%, actionable-route Macro-F1 stayed 1.0000, and
   high-value misses stayed 0/7. A single-run latency difference is treated as noise.
3. Scanned patent pages dominate latency and memory. One two-call OCR comparison now shows that
   72 DPI and 113 DPI are near-equivalent on USPTO page 8, but one prose page is not enough to
   change the 20M production setting; scanned tables, small print and degraded scans remain.
4. OCR strict-JSON, latency, usage and cost now have one paired sample. VLM semantic quality,
   failures and successful-region cost remain unmeasured. Provider runs stay explicitly enabled
   and hard limited to selected regions, never a whole large document.
5. Nine substantial local PPTX files cover parser compatibility and throughput. A separate
   eight-slide reviewed structure set now covers page anchors, nine tables, 15 picture uses and
   14 distinct relationships; cloud-enriched Element merge quality is still missing.

## Upstream Evidence And Selected Solution

- go-pdfium v1.19.6 exposes PDFium text-page load, character count, bounded text extraction,
  and close operations across its WASM implementation. MESGuard now checks character count
  before requesting text and shares the document-level extracted-rune budget with the primary
  parser: <https://pkg.go.dev/github.com/klippa-app/go-pdfium#Pdfium> and
  <https://github.com/klippa-app/go-pdfium/releases/tag/v1.19.6>.
- PDFium's public text API defines the same `FPDFText_LoadPage`, `FPDFText_CountChars`,
  `FPDFText_GetText`, and `FPDFText_ClosePage` lifecycle:
  <https://pdfium.googlesource.com/pdfium/+/refs/heads/main/public/fpdf_text.h>.
- Paddle's layout-detection documentation supports global or per-class confidence thresholds,
  layout NMS, unclip ratios and per-class merge modes. MESGuard applies a narrower domain
  arbitration after ONNX inference; broad threshold calibration remains deferred until the
  fixture set is large enough:
  <https://www.paddleocr.ai/latest/en/version3.x/module_usage/layout_detection.html>.
- An open `ledongthuc/pdf` hardening change documents unchecked allocations, hangs and parser
  panics, reinforcing the decision not to make that parser the only boundary for untrusted
  documents: <https://github.com/ledongthuc/pdf/pull/78>.

Pure-Go alternatives were not adopted in this checkpoint. `pdfcpu` exposes consolidated PDF
content streams rather than a drop-in reading-order text extractor, while newer text/table
libraries do not yet have enough project history or corpus evidence to justify replacing the
current parser during the resume-driven M2 schedule. PDFium-WASM therefore remains the focused,
process-local fallback rather than a second full document pipeline.

A separate three-run Windows thread-count A/B used the same eight pages at the 8M raster limit.
Intra-op 1/2/4 produced median average page times of 1457.09/1389.57/1283.88 ms and median peak
working sets of 569.6/602.2/606.0 MiB. Two threads retained the best median P95 and remain the
default; four is only a throughput candidate. Full method and source links are in
`knowledge-ingestion-quality-v1.md`.

Before enabling layout by default, expand the provider set beyond one scanned prose page and
compare recognition accuracy against reviewed references, valid JSON, P50/P95, failures, and
successful-region cost. A larger routing corpus still needs charts, software screenshots/error
states and scanned tables.

Run the default profile and capture OS resources with:

```powershell
.\scripts\evaluation\run_layout_routing_eval.ps1
```

Run the 8M candidate into separate ignored outputs with:

```powershell
.\scripts\evaluation\run_layout_routing_eval.ps1 `
  -ResourceOutput output/evaluation/layout-routing-public-v1-8m.resources.json `
  -EvaluatorArgs @(
    '-max-raster-pixels', '8000000',
    '-output', 'output/evaluation/layout-routing-public-v1-8m.observations.jsonl',
    '-summary', 'output/evaluation/layout-routing-public-v1-8m.summary.json'
  )
```
