# MESGuard PostgreSQL 数据库设计

## 文档状态

- 本文定义 MESGuard 业务数据库的目标结构、约束、索引、事务边界和迁移方式。
- 当前仓库已实现用户、Session、数据源、外部工单身份、CaseSnapshot、DiagnosisTask、TaskEvent、Outbox、DiagnosisStep、ToolExecution、EvidenceItem、DiagnosisReport、ReportEvidence 与反馈表；知识库等后续表尚未实现。
- PostgreSQL 是 MESGuard 的事实来源。外部 MES/ERP SQL Server 只读访问，不把外部业务表复制成可写主数据。
- 本文先固定数据库边界，具体 SQL 文件、Go 结构体和 Repository 实现随后按 M0/M1 纵向切片落地。

## 设计决策

### 数据库职责

PostgreSQL 保存 MESGuard 自己产生和需要追溯的事实：

- 用户、角色和登录会话；
- 外部工单身份以及诊断时的工单快照；
- 诊断任务、步骤、工具调用、证据、报告和人工反馈；
- TaskEvent 和 OutboxEvent；
- 附件元数据与权限关系；
- M2 的会话消息、知识文档、切块、向量和评测结果。

PostgreSQL 不保存：

- 外部 SQL Server 的整库副本；
- MinIO 原始文件二进制；
- Redis 缓存和 SSE 通知状态；
- RabbitMQ 任务事实；
- 模型密钥、数据库密码或对象存储凭证。

### 技术边界

```text
GORM
  普通CRUD、简单查询、事务编排、结构体扫描

goose + SQL文件
  数据库结构版本和数据迁移

原生SQL
  Outbox领取、Worker抢占、FOR UPDATE SKIP LOCKED、复杂查询

Repository接口
  隔离业务层与GORM、pgx和具体SQL实现
```

当前项目已经使用 GORM。M1 不更换 ORM，而是限制 `*gorm.DB` 只能出现在基础设施和 Repository 内部，Handler、UseCase 和领域对象不直接依赖它。

### 主键与时间

- 业务对象使用应用层生成的 UUIDv7；
- TaskEvent 使用每个任务内单调递增的 `seq`，用于 SSE 断线续传；
- 表内同时保留 `created_at`、`updated_at`，需要时增加 `started_at`、`completed_at`；
- 所有时间使用 PostgreSQL `timestamptz`，应用和数据库统一以 UTC 保存，展示时再转换为用户时区；
- 不使用数据库自增整数作为对外暴露的业务标识。

UUIDv7 的时间有序特性可以减少随机UUID作为索引键时的写入碎片，同时避免把连续自增ID暴露给外部调用者。UUID由应用生成，便于测试、消息幂等和跨运行角色创建记录。

### 结构化字段与 JSONB

稳定查询、关联、权限和状态字段必须使用独立列：

```text
id、user_id、task_id、status、scope、created_at、expires_at
```

来源系统原始快照、诊断范围、事件载荷等需要独立演进和历史重放的JSONB契约必须带Schema版本：

```text
payload JSONB NOT NULL
payload_schema_version INTEGER NOT NULL
```

简单标签、别名集合等不承担独立契约的JSONB不强制单独设置版本。不能把所有业务字段都塞进JSONB；需要过滤、唯一约束、权限判断或高频排序的字段必须结构化，否则数据库无法有效约束它们。

模型原始输入和输出默认不直接保存。数据库保存脱敏摘要、结构化结果、Token、耗时、成本和证据引用；需要保留的原始敏感内容必须经过单独的加密和保留策略评审。

## 逻辑关系

```text
users ───────────────┬─ sessions
                     ├─ conversations ── messages ── message_attachments
                     │        └────────── conversation_summaries
                     ├─ diagnosis_tasks ── diagnosis_steps
                     │                  ├─ tool_executions
                     │                  ├─ evidence_items
                     │                  ├─ task_events
                     │                  ├─ diagnosis_reports ── report_reviews
                     │                  └─ outbox_events
                     └─ attachments

data_sources ── external_cases ── case_snapshots
       │                  └────── diagnosis_task_data_sources
       └─ schema_catalog_versions ── schema_catalog_entries

knowledge_documents ── knowledge_document_versions ── knowledge_chunks
                                                        └─ embeddings（M2）
```

## 表设计

以下是逻辑表设计。字段名以 snake_case 表示，最终SQL类型和默认值在迁移文件中固定。

### users

保存本地账号和角色。

关键字段：

- `id UUID PRIMARY KEY`；
- `username`，大小写规范化后唯一；
- `display_name`；
- `password_hash`，只保存 Argon2id 哈希；
- `must_change_password`；
- `password_changed_at`；
- `role`，一期为 `analyst` 或 `admin`；
- `status`，至少包含 `active`、`disabled`；
- `last_login_at`、`created_at`、`updated_at`。

