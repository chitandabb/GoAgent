# MESGuard 异步消息与可靠投递设计

## 文档状态

- 本文定义 MESGuard 的 PostgreSQL Outbox、RabbitMQ 拓扑、发布确认、消费者确认、重试、死信、幂等和故障恢复规则。
- 当前仓库仍处于 Web 基础骨架阶段；RabbitMQ、Outbox Relay 和 Worker 尚未实现。
- 本文是 M1-B/M1-C 的实现输入，不代表当前系统已经具备异步诊断能力。
- PostgreSQL 是业务事实来源，RabbitMQ 只负责唤醒和分发，不保存任务、报告或证据的唯一副本。

## 设计目标

消息链路需要解决四个独立问题：

1. 数据库事务提交后，API 在发布消息前崩溃时不能漏发任务。
2. RabbitMQ 或网络故障时，消息可以重试，最终失败可以人工恢复。
3. RabbitMQ 的重复投递不能导致重复步骤、重复报告或重复扣费。
4. Worker 执行很长任务时崩溃，其他 Worker 可以在租约过期后安全接管。

本设计只承诺**至少一次投递**。不承诺 RabbitMQ 或 Outbox 的绝对只投递一次；业务幂等由 PostgreSQL 条件更新、唯一约束和 fencing token 保证。

## 适用范围

### M1

M1 只发布诊断执行消息：

```text
API 事务：CaseSnapshot + DiagnosisTask + TaskEvent + OutboxEvent
    ↓
Outbox Relay
    ↓
diagnosis.execute.v1
    ↓
Diagnosis Worker
```

### M2

M2 增加文档入库消息，但不改变 M1 的可靠性语义：

```text
API/Admin 事务：KnowledgeDocument + IngestionTask + OutboxEvent
    ↓
Outbox Relay
    ↓
knowledge.ingest.v1
    ↓
Ingestion Worker
```

诊断和文档入库拥有独立队列、重试链路、并发上限和死信队列，避免大型文档处理挤占诊断任务。

## 运行角色

仍然使用一个 Go 代码库和手动依赖装配，但运行角色独立：

```text
mesguard-api
    只写业务事务和 Outbox，不直接发布 RabbitMQ

mesguard-outbox-relay
    领取待发布 Outbox，发布并等待 Publisher Confirm

mesguard-diagnosis-worker
    消费诊断队列，执行诊断步骤

mesguard-ingestion-worker
    M2 消费入库队列，执行解析、OCR、VLM 和 Embedding
```

这不是微服务拆分。各角色共享领域包、Repository 接口和基础设施适配器，使用同一镜像或同一代码构建的不同启动命令。Relay 独立运行是为了让文档入库消息不依赖 Diagnosis Worker 是否存活，也让发布吞吐可以独立调节。

## RabbitMQ 拓扑

### 主交换机和工作队列

使用持久化 `direct` 交换机 `mesguard.tasks`。这些消息表达“交给某类 Worker 执行一次”，不是向多个订阅者广播的领域事件。

```text
mesguard.tasks (durable, direct)
├─ diagnosis.execute.v1
│    └─ mesguard.diagnosis.execute.v1
└─ knowledge.ingest.v1
     └─ mesguard.knowledge.ingest.v1
```

主队列要求：

- durable queue；
- persistent message；
- 手动 ACK，禁止 auto-ack；
- 独立消费者并发和 prefetch 配置；
- 绑定各自的重试交换机和最终死信交换机；
- 消息体只保存任务定位和版本信息。

M1 默认每个 Diagnosis Worker 的 prefetch 为 1，先保证长任务不会被单个进程预取后长期占用。压测后可以按 CPU、模型并发和外部 SQL Server 限额提高，不能只按 RabbitMQ 队列深度盲目调大。

### 重试和死信拓扑

每个工作队列单独配置三级延迟队列，不依赖 RabbitMQ Delayed Message 插件：

```text
mesguard.diagnosis.execute.v1
    └─ transient failure
       └─ diagnosis.retry.30s (TTL 30s)
          └─ after TTL → mesguard.diagnosis.execute.v1
             └─ second failure → diagnosis.retry.2m (TTL 2m)
                └─ after TTL → mesguard.diagnosis.execute.v1
                   └─ third failure → diagnosis.retry.10m (TTL 10m)
                      └─ after TTL → mesguard.diagnosis.execute.v1
                         └─ next failure → diagnosis.dead.v1
```

