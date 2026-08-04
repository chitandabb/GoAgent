# Mock 数据层(临时)

本目录是尚未落地业务接口的模拟数据落点,用于在后端逐步实现期间预览前端效果。
认证以及 M1 工单、诊断任务、SSE、正式报告、复核和管理员恢复接口已经连接真实
后端，不再使用本目录中的对应实现。旧诊断脚本仍留在 Mock 文件中供历史原型代码
参考，但不再从 `shared/api/index.ts` 导出，也不会驱动真实 M1 页面。
旧 `/assistant` Mock 页面也不再挂载；该路由已重定向到统一工作台。由于后端尚无
conversation/message 接口，工作台只在 `sessionStorage` 保存卷宗与 taskId 的导航关系。

- `data.ts` — 演示用户和未接入管理原型所需的静态数据;
- `scenario.ts` — 已停用的旧诊断演示脚本与报告模板;
- `api.ts` — 知识助手、知识库、用户/Catalog 等未接入域的模拟实现。

## 逐域替换步骤

1. 后端完成一个业务域后,在 `src/shared/api/` 增加对应真实适配器;
2. 在 `src/shared/api/index.ts` 删除该域的显式 Mock 导出并切换到真实实现;
3. 所有业务域完成后再删除整个 `src/mocks/` 目录;
4. OpenAPI 建立后,用生成类型替换 `src/shared/api/types.ts`。

业务组件只 import `@/shared/api`,不允许直接 import 本目录。
