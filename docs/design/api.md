# MESGuard HTTP API 与 SSE 契约

## 文档状态

- 本文定义 MESGuard M0 和 M1 的 HTTP API、认证、权限、幂等、错误码、分页与 SSE 契约。
- 当前仓库已实现 `/healthz`、本地认证、数据源发现、外部工单列表/详情、诊断任务创建/安全摘要查询、TaskEvent JSON/SSE、任务取消、RabbitMQ Diagnosis Worker 异步执行与报告落库、正式报告查询、管理员失败任务恢复、报告反馈查询/追加、管理员知识文档入库任务控制，以及独立会话创建/列表/详情、消息游标查询、用户消息持久化、异步 `/turns` 受理、回合状态查询和回合事件 JSON/SSE。已实现接口契约见 `api/openapi.yaml`，目标扩展契约见 `design/openapi.json`。
- 会话通过结构化工单/任务引用连接业务上下文；受控 `create_diagnosis_task` 命令服务及其窄 Tool Schema 由独立 Conversation Worker 中的 Agent 调用并复用既有诊断任务应用服务。带任务引用的回合可按需调用内部 `get_diagnosis_task_status`，读取前会再次校验最新消息引用和 owner/admin 权限。`conversation_turns` 已实现请求指纹、客户端 UUID 幂等、queued/running 单活跃约束、Worker 租约 fencing、自动重试和完成回放；`conversation_turn_events` 支持 `afterSeq`/`Last-Event-ID` 断线续传。HTTP 首次受理返回 `202 + turnId + queued`，完成幂等回放返回 `200`。
- 本文是 Handler、Use Case、Repository、React 前端和后续 OpenAPI 文件的共同设计输入。

## 设计原则

1. 资源查询和创建采用 RESTful 风格，复杂状态转换使用明确的领域命令接口。
2. Handler 只负责绑定、认证、权限上下文和响应映射，不能直接修改数据库状态。
3. 前端不能提交任意 `status` 改变任务状态；取消、恢复等操作必须经过领域用例。
4. PostgreSQL 是 Session、任务、事件、报告和幂等事实来源，Redis 只能优化。
5. JSON API 使用统一响应信封；SSE 和二进制附件响应使用各自的标准媒体类型。
6. API 不暴露 SQL Server 表列、MinIO object key、模型 Prompt、密钥或内部错误堆栈。
7. 普通用户看到面向业务的状态和证据；更详细的运维信息只向 admin 开放。

## 版本与命名

业务 API 的基础路径为：

```text
/api/v1
```

健康检查位于版本化业务 API 之外：

```text
/livez
/readyz
```

命名规则：

- URL 资源名使用复数、小写和短横线，例如 `diagnosis-tasks`；
- JSON 字段和查询参数使用 `camelCase`；
- PostgreSQL 字段继续使用 `snake_case`，不能直接泄漏为 API 契约；
- 主键使用 UUID 字符串；
- 时间使用 RFC 3339 UTC，例如 `2026-07-26T03:20:15Z`；
- 枚举值使用小写 `snake_case`，例如 `cancel_requested`；
- 金额或高精度数值不得使用可能丢失精度的 JSON 浮点数。

`/api/v1` 内的兼容性要求：

- 可以新增可选响应字段；
- 不删除或重命名已有字段；
- 不改变已有枚举值语义；
- 新增必填请求字段或不兼容语义时升级 API 主版本；
- TaskEvent、JSONB payload 和消息信封仍使用各自独立的 schema version。

## 统一响应

### JSON 成功响应

除 SSE、文件内容和无正文响应外，成功响应使用：

```json
{
  "code": 0,
  "message": "success",
  "data": {},
  "requestId": "01900000-0000-7000-8000-000000000001"
}
```

HTTP 状态码仍表达协议结果：

| 场景 | HTTP 状态 |
| --- | --- |
| 普通查询或命令成功 | `200 OK` |
| 创建同步资源 | `201 Created` |
| 已接受异步处理 | `202 Accepted` |
| 无响应正文 | `204 No Content` |

创建资源时应设置 `Location` 响应头。异步诊断创建返回任务查询地址，不表示诊断已经完成。

### JSON 错误响应

```json
{
  "code": 42201,
  "message": "业务参数校验失败",
  "data": {
    "fields": [
      {
        "field": "attachmentIds",
        "reason": "包含不属于当前用户的附件"
      }
    ]
  },
  "requestId": "01900000-0000-7000-8000-000000000001"
}
```

字段错误只能返回安全、可展示的信息。数据库错误、文件路径、SQL、凭证和堆栈只进入脱敏日志。

目标错误码：

| Code | HTTP | 含义 |
| --- | --- | --- |
| `40001` | 400 | 请求格式或基础参数错误 |
| `40101` | 401 | 未登录、Session 失效或账号状态失效 |
| `40301` | 403 | 当前用户无权执行操作 |
| `40401` | 404 | 资源不存在，或为防止枚举而隐藏无权资源 |
| `40501` | 405 | 请求方法不支持 |
| `40901` | 409 | 通用资源状态冲突 |
| `40911` | 409 | 相同 Idempotency-Key 对应不同请求 |
| `40921` | 409 | 任务当前状态不允许执行命令 |
| `40922` | 409 | 附件正在被其他上传请求处理 |
| `40923` | 409 | 外部工单在确认后发生变化 |
| `41301` | 413 | 附件超过大小限制 |
| `41501` | 415 | 不支持的附件媒体类型 |
| `42201` | 422 | 格式正确但业务校验失败 |
| `42901` | 429 | 请求超过限流策略 |
| `42902` | 429 | 异步任务积压，暂不接受新任务 |
| `50000` | 500 | 未分类的服务器内部错误 |
| `50301` | 503 | 当前操作依赖的外部服务不可用 |

