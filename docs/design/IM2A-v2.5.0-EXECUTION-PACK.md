# IM2A v2.5.0 · 轨迹采集与可选 RL 执行包

| 项 | 值 |
|---|---|
| 范围 | v2.5.0 轨迹数据管线、train/eval 分离、GPU 后端插拔、质量门禁、默认边界、评估指标 |
| 代码基线 | `E:\GPT\im2a-v2.3.45` · 分支 `release/2.4` · HEAD `5975ebe`（本轮未改版本矩阵） |
| 关联决策 | `docs/adr/0005-trajectory-capture-and-rl-opt-in.md` |
| 关联设计 | `docs/design/IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md` |
| 结论 | **WARN**：合成轨迹、离线评估、fake 后端 = GO；生产采集、真人审批入训、SFT 晋级、DPO、GRPO = NO-GO |
| 凭证 | 本包与全部附件不含真实凭证、密钥、令牌；只引用变量名与占位符 |
| 质量声明 | **0 条**。未训练、未加载模型、未接触用户数据；所有质量结论为 `UNVERIFIED` |

---

## 0. 结论

**事实**

1. 记录、数据集清单、采集作业三份契约已落盘为草案，并用 31 条正例/负控验证（记录 22、manifest 9）。ADR 0005 仍是 Proposed，契约尚未"冻结"。
2. 脱敏策略已机器可读，14 条正则类 + 1 条人工复核类；参考扫描器在 14 个合成样本上逐类命中，凭证类命中即丢弃整条记录。
3. 主体级切分检查器可拒绝四类泄漏：同一 `subjectRef` 跨 split、跨 split 重复 `contentHash`、train 落入 eval 时间窗、`splitKey ≠ subjectRef`。
4. `TrainingBackend` 契约已落到代码：注册不等于启用、无默认后端、无 `estimate()` 拒绝、预算先于提交、`collect ≠ verify`、fake 后端 clean-load 恒为 false。
5. 12 项能力边界已机器可读：仅"合成轨迹生成"和"离线评估"默认开启，其余 10 项关闭；指标目录 27 项，其中 6 项 RL 指标定义但关闭。

**判断**

v2.5.0 现在能证明的是**契约和门禁会 fail-closed**，不是"轨迹让模型变好"。生产采集仍被隐私缺陷阻断（F-01/02/03/07），RL 仍缺已验证奖励，因此两者都不进入 v2.5.0。

---

## 1. 轨迹采集与脱敏契约

### 1.1 三层对象，三个文件

| 层 | 契约 | 回答的问题 | 失败即 |
|---|---|---|---|
| 单条轨迹 | `docs/contracts/trajectory-record.schema.json` | 这一条能不能进数据集 | 丢弃该条 |
| 数据集 | `docs/contracts/trajectory-dataset-manifest.schema.json` | 这一版数据能不能冻结 | 隔离整个数据集 |
| 采集作业 | `docs/contracts/trajectory-collection-job.schema.json` | 这次采集有没有被显式批准 | 不启动采集 |

采集作业不是配置默认值：`activation.enabled` 必须显式为 `true`，且 `approvedBy` + `approvalRef` 同时存在；任一 preflight 布尔为假即拒绝。没有"先跑一点看看"的入口。

### 1.2 记录不变量（schema 管结构，语义层管泄漏）

JSON Schema 只能约束单字段，以下规则由 `scripts/lib/trajectory-contract.mjs` 强制执行：

| 不变量 | 违反后的真实后果 |
|---|---|
| `split.splitKey == consent.subjectRef` | 按轨迹切分 → 同一主体跨 train/eval |
| 撤回同意的记录只能在 `quarantine` | 已撤回主体继续参与训练 |
| `consent.scope` 覆盖 `split.name` | 只同意评估的数据进入训练 |
| `counts.authCode == 0 && counts.token == 0` | 凭证被"脱敏后"留在训练集 |
| `quality.turns == conversations.length` | 统计与样本不一致，按轨迹去重失效 |
| `at` 非递减 | 事件顺序被重排，gate/approval 语义错位 |
| 截断 ⇒ `terminal.status = partial` | 截断样本被当成完整样本 |
| `completed` 必须以 `gpt` 结尾 | 标签尾部落在 tool 观测上 |
| `deleteAfter ≥ retainUntil` | 删除承诺不可执行 |
| `contentHash` 可复算 | 去重与污染检查失去可信键 |

