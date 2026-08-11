# Dynamic Context Governance and Layered Conversation Memory

本文定义 MESGuard 简历第四点“动态上下文治理与分层记忆”的目标设计、实现边界、
评测口径和有序交付切片。本文是目标设计，不是当前完成声明；只有 Roadmap 标记完成且
固定集评测通过的能力，才能转写为简历实测结果。

## 1. 简历目标与事实边界

简历第四点当前目标为：

> 基于模型窗口、Tool Schema 和输出预留量动态计算 Token 预算，逼近阈值时将早期
> 对话压缩为包含事实、决策、证据以及待办事项的结构化摘要，并采用“摘要前置 +
> 尾部滑动窗口”重组 Prompt；结合 PostgreSQL 持久化 + Redis 热记忆缓存，使长会话
> 平均 Token 消耗降低 60%+。

其中 `60%+` 是待固定集验证的目标，不是当前事实。正式口径必须统计主聊天模型和摘要
模型的端到端 Token，并同时报告回答质量、Prompt Cache、成本和延迟，不能通过隐藏摘要
开销或降低回答质量凑指标。

简历第一点与本设计不存在冲突：TaskScope 在一次 Agent Run / Prompt Epoch 开始前冻结
已授权业务 Tool；Skill 采用渐进式装载，只提供 SOP，不在运行中授予权限。第一点已有
`97.78%` 工具选择准确率和 `46.08%` Tool Schema Prompt Token 降幅，评测变量是
TaskScope 最小 Tool 集合，不是 Skill 选择后的动态 Tool 注册。

## 2. 当前实现基础与缺口

### 2.1 已实现基础

- PostgreSQL 持久化 Conversation、Message、工单/任务/附件引用；
- `/turns` 异步受理、Outbox、RabbitMQ、Conversation Worker、Lease/Fencing；
- Turn 状态、重试、幂等回放、JSON/SSE 事件；
- Conversation 与 DiagnosisTask 生命周期独立，通过引用建立来源与导航关系；
- Diagnosis 的 TaskEvent、DiagnosisStep、ToolExecution 摘要、EvidenceItem、Report 和 Review；
- TaskScope、ToolCatalog、Skill Middleware、Provider Usage、Token/延迟观测；
- Redis 为可降级依赖，PostgreSQL 为事实源。

### 2.2 当前实现与剩余缺口

- 首次压缩会读取完整未压缩历史；存在 Active Snapshot 后读取 Snapshot 之后的全部增量，
  并保留最多 100 条已覆盖消息作为连续 Tail 重叠。执行输入采用 10,000 条保护上限，超限
  失败关闭而不是静默截断；
- Conversation 已可在 Feature Flag 下使用最多约 15% 模型窗口的 Continuous Token Tail，
  `conversationMaxContextRunes=32000` 仅保留为回滚路径；
- 当前消息始终保留，历史从近到远连续选择，遇到首条超预算消息即停止，不再跳选更老消息；
- Chat Profile 的 Context Window、Output Reserve、Safety Margin 和 Tool Growth Reserve 已进入调用前预算；
- 已有本地优先 TokenEstimator、Prompt Manifest，以及初始与后续 ReAct Provider 调用硬窗口拦截；
- `ConversationMemoryService` 使用独立 `conversation-memory` Profile 生成固定结构候选 Snapshot，
  经确定性校验后不可变写入 PostgreSQL，并支持同步 CAS 激活；
- Snapshot 已记录版本链、覆盖序号、Schema/Prompt/模型身份、Payload SHA-256 和 Provider Usage，
  Active Snapshot 已通过实际内容 Fingerprint 进入 Prompt Epoch；
- `maxTotalTokens` 只根据调用结束后的 Usage 结算，不能阻止单次请求超过窗口；
- 已在独立 Feature Flag 下启用硬阈值同步压缩和 `Active Summary + Continuous Tail`；
  尚无软阈值异步摘要任务、Memory Worker 和 Redis 热记忆；
- 没有固定长会话 Baseline/Experiment 评测集。

## 3. 目标与非目标

### 3.1 首版目标

1. Conversation 和 Diagnosis 共用 Provider 无关的 `TokenBudgetPlanner`；
2. Conversation 使用不可变、版本化的结构化 Summary Snapshot；
3. Prompt 使用 `Summary + Continuous Tail`，二者合计不超过约 20% 窗口；
4. PostgreSQL 保存原始消息和 Snapshot，Redis 只做可降级热缓存；
5. 摘要条目带来源消息序号，模型可按需恢复当前会话原文；
6. Tool Schema、Skill 和 Prompt Cache 采用 Prompt Epoch 治理；
7. 所有阈值、模型、Tokenizer、重试和 Provider 原生能力均配置化；
8. 通过固定集验证质量、Token、缓存、延迟、成本和估算误差。

