# 评测索引

这里保存固定数据集、Provider-free 合同测试和受控 Provider Smoke 的方法与结果。评测记录用于工程判断和回归，不代表线上 SLA，也不代表真实客户数据质量。

## 推荐先看

| 文档 | 关注点 |
| --- | --- |
| [Tool Selection v4](tool-selection-v4.md) | 固定 Tool Profile、Schema 指纹和工具选择评测 |
| [Conversation Text-to-SQL v2/v3](text-to-sql-conversation-v2.md) | 会话入口、只读 SQL、Tool 序列和失败分类 |
| [RAG Retrieval v1](rag-retrieval-v1.md) | FTS、Vector、RRF 和引用相关检索结果 |
| [Knowledge Ingestion Quality v1](knowledge-ingestion-quality-v1.md) | 文档解析、Office/PDF、视觉候选和资源边界 |
| [Evidence Gate Early Exit v1](evidence-gate-early-exit-v1.md) | 证据不足时的提前退出与报告边界 |
| [M4 Acceptance v1](m4-acceptance-v1.md) | 评测资产审计、历史结果分类和当前证据边界 |

## 完整记录

- [Conversation Quality v1](conversation-quality-v1.md)
- [Evidence Gate Early Exit v1](evidence-gate-early-exit-v1.md)
- [Knowledge Ingestion Quality v1](knowledge-ingestion-quality-v1.md)
- [Layout Routing Public v1](layout-routing-public-v1.md)
- [M4 Acceptance v1](m4-acceptance-v1.md)
- [Query Rewrite v1](query-rewrite-v1.md)
- [RAG Advanced v1](rag-advanced-v1.md)
- [RAG Ingestion Throughput v1](rag-ingestion-throughput-v1.md)
- [RAG Retrieval v1](rag-retrieval-v1.md)
- [Semantic Answer Cache v1](semantic-answer-cache-v1.md)
- [Table Recovery v1](table-recovery-v1.md)
- [Conversation Text-to-SQL v2](text-to-sql-conversation-v2.md)
- [Tool Selection v4](tool-selection-v4.md)

## 口径

- 固定集结果必须记录数据集范围、实现 revision 和配置身份。
- 历史合同变化后的结果只能标为历史、`retest_needed` 或 `obsolete`，不能混入当前汇总。
- Provider 调用必须显式授权，并设置 Case、调用次数、Token、超时和输出路径上限。
- 原始 Provider 响应、日志、密钥和本地产物不提交到仓库；仓库只保存可复核的摘要和合成固定集。
