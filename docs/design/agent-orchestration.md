# Agent 编排与工具治理设计

## 文档状态

- P0-P4 已完成：当前 Runner 每次调用创建独立的 Eino ADK `ChatModelAgent`，使用不可变 `TaskScope`、统一 `ToolCatalog`、`BeforeAgent` 运行时授权、原生 `SKILL.md` 和按需 reference Tool。
- `ticket-diagnosis` 可以在同一 Agent 循环内加载 `code-investigation` 并继续调用 GitHub Tool；旧的每 Skill Executor、Graph Dispatcher、结构化 Handoff 和兼容 Registry 已删除。
- 当前实现已在单 Agent 外接入薄 Evidence Gate Graph，默认最多两轮 Agent、8 次 Tool、16 条 Evidence、16000 个 Provider Token 和 90 秒总耗时；结构化报告校验失败或预算耗尽时生成 `partial_report`。
- 当前已实现 SQL Server 对象定义、已发布 Catalog 窄检索、受 QueryGuard/Catalog/资源限制保护的 `execute_readonly_query` Tool，以及 Docker PostgreSQL + SQL Server 的真实跨数据库联调。事实型只读 Tool 结果会生成运行时 `EvidenceItem`，Evidence Gate 要求报告 `sourceRef` 精确绑定本次证据；正式 Diagnosis Worker/SSE、证据持久化和可复现评测仍待补齐。
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
| P0 | `code-investigation` | 用固定仓库和 Commit 的代码证据解释故障 |
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

`TaskScope` 包含用户角色、任务类型、数据源、生产/产品库环境和依赖可用状态。注册到 Catalog 不等于暴露给模型。授权 Middleware 在 ADK `BeforeAgent` 阶段读取本次 `TaskScope` 并收敛 Tool 配置，只有 `ToolsFor` 返回的 Tool Schema 才进入本次运行。Catalog、Middleware、Tool 和无状态模型客户端可以复用；Eino `v0.9.13` 的 `ChatModelAgent` 在 Run 初始化时会改写内部配置，`-race` 已证明不能共享实例并发运行，所以目标 Runtime 必须为每次 Run 创建隔离 Agent。

所有 Tool 继续执行：参数 Schema 校验、应用策略重写、Context Timeout、行数/字节截断、敏感字段脱敏、调用轨迹和证据固化。Prompt 和 Skill 文本都不能代替这些安全边界。

### ChatModelAgent：动态调查内循环

ADK `ChatModelAgent` 负责多轮 Model -> Tool -> Model 循环。目标装配使用：

- StepFun ChatModel；
- 本次授权后的 Tools；
- Skill Middleware；
- usage、超时、重试、Tool 策略和轨迹 Middleware/Callback；
- `MaxIterations` 与任务总预算。

入口任务由程序直接附加 `ticket-diagnosis` 或未来 `knowledge-qa` 的完整入口指南，避免为了选择入口 Skill 额外调用一次模型。后续能力由 Agent 在同一循环内根据证据缺口选择。

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
- 结构化报告解析、Evidence 引用校验和持久化；当前 P6 只完成运行时快照，正式持久化留在 DiagnosisTask/Worker。

Agent 看到的是这些 Workflow 的窄 Tool 接口，不是内部每一个实现步骤。

## ToolSearch 决策

当前不启用 ToolSearch。原因是授权后 Tool 数量少且稳定，直接绑定省去一次搜索轮次，也更容易验证 StepFun 的 Tool Calling 和缓存行为。

当授权后 Tool 达到约 15 至 20 个、Schema Token 占比持续升高或同类 Tool 导致选择准确率下降时，再对 Eino `dynamictool/toolsearch` Middleware 做独立 POC。现有 Tool 保持 `tool.BaseTool`，因此未来主要替换装配层，不重写业务 Tool。

## GitHub MCP 安全边界

服务端连接官方 GitHub MCP Server 时设置：

- `X-MCP-Readonly: true`；
- `X-MCP-Tools` 当前只包含 `search_code`、`get_file_contents`、`list_commits`、`get_commit`；
- PAT 通过 `MESGUARD_GITHUB_MCP_TOKEN` 注入，不写入 TOML、数据库或日志。

具体私有仓库权限由 fine-grained PAT 或 GitHub App Installation 决定，MESGuard 不重复维护逐仓库 ACL。当前演示配置仍固定一个 owner/repo/ref，并在调用前强制重写或校验参数；多仓库 `search_repositories` 和 `allowedOwners` 边界尚未实现，后续必须先补参数治理和证据引用规则再开放。

演示环境使用只读 fine-grained PAT。生产目标使用 GitHub App Installation Token 或独立服务账号，并通过 `credential_ref` 注入；P0-P5 只预留凭证提供器接口，不提前实现 Token 自动轮换。

分支默认使用仓库默认分支，不维护产品版本、模块和路径映射。`search_code` 只提供候选路径，最终证据必须来自文件读取并记录实际 owner、repository、Commit SHA、文件路径和行号。GitHub MCP 不可用时保留其他证据并明确标注“GitHub MCP 工具暂时不可用”。

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

## 官方资料

- [Eino ADK ChatModelAgent](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model)
- [Eino Skill Middleware](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_skill)
- [Eino ToolSearch Middleware](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_toolsearch)
- [CloudWeGo Eino](https://github.com/cloudwego/eino)
- [Eino OpenAI ChatModel](https://github.com/cloudwego/eino-ext/tree/main/components/model/openai)
- [GitHub MCP Server](https://github.com/github/github-mcp-server)
