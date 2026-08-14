# Semantic Answer Cache v1

## 目的与边界

本记录验证 Global 企业知识问答的 L2 pgvector 语义答案缓存是否满足高 Precision 发布门禁，并测量
真实 PostgreSQL lookup 与包含公网 Query Embedding 的完整命中链路。缓存只复用由当前 Global
Knowledge Generation 支持的最终答案和引用，不缓存诊断、Web Search、附件、Personal 或上下文依赖答案。

## 数据集与人工标签

- 数据集：`testdata/semantic-cache-v1.json`，固定 120 对；
- 人工标签：38 对可复用、82 对不可复用，全部 `reviewed=true`；
- Calibration：80 对，其中 25 正例、55 负例；
- Holdout：40 对，其中 13 正例、27 负例；
- Holdout 不参与规则或阈值调整；在线 LLM Judge 不参与命中裁决。

初始校准暴露了三类确定性门禁缺口：时效/上下文问题、故障转移方向反转、启停动作相反。修复只
增加可解释的 protected facts，没有针对 Pair ID 写特例。校准始终复用同一份相似度 Observation，
规则调整没有重新调用 Embedding Provider。

## Embedding Observation 与阈值

- Provider/模型：DashScope `text-embedding-v4`，1024 维，Cosine，归一化；
- Profile Fingerprint：`98c558789fbf5e908a7ac30dfe2abae293587550343547d14c04e078d11ae667`；
- Global Knowledge Generation：`4`；
- Observation：24 次批量调用、2526 Embedding Token；
- 选择规则：Calibration Precision 至少 98%，再最大化 Recall；
- 最终阈值：`0.8721208486635312`，取最低已接收正例与下一相似度样本的中点，避免把运行时行为
  绑定到 Provider 浮点抖动。

| Split | Precision | Recall | Hit Rate | TP / FP / FN / TN |
| --- | ---: | ---: | ---: | ---: |
| Calibration (80) | 100% | 16.00% | 5.00% | 4 / 0 / 21 / 55 |
| Holdout (40) | 100% | 7.69% | 2.50% | 1 / 0 / 12 / 27 |

该结果通过发布门禁，但召回率很保守。这里优先避免错误复用企业答案；`100%` 只表示固定 Holdout
中的 1 次命中正确，不能外推为生产总体准确率。原始观测与报告分别见
`testdata/semantic-cache-v1.observations.json` 和 `testdata/semantic-cache-v1.calibration.json`。

## 延迟与降级

2026-08-14 在 Windows 开发机、Docker `pgvector/pgvector:pg16` 上执行
`semantic-cache-latency-v1` 固定流量重放。该数据集包含 5 个已人工确认的不同语义改写，每个问题重放
4 次；数据库夹具全部位于事务中并在结束后回滚：

| 边界 | 样本 | P50 | P95 | 额外模型调用 | 降级率 |
| --- | ---: | ---: | ---: | ---: | ---: |
| pgvector `LookupSemantic` | 200 | 2.101 ms | 3.199 ms | 不适用 | 0% |
| DashScope Query Embedding 完整链路 | 20 | 225.096 ms | 244.743 ms | 0 | 0% |

完整链路包含 Query Embedding、pgvector lookup、User/Assistant Message 与 Run Observation 提交，排除
浏览器网络和渲染。最终 Provider 重放共 21 次调用、439 Embedding Token：1 次批量验证/建索引，20 次
单问题命中；5 对问题的重复观测相似度为 `0.877562` 至 `0.968615`。命中不调用主聊天模型或 Tool。

原简历目标“命中请求 P95 200ms 内”未达到。数据库查询不是瓶颈，公网 Query Embedding 占主要延迟；
后续可评估本地 Embedding、Provider 区域/连接优化或异步预热，但不能用单问题或本地 Stub 指标替代
当前固定流量结果。阈值开发过程中另执行过一次 21 调用/239 Token 的单问题诊断重放，以及一次在
批量相似度检查后提前停止的调用；它们不进入最终 P50/P95。

## 可复现命令

校准不调用 Provider：

```powershell
go run ./tools/evaluation/mesguard-semantic-cache-eval `
  -mode calibrate `
  -dataset testdata/semantic-cache-v1.json `
  -observations testdata/semantic-cache-v1.observations.json `
  -output <new-report-path>
```

真实性能重放需要 PostgreSQL 配置和 DashScope Key，并使用独立 Observation 命令；成本门禁在连接
数据库或 Provider 前验证 1 次批量请求加 20 次单问题请求：

```powershell
go run ./tools/observation/mesguard-semantic-cache-observe `
  -fixture testdata/semantic-cache-latency-v1.json `
  -output <new-report-path> `
  -max-provider-calls 21
