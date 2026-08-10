# Table Recovery v1 Evaluation Contract

## Purpose

This evaluation closes the structured-table part of resume point 3 without turning every document
into a paid VLM workload. It separates provider-free contract correctness from real scanned-table
quality. Results from the first category must not be reported as OCR or table-recognition accuracy.

## Implemented provider-free gate

The following production boundaries are covered without network or model calls:

1. `table_recovery` maps to an independent `TableRecoveryProcessor`, not the generic picture/chart
   `OCRVLM` path;
2. requests accept only bounded PNG/JPEG `layout_region` crops with page provenance and a stable
   route reason;
3. results require non-empty Markdown and cells with row/column, spans, header flags, confidence,
   warnings, model/Prompt identity and optional usage;
4. strict decoding rejects unknown fields, trailing content, malformed Markdown fences, duplicate
   coordinates, invalid spans, NUL, oversized text and empty structures;
5. structured cells remain in Element metadata in Artifact schema v6. PostgreSQL receives only the
   Markdown `ElementTable` retrieval projection;
6. an unavailable table processor may fall back to generic visual text only with `partial` status;
7. long Markdown tables split at complete row boundaries and repeat the table header;
8. one mixed-page fixture verifies native text, table, picture and decorative regions route to
   `native/table/vision/skip`; only table/picture regions are cropped and the source page is not sent
   twice.

Verification command:

```powershell
go test ./internal/knowledgetable ./internal/platform/tablemodel `
  ./internal/knowledgeenrichment ./internal/knowledgeingestion ./internal/knowledge -count=1
```

## Real provider smoke result

The paid run remained intentionally small. Its predeclared gate was:

- one real scanned-table crop is required;
- one additional crop with merged cells is optional;
- no full multi-page document is sent directly to the table model;
- estimate and inspect crop identity before execution;
- stop at two calls or an estimated CNY 0.05, whichever comes first;
- keep `models.table` provider/model, Prompt version, source/crop SHA-256, latency, usage and cost.

Human review records:

- expected and observed row/column counts;
- exact header text;
- exact identifiers, numbers, signs, units and decimal places;
- merged-cell spans;
- unreadable cells and warnings;
- Markdown readability;
- unsupported or invented values;
- whether the fallback/degraded status is honest.

The selected public sample is the table region on NIST IR 8107 page 15. The existing ONNX evaluation
located the region on a mixed native-text page; the table command reused that normalized box and
cropped only the detected table.

| Field | Value |
| --- | --- |
| Source preview SHA-256 | `f2ec5832d0236bcb58c7f7317357609b7906569ae24f2ca4b7398983fbb61485` |
| Crop SHA-256 | `3236002a580d23da338daa5d2a9406252427921f689ed98579f95680c0c6c2bb` |
| Crop | `840x269`, 48,492 bytes |
| Provider/model | DashScope / `qwen3-vl-plus` |
| Official <=32K price used | CNY 1/M input, CNY 10/M output |
| Prompt v1 | 524/663/1,187 input/output/total Tokens; 15,273 ms; CNY 0.007154 |
| Prompt v2 | 658/662/1,320 input/output/total Tokens; 14,831 ms; CNY 0.007278 |
| Total paid observation | 2 calls; 2,507 Tokens; CNY 0.014432 |

Both calls preserved the important identifiers and descriptions and produced searchable Markdown.
However, the visible table has a vertically merged `ISO 10303-207/224/238` cell spanning the three
separate 207/224/238 description rows. Both responses collapsed those three visible rows into one
multiline cell, emitted 8 cells instead of the reviewed 10-cell grid, set `rowSpan=1`, reported no
warning and returned confidence 0.98/1.0. Prompt v2 explicitly prohibited this collapse, so a second
Prompt-only call did not fix structural fidelity.

The smoke therefore has two separate verdicts:

- routing/provider/searchable-table wiring: **pass**;
- exact merged-cell/span recovery: **partial**.

The adapter now applies a deterministic post-response guard: multiline cell text or `<br>` forces
`partial=true`, reason `multiline_cell_structure_ambiguous`, confidence at most 0.8 and a stable warning.
This does not invent missing structure; it keeps useful text while preventing an inaccurate span from
being published as a completed structured table. No third paid call was made.

The result is a bounded quality observation, not a general TEDS, OCR-accuracy or production
table-understanding claim. A larger fixed set or PP-StructureV3 table-crop sidecar is justified only
if exact merged-cell recovery becomes a product requirement rather than a documented enhancement.
Current public pricing source: <https://help.aliyun.com/zh/model-studio/qwen3-vl-plus>.

## Remaining resume-point-3 closure

After the bounded real table crop, the different compact Conversation knowledge-QA Case has passed
retrieval, citation precision/recall, preview consistency and human unsupported-claim review. One
independent resume gate remains:

1. one 4-8 document representative cross-format full-chain pair must preserve outcomes, Elements,
   Chunks, requests and Tokens while reporting its bounded throughput delta.

Multi-column reading order, cross-page table continuation, Office SmartArt/chart semantics, full
Docling/MinerU adoption and local OCR are documented extensions and do not block this evaluation.