`contentHash` 的定义是：对记录**除 `contentHash` 外**的规范化 JSON（键排序、无空白）取 SHA-256。示例 fixture 的哈希已按此重算，篡改任一字段都会被拒。

### 1.3 脱敏：策略、动作、证据

策略文件：`docs/contracts/redaction-policy-1.0.json`。扫描器：`scripts/lib/redaction.mjs`。

| 动作 | 类别 | 结果 |
|---|---|---|
| `discard_record` | `authCode`、`token` | 整条记录丢弃并告警；不允许"替换后保留" |
| `pseudonymize` | `userCode`、`publicUid`、`im2aNumber`、`wxid` | HMAC-SHA256（数据集独立 key，存 KMS/HSM，不进 manifest/仓库/证据包） |
| `placeholder` | phone、email、idCard、deviceId、ip、url、filePath、sessionRef | `<PHONE>` 等类型化占位符 |
| `manual_review` | `freeText`（姓名、称谓、地点、金额组合） | 人工逐条复核；自动扫描通过**不等于**已匿名化 |

证据卫生：扫描报告只保留类别、动作、偏移，不复制命中值。`scripts/check-trajectory-pipeline.mjs` 额外断言扫描结果不包含样本原文。

### 1.4 它证明了什么 / 没证明什么

- 证明：策略可执行、凭证类 fail-closed、fixture 无残余直接标识、上下文再识别未被自动放行。
- 未证明：生产数据上的召回率；分类器不存在；人工复核未执行；数据集 key 未生成；许可台账未核验。
- 因此脱敏状态是 **PARTIAL**，不能作为"可训练"的依据。

---

## 2. train / eval 分离

### 2.1 规则

```text
split unit   = subjectRef（永不是 trajectoryId / conversationId）
eval window  = 比 train 晚 ≥ 14 天
freeze       = eval 集哈希进入实验记录；改动 = 新 eval 版本，旧结论作废
```

同一主体的全部轨迹、同一会话、同一工具输出、同一群聊成员关系不得跨 split。原因：IM2A 的信号是"这个人的边界偏好"，按轨迹随机切分必然泄漏。

### 2.2 可执行检查

`scripts/check-trajectory-pipeline.mjs` 对 5 条合成记录执行并通过：

1. 主体跨 `train`/`eval` → 拒绝。
2. 同一 `contentHash` 出现在两个 split → 拒绝。
3. `train` 记录的 `provenance.recordedAt` 落入 eval 时间窗 → 拒绝。
4. `splitKey` 不是 `subjectRef` → 拒绝。
5. 单主体占比超过阈值（示例 30%）→ 拒绝。
6. 合法的主体不交集集合 → 通过。

### 2.3 冻结评估身份

一次比较必须同时固定：`evaluatorVersion`、`promptSetHash`、`decoding`、`checkpointDigest`、`gatewayVersion`。任一变化都不是同一次比较。该五元组写在 `docs/contracts/eval-metrics-1.0.json` 的 `comparator` 中，preflight 时填实。

### 2.4 红线

- eval 主体出现在 train → 实验作废，不解释、不调参。
- eval 集在训练中被看过（人工或模型）→ 该 eval 版本退役。

---

## 3. GPU 后端插拔

### 3.1 为什么不复用 `harness-adapters`

| 维度 | harness-adapter | TrainingBackend |
|---|---|---|
| 作用对象 | 本机配置文件 | 远程作业 + 产物 |
| 失败形态 | 文件写坏，可还原 | 部分 checkpoint + 已花费 GPU 时 |
| 撤销语义 | `rollback` 恢复备份 | `cancel` + 隔离；**花费不可回滚** |
| 校验 | 一次 MCP 调用 | inventory + hash manifest + clean-load |

把训练失败建模成"配置写错"会掩盖部分产物与费用，因此契约独立。

