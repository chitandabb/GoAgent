# Query Rewrite Contract Smoke v1

## 目的与边界

本记录验证 MESGuard 受控 Query Rewrite 的真实模型请求、严格 JSON 解码和 protected signals
门禁，不是检索质量 benchmark。当前没有足够固定样本计算 Recall@K、MRR、nDCG、Context
Precision，也没有证据证明改写能降低 Token 或成本。

生产配置保持 `[knowledge.retrieval.queryRewrite].enabled=false`。只有扩展固定集证明质量净收益，
并满足延迟、超时率和成本预算后，才考虑默认开启。

## 被测契约

- 输入：`SQL Server 2022 error 258 最近不能建立连接`；
- 模型：项目 `[models.chat]` 配置的 StepFun 模型；
- Prompt：`config/prompts/query-rewrite.md`，版本 `query-rewrite-v1`；
- 输出：严格 JSON，包含 lexical query、semantic query 和必须存在的 subqueries 数组；
- 门禁：原 Query 保留；错误码、版本、数字、时间和明确否定词不得丢失或新增；最多 2 个子查询；
- 失败语义：模型、解码或策略失败回退原 Query，标记 `provider_failed` 或 `policy_rejected`；调用方
  取消才中止整个检索。

## 2026-08-06 真实调用观察

本轮只进行了三次短 Query Rewrite 调用，没有调用 Embedding、Rerank、OCR 或 VLM：

| 次数 | 预算/调整 | 结果 | 结论 |
| ---: | --- | --- | --- |
| 1 | 10 秒内部预算 | 超时 | 默认 10 秒下真实供应商延迟不稳定 |
| 2 | 30 秒预算 | 约 7 秒返回，策略拒绝 | 模型输出没有完整保留 protected signals，门禁有效 |
| 3 | 输入显式携带服务端提取的 protected signals | 通过，测试总耗时观测约 17.6 秒 | 严格契约可用，但单次通过不是质量或 SLA 证据 |

本轮没有把供应商 Token 和价格固化为可复现观测，因此不报告成本。约 7 秒和约 17.6 秒也只是
单次开发环境观察，不是 P50/P95。

## 可复现命令

以下命令会产生一次真实、可能计费的 ChatModel 请求，并要求已配置项目 ChatModel 密钥：

```powershell
go test -tags=integration ./internal/platform/queryrewrite `
  -run TestStepFunQueryRewritePreservesProtectedSignals -count=1
```

集成测试强制使用 30 秒 Rewriter 预算并检查最终 QueryPlan 保留原 Query、通过 protected signals
门禁。普通 `go test ./...` 不会发起这次公网调用。

## 下一轮质量对照

扩展数据集至少覆盖口语省略、同义表达、错误码/版本/数值/否定保真、双 Chunk 答案和不需要改写
的直接查询。对每条 Query 运行 original-only 与 rewrite paired 对照，记录：

- Recall@5、MRR、nDCG@10、Context Precision/Recall；
- 查询放大倍数、Embedding 请求数、Prompt/Completion/Total Token；
- P50/P95、超时率、策略拒绝率、回退后基础检索成功率；
- 每千次查询估算成本，并把模型、Prompt version 和数据集版本固定。

在这些指标没有净收益前，只能表述为“实现了受控改写和可靠回退”，不能表述为“提升召回率”或
“降低 Token 消耗”。
