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
- 软阈值异步摘要任务、Memory Worker 和 Outbox 已实现：Turn 完成事务仅在
  `SoftThresholdReached && !HardCompactionTriggered` 时幂等创建 Job；Job 与首条 Outbox
  共用 PostgreSQL 事务，Worker 复用同一候选生成/校验服务；
- Memory Job 使用 `pending/running/retry_wait/succeeded/failed` 状态、Lease Renewal、递增
  Fencing Token、指数退避 + 可配置 jitter；候选 Snapshot 先保存，只有持有有效 Lease/Fencing
  的 Complete 事务才能 CAS 激活，过期 Worker 的候选可保留审计但不能发布；
- 异步 Worker 每个持久化 Job Attempt 只进行一次模型压缩，默认三个 Job Attempt 即总计最多
  三次模型调用，避免与 Service 内部重试相乘；同步硬阈值路径按 Summary `maxAttempts` 执行，
  当前默认一次，失败后向调用方返回可重试错误；
- 已通过真实 PostgreSQL 验证 Job 调度、并发 Claim 单赢家、Lease reclaim、过期 Worker 拒绝、
  重叠 Job 的 CAS Winner 收敛和 Retry Outbox；RabbitMQ Publisher/Consumer 路由及独立
  `mesguard-memory-worker` Compose 进程已启动验证；
- Redis Active Snapshot 热缓存已实现：PostgreSQL 先返回不含 JSONB Payload 的权威 Active
  Identity，Redis 再按 `Conversation ID + Snapshot ID` 读取不可变 Snapshot；命中必须同时匹配
  Version 和 Payload SHA-256，miss、超时、非法载荷、陈旧身份和写失败均回 PostgreSQL；
- Redis 默认 TTL 为两小时、10% jitter、单次命令 50ms 超时；命中/未命中使用 Debug 结构化
  观测，降级使用 Warn 并记录原因和缓存耗时，不会被描述为记忆丢失；真实 Redis 测试已覆盖
  TTL、索引删除和污染索引成员保护；
- 已实现 `context-governance-pilot-v1` 的 4 场景/12 检查点固定集、Current/Baseline/Experiment
  三组 Evaluator 和显式 Provider Observer；2026-08-12 的首轮真实 Provider 试跑因摘要失败率、
  可比样本和费用门禁问题判为无效，受控 Pilot 尚待重新执行。

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
生成、Usage 或校验失败只做有界重试，不生成确定性低质量替代摘要。候选 Snapshot 在 CAS
激活前还必须通过主 Chat Profile 的实际 Summary Token 预算门禁；超预算候选只保留审计，
不能替换 Active。硬阈值路径使用新 Active Summary 替换旧 Summary，再与连续 Tail 组装；
旧 Active 只有在 `through_seq + 1` 与 Tail 起点连续且仍能安全入窗时才允许回退，否则失败关闭。

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
- Summary 作为带明确边界标签的 User-role 不可信上下文进入主模型，不提升为 System 指令；
  Summary Fingerprint 同时包含消息角色，静态 System Prompt 始终保持最高指令优先级。

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

它只允许根据当前 Active Snapshot 中声明的 Entry 或消息序号，恢复当前 Conversation 的有界原文：

```text
每次最多 20 条消息
每次最多 8K Tokens
每轮最多调用 2 次
第一次超限返回 Run 级 continuation cursor
第二次后仍有内容则标记单轮预算截断，不返回不可用 cursor
只能读取当前用户有权访问的 Conversation
读取写入 Tool Trace
```

当前实现已完成：Tool 不接受 `conversationId`，Actor 和 Conversation 只来自当前
`CommandContext`；`memory` capability 由 Conversation `TaskScope` 在 Epoch 开始时授权，
不会根据模型选择的 Entry 动态改变 Tool Schema。Entry、直接序号和 Continuation Cursor
必须三选一；可在 Entry/直接序号选择器上附加最多 256 Rune 的 `query`，以确定性词项匹配、
CJK bigram、800 Rune 分块评分和相邻窗口合并定位相关原文。单条明确源序号也可附加
`contentOffsetRunes` 做无状态偏移读取，但不能与 Entry、Query 或 Cursor 混用。superseded/未知
Entry 与未声明序号在访问 PostgreSQL 前拒绝。PostgreSQL 读取在同一条查询中 JOIN Conversation
所有者，并要求授权序号完整命中。

