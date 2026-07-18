# MESGuard 项目优化与交付路线图

> 更新日期：2026-07-15
> 目标完成日期：2026-08-15
> 预计投入：约 60 小时
> 状态说明：`[ ]` 未开始、`[~]` 进行中、`[x]` 已完成、`[!]` 阻塞

## 1. 项目定位

MESGuard 是一个面向制造业 MES 软件售后与质量人员的工单智能诊断 Agent。项目来源于真实 MES 售后排障经验，但使用公开资料、合成工单、合成日志和模拟业务数据库独立实现，不包含实习公司的代码、客户数据及内部文档。

项目需要完成以下业务闭环：

```text
客户反馈工单
  -> 提取产品版本、业务对象、故障现象和缺失信息
  -> 检索产品文档、排障手册与历史解决案例
  -> 生成诊断计划并调用受控工具收集证据
  -> 分析 SQL Server 业务数据、执行计划、慢查询和本地日志
  -> 输出根因、置信度、证据链、修复建议和诊断报告
  -> 人工确认后沉淀为可复用案例
```

### 目标用户

- 主要用户：MES 实施、售后与运维人员。
- 次要用户：质量部门，用于聚合已确认案例、识别重复缺陷和知识缺口。

### 核心故障类型

1. 生产工单无法开工、报工或完工。
2. 设备数据采集中断或数据异常。
3. 物料批次与生产追溯链不完整。
4. 质量检验结果异常或记录缺失。
5. MES 报表、接口任务或 SQL 查询缓慢。
6. MES 与 ERP 等外围系统接口同步失败。

## 2. 范围控制

### 本期必须完成

- 单 Agent 多步诊断循环。
- Run、Turn、Step、ToolCall 和 Event 状态模型。
- SQL Server 只读查询、执行计划和慢查询诊断工具。
- 产品文档与历史工单的混合检索 RAG。
- Token 预算、结构化摘要和分层记忆。
- Agent Trace、离线评测和真实指标记录。
- Vue 3 + Tauri 精简诊断工作台。

### 本期明确不做

- 多 Agent 协作与 Agent Handoff。
- OCR、VLM、扫描件和复杂图表解析。
- 模型训练、微调或本地大模型部署。
- SQL Server 之外的业务数据库适配。
- 自动执行 `UPDATE`、`DELETE`、`MERGE`、DDL 或存储过程。
- 复杂质量分析大屏和低代码平台。
- 面向生产环境的多租户商业化交付。

### 后续扩展点

- 扫描件 OCR、表格结构恢复与图表语义理解。
- MySQL、PostgreSQL、Oracle 等 `DatabaseAdapter`。
- 多 Agent 专家协作与质量分析 Agent。
- 私有化模型、离线 Embedding 与本地 Reranker。
- 工单系统 Webhook、企业 IM 和真实监控平台连接器。

## 3. 目标架构

```text
Tauri + Vue 3
  |- 工单诊断
  |- Agent 执行时间线
  |- 工具审批
  `- 诊断报告

Go + Gin + Eino
  |- Agent Runtime
  |  |- ReAct Loop
  |  |- 状态机与检查点
  |  |- 超时、重试与取消
  |  `- SSE 结构化事件
  |- Tool Registry
  |  |- 工单查询
  |  |- SQL Server Schema
  |  |- Text-to-SQL
  |  |- SHOWPLAN_XML
  |  |- Query Store / DMV
  |  |- 本地日志分析
  |  `- RAG 检索
  |- Context Manager
  |- Memory Manager
  `- Evaluation & Trace

SQL Server
  |- SUPPORT_DEMO：客户、产品版本、工单和沟通记录
  `- MES_DEMO：工单、工序、设备、报工、质量和接口任务

PostgreSQL + pgvector
  |- 文档、分块和向量
  |- Agent Run / Step / ToolCall
  |- 已确认案例与长期记忆
  `- 评测集和评测结果

Redis
  |- 热会话和短期状态
  |- 语义缓存
  `- 限流与临时锁
```

## 4. 模型与配置治理

Chat、Embedding 和 Rerank 必须作为三个独立能力配置，不共享 Base URL、API Key、超时或重试策略。密钥只通过环境变量注入，不写入 TOML、数据库或日志。

```toml
[models.chat]
provider = "stepfun"
baseUrl = ""
apiKeyEnv = "CHAT_API_KEY"
model = "step-3.7-flash" # 实现时以供应商官方模型 ID 为准
timeoutSeconds = 60
maxRetries = 2

[models.embedding]
provider = "dashscope"
baseUrl = ""
apiKeyEnv = "EMBEDDING_API_KEY"
model = "text-embedding-v4"
timeoutSeconds = 30
maxRetries = 2

