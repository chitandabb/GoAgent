# Knowledge Ingestion Throughput Evaluation v1

## 目的与边界

该评测用于回答“混合文档入库的优化版本是否在保持产物完整性的前提下，使处理吞吐提升至少
40%”。当前完成了一个公开原生 PDF 的 `worker-core pilot`、三文档 PostgreSQL 批写消融，以及
十二文档的零成本 Parser/Chunking corpus audit；这些结果用于验证评测链路、扩充真实语料并定位
主要瓶颈，不是最终验收结果，也不能写入 `docs/简历.md`。

当前真实观测覆盖：

- MinIO 原文上传、读取和 Element Artifact 写入；
- 真实 PDF/Office Parser、Chunking 和 DashScope `text-embedding-v4`；
- Knowledge Worker claim/checkpoint/publish；
- PostgreSQL Chunk 和 pgvector staging、current 发布；
- 每轮新建并在结束时删除对象、文档、版本、任务、事件、Outbox、Chunk 和临时用户。

当前刻意不覆盖 RabbitMQ 实际 publish/consume、云 OCR/VLM、本地 ONNX layout 和 HTTP 上传。因此
结果只能称为 Worker 核心链路，不是 upload-to-publish 端到端吞吐或生产 SLA。

## 固定语料与格式合同

语料清单位于 `testdata/rag-ingestion-pilot-v1.corpus.json`，每个文档固定发布方、来源页、HTTPS
下载地址、使用边界、本地文件名、字节数、SHA-256、页数、MIME 和 `formatClass`。下载脚本只写入
被 Git 忽略的 `output/evaluation/`，并在原子替换前校验大小与 SHA-256。`formatClass` 不按 MIME 推断，因为原生 PDF 与
扫描 PDF 都是 `application/pdf`，但会进入不同处理路径。验收集合固定为：

`native-pdf`、`scanned-pdf`、`docx`、`xlsx`、`pptx`、`png`、`jpeg`、`text`。

未知或缺失类别会在任何基础设施/Provider 调用前失败。类别也进入 corpus fingerprint；改变类别、
文档顺序、ID 或 SHA-256 后，旧 observation 不能继续作为同一工作负载参与比较。

最终验收至少要求 40 份文档、8 类格式和 5 个完整 baseline/experiment pair。每对必须使用相同
corpus fingerprint、环境 fingerprint、文档数、格式数、字节和页数；成功/partial/失败文档集合、
Element 数和 Chunk 数有任何回退，`IntegrityPreserved` 都为 false。数据量或重复轮次不足时，
即使数值超过 40%，`AcceptanceEligible` 和 `MeetsTarget` 仍为 false。

## 2026-08-07 零成本 Corpus Audit

`-audit-only` 不连接 PostgreSQL、MinIO、RabbitMQ 或任何模型 Provider，但会真实执行生产 Parser 和
Chunking。当前固定清单已扩到 12 份公开文档、21,684,171 bytes，并覆盖全部 8 个格式类：
`native-pdf`、`scanned-pdf`、`docx`、`xlsx`、`pptx`、`png`、`jpeg`、`text`。结果为 5 份
`text_ready`、4 份 `text_ready_visual_pending`、3 份 `visual_enrichment_required`、0 份
`parser_failed`，共产生 4,190 个 Element、6,177 个 Chunk 和 128 个视觉候选。

其中 8,179,132 bytes 是实际物化在 Parser 结果中的 Office/PNG/JPEG 图片字节。PDF `document_page` 只引用
同一份不可变源文件和页码，`Content` 为空，因此不再按每页重复累计整份 PDF 大小。这个口径用于估算
本地内存/制品压力，不代表后续 OCR/VLM 请求体大小；扫描 PDF 的 16 个页面候选当前产生 0 个文本
Chunk，`-estimate-only` 会正确报告 0 次文本 Embedding 请求并保留视觉增强需求。

本轮新增 NIST XLSX、RFC Editor 纯文本、Wikimedia Commons CNC PNG 和 3D 打印 JPEG。XLSX/文本
分别产生 185/2,197 个 Chunk，两张图片按预期进入 `visual_enrichment_required`。Wikimedia 文件页
标记为 CC BY-SA 4.0，原始二进制仅保存在被 Git 忽略的本地评测目录；仓库只固定来源页、字节数和
SHA-256。格式门槛已达到 8/8，但总文档数只有 12/40，且没有 5 个完整全链路 pair，因此
`AcceptanceEligible=false`、`MeetsTarget=false`。

## Baseline 与 Experiment

Baseline 是受控的串行参考实现，不等同于本次改动前一个 Git commit：

| 参数 | Baseline | Experiment |
| --- | ---: | ---: |
| 文档并发 | 1 | 2 |
| Embedding batch | 1 | 10 |
| Embedding 并发 | 1 | 2 |
| Chunk INSERT batch | 1 | 100 |
| pgvector INSERT batch | 1 | 100 |

生产改动为 `[knowledge].chunkWriteBatchSize` 和
`NewKnowledgeWorkerRepositoryWithBatchSize`：Chunk 与向量分别使用 GORM `CreateInBatches`，
同时保留同一 fenced 事务、内容哈希和发布门禁。`batchSize=1` 用于复现逐行 INSERT 参考路径。
Embedding 批处理/并发在此前已经存在，本评测把它与数据库批写一起组成 experiment，因此首次结果
只能归因于“Embedding 批处理/并发 + PostgreSQL 批写”的组合，不能声称 491.68% 全部来自数据库改动。

## 2026-08-07 Worker-Core Pilot

文档为 NIST IR 8108：27 页、1,872,790 bytes、7 个可检索 Element、32 个 Chunk。视觉 Provider
在本轮禁用，因此 baseline 和 experiment 都发布为 `partial`；两边 outcome、Element 和 Chunk
完全一致。

