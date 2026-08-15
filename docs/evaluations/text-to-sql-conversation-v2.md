# Conversation Text-to-SQL v2 评测

## 目标与边界

`mesguard-text2sql-eval -mode conversation` 验证生产 Conversation 入口，而不是历史 direct 模式的强制 Tool Calling 能力测试。用户问题直接来自版本化固定集，进入真实 `ConversationRunner`、固定 `conversation-default` Tool Profile、`turn_context`、`RunAccess`、SQL Tool 与 `ReadonlyQueryExecutor`/QueryGuard；评测器不直接调用 SQL Tool，也不使用 `WithToolChoiceForced`。

Conversation v2 使用独立的 `text-to-sql-conversation-observation-v2` 合同，记录模型/Profile/实现 revision、Tool Schema 指纹、完整 Tool 尝试数、SQL 调用序列、SQL hash、结果集、最终答案、usage 与耗时。正式汇总要求同一身份且 `formal=true`；dirty/unknown 只允许生成明确标记为非正式的本地 smoke 汇总，不能和 clean 观测混合。

正确性拆为四层：

- Tool 轨迹完整：Runner 总调用数与 SQL 边界 recorder 一致，非 SQL 调用不能静默消失；
- Tool 顺序合规：允许一次或多次 `search_schema_catalog`，随后只能有一次最终 `execute_readonly_query`；
- SQL 执行正确：规范化列名、值、顺序和截断状态与固定集一致；
- 答案正确：最终自然语言答案必须保留固定集中的全部期望标量。这是确定性的 grounded-value 检查，不是 LLM Judge，也不宣称覆盖措辞质量或完整语义等价。

失败回合仍保留 Runner 已产生的 Provider usage；畸形 SQL Tool 参数形成单 case 的 `invalid_tool_arguments` 观测，不会中断整个数据集。`direct` 仍是默认模式，保持历史单次 Generate + 强制单 Tool Call 语义。

## 2026-08-15 正式小样本

运行身份：

- clean revision：`332a4c1`；
- Provider/模型：StepFun `step-3.7-flash`；
- reasoning effort：`medium`；
- Prompt：`conversation-v7`；
- Tool Profile：`conversation-default`；
- 固定集子集：`sql-total-cases`、`sql-new-count`、`sql-urgent-count`；
- 上限：3 Case、24 次 Provider 调用、48,000 Token；
- 原始观测：`testdata/text-to-sql-conversation-v2.observations.jsonl`；
- 汇总：`testdata/text-to-sql-conversation-v2.summary.json`。

| 指标 | 结果 |
| --- | ---: |
| Tool 顺序合规 | 2 / 3（66.7%） |
| SQL 执行正确 | 2 / 3（66.7%） |
| 答案正确 | 2 / 3（66.7%） |
| 端到端正确 | 2 / 3（66.7%） |
| 模型调用 | 18 |
| Prompt Token | 39,147 |
| Completion Token | 1,966 |
| Total Token | 41,113 |
| Cached Token | 33,088 |
| 平均耗时 | 6.749 秒 |

成功 Case 都通过自然语言输入自主完成 schema 搜索、只读 SQL 与最终答案；`sql-new-count` 生成 `Status = 'New'` 的计数 SQL，`sql-urgent-count` 生成 `Priority = 'Urgent'` 的计数 SQL，结果和答案均为 `2`。

失败 Case `sql-total-cases` 连续 7 次调用 `search_schema_catalog`，未调用 `execute_readonly_query`，最终以 `invalid_tool_sequence` 失败。它说明当前生产瓶颈不是 QueryGuard 或数据库执行，而是模型在 schema 探索阶段缺少收敛：固定 Tool Schema 与授权边界正确，但仅靠 SOP 仍可能重复搜索直至迭代预算耗尽。

下一步不应立即扩大固定集，而应先验证低成本收敛策略：在 Conversation Prompt 中明确“已有足够对象/列信息后必须执行查询”，并考虑对 `search_schema_catalog` 设置每轮可配置调用上限。该限制属于执行期 Tool policy，不改变固定 Profile Schema，也不应通过隐藏 Tool 破坏 Prompt Cache。优化后再用同一 3 Case 和相同身份合同复测，随后才扩展到 20 Case。

## 被丢弃的试跑

本轮另有两次 Provider 试跑不得计入正式指标：

- 旧的“严格恰好一次 search + 一次 query”判定把模型的多次 schema 搜索全部误判为失败，消耗 15 次调用、30,852 Token；该结果只用于修正评测合同；
- 修正序列后首次子集文件漏写 `sql-urgent-count.expectedRows`，消耗 15 次调用、32,147 Token；SQL 实际返回 `2`，但错误夹具期望零行，因此整轮作废。

本次验收实际 Provider 总量为 48 次模型调用、104,112 Token，其中正式可引用结果仅为最后一轮的 18 次调用、41,113 Token。价格不在程序侧估算。