前端需要采取不同行为时才增加领域错误码，不能为每条错误文案创建枚举。

## 通用请求规则

### Request ID

客户端可以发送：

```text
X-Request-ID: <UUID>
```

服务端校验格式后沿用；缺失或不合法时生成新 UUID，并在响应头和响应信封中返回。日志、TaskEvent 和后续 Outbox 使用该 ID 作为关联线索之一。

### Idempotency-Key

需要防重复的创建或恢复接口使用：

```text
Idempotency-Key: <UUID>
```

通用规则：

- 幂等事实持久化到 PostgreSQL；
- 唯一作用域至少包含当前用户和接口操作类型；
- 相同 Key、相同规范化请求返回原结果；
- 相同 Key、不同请求返回 `40911`；
- Redis 可以降低并发冲突，但不能成为唯一幂等存储；
- 服务端日志不记录请求中的密码或原始附件内容。

### 分页、过滤和排序

后台表格使用：

```text
page=1&pageSize=20
```

响应分页结构：

```json
{
  "items": [],
  "page": 1,
  "pageSize": 20,
  "total": 128
}
```

规则：

- `page` 默认 1；
- `pageSize` 默认 20，最大 100；
- 排序字段和方向由服务端白名单映射，不能直接拼接用户输入；
- TaskEvent 使用 `afterSeq`，不使用页码；
- 后续大型追加日志使用 cursor，避免深分页。

## 认证、Session 与 CSRF

### Session Cookie

登录成功后设置服务端 Session Cookie：

```text
Name: mesguard_session
HttpOnly: true
SameSite: Lax
Path: /
Secure: false（当前受控 HTTP 开发环境）
Secure: true（启用 HTTPS 后强制）
```

Cookie 只保存高熵随机令牌，PostgreSQL 只保存令牌哈希。默认空闲过期 2 小时、绝对过期 12 小时，均通过配置调整。

CSRF Token 同时写入 `mesguard_csrf` Cookie。该 Cookie 不设置 `HttpOnly`，因为浏览器端需要读取它并复制到 `X-CSRF-Token` 请求头；服务端仍只在 PostgreSQL 保存 CSRF Token 哈希。Session Cookie 与 CSRF Cookie 都使用 `SameSite=Lax`、`Path=/`，`Secure` 按部署配置决定。

普通认证请求最多每 5 分钟刷新一次空闲过期时间。SSE 心跳不刷新 Session；连接最长不能超过 Session 绝对有效期。退出只撤销当前 Session；密码修改、密码重置、禁用账号或修改角色撤销该用户全部 Session。

### CSRF

- 登录成功响应和 `GET /auth/me` 返回当前 Session 的 CSRF Token；
- `POST`、`PUT`、`PATCH`、`DELETE` 必须携带 `X-CSRF-Token`；
- 服务端同时校验可信 `Origin`；
- `GET`、`HEAD`、`OPTIONS` 不允许改变业务状态；
- 登录接口至少执行 Origin 校验，防止 login CSRF；
- 前端不能把 Session Cookie 或 CSRF Token写入日志。

### 登录限流

登录按来源 IP 与规范化用户名组合限流。用户名不存在与密码错误返回相同的 `40101` 文案，避免账号枚举。限流状态可以存 Redis；Redis 不可用时回退到进程内保守限流或拒绝高风险请求，不能完全关闭暴力破解保护。

## 权限规则

一期只有 `analyst` 和 `admin`：

| 资源/操作 | analyst | admin |
| --- | --- | --- |
| 查询授权数据源和外部工单 | 允许 | 允许 |
| 上传附件 | 仅自己 | 仅自己，管理查看需审计 |
| 创建诊断 | 允许 | 允许 |
| 查看、取消、反馈任务 | 仅自己创建的任务 | 全部任务 |
| 查看完整运维元数据 | 禁止 | 允许 |
| 管理用户和恢复失败任务 | 禁止 | 允许 |

当前任务、正式报告和报告反馈接口对其他 analyst 返回 `40301`，任务不存在时返回 `40401`；admin 可以跨用户读取任务和报告。失败任务恢复只允许 admin，并通过专用恢复表记录操作者、原因、原错误、原尝试次数、TaskEvent 和 Outbox 关联。

