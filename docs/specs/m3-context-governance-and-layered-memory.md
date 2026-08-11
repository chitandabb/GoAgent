# M3 动态上下文治理与分层记忆规格

> 状态：Ready for implementation
>
> 本规格是 MESGuard 简历第四点的实施与验收契约。其中“长会话平均 Token
> 消耗降低 60%+”仍是待固定集验证的目标，不是当前已实现事实。

## Problem Statement

MESGuard 已经拥有持久化 Conversation、异步 Turn 执行、Diagnosis Task、TaskScope、
Tool Catalog、Skill 渐进式装载和 Provider Usage 观测，但当前上下文选择仍使用
固定 Rune 上限和最近消息截取。该机制不知道实际模型窗口、Tool Schema、Skill、
输出预留和本轮 Tool Result 增长将消耗多少 Token，也不能在发送请求前证明
Prompt 一定不会超过模型硬窗口。

随着会话增长，系统只能丢弃早期消息，无法稳定保留用户事实、已确认决策、
后续修正、证据引用和未完成待办。现有算法还可能在某条大消息放不下时
跳过它、继续选择更早的小消息，破坏连续对话语义。这使得长会话的回答
连贯性、成本和可预测性都无法得到工程保证。

同时，Prompt Cache 依赖稳定前缀。如果每轮根据 Skill 选择在 Prompt 前部动态
插入或删除 Tool Schema，就会破坏已缓存前缀；但为缓存固定暴露未授权 Tool
又会破坏安全边界。系统需要将 TaskScope 授权、Skill SOP、Tool 暴露策略、
Prompt Epoch 和 Provider 缓存能力区分清楚，在授权正确的前提下尽量保持前缀
稳定。

最后，现有代码只在 Provider 返回 Usage 后结算总 Token，没有调用前 Token
Preflight、结构化 Summary Snapshot、PostgreSQL 事实源、Redis 热缓存、并发压缩
任务仲裁和可复现的长会话评测。因此简历第四点目前只是目标，还不能作为
已验证成果。

## Solution

MESGuard 将建立 Provider 无关的动态 Token 预算与分层会话记忆机制。每次模型
调用前，系统根据当前 Chat Model Profile 的硬窗口、最大输出、安全余量、System
Prompt、实际 Tool Schema、已装载 Skill、动态引用和本轮 Tool 增长预留计算
可用输入 Token，并用本地优先的 TokenEstimator 给出保守上界。估算误差使用
Provider 实际 Usage 持续校准；Prompt Cache 只影响成本和延迟，不会被错当成
可用窗口。

对 Conversation，系统使用“Active Summary Snapshot + Continuous Tail”重组模型上下文。
Summary 保留目标、用户事实、决策、修正、证据引用、未决问题、待办和任务/
报告引用；Tail 从当前消息向前按 Token 连续选取，不固定条数，不跳过中间
消息。Summary 与 Tail 总计不超过新一轮模型窗口的约 20%，默认分别不超过
5% 和 15%，同时服从剩余实际可用预算。

达到 70% 软阈值时，本轮完成后通过 Outbox 和 Worker 异步生成新 Snapshot；
达到 85% 硬阈值且没有可用 Snapshot 时，主模型调用前同步压缩。压缩使用
独立可配置的快速小模型，生成不可变、版本化、带源消息序号的结构化 Snapshot。
软压缩失败可重试并继续使用旧 Snapshot；硬压缩重试耗尽时向用户返回可重试
错误，绝不发送超窗 Prompt，也不用低质量确定性摘要静默丢失历史。

PostgreSQL 保存原始消息、不可变 Snapshot、压缩 Job 和 Prompt Manifest，是唯一
事实源。Redis 仅缓存 Active Snapshot 和最近 Tail 投影，不承担发布权和分布式
锁责任。Snapshot 激活通过 Lease、Fencing 和 CAS 确保过期 Worker 不能覆盖更新
结果。用户需要摘要中的细节时，模型可以通过受限的原文恢复 Tool，根据
Active Snapshot 已声明的条目和源消息序号读取当前 Conversation 原文。

