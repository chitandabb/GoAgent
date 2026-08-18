# 当前状态与后续计划

MESGuard 当前处于“后端主链路可运行、前端真实联调已打通、继续做工程收口”的阶段。本文件只保留当前状态，不再记录每个历史切片的完整开发日志。

## 已完成

- Go 模块化单体、手动依赖注入、统一错误/请求 ID/结构化日志和优雅退出。
- Session Cookie、CSRF、角色权限、强制改密和管理员用户管理。
- 外部工单浏览、只读 SQL Server 数据源和不可变 Case Snapshot。
- 诊断任务创建、幂等、取消、重试、Outbox、RabbitMQ、Worker、报告、复核和 SSE。
- Conversation 持久化、异步 turn、流式 `turn_message_delta`、附件和引用预览。
- 知识文档上传、版本、解析任务、文本/Office/PDF 处理、Embedding、FTS/Vector/RRF 检索和知识引用。
- React 工作台、任务列表与工单历史、助手会话、知识库管理和前后端联调 Demo。
- Tool Profile、RunAccess、Resource Grant、Investigation Policy、QueryGuard、Evidence Gate 和评测记录。

## 当前边界

- 工具执行明细页保留真实空态，公开的工具执行接口尚未开放。
- 部分模型报告正文有内容，但结构化 `report_evidence` 声明仍可能为空；前端不会伪造证据。
- Web Search、OCR/VLM、Embedding、Rerank 依赖外部 Provider，未配置密钥时按降级路径运行。
- 评测记录来自固定本地数据集或受控 Smoke，不等同于生产 SLA、客户数据结果或线上质量承诺。
- Docker Compose 默认值只用于本地演示，生产环境必须替换密码、密钥、域名和存储配置。

## 后续优先级

1. 继续补齐真实基础设施集成测试和失败恢复场景。
2. 完善报告结构化证据声明与引用门禁的端到端落库。
3. 根据实际部署需求补齐观测、告警、数据清理和对象孤儿回收。
4. 在有明确产品需求时再开放工具执行明细和更复杂的运营管理接口。

## 事实来源

- API 当前实现：[`api/openapi.yaml`](../api/openapi.yaml)
- 开发运行：[`development.md`](development.md)
- 设计与决策：[`README.md`](README.md)
- 评测事实：[`evaluations/README.md`](evaluations/README.md)