Cursor 是随机、不持久化、Run 级受校验句柄，绑定用户、Conversation、Active Snapshot、
授权序号、读取模式、相关窗口、消息索引和 Rune 偏移；不能跨 Run、用户、Conversation 或
Snapshot 使用，成功续传后旧 Cursor 失效。单条超长消息也可按 Rune 偏移继续读取，避免
“单条消息超过 8K”时生成无法前进的 Cursor。Token 计数复用当前 Chat Profile 的本地
TokenEstimator，并按
`UpperBoundTokens` 保守门禁。条数、Token 和每轮调用次数均配置化，但配置不能突破
`20 / 8192 / 2` 的硬上限；单次恢复 Token 上限还不能超过 `toolGrowthReserveTokens`，保证
Prompt Planner 为下一次 Tool Result 预留的窗口覆盖最坏返回量。

最终取舍是维持 Run-only Cursor 和每 Turn 两次上限，不引入跨 Turn Cursor 持久状态，也不宣称
任意 20K Rune 消息都能完整回放。第二次调用后若底层仍有内容，Tool 返回 `hasMore=true`、
`continuationAvailable=false`、`truncatedByTurnBudget=true`，让模型知道证据仍不完整但本轮不可
继续。优先使用相关窗口通常无需顺序翻阅整条消息；完整大日志、文档或多媒体内容进入附件解析
链路。这一取舍控制 Prompt 膨胀和状态复杂度，同时保留明确的证据不足语义。

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

首版 Redis 只缓存 Active Snapshot：

```toml
memoryCacheEnabled = true
memoryCacheTTL = "2h"
memoryCacheJitterRatio = 0.10
memoryCacheTimeoutMillis = 50
```

每次读取先由 PostgreSQL 轻量查询权威 `ActiveSnapshotIdentity(snapshot_id, version,
payload_sha256)`，再读取包含 Conversation + Snapshot ID 的 Redis Key。缓存载荷必须通过完整
Snapshot 校验并与权威身份一致，否则读取 PostgreSQL 完整 Active Snapshot 并尽力回填。该身份
查询是 Active 读取的事实线性化点；Redis 不持有发布权，不作为分布式锁，也不进入 CAS、Lease
或 Fencing。

Cache Adapter 维护每个 Conversation 的 Snapshot Key 索引，提供尽力清理 hook；当前产品尚无
Conversation 删除命令，未来接入删除事务后调用该 hook，清理失败依靠各 Snapshot Key 的 TTL
最终过期。当前 durable History 在 Runner 之前已经从 PostgreSQL 读取，Tail Projection 随每轮
`through_seq` 变化；缓存它不能减少事实库读取且几乎无法跨轮命中，因此改为基准驱动的后续增强，
不在首版建立重复的 History 失效协议。Prompt Manifest 缓存同样不在本切片实现。

## 11. Diagnosis Token Budget

Diagnosis 共用 TokenBudgetPlanner，但首版不生成 Conversation Summary：

- 初始 Prompt 预检；
- CaseSnapshot、Evidence、Tool Result 和 Report Output Reserve 分预算；
- Tool Result 保存完整证据，只向模型返回有界内容和 Evidence ID；
- 每次模型调用前重新预检；
- 首版只记录最大上下文水位并执行宿主侧硬保护；
- 如果真实固定集频繁超过 70%，再实现模型请求、宿主执行的 Diagnosis Compaction Epoch；
- 不能只依赖模型主动调用压缩 Tool，因为请求发送前超窗时模型没有机会执行 Tool。

当前实现由每次 Diagnosis Run 独占的 `diagnosisContextGuardModel` 包装主模型边界。
首个调用额外预算 Worker 冻结的 CaseSnapshot；后续调用依据 ADK 当前消息重新预算，System、
预加载 Skill、动态 Tool Contract、Evidence Index 和模型可见 Tool Result 分段计数。
CaseSnapshot 只作为预算 Seed，不重复注入用户 Prompt，模型仍通过 `read_external_case` 获取证据。

Tool 返回经过自身脱敏和领域限流的完整结果后，Runner 先生成不可变 EvidenceItem，再创建不超过
`maxToolResultBytes` 的模型可见 Envelope。Envelope 超限时只保留 `evidenceRef/sourceType/truncated`
和必要句柄，完整 Snapshot 与哈希不受模型投影截断影响。每次预算仍保留
`toolGrowthReserveTokens`，而 Chat Profile 的 `maxOutputTokens` 预留给结构化报告输出。