### 3.2 首版非目标

- 不做跨 Conversation 的用户长期记忆；
- 不把原始 Prompt、模型推理或完整 Tool Payload 持久化；
- 不做完整会话历史语义检索，首版只按 Summary 来源序号恢复原文；
- 不默认依赖 Provider 私有的服务端会话或原生 Compaction；
- 不在首版实现 Diagnosis 主动上下文压缩 Tool，先观测真实上下文峰值；
- 不把 Redis 作为事实源、分布式锁或 Snapshot 发布仲裁者；
- 不在本阶段修改前端工作台，后端和评测闭环后统一接入。

## 4. 核心模块

```text
TokenBudgetPlanner
  ├─ ConversationContextPreflight
  ├─ DiagnosisContextPreflight
  └─ 其他 ChatModel 调用

ConversationMemoryAssembler
  ├─ Active Summary Snapshot
  ├─ Continuous Tail
  ├─ Dynamic References
  └─ Current User Message

DiagnosisContextAssembler
  ├─ CaseSnapshot
  ├─ Evidence Index
  ├─ Bounded Tool Results
  └─ Report Output Reserve

ConversationMemoryService
  ├─ MemoryCompactor
  ├─ Snapshot Validator
  ├─ Snapshot Repository
  ├─ Redis Hot Cache
  └─ Source Recovery Reader
```

模块边界要求：

- Planner 只计算预算和状态，不读取数据库、不调用模型；
- Assembler 只按 Planner 结果组装上下文，不决定业务授权；
- Compactor 只生成候选 Snapshot，Repository 负责不可变写入和 CAS 激活；
- Tool 授权仍由 TaskScope/ToolCatalog 负责，Memory 不得扩大权限；
- Provider Adapter 负责模型请求序列化、Tokenizer、Usage 和原生优化能力。

## 5. Token 预算模型

每个 Chat Model Profile 增加：

```toml
contextWindowTokens = 131072
maxOutputTokens = 4096
promptSafetyMarginTokens = 2048
promptSafetyMarginRatio = 0.05
```

计算公式：

```text
safetyMarginTokens =
  max(promptSafetyMarginTokens, contextWindowTokens * promptSafetyMarginRatio)

availableInputTokens =
    contextWindowTokens
  - maxOutputTokens
  - safetyMarginTokens
```

调用前估算至少包含：

```text
System Prompt
+ Tool Schemas
+ Preloaded Skill
+ Summary Snapshot
+ Continuous Tail
+ Dynamic References
+ Current User Message
+ Current Run Tool Growth Reserve
```

缓存命中不减少上下文占用。窗口预检使用完整 Prompt Token；成本观测单独记录 Cache Hit
和 Cache Miss Token。

### 5.1 TokenEstimator

```go
type TokenEstimator interface {
    Estimate(ctx context.Context, input PromptInput) (TokenEstimate, error)
}

type TokenEstimate struct {
    EstimatedTokens int
    UpperBoundTokens int
    Method           string
    Profile          string
}
```

优先级：

1. Provider/模型对应的本地 Tokenizer；
2. 模型族 Tokenizer + Provider Chat Template/Tool Schema 补偿；
3. 保守字符估算；
4. 可选远端 Tokenize 只用于离线校准，不作为在线必要依赖。

每次模型返回后记录 `estimated/actual`，按 Provider + Model Profile 统计误差分布，使用
P95/P99 低估误差校准 Upper Bound 和 Safety Margin。

## 6. Conversation 压缩策略

### 6.1 触发阈值

```toml
softThresholdRatio = 0.70
hardThresholdRatio = 0.85
memoryMaxRatio = 0.20
summaryMaxRatio = 0.05
tailMaxRatio = 0.15
```

- 低于软阈值：使用当前 Snapshot + Tail；
- 达到软阈值：当前回合完成后异步生成 Snapshot；
- 达到硬阈值：下一次主模型调用前同步生成 Snapshot；
- 超过硬窗口：禁止发送请求；
- Summary + Tail 实际可以低于上限，但不能超过约 20%。

记忆预算：