约束：

- 禁止保存明文密码、可逆加密密码或日志中的密码；
- 禁用用户不能创建新任务和新会话，但历史任务保留操作者引用；
- 角色变更只能通过明确的管理员用例发生；
- 不对历史任务使用物理级联删除。

### sessions

服务端登录会话使用 PostgreSQL 持久化，Redis只能作为可选缓存。

关键字段：

- `id UUID PRIMARY KEY`；
- `user_id UUID NOT NULL REFERENCES users(id)`；
- `token_hash`，只保存会话令牌哈希，不保存明文Cookie值；
- `csrf_token_hash`，只保存CSRF Token哈希；
- `idle_expires_at`、`absolute_expires_at`；
- `revoked_at`；
- `last_seen_at`、`created_at`。

索引：

- `UNIQUE(token_hash)`；
- 对未撤销会话建立`(user_id, idle_expires_at) WHERE revoked_at IS NULL`部分索引。

不能把过期时间与`now()`的比较写入部分索引谓词，因为`now()`会随时间变化，不满足索引表达式的不可变性要求。查询时再判断空闲和绝对过期时间：

```sql
CREATE INDEX sessions_unrevoked_idx
ON sessions(user_id, idle_expires_at)
WHERE revoked_at IS NULL;

SELECT id, user_id, idle_expires_at, absolute_expires_at
FROM sessions
WHERE user_id = $1
  AND revoked_at IS NULL
  AND idle_expires_at > now()
  AND absolute_expires_at > now();
```

HTTP层只读取当前会话对应的用户，不把用户角色信任在浏览器提交的字段中。

### data_sources

保存外部数据源元信息，不保存连接密码。

关键字段：

- `id UUID PRIMARY KEY`；
- `code`，内部稳定标识；
- `name`；
- `source_type`，一期为 `sqlserver`；
- `environment`，例如 `demo`、`production`；
- `status`，例如 `active`、`disabled`；
- `credential_ref`，指向配置或密钥文件中的引用；
- `metadata JSONB`；
- `created_at`、`updated_at`。

一期只启用一个数据源，但表结构保留多个数据源边界。数据源连接凭证由进程配置装配，不能进入请求、任务事件或模型上下文。

### schema_catalog_versions

保存一个数据源的Schema Catalog发布批次。Catalog采用整版本发布，不能在每个Entry上分散维护`is_current`。

关键字段：

- `id UUID PRIMARY KEY`；
- `data_source_id UUID NOT NULL REFERENCES data_sources(id)`；
- `version INTEGER NOT NULL`；
- `status`，`draft`、`published`、`retired`；
- `scan_status`，`pending`、`running`、`succeeded`、`failed`；
- `scan_attempt_count`；
- `scan_owner`、`scan_lease_until`；
- `scan_started_at`、`scan_completed_at`、`scan_error`；
- `source_introspected_at`；
- `created_by UUID REFERENCES users(id)`；
- `published_by UUID REFERENCES users(id)`、`published_at`；
- `created_at`。

约束和索引：

```sql
UNIQUE(data_source_id, version)

CREATE UNIQUE INDEX schema_catalog_one_published_idx
ON schema_catalog_versions(data_source_id)
WHERE status = 'published';

CREATE UNIQUE INDEX schema_catalog_one_active_scan_idx
ON schema_catalog_versions(data_source_id)
WHERE scan_status IN ('pending', 'running');
```

新版本先以`draft`和`pending`扫描状态创建。低频Schema扫描由后台扫描器通过PostgreSQL租约领取，失败或进程退出后可以恢复，不为M1额外增加RabbitMQ队列。扫描成功并完成人工校验后，才在同一事务中把旧版本标记为`retired`、新版本标记为`published`。部分唯一索引保证每个数据源最多只有一个已发布版本。

### schema_catalog_entries

保存允许模型和预定义工具使用的表、视图、字段及其语义元数据。它不是从SQL Server全库直接暴露出来的系统目录，而是经过管理员确认、白名单和脱敏策略处理后的可查询目录。

关键字段：

- `id UUID PRIMARY KEY`；
- `catalog_version_id UUID NOT NULL REFERENCES schema_catalog_versions(id)`；
- `object_schema`、`object_name`、`object_type`；
- `column_name`、`data_type`、`nullable`；对象级条目的`column_name`为空；
- `comment`、`semantic_aliases JSONB`；
- `queryable`、`sensitivity_level`；
- `created_at`。

约束和索引：

```text
UNIQUE NULLS NOT DISTINCT(catalog_version_id, object_schema, object_name, column_name)
(catalog_version_id, object_name, queryable)
```

