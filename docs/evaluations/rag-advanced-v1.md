# Advanced RAG Paired Evaluation v1

## 目的与边界

`rag-advanced-v1` 用于比较三类一次只改变一个变量的检索增强：

- `child -> parent`：保持 original Query 和 RRF 不变，只增加逻辑 Parent 邻接上下文；
- `original -> rewrite`：保持逻辑 Parent 和 RRF 不变，只启用受控 Query Rewrite；
- `parent -> parent + compression`：保持 Query、召回、排序和 Parent 扩展不变，只启用整 Chunk
  上下文压缩。

这不是生产知识库 benchmark。质量固定集包含 4 份公开官方文档、21 个 Chunk 和 5 个
人工标注 Case。Parent 和 Rewrite 轴目前各只真实运行了 `pool-limit-wait-risk` 一个 Case；Compression
轴已运行全部 5 个 Case。另有独立的 `rag-compression-pressure-v1` 单 Case 压力集，只验证生产压缩
阈值是否真实触发。单 Case 结果不能写成总体质量增幅，当前 5 Case 也仍不是生产知识库 benchmark。

## 可复现语料合同

- Corpus：`testdata/rag-advanced-v1.corpus.json`；
- Case：`testdata/rag-advanced-v1.jsonl`；
- 来源：Go 官方数据库文档和 PostgreSQL 官方 Deadlock 文档，记录 HTTPS URL 与抓取日期；
- 完整性：每份文档固定内容 SHA-256；
- 分块：`markdown-rune-v1`，`maxRunes=420`，`overlapRunes=40`；
- 黄金证据：`documentKey + ordinal + content SHA-256`。文档内容或分块规则漂移会使校验失败。

真实命令在 PostgreSQL 事务中创建临时用户、文档、版本、Chunk 和 Embedding，复用生产
`BuildKnowledgeSearchService`，完成 baseline/experiment 后整体回滚。默认最多执行一个 Case，
必须显式提供 `-execute-provider`；`-validate-only` 和 `-list-chunks` 不连接数据库或调用模型。

## 2026-08-06 单 Case 观测

### 逻辑 Parent 上下文

| 指标 | Child baseline | Parent experiment | 变化 |
| --- | ---: | ---: | ---: |
| Hit Rate@3 | 1.0000 | 1.0000 | 0 |
| Document Recall@3 | 1.0000 | 1.0000 | 0 |
| MRR | 1.0000 | 1.0000 | 0 |
| Context Precision | 0.3333 | 0.3333 | 0 |
| Context Recall | 0.5000 | 1.0000 | +0.5000 |
| 上下文 rune | 658 | 1375 | +108.97% |
| 查询耗时 | 139.390 ms | 184.195 ms | +32.14% |
| Query Embedding Token | 21 | 21 | 0 |

该 Case 表明逻辑 Parent 补回了一条缺失黄金 Chunk，但也使送入后续回答阶段的上下文增加约
一倍。它没有改善检索命中和排序，且没有改善 Context Precision。至少完成全部 5 个 Case 并增加
更多跨 Chunk 问题前，不能把 `Context Recall 0.5 -> 1.0` 外推成总体增幅。

### 受控 Query Rewrite

| 指标 | Original baseline | Rewrite experiment | 变化 |
| --- | ---: | ---: | ---: |
| Hit Rate@3 / Recall@3 / MRR | 1.0000 | 1.0000 | 0 |
| Context Precision / Recall | 0.3333 / 1.0000 | 0.3333 / 1.0000 | 0 |
| FTS Query 数 | 1 | 4 | 4 倍 |
| Vector Query 数 | 1 | 3 | 3 倍 |
| 查询耗时 | 185.615 ms | 8432.964 ms | +4434.33% |
| Query Embedding Token | 21 | 54 | +157.14% |
| Rewrite Token | 0 | 1152 | +1152 |

Rewrite 被策略接受，但没有改变返回文档或黄金 Chunk。该次调用使用 231 Prompt Token、921
Completion Token，总计 1152 Token；查询延迟约为 baseline 的 45.43 倍。Completion 包含供应商
可能计入的思考 Token，当前响应没有提供可单独核验的 reasoning 明细。此结果支持继续保持
Query Rewrite 默认关闭，而不是证明 Query Rewrite 普遍无效。

