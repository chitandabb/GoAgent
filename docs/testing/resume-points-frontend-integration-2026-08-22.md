# 简历亮点前后端联调证据矩阵（2026-08-22）

## 1. 判定口径

本记录把“已实现”“后端测试通过”“前端冒烟”“真实前端→API→Worker/外部依赖→前端结果”分开。只有最后一种才计为前后端联调通过；模型或外部工具失败时，若 UI、API、SSE、重试、降级和报告闭环可观察，则记录为“失败闭环联调”，不把失败样本升级成成功样本。

| 标记 | 含义 |
| --- | --- |
| 通过 | 当前样本已完成真实前端到后端/外部依赖再回到前端的闭环。 |
| 部分 | 链路触发或失败安全闭环已验证，但简历描述的完整成功能力仍缺证据。 |
| 冒烟 | 仅做了单元/集成测试、资源启动、页面可打开或 API 健康检查。 |
| 未通过 | 当前样本暴露了缺陷或外部依赖未完成，不能作为成功证据。 |

## 2. 前四点结论

| 简历点 | 当前实现与已有测试 | 本次真实联调证据 | 结论与简历口径 |
| --- | --- | --- | --- |
| 1. Agent 编排与 MCP 工具治理 | `ToolProfile`、`RunAccess`、Diagnosis/Conversation Runner、GitHub MCP 只读 Tool 和 Tool Selection/Evidence Gate 评测均已在后端实现并有测试。 | 先通过任务 `01a02796-59d8-79f6-879c-245d514b5d6f` 观察到路径校验失败、MCP transport timeout 和 partial 降级；随后用工作台发起最小固定路径任务 `01a027b9-686c-76fc-8607-5cfea362838c`，前端收到 `partial=false` 的完整报告，报告 `01a027b9-8e5d-73cf-b734-ed3dd6ab3eab` 含 1 条 `get_file_contents` 证据，读取 `chitandabb/GoAgent` 的 `internal/agent/evidence_orchestrator.go`，返回 `checkEvidence`、`buildGraph`、`evidenceGapsNeedRetrieval` 等函数摘要。 | **通过（受控固定路径）。** 前端→API→Diagnosis Worker→GitHub MCP→Evidence Gate→报告→前端闭环已证实；失败/超时也能安全降级。当前证据不覆盖任意仓库搜索、tree/commit 导航或复杂多工具调查，简历应保留“只读 MCP 接入、固定路径代码取证和失败降级”边界。 |
| 2. 安全 Text-to-SQL 与数据库诊断 | QueryGuard、只读 SQL executor、Schema Catalog、数据源/资源授权和 SQL Server 适配器已有后端测试。 | 从 `/cases` 打开 `TKT-1002`，进入助手并发送只读 SQL 请求。第一次没有已发布 Catalog 时，UI 正确返回 Catalog 缺失并阻断查询。随后只在本地演示数据源写入已发布 Catalog 夹具，再次从 UI 发起请求，Conversation Worker 完成 Catalog 查询和只读 SQL，UI 返回外部工单状态 `New`、优先级 `Normal`。运行观测 `turn_id=01a02794-88d8-73c3-b78c-dfc5567568c6`：`execution_path=agent`、`model_calls=8`、`tool_calls=3`、`outcome=answered`。 | **通过（限定为本地演示 Catalog 夹具）。** 可以作为真实 FE/BE Text-to-SQL 联调证据；前端 `/admin/data-sources` 的 Catalog 管理仍是 Mock，不能宣称 Catalog 管理页面已联调。 |
| 3. 混合文档解析与 Agentic RAG | 版本化文档、PDF/Office 解析、PP-DocLayout-M ONNX 路由、Embedding、FTS/Vector/RRF、引用门禁和 `search_knowledge` 已实现；固定数据集和 provider-free 测试已有记录。 | 先上传 Markdown `e7622003-4bc5-40b7-a66a-058e39cc4ccd`，服务端版本 `01a027a2-962a-7770-8a69-829c7a338344` 完成 29 chunks/29 embeddings；再从浏览器上传测试 PDF，最新文档 `7c2431d4-65bc-43fc-a476-1e785d437591`、版本 `01a027ae-685c-7a6c-a779-90a5350f9613`、任务 `01a027ae-685c-7a6f-bae9-2b0e8be160a8` 均 `ready/succeeded`，`layoutPageCount=1`、`detectorUsed=true`，artifact 标明 `onnxruntime / PP-DocLayout-M` 与 `pdfium-wasm`。随后助手用精确词组 `MESGuard PDF Layout E2E Zebra 20260822` 检索，Agent turn `01a027ce-de64-7ac4-80c8-ed208e024800` 返回原文和 `source:knowledge:01a027ae-685c-7a6c-a779-90a5350f9613/...` 引用，并在 Langfuse 显示 OTel Observations。 | **通过（受控测试文档与精确检索词）。** 已完成浏览器上传→Knowledge Worker→PDFium/ONNX Layout→Embedding/发布→Assistant RAG→OTel/引用展示的闭环；同会话缓存 turn `01a027c9-77c6-7bf0-b197-fab7ae4d364e` 另证实精确缓存回放；宽泛文件名检索曾命中旧文档，说明召回边界仍需记录，不能写成“任意文件名检索均稳定”。 |
| 4. 动态上下文治理与分层记忆 | Context preflight、预算、压缩/快照、Memory Worker、分层记忆和固定 Tool Profile/RunAccess 约束已有实现与后端确定性测试。 | 助手会话 `01a027af-eaf0-71b5-8ffb-a2f68c63b7f6` 通过 UI 连续发送压力样本；第 4 个长 turn `01a027b2-7995-74a8-9935-5fa795cdbbf2` 记录 `soft_threshold_reached=true`、`estimated_upper_bound_tokens=85579`。随后 Memory Worker job `01a027b2-8423-7409-a18f-eb220e5fa643` 成功生成并激活 snapshot `01a027b2-abb6-7c3a-86e6-995ca93a7176`（through seq 8）；下一轮恢复 turn `01a027b3-bc91-706c-bc2d-4a060acdbcf1` 的 manifest 带 `summary_snapshot_id`，`tail_from_seq=6`、`tail_through_seq=9`，UI 返回“恢复链路已读取”。 | **通过（soft threshold/异步记忆/恢复链路）。** 已验证 UI→API→Conversation Worker→soft threshold→Memory Worker→active snapshot→下一轮摘要+tail 恢复→UI；hard threshold/硬压缩仍以确定性后端测试为证据，在线未宣称硬阈值收益。 |