PostgreSQL 16的`NULLS NOT DISTINCT`保证同一版本不会出现多条重复的对象级条目。Text-to-SQL通过`schema_catalog_versions.status = 'published'`找到当前版本，只能引用其中`queryable = true`的条目。管理员修改注释、别名、敏感级别或允许查询范围时创建新的Catalog版本，不能静默改变历史任务使用的目录。

### external_cases

保存外部工单身份，不保存工单完整内容。

关键字段：

- `id UUID PRIMARY KEY`；
- `data_source_id UUID NOT NULL REFERENCES data_sources(id)`；
- `external_case_key`，外部系统中的工单号；
- `external_case_type`；
- `last_seen_at`；
- `created_at`、`updated_at`。

约束：

```text
UNIQUE(data_source_id, external_case_key)
```

外部工单被删除或编号规则变化时，历史 `external_cases` 和快照不自动删除。

### case_snapshots

保存发起诊断时从SQL Server读取的不可变输入。

关键字段：

- `id UUID PRIMARY KEY`；
- `external_case_id UUID NOT NULL REFERENCES external_cases(id)`；
- `snapshot_no INTEGER NOT NULL`；
- `payload JSONB NOT NULL`；
- `payload_schema_version INTEGER NOT NULL`；
- `content_hash`；
- `source_read_at`；
- `redaction_status`；
- `truncation_status`；
- `created_by UUID REFERENCES users(id)`；
- `created_at`。

约束：

```text
UNIQUE(external_case_id, snapshot_no)
```

快照创建后不可更新。重新诊断必须创建新快照，不能覆盖旧诊断的输入。

### attachments

保存文件元数据和生命周期状态，原始对象位于MinIO。

关键字段：

- `id UUID PRIMARY KEY`；
- `owner_user_id UUID NOT NULL REFERENCES users(id)`；
- `idempotency_key`、`upload_request_fingerprint`；
- `scope`，一期为 `session` 或 `personal`；
- `storage_bucket` 和 `storage_object_key`；
- `original_filename`；
- `content_type`；
- `size_bytes`；
- `sha256`；
- `processing_status`，例如 `uploading`、`uploaded`、`processing`、`ready`、`failed`；
- `upload_attempt_count`；
- `upload_lease_owner`、`upload_lease_until`；
- `last_upload_error`、`uploaded_at`；
- `orphan_cleanup_at`；
- `retained_until`；
- `deleted_at`；
- `created_at`、`updated_at`。

安全约束：

- `(owner_user_id, idempotency_key)`建立唯一约束，同一用户重试返回或恢复同一附件记录；
- 上传失败或租约过期允许增加尝试次数后接管，不能因为唯一键存在就永久拒绝重试；
- 相同幂等键对应不同`upload_request_fingerprint`时返回冲突；
- Redis只能缓存上传锁，不能替代PostgreSQL中的幂等事实和租约；
- `storage_object_key` 只对服务端适配器可见，不进入模型上下文；
- 消息和任务只保存 `attachment_id`；
- `session` 只允许当前会话或关联诊断任务读取；
- `personal` 只允许所有者读取和检索；
- 未绑定任何消息、任务或文档的对象，超过24小时由清理任务删除；
- 已被证据引用的对象不能直接物理删除，必须先执行引用检查。

附件与任务、消息、知识文档之间使用关联表，不通过多个可空外键承担所有关系。

### diagnosis_tasks

表示一次完整诊断尝试，是M1的核心聚合根。

关键字段：

- `id UUID PRIMARY KEY`；
- `created_by UUID NOT NULL REFERENCES users(id)`；
- `external_case_id UUID NOT NULL REFERENCES external_cases(id)`；
- `case_snapshot_id UUID NOT NULL REFERENCES case_snapshots(id)`；
- `retry_of UUID NULL REFERENCES diagnosis_tasks(id)`；
- `idempotency_key`；
- `request_fingerprint`；
- `request_text`；
- `request_scope JSONB`，保存用户选择的数据范围和诊断选项；
- `request_scope_schema_version INTEGER NOT NULL`；
- `status`，`pending`、`running`、`cancel_requested`、`succeeded`、`failed`、`cancelled`；
- `attempt_count`；
- `claim_owner`、`claimed_at`、`lease_until`；
- `cancel_requested_at`；
- `last_error_code`、`last_error_message`；
- `started_at`、`completed_at`；
- `created_at`、`updated_at`。

约束：

- `(created_by, idempotency_key)`建立唯一约束；相同Key但`request_fingerprint`不同必须返回冲突；
- `retry_of` 只建立历史关联，不复用原任务的步骤、事件或报告；
- `succeeded` 只表示执行完成，不代表报告结论正确；
- 状态转换由领域用例控制，数据库只通过枚举或 CHECK 约束阻止非法字符串。

