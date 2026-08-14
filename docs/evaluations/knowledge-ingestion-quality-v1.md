# M2 Knowledge Ingestion Quality Evaluation v1

## Scope

This record separates deterministic Office parser throughput, local PDF raster trade-offs, and
measured cloud OCR behavior. It does not measure full upload-to-publish throughput, VLM chart
understanding, retrieval quality, or absolute OCR accuracy. Local course decks, rendered pages,
provider text and raw JSON stay under ignored `output/evaluation/` or outside the repository.

## PPTX Compatibility And Throughput

On 2026-08-05, `document-parse-eval-v1` parsed nine real operating-system course decks from
`D:\操作系统课件ppt`. The files contain 33 to 141 slides each and were not generated for the
test. The first run exposed that six valid packages used explicit zero-byte directory entries
such as `ppt/slides/`; the ZIP safety check incorrectly rejected them because `path.Clean`
removes a trailing slash. The parser now accepts only canonical zero-byte directory entries
while continuing to reject absolute paths, backslashes, aliases and traversal.

After the fix, 9/9 files succeeded:

| Metric | Result |
|---|---:|
| Source bytes | 19,891,356 |
| Slides | 752 |
| Elements | 5,577 |
| Text / table elements | 5,457 / 120 |
| Extracted runes | 125,987 |
| Referenced visual occurrences | 382 |
| Median aggregate parser time, 3 runs | 1,985.62 ms |
| Median throughput, 3 runs | 9.55 MiB/s |
| Throughput range | 8.60 to 10.31 MiB/s |
| Median slide throughput, 3 runs | 378.72 slides/s |
| Slide throughput range | 340.94 to 408.75 slides/s |
| Median file P95, 3 runs | 412.56 ms |

Each run starts a fresh evaluator process and validates the complete parser result. These are
in-process local OOXML parse times with production limits. They exclude file upload,
MinIO, RabbitMQ, cloud enrichment, chunk persistence and vector indexing, so it must not be
quoted as end-to-end ingestion throughput. Reproduce against an approved local folder with:

```powershell
.\scripts\evaluation\run_pptx_parse_eval.ps1 -InputRoot '<approved PPTX folder>'
```

## PPTX Element Quality Fixed Set

Parser throughput does not prove that slide order, tables, or image relationships are correct.
Two complementary course decks were therefore rendered and reviewed locally: one table-heavy
file-system deck and one image-heavy device-management deck. Eight representative slides were
then checked against an independent OOXML view of the slide XML and relationship parts. Git
stores only document names, byte lengths, SHA-256 values, and reviewed anchors; the source decks,
rendered slides, and raw observations remain local or under ignored `output/evaluation/`.

The fixed set contains 21 page-specific text anchors, nine DrawingML tables with independently
reviewed content anchors, and 15 picture uses resolving to 14 distinct slide relationships. One
device-management slide intentionally reuses `rId1` in two picture shapes; MESGuard keeps one
relationship-level visual asset, avoiding duplicate OCR/VLM work while retaining the slide link.

| Metric | Result |
|---|---:|
| Reviewed cases | 8 |
| Case pass rate | 8/8 (100%) |
| Page text-anchor recall | 100% |
| Table-anchor recall | 100% |
| Exact table-count rate | 100% |
| Exact distinct-relationship-count rate | 100% |
| Visual relationship completeness | 100% |
| Reviewed picture uses / distinct relationships | 15 / 14 |

The first run produced 6/8 because the annotation treated ordinary shape text as a table and
counted repeated `blip` uses as distinct relationships. Independent XML inspection corrected the
metric contract; production parser behavior was not weakened to fit the expected output.

```powershell
.\scripts\evaluation\run_pptx_element_quality_eval.ps1 `
  -InputRoot '<folder containing the two SHA-pinned PPTX files>'