Worker 在确认原消息前，先把同一个信封发布到对应的 TTL 重试队列并等待 Publisher Confirm；确认成功后再 ACK 原消息。TTL 到期后由死信交换机把消息重新路由到主交换机。重试队列只承载等待时间，不作为业务事实存储。

死信配置优先使用 RabbitMQ policy 管理，避免把无法动态调整的 `x-dead-letter-exchange` 参数散落在应用声明代码中。Compose 初始配置可以由声明脚本创建，生产变更必须能审查和重复执行。

### 队列类型

当前 Windows Server + 单 Linux VM 的 Compose 目标不宣称 RabbitMQ 高可用。M1 可以使用持久化 classic queue 降低单节点资源复杂度；如果后续部署多节点 RabbitMQ 集群，再评估 quorum queue，并通过压测验证长任务、重试和死信行为。

无论 classic 还是 quorum queue，都不能替代 PostgreSQL Outbox 和任务租约。单节点 RabbitMQ 的磁盘持久化只能降低重启丢失风险，不能解决宿主机或虚拟机故障。

## 消息信封

OutboxEvent 发布为统一的 JSON 消息信封。M1 示例：

```json
{
  "message_id": "01900000-0000-7000-8000-000000000001",
  "event_type": "diagnosis.execute",
  "schema_version": 1,
  "aggregate_type": "diagnosis_task",
  "aggregate_id": "01900000-0000-7000-8000-000000000002",
  "occurred_at": "2026-07-25T12:00:00Z",
  "correlation_id": "01900000-0000-7000-8000-000000000003",
  "causation_id": null,
  "payload": {
    "task_id": "01900000-0000-7000-8000-000000000002"
  }
}
```

规则如下：

- `message_id` 使用 OutboxEvent 的 UUIDv7；同一 Outbox 重发时保持不变。
- `event_type` 和 `schema_version` 决定路由和解析契约。
- `aggregate_id` 用于定位 DiagnosisTask 或未来的 IngestionTask。
- `correlation_id` 串联一次用户请求、任务和后续日志；`causation_id` 指向产生本消息的上游事件。
- `payload` 只放 `task_id`、必要版本和少量执行选项，不放工单全文、图片、Base64、SQL 结果、Prompt 或密钥。
- 不兼容的 schema version 不能猜测解析，直接转入最终死信并报警。
- 手动重投仍复用原 `message_id`；重新发起诊断才创建新 DiagnosisTask、新 OutboxEvent 和新消息。

RabbitMQ properties 至少设置：`delivery_mode=persistent`、明确的 `content_type`、`message_id`、`correlation_id` 和 `type`。这些属性用于排障，不取代 JSON 信封中的业务字段。

## Outbox 写入与 Relay

### 业务事务

创建诊断任务时，以下记录必须在同一个 PostgreSQL 事务中提交：

```text
CaseSnapshot
DiagnosisTask(status=pending)
TaskEvent(task_created)
OutboxEvent(event_type=diagnosis.execute, published_at=NULL)
```

事务中禁止调用 SQL Server、模型、MinIO、RabbitMQ 或其他网络服务。事务提交成功后 API 才返回 `task_id`；提交失败时不能出现可被用户继续访问的半成品任务。

### Relay 领取

Relay 使用短事务和 `FOR UPDATE SKIP LOCKED` 领取待发布记录：

```sql
SELECT id
FROM outbox_events
WHERE published_at IS NULL
  AND available_at <= now()
  AND (locked_until IS NULL OR locked_until < now())
ORDER BY created_at, id
FOR UPDATE SKIP LOCKED
LIMIT $1;
```

领取后写入 `locked_by`、`locked_at`、`locked_until`，提交事务，再在事务外发布 RabbitMQ。租约默认 5 分钟，必须大于一次发布和确认的正常耗时，但不能无限期阻塞恢复。

Relay 只能在收到 Publisher Confirm 后把 `published_at` 写入 PostgreSQL。发布成功但进程在标记前崩溃时，Outbox 会再次发布同一个 `message_id`；这是允许的重复投递，消费者必须幂等。

Relay 发布失败时：

1. 保留 `published_at=NULL`；
2. 增加 Outbox `attempt_count`；
3. 写入脱敏的 `last_error`；
4. 计算下一次 `available_at`；
5. 租约到期后允许其他 Relay 领取。

