# Mock 数据层(临时)

本目录是**唯一**的模拟数据落点,用于在后端就绪前预览前端效果。

- `data.ts` — 演示用的工单、任务、用户、依赖状态等静态数据;
- `scenario.ts` — 诊断执行脚本(模拟 Worker 的事件序列)与报告模板;
- `api.ts` — 模拟 API 实现:内存存储、假延迟、模拟 SSE 事件推送。

## 接入真实后端后的删除步骤

1. 删除整个 `src/mocks/` 目录;
2. 把 `src/shared/api/index.ts` 中对 `@/mocks/api` 的转发替换为基于
   fetch 的真实实现(统一信封解包、CSRF 注入、SSE 用 EventSource);
3. 打开 `vite.config.ts` 中注释掉的 `/api` 代理配置;
4. 用 openapi-typescript 生成的类型替换 `src/shared/api/types.ts`。

业务组件只 import `@/shared/api`,不允许直接 import 本目录。
