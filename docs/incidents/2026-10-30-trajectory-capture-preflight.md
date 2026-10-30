# Incident 2026-10-30 — trajectory capture blocked at preflight

## Status

FAIL — 生产轨迹采集与 RL 在 v2.5.0 预检阶段被阻断。合成轨迹工具链不受此阻断影响。

## Observed Evidence

- 无 v2.5.0 代码线可比：`version-matrix.json` 平台 2.4.6，`validate.mjs:39` 硬断言 2.4.6；九门禁 `Compat=blocked`，`healthz` 被 v2.3.45 发布显式排除 → 没有匹配基线，"轨迹让模型变好"无从判定。
- 轨迹原料所在端点正是隐私评审的失败面：主体取自 query（F-03/F-04）、`messages`/`im_messages` 不在 `erasureSources`（F-07）、无保留期自动化（F-08）、静态令牌不注入主体（F-02）。
- 记录必然触碰 `users.identifier`（F-01：同时是登录凭据），其空间为 12 位数字 + Luhn（约 10^10），裸哈希可离线反查。

## Failure Classification

- 主类：**数据契约 + 隐私/合规**。次类：许可链未建立（MIT 只覆盖代码，不覆盖会话内容）。
- 不是 GPU、loss、吞吐或调度问题：本轮未运行任何训练，不存在可归因的优化失败。

## Next Minimal Test

- 保持固定：训练代码、发布门禁、后端配置均不改动；本轮只读。
- 唯一变更：新增 `docs/contracts/` 两份契约与一份 fixture（不接入 `validate.mjs`）。
- 测量：`AC-25` 表覆盖契约测试的差集大小；`AC-12` "query 取主体" 的命中数；契约负控通过数（记录 10/10、manifest 2/2）。
- 各解释的预测：若 F-03/F-07 未修 → 差集非空且 AC-12 命中 > 0；若已修 → 两者均为 0，且负控仍全部拒绝。
- 停止条件：差集 = 0 且 AC-12 = 0 之前，不开启任何采集；负控出现假阴性时先修契约，不修数据。

## Stop Condition

- 命中任一硬红线（PII/凭证、跨主体泄漏、标签掩码错误、clean-load 失败）立即停止，不"先跑完再分析"。
- 采集前置未满足时，不得以"先小规模采一点"绕过门禁。

## Artifacts to Preserve

- 本事故记录、`docs/design/IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md`、两份 schema、fixture，以及负控输出 `ALL CONTRACT CHECKS PASS`。
- 隐私评审 F-01..F-14 与 AC-01..AC-54 清单（决定修复范围的唯一依据）。
- 九门禁快照：分支 `release/2.4`，HEAD `5975ebe`，`release-manifest.json` 原样。

## Risks and Limitations

- 隐私结论来自静态分析（评审 §11 自述），生产实例未做动态验证；本记录不能证明生产已修复，也不能证明未修复。
- 许可条款未逐项核验，需 license ledger 补齐后才能进入 P2。
- 阈值为建议值，未在真实 GPU 上校准；所有质量结论当前均为 `UNVERIFIED`。