连续失败达到运维阈值时，Relay 不删除 Outbox，而是将事件标记为需要人工关注并暴露积压指标。RabbitMQ 恢复后可以继续补发。

### Publisher Confirm

Relay 必须启用 Publisher Confirm，并对每条发布等待 ACK/NACK 或有界超时：

- Confirm ACK：允许标记 `published_at`；
- Confirm NACK：保留未发布状态，按 Outbox 退避重试；
- 通道断开或超时：不能假设未发布，保留未发布状态并允许重复发布；
- 发布确认不等于消费者已经执行成功，只表示 RabbitMQ 接受了消息。

## Worker 消费流程

Worker 使用手动 ACK。收到消息后的顺序如下：

```text
basic.deliver
    ↓
解析信封和 schema
    ↓
读取 PostgreSQL 当前任务状态
    ↓
短事务原子领取任务 / 判断幂等
    ↓
执行步骤（事务外）并定期续租
    ↓
短事务写步骤、证据、TaskEvent 和任务状态
    ↓
达到持久化终态，或已确认转发到重试队列后 basic.ack
```

### 领取条件

只有以下任务才允许领取：

- `pending`；
- `running` 且 `lease_until` 已过期；
- 允许恢复的临时失败状态。

领取事务必须同时：

1. 检查任务没有被取消或进入终态；
2. 增加 `attempt_count`；
3. 写入随机 `claim_owner` 和新的 `lease_until`；
4. 产生本次执行的 fencing token；
5. 写入 `task_started` 或恢复事件。

如果任务已经是 `succeeded`、`failed` 或 `cancelled`，Worker 直接 ACK，不重复产生报告。如果任务由其他 Worker 持有且租约仍有效，本次投递不能并发执行；Worker 应进入有界延迟重试，不能立即循环 `nack(requeue=true)` 消耗 CPU。

### Fencing token

租约过期后，旧 Worker 可能因为网络分区仍在运行。因此步骤、证据、报告和任务状态写入必须带当前 `claim_owner` 与 `attempt_count` 条件：

```sql
UPDATE diagnosis_tasks
SET status = $status,
    updated_at = now()
WHERE id = $task_id
  AND claim_owner = $claim_owner
  AND attempt_count = $attempt_count
  AND lease_until > now();
```

更新行数为 0 时，旧 Worker 必须停止后续业务写入并释放外部调用。fencing token 防止旧租约持有者覆盖新 Worker 的结果。

### 续租和崩溃恢复

Worker 在长步骤中按固定间隔续租。续租失败不能被忽略；后续步骤开始前必须再次确认自己仍持有 fencing token。

Worker 崩溃或连接断开时，RabbitMQ 会重新投递未 ACK 消息。重新投递后：

- 租约仍有效：不执行第二份诊断，进入有界延迟等待；
- 租约已过期：新 Worker 原子接管并增加 `attempt_count`；
- 任务已终态：直接 ACK；
- 任务已取消：记录取消结果并 ACK。

不从半截模型流或半次工具调用中恢复。恢复从当前未完成的 DiagnosisStep 边界开始；已成功步骤只有在输入、Prompt、模型、工具和 schema 版本一致且被声明为可复用时才跳过。

### ACK、NACK 与断线

- 业务终态已经在 PostgreSQL 提交后，或重试消息已经收到 Publisher Confirm 后，才 `basic.ack` 原消息。
- ACK 丢失会造成重复投递，但不会造成重复业务结果；下一次消费通过状态和幂等键快速结束。
- 参数非法、权限拒绝、schema 不支持等永久错误不重试，进入最终死信并把任务标记为 `failed`。
- 网络超时、模型暂时不可用、RabbitMQ 连接中断等临时错误进入分级重试。
- 不直接使用无限 `basic.nack(requeue=true)`，避免故障时形成热循环。

## 重试分类

| 错误类型 | 是否重试 | 处理方式 |
| --- | --- | --- |
| 参数或 schema 不合法 | 否 | 记录失败，最终死信 |
| 权限拒绝、SQL 安全策略拒绝 | 否 | 记录失败，提示人工修正 |
| 任务已取消或已进入终态 | 否 | ACK，保持当前事实 |
| SQL Server/模型/MinIO 暂时超时 | 是 | 30s、2m、10m 分级重试 |
| Worker 进程崩溃或连接断开 | 是 | RabbitMQ 重投，等待租约恢复 |
| 结果为空 | 否 | 作为业务结果或证据不足，不当作系统故障 |
| 第三方持续不可用 | 有限 | 用尽重试后失败并进入死信 |