[models.rerank]
provider = "dashscope"
baseUrl = ""
apiKeyEnv = "RERANK_API_KEY"
model = "" # 接入时根据账号可用模型填写
timeoutSeconds = 15
maxRetries = 1
```

需要定义统一接口：

- `ChatProvider`
- `EmbeddingProvider`
- `RerankProvider`
- `ModelConfigValidator`

Rerank 不要求 Eino 提供特定供应商实现，可以通过 Go HTTP Adapter 封装云端 API，并作为 Eino Graph 的 Lambda Node 接入。

## 5. Agent Runtime 设计

### 状态模型

- `AgentRun`：一次完整工单诊断任务。
- `AgentTurn`：一次用户输入或用户补充信息。
- `AgentStep`：模型推理、工具调用、审批或总结步骤。
- `ToolCall`：结构化保存工具名、参数、结果、耗时和状态。
- `AgentEvent`：向前端推送的统一事件。
- `ApprovalRequest`：高风险工具的审批请求和决策记录。
- `Artifact`：诊断报告、SQL 草案和导出文件。

### 停止条件

- 模型返回最终诊断结果。
- 达到最大步骤数或 Token/费用预算。
- 用户取消任务。
- 工具连续失败超过阈值。
- 缺少必要信息，需要用户补充。
- 工具进入等待审批状态。

### 工具风险等级

| 等级 | 示例 | 默认策略 |
|---|---|---|
| L0 | 产品文档、历史案例检索 | 自动执行 |
| L1 | Schema、索引和配置读取 | 自动执行并审计 |
| L2 | AST 校验后的只读 SQL、执行计划 | 首次确认或按会话授权 |
| L3 | 索引或数据修复 SQL 草案 | 只生成，不执行 |
| L4 | 写 SQL、DDL、危险存储过程 | 不向 Agent 注册 |

## 6. SQL Server 诊断工具链

### 计划工具

- `get_ticket`
- `describe_schema`
- `search_table`
- `validate_tsql`
- `estimate_query_plan`
- `execute_readonly_query`
- `find_slow_queries`
- `inspect_query_store`
- `inspect_index_usage`
- `inspect_wait_stats`
- `search_application_logs`

### 安全防线

1. 使用不同权限的连接池隔离业务查询、执行计划和诊断 DMV。
2. 业务查询账号只授予指定表或视图的 `SELECT`。
3. 执行计划账号按需授予 `SHOWPLAN`。
4. DMV 权限按需使用 `VIEW DATABASE STATE`；服务器级权限默认关闭。
5. 只允许单条语句，限制结果行数、执行时间和返回字节数。
6. 通过 T-SQL 静态检查拒绝写操作、DDL、危险函数和跨库访问。
7. AST 检查不是最终安全边界，数据库最小权限账号才是最终防线。
8. SQL、参数、审批人、耗时和结果摘要全部进入审计轨迹。

## 7. RAG 与知识治理

### 数据来源

- 文本型 PDF、DOCX、HTML 和 Markdown 产品文档。
- MES 排障手册与版本变更记录。
- 已人工确认的历史工单与解决方案。
- 未解决工单只作为诊断任务，不直接进入知识库。

### 入库管道

```text
加载
  -> 格式解析
  -> 内容哈希与去重
  -> 文档版本识别
  -> 标题层级感知切分
  -> Metadata 与 ACL 补充
  -> 云端 Embedding
  -> pgvector + PostgreSQL FTS 入库
```

### 检索管道

```text
问题规范化 / Query 改写
  -> 向量召回与 FTS 并行召回
  -> Metadata 权限和产品版本过滤
  -> RRF 融合
  -> 云端 Rerank
  -> 上下文组装与引用编号
  -> 回答及引用校验
