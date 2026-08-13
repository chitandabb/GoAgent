# Evidence Gate Early Exit 成对评测 v1

## 评测目的

该固定集验证一个窄变量：同一诊断 Case 在关闭和开启 Early Exit 时，现有 Evidence Gate 是否能在证据充分后少执行调查，同时不降低完成率、结论正确率和引用/证据正确率。它不引入另一套停止状态机，也不把模型的“我已经完成”当作最终裁决。

Baseline 仅设置 `DisableEarlyExit=true`；Experiment 使用生产默认值。模型 Profile、Prompt、Runner 模式、Tool 合同、Agent/Tool/Token/超时预算和 Case 必须由同一个 `pairingFingerprint` 绑定。任一控制项不一致时拒绝配对，而不是把差异计入 Early Exit 收益。

## Provider-free 固定集

当前 `evidence-gate-early-exit-v1` 有 3 个 reviewed Fixture Case：

| Case | 人工标注的证据充分轮次 | 目的 |
| --- | ---: | --- |
| `ticket-evidence-sufficient-first-run` | 1 | 验证 Experiment 首轮通过门禁后结束，Baseline 继续到原运行预算 |
| `knowledge-gap-repaired-second-run` | 2 | 验证证据不足时两个 arm 都继续补充调查 |
| `dependency-missing-remains-insufficient` | 0 | 验证预算结束后两个 arm 都返回显式 partial，不伪造结论 |

距离规格中的 30 个 reviewed Case 目标还差 27 个。三个 Case 是确定性脚本数据，只用于验证开关、评分、质量门禁、问题明细和 Ledger 集成；其中的耗时与 Token 是 Fixture 计量，不能解释为 StepFun、DeepSeek 或其他真实 Provider 的性能。

零 Provider 重放命令：

```powershell
go run ./tools/evaluation/mesguard-evaluation-ledger `
  -inventory config/evaluation-assets-v1.json `
  -asset evidence-gate-early-exit-v1 `
  -output <new-ledger-output.json> `
  -model-profile fixture-provider-free `
  -config-fingerprint sha256:<64-hex-config-fingerprint> `
  -implementation-revision git:<revision>
```

输出文件必须不存在，避免覆盖历史 Observation 或报告。本次版本化结果位于 `testdata/evidence-gate-early-exit-v1.ledger-v1.json`。

## 当前确定性结果

| 指标 | Baseline | Experiment | 变化 |
| --- | ---: | ---: | ---: |
| Case 完成率 | 2/3 | 2/3 | 不变 |
| reviewed 结论正确率 | 3/3 | 3/3 | 不变 |
| reviewed 引用/证据正确率 | 3/3 | 3/3 | 不变 |
| High Severity 错误结论 | 0 | 0 | 不变 |
| Agent Runs | 6 | 5 | -16.67% |
| 模型调用 | 12 | 10 | -16.67% |
| Tool Calls | 6 | 5 | -16.67% |
| 总 Token | 2700 | 2300 | -14.81% |
| P50 Fixture 延迟 | 800 ms | 700 ms | -12.50% |
| P95 Fixture 延迟 | 900 ms | 800 ms | -11.11% |

`dependency-missing-remains-insufficient` 在两个 arm 中都单独记录 `evidence_gate_partial` 和 `evidence_insufficient_after_budget`。质量门禁要求所有 Observation 都完成人工复核、Experiment 的完成/结论/引用指标不低于 Baseline，且不新增 High Severity 错误结论；任一条件不满足时，报告中的性能收益字段保持为零，回归 Case 单独列出。

以上百分比不是简历指标。真实模型的 Tool 循环、缓存命中、网络抖动和生成长度都可能改变收益，必须在同模型同配置的 reviewed Provider 固定集上重测。

## 真实 Provider 复测防线

真实成对运行沿用现有 `mesguard-agent-paired-eval`，但必须显式选择 Evidence Gate 变量并授权三类上限：

```powershell
go run ./tools/evaluation/mesguard-agent-paired-eval `
  -comparison evidence-gate `
  -dataset <reviewed-diagnosis-cases.jsonl> `
  -output <new-raw-observations.jsonl> `
  -allow-provider-calls `
  -max-cases <N> `
  -max-provider-calls <upper-bound> `
  -max-provider-tokens <upper-bound>
```

命令在创建模型客户端前计算并输出 Case 数、Provider 调用保守上界和双 arm Token 总预算上界；任何上限不足都会快速失败。单个 Provider 错误不会增加样本数、Agent Runs、Tool Calls 或 Token 预算。

原始 Provider Observation 默认 `qualityReviewed=false`，因此不能通过质量门禁。人工需要根据 Gold 标注 `evidenceSufficientAtRun`，并逐 arm 复核 `completed`、`conclusionCorrect`、`citationCorrect` 和 `highSeverityWrongConclusion`，再生成新的版本化 Ledger。Fixture 与真实 Provider 结果必须使用不同数据集版本，不能覆盖本文件中的固定结果。
