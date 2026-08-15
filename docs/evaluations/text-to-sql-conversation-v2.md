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

该轮结果只能说明 StepFun 在这一小组 Case 上存在 schema 探索不收敛，不能直接推出所有 Provider 都需要相同的硬调用上限。后续必须通过命名 Profile 在同一生产入口上做受控对照，再决定是模型选择、Prompt 引导还是执行期 Tool policy 问题。

## 2026-08-15 OpenCode Go 命名 Profile 单 Case 正式复测

评测器在 clean revision `2c6dcf6` 增加 `-profile` 与 `-case-id`：命名 Profile 选择不会修改生产 `activeProfile`，Profile 指纹、Provider 构造和 Observation 身份都来自实际选择的最终 Profile；单 Case 选择发生在成本预算和 Provider 创建之前。以下是两个相同 revision/Profile/Prompt/Tool Schema 身份下的独立正式单 Case 运行，不作为完整 2 Case 或 20 Case 固定集汇总：

- Provider/Profile：OpenCode Go `opencode-deepseek-main`；
- 模型：`deepseek-v4-flash`；
- reasoning effort：`none`；
- Prompt：`conversation-v7`；
- Tool Profile：`conversation-default`；
- 每次上限：1 Case、8 次 Provider 调用、16,000 Token；
- 原始 Observation/Summary：本地忽略目录 `output/evaluation/text-to-sql-opencode-*.{observations.jsonl,summary.json}`。

| Case | 结果 | Tool 顺序 | 模型调用 | Total Token | Cached Token | 耗时 |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| `sql-new-count` | 端到端正确 | schema search ×2 → readonly query | 3 | 7,024 | 4,352 | 6.531 秒 |
| `sql-total-cases` | 端到端正确 | schema search ×3 → readonly query | 4 | 8,619 | 7,680 | 9.771 秒 |

两次运行共消耗 7 次模型调用、15,643 Token，其中 12,032 Cached Token；价格仍不在程序侧估算。`sql-total-cases` 从 StepFun 的 7 次 schema search 后未执行 SQL，变为 OpenCode Go 的 3 次 search 后正确执行 `SELECT COUNT(*) AS Total FROM dbo.v_MESGuardExternalCases` 并回答 `4`。这证明生产 SQL Tool、RunAccess、QueryGuard 和数据库执行链路能够完成该 Case，原失败至少部分是模型行为差异，而不是统一运行时能力断裂。

当前不设置 `search_schema_catalog=2` 的生产硬上限：正确的 `sql-total-cases` 已实际需要 3 次 search，而且现有 `agentToolRunPolicy` 超限会终止整轮 Conversation，而不是把可恢复结果交还模型。重复 search 仍是 Token/延迟优化项；下一步应先在同一 3 Case 上做 StepFun/OpenCode 可比复测，再以不改变固定 Tool Schema、不牺牲正确率为前提评估 Prompt 收敛或可恢复的执行期反馈。扩大到 20 Case 仍需等待可比小样本稳定。

## 被丢弃的试跑

本轮另有两次 Provider 试跑不得计入正式指标：

- 旧的“严格恰好一次 search + 一次 query”判定把模型的多次 schema 搜索全部误判为失败，消耗 15 次调用、30,852 Token；该结果只用于修正评测合同；
- 修正序列后首次子集文件漏写 `sql-urgent-count.expectedRows`，消耗 15 次调用、32,147 Token；SQL 实际返回 `2`，但错误夹具期望零行，因此整轮作废。

本次验收实际 Provider 总量为 48 次模型调用、104,112 Token，其中正式可引用结果仅为最后一轮的 18 次调用、41,113 Token。价格不在程序侧估算。
