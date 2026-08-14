# MESGuard 系统架构

## 文档状态

- 本文描述 MESGuard 的目标系统架构和运行边界。
- 当前仓库已完成 Web 基础、认证、M1-A1 只读 ERP 工单后端，以及 StepFun 模型、GitHub MCP 只读接入、单 ADK ChatModelAgent 内循环和薄外层 Evidence Gate Graph；P7 已接入任务创建、快照、TaskEvent/Outbox 原子事实、事件 JSON 补读、取消命令、Outbox Relay、Diagnosis Worker、正式证据/报告持久化和报告反馈基础。
- Diagnosis Worker 已具备 RabbitMQ 手动 ACK、三级 TTL 重试、最终死信、租约续期和 fencing 终态提交。MinIO、Ingestion Worker、正式报告读取 API 和 TaskEvent SSE 产品链路仍按里程碑实现；独立 Agent 或单次 Worker 烟雾验证不能描述成完整诊断功能已上线。
- 本文定义组件职责和数据流，不展开数据库字段、RabbitMQ交换机、HTTP字段或Eino Graph节点。

## 架构目标

MESGuard需要同时满足以下目标：

1. HTTP请求快速返回，长时间诊断在后台可靠执行。
2. 页面断开、API重启或RabbitMQ重复投递时，不丢失任务事实或重复生成报告。
3. 外部MES/ERP SQL Server始终只读，模型不能绕过查询安全边界。
4. 附件原文件、业务元数据、向量和临时缓存各自存入适合的存储。
5. Redis故障只降低实时性和缓存能力，不能导致任务或报告丢失。
6. 诊断、知识问答和文档入库使用不同执行模型，避免相互阻塞。
7. 本地和小规模生产都能使用Docker Compose运行，不提前引入Kubernetes。
8. 系统能够记录日志、指标、Token、成本和任务事件，支持故障定位和效果评测。

## 系统上下文

~~~text
公司用户
  │
  │ HTTP（当前）/ HTTPS（未来）
  ▼
Nginx + React
  │
  ▼
MESGuard API
  ├─ PostgreSQL
  ├─ Redis
  ├─ MinIO
  ├─ 外部 SQL Server（只读）
  ├─ 阶跃星辰 Chat/VLM
  └─ 百炼 Embedding/Rerank

PostgreSQL Outbox
  │
  ▼
RabbitMQ
  ├─ Diagnosis Worker
  └─ Ingestion Worker

Diagnosis Worker
  ├─ Eino Graph / ChatModelAgent
  ├─ 外部 SQL Server（只读）
  ├─ MinIO附件
  ├─ 全局/个人知识库
  └─ 模型服务

Ingestion Worker
  ├─ 文档解析与OCR
  ├─ ONNX Runtime（M2增强）
  ├─ VLM图片描述
  ├─ Embedding/Rerank
  ├─ MinIO
  └─ PostgreSQL + pgvector
~~~

## 运行拓扑

### 本地开发

开发者在Windows上使用Docker Desktop运行Linux容器：

~~~text
Windows 10/11
├─ GoLand
├─ Vite Dev Server（前端开发阶段）
└─ Docker Desktop / WSL2
   └─ Docker Compose
      ├─ PostgreSQL + pgvector
      ├─ SQL Server演示库
      ├─ Redis
      ├─ RabbitMQ（M1）
      ├─ MinIO（M1）
      ├─ mesguard-api
      ├─ outbox-relay（M1）
      ├─ diagnosis-worker（M1）
      ├─ ingestion-worker（M2）
      └─ Nginx + React（前端阶段）
~~~

本地通过HTTP访问。开发环境可以映射数据库和中间件端口，方便调试，但这些映射不能原样视为生产安全配置。

### 小规模生产目标

生产宿主机贴合公司Windows Server环境，但Linux容器运行在显式维护的Hyper-V Linux虚拟机中：

~~~text
Windows Server
├─ 公司现有 MES/ERP SQL Server
└─ Hyper-V Linux VM
   └─ Docker Engine + Docker Compose
      ├─ Nginx + React
      ├─ mesguard-api
      ├─ outbox-relay
      ├─ diagnosis-worker
      ├─ ingestion-worker（M2）
      ├─ PostgreSQL + pgvector
      ├─ RabbitMQ
      ├─ Redis
      └─ MinIO