### diagnosis_task_data_sources

记录一次任务实际使用过的数据源，而不是只记录发起时的默认数据源。

关键字段：

- `task_id UUID REFERENCES diagnosis_tasks(id)`；
- `data_source_id UUID REFERENCES data_sources(id)`；
- `catalog_version_id UUID REFERENCES schema_catalog_versions(id)`；
- `access_scope JSONB`；
- `access_scope_schema_version INTEGER NOT NULL`；
- `confirmed_by UUID REFERENCES users(id)`；
- `confirmed_at`。

主键为 `(task_id, data_source_id)`。

### diagnosis_task_attachments

记录任务使用的附件及其用途。

关键字段：

- `task_id UUID REFERENCES diagnosis_tasks(id)`；
- `attachment_id UUID REFERENCES attachments(id)`；
- `purpose`，例如 `problem_image`、`chat_screenshot`、`log_file`；
- `created_at`。

主键为 `(task_id, attachment_id)`。该关联不会自动把 `session` 附件提升为个人知识库。

### diagnosis_steps

记录诊断Graph中的稳定步骤和每次尝试结果。

关键字段：

- `id UUID PRIMARY KEY`；
- `task_id UUID NOT NULL REFERENCES diagnosis_tasks(id)`；
- `step_no INTEGER NOT NULL`；
- `step_type`；
- `display_name`；
- `status`，例如 `pending`、`running`、`succeeded`、`failed`、`skipped`；
- `attempt_count`；
- `input_summary JSONB`；
- `output_summary JSONB`；
- `last_error_code`、`last_error_message`；
- `started_at`、`completed_at`、`duration_ms`；
- `created_at`、`updated_at`。

M1 当前迁移按执行尝试保留步骤结果，约束为：

```text
UNIQUE(task_id, attempt_count, step_no)
```

同一尝试内不允许重复步骤序号；租约过期接管后的新尝试保留独立步骤轨迹。每次重要工具调用仍在 `tool_executions` 中追加记录。

### tool_executions

记录模型、数据库、附件、检索、代码和Web工具的实际调用。

关键字段：

- `id UUID PRIMARY KEY`；
- `task_id UUID NOT NULL REFERENCES diagnosis_tasks(id)`；
- `step_id UUID REFERENCES diagnosis_steps(id)`；
- `tool_name`、`tool_version`；
- `model_provider`、`model_id`；
- `input_summary JSONB`、`output_summary JSONB`；
- `status`、`error_kind`、`retry_count`；
- `started_at`、`completed_at`、`duration_ms`；
- `input_tokens`、`output_tokens`、`cached_tokens`；
- `estimated_cost`、`currency`；
- `row_count`、`truncated`；
- `created_at`。

普通日志只记录调用ID和脱敏摘要。原始SQL、完整附件内容和敏感模型Prompt不直接写入应用日志。

### evidence_items

保存可以支持报告判断的证据快照或引用。

关键字段：

- `id UUID PRIMARY KEY`；
- `task_id UUID NOT NULL REFERENCES diagnosis_tasks(id)`；
- `source_type`，例如 `case_snapshot`、`sql_result`、`attachment`、`knowledge_chunk`、`code`、`web`；
- `source_id UUID NULL`；
- `source_locator JSONB`，例如页码、行号、字段、查询标识或网页URL；
- `source_locator_schema_version INTEGER NOT NULL`；
- `content_text TEXT NULL`，保存可展示、全文检索或引用的脱敏文本；
- `content_data JSONB NULL`，保存列、行、页码、区域、版本等结构化证据；
- `content_schema_version INTEGER NULL`；
- `content_hash`；
- `collected_at`；
- `redaction_status`、`truncated`；
- `validity_status`，例如 `valid`、`superseded`、`invalid`；
- `created_at`。

以下规则由goose迁移创建为PostgreSQL CHECK约束，应用层同时校验并返回更友好的业务错误：

```sql
CONSTRAINT evidence_content_present_ck CHECK (
    NULLIF(BTRIM(content_text), '') IS NOT NULL
    OR content_data IS NOT NULL
),
CONSTRAINT evidence_content_data_type_ck CHECK (
    content_data IS NULL
    OR jsonb_typeof(content_data) IN ('object', 'array')
),
CONSTRAINT evidence_content_schema_ck CHECK (
    (
        content_data IS NULL
        AND content_schema_version IS NULL
    )
    OR
    (
        jsonb_typeof(content_data) IN ('object', 'array')
        AND content_schema_version > 0
    )
)
```

