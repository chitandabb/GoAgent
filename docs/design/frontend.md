# MESGuard 前端设计

## 文档状态

- 本文描述 `web/` 目录中 React 工作台的结构、设计语言落地和与后端契约的对接方式。
- 当前实现采用**真实认证 + 真实 M1 诊断闭环 + 未实现域显式 Mock/占位**的渐进接入方式。
  工单、诊断任务、TaskEvent SSE、取消、正式报告、人工复核和管理员恢复均连接
  `api/openapi.yaml` 中的真实接口；知识助手尚未接入，知识库和部分管理原型仍使用 Mock。
- 视觉规范来源是 [`DESIGN-apple.md`](DESIGN-apple.md);本文只记录落地决策,
  不重复 token 数值。

## 技术选型

| 关注点 | 选择 | 理由 |
| --- | --- | --- |
| 框架 | React 19 + TypeScript + Vite | Nginx 托管纯静态,无 SSR 需求 |
| 路由 | React Router v8(library 模式) | 嵌套布局 + 受保护路由 |
| 服务端状态 | TanStack Query | 缓存失效、轮询降级、分页均为内置能力 |
| 客户端状态 | React Context(仅认证) | 除当前用户与 CSRF 外无全局客户端状态 |
| 样式 | Tailwind CSS v4 + CSS 变量 token | token 唯一落点 `src/styles/tokens.css` |
| 组件 | shadcn/ui + Radix + TanStack Table | 组件库负责行为与可访问性;Apple token 负责外观 |
| API 类型 | 按 OpenAPI 手写(临时) | `m1-types.ts` 与当前机器契约对齐，后续可由 openapi-typescript 生成替换 |

组件库迁移已于 2026-07-27 完成。`components.json` 将 shadcn 生成路径固定到
`shared/ui`,现有页面不直接依赖 Radix 或 sonner;Apple token 仍是外观的唯一
来源。

## 目录结构与依赖规则

```text
web/src/
├─ app/                 应用壳:router、providers、auth、layouts、404
├─ features/            按业务域分片:auth / cases / diagnosis / reports /
│                       assistant / knowledge / admin
├─ shared/
│  ├─ api/              API 边界:client/auth/business/task-events + M1 类型
│  ├─ lib/              status 元数据映射、时间格式化等纯函数
│  └─ ui/               设计系统组件(Button/Card/Badge/DataTable/Dialog/
│                       Toast/Field/Chips/AttachmentPreview/…)
├─ mocks/               全部模拟实现(临时,见下文替换步骤)
└─ styles/tokens.css    设计 token 唯一落点
```

依赖规则:

1. feature 之间不互相 import;共享代码下沉到 `shared/`。
2. 业务组件只允许从 `@/shared/api` 取数据,禁止直接 import `@/mocks`。
3. 组件内不写裸 hex 颜色,只引用 token 类。
4. 页面不直接操作 `window.confirm/prompt`,统一使用 `shared/ui/Dialog`。

## 页面地图与权限

```text
/login                      登录(演示账号提示)
/change-password            修改密码;mustChangePassword=true 时被强制跳转至此
/cases                      外部工单列表(数据源切换、搜索、状态筛选)
/cases/:id                  工单详情(来源指纹、相关任务入口、发送到工作台)
/workbench/:workspaceId     统一 Agent 工作台(左侧会话历史、中间问题/任务进度/
                            报告、右侧卷宗与待办工单)
/tasks                      当前浏览器最近任务入口(不是服务端任务列表;支持输入 taskId)
/tasks/:id                  真实任务摘要与 SSE 时间线;取消 / 条件化 admin 恢复 /
                            重新诊断；证据和工具明细明确标记为后端未开放
/tasks/:id/report           正式报告、证据声明元数据、Token/版本与真实复核历史
/knowledge                  知识库(个人库 / 全局库 / 案例卡片;入库状态机)
/admin/users                用户管理(创建、启禁、改角色、重置密码)      [admin]
/admin/data-sources         数据源与 Schema Catalog(扫描、草稿编辑、发布) [admin]
/admin/system               未接入占位(后端尚无监控、任务列表和死信接口) [admin]
*                           404
```

权限由 `app/auth.tsx` 的 `RequireAuth` / `RequireAdmin` 路由守卫实现;
analyst 访问 admin 路由重定向首页,未登录访问任何页面重定向 `/login`
并携带回跳地址。

