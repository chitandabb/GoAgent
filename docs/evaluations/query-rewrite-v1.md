# Query Rewrite Contract Smoke v1

## 目的与边界

本记录验证 MESGuard 受控 Query Rewrite 的真实模型请求、严格 JSON 解码和 protected signals
门禁，不是检索质量 benchmark。当前没有足够固定样本计算 Recall@K、MRR、nDCG、Context
Precision，也没有证据证明改写能降低 Token 或成本。

生产配置保持 `[knowledge.retrieval.queryRewrite].enabled=false`。只有扩展固定集证明质量净收益，
并满足延迟、超时率和成本预算后，才考虑默认开启。

## 被测契约

- 输入：`SQL Server 2022 error 258 最近不能建立连接`；
- 模型：历史观察使用主 `stepfun-main`；当前实现按
  `[knowledge.retrieval.queryRewrite].modelProfile` 选择独立 Profile；
- Prompt：`config/prompts/query-rewrite.md`；历史观察使用 `query-rewrite-v1`，当前预算合同为
  `query-rewrite-v2`；
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

以下命令会产生一次真实、可能计费的 ChatModel 请求，并要求已配置 Query Rewrite Profile 密钥：

```powershell
go test -tags=integration ./internal/platform/queryrewrite `
  -run TestConfiguredQueryRewriteProfilePreservesProtectedSignals -count=1
```

集成测试强制使用 30 秒 Rewriter 预算并检查最终 QueryPlan 保留原 Query、通过 protected signals
门禁。普通 `go test ./...` 不会发起这次公网调用。

## 2026-08-06 首个真实 paired Case

`tools/observation/mesguard-rag-paired-observe` 已使用版本化公开语料和生产 SearchService 完成一个
`original -> rewrite` Case。Rewrite 被接受，但 Hit Rate@3、Document Recall@3、MRR 和 Context
Precision/Recall 均没有变化；FTS/Vector 查询数从 `1/1` 增加到 `4/3`，Query Embedding 从 21
增加到 54 Token，Rewrite 本身消耗 1152 Token，耗时从 185.615 ms 增加到 8432.964 ms。

这仍是单 Case，不是总体质量结论。它证明成本与延迟放大已经可测，并支持继续保持默认关闭。
语料、命令、完整指标和边界见 [`rag-advanced-v1.md`](rag-advanced-v1.md)。

## 2026-08-07 根因与改造

现象不是“Query Rewrite 天生要 1152 Token”，而是旧实现复用了主 Agent 的 StepFun Profile：
`reasoningEffort=medium`、`maxOutputTokens=4096`。`maxOutputRunes=2048` 只在响应返回后拒绝超长文本，
不会限制供应商生成的推理 Token，因此这组配置让一个短 JSON 改写任务产生了 231 Prompt + 921
Completion Token，并把总延迟放大到基础检索的约 45.43 倍。

当前已改为命名 Profile：主 Agent 使用 `activeProfile=stepfun-main`，Query Rewrite 指向独立
`qwen-rewrite`，候选配置为 `qwen3.6-flash`、Thinking disabled、temperature 0、3 秒、256 输出 Token、
最多 1 个子查询。Provider Factory 对 StepFun/DeepSeek/DashScope 的推理字段分别映射，并有离线请求
形状测试；构建失败时只将 `query_rewrite` 标为 provider failed，基础 FTS/Vector 继续运行。

新 Profile 尚未运行真实 paired Case，因此目前只能确认“根因已隔离、预算已前置”，不能声称 Token
或延迟已经下降。下一次真实测试最多先跑 1 个 Case，再决定是否扩到 5 个固定 Case。

第一次 `qwen-rewrite` 合同 smoke 已于 2026-08-07 执行 1 次，模型约 1.14 秒返回可解码 JSON，但
返回 2 个子查询，超过运行时 `maxSubqueries=1`，因此被确定性门禁拒绝。本次没有继续调用，也没有
因错误路径缺少 Usage 而臆测 Token。根因是 Prompt v1 固定写“0 到 2 个”，运行时预算却收紧为 1。

修复后，服务端在不可信 Query 旁显式传入 `maxSubqueries`，Prompt v2 只引用该运行时预算；服务端
仍保留返回后的数量校验，避免模型不服从 Prompt。该修复已通过离线单测，尚未进行第二次付费调用。

## 下一轮质量对照

扩展数据集至少覆盖口语省略、同义表达、错误码/版本/数值/否定保真、双 Chunk 答案和不需要改写
的直接查询。对每条 Query 运行 original-only 与 rewrite paired 对照，记录：

- Recall@5、MRR、nDCG@10、Context Precision/Recall；
- 查询放大倍数、Embedding 请求数、Prompt/Completion/Total Token；
- P50/P95、超时率、策略拒绝率、回退后基础检索成功率；
- 每千次查询估算成本，并把模型、Prompt version 和数据集版本固定。

paired 数据合同、运行时 Search observer、纯离线汇总器和真实 fixture 命令已经实现：黄金 Chunk
使用文档键、ordinal 和内容 SHA-256 固定，baseline/experiment 必须使用相同底层检索 profile、通道
与 K，且每次只改变 Query 或 Context 一个轴。固定集已有 5 个 Case，但目前只运行了其中一个，
因此仍不能新增总体质量结论。

在这些指标没有净收益前，只能表述为“实现了受控改写和可靠回退”，不能表述为“提升召回率”或
“降低 Token 消耗”。