Prompt 使用 Epoch 治理稳定前缀。当前默认策略是：TaskScope 在 Agent Run / Prompt
Epoch 开始时冻结授权 Tool，Skill 只提供 SOP 和使用建议，不在运行中扩大
Tool 权限，因此普通回合不改变 Tool Schema 前缀。System Prompt、Model Profile、
Tool Contract、预加载 Skill 或 Active Summary 变化时开启新 Epoch。通用 Provider
需要新增 Tool 时保留旧顺序并在末尾追加；撤权则安全优先，立即开启不含该 Tool
的新 Epoch。Provider 原生 Deferred Tool 和 Compaction 只是可选 Adapter 优化，不是
正确性依赖。

Diagnosis 与 Conversation 共用 TokenBudgetPlanner，但首版不为 Diagnosis 生成会话
Summary。它对 CaseSnapshot、Evidence、Tool Result 和报告输出分配预算，保存完整证据
而只向模型返回有界内容和稳定引用。如果后续实测显示诊断上下文经常超过阈值，
再引入由模型请求、宿主执行的 Diagnosis Compaction；不依赖“模型超窗后自己
调 Tool”这一不可达路径。

## User Stories

1. As a 业务用户, I want 在长会话中继续追问早期事实, so that 系统不会因为会话过长而忘记已确认信息。
2. As a 业务用户, I want 后续修正能够覆盖旧事实, so that 系统不会在后续回答中反复使用已被否定的信息。
3. As a 业务用户, I want 已做出的决策在压缩后仍然被保留, so that 后续讨论不会重新打开已经关闭的分歧。
4. As a 业务用户, I want 未完成待办的状态能被持续跟踪, so that 长会话不会遗漏后续行动。
5. As a 业务用户, I want 已完成或已取消的待办不再被当作开放任务, so that 回答状态与实际进度一致。
6. As a 业务用户, I want 摘要中的证据、任务和报告保留稳定引用, so that 模型可以在需要时追溯原始资料。
7. As a 业务用户, I want 会话压缩在大多数情况下无感进行, so that 我不会因后台整理上下文而中断工作。
8. As a 业务用户, I want 硬压缩失败时收到明确的可重试错误, so that 系统不会静默丢弃历史或产生不可预测回答。
9. As a 业务用户, I want 同一会话的记忆不会泄露到其他会话, so that 每个 Conversation 都保持独立的业务语境。
10. As a 业务用户, I want 对话在 Redis 不可用时仍能恢复记忆, so that 缓存故障不会造成事实丢失。
11. As a 业务用户, I want 长会话不会发送超过模型硬窗口的 Prompt, so that 我不会因为可预防的超窗错误丢失本轮工作。
12. As a 业务用户, I want 最近对话保持连续而不是抽样组合, so that 模型能够理解当前话题的完整转折。
13. As a 业务用户, I want 当前消息始终获得足够预算, so that 系统不会为了保留旧历史而损害正在进行的讨论。
14. As a 业务用户, I want 诊断任务结果以小型账本和按需报告读取接入会话, so that 报告全文不会每轮重复占用上下文。
15. As an 分析师, I want 在摘要丢失细节时读取已引用的原始消息, so that 我可以在不恢复整段历史的情况下核对关键语义。
16. As an 分析师, I want 原文恢复只允许当前 Active Snapshot 声明的来源, so that 模型不能把该 Tool 变成无界会话搜索。
17. As an 分析师, I want 附件、证据和 Tool Result 通过有界预览与句柄进入 Prompt, so that 大对象不会挤占当前对话预算。
18. As an 分析师, I want 会话目标、未决问题和诊断报告引用一起保留, so that 知识问答和诊断后续追问能共用同一对话入口。
19. As an 系统管理员, I want 为不同 Chat Model Profile 配置窗口、输出预留和安全余量, so that 切换 Provider 或模型时不需要改动业务实现。
20. As an 系统管理员, I want 独立配置快速小模型执行会话摘要, so that 压缩不会被绑定到主对话模型的成本和延迟。
21. As an 系统管理员, I want 配置软阈值、硬阈值、Summary/Tail 比例和重试策略, so that 我可以根据线上负载和模型特性调整治理策略。
22. As an 系统管理员, I want 选择本地精确、本地校准或保守估算 Tokenizer 策略, so that 线上 Preflight 不依赖远程 Tokenize 接口。
23. As an 系统管理员, I want 按 Profile 可选开启 Provider 原生 Compaction, so that 支持该能力的 Provider 可获得更好优化而不影响通用路径。
24. As an 系统管理员, I want 在发现质量问题时通过 Feature Flag 回退 Summary + Tail, so that 不需要删除原始消息或回滚派生数据。
25. As an 系统管理员, I want 只向管理和评测界面暴露有界 Prompt Manifest 元数据, so that 可观测性不会泄露完整 Prompt 和敏感内容。
26. As an 运维人员, I want 观测每轮估算 Token、上界、实际 Usage 和误差, so that 我可以发现某个 Provider 或模型的估算漂移。
27. As an 运维人员, I want 区分原始 Token 降幅、缓存命中、成本和延迟, so that 任何简历指标都不会把 Prompt Cache 节省冒充为摘要节省。
28. As an 运维人员, I want 观测 Prompt Epoch 更换和各稳定段 Fingerprint, so that 我可以定位是 Summary、Tool Contract、Skill、Prompt 还是 Model Profile 破坏了缓存前缀。
29. As an 运维人员, I want 观测压缩 Job 的重试、Lease、Fencing 和激活结果, so that 过期 Worker 和重复任务可被识别且不会污染 Active Snapshot。
30. As an 运维人员, I want Redis 故障时自动回退 PostgreSQL, so that 缓存不可用只造成性能下降而不造成功能错误。
31. As an 运维人员, I want 系统记录上下文高水位与超窗拦截, so that 可以用真实数据决定 Diagnosis 是否需要进一步压缩。
32. As an 安全负责人, I want 记忆组装不能扩大 TaskScope 已授权 Tool 集合, so that 压缩和缓存优化不会绕过 Tool 授权。
33. As an 安全负责人, I want Skill 装载只改变 SOP 而不直接授予 Tool 权限, so that 模型无法通过选择 Skill 自我扩权。
34. As an 安全负责人, I want Tool 撤权时优先创建不含该 Tool 的新 Prompt Epoch, so that 缓存命中不会凌驾于安全正确性。
35. As an 安全负责人, I want 原文恢复只访问当前用户有权的 Conversation, so that 序号、Entry ID 或 Cursor 不能用于跨会话读取。
36. As an 安全负责人, I want 原始 Prompt、模型推理、完整 Tool Payload 和凭证不被记忆模块持久化, so that 可观测和审计不会放大敏感数据面。
37. As a 开发者, I want Conversation 和 Diagnosis 共用同一个 TokenBudgetPlanner Interface, so that 窗口算法、安全余量和 Provider 差异只需维护一次。
38. As a 开发者, I want Conversation Memory 以一个深 Module 隐藏估算、组装、压缩、缓存和持久化细节, so that 调用方不需要理解整条记忆实现。
39. As a 开发者, I want Provider Adapter 封装 Tokenizer、Wire 序列化、Usage 映射和原生能力, so that 从 StepFun 切换到 DeepSeek 或其他 Provider 时上层治理语义保持不变。
40. As a 开发者, I want Prompt 使用 Provider 无关的语义分段, so that 不同 Provider 可以在不破坏上层规则的前提下选择合适的 Wire 字段顺序。
41. As a 开发者, I want Fingerprint 基于模型实际可见的规范化内容, so that 新增字段不需要靠人工猜测是否影响 Prompt Cache。
42. As a 开发者, I want Summary Snapshot 是不可变派生事实, so that 我可以审计、对比、回滚并重现某个 Prompt Epoch。
43. As a 开发者, I want 新 Snapshot 基于旧 Snapshot 和新增可压缩消息增量合并, so that 长会话不需要每次重新摘要全部历史。
44. As a 开发者, I want Summary 条目记录源消息序号和 supersede 关系, so that 修正、Todo 状态变更和原文追溯都有确定语义。
45. As a 开发者, I want 只有通过 Schema、来源、范围、引用和 Active Entry 校验的 Snapshot 才能激活, so that 模型输出格式错误不会污染运行时记忆。
46. As a 开发者, I want 压缩 Worker 重用现有 Outbox、Lease 和 Fencing 模式, so that 新能力与已验证的异步任务语义一致。
47. As a 开发者, I want 旧 Conversation 在首次接近阈值时才懒生成 Snapshot, so that 上线时不需要运行昂贵的全量回填。
48. As a 开发者, I want 删除 Conversation 时级联删除 Snapshot、Job 和 Manifest 并尽力清除缓存, so that 会话生命周期不会遗留无主派生数据。
49. As an 评测负责人, I want 在同一模型、Tool Contract 和输出预留下对比完整历史与 Summary + Tail, so that Token 降幅只反映上下文治理决策。
50. As an 评测负责人, I want Token 主指标同时包含主模型和摘要模型的 Input/Output, so that 不会通过隐藏压缩成本夸大降幅。
51. As an 评测负责人, I want 对事实、决策、修正、Todo、证据和回答正确性设置质量门, so that 系统不会用丢失关键语义换取更好看的 Token 数字。
52. As an 评测负责人, I want 使用独立可配置 LLM Judge 做离线质量判定, so that 生产运行时不增加 Judge 成本且评测与被测模型分离。
53. As an 评测负责人, I want 直接预置长历史而只在检查点调用模型, so that 固定集不需要为构造上百轮会话支付不必要的费用。
54. As an 评测负责人, I want 评测具有调用数、估算成本和并发度硬上限, so that 供应商限流和测试费用可被控制。
55. As an 评测负责人, I want 分别报告原始 Token、主 Prompt、Summary 开销、Cache Hit、成本和首 Token 延迟, so that 优化效果可以被解释而不是只有一个汇总数字。
56. As an 评测负责人, I want 将超过 Baseline 模型窗口的剧本单独统计可继续执行率, so that 不可比样本不会污染 Token 降幅主口径。