```text
memoryBudget = min(
    contextWindowTokens * 20%,
    扣除 System / Tool / Skill / Output / Safety / Tool Growth 后的剩余空间
)
```

Summary 上限约 5%，Tail 上限约 15%。不固定保留消息轮数；Tail 从当前消息开始向前连续
选择，不能跳过中间的大消息去选择更老的小消息。

### 6.2 尾部内容规则

- 当前 User Message 必须保留；
- 最近历史按 Token 连续选择；
- 历史 Tool Result 不长期复制，使用 Evidence/Attachment/Report 引用；
- 单条附件或 Tool Result 过大时只保留有界内容和原文句柄；Conversation 当前 Run 内的
  Tool Result 通过 `sha256` 句柄和受 TaskScope 约束的分页 Tool 恢复，Run 结束即释放；
- Tail 中不能插入后来生成的动态块，新增上下文一律尾部追加；
- Summary 更新意味着新的 Prompt Epoch，不把新摘要追加在旧摘要后。

## 7. Summary Snapshot

Snapshot 是不可变派生事实，不覆盖原始 Message。Payload 使用固定结构：

当前实现状态：`00027` 已建立 `conversation_memory_snapshots`，Service 支持首次全历史压缩、
“上一 Active Snapshot + 新消息”增量合并和 CAS 激活。模型输出会校验固定字段、重复/未知字段、
用户事实来源、覆盖序号、稳定 Evidence/Task/Report 引用、supersede 环与同一谱系 Active 唯一性；
生成、Usage 或校验失败只做有界重试，不生成确定性低质量替代摘要。硬阈值路径使用新 Active
Summary 替换旧 Summary，再与连续 Tail 组装；旧 Active 可在刷新失败时受控回退。

```text
conversationGoal
facts[]
decisions[]
corrections[]
evidenceReferences[]
openQuestions[]
todos[]
taskReferences[]
reportReferences[]
```

每个 Entry 至少包含：

```text
entryId
content
sourceMessageSeqs
status
supersedesEntryId（可选）
```

约束：

- 模型推测不能写入用户事实；
- Correction 必须显式 supersede 旧事实；
- Todo 使用 `open/completed/cancelled`；
- Evidence/Task/Report 只保留稳定 ID、类型、来源和一句话结论；
- 新 Snapshot 基于旧 Snapshot + 新增可压缩消息增量合并；
- 记录 `from_seq`、`through_seq` 和 `supersedes_snapshot_id`；
- 默认 `maxSummaryOutputTokens=4096`，硬上限不超过模型窗口约 5%，配置化；
- Snapshot 生成后先通过严格 JSON Schema、来源、范围、引用和 active Entry 校验；
- 校验失败不激活，继续使用上一份有效 Snapshot。

运行时不增加 LLM Judge。离线评测使用独立、配置化的 Judge 检查 Faithfulness 和语义保留。

### 7.1 摘要失败

- 软阈值异步失败：默认重试三次，指数退避 + jitter，继续使用旧 Snapshot；
- 硬阈值同步失败：在请求截止时间内重试；
- 重试耗尽：返回可重试 `context_preparation_failed`，不调用主模型；
- 不使用低质量确定性摘要，不静默丢弃历史，不发送超过窗口的 Prompt。

## 8. 原文恢复与损失控制

首版增加稳定高层 Tool：

```text
read_conversation_memory_sources
```

它只允许根据当前 Active Snapshot 中声明的 Entry 或消息序号，恢复当前 Conversation 原文：

```text
每次最多 20 条消息
每次最多 8K Tokens
每轮最多调用 2 次
超限返回 continuation cursor
只能读取当前用户有权访问的 Conversation
读取写入 Tool Trace
```

该能力解决“摘要保留结论但丢失细节”，不能解决“摘要完全遗漏某个话题”。后者通过固定集
质量门控制；完整历史关键词/语义检索、分层摘要、重要性自适应压缩、双模型验证和摘要漂移
检测保留为后续增强点。

## 9. Prompt Epoch、Tool 和 Skill

### 9.1 语义分段

```text
Stable System Prompt
Canonical Tool Schema
Preloaded Entry Skill
Active Summary Snapshot
Continuous Tail
Dynamic References
Current User Message
Current Run Tool Call / Result
```

这是 Provider 无关的语义顺序，不强制所有 API 使用相同 Wire 字段顺序。Provider Adapter 负责
具体序列化、Tokenizer、缓存断点和 Usage 映射。

### 9.2 Fingerprint

