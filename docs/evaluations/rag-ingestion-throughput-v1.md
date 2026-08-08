# Knowledge Ingestion Throughput Evaluation v1

## 目的与边界

该评测用于回答“混合文档入库的优化版本是否在保持产物完整性的前提下，使处理吞吐提升至少
40%”。当前完成了一个公开原生 PDF 的 `worker-core pilot`、三文档 PostgreSQL 批写消融、双文档
文档并发五轮消融，以及四十文档的零成本 Parser/Chunking corpus audit。五轮并发消融可以支撑
“受控 Worker-core 吞吐提升 40%+”的简历口径；其余结果用于验证链路、扩充语料和定位瓶颈，不能
把局部结果外推成四十文档混合视觉端到端指标。

当前真实观测覆盖：

- MinIO 原文上传、读取和 Element Artifact 写入；
- 真实 PDF/Office Parser、Chunking 和 DashScope `text-embedding-v4`；
- Knowledge Worker claim/checkpoint/publish；
- PostgreSQL Chunk 和 pgvector staging、current 发布；
- 每轮新建并在结束时删除对象、文档、版本、任务、事件、Outbox、Chunk 和临时用户。

当前刻意不覆盖 RabbitMQ 实际 publish/consume、云 OCR/VLM、本地 ONNX layout 和 HTTP 上传。因此
结果只能称为 Worker 核心链路，不是 upload-to-publish 端到端吞吐或生产 SLA。

## 固定语料与格式合同

语料清单位于 `testdata/rag-ingestion-throughput-v1.corpus.json`，每个文档固定发布方、来源页、HTTPS
下载地址、使用边界、本地文件名、字节数、SHA-256、页数、MIME 和 `formatClass`。下载脚本只写入
被 Git 忽略的 `output/evaluation/`，并在原子替换前校验大小与 SHA-256。`formatClass` 不按 MIME 推断，因为原生 PDF 与
扫描 PDF 都是 `application/pdf`，但会进入不同处理路径。验收集合固定为：

`native-pdf`、`scanned-pdf`、`docx`、`xlsx`、`pptx`、`png`、`jpeg`、`text`。

未知或缺失类别会在任何基础设施/Provider 调用前失败。类别也进入 corpus fingerprint；改变类别、
文档顺序、ID 或 SHA-256 后，旧 observation 不能继续作为同一工作负载参与比较。

当前全规模验收合同至少要求 40 份文档、8 类格式和 5 个完整 baseline/experiment pair。每对必须使用相同
corpus fingerprint、环境 fingerprint、文档数、格式数、字节和页数；成功/partial/失败文档集合、
Element 数和 Chunk 数有任何回退，`IntegrityPreserved` 都为 false。数据量或重复轮次不足时，
即使数值超过 40%，`AcceptanceEligible` 和 `MeetsTarget` 仍为 false。

## 2026-08-07 零成本 Corpus Audit

`-audit-only` 不连接 PostgreSQL、MinIO、RabbitMQ 或任何模型 Provider，但会真实执行生产 Parser 和
Chunking。当前固定清单已扩到 40 份公开文档、162,852,270 bytes，并覆盖全部 8 个格式类：
`native-pdf`、`scanned-pdf`、`docx`、`xlsx`、`pptx`、`png`、`jpeg`、`text`。结果为 27 份
`text_ready`、10 份 `text_ready_visual_pending`、3 份 `visual_enrichment_required`、0 份
`parser_failed`，共产生 5,946 个原始 Element、5,854 个可检索 Element、12,864 个 Chunk 和 139 个
视觉候选。`element-merge-v1` 抑制了 92 个同位置重复或仅包含分隔符/项目符号的非语义 Element；
它们仍保留在 Artifact 审计记录中，但不进入 Chunk/Embedding。

其中 8,179,132 bytes 是实际物化在 Parser 结果中的 Office/PNG/JPEG 图片字节。PDF `document_page` 只引用
同一份不可变源文件和页码，`Content` 为空，因此不再按每页重复累计整份 PDF 大小。这个口径用于估算
本地内存/制品压力，不代表后续 OCR/VLM 请求体大小；扫描 PDF 的 16 个页面候选当前产生 0 个文本
Chunk，`-estimate-only` 会正确报告 0 次文本 Embedding 请求并保留视觉增强需求。

