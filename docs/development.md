# 开发指南

本文只保留能让新开发者跑通 MESGuard 的必要步骤。详细设计见 [文档索引](README.md)。

## 环境要求

- Go 1.25.3
- Node.js 22+
- Docker Desktop / Docker Compose
- Windows、macOS 或 Linux 均可；Windows 示例使用 PowerShell

## 配置原则

1. 从仓库根目录的 `.env.compose.example` 复制出本地 `.env`。
2. `.env`、Provider Key、数据库密码和本地挂载目录不提交 Git。
3. `config/mesguard.toml` 用于本地进程，`config/mesguard.docker.toml` 用于 Compose 容器。
4. API 启动时检查迁移版本，但不会自动执行迁移。
5. 模型、GitHub MCP、Firecrawl、Embedding 和 Rerank Provider 均可在没有密钥时以降级能力启动。

```powershell
Copy-Item .env.compose.example .env
```

## 运行本地联调 Demo

### 启动基础依赖

```powershell
docker compose up -d postgres sqlserver sqlserver-seed redis rabbitmq minio
docker compose ps
```

### 执行迁移并启动 API

```powershell
go run ./cmd/mesguard-migrate status
go run ./cmd/mesguard-migrate up
go run ./cmd/mesguard-migrate check
go run ./cmd/mesguard-api
```

健康检查：

```powershell
Invoke-RestMethod http://127.0.0.1:9090/healthz
```

### 启动前端

在另一个终端：

```powershell
Set-Location web
npm install
npm run dev
```

访问 `http://127.0.0.1:5173`。Vite 会把 `/api` 和 `/healthz` 代理到本地 API。

### 启动完整 Worker 链路

本地 API 和 Compose `backend` 只能选择一种运行方式。完整 Docker 模式：

```powershell
docker compose up -d --build migrate backend outbox-relay diagnosis-worker conversation-worker knowledge-worker memory-worker
docker compose ps backend outbox-relay diagnosis-worker conversation-worker knowledge-worker memory-worker
```

完整链路需要 RabbitMQ、SQL Server、MinIO、PostgreSQL 和相应迁移服务；模型 Key 为空时，认证、工单浏览和部分只读能力仍可用，但 Agent 调用会降级或失败。

## 创建本地账号

密码只从环境变量读取：

```powershell
$env:MESGUARD_INITIAL_USER_PASSWORD = "change-this-locally"
go run ./cmd/mesguard-user -username demo-admin -display-name "Demo Admin" -role admin
```

账号首次登录需要修改密码。不要把真实密码写进 README、脚本、测试数据或 Docker Compose 文件。

## 测试与构建

```powershell
# Go 全量测试
go test ./...

# 前端类型检查和生产构建
Set-Location web
npm run build
```

需要真实基础设施的集成测试使用明确的 `MESGUARD_TEST_*` 环境变量：

```powershell
go test -tags=integration ./internal/platform/postgres ./internal/platform/rabbitmq ./internal/platform/minio -count=1
```

Provider Smoke 必须显式设置 API Key、调用上限和输出路径；没有授权时不要运行付费评测。

## 常用目录

- `cmd/`：可执行程序入口。
- `internal/`：业务、Agent、HTTP、存储和基础设施实现。
- `config/prompts/`、`config/skills/`：运行时 Prompt 和 Skill。
- `db/migrations/`：Goose 迁移，执行顺序不可手工跳过。
- `testdata/`：合成固定集和评测输入，不放真实业务数据。
- `output/`、`logs/`：本地产物，已被 Git 忽略。

## 排查顺序

1. `GET /healthz`：确认 API 与 PostgreSQL 迁移状态。
2. `docker compose ps`：确认依赖和 Worker 健康状态。
3. 查看 API/Worker 日志中的 request ID、task ID 或 turn ID。
4. 检查 PostgreSQL Outbox、RabbitMQ 队列和 MinIO 对象状态。
5. 先复现 Provider-free 单测，再进行受限真实 Provider Smoke。

## 安全边界

- SQL Server 只使用专用只读账号和白名单 Catalog。
- API Key 只从环境变量或本地 Secret Store 读取。
- 上传文件经过大小、格式和 SHA-256 校验，临时文件在请求结束后清理。
- Demo 数据是合成数据；不要连接真实客户、ERP 或生产数据库。
