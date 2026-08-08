# 诊断 Skill、数据源与工具治理

## 文档状态

- 本文定义诊断 Agent 如何访问远程 SQL Server、数据库执行证据、代码、知识库、公开网页和日志。
- 当前代码已实现单 ADK Agent 内循环：`ticket-diagnosis` 可在同一次 Run 中按需加载 `code-investigation` 或 `sql-investigation`，继续调用工单、GitHub 和 SQL Server 对象定义只读 Tool；普通调查不使用 Handoff。
- 当前已实现 SQL Server 对象定义读取、PostgreSQL 已发布 Catalog 的窄检索、受 QueryGuard/Catalog/资源限制保护的 `execute_readonly_query` Tool、后端拥有的 `search_knowledge`、Firecrawl `web_search`/`fetch_public_page`，以及会话专用 `create_diagnosis_task`/`get_diagnosis_task_status`。Docker PostgreSQL + SQL Server 的真实跨数据库联调、混合检索固定集和运行时/正式 EvidenceItem 已验证；知识检索结果只有通过 Chunk 身份、版本、内容哈希和定位字段校验后才可进入 `knowledge_chunk` EvidenceItem。任务状态 Tool 只有在当前消息带有已验证任务引用时才进入 TaskScope，并继续复用 owner/admin 权限。Web Search 已完成 Query 脱敏、Run 预算、搜索结果 URL 授权、公网 DNS/IP 校验、响应上限和 `web` 引用快照；真实 Firecrawl smoke 仍依赖本机 Key/额度。Catalog 扫描/发布管理、Query Store、附件正文和运行日志 Tool 仍未实现，本文不把目标能力当作已验证结果。

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

首批 Tool 规划为：

| Tool | 作用 | 生产库 |
| --- | --- | --- |
| `search_schema_catalog` | 根据业务词、表字段 Comment 和别名检索已发布候选对象（当前已实现） | 允许 |
| `get_database_object_definition` | 读取管理员允许的存储过程、函数或视图定义（当前已实现） | 允许 |
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
6. 对结果字段做安全转换；成功查询结果由上层继续固化为 EvidenceItem；
7. 日志只记录查询指纹、策略版本、耗时和结果规模，原始敏感 SQL 进入受控审计证据而非普通 Zap 日志。

读取存储过程定义使用固定参数化的 `sys.objects`、`sys.schemas` 和 `OBJECT_DEFINITION` 元数据查询，不允许模型自行调用 `sp_helptext`、`xp_cmdshell` 或其他系统过程。Tool 只接受简单 `schema`/`objectName` 标识符，并由 `allowedSchemas` 配置和数据库只读账号共同限制。

### 只读 SQL QueryGuard 与执行器（窄版本已开放）

