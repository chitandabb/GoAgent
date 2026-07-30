# 诊断 Skill、数据源与工具治理

## 文档状态

- 本文定义诊断 Agent 如何访问远程 SQL Server、数据库执行证据、代码、知识库、公开网页和日志。
- 当前过渡代码已经实现 `ticket-diagnosis -> code-investigation` 的结构化 Handoff、工单只读 Tool 和 GitHub MCP 只读 Tool；目标架构会将普通调查合并到单 Agent 内循环，Handoff 不再作为 SQL/RAG/Web 的默认切换方式。
- SQL、RAG、Web Search、附件正文和运行日志 Tool 尚未实现，本文是后续实现边界，不能作为当前能力宣传。

## 总体原则

模型不能直接获得数据库连接串、服务器账号、文件路径或任意网络访问能力。管理员配置的是数据源、凭证引用、网络模式和能力策略；模型只能在一次任务已经授权的数据源范围内，通过稳定的 `dataSourceId` 调用受控 Tool。

~~~text
模型生成调用意图
  -> TaskScope 运行时 Tool 授权
  -> 任务数据源授权
  -> Tool 参数 Schema 校验
  -> SQL/路径/查询策略校验
  -> 受限基础设施适配器
  -> 结果截断、脱敏和证据固化
~~~

Prompt 是行为指导，不是安全边界。生产库只读必须同时由 SQL Server 登录权限、应用策略和查询执行限制保证。

## 多服务器与客户网络

MESGuard、客户应用和 SQL Server 不需要部署在同一台机器。一个 Diagnosis Worker 可以连接多个网络可达的数据源，每个数据源独立配置地址、数据库、环境和凭证引用。

### M1：部署实例直连

一期采用直连模式：

~~~text
Diagnosis Worker
  -> 公司内网、专线或 VPN
  -> SQL Server TCP Endpoint
~~~

管理员配置：

- 数据源名称、类型、环境和用途；
- 主机、端口、实例和数据库等非敏感连接信息；
- 指向环境变量或挂载 Secret 文件的 `credential_ref`；
- 允许访问的表、视图、存储过程和批量匹配规则；
- 最大执行时间、返回行数、结果字节数和并发数；
- 可用能力，例如 Catalog、对象定义、只读查询、Query Store 或实验库执行。

数据库密码不写入 PostgreSQL 普通业务表，也不返回管理前端。修改连接和授权策略必须产生管理员审计记录。

### 未来：客户侧 Connector

如果 MESGuard 所在网络无法主动连接客户数据库或日志平台，再增加部署在客户网络内的轻量 Connector：

~~~text
客户侧 Connector
  -> 主动建立出站 mTLS/HTTPS 长连接
  -> MESGuard Connector Gateway
~~~

Connector 只实现版本化的窄动作，例如读取对象定义、执行受限查询、查询日志，不提供任意 Shell、任意文件读取或透明数据库代理。M1 不实现 Connector，代码通过 `DiagnosticDataSource` 和 `LogSource` 接口保留替换点。

若不同客户之间要求严格隔离，优先为每个客户或网络域部署独立 MESGuard 实例，不在单个演示实例中提前实现复杂多租户远程控制。

## SQL Server 权限分层

### 生产库

生产库强制使用数据库侧只读账号，允许的能力为：

- 读取管理员发布的 Schema Catalog；
- 读取授权表和视图；
- 读取授权存储过程、函数和视图定义；
- 在开启且授权时读取 Query Store 或必要 DMV 的聚合执行统计；
- 生成估算执行计划；
- 执行经过校验、限时、限行和限量的单条只读查询。

禁止 DDL、DML、动态执行、任意存储过程调用、跨库访问、链接服务器、备份恢复和服务器文件操作。即使应用校验遗漏，数据库账号也不能写入。

### 产品库 / LAB

产品库与生产库使用不同账号和连接池。后续 `sql-optimization-lab` Skill 可以执行受控实验，但必须：

- 使用从生产数据脱敏同步的独立数据库；
- 每次实验记录基线、候选 SQL、对象版本、执行计划、耗时和资源统计；
- 设置语句超时、并发和资源上限；
- 在隔离 schema、临时对象或可恢复快照中运行；
- 实验结束清理对象，失败时执行恢复流程；
- 永远不把优化 SQL 自动回写生产库。

产品库权限放宽不代表让模型获得数据库管理员权限。恢复、快照和高风险 DDL 由固定应用流程执行，不由模型自由拼接。

## SQL Skill 与 Tool

SQL 能力放在独立的 `sql-investigation` Skill，不把全部 SQL Tool 塞给工单诊断 Skill。

首批 Tool 建议为：

| Tool | 作用 | 生产库 |
| --- | --- | --- |
| `search_schema_catalog` | 根据业务词、表字段 Comment 和别名检索候选对象 | 允许 |
| `get_database_object_definition` | 读取授权存储过程、函数或视图定义 | 允许 |
| `execute_readonly_query` | 执行单条受限查询并返回截断结果 | 允许 |
| `get_estimated_query_plan` | 获取估算计划，不执行原始业务语句 | 按权限允许 |
| `search_query_store` | 查询时间窗口内的历史执行统计和计划变化 | 开启 Query Store 且授权时允许 |
| `compare_lab_query` | 在产品库比较基线和候选实现 | 禁止，仅 LAB |

模型生成的 SQL 一律视为不可信输入。`execute_readonly_query` 至少执行：

