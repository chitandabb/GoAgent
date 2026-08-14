# M4 Agent 评测与性能优化验收

> 验收日期：2026-08-14
> 验收方式：版本化资产审计、历史 Observation 重放、确定性指标重算和当前固定集复核
> 本轮新增 Provider 调用：0

## 结论

M4 已完成统一可靠性语义、OpenTelemetry Agent 链路、Evidence Gate Early Exit 成对质量门禁、
Global Knowledge Generation、L1 精确答案缓存、L2 pgvector 语义答案缓存和 Redis Stack 同合同消融。
最终简历只使用当前语义缓存固定集和完整命中链路的可复现结果；Tool 选择、Text-to-SQL、RAG 和综合
诊断的历史结果继续保留，但凡实现合同已经变化的资产均不作为第五点的当前量化证据。

原目标“缓存命中 P95 200 ms 内”和“平均模型调用成本降低 35%+”未被当前证据支持，已经从简历删除。
`MPR` 是错误指标名，统一更正为 `MRR`。

## 资产审计

`config/evaluation-assets-v1.json` 当前登记 23 项版本化评测/观测资产：

| 状态 | 数量 | 本轮处理 |
|---|---:|---|
| `reusable` | 5 | 校验声明制品及 SHA-256，在原有边界内继续复用 |
| `recomputed` | 4 | 从固定集或已记录 Observation 零 Provider 重算/复核 |
| `retest_needed` | 12 | 保留历史结果，但从当前简历证据中排除 |
| `obsolete` | 2 | 仅保留为评测器合同或历史记录 |

`mesguard-m4-acceptance` 会读取清单与 `config/m4-acceptance-v1.json`，校验每个声明制品，记录 Inventory、
运行配置和证据制品 SHA-256，并且只允许 `reusable/recomputed` 资产进入当前证据。引用
`retest_needed/obsolete` 资产会直接失败。报告固定记录 `providerCalls=0`，不会通过验收命令隐式调用模型。
当前 `testdata/m4-acceptance-v1.json` 绑定实现提交 `4ae71a0` 和运行配置 SHA-256，并纳入 QueryGuard、
Evidence Gate Fixture、语义缓存 Calibration、PostgreSQL 完整命中链路及 Provider 消融 5 项当前证据。

复现命令：

```powershell
go run ./tools/evaluation/mesguard-m4-acceptance `
  -inventory config/evaluation-assets-v1.json `
  -manifest config/m4-acceptance-v1.json `
  -runtime-config config/mesguard.toml `
  -output testdata/m4-acceptance-v1.json `
  -implementation-revision git:<acceptance-code-revision>