新增的 28 份文档来自 NIST Advanced Manufacturing Series，覆盖增材制造、数字孪生、制造数据、
设备监测和产业统计，单份为 6 到 332 页。连同原有 NIST XLSX、RFC Editor 纯文本、Wikimedia
PNG/JPEG、Microsoft Office 和扫描专利，文档数与格式门槛现为 40/40、8/8。当前来源家族仍偏向
NIST，后续应补充其他发布方，而不是把 40 误写成 40 个独立来源。

准入过程还隔离了两个反例：83,854,098 bytes 的 NIST AMS 100-4 超过项目 50 MiB 上传上限；
NIST AMS 100-32 虽只有 50 页/1.70 MB，但 `GetTextByRow` 单文档运行超过 40 秒仍未完成。前者按
生产上传门禁拒绝，后者移出正向吞吐集并记为 PDF 解析隔离问题，随后用已通过审计的 NIST AMS
100-17 替换。最终 audit 在约 7.4 秒内完成，fingerprint 为
`04482449b8c4d2f82fb2196cb4a10d6851b3c56b69dff530740bc6dc22f029f4`。

`-estimate-only` 给出 12,864 个 Chunk；逐 Chunk 参考路径需要 12,864 次 Embedding 请求，生产
batch=10 路径需要 1,306 次，并按本地保守 Token 估算同时输出整轮预计费用。该估算没有发起 Provider
请求。虽然规模/格式门禁已经通过，但尚无
5 个完整全链路 pair，因此 `AcceptanceEligible=false`、`MeetsTarget=false`。

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

## 2026-08-07 Worker-Core 隔离与文档并发消融

观察器最初通过生产 `QueueVersion` 创建任务后直接调用内嵌 Worker，但同时留下了可见的
`knowledge.ingest` Outbox。正在运行的真实 Outbox Relay/Knowledge Worker 会抢先 claim 部分评测任务，
导致 Baseline 出现 `knowledge ingestion task lease is held`，而 Experiment 偶然成功。该 2 文档 pair
曾给出 487.42% 的失真增幅，`IntegrityPreserved=false`，已明确排除，不能参与任何性能结论。

修复后，评测器在同一 PostgreSQL 事务内创建任务并删除本次评测专属 Outbox，再提交任务事实；真实
Relay 因此永远看不到该消息。Observation 也新增逐文档任务状态、Worker action/reason、Element、Chunk
和 Embedding Token，避免失败只剩聚合数字。

隔离后的两文档组合 pair 覆盖 NIST IR 8108 与 ICSA-RW PPTX，共 42 个可检索 Element、70 个 Chunk。
串行参考路径 `1/1/1/1` 与组合实验 `2/10/2/100` 分别耗时 14,009 ms/1,088 ms，请求数 `70 -> 8`，
Chunk/向量 INSERT 批次均 `70 -> 2`，两边 Token 均为 9,606，完整性保持。1,187.59% 仍是四项优化
叠加的单 pair 观测，不用于简历指标。

随后运行只改变文档并发 `1 -> 2` 的三轮交替顺序消融；两边 Embedding batch=10、Embedding
并发=2、Chunk/向量 batch=100，且每轮均为 42 Element、70 Chunk、8 次请求、9,606 Token 和
2+2 写入批次：

| 指标 | 并发 1 | 并发 2 | 变化 |
| --- | ---: | ---: | ---: |
| 中位总耗时 | 1,942 ms | 1,052 ms | -45.83% |
| P95 总耗时 | 2,174.2 ms | 1,058.3 ms | -51.32% |
| 中位文档吞吐 | 61.79/min | 114.07/min | +84.60% |
| 三轮 Embedding 请求 | 24 | 24 | 0 |
| 三轮 Embedding Token | 28,818 | 28,818 | 0 |
| 三轮 Chunk/向量写入批次 | 6+6 | 6+6 | 0 |

三轮 `IntegrityPreserved=true`，说明在这个双文档小样本上文档级并发有独立净收益；但选择工作负载
只有 2 文档/2 类/3 pair，仍为 `AcceptanceEligible=false`、`MeetsTarget=false`，不能外推到 40 文档
混合视觉全链路。

### 预算保护后的五轮复测