## 2026-08-07 五 Case 上下文压缩观测

Compression pair 使用 production 配置 `maxChunks=6`、`maxRunes=3000`、`minScore=0.05`。两组都先
执行 original Query、RRF 和逻辑 Parent；baseline 返回全部 Parent 邻接 Chunk，experiment 再按
查询词覆盖、protected signals、邻接距离和命中排名选择完整 Chunk。算法不摘要、不截断正文，保留
原 `contentText/contentSha256`。

| 指标 | Parent baseline | Compression experiment | 变化 |
| --- | ---: | ---: | ---: |
| Hit Rate@3 | 1.0000 | 1.0000 | 0 |
| Document Recall@3 | 0.9000 | 0.9000 | 0 |
| MRR | 1.0000 | 1.0000 | 0 |
| Context Precision | 0.2590 | 0.2590 | 0 |
| Context Recall | 0.7000 | 0.7000 | 0 |
| Parent 邻接 Chunk | 13 | 13 | 0 |
| 平均 Parent 邻接 rune | 575.4 | 575.4 | 0 |
| Query Embedding Token | 112 | 112 | 0 |
| 平均查询耗时 | 184.84 ms | 161.89 ms | -12.41%（非因果） |

本次 5 Case 没有任何 Chunk 达到压缩阈值：平均每个 Case 只有 2.6 个邻接 Chunk、575.4 rune，远低于
6 Chunk/3000 rune。因此实验只验证了 production SearchService、统计、Evidence 哈希和离线汇总链路，
**没有证明 Token 或上下文降低**。耗时差异容易受到数据库/Provider 抖动和 baseline-first 缓存顺序
影响，也不能写成压缩带来的性能提升。下一版必须增加多章节、多命中和长 Parent 压力 Case，或增加
单独标记的 stress arm；不能为了得到好看的降幅而直接把生产阈值调低。

## 2026-08-07 长 Parent 压力 Case

`rag-compression-pressure-v1` 固定 PostgreSQL 官方 Advisory Locks 和 Deadlocks 两份文档，共 17 个
Chunk。唯一 Case 以 `K=6` 同时询问 session/transaction 锁语义、共享内存上限和 `LIMIT` 求值顺序，
使命中分散到同一长章节。它与质量固定集分开，避免用刻意施压的 Query 抬高通用质量结果。

命令新增 `-require-compression-acceptance` 门禁：只有至少省略一个邻接 Chunk 且 Gold Context Recall
不下降才写出结果。第一次 `K=5` 诊断运行命中 `0/2/7/10/11`，邻接候选恰好为 6 个，被门禁拒绝；
未调整生产 `6 Chunk/3000 rune/0.05` 阈值。改为 `K=6` 后连续三次门禁运行结果一致：

| 指标 | Parent baseline | Compression experiment | 变化 |
| --- | ---: | ---: | ---: |
| Hit Rate@6 / Document Recall@6 / MRR | 1.0000 | 1.0000 | 0 |
| Context Precision | 0.4615 | 0.5000 | +0.0385 |
| Gold Context Recall | 1.0000 | 1.0000 | 0 |
| Parent 邻接 Chunk | 7 | 6 | -1 |
| Parent 邻接 rune | 1507 | 1438 | -4.58% |
| 命中 + 邻接总 rune | 3807 | 3738 | -1.81% |
| Query Embedding Token | 28 | 28 | 0 |

三次重复运行都使用最多 2 次文档 Embedding 和 2 次 Query Embedding，不调用 Rewrite、Rerank、VLM
或主聊天模型。该结果证明 production 阈值可触发、整 Chunk 选择保持哈希且本 Case 不丢黄金上下文；
4.58% 是单压力 Case 的邻接正文降幅，不是平均 Token 降幅。各轮延迟受顺序和网络调用抖动影响，
没有因果意义。要形成简历指标仍需多个不同来源、不同命中分布的压力 Case 和回答阶段 Token 观测。

## 2026-08-07 Agentic 二次检索固定集

`agentic-retrieval-v1` 不重复测底层召回排序，而是隔离验证 Evidence Gate 与模型的二次检索决策。
第一轮由固定 invoker 注入三种状态，第二轮才使用真实 `stepfun/step-3.7-flash`、生产
`EvidenceOrchestrator`、生产 Runner 和真实 `search_knowledge` Tool；KnowledgeSearcher 为固定内存
夹具，不调用 PostgreSQL、Embedding、Rerank、OCR、VLM 或 Web Search。

