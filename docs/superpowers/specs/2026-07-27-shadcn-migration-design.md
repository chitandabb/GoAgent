# 前端组件库迁移设计:shadcn/ui + Apple 设计语言

- 日期:2026-07-27
- 状态:已批准(待实施)
- 关联文档:[ADR 002](../../decisions/002-prefer-open-source-components.md)、
  [frontend.md](../../design/frontend.md)、[DESIGN-apple.md](../../design/DESIGN-apple.md)

## 目标

把 `web/src/shared/ui/` 的手写组件层整体迁移到 shadcn/ui 体系:
库负责行为与可访问性(焦点管理、键盘导航、ARIA、Portal),
DESIGN-apple.md 的 token 负责全部外观。一次性迁移,不留双轨。

## 决策

### 选型:shadcn/ui(Tailwind v4 模式)

- 组件源码经 CLI 拷入仓库,可完全定制,不是 npm 黑盒;
- 底层为 Radix 原语 + `class-variance-authority` + `clsx` + `tailwind-merge`;
- 图标用 `lucide-react`;Toast 用 `sonner`;
- 表格用 TanStack Table(headless)+ shadcn Table 外观;
- React 19 / Vite / React Router v7 / TanStack Query 均不变。

否决的替代方案:

- **Ant Design / MUI 全家桶**:设计语言强势(自有阴影体系、密度、字阶、
  品牌色),与 DESIGN-apple.md 的核心特质(单一交互色、零卡片阴影、
  pill 只给操作元素、字重 500 缺席)正面冲突,主题定制成本高且上限低。
- **Radix / Base UI 裸用**:每个组件的样式骨架都要手搭,工作量介于现状
  与 shadcn 之间,无额外收益。

### 迁移范围:整体迁移

修订 ADR 002 前端段的"存量不迁移"条款。理由:渐进双轨的维护成本
高于一次性切换;切换后仓库内只有一套组件体系。

## 主题策略(保住 Apple 风格的关键)

shadcn 外观由 CSS 变量契约(`--primary`、`--radius`、`--card` 等)驱动。
在 `src/styles/tokens.css` 中把这组变量映射到现有 Apple token,
**不使用 shadcn 默认主题**:

- `--primary` = `#0066cc`;交互色只有这一个蓝;
- 卡片:白底 + 1px hairline + 18px 圆角 + 零阴影;
- 按钮:pill 圆角,按压态 `scale(0.95)`;
- 字重阶梯 300/400/600;shadcn 默认使用 `font-medium`(500)处统一改为
  400 或 600(DESIGN-apple.md 明确 500 缺席);
- 密度双档:表格/表单/列表 14px,报告正文与工单描述等阅读态 17px;
- 状态色 ok/warn/danger 仅表达状态语义,不作交互色。

不变量:`tokens.css` 仍是唯一 token 落点;组件内禁止裸 hex。

## 组件迁移映射

| 现有(shared/ui) | 去向 |
| --- | --- |
| Button | shadcn Button(pill 变体为默认) |
| Badge | shadcn Badge |
| Card | shadcn Card |
| Dialog | shadcn Dialog(Radix 焦点陷阱) |
| Field | shadcn Label + Input + Select + Textarea 组合 |
| Spinner | 保留手写(shadcn 无对应;内部实现改用 lucide `Loader2` 旋转图标) |
| Toast | sonner |
| DataTable | TanStack Table + shadcn Table |
| Chips | shadcn ToggleGroup / Badge 变体 |
| PageHeader | 保留手写(纯展示型,无对应物) |
| EmptyState | 保留手写 |
| Wordmark | 保留手写 |
| AttachmentPreview | 保留手写 |

## 架构不变量

- `shared/ui` 仍是页面唯一的组件 import 边界;shadcn 生成的组件落在
  `shared/ui` 下(调整 CLI 的 components 输出路径)。
- 页面层只改 import 与 props 适配;信息架构、路由、mock 层、
  TanStack Query 逻辑全部不动。
- feature 之间不互相 import;业务组件只从 `@/shared/api` 取数据。

## 文档同步

- ADR 002:删除前端段"存量不迁移"条款,记录本次整体迁移决策与理由;
- frontend.md:更新技术选型表(组件行)与设计语言落地决策。

## 验收标准

1. `npm --prefix web run build`(tsc + vite)通过;
2. dev server 逐页走查 12 个页面(9 个工作台页 + 3 个 admin 页),
   外观符合 DESIGN-apple.md:单蓝交互色、无卡片阴影、pill 语法、
   字重阶梯、双档密度;
3. Dialog / Select / Toast 键盘可达(Tab 循环、Esc 关闭、焦点返回),
   由 Radix / sonner 行为保证并抽查验证;
4. 仓库中不残留被替换组件的死代码。

## 风险与对策

- **shadcn 默认样式渗漏**(默认阴影、`font-medium`、默认圆角):
  初始化后逐组件过一遍 class,替换为 token 类;验收第 2 条兜底。
- **React 19 + Tailwind v4 兼容**:shadcn 官方已支持该组合;若个别
  组件有兼容问题,以 Radix 原语 + 自写样式为兜底(仍符合 ADR 002)。
- **Chips 交互语义差异**:现有 Chips 同时承担筛选与展示;迁移时按
  用途分流(筛选 → ToggleGroup,展示 → Badge),避免硬套。
