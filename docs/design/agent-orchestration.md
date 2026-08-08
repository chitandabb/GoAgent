# Agent 编排与工具治理设计

## 文档状态

- P0-P4 已完成：当前 Runner 每次调用创建独立的 Eino ADK `ChatModelAgent`，使用不可变 `TaskScope`、统一 `ToolCatalog`、`BeforeAgent` 运行时授权、原生 `SKILL.md` 和按需 reference Tool。
- `ticket-diagnosis` 可以在同一 Agent 循环内加载 `code-investigation` 并继续调用 GitHub Tool；旧的每 Skill Executor、Graph Dispatcher、结构化 Handoff 和兼容 Registry 已删除。
- 当前实现已在单 Agent 外接入薄 Evidence Gate Graph，默认最多两轮 Agent、8 次 Tool、16 条 Evidence、16000 个 Provider Token 和 90 秒总耗时；结构化报告校验失败或预算耗尽时生成 `partial_report`。
- 生产系统指令、评测 baseline 指令、Evidence Gate 报告契约和独立会话指令已外置到 `config/prompts/`，由 `[agent]` 配置启动期一次加载并缓存；`promptVersion` 与 `conversationPromptVersion` 是人工发布标签。当前不做热更新、内容哈希或 Nacos Prompt 发布。
- 当前已实现 SQL Server 对象定义、已发布 Catalog 窄检索、受 QueryGuard/Catalog/资源限制保护的 `execute_readonly_query` Tool，以及 Docker PostgreSQL + SQL Server 的真实跨数据库联调。事实型只读 Tool 结果会生成运行时 `EvidenceItem`，知识检索结果还必须通过文档/版本/Chunk/内容哈希校验并映射为 `knowledge_chunk`；Evidence Gate 要求报告 `sourceRef` 精确绑定本次证据，Diagnosis Worker 会把通过门禁或明确降级的报告及其证据引用正式落库。
- P7 正式任务链路已接通任务创建、TaskEvent JSON/SSE 补读、取消命令、Worker Claim/续租/fencing、Outbox Relay、RabbitMQ Consumer/三级重试/死信，以及 DiagnosisStep、ToolExecution、EvidenceItem、ReportEvidence 和 DiagnosisReport 的 fenced 事务提交；正式报告读取、管理员失败恢复和报告反馈也已接入。
- 迁移步骤和验收标准见 [`agent-implementation-plan.md`](agent-implementation-plan.md)。准确率和 Token 降幅仍是评测目标，不是已达到的项目结果。

## 目标架构

~~~text
用户问题 / 工单
  -> Context Builder
       - 用户、角色、任务与数据源范围
       - 对话摘要和最近消息
       - 附件、依赖可用性和总预算
  -> 单个 ChatModelAgent / ReAct 内循环
       - 入口 Skill 指南
       - 本次授权后的原生 Tool
       - 按需读取其他 Skill 与 references
  -> Evidence Gate
       - 充分：生成报告
       - 不足且有预算：携带缺口回 Agent
       - 超预算/依赖缺失：生成部分报告
~~~

普通 SQL、代码、知识库、附件和 Web 调查都在同一个 Agent 内循环中完成。只有必须隔离上下文、权限或预算的大型代码调查与脱敏 Web Research，才考虑 ADK Handoff/Fork。

独立工作台会话使用单独的轻量 Conversation Agent Runtime。它只加载持久化的 user/assistant
历史和当前消息引用，按 `TaskScope` 动态暴露 case、knowledge、web Tool；`create_diagnosis_task`
是只在唯一 selected case 且由直接用户消息明确请求诊断时可见的受控命令。该 Runtime 返回最终
回答并由会话服务持久化助手消息，但不会把长耗时 Diagnosis Worker、原始 Tool 结果或模型推理
过程塞进会话请求。
`conversation_turns` 以客户端 UUID、规范化请求指纹和 PostgreSQL 租约约束每个回合：同 key
失败重试复用原用户消息，完成重试直接回放原助手消息；单个会话同时只允许一个未过期回合。
这解决 HTTP 重试幂等，但模型调用仍在 API 进程内同步执行，不等同于后台 Conversation Worker。

## 各层职责

### Skill：领域指南

Skill 描述调查步骤、证据标准、停止条件和输出规范。它回答“遇到这类问题应该怎样调查”，不实现外部能力，也不决定最终权限。

目标目录：

