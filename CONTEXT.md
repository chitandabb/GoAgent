# MESGuard Agent Runtime

MESGuard 以统一会话承接用户交互，并以独立诊断任务承接长耗时、可审计的故障调查。本词汇表只定义 Agent 编排与访问控制中的领域语言。

## Language

**Conversation Runtime**:
处理知识问答、只读数据查询、工单讨论和诊断命令的会话执行模式；它不拥有所创建诊断任务的生命周期。
_Avoid_: Knowledge Task, Chat Task

**Diagnosis Runtime**:
执行已冻结诊断任务的异步调查模式；它产出 Evidence 和结构化报告，不创建新的诊断任务。
_Avoid_: Diagnosis Conversation

**Tool Profile**:
某个部署内供一种 Runtime 稳定使用的模型可见 Tool 合同集合；用户、消息引用和依赖瞬时故障不会改变它。
_Avoid_: Dynamic Tool Scope, Capability Set

**Permission**:
一次运行被允许执行的操作种类，例如读取知识、只读查询 SQL 或创建诊断任务。
_Avoid_: Capability

**Resource Grant**:
一次运行可访问的具体资源集合，例如数据源、工单、附件、任务或代码仓库。
_Avoid_: Scope

**Investigation Policy**:
诊断任务创建时冻结并持久化的权限上限；后续执行只能在该上限内收窄。
_Avoid_: Task Capability

**Run Access**:
某一次 Agent 运行的有效 Permission 与 Resource Grant 快照；它是执行期 Guard 的唯一授权输入。
_Avoid_: TaskScope

**Skill**:
供 Diagnosis Runtime 按需读取的调查 SOP，描述步骤、证据标准和停止条件，但不授予 Tool 权限。
_Avoid_: Tool Bundle, Permission Template