## 接口总览

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/livez` | 无 | 进程存活 |
| GET | `/readyz` | 无 | 当前角色就绪 |
| POST | `/api/v1/auth/login` | 无 | 登录并创建 Session |
| POST | `/api/v1/auth/logout` | 登录 | 撤销当前 Session |
| GET | `/api/v1/auth/me` | 登录 | 当前用户与 CSRF Token |
| POST | `/api/v1/auth/change-password` | 登录 | 修改自己的密码 |
| GET | `/api/v1/admin/users` | admin | 用户列表 |
| POST | `/api/v1/admin/users` | admin | 创建用户 |
| PATCH | `/api/v1/admin/users/{userId}/status` | admin | 启用或禁用用户 |
| PATCH | `/api/v1/admin/users/{userId}/role` | admin | 修改角色 |
| POST | `/api/v1/admin/users/{userId}/reset-password` | admin | 重置临时密码 |
| GET | `/api/v1/admin/data-sources` | admin | 数据源运行与Catalog状态 |
| POST | `/api/v1/admin/data-sources/{dataSourceId}/catalog-scans` | admin | 创建Schema扫描任务 |
| GET | `/api/v1/admin/data-sources/{dataSourceId}/catalog-versions` | admin | Catalog版本列表 |
| GET | `/api/v1/admin/catalog-versions/{versionId}/entries` | admin | Catalog条目列表 |
| PATCH | `/api/v1/admin/catalog-versions/{versionId}/entries/{entryId}` | admin | 编辑字段语义与白名单 |
| POST | `/api/v1/admin/catalog-versions/{versionId}/publish` | admin | 发布Catalog版本 |
| GET | `/api/v1/data-sources` | 登录 | 可用数据源列表 |
| GET | `/api/v1/external-cases` | 登录 | 实时工单列表 |
| GET | `/api/v1/external-cases/{externalCaseId}` | 登录 | 实时工单详情 |
| GET | `/api/v1/conversations` | 登录 | 当前用户会话列表 |
| POST | `/api/v1/conversations` | 登录 | 创建独立会话 |
| GET | `/api/v1/conversations/{conversationId}` | 登录 | 会话摘要 |
| GET | `/api/v1/conversations/{conversationId}/messages` | 登录 | 按 seq 补读消息 |
| POST | `/api/v1/conversations/{conversationId}/messages` | 登录 | 追加用户消息和结构化引用；当前不触发会话 Agent |
| POST | `/api/v1/conversations/{conversationId}/turns` | 登录 | 使用 UUID 幂等键异步受理会话 Agent 回合；原子写消息/turn/Outbox，失败复用用户消息，完成结果可回放 |
| GET | `/api/v1/conversations/{conversationId}/turns/{turnId}` | 登录 | 查询回合状态安全摘要 |
| GET | `/api/v1/conversations/{conversationId}/turns/{turnId}/events` | 登录 | 回合事件 JSON 历史或 SSE 订阅 |
| POST | `/api/v1/conversations/{conversationId}/attachments` | 登录 | 向当前用户会话流式上传一个 session 附件 |
| GET | `/api/v1/conversations/{conversationId}/attachments/{attachmentId}/preview` | 登录 | 受 owner/conversation 约束的附件文本预览 |
| GET | `/api/v1/knowledge-citations/{chunkId}` | 登录 | 按知识 scope 与版本状态读取 Chunk 引用预览 |
| GET | `/api/v1/diagnosis-tasks` | 登录 | 任务列表 |
| POST | `/api/v1/diagnosis-tasks` | 登录 | 创建异步诊断任务 |
| GET | `/api/v1/diagnosis-tasks/{taskId}` | 登录 | 任务详情与步骤摘要 |
| POST | `/api/v1/diagnosis-tasks/{taskId}/cancel` | 登录 | 请求取消任务 |
| GET | `/api/v1/diagnosis-tasks/{taskId}/events` | 登录 | TaskEvent JSON 历史或 SSE 订阅 |
| GET | `/api/v1/diagnosis-tasks/{taskId}/evidence` | 登录 | 证据列表 |
| GET | `/api/v1/diagnosis-tasks/{taskId}/tool-executions` | 登录 | 工具执行记录 |
| GET | `/api/v1/diagnosis-tasks/{taskId}/report` | 登录 | 任务正式报告 |
| GET | `/api/v1/diagnosis-reports/{reportId}/reviews` | 登录 | 当前反馈与历史 |
| POST | `/api/v1/diagnosis-reports/{reportId}/reviews` | 任务创建者 | 追加报告反馈 |
| POST | `/api/v1/admin/diagnosis-tasks/{taskId}/recover` | admin | 恢复失败任务 |
| GET | `/api/v1/admin/system/dependencies` | admin | 详细依赖状态 |

## 健康检查

### `GET /livez`

只判断当前进程是否存活，不检查下游依赖。返回最少信息：

```json
{
  "status": "ok",
  "role": "api"
}
```

### `GET /readyz`

按运行角色检查核心依赖：

- API：PostgreSQL；
- Outbox Relay：PostgreSQL、RabbitMQ；
- Diagnosis Worker：PostgreSQL、RabbitMQ；
- Ingestion Worker：PostgreSQL、RabbitMQ、MinIO。

Redis 不可用通常是 degraded 而不是未就绪。未就绪返回 `503`，但不包含连接地址、凭证或底层异常。

### `GET /api/v1/admin/system/dependencies`

返回各依赖的 `up/down/degraded`、检查时间、耗时和安全错误摘要。该接口不能返回 DSN、密码、Access Key 或模型密钥。

## 认证接口

### `POST /api/v1/auth/login`

请求：

```json
{
  "username": "analyst01",
  "password": "<password>"
}
```

成功返回 `200`，设置 Session Cookie：

```json
{
  "user": {
    "id": "...",
    "username": "analyst01",
    "displayName": "售后分析员",
    "role": "analyst",
    "mustChangePassword": false
  },
  "csrfToken": "...",
  "idleExpiresAt": "2026-07-26T05:20:15Z",
  "absoluteExpiresAt": "2026-07-26T15:20:15Z"
}
```

账号被禁用、密码错误和用户名不存在都使用安全的通用提示。`mustChangePassword=true` 时，仅允许访问当前会话、修改密码和退出接口。

### `GET /api/v1/auth/me`

返回当前用户、Session 有效期和 CSRF Token，用于页面刷新后恢复前端认证状态。

### `POST /api/v1/auth/logout`

撤销当前 Session 并清除 Cookie。重复退出保持幂等；没有有效 Session 时也不暴露令牌是否曾存在。

### `POST /api/v1/auth/change-password`

```json
{
  "currentPassword": "...",
  "newPassword": "..."
}
```

验证成功后更新 Argon2id 哈希、清除 `mustChangePassword` 并撤销全部旧 Session。响应清除当前 Cookie，用户需要用新密码重新登录。

## 用户管理接口

### `POST /api/v1/admin/users`

```json
{
  "username": "analyst02",
  "displayName": "实施顾问",
  "role": "analyst",
  "temporaryPassword": "..."
}
```

成功返回 `201`。临时密码只在请求中出现，服务端响应不回显；新用户 `mustChangePassword=true`。禁止匿名注册，首个 admin 仍通过初始化命令创建。

### `GET /api/v1/admin/users`

支持 `page`、`pageSize`、`keyword`、`role`、`status`、`sortBy` 和 `sortOrder`。不返回 `passwordHash`、Session token 或密码历史。

### 用户状态与角色

```text
PATCH /api/v1/admin/users/{userId}/status
PATCH /api/v1/admin/users/{userId}/role
```

状态请求只允许 `active/disabled`，角色只允许 `analyst/admin`。成功后撤销目标用户全部 Session。系统必须防止最后一个可用 admin 把自己禁用或降级，避免失去管理入口。

### `POST /api/v1/admin/users/{userId}/reset-password`

请求包含新的临时密码。成功后设置 `mustChangePassword=true` 并撤销目标用户全部 Session，不返回旧密码或哈希。

## 数据源与外部工单

### 数据源管理边界

SQL Server地址、账号和密码由TOML、`.env`或生产密钥文件装配，不通过HTTP创建、读取或修改。admin API只管理安全元数据、连通状态和Schema Catalog，响应不得包含`credentialRef`的实际值、DSN或密码。

### `GET /api/v1/admin/data-sources`

返回数据源名称、类型、环境、启用状态、最近连通检查、当前已发布Catalog版本和最近扫描状态。admin可以据此判断数据源是否具备工单查询与Text-to-SQL条件，但不能从接口下载连接配置。

### `POST /api/v1/admin/data-sources/{dataSourceId}/catalog-scans`

创建低频、受控的Schema扫描任务，要求`Idempotency-Key`并返回`202`：

```json
{
  "catalogVersionId": "...",
  "version": 3,
  "scanStatus": "pending"
}
```

扫描只读取允许的SQL Server schema、表、视图和字段元数据，不读取业务表数据。M1不增加新的RabbitMQ队列：API只创建持久化`pending`扫描记录，由同一代码库中的受限后台扫描器使用PostgreSQL租约领取；API进程重启后仍可继续恢复。扫描频率或规模增长后再迁入Ingestion Worker。

同一数据源已有`pending/running`扫描时返回`40901`。扫描失败保留错误摘要和草稿版本，不能影响当前`published`版本。

### Catalog版本与条目

```text
GET /api/v1/admin/data-sources/{dataSourceId}/catalog-versions
GET /api/v1/admin/catalog-versions/{versionId}/entries
PATCH /api/v1/admin/catalog-versions/{versionId}/entries/{entryId}
```

版本列表返回扫描状态、发布状态、创建者、发布时间和条目统计。条目列表支持按schema、对象名、字段名、`queryable`和`sensitivityLevel`筛选。

只有`draft`版本允许编辑：

```json
{
  "comment": "生产订单号",
  "semanticAliases": ["工单号", "订单编号"],
  "queryable": true,
  "sensitivityLevel": "internal"
}
```

管理员未补充Comment时，后续可以由LLM生成候选语义，但候选内容仍需保留生成来源并由admin确认后才能发布。接口不能让模型自动扩大`queryable`范围或降低敏感级别。

### `POST /api/v1/admin/catalog-versions/{versionId}/publish`

只允许发布扫描成功、校验通过的`draft`版本。事务中将当前published版本置为`retired`，再把目标版本置为`published`并记录操作者。重复发布同一版本返回当前结果；发布旧版本、扫描失败版本或含非法白名单的版本返回`40901/42201`。

正在运行的诊断继续使用任务创建时绑定的Catalog版本；新任务只使用最新published版本。发布动作不会静默改变历史任务的查询边界。

### `GET /api/v1/data-sources`

只返回当前用户可使用的启用数据源：

```json
{
  "items": [
    {
      "id": "...",
      "name": "MES 演示库",
      "type": "sqlserver",
      "status": "active",
      "publishedCatalogVersionId": "..."
    }
  ]
}
```

不返回 DSN、用户名、密码或网络地址。即使一期只有一个数据源，前端仍显式提交 `dataSourceId`。

### 统一工单 DTO

外部 SQL Server 字段由 Adapter 映射为：

```json
{
  "externalCaseId": "...",
  "dataSourceId": "...",
  "externalCaseKey": "WO-2026-00128",
  "title": "报工后库存未更新",
  "description": "...",
  "status": "open",
  "priority": "high",
  "customerName": "...",
  "productName": "...",
  "productVersion": "...",
  "reportedAt": "2026-07-25T08:00:00Z",
  "sourceUpdatedAt": "2026-07-26T01:00:00Z",
  "sourceFingerprint": "sha256:...",
  "attributes": {},
  "attributesSchemaVersion": 1
}
```

固定字段用于前端展示和筛选；MES 特有字段只允许经过白名单和脱敏后进入 `attributes`。API 不返回 SQL Server 表名、列名和原始查询结果。

`externalCaseId` 是 MESGuard 的稳定身份。Adapter 发现工单时可以幂等注册或更新 `external_cases.last_seen_at`，但读取接口不创建 CaseSnapshot，也不向 SQL Server 回写。

### `GET /api/v1/external-cases`

要求 `dataSourceId`，支持：

```text
keyword
status
priority
reportedFrom
reportedTo
page
pageSize
sortBy
sortOrder
```

允许的筛选和排序必须映射到只读 SQL Server 查询白名单，并设置查询超时、行数和结果大小限制。

### `GET /api/v1/external-cases/{externalCaseId}`

根据 MESGuard 身份定位数据源和外部工单号，再实时读取 SQL Server。`sourceFingerprint` 基于规范化、脱敏后的诊断输入字段计算，用于创建任务时防止用户确认后工单静默变化。

## 附件接口

### `POST /api/v1/conversations/{conversationId}/attachments`

请求：

```text
Content-Type: multipart/form-data
Idempotency-Key: <UUID>