这些约束保证`content_text`和`content_data`至少有一个有效值，拒绝空字符串、JSON字面量`null`和没有Schema版本的结构化证据。代码、OCR和网页证据主要使用`content_text`，SQL结果和多模态定位主要使用`content_data`，必要时两者同时保存。

证据默认追加保存。发现错误时新增更正证据，并将旧证据标记为不可用，不在原记录上静默修改。

### diagnosis_reports

保存一次诊断任务生成的报告。

关键字段：

- `id UUID PRIMARY KEY`；
- `task_id UUID NOT NULL UNIQUE REFERENCES diagnosis_tasks(id)`；
- `conclusion_status`，`conclusive`、`probable`、`inconclusive`；
- `business_summary JSONB`；
- `technical_summary JSONB`；
- `report_schema_version INTEGER NOT NULL`；
- `risk_level`；
- `model_provider`、`model_id`、`prompt_version`；`model_id` 保存供应商请求使用的完整模型标识，不再从中猜测或拆分独立版本；
- `generated_at`、`created_at`、`updated_at`。

报告结论必须通过关联表引用证据。没有证据引用的内容只能作为待验证假设。

### report_evidence

报告与证据的多对多关系。

关键字段：

- `report_id UUID REFERENCES diagnosis_reports(id)`；
- `evidence_id UUID REFERENCES evidence_items(id)`；
- `claim_key`；
- `claim_text`，保存报告中被证据支持或反驳的完整判断；
- `support_type`，例如 `supports`、`contradicts`、`context`；
- `created_at`。

主键为 `(report_id, evidence_id, claim_key)`。

### report_reviews

保存人工反馈，不是审批流。

关键字段：

- `id UUID PRIMARY KEY`；
- `report_id UUID NOT NULL REFERENCES diagnosis_reports(id)`；
- `reviewed_by UUID NOT NULL REFERENCES users(id)`；
- `verdict`，`adopted`、`partially_adopted`、`rejected`；
- `comment`；
- `created_at`。

反馈可以追加多次。当前有效反馈通过查询规则确定，不覆盖历史反馈。

### task_events

保存任务进度、审计和SSE回放事件。

关键字段：

- `task_id UUID NOT NULL REFERENCES diagnosis_tasks(id)`；
- `seq BIGINT NOT NULL`；
- `event_type`；
- `payload JSONB NOT NULL`；
- `payload_schema_version INTEGER NOT NULL`；
- `created_at`。

主键为 `(task_id, seq)`。写入新事件时必须在事务中为该任务分配下一个序号，不能依赖Redis计数器。

查询断点使用：

```sql
SELECT task_id, seq, event_type, payload, payload_schema_version, created_at
FROM task_events
WHERE task_id = $1 AND seq > $2
ORDER BY seq
LIMIT $3;
```

### outbox_events

保存需要发布到RabbitMQ的待发送事件。

关键字段：

- `id UUID PRIMARY KEY`；
- `event_type`；
- `aggregate_type`、`aggregate_id`；
- `payload JSONB NOT NULL`；
- `payload_schema_version INTEGER NOT NULL`；
- `attempt_count`；
- `available_at`；
- `locked_at`、`locked_by`、`locked_until`；
- `published_at`；
- `requeue_count`；
- `last_requeued_at`、`last_requeued_by UUID REFERENCES users(id)`；
- `last_error`；
- `created_at`。

OutboxEvent必须和任务、首个TaskEvent或状态变化写入同一个PostgreSQL事务。正常发布成功后标记`published_at`，不能删除任务事实。admin执行受审计的失败任务恢复时，可以在同一事务中增加`requeue_count`、记录操作者、清空`published_at`并重新设置`available_at`，以便用同一个消息ID补发；普通业务代码不能重置已发布Outbox。

Outbox Relay使用原生SQL和行锁领取：

```sql
SELECT id
FROM outbox_events
WHERE published_at IS NULL
  AND available_at <= now()
  AND (locked_until IS NULL OR locked_until < now())
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT $1;
```

Relay在短事务中领取记录并写入有限租约，提交后再发布RabbitMQ。进程崩溃时，其他Relay可以在`locked_until`过期后重新领取。租约不能保证绝对只发布一次，消费者仍必须幂等。

消息只携带任务ID、事件类型、版本和幂等标识，不携带工单全文、图片或大段证据。

## M2表设计边界

M2再增加以下表，避免M1提前引入知识库的复杂状态：

### conversations 与 messages

`conversations`保存用户会话和可见范围，`messages`保存用户、助手和工具消息。消息只通过关联表引用附件，不保存永久URL或完整Base64。

M2初期消息发送后不可编辑，不支持对话分支。用户修正问题时发送新消息；重新生成回答也创建新的追加记录，不修改已被摘要或引用的历史消息。