```

结构化结果见 `testdata/semantic-cache-v1.performance.json`。

## PostgreSQL / Redis Stack Provider 消融

Ticket 08 在不改变 Conversation、Eligibility、Generation、Embedding Profile 和阈值的前提下增加了
可选 Redis Stack Provider。单次部署只选择一个 Provider，不双写、不迁移缓存；PostgreSQL 仍负责
Global Knowledge Generation、来源和 Embedding Profile 的权威校验。Redis Stack 使用 Hash 保存答案，
RediSearch HNSW 负责向量候选召回，候选仍经过相同的 protected-facts 过滤。

同一 5 问、20 次完整命中固定流量结果如下：

| Provider / 边界 | 样本 | P50 | P95 | 降级率 |
| --- | ---: | ---: | ---: | ---: |
| PostgreSQL `LookupSemantic` | 200 | 2.101 ms | 3.199 ms | 0% |
| Redis Stack `LookupSemantic` | 200 | 2.624 ms | 3.215 ms | 0% |
| PostgreSQL 完整命中链路 | 20 | 225.096 ms | 244.743 ms | 0% |
| Redis Stack 完整命中链路 | 20 | 215.260 ms | 253.714 ms | 0% |

Redis Stack 相比 PostgreSQL 的 lookup P50 增加约 24.9%，P95 增加约 0.5%；完整链路 P50 降低约
4.4%，但 P95 增加约 3.7%。变化方向不一致且远小于 Query Embedding 抖动，不能证明 Redis Stack
带来稳定收益。一次 `docker stats --no-stream` 开发机快照中，独立 Redis Stack 容器约占 13.34 MiB，
PostgreSQL 容器约占 58.83 MiB；该快照只用于说明额外服务成本，不是生产容量结论。

120 对人工集使用同一 pairwise 相似度、Eligibility 和 protected-facts 决策，因此两个 Provider 的正式
发布口径仍为 Calibration Precision 100% / Recall 16%，Holdout Precision 100% / Recall 7.69%。该
Precision 只由人工标注的 Pair Gold Label 计算。同一次 24-call Observation 还复用同一批 240 个向量，
分别把 120 个 Anchor 放入 PostgreSQL 临时 pgvector 表和 Redis HNSW 索引，以相同阈值、候选上限、
顺序和冲突过滤执行全索引诊断：

| Provider | Calibration 严格 Pair 身份 | Holdout 严格 Pair 身份 | 跨锚点 | Lookup P50 / P95 |
| --- | ---: | ---: | ---: | ---: |
| PostgreSQL | 80% / 16% | 100% / 7.69% | 1 | 3.201 / 3.936 ms |
| Redis Stack | 80% / 16% | 100% / 7.69% | 1 | 1.495 / 1.934 ms |

两端唯一的跨锚点返回完全一致：`hardneg-06` 的“如何发布知识文档？”命中了 `context-01` 的锚点
“知识文档如何发布？”，相似度约 `0.935545`。人工复核认为两问可共享答案，因此表中的 80% 只表示
严格 Pair ID 身份，不是 Cache Precision，不能据此判定错误答案；当前 120 对数据只标注 pairwise
关系，不能充当所有锚点之间的全局相关性标签。该诊断证明两种索引在固定集上的候选行为一致，同时
Redis 的纯索引查询更快，但完整链路收益仍被公网 Query Embedding 吞没。结构化结果见
`testdata/semantic-cache-v1.provider-ablation.observations.json`。

PostgreSQL 与 Redis Stack 共享的 tagged contract suite 覆盖 exact/semantic lookup、逻辑 TTL、答案大小
限制，以及 Lookup/LookupSemantic/Put/IndexSemantic 的取消与超时；各后端的存储专项测试继续覆盖容量、
Generation 失效和 malformed record。Redis Stack 运行时连接或索引初始化失败时不挂载答案缓存，后续按
原有 best-effort 语义继续正常 RAG。可选 Compose Profile 不替换现有基础 Redis；本轮实测配置的 lookup/
write timeout 分别为 100/200 ms，固定流量没有 timeout 或降级事件。

**决策：保留 Redis Stack 适配器作为候选，不作为默认部署。** PostgreSQL/pgvector 已是必需依赖，
当前消融没有证明增加一个常驻服务能换来可重复的 P95 或质量收益；默认配置继续使用 `postgres`。

Redis Stack 完整链路最终观测使用 21 次调用、439 Embedding Token。120 对双 Provider 全索引最终观测
使用 24 次批量调用、2526 Embedding Token；在修正 Eligibility 夹具、补充跨锚点明细和统一 PostgreSQL
候选边界前还执行了三次同规模预跑。因此 Ticket 08 实际新增合计 117 次 Embedding 调用、10543 Token，
未调用聊天模型、Rerank、OCR 或 VLM。

```powershell
docker compose --profile semantic-cache-redis-stack up -d redis-stack

go run ./tools/observation/mesguard-semantic-cache-observe `
  -fixture testdata/semantic-cache-latency-v1.json `
  -output <new-report-path> `
  -max-provider-calls 21 `
  -cache-provider all

go run ./tools/evaluation/mesguard-semantic-cache-eval `
  -mode observe `
  -dataset testdata/semantic-cache-v1.json `
  -output <new-observation-path> `
  -generation <current-global-generation> `
  -max-provider-calls 24 `
  -cache-provider redis-stack
```

Redis 固定流量结构化结果见 `testdata/semantic-cache-v1.redis-stack.performance.json`。