保守上界超过硬窗口时 Guard 在 Provider 调用前阻断。估算失败同样 fail-closed；Evidence
Orchestrator 将两者收敛为 `context_window_exceeded/context_preflight_failed` 部分报告，不在相同
上下文上重复尝试。正式报告的 `technical_summary.contextObservation` 记录 Preflight 调用与失败数、
最大上界/可用输入/水位比例、模型投影截断数、硬窗口拦截数、两类预留和估算方法。

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
conversationMemoryProfile = "stepfun-conversation-memory"

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

[models.chat.profiles.stepfun-conversation-memory]
provider = "stepfun"
model = "step-3.7-flash"
reasoningEffort = "low"
responseFormat = "json_schema"
responseSchema = "conversation_memory_v1"
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
diagnosisPreflightEnabled = true
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

结构化摘要采用 Provider 能力协商，而不是假设所有 OpenAI 兼容端点能力相同：

| Provider | `json_object` | 严格 `json_schema` | 当前摘要用途 |
|---|---:|---:|---|
| StepFun | 支持 | 支持 | 默认，使用 `conversation_memory_v1` 且 `strict=true` |
| DeepSeek | 支持 | 未确认 | 不允许静默承接严格 Schema Profile |
| DashScope | 支持 | 支持 | 可选 Profile，不再作为当前默认摘要模型 |

以上边界以 2026-08-12 官方 Chat/JSON Output 文档为准。DeepSeek 文档只给出
`response_format={"type":"json_object"}`，并提示可能返回空 `content`；因此 Factory 将
`JSONOutput` 与 `JSONSchemaOutput` 分开声明。切换 Provider 时先校验 Profile 所需能力：
严格 Schema 不可用就 fail-fast，不能偷偷降级为 Prompt-only。若未来允许弱化，需要管理员显式
选择独立 Profile，并接受质量口径变化。

无论原生约束强弱，摘要都保留三层防线：Provider 原生格式约束、Prompt 中的字段说明与示例、
Go 侧严格解码及领域校验。JSON Schema 只能保证形状，来源序号存在性、用户事实不可丢、
supersede 唯一性、lineage 和会话边界仍由应用校验。`finish_reason=length`、空内容、解析失败和
领域不变量失败均不得激活 Snapshot。

只观测/Continuous Tail 路径的 Preflight 使用独立、可配置的短超时，主模型超时在观测完成后
单独起算；观测失败只记录有界错误码与降级 Manifest。启用 Summary + Tail 后，同步压缩与
当前 Turn 共享总截止时间，压缩或组装失败采用 fail-closed，不调用主模型。
Manifest 通过 `preflight_status`、`prompt_identity_available` 和 `estimate_available` 区分
成功、部分已知和失败观测；未知 Tool Contract 不生成伪 Epoch，估算失败不生成 0 Token 样本。
Conversation 与 Diagnosis 使用显式 Runtime Role：只有 Conversation Runtime 构造并依赖
`conversation-memory` Profile，Diagnosis Worker 不因 Summary + Tail 开启而要求摘要模型凭证。

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

当前 Pilot 固定集版本为 `fixture-2026-08-12-v3`。生产本地估算器已固定压力梯度：每个场景
`cp1` 未达到硬阈值，`cp2` 达到 85% 硬阈值但仍可先压缩，`cp3` 的完整 Baseline 历史超过
模型窗口。各 Arm 的检查点时间线单调递增，真实检查点回答不反馈给下一个检查点，避免不同
Arm 的回答质量反向改变后续输入；Experiment 在同一场景内复用 Active Snapshot。

```toml
maxEvaluationCalls = 200
maxEstimatedCostCNY = 10
evaluationConcurrency = 1
```

Provider-free 全量计划仍用于容量评审，不再直接作为远程执行许可；Summary 最坏调用数按配置的
`maxAttempts` 计算。真实 Observer 默认只允许筛选后的单个检查点，硬上限为 1 次主模型、1 次
Summary，累计估算输入 Token 各 130K、保守费用 0.50 CNY。扩大范围必须显式提高相关预算；
每次主模型、Summary 和失败重试都会在 Provider 前分别预留调用数、累计输入 Token 和费用。

2026-08-12 首轮试跑暴露了旧门禁错误：全量运行及后续诊断文件共记录 89 次 DashScope Summary
请求；其中 Provider 返回 Usage 的输入约 6,771,035 Token、输出约 42,036 Token，另有 3 次
失败请求没有 Usage。根因是 65K-100K Token 的长摘要输入叠加 `maxAttempts=3` 和连续复测。
该批次可比样本不足，全部作废，不进入简历指标。当前摘要 Profile 已切换到 StepFun Token Plan；
生产可靠性默认仍保留 `maxAttempts=3`，测试成本由 Observer 在 Provider 调用前按最坏重试次数实施
独立分类型预算，不能通过降低生产重试次数来规避评测费用门禁。
远程复测需用户再次明确授权。

