# Conversation Answer Quality Evaluation v1

## 目的与边界

`conversation-quality-v1` 是统一会话中知识问答、附件问答和公网问答的质量验收合同。它把
“检索到了什么”“回答引用了什么”“引用预览是否仍指向同一份内容”“系统是否正确降级”和
“这次请求花了多少时间/Token/费用”分开统计。

当前已实现 provider-free 的数据合同、确定性聚合器和 CLI，并接通运行时结构化引用事实：

```text
testdata/conversation-quality-v1.jsonl
testdata/conversation-quality-v1.seeded.observations.jsonl
    -> mesguard-conversation-quality-eval
    -> JSON summary
```

固定集中的 `seeded_contract` 观测是聚合器自检数据，不是模型质量结果，不调用 Embedding、
Rerank、OCR、VLM、Web Search 或主聊天模型，也不能据此修改简历指标。Conversation Runner 现已
从本轮通过证据门禁的 `search_knowledge`、`read_attachment` 和 `fetch_public_page` 结果生成
`citationSources`；每项都带后端生成的完整 marker（例如
`[source:knowledge:11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222]`）引用这些来源；
尖括号、引号和反引号都不是 marker 字符。未知引用会使回合失败且不落
助手消息。Worker 会把实际引用的 `sourceType + sourceRef + contentSha256 + position` 与回答原子
持久化并通过消息 API/幂等回放返回。成功或明确降级的 Conversation run 还会在同一完成事务中
记录模型供应商/ID、Prompt 版本、Provider usage、耗时、已验证检索来源和稳定降级通道。自动重试
耗尽后的终态失败也在 lease fencing 事务中保存最后一次安全观测和稳定 `errorType`，但不保存原始
异常文本，也不要求伪造 assistant message 或 Provider usage。离线
`mesguard-conversation-quality-export` 只需 `caseId -> turnId` 选择清单即可从 PostgreSQL 生成
`recorded_run` JSONL；答案、Token、引用和延迟不再手填。另有
`mesguard-conversation-quality-observe` 使用事务级公开知识夹具直接执行低成本真实模型观察。首个连接池
Case 的十四次边界探针保留为失败/控制面证据；不同的 transaction Case 已在 `conversation-v6` 下形成
首条严格通过样本。正常回答不增加调用；只有“来源 Tool 已成功且最终零合法 marker”时，Runner 才用
同一模型执行一次 Tool-free、严格 JSON 的 `conversation-citation-repair-v1`，修复结果仍须通过本轮
sourceRef/哈希白名单。独立 LLM Judge 的严格输入、
执行和汇总链路已实现，并已对该失败答案完成一次显式授权的独立模型/人工对照校准；Provider 配置
随后恢复默认关闭。真实
PostgreSQL + MinIO 的小文件 HTTP 链路已完成零 Provider 调用 smoke，详见下文；它只证明附件边界
和引用链可运行，不代表模型质量。

## 观测合同

每个 Case 记录：

- `relevantSources`：人工标注的相关来源，使用 `sourceType + sourceRef + contentSha256`；
- `requiredCitationRefs`：回答必须引用的来源子集；
- `requiredAnswerTerms`：仅用于 provider-free 冒烟的必要信号词，不等同于事实正确率；
- `forbiddenAnswerTerms`：回答不应出现的明显错误或越权信号；
- `expectedOutcome` 和 `expectedDegradedChannels`：例如扫描附件只有视觉元数据时应返回
  `degraded` 和 `attachment_visual_only`。

每次 Observation 记录检索来源、回答引用、模型/提示词版本、供应商 usage、耗时和预估费用。
引用的 `contentSha256` 必须和标注来源一致；知识 Chunk 和附件引用还可以携带预览哈希，
用于验证展示层拿到的内容没有漂移。公网来源目前以 HTTPS 页面 URL 作为稳定引用，预览检查
不对公网来源自动计分，因为当前还没有同一授权路径的公网预览 API。

真实观察固定集在提交到数据库前使用稳定的 `relevantChunks` 和 `requiredCitationChunks`。前者表示
与问题相关、允许被引用的来源集合，后者表示答案必须覆盖的子集；`requiredCitationChunks` 省略时才
向后兼容地回退为全部 `relevantChunks`。必需来源必须属于相关来源，且两组都由文档 key、Chunk
序号和内容 SHA-256 三元组固定。这样可以表达“多个来源都正确，但只要求覆盖其中关键证据”的 Case，
也不会因为某次模型多引了一个弱相关段落就临时扩大金标。