~~~

当前复现和受控内网部署先使用HTTP。以后接入公司内部CA、公司反向代理或公网域名时，再在Nginx或上游网关终止TLS。Docker Desktop不作为Windows Server生产运行时。

该方案是单机部署，不宣称高可用。Windows Server、Linux VM或虚拟磁盘故障都会使整套MESGuard暂时不可用。

## 代码库与构建产物

MESGuard保持一个Go代码库和一套领域模块，不拆成独立微服务仓库，也不为API和Worker建立服务间RPC。

M1运行三个Go角色，M2增加Ingestion Worker后变为四个：

~~~text
API角色
  HTTP、SSE、认证、查询、知识助手流式对话

Outbox Relay角色
  PostgreSQL Outbox领取、Publisher Confirm、RabbitMQ发布

Diagnosis Worker角色
  RabbitMQ消费、Eino诊断Graph、报告生成

Ingestion Worker角色（M2增加）
  文档解析、OCR、ONNX分类、VLM描述、Embedding和索引
~~~

API、Outbox Relay和Diagnosis Worker使用同一个基础Go后端镜像，通过不同启动角色运行。Ingestion Worker仍来自同一代码库，但使用包含ONNX Runtime和文档解析依赖的增强镜像，避免扩大API镜像和运行时攻击面。

每个运行角色有独立的手动依赖装配入口和优雅关闭顺序，共享领域代码、仓储接口和基础设施适配器。

## 组件职责

### Nginx与React

Nginx是浏览器唯一业务入口：

- 托管React静态资源；
- 反向代理API；
- 转发SSE并关闭不合适的代理缓冲；
- 以后承担TLS终止和静态资源缓存。

生产环境不让浏览器直接访问Go API、MinIO、RabbitMQ或数据库。本地开发使用Vite Dev Server代理API。

### MESGuard API

API负责：

- 本地账号认证和analyst/admin权限；
- 数据源安全元数据、低频Schema Catalog扫描和版本发布；
- 外部工单列表与详情查询；
- 创建工单快照、诊断任务、TaskEvent和OutboxEvent；
- 查询任务、证据、报告和反馈；
- SSE事件读取和断线续传；
- 附件上传流程和权限校验；
- 知识助手的直接流式执行；
- 健康检查和管理员依赖状态。

API不在HTTP请求内执行完整诊断或文档入库。

### Outbox Relay

Outbox Relay负责：

- 使用短事务和`FOR UPDATE SKIP LOCKED`领取待发布OutboxEvent；
- 写入有限租约，使Relay崩溃后其他实例可以重新领取；
- 把诊断和文档入库消息路由到各自RabbitMQ队列；
- 等待Publisher Confirm后标记`published_at`；
- 发布失败时记录错误并按退避时间重新尝试；
- 暴露未发布数量、最老事件年龄、发布失败和租约过期指标。

Relay不执行业务诊断，也不修改任务结论。它可能重复发布同一个`message_id`，Worker必须通过PostgreSQL状态、租约和幂等键避免重复业务结果。

### Diagnosis Worker

Diagnosis Worker负责：

- 消费RabbitMQ诊断消息；
- 原子领取任务并保护幂等；
- 执行Eino诊断Graph和受限Agent；
- 调用SQL、知识检索、附件读取、代码搜索等工具；
- 保存步骤、工具调用、证据、报告和任务事件；
- 处理取消、超时、有限重试和失败。

消息只携带task_id、事件类型和必要版本，不携带工单全文、图片或大段证据。

### Ingestion Worker

Ingestion Worker在M2引入，负责：

- 从MinIO读取待处理文档；
- 拆分文本、表格、截图、图表和示意图；
- 执行OCR与表格结构恢复；
- 使用规则和本地ONNX版面模型进行页面/区域两级路由；
- 调用VLM生成结构化图片描述；
- 创建KnowledgeDocument、KnowledgeChunk和解析版本；
- 调用Embedding并写入pgvector；
- 记录处理耗时、错误、模型版本和成本。

它使用独立RabbitMQ队列和资源限制，避免大型文档处理阻塞工单诊断。