该轮仍产出了可复用的工程结果，但没有产出 Token 降幅结论：修复了 Experiment 摘要失败成本被
汇总隐藏的问题；区分“实验 Arm 超窗”和“Provider 实际硬窗口违规”；将 Continuous Tail 从固定
100 条改为受 10,000 条、8 MiB 和 Token 预算共同约束的连续后缀选择；补齐当前消息必选、序号
缺口停止、摘要错误分类及错误链；并把主模型与 Summary 的调用数、输入 Token 和费用改为独立的
Provider 前硬门禁。首轮 36 条运行只有 4 个可比 Pair，表面 Raw Token Reduction 约 -0.08%，
失败率过高，不能解释为方案无收益或作为简历数据。

以后每次远程评测必须在执行前给出 Provider/Profile、样本数、最坏调用次数、单次估算输入/输出
Token、最坏总 Token、费用上限和重试次数。默认只允许一次筛选后的最小探针；没有用户对该预算
的当次明确授权，不得扩大样本或连续重跑。

2026-08-12 StepFun 严格 Schema 首次探针只执行 `incident-correction/incident-cp2/experiment`：
Summary 预估输入 88,727 Token，在 Profile 的 30 秒超时内没有返回，记录 1 次 Summary 请求、
0 次主模型请求且无 Provider Usage。它证明分类型调用门禁和“失败不进入主模型”生效，但既不能
证明严格 Schema 已被端点接受，也不能把未知 Usage 当作 0 Token。原错误分类将嵌套在
`ErrProviderRequest` 内的 Deadline 误记为 `agent_timeout`，现已区分为 `summary_timeout`。
下一次协议兼容验证应使用小输入专用 Smoke；长输入延迟另设单样本测试，不能再拿高成本 Pilot
同时承担协议探测、质量和性能验证三种职责。

小输入协议 Smoke 已落在 `tools/smoke/mesguard-conversation-memory-smoke`，默认只输出离线
Preflight，必须显式增加 `-execute-provider` 才允许一次 Provider 调用。它使用最小协议 Prompt、
生产 `conversation_memory_v1` Schema、同一 `memorycompactor` 严格解码和 `ValidatePayload` 领域
校验，不写 PostgreSQL、不激活 Snapshot，也不调用主模型。字段级 Schema 约束补齐后，保守输入
上界硬限制调整为 3,000 Token；这仍与 88K 长输入 Pilot 明确隔离。

2026-08-12 首次小输入 StepFun Smoke 已确认端点接受 `type=json_schema`、`strict=true` 请求，
返回内容也通过 JSON 解码和固定九字段结构检查，但 Go 领域校验以
`conversation memory entry status is invalid` 拒绝结果。根因是反射生成的原 Schema 只把
`EntryStatus` 和 `ReferenceType` 表达成任意字符串；现已将状态和引用类型合法值写入 Schema
枚举，同时继续保留“不同分区允许哪些状态”等 Go 侧语义校验。枚举增强后的初版离线保守输入
上界为 1,984 Token；继续补齐 Entry ID、内容长度和来源数组约束后，完整 Schema 的保守上界约为
2,581 Token。

随后将未经 StepFun 子集确认的 `pattern`、`minItems/maxItems`、`uniqueItems` 等关键字从 Wire
Schema 移除，完整约束继续由 Go 领域层执行；这样既保持了 Provider 严格结构约束，也避免把
Draft JSON Schema 的全部能力错误地假设为 Provider 能力。最终离线保守上界为 1,984 Token。
修复后的单次 Smoke 已通过：StepFun 接受严格 Schema，输入 224、输出 1,837、总计 2,061
Token，耗时 14.8 秒，`domainValidated=true`，没有写入 Snapshot。这个结果只证明协议与小输入
领域契约，不代表 88K 长摘要的延迟或 Pilot/Acceptance 指标已经通过。

Smoke 的失败输出只允许稳定机器码，例如 `provider_http_400`、`provider_timeout`、
`entry_entry_id` 和 `entry_status`，不输出 Provider 原始错误或模型生成内容。这样失败请求的
Usage、延迟和阶段可审计，同时不会把敏感 Prompt 或摘要泄露到评测日志。

会话记忆装配边界同时强制 `responseFormat=json_schema` 与
`responseSchema=conversation_memory_v1`；配置为 `text`、`json_object` 或其他 Schema 时，在
Provider 调用前 fail-fast。领域校验错误由 `conversationmemory.FailureCode` 统一归一化，生产
修复提示、Smoke 和 Pilot 只添加各自阶段语义，避免同一错误在三处产生不同统计名称。