| 指标 | Baseline | Experiment | 变化 |
| --- | ---: | ---: | ---: |
| 总耗时 | 6686 ms | 1130 ms | -83.10% |
| Queue/MinIO 阶段 | 62 ms | 44 ms | 仅观测，不作因果结论 |
| Worker processing | 6623 ms | 1086 ms | -83.60% |
| Embedding 请求 | 32 | 4 | -87.50% |
| Embedding Token | 7904 | 7904 | 0 |
| Chunk INSERT 批次 | 32 | 1 | -96.88% |
| 向量 INSERT 批次 | 32 | 1 | -96.88% |
| 文档吞吐 | 8.97/min | 53.10/min | +491.68% |

该 pair 的 `IntegrityPreserved=true`，但只有 1 份文档、1 类格式和 1 个 pair，因此：

```text
AcceptanceEligible=false
MeetsTarget=false
```

单文档下 `documentConcurrency=2` 没有实际作用；本轮也没有逐项消融 Embedding batching 与数据库
batching。Provider 网络抖动、数据库缓存和 baseline/experiment 顺序都可能放大差异。491.68% 是
定位信号，不是最终性能结论。

## 2026-08-07 PostgreSQL 批写消融

为拆开首次 pilot 的混杂变量，`-database-ablation` 在计时前完成真实 PDF/DOCX 解析和 Chunking，
并为每个 Chunk 生成仅用于未发布 staging 版本的确定性归一化向量。计时区间只包含生产
`SaveParsedResult`；两边 Embedding batch/concurrency、Chunk、向量和事务边界相同，唯一变量为
`chunkWriteBatchSize=1/100`。该模式不调用 MinIO、RabbitMQ、Embedding、OCR、VLM 或 layout，
也不执行 `Complete`，临时版本不会成为 current。

固定集为两份 NIST 原生 PDF 和一份 Microsoft SSIS DOCX，共 3 文档、2 个 `formatClass`、743 个
Chunk。五轮交替 baseline-first/experiment-first 的结果如下：

| 指标 | Baseline | Experiment | 变化 |
| --- | ---: | ---: | ---: |
| 中位 staging 耗时 | 1752 ms | 406 ms | -76.15% |
| P95 staging 耗时 | 1812.2 ms | 440 ms | -75.72% |
| 中位 Chunk/s | 424.09 | 1830.05 | +331.52% |
| 配对吞吐变化中位数 | - | - | +319.21% |
| 每轮 Chunk INSERT 批次 | 743 | 9 | -98.79% |
| 每轮向量 INSERT 批次 | 743 | 9 | -98.79% |
| Provider 请求 / Token | 0 / 0 | 0 / 0 | 0 |

五轮的产物计数一致，结束后临时用户和知识文档残留为 `0|0`。该消融支持“减少 PostgreSQL
往返本身能显著提升 staging 吞吐”，但不能与 491.68% 组合 pilot 相加，也不能外推到包含 Parser、
网络 Provider、RabbitMQ 和视觉识别的端到端比例。它满足 5 个 pair，但只有 3 文档/2 类，因此
仍为 `AcceptanceEligible=false`、`MeetsTarget=false`。

三文档首轮还暴露了真实 Office 兼容性问题：SSIS DOCX 的 `word/document.xml.rels` 合法引用
`../customXml/item*.xml`，旧代码错误地把“离开 `word/`”视为“离开 ZIP 包”。Relationship 解析现按
虚拟包根逐段消解，允许同包跨顶层引用，仍拒绝真正越过根的 `..`、反斜杠、外部 URL 和 URI scheme；
对应回归测试已加入。该修复是语料兼容性前置项，不计入吞吐优化。

## 可复现命令

先做零成本语料和本地 Parser 检查：

```powershell
.\scripts\evaluation\fetch_rag_ingestion_corpus.ps1

go run ./cmd/mesguard-ingestion-throughput-observe -validate-only -max-documents 12
go run ./cmd/mesguard-ingestion-throughput-observe -audit-only -max-documents 12
go run ./cmd/mesguard-ingestion-throughput-observe -estimate-only -max-documents 12

go run ./cmd/mesguard-ingestion-throughput-observe `
  -database-ablation -max-documents 3 -repetitions 5 -timeout 15m
```

真实 Provider pair 必须显式授权，并要求 PostgreSQL、MinIO、配置的 Embedding API Key 可用：

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe `
  -execute-provider -max-documents 1 -repetitions 1 -timeout 15m

go run ./cmd/mesguard-ingestion-throughput-eval `
  -input output/evaluation/rag-ingestion-pilot-v1.observations.jsonl `
  -output output/evaluation/rag-ingestion-pilot-v1.summary.json `
  -target-increase-percent 40
```

Observation 和 summary 位于被 Git 忽略的 `output/evaluation/`；评测记录只保存人工复核后的聚合
结果，不提交 Provider 原始内容或凭据。不要为了凑够 8 类而复制同一文件或自行生成低质量文档。

## 下一步

1. 将公开语料从当前 12 份/8 类扩到 40 份/8 类，优先增加各类来源多样性、视觉质量难例和接近
   配置上限的大文档，并在 Provider 调用前持续执行零成本 signature/parser/chunk audit；
2. 在扩充到至少 3 个可完整入库文档后再做小规模 worker-core pair，验证文档并发是否产生净收益；
3. 最终验收至少运行 5 个交替顺序的完整 pair，保留完整性门禁；
4. 加入 RabbitMQ 实际投递，并把 OCR/VLM/ONNX 分成单独的质量、成本与吞吐分层，避免大页数
   VLM 测试污染主吞吐口径。
