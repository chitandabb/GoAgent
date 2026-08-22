# 统一助手全链路验收记录（2026-08-22）

## 验收范围

本轮在 Docker Compose 环境中，从当前 React 前端发起三次请求，覆盖企业知识问答、工具活动、工单关联、排查任务创建、异步执行、Evidence Gate 和右侧工单卷宗。所有结果均来自本轮新产生的会话回合和任务，不复用历史任务作为通过证据。

```text
React 统一助手
  -> Conversation API / SSE
  -> Conversation Worker
  -> 企业知识检索或 create_diagnosis_task
  -> PostgreSQL Outbox / RabbitMQ
  -> Diagnosis Worker
  -> Evidence Gate / Report
  -> Task SSE
  -> 工单卷宗
```

运行环境：

- Web：`http://127.0.0.1:5173`
- API：`http://127.0.0.1:9090`
- 后端、前端、Conversation Worker、Diagnosis Worker、Knowledge Worker、Memory Worker、Outbox Relay、PostgreSQL、RabbitMQ、Redis、MinIO 和 SQL Server 均运行于 Docker。

## 场景一：企业知识问答

问题：

> SSIS 数据流处理 XML、文本或图片等 BLOB 数据时，如果缓冲区频繁写入磁盘，应该调整哪两个临时存储路径、放到什么存储上，并通过哪个性能计数器确认是否仍在暂存？请只根据企业知识库回答并给出引用。

结果：

| Item | Result |
| --- | --- |
| Conversation | `01a02960-1f0f-7ecc-9008-7dc76d285a68` |
| Turn | `01a0297a-bfdf-7722-ba02-bb33ba289c95` |
| Turn status | `completed`, attempt `1` |
| Run outcome | `answered` |
| Tool calls | `2` |
| Duration | `10,706 ms` |
| Knowledge source | `microsoft-ssis-operational-tuning-guide.docx` |
| Visible citations | `2` |

前端在正文生成前依次显示 `search_knowledge` 和 `read_conversation_tool_result`，并展示查询摘要、命中文档和安全结果摘要。最终回答使用 Markdown 列表，引用卡可展开查看原始片段。

## 场景二：证据不足的受控结束

请求：

> 请为当前 TKT-1002 创建排查任务：核对为什么系统仍判定 OP20 未完成，整理已确认事实、还需补充的信息，并生成可直接交给开发人员的摘要。

结果：

| Item | Result |
| --- | --- |
| Conversation turn | `01a0297b-55db-76b6-b151-062b94d99f93` |
| Conversation attempts | `2`；首次运行耗尽 Agent token budget，30 秒后自动重试并完成 |
| Diagnosis task | `01a0297c-1054-7a77-a014-842e937e96be` |
| Task status | `succeeded`, attempt `1` |
| Report | `01a0297c-4521-76dc-9db9-ae1be354e848` |
| Conclusion | `inconclusive` |
| Partial | `true` |
| Task events | `task_created -> task_started -> task_succeeded` |

工单读取成功，但数据目录没有匹配到可安全查询的对象；Conversation Agent 的一次只读查询尝试被策略拦截。Diagnosis Worker 最终完成任务，Evidence Gate 因缺少可支撑根因的有效证据将报告标记为 `inconclusive`。前端显示“已完成”和“证据不足”，没有把任务完成误写为根因已确认。

## 场景三：结论明确的成功任务

请求：

> 请为当前 TKT-1001 创建排查任务：只读取工单，核对标题与现场描述是否一致，并整理一句可直接交给开发人员的摘要。

结果：

| Item | Result |
| --- | --- |
| Conversation turn | `01a0297c-bde6-77de-be3c-98f5308d9a09` |
| Turn status | `completed`, attempt `1` |
| Conversation tool calls | `1` |
| Conversation duration | `3,948 ms` |
| Diagnosis task | `01a0297c-c9db-7bb7-ae3e-65241ad4856f` |
| Task status | `succeeded`, attempt `1` |
| Report | `01a0297d-066f-716f-974f-9438b621b13b` |
| Conclusion | `conclusive` |
| Partial | `false` |
| Diagnosis tool | `read_external_case`, `succeeded` |
| Report evidence | `case_snapshot` × 2，均为 `supports + valid` |
| Task events | `task_created -> task_started -> task_succeeded` |

任务在约 15.2 秒内完成。右侧卷宗显示“任务已提交 → 开始处理 → 排查单已生成”，最终显示“结论明确”。报告确认工单标题和现场描述均指向末道工序 OP50 报工后成品库存未联动入库。

## 前端验收

- 页面固定在浏览器视口内，会话列表、对话区和工单卷宗独立滚动。
- 新会话只创建一条记录，首条用户消息更新会话标题。
- 用户消息使用统一的品牌色气泡；助手正文按 Markdown 渲染。
- 工具活动在回答完成前可见，完成后可再次展开，不展示自由文本思维链。
- 工单可以直接在右侧列表选择，卷宗提供工单号和摘要复制入口。
- 工单切换后，卷宗只展示当前工单创建的最近任务，不会短暂串入上一张工单的处理结果。
- 修复后切换到无历史任务的 `TKT-1004`，卷宗显示“尚未开始排查”；切回 `TKT-1001` 后恢复对应的“结论明确”任务。
- 任务成功与报告结论分别展示；`succeeded + inconclusive` 不会显示为“结论明确”。

本轮最终状态截图：

![统一助手全链路验收](../screenshots/unified-assistant-e2e-20260822.png)

## 结论与边界

本轮证明当前前端能够完成“企业知识问答 → 工单关联 → Conversation Agent 创建任务 → 异步排查 → Evidence Gate → 工单卷宗”的完整链路，并能区分结论明确与证据不足两种报告状态。

本次结果只覆盖两个固定合成工单和一个公开 SSIS 调优文档。单次本地耗时不构成生产 SLA，复杂根因任务的可判定性仍取决于数据目录、只读数据源和知识资料覆盖度。