file=<binary>
```

当前每次上传一个文件，scope 由路由固定为 `session`，客户端不能扩大范围。API 先将输入流式写入
有大小上限的临时文件并计算 SHA-256，校验后写入 MinIO，不使用 Base64，也不把完整文件读入内存。

允许类型为 UTF-8 TXT/Markdown/LOG/JSON/CSV/SQL/XML/YAML、PDF、DOCX、XLSX、PPTX、PNG 和
JPEG。服务端校验扩展名对应的文件签名或 OOXML 主结构；加密 Office 包、宏格式、任意 ZIP、
可执行文件、NUL 文本和非 UTF-8 文本均拒绝。单文件上限复用 `[minio].maxObjectBytes`。

幂等状态存 PostgreSQL：

- 第一次请求先固化不可变对象，再插入 `uploaded` 附件事实；
- 同一用户、同一 UUID Key、相同 conversation 和上传指纹返回原 `attachmentId`；
- 相同 Key 对应不同 conversation、文件名、媒体类型、大小或 SHA-256 时返回冲突；
- 并发唯一键冲突后重新读取事实并按同一规则判定，不依赖 Redis；
- 数据库插入失败时只对本次新写对象执行尽力清理。

当前未实现 `uploading/failed` 租约状态、失败上传接管和孤儿对象定时清理，不能用目标设计中的
`40922` 语义描述现有接口。

首次成功返回 `201`，幂等重放返回 `200`：

```json
{
  "attachmentId": "...",
  "conversationId": "...",
  "originalName": "problem.png",
  "mediaType": "image/png",
  "sizeBytes": 483920,
  "contentSha256": "...",
  "scope": "session",
  "status": "uploaded",
  "replayed": false,
  "createdAt": "2026-07-26T03:20:15Z"
}
```

### 附件引用预览

```text
GET /api/v1/conversations/{conversationId}/attachments/{attachmentId}/preview
GET /api/v1/knowledge-citations/{chunkId}
```

附件预览先校验当前用户和 conversation，复用知识 Parser，最多返回 2,000 rune 的文本/表格元素、
页码、章节、元素类型、Parser 版本、内容哈希和视觉内容数量。它不代理原始二进制，也不返回 bucket、
object key、ETag、永久 URL 或 MinIO 凭证。图片或扫描页只返回视觉内容数量，不隐式调用 OCR/VLM。

知识引用预览按 `chunkId` 返回稳定 `knowledge_chunk:{chunkId}` 来源、文档/版本/Chunk、页码、章节、
原文和内容哈希。global 文档可由登录用户读取；personal 文档只允许 owner；已删除文档、非
`ready/retired` 版本和越权 personal 文档统一返回 404。

当前不提供原始附件下载或任意物理删除接口。孤儿对象清理与保留期任务仍待实现。

## 诊断任务接口

### `POST /api/v1/diagnosis-tasks`

请求头：

```text
Idempotency-Key: <UUID>
X-CSRF-Token: <token>
```

请求：

```json
{
  "externalCaseId": "...",
  "expectedSourceFingerprint": "sha256:...",
  "evidenceDataSourceIds": ["..."],
  "requestText": "请先检查数据库中的业务状态",
  "requestScope": {
    "requestedSkill": "sql-investigation",
    "allowedCapabilities": ["case", "sql"],
    "timeRange": {
      "from": "2026-07-25T00:00:00Z",
      "to": "2026-07-26T00:00:00Z"
    }
  },
  "requestScopeSchemaVersion": 1,
  "attachments": [
    {
      "attachmentId": "...",
      "purpose": "problem_image"
    }
  ],
  "retryOfTaskId": null
}
```

`requestScope.allowedCapabilities` 是任务创建时冻结的能力白名单，可由调用方声明的值为 `case`、`code`、`sql`，后端会始终为新建诊断任务追加 `knowledge`。用户不得显式提交 `knowledge`，因此前端不应提供该 Tool 开关。它与服务健康状态分离：声明 `sql` 不代表 SQL Server 当前可用，未声明 `sql` 时即使 SQL Server 健康也不会向模型暴露 SQL Tool。未提供该字段时，后端按 `requestedSkill` 推导 `case`/`code`/`sql`，再附加 `knowledge`。
`code-investigation` 必须包含 `case + code`，`sql-investigation` 必须包含 `case + sql`；常规 `ticket-diagnosis` 可以声明 `case`、`case + code`、`case + sql` 或三者组合，但始终会同时拥有后端策略追加的 `knowledge`。

API 在创建前重新只读查询 SQL Server 并计算 fingerprint：

- 与 `expectedSourceFingerprint` 一致时继续；
- 不一致返回 `40923`，前端刷新工单后让用户重新确认；
- SQL Server 查询、附件校验和数据源权限检查发生在 PostgreSQL 业务事务之前；
- PostgreSQL 事务写入 CaseSnapshot、DiagnosisTask、关联数据源、首个 TaskEvent 和 OutboxEvent；当调用来自 Conversation Agent 且当前消息带附件时，同一事务还写 `diagnosis_task_attachments`；
- 事务提交后返回 `202` 和 `Location: /api/v1/diagnosis-tasks/{taskId}`。

当前实现已完成外部工单重读、fingerprint 校验、脱敏 CaseSnapshot、DiagnosisTask、TaskEvent 和 OutboxEvent 的原子落库，并支持同一用户同一幂等键的重放/冲突判断。会话附件与 MinIO 流程已经实现；Conversation Agent 的创建命令会从当前最新 user message 冻结全部或指定子集附件，并在同一任务事务写入 `diagnosis_task_attachments`。直接创建诊断任务的 HTTP 契约仍拒绝非空 `attachments`，因为它没有消息级授权上下文，不会静默忽略。

响应：

```json
{
  "taskId": "...",
  "status": "pending",
  "replayed": false,
  "createdAt": "2026-07-26T03:20:15Z"
}
```

同一用户、同一 Idempotency-Key 和相同请求返回原任务并设置 `replayed=true`；请求不同返回 `40911`。`retryOfTaskId` 只表示普通用户重新诊断的历史关联，仍必须使用新 Key、重新读取外部工单并创建新快照。

### `GET /api/v1/diagnosis-tasks`

analyst 只能查询自己的任务；admin 可以使用 `createdBy` 筛选全部任务。支持：

```text
status
externalCaseId
createdFrom
createdTo
page
pageSize
sortBy
sortOrder
```

列表只返回摘要，不嵌入完整 Evidence、ToolExecution 或报告正文。

### `GET /api/v1/diagnosis-tasks/{taskId}`

返回：

- 任务 ID、状态、创建和完成时间；
- 工单身份和不可变 CaseSnapshot 摘要；
- 本次使用的数据源；
- 附件安全元数据；
- DiagnosisStep 摘要和最新尝试状态；
- 取消状态、稳定错误码和安全错误信息；
- `reportAvailable` 和报告 ID。

不返回模型思维过程、系统 Prompt、密钥、完整原始 SQL 或未脱敏证据。

当前实现已返回任务状态、请求摘要、CaseSnapshot ID、错误摘要和报告可用性；Worker 已完成步骤、证据、工具执行和报告持久化，但这些明细仍由各自的专用读取接口逐步开放。

### `GET /api/v1/diagnosis-tasks/{taskId}/events`

当前实现提供 JSON 历史查询，按 `seq` 升序返回：

```http
GET /api/v1/diagnosis-tasks/{taskId}/events?afterSeq=18&limit=100
Accept: application/json
```

- `afterSeq` 是排他游标，默认 `0`；
- `limit` 默认 `100`，最大 `200`；
- 响应返回 `items`、`afterSeq`、`nextAfterSeq` 和 `hasMore`；
- 任务创建者只能读取自己的事件，admin 可以读取全部任务；
- payload 只能是应用生成的安全结构化轨迹，不能放入日志、Prompt、密钥、原始 SQL 或模型内部思维过程。

同一路径通过 `Accept: text/event-stream` 协商 SSE，不另建一套事件身份和游标规则。SSE 建流前复用相同的 owner/admin 授权；`Last-Event-ID` 存在时优先于 `afterSeq`。

### `POST /api/v1/diagnosis-tasks/{taskId}/cancel`

无需单独 Idempotency-Key，命令本身按任务状态幂等：

- `pending/running`：事务写入 `cancel_requested` 和 TaskEvent，返回 `202`；
- 已经 `cancel_requested/cancelled`：返回当前状态，不重复写事件；
- `succeeded/failed`：返回 `40921`；
- 任务创建者和 admin 可以取消；
- 关闭 SSE 不会触发此接口。

当前实现已经落地该命令：首次状态转换返回 `202`，重复取消返回 `200`；权限检查、状态条件更新和 `task_cancel_requested` 事件均有领域、HTTP 和 PostgreSQL 集成测试。Worker 会在领取和执行边界识别取消请求并提交 `cancelled` 终态。

## TaskEvent SSE

### `GET /api/v1/diagnosis-tasks/{taskId}/events`

同一路径已经实现 `text/event-stream` 表示。服务端先按游标从 PostgreSQL 补读，再以固定间隔轮询新 TaskEvent；Redis 尚未参与唤醒，因此不会成为事件事实来源。

```http
GET /api/v1/diagnosis-tasks/{taskId}/events?afterSeq=18
Accept: text/event-stream
```

事件：

```text
retry: 3000

