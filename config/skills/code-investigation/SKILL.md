---
name: code-investigation
description: 在授权 GitHub 仓库内检索代码、文件与提交，形成可追溯的只读代码证据
---

# 代码调查

## 目标

根据工单或诊断阶段提供的检索锚点，在当前 GitHub MCP 凭据允许访问的仓库中定位相关代码和变更。所有操作只读，不创建分支、提交、Issue 或 Pull Request。

## 执行流程

1. 不知道目标仓库时，先用 `search_repositories` 按仓库名、语言、组织或业务关键词发现候选；不要假设固定 owner、repository 或分支。
2. 选定仓库后，用 `search_code` 根据错误文本、标识符、模块名或业务字段定位候选文件；搜索结果必须保留实际 repository 和 sha。若结果包含 `status=index_pending` 或 `incomplete_results=true`，统一视为 GitHub Code Search 的上游不完整/降级响应：它可能表示查询超过时间限制或结果不完整，不是“索引尚未完成”的权威状态，也不表示没有匹配代码；已知路径或提交时改用文件/提交读取，并在结论中说明限制。
3. `search_code` 不完整且仓库已选定时，先用 `get_repository_tree` 获取候选路径：优先传入已知 `tree_sha`，用 `path_filter` 限定业务目录，只在范围较窄时使用 `recursive=true`，并分别检查 `upstream_truncated`、`candidate_limit_reached`、`filtered_count` 和 `omitted_count`。MESGuard 会将正常树结果整理为 `status=candidate_paths`，只保留 `type=blob` 文件、排除常见构建/依赖目录并限制候选数量；树结果只是文件清单，不是代码内容或根因证据，仍需优先选择目标语言和业务路径相关的少量文件。
4. 只对有价值的候选调用 `get_file_contents`，按需传入实际 ref 或 sha，避免无边界读取仓库。文件读取后必须以返回或追溯得到的 Commit SHA 固定证据版本。
5. 只有需要解释版本变化时才调用 `list_commits` 和 `get_commit` 追溯提交；分支/标签可作为历史查询参数，最终证据固定为 Commit SHA。
6. 证据不足、仓库不在当前 GitHub 凭据可见范围或 GitHub MCP 不可用时，明确说明限制，不虚构代码结论。

## 深入规则

需要确认代码证据的最小引用格式和结论边界时，调用 `read_skill_reference` 读取 `references/code-evidence.md`。不要在普通搜索阶段预先加载。

## 输出要求

- 每条代码结论至少引用仓库、Commit SHA、文件路径和相关符号或行段。
- 区分“代码中存在”“提交中发生变化”和“可能导致故障”三类结论。
- 不把搜索命中数量当作根因证据，不声称已经修改或验证运行环境。
- `get_repository_tree` 的目录清单用于缩小读取范围；如果返回 `upstream_truncated=true` 或 `candidate_limit_reached=true`，或者候选仍过多，继续缩小 `path_filter`，不要把整棵树或所有候选文件读入上下文。`filtered_count` 只表示应用主动排除的目录/条目，不等价于上游截断。