消息需要记录：

- `role`；
- `content`或结构化内容；
- `content_schema_version`，结构化内容非空时必填；
- `generation_status`，例如 `completed`、`interrupted`、`failed`；
- `sequence_no`；
- `model_provider`、`model_id`、`prompt_version`；
- `created_at`。

`message_attachments`保存消息和附件的多对多关系，并记录附件在消息中的顺序和用途：

- `message_id UUID REFERENCES messages(id)`；
- `attachment_id UUID REFERENCES attachments(id)`；
- `position INTEGER`；
- `purpose`；
- `created_at`。

主键为 `(message_id, attachment_id)`，并增加`UNIQUE(message_id, position)`保证附件展示顺序唯一。`position`是附件在消息中的顺序，不是PDF页码；模型读取的页码或区域记录在ToolExecution和EvidenceItem的定位信息中。

诊断任务附件和消息附件是两条独立关联，不通过修改附件的归属范围实现权限提升。

### conversation_summaries

保存动态上下文治理生成的派生摘要，不覆盖原始消息。

关键字段：

- `id UUID PRIMARY KEY`；
- `conversation_id UUID NOT NULL REFERENCES conversations(id)`；
- `range_start_seq BIGINT NOT NULL`、`range_end_seq BIGINT NOT NULL`；
- `summary_text TEXT NULL`、`summary_data JSONB NULL`；
- `summary_schema_version INTEGER NOT NULL`；
- `input_token_count`、`output_token_count`；
- `model_provider`、`model_id`、`prompt_version`；
- `content_hash`；
- `status`，`active`或`superseded`；
- `created_at`。

以下规则由goose迁移创建为PostgreSQL CHECK约束：

```sql
CONSTRAINT conversation_summary_range_ck CHECK (
    range_start_seq >= 1
    AND range_end_seq >= range_start_seq
),
CONSTRAINT conversation_summary_content_ck CHECK (
    NULLIF(BTRIM(summary_text), '') IS NOT NULL
    OR summary_data IS NOT NULL
),
CONSTRAINT conversation_summary_data_type_ck CHECK (
    summary_data IS NULL
    OR jsonb_typeof(summary_data) IN ('object', 'array')
),
CONSTRAINT conversation_summary_schema_version_ck CHECK (
    summary_schema_version > 0
)
```

应用层在写入前执行同样的校验，Repository根据约束名称把数据库错误转换成稳定的应用错误。上下文组装器只使用当前有效摘要；摘要生成失败时回退到原始消息，不能造成聊天记录丢失。

### knowledge_documents、knowledge_document_versions、knowledge_chunks

文档逻辑身份、解析版本和检索块分开保存：

- `knowledge_documents`保存范围：`global` 或 `personal`、所有者和删除状态；
- `knowledge_document_versions`保存解析器、OCR、VLM和Embedding版本；
- `knowledge_chunks`保存文本、页码、章节、元素类型、attachment_id、内容哈希和解析元数据。

文档重新解析或更换Embedding模型时创建新版本，不能覆盖已经被历史报告引用的旧版本。

### embeddings

向量表保存：

- `chunk_id`；
- `embedding_model`；
- `embedding_dimensions`；
- `embedding vector(n)`；
- `created_at`。

向量维度由实际启用的Embedding模型和输出配置决定，经过POC确认后再在迁移中固定为`vector(n)`。单独保存`embedding_dimensions`不能让一个近似索引兼容任意维度；未来同时使用不同维度时，应使用独立表、分区或按模型建立的表达式索引。

M2采用渐进策略：

1. 数据量较小时使用`vector(n)`和精确余弦距离检索，先建立正确性基线；
2. 精确检索达到性能阈值后，默认评估`HNSW + vector_cosine_ops`；
3. 当前pgvector的HNSW/IVFFlat索引对`vector`支持最多2000维，对`halfvec`支持最多4000维；
4. `halfvec(n)`只有在维度超过`vector`索引上限、索引内存成为瓶颈，且离线评测证明召回损失可接受时才启用；
5. IVFFlat仅在数据量、构建时间或内存特征更适合时通过对比实验选择；
6. 近似索引叠加权限过滤时评测Recall@K，并按pgvector版本评估迭代扫描。

检索必须在SQL中按用户权限、知识范围和文档状态过滤，不能先把全库候选返回应用再补做权限判断。

## 索引和约束原则

必须建立的索引类型：

- 所有外键列的查询索引；
- 状态与时间组合索引，例如 `(status, created_at)`；
- TaskEvent 的 `(task_id, seq)`；
- Outbox 的`(available_at, locked_until, created_at) WHERE published_at IS NULL`部分索引；
- 会话的`(user_id, idle_expires_at) WHERE revoked_at IS NULL`部分索引，不能包含`now()`；
- 外部工单的 `(data_source_id, external_case_key)` 唯一索引；
- 附件哈希、知识文档版本和Chunk来源索引。