M2-A7 中的 ONNX 能力是专用 `LayoutRouter`，不是完整 Docling 运行时，也不承担 OCR/VLM。
它在 Knowledge Worker 内结合原生文本质量与页面区域检测结果，输出带 bounding box、类型、置信度
和 reason code 的路由决策；OCR 与 VLM 继续通过云端配置端口执行。Eino 可以提供 Transformer 与
Callback 适配，但持久化事实仍使用 MESGuard 的 Element/Artifact 契约。

### PostgreSQL与pgvector

PostgreSQL是MESGuard事实来源，保存：

- 用户和权限；
- 外部工单身份与工单快照；
- 诊断任务、步骤、工具调用和证据；
- 报告与人工反馈；
- TaskEvent与OutboxEvent；
- 会话、消息、附件元数据；
- 知识文档、Chunk、向量元数据和评测结果。

pgvector用于文本Embedding检索。任务事实、报告和事件不能只存Redis或RabbitMQ。

### RabbitMQ

RabbitMQ负责异步任务分发和削峰，不是任务事实来源。

它承载诊断和文档入库队列，并支持消费者确认、有限重试和死信处理。消息可能重复投递，Worker必须根据PostgreSQL状态和幂等键避免重复业务结果。

### Redis

Redis是可降级组件，负责：

- 缓存；
- 限流计数；
- SSE新事件实时通知；
- 可丢失、可重建的短期状态。

Worker先把TaskEvent写入PostgreSQL，再通过Redis通知API“某任务有新事件”。API收到通知后仍从PostgreSQL读取真实事件。Redis不可用时，SSE退化为定时查询PostgreSQL。

### MinIO

MinIO保存图片、PDF、日志和原始知识文档。PostgreSQL只保存附件元数据、object key、哈希、权限、处理状态和引用关系。

对象默认私有。聊天上下文不保存永久URL或Base64，只保存attachment_id和轻量元数据。模型需要查看时，通过受控read_attachment工具读取有限文本或多模态内容。

### 外部SQL Server

外部SQL Server继续作为MES/ERP工单和业务数据来源。它不要求与MESGuard部署在同一台服务器，只要求Diagnosis Worker通过公司内网、专线或VPN访问目标数据库端口。API和Diagnosis Worker通过同一只读适配层访问，但每个进程使用独立连接池。

共享适配层负责：

- 只读账号；
- 连接、查询和事务超时；
- 表和字段白名单；
- Schema Catalog；
- SQL安全校验；
- 行数、结果大小和并发限制；
- 字段脱敏和审计记录。

如果目标客户网络不允许MESGuard主动连接，一期为该网络域部署独立MESGuard实例；后续确有跨网络集中诊断需求时，再增加客户侧只出站Connector。Connector只能提供版本化的受控数据源和日志动作，不能成为任意数据库代理或远程Shell。详细权限和Tool边界见`diagnostic-tools.md`。

MESGuard不向SQL Server回写工单、报告或业务状态。

## Eino与模型服务边界

Eino是AI组件和编排基础，项目不重复定义另一套ChatModel或Embedding接口。

~~~text
诊断Graph / 知识助手Agent
  ↓
Eino ChatModel、Embedding、Retriever、Tool、Graph
  ↓
项目级装配与治理
  ↓
阶跃星辰、百炼或未来本地模型
~~~

项目的AI平台层只负责：

- 从TOML和环境配置创建Eino组件；
- 根据业务用途注册和选择模型；
- 配置BaseURL、密钥、模型名和超时；
- 通过Callbacks记录Token、缓存、耗时和成本；
- 管理Prompt、工具和模型版本；
- 在发送到远端模型前执行权限判断和必要脱敏；
- 限制重试、工具调用次数和最大上下文。

阶跃星辰承担聊天与多模态模型调用；百炼承担text-embedding-v4和Rerank。具体兼容适配需要通过POC验证，不能因为接口声称兼容就跳过返回字段、流式和Token统计测试。

## 核心数据流

### 发起诊断

~~~text
用户选择外部工单
  ↓
API只读查询SQL Server
  ↓
用户确认数据源、范围和附件
  ↓
PostgreSQL同一事务
  ├─ CaseSnapshot
  ├─ DiagnosisTask(pending)
  ├─ TaskEvent(task_created)
  └─ OutboxEvent(unpublished)
  ↓