```

This set proves the reviewed PPTX structure slice only. It does not evaluate speaker notes,
animations, SmartArt semantics, chart data, cloud VLM descriptions, or OCR/native duplicate
suppression after enrichment.

## Local ONNX Thread A/B

ONNX Runtime documents intra-op threads as operator-level parallelism and recommends measuring
thread settings for the actual host/model. The evaluator therefore gained optional
`-intra-op-threads` and `-inter-op-threads` overrides that do not change the production profile.
On Windows x64 with 16 logical processors, the same eight-page layout set and 8M raster limit ran
three fresh processes per candidate; inter-op remained 1 and no cloud provider was called.

| Intra / inter threads | Median average page | Median P95 | Median process duration | Median peak working set |
|---|---:|---:|---:|---:|
| 1 / 1 | 1457.09 ms | 3066.71 ms | 16.36 s | 569.6 MiB |
| 2 / 1 | 1389.57 ms | 2757.74 ms | 16.35 s | 602.2 MiB |
| 4 / 1 | 1283.88 ms | 2971.31 ms | 15.25 s | 606.0 MiB |

All candidates kept actionable-route Macro-F1 at 1.0000 and high-value misses at 0/7. Four
threads reduced median average page latency by about 11.9% versus one thread, but P95 improved
only about 3.1% and varied non-monotonically. Two threads had the best median P95 and remain the
Windows production default; four threads are only a throughput candidate for a larger corpus and
the Linux Worker image.

Sources and implications:

- ONNX Runtime enables graph optimizations by default, distinguishes intra-op from inter-op
  parallelism, and explicitly recommends workload-specific thread testing:
  <https://onnxruntime.ai/docs/performance/tune-performance/threading.html>.
- PaddleOCR exposes per-class thresholds, layout NMS, unclip ratios and merge modes. It reports
  PP-DocLayout-M as a speed/quality balance and PP-DocLayout-S as faster but lower mAP; MESGuard
  keeps M and performs narrow domain arbitration because rendering and scanned-page handling,
  not only 640x640 model inference, dominate the current end-to-end path:
  <https://www.paddleocr.ai/latest/en/version3.x/module_usage/layout_detection.html>.
- PP-StructureV3 follows layout analysis, element analysis, and structured formatting, with
  table/formula/chart modules enabled selectively. This supports MESGuard's staged routing design
  without requiring the complete Python pipeline in production:
  <https://www.paddleocr.ai/latest/en/version3.x/pipeline_usage/PP-StructureV3.html>.
- PresentationML stores each slide in its own part and uses explicit relationships for images,
  charts, and other content. This is the independent basis for the PPTX quality contract:
  <https://learn.microsoft.com/en-us/office/open-xml/presentation/structure-of-a-presentationml-document>.
- Azure Document Intelligence can parse PPTX text but documents that embedded/linked Office
  images are unsupported. It is therefore a possible comparison service, not a lossless
  replacement for MESGuard's relationship-aware Office parser:
  <https://learn.microsoft.com/en-us/azure/ai-services/document-intelligence/prebuilt/layout>.

## Linux Knowledge Worker Packaging And Resource Gate

The local ONNX assets are not committed and are not added to the default backend image. A
verification script stages only the SHA-pinned 23,499,360-byte PP-DocLayout-M ONNX file, the
24,268,848-byte Linux x64 ONNX Runtime library, model README/license metadata, ONNX Runtime
license, and third-party notices into an ignored BuildKit context. `Dockerfile.knowledge-worker`
copies that context only into the enhanced Knowledge Worker image and verifies both binary hashes
again during the build. The optional `docker-compose.layout.yml` overlay leaves the normal Compose
path unchanged and runs the enhanced Worker as UID/GID 65532 with a read-only root filesystem,
all Linux capabilities dropped, and `no-new-privileges`.

The final local enhanced Worker image was 93,122,081 bytes by `docker image inspect`. The current
multi-role backend image was 145,274,049 bytes; the specialized image is smaller despite its model
assets because it contains one application binary rather than every API/Worker binary. This is a
local image-layout observation, not a registry compressed-size or cold-pull benchmark.

The evaluation target contains the same model/runtime plus the fixed-set evaluator and GNU time.
It ran the eight-page public routing set inside Docker Desktop's Linux/amd64 VM with two CPUs,
2 GiB memory, no network, a read-only root filesystem, no capabilities, and the production 2/1
ONNX thread profile at the 8M raster limit. No cloud provider was called.

| Linux container metric | Result |
|---|---:|
| Page-class / actionable-route Macro-F1 | 1.0000 / 1.0000 |
| High-value misses | 0/7 |
| Cloud-bound-region avoidance proxy | 74.03% |
| Average page / P50 / nearest-rank P95 | 1,184.60 / 769.93 / 2,589.62 ms |
| Runtime initialization | 3,427.56 ms |
| Total wall time | 15.04 s |
| User / system CPU | 16.01 / 0.73 s |
| Average process CPU | 115% (about 1.15 cores) |
| Peak resident set | 669,057,024 bytes (638.06 MiB) |
| Swaps / major page faults | 0 / 0 |
| Evaluation image size | 101,489,538 bytes |

Compared with the Windows 2/1 median, this Linux run's average page latency was about 14.8% lower
and P95 about 6.1% lower. Linux maximum RSS was about 6.0% above the Windows peak working-set
median, but those OS counters are not definitionally identical and one Linux run is not a stable
cross-platform benchmark. The evidence proves packaging, native-library loading, fixed-set quality,
and bounded CPU/RAM operation on Linux; production-host repetition remains necessary for capacity
planning.

```powershell
.\scripts\runtime\fetch_onnxruntime.ps1 -Platform linux-x64
.\scripts\models\fetch_and_convert_pp_doclayout.ps1
.\scripts\runtime\prepare_knowledge_worker_assets.ps1

