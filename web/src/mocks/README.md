# Mock 数据层(临时)

本目录是尚未落地业务接口的模拟数据落点,用于在后端逐步实现期间预览前端效果。
认证接口已经由 `src/shared/api/auth.ts` 连接真实后端,不再使用本目录中的认证实现。

- `data.ts` — 演示用的工单、任务、用户、依赖状态等静态数据;
- `scenario.ts` — 诊断执行脚本(模拟 Worker 的事件序列)与报告模板;
- `api.ts` — 模拟 API 实现:内存存储、假延迟、模拟 SSE 事件推送。

## 逐域替换步骤

1. 后端完成一个业务域后,在 `src/shared/api/` 增加对应真实适配器;
2. 在 `src/shared/api/index.ts` 将该域的导出从 Mock 切换到真实实现;
3. 所有业务域完成后再删除整个 `src/mocks/` 目录;
4. OpenAPI 建立后,用生成类型替换 `src/shared/api/types.ts`。

业务组件只 import `@/shared/api`,不允许直接 import 本目录。