固定集还可以声明 `retrievalMaxResults`；当前省略时按版本化观察器默认值 `3` 解释。观察器会在调用
真实 `search_knowledge` 前覆盖模型给出的 `maxResults`，确保答案评测与原检索固定集的 `K=3` 使用
同一候选规模。生产 Tool 仍保留默认最多 8 条和最大 20 条的原有行为，这一门禁只消除评测分母漂移。

`judge` 是可选的人工或 LLM Judge 记录，包含 `faithfulness`、`answerRelevance` 和
`citationAlignment` 三个 0 到 1 分数。聚合器只平均 Judge 结果，不把关键词命中率命名为
“忠实度”，也不会在没有 Judge 的情况下生成忠实度结论。

### 独立 claim-level Judge 链路

当前链路把生成答案与评分彻底分开：

```text
recorded_run + Chunk 固定集 + 人工 gold facts
  -> mesguard-conversation-quality-judge-export（零 Provider 调用）
  -> 自包含 rag-judge-v2 JSONL
  -> mesguard-rag-judge（独立模型、严格 JSON、费用门禁）
  -> mesguard-conversation-quality-eval -judge ...（只合并辅助分数）
```

`testdata/conversation-quality-judge-facts-v1.jsonl` 保存 5 个 Case 的人工关键事实；导出器同时固定
生成答案的 provider/model、允许来源、答案实际引用的证据正文及 SHA-256。实际引用可以出现在黄金
来源之外，以便 Judge 识别错误或弱相关引用，但不会被导出器加入 `allowed_sources`。生成模型与 Judge
provider/model 完全相同时，执行会在 Provider 调用前拒绝；证据哈希、raw/resolved Case、K、必需
引用映射或数据集任一漂移也会拒绝。

本轮已对现有 Qwen `conversation-v4` 失败观测完成纯本地导出：2 条人工 gold facts、2 个允许来源、
3 个实际引用证据，其中额外概述来源只存在于 `cited_evidence`。以下命令均未调用模型：

```powershell
go run ./cmd/mesguard-conversation-quality-judge-export -overwrite
go run ./cmd/mesguard-rag-judge `
  -input output/evaluation/conversation-quality-recorded-v1.judge-inputs.jsonl `
  -validate-only
go run ./cmd/mesguard-rag-judge `
  -input output/evaluation/conversation-quality-recorded-v1.judge-inputs.jsonl `
  -estimate-only
```

Provider-free 校验结果为 1 Case、0 Provider 调用；按当前 `qwen3-max`、2048 最大输出和保守价格系数，预估上界为
Prompt `<=2572`、Completion `<=2048`、约 `0.034864 CNY`，低于默认 `0.05 CNY` 门禁。它只是调用前
规划，不是实际 Token 或账单；Provider 在途结算仍可能越过门禁。

在用户明确授权后临时启用 Judge 并只运行该 Case，实际 Prompt/Completion/Total Token 为
`2512/713/3225`，耗时 `16595 ms`，配置价格估算 `0.018604 CNY`，随后立即恢复
`[models.judge] enabled=false`。`qwen3-max` 给出 `partial`：Answer Correctness `3/4`、Faithfulness
`2/4`、Answer Relevance `2/4`、Citation Correctness `1/4`、Refusal Correctness `4/4`。它准确识别
“服务不可用/性能下降”和“资源耗尽”为缺少证据的扩展断言，并指出第三条调优引用不在
`allowed_sources`；没有误报黄金事实缺失。人工逐项核对与该结论一致，因此首个 Judge 校准样本通过，
但被评答案本身仍不通过。

使用 `mesguard-conversation-quality-eval -judge <jsonl>` 合并后，`judgedRuns=1`，Faithfulness、Answer
Relevance、Citation Alignment 分别为 `0.50/0.50/0.25`，而确定性 `passedRuns` 仍为 `0`。这验证了
Judge 只影响 `judgedRuns` 和三个辅助均值，不覆盖确定性 pass/fail、引用 ID 或内容哈希事实。

## 指标口径

确定性指标按精确的来源引用和内容哈希计算：