单条消息最多三次自动重试。重试次数写入消息头和 Worker 运行记录，但业务事实以 PostgreSQL 的 `attempt_count`、步骤尝试记录和错误字段为准，不能只信任客户端可篡改的 header。

## 死信与人工恢复

最终死信必须保留原始消息、死信原因、最后错误、队列、路由键、首次时间和最近尝试时间。死信不是删除区，也不是自动忽略区。

当消息耗尽自动重试：

1. Worker 将任务标记为 `failed`，保存稳定错误码和脱敏错误信息；
2. RabbitMQ 将消息路由到对应最终死信队列；
3. admin 在管理界面看到任务、Outbox、消息和错误关联；
4. admin 可以执行“恢复任务”，在 PostgreSQL 事务中将可恢复的 `failed` 任务重新置为 `pending`，追加 `task_requeued` TaskEvent，并重新打开原 Outbox 记录；
5. Relay 重新发布同一个 `message_id`。死信队列中的原消息保留作为审计副本，不直接与主队列消息同时消费；
6. 恢复前必须检查任务没有正式报告、没有被取消、没有新的运行者，并记录操作者和原因；
7. 恢复增加执行尝试和步骤尝试，但不新建任务、不覆盖已有步骤、证据或报告。

`failed -> pending` 只允许 admin 的受审计恢复用例发生。普通用户点击“重新诊断”仍然创建新的 DiagnosisTask，并通过 `retry_of` 关联历史任务。

如果业务人员希望基于新工单快照再次诊断，必须走“重新诊断”用例，创建新的 DiagnosisTask 并通过 `retry_of` 关联历史任务。

## 幂等边界

### API 幂等

创建诊断任务使用用户或客户端作用域内的 `idempotency_key` 唯一约束。重复请求返回原任务，不重复创建快照、TaskEvent 或 Outbox。

### Relay 幂等

同一 OutboxEvent 只使用同一 `message_id` 发布。正常发布确认写入是条件更新；admin 恢复时允许在事务中清空该 Outbox 的 `published_at` 并增加 `requeue_count`，这不是静默覆盖，而是必须伴随 `task_requeued` 事件和管理员审计记录。

### Worker 幂等

Worker 必须同时依赖：

- DiagnosisTask 状态条件；
- `claim_owner`、`attempt_count` 和 `lease_until`；
- DiagnosisStep 的步骤类型和尝试号；
- ToolExecution 的业务幂等键；
- EvidenceItem、DiagnosisReport 的唯一约束。

幂等不是“收到相同消息就不做任何事”。如果上一次执行在外部只读查询完成后崩溃，下一次可以重新查询并产生新的 ToolExecution；但不能生成第二个正式报告或覆盖原有证据。外部调用必须是只读或具备明确的幂等语义，消息系统不能为非幂等写操作提供安全保证。

## 取消、超时与队列积压

取消不是删除 RabbitMQ 消息。API 在 PostgreSQL 中写入 `cancel_requested` 和 TaskEvent；Worker 在步骤边界、工具返回和续租时检查取消标记，然后把任务置为 `cancelled` 并 ACK 当前消息。

任务超时分为：

- 单次工具超时：结束当前 ToolExecution，按错误分类决定是否重试；
- 单步骤超时：结束当前步骤，按任务策略失败或继续；
- 整体任务超时：请求协作取消，保存已有记录并进入 `failed` 或 `cancelled`。

队列和 Outbox 至少暴露以下指标：

- 主队列深度、最老消息年龄；
- 每类重试队列深度；
- 最终死信数量和最近错误；
- Outbox 未发布数量、最老记录年龄和租约过期数量；
- 发布 ACK/NACK、消费 ACK、重投和重复领取次数；
- 当前运行任务、租约续期失败和 fencing token 拒绝次数。

当 Outbox 积压或诊断队列超过配置阈值时，API 可以拒绝新的诊断创建并返回明确的系统繁忙原因，但不能删除已创建的任务。Redis 只用于实时通知，不能用来判断消息是否已经执行。

## 运维恢复流程

### RabbitMQ 短时不可用

API 仍可在 PostgreSQL 中创建任务和 Outbox。Relay 持续退避；SSE 和任务查询继续显示 `pending` 与积压状态。超过阈值后停止接受新任务，避免无限积压。

