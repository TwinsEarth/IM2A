# Gate Record — v2.5.0 / synthetic approval-trajectory SFT

Date: 2026-10-30 · Baseline: branch `release/2.4`, HEAD `5975ebe`, platform 2.4.6 / Go 2.3.26
Contract: `docs/design/IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md` · ADR 0005

## Behavior Target and Non-Goals

- **Target**：在冻结的合成审批场景上，模型在执行任何写操作前先申请审批（`REQUIRE_APPROVAL`），被 `DENY` 后不重试同一 Skill，并保持 `source/degraded/reason` 的降级诚实语义。
- **Non-goals**：不做 RL；不接触生产或用户数据；本门禁不产生任何"模型变好"的质量声明；不晋级到回复路径。
- **Why this target**：IM2A 的失败模式是"越权执行"与"假成功"，不是"答得不够聪明"；先修行为边界，再谈能力。

## Fixed Comparator Contract

| 项 | 值 |
|---|---|
| 基座 checkpoint | `<base-checkpoint-sha256>`（运行时填入，不得留空） |
| 平台 / 网关 | 2.4.6 / 2.3.26 |
| 提示模板 | `templateHash = <sha256:...>`（与记录 `source.templateHash` 同源） |
| 解码 | temperature 0 / top_p 1 / max_tokens 1024 |
| 评估器 | `eval-harness-0.1.0` + `promptSetHash = <sha256:...>` |
| 评估集 | `im2a-synth-gate-1@0.1.0`，manifest `sha256:<...>` |
| GPU 包络 | 1 × 24GB，≤ 4 GPU·h |
| 预算 | `costUsdMax = <approved>`；无上界估计即拒绝提交 |
| 种子 | 20261030 |

**基线定义**：同一 checkpoint、同一解码、同一冻结评估集、无适配器。任何一项变化 → 重新跑基线，旧基线不得复用。

## Gate: Preflight

| 检查 | 当前状态 | 证据 |
|---|---|---|
| 记录契约草案 + 哈希 | **PASS** | `docs/contracts/trajectory-record.schema.json` |
| 数据集清单契约草案 + 哈希 | **PASS** | `docs/contracts/trajectory-dataset-manifest.schema.json` |
| 采集作业契约草案 + 哈希 | **PASS**（显式激活） | `docs/contracts/trajectory-collection-job.schema.json` |
| 正例 + 负控 | **PASS**：记录 22/22、manifest 9/9 | `node scripts/check-trajectory-contract.mjs` |
| 跨字段语义负控 | **PASS**：跨主体 split、凭证计数、contentHash、保留期均被拒 | 同上 |
| 脱敏策略与扫描 | **PARTIAL**：策略 + 扫描器，仅合成样本；`freeText` 仍需人工复核 | `node scripts/check-trajectory-pipeline.mjs`（36/36） |
| 主体级切分纯净度 | **PARTIAL**：检查器可拒泄漏；尚未在真实数据集上运行 | 同上 |
| 5k 合成轨迹 | NOT STARTED | — |
| 本地 `TrainingBackend` | **PARTIAL**：契约 + 注册表 + fake；无 GPU、fake 不过 clean-load | `node scripts/check-training-backend.mjs`（28/28） |
| 回滚演练 | NOT STARTED | — |
| 预算批准 | NOT STARTED | — |

**门禁状态：UNVERIFIED（契约与工具链部分通过；无训练、无真实数据）**。契约层可执行，管线层仅合成可执行；不得进入 smoke。

## Single Change Under Test

只新增"契约 + 脱敏 + 合成生成 + 本地后端 + 评估 harness"，**不改模型权重、不改提示模板、不改解码**。

选择理由（最弱够用方法）：本门禁要证明的是"数据契约能被执行"，不是"模型会变好"。用 SFT 训练来验证 schema 是过度方法；用真实数据验证合成管线则顺序错误。

## Required Measurements

| 测量 | 方法 | 阈值 | 证据物 |
|---|---|---|---|
| schema 通过率 | 契约校验器逐条 | 100% | 校验输出 |
| deny-list 命中 | 正则 + 分类器 | 0 | 扫描报告 |
| 掩码违规 | 逐 token 检查 labels | 0（`system/human/tool/gate/approval` 不得有 loss） | tokenized 样例 |
| 切分纯净度 | `subjectRef` 交集 | 0 | 切分报告 |
| 去重 | 精确 + MinHash | 跨 split 精确 0；近重 < 0.8 | 去重报告 |
| manifest 完整性 | 分片哈希复算 | 100% | manifest |
| 人工复核 | 5 条逐条读 | 无个体可识别 | 复核记录 |
| save/load | 保存后 clean-load | 通过 | 探针日志 |
| eval 空跑 | 冻结评估集 | 跑通且指标非空 | 评测输出 |
| 成本 | `estimate()` | ≤ 预算上界 | 估算记录 |

## Promotion or Stop Decision

- **Promote to smoke** 仅当上表全部 PASS，且回滚演练与预算批准两项完成。
- **Stop** 条件：deny-list 命中、掩码违规、切分交集非空、manifest 校验失败、load 失败、估算超预算 —— 任一命中即停，先归因再改动。
- 本门禁**不能**晋级模型，只能晋级管线；模型晋级必须走 `signal` 与 `controlled`。

## Preserved Evidence

```text
docs/contracts/trajectory-record.schema.json            sha256:cc34320d0dbbf0e15e0951d36a7171763bb823dcbba12896b46a0f40cfb70ebe
docs/contracts/trajectory-dataset-manifest.schema.json  sha256:4cd01c236b6625135162b8ac68efec53eeccffb63cf293f59dd41a8265ccade6
docs/contracts/trajectory-collection-job.schema.json    sha256:c02062dc6b04c1a3bc8afeaa1d3d4867bd7bb27f9ff192fe9eb93b4f357304ce
docs/contracts/redaction-policy-1.0.json                sha256:92bda4f3d3e0b32d289eebc5e6c6b1a68498eddba2f317d7a202467302cf632e
docs/contracts/capability-defaults-1.0.json             sha256:b28113e02b87c7889f0fd8112f84575d7e89fd01946c49bf8cb1163018baaa46
docs/contracts/eval-metrics-1.0.json                    sha256:ac4b2ef5a9ed45c41124fd4ab01d09ee9618e47264ad046bbeedd30a04a946dc
docs/contracts/fixtures/trajectory-record.synthetic.json sha256:8f62876a8ed0ac3feed87209a94b2b99f62c787b0aaaa5f74aea6777653b4446
scripts/check-trajectory-contract.mjs 输出             ALL CONTRACT CHECKS PASS（31/31）
scripts/check-trajectory-pipeline.mjs 输出             ALL PIPELINE CHECKS PASS（36/36）
scripts/check-training-backend.mjs 输出                ALL BACKEND CHECKS PASS（28/28）
scripts/check-v250-boundaries.mjs 输出                 ALL BOUNDARY CHECKS PASS（100/100）
数据集 manifest（冻结后）                               sha256:<...>
解析后的训练配置（resolved config，不含凭证）            <path>
tokenized 样例（5 条，已脱敏）                          <path>
GPU 作业记录（jobId / 实际 GPU·h / 实际费用）           <path>
```
