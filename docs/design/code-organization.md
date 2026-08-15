# MESGuard 代码组织与依赖边界

## 文档状态

- 生效日期：2026-08-10。
- 本文是目录职责和包依赖方向的事实来源；功能进度仍记录在 `docs/roadmap.md`。
- 本轮只区分生产入口与研发工具，不改变 API、数据库、配置、业务逻辑或二进制名称。

## 顶层目录

| 目录 | 职责 |
| --- | --- |
| `cmd/` | 需要部署或正式运维的可执行入口 |
| `tools/evaluation/` | 离线指标汇总、固定集评测和 Judge |
| `tools/observation/` | 连接真实数据库、对象存储或 Provider 的受控观测命令 |
| `tools/export/` | 把持久化事实导出为离线评测数据 |
| `tools/smoke/` | 小规模手工 Smoke，不作为生产服务 |
| `internal/` | 不对模块外暴露的领域、应用、适配器和装配实现 |
| `api/` | 已实现 HTTP/SSE 的唯一机器可读 OpenAPI 契约 |
| `scripts/` | 可复现的环境、语料和评测编排脚本 |
| `testdata/` | 可提交的小型固定集、金标和离线 Observation |
| `output/` | 被 Git 忽略的本地运行产物和 Provider 原始输出 |
| `web/` | 独立前端工程 |

## 可执行入口

`cmd/` 当前只保留以下正式入口：

- `mesguard-api`；
- `mesguard-conversation-worker`；
- `mesguard-diagnosis-worker`；
- `mesguard-knowledge-worker`；
- `mesguard-memory-worker`；
- `mesguard-outbox-relay`；
- `mesguard-migrate`；
- `mesguard-user`。

生产入口应保持为薄组合层，只负责配置、日志、生命周期和调用 `internal/bootstrap`。研发命令即使仍是
`package main`，也必须放到对应的 `tools/` 分类下；命令是否会连接 Provider、产生费用或修改临时事实，
由其文档和显式执行开关说明。

## 依赖方向

允许的主要方向：

```text
cmd / tools
    -> bootstrap / evaluation
        -> transport + domain/use case + platform adapter
            -> consumer-owned interfaces
```

必须保持的约束：

1. `conversation`、`diagnosis`、`knowledge`、`attachment`、`externalcase` 等领域/用例包不得反向依赖
   `platform`、`transport`、`bootstrap` 或 `tools`。
2. HTTP Handler 只做绑定、认证、权限上下文和响应映射，不直接操作 GORM、MinIO 或模型客户端。
3. PostgreSQL、SQL Server、MinIO、RabbitMQ 和模型 Provider 作为适配器实现消费方定义的接口。
4. `bootstrap` 是显式组合根，允许知道具体实现，但不承载业务规则。
5. `tools` 可以复用生产 Parser、Retriever、Repository 和 Agent 合同；生产运行时不得依赖评测命令。
6. 新 Tool 通过显式模块构造返回注册项，不使用全局 `init()` 或隐藏 blank import 注册。

## Provider 切换边界

- Chat Model 已通过命名 Profile 和 Provider Adapter 在已编译实现之间配置切换。
- Web Search、Object Store、Repository、Embedding、Rerank、Vision 和 Table 的上层均依赖接口。
- 新增全新 Provider 可以修改组合根和对应能力 Factory，但不能要求领域服务理解供应商参数。
- 不创建一个覆盖 Chat、Embedding、Rerank、OCR、VLM 的万能 Provider 接口；不同能力保留独立合同。

### OpenCode Go 方言 Adapter（第一阶段）

- `provider = "opencode-go"` 是独立方言 Adapter（`opencodeGoAdapter`），复用
  共享的 Eino OpenAI Chat Completions transport（`openai.NewChatModel`），不
  升级或重写 Eino SDK，不引入全局 Registry，不使用 `init()` 自注册，不新增
  万能 ModelProvider 大接口。
- `providerAdapter` 不是一整套模型客户端：它只负责校验 Provider 专有参数、
  声明 Provider 能力、把专有参数映射到共享 `openai.ChatModelConfig`。opencode-go
  与 deepSeekAdapter 是两个不同方言：deepseek 表达 DeepSeek 官方接口并注入
  `thinking`/`reasoning_effort`；opencode-go 是另一个网关，不注入任何
  thinking 参数。Provider 是唯一路由键，模型名含 "deepseek" 不会路由到
  deepSeekAdapter。
- opencode-go 的 `reasoningEffort`/`thinkingMode`/`responseFormat` 能力约束在
  Adapter 创建阶段验证，config 包不复制 Provider 专有参数校验；配置包仍
  fail-closed 拒绝未知 Provider 名称。
- 密钥按 Profile 选择性读取：`NewActive` 只读 activeProfile 的 `apiKeyEnv`，
  `NewProfile` 只读被指定 Profile 的 `apiKeyEnv`，配置加载阶段不批量读取所有
  Provider Key。缺 Key 时按 Profile fail-closed，不影响其他 Profile。
- OpenCode Go 的基础流式 Tool Calling 已于 2026-08-15 通过一次受控真实 Smoke；
  该结果只证明 Tool Call → Tool Result → 最终回答与 Usage 回传，不代表
  Conversation/Diagnosis 生产质量已验收。ReasoningEffort/ThinkingMode/
  ReasoningContentRequired/JSONOutput/JSONSchemaOutput 均为 false；JSON Object/
  JSON Schema 当前未声明。

## 当前已知结构债务

以下问题已确认，但不属于 2026-08-10 的目录迁移范围：

1. `internal/agent` 混合 Runtime、Tool 和 Evaluation；
2. `internal/platform/postgres` 同时实现多个领域 Repository；
3. `internal/bootstrap` 的组合函数随着功能增长而变长；
4. `internal/platform/config/config.go` 和部分 Repository 文件过大；
5. 若干 `tools` 命令仍在 `package main` 中承载较多评测实现。

在简历第四、第五点与前端完成后，联调前再按以下顺序处理：

1. 先把评测实现抽到 `internal/evaluation/<capability>`，保留薄 Tool main；
2. 在同一 package 内拆分大 Config 和 Repository 文件；
3. 再评估拆分 PostgreSQL 领域适配器和 Agent Runtime/Tools package；
4. 每个切片都必须保持 API、迁移、配置和固定集结果不变，并运行全仓及真实基础设施回归。

## 文档治理

- `docs/简历.md`：唯一简历事实源；
- `docs/roadmap.md`：当前状态和下一切片；
- `docs/design/*.md`：稳定设计和边界；
- `docs/evaluations/*.md`：可复现实验与失败样本；
- `docs/development.md`：命令和本地运维；
- `api/openapi.yaml`：唯一机器可读 HTTP/SSE 实现契约。

过期阶段计划不长期保留；已经完成或作废的实施步骤应沉淀为 Roadmap 结果、ADR、设计边界或评测记录，
避免同时维护“目标契约”和“已实现契约”。