API立即返回task_id
  ↓
Outbox Relay发布RabbitMQ
  ↓
Diagnosis Worker原子领取任务
  ↓
Eino Graph执行并持续写入步骤、证据和事件
  ↓
生成DiagnosisReport
~~~

API不能采用“提交数据库事务后直接发RabbitMQ”的双写方式，否则进程可能在两步之间崩溃并永久漏发任务。

### 进度与SSE

~~~text
Diagnosis Worker
  ↓ PostgreSQL事务
TaskEvent
  ↓ 提交成功后
Redis通知
  ↓
API SSE读取PostgreSQL新事件
  ↓
浏览器
~~~

浏览器断线后携带最后事件序号重连，从PostgreSQL补读。关闭SSE连接不会取消任务，取消通过独立HTTP命令写入cancel_requested。

### 知识助手

~~~text
用户发送消息和attachment_id
  ↓
API保存用户Message
  ↓
API直接运行Eino ChatModelAgent
  ├─ 检索当前会话文件
  ├─ 检索个人和全局知识库
  ├─ 按需Web Search
  └─ 按需read_attachment
  ↓
SSE返回文本分片并增量保存assistant Message
~~~

知识问答不经过RabbitMQ，优先保证首Token延迟。用户停止后取消当前Go context，保留已经生成的内容并标记interrupted。API进程意外退出时本轮生成可以失败，不要求自动续写。

### 附件读取

~~~text
浏览器上传
  ↓
API授权并创建Attachment元数据
  ↓
MinIO保存私有对象
  ↓
Message只引用attachment_id
  ↓
模型按需调用read_attachment
  ↓
服务端校验用户、会话、任务和知识范围
  ↓
返回有限文本、指定页或多模态ToolResult
~~~

模型不能传bucket、object key、文件系统路径或对象存储凭证。

### 混合文档入库

~~~text
管理员或用户提交知识文件
  ↓
PostgreSQL写入入库任务和Outbox
  ↓
RabbitMQ document-ingestion队列
  ↓
Ingestion Worker
  ├─ 元素拆分
  ├─ OCR/表格结构恢复
  ├─ 规则和ONNX路由
  ├─ VLM结构化描述
  └─ Embedding
  ↓
PostgreSQL + pgvector
  ↓
MinIO原图与混合块元数据关联
~~~

检索默认返回描述、页码和attachment_id，不自动把原图注入模型上下文。模型确需核对时才调用read_attachment。

## 网络边界

目标Compose至少划分入口网络和内部网络：

~~~text
入口
  Nginx HTTP端口

内部
  API
  Worker
  PostgreSQL
  RabbitMQ
  Redis
  MinIO
~~~

当前规则：

- 普通用户只访问Nginx；
- API、数据库、RabbitMQ、Redis和MinIO不向公司局域网公开端口；
- 管理页面仅通过管理员网络、SSH隧道或受控端口访问；
- Linux VM只向管理网络开放必要的SSH；
- API与Worker通过受控网络只读访问外部SQL Server；
- 访问阶跃星辰和百炼只允许出站HTTPS；
- 当前内网复现使用HTTP，未来通过Nginx或公司网关增加TLS。

## 依赖分级与降级

| 运行角色 | 依赖故障 | 行为 |
| --- | --- | --- |
| API | PostgreSQL不可用 | 未就绪，拒绝核心读写 |
| API | Redis不可用 | 继续运行，缓存失效，SSE轮询PostgreSQL |
| API | RabbitMQ不可用 | 写任务与Outbox，短时保持pending；积压超阈值后拒绝新诊断 |
| API | MinIO不可用 | 普通查询可用，上传、预览和附件读取不可用 |
| API | SQL Server不可用 | 历史报告和知识助手可用，工单浏览和新快照不可用 |
| API | 模型不可用 | 历史查询可用，知识问答快速失败 |
| Outbox Relay | PostgreSQL或RabbitMQ不可用 | 未就绪，不领取或发布 Outbox；租约到期后允许恢复 |
| Diagnosis Worker | PostgreSQL或RabbitMQ不可用 | 未就绪，不领取任务 |
| Diagnosis Worker | SQL Server或模型暂时不可用 | 有限重试和退避，超过阈值失败或进入死信流程 |
| Ingestion Worker | PostgreSQL、RabbitMQ或MinIO不可用 | 未就绪，不领取入库任务 |
| Ingestion Worker | OCR、ONNX或模型不可用 | 按元素和错误类型重试或标记入库失败 |