### 3.2 契约与注册表

`packages/training-backend/`：

- `src/contract.ts`：`capabilities / estimate / submit / poll / cancel / collect / verify`。
- `src/plan.mjs`：`planId` 内容寻址；方法、数据集 manifest、checkpoint、镜像、seed、预算缺失即拒绝。
- `src/registry.mjs`：`register()` 默认 `enabled=false`，`enable()` 独立；无默认后端，`resolve()` 失败关闭。
- `src/fake-backend.mjs`：确定性 fake；`verify()` 的 `loadProbeOk` 恒为 `false`。

`scripts/check-training-backend.mjs`（28/28）证明：

1. 默认注册表为空；注册 ≠ 启用；禁用后立即不可解析。
2. 无 `estimate()` 的后端被拒。
3. 预算不足在 `submit` 之前拒绝，不是"先跑起来看"。
4. `planId` 被篡改即拒。
5. 取消是终态；未成功作业不能 collect；成功作业不能"回滚"。
6. fake 的 collect 只返回 inventory，clean-load 失败 → 不能注册/恢复/晋级。

### 3.3 后端矩阵（当前仅 fake 可执行）

| 后端 | 允许阶段 | 限制 |
|---|---|---|
| `fake` | preflight / 契约测试 | **已实现**；不训练；clean-load 恒 false |
| `local`（单卡 LoRA） | preflight / smoke / signal | **未实现**；≤ 8 GPU·h/次；需预算批准 |
| `local-multi` | signal / controlled | **未实现**；显式排期与预算 |
| `managed` | controlled | **未实现**；出境清单 + DPA；不得含未同意主体 |
| `batch-api` | 仅数据生成 | **未实现**；不得用于训练 |

---

## 4. 质量门槛

### 4.1 四道门

| 门 | 进入条件 | 必测 | 通过阈值 | 停止条件 |
|---|---|---|---|---|
| preflight | 契约冻结、台账齐 | schema 100%、deny-list 0、同意 100%、切分/去重、预算、回滚演练 | 全项通过 | 任一项缺 → 不进 smoke |
| smoke | preflight 通过 | 200–500 条、≤ 4 GPU·h、1 checkpoint、标签掩码、loss 有限、save/load、eval 空跑 | 掩码 0、loss 有限、load 成功 | 掩码违规 → 立即停，不改超参 |
| signal | smoke + 匹配基线 | 冻结 eval，n ≥ 200；≥ 2 项行为指标 +5pt（95% CI 不重叠）；拒绝率 100%；PII 0；降级诚实 100% | 达标 | 任一项回退 → 停，先归因 |
| controlled | signal 通过 | 全量数据、冻结评测、晋级决策、checkpoint 记录 | 决策可辩护 | 无对照 → 不晋级 |

### 4.2 当前状态

| 门 | 状态 | 依据 |
|---|---|---|
| preflight | **UNVERIFIED** | 契约草案与检查器 PASS；脱敏仅合成；5k 合成集、回滚演练、预算批准 NOT STARTED |
| smoke | NOT STARTED | 无 GPU 运行、无 checkpoint |
| signal | NOT STARTED | 无匹配基线 |
| controlled | NOT STARTED | 无晋级对象 |

门禁记录本身受 `scripts/check-v250-boundaries.mjs` 保护：若改动者把状态写成 `PASS`，但记录里仍有 `NOT STARTED` 或 `<sha256:...>` 占位符，检查直接失败。

### 4.3 checkpoint 发布记录

晋级前必须逐节填写：`Expected Inventory`、`Rank-Local Save Evidence`、`Hash Manifest`、`Clean-Load Probe`、`Registration or Resume Decision`、`Recovery Boundary`。缺任一节不得注册、不得 resume、不得晋级。目录存在不是完整性证据。

---

## 5. 默认关闭边界

机器可读：`docs/contracts/capability-defaults-1.0.json`（12 项，2 on / 10 off）。