## Implementation Decisions

- 本规格只建立单 Conversation 隔离记忆。用户跨 Conversation 长期记忆属于独立隐私和授权问题，不与本阶段混合。
- TokenBudgetPlanner 是 Provider 无关的深 Module。它通过小 Interface 接收已规范化的 Prompt 分段、Model Profile 和运行预留，返回可用输入、估算值、保守上界、阈值状态和不可超过的硬上限。它不读数据库、不调用模型、不决定 Tool 授权。
- ConversationMemory 是 Conversation Runner 使用的主深 Module。其 Interface 对调用方只暴露“为本轮准备可执行上下文”和必要的错误/观测结果，将 Snapshot 选择、Tail 组装、软/硬压缩、校验、缓存和持久化细节隐藏在 Implementation 中。
- ConversationMemory 的 Implementation 内部可以包含 TokenEstimator、MemoryCompactor、Snapshot Validator、Repository 和 Cache Adapter 等内部 Seam，但这些不成为 Conversation Runner 的公开 Interface，以保持 Depth、Leverage 和 Locality。
- DiagnosisContextAssembler 复用 TokenBudgetPlanner，但不复用 Conversation Summary。它管理 CaseSnapshot、Evidence Index、有界 Tool Result、主模型输入和报告输出预留。
- 每个 Chat Model Profile 需要配置 Context Window、Max Output Tokens、绝对和比例安全余量、Tokenizer Strategy 以及可选 Provider 原生能力。现有模型配置是唯一来源，不重复建立第二套 Profile。
- 可用输入 Token 等于硬窗口减去最大输出预留和 Safety Margin。Safety Margin 取绝对 Token 与窗口比例中的较大值。
- Prompt 估算必须包含 Stable System Prompt、Canonical Tool Schema、Preloaded Entry Skill、Active Summary、Continuous Tail、Dynamic References、Current User Message 和 Current Run Tool Growth Reserve。
- TokenEstimator 优先使用 Provider/模型对应的本地精确 Tokenizer；无精确实现时使用模型族 Tokenizer 与 Chat Template/Tool Schema 补偿；再不可用时使用经校准的保守字符估算。远程 Tokenize 仅用于可选离线校准，不是线上依赖。
- Provider 每次返回的实际 Usage 用于计算估算误差，按 Provider + Model Profile 统计 P95/P99 低估分布并校准保守上界。缓存命中不降低 Prompt 的窗口 Token 数，只单独进入成本与延迟观测。
- 默认软阈值为可用输入的 70%，硬阈值为 85%。软阈值在当前回合完成后创建异步压缩 Job；硬阈值在下次主模型调用前同步保证有可用 Snapshot。
- Memory 预算默认不超过新模型窗口的 20%；Summary 默认不超过 5%，Continuous Tail 默认不超过 15%。当其他 Prompt 分段占用更多预算时，Memory 必须继续收缩而不是挤占输出或 Safety Margin。
- Current User Message 在语义分段中仍保持为独立末尾消息，但在 Memory 预算中计入 Tail 上限。Tail 从当前消息向前按 Token 连续选取，没有固定“最近 N 轮”保留值。
- 只压缩已完成历史。当前 User Message、本轮活跃 Tool Call/Result、当前安全政策和必须执行的输出契约不得被 Summary 替代。
- 大附件、Tool Result、Evidence 和 Diagnosis Report 不反复复制进历史。模型只获得有界内容、稳定 ID、哈希、一句话结论和按需读取句柄。
- Summary Snapshot 是不可变、版本化派生事实，不覆盖或删除原始 Message。它记录覆盖范围、前驱 Snapshot、Schema 版本、摘要模型 Profile、Prompt 版本、Payload 哈希、Token Usage、状态和激活时间。
- Snapshot Payload 使用固定结构：conversation goal、user-stated facts、decisions、corrections、evidence references、open questions、todos、task references 和 report references。模型推测不得进入用户事实。
- 每个 Summary Entry 都具有稳定 Entry ID、内容、源消息序号、状态和可选 supersedes 关系。Correction 显式替代旧事实；Todo 只允许 open、completed 和 cancelled 状态。
- 新 Snapshot 默认基于上一个 Active Snapshot 和新增可压缩消息增量合并。摘要丢失无法完全消除，首版通过结构化条目、来源序号、修正语义、质量门和原文恢复降低损失。
- Snapshot 必须在严格 JSON Schema、源消息序号、覆盖范围、稳定引用、supersede 关系和 Active Entry 唯一性校验通过后才可激活。生产校验是确定性的，不调用 LLM Judge。
- 摘要模型使用独立的 Conversation Memory Profile，默认快速小模型，Provider、Model、Context Window、Output Token、Prompt Version 和重试均可配置。
- 软压缩失败默认有界重试三次，使用指数退避和 jitter，且不影响本轮已完成回答。硬压缩在请求截止时间内重试，重试耗尽返回 context preparation failed 类型的可重试错误。
- 首版不实现确定性摘要降级。失败时保留上一份已验证 Snapshot；如果旧 Snapshot + Tail 仍无法安全调用，就向用户返回可重试错误，不静默丢历史。
- 原文恢复 Tool 是稳定高层 Interface，只允许读取当前 Active Snapshot 声明的 Entry 或源消息序号。默认每次最多 20 条消息、8K Token，每轮最多调用两次，超限使用 Continuation Cursor。
- 原文恢复必须经过当前 Conversation 所有权校验、Snapshot 来源校验、条数/Token/调用次数限制和 Tool Trace 记录。
- Prompt 使用 Provider 无关的语义顺序：Stable System、Canonical Tool Schema、Preloaded Entry Skill、Active Summary、Continuous Tail、Dynamic References、Current User Message、Current Run Tool Call/Result。Provider Adapter 决定具体 Wire 映射。
- 普通回合只在末尾追加新消息和本轮 Tool Call/Result。System Prompt、Model Profile、Canonical Tool Schema、Preloaded Skill 或 Active Summary 变化时开启新 Prompt Epoch，不把新 Summary 继续追加在旧 Summary 后造成冲突记忆。
- Fingerprint 基于模型实际看到的规范化稳定内容，至少覆盖 Model Profile、System Prompt、Tool Schema、Skill Prompt 和 Summary Snapshot。Fingerprint 只用于 Epoch 判定和观测，不参与授权，不发送给模型。
- TaskScope 在 Agent Run / Prompt Epoch 开始前冻结已授权 Tool Contract。Skill 只能建议如何使用已授权 Tool，不能在当前 Epoch 中新授权 Tool，从而同时维持简历第一点的最小白名单语义和普通回合的 Prompt Cache 稳定。
- Provider Tool Exposure Strategy 支持 static frozen、native deferred、epoch rebind 和 gateway 四种能力标记。默认通用路径是 static frozen；Provider 明确支持原生延迟暴露时才使用 native deferred。
- 通用 Provider 在运行中必须新增 Tool 时通过 epoch rebind 建立新 Epoch，保留旧 Tool Schema 的规范顺序，只把新 Tool 追加到末尾。撤销 Tool 权限时不保留有风险的旧前缀，立即切换到新授权集合。
- 动态加载的 Skill 全文不跨回合持久化。只保存 Skill ID、Version/Hash 和执行时间；新回合确实需要时再加载当前版本。
- Provider 原生 Prompt Cache、Deferred Tool 和 Compaction 是 Adapter 层可选优化，不是上层正确性依赖。Provider 切换可复用 Provider 无关的结构化 Snapshot，但会开启新 Prompt Epoch。
- PostgreSQL 是 Message、Snapshot、Memory Job 和 Prompt Manifest 的事实源。Snapshot 和 Manifest 不保存原始 Prompt、原始推理、完整 Tool Payload、凭证或 MinIO 内部坐标。
- 需要持久化不可变 Snapshot、压缩 Job 和 Prompt Manifest 元数据。Snapshot 使用关系列表达范围、版本、状态和引用，使用 JSON 载荷保存结构化条目。
- 软阈值压缩通过 Memory Job + Outbox 交给 Worker；硬阈值路径调用同一 MemoryCompactor Implementation，避免异步和同步路径产生两套摘要语义。
- Active Snapshot 只能通过 CAS 激活：压缩时使用的 Base Snapshot 仍是当前 Active、新覆盖序号更大，且 Worker Lease/Fencing 仍有效。过期 Worker 产生的 Snapshot 可保留审计，但不能激活。
- Redis 只是 Active Snapshot、Prompt Manifest 和最近 Tail 投影的可降级 Cache Adapter。默认 TTL 为两小时并加 10% jitter；缓存 Key 包含 Conversation 和 Snapshot ID；失败时读 PostgreSQL。
- 删除 Conversation 时 PostgreSQL 级联删除 Memory 派生事实，Redis 执行尽力清理，清理失败依靠 TTL 最终过期。
- Prompt Manifest 只记录 Epoch ID、各稳定段 Fingerprint、Summary Snapshot ID、Tail 序号范围、估算/实际/Cache Token、误差、延迟和降级状态等元数据。
- Diagnosis 首版只实现调用前 Preflight、有界 Tool Result、Evidence 句柄和上下文高水位观测，不建立 Diagnosis 多轮记忆。
- 如果固定集显示 Diagnosis 经常超过 70% 水位，后续可增加由模型请求、宿主校验并执行的 Diagnosis Compaction。宿主仍必须在调用模型前拦截超窗 Prompt。
- Provider-native Compaction 按 Model Profile 可选开启。如果原生压缩无法输出 MESGuard 要求的结构、来源和审计元数据，它只能作为运行时优化，不能替代持久化 Snapshot。
- 上线按四阶段进行：只观测 Estimate/Manifest；生成 Snapshot 但不用于回答；通过 Pilot 后仅对测试账号启用；通过 Acceptance 后全量启用。

