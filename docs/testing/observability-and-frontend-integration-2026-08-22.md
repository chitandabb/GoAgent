# OTel、ONNX Layout 与前后端联调测试记录（2026-08-22）

## 1. 测试范围

本次验证覆盖：

- 开启 OpenTelemetry OTLP 导出，并用 Langfuse 可视化查看真实业务 Trace。
- 开启 PP-DocLayout-M ONNX Layout 运行时，校验 Docker Knowledge Worker 能加载资源并启动。
- 启动 PostgreSQL、SQL Server、Redis、RabbitMQ、MinIO、Search、API、Outbox Relay、Diagnosis Worker、Conversation Worker、Knowledge Worker、Memory Worker、Vite 前端和 Langfuse 监控后端。
- 通过真实前端页面联调 Evidence Gate 诊断任务和语义答案缓存。
- 将真实 UI/Worker 结果与 provider-free 的 Evidence Gate Early Exit 确定性测试分开记录，避免把“链路跑通”误写成“Early Exit 质量收益已证明”。

测试工作区：`D:\develop\project\go-project\GoAgent`。本记录对应工作区当前未提交变更；未把本地 `.env` 中的 API Key、OTel Key 或密码写入文档。

## 2. 配置与资源

### 2.1 已修改的跟踪文件

| 文件 | 变更 |
| --- | --- |
| `config/mesguard.toml` | `observability.enabled=true`；`knowledge.layout.enabled=true`；本机模型和 ONNX Runtime 路径改为已校验的 `output/` 资源。 |
| `config/mesguard.docker.toml` | `observability.enabled=true`；Docker Layout 保持开启。OTLP 地址为 `http://langfuse-web:3000/api/public/otel/v1/traces`。 |
| `docker-compose.yml` | API、Diagnosis/Conversation/Knowledge/Memory Worker 注入 `MESGUARD_OTEL_HEADERS_JSON`。 |
| `deploy/observability/docker-compose.langfuse.yml` | 单节点 ClickHouse 增加 `CLICKHOUSE_CLUSTER_ENABLED=false`，避免 v4 migration 使用不存在的 Zookeeper 集群。 |

本地 Langfuse 密钥和 OTLP Basic Auth Header 仅放在被忽略的 `.env`，没有进入 Git。

### 2.2 ONNX 资源校验

| 资源 | 容器路径 | SHA-256 | 结果 |
| --- | --- | --- | --- |
| PP-DocLayout-M ONNX | `/app/models/pp-doclayout-m.onnx` | `b237c7e4aef235de8f45778ff2dd96dc21480cade40f01435f640b0ff68ee010` | 通过 |
| ONNX Runtime Linux | `/app/runtime/libonnxruntime.so` | `1461ef7cc3d9e49982591721683cc3e3a55580aeca9a5254e7aac47b75ee4bab` | 通过 |

`mesguard-knowledge-worker` 容器内再次执行 `sha256sum`，并输出 `ONNX_ASSETS_PRESENT`；Worker 正常启动。随后用工作区测试夹具 `testdata/mesguard-onnx-layout-integration.pdf` 完成浏览器上传型 Layout ingestion，详见 4.5。

## 3. 服务与入口

| 入口 | 地址 | 结果 |
| --- | --- | --- |
| MESGuard API | `http://127.0.0.1:9090/healthz` | HTTP 200 |
| MESGuard 前端 | `http://127.0.0.1:5173/` | Vite 已启动，登录和页面请求正常 |
| Langfuse Trace 页面 | `http://127.0.0.1:3000/project/mesguard-local/traces` | 已登录并可查看 Trace |

Langfuse v4.16.0 OSS 的 Postgres、Redis、ClickHouse、MinIO、Worker、Web 均为运行/健康状态。Langfuse Trace 页面刷新后显示 `89` 条 Observations，环境为 `development`，包括：

- `agent.conversation`：7
- `tool.search_knowledge`：20
- `retrieval.knowledge_search`：16
- `model.chat` / `model.chatmodel`：23 / 23
- 类型汇总：`GENERATION` 46、`SPAN` 43

这证明 MESGuard Worker 的 OTel span 已经通过 Langfuse OTLP 入口进入可视化后端。首次打开 Home 页面时数据尚未刷新，显示 0；进入 Trace 页面并刷新后得到上述数据。

## 4. 前后端联调结果

### 4.1 OTel：通过

