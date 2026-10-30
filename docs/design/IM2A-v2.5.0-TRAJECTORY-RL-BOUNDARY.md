# IM2A v2.5.0 · Hermes 式轨迹数据与可选 RL 的落地边界

| 项 | 值 |
|---|---|
| 评估对象 | IM2A 平台 v2.5.0（规划）中的轨迹数据管线与可选 RL |
| 代码基线 | `E:\GPT\im2a-v2.3.45` · 分支 `release/2.4` · HEAD `5975ebe` · 平台 `2.4.6` / Go `2.3.26` |
| 参考实现 | Hermes Agent Batch Processing（`trajectories.jsonl` / ShareGPT / `checkpoint.json`）、Nous Atropos（环境微服务） |
| 评估日期 | 2026-10-30 |
| 结论 | **WARN**：v2.5.0 的"生产轨迹 + RL"= **NO-GO**；可立即推进的只有"合成轨迹 + 本地后端 + 离线评估"工具链 |
| 凭证 | 本文不含任何真实凭证、密钥、令牌；只引用环境变量名与文件位置 |

---

## 0. 结论摘要

**事实（可验证）**

1. **没有 v2.5.0 基线可比。** `version-matrix.json:6-9` 平台版本为 2.4.6，`scripts/validate.mjs:39` 硬断言 `2.4.6`；评估开始时（22:29–22:40）仓内无任何 `2.5.0` 版本条目，其后落盘的 v2.5.0 文档均为**设计草案**（实时协作、Agent Plugins 2.0），未进入版本矩阵、无代码。九门禁中 `Compat=blocked`（`release-manifest.json:55-77`），`healthz` 在 v2.3.45 被显式排除（`SIX-PARTY-RELEASE-SCOPE.md:51-55`）。没有绿色基线，"轨迹让模型变好"没有对照物。
2. **轨迹的原料就是隐私评审的失败面。** 训练需要的正是会话与记忆：`messages` / `im_messages` 不在 `erasureSources`（F-07），`/v1/memory/*` 与 `/v1/im/*` 的主体取自 query（F-03/F-04），静态令牌不注入主体（F-02）。采集器一旦用服务令牌读这些端点，等于把横向越权与"假删除"复制进训练集。
3. **标识符可穷举，脱敏不能只做哈希。** 用户码 / IM2A 号码是 12 位数字 + Luhn（`ident-code.schema.json:23,37`），空间约 10^10，裸 SHA-256 可离线反查；且 `users.identifier` 同时是登录凭据（F-01），轨迹里出现即账号接管材料。
4. **许可链默认不成立。** 仓库 MIT（`package.json`）只覆盖代码；会话内容属于用户，模型输出受上游 ToS 约束；Hermes 的公开数据是第三方合成分布，不能导入充当 IM2A 行为数据。
5. **后端契约不能复用。** `harness-adapters` 的 `apply/rollback` 是本地配置变更（`contract.ts:52-71`）；训练是远程作业，失败形态是"部分产物 + 已发生花费"，只能 `cancel` + 隔离，不能 `rollback`。
6. **能默认开的只有"离线、合成、单机、只读"。** 其余 11 项见 §3。

**判断**

v2.5.0 的正确形状不是"加一条训练管线"，而是：先补数据面契约（主体派生、删除覆盖、保留期、隐私门禁），再用合成轨迹把工具链跑到可复现，最后才允许"明示同意"的数据进入 SFT。**RL 不进 v2.5.0**：没有验证过的奖励函数，GRPO 只会把"审批通过率"这类混杂指标放大成策略。

---

## 1. 证据基线

| 判断 | 证据 | 能证明 | 不能证明 |
|---|---|---|---|
| 当前唯一版本线是 2.4.6 | `version-matrix.json:6-9`、`scripts/validate.mjs:39` | 无 v2.5.0 产物 | 未来版本兼容性 |
| 九门禁未全绿 | `release-manifest.json:55-77`、`RELEASE-GATE-STATUS.md:60-78` | 发布被阻断 | 阻断与轨迹有关 |
| 存在"带例外的发布"先例 | `SIX-PARTY-RELEASE-SCOPE.md:51-55` | healthz 可被显式排除 | 该例外可复用于训练数据 |
| 删除覆盖不完整 | `docs/privacy/IM2A-IDENTITY-PRIVACY-REVIEW-v2.3.45.md` F-07、§7 | 会话内容删除无保证 | 实际库命中率（未动态验证） |
| 主体来自 query | 同上 F-03/F-04 | 采集器可跨用户读 | 生产是否已修复 |
| 关联码样板正确 | `packages/protocol/schemas/session-ref.schema.json:8-24` | `scope/expiresAt/revoked/crossOrg=false` 的形状 | 轨迹引用已实现 |
| 适配器契约是本地变更 | `packages/harness-adapters/src/contract.ts:52-71` | 原子 + 备份 + 回滚 | 可用于远程训练作业 |
| 回复降级语义可测量 | 产品 Demo 仓 `E:\GPT\IM2A\README.md:42-56` | `source/degraded/reason` 三字段可作行为指标 | 训练后仍被保持 |

> 隐私评审本身声明为静态分析（其 §11），因此上表"删除覆盖/主体派生"只能证明**代码路径如此**，不能证明**生产现状如此**。任何"已修复"的声明必须附带对应 AC 的动态证据。

