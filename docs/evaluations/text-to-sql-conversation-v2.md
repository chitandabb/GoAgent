# Conversation Text-to-SQL 评测（v2 历史基线 / v3 当前合同）

## 目标与边界

`mesguard-text2sql-eval -mode conversation` 验证生产 Conversation 入口，而不是历史 direct 模式的强制 Tool Calling 能力测试。用户问题直接来自版本化固定集，进入真实 `ConversationRunner`、固定 `conversation-default` Tool Profile、`turn_context`、`RunAccess`、SQL Tool 与 `ReadonlyQueryExecutor`/QueryGuard；评测器不直接调用 SQL Tool，也不使用 `WithToolChoiceForced`。

当前评测器已硬切到独立的 `text-to-sql-conversation-observation-v3` 合同，记录模型/Profile/实现 revision、Tool Schema 指纹、完整 Tool 尝试数、SQL 调用序列、SQL hash、结果集、最终答案、usage 与耗时；同时对 `search_schema_catalog` 额外记录归一化 keyword 的 SHA-256 hash、返回数组长度和同 Case 内是否重复。原始 keyword、参数 JSON 和 Tool 返回正文不落盘。v3 Validator 与 reducer 拒绝历史 direct v1 和 conversation v2 观测；旧 v2 资产仅作历史基线，不得混入 v3 汇总。正式汇总要求同一身份且 `formal=true`；dirty/unknown 只允许生成明确标记为非正式的本地 smoke 汇总，不能和 clean 观测混合。

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

评测器在 clean revision `2c6dcf6` 增加 `-profile` 与 `-case-id`：命名 Profile 选择不会修改生产 `activeProfile`，Profile 指纹、Provider 构造和 Observation 身份都来自实际选择的最终 Profile；单 Case 选择发生在成本预算和 Provider 创建之前。以下三个相同 revision/Profile/Prompt/Tool Schema 身份下的独立正式单 Case 运行覆盖了 StepFun 小样本的同一组 Case；原始文件保持逐 Case 独立，没有伪装成一次多 Case 汇总：

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
| `sql-urgent-count` | 失败：未执行 SQL | schema search ×8 | 5 | 11,781 | 10,112 | 10.055 秒 |

三次运行的 Tool/SQL/答案/端到端准确率均为 2/3（66.7%），共消耗 12 次模型调用、26,060 Prompt Token、1,364 Completion Token、27,424 Total Token，其中 22,144 Cached Token，平均耗时 8.786 秒。价格仍不在程序侧估算。与 StepFun 同三 Case 的描述性基线相比，OpenCode Go 的模型调用和 Total Token 都减少约 33.3%，但平均耗时高约 30.2%；两组 observation 来自不同实现 revision 和模型身份，不作为严格 paired 因果指标。

失败位置发生了迁移：`sql-total-cases` 从 StepFun 的 7 次 schema search 后未执行 SQL，变为 OpenCode Go 的 3 次 search 后正确执行 `SELECT COUNT(*) AS Total FROM dbo.v_MESGuardExternalCases` 并回答 `4`；但 `sql-urgent-count` 在 OpenCode Go 上连续成功调用 schema search 8 次，仍未执行 SQL。这证明生产 SQL Tool、RunAccess、QueryGuard 和数据库执行链路能够完成这些查询，同时也证明单纯替换 Provider 不能解决 schema 探索不收敛。

当前不设置 `search_schema_catalog=2` 的生产硬上限：正确的 `sql-total-cases` 已实际需要 3 次 search，而且现有 `agentToolRunPolicy` 超限会终止整轮 Conversation，而不是把可恢复结果交还模型。下一步应先补齐 schema search 的受控诊断事实（至少记录归一化 keyword hash、返回条数和是否重复），再以不改变固定 Tool Schema、不牺牲正确率为前提比较“Prompt 明确元数据/业务值边界”和“可恢复重复搜索反馈”。在失败根因可观察之前不新增 Planner、意图分类器或硬限流；扩大到 20 Case 仍需等待三 Case 收敛。

## 2026-08-15 Observation v3 单 Case 诊断复测

Observation v3 在 clean revision `37b361c` 上对之前失败的 `sql-urgent-count` 只执行了一次受控正式复测：