| 指标 | 计算口径 |
| --- | --- |
| Context Precision | 正确的 `retrieved source` 数 / 返回的 retrieved source 数 |
| Context Recall | 正确的 retrieved source 数 / Case 标注的 relevant source 数 |
| Citation Precision | 正确的引用数 / 回答中的引用数 |
| Citation Recall | 命中的 required citation 数 / required citation 数 |
| Preview Consistency | 预览哈希匹配数 / 所有需要预览的有效引用数 |
| Answer Term Recall | 命中的必要信号词数 / 必要信号词数；仅作合同冒烟 |
| Outcome Accuracy | 实际 outcome 与 Case 预期 outcome 一致的请求数 / 请求数 |

延迟使用最近秩（nearest-rank）P50/P95，Token 只接受供应商 `usage`，费用使用调用前估算或
结算后的 provider cost 字段，不用字符数替代。`estimatedCostPerThousandCny` 是平均每请求
费用乘以 1,000，不能和一次批量任务总费用混写。

评测按 `observationKind` 隔离：`seeded_contract` 与 `recorded_run` 不能混跑，避免用固定
夹具结果污染真实模型结果。CLI 还拒绝重复 Case、重复 Run、未知字段、来源哈希不合法和
版本混用。

## 当前零 Provider 调用结果

命令：

```powershell
go run ./cmd/mesguard-conversation-quality-eval `
  -dataset testdata/conversation-quality-v1.jsonl `
  -input testdata/conversation-quality-v1.seeded.observations.jsonl
```

当前三条 seeded contract 观测覆盖 knowledge、attachment 和 web 三类来源，并包含一个附件
视觉降级 Case。结果为：`3/3` 通过，Context Precision/Recall、Citation Precision/Recall、
Answer Term Recall、Expected Degraded Channel Recall 和可预览引用一致性均为 `1.0`；P50/P95
为 `200/300 ms`，Token 为 `350`，估算费用字段为 `0.0035 CNY`。这三项都是用于验证
聚合公式的固定夹具值；本次没有真实 Provider 请求，实际 Provider Token 和费用均为零。
这些数字只证明合同和聚合器计算正确，不证明模型的回答质量、真实延迟或生产成本。

## 低成本真实 Provider 先导观察

新增的 `conversation-quality-recorded-v1` 固定集复用已固定 SHA-256 的公开 Go/PostgreSQL 语料，
当前包含 5 个知识问答 Case、4 份文档、21 个 Chunk。先做零 Provider 校验和费用规划：

```powershell
go run ./cmd/mesguard-conversation-quality-observe -validate-only -max-cases 5
go run ./cmd/mesguard-conversation-quality-observe -estimate-only -max-cases 1
```

单 Case 规划输出为 1 份文档、6 个 Chunk、至多 1 个文档 Embedding 请求、估算 Embedding Token
不超过 557、计划聊天 Token 10,096、计划费用约 0.0407 CNY，低于默认 0.05 CNY 门禁。这里的
“计划”不是 Provider 侧硬配额：Runner 只能在一次调用返回 usage 后结算，已经发出的单次调用可能
使实际累计 Token 或费用超过计划值。因此命令默认只选 1 个 Case，必须显式加
`-execute-provider`，并在每个 Case 后再次结算；该门禁不能被表述为绝对不超支。

在 `pool-limit-wait-risk` 上做了十四次小额探针；所有实际发起知识检索的探针均命中 2/2 黄金 Chunk，Context Recall 为 1.0。
前十一次没有形成可接受的最终回答；后三次已形成有效答案，但均没有通过严格的引用精度门禁：

