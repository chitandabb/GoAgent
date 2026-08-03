# MESGuard 领域模型与状态机

## 文档状态

- 本文描述 MESGuard 的目标领域模型和状态转换规则。
- 当前仓库已实现认证与统一 ExternalCase 领域模型；诊断任务及后续状态机尚未全部实现。
- 本文先确定业务语义，不绑定具体 PostgreSQL 表名、ORM 模型或 HTTP 字段。
- 数据库字段、索引和迁移方式在后续 database.md 中单独确定。

## 建模原则

MESGuard 的核心事实是：某一次诊断任务在某个时间点，基于某个工单快照和一组数据源，采集了哪些证据，并生成了什么报告。

因此建模遵守以下原则：

1. 外部 MES/ERP 仍然是工单和业务数据的来源，MESGuard 不回写。
2. 工单快照、诊断任务、执行记录、证据和报告均属于 MESGuard 自己的事实数据。
3. 同一外部工单可以有多次诊断，每次诊断都是独立任务，旧任务不被覆盖。
4. 执行状态、报告结论状态和人工反馈状态分开保存。
5. 重要执行记录追加保存，不能依赖 Redis 或浏览器连接恢复。
6. 任务、步骤、工具调用和证据都要关联操作者、数据源、时间和版本。
7. 状态转换只能通过明确的领域操作发生，不能由任意 HTTP 请求直接修改状态字段。

## 领域对象总览

~~~text
User
  │ owns / starts
  ▼
ExternalCase ── creates ──> CaseSnapshot
                                  │
                                  ▼
                           DiagnosisTask
                           /      |       \\
                          /       |        \\
                         ▼        ▼         ▼
                 DiagnosisStep ToolExecution EvidenceItem
                         │                     │
                         └──────────────┬──────┘
                                        ▼
                                 DiagnosisReport
                                        │
                                        ▼
                                  ReportReview

Conversation ── has ──> Message ── references ──> Attachment
                                              │
                                              ├─ session scope
                                              └─ personal knowledge scope

KnowledgeDocument ── has ──> KnowledgeChunk ── has ──> Embedding
~~~

## 核心领域对象

### User

表示 MESGuard 使用者。一期只有 analyst 和 admin 两个角色。User 负责确定谁发起任务、谁可以看到会话和附件、谁提交报告反馈，以及谁执行管理员操作。

用户删除或禁用后，历史任务和审计记录不能被物理级联删除。历史记录应保留不可用的操作者引用或匿名化标识。

### ExternalCase

表示外部 MES/ERP 中的工单身份，不保存外部系统的全部内容。它至少能够定位来源数据源、外部工单标识和外部系统类型。

ExternalCase 不是 MESGuard 的工单主数据。外部工单变化时，ExternalCase 身份保持稳定，新的诊断通过新的 CaseSnapshot 记录当时内容。

### CaseSnapshot

表示发起诊断时从外部 SQL Server 读取的工单快照，至少包含结构化字段、问题描述、来源标识、读取时间、内容版本或哈希，以及脱敏和截断信息。

CaseSnapshot 创建后不随外部工单变化而修改。需要重新诊断时，创建新的快照。

### DataSource

表示可以被 MESGuard 访问的外部数据源元信息。一期模型支持多个数据源，用于区分工单库、生产库和产品库，但实际只启用一个配置。

数据源连接必须使用只读账号，凭证由配置或密钥管理系统提供，不进入业务日志和模型上下文。一次 DiagnosisTask 保存它实际使用过的数据源集合，不能只依赖当前默认数据源。

### DiagnosisTask

表示一次完整的异步诊断尝试，是最重要的聚合根。Task 关联发起人、请求幂等标识、ExternalCase、CaseSnapshot、用户补充说明、诊断范围、数据源集合、附件、步骤、工具调用、证据和报告。

同一个 Task 内部允许步骤重试，但用户点击“重新诊断”时必须创建新的 DiagnosisTask，并通过 retry_of 或等价关系关联旧任务。

### DiagnosisStep

表示诊断流程中的一个可追踪步骤，例如读取工单、读取附件、查询业务状态、检索案例或生成报告。

Step 记录稳定的步骤类型、显示名称、执行顺序、当前状态、尝试次数、最后错误、输入输出摘要、开始时间、结束时间和耗时。

步骤输出如果构成诊断依据，应创建 EvidenceItem，不能只保存在日志中。

### ToolExecution

表示某个具体工具的一次调用，包括模型调用、数据库查询、附件读取、知识检索、代码搜索或 Web Search。

它记录工具名称和版本、脱敏后的参数摘要、调用步骤或模型标识、开始结束时间、耗时、超时、返回行数、截断标记、错误分类、重试次数，以及模型调用的 Token 和成本信息。

