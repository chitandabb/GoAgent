# Agent 可观测链路

## 当前合同

MESGuard 使用 OpenTelemetry 作为可移植的链路合同，Zap 继续承担业务日志职责。一次运行由
`agent.*` 根 Span 串起 `model.*`、`tool.*` 与 `retrieval.*` 子 Span，降级通过
`mesguard.degradation` Event 记录。运行期使用 `traceId`、`runId`、`conversationId` 和可选
`taskId` 关联业务日志、任务事实与链路；Eino `ToolInput.CallID` 在适用的 Tool Span 中记录为 `toolCallId`。

默认 Span 只记录稳定名称、Provider/Model、Token Usage、结果状态、耗时、检索结果数量、数据通道和
降级摘要。Prompt、Answer、工具参数、检索原文与 Evidence 原文均不进入遥测。原文采样仍保留为未来
开发环境增强，不属于当前实现，也不会因为启用 Exporter 而自动打开。

## 失败边界

- `[observability].enabled = false` 时不解析凭证、不创建 Exporter，也不产生网络请求。
- Exporter 启动失败时记录 Zap Warning，Agent 继续以 No-op Provider 工作。
- Batch Exporter 异步发送；接收端超时或拒绝数据不会改变回答、任务或报告状态。同类 SDK 导出错误
  最多每 `errorLogIntervalMillis` 写一条脱敏 Zap Warning，避免接收端长期故障刷屏。
- 关闭进程时尽力 Flush；关闭失败只写 Zap Warning，不进入业务关闭错误链，也不改变已提交业务事实。
- Langfuse 只是本地开发/评测后端，不是 MESGuard 的事实源或健康依赖。

## 配置与本地 Langfuse

TOML 使用 `[observability]`：

```toml
[observability]
enabled = true
serviceName = "mesguard-api"
environment = "development"
otlpEndpoint = "http://localhost:3000/api/public/otel/v1/traces"
headersEnv = "MESGUARD_OTEL_HEADERS_JSON"
sampleRatio = 1.0
exportTimeoutMillis = 3000
errorLogIntervalMillis = 60000
```

`MESGUARD_OTEL_HEADERS_JSON` 必须由环境变量提供，示例结构如下；不要把真实密钥写入 TOML：

```json
{"Authorization":"Basic <base64(public-key:secret-key)>","x-langfuse-ingestion-version":"4"}
```

可选本地部署位于 `deploy/observability/docker-compose.langfuse.yml`。该文件沿用 Langfuse v4 官方
组件边界（Web、Worker、PostgreSQL、Redis、ClickHouse、MinIO），所有服务都属于
`observability` Profile，并使用独立服务名和数据卷，不复用 MESGuard 的业务 PostgreSQL、Redis 或
MinIO。启动前必须显式提供文件中标记为 `required` 的开发密钥：

```powershell
docker compose -f docker-compose.yml -f deploy/observability/docker-compose.langfuse.yml `
  --profile observability config --quiet
docker compose -f docker-compose.yml -f deploy/observability/docker-compose.langfuse.yml `
  --profile observability up -d
```

组合两个 Compose 文件会让 MESGuard 容器与 `langfuse-web` 进入同一个默认网络，Docker TOML 中的
`http://langfuse-web:3000/...` 才能解析；如果 MESGuard 在宿主机直接运行，则只启动 Langfuse 文件并
使用 `http://localhost:3000/...`。

Langfuse 官方对当前 Docker Compose 单机部署建议至少 4 核、16 GiB 内存，因此它不随默认开发栈
启动；资源不足时可以把同一个 OTLP 配置指向独立测试机或 Langfuse Cloud。

## 验证

自动测试使用 OpenTelemetry In-memory Exporter 验证父子 Trace、运行身份、模型 Usage、降级事件和
默认无原文合同，不依赖 Docker 或 Langfuse。真实接收端 Smoke 需要本机可用的 Langfuse 凭证与足够
资源，属于显式启用测试，不能成为普通 `go test ./...` 的前置条件。
