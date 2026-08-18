# MESGuard

MESGuard 是一个面向工业软件场景的证据驱动诊断 Agent：它把 ERP/MES 工单、只读业务数据、企业知识文档和会话上下文组织成可追溯的诊断流程。

项目采用 Go 后端和 React 工作台，重点验证 Agent 权限治理、异步任务、Text-to-SQL 安全边界、Agentic RAG、引用预览和前后端真实联调。当前仓库用于工程展示与本地 Demo，不提供发行包、SaaS 服务或生产 SLA。

> 项目是在公开的 [GopherAI](https://github.com/youngyangyang04/GopherAI) Go AI 服务基础上持续演进的 MESGuard 实现。当前代码、架构和业务能力已远超早期基线；原仓库的 GPLv3 授权文件仍保留在本项目中。

## 当前能力

- **统一 Agent Runtime**：Conversation 与 Diagnosis 使用固定 Tool Profile，执行期通过 `RunAccess`、`ResourceGrant` 和诊断任务冻结的 `InvestigationPolicy` 做授权。
- **可审计诊断任务**：工单快照、附件快照、Outbox、RabbitMQ Worker、租约 fencing、重试、取消、报告和复核记录均持久化。
- **安全 Text-to-SQL**：Schema Catalog、SQL 静态检查、只读账号、超时与行数限制共同约束数据库诊断。
- **文档 RAG**：支持文档版本、解析任务、文本/Office/PDF 处理、Embedding、FTS/Vector/RRF 召回、引用绑定和预览。
- **会话助手**：会话、消息、附件、回合状态和 SSE 事件流由后端持久化，前端支持流式回答、附件和引用预览。
- **工程化基础设施**：PostgreSQL/pgvector、SQL Server、Redis、RabbitMQ、MinIO、Docker Compose 和结构化日志。

## 架构概览

```text
React Workbench
      │ HTTP / SSE
      ▼
Gin API ── PostgreSQL / pgvector
  │             │
  ├─ Outbox ── RabbitMQ ── Diagnosis Worker
  │                         Conversation Worker
  │                         Knowledge Worker
  │
  ├─ SQL Server（只读 ERP/MES 测试数据）
  ├─ Redis（缓存、锁和短期状态）
  └─ MinIO（附件与知识源对象）
```

详细边界见：

- [系统架构](docs/design/system-architecture.md)
- [Agent 编排与工具治理](docs/design/agent-orchestration.md)
- [API 与 SSE 契约](docs/design/api.md)
- [数据库设计](docs/design/database.md)
- [知识入库与检索](docs/design/rag-ingestion-and-retrieval.md)

## 技术栈

- Go 1.25.3、Gin、GORM、Eino
- PostgreSQL 16、pgvector、SQL Server 2022、Redis 7
- RabbitMQ、MinIO、Docker Compose
- React、TypeScript、Vite、TanStack Query
- ONNX Runtime、OpenTelemetry、可选的模型/Embedding/Rerank Provider

## 本地运行

### 环境要求

- Go 1.25.3
- Node.js 22+
- Docker Desktop / Docker Compose
- 至少 PostgreSQL、SQL Server、Redis、RabbitMQ、MinIO 可用

### 1. 准备环境变量

```powershell
Copy-Item .env.compose.example .env
```

`.env` 只用于本地配置。模型、GitHub MCP、Firecrawl 和 DashScope 密钥可以留空；不要把真实密钥写入 Git。

### 2. 启动基础依赖

```powershell
docker compose up -d postgres sqlserver sqlserver-seed redis rabbitmq minio
go run ./cmd/mesguard-migrate up
go run ./cmd/mesguard-api
```

API 默认地址：`http://127.0.0.1:9090`。

### 3. 启动前端

```powershell
Set-Location web
npm install
npm run dev
```

前端默认地址：`http://127.0.0.1:5173`。

### 4. 启动完整异步链路

如果需要诊断、会话和知识入库 Worker，使用 Docker 模式启动后端和 Worker。不要同时运行本地 API 与 Compose 的 `backend`，否则会发生端口冲突。

```powershell
docker compose up -d --build migrate backend outbox-relay diagnosis-worker conversation-worker knowledge-worker memory-worker
```

### 5. 创建本地账号

密码只通过环境变量传入，不写入命令参数或仓库：

```powershell
$env:MESGUARD_INITIAL_USER_PASSWORD = "change-this-locally"
go run ./cmd/mesguard-user -username demo-admin -display-name "Demo Admin" -role admin
```

## 验证

```powershell
go test ./...

Set-Location web
npm run build
```

真实外部依赖测试需要额外的 `MESGUARD_TEST_*` 环境变量，详见 [开发指南](docs/development.md)。

## 仓库结构

| 路径 | 内容 |
| --- | --- |
| `cmd/` | API、迁移、用户管理和 Worker 入口 |
| `internal/` | 领域逻辑、Agent、存储、HTTP 和基础设施实现 |
| `web/` | React 工作台 |
| `config/` | TOML 配置、Prompt 和 Skill |
| `db/migrations/` | Goose 数据库迁移 |
| `api/openapi.yaml` | 已实现 HTTP/SSE 接口契约 |
| `tools/` | 评测、观测、导出和 Smoke 工具 |
| `testdata/` | 合成固定集和可审计评测输入 |
| `docs/` | 设计、开发、决策和评测文档 |

## 文档与边界

从 [docs/README.md](docs/README.md) 开始阅读。评测记录使用固定本地数据集或受控 Smoke，不能直接解释为生产 SLA；仓库中的 ERP、账号和文档数据均用于本地演示，不应连接真实生产系统。

当前已知边界和后续切片见 [docs/roadmap.md](docs/roadmap.md)。

## 许可证

仓库保留原始 GopherAI 基线中的 GPLv3 许可证，见 [LICENSE](LICENSE)。本项目当前不额外声明新的开源授权；如需对外分发或改变许可证，应先确认原始代码、实习项目代码和测试数据的版权归属。