原始敏感参数不能因为方便调试而写入普通应用日志。

### EvidenceItem

表示可以支持报告判断的证据快照或引用。来源可以是 CaseSnapshot、受控 SQL Server 查询、附件的图片/OCR/解析片段、全局或个人知识库片段、只读代码搜索结果，或脱敏后的公开网页。

EvidenceItem 至少保存来源类型、来源标识、采集时间、内容摘要或快照、内容哈希、脱敏和截断状态，以及可展示的引用位置。

证据创建后默认不可原地修改。发现错误时创建更正或补充证据，并在报告中标记旧证据不可用。

### DiagnosisReport

表示一次成功完成的诊断输出。报告不等于事实结论，而是基于当前证据生成的可审核判断。

报告包含业务摘要层和技术证据层。报告必须关联使用过的 EvidenceItem；没有证据引用的结论只能作为待验证假设，不能显示为已确认根因。

### ReportReview

表示使用人对报告的业务反馈，不是审批流，也不改变外部 MES/ERP 状态。

一期反馈值为：

~~~text
pending → adopted
        → partially_adopted
        → rejected
~~~

反馈记录操作者、时间和可选说明，用于离线评测和案例整理。若允许重复反馈，应保留历史事件并单独计算当前有效反馈。

### Attachment

表示用户上传的图片、PDF、日志或其他文件。原始二进制保存在 MinIO，数据库只保存元数据和权限关系。

Attachment 具有两种归属范围：session（只属于当前会话或诊断任务）和 personal（属于上传用户的个人知识库）。一期不支持用户上传后申请进入全局知识库。附件被报告引用，也不会自动改变归属范围。

### Conversation 与 Message

Conversation 表示知识助手的一次对话，Message 表示用户、模型和工具之间的消息。

Message 保存文字、角色、顺序、生成状态和附件引用。消息不保存永久对象 URL 或完整 Base64。上下文组装器只提供轻量附件清单，模型需要查看时通过 read_attachment 获取受限文本或多模态内容。

M2初期消息发送后不可编辑，也不支持对话分支。用户修正问题时追加新消息，重新生成回答同样创建新的追加记录。动态上下文摘要属于可重建的派生数据，不能覆盖或删除原始消息。

知识助手消息的生成状态与诊断任务状态分开：聊天停止生成后保留已生成内容，并把消息标记为 interrupted。

### KnowledgeDocument、KnowledgeChunk 与 Embedding

KnowledgeDocument 表示全局或个人知识库中的逻辑文档，KnowledgeChunk 表示可检索片段，Embedding 表示某个模型和版本生成的向量。

同一对象的可见范围必须在检索前过滤：当前会话文件、当前用户个人知识库，以及用户有权限访问的全局知识库。不能先召回所有向量再在应用层补做权限过滤。

文档重处理或 Embedding 模型升级时创建新的版本，不能静默覆盖仍被历史报告引用的旧版本。

#### 混合文档元素与解析管线

复杂文档不能只按纯文本处理。解析器需要先把文档拆成可追踪的元素：普通文本段落、表格、软件截图、统计图、流程图、架构图和装饰性图片。每个元素都保留所属文档、页码、章节路径和页面区域。

元素按以下规则分流：

~~~text
普通文本       → 清洗、分块、Embedding
结构化表格     → 恢复行列关系，转为 Markdown/HTML/JSON，再进入 Embedding
扫描表格       → OCR 识别文字，再做表格结构恢复，原图保留
软件截图       → OCR 提取错误码和文字，必要时调用 VLM 描述界面状态
复杂图表/示意图 → VLM 生成结构化语义描述
装饰性图片     → 默认不进入 Embedding，仍可作为原文附件保存
~~~

规则无法判断时，可以使用轻量 ONNX 分类器做图片元素路由；ONNX 只负责便宜的分类，不是唯一决策者。低置信度结果需要回退到规则或 VLM 判断。Go 服务通过 ONNX Runtime 的原生运行库或Go封装调用模型，不引入 Python 微服务；部署时必须验证对应的动态库和执行提供程序。

VLM 描述必须包含类型、标题或图注、摘要、实体、关系、关键数值和识别警告等结构化字段。统计图、流程图、架构图和软件截图使用不同的描述字段，不能只保存一段无法核对的自由文本。

原始图片继续保存到 MinIO，数据库保存附件标识和解析元数据。用于向量检索的混合块由以下内容组成：

~~~text
图片或表格的结构化描述
+ 图片标题、图注和所属章节
+ 图片前后相邻正文的上下文摘要
+ 原始附件 ID、页码和页面区域
~~~

混合块使用文本 Embedding 模型进入 pgvector。当前使用的 text-embedding-v4 是文本向量模型，因此图片通过描述文本参与语义召回，而不是直接把原始图片当作文本向量输入。