`/workbench/:workspaceId` 是目标统一 Agent 主体验。会话不固定一个主工单或任务；右侧卷宗
允许选择工单，发送消息时把选择固化为结构化消息引用。用户明确要求诊断后，目标链路由
会话 Agent 调用受控 `create_diagnosis_task` Tool，中间区域再显示任务进度、报告与复核。
选择工单本身不触发模型调用或任务创建。

当前后端没有 conversation/message 或 Agent 创建任务 Tool，因此 `workspaceId`、当前卷宗选择及
taskId 关联仍只是浏览器 `sessionStorage` 导航适配层，现有 UI 仍直接调用任务创建 API。
目标边界见 ADR 004；`/assistant` 兼容路由已重定向至工作台，但知识会话尚未接入统一外壳。
保留 `/tasks/:id` 和报告深链接用于刷新恢复、分享和运维定位。

## 设计语言落地决策

DESIGN-apple.md 是营销站规范,工作台按以下决策"翻译":

- **继承**:单一交互色 `#0066cc`;白/羊皮纸/近黑三层表面;卡片 = 白底 +
  1px hairline + 18px 圆角、无投影;pill 圆角只给操作类元素(按钮、筛选
  chip、搜索框);字重阶梯 300/400/600;按压态 `scale(0.95)`;毛玻璃仅用于
  toast 与未来的 sticky bar。
- **密度调整**:表格、表单、列表用 14px 档;17px 阅读节奏只保留给报告正文
  和工单描述这类"阅读态"内容。
- **中文字体**:栈为 `-apple-system, SF Pro Text, Inter, PingFang SC,
  Microsoft YaHei, Segoe UI, system-ui`;负字距只作用于拉丁/数字为主的大标
  题,中文标题字距归零。
- **状态色扩展**(规范 Known Gaps):ok/warn/danger 三色只表达状态语义,
  不作交互色;交互仍然只有一个蓝。

## 状态机 → UI 映射

领域状态机(domain-and-state-machine.md)在界面上的固定表达:

| 状态机 | UI 表达 | 位置 |
| --- | --- | --- |
| DiagnosisTask | Badge(执行中带呼吸点)+ pending/running/cancel_requested 的取消按钮；终态可重新诊断；仅 `failed + 无报告 + agent_execution_failed` 向 admin 展示恢复 | 最近任务入口/详情 |
| TaskEvent | 按后端真实生命周期事件显示并保留安全 payload 摘要；未知事件回退到事件名，不假设步骤/工具事件 | 任务详情-执行过程 |
| SSE 连接 | 补读历史(灰)/实时连接(绿)/断线续传(橙)/重试停止(红)/已结束(灰) | 执行过程卡头部 |
| 报告结论 | 分开展示业务摘要、技术摘要、结论、风险、置信度、限制、缺失证据和运行元数据 | 报告页 |
| ReportReview | 最新一条为当前有效反馈,历史倒序展示 | 报告页反馈区 |
| Message 生成 | 旧 Mock 原型保留流式光标与 interrupted 状态，但页面未挂载 | 知识助手 |
| Attachment/文档入库 | 已上传/处理中(轮询刷新)/可检索/处理失败(含原因,不伪装成功) | 知识库 |
| Catalog 版本 | draft(可编辑白名单)/ published / retired;未发布数据源标"不可用于 Text-to-SQL" | admin 数据源 |

## 服务端状态与 SSE 策略

- 查询统一走 TanStack Query。由于后端没有 task 列表接口，`/tasks` 只从
  `sessionStorage` 读取本会话的 taskId 导航记录，再逐条调用任务详情接口；
  本地记录不保存或替代任务状态。
- 任务详情先用 JSON `afterSeq` 分页补读历史，再以相同游标建立同源
  `EventSource`；事件按 `seq` 去重升序。连接中断后前端关闭浏览器自动重连，按指数退避重新调用
  JSON 历史接口探测并补读，再使用最新 `afterSeq` 建流，不把 CSRF Token 放入 URL。
- 暂时断开时保留已收到事件并显示续传状态；最多自动重试 5 次，超过上限进入明确失败态并提供
  手动重连。探测请求返回 401 时复用全局未认证处理清除登录态，由路由守卫返回登录页；不能把
  普通网络故障直接判定为 Session 过期。收到 succeeded/failed/cancelled 后关闭流并刷新任务摘要。
  管理员成功恢复同一任务后重新建立流。
