# RAG Retrieval Fixed Set v1

## 目的与边界

这是一组用于比较 PostgreSQL FTS、pgvector 精确向量检索和 RRF 融合的固定集，
不是线上 SLA、真实企业知识库覆盖率或生产成本承诺。语料包含 12 份工业主题文档、
24 个 Chunk 和 24 条查询，其中包含 literal 与 paraphrase 两类问题。每条查询的相关
文档标签在 `testdata/rag-retrieval-v1.jsonl` 中固定，结果不会依赖人工临时判断。

评测程序在一个 PostgreSQL 事务中创建临时用户、文档版本、Chunk 和向量，测量结束后回滚；
因此不会污染开发数据库。Vector/RRF 会调用配置的 Embedding 服务，固定集只产生 24 个
文档 Chunk 的批量 Embedding 输入和 24 个查询输入，默认批次上限为 10，未调用 OCR/VLM。

## 指标口径

- `Recall@5`：24 条查询中，返回前 5 个不同文档后至少包含一份标注相关文档的比例；
- `MRR`：每条查询第一份相关文档排名的倒数的平均值；未命中记为 0；
- `averageQueryDurationMillis`：从查询开始到检索结果返回的平均耗时。Vector/RRF 包含查询
  Embedding 和 PostgreSQL 检索，不能与只测 SQL 的 FTS 延迟直接当作同一成本比较；
- `ingestionDurationMillis`：创建文档、切块、批量 Embedding 和向量写入的端到端耗时；
- `embedding*Requests`、`embedding*Tokens` 和 `embedding*DurationMillis`：来自供应商响应的
  成功调用 Token，以及按 query/document 输入类型统计的调用尝试数和耗时。Token 不用字符数估算；
- `embeddingEstimatedCostCNY`：只有命令行提供 `-embedding-price-cny-per-million` 时才计算，
  公式为 `embeddingTotalTokens / 1,000,000 * price`。未提供供应商当前价时不虚构成本。
- Rerank 评测使用 `rrf-rerank`：先取最多 30 个 FTS/Vector/RRF 候选，再调用真实 `qwen3-rerank`
  排到 K 个结果；记录请求数、供应商返回的 Token（若有）和耗时。供应商没有返回可解析 Token 时
  保持为未知，不用字符数反推成本。

## 2026-08-06 结果

| Retriever | Recall@5 | MRR | 平均查询 ms | 入库 ms | Embedding Token |
|---|---:|---:|---:|---:|---:|
| PostgreSQL FTS | 23/24 = 95.83% | 0.9028 | 1.46 | 78.09 | 0 |
| PostgreSQL Vector | 24/24 = 100% | 1.0000 | 187.19 | 1531.78 | 796 |
| PostgreSQL RRF | 24/24 = 100% | 0.9792 | 211.30 | 1393.33 | 796 |
| PostgreSQL RRF + qwen3-rerank | 24/24 = 100% | 0.9792 | 258.79 | 1417.85 | 796 |

RRF + Rerank 的 24 次真实 Rerank 请求全部成功，Rerank 累计耗时 1965.96 ms，平均约 81.9 ms/次。
本次供应商响应没有返回可解析的 `usage.total_tokens`，因此没有写入 Token 或成本。相对文档中另一轮
RRF 观测的 211.30 ms，整体均值增加 47.49 ms，约 22.5%；这是独立运行的观测差异，不能当作严格
因果或稳定 P95 结论。质量上 RRF + Rerank 没有超过当前 RRF 的 Recall/MRR，当前结论是“接口可用、
固定集未见质量提升、增加可观测延迟”，不能把 Rerank 默认打开。

Vector 和 RRF 的 796 Token 均由 604 个文档 Token 与 192 个查询 Token 构成；文档侧为 3
个批量请求，查询侧为 24 个请求。RRF 在本固定集上没有超过 Vector 的 MRR，说明融合策略
不是“必然提升排序质量”；它的价值是同时保留关键词精确命中和语义召回，并允许一条通道
失败时降级到另一条通道。FTS 的延迟优势来自不调用云端 Embedding，不能直接解释为完整
端到端延迟优势。

输出文件位于被忽略的 `output/evaluation/`：

- `rag-retrieval-v1-fts.summary.json` 和 `.observations.jsonl`；
- `rag-retrieval-v1-vector.summary.json` 和 `.observations.jsonl`；
- `rag-retrieval-v1-rrf.summary.json` 和 `.observations.jsonl`。
- `rag-retrieval-v1-rrf-rerank.summary.json` 和 `.observations.jsonl`。

## 重跑命令

```powershell
go run ./cmd/mesguard-rag-retrieval-eval `
  -retriever fts `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-fts.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-fts.summary.json

go run ./cmd/mesguard-rag-retrieval-eval `
  -retriever vector `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-vector.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-vector.summary.json `
  -embedding-price-cny-per-million <current-provider-price>

go run ./cmd/mesguard-rag-retrieval-eval `
  -retriever rrf `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-rrf.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-rrf.summary.json `
  -embedding-price-cny-per-million <current-provider-price>

go run ./cmd/mesguard-rag-retrieval-eval `
  -retriever rrf-rerank `
  -corpus testdata/rag-retrieval-v1.corpus.jsonl `
  -dataset testdata/rag-retrieval-v1.jsonl `
  -output output/evaluation/rag-retrieval-v1-rrf-rerank.observations.jsonl `
  -summary output/evaluation/rag-retrieval-v1-rrf-rerank.summary.json `
  -embedding-price-cny-per-million <current-provider-price> `
  -rerank-price-cny-per-million <current-provider-price>

# 成本受控冒烟：只评测一条查询，仍会重建固定集文档和 Embedding。
go run ./cmd/mesguard-rag-retrieval-eval `
  -retriever rrf-rerank -case-id rag-transaction-timeout-paraphrase `
  -output $env:TEMP\mesguard-rerank-smoke.observations.jsonl `
  -summary $env:TEMP\mesguard-rerank-smoke.summary.json
```

## 未覆盖事项

当前固定集没有覆盖同义词冲突、数值/否定条件、多文档答案、个人 scope 隔离、Chunk 边界
变化、查询改写的质量对照、逻辑 parent 展开的上下文质量、Web Search 或 HNSW；Rerank 也没有 nDCG@10 所需的多相关文档
分级排序标注。下一轮应补充带 graded relevance 的评测集，并记录 nDCG@10、MRR、Recall@5、
P50/P95、超时率和每千次查询成本。Query Rewrite 当前只有结构化契约 smoke，见
[`query-rewrite-v1.md`](query-rewrite-v1.md)，不能与本表质量指标合并。Advanced RAG paired 数据
合同、运行时 Search observer 与离线汇总器已经完成，但没有扩展黄金集或真实 Provider observations，
同样不能据此补写增幅。