操作路径：登录前端 → `助手` → 创建会话 → 发送知识库问题。

观察到：

1. 前端创建会话并调用 `POST /api/v1/conversations/:conversationId/turns`，API 返回 `202`。
2. Conversation Worker 消费 RabbitMQ 任务，成功提交 assistant message；失败重试也能通过事件流回放，不影响 OTel 导出。
3. Langfuse Trace 页面出现 `agent.conversation`、`tool.search_knowledge`、`retrieval.knowledge_search` 和 `model.chat` 观察项；页面可展开查看 service、environment、run id 和 token attributes。

结论：OTel 已形成“前端请求 → API → Worker → OTLP → Langfuse UI”的真实链路。

### 4.2 语义答案缓存：通过

为了控制检索结果大小，使用了同一知识文件的受控问题，并在 UI 中发起两类请求：

| 轮次 | UI 行为 | 服务端证据 |
| --- | --- | --- |
| 首轮 | 发送“请只从知识库检索 `layout-routing-public-v1.corpus.json`，回答其中有多少条目；只返回最多 3 个最相关片段，不要搜索网络。” | 正常 Agent/RAG；`conversation_turn_run_observations.execution_path=agent`，`model_calls=6`，`tool_calls=2`，`total_tokens=43154`；写入带 embedding 的缓存行。 |
| 对照轮 | 发送语义相近但差异较大的问题 | 正常 Agent/RAG；`execution_path=agent`，`model_calls=4`，`tool_calls=1`，`total_tokens=22170`；写入第二条缓存行。 |
| 命中轮 | 发送最小表面差异问题：“请只从知识库检索 `layout-routing-public-v1.corpus.json`，回答其中有多少条目；只返回最多 3 个最相关片段，不要搜索网络”（去掉末尾句号） | UI 返回相同回答和引用；`execution_path=semantic_cache_hit`，`cache_layer=semantic`，`source_run_id=01a0276c-3de9-7a01-8164-4594b34a4a41`，`model_calls=0`，`tool_calls=0`，`total_tokens=0`。 |

数据库验证：

- `semantic_answer_cache` 最终为 2 行，2 行均有 `question_embedding`。
- 命中轮没有新增缓存行，说明不是第三次正常生成后再写入，而是复用了已有语义缓存。
- 命中轮对应的 UI/Worker 任务为 `01a0276e-2ae0-7c12-abed-fc0ece3b953f`，运行观测的 `source_run_id` 指向首轮缓存来源。

结论：语义缓存已完成真实“前端输入 → API → Conversation Worker → PostgreSQL pgvector → 前端回答/引用”的命中联调。

补充失败证据：最开始使用宽泛的 MESGuard 知识问题时，Worker 曾因知识检索结果过大触发 `agent token budget exhausted`，随后受 RabbitMQ retry policy 重试。之后使用受控的最多 3 片段问题完成了缓存 miss/hit 验证。这个问题属于宽泛 RAG/Agent 预算边界，不影响已完成的缓存命中链路，但需要作为后续容量和 Prompt 控制项处理。

### 4.3 Evidence Gate 前后端联调：链路通过，在线 Early Exit 质量收益未宣称

操作路径：登录前端 → `工单` → `TKT-1002` → `进入诊断工作区` → 输入诊断问题 → `开始诊断`。

本次任务：

- `taskId=01a02772-2df9-7304-bd9d-f4d21ff4b6ea`
- `caseSnapshotId=01a02772-2df9-72ff-a419-7d3da0f133c8`
- `reportId=01a02772-a904-7d99-bbf5-a98c374bf1de`
- 创建时间约 `11:09:49`，第 1 次执行，最终任务状态 `succeeded`

前端实时事件依次显示：

1. `任务已创建`，状态 `pending`；
2. `开始执行`，状态 `running`；
3. `诊断成功`，`partial=true`、报告已落库；
4. 页面可展开“正式诊断报告”。

报告页面明确显示：

- 标题：`证据不足`
- `报告完整性：部分报告`
- `停止原因：evidence_gate_partial`
- `Agent 运行：2`
- Prompt / Completion：`76374 / 1840`
- 总 Token：`78214`
- 技术摘要：`Evidence Gate 未通过，当前内容仅作为待验证线索。`

数据库中的 `diagnosis_steps` 还原了同一条链路：第 1 轮 Agent 停止 → `read_external_case` → 两次 `search_knowledge` → Evidence Gate `needs_evidence` → 第 2 轮 Agent 停止 → 结构化报告门禁 `partial` → 部分报告落库。