加入 Provider 预检、费用硬预算、平滑限速和 429 熔断后，使用同一双文档固定集重新运行 5 个
baseline/experiment 交替顺序 pair。预检估算 80 次请求、100,830 Token、0.0504 元；由于保守估算
略高于默认 0.05 元，人工复核后仅把本次命令上限提高到 0.06 元。实际结果为每个 arm 8 次请求、
9,606 Token，整轮共 80 次请求、96,060 Token，按同步价格估算 0.04803 元，未调用 OCR/VLM。

| 指标 | 文档并发 1 | 文档并发 2 | 变化 |
| --- | ---: | ---: | ---: |
| 中位总耗时 | 2,124 ms | 1,450 ms | -31.73% |
| P95 总耗时 | 2,518 ms | 1,500.2 ms | -40.42% |
| 中位文档吞吐 | 56.50/min | 82.76/min | +46.48% |
| 五轮 Embedding 请求 | 40 | 40 | 0 |
| 五轮 Embedding Token | 48,030 | 48,030 | 0 |
| 每轮 Element / Chunk | 42 / 70 | 42 / 70 | 0 |
| 每轮 Chunk/向量写入批次 | 2+2 | 2+2 | 0 |

五轮均无失败回退，`IntegrityPreserved=true`，临时用户/文档残留为 `0|0`。这个结果在相同批处理、
Embedding 并发和数据库写入策略下，只改变文档并发 `1 -> 2`，因此可以支持“受控 Worker-core
文档处理吞吐提升 40%+”的简历口径。它仍只有 2 文档/2 格式，当前全规模门禁继续给出
`AcceptanceEligible=false`、`MeetsTarget=false`；不能表述成 40 文档混合视觉端到端提升 46.48%。

## 2026-08-07 四十文档 Provider Pilot（无效）

文档数与格式门禁通过后，运行了一个只改变文档并发 `1 -> 2` 的 40 文档 pair；两边均保持
Embedding batch=10、Embedding 并发=2、Chunk/向量 batch=100，且 OCR/VLM 关闭。

| 指标 | 并发 1 | 并发 2 | 说明 |
| --- | ---: | ---: | --- |
| 总耗时 | 280,459 ms | 116,097 ms | 表面变化不可用于性能结论 |
| 成功 / partial / failed | 27 / 10 / 3 | 17 / 9 / 14 | 失败集合发生回退 |
| Element | 5,854 | 5,381 | 不一致 |
| Chunk | 12,864 | 10,923 | 少处理 1,941 |
| Embedding 请求 | 1,306 | 1,106 | 因失败提前减少 |
| Embedding Token | 2,585,532 | 2,083,375 | 因失败提前减少 |

并发 1 arm 完整处理了全部可检索内容；3 个 failed 是关闭 OCR/VLM 后三份纯视觉文档的固定结果。
并发 2 arm 在后 11 份文档上收到百炼 `429 Throttling.AllocationQuota`，这些任务进入 `retry_wait`，
再加同样的 3 个纯视觉 failed，导致聚合 failed=14。汇总器因此给出：

```text
median_throughput_increase=141.57%
IntegrityPreserved=false
AcceptanceEligible=false
MeetsTarget=false
```

141.57% 是少处理文档造成的伪收益，明确排除。两个 arm 实际合计 2,412 次 Embedding HTTP 请求、
4,668,907 Token；按北京地域同步价 0.0005 元/千 Token 估算为 2.3345 元，与控制台账单约 2.5 元一致。
并发 2 arm 的平均 RPM 为 571.6，低于官方 1,800；平均 TPM 为 1,076,707，已经达到官方
1,200,000 TPM 上限的 89.7%，短时突发越过滚动窗口是最合理的 429 根因。百炼错误码文档也明确把
`Throttling.AllocationQuota` 归因为 TPS/TPM，而不是 API Key 的累计余额或永久硬额度。

因此不能删除失败文档、只比较成功子集，也不能继续重试来消耗费用。无效 observation 保留在本地
ignored 输出中，用于复核，不进入最终五轮汇总。官方口径见：

- `https://help.aliyun.com/zh/model-studio/rate-limit`
- `https://help.aliyun.com/zh/model-studio/error-code#token-limit`
- `https://help.aliyun.com/zh/model-studio/embedding`

## Provider 成本与限流保护

