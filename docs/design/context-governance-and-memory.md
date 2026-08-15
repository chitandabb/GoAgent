# 动态上下文治理与分层记忆设计

本文解释 MESGuard 简历第四点的现行设计、模块边界和关键取舍。验收合同见
[`m3-context-governance-and-layered-memory.md`](../specs/m3-context-governance-and-layered-memory.md)。

## 架构

```text
Conversation Runner
  -> Conversation Context Preflight
       -> TokenBudgetPlanner
       -> Current Summary Reader
       -> Continuous Tail Selector
       -> Prompt Manifest
  -> soft threshold -> Outbox -> Memory Worker --+
  -> hard threshold -> synchronous compaction ----+-> Conversation Coordinator
                                                       -> reload Current Summary
                                                       -> MemoryCompactor
                                                       -> Validator
                                                       -> Summary Repository
```

`TokenBudgetPlanner` 只计算预算，不读数据库、不授权 Tool、不调用模型。`ConversationMemoryService`
隐藏压缩、校验、发布和缓存细节。记忆模块永远不能扩权：部署配置决定固定 Tool Schema（`ToolProfile`），`RunAccess`/`ResourceGrant` 决定可执行性，当前消息引用和临时依赖健康不再动态删除 Schema；任何阶段都不读取摘要内容决定权限。

## 为什么只有 Current Summary

原始消息已经完整保存，因此摘要无需再承担事实版本库职责。旧设计给 Entry 增加 predecessor、
active/superseded 和唯一叶子，把自然变化的会话事实变成模型必须维护的图一致性问题。真实测试反复
出现 `multiple_active_entries`，并触发结构修复重试，放大 Token、费用和延迟。

最终设计把摘要定义为当前状态投影：上一摘要与新增消息合成下一摘要，可以更新、合并、删除条目；
`throughSeq` 是唯一增量边界，`sourceMessageSeqs` 用于追溯原文。修正仍可作为当前有用结论保留，
但不再指向旧 Entry。历史审计直接读取原始消息。

## 并发设计

软压缩用于用户无感，硬压缩用于“再不压缩就无法安全调用模型”。它们共享同一 Conversation 锁，
不是两套并发摘要流程。生产使用 PostgreSQL advisory lock，因为 API 与 Memory Worker 是不同进程；
锁绑定单一数据库连接，释放使用不可取消的短超时 context，并在 panic 路径通过 defer 执行。

等待者获得锁后必须重读摘要：若另一路已经把 `throughSeq` 推进到目标，则直接复用。这一检查同时
避免两个模型调用。不同 Conversation 不共享锁，可并行压缩。

Worker 先在 Coordinator 内发布摘要，再提交 Job 成功。若锁释放后另一路又发布了更靠后的摘要，
Job Complete 可以按覆盖关系成功；同一 Snapshot ID 必须边界完全一致，不同 Snapshot ID 必须比
Worker 结果更靠后，不能用同边界的另一份摘要冒充成功。Job Lease 与 Fencing 只防止旧 Worker
提交任务状态。

## Provider 输出与引用

Provider-facing Schema `conversation_memory_v2` 不包含 `supersedesEntryId` 或 superseded 枚举。
持久化 `Payload` 类型暂保留 schema v1 只读兼容字段，但 Provider 若输出 lineage，Service 会拒绝并
进入现有有界结构重试，不会静默删除条目。Prompt `conversation-memory-v9` 明确只输出当前状态。

模型可以选择保留哪些 Evidence/Task/Report，并给出简短当前结论；应用侧从结构化消息 catalog
认证并重建引用 ID、类型、哈希、来源和 Entry ID。已有引用的身份保持稳定，但模型可更新结论文字。
伪造或无结构化来源的引用被删除。

## Prompt 与缓存

模型 Profile 提供窗口、输出上限、安全余量和 Tokenizer 策略。Planner 对实际可见的 System、Tool、
Skill、Summary、Tail、引用、当前消息和 Tool Growth 做保守估算。Summary/Tail 默认占窗口 5%/15%；
Summary 先按业务优先级排序，再用 Planner 二分选择预算内最大 Entry 前缀。