id: 19
event: task_started
data: {"seq":19,"eventType":"task_started","payload":{"attemptCount":1},"payloadSchemaVersion":1,"createdAt":"..."}

```

规则：

- SSE `id` 等于 PostgreSQL `task_events.seq`；
- 浏览器自动重连使用 `Last-Event-ID`；页面刷新后可以用 `afterSeq`；
- 两者同时存在时 `Last-Event-ID` 优先；
- API 先从 PostgreSQL 按序补读，再轮询 PostgreSQL 新事件；
- 轮询是当前可靠基线，后续可以增加 Redis 唤醒但不能替代 PostgreSQL；
- 每 15 秒发送 SSE 注释心跳，不产生 TaskEvent；
- 终态事件发送完成后正常关闭连接；
- 应用收到停机信号时主动关闭 SSE，避免长连接阻塞优雅关停；
- 连接达到当前 Session 的 `absoluteExpiresAt` 时关闭，重新连接会重新执行 Session 校验；
- M1 不传模型原始内部思维过程，也不把日志行直接当 SSE 事件；可以传输应用生成的结构化调查轨迹，包括阶段、脱敏 Tool 摘要、证据缺口和 Evidence Gate 结果，供前端默认折叠、按需展开。

SSE 建立后不能再改用 JSON 统一错误信封。连接前的认证、权限或参数失败正常返回 JSON 错误；连接后的读取异常使用不含底层原因的 SSE `error` 事件，并依靠 `requestId` 日志排查。

## 证据与工具执行

### `GET /api/v1/diagnosis-tasks/{taskId}/evidence`

支持 `sourceType`、`validityStatus`、`page` 和 `pageSize`。返回脱敏的 `contentText/contentData`、来源类型、来源定位、采集时间、截断状态和有效性。

附件证据返回 `attachmentId`、页码或区域，不返回 object key。代码证据返回仓库、Commit、文件和行号；Web 证据返回公开 URL 与检索时间。

### `GET /api/v1/diagnosis-tasks/{taskId}/tool-executions`

支持 `stepId`、`toolName`、`status`、`page` 和 `pageSize`。analyst 看到工具名称、耗时、状态和脱敏摘要；admin 可以看到 Token、缓存 Token、成本、行数和截断信息。

任何角色都不能通过该接口读取完整系统 Prompt、模型内部思维过程、数据库凭证或未脱敏原始参数。

## 报告与反馈

### `GET /api/v1/diagnosis-tasks/{taskId}/report`

任务存在但尚无正式报告时返回 `40921`；任务不存在返回 `40401`，其他 analyst 越权读取返回 `40301`。报告可用后返回：

- `conclusionStatus`：`conclusive/probable/inconclusive`；
- `riskLevel`；
- `businessSummary`；
- `technicalSummary`；
- `partial`、`missingEvidence`、Token 用量和 Agent 运行摘要；
- 报告 schema、`modelProvider + modelId` 和 `promptVersion`；
- 按 claim 组织的 Evidence 身份、来源定位、支持类型、哈希、截断与有效性元数据。

当前接口不返回完整 Evidence 内容、原始 Prompt、模型内部推理或原始 SQL；人工复核继续通过独立的 `/diagnosis-reports/{reportId}/reviews` 接口读取和追加。仓储最多执行一次任务/报告查询和一次证据声明查询，并严格按 v1 schema 解码持久化 JSON。

任务 `succeeded` 只表示流程完成。`inconclusive` 是正常报告结论，不映射为 HTTP 或任务失败。

### 报告反馈

```text
GET  /api/v1/diagnosis-reports/{reportId}/reviews
POST /api/v1/diagnosis-reports/{reportId}/reviews
```

提交请求：

```json
{
  "verdict": "partially_adopted",
  "comment": "数据库方向正确，但仍需开发确认代码逻辑"
}
```

`adopted` 对应前端的👍，`rejected` 对应👎，`partially_adopted` 保留给需要人工补充判断的情况。只有任务创建者可以提交，管理员可以查看但不能代替创建者修改。每次提交新增记录，最新一条为当前有效反馈；反馈不回写 MES/ERP，也不会自动进入全局知识库。一期不提供删除反馈接口。

当前后端已实现这两个接口和 PostgreSQL 持久化。Diagnosis Worker 成功提交后，任务摘要的 `reportAvailable` 为 `true`，正式报告查询返回持久化报告 ID；评测 observation 的 `runId` 仍不是报告 ID。

## 管理员失败恢复

### `POST /api/v1/admin/diagnosis-tasks/{taskId}/recover`

请求头要求 `Idempotency-Key`，请求体：

```json
{
  "reason": "模型服务恢复，重新执行未完成步骤"
}
```

只有同时满足以下条件才返回 `202`：

- 任务为 `agent_execution_failed` 的可恢复 `failed`；
- 没有正式报告；
- 没有活动租约或其他运行者；
- 任务未请求取消，创建者和关联数据源仍为 active；
- 当前管理员有权限且提供恢复原因。

同一 PostgreSQL 事务把任务置为 `pending`，清除租约、完成时间和上次错误，追加 `task_requeued` TaskEvent，记录管理员审计并重新打开原 Outbox。Relay 继续使用同一个 `messageId` 发布。恢复事务保留原 `attemptCount`，下一次 Worker Claim 才增加尝试次数；恢复接口不能修改原请求范围、附件或数据源，输入变化必须创建新的诊断任务。

幂等范围为 `task + admin + Idempotency-Key`：相同 Key 和原因重放返回原结果与 `200`，相同 Key 但原因不同返回 `40911`。`succeeded/cancelled/running`、存在正式报告、已取消、依赖停用或不可恢复失败返回 `40921`。

## CORS 与入口

生产目标由 Nginx 同源托管 React 和反向代理 `/api`，默认不开放跨域业务访问。Vite 开发服务器通过代理访问 API。

如果后续确实需要独立前端域名，必须显式配置允许来源、Cookie credentials、CSRF 和 HTTPS；不能使用 `Access-Control-Allow-Origin: *` 搭配 Session Cookie。

Nginx 转发 SSE 时关闭不合适的响应缓冲，并设置大于心跳间隔的读取超时。浏览器、普通用户和前端代码不能直接访问 PostgreSQL、RabbitMQ、Redis 或 MinIO 管理端口。

## OpenAPI 与实现同步

`api.md` 负责说明跨接口原则、权限、状态机和故障语义。开始实现具体 Handler 时增加机器可读的 OpenAPI 文件，例如：

```text
api/openapi.yaml
```

OpenAPI 负责精确字段、required、枚举、格式和示例，并用于生成前端 TypeScript 类型或联调 Mock。代码变更必须同时更新 OpenAPI；不能只依赖运行时 Swagger 注解猜测领域契约。

## 验收清单

1. 所有 JSON 字段和查询参数使用 camelCase，URL 资源使用短横线。
2. HTTP 状态码、应用错误码和统一响应信封语义一致。
3. 未登录、越权、账号禁用、CSRF 失败和 Session 过期均有测试。
4. analyst 不能读取、取消或反馈其他 analyst 的任务和附件。
5. 同一诊断 Idempotency-Key 重试只产生一个快照、任务和 Outbox。
6. 同一附件 Key 的成功重放返回原附件，不同上传指纹返回冲突；失败租约恢复仍待实现。
7. 外部工单 fingerprint 变化时不静默创建诊断任务。
8. 关闭或重连 SSE 不取消任务，`Last-Event-ID/afterSeq` 可以无缺口补读。
9. Redis 不可用时 Session、幂等和 TaskEvent 事实仍然正确。
10. 取消、恢复和用户状态修改只能走领域命令，不能任意 PATCH 状态。
11. 附件内容、证据和工具记录不会泄漏 object key、凭证、Prompt 或内部思维过程。
12. `/livez` 不因依赖故障触发重启风暴，`/readyz` 按角色准确反映就绪状态。
13. Catalog扫描失败不影响当前published版本，未发布版本不能用于Text-to-SQL。

## 后续工作

M0、M1-A1 和 P7 任务创建、TaskEvent JSON 历史/SSE、取消命令、Outbox Relay、RabbitMQ Consumer、Diagnosis Worker、正式报告查询及管理员失败恢复已实现。M2 当前已实现管理员知识原文上传、幂等重放/冲突、入库任务查询/取消、Worker claim/lease/checkpoint/fencing、多格式解析、Element Artifact、Embedding/pgvector、FTS/Vector/RRF 召回、真实 `qwen3-rerank` 固定集评测、知识问答 Runner 内部的 `search_knowledge` Tool，以及独立会话持久化、消息读取、Conversation Agent `/turns`、turn 幂等账本、助手最终消息写入、回合状态查询、可续传回合事件 SSE、引用门禁的内部任务状态 Tool、会话附件上传/消息关联/受控正文读取、附件/知识 Chunk 引用预览、诊断任务附件冻结/任务级读取、知识/附件/网页的助手结构化引用持久化，以及成功、降级和终态失败回合的 recorded-run 事实与离线导出器。失败观测受 lease owner/deadline 事务门禁，只保存稳定错误类型，不增加公开 HTTP 接口。新建诊断任务已由后端自动冻结 knowledge capability，前端不提供 Tool 开关。真实 PostgreSQL + MinIO 的小型 TXT HTTP smoke 已验证上传幂等、消息授权、Tool 读取、引用预览、跨用户拒绝和敏感对象坐标不泄漏，且未调用 Provider。`conversation-v6` 还为“来源已取回但主答案零引用”增加一次失败触发、严格 JSON、同模型的受控引用修复；首个 transaction Case 已通过引用精度/召回、预览一致性与人工无证据扩写复核。机器可读契约只随已实现 Handler 更新在 `api/openapi.yaml`；内部 Tool 不作为公开 HTTP API 伪装进 OpenAPI。下一步是代表性跨格式全链路 pair、更大多 Case 稳定性、前端回合/附件/引用接入，以及 personal 附件与失败上传租约/孤儿清理等工程收尾。

### 会话消息引用约定

assistant message 的 `citations` 是有序数组，每项只返回：

```json
{
  "position": 0,
  "sourceType": "knowledge_chunk",
  "sourceRef": "knowledge:<documentVersionId>/<chunkId>",
  "contentSha256": "<64 lowercase hex>"
}
```

`sourceType` 还可为 `attachment` 或 `web`。附件引用为 `attachment:<attachmentId>`；网页引用必须是
无 userinfo 的 HTTPS URL。Runner 只接受最终回答中完整复制的 marker，例如
`[source:knowledge:11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222]`；
尖括号、引号和反引号都不是语法字符，且 marker 必须与同一 Agent
run 中后端验证过的 Tool 来源完全匹配的 marker。重复 marker 在持久化时去重并按首次出现排序，
未知/篡改/超过 20 个不同来源的回答不会生成 assistant message。HTTP 消息补读和完成幂等回放均
返回相同 `citations`；SSE `turn_completed` 仍只发布安全的 assistant message ID 和引用数量，客户端
收到终态后通过消息/回放响应读取完整引用元数据。知识和附件正文不得从 `sourceRef` 直接拼对象
地址，必须调用已有受权预览 API；网页只允许打开返回的 HTTPS URL。

### 会话回合事件约定

`GET /api/v1/conversations/{conversationId}/turns/{turnId}` 返回回合当前安全摘要：状态、用户/助手消息 ID、尝试次数、时间、自动重试时间和面向用户的失败摘要。它不返回 `lease_owner`、请求指纹、模型配置、Prompt、工具结果或异常堆栈。

`GET .../events` 默认返回 JSON 游标页；当 `Accept` 包含 `text/event-stream` 时返回 SSE。事件表是事实源，Redis/RabbitMQ 只负责唤醒和投递。事件类型为 `turn_queued`、`turn_running`、`turn_retry_scheduled`、`turn_completed` 和 `turn_failed`。临时模型/Tool 失败转为带 `retry_at` 的 queued 回合，`turn_retry_scheduled` 不是终态；只有达到自动重试上限才写 `turn_failed`，SSE 在发送完成/失败终态后关闭。