| 探针 | Chat Token | 估算费用（含夹具 Embedding） | 终止原因 | 发现 |
| --- | ---: | ---: | --- | --- |
| 4,000 Token 预算 | 4,090 | 0.0049 CNY | `token_budget_exhausted` | 正确证据已召回，但预算过低 |
| 6,000 Token / 2 Tool | 4,545 | 0.0067 CNY | `agent_response_invalid` | 来源 marker 合同含义不够明确 |
| Prompt `conversation-v2` | 10,772 | 0.0145 CNY | `token_budget_exhausted` | usage 后结算导致在途调用越过计划 |
| 1 Tool 上限 | 4,434 | 0.0057 CNY | `tool_call_budget_exhausted` | 模型尝试二次检索，硬错误阻止最终作答 |
| 首次真实搜索 + 完整缓存 | 10,188 | 0.0116 CNY | `token_budget_exhausted` | 缓存避免重复 Embedding，但完整 6 Chunk 再次占用上下文 |
| 压缩缓存 + 8,000 Token | 8,349 | 0.0094 CNY | `token_budget_exhausted` | Token 降低 18.1%，但模型继续第三次搜索 |
| 首轮 Schema 修复试跑 | 0 | 约 0.0002 CNY | `agent_execution_failed` | 评测 Wrapper 误覆盖 call-time Tool Schema；未产生聊天 usage，已由回归测试修复 |
| 保留首轮 Schema、结果后移除搜索 Tool | 2,002 | 约 0.0028 CNY | `agent_execution_failed` | 两次模型调用、黄金召回完整，但 Eino 未收到可落地终答 |
| 结果后移除 Tool + `tool_choice=none` | 2,000 | 约 0.0028 CNY | `agent_execution_failed` | Provider 请求边界进一步收紧，仍未形成可落地终答；停止重复付费探针 |
| `qwen-qa-eval` 兼容性对照 | 3,764（回调重复计数） | 约 0.0091 CNY（保守上界） | 未通过质量门禁 | 1 次实际模型调用直接回答，`finishReason=stop`，未调用知识 Tool，Context/Citation Recall 均为 0 |
| Qwen 强制首检 + 结果后移除 Schema | 1,072 | 约 0.0014 CNY | `agent_execution_failed` | 首轮正确检索；第二轮历史仍有 Tool Call/Result，却移除了对应 Schema，暴露协议边界错误 |
| Qwen 强制首检 + 保留 Schema/禁用新 Tool Call | 5,499 | 约 0.0083 CNY | 严格质量门禁未通过 | 两次真实调用、有效答案、2/2 必需引用和预览均正确；Context Precision `0.3333`、Citation Precision `0.6667` |
| 固定 `K=3` + `conversation-v3` | 4,475 | 约 0.0066 CNY | 严格质量门禁未通过 | 3 个 hit 加 1 个 Parent 上下文；Context Precision 提升到 `0.5`，但仍用概述段落支撑过于具体的参数建议 |
| 固定 `K=3` + `conversation-v4` | 4,482 | 约 0.0063 CNY | 严格质量门禁未通过 | 额外引用与紧邻断言已同等具体，但答案仍扩写未被当前证据直接支撑的风险；Citation Precision 仍为 `0.6667` |

前六次无模型 Wrapper 的历史配置估算合计约 0.0528 CNY。后三次使用模型 Wrapper 的输出被发现会让
外层 Agent 节点和内部 OpenAI-compatible Client 对同一次请求各记一次 usage，因此不能把十四次 CLI
估算直接相加作为总成本。该问题只会保守放大观察器的 Token/费用与预算消耗，不会减少实际 Provider
请求；Wrapper 现已隔离内部回调，后续以“一次实际请求只结算一次 usage”为门禁。所有费用仍是配置
价格系数估算，不是云平台账单。

Prompt 的 marker 语法在 `conversation-v2` 明确；`conversation-v3` 进一步要求引用紧邻被支撑的断言，
且来源与断言保持同等具体程度。`conversation-v4` 再要求使用来源型 Tool 后只回答用户实际询问且被
证据直接支持的内容，不主动补充缺证据的风险、参数、数值、最佳实践或建议。质量
观察器还加入了评测专用的单次真实检索边界：首次 `search_knowledge` 正常执行，后续尝试复用首次
结果并附带“立即基于现有证据回答”的提示。完整缓存的真实探针确认不会重复请求 Embedding；把缓存
压缩为一条仍可通过引用门禁的完整结果后，累计 Token 从 10,188 降至 8,349，但 StepFun 仍忽略停止
提示并发起第三次搜索。观察器因此在模型适配层增加更确定的边界：输入中已经存在
`search_knowledge` Tool message 时，下一轮调用保留该 Tool Schema 以满足既有 Tool Call/Result 的消息
协议，但设置 Eino `ToolChoiceForbidden` 禁止产生新的 Tool Call；缓存 Wrapper 作为防御性兜底保留。
一次“移除 Schema + 禁用 Tool”的真实 Qwen 探针正是因为破坏历史消息协议而失败，修复后脚本模型
已验证只需“一次搜索决策 + 一次最终答案”两次模型调用。它不改变生产 Conversation Runner 的多轮
检索能力。真实 StepFun 复跑曾稳定收敛到两次模型调用和约 2,000 Chat Token，且仍召回全部黄金 Chunk，
但没有产生 Eino 可落地的最终答案；当前不把该现象写成供应商缺陷，也不继续用同一配置烧钱重试。