1. 只允许单条 `SELECT` 或只读 CTE；
2. 使用 SQL Server 方言解析和 AST/Token 策略校验，不能只做字符串黑名单；
3. 校验对象必须属于任务授权的数据源和发布 Catalog；
4. 禁止跨库名、系统危险对象、动态 SQL 和写入结构；
5. 设置 Context Timeout、最大行数、最大字节数和并发限制；
6. 对结果字段脱敏并固化 EvidenceItem；
7. 日志只记录查询指纹、策略版本、耗时和结果规模，原始敏感 SQL 进入受控审计证据而非普通 Zap 日志。

读取存储过程定义使用固定参数化的元数据查询，不允许模型自行调用 `sp_helptext`、`xp_cmdshell` 或其他系统过程。

## 数据库执行证据

很多业务流转集中在存储过程中，因此 M1 优先支持数据库侧证据：

- 存储过程、函数和视图定义；
- Query Store 中的查询、计划、运行时聚合统计和计划变化；
- 必要 DMV 中的当前或近期执行统计；
- 估算执行计划；
- 业务表中的状态流转记录。

这些数据不能等同于完整应用运行日志：Query Store 是查询性能历史，DMV 可能在实例重启或缓存淘汰后丢失，也不保证包含完整业务参数。报告必须标注来源、时间范围、数据新鲜度和缺失限制。

## 应用运行日志

运行代码的服务器可能与 MESGuard 分离，因此不采用“Agent 远程登录服务器然后读取任意文件”的方案。日志能力按来源适配：

1. 已有集中日志平台：接入 Elasticsearch、Loki、Seq 等只读 API；
2. 用户提供日志文件：作为任务附件上传 MinIO，由 `read_attachment` 按需读取；
3. Windows Event Log、共享目录或本地滚动文件：未来由客户侧 Connector 执行受限查询；
4. 没有可访问日志源：报告明确说明“应用日志证据不可用”。

未来 `log-investigation` Skill 可以使用 `search_logs`、`get_log_context` 两类抽象 Tool。管理员配置日志源、时间窗口上限、索引/目录白名单和脱敏规则；模型不能提交服务器凭证、UNC 路径或任意正则扫描整台服务器。

## 可扩展 Agent 编排

最终使用“单 Agent 内循环 + 薄外层 Graph”：

~~~text
prepare_context
  -> ChatModelAgent
       - 按需读取 Skill 指南
       - 调用本次 TaskScope 授权的 SQL/代码/RAG/附件/Web Tool
  -> evidence_gate
       -> 证据不足且有预算：回 ChatModelAgent
       -> 证据充分：report
       -> 超预算或依赖缺失：partial_report
~~~

普通 SQL、代码、RAG、附件和 Web 调查不离开当前 Agent 循环。外层 Graph 只校验总预算、取消状态、Evidence 引用和报告完整性。大型代码调查或必须先脱敏再联网的 Web Research 如果确实需要隔离上下文，才单独使用 Handoff/Fork。

Tool 的最终授权来自用户角色、任务类型、数据源、生产/产品库环境和依赖可用状态。Skill 只提供 SOP，不授予权限。具体迁移顺序见 [`agent-implementation-plan.md`](agent-implementation-plan.md)。

## Skill 与 Tool 规划

| Skill | 主要 Tool | 说明 |
| --- | --- | --- |
| `ticket-diagnosis` | `read_external_case` | 读取工单、整理线索和规划调查 |
| `sql-investigation` | Catalog、对象定义、只读查询、Query Store | 核对业务数据和数据库执行证据 |
| `code-investigation` | GitHub MCP 只读 Tool | 定位代码与提交 |
| `attachment-investigation` | `read_attachment`、OCR/VLM 结果读取 | 按需分析截图、PDF和日志附件 |
| `knowledge-qa` | `search_knowledge`、`get_knowledge_chunk` | 全局与个人知识库问答 |
| `web-research` | `web_search`、`fetch_public_page` | 只查询公开、脱敏问题并保留引用 |
| `log-investigation` | `search_logs`、`get_log_context` | 接入可用的只读日志源 |
| `sql-optimization-lab` | LAB 查询、计划比较和清理流程 | 后续受控优化实验 |

Web Search 不默认获得工单原文。进入 `web-research` 前必须把公司名、客户名、工单号、内部地址、SQL/日志原文和代码片段移除，只允许搜索通用产品概念、公开错误码和公开依赖资料。

代码调查不维护应用内逐仓库授权表。fine-grained PAT 或 GitHub App Installation 决定实际可访问私有仓库，MCP 配置中的 `allowedOwners` 只限制公司组织/演示账号范围。`search_repositories` 用于在该范围内发现候选仓库；最终证据记录实际仓库、Commit SHA、文件和行号。当前不开放公司范围外的公共代码研究，未来如有 Coding Agent 需求，应作为独立 Skill 和授权范围启用。

## 前端与 API 交接

诊断后端稳定后同步更新 `docs/design/openapi.json` 和前端交接说明，至少覆盖：

- 创建、查询、取消和重试 DiagnosisTask；
- TaskEvent SSE、`Last-Event-ID` 断线补读和心跳；
- `agent.started`、`skill.loaded`、`tool.completed`、`evidence.updated`、依赖降级、报告完成等事件；
- 报告、证据引用、Tool 执行摘要和 Token/耗时信息；
- 管理员数据源、能力策略和依赖状态页面；
- GitHub MCP 暂时不可用、SQL 源不可达、日志证据缺失等明确状态。

诊断页面应以“任务时间线 + 证据 + 最终报告”为主，不展示模型内部思维过程、完整 Prompt、凭证或原始敏感 Tool 参数。最终报告一次性发布；SSE 先传阶段进度，知识问答另行支持逐 Token 流式输出。