- Provider/Profile：OpenCode Go `opencode-deepseek-main`；
- 模型：`deepseek-v4-flash`；
- Prompt / Tool Profile：`conversation-v7` / `conversation-default`；
- 上限：1 Case、8 次 Provider 调用、16,000 Token、2 分钟；
- 本地原始产物：`output/evaluation/text-to-sql-conversation-v3-opencode-go-sql-urgent-count.{observations.jsonl,summary.json}`（Git 忽略）。

| 顺序 | Tool | keyword hash | 返回条数 | 同 Case 重复 |
| ---: | --- | --- | ---: | --- |
| 1 | `search_schema_catalog` | `sha256:e45308061f71ca078488dab3f2ce373b3838d355dcbedc7ca3297a6c9636a39c` | 3 | 否 |
| 2 | `search_schema_catalog` | `sha256:3092a5f0dba05ff3cadc3c2c8eeacd47940b1837f9534bc559d19d8435c8b219` | 1 | 否 |
| 3 | `execute_readonly_query` | — | — | — |

该回合端到端正确：Tool 轨迹、SQL 执行、自然语言答案均通过。共 3 次模型调用，Prompt/Completion/Total Token 为 `6437/252/6689`，Cached Token `5760`，Reasoning Token `100`，耗时 5.796 秒。

本次观测只证明“两个不同 keyword 都命中正结果后，模型能够收敛到 SQL”；之前 8 次 search 的失败未在 v3 复测中复现，而历史 v2 观测又没有 keyword/result-count 诊断，因此不能宣称 Prompt 或生产策略已修复。当前决策是保持生产 Prompt、Tool Schema 和调用上限不变，不新增 Planner/意图分类器；以后只在 v3 失败样本出现时按事实选择治理：全部零结果则改进“元数据词 vs 业务值”引导；已有正结果仍持续扩散搜索则补停止条件；同 hash 重复则设计可恢复的重复搜索反馈，而不直接终止整轮。

## 2026-08-16 OpenCode Go 20 Case 正式全量复测

Prompt 前缀稳定修正和生产主模型硬切后，在 clean revision `7c4f5d6` 上完成固定集全部 20 Case 的一次正式运行：

- Provider/Profile：OpenCode Go `opencode-deepseek-main`；
- 模型：`deepseek-v4-flash`；
- Prompt / Tool Profile：`conversation-v8` / `conversation-default`；
- Observation：`text-to-sql-conversation-observation-v3`；
- 实现身份：clean revision `7c4f5d6`，`formal=true`；
- 原始观测：`output/evaluation/text-to-sql-opencode-conversation-v3-20.observations.jsonl`；
- 汇总：`output/evaluation/text-to-sql-opencode-conversation-v3-20.summary.json`。

在 revision `7c4f5d6` 的 clean checkout 中，配置好 OpenCode Go 与只读 SQL Server 所需环境变量后，可用以下脱敏命令复现；命令不包含或打印凭据：

```powershell
go run ./tools/evaluation/mesguard-text2sql-eval `
  -mode conversation -profile opencode-deepseek-main -timeout 10m `
  -allow-provider-calls -max-cases 20 -max-provider-calls 160 -max-provider-tokens 320000 `
  -output output/evaluation/text-to-sql-opencode-conversation-v3-20.observations.jsonl `
  -summary output/evaluation/text-to-sql-opencode-conversation-v3-20.summary.json
```

| 指标 | 结果 |
| --- | ---: |
| Tool 顺序合规 | 19 / 20（95%） |
| SQL 执行正确 | 18 / 20（90%） |
| 答案正确 | 18 / 20（90%） |
| 端到端正确 | 18 / 20（90%） |
| 模型调用 | 77 |
| Prompt Token | 175,187 |
| Completion Token | 12,698 |
| Total Token | 187,885 |
| Cached Token | 160,896 |
| Cached / Prompt Token | 91.8% |
| 平均耗时 | 11.327 秒 |

18 个成功 Case 都从自然语言输入经生产 Conversation 入口自主完成 schema 搜索、只读 SQL 与 grounded-value 答案。Provider 上报的 Cached Token 占 Prompt Token 约 91.8%；这是该网关在本次固定集上的 Usage 观测值，不等同于应用语义缓存命中率，也不外推到其他 Provider 或生产流量。

两个失败 Case 暴露的是不同问题：

