# 统一聊天工具活动与回答来源联调记录（2026-08-22）

## 结论

统一聊天已完成真实前后端联调。用户现在无需打开 Langfuse 就能在回答内判断：

- 本次回答是模型直接回答、企业知识库、公开网络、会话附件，还是回答/语义缓存命中；
- Agent 正在使用哪个工具、查询什么、是否成功、返回了什么安全摘要、耗时多久；
- 自动重试后的新工具事件属于第几次尝试。

界面不展示模型内部思维链、系统 Prompt、密钥、底层异常、原始工具载荷或数据库原始行。SQL 输入仅显示掩码后的只读语句，结果仅显示行数、字段和截断状态。

## 实现范围

| 层次 | 已实现内容 |
| --- | --- |
| Conversation Runner | 每次工具调用生成稳定 `activityId`，在执行前后发布开始/完成事件；工具专用投影器生成业务名称与脱敏输入/结果摘要。 |
| PostgreSQL | `conversation_turn_events` 持久化 `turn_tool_started` / `turn_tool_completed`；事件同时支持 JSON 游标补读和 SSE 实时订阅。 |
| 回答来源 | 助手消息返回 `turnId` 与 `provenance`，包含执行路径、缓存层、工具次数、来源类型计数、结果和耗时。 |
| 前端 | 运行中默认展开处理过程；历史回答固定显示来源，用户主动展开时再懒加载工具事件。 |
| 安全边界 | 原始参数和结果不进入事件；SQL 字符串、数字字面量被掩码；工具失败只显示稳定的用户可读摘要。 |

## 浏览器联调样本

环境：Docker 前端 `http://127.0.0.1:5173`、API `http://127.0.0.1:9090`、Conversation Worker、PostgreSQL、SearXNG、知识库与模型服务。

| 场景 | 输入与观测 | 结果 |
| --- | --- | --- |
| 模型直接回答 | 历史问题“csharp写Agent的框架有哪些?”；消息补读后来源栏显示“模型已有知识 · 未检索知识库或网络”。 | 通过。该回答不是知识库或网络搜索结果。 |
| 公开网络 | turn `01a0293d-d10c-761a-b3cc-5c0739f9428e`；问题要求搜索微软官方资料。SSE 实时出现 `web_search`、`fetch_public_page`、查询词、页面标题/域名、结果摘要和耗时。第一次执行自动重试，事件历史保留两次尝试；最终观测为 `tool_calls=5`、`duration_millis=24587`、`outcome=degraded`，回答引用 2 个公开网页来源。 | 通过。工具过程、重试和最终公开网络来源都可见；搜索结果质量导致过一次重试，未把它写成无降级样本。 |
| 企业知识库 | turn `01a02940-7f28-7d17-bc04-97cbb6bb6527`；检索 `mesguard-onnx-layout-integration.pdf`。运行中显示 `search_knowledge`；终态显示“企业知识库”，引用卡显示文档标题、页码和片段，历史展开仍能读取工具摘要。 | 通过。知识检索、引用展示和事件持久化闭环成立。 |
| 精确回答缓存 | turn `01a02945-13d9-7c5d-b120-8be1df778437`；重复稳定问题后命中 `execution_path=semantic_cache_hit`、`cache_layer=exact`、`tool_calls=0`、`duration_millis=2`，`source_run_id=01a02888-3905-7104-90ea-f8d105f63fd3`。UI 显示“回答缓存 · 原回答来源：企业知识库 1 条”。 | 通过。缓存短路没有伪造工具步骤，同时保留原回答来源。 |

联调过程中还修复了一个正文展示缺陷：后端引用 marker 只用于引用校验和卡片定位，现在会在 Markdown 渲染前移除，不再把 UUID 替换成“排查任务”后泄露到正文。

## 自动化验证

```text
go test ./internal/conversation ./internal/agent ./internal/platform/postgres ./internal/transport/http ./api -count=1
PASS

npm run build
PASS（仅保留既有的 Vite chunk size warning）

GET /healthz
HTTP 200
```

新增测试覆盖：活动事件类型与终态语义、活动生命周期和大小校验、工具中间件开始/完成事件、知识库/网络结果投影、SQL 原始行与错误不泄漏、回答来源投影。

## 截图

- [缓存与原始知识库来源](../../output/playwright/unified-chat-cache-provenance-20260822.png)
- [公开网络工具活动历史](../../output/playwright/unified-chat-web-tools-20260822.png)

## 已知边界

- 本次功能上线前产生的历史回合只有运行观测，没有工具活动事件；来源仍可显示，展开时会明确提示当时未保存调用摘要。
- “来源条数”来自该次运行取回并校验过的来源集合；回答实际使用的引用数量仍以独立引用卡为准。
- 不展示自由文本思维链。用户看到的是应用生成、可审计、可脱敏的动作与结果摘要。
