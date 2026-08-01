---
name: sql-investigation
description: 在授权 SQL Server 数据源中读取对象定义并整理可追溯的数据库证据
---

# SQL 调查

## 目标

根据工单中的错误码、模块、存储过程或业务对象线索，读取管理员允许的 SQL Server 对象定义，辅助判断业务流转。当前能力严格只读，不执行任意 SQL，不修改生产库或产品库。

## 执行流程

1. 先确认工单证据中存在明确的 schema、对象名、模块或错误线索；没有锚点时先报告证据缺口。
2. 不确定对象或字段名称时，先调用 `search_schema_catalog` 检索管理员发布的 queryable 条目；关键词只能是业务词、表名、字段名或语义描述，不得传 SQL 片段。
3. 需要核对存储过程、视图或函数实现时，调用 `get_database_object_definition`，只传简单的 `schema` 和 `objectName`。
4. 把工具返回的定义和 Catalog 条目当作数据库事实，区分“定义中明确存在”“根据定义推断”和“仍需运行统计或日志验证”。
5. 不得生成或调用 `sp_helptext`、`xp_cmdshell`、动态 SQL、跨库查询或任意系统过程。
6. 生产库只能使用只读能力；产品库/LAB 的写入实验属于未来独立流程，当前不可调用。

## 深入规则

需要判断对象定义如何支持结论时，调用 `read_skill_reference` 读取 `references/evidence-rules.md`。不要为了获得更多上下文而一次性读取全部参考资料。

## 输出要求

- 说明数据源环境、schema、对象名和对象类型。
- 对截断定义明确标注，不能声称看到了未返回的部分。
- 结论引用对象定义中的关键过程、表或条件，并说明需要哪些执行日志、Query Store 或业务数据来验证。
- SQL Server 不可用或对象不存在时输出 `inconclusive`，并保留限制说明。