- 关闭页面只断开 SSE,不取消任务;取消走独立命令接口。
- 知识助手的旧 Mock 流式实现不经过任务队列，但当前页面未挂载；待服务端
  conversation/message 契约落地后再接入统一外壳。

## 认证流程

- Session Cookie 由浏览器自动携带;前端在内存持有 CSRF token(不落
  localStorage),非 GET 请求注入 `X-CSRF-Token`。
- 刷新后由 `GET /auth/me` 恢复用户与 token;失败落到 `/login`。
- `mustChangePassword=true` 时 `RequireAuth` 强制跳 `/change-password`,
  该页为独立布局(无工作台导航),只提供改密与退出。
- 改密成功即视为服务端撤销全部 Session:前端清空本地状态并要求重新登录。
- 全局 401 由 fetch 层清除内存 CSRF 和当前用户,路由守卫跳转登录并携带回跳地址。
- 后端当前尚未注册 `change-password`,因此临时密码账号仍不能完成真实改密闭环。

## API 接入状态

| 业务域 | 当前状态 |
| --- | --- |
| 认证 login/me/logout | 真实 API；Session Cookie + 内存 CSRF |
| 数据源、ERP 工单列表/详情 | 真实 API；工单查询按后端分页，503 显示降级错误 |
| 创建诊断 | 当前真实 API 由 UI 直接调用；目标改为 Agent 的受控命令 Tool，能力和幂等由后端注入 |
| 任务详情、取消、SSE | 真实 API；历史补读、断线续传、终态关闭 |
| 统一工作台 | 当前真实诊断入口仍使用本地工作区；目标会话与卷宗/任务解耦，内联 SSE、报告与复核保留 |
| 正式报告、人工复核 | 真实 API；仅展示证据声明，不伪造证据正文；admin 只读复核，创建者权限由后端校验 |
| admin 恢复 | 真实 API；填写原因并使用稳定 UUID 幂等键，409 展示后端原因 |
| task 列表/工单历史诊断 | 后端未实现；只提供本会话 taskId 导航记录 |
| 任务证据明细/工具执行 | 后端未实现；页面显示未开放状态 |
| admin 系统监控/死信 | 后端未实现；页面显示未接入状态 |
| 服务端 conversation/message | 后端未实现；消息工单引用、任务引用和 Agent 创建任务 Tool 也未实现 |
| 知识助手 | 旧 Mock 页面不再挂载，`/assistant` 重定向工作台；知识会话未接入 |
| 知识库、用户与 Catalog 管理 | 仍为 Mock；不属于本次 M1 诊断闭环 |

`src/shared/api/index.ts` 不再导出整套 `mocks/api`，只显式导出未实现域需要的
Mock 函数。`client.ts` 统一处理响应信封、Cookie、CSRF、字段错误、401 和网络
错误；M1 真实适配器位于 `business.ts`，SSE 位于 `task-events.ts`。

后续按业务域渐进替换:

1. 后端完成一个业务域后,在 `src/shared/api/` 增加对应真实适配器;
2. 在 `src/shared/api/index.ts` 将该域导出从 Mock 切换到真实实现;
3. 全部剩余业务域完成后删除 `src/mocks/`;
4. 用 openapi-typescript 生成类型替换手写的 `m1-types.ts`。

mock 保留了与 api.md 一致的错误语义(40101/40301/40401/40901/40921/
40923/42201),前端按 code 分支的行为在切换后无需改动。

## 已知未实现(原型边界)

- 服务端任务列表、工单全量诊断历史、任务级证据/工具执行明细、管理员系统监控与死信接口。
- 服务端 conversation/message、消息工单引用、会话任务引用与 Agent 创建任务 Tool；跨浏览器、跨设备不保留本地会话编排。
- read_attachment 工具调用痕迹在助手消息中的展示;知识文档详情页(chunk
  预览、原图关联);失败文档的重新解析入口。
- 统一工作台在桌面显示三栏；移动端中心流保持单列，会话与卷宗使用左右抽屉。
- 真实附件上传；当前创建诊断契约明确只接受空附件列表。
- 修改密码真实接口、OpenAPI 自动类型生成。
- 知识与管理 Mock 数据存于内存,整页刷新后运行期新建的数据会重置。

## 本地运行

```text
npm --prefix web run dev    # http://localhost:5173
认证依赖 http://127.0.0.1:9090;本地账号使用 mesguard-user 命令初始化
```