索引设计必须通过实际查询和 `EXPLAIN (ANALYZE, BUFFERS)` 验证，不能因为“可能以后会查询”就为所有字段建立索引。

约束优先级：

1. PostgreSQL约束保证数据结构合法：唯一、非空、外键、CHECK；
2. 领域用例保证状态转换合法；
3. Repository保证查询范围和锁语义；
4. Handler只负责输入绑定和权限上下文，不直接修改状态字段。

## 事务边界

### 创建诊断任务

以下操作必须在一个PostgreSQL事务中完成：

```text
确认外部工单快照
  + 创建 CaseSnapshot
  + 创建 DiagnosisTask(pending)
  + 创建首个 TaskEvent
  + 创建 OutboxEvent(unpublished)
```

事务提交后再返回`task_id`。事务中不能调用SQL Server、模型、MinIO或RabbitMQ。

### Worker领取任务

Worker在短事务中检查任务状态、租约和幂等条件，成功后写入领取者和`lease_until`。模型调用和SQL Server查询必须在事务外执行，避免长期持有数据库锁。

当前 PostgreSQL Repository 已实现按任务 ID 的原子 Claim 和续租契约：首次领取或过期接管递增 `attempt_count`，返回 `claim_owner + attempt_count` fencing token，并分别追加 `task_started/task_reclaimed` 事件；活跃租约、取消中任务和终态任务返回显式 disposition，不重复执行。续租只允许仍处于 `running`、租约未过期且 fencing token 完全匹配的 Worker，旧 token 更新行数为 0。实际 RabbitMQ Consumer 和 Agent 步骤写入将在后续切片接入。

### 写入步骤和证据

每个重要步骤完成后使用短事务写入步骤状态、工具调用摘要、证据和TaskEvent。Redis通知必须发生在事务提交之后。

### 生成报告

报告正文、报告引用关系、任务最终状态和最终TaskEvent在同一事务中提交。报告生成失败时不能把任务标记为`succeeded`。

### 取消任务

取消请求只在事务中将`pending`或`running`任务标记为`cancel_requested`并追加事件。Worker在步骤和工具边界检查取消，随后写入`cancelled`。关闭SSE连接不触发数据库状态变更。

当前取消 Repository 已实现任务行锁、状态条件更新、任务内下一个 `seq` 分配和事件写入的单事务边界；重复取消不追加事件，`succeeded/failed` 返回稳定状态冲突。Worker 协作写入 `cancelled` 尚未实现。

## 迁移策略

使用 `goose` 管理版本化SQL文件，不使用GORM AutoMigrate作为生产迁移机制。

推荐目录：

```text
db/
  migrations/
    00001_enable_extensions.sql
    00002_create_users_sessions.sql
    00003_create_data_sources_external_cases.sql
    00004_create_schema_catalog.sql
    00005_create_diagnosis_tasks.sql       # 当前已实现：快照、任务和任务数据源
    00006_create_diagnosis_reports_reviews.sql # 当前已实现：报告最小元数据和追加反馈
    00007_create_task_events_outbox.sql    # 当前已实现：TaskEvent和Outbox事实表
    00008_create_evidence_reports.sql      # 后续 M1-B
```

每个迁移文件包含明确的Up和Down部分。生产环境通过独立迁移命令或发布步骤执行，API和Worker启动时只检查数据库是否达到要求版本，不在多个实例启动过程中同时自动改表。

迁移要求：

- 一个迁移只完成一个可审查的结构变化；
- 大表新增索引评估并发创建和锁影响；
- 删除字段、收缩类型和数据清理分成多个发布阶段；
- 数据迁移必须记录行数、耗时和失败原因；
- 迁移前后执行Schema检查和关键查询测试；
- 生产迁移前先在备份恢复库演练；
- 不依赖回滚脚本替代备份，无法安全逆转的迁移采用前向修复。

## Repository与事务接口

业务层不接收`*gorm.DB`，只依赖面向用例的接口，例如：

```go
type DiagnosisTaskRepository interface {
    CreateTask(ctx context.Context, input CreateTaskRecord) (TaskCreateResult, error)
    GetTask(ctx context.Context, taskID uuid.UUID) (DiagnosisTask, error)
}
```

需要跨多个Repository保持原子性的用例使用事务执行器，但事务接口不把GORM泄漏到业务层：

