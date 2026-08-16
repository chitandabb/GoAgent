# Tool Selection v4 正式评测

## 目标与合同

`mesguard-tool-selection-eval` 比较固定 `evaluation-wide-v2` 与生产 `diagnosis-default` Tool Profile。两臂共用相同模型、消息、生产 system instruction、入口 Skill、`task_context` 与生成参数，唯一对照变量是最终模型可见 Tool Profile/Schema。

当前正式合同为 `tool-selection-observation-v4`。OpenCode Go `deepseek-v4-flash` 不接受当前 OpenAI 兼容请求中的 `tool_choice=required`，而省略该字段时可正常自主选择 Tool；因此本次正式运行显式记录 `toolChoiceMode=absent`。它不修改生产 Runner，因为生产 Conversation/Diagnosis 本来就不强制 Tool Choice。

## 2026-08-16 OpenCode Go 45 Case 正式运行

运行身份与资产：

- clean revision：`7c4f5d6`；
- Provider/Profile：OpenCode Go `opencode-deepseek-main`；
- 模型：`deepseek-v4-flash`；
- Tool Choice：`absent`；
- wide / production Profile：`evaluation-wide-v2` / `diagnosis-default`；
- 固定集：`tool-selection-v1` 全部 45 Case；
- 原始观测：`output/evaluation/tool-selection-opencode-v4-45.observations.jsonl`；
- 汇总：`output/evaluation/tool-selection-opencode-v4-45.summary.json`。

在 revision `7c4f5d6` 的 clean checkout 中，配置好 OpenCode Go 与 GitHub MCP 所需环境变量后，可用以下脱敏命令复现；17,694,720 是 `45 × 3 × 131072` 的 fail-closed Token 硬上界，不是预计消耗：

```powershell
go run ./tools/evaluation/mesguard-tool-selection-eval `
  -profile opencode-deepseek-main -tool-choice-mode absent -concurrency 4 `
  -allow-provider-calls -max-cases 45 -max-provider-calls 135 -max-provider-tokens 17694720 `
  -output output/evaluation/tool-selection-opencode-v4-45.observations.jsonl `
  -summary output/evaluation/tool-selection-opencode-v4-45.summary.json
```

| 指标 | wide | production |
| --- | ---: | ---: |
| 正确 | 44 / 45（97.78%） | 44 / 45（97.78%） |
| 非法选择率 | 0 | 2.22% |
| 越权选择率 | 0 | 0 |
| 失败运行 | 0 | 1 |
| Prompt Token | 178,105 | 166,993 |
| Completion Token | 10,827 | 9,830 |
| Total Token | 188,932 | 176,823 |
| 平均耗时 | 3.704 秒 | 3.559 秒 |

44 个 Case 形成有效配对，2 条观测未配对。production 相对 wide 的 paired 降幅为：Prompt Token 4.12%、Tool Schema Token 4.44%、Schema bytes 4.64%。这些是固定评测集上的 paired 结果，不代表生产流量准确率或延迟收益。

Provider accounting 共记录 135 次 Generate 尝试，其中 134 次返回 Usage、1 次缺失 Usage；累计 Prompt 357,828、Completion 20,702、Total 378,530、Cached 319,104、Reasoning 13,718 Token。Cached Token 占 Prompt Token 约 89.2%，只按 Provider 返回的 Usage 口径计算，不估算价格。

两处异常分别是：

- `github-repository-04 / production` 返回一次 `provider_server_error`，没有 Tool Call 和 Usage；对应 wide 臂正确选择 `search_repositories`。这是 Provider 5xx 失败，不计作模型 Tool 选择质量错误；
- `github-code-02 / wide` 选择 `skill`，而固定集期望 `search_code`；对应 production 臂正确选择 `search_code`。该观测构成 wide 臂唯一的选择错误。

因此可引用结论是：OpenCode Go 在 v4/absent 合同下完成 45 Case 正式运行，两臂各 44/45 正确、越权率均为 0，production 在 44 个可配对 Case 上减少约 4.12% Prompt Token；同时保留一次 Provider 5xx 和一次 wide Tool 误选，不宣称 100% 稳定或 100% 准确。
