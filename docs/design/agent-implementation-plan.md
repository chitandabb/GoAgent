# Agent 实施计划

## 目的

本文记录 2026-07-30 确认的 Agent 演进顺序，作为上下文压缩、会话切换和中断后的恢复入口。当前完成情况只在
[`../roadmap.md`](../roadmap.md) 更新；长期架构边界见
[`agent-orchestration.md`](agent-orchestration.md) 和
[`diagnostic-tools.md`](diagnostic-tools.md)。

## 当前基线

当前工作区已经具备可运行的单 ADK Agent 基线：

- Eino `v0.9.13` 和 StepFun `step-3.7-flash` Tool Calling；
- `ticket-diagnosis`、`code-investigation` 两个原生 `SKILL.md` 包及按需 references；
- `read_external_case` 和六个只读 GitHub MCP Tool；
- Tool 参数校验、超时、结果截断、调用轨迹和 Provider Token usage；GitHub Code Search
  返回不完整结果时的有限重试与显式降级状态；
- 每次 Run 独立创建的 `adk.ChatModelAgent`，入口 Skill 由任务上下文确定；
- `TaskScope -> ToolCatalog -> BeforeAgent` 的运行时 Tool 授权；
- 同一内循环中的工单读取、Skill 渐进加载和 GitHub 代码调查；
- 模型、正式 Runner 和评测烟雾命令。

真实 StepFun 工单烟雾运行已经证明模型协议、Tool Call 和 usage 聚合可用，但没有证明完整诊断产品链路，也不能作为简历指标。GitHub MCP 的用户凭据握手、六个工具加载、仓库搜索、仓库树候选读取、按 Commit SHA 文件读取和提交追溯已经验证；`search_code` 返回不完整结果时会通过有限重试和 `index_pending` 显式降级，不能据此推断索引状态或无匹配。

P0 已于 2026-07-30 验收：隔离的 ADK `ChatModelAgent` POC 在不替换现有 Runner 的前提下，完成了原生 Skill Middleware 渐进加载、同一 ReAct 循环中的业务 Tool 调用、非流式/流式事件、Context 取消、最大迭代限制和 Provider usage 聚合。真实 StepFun 非流式与流式运行均按 `skill -> read_external_case` 顺序完成三次模型调用；单次烟雾数据只证明兼容性，不作为简历效果指标。

Eino `v0.9.13` 的 `adk/filesystem.InMemoryBackend` 在 Windows 下存在 Skill glob/read 路径分隔符不一致，P0 曾使用局部适配器封装。P2 已替换为项目只读目录 Backend，并保留 Windows slash/backslash 回归测试。

P1 已于 2026-07-31 验收：新增不可变 `TaskScope`、启动期只读 `ToolCatalog` 和 ADK `BeforeAgent` 授权 Middleware。Catalog 根据角色、任务类型、同一数据源的 role/safety mode 组合及依赖可用性过滤 Tool；60 个并发 Run 证明授权状态不串用，production 不能获得 bounded-LAB Tool，GitHub MCP 不可用只移除对应 Tool。GCC 到位后的 `-race` 检测发现 Eino `v0.9.13` 的同一 `ChatModelAgent` 实例不能被并发 Run；因此共享 Catalog、Middleware、Tool 和模型客户端，但每次 Run 创建独立 Agent 实例。P3 已将这套生命周期接入正式 Runner。

P2 已于 2026-07-31 验收：两个 Skill 已迁移为 Eino 原生 `SKILL.md + references`，旧 TOML/Prompt 文件删除；项目只读 filesystem Backend 统一拒绝写入、路径逃逸、symbolic link 和 Windows Junction/reparse point，`read_skill_reference` 只允许按行读取 `references/*.md` 并限制行数/字节数。测试证明初始 Tool 描述只包含 Skill 元数据，完整指南和 reference 分别按需进入模型上下文。脚本执行仍未开放。

旧的每 Skill ReAct/Handoff 过渡实现已在 P3 删除。后续 SQL、RAG 或 Web 调查继续接入当前单 Agent 内循环，不再恢复 Dispatcher。

## 已确认的目标

~~~text
prepare_context
      |
      v
single ChatModelAgent / ReAct loop
  - 按任务授权后的原生 Tool
  - 按需读取 Skill 指南
  - 在同一内循环中连续调查
      |
      v