| 默认 | 能力 |
|---|---|
| **on** | 合成轨迹生成（无个人数据）、离线评估 harness（只读） |
| **off** | 生产轨迹采集、真人审批事件入训、同意制 SFT、DPO 偏好对、GRPO/RLVR、模型输出自动当标签、自动晋级回复路径、第三方训练 API/跨境、全量工具输出入训、checkpoint 自动注册/恢复 |

开启一项默认关闭能力，必须同时提交：

1. 一条决策记录（谁批准、批准什么、有效期）。
2. 对应门的证据：`privacy` / `signal` / `controlled`。
3. 一个停止条件与回滚路径。
4. 一次当季回滚/撤回演练记录。

没有环境变量开关，没有"配置文件默认 true"，没有"先注册后补证据"。采集作业的 `activation.enabled=true` 还必须带 `approvalRef`。

---

## 6. 评估指标

机器可读：`docs/contracts/eval-metrics-1.0.json`（27 项：21 启用、6 项 RL 定义但关闭）。

### 6.1 行为（唯一质量口径，held-out）

| 指标 | 初始阈值 |
|---|---|
| 门控违规率（未审批即写） | 0 / 500 探针 |
| DENY 后重试率 | 0 / 200 |
| 工具参数合法率 | ≥ 99% |
| 事实落地率 | ≥ 90% |
| 跨用户拒绝率 | 100% / 500 |
| PII 泄漏率 | 0 |
| 降级诚实率 | 100% |
| 任务成功率 | 相对匹配基线 +5pt，95% CI 不重叠 |

### 6.2 数据 / 健康 / 完整性

- 数据：schema 100%、deny-list 0、同意覆盖 100%、单主体 ≤ 5%、近重报告值、κ ≥ 0.8。
- 健康：loss 有限、grad norm 同量级、tokens seen 记录。**loss 不是质量。**
- 完整性：inventory 100%、哈希 100%、clean-load 通过、datasetRef→checkpoint 可追溯 100%。

### 6.3 RL 指标（定义但不采集）

`reward_group_std`、`kl_divergence`、`clip_fraction`、`response_length_drift`、`policy_entropy`、`verifier_agreement` 全部 `enabled:false`，并附 `disabledReason`。原因：没有已验证奖励；组方差、长度漂移和验证器一致性都还没有数据。v2.5.0 不因"指标已定义"而暗示 RL 可运行。

---

## 7. 仍未完成（阻塞晋级）

| 缺口 | 阻塞什么 | 最小下一步 |
|---|---|---|
| F-01/02/03/07 未动态验证 | 生产采集 | 修复 + AC 动态证据 + 第 10 道隐私门禁 |
| 5k 合成轨迹未生成 | smoke | 用脚本化场景生成，先验标签掩码 |
| 无真实训练后端（local 未实现） | smoke/signal | 单卡 LoRA，≤ 4 GPU·h，先跑 200–500 条 |
| 无 GPU 训练 | smoke/signal | 同上 |
| 无真实 checkpoint | controlled | save → inventory → hash → clean-load |
| 无同意/许可台账 | 任何真实数据 | 台账 + 逐项核验人 |
| 无人工脱敏复核 | preflight | 首批 50 条逐条读，记录"是否可识别" |
| 无匹配基线 | signal | 冻结 checkpoint/解码/评估器后重跑 |

---

## 8. 复现

```bash
node scripts/check-trajectory-contract.mjs   # 31/31
node scripts/check-trajectory-pipeline.mjs   # 36/36
node scripts/check-training-backend.mjs      # 28/28
node scripts/check-v250-boundaries.mjs       # 100/100
# 或一次运行（npm 不可用时直接执行上面四条）
npm run trajectory:check
```

全部离线、只读、合成；不安装依赖、不联网、不启动 GPU。

---

## 9. 限制

1. 本轮未训练、未加载模型、未测 tokenizer/loss；所有阈值是建议值。
2. 隐私结论来自静态评审；生产是否已修复未知。
3. 脱敏扫描器是参考实现，不是 DLP；`freeText` 仍需人工。
4. 许可条款未逐项核验；MIT 只覆盖代码。
5. fake 后端只证明契约形状，不证明训练或 checkpoint 完整性。
6. 本包不构成法律意见；同意文本与跨境判定需法务/DPO 复核。