观察器现支持 `-chat-profile`，并增加不记录正文、Prompt、Tool 参数、调用 ID 或 Provider 原始错误的
安全消息形态诊断。首次 Qwen 对照由此确认：模型只产生一次 `assistant + content + stop`，没有调用
`search_knowledge`。这说明该 Case 失败在检索前，而不是引用解析。为了把“工具选择准确率”和“拿到
证据后的回答质量”分开，质量观察器现在对知识专用固定集在首轮发送 `ToolChoiceForced`，且其 Catalog
只暴露 `search_knowledge`；收到结果后再发送 `ToolChoiceForbidden`。生产 Conversation Agent 不受此
约束，Tool 选择继续由独立 `tool-selection-v1` 固定集验收。评测 Wrapper 同时隔离内部 Client 回调，
避免一笔 Provider usage 被外层节点重复累计。最新 `conversation-v4` Qwen 观察为两次模型调用，
Prompt/Completion/Total Token `3915/567/4482`，耗时 `5247 ms`，在线估算费用 `0.0062115 CNY`，
夹具 Embedding 估算费用约 `0.000128 CNY`。Outcome Accuracy、Context Recall、Citation Recall、Answer
Term Recall 和 Preview Consistency 均为 `1.0`，Context Precision 为 `0.5`，Citation Precision 仍为
`0.6667`，`passed=0`。与 5,499 Token 的上一可落地 run 相比，Token 下降约 `18.49%`、耗时下降约
`44.00%`、在线估算费用下降约 `24.17%`；由于同时改变 K 和 Prompt，这只是观测差异，不能归因为单一决策。
额外概述来源现在能直接支撑其紧邻的泛化调优句，但该句对原问题只是旁支；更重要的是答案仍包含
“服务不可用/资源耗尽”等未被两条必需证据直接支持的扩展风险。确定性引用 ID/哈希指标不能发现
“未带引用的自然语言断言”，因此不把该 run 改判通过，也不继续用 Prompt 试错烧钱。

### 零引用瓶颈与受控引用修复

换用固定集中的 `transaction-commit-failure` 后，`qwen3.6-flash` 在 `conversation-v4` 和
`conversation-v5` 下都正确召回 2/2 黄金 Chunk，也给出语义正确的简短答案，但连续两次没有输出任何
marker，后端均如实记为 `insufficient_evidence`。把完整 marker 放进 `citationSources` 并加强 Prompt
提高了可复制性，却没有形成工程保证。另一次 `qwen3-max`/deadlock 对照能输出 marker，但引用了两个
旁支 Chunk，并补充语料中没有的 `SQLSTATE 40P01`；Citation Precision 只有 `0.5`。将强模型输出硬限
到 384 Token 又直接得到 `finishReason=length`。这些失败说明：只靠 Prompt、强模型或过低输出上限都
不能稳定同时满足引用完整性、精度、成本和可落地响应。

当前生产实现改为失败触发的受控修复：Tool Middleware 在内存中保留最多 64 KiB 的本轮已验证来源
结果；主答案零合法引用时，使用同一模型、温度 0、禁止 Tool、独立 30 秒超时和 768 输出 Token 执行
一次严格 JSON 修复。请求只包含当前问题、草稿、原证据 JSON 和允许 marker；修复必须删除无证据细节、
使用最小充分来源，并再次通过 `ResolveAnswerCitations`。非法 JSON、未知 marker、超时、截断、输入超限
或零引用都会保留原 `insufficient_evidence`，不会伪造来源。修复 usage 纳入同一回合 Token 预算。

最终 `transaction-commit-failure` 观察结果：