evidence_gate
  |-- 证据不足且还有预算 --> 回到 Agent
  |-- 证据充分 -----------> report
  `-- 预算耗尽 -----------> partial_report
~~~

边界如下：

1. **Tool 是能力**：数据库、GitHub、附件、知识库和网络访问都实现为 Eino `tool.BaseTool`，只注册一次。
2. **Skill 是指南**：保存调查 SOP、证据标准、停止条件和输出要求，不重复实现 Tool，也不是权限边界。
3. **单 Agent 是动态内循环**：普通 SQL、代码、RAG、附件和 Web 调查在同一个 Agent 循环中完成。
4. **Graph 是薄外层**：只承载上下文准备、证据门禁、预算循环、成功/部分报告等真实状态分支。
5. **Workflow 是确定流程**：SQL 安全执行、RAG 混合检索、文档解析和报告校验使用固定步骤，不交给模型自由编排。
6. **Handoff/Fork 是例外**：只用于需要隔离上下文、权限或预算的大型代码调查和脱敏 Web Research；普通能力切换不使用 Handoff。
7. **ToolSearch 暂缓**：当前 Tool 少且稳定，先按任务授权后直接绑定；达到启用阈值后再评估动态检索。

## 实施顺序

后续严格按下面顺序推进。前一阶段验收失败时，不在后一阶段继续堆功能。

### P0：冻结基线与 ADK 最小 POC

状态：**已完成（2026-07-30）**。

目标：证明 StepFun 能在 Eino ADK `ChatModelAgent` 下工作，避免先大规模重构再发现协议不兼容。

工作：

- 用独立测试或烟雾入口创建 `adk.NewChatModelAgent`；
- 验证一个本地只读 Tool 的多轮调用；
- 接入 Eino Skill Middleware，验证 `SKILL.md` 能被发现和按需读取；
- 验证 Callback usage、超时、非流式运行和流式事件；
- 保持现有 Runner 为默认路径，POC 不进入 HTTP 产品链路。

验收：真实 StepFun 至少完成一次 Agent -> Tool -> Agent；Token usage 不重复计数；取消 Context 后调用能结束；流式能力不会丢失最终 usage。

### P1：统一 ToolCatalog 与运行时授权

状态：**已完成（2026-07-31）**。

目标：Tool 只注册一次，Skill 和 Agent 装配不再各自维护 Tool 实例。

核心接口方向：

~~~go
type AgentToolProvider interface {
    ToolsFor(ctx context.Context, scope TaskScope) ([]tool.BaseTool, error)
}
~~~

`TaskScope` 至少包含用户角色、任务类型、数据源、生产/产品库环境和可用依赖。Catalog 注册不等于向模型暴露；授权 Middleware 在 ADK `BeforeAgent` 阶段读取 `TaskScope`，把 `ToolsFor` 的结果写入本次运行的 Tool 配置。Catalog、Middleware 和 Tool 可以复用；Eino `v0.9.13` 的 `ChatModelAgent` 每次 Run 独立创建，不在并发请求间共享实例。不同任务看到的 Tool Schema 不同。

验收：重复 Tool 名启动失败；无授权 Tool 不进入模型；并发任务的 Tool 范围不串用；GitHub MCP 不可用时只移除代码能力；生产库任务不能获得 LAB Tool。

### P2：Skill 迁移为原生包

状态：**已完成（2026-07-31）**。

目标：从 `skill.toml + system-prompt.md` 迁移到 Eino Skill Middleware 可读取的目录。

~~~text
config/skills/<skill-id>/
|-- SKILL.md
`-- references/
~~~

顺序：先迁移 `ticket-diagnosis`，再迁移 `code-investigation`。`SKILL.md` 只写 SOP、证据标准、停止条件和输出规范；Tool 权限仍来自 `AgentToolProvider`。

未来若增加 `scripts/`，脚本只允许确定性转换、格式化和本地校验，不保存凭证，不直接连接生产库，不提供任意 Shell、任意网络或任意文件访问。P2 没有创建或启用脚本执行能力，只验证文档和 references 的渐进式读取。

验收：Agent 初始上下文只包含 Skill 摘要；需要时才读取完整指南；删除旧 Prompt 后行为测试仍通过；路径逃逸、符号链接和 Windows Junction 资源被拒绝。

