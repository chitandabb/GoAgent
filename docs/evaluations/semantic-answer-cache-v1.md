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