这次在线结果证明了前端、API、SSE、Diagnosis Worker、Evidence Gate、报告持久化和前端展示均已联调。但它不是“Early Exit 通过并带来质量/Token 收益”的成功样本：本轮模型没有产出可校验结构化报告，门禁安全地降级为部分报告并停止。在线路径因此应记为“安全提前停止/部分报告通过，Early Exit 质量收益待补样本”。

### 4.3.1 GitHub MCP 固定路径成功取证：通过（受控范围）

为了补齐第一点的成功样本，在同一工作台对 `TKT-1002` 发起最小请求，只允许一次 `get_file_contents`，固定参数为 `owner=chitandabb`、`repo=GoAgent`、`path=internal/agent/evidence_orchestrator.go`、`ref=main`。

结果：

- `taskId=01a027b9-686c-76fc-8607-5cfea362838c`
- `caseSnapshotId=01a027b9-686c-76f9-9ca8-8373602b2bdf`
- `reportId=01a027b9-8e5d-73cf-b734-ed3dd6ab3eab`
- 前端实时事件为创建、执行、成功；任务 `succeeded`，`partial=false`。
- UI 正式报告为“结论明确”，包含 1 条 `get_file_contents` 证据（`evidence:e930a2d0-50d3-4222-8113-f50087c80e2e`），并展示 `checkEvidence`、`buildGraph`、`evidenceGapsNeedRetrieval`、`finishReport`、`finishPartialReport` 等函数摘要。

这证明“前端请求 → API → Diagnosis Worker → 只读 GitHub MCP → Evidence Gate → 报告持久化 → 前端报告”已闭环。它是受控固定路径成功样本，不代表任意仓库搜索、tree/commit 导航或复杂多工具调查都已完成；此前 `path=/` 校验失败与 MCP timeout 仍保留为失败/降级证据。

### 4.4 Deterministic Evidence Gate Early Exit：通过

执行：

```text
go test ./internal/agent -run 'Test(EvidenceOrchestrator|EvaluateEvidenceGateEarlyExit|BuildEvidenceGateEarlyExit)' -count=1 -v
```

结果：全部通过，包括：

- `TestEvaluateEvidenceGateEarlyExitAllowsBenefitsOnlyAfterQualityGate`
- `TestEvaluateEvidenceGateEarlyExitSuppressesBenefitsOnRegression`
- `TestEvaluateEvidenceGateEarlyExitRejectsNonIdenticalPairControls`
- `TestEvaluateEvidenceGateEarlyExitRequiresHumanReviewBeforeClaims`
- `TestEvidenceOrchestratorAcceptsValidReport`
- `TestEvidenceOrchestratorCanDisableEarlyExitForPairedEvaluation`
- `TestEvidenceOrchestratorReturnsPartialAtAgentRunLimit`
- `TestEvidenceOrchestratorStopsBeforeSecondRunAtTokenLimit`

这些是 provider-free 的确定性门禁测试，证明 Early Exit 的状态机、成对评测控制、质量门禁和 partial fail-closed 行为通过；它们不能替代真实模型质量评测，也不能证明本次 TKT-1002 在线运行获得了 Early Exit 性能收益。

### 4.5 浏览器上传 → ONNX Layout → 发布 → RAG 引用：通过（受控测试夹具）

先通过知识库页面上传 `docs/decisions/003-local-onnx-layout-routing.md`，得到：

- `document_id=e7622003-4bc5-40b7-a66a-058e39cc4ccd`
- `version_id=01a027a2-962a-7770-8a69-829c7a338344`
- 版本 `ready`，解析元数据为 `chunkCount=29`、`embeddingCount=29`、`layoutPartial=false`。

再用 `testdata/mesguard-onnx-layout-integration.pdf` 上传唯一文本的 PDF，最新成功样本为：

- `document_id=7c2431d4-65bc-43fc-a476-1e785d437591`
- `version_id=01a027ae-685c-7a6c-a779-90a5350f9613`
- `task_id=01a027ae-685c-7a6f-bae9-2b0e8be160a8`
- 版本 `ready`，任务 `succeeded`，`layoutPageCount=1`、`detectorUsed=true`、`layoutPartial=false`。
- 对象 artifact 的 layout model 为 `Provider=onnxruntime`、`Name=PP-DocLayout-M`，renderer 为 `Provider=pdfium-wasm`、`Name=pdfium`。

