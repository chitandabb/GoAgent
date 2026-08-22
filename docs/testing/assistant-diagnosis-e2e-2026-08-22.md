# 会话助手创建排查任务全链路记录（2026-08-22）

## 结论

本轮在统一会话工作台完成真实前后端联调：用户发送普通业务问题，助手通过 SSE 流式返回；用户随后要求处理当前 `TKT-1001` 工单，Conversation Agent 调用受控创建命令生成排查任务；Diagnosis Worker 完成处理后，右侧工单卷宗实时展示事件和明确结论。

最终链路成功。业务界面没有展示任务 UUID、英文状态、Token、模型版本或内部工具名称。

## 联调范围

```text
React 会话工作台
  -> Conversation API / SSE
  -> Conversation Worker
  -> create_diagnosis_task
  -> PostgreSQL + Outbox + RabbitMQ
  -> Diagnosis Worker
  -> read_external_case
  -> Evidence Gate / Report
  -> Task SSE
  -> 右侧工单卷宗
```

使用的业务对话：

1. `先不用处理工单。请只用客服能听懂的一句话告诉我：你能怎样帮助我把客户问题整理后交给开发？`
2. `请为当前 TKT-1001 创建排查任务：只读取工单，核对标题与现场描述是否一致，并整理一句可直接交给开发的摘要。`

两轮会话均一次完成，第一轮产生 2 个 `turn_message_delta`，第二轮产生 3 个 `turn_message_delta`，前端逐步显示返回内容。

## 成功证据

| 项目 | 结果 |
| --- | --- |
| 会话 | `01a028ee-123f-75a0-8136-a6e1c0ac6af6` |
| 第一轮 | `completed`，attempt `1`，18:04:49—18:04:52 |
| 第二轮 | `completed`，attempt `1`，18:04:56—18:05:00 |
| 排查任务 | `01a028ee-4677-7c7b-bac5-417452b526b8` |
| 报告 | `01a028ee-69f7-7a72-a5d9-cfa0c9cca50a` |
| 任务结果 | `succeeded`，attempt `1`，约 9.1 秒完成 |
| 事件顺序 | `task_created -> task_started -> task_succeeded` |
| 工具执行 | `read_external_case` × 1，`succeeded` |
| 报告结论 | `conclusive`，confidence `high`，risk `high` |
| 报告证据 | `case_snapshot` × 3；`supports + valid` × 1，`context + valid` × 2 |
| 前端结果 | `已完成`、`排查单已生成`、`结论明确` |

最终业务结论为：工单标题与现场描述一致，均指向末道工序 OP50 报工成功后成品库存未生成入库记录。

## 前端验收

- 统一工作台保持三栏：会话列表、对话、工单卷宗；模型创建出的最新任务自动进入右侧卷宗。
- 右侧卷宗展示工单标题、现场描述、客户、产品、本次要求、实时事件和业务结论。
- 模型原始回执中的任务 UUID 和英文状态由展示层收口为业务文案；用户消息不再重复显示“任务已创建”标签。
- 页面外层固定在浏览器视口内，各栏独立滚动。
- `1920px` 视口复验时应用铺满页面；此前右侧 320px 空白来自自动化浏览器被固定为 `1600px` 视口后再最大化窗口，不是应用 `max-width`。
- 前端生产构建通过：`npm run build`。

## 录制产物

- [全链路 GIF](../screenshots/assistant-to-diagnosis-e2e-20260822.gif)：`1280×720`，57 帧，约 11.4 秒，约 226 KiB。
- [最终状态截图](../screenshots/assistant-diagnosis-conclusive-20260822.png)：`1600×900`。
- 原始逐帧截图保存在本地 `output/playwright/unified-demo-20260822-v2/`，不作为 README 发布素材。

GIF 使用真实页面逐帧截图生成，没有 4:3 录制容器，因此不存在底部灰色补边。模型等待和任务等待阶段只保留少量有代表性的变化帧，避免长时间停在加载画面。

## 口径边界

本次证据证明“会话请求 → 模型创建任务 → 异步排查 → 右侧卷宗展示结果”在 `TKT-1001` 固定工单上联调成功。它不证明所有复杂根因问题都能得到明确结论，也不把单次本地耗时外推为生产 SLA。