```

## 当前量化证据

### 语义答案缓存

固定集为 `semantic-cache-v1` 的 120 对人工复核问题，包含 38 对可复用样本和 82 对不可复用样本，按固定
种子分成 80 对 Calibration 和 40 对 Holdout。Embedding Profile 为 DashScope `text-embedding-v4` 的
指纹 `98c558...ae667`，规范化版本为 `semantic-question-v1`，阈值为 `0.8721208486635312`。

| 数据集 | Cache Precision | Cache Recall | Hit Rate | TP / FP / FN / TN |
|---|---:|---:|---:|---:|
| Calibration，80 对 | 100% | 16.00% | 5.00% | 4 / 0 / 21 / 55 |
| Holdout，40 对 | 100% | 7.69% | 2.50% | 1 / 0 / 12 / 27 |

该结果通过 98% Precision 发布门禁，但 Holdout 只命中 1 对，因此只能证明当前固定集上的保守正确性，
不能外推为生产命中率。Calibration/Observation 原始调用为 24 个 Embedding 请求、2526 Token；Ticket 09
只重算已记录向量，没有新增调用。

固定 5 问、20 次命中重放使用 PostgreSQL + pgvector 默认 Provider：

| 边界 | 样本 | P50 | P95 |
|---|---:|---:|---:|
| 仅 pgvector Lookup | 200 | 2.101 ms | 3.199 ms |
| Query Embedding + Lookup + 消息/Observation 提交 | 20 | 225.096 ms | 244.743 ms |

20 次完整命中均为 0 次主模型调用、0 次 Tool 调用、0 次降级。该 P95 排除浏览器网络和渲染，并明确高于
原 200 ms 目标；主要耗时来自公网 Query Embedding，而不是 pgvector 查询。

### PostgreSQL 与 Redis Stack 消融

两种 Provider 使用同一批 240 个向量、同阈值、同候选数、同冲突过滤和同固定流量。全索引诊断中，
PostgreSQL Lookup P50/P95 为 3.201/3.936 ms，Redis Stack 为 1.495/1.934 ms；两者产生相同的 1 个跨
Anchor 候选。人工复核确认“如何发布知识文档？”与“知识文档如何发布？”可复用，因此严格 Pair ID
不一致不是 Cache 误命中。

Redis Stack 的隔离索引更快，但其完整命中链路 P95 为 253.714 ms，没有优于 PostgreSQL 的 244.743 ms。
当前默认继续使用 PostgreSQL，Redis Stack 只保留为可选适配器，不为尚未体现的端到端收益增加生产依赖。

### Early Exit

3 个 reviewed Fixture Case 的 Baseline/Experiment 质量门禁通过：完成率、结论正确率和引用正确率均未回退，
模型调用 12 -> 10、Tool 调用 6 -> 5、总 Token 2700 -> 2300。该数据只验证成对评测器、应用侧 Evidence
Gate 和问题明细合同，不是 Provider 性能指标，也不写入简历量化结果。真实 Provider 固定集仍缺 27 个
reviewed Case；后续如复测，必须先声明 Case、请求和 Token 上限。

## 历史指标边界

| 领域 | 可重放历史结果 | 当前结论 |
|---|---|---|
| Tool 选择 | 45 对 Case，Filtered 97.78%，Tool Schema Prompt Token 降低 46.08% | 90 条 Observation 可重放；Tool Catalog、Skill 和会话 Tool 暴露已变化，需选择性复测后才能更新当前口径 |
| Text-to-SQL | 20 Case，规范化结果集执行正确率 100% | 模型 Profile 与编排合同已变化，不作为 M4 当前证据 |
| QueryGuard | 52/52，40/40 高风险操作拦截 | 确定性 `tsql-readonly-v1` 固定集本轮零 Provider 重跑一致 |
| RAG | 历史报告已使用 Recall@K、MRR、Context Precision 和 Citation Correctness | Advanced RAG、Query Rewrite 和回答链路已变化，未执行付费复测，不形成新指标 |
| 综合诊断 | 旧单 Case 与评测器 Sample | 与当前会话驱动诊断、Evidence Gate 和知识链路不一致，标记 `obsolete` |

这张表刻意区分“历史值可以审计重放”和“当前实现已重新测量”。本轮没有为了收口而扩大样本、并发、重试
或 Token 预算。

## 可靠性与可观测验收

- `strict`、`repair_then_fail`、`best_effort` 已分别接入授权/安全边界、严格结构化输出和可选增强；失败不会
  变成未经校验的自由文本，也不会创建第二套业务状态机。
- Query Rewrite、Embedding/Vector、FTS、Rerank、缓存和遥测故障均有明确基础路径及标准 Degradation Event；
  双路检索都失败时不会伪装成“正常零命中”。
- Eino Callback 产生 Agent、Model、Tool、Retrieval 和 Degradation 关联 Span，并保留 Provider、model、
  operation、Prompt/Completion/Cached/Reasoning Token 与延迟；默认不记录 Prompt、答案或证据原文。
- OpenTelemetry Exporter 和语义缓存不可用时，自动测试验证业务回答仍走基础 RAG。Langfuse 是可选开发
  Profile，未启用或拒绝数据不改变业务结果；完整 Langfuse UI 父子链路仍是显式运维 Smoke，不虚报为本轮完成。

## 选择性复测决定

本轮没有执行 Tool、Text-to-SQL、Advanced RAG、综合诊断、OCR、VLM、GitHub Code Search 或上下文治理的
真实 Provider 复测。原因不是把失败隐藏起来，而是第五点最终量化结论不依赖这些旧指标，并且相关合同、
云端版本或外部服务状态已经变化。未来只有在需要更新对应简历点或发布门禁时，才按清单中的
`retest_needed` 项逐个执行受控复测。