### Relay 崩溃

其他 Relay 在 `locked_until` 过期后领取同一 Outbox。可能重复发布同一 `message_id`，由 Worker 幂等处理。

### Worker 崩溃

未 ACK 消息由 RabbitMQ 重投；任务租约过期后由新 Worker 接管。旧 Worker 即使恢复，也会因为 fencing token 不匹配而无法提交结果。

### RabbitMQ 数据丢失

已提交但未标记 `published_at` 的 Outbox 会重新发布。已经标记为 published 但 RabbitMQ 实际丢失的极端窗口，需要依靠投递审计、任务状态扫描和管理员补偿检查；因此 Relay 标记 published 不等于任务已成功。对仍处于 `pending` 且长期没有 `task_started` 的任务，应由恢复巡检创建补偿 Outbox 或提示 admin 重投。

### PostgreSQL 不可用

Relay 和 Worker 不领取新任务。RabbitMQ 中未 ACK 的消息会等待或重投，但没有 PostgreSQL 就不能安全判断幂等和租约，Worker 应暂停业务执行而不是盲目消费。

## 安全边界

- RabbitMQ 用户按运行角色授予最小权限；API 不获得消费权限，Worker 不获得管理权限。
- 消息中禁止出现数据库密码、模型密钥、MinIO 凭证、完整工单正文和原始附件。
- 日志记录 `message_id`、`task_id`、`correlation_id` 和错误码，不记录完整消息 payload 或敏感参数。
- 管理员重投必须记录操作者、原因、时间和目标消息。
- RabbitMQ 管理端口不暴露给普通用户或浏览器；生产只通过受控管理网络访问。

## 实现顺序

1. 用 PostgreSQL 迁移建立 Outbox 状态、索引和租约字段。
2. 实现 `OutboxRepository` 的短事务领取、租约续期、成功确认和失败退避。
3. 实现 RabbitMQ 连接、交换机、队列、policy 和健康检查声明。
4. 用 Publisher Confirm 验证 Relay 重启、超时、NACK 和重复发布。
5. 先用假的 DiagnosisHandler 验证手动 ACK、重复投递、租约过期和 fencing token。
6. 实现三级 TTL 重试和最终死信记录。
7. 再接入真实诊断步骤、TaskEvent、取消和报告写入。
8. M2 复用同一套信封、Relay 和重试规范，增加独立入库队列。

## 验收场景

至少自动化验证以下场景：

1. 事务提交前进程退出：没有任务或 Outbox 半记录。
2. 事务提交后、Relay 发布前进程退出：Relay 可以补发。
3. 发布成功、标记 published 前进程退出：重复消息不会重复报告。
4. Publisher NACK 或超时：Outbox 保留并按退避重新发布。
5. Worker 领取后崩溃：消息重投，租约到期后新 Worker 接管。
6. 旧 Worker 在租约过期后恢复：fencing token 阻止旧结果写入。
7. 两个 Worker 并发领取：只有一个成功，另一个不会执行诊断。
8. 任务已 succeeded/cancelled 时收到重复消息：直接 ACK，不产生新结果；普通消费不能自动恢复 failed 任务。
9. 临时错误三次后进入死信：任务状态为 failed，admin 可以重投原消息。
10. 永久错误：不进入无限重试，不污染重试队列。
11. RabbitMQ 不可用：任务和 Outbox 仍可查询，超过积压阈值后新建任务被明确拒绝。
12. Redis 不可用：任务事实和 SSE 补读仍依赖 PostgreSQL，不丢失事件。

## 参考资料

- [RabbitMQ Publisher Confirms](https://www.rabbitmq.com/docs/publishers)
- [RabbitMQ Consumer Acknowledgements and Publisher Confirms](https://www.rabbitmq.com/docs/confirms)
- [RabbitMQ Dead Letter Exchanges](https://www.rabbitmq.com/docs/dlx)
- [RabbitMQ Time-To-Live and Expiration](https://www.rabbitmq.com/docs/ttl)
- [RabbitMQ Quorum Queues](https://www.rabbitmq.com/docs/quorum-queues)
- [PostgreSQL `SELECT ... FOR UPDATE SKIP LOCKED`](https://www.postgresql.org/docs/current/sql-select.html)

## 后续工作

M0认证、Session、Repository和事务基础已完成；RabbitMQ与Worker仍在M1-B/M1-C按本文约束实现。