不手工猜测 Role/Capability/Dependency 是否改变 Prompt，直接哈希模型实际看到的稳定内容：

```text
system_prompt_fingerprint
tool_schema_fingerprint
skill_prompt_fingerprint
model_profile_fingerprint
summary_snapshot_fingerprint
```

Fingerprint 只用于观测、Epoch 判定和缓存分析，不参与业务授权，也不发送给模型。

### 9.3 Skill 与 Tool 暴露

当前默认策略：

```text
TaskScope 在 Prompt Epoch 开始时冻结已授权 Tool
Skill 只指导 Tool 使用，不动态授予权限
```

Skill frontmatter 未来可声明 `recommended_tools`，用于启动校验、SOP、评测和 Schema 成本分析，
但不能扩大 TaskScope。

Provider Tool 暴露策略：

```text
static_frozen   默认通用路径
native_deferred Provider 原生支持延迟 Tool 激活时可选
epoch_rebind    通用 Provider 需要新增/撤销 Tool 时重建 Agent Epoch
gateway         Tool 规模达到几十/上百后再评估
```

`epoch_rebind` 保留旧 Tool 顺序，新 Tool 只追加到末尾；安全撤权优先，新建不含被撤销 Tool 的
Epoch。Provider 原生 deferred 和 Compaction 都是适配器级可选优化，不能成为正确性依赖。

动态加载的 Skill 全文不跨回合持久化；只记录 `skill_id + version/hash + executed_at`，需要时重新
读取当前版本。

## 10. PostgreSQL、Redis 与并发

PostgreSQL 是事实源，目标表：

```text
conversation_memory_snapshots
conversation_memory_jobs
conversation_prompt_manifests
```

Snapshot 关键元数据：

```text
conversation_id
supersedes_snapshot_id
from_seq / through_seq
schema_version
summary_model_profile
prompt_version
payload JSONB
payload_sha256
estimated_tokens
actual_prompt_tokens
actual_completion_tokens
status
created_at / activated_at
```

软阈值通过事务写 Memory Job + Outbox，由独立 Worker Claim；硬阈值同步调用同一个
MemoryCompactor。发布 Active Snapshot 使用 CAS：

```text
base_snapshot_id 仍是当前 Active
through_seq 大于当前 Active
Worker Lease/Fencing 仍有效
```

过期 Worker 的 Snapshot 可保留审计，但不能激活。

Redis 只缓存 Active Snapshot、Prompt Manifest 和最近 Tail 投影：

```toml
memoryCacheTTL = "2h"
memoryCacheJitterRatio = 0.10
```

Redis Key 包含 Conversation + Snapshot ID；失败时回 PostgreSQL。Redis 不持有发布权，不作为
分布式锁。删除 Conversation 时 PostgreSQL 级联删除 Memory 派生事实，并尽力清除 Redis；缓存
清除失败依靠 TTL 最终过期。

## 11. Diagnosis Token Budget

Diagnosis 共用 TokenBudgetPlanner，但首版不生成 Conversation Summary：

- 初始 Prompt 预检；
- CaseSnapshot、Evidence、Tool Result 和 Report Output Reserve 分预算；
- Tool Result 保存完整证据，只向模型返回有界内容和 Evidence ID；
- 每次模型调用前重新预检；
- 首版只记录最大上下文水位并执行宿主侧硬保护；
- 如果真实固定集频繁超过 70%，再实现模型请求、宿主执行的 Diagnosis Compaction Epoch；
- 不能只依赖模型主动调用压缩 Tool，因为请求发送前超窗时模型没有机会执行 Tool。

## 12. Provider 原生 Compaction

MESGuard-managed Compaction 仍可调用远端快速小模型，它与“本地模型摘要”不是同义词。默认路径
由 MESGuard 控制触发、结构、来源、持久化和回滚。

Provider 原生 Compaction 按 Chat Model Profile 可选开启：

```text
默认：MESGuard-managed structured compaction
可选：Provider-native compaction
不支持：回退 MESGuard-managed
Provider 切换：不依赖旧 Provider 私有会话状态
```

首版只预留 Provider Adapter 接口和配置，不要求实现每家 Provider 的原生能力。原生压缩若不能
产出 MESGuard 的结构化来源和审计元数据，只能作为运行时优化，不能替代 PostgreSQL Snapshot。

## 13. 配置契约

