# MESGuard 文档

根目录 [README](../README.md) 负责项目定位、启动方式和仓库结构；本目录只保留工程设计、开发运行、决策记录和评测事实。

## 推荐阅读顺序

1. [开发指南](development.md)：本地依赖、Docker、API、前端和测试。
2. [系统架构](design/system-architecture.md)：服务角色、数据流和故障边界。
3. [Agent 编排与工具治理](design/agent-orchestration.md)：Runtime、Tool Profile、RunAccess 和 Skill。
4. [API 与 SSE 契约](design/api.md)：HTTP 资源、认证、幂等、错误和事件流。
5. [数据库设计](design/database.md)：核心表、版本、事务和历史事实。
6. [知识入库与检索](design/rag-ingestion-and-retrieval.md)：解析、切块、向量召回和引用。

## 工程设计

- [产品与工作流](design/product-and-workflow.md)
- [领域与状态机](design/domain-and-state-machine.md)
- [代码组织](design/code-organization.md)
- [前端设计](design/frontend.md)
- [消息与异步任务](design/messaging.md)
- [诊断工具与安全边界](design/diagnostic-tools.md)
- [Agent 可观测性](design/agent-observability.md)
- [上下文治理与记忆](design/context-governance-and-memory.md)

## 架构决策

- [ADR 001：模块化单体与手动依赖注入](decisions/001-modular-monolith-architecture.md)
- [ADR 002：优先使用成熟开源组件](decisions/002-prefer-open-source-components.md)
- [ADR 003：本地 ONNX 布局路由](decisions/003-local-onnx-layout-routing.md)
- [ADR 004：会话驱动的诊断任务命令](decisions/004-conversation-driven-diagnosis-commands.md)
- [ADR 005：统一 Agent Runtime 与稳定 Tool Profile](decisions/005-unified-agent-runtime-and-stable-tool-profiles.md)

## 评测与证据

[评测索引](evaluations/README.md)说明每类记录的范围、当前有效性和固定集限制。评测文件只记录可复核的方法与结果，不代表生产承诺。

前后端运行验收记录位于 [`testing/`](testing/)；最新的统一助手、知识问答和排查任务链路见 [2026-08-22 全链路验收](testing/full-chain-acceptance-2026-08-22.md)。

## 文档规则

- 公开文档只描述当前实现；历史方案必须明确标为历史或已废弃。
- 运行命令只维护在 [development.md](development.md)，避免多个文档复制不同版本。
- API 的机器可读事实只维护在 [`api/openapi.yaml`](../api/openapi.yaml)。
- 不在仓库文档中保存密码、API Key、个人简历和联系方式。
- 合成测试数据可以提交；真实 ERP、客户、附件和 Provider 响应不得提交。