### P3：切换为单 Agent 内循环

状态：**已完成（2026-07-31）**。

目标：移除普通 Skill 之间的退出、Handoff 和再次启动模型。

工作：

- 用 ADK `ChatModelAgent` 替换按 Skill 创建的 `ReActExecutor`；
- 入口根据任务类型预加载 `ticket-diagnosis` 或未来 `knowledge-qa` 的完整指南；
- 其余 Skill 在同一循环中按需读取；
- 保留 Tool 参数策略、超时、截断、轨迹、Zap 字段和 usage 聚合；
- POC 与回归测试通过后，删除 `request_code_investigation`、普通 Handoff Dispatcher 和每 Skill Executor。

验收：一条工单请求可在同一个 Agent 循环内先读工单、再查代码；GitHub MCP 不可用时保留已有证据并返回明确限制；不会额外调用 Handoff Tool。

实际验收：确定性模型按 `read_external_case -> skill(code-investigation) -> search_code -> final` 完成一次运行；测试同时覆盖 TaskScope 缺失、GitHub 降级、Tool Schema 白名单、32 KiB 截断、Provider usage、Context 取消、最大迭代和并发隔离。2026-07-31 的真实 StepFun 正式 Runner 烟雾运行调用 `read_external_case`，两次模型调用共报告 4397 Tokens（其中 cached 1600），约 10.37 秒完成。旧 Handoff Tool、Dispatcher、每 Skill Executor、Registry 与兼容 Loader 已删除。2026-08-01 的真实只读 smoke 又验证了私有 C# 仓库的仓库发现、提交追溯、固定 SHA 文件读取和一次完整 `search_code` 返回；仍待完成的是跨仓库、跨查询形态的 Code Search 稳定性评测与 P5 完整效果评测。

### P4：增加薄外层 Graph 与 Evidence Gate

状态：**已完成（2026-07-31）**。

目标：模型负责调查，应用负责完成条件和总预算。

Graph 状态至少记录：任务范围、已收集证据、证据缺口、Token/耗时/Tool 调用预算、循环次数、依赖降级和取消状态。

`evidence_gate` 是确定性校验，不要求模型自己宣布成功。第一版只检查结构化报告是否包含结论、证据引用、来源、限制和置信度；证据不足且还有预算时生成一条补证指令回到 Agent，最多循环固定次数。

验收：证据不足不会伪装为成功；循环和 Token 均有硬上限；取消后不再开始新 Tool；预算耗尽产生 `partial_report` 并说明缺失证据。

实际验收：已实现 `prepare_context -> agent_loop -> evidence_gate -> report/partial_report` Eino Graph，默认限制为两轮 Agent、8 次 Tool、16 条 Evidence、16000 Tokens 和 90 秒总耗时。严格 JSON 解码拒绝未知字段，Evidence 的 `sourceTool` 必须来自本次成功 Tool，缺失结论、来源、限制或置信度会触发一次定向补证。确定性测试覆盖完整报告、补证、Agent/Token/Tool 上限和取消前置检查。2026-07-31 的真实 StepFun 烟雾运行一轮通过门禁，3 次模型调用共 10434 Tokens、耗时约 67.82 秒，最终以 `inconclusive + low confidence` 明确列出缺失日志与数据库证据。

Token 上限按供应商 usage 在每次模型响应后结算：达到阈值后会取消当前 Agent Run并禁止开始下一次模型或 Tool。由于供应商不会在请求前给出精确输入 Token，已经在途的单次调用可能在结算时越过阈值；当前实现不把这个边界伪装成精确计费预留。

### P5：真实评测与第一条简历闭环

状态：**评测契约、统计 CLI 和 GitHub 工具级真实评测切片已完成；完整 Agent paired 数据集待补**。

目标：用固定数据集得到可复现指标，而不是把样例统计或静态 Schema 字节数写进简历。

对比至少包括：

- 基线：全部授权 Tool Schema + 无 Skill 渐进式读取；
- 实验：任务范围过滤后的 Tool + Skill 渐进式读取；
- 相同模型、温度、问题集、最大迭代和输入上下文；
- Tool 选择准确率、越权调用率、任务完成率、输入 Token、TTFT、总耗时和失败类型。

