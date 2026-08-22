# MESGuard 前端设计

## 文档状态

本文描述 `web/` React 工作台的当前结构、状态来源和后端接入边界。页面优先展示真实 API 返回的事实；后端尚未开放的能力使用明确空态，不用 Mock 冒充已实现功能。

当前真实接入域：认证、外部工单、诊断任务、任务事件 SSE、报告与复核、会话与 turn、附件与引用预览、知识库文档管理、管理员用户管理。Schema Catalog、数据源维护和系统监控仍保留受限原型或空态。

## 技术选型

| 关注点 | 选择 |
| --- | --- |
| 框架 | React 19 + TypeScript + Vite |
| 路由 | React Router |
| 服务端状态 | TanStack Query，负责缓存、失效、轮询和分页 |
| 客户端状态 | React Context 只承载认证；业务状态优先由 URL、Query 和服务端事实驱动 |
| 样式 | Tailwind CSS v4 + `src/styles/tokens.css` |
| 组件 | 项目自有 UI 组件、Radix、TanStack Table、Lucide |
| API 类型 | `shared/api/m1-types.ts` 与 `api/openapi.yaml` 对齐，后续可生成替换 |

## 目录结构与依赖规则

```text
web/src/
├─ app/                 应用壳、路由、认证和布局
├─ features/            auth / assistant / cases / diagnosis / reports /
│                       knowledge / admin / workbench
├─ shared/api/          fetch、认证、业务接口、SSE 和类型
├─ shared/lib/          状态映射、时间格式化和纯函数
├─ shared/diagnosis-run/诊断运行展示组件、事件时间线与运行控制 hook
├─ shared/workspace/    工作区导航上下文（不承载后端事实）
├─ shared/ui/           Button、Card、Badge、Dialog、Field、Toast 等
├─ mocks/               尚未接入域的显式原型数据
└─ styles/tokens.css    视觉 token 唯一落点
```

依赖规则：

1. Feature 之间不直接互相依赖，共享逻辑下沉到 `shared/`。
2. 页面通过 `shared/api` 访问后端，不直接访问 `fetch` 或数据库。
3. 业务状态由后端响应、SSE 事件和 Query 缓存驱动，不在浏览器伪造任务进度。
4. 组件使用 token 类，不在页面散落裸颜色和临时交互状态。
5. 未实现域必须显示 Mock 或未开放边界，不能把演示数据写成业务事实。

工作台采用 ToB 控制台壳层：桌面端固定深色操作侧栏，主区域保持冷灰工作面和高密度信息卡。全局主入口收敛为“工单 / 任务 / 助手 / 知识库”，诊断工作区作为工单内的操作上下文，不再与全局导航重复。诊断运行时间线、`useDiagnosisRun` 运行控制 hook 与工作区导航上下文下沉到 `shared/`，避免 `cases`、`diagnosis`、`workbench` 之间形成循环或横向 Feature 依赖。`TaskDetailPage` 和工作台的 `DiagnosisRunBlock` 共用同一套任务查询、SSE 事件去重/重连、取消、恢复和 Query 缓存失效策略；页面只负责不同的 ToB 展示组合。

## 页面与权限

```text
/login                    登录
/change-password          首次登录改密
/assistant                会话助手、流式回答、附件和内联引用
/cases                    外部工单列表
/cases/:id                工单详情与服务端诊断历史
/workbench/:workspaceId   诊断工作台
/tasks                    服务端诊断任务列表、状态筛选与分页
/tasks/:id                任务详情、时间线、证据和工具边界
/tasks/:id/report         正式报告与复核
/knowledge                管理员知识文档、版本和解析任务
/admin/users              管理员用户管理
/admin/data-sources       Schema Catalog 原型边界
/admin/system             系统监控未开放空态
```

未登录用户由 `RequireAuth` 重定向到 `/login`；非管理员访问管理页面时显示权限边界或回到业务首页。修改密码成功后服务端撤销会话，前端要求重新登录。

## 数据与事件流

- 非 GET 请求由 `shared/api/client.ts` 自动附加内存中的 CSRF Token；`FormData` 保留浏览器自动生成的 multipart boundary。
- 诊断任务创建返回异步任务，任务进度通过 JSON 查询和 TaskEvent SSE 恢复。
- Conversation turn 通过排队、运行、工具开始/完成、`turn_message_delta`、完成或失败事件驱动 UI；delta 按 `position` 去重和拼接，工具事件按 `activityId` 合并生命周期。
- 每条助手消息固定展示回答来源；历史工具明细在用户展开时按 turn 事件懒加载，运行中的工具名称、脱敏参数和结果则随 SSE 实时更新。
- 知识文档列表由管理员接口分页读取，解析中的任务按状态轮询；版本上传创建新版本，不覆盖历史版本。
- 知识和附件引用只能通过后端返回的 citation 数据和授权预览接口打开，不能从正文中的任意 UUID 或 URL 猜测链接。

## 状态表达

| 后端事实 | 前端表达 |
| --- | --- |
| `pending` / `running` | 明确状态徽章、进度或处理中提示 |
| `cancel_requested` | 取消中，禁用重复取消 |
| `succeeded` / `failed` / `cancelled` | 终态摘要和可用操作 |
| `turn_message_delta` | 流式回答气泡，最终消息回填后移除临时气泡 |
| `turn_tool_started` / `turn_tool_completed` | 回答内实时处理过程；不展示 Prompt、模型思维链或原始工具载荷 |
| 空证据声明 | 明确“报告没有结构化证据声明”，不伪造内容 |
| 未开放接口 | 空态或未开放提示，不生成假数据 |

## Mock 边界

`shared/api/index.ts` 只导出尚未接入真实后端的 Catalog/数据源原型。认证、诊断、会话、知识库和用户管理不再使用旧的本地任务记录或页面级 Mock。

新增真实域时，按以下顺序切换：

1. 在后端 Handler、OpenAPI 和集成测试中落地接口。
2. 在 `shared/api/` 增加真实适配器和类型。
3. 将 `shared/api/index.ts` 的导出从 Mock 切换到真实适配器。
4. 用 Query、SSE 和明确错误态替换页面内临时状态。

## 验证

```powershell
Set-Location web
npm run build
```

浏览器联调依赖本地 API `http://127.0.0.1:9090` 和 Vite `http://127.0.0.1:5173`；完整启动方式见 [`../development.md`](../development.md)。
