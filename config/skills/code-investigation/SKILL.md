---
name: code-investigation
description: 在授权 GitHub 仓库内检索代码、文件与提交，形成可追溯的只读代码证据
---

# 代码调查

## 目标

根据工单或诊断阶段提供的检索锚点，在当前 GitHub MCP 凭据允许访问的仓库中定位相关代码和变更。所有操作只读，不创建分支、提交、Issue 或 Pull Request。

## 执行流程

1. 先用 `search_code` 根据错误文本、标识符、模块名或业务字段定位候选文件。
2. 只对有价值的候选调用 `get_file_contents`，避免无边界读取仓库。
3. 只有需要解释版本变化时才调用 `list_commits` 和 `get_commit` 追溯提交。
4. 证据不足、仓库不可访问或 GitHub MCP 不可用时，明确说明限制，不虚构代码结论。

## 深入规则

需要确认代码证据的最小引用格式和结论边界时，调用 `read_skill_reference` 读取 `references/code-evidence.md`。不要在普通搜索阶段预先加载。

## 输出要求

- 每条代码结论至少引用仓库、Commit SHA、文件路径和相关符号或行段。
- 区分“代码中存在”“提交中发生变化”和“可能导致故障”三类结论。
- 不把搜索命中数量当作根因证据，不声称已经修改或验证运行环境。