docker compose -f docker-compose.yml -f docker-compose.layout.yml build knowledge-worker
.\scripts\evaluation\run_linux_layout_routing_eval.ps1
```

## Bounded OCR Pair

The OCR evaluator defaults to dry-run. Provider execution requires an explicit flag and always
uses exactly two render candidates for one selected PDF page. The test used USPTO patent
US4575330 page 8, an image-only two-column prose page, with `qwen-vl-ocr-latest` and the same
strict JSON prompt.

| Metric | 20M candidate | 8M candidate |
|---|---:|---:|
| Effective DPI | 113 | 72 |
| Raster size | 3642 x 5349 | 2320 x 3408 |
| Encoded PNG bytes | 4,434,989 | 391,009 |
| OCR latency | 19,466.14 ms | 13,514.29 ms |
| Prompt / completion Token | 8,176 / 1,512 | 7,741 / 1,553 |
| Total Token | 9,688 | 9,294 |
| Strict JSON | pass | pass |
| Estimated cost | CNY 0.003209 | CNY 0.003099 |

The 8M candidate reduced encoded bytes by 91.2%, provider latency by 30.6%, and total Token by
4.1% on this one pair. Combined estimated cost was CNY 0.006308. Pricing uses the Alibaba Cloud
China listing observed on 2026-08-05: CNY 0.3 per million input Token and CNY 0.5 per million
output Token. The evaluator exposes both prices as command arguments because pricing changes.

The two OCR texts differ by 33 character edits and have 99.54% normalized character similarity.
Manual inspection found the same prose and identifiers; the 72-DPI output retained slightly
more printed line-end hyphenation. Google Patents text was used only as an auxiliary semantic
check because its page boundaries and paragraph normalization differ from the scanned PDF.
This is evidence of paired equivalence for one clean prose page, not 99.54% absolute OCR
accuracy and not enough evidence to switch the production raster limit.

```powershell
# local render only
go run ./tools/evaluation/mesguard-ocr-quality-eval -input '<approved PDF>' -page 8