真实执行现在会在连接 PostgreSQL、MinIO 和 Embedding Provider 之前，本地运行 Parser、生产
Element Merge 与 Chunking，估算“两组 × 重复次数”的请求、Token 和费用。默认同步价格按
0.5 元/百万 Token，整条命令预算为 0.05 元；预估超出时直接拒绝，必须缩小语料，或人工复核后显式
提高 `-max-provider-cost-cny`。该预算是评测安全阈值，不代表供应商余额。

执行阶段还用保守 Token 估算进行匀速调度，默认 900 RPM、600,000 TPM，均为当前百炼北京地域
官方上限的一半。实际返回 Token 也累计校验；预计或实际 Token 越过费用预算时取消剩余请求。第一个
429 会立即取消整条命令，不再把后续失败任务跑完后生成不可比较的 Observation。OCR/VLM 仍默认关闭。
40 文档只继续用于零成本 Parser/Chunk audit；任何全规模 Provider pair 都需要先单独给出预计请求、
Token 和费用，并取得人工确认。

## 可复现命令

先做零成本语料和本地 Parser 检查：

```powershell
.\scripts\evaluation\fetch_rag_ingestion_corpus.ps1

go run ./cmd/mesguard-ingestion-throughput-observe -validate-only -max-documents 40
go run ./cmd/mesguard-ingestion-throughput-observe -audit-only -max-documents 40
go run ./cmd/mesguard-ingestion-throughput-observe -estimate-only -max-documents 40

go run ./cmd/mesguard-ingestion-throughput-observe `
  -estimate-only -document-concurrency-ablation `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster" `
  -repetitions 5

go run ./cmd/mesguard-ingestion-throughput-observe `
  -database-ablation -max-documents 3 -repetitions 5 -timeout 15m
```

真实 Provider pair 必须显式授权，并要求 PostgreSQL、MinIO、配置的 Embedding API Key 可用：

```powershell
go run ./cmd/mesguard-ingestion-throughput-observe `
  -execute-provider -max-documents 1 -repetitions 1 -timeout 15m `
  -max-provider-cost-cny 0.05 -provider-rpm 900 -provider-tpm 600000

go run ./cmd/mesguard-ingestion-throughput-observe `
  -execute-provider -document-concurrency-ablation `
  -document-ids "nist-ir-8108,icsarw-shikhaliyev-poster" `
  -repetitions 5 -timeout 15m `
  -max-provider-cost-cny 0.06 -provider-rpm 900 -provider-tpm 600000 `
  -output output/evaluation/rag-ingestion-document-concurrency-budgeted-v1.observations.jsonl

go run ./cmd/mesguard-ingestion-throughput-eval `
  -input output/evaluation/rag-ingestion-document-concurrency-budgeted-v1.observations.jsonl `
  -output output/evaluation/rag-ingestion-document-concurrency-budgeted-v1.summary.json `
  -target-increase-percent 40
```

Observation 和 summary 位于被 Git 忽略的 `output/evaluation/`；评测记录只保存人工复核后的聚合
结果，不提交 Provider 原始内容或凭据。不要为了凑够 8 类而复制同一文件或自行生成低质量文档。

## 下一步

1. 在已完成的双文档 5-pair 指标基础上，对 4 到 8 份代表性小文档运行 `-estimate-only`，只在默认
   0.05 元预算内补一个跨格式 Provider smoke，不再重复烧钱追逐同一增幅，也不自动运行 40 文档 pair；
2. 把 40 文档/8 格式保留为零成本兼容性与 Parser/Chunk 吞吐证据，把小规模真实 Provider pair
   用于完整链路 smoke 和单变量消融；二者分别报告，不能把局部增幅外推为全规模端到端增幅；
3. 是否仍投入 40 文档/5 pair 的同步 Provider 费用，须在预检给出总 Token/费用后单独决定；Batch API
   虽价格减半且不受同步限流，但异步语义不同，不能替代同步延迟对照；
4. 增加非 NIST 发布方与视觉质量难例，避免文档数量达标掩盖来源集中；
5. 为不可中断的 PDF 页文本提取增加进程级隔离或等价的可终止边界，防止异常 PDF 长时间占用 Worker；
6. 加入 RabbitMQ 实际投递，并把 OCR/VLM/ONNX 分成单独的质量、成本与吞吐分层，避免大页数
   VLM 测试污染主吞吐口径。