## 3. 第五点相关的补充结果

| 能力 | 当前证据 | 结论 |
| --- | --- | --- |
| OpenTelemetry | 前端知识助手请求 → API → Conversation Worker → OTLP → Langfuse。Langfuse Trace 页面已显示 `agent.conversation`、`tool.search_knowledge`、`retrieval.knowledge_search`、`model.chat` 等真实 Observations。 | **前后端联调通过。** |
| 语义缓存 | UI 首轮生成后，近似问题命中 PostgreSQL/pgvector 语义缓存；命中轮 `execution_path=semantic_cache_hit`、`model_calls=0`、`tool_calls=0`、`total_tokens=0`，UI 返回同一回答和引用。 | **前后端联调通过。** |
| Evidence Gate | UI → API → SSE → Diagnosis Worker → Evidence Gate → 报告持久化 → UI 已形成真实链路；TKT-1002 产生了 `partial=true` 安全降级报告。 | **链路通过；在线 Early Exit 质量收益未证明。** |
| Early Exit | `go test ./internal/agent -run 'Test(EvidenceOrchestrator|EvaluateEvidenceGateEarlyExit|BuildEvidenceGateEarlyExit)' -count=1 -v` 全部通过，覆盖质量门禁、成对控制、Human Review 和 Token 限制。 | **确定性门禁通过；不是模型质量收益的替代证据。** |
| ONNX Layout | 模型和 Linux Runtime SHA-256 校验通过，Docker Knowledge Worker 能加载并启动。 | **资源/启动冒烟历史快照；完整浏览器上传解析见下方补充联调样本。** |

| ONNX Layout（本轮补充联调） | 测试夹具 `testdata/mesguard-onnx-layout-integration.pdf` 已由浏览器上传，artifact 包含 `detectorUsed=true`、`PP-DocLayout-M`、`onnxruntime` 和 `pdfium-wasm`，并已通过精确词组回到 Assistant RAG 引用。 | **受控 PDF 上传/解析/发布/RAG 通过；宽泛召回仍有边界。** |

上面的 ONNX 行保留了启动冒烟的历史快照；本行补充本轮真实上传与检索证据。第五点的 OTel、语义缓存和 Evidence Gate 详细日志见 [`observability-and-frontend-integration-2026-08-22.md`](./observability-and-frontend-integration-2026-08-22.md)。

## 4. 前端结构调整结论

这次需要改，而且已经先做了低风险、能直接改善后续联调的结构调整：

- `WorkbenchLayout` 统一承载 ToB 控制台壳层：深色操作侧栏、收敛后的业务导航和移动端导航；页面不再各自实现壳层。
- `shared/diagnosis-run/EventTimeline` 和 `useDiagnosisRun` 统一诊断事件展示、SSE 去重/重连、取消、恢复和任务缓存失效，`shared/workspace` 统一工作区导航上下文；`cases`、`diagnosis`、`workbench` 不再横向导入彼此的内部模块。
- 助手引用改为内联显示来源标题、页码和片段，点击后进入浅色授权预览；会话侧栏使用首条用户消息作为标题，任务列表使用服务端 `total` 做分页，工单详情只展示最近任务。
- `MetricStrip`、`PageHeader`、`Card` 和 token 统一了信息密度、状态色和 ToB 视觉语言；Catalog/数据源仍明确保留 Mock 边界。
- 已验证 `go test ./... -count=1`、`npm run build` 和 `git diff --check`；浏览器已检查工作台、任务详情、助手引用/来源预览、知识库和诊断任务分页。完整截图与数据清理记录见 [`frontend-ui-acceptance-2026-08-22.md`](./frontend-ui-acceptance-2026-08-22.md)。

仍建议作为下一轮结构债务处理，但不阻塞本次页面交付：

1. 把 `AssistantPage` 的会话列表、turn 流式状态、附件和引用拆为 hooks/子组件；当前页面仍承担过多职责。
2. Catalog 后端接口完成后，再把 `shared/api/index.ts` 的 Mock 导出切换为真实适配器，并补一条浏览器上传/发布/检索回归。

## 5. 工作区与证据边界

- 本记录对应当前未提交工作区；没有创建 Git commit。
- 本地 Catalog 发布版本是测试夹具，不是代码内置种子，也没有把数据库密码、Provider Key、OTel Header 或 GitHub Token 写入文档。
- 结论按“实现、测试、冒烟、真实联调、在线质量收益”分层，后续简历修改应以本矩阵为准，不把 partial 或 startup smoke 改写成完整成功率。
