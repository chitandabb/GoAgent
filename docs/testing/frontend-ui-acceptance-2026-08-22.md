# 前端结构与真实数据验收记录（2026-08-22）

## 1. 验收范围

本轮针对真实 Vite 前端、MESGuard API、PostgreSQL、RabbitMQ Worker 和知识库接口完成：

- 收敛全局入口：工单、任务、助手、知识库；诊断工作区保留为工单内操作上下文。
- 删除冗余环境说明、重复入口和非用户可见的技术解释。
- 验证助手引用内联展示、来源标题/页码/片段和浅色来源预览。
- 验证会话标题使用首条用户消息，且新会话标题写入数据库。
- 验证诊断任务列表分页、加载态和工单详情页最近任务上限。
- 清理本地历史业务演示数据，保留合成工单与完整知识库，再从真实页面重跑诊断与知识引用。

测试工作区：`D:\develop\project\go-project\GoAgent`。测试账号为本地验收账号，未将密码写入仓库。

## 2. 代码与结构验收

| 项目 | 验证结果 |
| --- | --- |
| 全局导航 | `WorkbenchLayout` 只保留“工单 / 任务 / 助手 / 知识库”，系统管理独立收纳；根路径重定向到 `/cases`。 |
| 诊断工作区 | 工单详情进入 `/workbench/:workspaceId`；工作区左侧只保留工单卷宗和“选择工单”，不再重复显示全局工作台/知识会话入口。 |
| 引用展示 | `CitationList` 通过授权预览 API 获取文档/附件标题、页码和片段，默认内联显示；点击“查看完整片段”打开浅色 `来源预览`。不猜测对象存储 URL。 |
| 会话标题 | 列表接口返回 `firstUserMessage`；前端按显式标题 → 首条用户消息 → 已加载消息回退；后端首条 user message 会生成最多 72 个字符的持久标题。 |
| 任务列表 | `/tasks` 使用真实 `total`、每页 6 条、上一页/下一页和页码边界；`DataTable` 不再把当前页行数误显示为总数。 |
| 工单详情 | `/cases/:caseId` 只加载最近 5 条任务，超过部分提供“查看全部 N 个任务”入口，避免历史任务把页面无限拉长。 |
| 本地工作区索引 | `sessionStorage` 工作区索引升至 v2，清理服务端任务后不会继续显示已失效 taskId。 |

## 3. 数据清理与重建

清理前先生成 PostgreSQL custom-format 快照：

```text
artifacts/backups/mesguard-preclean-20260822.dump
SHA-256: 437F9D824A76C2211AA6AD7AE431488A9FAC71421EF567027B41976F9DF95C75
```

清理范围只针对本地业务演示数据：诊断任务、报告、证据、快照、任务事件、会话、turn、消息、附件、语义缓存及其已发布 outbox 历史。保留：

- 4 个合成 ERP 工单：`TKT-1001` ～ `TKT-1004`；
- 1 个演示 SQL Server 数据源；
- 全部知识库：42 个文档、14,957 个 chunk，知识文档版本与 embedding 未删除；
- 3 个可用账号：`admin01`、`analyst01`、`ops-admin`。

清理后通过数据库复核确认：诊断任务和报告从 0 开始，随后仅由浏览器真实操作产生本轮样例；知识库数量保持不变。

## 4. 真实页面联调样例

### 4.1 诊断任务

路径：`工单 → TKT-1002 → 进入诊断工作区 → 开始诊断`。

- 请求：`请分析 TKT-1002 工单无法关闭的根因，核对未完成工序与库存/报工状态，给出可验证的修复建议和证据。`
- `taskId=01a0287b-0796-731a-b704-5f677d6f6f71`
- `reportId=01a0287b-5170-783a-9572-9df9a26f1782`
- 前端实时收到：任务已创建 → 开始执行 → 诊断成功。
- 最终状态：`succeeded`，`partial=true`，报告结论为“证据不足”；停止原因 `evidence_gate_partial`。

这条样例证明前端、API、SSE、Diagnosis Worker、Evidence Gate、报告持久化和报告页面闭环；它被明确记录为安全降级/部分报告，不宣称在线 Early Exit 质量收益。

另有一条用于发布演示的受控成功样例，使用没有历史任务的 `TKT-1003` 和完整可读的 `internal/agent/skill.go` 固定路径取证，任务 `01a028b5-58c2-7d5b-99d8-0f2cc5304500` 为 `succeeded`、报告 `conclusive`、`partial=false`、低风险，详见 [`diagnosis-success-e2e-2026-08-22.md`](./diagnosis-success-e2e-2026-08-22.md)。

### 4.2 知识引用与会话标题

路径：`助手 → 新对话 → 精确检索 PDF 夹具短语`。

- 成功会话：`conversationId=01a02888-35bb-746e-94e2-1bcdec88e993`；turn：`01a02888-3905-7104-90ea-f8d105f63fd3`。
- 查询：`请检索精确短语 MESGuard PDF Layout E2E Zebra 20260822，返回 1 个片段。`
- UI 显示真实来源标题 `mesguard-onnx-layout-integration.pdf`、页码 `第 1 页`、原文片段和浅色预览弹窗。
- 数据库中 `conversations.title` 已写入首条用户消息，侧栏重载后不再出现“未命名会话/新会话”。
- Langfuse Trace 页面可看到同一会话的 `agent.conversation`、`tool.search_knowledge`、`retrieval.knowledge_search` 和 `model.chat` Observations；本轮诊断也可看到 `agent.diagnosis` 与任务相关 run。

另一个受控两片段查询验证了 `003-local-onnx-layout-routing.md` 与 PDF 夹具的双引用；宽泛知识问题触发过 `agent token budget exhausted`，已作为失败边界观察后从最终演示数据中移除。

## 5. 浏览器与构建结果

已检查桌面端 1440×900：

- [工单列表](../../docs/screenshots/ui-cases-20260822.png)
- [任务列表](../../docs/screenshots/ui-tasks-20260822.png)
- [助手内联引用](../../docs/screenshots/ui-assistant-citations-20260822.png)
- [浅色来源预览](../../docs/screenshots/ui-assistant-source-preview-20260822.png)
- [诊断工作区](../../docs/screenshots/ui-workbench-20260822.png)
- [任务详情](../../docs/screenshots/ui-task-detail-20260822.png)
- [正式报告](../../docs/screenshots/ui-report-20260822.png)
- [知识库](../../docs/screenshots/ui-knowledge-20260822.png)
- [用户管理](../../docs/screenshots/ui-admin-users-20260822.png)

验证命令：

```text
go test ./... -count=1                 通过
Set-Location web; npm run build       通过
docker compose config --quiet         通过（此前启动配置复核）
git diff --check                       通过
```

## 6. 仍保留的边界

- 知识库故意保留历史文档和失败/部分完成版本，没有把它们伪装成全量生产数据。
- 当前示例任务是安全降级的部分报告；不能据此声称诊断结论质量或 Early Exit 性能收益。
- `AssistantPage` 仍承担会话列表、turn 流式状态、附件和引用编排，已拆出引用组件，但 hooks 级别的进一步拆分留作下一轮结构债务。
- Schema Catalog、数据源维护和系统监控页面仍按当前实现显示受限原型/空态，不纳入本轮“真实接入”宣传。
