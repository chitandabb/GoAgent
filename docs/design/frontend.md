# MESGuard 前端设计

## 文档状态

- 本文描述 `web/` 目录中 React 工作台的结构、设计语言落地和与后端契约的对接方式。
- 当前实现采用**真实认证 + 业务 Mock**的渐进接入方式:`login/me/logout` 已连接
  Go 后端,其余尚未落地的业务接口继续使用 `web/src/mocks/`。原型页面保持可运行,
  后端每完成一个业务域就按本文"Mock 替换"一节切换该域。
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
| API 类型 | 手写(临时) | OpenAPI 建立后由 openapi-typescript 生成替换 |

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
│  ├─ api/              API 边界:index.ts(唯一入口)+ types.ts
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
/cases/:id                  工单详情(17px 阅读态、来源指纹、历史诊断)
/cases/:id/diagnose         发起诊断(数据源确认、时间范围、附件、40923 处理、
                            ?retryOf= 重新诊断预填)
/tasks                      任务列表(轮询兜底;admin 可按发起人筛选)
/tasks/:id                  任务详情(执行过程 SSE 时间线 | 证据 | 工具执行;
                            取消 / admin 恢复 / 重新诊断 / 重试关联)
/tasks/:id/report           两层报告(业务摘要 + 技术证据;inconclusive 专属版式;
                            反馈 采纳/部分采纳/驳回)
/assistant                  知识助手(流式对话、停止生成、来源引用、联网开关、
                            附件二选一归属)
/knowledge                  知识库(个人库 / 全局库 / 案例卡片;入库状态机)
/admin/users                用户管理(创建、启禁、改角色、重置密码)      [admin]
/admin/data-sources         数据源与 Schema Catalog(扫描、草稿编辑、发布) [admin]
/admin/system               依赖状态、指标、失败任务清单、死信队列       [admin]
*                           404
```

权限由 `app/auth.tsx` 的 `RequireAuth` / `RequireAdmin` 路由守卫实现;
analyst 访问 admin 路由重定向首页,未登录访问任何页面重定向 `/login`
并携带回跳地址。

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
| DiagnosisTask | Badge(执行中带呼吸点)+ 按钮可用性:pending/running 可取消;终态可重新诊断;failed 且无报告时 admin 可恢复 | 任务列表/详情 |
| TaskEvent | 时间线:步骤=空心蓝点,完成=绿勾,工具=灰点,失败=红叉,取消/重入队=橙点 | 任务详情-执行过程 |
| SSE 连接 | chip:实时连接(绿)/ 重连中(橙)/ 降级轮询(橙)/ 已结束(灰) | 执行过程卡头部 |
| 报告结论 | conclusive/probable 常规版式;inconclusive 专属版式:「已检查 / 仍缺少 / 下一步建议」三列,不显示"最可能原因" | 报告页 |
| ReportReview | 最新一条为当前有效反馈,历史倒序展示 | 报告页反馈区 |
| Message 生成 | 流式光标;interrupted 显示"生成已中止 · 已保留部分内容" | 知识助手 |
| Attachment/文档入库 | 已上传/处理中(轮询刷新)/可检索/处理失败(含原因,不伪装成功) | 知识库 |
| Catalog 版本 | draft(可编辑白名单)/ published / retired;未发布数据源标"不可用于 Text-to-SQL" | admin 数据源 |

## 服务端状态与 SSE 策略

- 查询统一走 TanStack Query;列表页用 `refetchInterval` 轮询兜底,详情页
  才建立 SSE。
- 任务详情的事件流:进入页面先补读历史(`afterSeq`),再持续接收;事件按
  `seq` 去重合并;收到状态变化类事件时 invalidate 任务与列表查询;真实实
  现使用 `EventSource` + `Last-Event-ID`,连接状态来自 onopen/onerror。
- 关闭页面只断开 SSE,不取消任务;取消走独立命令接口。
- 知识助手流式生成不经过任务队列;"停止生成"保留已有内容并标记
  interrupted。

## 认证流程

- Session Cookie 由浏览器自动携带;前端在内存持有 CSRF token(不落
  localStorage),非 GET 请求注入 `X-CSRF-Token`。
- 刷新后由 `GET /auth/me` 恢复用户与 token;失败落到 `/login`。
- `mustChangePassword=true` 时 `RequireAuth` 强制跳 `/change-password`,
  该页为独立布局(无工作台导航),只提供改密与退出。
- 改密成功即视为服务端撤销全部 Session:前端清空本地状态并要求重新登录。
- 全局 401 由 fetch 层清除内存 CSRF 和当前用户,路由守卫跳转登录并携带回跳地址。
- 后端当前尚未注册 `change-password`,因此临时密码账号仍不能完成真实改密闭环。

## Mock 层契约与替换步骤

`src/mocks/` 是尚未落地业务域的模拟落点(数据、诊断执行脚本、假 SSE、假流式生成)。
`src/shared/api/client.ts` 已统一处理响应信封、Cookie、CSRF、字段错误和 401。
后续按业务域渐进替换:

1. 后端完成一个业务域后,在 `src/shared/api/` 增加对应真实适配器;
2. 在 `src/shared/api/index.ts` 将该域导出从 Mock 切换到真实实现;
3. 任务事件接口落地时使用 EventSource + Last-Event-ID 替换假 SSE;
4. 全部业务域完成后删除 `src/mocks/`;
5. 用 openapi-typescript 生成的类型替换 `src/shared/api/types.ts`。

mock 保留了与 api.md 一致的错误语义(40101/40301/40401/40901/40921/
40923/42201),前端按 code 分支的行为在切换后无需改动。

## 已知未实现(原型边界)

- read_attachment 工具调用痕迹在助手消息中的展示;知识文档详情页(chunk
  预览、原图关联);失败文档的重新解析入口。
- 响应式断点(规范 834px 折叠导航等):当前为桌面布局。
- 真实附件上传(拖拽、进度、41301/41501 校验)、附件预览为占位渲染。
- 修改密码真实接口、OpenAPI 类型生成(等待后端 `api/openapi.yaml`)。
- mock 数据存于内存,整页刷新后运行期新建的数据会重置(预置演示数据保留)。

## 本地运行

```text
npm --prefix web run dev    # http://localhost:5173
认证依赖 http://127.0.0.1:9090;本地账号使用 mesguard-user 命令初始化
```