降级必须对用户和管理员可见。不能把依赖异常静默转换成成功结果。

## 健康检查

每个Go运行角色提供不同语义的检查：

~~~text
/livez
  只判断进程是否存活
  不因为下游故障触发重启风暴

/readyz
  判断当前角色是否具备接收工作的核心条件

/api/v1/admin/system/dependencies
  展示详细依赖状态
  只允许管理员或内部监控访问
~~~

Compose使用healthcheck和depends_on控制启动顺序，但业务代码仍必须处理运行期间的依赖故障。启动顺序不是运行时可靠性保证。

## 配置与密钥

本地开发允许使用不提交Git的.env覆盖TOML配置。模板中的敏感字段保持空白或使用明确的开发占位值。

生产配置遵守：

- 非敏感配置使用只读配置文件；
- 模型密钥、数据库密码和MinIO凭证使用受限权限的环境文件或挂载文件；
- 密钥不写入镜像、仓库、日志、TaskEvent或Prompt；
- API、Diagnosis Worker和Ingestion Worker只获得各自需要的凭证；
- 配置记录版本，但不记录密钥值；
- 修改模型、数据源或安全策略后需要可审计。

## 可观测性

M1先实现埋点，不让完整监控平台阻塞业务闭环。

结构化日志至少关联：

~~~text
request_id
user_id
conversation_id
task_id
step_id
tool_execution_id
trace_id（接入追踪后）
service_role
~~~

核心指标至少包括：

- HTTP请求量、错误率和耗时；
- SSE连接数和断线恢复次数；
- 任务成功、失败、取消和积压；
- 步骤、工具和Worker处理耗时；
- Outbox未发布数量与最老事件年龄；
- RabbitMQ队列深度、重投和死信数量；
- SQL Server查询耗时、超时和截断数量；
- 模型调用耗时、Token、缓存命中和成本；
- 文档入库耗时、OCR/VLM调用率和失败数量；
- PostgreSQL、Redis、RabbitMQ、MinIO和模型依赖状态。

后续接入Prometheus/Grafana和OpenTelemetry或兼容平台。日志、指标和Trace用于定位问题，但不能替代TaskEvent和业务事实。

## 持久化、备份与恢复

Linux VM中的PostgreSQL、RabbitMQ、Redis和MinIO使用独立命名卷或数据卷。高频数据库卷不直接绑定到Windows共享目录。

备份策略至少覆盖：

- PostgreSQL逻辑备份和定期恢复演练；
- MinIO原始附件与知识文件备份；
- 配置文件、Compose文件和镜像版本清单；
- Linux VM配置和虚拟磁盘级灾难恢复；
- 备份复制到Windows宿主机之外的NAS或备份服务器。

RabbitMQ和Redis不作为核心事实备份来源。RabbitMQ丢失后，根据PostgreSQL任务和Outbox进行补偿；Redis丢失后重建缓存和通知状态。

只生成备份文件但从不验证恢复，不算完成备份能力。

## 资源与扩展边界

当前采用单机Compose，不做自动弹性扩容。可以在同一Linux VM内增加Diagnosis Worker副本，但必须依靠RabbitMQ消费者和PostgreSQL原子领取保护并发。

资源策略：

- API保留稳定CPU和内存，避免被文档解析挤占；
- Diagnosis Worker限制并发模型调用和SQL查询；
- Ingestion Worker单独设置更高内存但更低并发；
- PostgreSQL、RabbitMQ和MinIO保留磁盘容量告警；
- Outbox、队列或附件积压超过阈值时停止接收对应新工作。

出现以下条件后再评估拆分主机、托管服务或Kubernetes：

- 单机资源成为持续瓶颈；
- API和Worker需要跨主机独立扩缩容；
- 状态服务需要高可用；
- 维护窗口无法接受；
- 数据量或恢复时间超过单机方案能力。

## 当前实现与目标差距

