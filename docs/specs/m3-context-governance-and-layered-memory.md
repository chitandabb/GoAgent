# M3 动态上下文治理与分层记忆规格

> 状态：运行时合同已实现，固定集验收待执行。
> 本规格是简历第四点的唯一现行契约。`60%+` 是原始目标，不是必须凑出的结果。

## 目标

MESGuard 根据模型窗口、输出预留、System Prompt、Tool Schema、Skill、引用和 Tool Result
增长预留动态计算 Token 预算；长会话使用“结构化当前摘要 + 连续尾部窗口”重组 Prompt，
不发送超过模型硬窗口的请求，并在压缩后保留事实、决策、修正、待办和可信引用。

首版只做单 Conversation 隔离，不做跨会话长期记忆。Diagnosis 复用 TokenBudgetPlanner 和
有界 Tool Result，但不生成 Conversation Summary。

## 事实与摘要模型

- `conversation_messages` 是唯一语义事实源，永久保存到会话生命周期结束。
- 每个 Conversation 在运行时只有一份 Current Summary 和 `throughSeq`。
- 增量压缩输入是“上一 Current Summary + `throughSeq` 之后的已完成消息”。
- Payload 固定包含 goal、facts、decisions、corrections、evidence、open questions、todos、tasks、reports。
- 普通 Entry 只表达当前有效状态；Todo 为 open/completed/cancelled。
- 不维护 Entry lineage、`supersedesEntryId`、active/superseded 图或 `multiple_active_entries`。
- 模型决定当前是否保留稳定引用；应用侧认证并重建 ID、类型、哈希和来源序号。
- 摘要可丢弃并由原始消息重建，不是第二套事实版本库。

schema v1 Snapshot/Job 表中的 predecessor、candidate/active、activation 字段仅为已有数据兼容壳，
不属于运行时业务模型；物理清理由后续独立 migration 完成。

## Token 与 Prompt

- `availableInput = contextWindow - maxOutput - max(absoluteSafety, ratioSafety)`。
- 调用前估算包含模型实际可见的所有 Prompt Segment，缓存命中不减少窗口占用。
- Summary + Continuous Tail 默认不超过模型窗口 20%，分别默认不超过 5% 和 15%。
- Tail 从当前消息向前连续选择，遇到首条放不下即停止，不跳选更老消息。
- Summary 投影按实际 Token 预算动态选择 Entry；`promptMaxEntries=64` 只防止碎片化。
- Current User Message、当前 Tool Call/Result 和安全/输出合同不能被摘要替代。
- Prompt Epoch 冻结 TaskScope 授权 Tool Schema；Skill 装载不授予新权限。

## 软硬压缩

软阈值默认 70%，在 Turn 完成后通过 Outbox/Worker 异步触发，用户不等待。硬阈值默认 85%，
在主模型调用前同步等待压缩，失败时返回可重试错误，不使用低质量确定性摘要。

软硬路径共享 Conversation Coordinator：

1. 获取 Conversation 级锁；生产使用 PostgreSQL session advisory lock，本地测试使用进程锁。
2. 锁内重读 Current Summary。
3. 若 `throughSeq` 已覆盖目标，直接复用，不调用模型。
4. 否则基于上一摘要和新增消息生成、严格解码、认证引用、确定性校验并发布。
5. 释放锁后继续主模型或提交异步 Job 终态。

Job Lease/Fencing 只保护投递、重试、重复消费和终态提交，不仲裁摘要发布。异步 Job 每个 durable
attempt 只调用模型一次；同步硬压缩按 Summary `maxAttempts` 做有界结构修复重试。

Provider 若输出 schema v1 lineage 字段，必须校验失败并进入上述有界重试；不得通过静默删除
superseded 条目来伪造成功。Job 提交成功时，同一摘要 ID 必须与 Worker 结果边界一致；不同摘要 ID
只有在其边界严格更新且已覆盖 Job 目标时，才能视为并发路径已经完成工作。

## 存储与恢复

- PostgreSQL：原始消息、摘要投影、压缩 Job、Prompt Manifest。
- Redis：Current Summary 热缓存；不可用时回退 PostgreSQL，不持有锁或发布权。
- 原文恢复 Tool 只读取当前摘要声明的来源，受 Actor/Conversation/Run、20 条、8K Token 和调用次数限制。
- 大附件、报告和 Tool Result 通过稳定句柄及有界预览进入 Prompt，不复制全文。

## 配置

模型窗口、输出预留、安全余量、Tokenizer 策略、软硬阈值、Summary/Tail 比例、压缩超时、
重试和摘要模型 Profile 均配置化。摘要 Provider Schema 为 `conversation_memory_v2`，Prompt 为
`conversation-memory-v9`。切换 Provider 需要 Adapter 与离线/小流量能力验收，不承诺运行时热切换。

## 验收

固定集必须在同一主模型、Tool Contract、输出预留和语料下对比 Baseline 与 Experiment，并报告：

- 主模型输入 Token 降幅；
- 包含摘要开销的端到端 Token；
- Baseline 超窗后的可继续率；
- 回答正确性、Fact/Decision/Correction/Todo/Reference 指标；
- 摘要成功率、软压缩用户阻塞率、重复摘要调用抑制率；
- P50/P95 延迟、Provider 成本、缓存命中、TokenEstimator P95 误差；
- 已发送 Prompt 超过硬窗口次数，必须为 0。

若端到端 Token 未下降，不得宣称“平均降低 60%+”；应改写为实测的主模型输入降幅、超窗恢复
或其他通过固定集验证的工程指标。远程评测必须默认单并发、单 Case、小批次、调用/Token/费用三重门禁。

## 已验证与待办

已验证：动态 Planner、Continuous Tail、Prompt Manifest、Summary 投影、Redis 降级、原文恢复、
软硬路径协调、本地 race 并发测试、Worker 单次调用边界、全仓测试。真实 PostgreSQL 双连接 advisory
lock 测试已加入 integration suite。

受控真实序列 `incident-correction` 中，cp2 主模型输入从 59,386 降至 12,434，下降约 79.1%；共同
成功的 cp1+cp2 主模型输入从 89,558 降至 42,606，下降约 52.4%。Baseline 的 cp3 预估输入为
132,621，超过 128K 并在调用前拒绝；Experiment 三个 checkpoint 均成功继续执行。三轮 Experiment
端到端总 Token 为 160,377，其中 Summary 为 101,534，不能支撑端到端平均降低 60%。

待办：可选地扩展到全量固定集；按实测更新简历；后续 migration 收敛 schema v1 兼容列；前端联调
放到整体功能闭环阶段。

## 已知工程约束

生产 Coordinator 在摘要远程调用期间持有 PostgreSQL session advisory lock，因此同时占用一个数据库
连接，等待同一 Conversation 的请求也会占连接。当前 Memory Worker `QoS=1` 且数据库池默认上限
100，现阶段可控。上线需观测锁等待、持锁时间、压缩并发和连接池等待；达到容量边界后优先限制压缩
并发或使用独立小连接池，不预先引入 Redis 锁、额外租约状态机或第二套协调基础设施。

## 非目标

- 跨 Conversation 长期记忆或用户画像；
- 全历史向量检索；
- 保存 Chain-of-Thought、完整 Prompt、凭证或完整 Tool Payload；
- Redis 分布式锁或事实源；
- Provider 私有 Compaction 作为正确性依赖；
- 为追求指标隐藏摘要 Token、降低质量或无限远程复测。