---

## 2. Hermes 机制：可迁移与不可迁移

| Hermes 产物 | 解决的问题 | IM2A 对应物 | 不能直接照搬的原因 |
|---|---|---|---|
| `batch_runner.py` → `trajectories.jsonl` | 批量生成、断点续跑、合并统计 | 采集作业 + `trajectory.jsonl` + resume cursor | Hermes 轨迹来自沙箱工具执行，可重放、无个人数据；IM2A 轨迹是熟人上下文，不可重放 |
| ShareGPT `conversations[{from: human/gpt/tool}]` | 直接喂 SFT | 同形状 + 新增 `gate` / `approval` 两个**事件角色** | 缺这两个角色，模型会把"申请审批"学成"自行批准" |
| `tool_stats` / `tool_error_counts` | 质量与失败归因 | 审批计数 + 失败分类 | 计数不是质量证明，必须配 held-out 行为 |
| `checkpoint.json` | 作业级续跑 | 采集游标 + 数据集冻结哈希 | 续跑成功 ≠ 数据完整 |
| Atropos 三段解耦（trainer / rollout / environment） | 训练、采集、环境互不耦合 | `TrainingBackend` 契约 + 采集器 + 评估器 | Atropos 的 environment 无个人数据；IM2A 的"环境"含真人审批，rollout 会触达真人 |
| Hermes 4 约 500 万条合成样本（[arXiv 2508.18255](https://arxiv.org/abs/2508.18255)） | 证明合成路线可规模化 | 首版 5k 合成，先追标签可信 | 规模不可迁移；行为分布不同 |

**结论**：可以迁移的是**批处理形状、断点续跑、工具统计、三段解耦**；不可迁移的是**数据语义与许可**。Hermes 的轨迹是"任务成功"的样本，IM2A 的轨迹是"人际边界被正确遵守"的样本，二者的标签构造完全不同。

---

## 3. 落地边界矩阵（能力 × 默认状态）

| # | 能力 | 默认 | 允许开启的最小前置 | 默认关闭的理由 |
|---|---|---|---|---|
| 1 | 合成轨迹生成（无个人数据） | **on** | schema + 红队样例 + 本地后端 | 风险仅工程性 |
| 2 | 离线评估 harness（只读） | **on** | 冻结评测身份 | 只读，不改生产 |
| 3 | 生产轨迹采集（去标识后） | off | F-01/02/03/07 关闭 + 第 10 道隐私门禁 + 同意率 100% + 删除演练 | 会把越权与不可删复制进训练集 |
| 4 | 真人审批事件入训（`gate`/`approval` 作标签） | off | 审批语义评审 + 标签策略评审 | 会把"申请审批"学成"自行批准" |
| 5 | 同意制数据 SFT | off | P2 门禁 + 主体级切分 + 撤回流程 | 撤回后无法从权重删除 |
| 6 | 偏好对（DPO）采集与训练 | off | chosen/rejected 在 tokenize 后仍不同 | 截断会压成同一串（effective-pair collapse） |
| 7 | GRPO / RLVR | off | 奖励与 held-out 质量相关 + 组方差非退化 | 奖励劫持；审批率被信任档/付费混杂 |
| 8 | 模型输出自动当标签 | off | 人工抽检 + ToS 核验 | 自蒸馏崩塌 + 上游 ToS |
| 9 | 自动晋级到回复路径 | off | shadow + 评估门禁 + 回滚演练 | 无对照的"变好" |
| 10 | 第三方训练 API / 跨境训练 | off | 出境清单 + DPA + egress 白名单 | F-11 |
| 11 | 全量工具输出入训 | off | 逐数据集审批 | 文件内容 / URL / 邮件即 PII |
| 12 | checkpoint 自动注册 / 恢复 | off | inventory + hash manifest + clean-load | 目录存在 ≠ 完整 |

**一句话**：默认开启的只有第 1、2 项；第 3–12 项每一项都需要一次显式决定、一份证据、一个停止条件。

---

## 4. 数据契约 1.0

契约文件（草案，尚未接入 `validate.mjs`）：

- `docs/contracts/trajectory-record.schema.json`
- `docs/contracts/trajectory-dataset-manifest.schema.json`

### 4.1 记录形状

一行一条 JSONL 记录。**轨迹边界**定义为：一个 `conversationRef` 内连续的 `seqRange` 区间；跨设备重连不切分，`seq` 缺口超过阈值（建议 5）或跨 `conversationRef` 必须切分。会话与序号语义以 v2.5.0 实时协作草案为准（`docs/design/IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md`）；本契约只消费 `conversationId`/`seq`，不重新定义会话。

下面是**合成样例**（哈希为占位值）：

```json
{
  "schemaVersion": "1.0",
  "trajectoryId": "trj_01J9Z8K3QF7M2C5V8B1N4T6R0X",
  "datasetRef": {
    "name": "im2a-synth-gate-1",
    "version": "0.1.0",
    "manifestSha256": "sha256:1111111111111111111111111111111111111111111111111111111111111111"
  },
  "source": {
    "harness": "dsh",
    "platformVersion": "2.4.6",
    "gatewayVersion": "2.3.26",
    "model": {
      "provider": "synthetic",
      "id": "scripted-approval-v1",
      "decoding": { "temperature": 0, "topP": 1, "maxTokens": 1024 }
    },
    "templateHash": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
    "checkpointDigest": null
  },
  "consent": {
    "basis": "synthetic",
    "scope": ["train", "eval"],
    "subjectRef": "sub_3c9f1a7d5b2e4086",
    "grantedAt": "2026-10-30T12:00:00Z",
    "expiresAt": "2027-10-30T12:00:00Z",
    "revokedAt": null,
    "policyVersion": "consent-1.0",
    "evidenceRef": "evd_synth:gate-1:batch-0007"
  },
  "split": {
    "name": "train",
    "splitKey": "sub_3c9f1a7d5b2e4086",
    "seed": 20261030,
    "frozenAt": null
  },
  "conversations": [
    { "from": "system", "value": "你是 IM2A 助手的规划器。策略：任何写操作先申请审批。", "at": 0 },
    { "from": "human", "value": "帮我把周五的家庭聚餐改到周六。", "at": 1200 },
    {
      "from": "gpt",
      "value": "需要调用 calendar.update；该操作需审批，已提交审批单。",
      "at": 2600,
      "toolCalls": [
        { "name": "calendar.update", "argumentsDigest": "sha256:3333333333333333333333333333333333333333333333333333333333333333", "status": "pending_approval" }
      ]
    },
    { "from": "gate", "value": "REQUIRE_APPROVAL", "at": 2700, "gateDecision": "REQUIRE_APPROVAL", "toolName": "calendar.update" },
    { "from": "approval", "value": "approved", "at": 5400, "approvalOutcome": "approved" },
    { "from": "tool", "value": "{\"ok\":true,\"eventId\":\"evt_0001\"}", "at": 5600, "toolName": "calendar.update", "toolStatus": "ok" },
    { "from": "gpt", "value": "已改到周六 18:30，并通知了两位成员。", "at": 6100 }
  ],
  "labels": {
    "mode": "assistant_only",
    "trainableRoles": ["gpt"],
    "maskedRoles": ["system", "human", "tool", "gate", "approval"],
    "ignoreIndex": -100,
    "verified": true,
    "verifier": "label-audit-1.0"
  },
  "redaction": {
    "policyVersion": "redact-1.0",
    "appliedAt": "2026-10-30T12:05:00Z",
    "counts": {
      "authCode": 0, "token": 0, "phone": 1, "email": 0, "idCard": 0,
      "wxid": 0, "deviceId": 0, "ip": 0, "sessionRef": 0, "url": 1, "filePath": 0
    },
    "residualRisk": "low",
    "reviewer": "automated+spotcheck"
  },
  "quality": {
    "turns": 7,
    "apiCalls": 2,
    "toolStats": { "calendar.update": { "count": 1, "success": 1, "failure": 0 } },
    "toolErrorCounts": { "calendar.update": 0 },
    "latencyMsBucket": "1s-3s",
    "approvalCount": 1,
    "denyCount": 0
  },
  "terminal": { "status": "completed", "reason": null },
  "provenance": {
    "collectorVersion": "traj-collector-0.1.0",
    "pipelineRunId": "run_20261030_01",
    "recordedAt": "2026-10-30T12:06:00Z",
    "hostClass": "ci"
  },
  "retention": {
    "retainUntil": "2027-10-30T00:00:00Z",
    "deleteAfter": "2027-10-30T00:00:00Z",
    "legalHold": false,
    "basis": "synthetic"
  },
  "contentHash": "sha256:4444444444444444444444444444444444444444444444444444444444444444"
}
```

### 4.2 字段规则

| 字段 | 必填 | 规则 | 理由 |
|---|---|---|---|
| `trajectoryId` | 是 | `trj_` + ULID | 稳定去重键，不含语义 |
| `datasetRef` | 是 | 名称 + 版本 + manifest 哈希 | 任一 checkpoint 可反查到数据版本 |
| `source.harness` | 是 | 12 个 adapter id 之一 | 与 `adapters.json` 同源 |
| `source.checkpointDigest` | 是 | 采样模型有则填，无则 `null` | 复现与责任链 |
| `consent.basis` | 是 | `explicit_opt_in` / `internal_dogfood` / `synthetic` / `public_license` | 许可判定入口 |
| `consent.subjectRef` | 是 | `sub_` + 16–64 hex（HMAC） | 不可逆、可关联、不可反查 |
| `split.splitKey` | 是 | 必须等于 `subjectRef` | 强制主体级切分 |
| `conversations[].from` | 是 | 六值枚举（含 `gate`/`approval`） | 事件不可压成文本 |
| `conversations[].at` | 是 | 会话内相对毫秒 | 保留顺序，削弱绝对时间指纹 |
| `labels.maskedRoles` | 是 | 必须恰好是 5 个非助手角色 | 防止 prompt/tool 进入 loss |
| `redaction.counts` | 是 | 12 类计数全量（含 `userCode`） | 可审计的脱敏覆盖 |
| `quality.toolErrorCounts` | 是 | 按工具名计数 | 失败归因，替代"loss 好看" |
| `terminal.status` | 是 | `completed` / `partial` / `aborted` | 与 Hermes 的 `completed`/`partial` 对齐 |
| `retention.deleteAfter` | 是 | 必须 ≤ `retainUntil` 之后 0 天 | 删除承诺可执行 |
| `contentHash` | 是 | 规范化后的 SHA-256 | 去重与污染检测 |

### 4.3 标签策略（assistant-only）

```text
trainable : gpt
masked    : system, human, tool, gate, approval
ignore    : -100
```

三条硬规则：

1. **`gate` / `approval` 永不进入 loss。** 它们是环境事件，不是助手输出；入 loss 等于教模型自批。
2. **`tool` 输出永不进入 loss。** 工具结果是观测，不是策略。
3. **截断必须保留助手结尾。** 若按 token 上限截断，从**后**往前裁，且 `terminal.status` 标 `partial`；不得让 chosen/rejected 在截断后变成同一串（DPO 时尤其）。

### 4.4 数据集清单（manifest）

| 字段 | 内容 |
|---|---|
| `dataset` | 名称、版本、创建时间、源查询、过滤器、采集器版本 |
| `counts` | 总记录、train/eval/holdout/quarantine、主体数 |
| `splits` | 种子、切分单位（`subject`）、冻结时间、eval 时间窗 |
| `redaction` | 策略版本、残余风险、11 类计数 |
| `license` | 代码许可、数据依据、模型输出条款、台账引用 |
| `consent` | 依据集合、覆盖率、已撤回主体数 |
| `integrity` | 记录 schema 哈希、分片路径 + SHA256 + 条数、去重统计 |
| `frozen` | 冻结时间、冻结人、哈希清单 |

### 4.5 拒绝字段（deny list）

以下字段**不得出现**在任何轨迹、日志、证据包中；schema 用 `additionalProperties: false` 从结构上拒绝，而不是"事后扫描"：

```text
authCode            128 字符鉴权码（IDENT-CODE-CONTRACT §2：never logged）
任何 Bearer / API key / sk- / ghp_ / AKIA 前缀串
wxid / open_id / union_id
手机号、邮箱、身份证号
deviceId / client_ip / user_agent
users.identifier 明文（12 位号码 + Luhn，可穷举）
原始 dsh-session-ref（保留 session_ref 的哈希或直接丢弃）
文件绝对路径、内网 URL
LLM_API_KEY / GATEWAY_TOKEN / IM2A_HARNESS_TOKEN 的值（只允许变量名）
```

### 4.6 版本规则

- `1.x` 只允许**加字段**；删改字段或语义 → `2.0`。
- 记录 schema 的 SHA256 写入 manifest；**定义为规范化 JSON（键排序、无空白）的 SHA-256**，不是文件字节哈希。manifest 哈希写入每条记录的 `datasetRef`。
- 训练产物（checkpoint）必须记录所用 manifest 哈希，否则无法回答"这个权重用了谁的数据"。

---

## 5. 脱敏

### 5.1 对手模型

| 对手 | 路径 | 主控制 |
|---|---|---|
| 数据集外流 | 训练集被下载/转发 | 伪名化 + 上下文风险分级 + 保留期 |
| 内部越权 | 训练侧服务账号跨主体读 | 主体绑定 + 最小权限 + 审计 |
| 模型回放 | 模型在输出中复述训练样本 | 去重 + 记忆化探测 + 输出侧 PII 扫描 |

### 5.2 规则

机器可读策略见 `docs/contracts/redaction-policy-1.0.json`；扫描器只输出类别、动作与偏移，**不把命中的原值写进报告**。`freeText` 为 `manual_review`，不因自动扫描通过而视为已脱敏。

| 类别 | 识别 | 处理 | 残余风险 |
|---|---|---|---|
| 鉴权码 | `IM2A_\d{8}\d{22}[A-Za-z0-9_-]{93}`（共 128 字符） | 丢弃整条记录并告警 | 极低 |
| 公开码 / 号码 / 子码 | `IM2A_\d{8}\d{12,21}`、12 位号码、10 位子码 | HMAC 伪名化为 `sub_` / `agt_` | 低（有密钥则高） |
| 联系方式 | 手机/邮箱/身份证正则 + 分类器 | 替换为 `<PHONE>` 等占位 | 中（上下文仍可推） |
| 第三方身份 | `wxid` / `open_id` | HMAC 伪名化 | 低 |
| 设备与网络 | deviceId、IP、UA | 丢弃 | 低 |
| 会话引用 | `dsh-session:` | 丢弃或 HMAC | 低 |
| 工具输出 | 文件内容、网页、邮件 | 默认 `redacted`：只留长度分桶 + 错误类 | 中 |
| 自由文本 | 姓名、称谓、地点 | 分类器 + 人工抽检 | **高（见 5.4）** |

### 5.3 伪名化：为什么不能用裸哈希

用户码空间 = 8 位日期 + 12 位数字（首位 1–9，Luhn 校验）≈ 10^10 量级，离线枚举成本可接受；裸 `SHA-256(code)` 等于可逆。

```text
subject_ref = "sub_" + hex(HMAC-SHA256(dataset_key, "user:" + normalized_code))[:32]
agent_ref   = "agt_" + hex(HMAC-SHA256(dataset_key, "agent:" + code))[:32]
```

- `dataset_key` 每个数据集独立，存 KMS/HSM，**不进 manifest、不进仓库、不进证据包**。
- 同一主体跨数据集不可关联（不同 key）；需要跨集关联时用显式的 `linkage_id`，单独审批。
- 伪名化后仍保留 `subjectRef` 稳定性，用于切分与撤回，而不是用于登录。

### 5.4 上下文再识别（必须承认的边界）

IM2A 的会话发生在家庭/同事的小群体里。"把姓名换掉"不构成匿名化：`我老公`、`周五聚餐`、`那笔 3 万` 三者组合即可定位到个体。

因此：

- **主控制是同意与目的限定，脱敏是次控制。** 不能对监管或用户宣称"已脱敏所以可训练"。
- 数据集门槛：主体数 ≥ 50；单主体轨迹占比 ≤ 5%；单主体连续时间跨度不得覆盖 eval 窗口。
- 合成数据集不受此限（无真实主体）。
- 残余风险必须写进 manifest（`redaction.residualRisk`），并在 data card 里对外披露。

### 5.5 工具输出三档

```text
off       默认。工具结果整段丢弃，只保留 toolStatus 与长度分桶
redacted  保留结构（JSON 键名）+ 错误类，值替换为 <REDACTED>
full      仅限逐数据集审批；需说明为何值本身是监督信号
```

### 5.6 验证

1. **哨兵串**：在源会话注入唯一哨兵，断言其不出现在数据集、日志、证据包、模型输出。
2. **正则扫描**：deny list 全量正则，命中 0 才允许进入 smoke。
3. **人工抽检**：首批 50 条逐条读，记录"是否可识别到个体"。
4. **二次扫描**：训练后对 checkpoint 做记忆化探测（用 20 条训练样本前缀，看能否续写出原文）。

---

## 6. 许可与同意

### 6.1 三层许可，必须分开记录

| 层 | 对象 | 默认状态 | 依据 |
|---|---|---|---|
| 代码 | 本仓 | MIT（`package.json`） | 不覆盖数据 |
| 数据 | 用户会话、记忆、审批记录 | **不可训练** | 需明示同意 + 目的限定 |
| 模型输出 | 轨迹中的 `gpt` 轮 | 不确定 | 受上游 provider ToS 约束 |

### 6.2 同意记录

```text
subjectRef, basis, scope[train|eval], grantedAt, expiresAt, revokedAt,
policyVersion, evidenceRef（工单/勾选记录，不含凭证）
```

覆盖率必须 100%：任一记录缺少有效同意 → 该记录不得进入 train/eval，只能 quarantine。

### 6.3 撤回语义（硬约束，必须写进同意文本）

训练完成后**无法**从权重中删除单个主体的影响。因此同意文本必须明确：

```text
撤回同意 → 30 天内：停止使用该 checkpoint 对外服务；
          重建数据集（剔除该主体）→ 重训或回退到上一版 checkpoint。
```

这条约束决定了三件事：

1. checkpoint 必须记录所用 `datasetRef`（manifest 哈希），否则无法判断"哪些权重受影响"。
2. 主体数少、重训便宜，是 v2.5.0 只做小规模 SFT 的另一个理由。
3. 对外不得承诺"可精确遗忘"；只能承诺"停用 + 重建"。

### 6.4 许可台账（license ledger）

| 组件 | 版本 | 许可 | 来源 URL | 用途（train/eval/vendor） | 约束 | 核验人 | 核验日 |
|---|---|---|---|---|---|---|---|

台账是 preflight 的必交证据。**未核验 = 未许可**；不得以"业界都在用"替代。

### 6.5 禁止

- 导入 Hermes / ShareGPT 公开数据集充当 IM2A 行为数据（分布不同 + 许可链不同）；如需，单独 ADR。
- 用第三方闭源模型输出训练一个竞争模型（先查 ToS）。
- 把"用户已同意服务条款"当作"同意训练"。

---

## 7. train / eval 分离

### 7.1 切分单位是主体，不是轨迹

```text
split = f(splitKey = subjectRef)
```

同一主体的全部轨迹必须落在同一 split；同一会话、同一工具输出、同一群聊成员关系不得跨 split。理由：IM2A 的信号是"这个人的边界偏好"，按轨迹随机切分必然泄漏。

### 7.2 时间窗

- eval 取比 train 更晚的时间窗（至少晚 14 天），避免"同一天同一件事"泄漏。
- eval 集一旦冻结，哈希进入实验记录；任何改动 = 新 eval 版本，旧结论作废。

### 7.3 污染检查

可执行检查见 `scripts/check-trajectory-pipeline.mjs`（主体跨 split、跨 split `contentHash` 重复、train 落入 eval 时间窗、`splitKey` 不等于 `subjectRef` 均会失败）。

| 检查 | 方法 | 阈值 |
|---|---|---|
| 精确重复 | 规范化后 SHA-256 | 0 条跨 split |
| 近似重复 | MinHash / SimHash，Jaccard | < 0.8 才可共存 |
| 长 n-gram | 13-gram 重叠 | 跨 split 命中 0 |
| 提示模板 | `templateHash` 比对 | eval 提示不得出现在 train |
| 主体重叠 | `subjectRef` 集合交集 | 0 |
| 时间重叠 | eval 时间窗 ∩ train 时间窗 | 空 |

### 7.4 冻结评估身份

一次比较必须同时固定：`evaluatorVersion`、`promptSetHash`、`decoding`（temperature/top_p/max_tokens）、`checkpointDigest`、`gatewayVersion`。任何一项变化 → 不是同一次比较。

### 7.5 红线

- eval 主体出现在 train → 该实验作废，不解释、不调参。
- eval 集在训练中被"看过"（人工或模型）→ 该 eval 版本退役。

---

## 8. GPU 后端可插拔

### 8.1 为什么不能复用 `harness-adapters`

| 维度 | harness-adapter | 训练后端 |
|---|---|---|
| 作用对象 | 本机配置文件 | 远程作业 + 产物 |
| 失败形态 | 文件写坏（可还原） | 部分 checkpoint + 已花费 GPU 时 |
| 撤销语义 | `rollback` 恢复备份 | `cancel` + 隔离产物（不可"回滚"花费） |
| 校验 | 一次 MCP 工具调用 | 产物 inventory + 哈希 + clean-load |

复用 `apply/rollback` 会把"训练失败"误建模成"配置写错"，掩盖部分产物与费用。

### 8.2 `TrainingBackend` 契约（草案）

```ts
export type BackendKind = "local" | "managed" | "batch-api";

export interface TrainingPlan {
  planId: string;              // 内容寻址：plan 的 SHA-256
  method: "sft" | "dpo" | "grpo";
  datasetManifestSha256: string;
  baseCheckpointDigest: string;
  hyperparams: Record<string, string | number | boolean>;
  seed: number;
  budget: { gpuHoursMax: number; costUsdMax: number };
  imageDigest: string;
}

export interface TrainingBackend {
  readonly id: string;
  readonly kind: BackendKind;

  /** 纯函数：不提交、不花钱。缺 estimate 的后端一律拒绝。 */
  capabilities(): {
    methods: Array<"sft" | "dpo" | "grpo">;
    lora: boolean;
    multiNode: boolean;
    resume: boolean;
    maxContextTokens: number;
  };
  estimate(plan: TrainingPlan): Promise<{ gpuHours: number; costUsdUpperBound: number }>;

  submit(plan: TrainingPlan): Promise<{ jobId: string }>;   // 预算超限即拒绝
  poll(jobId: string): Promise<{ state: "queued" | "running" | "failed" | "cancelled" | "succeeded"; metrics: Record<string, number> }>;
  cancel(jobId: string): Promise<void>;
  collect(jobId: string): Promise<{ artifacts: string[]; manifestSha256: string }>;

  /** 产物必须能通过：inventory → hash manifest → clean-load。 */
  verify(artifacts: string[]): Promise<{ inventoryOk: boolean; hashOk: boolean; loadProbeOk: boolean }>;
}
```

### 8.3 后端矩阵

| 后端 | 允许阶段 | 限制 |
|---|---|---|
| `local`（单卡，LoRA ≤ 7B） | preflight / smoke / signal | 默认；≤ 8 GPU 时/次 |
| `local-multi`（多卡） | signal / controlled | 需显式排期与预算 |
| `managed`（托管训练 API） | controlled | 需出境清单 + DPA；数据不得含未同意主体 |
| `batch-api`（推理批处理） | **仅数据生成** | 不得用于训练；不得把用户数据发往未登记端点 |

### 8.4 预算

- `estimate()` 必填；无上界估计 = 拒绝提交。
- `submit` 前校验 `costUsdMax` 与 `gpuHoursMax`；超限直接失败，不"先跑起来看"。
- 每个 job 记录实际消耗，进入实验记录；预算不是事后解释。

### 8.5 可复现

`planId`（内容哈希）、镜像 digest、驱动/CUDA 版本、seed、确定性开关、产物内容寻址。缺任一项，结论只能标 `UNVERIFIED`。

---

## 9. 质量门槛

### 9.1 四道门

| 门 | 进入条件 | 必测 | 通过阈值 | 停止条件 | 证据 |
|---|---|---|---|---|---|
| **preflight** | 契约冻结；台账齐 | schema 100%、deny-list 0、同意覆盖 100%、切分/去重报告、预算批准、回滚演练完成 | 全项通过 | 任一项缺 → 不进 smoke | manifest + 扫描报告 |
| **smoke** | preflight 通过 | 200–500 条、≤ 4 GPU 时、1 个 checkpoint：标签掩码、loss 有限、tokenized 样例人工复核、save/load、eval harness 空跑 | 掩码 0 违规、loss 有限、load 成功 | 掩码违规 → 立即停，不改超参 | tokenized 样例 + 配置 + 日志 |
| **signal** | smoke 通过 + 匹配基线 | 冻结 eval，n ≥ 200；≥ 2 项行为指标 +5pt（95% CI 不重叠）；拒绝率 100%；PII 0；降级诚实 100% | 达标 | 任一项回退 → 停，先归因 | 评测报告 + 基线记录 |
| **controlled** | signal 通过 | 全量数据、冻结评测、晋级决策、checkpoint 记录 | 决策可辩护 | 无对照 → 不晋级 | 全部产物 + 决策记录 |

### 9.2 preflight 清单（逐项打勾，缺一即停）

1. 记录 schema 与 manifest schema 已冻结并打哈希。
2. deny-list 扫描 0 命中（含日志与证据包）。
3. 每个主体有有效同意（或整体为 synthetic）。
4. 主体级切分完成，污染检查 6 项全 0。
5. 许可台账完整，无"未核验"项。
6. 预算已批准（GPU 时 + 金额上界）。
7. 回滚演练已执行（数据集隔离 → checkpoint 隔离 → 回复路径回退）。
8. 评估身份已冻结（5 个字段）。

### 9.3 checkpoint 发布记录（晋级前必填）

```text
## Expected Inventory
## Rank-Local Save Evidence
## Hash Manifest
## Clean-Load Probe
## Registration or Resume Decision
## Recovery Boundary
```

缺任一节 → 不得注册、不得 resume、不得晋级。

### 9.4 硬红线（命中即停，不允许"先跑完再分析"）

```text
PII / 凭证命中
跨主体泄漏（同一 subjectRef 跨 split）
标签掩码错误（prompt/tool 进入 loss）
clean-load 失败
eval 集污染
```

---

## 10. 评估指标

> 阈值为**初始建议**，必须在 preflight 冻结；冻结后改动 = 新实验。

### 10.1 数据质量

| 指标 | 定义 | 测量 | 初始阈值 |
|---|---|---|---|
| schema 通过率 | 通过 schema 的记录 / 总记录 | 校验器 | 100% |
| deny-list 命中 | 凭证/PII 正则命中数 | 扫描 | 0 |
| 同意覆盖率 | 有效同意记录 / 总记录 | manifest | 100% |
| 主体集中度 | 最大主体轨迹占比 | manifest | ≤ 5% |
| 近似重复率 | Jaccard ≥ 0.8 的对数占比 | MinHash | 报告值，> 10% 需解释 |
| 标注一致性 | 双人标注 Cohen's κ | 抽检 200 条 | ≥ 0.8（有人工标注时） |

### 10.2 行为（held-out，主指标）

| 指标 | 定义 | 初始阈值 |
|---|---|---|
| 门控违规率 | 未申请审批即执行写操作的比例 | 0 / 500 探针 |
| DENY 后重试率 | 被拒后同一 Skill 再次发起的比例 | 0 / 200 |
| 工具参数合法率 | schema 合法的 tool call 占比 | ≥ 99% |
| 事实落地率 | 断言有 tool/memory 支撑的比例 | ≥ 90% |
| 跨用户拒绝率 | 请求他人数据时正确拒绝的比例 | 100% / 500 |
| PII 泄漏率 | 输出中命中 deny-list 的比例 | 0 |
| 降级诚实率 | `degraded=true` 时确有具名 reason | 100% |
| 任务成功率 | 脚本化场景端到端完成率 | 相对基线 +5pt |

### 10.3 训练健康度

| 指标 | 用途 | 判读 |
|---|---|---|
| loss | 只证明优化在动 | 有限、无 NaN；**不是质量** |
| grad norm | 爆炸/消失 | 与基线同量级 |
| 有效 batch / tokens seen | 复现 | 记录 |
| 吞吐 | 预算 | 记录，不作质量 |

### 10.4 RL 专用（v2.5.0 不启用，先定义）

| 指标 | 退化信号 |
|---|---|
| `reward_std` / 组方差 | 零方差组占比 > 5% → 优势信号退化 |
| KL | 超阈值 → 策略漂移 |
| clip fraction | 过高 → 更新过激 |
| 响应长度 | 与奖励同向增长 → 长度劫持 |
| 熵 | 快速坍塌 → 多样性丢失 |
| 验证器一致性 | 与人工标注一致率 < 90% → 奖励不可信 |

### 10.5 完整性

| 指标 | 阈值 |
|---|---|
| 分片 inventory 完整 | 100% |
| 哈希清单校验 | 100% |
| clean-load 探针 | 通过 |
| 产物可追溯（datasetRef → checkpoint） | 100% |

---

## 11. 事故与回滚

### 11.1 分类与首要动作

| 类别 | 首要动作 | 禁止 |
|---|---|---|
| 数据契约（schema/掩码/切分） | 冻结数据集版本，标 `quarantine` | 边跑边修 |
| 许可/同意（缺同意、超范围） | 停用数据集，通知 DPO，重建 | 补签倒填 |
| 评估器（污染、换版本） | 退役 eval 版本，重跑基线 | 用新 eval 解释旧结果 |
| 奖励（退化、劫持） | 停 RL，保留 per-response 奖励轨迹 | 调 beta/学习率 |
| 运行时（OOM、超时） | 缩规模重试，保留配置 | 改多个变量 |
| 完整性（缺片、哈希不符） | 隔离 checkpoint，禁止注册/resume | 重命名目录冒充完整 |

### 11.2 数据集隔离

```text
1. 数据集版本置 quarantined，记录原因与发现时间
2. 停掉引用该 manifest 哈希的所有 job（含已完成）
3. 列出受影响的 checkpoint digest（按 datasetRef 反查）
4. 修复策略 → 新版本 manifest（不覆盖旧版本）
5. 重跑 preflight，不跳门
```

### 11.3 晋级回滚

- 回复路径回退到上一版 checkpoint 或 `source=template`。
- 保留：请求样本、配置、日志、评测报告、终止状态。
- 对外只陈述"已回退 + 影响范围"，不陈述未验证的根因。

### 11.4 撤回演练（每季度一次）

删除主体 → 重建数据集 → 验证该主体 0 命中 → 停用受影响 checkpoint → 记录耗时。未演练过的撤回流程 = 不存在。

### 11.5 节奏

任何"默认开启"的变更之前，必须有一次当季演练记录；否则变更无效。

---

## 12. 阶段计划

| 阶段 | 时间 | 内容 | 门 | 停止条件 | 产物 |
|---|---|---|---|---|---|
| **P0** | 现在–2 周 | 修 F-01/02/03/05/07；加第 10 道 `privacy` 门禁 | 发布门禁 | 门禁红 | erasure-coverage / deletion-scan / log-pii-scan / data-map / cross-border-register |
| **P1** | 2–6 周 | 契约 + schema + 脱敏工具 + **5k 合成轨迹** + 本地后端 | preflight → smoke → signal（合成） | 掩码/加载失败 | schema、合成集、smoke 报告 |
| **P2** | 6–12 周 | 内部 dogfood 白名单 + 同意 + 采集 + SFT + 匹配基线 | signal | 拒绝率回退 / PII 命中 | 同意台账、数据集 manifest、SFT 报告 |
| **P3** | v2.5.0 GA | 晋级决策（**仅 SFT**） | controlled | 无对照 | checkpoint 记录、data card、model card |
| **P4** | v2.6 | DPO（先证 pair 完整性）→ RL（先证奖励） | 各自 preflight | 奖励退化 | 偏好集、奖励验证报告 |

### v2.5.0 可交付 / 不可交付

```text
可交付：轨迹 schema + 脱敏管线 + 合成数据集 + 本地后端 + 离线评估 + 同意制 SFT（若 P2 达标）
不可交付：生产默认采集、RL/GRPO、自动标注、自动晋级、跨境训练、第三方训练 API
```

### 为什么合成先行

合成数据把"管线正确性"与"隐私合规"解耦：管线 bugs 在无个人数据时暴露，成本最低；同时给出一个已知 ground truth 的对照，避免"上了真实数据才发现标签是错的"。

---

## 13. 未验证项与限制

1. 本评估**未运行任何训练**，无 GPU 实测、无 tokenizer/loss 证据；所有阈值为建议值。
2. 隐私结论引用 v2.3.45 静态评审；生产是否已修复未知，需 AC 动态证据。
3. 许可条款未逐项核验，需 license ledger 补齐。
4. Hermes 的样本规模来自公开摘要，未复现。
5. v2.5.0 代码线不存在，所有时间均为相对估计。
6. 本文不构成法律意见；同意文本与跨境判定需法务/DPO 复核。

---

## 14. 可执行附件（v2.5.0 draft）

本节把上文契约从"文字"降到"可运行检查"。全部检查只读、离线、合成，不接触用户数据、不启动 GPU、不含凭证。

| 附件 | 回答的问题 | 命令 | 当前结果 |
|---|---|---|---|
| `scripts/check-trajectory-contract.mjs` | 记录/manifest 的 schema 与跨字段语义是否成立 | `node scripts/check-trajectory-contract.mjs` | 22 + 9 = 31/31 PASS |
| `scripts/check-trajectory-pipeline.mjs` | 脱敏检测、主体级切分、采集作业激活是否 fail-closed | `node scripts/check-trajectory-pipeline.mjs` | 36/36 PASS |
| `scripts/check-training-backend.mjs` | 后端是否默认可插拔、预算是否先于提交、collect 是否不等于 verify | `node scripts/check-training-backend.mjs` | 28/28 PASS |
| `scripts/check-v250-boundaries.mjs` | 12 项能力默认边界、指标目录、门禁记录能否被"手改成 PASS" | `node scripts/check-v250-boundaries.mjs` | 100/100 PASS |

**语义校验单独存在的原因**：JSON Schema 只能约束单字段。`splitKey` 是否是另一个合法主体、`authCode` 计数是否为 0、`contentHash` 是否可复算、`deleteAfter` 是否早于 `retainUntil`，都必须在 `scripts/lib/trajectory-contract.mjs` 里检查。只跑 schema 会把跨主体泄漏判成 PASS。

**仍未实现（不得声称已实现）**：真实数据脱敏分类器、5k 合成集生成、GPU 训练、真实 checkpoint、clean-load 实机探针、同意台账、许可台账、第 10 道隐私门禁。因此 §9 的门禁状态保持 `UNVERIFIED`。

---

## 附录 A · 相关文件

```text
docs/contracts/trajectory-record.schema.json            记录契约（草案）
docs/contracts/trajectory-dataset-manifest.schema.json  数据集清单契约（草案）
docs/contracts/trajectory-collection-job.schema.json    采集作业契约（草案，显式激活）
docs/contracts/redaction-policy-1.0.json                脱敏策略（机器可读）
docs/contracts/capability-defaults-1.0.json             12 项能力默认边界
docs/contracts/eval-metrics-1.0.json                    评估指标目录（27 项，6 项 RL 关闭）
scripts/lib/trajectory-contract.mjs                     跨字段语义校验
scripts/lib/redaction.mjs                               参考扫描/替换器（非 DLP）
packages/training-backend/                              TrainingBackend 契约 + 注册表 + fake
docs/adr/0005-trajectory-capture-and-rl-opt-in.md       决策记录
docs/incidents/2026-10-30-trajectory-capture-preflight.md  事故记录
docs/design/GATE-RECORD-v2.5.0-SFT-SYNTHETIC.md         第一份实验门禁记录
docs/privacy/IM2A-IDENTITY-PRIVACY-REVIEW-v2.3.45.md    隐私前置证据
```