```toml
[models.chat]
activeProfile = "stepfun-main"
conversationMemoryProfile = "qwen-conversation-memory"

[models.chat.profiles.stepfun-main]
provider = "stepfun"
model = "..."
contextWindowTokens = 131072
maxOutputTokens = 4096
promptSafetyMarginTokens = 2048
promptSafetyMarginRatio = 0.05
tokenizerStrategy = "local_calibrated"
toolExposureStrategy = "static_frozen"
providerNativeCompactionEnabled = false

[models.chat.profiles.qwen-conversation-memory]
provider = "dashscope"
model = "..."
contextWindowTokens = 131072
maxOutputTokens = 4096
promptSafetyMarginTokens = 2048
promptSafetyMarginRatio = 0.05
tokenizerStrategy = "local_calibrated"
toolExposureStrategy = "static_frozen"
providerNativeCompactionEnabled = false

[models.judge]
provider = "..."
model = "..."
enabled = false

[agent.contextMemory]
shadowPreflightEnabled = true
continuousTailEnabled = true
summaryTailEnabled = true
softThresholdRatio = 0.70
hardThresholdRatio = 0.85
memoryMaxRatio = 0.20
summaryMaxRatio = 0.05
tailMaxRatio = 0.15
preflightTimeoutMillis = 250
toolGrowthReserveTokens = 8192

[agent.contextMemory.summary]
enabled = true
promptFile = "config/prompts/conversation-memory-summary.md"
promptVersion = "conversation-memory-v1"
maxPayloadBytes = 65536
maxAttempts = 3
retryBaseDelayMillis = 250
```

Chat Profile 的窗口、输出、Safety Margin、Tokenizer 和 Tool/Compaction 能力已实现为
现有命名 Profile 的扩展契约，`conversationMemoryProfile` 只引用命名 Profile，不重复
建立 Provider 凭证。`agent.contextMemory` 已启用 Shadow Preflight、Continuous Tail 与
Summary + Tail。关闭 `summaryTailEnabled` 可回退 Continuous Tail；再关闭
`continuousTailEnabled` 可回退 Rune 路径，原始消息和已生成 Snapshot 都不需要回滚。

只观测/Continuous Tail 路径的 Preflight 使用独立、可配置的短超时，主模型超时在观测完成后
单独起算；观测失败只记录有界错误码与降级 Manifest。启用 Summary + Tail 后，同步压缩与
当前 Turn 共享总截止时间，压缩或组装失败采用 fail-closed，不调用主模型。
Manifest 通过 `preflight_status`、`prompt_identity_available` 和 `estimate_available` 区分
成功、部分已知和失败观测；未知 Tool Contract 不生成伪 Epoch，估算失败不生成 0 Token 样本。

当前实现将模型可见 Prompt 估算与 Tool Growth Reserve 分开：前者用于和首个 Provider
调用的实际 Prompt Usage 校准误差，后者只进入保守上界和阈值判断，避免默认预留制造
虚假的估算高估。Canonical Tool Contract 只包含 Eino OpenAI 兼容 Adapter 实际发送的
名称、描述和参数 Schema；TaskScope 导致的 Tool Contract 变化会自然产生新的 Prompt
Epoch，但不会为了缓存命中扩大授权。Continuous Token Tail 已在 Feature Flag 下启用；
关闭开关回到 Rune 路径时，若选出非连续历史，Manifest 会标记
`context_degraded/non_continuous_tail`，便于灰度观测与回滚比较。

## 14. Prompt Manifest 与观测

只保存元数据，不保存完整 Prompt：

```text
prompt_epoch_id
preflight_status / failure_stage
prompt_identity_available / estimate_available
model_profile_fingerprint
system_prompt_version
tool_schema_fingerprint
skill_prompt_fingerprint
summary_snapshot_id
hard_compaction_triggered
tail_from_seq / tail_through_seq
estimated_prompt_tokens
estimated_upper_bound_tokens
actual_prompt_tokens
cache_hit_tokens
cache_miss_tokens
completion_tokens
estimation_error_ratio
context_degraded
duration
```

正常软压缩对用户无感；硬压缩的细粒度“正在整理会话上下文”Turn Event 是可选 UX 增强，
当前先记录不含载荷的 Manifest 触发位。重试耗尽由 Turn 失败/重试语义向用户显示
“上下文整理失败，请重试”。普通用户不读取内部 Snapshot、Fingerprint 或缓存明细，管理员和
评测工具只读取有界元数据。

## 15. 评测设计

### 15.1 三组对照