随后在 `助手` 输入精确词组 `MESGuard PDF Layout E2E Zebra 20260822`，UI 返回：

```text
MESGuard PDF Layout E2E Zebra 20260822 Model evidence fixture for ONNX integration
source:knowledge:01a027ae-685c-7a6c-a779-90a5350f9613/d20bb19c-1736-4dc8-83c3-f805d4a2da17
```

首次 Agent/RAG 样本的 turn 为 `01a027af-0c39-716e-9e32-7239040b6f2b`，运行观测为 `execution_path=agent`、`model_calls=4`、`tool_calls=1`、`outcome=answered`；同会话的 OTel 回归 turn 为 `01a027ce-de64-7ac4-80c8-ed208e024800`，数据库记录 `total_tokens=14284`，Langfuse 在 12:51 显示同一 `run_id=01a027ce-de62-7534-80de-a678f1ff5c13` 下的 `agent.conversation`、`model.chat`、`retrieval.knowledge_search` 和 `tool.search_knowledge`，检索 `result_count=1`、`context_truncated=false`、`degraded=false`。

随后同会话的缓存回归 turn `01a027c9-77c6-7bf0-b197-fab7ae4d364e` 命中 `execution_path=semantic_cache_hit`、`cache_layer=exact`，`model_calls=0`、`tool_calls=0`，并通过 `source_run_id=01a027af-0c39-716e-9e32-7239040b6f2b` 返回同一引用；缓存短路不新增模型/工具 Observation，属于预期行为。

这条记录闭合了浏览器上传、Knowledge Worker、PDFium/ONNX Layout、embedding/发布、Assistant 检索、引用展示、OTel 可视化和精确缓存回放。此前用宽泛文件名查询命中旧 Markdown 文档，说明召回边界存在；本次结论限定为受控测试夹具和精确查询。

### 4.6 soft threshold → Memory Worker → snapshot → 下一轮恢复：通过（hard threshold 除外）

助手会话 `01a027af-eaf0-71b5-8ffb-a2f68c63b7f6` 通过 UI 连续发送上下文压力样本。第 4 个长 turn `01a027b2-7995-74a8-9935-5fa795cdbbf2` 的 context manifest 记录 `soft_threshold_reached=true`、`estimated_upper_bound_tokens=85579`，随后：

- Memory Worker job `01a027b2-8423-7409-a18f-eb220e5fa643` 状态 `succeeded`；
- active snapshot `01a027b2-abb6-7c3a-86e6-995ca93a7176`，`from_seq=1`、`through_seq=8`、prompt version `conversation-memory-v9`；
- 下一轮恢复 turn `01a027b3-bc91-706c-bc2d-4a060acdbcf1` 的 manifest 带 `summary_snapshot_id`，`tail_from_seq=6`、`tail_through_seq=9`、`context_degraded=false`；
- UI 返回“恢复链路已读取”。

这证明 soft threshold 之后的异步记忆生成、快照激活、摘要+tail 恢复和 UI 回显已真实联调。hard threshold/硬压缩仍以确定性后端测试为证据，在线未宣称硬阈值收益。

## 5. 当前结论与后续缺口

| 项目 | 结论 |
| --- | --- |
| OTel 导出 | 通过；Langfuse UI 已收到真实 Observations。 |
| ONNX Layout 开关/资源/Worker 启动 | 通过；受控 PDF 已完成浏览器上传、Layout artifact 生成、发布和 RAG 引用。 |
| 语义缓存 FE/BE | 通过；有数据库和运行观测双重命中证据。 |
| Evidence Gate FE/BE | 通过；真实在线运行安全地产生 partial report。 |
| Evidence Gate Early Exit 质量收益 | 不能由本次单条在线任务宣称；确定性门禁测试通过，需扩充 reviewed/provider 评测集后再报告收益。 |
| GitHub MCP 成功取证 | 受控固定路径通过；任意仓库搜索/tree/commit 导航仍未作为成功样本宣称。 |
| 上下文 soft threshold / 分层记忆恢复 | 通过；hard threshold 在线收益仍未宣称。 |
| 宽泛知识问题的 Agent token 预算 | 已复现一次，后续应增加受控检索、结果截断或单独预算回归。 |

Langfuse Trace 页面和 MESGuard Workbench 页面已保留在浏览器中，便于继续查看。本次没有创建 Git commit。