```

### 案例知识结构

已解决工单需要清洗为结构化案例，不能直接整条向量化：

```json
{
  "symptom": "月末生产报表加载超过一分钟",
  "environment": "MES 3.2 / SQL Server 2022",
  "rootCause": "缺少复合索引导致大范围扫描",
  "evidence": ["Query Store", "SHOWPLAN_XML", "logical reads"],
  "resolution": "增加索引并更新统计信息",
  "applicableVersions": ["3.1", "3.2"]
}
```

## 8. 上下文与记忆治理

### 上下文组成

1. System Prompt 与工具规范。
2. 工单事实和当前 MES 环境。
3. 结构化工作记忆。
4. RAG 检索证据。
5. 最近若干轮原始对话。
6. 当前步骤需要的工具结果。

### 结构化工作记忆

- 已确认事实。
- 当前诊断假设及置信度。
- 已收集证据与来源。
- 已排除原因。
- 待执行步骤。
- 用户约束和审批状态。

### 压缩策略

- 每次调用前计算 Token 预算。
- 超过阈值时异步压缩早期消息。
- 使用“结构化摘要 + 尾部滑动窗口”重组上下文。
- 原始消息仍持久化，摘要失败时可以恢复。
- 未经人工确认的模型推断不得写入长期案例记忆。

## 9. 评测体系

### 数据集规划

- [ ] 20-30 篇产品文档和排障手册。
- [ ] 30-50 条已解决历史工单，用作案例知识库。
- [ ] 30 条独立评测工单，不进入 RAG 索引。
- [ ] 每条评测工单标注根因、必要证据、预期工具和禁止动作。
- [ ] 构造正常 SQL、慢 SQL、危险 SQL 和越权 SQL 测试集。

### RAG 指标

- Recall@K
- MRR
- nDCG@K
- Context Precision
- Citation Accuracy

### Agent 指标

- 根因诊断准确率。
- 必要证据召回率。
- 工具选择与参数准确率。
- 危险 SQL 拦截率。
- 平均诊断步骤数。
- 任务完成率和失败原因分布。
- TTFT、端到端 P50/P95 延迟和 Token 成本。

### 指标使用原则

- 先记录无优化 Baseline，再进行优化和对照实验。
- LLM-as-Judge 只能辅助评价完整性，不能作为唯一标准。
- 简历只使用最终实测结果，不使用本文件中的规划值。
- 保存评测配置、模型版本、数据集版本和随机参数，保证可复现。

## 10. 四周排期

### 第一周：2026-07-16 至 2026-07-22

目标：完成 Agent Runtime 骨架和合成 MES 数据基础。

- [ ] 重构 Chat、Embedding、Rerank 独立配置。
- [ ] 设计并迁移 Agent Run、Step、ToolCall、Event 表。
- [ ] 实现单 Agent 多步循环和停止条件。
- [ ] 实现结构化 SSE 事件协议。
- [ ] 支持任务取消、最大步骤数和超时。
- [ ] 构造 SQL Server `SUPPORT_DEMO` 与 `MES_DEMO` Schema。
- [ ] 生成第一批工单、业务数据和异常场景。

验收标准：输入一条工单后，Agent 能完成至少两次工具调用，并将完整执行轨迹持久化和推送到前端。

### 第二周：2026-07-23 至 2026-07-29

目标：完成安全 SQL Server 诊断闭环。

- [ ] 实现 Tool Registry 与风险等级。
- [ ] 实现工单、Schema、只读 SQL 和本地日志工具。
- [ ] 实现 T-SQL 静态校验、行数限制和查询超时。
- [ ] 接入 `SHOWPLAN_XML`、Query Store 和核心 DMV。
- [ ] 实现工具审批、连接池权限隔离和审计记录。
- [ ] 打通至少三类 MES 故障诊断案例。
- [ ] 为工具与 SQL 安全边界补充单元测试和集成测试。

验收标准：Agent 能诊断一条慢查询工单，给出执行计划证据和优化草案，同时拒绝所有写 SQL 测试样例。

### 第三周：2026-07-30 至 2026-08-05

目标：完成 RAG、上下文和记忆治理。

- [ ] 实现文本型 PDF、DOCX、HTML、Markdown 解析。
- [ ] 实现内容去重、版本管理和标题感知切分。
- [ ] 完成 pgvector 与 PostgreSQL FTS 双路索引。
- [ ] 实现并行召回、RRF、Rerank 和引用组装。
- [ ] 实现 Token 预算和结构化摘要。
- [ ] 实现工作记忆、会话记忆和确认案例分层。
- [ ] 建立第一版 RAG 与 Agent 评测集。

验收标准：评测程序可以自动运行并输出 RAG 指标；Agent 能引用产品文档和历史工单完成一次多证据诊断。

### 第四周：2026-08-06 至 2026-08-12

目标：完成工作台、评测、文档和演示。

- [ ] 使用 Tauri 封装现有 Vue 3 前端。
- [ ] 完成工单输入、工具时间线、审批和报告页面。
- [ ] 实现用户授权后的本地日志与诊断包读取。
- [ ] 完成 Agent Trace 和评测结果展示。
- [ ] 运行 Baseline、优化实验和安全测试。
- [ ] 记录真实指标、环境与复现命令。
- [ ] 补充架构图、演示脚本和 README。

验收标准：在一台开发机上可以从合成工单开始，完整演示诊断、审批、证据收集、报告生成和案例确认流程。

### 缓冲期：2026-08-13 至 2026-08-15

- [ ] 修复阻塞性缺陷。
- [ ] 完成构建、容器和启动脚本检查。
- [ ] 录制演示视频或 GIF。
- [ ] 根据实测数据更新简历项目描述。
- [ ] 准备项目相关面试问题与答案。

## 11. 每周时间分配建议

| 工作项 | 每周建议投入 |
|---|---:|
| MESGuard | 14-16 小时 |
| IM 聊天室项目 | 8-10 小时 |
| 算法题 | 7 小时 |
| 八股与项目复盘 | 5-7 小时 |

MESGuard 优先保证完整闭环，不以代码量或功能数量作为进度标准。

## 12. 风险与降级策略

| 风险 | 触发信号 | 降级方案 |
|---|---|---|
| SQL Server 诊断权限复杂 | DMV 或计划查询持续受阻 | 先使用预置诊断视图和合成计划数据 |
| Rerank API 不稳定 | 超时率或错误率过高 | 跳过 Rerank，直接使用 RRF TopK |
| 文档解析范围过大 | 多格式解析占用超过两天 | 先保留 Markdown、HTML 和文本 PDF |
| Tauri 集成耗时 | IPC 或打包问题超过一天 | 使用 Vue Web 完成演示，Tauri 延后 |
| 评测数据不足 | 第三周仍不足 20 条 | 减少故障类别，保证标注质量 |
| Agent 循环不稳定 | 步骤失控或重复调用 | 限制最大步骤并增加确定性状态节点 |
| 云端模型延迟波动 | P95 严重影响演示 | 增加超时、重试、缓存和降级模型 |

## 13. 完成定义

只有同时满足以下条件，项目才算完成：

- [ ] 至少三类 MES 故障可以端到端诊断。
- [ ] Agent 支持多步工具调用、取消、超时和状态持久化。
- [ ] 数据库工具严格只读，危险 SQL 测试全部被拒绝。
- [ ] RAG 支持混合召回、重排和引用溯源。
- [ ] 上下文压缩后能够从原始消息恢复。
- [ ] 长期案例必须经过人工确认后写入。
- [ ] 离线评测能够一条命令运行并生成结果。
- [ ] Tauri 或 Web 工作台能够完成完整演示。
- [ ] README 包含架构、启动、演示、评测和安全边界。
- [ ] 简历中的每个指标都能定位到评测数据和运行记录。

## 14. 目标简历亮点

以下是实现方向，不是当前已完成事实。最终必须根据真实代码和实测结果改写。

1. 工单驱动的 Agent Runtime 与可恢复状态机。
2. SQL Server Text-to-SQL 与慢查询安全诊断工具链。
3. 多格式知识治理与 pgvector + FTS 混合检索 RAG。
4. 动态 Token 预算、结构化摘要与分层记忆。
5. Tauri 本地诊断工作台、Agent Trace 与离线评测。

## 15. 进度日志

每次完成可验收结果后更新，不记录“学习了某技术”一类无法验证的进度。

| 日期 | 阶段 | 状态 | 完成结果 | 验证方式 | 阻塞项 | 下一步 |
|---|---|---|---|---|---|---|
| 2026-07-15 | 项目规划 | 已完成 | 确定 MES 工单诊断场景、技术边界和四周排期 | 路线图评审 | 无 | Agent Runtime 设计 |
| 2026-07-18 | 基础设施与依赖 | 已完成 | PostgreSQL/Redis/SQL Server 开发环境就绪；旧持久化链路迁移至 PostgreSQL；Eino 升级至 v0.9.12 | `go test ./...`、PostgreSQL 集成测试、Compose 健康检查 | 无 | Agent Run/Step/ToolCall/Event 最小状态模型 |

## 16. 决策记录

| 日期 | 决策 | 原因 |
|---|---|---|
| 2026-07-15 | 使用单 Agent，不做多 Agent | 控制复杂度，优先完成可评测闭环 |
| 2026-07-15 | 被诊断业务库使用 SQL Server | 更符合制造业 MES 场景和实习经验 |
| 2026-07-15 | Agent 系统库使用 PostgreSQL + pgvector | 统一存储状态、知识、记忆与评测数据 |
| 2026-07-15 | 使用云端模型，移除 Python Sidecar | 聚焦 Go + Eino，降低部署和维护复杂度 |
| 2026-07-15 | 不执行任何写 SQL | 保证诊断工具安全边界和可审计性 |
| 2026-07-15 | OCR/VLM 延后 | 文本知识库足以支撑本期核心闭环 |
| 2026-07-15 | 项目按个人脱敏复现定位 | 遵守保密要求，区分实习经历与独立实现 |