`execute_readonly_query` 不使用字符串前缀或简单黑名单判断，也不在项目内重写完整
T-SQL Parser。项目参考 [Bytebase Omni](https://github.com/bytebase/omni) 的语句分类、
对象提取和对抗测试思路，自行实现一个默认拒绝的窄 `QueryGuard`：词法层必须正确处理
注释、字符串和带引号标识符；策略层只接受单条 `SELECT` 或只读 CTE，识别 `UNION`、
拒绝 `SELECT INTO`，并提取引用对象供 TaskScope 和已发布 Catalog 复核。

首版明确拒绝变量、临时表、动态 SQL、跨库/链接服务器、危险系统对象和无法可靠分析的
方言结构。它不是通用 T-SQL AST，不负责格式化或执行计划分析。当前执行器先复核
QueryGuard 提取的对象与 PostgreSQL 已发布 Catalog 的对象级白名单，再进入 SQL Server；
同时强制执行 Context Timeout、最大行数、最大结果字节数和并发信号量。正常、对抗、
授权、脱敏错误和并发限制已有单测；真实 SQL Server + 已发布 Catalog 的联调已由 opt-in 集成测试覆盖，成功结果会在 Agent Runner 中生成受限运行时 EvidenceItem。正式任务持久化和字段级脱敏策略仍需在 DiagnosisTask/Worker 链路中完成。

Catalog 的 `queryable=true` 只是应用层授权，不能授予数据库权限。两侧策略必须取交集：
对象既要出现在任务允许的已发布 Catalog 中，也要能被该数据源的只读账号访问。生产环境
优先向账号和 Catalog 发布受控视图；未授权基础表即使被模型写进 SQL，也应由数据库拒绝。

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

## 代码调查与 GitHub MCP

代码调查采用分层的“候选发现 + 目录清单 + 版本固定读取”只读链路，不把 GitHub Code Search 当作完整、稳定的代码索引。`search_repositories` 用于发现当前凭据可见的仓库，`search_code` 负责低成本定位候选路径；如果 Code Search 返回不完整且仓库已经确定，则使用 `get_repository_tree` 读取仓库树，优先通过 `tree_sha` 固定版本、`path_filter` 缩小目录，并分别检查 `upstream_truncated` 和 `candidate_limit_reached` 后只挑选少量文件。最终代码结论必须回到 `get_file_contents`，并记录实际 owner、repository、Commit SHA、文件路径和行号。需要解释版本变化时，再通过 `list_commits` 和 `get_commit` 固定提交证据。

代码检索的分层边界如下：

~~~text
Layer 0  search_repositories
          -> 发现当前凭据可见的仓库
Layer 1  search_code
          -> 关键词候选路径；完整结果优先
Layer 1b get_repository_tree
          -> Code Search 不完整时按目录前缀取得候选文件清单
Layer 2  get_file_contents
          -> 读取少量文件并固定 Commit SHA，形成代码证据
Layer 3  本地 shallow clone + rg/git grep
          -> 当前未实现，仅在远端候选链路经稳定性评测证明不足后评估
~~~

树目录不是无边界的“全仓库索引”：Git Trees API 的递归结果可能被标记为上游截断，应用侧包装会将正常结果整理为 `status=candidate_paths`，只保留 `blob` 文件、排除常见构建/依赖目录并限制候选数量，同时分别通过 `upstream_truncated`、`candidate_limit_reached`、`filtered_count` 和 `omitted_count` 表达上游截断、候选上限、主动过滤和候选溢出。因此 Skill 要求优先传入窄 `path_filter`，不要把整棵大仓库树直接交给模型。Layer 3 未来若启用，搜索结果必须带本地快照 Commit SHA、采集时间和过期标记，并与远端证据区分。

GitHub REST Search API 的 `incomplete_results=true` 表示查询可能超过服务端时间限制、结果可能不完整；它不是“仓库尚未完成索引”的权威状态，也不能证明 Token 无效，更不能证明没有匹配代码。MESGuard 对该响应最多做三次短间隔重试；仍不完整时返回现有的 `status=index_pending` 机器状态。这个状态在应用语义上表示 `search_degraded`，不生成代码 `EvidenceItem`，最终报告必须保留搜索不完整的限制。

已知路径或已知提交应优先走确定性证据链：

~~~text
search_repositories
  -> list_commits
  -> get_commit
  -> get_file_contents(固定 Commit SHA)
  -> 代码证据（文件、符号或行段）
~~~

这条链路可以可靠支持“某个已知文件/提交中有什么”或“某次变更改了什么”，但不是任意关键词代码搜索的等价替代。若候选路径未知且 Code Search 多次不完整，报告只能说明搜索依赖不可用或结果不完整，不能下“代码中不存在”的结论。

2026-08-01 的真实只读 smoke 已验证私有仓库 `chitandabb/mesguard-csharp-demo`：仓库发现、提交列表、提交详情、固定 SHA 读取 `src/MesGuard.CaseService/TicketSearchService.cs` 均成功；同一仓库的 `TicketSearchService` 查询本次返回 3 条完整结果。此前相同目标的多次查询曾返回 `incomplete_results=true`，因此这组结果只能证明当前请求成功，不能承诺 GitHub Code Search 永远完整可用。

参考：[GitHub REST Search API](https://docs.github.com/en/rest/search/search)、[About GitHub Code Search](https://docs.github.com/en/search-github/github-code-search/about-github-code-search)、[Git Trees API](https://docs.github.com/en/rest/git/trees)、[GitHub MCP Server](https://github.com/github/github-mcp-server)。

### 分层检索评测

当前提供一个不依赖模型的评测命令：

~~~text
go run ./cmd/mesguard-github-search-eval -cases testdata/github-code-search-v2.jsonl
~~~

命令顺序执行 Code Search、树候选和固定 SHA 文件读取，最多接受 20 条样本；凭据只从现有 `.env`/配置读取，不输出 Token、不写仓库、不创建本地缓存。输出中的 `fallbackRecoveryRate` 只有在实际观察到 `searchStatus=incomplete` 的样本时才有分母；没有不完整样本时必须保留为未测量，不能解释为“fallback 失败”。评测器会保留每条样本的多个阶段错误；取消时停止新样本，并输出已经完成样本的部分汇总，`requestedCases` 与 `cases` 分别表示请求数和已完成数。

2026-08-02 的 `github-code-search-v2` 真实运行覆盖私有 C#、GoAgent、GoChat 和公开 `Hello-World`，并包含 `in:file` 查询，共 6 条样本：Search 完整 2/6，2 条 `incomplete_results` 均通过树候选和固定 SHA 文件读取恢复，树路径召回与已知路径文件核验均为 6/6，fallback 恢复为 2/2。两条 GoChat Search 因 GitHub API rate limit 返回错误；树和固定 SHA 文件读取仍成功，错误详情已在评测输出中保留。

### Agent paired 评测

同一版本化样本顺序运行 baseline 和 experiment，并把 Provider usage、实际 Tool、
Evidence、报告状态和耗时写入 observation：

~~~text
go run ./cmd/mesguard-agent-paired-eval \
  -dataset testdata/agent-evaluation.real-v1.jsonl \
  -output testdata/agent-evaluation.real-v1.observations.jsonl
go run ./cmd/mesguard-agent-eval \
  -dataset testdata/agent-evaluation.real-v1.jsonl \
  -input testdata/agent-evaluation.real-v1.observations.jsonl

# 扩展样本；SQL v3/v4 仅为观察完整 SQL 链路临时提高预算
go run ./cmd/mesguard-agent-paired-eval \
  -dataset testdata/agent-evaluation.real-v2.jsonl \
  -output testdata/agent-evaluation.real-v2.observations.jsonl
go run ./cmd/mesguard-agent-paired-eval \
  -dataset testdata/agent-evaluation.real-v3.jsonl \
  -output testdata/agent-evaluation.real-v3.observations.jsonl \
  -max-total-tokens 32000
go run ./cmd/mesguard-agent-paired-eval \
  -dataset testdata/agent-evaluation.real-v4.jsonl \
  -output testdata/agent-evaluation.real-v4.observations.jsonl \
  -max-total-tokens 32000
~~~

SQL v3/v4 还要求运行进程通过 `MESGUARD_SQLSERVER_PASSWORD` 注入只读账号密码；
v4 同时需要 PostgreSQL 连接密码。v4 会在本地 PostgreSQL transaction 中插入一版最小
published Catalog，baseline/experiment 共用后 rollback；它不是迁移、seed 或生产发布流程。
命令不会从参数、代码或 observation 写入凭证。

2026-08-02 的 `agent-real-v1` 首条真实工单样本输入 Token 为 baseline 5960、
experiment 4640，配对降幅 22.15%；两次均完成 `read_external_case -> case_snapshot`，
没有调用禁止的代码 Tool。`agent-real-v2` 扩展为工单、代码调查、GitHub 降级 3 条样本：
两种 variant 的路由/首 Tool/Evidence 覆盖均为 1/1、禁止调用为 0；代码调查实际执行
代码 Tool，但两边均因默认 16000 Token 预算停止，`failureTypes` 为
`token_budget_exhausted: 2`。v2 的 paired 输入 Token 为 baseline 28651、experiment
35573，experiment 更高，不能把工单样本的 Token 降幅外推到代码任务。

SQL v3 使用 `-max-total-tokens 32000` 仅观察完整 SQL 对象定义链路：baseline/experiment
均执行 `read_external_case -> get_database_object_definition`，并取得
`case_snapshot + sql_object_definition`；输入 Token 为 6689/5228，耗时为 5.70/5.35 秒。
TTFT 尚未测量，v3 仍是 1 条样本，不写入简历指标。

SQL v4 使用同样的临时预算观察 Catalog 搜索和受限只读查询：baseline/experiment 均执行
`read_external_case -> search_schema_catalog -> execute_readonly_query`，并取得
`case_snapshot + schema_catalog + sql_query`；输入 Token 为 12303/14053，耗时为
17.64/24.15 秒，任务完成率均为 1/1，禁止 Tool 调用均为 0。实验侧使用
`sql-investigation` 入口 Skill；paired 输入 Token 和耗时均未下降，因此不外推为效果指标。
评测结束后 PostgreSQL Catalog 版本和条目均为 0，证明夹具已回滚。

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
| `ticket-diagnosis` | `read_external_case`、`search_knowledge` | 读取工单、按证据缺口查询企业知识并规划调查 |
| `sql-investigation` | 对象定义、`search_schema_catalog`、`execute_readonly_query`（窄版本当前） | 核对业务数据和数据库执行证据 |
| `code-investigation` | GitHub MCP 只读 Tool | 定位代码与提交 |
| `attachment-investigation` | `read_attachment`、OCR/VLM 结果读取 | 按需分析截图、PDF和日志附件 |
| `knowledge-qa` | `search_knowledge` | 全局与个人知识库问答；不获得诊断数据源能力，结果必须携带文档/版本/Chunk/哈希定位 |
| `web-research` | `web_search`、`fetch_public_page` | 只查询公开、脱敏问题并保留引用 |
| `log-investigation` | `search_logs`、`get_log_context` | 接入可用的只读日志源 |
| `sql-optimization-lab` | LAB 查询、计划比较和清理流程 | 后续受控优化实验 |

Web Search 不默认获得工单原文。进入 `web-research` 前必须把公司名、客户名、工单号、内部地址、SQL/日志原文和代码片段移除，只允许搜索通用产品概念、公开错误码和公开依赖资料。

当前 `internal/webresearch` 已把这一要求固化为服务端出口策略：工单字段和管理员词典执行确定性
替换，凭证、连接串和结构化私有内容直接拒绝，脱敏后技术信号不足也拒绝。SearchProvider 和
ContentProvider 只能接收策略构造的 `PublicQuery` 和 `PublicURL`，不能接收任意字符串。LLM 改写
不参与安全判定，命中审计只记录类别和数量，不能把敏感原值写入日志。

`web_search` 只返回候选摘要和 Run 内随机 `resultId`，不产生 Evidence；`fetch_public_page` 只接受
同一 Run 已授权的 `resultId`，成功后才产生 `web` EvidenceItem。快照保存 URL、域名、标题、
可得的页面时间、抓取时间、来源等级、正文哈希和截断状态，引用门禁会重算正文 SHA-256。单 Run
预算为最多 2 次 Search/3 次 Fetch，重复 Fetch 命中内存快照而不再次计费。URL Gate 拒绝非 HTTP(S)、
用户信息、异常端口、localhost、私网/链路本地/保留地址和混合 DNS；提交 URL 与 Provider 最终
报告 URL 都要复验。Direct Content Provider 在跟随每一跳重定向前复用 URL Gate；Firecrawl 内部
不可观测的中间重定向仍依赖供应商自身 SSRF 防护，这是该适配器的明确边界。

网页内容经 Provider 提取、字符清洗和大小限制后，以 `untrustedContent=true` 进入模型。Firecrawl
使用 `onlyMainContent` Markdown，Direct Provider 只提取有限可见文本且不执行脚本；System Prompt
和 Skill 明确禁止执行网页指令、泄露上下文或扩大 Tool 权限。这属于数据/指令隔离和确定性授权
共同防护，不声称能够语义识别所有 Prompt Injection 文本。

代码调查不维护应用内逐仓库、组织或分支授权表。fine-grained PAT 或 GitHub App Installation 决定 GitHub 返回的可见范围；`search_repositories` 用于按查询发现候选仓库，后续 Tool 直接使用选中的 owner/repo/ref/sha。MESGuard 只保留代码相关的只读 Tool，不把凭据在 GitHub 侧拥有的范围缩窄为单仓库。最终证据记录实际仓库、Commit SHA、文件和行号。

## 前端与 API 交接

诊断后端稳定后同步更新 `docs/design/openapi.json` 和前端交接说明，至少覆盖：

- 创建、查询、取消和重试 DiagnosisTask；
- TaskEvent SSE、`Last-Event-ID` 断线补读和心跳；
- `agent.started`、`skill.loaded`、`tool.completed`、`evidence.updated`、依赖降级、报告完成等事件；
- 报告、证据引用、Tool 执行摘要和 Token/耗时信息；
- 管理员数据源、能力策略和依赖状态页面；
- GitHub MCP 暂时不可用、SQL 源不可达、日志证据缺失等明确状态。

诊断页面应以“任务时间线 + 证据 + 最终报告”为主。时间线可以默认折叠并展示结构化、脱敏的调查过程，但不展示模型原始 `ReasoningContent`、完整 Prompt、凭证或原始敏感 Tool 参数。最终报告一次性发布；SSE 先传阶段进度，知识问答另行支持逐 Token 流式输出。