P5 首轮评测覆盖当前已实现的工单、代码和需要拒绝/降级的请求。代码调查额外比较完整 `search_code`、不完整后 `get_repository_tree` 候选链路和已知路径直接读取的召回、输入 Token、耗时与失败类型；不把本地 clone 方案的假设收益写成指标。开发阶段先用 4 至 5 个案例打通链路，P5 前扩展为约 10 至 12 个不同根因、每个 3 至 5 种业务问法，总计约 40 至 50 条固定请求。C# 演示仓库、SQL Server 合成工单、数据库证据和 Git Commit 共用同一组故障场景。

每个案例标注 `expected_tools`、`forbidden_tools`、`required_evidence`、`expected_root_cause` 和 `acceptable_limits`。Tool、越权、Token 和 Evidence ID 由代码确定性评分，语义结论由人工复核；不同供应商的 LLM Judge 只作辅助，并记录 Judge 模型、版本、Rubric 以及与人工评分的一致率。所有失败样本必须保留。

后续每增加 SQL、知识问答或附件能力，都向同一版本化评测体系补充样本并重新运行对照实验。目标值 `93%+` 和 `45%+` 只在真实实验达到后写入项目状态。

当前已提供 `EvaluationCase`/`EvaluationObservation` JSONL 契约和
`cmd/mesguard-agent-eval` 配对统计命令。仓库中的 `dev-v1` 两案例样例只验证
版本、配对、失败类型、证据覆盖和 Token 差异计算；在 GitHub MCP、SQL 调查
Tool 和固定合成故障集准备好前，不把样例结果写进简历。

GitHub 代码调查另有不依赖模型的
`cmd/mesguard-github-search-eval` 命令，使用
`testdata/github-code-search-v1.jsonl` 顺序执行 `search_code`、固定
`tree_sha` 的 `get_repository_tree` 和固定 SHA 的 `get_file_contents`，用于
区分 Search 完整率、候选路径召回、树候选不完整、已知路径文件核验和 fallback
恢复率。2026-08-02 的两条真实样本中，Search 完整率、两层路径召回和文件核验均为
2/2；没有观察到 `incomplete_results`，因此不把 fallback 恢复率写成指标，也不据此
决定实现本地 clone/search。

评测器现在同时记录 `errorType` 和完整的 `errorTypes`，取消时停止后续样本并返回
带 `requestedCases`/`cases` 的部分汇总；仓库树候选响应分别记录上游截断、候选上限、
应用过滤和候选溢出，避免把主动过滤误判为远端截断。重试包装对非法 JSON、负延迟和
不足的延迟配置 fail-closed，文件读取参数拒绝超长路径及 `sha`/`ref` 二义性。

### P6：实现 sql-investigation（进行中，运行时证据切片已完成）

目标：完成最有业务价值的数据库证据链。

顺序：Schema Catalog -> 对象定义 -> SQL Server 只读校验 -> 限时限行查询 -> Query Store/估算计划 -> EvidenceItem。当前已完成 Catalog 基础表和已发布版本检索 Tool，以及对象定义 Tool：对象定义使用固定参数化查询 `sys.objects`/`sys.schemas`/`OBJECT_DEFINITION`，只接收简单标识符，并由 `allowedSchemas`、TaskScope 和数据库只读账号共同限制。Catalog 只返回 active 数据源、published 版本和 queryable 条目。生产库使用数据库只读账号和应用双重限制；产品库/LAB 的实验能力以后单独实现。

SQL 方言校验、Catalog 发布模型和 Evidence Gate 的字段契约会影响后续安全边界；QueryGuard
和窄 `execute_readonly_query` Tool 已完成单测与运行时装配，但真实 SQL Server + 已发布
Catalog 联调。成功的事实型只读 Tool 结果现在会生成带唯一 `evidenceRef`、来源、哈希、
采集时间和截断状态的运行时 EvidenceItem，报告引用必须解析到本次运行的 EvidenceItem。
正式 DiagnosisTask/Worker 持久化仍未开始。

项目不直接依赖尚无正式 Release 且要求 Go `1.25.7` 的 Bytebase Omni，也不重写其完整
T-SQL Parser。当前 POC 已实现默认拒绝的窄 QueryGuard，只处理词法边界、单条只读查询
分类、危险结构拒绝和表/函数引用提取；表驱动、模糊测试和真实 SQL Server CTE+UNION
只读执行均已验证。数据库只读账号、TaskScope/Catalog 授权和执行资源限制继续作为独立
防线；正式 EvidenceItem 存储、报告关联和 SSE 展示将在 P7 任务链路中落地。

