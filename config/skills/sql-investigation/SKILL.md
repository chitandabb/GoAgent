---
name: sql-investigation
description: 在授权 SQL Server 数据源中检索 Schema Catalog、执行单条只读 T-SQL 并整理可追溯的数据库证据
---

# SQL 调查

## 目标

根据工单中的错误码、模块、存储过程或业务对象线索，读取管理员允许的 SQL Server 对象定义、检索已发布 Schema Catalog 并执行单条只读 T-SQL，辅助判断业务流转和核对实时业务数据。当前能力严格只读，不执行任意 SQL，不修改生产库或产品库。

## 执行流程

1. 先确认工单证据中存在明确的 schema、对象名、模块或错误线索；没有锚点时先报告证据缺口。
2. 不确定对象或字段名称时，先调用 `search_schema_catalog` 检索管理员发布的 queryable 条目；关键词只能是业务词、表名、字段名或语义描述，不得传 SQL 片段。
3. 需要核对存储过程、视图或函数实现时，调用 `get_database_object_definition`，只传简单的 `schema` 和 `objectName`。
4. 需要核对实时业务数据、记录或聚合时，根据用户请求生成单条只读 T-SQL，并用 `execute_readonly_query` 执行；只读账号、QueryGuard、已发布 Schema Catalog、超时、行数/字节数和并发限制由后端执行器强制，Agent 不接触连接或凭证。
5. 把工具返回的 Catalog 条目和查询结果当作数据库事实，区分“定义中明确存在”“根据定义推断”和“仍需运行统计或日志验证”。
6. 不得生成或调用 `sp_helptext`、`xp_cmdshell`、动态 SQL、跨库查询、DDL、DML 或任意系统过程；执行器未授权时不要换措辞重试或换库绕过。
7. 生产库只能使用只读能力；产品库/LAB 的写入实验属于未来独立流程，当前不可调用。

## 停止条件

- 数据源未授权、查询被 QueryGuard 拒绝、超时或返回零行时立即停止，报告证据缺口，绝不编造结果。
- 单条查询已返回足够证据时立即停止，不要追加无关查询或扩大结果集。
- 任务只需要元数据或对象定义时，不要执行数据查询。

## 深入规则

需要判断对象定义如何支持结论时，调用 `read_skill_reference` 读取 `references/evidence-rules.md`。不要为了获得更多上下文而一次性读取全部参考资料。

## 输出要求

- 说明数据源环境、schema、对象名和对象类型。
- 对截断结果明确标注，不能声称看到了未返回的部分。
- 结论引用返回结果中的关键过程、表、条件或数值；引用 `execute_readonly_query` 或 `search_schema_catalog` 返回结果时，如结果包含后端 `citationSources` 的 `marker`，逐字复制到支持的主张之后。
- SQL Server 不可用或对象不存在时输出 `inconclusive`，并保留限制说明。