- `sql-equipment` 在两次有效 schema 搜索后连续执行两次 `execute_readonly_query`，两次底层执行均成功，但违反当前“一次最终查询”的评测合同，记为 `invalid_tool_sequence`；
- `sql-customer-distribution` 最终生成 `COUNT(WorkOrderNo)`。`CUST-C` 的 `WorkOrderNo` 为 NULL，因此返回 0；固定集要求统计记录数，正确业务语义应为 `COUNT(*)`，记为 `result_mismatch`。

这次 20 Case 结果推翻了此前“扩大到 20 Case 仍需等待”的待办状态，但没有证明 SQL 治理已经完结。后续本地切片已将 Prompt 升为 `conversation-v9`，明确“统计记录数”使用 `COUNT(*)`，并把每轮第二次 `execute_readonly_query` 转为不触发底层 executor 的结构化 `blocked` 结果；重复请求仍消耗 Tool-call 预算、进入失败执行轨迹，并由评测器记录为无 SQL/hash 的 `tool_run_limit_exhausted` 尝试，因此模型请求两次查询时仍判 `invalid_tool_sequence`，不会被治理层掩盖。只有剩余 Tool-call 预算允许时回合才继续，否则预算错误优先 fail-closed。该实现先通过本地桩测试，再按下一节在 clean revision 上定向复测；无论定向结果如何，都不得把 v8 的 18/20 全量指标改写成 v9 20/20。

## 2026-08-16 conversation-v9 两个失败 Case 正式复测

在 clean revision `2b7a257` 上分别对 v8 的两个失败 Case 做了一次正式单 Case 复测。两次运行都记录 `formal=true`、Prompt `conversation-v9`、Profile `conversation-default`，并沿用 OpenCode Go `opencode-deepseek-main`；每次授权上限为 1 Case、8 次 Provider 调用、16,000 Token。它们是两个独立 Summary，不伪装成一次两 Case 聚合运行。

| Case | Tool 轨迹 | SQL | 结果 | Model / Total / Cached Token | 耗时 |
| --- | --- | --- | --- | ---: | ---: |
| `sql-equipment` | schema search ×4 → readonly query ×1 | 按 `TicketID` 查询 `EquipmentCode` | Tool/执行/答案/端到端全部正确 | 5 / 13,148 / 9,856 | 17.342 秒 |
| `sql-customer-distribution` | schema search ×4 → readonly query ×1 | `SELECT CustomerCode, COUNT(*) ... GROUP BY CustomerCode` | Tool/执行/答案/端到端全部正确 | 4 / 10,101 / 7,296 | 12.688 秒 |

两次复测都只请求并执行了一次 `execute_readonly_query`，因此没有触发第二次查询的 runtime block；`sql-customer-distribution` 明确从 v8 的 `COUNT(WorkOrderNo)` 修正为 `COUNT(*)`。原始资产为：

- `output/evaluation/text-to-sql-conversation-v3-v9-sql-equipment.{observations.jsonl,summary.json}`；
- `output/evaluation/text-to-sql-conversation-v3-v9-sql-customer-distribution.{observations.jsonl,summary.json}`。

复现时使用上文 20 Case 命令的相同 Profile/身份设置，分别增加 `-case-id sql-equipment` 或 `-case-id sql-customer-distribution`，并把上限收紧为 `-max-cases 1 -max-provider-calls 8 -max-provider-tokens 16000`，输出到上述独立文件。首次缺少 SQL 密码与误用 SA 密码的两次启动都在 Provider 创建前失败，产生 0 次云端调用且未生成正式资产。

结论只限于“此前两个失败 Case 在 v9 上各正式通过一次”。它支持关闭本轮定向治理，但样本仍只有 2 个且没有重复运行；完整 20 Case 的当前正式指标仍是 v8 的 18/20，不能改写为 v9 20/20，也不能据此声称生产流量 100% 准确。

## 被丢弃的试跑

本轮另有两次 Provider 试跑不得计入正式指标：

- 旧的“严格恰好一次 search + 一次 query”判定把模型的多次 schema 搜索全部误判为失败，消耗 15 次调用、30,852 Token；该结果只用于修正评测合同；
- 修正序列后首次子集文件漏写 `sql-urgent-count.expectedRows`，消耗 15 次调用、32,147 Token；SQL 实际返回 `2`，但错误夹具期望零行，因此整轮作废。

本次验收实际 Provider 总量为 48 次模型调用、104,112 Token，其中正式可引用结果仅为最后一轮的 18 次调用、41,113 Token。价格不在程序侧估算。