### P7：接入正式任务链路

在 Agent 核心稳定后，再接 DiagnosisTask、Worker、TaskEvent、SSE、取消、Outbox/RabbitMQ 和报告持久化。接口稳定后更新 `docs/design/openapi.json`，并给前端任务提供页面状态、SSE 事件、错误降级和联调说明。

MinIO/附件、RAG、Web Search、日志源和 SQL 优化实验按交付计划继续推进，不与 P0-P5 并行铺空壳。

## Skill 与 Tool 优先级

| 优先级 | Skill | Tool/能力 |
| --- | --- | --- |
| P0 | `ticket-diagnosis` | 工单读取、调查规划、证据汇总 |
| P0 | `sql-investigation` | Catalog、对象定义、只读查询、Query Store |
| P0 | `code-investigation` | GitHub MCP 只读 Tool |
| P1 | `knowledge-qa` | 混合检索和知识块读取 |
| P1 | `attachment-investigation` | 附件元数据、文本/OCR/VLM结果读取 |
| P1 | `web-research` | 脱敏搜索和公开页面读取 |
| P2 | `log-investigation` | 日志平台或 Connector 的只读查询 |
| P2 | `sql-optimization-lab` | 产品库受控实验 Workflow |
| P2 | `report-synthesis` | 报告规范；优先作为校验器而非独立 Agent |

## ToolSearch 启用条件

满足以下任一条件再做 POC：

- 单次授权后仍有约 15 至 20 个以上 Tool；
- Tool Schema 持续占输入 Token 的显著比例；
- 首次 Tool 选择准确率因同类 Tool 增多明显下降；
- 多个外部 MCP Server 导致启动和模型绑定成本不可接受。

启用前必须验证 StepFun 对动态 Tool 列表的兼容性、额外搜索轮次的延迟，以及 KV Cache 命中变化。现有 Tool 继续实现 `tool.BaseTool`，因此届时主要改装配层，不重写业务 Tool。

## 删除与保留规则

P3 已完成以下删除：

- 普通 Skill 的 `request_*_investigation` Handoff Tool；
- `handoff_dispatcher` 和环路跳转状态；
- 每个 Skill 单独创建的 `ReActExecutor`；
- 旧 Runner 使用的 `legacySkillProfiles` 与 `LoadSkillDefinitions` 兼容转换层。

继续保留并迁移：Tool 参数治理、MCP 只读策略、运行超时/迭代/结果预算、调用轨迹、Provider usage、Zap 日志、评测聚合和依赖降级。总 Token 与 Evidence 预算已在 P4 实现。

## 恢复工作时的检查清单

1. 读取本计划与 `docs/roadmap.md`，确认当前处于哪个 P 阶段。
2. 查看 `git status --short`，不要暂存另一前端任务的 `docs/design/openapi.json` 或前端改动。
3. 运行 `go test ./... -count=1`、`go vet ./...`、`docker compose config --quiet` 和 `git diff --check`。
4. 需要 Eino API 细节时先查当前官方文档和仓库版本，不凭旧接口记忆编码。
5. 每完成一个阶段，只在 `roadmap.md` 记录已验证事实；目标设计不冒充当前能力。

## 已确认的产品与集成边界

- 一个统一 Agent 工作台承载知识会话和诊断会话，后端依据卷宗而非模型猜测入口类型；
- 一个诊断会话固定一个主工单，同一工单可以有多个独立会话；绑定工单不自动运行；
- 知识会话选择工单时打开新诊断会话，原会话保留并可继续；
- GitHub Token/GitHub App 决定仓库、分支和私有代码的可见范围，不建设应用内逐仓库、组织或分支 ACL；
- `code-investigation` 通过只读 `search_repositories` 发现当前凭据可见的候选仓库，必要时用 `search_code` 或 `get_repository_tree` 缩小候选，再读取选中的文件和提交；
- 演示使用 fine-grained PAT，生产预留 GitHub App/服务账号凭证提供器但不在 P0-P5 实现轮换；
- 私有 C# 演示仓库与 SQL Server 合成数据使用同一组可追溯故障案例。