```text
Current：历史 32K Rune 非连续筛选
Baseline：Token 预检 + 完整连续历史，不摘要
Experiment：Token 预检 + Summary Snapshot + Continuous Tail
```

摘要效果使用 Baseline vs Experiment；Current 只证明为什么替换 Rune 算法。超过模型窗口的
会话单独统计可继续执行率，不混入 Token 降幅主指标。

### 15.2 数据集与成本

Pilot：4 个会话剧本，每个 3 个检查点；Acceptance：12 个会话剧本，每个 3 个检查点。
历史消息直接预置，不为构造 100 轮历史调用 100 次模型。检查点才调用 Summary、主模型和可选
Judge。

```toml
maxEvaluationCalls = 200
maxEstimatedCostCNY = 10
evaluationConcurrency = 1
```

### 15.3 指标

质量门：

```text
Fact Recall >= 95%
Decision Recall >= 95%
Correction Accuracy = 100%
Todo State Accuracy >= 95%
Evidence Reference Recall >= 95%
Hallucination Rate <= 2%
Answer Correctness 相对 Baseline 下降 <= 2 个百分点
```

工程门：

```text
端到端 Raw Token Reduction 目标 >= 60%
TokenEstimator P95 低估误差 <= 5%
Prompt 超过模型硬窗口次数 = 0
```

Token 主口径包含主模型和摘要模型全部 Input/Output Token：

```text
rawTokenReduction =
  1 - experimentAllModelTokens / baselineAllModelTokens
```

另行报告 Main Prompt Reduction、Summary Overhead、Cache Hit Ratio、Cache-adjusted Cost、
First Token Latency 和 Prompt Epoch Churn，不能把缓存命中 Token 伪装成被摘要消除的 Token。

## 16. Feature Flag 与上线顺序

1. 影子计算 Token Estimate 和 Prompt Manifest，不改变 Prompt（已完成）；
2. 在独立 Feature Flag 下启用 Continuous Tail 和逐次 Provider 硬窗口保护，保留 Rune 回滚（已完成）；
3. 生成 Shadow Snapshot，但不用于回答（已完成）；
4. 在独立 Feature Flag 下启用硬阈值同步 CAS 激活和 Summary + Tail（已完成，待 Pilot）；
5. Pilot 与 Acceptance 通过后全量启用；
6. 发生质量问题关闭 Summary，回退 Continuous Tail；再关闭 Continuous Tail 可回退 Rune 路径，
   原始消息和 Snapshot 均无需回滚。

## 17. 有序实现切片

1. 配置、Provider Profile、TokenEstimator 和 TokenBudgetPlanner（已完成）；
2. Continuous Token Tail、Feature Flag 回滚和逐次 Provider 硬窗口保护（已完成）；
3. Snapshot/Manifest 领域模型、迁移、Repository 和 CAS（已完成）；
4. Structured MemoryCompactor、Validator 和重试语义（已完成）；
5. ConversationMemoryAssembler 接入硬阈值 Summary + Tail（已完成）；
6. Outbox/Worker、Lease/Fencing 和 Redis 热缓存；
7. `read_conversation_memory_sources` Tool；
8. Diagnosis Context Preflight 与有界 Tool Result；
9. Provider 原生 Compaction/Tool Exposure 接口预留；
9. Pilot、Acceptance、指标核验和简历最终口径；
10. 后端闭环后统一设计前端压缩状态和管理员观测页。

## 18. 后续增强点

- 当前会话历史关键词/语义检索；
- 分层摘要：消息段摘要、阶段摘要、全局摘要；
- 重要性/信息熵驱动的自适应压缩；
- 双模型摘要校验和摘要漂移检测；
- Provider 原生 deferred Tool、Prompt Cache 和 Compaction 深度适配；
- Tool 数量达到大规模后的 Tool Gateway；
- 模型请求、宿主执行的 Diagnosis Context Compaction；
- 跨 Conversation 长期记忆，必须独立进行隐私、删除和授权设计。

这些增强点不能在首版评测完成前写成已实现结果。

## 19. 参考

- Eino ADK ChatModelAgent、Skill Middleware 和 Tool Calling；
- LangChain Summarization Middleware 的 trigger/keep 可配置模型；
- Anthropic Prompt Caching、Context Compaction 和 Mid-conversation Tool Changes；
- DeepSeek Context Cache 和 Tool Calling Usage；
- 项目内 `agent-orchestration.md`、`database.md`、`004-conversation-driven-diagnosis-commands.md`。