# exactly two configured OCR calls
go run ./tools/evaluation/mesguard-ocr-quality-eval -input '<approved PDF>' -page 8 -execute-provider
```

Sources:

- Alibaba Cloud Model Studio pricing: <https://help.aliyun.com/zh/model-studio/model-pricing>
- Google Patents auxiliary text: <https://patents.google.com/patent/US4575330A/en>

## Bounded VLM Region Pair

On 2026-08-06, `vlm-quality-eval-v1` compared the configured DashScope
`qwen3-vl-plus` and StepFun `step-3.7-flash` low-reasoning endpoint over the same three
reviewed diagram crops. The sources are real operating-system course slides: a host I/O
hierarchy, a SPOOLing print path, and a disk physical/logical structure. The evaluator pins each
rendered slide by SHA-256, checks the crop boundary, limits the set to three cases and four
million pixels per crop, and sends only the crop rather than the PPTX or complete slide deck.
Source slides, crops, raw model text, and reports remain under ignored `output/evaluation/`.

Both providers used `visual-description-v2`, the same strict two-field JSON contract, a 120-second
per-call timeout, and a 2,048 output-Token limit. Automatic quality checks require reviewed text
anchors plus relation facts; relation terms may be Chinese or English because both providers
returned English descriptions while preserving Chinese labels. These deterministic checks are a
regression gate, not a replacement for manual semantic review.

| Metric | qwen3-vl-plus | step-3.7-flash |
|---|---:|---:|
| Calls / strict JSON | 3 / 3 | 3 / 3 |
| Text-anchor recall | 100% | 100% |
| Reviewed relation-fact recall | 88.89% (8/9) | 88.89% (8/9) |
| Citation-useful threshold | 3/3 | 3/3 |
| Human fully correct cases | 2/3 | 2/3 |
| Mean provider latency | 5,346.26 ms | 7,871.56 ms |
| P50 / nearest-rank P95 | 4,127.73 / 8,251.92 ms | 9,012.95 / 9,774.98 ms |
| Prompt / completion / total Token | 1,691 / 515 / 2,206 | 1,611 / 3,129 / 4,740 |
| Estimated total cost | CNY 0.006841 | Step Plan subscription quota |
| Cost per successful region | CNY 0.002280 | Not asserted for fixed subscription |

Manual review found one material relation error per provider. Qwen redistributed two devices as
one device on each of two controllers, while the diagram connects both devices to one controller.
StepFun correctly described that hierarchy, but on the disk crop it described the 1/2/3 sector
labels as track numbers. Both omitted the reviewed cross-platter explanation that aligned tracks
form a cylinder. Therefore 100% anchor recall and 88.89% keyword fact recall must not be quoted as
perfect chart understanding.

A preceding 1,024-output-Token pilot returned strict JSON for all three Qwen calls, but StepFun
returned empty final content on the second crop after one successful call; its remaining case was
suppressed by the evaluator's provider circuit breaker. At 2,048 Tokens StepFun completed 3/3.
This is evidence that the reasoning model needs a larger completion budget for this workload, not
evidence that image input is unsupported. The final paired run made Qwen about 32.1% faster by
mean latency and used 53.5% fewer total Tokens. MESGuard therefore retains `qwen3-vl-plus` as the
production Vision profile and keeps StepFun as a configurable candidate; the three-case set is too
small to claim general model superiority.

The current Alibaba Cloud China listing for `qwen3-vl-plus` at up to 32K input Tokens is CNY 1 per
million input Tokens and CNY 10 per million output Tokens. The evaluator exposes both prices as
arguments. Step Plan is recorded as subscription quota because no independent per-token amount was
asserted. Across the 1,024 pilot and 2,048 final run, measured DashScope cost was approximately
CNY 0.013782; no OCR call or whole-document VLM call was made.

```powershell
# SHA/crop validation only; no provider calls
.\scripts\evaluation\run_vlm_quality_eval.ps1 `
  -InputRoot '<folder containing the reviewed slide renders>'

# at most 3 cases x 2 providers; stops one provider after its first error
.\scripts\evaluation\run_vlm_quality_eval.ps1 `
  -InputRoot '<folder containing the reviewed slide renders>' `
  -ExecuteProvider
```

Pricing source: Alibaba Cloud Model Studio,
<https://help.aliyun.com/zh/model-studio/model-pricing>.

## Remaining Quality Gates

1. Add reviewed scanned-table, small-font, skewed and degraded-image OCR pairs before choosing
   8M as the default.
2. Expand PPTX quality beyond the current eight-slide structure set to chart data, SmartArt,
   speaker notes and cloud-enriched Element merge behavior.
3. Expand VLM quality beyond three diagrams to charts, screenshots, degraded crops and
   cloud-enriched Element merge behavior before claiming broad visual understanding.
4. Measure end-to-end upload-to-publish throughput with MinIO, RabbitMQ, PostgreSQL and pgvector
   after retrieval implementation is connected.