~~~text
config/skills/<skill-id>/
|-- SKILL.md
`-- references/
~~~

Eino Skill Middleware 通过 filesystem Backend 发现 Skill，并把 Middleware 放入 `adk.ChatModelAgentConfig.Handlers`。应用根据页面/任务上下文预加载入口 Skill 的完整 `SKILL.md`；其他 Skill 初始只暴露名称和描述，需要时再读取完整指南或 references，形成渐进式暴露。

未来的脚本也不是任意插件机制。只允许确定性转换、格式化和本地校验；不得保存凭证、连接生产库或获得任意 Shell、文件和网络权限。P2 不创建 `scripts/`，也不启用脚本执行。

Skill 优先级：

| 优先级 | Skill | 作用 |
| --- | --- | --- |
| P0 | `ticket-diagnosis` | 从工单出发规划调查、识别证据缺口 |
| P0 | `sql-investigation` | 调查业务数据、对象定义、计划和执行历史 |
| P0 | `code-investigation` | 在 GitHub 凭据可见范围内用仓库和 Commit 代码证据解释故障 |
| P1 | `knowledge-qa` | 全局/个人知识库问答 |
| P1 | `attachment-investigation` | 分析图片、PDF、日志和聊天附件 |
| P1 | `web-research` | 对脱敏后的公开问题进行联网调查 |
| P2 | `log-investigation` | 查询已配置日志源 |
| P2 | `sql-optimization-lab` | 在产品库执行受控性能实验 |

### Tool：受控能力

Tool 是模型与外部世界交互的唯一正式入口。SQL、GitHub、知识库、附件、日志和网络访问都必须实现为 Eino `tool.BaseTool`，不能藏在 Skill 脚本里绕过治理。

统一 ToolCatalog 负责注册，`AgentToolProvider` 根据本次任务过滤：

~~~go
type AgentToolProvider interface {
    ToolsFor(ctx context.Context, scope TaskScope) ([]tool.BaseTool, error)
}
~~~

`TaskScope` 包含用户角色、任务类型、数据源、生产/产品库环境、任务创建时冻结的
`allowedCapabilities`，以及 Runtime 探测到的依赖可用状态。能力授权与依赖健康相互独立：
GitHub MCP 或 SQL Server 在线不会自动扩大本任务的 Tool 集合。注册到 Catalog 不等于暴露给
模型；Tool 必须同时满足角色、任务、数据源、安全模式、业务能力和依赖健康六类条件。
授权 Middleware 在 ADK `BeforeAgent` 阶段读取本次 `TaskScope` 并收敛 Tool 配置，只有
`ToolsFor` 返回的 Tool Schema 才进入本次运行。Catalog、Middleware、Tool 和无状态模型客户端
可以复用；Eino `v0.9.13` 的 `ChatModelAgent` 在 Run 初始化时会改写内部配置，`-race` 已证明
不能共享实例并发运行，所以目标 Runtime 必须为每次 Run 创建隔离 Agent。

所有 Tool 继续执行：参数 Schema 校验、应用策略重写、Context Timeout、行数/字节截断、敏感字段脱敏、调用轨迹和证据固化。Prompt 和 Skill 文本都不能代替这些安全边界。

### Prompt：可配置指令

`diagnosis-system.md`、`evaluation-baseline.md` 和 `report-contract.md` 分别承载生产 Agent 基础指令、评测 baseline 基础指令和结构化报告契约。应用启动时读取并校验文件，Runner 仍在运行期追加入口 Skill、授权数据源和依赖降级状态，Evidence Orchestrator 仍由代码追加上一轮报告和门禁缺口。这样可编辑内容与不可绕过的运行时状态保持分离。

`promptVersion` 由发布者在修改 Prompt 后显式递增，用于报告和评测追溯；它不是模型版本，也不自动根据文件内容生成。当前工程阶段只保证“文件配置、启动失败保护、重启生效和版本字段预留”，未来若接入 Nacos 或独立 Prompt 平台，应继续向 Runner 注入同一组已解析指令，不改变 Tool 授权和 Evidence Gate 的代码边界。

### ChatModelAgent：动态调查内循环

ADK `ChatModelAgent` 负责多轮 Model -> Tool -> Model 循环。目标装配使用：

- StepFun ChatModel；
- 本次授权后的 Tools；
- Skill Middleware；
- usage、超时、重试、Tool 策略和轨迹 Middleware/Callback；
- `MaxIterations` 与任务总预算。

入口任务由程序直接附加 `ticket-diagnosis` 或未来 `knowledge-qa` 的完整入口指南，避免为了选择入口 Skill 额外调用一次模型。诊断任务的 `knowledge` capability 由后端自动写入并冻结，前端不提供 Tool 选择。后续能力由 Agent 在同一循环内根据证据缺口选择。

### Graph：外层状态与门禁

Graph 不为每个 Skill 建节点，只保留真实的应用状态分支：

1. `prepare_context`：构造权限、记忆、附件、依赖和预算；
2. `agent_loop`：运行单个 ChatModelAgent；
3. `evidence_gate`：确定性检查证据完整度、引用、限制和预算；
4. `report`：生成并校验完整报告；
5. `partial_report`：证据不足、依赖缺失或预算耗尽时输出明确限制。

Evidence Gate 可以在固定上限内把“缺少哪些证据”作为补充指令送回 Agent。Graph 的循环上限、Token、耗时、Tool 次数和取消状态由应用控制，不能只依赖模型自觉停止。Tool、Evidence 和 Agent 轮次是严格的执行前门禁；Token 来自供应商调用后的 usage 结算，达到上限后立即取消当前 Agent Run 且不再启动新模型或 Tool，但已经发出的单次模型调用无法在返回 usage 前撤回，因此可能出现一次结算越界，不能把它描述成精确计费预留。

### Workflow：确定性子流程

以下能力使用固定 Workflow 或普通 Go Service，不交给 Agent 自由组合内部步骤：

- SQL 解析、只读校验、授权校验、执行和结果截断；
- Query 改写、向量/FTS 混合召回、融合和重排；
- 文档解析、OCR/VLM 路由、切块和索引；
- 结构化报告解析、Evidence 引用校验和持久化；知识 Chunk 证据还要求可复核的文档版本、Chunk、哈希和定位字段。

Agent 看到的是这些 Workflow 的窄 Tool 接口，不是内部每一个实现步骤。

## ToolSearch 决策

当前不启用 ToolSearch。原因是授权后 Tool 数量少且稳定，直接绑定省去一次搜索轮次，也更容易验证 StepFun 的 Tool Calling 和缓存行为。

当授权后 Tool 达到约 15 至 20 个、Schema Token 占比持续升高或同类 Tool 导致选择准确率下降时，再对 Eino `dynamictool/toolsearch` Middleware 做独立 POC。现有 Tool 保持 `tool.BaseTool`，因此未来主要替换装配层，不重写业务 Tool。

## GitHub MCP 安全边界

服务端连接官方 GitHub MCP Server 时设置：

- `X-MCP-Readonly: true`；
- `X-MCP-Tools` 当前只包含 `search_repositories`、`search_code`、`get_repository_tree`、`get_file_contents`、`list_commits`、`get_commit`；
- PAT 通过 `MESGUARD_GITHUB_MCP_TOKEN` 注入，不写入 TOML、数据库或日志。

具体仓库、分支和私有代码权限由 GitHub Token 或 GitHub App Installation 决定，MESGuard 不重复维护 owner/repository/ref 或 `allowedOwners` ACL。`search_repositories` 用于在 GitHub 当前凭据可见范围内发现候选仓库；后续文件和提交 Tool 使用模型从结果中选出的 owner/repo/ref/sha，不再被应用配置重写到固定仓库或分支。应用层只负责 Tool 白名单、参数形状、路径边界和结果规模，不扩大 GitHub 凭据本身的权限。`get_repository_tree` 只读仓库树，并通过 `tree_sha` 固定版本、`path_filter` 缩小候选目录；它提供文件清单，不替代最终的固定 SHA 文件证据。

演示环境使用只读 fine-grained PAT。生产目标使用 GitHub App Installation Token 或独立服务账号，并通过 `credential_ref` 注入；P0-P5 只预留凭证提供器接口，不提前实现 Token 自动轮换。

文件读取不指定 ref/sha 时遵从 GitHub Tool 的默认分支行为；需要调查其他分支时由调用参数显式传入 ref，最终证据统一固定到实际 Commit SHA。`search_code` 只提供候选路径，最终证据必须来自文件读取并记录实际 owner、repository、Commit SHA、文件路径和行号。应用对 `incomplete_results=true` 做最多三次短间隔重试；仍不完整时返回 `status=index_pending`。这里的 `index_pending` 是应用内部的搜索降级状态，不是 GitHub 索引完成度结论；系统不生成代码证据，并在最终回答中明确“不能据此证明没有匹配”。GitHub MCP 不可用时保留其他证据并明确标注“GitHub MCP 工具暂时不可用”。

## 工单 Tool 的数据最小化

`read_external_case` 复用现有 ERP 工单服务，但返回独立证据 DTO。附件只返回名称、类型、大小和内容哈希，不返回 MinIO `objectKey`、永久 URL 或访问凭证。

## Token、流式与日志

模型 usage 必须来自供应商响应，按一次任务中的 ChatModel 调用累计 prompt、completion、total、cached 和 reasoning Token。Callback/Middleware 需要按组件类型和调用 ID 去重，避免 Graph 与 Agent 对同一响应重复计数。

诊断任务通过 SSE 发布结构化调查轨迹、Tool 摘要、Evidence Gate 结果和最终报告。前端可将调查轨迹默认折叠、按需展开，但不输出或持久化模型原始 `ReasoningContent`、完整 Prompt 和敏感 Tool 参数。知识问答另行支持逐 Token 流式输出。最终报告仍需持久化并一次性确认，浏览器断开不取消后台任务。

应用边界统一使用 Zap 记录任务 ID、Agent Run ID、模型、Skill、Tool、耗时、Token 和降级状态。底层返回错误，由最了解请求/任务语义的边界层记录一次，避免重复日志。

## StepFun ChatModel 现状

聊天和多模态模型使用 Step Plan OpenAI 兼容接口，当前模型为 `step-3.7-flash`，API Key 仅通过 `.env` 的 `MESGUARD_STEPFUN_API_KEY` 注入。现有本地协议测试和真实烟雾请求已经验证 Tool Calling 与 provider usage。

历史烟雾数据只能证明协议链路有效。过渡 Handoff 方案曾出现总 Token 从 1444 增至 1954 的单次对比，说明额外 Tool Schema 和模型轮次有成本；该样本不是当前架构评测，也不能作为 45% 降幅结论。

## 评测口径

固定相同模型、温度、问题集、输入上下文和最大迭代，对比：

- 基线：一次绑定全部授权 Tool Schema，不使用 Skill 渐进式读取；
- 实验：按 TaskScope 过滤 Tool，并使用 Skill 渐进式读取。

| 指标 | 定义 |
| --- | --- |
| Tool 选择准确率 | 首个关键 Tool 与标注一致的样本比例 |
| 任务完成率 | 满足预定义证据和报告要求的样本比例 |
| 越权调用率 | 被策略拒绝或不属于 TaskScope 的调用数 / 总调用数 |
| 输入 Token 降幅 | `(baseline_input_tokens - experiment_input_tokens) / baseline_input_tokens` |
| 端到端耗时 | 从 Agent 开始到完整/部分报告完成 |

P5 的评测输入分为两类：版本化 `EvaluationCase` 保存 expected tools、forbidden tools、required evidence、可接受结论和根因标注；每次真实运行单独保存 `EvaluationObservation`，包括 baseline/experiment、Run ID、模型配置、实际 Tool、报告状态和 Provider usage。CLI 会按 `caseId` 配对，只有模型、版本和 reasoning effort 一致时才计算 Token/TTFT/耗时差异。

Tool Schema 字节数只能做静态规模检查，不能替代 Provider Token。`testdata/*sample*` 只验证统计程序，不是项目效果证据。真实实验要保留该次实际 `AllowedTools`、评测集版本、模型配置、原始脱敏结果和可复现命令。

2026-08-02 的首条真实 `agent-real-v1` 工单样本已验证 paired CLI：baseline 暴露
7 个业务 Tool Schema，experiment 暴露 `read_external_case`、`read_skill_reference`
和 `skill` 共 3 个 Schema；两者都正确先调用 `read_external_case`，并通过 Evidence
Gate 取得 `case_snapshot`。Provider 输入 Token 从 5960 降到 4640，配对降幅为
22.15%；总 Token 从 7095 降到 6229。样本只有 1 条，experiment 耗时从 10.55 秒
升到 12.14 秒，TTFT 尚未测量，因此不把这次结果写成效果承诺。

随后 `agent-real-v2` 扩展了工单、代码调查和 GitHub 降级三类请求：两种 variant 的
路由、首 Tool、Evidence 覆盖均为 1/1，禁止 Tool 调用均为 0；代码调查在默认 16000
Token 总预算下两边都记录为 `token_budget_exhausted` partial，降级样本没有调用代码
或 SQL Tool。`agent-real-v3` 在临时 32000 Token 评测预算下验证了 SQL Server 视图定义
读取和 `sql_object_definition` Evidence，两边都通过门禁。v2/v3 的结果只说明不同
任务形态的真实成本与边界，不能合并成简历效果指标。

`agent-real-v4` 又在事务内临时 published Catalog 上验证了 SQL 调查的
`read_external_case -> search_schema_catalog -> execute_readonly_query` 顺序；两边都取得
`case_snapshot`、`schema_catalog` 和 `sql_query`，无禁止调用且完整通过门禁。baseline/
experiment 输入 Token 为 12303/14053，耗时为 17.64/24.15 秒；Catalog 夹具在命令结束时
rollback，不代表生产 Catalog 已具备扫描、审核和发布管理。该组结果同样只用于边界和成本
观察，不能合并成简历指标。

## 官方资料

- [Eino ADK ChatModelAgent](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model)
- [Eino Skill Middleware](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_skill)
- [Eino ToolSearch Middleware](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_toolsearch)
- [CloudWeGo Eino](https://github.com/cloudwego/eino)
- [Eino OpenAI ChatModel](https://github.com/cloudwego/eino-ext/tree/main/components/model/openai)
- [GitHub MCP Server](https://github.com/github/github-mcp-server)