旧 v1 在 Prompt Epoch 开始时冻结 TaskScope 过滤后的 Tool Schema；统一 Runtime v2 改为部署内固定 Tool Profile，同一部署内的当前消息引用和依赖瞬时健康不再开启新 Schema Epoch。Skill 渐进加载只追加 SOP，不动态授予 Tool。部署配置导致 Profile 变化时必须产生新的 Tool Schema 指纹与 Prompt Epoch；缓存命中只影响成本/延迟，不改变窗口 Token 计算。

## 持久化兼容边界

PostgreSQL schema v1 仍保存不可变 Snapshot 行、candidate/active 状态、predecessor 和 Job activation
字段。这些是当前 Repository 的存储实现和旧数据兼容，不是业务版本图。运行时只读取 Current Summary。
后续单独 migration 可改为每 Conversation 一条 current projection 或保留审计行但删除发布仲裁字段。

Redis 只缓存 PostgreSQL 认定的 Current Summary，miss、超时、脏数据和写失败均回源；不参与锁、
授权或发布。删除 Conversation 时 PostgreSQL 级联，Redis 尽力清理并依赖 TTL 兜底。

## 工程风险与取舍

PostgreSQL session advisory lock 必须绑定同一个连接，因此远程摘要调用期间会占用一个连接；同一
Conversation 的等待者在等锁时也会占连接。当前 Memory Worker 使用 `QoS=1` 串行消费，默认数据库
池上限为 100，单实例异步路径的连接压力可控；API 硬压缩和未来横向扩容仍可能放大占用。

当前不为这个容量风险引入 Redis 锁、新租约状态机或第二套协调器。上线观测应至少记录锁等待时长、
持锁时长、压缩并发和数据库池等待。只有这些指标逼近容量边界时，再选择限制压缩并发、给压缩使用
独立小连接池，或缩短持锁区间；最后一种方案必须先解决锁外模型调用导致的重复计算问题。

schema v1 的 candidate/active、predecessor 和旧公开方法仍会增加认知成本，但它们被限制在兼容层，
生产入口统一使用 `PrepareActive`。在固定集闭环前物理迁移数据库或删除兼容 API，收益低于回归风险。

## 失败策略

- 本地估算失败：Continuous Tail 启用后 fail closed。
- Summary Provider/Schema/校验失败：有界重试；旧摘要仍可用则保留，否则硬阈值返回可重试错误。
- 异步失败：Job 指数退避，当前用户回合不阻塞。
- Redis 失败：回退 PostgreSQL。
- Prompt 超窗：调用前拒绝，绝不寄希望于模型在超窗后自己调用压缩 Tool。
- 不实现低质量确定性摘要兜底。

## 指标与实验教训

受控真实序列 `incident-correction` 已完成：cp2 主模型输入从 59,386 降至 12,434，下降约 79.1%；
Baseline 的 cp3 预估输入为 132,621，超过 128K 而在调用前拒绝，Experiment 三个 checkpoint 均成功
继续执行。共同成功的 cp1+cp2 主模型输入从 89,558 降至 42,606，下降约 52.4%。Experiment
三轮端到端总 Token 为 160,377，其中 Summary 为 101,534；首次摘要成本尚未被短序列摊薄，
因此不能宣称端到端平均降低 60%。

这次踩坑形成三条工程规则：

1. 首次压缩成本必须在多轮复用中摊销，单点主模型降幅不是端到端降幅。
2. 评测默认单 Case、小批次、调用/Token/费用门禁；失败样本不能自动无限重跑。
3. 不让模型维护可以由原始事实源替代的版本图；确定性身份由应用拥有，模型只负责语义选择。

最终固定集同时报告主模型输入、端到端 Token、超窗恢复、质量、延迟、成本、摘要成功率、软压缩
阻塞率和重复调用抑制率。简历数字必须来自固定集实测；原始 `60%+` 目标可被诚实替换。

## 实现状态

已完成 Planner、Token Tail、Prompt Manifest、Summary + Tail、软硬阈值、Conversation Coordinator、
Memory Worker、Redis 缓存、稳定引用、原文恢复、Diagnosis preflight、Provider-free Pilot 和成本门禁。
Provider lineage 会被严格拒绝，Job 完成条件已防止同边界错误摘要冒充成功。本地 coordinator race
测试与全仓测试通过，真实 PostgreSQL 双连接测试已纳入 integration suite。

剩余工作是可选的全量固定集复测、按实测更新简历，以及后续 schema v1 兼容列 migration；前端展示在
后端整体闭环后统一联调。