每个元素或混合块至少记录：document_id、document_version、page_number、element_index、bounding_box、element_type、attachment_id、section_path、parser_version、classifier_version、ocr_version、vlm_model、vlm_prompt_version、embedding_model 和 content_hash。处理组件升级时创建新的解析版本，不能覆盖历史报告所引用的描述。

检索命中后默认返回混合块文本、来源页码和 attachment_id，不自动把原图塞入模型上下文。模型需要核对原图时，才通过受控的 read_attachment 工具读取指定图片或页面。前端可以根据元数据展示原图缩略图，报告引用仍然指向具体文档、页码和元素。

这一管线的评测需要比较“仅文本/OCR”与“元素分流 + 结构化表格 + VLM 描述 + 原图关联”两种方案，至少记录 Image Recall@K、Table Recall@K、MRR、引用准确率、视觉问答准确率、路由准确率、VLM 调用率、处理延迟和成本。

### TaskEvent 与 OutboxEvent

TaskEvent 是面向进度、审计和 SSE 回放的追加事件。OutboxEvent 是在同一 PostgreSQL 事务中与任务状态或业务事实一起写入的待发布事件。

二者职责不同：TaskEvent 说明任务发生了什么；OutboxEvent 保证待发送的 RabbitMQ 消息不会因为 API 进程宕机而永久漏发。OutboxEvent 发布成功后可以标记已发送，但不能删除任务事实或 TaskEvent。

## DiagnosisTask 状态机

### 状态定义

~~~text
pending
    已创建，等待 Outbox 发布或 Worker 领取
running
    Worker 已领取并正在执行步骤
cancel_requested
    用户请求取消，等待 Worker 协作停止
succeeded
    所有必要步骤完成并生成报告
failed
    系统执行失败，未能生成正式报告
cancelled
    已停止后续执行，保留已有记录，不生成正式报告
~~~

### 允许的转换

~~~text
pending ───────────────> running
   │                         │
   │                         ├──────────────> succeeded
   │                         ├──────────────> failed
   │                         └──────────────> cancel_requested
   │                                                  │
   └──────────────> cancel_requested <────────────────┘
                                      │
                                      ▼
                                  cancelled
~~~

cancel_requested 是过渡状态，不能长期停留。Worker 要在步骤边界和外部调用返回后检查取消标记。

状态规则：

- pending 任务可以被取消，取消后进入 cancel_requested。
- running 任务可以被取消，但不能回滚已经产生的证据。
- succeeded 和 cancelled 是终态，不允许回到 pending 或 running。`failed` 表示本次自动执行失败；只有 admin 通过受审计的恢复用例，且任务尚未生成正式报告时，才允许 `failed → pending`，用于重用同一任务和消息ID继续执行。
- succeeded 只代表执行链完成，不代表报告结论正确。
- failed 表示系统故障或不可恢复的执行错误，不表示业务上没有找到根因。
- cancelled 不生成正式报告，但保留已完成步骤、工具调用和证据。
- 普通用户对已结束任务再次诊断必须创建新任务，不能原地重置状态。admin 的失败任务恢复是运维补偿，不等价于重新诊断，必须追加 TaskEvent 和审计记录。
- 任务状态和生命周期事件是编译期领域协议，不是运行配置。Go 代码通过强类型
  `TaskStatus`、`TaskEventType` 及唯一的终态状态/事件映射维护；HTTP、Worker 和 PostgreSQL
  Adapter 不得各自硬编码终态集合。超时、重试和心跳可以配置，状态机枚举不能配置化。
- `succeeded`、`failed`、`cancelled` 在当前执行阶段都产生终态事件并关闭 SSE；管理员从
  `failed` 恢复时追加 `task_requeued`，客户端重新连接后从事件游标继续读取。

## DiagnosisStep 状态机

~~~text
pending → running → succeeded
              │  ├→ failed
              │  ├→ cancelled
              │  └→ skipped
~~~

- 一个任务可以有多个步骤，但同一时刻只有符合依赖关系的步骤可以运行。
- skipped 必须记录跳过原因，例如证据已经足够或步骤不适用于当前问题。
- 步骤重试应增加尝试次数和新的 ToolExecution，不覆盖原失败记录。
- 关键步骤失败通常终止任务，非关键步骤可以记录限制后继续。
- 任务取消后，未开始步骤标记为 cancelled 或 skipped，不能伪造为成功。

## ToolExecution 状态机

~~~text
requested → running → succeeded
                │  ├→ failed
                │  ├→ timed_out
                │  └→ cancelled
                └→ retried_by_new_attempt
~~~

工具错误需要区分：