| Case | 第一轮状态 | 第二轮 `search_knowledge` | 新增稳定知识证据 | 停止原因 | Token | 耗时 |
| --- | --- | --- | --- | --- | ---: | ---: |
| `evidence-gap-search` | 缺少可追溯证据 | 实际调用 1 次 | 是 | `new_evidence_added` | 9678 | 9905 ms |
| `format-only-no-search` | 仅 confidence 格式错误 | Tool 被隐藏，0 次 | 否 | `not_eligible` | 6775 | 13613 ms |
| `valid-first-pass-no-search` | 首轮报告通过 | 不产生第二轮 | 否 | `not_needed` | 0 | 0 ms |

三 Case 的 attempt expectation accuracy、attempt precision/recall、added-evidence expectation accuracy、
stop-reason accuracy 均为 1.0，完成 3/3、partial 0、失败 0；合计 Prompt 13950、Completion 2503、
Total 16453 Token。正向 Case 的实际 Tool 序列为 `read_external_case -> search_knowledge`，新增证据按
`documentVersionId + chunkId + contentSha256` 判定，不把变化的 Evidence SourceRef 误认为新内容。

一次 8000 Token 预跑在 Provider 完成第三次模型响应后结算到 10842 Token，预算取消阻止了后续动作，
但日志出现 `context canceled`。这是 Usage 只能在单次响应返回后结算造成的软上限边界，不是检索失败；
评测默认已与生产 Evidence Gate 对齐为每 Case 16000 Token，完整运行未触发取消。该固定集只有 3 个
刻意构造的控制 Case，且第一轮报告和知识结果都是固定夹具，因此只能证明授权、选择、增量证据和
停止语义，不能宣称通用 Agentic RAG 准确率、答案质量提升或总体 Token 降低。

## 命令

```powershell
# 完全离线校验
go run ./tools/observation/mesguard-rag-paired-observe -validate-only

# 会调用 Embedding；单 Case、单变量、显式授权
go run ./tools/observation/mesguard-rag-paired-observe `
  -execute-provider -axis context -case-id pool-limit-wait-risk -timeout 3m

# 会调用 Embedding 和 ChatModel；Query Rewrite 仍不会修改生产配置
go run ./tools/observation/mesguard-rag-paired-observe `
  -execute-provider -axis rewrite -case-id pool-limit-wait-risk -timeout 3m

# 会调用 Embedding，不调用 Rewrite/Rerank/主聊天模型；当前运行全部 5 Case
go run ./tools/observation/mesguard-rag-paired-observe `
  -execute-provider -axis compression -retriever rrf -max-cases 5 -timeout 3m

# 单 Case 生产阈值压力门禁；没有真实省略或 Gold Context Recall 下降时失败
go run ./tools/observation/mesguard-rag-paired-observe `
  -corpus testdata/rag-compression-pressure-v1.corpus.json `
  -dataset testdata/rag-compression-pressure-v1.jsonl `
  -execute-provider -axis compression -require-compression-acceptance `
  -max-cases 1 -timeout 3m

# 三 Case Agentic 二次检索决策固定集；只在前两个 Case 产生真实 ChatModel 调用
go run ./tools/evaluation/mesguard-agentic-retrieval-eval `
  -execute-provider -max-cases 3 -timeout 90s
```

输出写入被忽略的 `output/evaluation/`。扩大 Case 数前必须先查看命令打印的 Provider 请求预算；
不能在未确认当前价格时虚构每千次成本。

## 下一轮

1. 离线复核 Parent/Rewrite 轴其余 4 个 Case，再分别完成全部 5 Case；
2. 增加数值/否定保真、缩写歧义、至少两份文档共同作答和更多不同命中分布的压力 Case；
3. 扩大压力集后汇总 Compression Ratio、Gold Context Recall 和回答阶段实际 Prompt Token，不外推单 Case；
4. 扩展 Agentic 固定集到检索失败、重复证据、模型未选择和答案/引用质量对照，并至少重复三轮；
5. 只有 paired 固定集证明净收益后，才评估默认开启 Query Rewrite 或扩大逻辑 Parent 窗口。