## Testing Decisions

- 好测试只观察 Module Interface 外部可见行为，不断言内部函数调用顺序、私有数据结构或某个具体 Adapter 的实现步骤。输入是 Conversation、Message、TaskScope、Model Profile 和 Provider 行为；可观察输出是回答/错误、模型可见上下文、Prompt Manifest、Snapshot 状态和持久化副作用。
- 主测试 Seam 是现有 Conversation Turn 执行 Interface。测试通过 Conversation Runner/AgentResponder 执行完整回合，注入可控 Model、TokenEstimator、Clock 和必要 Adapter，验证预算、Summary + Tail、连续序号、软/硬触发、可重试错误、原文恢复授权、引用与硬窗口保护。
- 主 Seam 应覆盖短会话不压缩、软阈值只排队、硬阈值同步压缩、使用旧 Snapshot、压缩重试成功、重试耗尽、大消息不跳过、当前消息保留、Tool Schema 改变换 Epoch 和 Provider 切换换 Epoch。
- 第二个必要 Seam 是 PostgreSQL Memory Repository/Worker Interface，使用真实 PostgreSQL 验证事务语义，不用内存 Fake 伪装数据库并发。覆盖 Job Claim、Lease 续期、Fencing、幂等重投、Snapshot 不可变写入、CAS 激活、过期 Worker 不能覆盖、Conversation 删除级联和 Active Snapshot 唯一性。
- Redis 作为可降级 Cache Adapter 做合同测试：命中与 PostgreSQL 返回相同 Snapshot；超时、异常、旧 Snapshot 和删除失败都不破坏事实正确性；TTL 和 jitter 保持在配置边界内。
- TokenEstimator Adapter 做合同测试，用各 Model Profile 的固定 Prompt Fixture 验证规范化分段、Tool Schema 序列化、Upper Bound 和 Usage 校准。不要只测纯文本，必须包含 Tool Schema、中英混合文本、长 ID、JSON 和 Tool Result。
- Provider Adapter 做窄合同测试，验证语义 Prompt 分段到 Wire 请求的映射、Usage/Cache Token 回填、Tool Schema 顺序和 Capability Flag。Provider 私有特性不泄露到 ConversationMemory Interface。
- Snapshot Validator 的确定性规则使用表驱动边界测试，覆盖非法 Schema、越界序号、未知引用、自循环 supersede、多个 Active Entry、非法 Todo 状态、超大 Payload 和模型推测误入事实字段的可检测格式。
- 原文恢复 Tool 通过 Tool Interface 测试当前 Conversation 授权、Entry 来源、消息范围、20 条/8K Token 上限、两次每轮限制、Continuation Cursor、Trace 和跨会话拒绝。
- Diagnosis 通过现有诊断执行高 Seam 测试共享 Planner、有界 Tool Result、Evidence 句柄、报告输出预留和硬窗口拦截，不把内部每个 Tool 的截断辅助函数当作主测试面。
- 先行只观测阶段必须对比新估算与现有实际 Usage，但不改变发送给模型的 Prompt。该阶段验收估算数据完整性、P95 低估误差和零行为泄漏。
- Pilot 固定集使用 4 个会话剧本，每个 3 个检查点；Acceptance 使用 12 个会话剧本，每个 3 个检查点。历史消息直接预置，仅在检查点调用 Summary、主模型和可选 Judge。
- 评测包含 Current、Baseline 和 Experiment 三组。Current 是现有 Rune 算法，只用于证明替换必要性；Baseline 使用同一模型、Tool Contract 和输出预留的完整连续历史，不做 Summary；Experiment 使用 Summary + Continuous Tail。
- 质量门默认为 Fact Recall 不低于 95%、Decision Recall 不低于 95%、Correction Accuracy 等于 100%、Todo State Accuracy 不低于 95%、Evidence Reference Recall 不低于 95%、Hallucination Rate 不高于 2%，且 Answer Correctness 相对 Baseline 下降不超过 2 个百分点。
- 工程门默认为端到端 Raw Token Reduction 目标不低于 60%、TokenEstimator P95 低估误差不超过 5%、超过模型硬窗口的 Prompt 数为零。
- Raw Token Reduction 主口径统计 Experiment 相对 Baseline 的全部模型 Input/Output Token，包含 Summary Model 开销。另行报告 Main Prompt Reduction、Summary Overhead、Cache Hit Ratio、Cache-adjusted Cost、First Token Latency 和 Prompt Epoch Churn。
- 固定集默认调用上限 200、估算成本上限 10 元人民币、并发度 1。预估超过成本或调用上限时在调用 Provider 前停止。
- 运行时 Snapshot 校验不使用 LLM Judge。独立、可配置 Judge 只用于离线固定集，评估 Faithfulness、语义保留和答案正确性。
- 现有 Conversation Runner、Service ExecuteTurn、Async Repository、Worker Lease/Fencing 和诊断 Runner 测试是先例。新测试应沿用这些高 Seam 和真实数据库集成模式，不建立大量与实现绑定的浅层 Mock。