~~~text
validation_error   参数不符合工具契约，不应盲目重试
permission_denied  没有权限，重试不能解决
timeout            外部调用超时，可按策略有限重试
transient_error    网络或服务暂时异常，可有限重试
business_empty     查询成功但没有结果，不是系统失败
unsafe_request     SQL、路径或参数被安全策略拒绝
~~~

business_empty 和 unsafe_request 都不应当成普通网络错误重试。

## 报告结论状态

报告生成后单独保存结论状态：

~~~text
conclusive
    证据链足以支持明确判断
probable
    有较强候选原因，但仍需要人工或开发人员验证
inconclusive
    证据不足，系统明确拒绝确定根因
~~~

- conclusive 必须至少关联一个直接证据，并列出关键依据和限制。
- probable 必须区分最可能原因和尚未排除的替代原因。
- inconclusive 必须说明已经检查了什么、缺少什么，以及下一步建议。
- 证据不足不是任务失败，正常完成的任务可以生成 inconclusive 报告。
- 任何结论状态都不能授权系统自动修改外部数据。

## Attachment 状态机

~~~text
uploading → uploaded → processing → ready
                         │             │
                         └→ failed     └→ deleted / expired
~~~

- 只有 ready 附件可以被 read_attachment 或索引 Worker 读取。
- uploaded 表示对象已到 MinIO，但文件类型、大小和哈希仍需服务端确认。
- failed 要保存失败原因，允许用户重新上传新对象，不覆盖旧对象记录。
- session 附件按会话或任务保留策略清理；清理前如果仍被报告引用，应保留引用失效状态。
- personal 附件由用户主动删除或按生命周期策略清理，删除不应删除历史消息文本。

## Message 生成状态机

~~~text
pending → streaming → completed
              │  ├→ interrupted
              │  └→ failed
~~~

interrupted 表示用户主动停止、浏览器取消请求或服务端协作取消，已经生成的内容必须保留。它不应伪装成完整回答，也不应自动写入全局知识库。

## 任务取消与 SSE 事件

SSE 只负责服务端向浏览器发送进度，关闭 SSE 连接不等于取消任务。

~~~text
GET /tasks/{id}/events
    只建立事件监听

POST /tasks/{id}/cancel
    写入 cancel_requested 并产生 TaskEvent
~~~

任务事件需要带递增序号或等价游标，允许浏览器断线后从最后位置恢复。事件至少覆盖：

~~~text
task_created
task_started
step_started
tool_completed
evidence_created
task_cancel_requested
task_cancelled
task_failed
task_succeeded
report_created
~~~

事件是进度和审计记录，不是唯一事实来源。任务当前状态仍以 DiagnosisTask 为准。

## RabbitMQ 重复投递与幂等规则

RabbitMQ 消息只携带任务定位信息和必要版本，不携带工单全文、图片或证据大对象。

Worker 收到消息后必须：

1. 根据任务 ID 读取 PostgreSQL 当前状态。
2. 通过数据库条件更新或租约机制原子领取任务。
3. 已经是 succeeded 或 cancelled 的任务直接确认消息，不重复产生报告；普通消费遇到 failed 任务也直接确认，只有 admin 恢复事务重新置为 pending 后才允许再次领取。
4. 正在被其他 Worker 执行的任务不重复领取。
5. 处理成功后再确认 RabbitMQ 消息。
6. 进程崩溃时允许消息重新投递，依靠任务状态、步骤尝试号和工具幂等键避免重复业务结果。

重复投递是正常情况，不应依赖 RabbitMQ 只投递一次的假设。

## 领域不变量

- 一个诊断任务最多有一个正式报告版本；重新诊断创建新任务。
- succeeded/cancelled 任务不能重新运行；failed 任务只能通过 admin 受审计恢复重新运行，普通用户重新诊断必须创建新任务。
- 取消任务不产生正式报告。
- 任务执行成功不等于根因结论已确认。
- 报告结论必须引用证据，证据不足必须使用 inconclusive。
- EvidenceItem 创建后不可静默修改，历史报告引用必须可追溯。
- 个人附件和个人知识库不能被其他用户检索。
- 外部 SQL Server 只读，任何领域操作都不能产生回写命令。
- Redis 丢失不能导致任务、报告、事件事实丢失。
- RabbitMQ 重复消息不能导致重复报告、重复扣费或重复状态推进。
- Web 浏览器断线不能改变任务执行状态。

## 后续设计输入

~~~text
database.md
  将这些对象映射为表、外键、唯一约束和索引

messaging.md
  详细定义 Outbox、RabbitMQ 消息、租约、重试和死信（已完成）

api.md
  将领域操作映射为命令、查询和 SSE 事件（已完成）

ai-diagnosis.md
  将 DiagnosisStep 和 ToolExecution 映射为 Eino Graph/Agent 节点

frontend.md
  将状态机映射为页面按钮、时间线和可恢复交互
~~~