```go
type TxManager interface {
    WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

实际实现可以使用GORM Transaction，Outbox领取等特殊路径可以使用同一连接上的原生SQL。

Repository必须：

- 使用调用方传入的context和超时；
- 将数据库错误转换成稳定的领域错误；
- 不在查询失败时返回部分成功实体；
- 对更新状态使用带旧状态条件的SQL，避免并发覆盖；
- 对幂等冲突返回可识别的业务错误；
- 将分页、排序和过滤限制在允许字段集合内。

## 删除、保留与归档

### 永久事实

任务、TaskEvent、工具调用、证据、报告和反馈默认追加保存。发现错误使用更正、失效或补充记录，不物理覆盖历史事实。

### 可停用对象

用户、数据源、知识文档和附件使用状态或软删除字段。停用后不再出现在新的查询和检索中，但历史记录仍能解析其原始ID。

### 诊断附件

- `session`附件不进入个人或全局知识库；
- 未绑定的孤儿对象超过24小时清理；
- 已绑定任务或证据的对象暂不立即删除；
- 后续配置保留期时，先检查报告引用、导出需求和审计保留要求；
- 删除MinIO对象前先写入删除状态和审计记录，失败时可重试；
- 删除原文件后，报告仍保留必要的脱敏证据快照和哈希。

## 备份与恢复

PostgreSQL是事实来源，至少需要：

- 定期逻辑备份或物理备份；
- 备份复制到Linux VM之外；
- 在独立恢复环境验证迁移和关键查询；
- 记录备份版本、数据库版本和Compose镜像版本；
- 定期验证TaskEvent、Outbox和附件引用的一致性。

Redis或RabbitMQ丢失不能通过备份恢复任务事实。RabbitMQ丢失后由PostgreSQL中的未发布Outbox补偿；Redis丢失后重新读取TaskEvent构建通知状态。

## M1与M2实现顺序

### M0

- 建立goose目录和迁移命令；
- 建立事务执行器和Repository错误约定；
- 增加配置、迁移和启动失败测试。

### M1第一批

- `users`、`sessions`、`data_sources`；
- `schema_catalog_versions`、`schema_catalog_entries`；
- `external_cases`、`case_snapshots`；
- `attachments`及诊断任务附件关联；
- `diagnosis_tasks`、`diagnosis_task_data_sources`（当前已通过 `00005` 建立）。

### M1第二批

- `task_events`、`outbox_events`（当前已通过 `00007` 建立，并由任务创建事务写入首个事实）；
- `diagnosis_steps`、`tool_executions`；
- Outbox Relay、Worker领取和幂等所需索引。

### M1第三批

- `evidence_items`、`report_evidence`；
- `diagnosis_reports` 的完整证据关联和 `report_reviews` 的正式任务联调；当前 `00006` 已先建立报告最小元数据和追加反馈表，任务创建基础已接通，但正式报告仍待 Worker。
- 数据库查询、并发、重复消息和取消测试。

### M2

- `conversations`、`messages`、`message_attachments`、`conversation_summaries`；
- 知识文档、解析版本、Chunk、Embedding和评测结果；
- pgvector扩展和向量索引在实际Embedding维度确定后加入迁移。

## 验收清单

数据库设计实现后必须验证：

1. 全新数据库可以按顺序完成所有迁移。
2. 重复执行迁移不会重复创建表、索引或数据。
3. 创建诊断任务的快照、任务、事件和Outbox具有同一事务边界。
4. 外部工单变化不会修改已有CaseSnapshot。
5. 重复幂等请求不会创建第二个任务。
6. 两个Worker并发领取时不会领取同一任务。
7. SSE使用TaskEvent序号可以补读，不依赖Redis计数。
8. 任务、报告和证据不能被普通删除接口物理删除。
9. 附件权限、孤儿清理和报告引用检查可重复测试。
10. 迁移、索引和关键查询使用测试数据执行`EXPLAIN`验证。

## 参考资料

- [PostgreSQL Documentation](https://www.postgresql.org/docs/current/)
- [PostgreSQL CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)
- [PostgreSQL Partial Indexes](https://www.postgresql.org/docs/current/indexes-partial.html)
- [GORM Documentation](https://gorm.io/docs/)
- [GORM SQL Builder and Raw SQL](https://gorm.io/docs/sql_builder.html)
- [goose Documentation](https://pressly.github.io/goose/)
- [pgx PostgreSQL Driver](https://github.com/jackc/pgx)
- [pgvector Documentation](https://github.com/pgvector/pgvector)

## 后续工作

用户、Session、DataSource、ExternalCase、CaseSnapshot、DiagnosisTask、TaskEvent和Outbox基础已经落地；TaskEvent 游标查询、取消命令、Worker Claim/续租/fencing 和 Outbox Relay 也已实现。后续按`docs/roadmap.md`的纵向切片继续实现 RabbitMQ Consumer、Worker执行、证据和报告。