| 指标 | 结果 |
| --- | ---: |
| Outcome / Passed | `answered` / `1` |
| Context Precision / Recall | `0.5 / 1.0` |
| Citation Precision / Recall | `1.0 / 1.0` |
| Preview Consistency / Answer Term Recall | `1.0 / 1.0` |
| 模型调用 | `3`（检索决策、原回答、失败触发修复） |
| Prompt / Completion / Total Token | `6661 / 364 / 7025` |
| 延迟 | `3869 ms` |
| 在线估算费用 | `0.008141 CNY` |
| 夹具 Embedding 费用 | `0.000123 CNY` |

人工复核确认最终答案只保留“Commit 失败后丢弃 Tx 查询/执行结果”和“Rollback 后事务无效且未提交”
两条直接证据，精确引用两条黄金 Chunk，没有保留此前的网络错误等扩写。本次未再调用 LLM Judge。
`Context Precision=0.5` 仍揭示检索层返回了 4 条来源而黄金相关项只有 2 条；它是下一轮候选压缩优化，
不能被答案层的通过结果掩盖。本轮五次新观察的聊天费用估算合计约 `0.063894 CNY`，夹具 Embedding
合计约 `0.000595 CNY`；只有最后一次属于通过样本，前四次保留为失败消融证据。

## 真实 PostgreSQL + MinIO 小文件 HTTP smoke

集成测试 `TestAttachmentHTTPMinIOSmoke` 使用本地真实 PostgreSQL 事务、真实 MinIO 和 Gin HTTP
路由，上传一份 49 字节 UTF-8 TXT。它覆盖：创建会话、multipart 上传、相同幂等键重放、上传后但
尚未随消息发送时拒绝 `read_attachment`、消息关联后允许 Tool 读取、附件引用预览、跨用户预览
返回 404，以及所有 API/Tool 输出不泄漏 bucket、object key、ETag、密钥或永久 URL。测试结束会
回滚 PostgreSQL 事务并删除专用测试 bucket/object。

该 smoke 复用生产 `knowledgeparser.Router` 对媒体类型做规范化，不触发 OCR、VLM、Embedding、
Rerank、Web Search 或聊天模型，因此 Provider 调用和费用均为零。它不是 Context Precision、
Faithfulness 或回答质量样本，只是会话附件 `HTTP -> MinIO -> PostgreSQL -> 消息授权 -> Tool ->
引用预览` 的真实基础设施验收。

## 下一步

1. 首条不同 Case 的严格通过样本已经完成。停止重复调用 `pool-limit-wait-risk`、
   `transaction-commit-failure` 和当前 deadlock 对照；不得扩大 gold、继续 Prompt-only 试错或让 Judge
   覆盖确定性失败事实。后续先把通过样本作为回归基线，再决定是否扩展答案质量集。
2. 对真实产品回合仍使用 `caseId -> turnId` 选择清单和离线导出器：

   ```powershell
   go run ./cmd/mesguard-conversation-quality-export `
     -dataset testdata/conversation-quality-v1.jsonl `
     -selections <recorded-run-selections.jsonl> `
     -output output/evaluation/conversation-quality-v1.recorded.observations.jsonl
   ```

   选择清单允许填写按供应商账单/价格表计算的 `estimatedCostCny`，不能从字符数估算 Token；Token
   必须来自持久化 Provider usage。导出前可加 `-validate-only`，不会连接数据库或调用 Provider。
3. 可在简历第三点后端收口之后扩展至 4-8 个小型公开/脱敏 Case，覆盖正常回答、证据不足、附件补传、
   Web Search 兜底、provider 失败和 stale preview；先跑 recorded_run，再选择少量 Case 做人工或 LLM Judge。
4. 将当前 TXT MinIO smoke 保留为零成本回归门禁；只有在明确预算后才增加少量图片/OCR/VLM
   smoke，不上传大页数文档，也不重复执行 40 文档全链路 Provider pair。
5. 前端根据消息 `citations` 渲染引用 Chip，知识与附件正文继续走现有受权预览 API；网页引用只
   打开经过后端验证的 HTTPS URL。回答正文中的 marker 是机器绑定语法，不作为最终展示样式。
6. 失败 Case 可直接映射到没有 assistant message 的终态 failed Turn；导出使用持久化的稳定
   `errorType`。只有 Provider 实际返回 usage 才记录 Token，不能把零 usage 解释为“免费调用”。同一
   Turn 显式重跑时旧失败观测会随重新入队事务清除，因此选择清单应只引用当前终态 Turn。