## Out of Scope

- 跨 Conversation 的用户长期记忆、用户偏好画像和跨会话个人化。
- 完整会话历史的关键词或语义检索；首版只根据 Active Snapshot 条目和源消息序号恢复原文。
- 分层摘要、信息熏驱动压缩、双模型交叉验证、摘要漂移监测和自适应 Summary/Tail 比例。
- Provider 私有服务端会话作为唯一事实源，以及对某一 Provider 的强绑定。
- 首版为所有 Provider 实现原生 Deferred Tool、Prompt Cache 或 Compaction 深度适配。
- Tool 数量达到几十或上百后的 Tool Gateway、语义 Tool Retrieval 和运行中大规模 Tool Schema 分页。
- 首版 Diagnosis 压缩 Snapshot 或由模型调用的 Diagnosis Compaction Tool/Skill；本阶段只做 Planner、有界 Tool Result 和高水位观测。
- 将原始 Prompt、原始 Chain-of-Thought、完整 Tool Payload、凭证或 MinIO 内部坐标持久化。
- 用确定性文本抽取在摘要模型失败后生成低质量替代 Summary。
- 把 Redis 作为 Snapshot 事实源、发布仲裁者或分布式锁。
- 本阶段的前端交互改造、压缩管理页和普通用户可见的技术元数据。
- 为现有所有 Conversation 做上线前全量 Snapshot 回填；旧会话采用阈值触发的懒生成。
- 在没有固定集实测的情况下把 60%+ 写成已完成事实。

## Further Notes

- 该规格从《Dynamic Context Governance and Layered Conversation Memory》目标设计中收敛而来，Roadmap 的 M3 顺序以本规格为实施依据。
- 简历第一点的可支撑口径是：场景路由与渐进式 Skill 装载用于缩小推理范围，不参与授权；TaskScope 在 Run 开始前生成最小 Tool 白名单并在当前 Epoch 内冻结。本规格没有改写该评测变量。
- 长会话摘要一定存在信息损失。首版的工程目标不是宣称“无损”，而是通过结构化语义、来源绑定、修正优先、原文恢复、固定集质量门和可回滚发布将损失变成可观测、可拦截、可追溯的风险。
- 摘要的 5%、Tail 的 15% 和总 Memory 的 20% 是首版默认值，不是行业不变常数。它们必须保持可配置，但调整后仍需通过相同质量门。
- 指标转写规则：只有 Acceptance 固定集通过后，才能将实测 Raw Token Reduction 写入简历。如果实测不是 60%+，必须替换为实际数值或改写为不带未验证数字的工程口径。
- 本地规格已准备进入实现拆票；由于当前代码仓库未开启 GitHub Issues，本次不创建远程 Issue，也不自动修改仓库设置。
