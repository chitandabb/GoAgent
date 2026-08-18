# Mock 数据层(临时)

本目录是尚未落地业务接口的模拟数据落点,用于在后端逐步实现期间预览前端效果。
认证、M1 工单、诊断任务、SSE、正式报告、复核、管理员恢复、AI 会话（含附件与
turn 事件流）、知识库上传/解析任务、任务列表和用户管理均已连接真实后端，不再
使用本目录中的对应实现。

当前仅剩 Schema Catalog / 管理端数据源域仍由 Mock 驱动（`/admin/data-sources`
页）；系统状态页不再用 Mock 冒充真实数据，保持显式空态。旧诊断脚本、知识助手
流式模板等历史原型保留在 Mock 文件中，但不再从 `shared/api/index.ts` 导出。

- `data.ts` — 演示数据源与 Catalog 原型所需的静态数据;
- `scenario.ts` — 已停用的旧诊断演示脚本与报告模板;
- `api.ts` — Catalog/数据源管理域的模拟实现与历史原型。

## 逐域替换步骤

1. 后端完成一个业务域后,在 `src/shared/api/` 增加对应真实适配器;
2. 在 `src/shared/api/index.ts` 删除该域的显式 Mock 导出并切换到真实实现;
3. 所有业务域完成后再删除整个 `src/mocks/` 目录;
4. OpenAPI 建立后,用生成类型替换 `src/shared/api/types.ts`。

业务组件只 import `@/shared/api`,不允许直接 import 本目录。