| 能力 | 当前状态 | 目标里程碑 |
| --- | --- | --- |
| Gin API与统一响应 | 已实现基础骨架 | M1持续扩展 |
| PostgreSQL与Redis连接 | 已实现关键/降级依赖语义 | M1持续验证 |
| Zap结构化日志 | 已实现 | M1增加任务和模型字段 |
| SQL Server演示容器与工单只读适配器 | M1-A1已实现并验证数据库拒写；受 QueryGuard/Catalog/资源限制的窄查询 Tool 已接入；SQL v3/v4 paired 运行已验证对象定义、Catalog 检索、只读查询和运行时 EvidenceItem | M1-D Catalog 扫描/审核/发布管理、正式 EvidenceItem 持久化与更多诊断查询 |
| RabbitMQ与Outbox | PostgreSQL Outbox事实表、任务创建原子写入、`SKIP LOCKED` Relay、有限租约、失败退避、持久主队列和 Publisher Confirm 已实现并通过真实集成测试；诊断与知识入库 Consumer 均已接入有界重试/死信 | M1 |
| MinIO与附件 | 已实现附件/知识原文/Element Artifact引用与有界对象访问 | M1持续扩展 |
| Diagnosis Worker | RabbitMQ Consumer、严格 ACK/重试、Claim/续租/fencing、Agent 执行、取消收尾、EvidenceItem/ReportEvidence/DiagnosisReport 持久化已实现并通过真实 PostgreSQL 联调；进程崩溃演练仍是后续质量门 | M1 |
| React + Nginx | 未实现 | M1 |
| Eino Agent 与 StepFun | 当前单 ADK Agent、TaskScope/Catalog 授权、Skill 渐进加载、usage 和 Evidence Gate 已通过测试与真实烟雾验证；统一 Runtime v2 已确定固定 ToolProfile + RunAccess Guard 合同并进入兼容迁移，旧 paired 指标待生产入口复测；P7 已接入正式 Worker 执行、报告生成/读取、TaskEvent 补读、SSE、取消、恢复和报告反馈基础链路 | M1 |
| 知识助手与pgvector RAG | M2-A1 至 M2-A8 已实现版本化文档、MinIO 入库、PDF/Office 解析、ONNX 页面/区域路由、Embedding、FTS/Vector/RRF、可选 Rerank、`search_knowledge` 和知识 Chunk 引用门禁；Query 改写、Agentic 二次检索、Web Search 和公开知识问答 API 仍待实现 | M2 |
| Ingestion Worker与ONNX | Knowledge Worker、云OCR/VLM、独立Table Processor、PDFium-WASM、PP-DocLayout-M ONNX Router、区域显式路由、自适应DPI、确定性Element合并和Artifact v6 provenance已实现；公开路由集、Windows/Linux资源、OCR像素对照、三图VLM成对测评、NIST真实表格单区smoke和非root专用镜像已记录，精确merged-cell与扩充合并质量测评为增强项 | M2 |
| GitHub MCP代码调查工具 | 已实现只读接入、凭据握手、仓库搜索、仓库树候选读取、固定SHA文件读取、提交追溯和不完整搜索降级；v2 工具级评测覆盖私有C#、GoAgent、GoChat和公开仓库共6条样本，2条真实不完整响应均恢复成功；Agent paired 已覆盖工单、代码调查和 GitHub 降级三类样本，完整数据集仍待扩展 | M1 |
| SQL性能实验室 | 未实现 | M4 |
| Prometheus/Grafana/Trace平台 | 未实现 | 业务埋点后按需接入 |

## 后续专项设计

本文确定组件边界，后续文档继续展开：

~~~text
code-organization.md
  生产与研发工具目录、包依赖方向和待处理结构债务

database.md
  PostgreSQL表、约束、索引和迁移

messaging.md
  RabbitMQ、Outbox、重试、死信和补偿（已完成）

attachments.md
  MinIO对象、上传、解析、清理和read_attachment

ai-diagnosis.md
  Eino Graph、工具权限和Text-to-SQL

api.md
  HTTP命令、查询、错误码和SSE协议（已完成）

frontend.md
  React页面、状态和权限交互

evaluation.md
  合成故障集、RAG和工程指标
~~~