长输入延迟已完成单检查点探针。固定 `incident-correction/incident-cp2/experiment` 的压缩前
本地估算为 88,727 Token：30 秒配置只产生 1 次 Summary、0 次主模型调用且无 Usage；随后用
Observer 专用 60 秒覆盖运行，Provider 报告输入 61,561、输出 4,096 Token，暴露一次输出顶满
波动。最终以 `summary-attempts=1` 复测成功：Summary 输入 61,632、输出 3,542、缓存 896 Token，
主模型输入 12,303、输出 249 Token，总耗时 38.57 秒，回答正确保留“库存行锁死锁替代网络抖动”
和 `report:diag-2048-b`。据此生产 Summary 单次超时调整为 60 秒，Conversation 总截止时间调整为
120 秒；同步硬压缩另设 90 秒阶段上限，始终为主回答预留至少约 30 秒。三次修复重试是请求
截止时间内的尝试上限，不承诺每次都耗满 60 秒；异步软压缩仍由 Worker 的持久化重试完成。

`finish_reason=length` 现在映射为稳定的 `summary_output_truncated`，并在重试时传入
`repairCode=output_truncated`，要求模型删除重复和低价值条目，而不是把截断误报为泛化 Provider
失败。Observer 的 `summary-timeout`/`summary-attempts` 仅覆盖评测进程，不修改生产配置；每次
请求仍受调用、累计 Prompt Token 和费用门禁约束。单检查点成功只完成长摘要延迟与链路验收，
不等于 4 场景 Pilot 或 12 场景 Acceptance 已通过。

进程内结构修复采用指数退避和 10% jitter，等待继承压缩阶段 `ctx`；多个会话同时遇到截断、
Schema 错误或 Provider 瞬态失败时，不会按完全相同的间隔形成同步重试峰值。

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
可比 Token、成本和首 Token 延迟只纳入 Baseline 与 Experiment 均在硬窗口内且无错误的检查点；
窗口内 Provider/Runner 失败单独计入 `failedRuns` 并触发 `run_failure` 质量门，避免失败样本被
静默排除后抬高降幅。

## 16. Feature Flag 与上线顺序

1. 影子计算 Token Estimate 和 Prompt Manifest，不改变 Prompt（已完成）；
2. 在独立 Feature Flag 下启用 Continuous Tail 和逐次 Provider 硬窗口保护，保留 Rune 回滚（已完成）；
3. 生成 Shadow Snapshot，但不用于回答（已完成）；
4. 在独立 Feature Flag 下启用硬阈值同步 CAS 激活和 Summary + Tail（已完成，待真实 Pilot）；
5. 启用软阈值 Memory Outbox/Worker，异步生成候选并在有效 Lease/Fencing 下 CAS 发布（已完成，待真实 Pilot）；
6. Pilot 固定集、Evaluator 和受预算保护的 Observer（已完成；真实 Provider 执行待显式成本确认）；
7. Pilot 与 Acceptance 通过后全量启用；
8. 发生质量问题关闭 Summary，回退 Continuous Tail；再关闭 Continuous Tail 可回退 Rune 路径，
   原始消息和 Snapshot 均无需回滚。

## 17. 有序实现切片

1. 配置、Provider Profile、TokenEstimator 和 TokenBudgetPlanner（已完成）；
2. Continuous Token Tail、Feature Flag 回滚和逐次 Provider 硬窗口保护（已完成）；
3. Snapshot/Manifest 领域模型、迁移、Repository 和 CAS（已完成）；
4. Structured MemoryCompactor、Validator 和重试语义（已完成）；
5. ConversationMemoryAssembler 接入硬阈值 Summary + Tail（已完成）；
6. Memory Outbox/Worker、Lease/Fencing（已完成）；
7. Redis Active Snapshot 热记忆缓存与 PostgreSQL 回源（已完成；Tail Projection 缓存改为基准驱动增强）；
8. `read_conversation_memory_sources` Tool（已完成：相关窗口、单条偏移、Run 级 Cursor 与单轮预算截断）；
9. Diagnosis Context Preflight 与有界 Tool Result（已完成）；
10. Provider 原生 Compaction/Tool Exposure 接口预留；
11. Pilot 工具链（已完成）与真实 Provider 观测（待显式成本确认）；
12. Acceptance、指标核验和简历最终口径；
13. 后端闭环后统一设计前端压缩状态和管理员观测页。

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
