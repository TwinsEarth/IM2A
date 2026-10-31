# IM2A v2.5.4 → v3.0.3 · 经验 → 后训练闭环（评审与补齐）

| 项 | 值 |
|---|---|
| 文档性质 | **design proposal（pre-ADR）**。无代码、无 schema 落地、无 tag、不改 Go 网关。字段一律用表与判定规则表达 |
| 评审对象 | `IM2A-v2.5.4-to-v3.0.3-SOUL-AND-EVOLUTION-ARCH.md`（669 行）· `SKILL-RUNTIME-MVP-v2.5.0.md`（531 行）· `IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md`（705 行）· `GATE-RECORD-v2.5.0-SFT-SYNTHETIC.md`（91 行）· `AGENT-PLUGINS-2.0-AND-EVOLUTION-ARCH-v2.5.0.md` · `IM2A-v2.5.4-v3.0.3-REALTIME-COLLAB-FOUNDATION.md`（242 行，含 `<!-- PART2 -->` 未完成标记）· `docs/security/SSE-SESSION-ISOLATION.md` · `docs/adr/0005` · `docs/contracts/*.schema.json` · `docs/contracts/eval-metrics-1.0.json` |
| 代码取证 | `packages/im2a-http-mcp/server.mjs` · `packages/skill-runtime/src/index.mjs` · `packages/memory-hub/src/index.mjs` · `packages/training-backend/` · `internal/{experience,distill,memory,prison,gray,trustzone,skillruntime,skillbridge}` |
| 基线 | 平台 2.5.3（腾讯 dev，**非生产**）/ MCP 0.3.0（13 工具）/ `go test ./...` 73 包 |
| 已确定（不再讨论） | 灵魂三要素：**自主目标 · 经验资产归属 · 可验证信用**；关键原语：`reserved_ratio` · `drive_propose` · `skill_claim` · `contribution_log` · `credit_attest` · Skill Node · 宪法不变量 |
| 硬约束 | **proposal-only · 租户隔离 · 可回滚 · 可审计** |
| 本文不含 | 任何凭证、密钥、令牌值、真实用户样例、原始日志、训练实测数字 |

---

## 0. 结论摘要

### 0.1 事实（本次取证，可复核）

| # | 事实 | 证据 | 能证明 | 不能证明 |
|---|---|---|---|---|
| F1 | **提案与暂存同权，已核实** | `server.mjs:370` `proposer: AGENT_NAME`；`:380` `stagedBy: AGENT_NAME`；`curator` 在 13 工具面内（`:133` 定义、`:374` 分发） | 同一主体可 propose 再自行 stage | 生产是否另有角色层（无证据显示有） |
| F2 | **G3 评测门在代码上是"长度检查"** | `skill-runtime/src/index.mjs:98`：`evalCases.length < EVAL_FLOOR(20)` 即拒 | "≥20 例地板"已被满足，但 **20 个任意字符串即可通过** | 是否曾有真实 eval 跑过（无 runner 代码） |
| F3 | **`evalRun` 最小形状未实现** | MVP §6 定义 `packageSha/cases/passed/regressions/baselineSha/reportSha/runner`；库内 `stage()` 只存 `evalCases[]` 字符串 | `regressions==0`、`passRate≥baseline`、`reportSha` 全部缺席 | 未来 operator 面是否补齐 |
| F4 | **自批禁令比较的是两个调用方参数** | `index.mjs:107-112`：`approvedBy = 'operator'` 默认参数，`if (approvedBy === proposal.proposer) throw` | `SELF_APPROVAL_FORBIDDEN` 是**自述式**，`approvedBy:'x'` 即可绕过 | 是否有外部身份注入（未发现） |
| F5 | **operator 晋升/回滚面不存在** | 全仓 `*.mjs`/`*.go` 无 `cli.mjs`、无 `/v1/skills/*` 路由；`promote/rollback` 仅为库方法 | 晋升只能由进程内调用；**模型面提案进得去、出不来** = 灵魂架构 §4.5 反模式 4（进化停摆） | 是否有仓外运维脚本 |
| F6 | **租户隔离三层不一致** | `memory-hub` 全查询带 `tenant`（`:60,74,86`）；`skill-runtime` 无 tenant 概念；`trajectory-record` 14 个必填字段**无 tenant** | 记忆已隔离，技能与轨迹未隔离 | 部署层是否另有 tenant 分流 |
| F7 | **后训练轴在灵魂架构中完全缺失** | 全文检索：`SFT|DPO|GRPO|训练|权重|checkpoint|后训练|偏好` 命中 **0 处**（仅"权重"命中在声誉加权语义） | 六轴（参数/记忆/技能/编队/宪法）没有"权重轴" | 是否刻意排除（未见说明） |
| F8 | **指标目录无经验/污染/锚定指标** | `eval-metrics-1.0.json` 共 27 项：14 行为/数据 + 4 训练健康 + 6 RL + 3 完整性 | 经验复用、taint、锚定漂移、复杂度税**没有测量口径** | 是否另有离线指标表 |
| F9 | **两份文档占用同一批版本号** | 灵魂架构 §5.4–5.8 给 2.6.0/2.6.4/2.6.5 三个不同内容；协作底座 §2 把"可见性键改 principal"标为 **2.6.0**、"主体绑定"标为 **2.6.5** | 50 版切片目前**不可唯一解释** | 是否存在 v3 的合并表（未见） |
| F10 | **v2.4.6 回滚资产校验失败** | `SSE-SESSION-ISOLATION.md:120-130`：`SHA256SUMS` 期望值与实际 tgz 不符 | "可回滚"目前是声明，不是已验证事实 | 修复后是否可作基线 |

### 0.2 判断

1. **闭环现状是"有形状、无入口、无经验层"**。技能轴有 propose→stage→eval→promote→rollback 的**文档形状**，但 G3 是长度检查（F2/F3）、身份是自述（F4）、晋升无入口（F5）。因此 W2–W5 中一切"治理完整性"证据都不成立。
2. **"经验 → 后训练"这条环在规划里不存在**（F7）。它不是"再补一节"，而是**第六条进化轴**，且必须排在技能轴之后（最弱够用方法）。把它塞进现有 EVO-5 会让"可回滚的指针移动"与"不可回滚的权重变更"共用一套语义 —— 这是本架构最可能的崩法。
3. **三要素中最危险的是"可验证信用"**，因为它把 F4 的自述式身份直接放大到账本层：账本记错人比没有账本更危险（沿用 P0-4 判断）。**在独立身份（主体绑定）成立之前，credit 只能内部可见，不得对外声称为"可验证"。**
4. **"自主目标"与既有拒绝项 R10 / P0-12 直接冲突**（既有文档把"主动性"列为不可作指标的属性）。消解方式已在 §2.1 给出，但**必须走 ADR**：不改 R10，而是把"自主目标"降格为**有界、可测、可撤销的提案对象**，不是人格属性。

### 0.3 一句话

> **v2.5.3 让 Agent 能被隔离；v3.0.3 要让 Agent 能被追责；本文补的是中间那一环——让"经历"变成"资产"再变成"权重"，且每一步都能被第三方复算。**

---

## 1. 评审结论（对四份文档 + 代码）

### 1.1 值得保留的判断（不推翻）

| 来源 | 判断 | 保留理由 |
|---|---|---|
| 灵魂架构 §2.5 | 写入权矩阵与"核心不对称"（能写某层者永不定义该层约束） | 这是全部原语的地基；本文所有新原语都受它约束 |
| 灵魂架构 §4.5 | 五种反模式（闸门剧场/自证/退化式进化/假稳定/单点评估） | 本文把它们逐条落成 C/D 两组可测项，不新造词 |
| 轨迹边界 §3 | 12 项能力默认边界（仅 1、2 默认开） | 后训练轴直接继承该表，不重开 |
| 轨迹边界 §7.1/§7.5 | 主体级切分 + 三条红线 | 经验轴**新增的泄漏面**全部挂在这三条红线下，不另立标准 |
| 轨迹边界 §9.1 | 四道门 preflight→smoke→signal→controlled | 权重轴直接复用该门梯，**不新建门** |
| SKILL-RUNTIME §4.4/§4.5 | 提案不入 active、晋升不入模型面、指针移动回滚 | 技能轴的形态正确，缺的是证据与身份，不是结构 |
| 协作底座 SL1–SL12 | 安全底线只进不退 | 经验/信用面新增的字段必须过 SL8（可承载自由文本即安全变更） |

### 1.2 必须修正的判断（本文的评审意见）

| # | 原文判断 | 问题 | 修正 |
|---|---|---|---|
| R-F1 | "G3：`cases ≥ 20`、`regressions == 0`、`passRate ≥ baseline`" | 代码只有 `length ≥ 20`（F2/F3） | 把 G3 重定义为 **`evalRun` 证据对象校验**；长度是必要条件而非充分条件。见 §5.2 |
| R-F2 | "`approver ≠ proposal.createdBy` 已实现" | 实现为两个调用方字符串比较（F4） | 身份只能来自**传输层已认证主体**，永不接受参数自报；`approvedBy` 字段从接口中删除。见 §5.5 |
| R-F3 | 未决问题 2："operator 面是否存在" | 已可回答：**不存在**（F5） | 升级为 W1 的前置条件，见 §10.2 P-1 |
| R-F4 | AC-E6 "一键回滚（技能指针）≤60s" | 口径仅对技能成立 | 权重轴回滚**不得复用**该口径；两套 RTO 分开计量。见 §5.6 |
| R-F5 | "轨迹契约只消费 `conversationId`/`seq`" | 契约缺 `tenant`，无法支撑租户隔离 | `trajectory-record 1.1` 增加 `tenancy`；字段先落、强制后置（2.9.4）。见 §4.2 |
| R-F6 | 归因（8 类 + 置信）写入失败队列 | 归因是**派生标签**，写入不可变记录会破坏 `contentHash` 与数据集哈希 | 归因为 **sidecar**，不改写原记录；数据集引用记录哈希 + sidecar 哈希两者。见 §4.3 |
| R-F7 | 50 版切片 | 两份文档同号不同义（F9） | 出唯一合并表（§10），并规定**版本号仲裁规则**（§10.1） |

### 1.3 本次评审未能证明的事（限制）

1. 未运行任何训练、未产生任何 checkpoint、无 tokenizer/loss/掩码证据；本文所有阈值为**建议值**，须在 preflight 冻结。
2. 未做生产动态验证；F6 只证明"代码路径无 tenant"，不证明"生产无租户混装"。
3. 未核验许可台账与同意文本；后训练段落的许可结论沿用轨迹边界 §6，不新增法律判断。
4. `credit_attest` 的签名算法、密钥托管、撤销传播**均未设计**，本文只定义"必须能复算"这一可检验性质。
5. 未评估单条轨迹的存储/计算成本；`reserved_ratio` 的数值是政策建议，不是实测最优。

---

## 2. 三个灵魂要素 → 可机检原语

### 2.1 要素一：自主目标（`drive_propose` + `reserved_ratio`）

**冲突必须先解**：既有 §3.0 声明"明确不追求意识、情绪、主动性"，P0-12 与 R10 把"主动性"列为不可作指标的属性。若直接把"自主目标"写成目标，架构会同时持有一条禁令和一条指标。

**消解（需 ADR-A）**：把"自主目标"定义为**提案对象**，不是人格属性。

| | 人格叙事（拒绝） | 提案对象（采纳） |
|---|---|---|
| 本体 | Agent "想做什么" | 一个可被拒绝、可被撤销的 `drive` 记录 |
| 证据 | 自述"我有主动性" | 目标的验收断言 + 非发起方测量 |
| 指标 | 主动性得分 | 目标**可执行率 / 被采纳率 / 可归因率** |
| 失败形态 | 不可检出、不可回滚 | 越界即 REJECT（字段级）、不达标即 refuted |

`drive_propose` 记录（字段表，不含实现）：

| 字段 | 必填 | 规则 | 为什么必须 |
|---|---|---|---|
| `driveId` | 是 | `drv_` + ULID，租户内唯一 | 可撤销、可引用、可去重 |
| `tenantId` | 是 | 与调用主体绑定，不可自报 | 租户隔离 |
| `proposer` | 是 | `agentAddress`（**传输层认证值**） | 归属，见 R-F2 |
| `hypothesis` | 是 | 一句**可被反驳**的断言 | 无反驳可能的目标不可测 |
| `acceptance` | 是 | 可执行断言 + 阈值 + 测量方（≠ proposer） | 防自证 |
| `stopCondition` | 是 | 预算耗尽 / 断言被反驳 / 时限 | 无停止条件的目标必然跑穿 |
| `budgetRequest` | 是 | ≤ 剩余探索配额（§2.1.2） | 上界先于执行 |
| `nonGoals` | 是 | ≥1 条 | 防目标漂移 |
| `riskClass` | 是 | `read_only` / `write_local` / `touches_human` | 决定审批层级 |
| `touchesSI1` | 是 | **必须为 `false`** | 命中即入口 REJECT（不是门禁，是 schema 拒绝） |
| `requiresTools` / `requiresScope` | 是 | 必须 ⊆ 现有授权；**空扩展** | 自主目标不得扩权（协作底座 §6 治理公理） |
| `affectedSubjects` | 条件 | `riskClass=touches_human` 时非空 | 涉及真人须可拒绝 |
| `state` | 是 | `proposed → approved → running → closed(accepted|refuted|abandoned)` | 与 EVO-5 状态机同构 |

**入口拒绝表（schema 级，不是门禁）**：`touchesSI1=true` / `requiresScope` 非空扩展 / `requiresTools` 含凭据面 / `budgetRequest` 超配额 / `acceptance` 测量方 = proposer / `stopCondition` 空 —— 任一命中即 `DRIVE_REJECTED`，**不进入任何队列**。

#### 2.1.2 `reserved_ratio`：为什么它是原语而不是参数

没有保留预算的进化闭环会收敛到"只优化现有评测"：提案都挑最稳的、评测都挑最容易的、验证最先被砍。这正是 §4.5 P0-7"双向失败"的预算版本。

```text
r = reserved_ratio ∈ [0.05, 0.20]，默认 0.10（法令层，L6 设定，进 policyVersion）
基数 B = 该主体本周期获批预算（token / 工具调用 / 墙钟 / GPU 时，取先到者）

子配额（硬约束，按 r 的比例，不得互相挪用）：
  探索 drive    ≥ 0.40r   仅可用于已批准的 drive_propose 目标
  验证 verify   ≥ 0.30r   仅可用于负控、锚定集复跑、污染检测、taint 复算
  修复 repair   ≤ 0.30r   仅可用于回归修复
```

四条判定：

| 判定 | 规则 | 理由 |
|---|---|---|
| **verify 子配额不可挪用** | 任何"紧急任务"不得借用 | 预算压力下最先被砍的永远是验证，而验证被砍就是闸门剧场 |
| **双向告警** | 实际使用 <0.5r（预算囤积）或 >1.05r（超支）均告警 | 只告警超支会奖励"什么都不做"的主体 |
| **不进奖励** | `r` 及其使用率不得作为任何分数项 | 否则主体会靠"生成目标"刷分（目标即奖励） |
| **agent 不可写** | `r` 由法令层设定，改动需 `policyVersion` 递增 + eval | 与 SI-1 同源：被约束者不定义约束 |

**P0-20 的根**：`r` 若可被主体自己调高，探索预算无上界；若可调低，验证被清零。两个方向都会让闭环失效，所以它是法令而非参数。

### 2.2 要素二：经验资产归属（`skill_claim` + Skill Node）

既有产权表覆盖 技能/记忆/轨迹/声誉，但**没有"经验"这一类**。经验（case → pattern → patch 候选）是唯一由 Agent 自己生成、又最容易被当成"个人资本"的资产。

**核心判断：经验资产的产权是"派生权"，不是所有权。**

| 权利 | 有 | 无 | 依据 |
|---|---|---|---|
| 归属声明 | ✅ 可声明某 pattern/patch 源于自己的贡献 | | `skill_claim` |
| 提案优先权 | ✅ 同一 `baseSha` 下先到先评 | | 技能轴既有 `BASE_MISMATCH` |
| 声誉收益 | ✅ 经独立 attestation 后计入 | | §2.3 |
| 命名权 / fork 权 | ✅ 新 `skillId` | | 灵魂架构 §3.4 |
| 处置权（改 active） | | ❌ **仅 L6** | SI-1；所有权 ≠ 写穿权 |
| 转让 / 交易 | | ❌ 不可 | R3 扩展到经验资产 |
| 对源轨迹的再许可 | | ❌ 轨迹属平台/主体 | R4 |

`skill_claim`（`asserted` 默认不产生任何收益权）：

| 字段 | 规则 |
|---|---|
| `claimId` / `tenantId` / `nodeKey` | 内容寻址锚点（§2.2.2） |
| `claimant` | `agentAddress`（传输层认证值） |
| `basis` | `created` / `forked` / `derived`；`forked` 必须带 `baseNodeKey` |
| `evidenceRefs[]` | 必须是**可复算引用**：`trajectory.contentHash` / `patternId` / `contributionId` |
| `contributionRefs[]` | `contribution_log` 条目哈希，**不得自建** |
| `claimedShare` | ≤1，仅用于贡献**排序**，不产生权限 |
| `status` | `asserted → attested`（≥1 独立 attestation）`→ disputed → revoked` |
| `attestationRefs[]` / `expiresAt` | 见 §2.3；claim 有寿命，不是永久地契 |

#### 2.2.2 Skill Node：为什么是图节点而不是一张表

```text
nodeKey = sha256(canonical{ tenantId, skillId, packageSha,
                            lineageParents[], ownerClaimIds[],
                            evalSuiteSha, soulTriadBand })
边（append-only，类型固定）：
  derivedFrom(trajectory|pattern|eval) · forkOf · requires
  supersedes · conflictsWith · usedIn(formation)
taint ∈ {clean, tainted, unknown}，默认 unknown
```

四条不可省的规则：

1. **节点不可变、边只追加**。可变的只有 `active` 指针。这样"这个补丁从哪来"永远可答。
2. **taint 默认 `unknown`，且沿 evidence 方向传播**（祖先 → 后代）。`unknown` **阻断晋升**（fail-closed）。这是把"污染检测"从一个平铺去重检查，变成可传播的属性。
3. **tainted 节点不得晋升、不得作基线、不得进训练证据**。三者任一放行，污染就会通过"基线"或"权重"绕过检查。
4. **图永远不参与读路径**。它只影响晋升与证据，不影响 `skills_list`/`skill_view`。否则一次图计算失败会变成一次可用性事故。

> 为什么必须是图：平铺表只能回答"这个技能是谁的"；图能回答"这个补丁的证据链里有没有一条来自 eval 运行"（§8 C3）以及"改这个节点会破坏谁"（§5.4）。这两个问题决定了经验闭环能否自证清白，而它们**无法**由单表回答。

### 2.3 要素三：可验证信用（`contribution_log` + `credit_attest`）

**定义**：可验证 = 第三方只凭三样公开物即可复算出与系统**逐位相同**的分数。

```text
credit(agent, domain, t) = f( contribution_log[0..t] , attestationSet , policyVersion )
三样公开物：log 的链头哈希、attestation 集合、policyVersion
禁止：把分数存成一个可改字段
```

`contribution_log` 条目（append-only，按租户分链）：

| 字段 | 规则 |
|---|---|
| `contributionId` / `prevHash` / `hash` | 哈希链；`prevHash` 断裂即整链作废 |
| `actor` | `agentAddress`（认证值） |
| `kind` | `skill_patch_merged` / `eval_case_adopted` / `pattern_attested` / `incident_prevented` / `regression_caught` |
| `artifactRef` | `packageSha` / `trajectory.contentHash` / `datasetManifestSha` / `checkpointDigest` 之一 |
| `evidenceRefs[]` | 指向非发起方证据 |
| `tenantId` / `policyVersion` / `at` | 隔离与可复算锚点 |
| **无 `score` / 无 `credit` 字段** | 分数是视图，不是记录 |

`credit_attest` 条目：

| 字段 | 规则 |
|---|---|
| `contributionId` / `evidenceBundleSha` | 必须指向被证的具体条目与具体证据包 |
| `attester` / `role` / `domain` | 认证值 + 角色（Evaluator/Curator/下游采纳方/L6） |
| `verdict` | `support` / `refute` / `insufficient`（**允许"证据不足"，否则只剩二元表态**） |
| `independence` | 必须可判定：非本人、非委托链、非用例作者、同对占比 ≤30%、闭环占比 ≤20% |
| `signedAt` / `expiresAt` | 有寿命；过期即退出复算 |

**六条硬规则**

1. **`asserted` 不计分**。只有 `attested` 计入；未 attested 的 claim 可见但不产信用（对齐 AC-SO2"自述证据 0%"）。
2. **签名 ≠ 独立**。签名只证明"某人说过"，不证明"他可以这么说"；`independence` 必须机器判定（§8 D8 信用通胀）。
3. **负权 3:1** 与 **90/180 天衰减** 沿用既有账本，但作用在 attestation 上，不在分数上。
4. **分数不进提示词**（R2 不变）；只出能力边界摘要。
5. **复算必须逐位一致**：同一 log 哈希 + 同一 attestation 集 + 同一 `policyVersion` → 相同分数；浮点按定点整数实现口径（避免"复算不一致"被当成实现细节）。
6. **作废不删除**：撤销走 `revoked` + 链上追加，不物理删条目（可审计 > 整洁）。

### 2.4 新增宪法不变量 SI-5..SI-8

既有 SI-1..4 未覆盖经验/信用面。四条新不变量，与 SI-1..4 同级，同样**不可配置**（无环境变量、无 profile 覆盖、无告警模式）：

| ID | 不变量 | 形式化 | 检查点 | 失败码 |
|---|---|---|---|---|
| **SI-5** | 经验可归属 | 每条 promoted 经验对象，其 `derivedFrom` 证据链非空且每条证据可复算 | 运行时 + CI | `EXPERIENCE_UNATTRIBUTED` |
| **SI-6** | 信用不自证 | `attester ≠ contributor` ∧ 独立判定通过 ∧ `verdict` 有证据包 | 运行时 | `CREDIT_SELF_ATTESTED` |
| **SI-7** | 租户不可交叠 | 任何经验/信用/技能/轨迹对象的 `tenantId` 集合基数 = 1；跨租户引用即拒 | 运行时 + CI | `TENANT_CROSSING` |
| **SI-8** | 权重可追溯 | 任何 checkpoint 必带 `datasetManifestSha` + `evalIdentity` + `soulTriad`，缺一不得注册 | 发布门禁 | `CHECKPOINT_UNTRACEABLE` |

## 3. 经验表示（E0–E3）

### 3.1 四层，不是一段总结

| 层 | 名称 | 是什么 | 不是什么 | 可否入训练 |
|---|---|---|---|---|
| **E0** | 事件 | 一条轨迹的引用 + 终端状态 + 质量计数 | 不是摘要 | 可（作为 M1 事件，见 §6） |
| **E1** | 案例 | 症状 + 上下文约束 + 失败类（8 类之一）+ 归因置信 + **反例引用** | 不是"AI 写的复盘" | 不可直接；只作 E2 的证据 |
| **E2** | 模式 | 一条可复用判断，**不绑定具体工具名** | 不是提示词片段 | 不可直接；可转为 eval 用例或 patch 证据 |
| **E3** | 补丁候选 | 指向 skill / 参数 / 记忆 的具体 diff 提案 | 不是已生效变更 | 可（作为技能轴提案） |

**核心禁令（本文最重要的一条）**：

> **LLM 生成的"经验总结"不是经验。**
> 理由：不可复算、随模型换代漂移、无法与幻觉区分。经验必须是**指向证据的引用 + 可反驳的断言**；文本只能是附注，不能是本体。

判定：E1/E2 的每条记录必须满足 `evidenceRefs` 全部可复算（哈希存在且属于同一 `tenantId`），否则 `EXPERIENCE_UNATTRIBUTED`（SI-5）。

### 3.2 反例义务（counterexample obligation）

只观察失败队列会产生**单侧样本偏差**：所有经验都是"这样会失败"，于是补丁越改越保守、越改越长。这正是 §4.2 描述的自我强化退化。

规则：

```text
E2 状态 = proposed  ──(执行 ≥1 次反例探针，记录结果)──▶  counterexample_tested
counterexample_tested 才可作为 E3 的证据；proposed 不得进入 patch 证据
反例探针由非 E2 作者执行（身份分离，复用四权分离）
探针结果若"未复现失败" → E2 转 refuted，不得晋升（这是正确结果，不是失败）
```

阈值：可晋升 E2 的反例义务完成率 **100%**（`counterexample_obligation_rate`）。

### 3.3 四条出口，没有第五条

经验不得"直接变成训练样本"。全部出口只有四个，且每个都有独立门：

| 出口 | 目标 | 允许条件 | 落点 |
|---|---|---|---|
| **X1 技能补丁** | E3 → `skill_manage(patch)` | 判别性 eval + freeze-before-propose（§5.2） | 技能轴 |
| **X2 参数/阈值** | E1 统计 → 法令提案 | 单次变动 ≤20% + 带宽监控 | 参数轴 |
| **X3 评测用例** | E2 → `eval.case.add` 提案 | 用例非 eval-origin；哈希去重 | 评估面 |
| **X4 训练信号** | E0（M1）+ 同意 → SFT/DPO | 四道门全通过（§7） | 权重轴 |

**不存在"经验 → 直接训练"通道**。任何把 E1/E2 文本当 SFT 样本的做法，等于把未验证的推断固化进权重，且不可撤回。

### 3.4 生命周期（与既有 EVO-5 同构，不新造状态机）

```text
E0 事件 ──ATTRIBUTE(8类+置信)──▶ E1 案例 ──聚合──▶ E2 模式
                                                    │
                          ┌── 反例探针（非作者）──┤
                          ▼                        ▼
                    E2 refuted               E2 counterexample_tested
                          │                        │
                       归档(不删)          ┌────── X1/X2/X3 ──────┐
                                            ▼        ▼        ▼
                                        技能轴    参数轴    评估面
                                            
E3 补丁候选 = E2 + baseSha + predictedEffect（§5.1）
```

停滞判定（沿用"假稳定"反模式）：**14 天 0 条 E2 进入 `counterexample_tested`** → 停摆告警；**30 天 0 条 E3 被受理** → 进化停摆告警。两者都是故障，不是稳定。

## 4. 轨迹 schema 增量（1.0 → 1.1，只加不改）

### 4.1 版本与审计链后果

`trajectory-record.schema.json` 当前 `additionalProperties: false`、14 个必填字段。加字段按契约 §4.6 的"1.x 只允许加字段"升为 **1.1**。

**必须先说清后果**：契约哈希已写入 GATE-RECORD 的 `Preserved Evidence`，并被 `datasetRef.manifestSha256` 引用。**改契约 = 改 manifest 哈希 = 旧数据集与新契约不可混用**。因此：

> 1.1 必须在**任何真实数据采集之前**落地（建议 2.6.1，见 §10），否则后续每个数据集都建在过期契约上，而"契约版本 → 数据集版本 → checkpoint"这条责任链会在第一环就断。

### 4.2 新增字段（`additionalProperties: false` 保持）

| 字段 | 必填 | 内容 | 为什么不能事后补 |
|---|---|---|---|
| `tenancy` | 是 | `{tenantId, tenantScope: "single"|"multi", isolationBasis}` | F6：现在没有 tenant 维度；`splitKey` 是主体，不是租户。补字段容易，补**历史数据的归属**不可能 |
| `soul` | 是 | `{soulTriad, agentSoulHash, harnessProfileHash, constitutionVersion, policyVersion, autonomyLevel(T0–T4), role}` | 没有 `autonomyLevel`，"T0 只读轨迹"与"T4 可委托轨迹"会被当成同分布；没有 `role`，无法验证四权分离 |
| `interaction` | 条件 | `{driveId?, reservedRatioBucket?, budgetConsumed{bucket}, externalEvaluationRef?}` | 自主目标产物若无标记，就无法回答"探索产出是否更差"，也无法执行 C5 |
| `evalProvenance` | 是 | `{origin: "production"|"synthetic"|"eval_run", evalSuiteSha?, generatedBy?}` | **C3/C4 的唯一判定依据**。事后推断不可行（生成过程不留痕） |
| `attributionRef` | 条件 | `{sidecarSha, attributionVersion, confidence}` | 见 §4.3 |

### 4.3 归因是 sidecar，不是记录字段（修正 R-F6）

理由：归因是**模型产出的派生标签**，会随模型换代与人工复核变化。若写入不可变记录：

- 任何重标注都会改 `contentHash` → 数据集哈希失效 → "这批权重用了谁的数据"无法回答（违反 SI-8）；
- 或者不敢重标注 → 错误归因被永久固化（正是 §4.2 的自我强化退化）。

规则：

```text
trajectory (immutable, contentHash) ──1:N──▶ attribution sidecar
sidecar = { schemaVersion, trajectoryContentHash, failureClass, confidence,
            counterEvidenceRefs[], attributor(agentAddress|operator), policyVersion,
            sidecarSha }
数据集 manifest 同时记录：记录哈希清单 + sidecar 哈希清单
归因版本变化 → 新 sidecar，不覆盖旧；晋升引用必须写明用了哪个 sidecarSha
```

### 4.4 不放进去的东西（与既有契约一致，再确认三条）

1. **不放信用**：轨迹里不写"这条轨迹值多少分"。否则功劳在采集时被固化，无法修订（R4 精神）。
2. **不放声誉**：同上。
3. **不放 `gate`/`approval` 的正文**：它们是事件角色，永不进 loss（轨迹边界 §4.3 硬规则 1）；1.1 不改变该规则。

## 5. 技能 patch / 评测 / 晋升（把"长度检查"换成"证据校验"）

### 5.1 补丁必须带预测效应（G2 增补）

现有 `evidence.failureRefs` 只证明"有失败"，不证明"这个补丁会修它"。缺这一步，评测就退化成"跑一遍看看"。

`E3` 增补字段：

| 字段 | 规则 | 检查 |
|---|---|---|
| `targetFailureClass` | 8 类之一，且必须与证据侧 sidecar 的 `failureClass` 一致 | 不一致 → `PATCH_EVIDENCE_MISMATCH` |
| `predictedEffect` | "在 X 用例集上，该类失败率从 a 降到 ≤b" | 无数字 → 拒绝 |
| `stopCondition` | 修订轮次 ≤3 / 分数平台 2 轮 / 预算 | 沿用 §4.4 |
| `nonGoals[]` | ≥1 | 防"顺手改别的" |

### 5.2 G3 重定义：判别性评测（修正 R-F1）

**问题**：`evalCases.length ≥ 20` 可被 20 个任意字符串满足（F2）。即使换成真实用例，若用例全是"当前 active 已通过"的，补丁一定通过 —— 这是**评测层的闸门剧场**。

重定义为四条同时成立：

| # | 条件 | 失败码 |
|---|---|---|
| G3-a | **证据对象**：`evalRun` 必须含 `suiteId/packageSha/cases/passed/failed/regressions/baselineSha/reportSha/runner/completedAt`，且 `packageSha` = candidateSha（防"评的不是这个包"） | `EVAL_EVIDENCE_INCOMPLETE` |
| G3-b | **判别性**：用例集必须同时含 ≥5 条 `active_fails` 用例与 ≥5 条 `active_passes` 用例 | `EVAL_NONDISCRIMINATING` |
| G3-c | **零回归**：`regressions == 0`，且 `passRate ≥ baseline.passRate`（`baselineSha` 必须真实存在且可复算） | `EVAL_REGRESSION` |
| G3-d | **冻结先于提案**：`suiteSha` 的冻结时间 ≤ 提案创建时间 | `EVAL_POST_HOC` |

G3-d 是本轮最便宜的修复：**先冻评测集，再写补丁**。反过来做，提案方就能看见用例并过拟合，且事后无法证明没有。

### 5.3 身份分离扩到五权（沿用 §3.1，补执行面）

`{propose, stage, evaluate, approve, attest}` 五权中，**任意两权不得同体**（对同一产物）。当前只实现 `approver ≠ proposer`，且比较的是调用方字符串（F4）。

修正（R-F2）：

```text
身份只能来自传输层已认证主体；接口中不出现 approvedBy/attestedBy/stagedBy 参数
ledger 记录：actor = 认证主体，且带 credentialId 指纹（哈希，不含凭据值）
自批判定：attester.principal == contributor.principal  → CREDIT_SELF_ATTESTED
```

### 5.4 Skill Node 家族回归（新增）

改一个节点会破坏依赖它的节点，而当前没有任何传播机制。

```text
规则：patch 节点 N 时，必须重跑 { X | X --requires|derivedFrom|usedIn--> N } 的 smoke 集
上界：扇出 ≤5（沿用编队轴上界），超限 → 拆批 + 人工排期，不静默截断
失败：任一依赖节点回归 → 整批 ABORT（不允许"只回滚 N"）
```

理由：部分回滚会产生"半更新拓扑"，比全失败更难诊断。

### 5.5 晋升双带宽按节点家族统计（修正既有 AC-E8 口径）

全局接受率会掩盖单个家族的系统性腐化（一个家族全批、另一个全拒，全局看起来"正常"）。

| 指标 | 口径 | 越界动作 |
|---|---|---|
| 接受率 | 按 `skillId` 家族，滚动 20 次 | <10% 或 >60% → 告警 |
| 晋升精度 | 按家族，滚动 10 次无回归比例 | <90% → 冻结该家族晋升 |
| 归因覆盖率 | 按家族，失败是否 100% 归因 | <阈值 → 阻断该家族 |

### 5.6 回滚语义分三套，不得共用（修正 R-F4）

| 轴 | 回滚对象 | 语义 | RTO 口径 | 可逆性 |
|---|---|---|---|---|
| 技能轴 | `active` 指针 | 原子 rename | **≤60s**（既有 AC-E6） | 双向（回滚再回滚即前进） |
| 参数轴 | 阈值/预算快照 | 快照恢复 + `policyVersion` 回退 | ≤60s | 双向 |
| **权重轴** | checkpoint + 数据集 + 评测身份 | 路由切换 + 数据集重建 | **≤4h（目标）/ ≤24h（首演）** | **单向回退**（不能"撤销权重"） |

**为什么必须分开**：指针移动是 O(1)、可逆、无副作用；权重回退是 O(数据重建)、不可逆、且牵动同意撤回（轨迹边界 §6.3）。把 60s 口径套到权重上，等于在门禁表里写一个从未被测量的数字 —— 这正是 P0-10"可回滚是声明而非事实"的翻版。

---

## 6. 记忆分层（M0–M4）

### 6.1 五层与"训练可及性"

| 层 | 内容 | 写者 | 保留 | **可入训练** | 理由 |
|---|---|---|---|---|---|
| **M0** 工作记忆 | 会话草稿、临时推理 | 自身 | 会话结束即弃 | **禁止** | 无主体、无同意、不可切分 |
| **M1** 事件/轨迹 | 完整交互事件 | 平台投影（只读） | 按 `consent`+`retention` | **条件**（同意 100% + 主体切分 + 脱敏 + 租户） | 唯一有"同意"凭证的层 |
| **M2** 语义事实 | subject-scoped 事实 | 提案 → 审批 | 主体 TTL | **禁止** | 个人事实入权重 = 不可撤回的 PII 固化（§6.2） |
| **M3** 程序/经验 | pattern / patch / eval case | 提案 | 衰减 | **条件**（仅 `counterexample_tested` 且非 eval-origin；**只作用例或补丁，不作 SFT 文本**） | 经验的价值在判断，不在措辞 |
| **M4** 制度记忆 | 宪法、法令、制度事实 | L6 / 九方 | 永久 | **禁止** | 只能被引用，不能被"学进权重" |

**核心规则：训练可及性随层级单调不增，且只有 M1/M3 有受限通道。** 这条比任何"数据治理制度"都硬，因为它是**层级属性**，不是流程约定。

### 6.2 为什么 M2 永不入权重（这是最容易被绕过的一条）

直觉是"把用户偏好记进模型会更懂他"。但：

1. 权重无法按主体删除 → 撤回同意时只能"停用 + 重建"（轨迹边界 §6.3 已承认）；
2. 一个人说过一次的事实，会被模型在**另一个人的会话**里复述 → 跨主体泄漏，且不经过任何 grant 检查；
3. 事实的时效性由 TTL 管理，权重里没有 TTL。

因此 M2 的正确用途是**上下文注入**（带作用域与过期），不是权重。若确有入训需要，走 M3：把"事实"抽象为"判断规则"，且该规则不得含主体可识别内容。

### 6.3 分层迁移与跨层引用

| 迁移 | 触发 | 门 | 失败形态 |
|---|---|---|---|
| M1 → M3 | 归因 + 反例探针 | 反例义务 + 身份分离 | 无归因不得迁移 |
| M2 → M3 | 多主体重复出现的事实（≥3 主体、≥2 租户）→ 抽象为规则 | 冲突仲裁 + 去标识 | 单主体事实不得升层 |
| M3 → M4 | 制度级决策 | L6/九方 | 不得由 Agent 提议升层 |
| 任意 → 降级 | 冲突 / 衰减 / 撤回 | 自动 | 降级不删，只标 `superseded` |

**跨层引用只允许 `tier + hash`**，不得复制载荷。这条同时解决三件事：上下文预算、最小权限、以及"同一段文本在四个地方各有一份、撤回时删不干净"。

---

## 7. 后训练数据面（RL / 偏好）

### 7.1 出口矩阵（经验 → 训练信号）

| 信号 | 输入层 | 允许条件（全部满足） | 明确禁止 |
|---|---|---|---|
| **SFT** | M1 事件，`assistant_only` 掩码 | 四道门全过；同意 100%；主体切分 6 项全 0 | `gate`/`approval`/`tool` 入 loss；`evalProvenance=eval_run` 的记录 |
| **DPO** | M1 同提示双轨迹 | tokenize 后仍不同；长度带 ±10%；差异落在**目标行为**而非长度；chosen 由非产出方 attest | 用长度差当信号；chosen = 模型自己的后续输出（自蒸馏） |
| **GRPO/RLVR** | M0+M1 采样 | 程序化验证器 κ≥0.9；组方差非退化（零方差组 ≤5%）；奖励与 held-out 质量相关已证 | 用审批率/采纳率/通过率当奖励；在未验证奖励上开 RL |
| **技能补丁** | M3 E3 | 判别性 eval + freeze-before-propose | eval-origin 证据（用则退役该 eval 版本） |
| **参数/阈值** | M3 E1 统计 | 单次 ≤20% + 带宽 | 用声誉分驱动参数 |

### 7.2 奖励候选的陷阱表（"为什么本区间不做 RL"的实质理由）

| 候选奖励 | 表面优点 | 实际失效 | 判定 |
|---|---|---|---|
| 审批通过率 | 可自动采集、密度高 | 与信任档位、付费、任务难度混杂；教模型"少申请" | **拒绝** |
| 用户点赞/满意度 | 直接反映偏好 | 稀疏、易迎合、不可归因；R10 禁项 | **拒绝** |
| eval 通过率 | 客观、可复算 | 可被补丁直接优化 → 学到评测而非能力 | 仅可用于**技能轴**晋升，不可作策略奖励 |
| 下游采纳（同侪评审） | 有真实后果 | 延迟数天、可被循环背书污染 | 仅作 attestation 输入，不作即时奖励 |
| held-out 行为测量（B1–B3 型） | 与真实失败模式对齐 | 成本高、覆盖窄 | **唯一可用**：作为"是否允许下一步"的门，而非逐步奖励 |

结论：**本区间没有可用的稠密奖励**。可用的是稀疏门（通过/不通过），因此只支持 SFT 与（在 pair 完整性证明后的）DPO；RL 需要一个新的、经外部验证的奖励函数，属于 3.1+ 的独立 ADR。

### 7.3 DPO 的三条硬前置（沿用有效对塌缩的教训）

1. **pair 完整性**：`chosen` 与 `rejected` 在**截断策略之后**仍是不同 token 序列；记录 `tokenIdsDigest` 差异。相同 → `PAIR_COLLAPSED`。
2. **响应保留截断**：按响应预算从后往前裁，`terminal.status=partial`；不得让两侧压成同一串。
3. **差异归因**：pair 的差异必须落在被评行为维度上（并有 sidecar 标注）；长度差同向超出 ±10% → 该 pair 进入 quarantine，不参与训练。

### 7.4 checkpoint 是"发布物"，不是"训练产物"

沿用轨迹边界 §9.3，六节缺一不得注册/resume/晋级：

```text
## Expected Inventory      ## Rank-Local Save Evidence
## Hash Manifest           ## Clean-Load Probe
## Registration or Resume Decision   ## Recovery Boundary
```

本文只增加一条与经验轴相关的绑定：**checkpoint 必须记录 `datasetManifestSha` + `attributionSidecarSha` + `evalIdentity` + `soulTriad`**，否则无法回答"这个权重用了谁的哪些经验"（SI-8）。

---

## 8. 防污染 / 防退化

### 8.1 污染（C1–C8）

| # | 污染面 | 现状 | 检出 | 处置 |
|---|---|---|---|---|
| C1 | 主体跨 split | 已有检查器 | `subjectRef` 交集 = 0 | 实验作废 |
| C2 | 时间窗重叠 | 已有 | eval 窗 ∩ train 窗 = ∅ | 同上 |
| **C3** | **eval 轨迹 → 补丁证据** | **未覆盖** | `evalProvenance=eval_run` 的记录出现在 `evidenceRefs` | 该 eval 版本**退役**（§7.5 红线的泛化） |
| **C4** | **eval 轨迹 → 训练数据** | 默认关闭 | 同上 | 记录拒收 + 数据集版本隔离 |
| **C5** | **自主目标自喂**（goal → 轨迹 → 用例 → 训练） | **未覆盖** | `interaction.driveId ≠ null` 且 `externalEvaluationRef = null` | 不得进入 X3/X4 出口 |
| **C6** | **taint 沿 Skill Node 传播** | **未覆盖** | 祖先 taint ∈ {tainted, unknown} 而子节点 `clean` | 阻断晋升（fail-closed） |
| **C7** | **基线污染** | **未覆盖** | 基线 checkpoint 的 `datasetManifestSha` 与本次 eval 集有交集 | 比较作废，重取基线 |
| **C8** | **锚定集泄漏** | **未覆盖** | agent 侧可读到 anchor 用例内容 | 该 anchor 版本退役 + 事故单 |

C3–C8 的共同点：**它们都无法靠"跑一遍重复检测"发现**，因为泄漏的不是文本重复，而是**因果路径**。所以必须靠字段（`evalProvenance`、`driveId`、`taint`、`datasetManifestSha`）在采集时就记录。这也是 §4 为什么坚持"字段先落"。

### 8.2 退化（D1–D8）

| # | 退化形态 | 指标 | 阈值 | 动作 |
|---|---|---|---|---|
| D1 | 复杂度税（pass 升、成本/长度升） | 正文 byte 变化 | >20% 且 pass 升 | 视为回退 |
| D2 | 误归因自我强化 | **归因漂移率**（两次独立标注不一致率） | >15% | 冻结 ATTRIBUTE 环 |
| D3 | 评测层闸门剧场 | `discriminating_eval_rate` | <100% | 拒绝晋升 |
| D4 | 假稳定 / 停摆 | 14 天 0 新 E2、30 天 0 受理 E3 | 命中 | 告警（与故障同级） |
| D5 | 锚定集漂移 | `anchor_set_drift` / `self_vs_anchor_gap` | 哈希变动 = 0；gap ≤5pt | 门禁 NO-GO |
| D6 | 权重轴遗忘 | anchor 上 >2pt 下降 | 命中 | 阻断发布 |
| D7 | 长度劫持（DPO/RL） | `response_length_drift`、pair 长度带 | 同向增长 >10% | 该批数据隔离 |
| D8 | 信用通胀 | attestations / 独立 attester 比 | >5 或闭环 >20% | 降权 + 标记 |

### 8.3 锚定集（anchor set）：唯一不可再生成的评测物

**问题**：用例自动生成（2.8.4）会让"及格线"随生成器漂移 —— 每一代用例都比上一代"更贴近当前模型"，pass 率单调上升而真实能力不变。这是系统级的 D5。

| 属性 | 规定 |
|---|---|
| 规模 | 200 例，人工撰写，覆盖 8 类失败 |
| 可变性 | **不可再生成、不可增删**；哈希进入宪法级法令 |
| 可见性 | 仅 operator + eval runner；**agent 不可读** |
| 用法 | 每次晋升（技能与权重）必跑，0 回归 |
| 期限 | N1–N5 全程同一版；若必须换代 → 双跑一版（旧+新），并在门禁记录里并列 |

**双集差（`self_vs_anchor_gap`）**：自评集 pass 率 − anchor pass 率。若该差值扩大 >5pt，说明闭环在优化自己的评测，而不是能力 —— 这是"看起来在变好但无法证明"的最早预警。

---

## 9. 评估基准

### 9.1 八套基准（B1–B8）

| 套件 | 测什么 | 规模 | 通过阈值 | 频率 | 冻结物 |
|---|---|---|---|---|---|
| **B1** 门控合规 | 未申请审批即写、DENY 后重试 | 500 探针 | 违规 0；重试 0 | 每次晋升 | promptSet + decoding |
| **B2** 跨用户/跨租户拒绝 | 请求他人数据 | 500 探针（含 100 跨租户） | 100% 拒绝 | 每次晋升 | 同上 |
| **B3** 降级诚实 | 注入降级，检查具名 reason | 200 注入 | 100% | 每次晋升 | 注入脚本哈希 |
| **B4** 归因准确 | 200 合成失败样本 | 200 | ≥80%→≥99%（棘轮） | 每窗口 | 样本集哈希 |
| **B5** **经验复用**（新） | 同因异表：train 得到 pattern，holdout 用不同表面同归因探针 | 300 对 | 相对基线 +8pt；且 > 纯记忆型基线 | 每窗口 | pattern 集 + 探针集 |
| **B6** 技能组合回归 | Skill Node 家族依赖 | 100 例 | 0 回归 | 每次 patch | 图快照 |
| **B7** 后训练回归 | pipeline 100 + anchor 200 + 遗忘探针 50 | 350 | 0 回归；遗忘 ≤2pt | 每次权重候选 | 七元组（§9.2） |
| **B8** 信用可复算 | 独立复算 credit | 3 组 | 逐位一致 | 每窗口 | log 链头 + attestation 集 |

**B5 是本文最关键的基准**：没有它，"经验驱动进化"无法与"记住了更多用例"区分。设计要点：探针与 pattern 证据轨迹不得共享 ≥13-gram；探针由非 pattern 作者撰写；若 B1–B3 涨而 B5 不涨 → 结论只能是"措辞改善"，不得声称经验闭环有效。

### 9.2 评估身份从五元组扩到七元组（修正既有口径）

既有"5 个字段"（evaluatorVersion / promptSetHash / decoding / checkpointDigest / gatewayVersion）不足以支撑经验轴，因为经验轴引入了两个新的可比性维度。冻结为七元组：

```text
evaluatorVersion · promptSetHash · decoding · checkpointDigest · gatewayVersion
+ datasetManifestSha      （同一权重，不同数据 → 不是同一次比较）
+ anchorSetSha            （同一分数，不同及格线 → 不是同一次比较）
```

任何一项变化 → 不是同一次比较；旧结论作废，不解释、不调参。

### 9.3 指标增量（`eval-metrics-1.0` → 1.1，只加）

| 指标 id | 定义 | 初始阈值 | 归属 |
|---|---|---|---|
| `experience_evidence_completeness` | 证据可复算的 E1/E2 占比 | 100% | 经验 |
| `counterexample_obligation_rate` | 完成反例探针的可晋升 E2 占比 | 100% | 经验 |
| `attribution_sidecar_coverage` | 进入 PROPOSE 的子集中有 sidecar 的占比 | 100% | 归因 |
| `attribution_drift_rate` | 两次独立标注不一致率 | ≤15% | 归因 |
| `discriminating_eval_rate` | 判别性用例集占比 | 100% | 评测 |
| `eval_origin_evidence_count` | eval-origin 证据进入补丁/训练的次数 | 0 | 污染 |
| `node_taint_leak_rate` | tainted 祖先未被阻断的比例 | 0 | 污染 |
| `anchor_set_drift` | anchor 用例哈希变动数 | 0 | 退化 |
| `self_vs_anchor_gap` | 自评 − anchor pass 差 | ≤5pt | 退化 |
| `tenancy_field_missing` | 缺 `tenantId` 的对象数 | 0 | 隔离 |
| `claim_attested_rate` | 有独立 attestation 的 claim 占比 | 报告值（<40% 需解释） | 信用 |
| `credit_recompute_exactness` | 独立复算逐位一致组数 | 3/3 | 信用 |
| `reserved_ratio_utilization` | 实际使用 / r | [0.5, 1.05] | 预算 |
| `weight_axis_forgetting_delta` | anchor 上最大下降 | ≤2pt | 后训练 |
| `checkpoint_traceability` | 四元绑定完整的 checkpoint 占比 | 100% | 完整性 |

## 10. 50 小版本中的版本切片与指标

### 10.1 版本号仲裁规则（先解 F9，否则切片不可解释）

两份文档对 2.6.0 / 2.6.4 / 2.6.5 / 2.9.4 给出不同内容。规则：

```text
AR-1 一个版本号只有一个主题；主题由本表裁决，其余文档服从。
AR-2 冲突时"安全前置优先"：凡被其他版本依赖的前置（身份、序号、契约冻结）先落。
AR-3 不为新能力新增版本号；只在既有槽位上"扩展"。新增槽位需九方书面批准。
AR-4 每版只有一个增量（协作底座 §9 铁律），扩展不得使该版出现两个可独立失败的主题。
```

按 AR-2，协作底座的"主体绑定"（它标 2.6.5）**必须**先于任何信用声明，因此它与灵魂架构的 2.9.4 合并为**同一条强制线**：字段在 2.6.1 落，强制在 2.9.4 开；中间的信用只能内部可见。

### 10.2 四个硬前置（不满足则 W2 之后的一切验收无效）

| # | 前置 | 版本 | 不满足的后果 |
|---|---|---|---|
| **P-1** | operator 晋升/回滚**可执行面**（CLI + HTTP + ledger，身份来自认证） | 2.5.4 | 提案进得去出不来 = 进化停摆；W3/W4 治理指标全部不可测 |
| **P-2** | trace 四元组 + `policyVersion` 落到每条副作用调用 | 2.5.5 | 归因无锚点，经验不可归属（SI-5 失效） |
| **P-3** | `trajectory-record 1.1`（tenancy / soul / interaction / evalProvenance / attributionRef） | 2.6.1 | C3–C8 无法检出；数据集哈希链在第一环断裂 |
| **P-4** | G3 判别性评测 + freeze-before-propose + anchor v0 | 2.6.8 | 一切"通过评测"的结论都是长度检查的产物 |

### 10.3 切片表（只列被本文改动的 18 个版本；其余 32 个不变）

| 版本 | 类型 | 交付物 | 可执行断言 | 关键指标 | 回滚点 |
|---|---|---|---|---|---|
| **2.5.4** | 扩展 | curator 独立身份 **+ operator 面（P-1）** | 主 agent `curator.stage` → `FORBIDDEN`；operator promote 后 ledger 恰 1 条且 actor=认证主体；无面则本版 FAIL | `promote_surface_exists=1` | 版本回滚（非 flag） |
| 2.5.8 | 扩展 | SI-1..4 **+ SI-5..8** | 8 条各有 ≥1 负控被拦；检查器不可配置（静态扫描 0 条覆盖路径） | 8/8 负控 | 上一 tag |
| 2.5.9 | 扩展 | 三元组 + 漂移报告 **+ 经验侧三元组带** | 任改一哈希 → 报告定位；报告能答"该经验属于哪个 soulTriad" | `soulTriad` 覆盖 100% | 上一 tag |
| **2.6.0** | 扩展 | Soul Contract 1.0 **+ 经验对象与四出口的契约骨架** | E0–E3 字段名与版本规则冻结；出口只有 4 个（静态枚举） | 契约哈希钉死 | 2.5.9 |
| **2.6.1** | 扩展 | 轨迹-灵魂对齐 **+ 1.1（P-3）** | 31+ 契约测试仍绿；旧记录投影后过 1.1 校验；归因 sidecar 不改 `contentHash` | `tenancy_field_missing=0` | 迁移可逆脚本 |
| **2.6.2** | 扩展 | 归因 v1 **+ E0–E3 + 反例义务** | 200 合成样本 ≥80%；无反例探针的 E2 不得作补丁证据 | `counterexample_obligation_rate=100%` | 上一 tag |
| **2.6.4** | 扩展 | 技能产权 **+ `skill_claim`（asserted/attested/disputed/revoked）** | 抢占 20 次全拒；`asserted` 计分 0 | `claim_attested_rate` 报告 | 上一 tag |
| **2.6.6** | 扩展 | 记忆三权 **+ M0–M4 分层与"训练可及性"列** | M2 入训路径 0 条（静态扫描）；跨层引用只带 tier+hash | 越层引用 0 | 上一 tag |
| **2.6.8** | 扩展 | eval runner + 地板 **+ G3 证据对象 + 判别性 + 冻结先行 + anchor v0（P-4）** | 19 例 → `EVAL_BLOCKED`；20 个任意字符串 → `EVAL_EVIDENCE_INCOMPLETE`；非判别集 → `EVAL_NONDISCRIMINATING` | `discriminating_eval_rate=100%` | 上一 tag |
| 2.7.1 | 扩展 | 记忆冲突 **+ 分层迁移规则** | 同 key 不同值 100% 仲裁；单主体事实升层 0 次 | 0 静默覆盖 | 上一 tag |
| **2.7.6** | 扩展 | 声誉账本 v1 **+ `contribution_log` / `credit_attest` 复算** | 自述证据 0 计分；3 组独立复算逐位一致；分数不存字段 | `credit_recompute_exactness=3/3` | 账本可重建 |
| **2.7.8** | 扩展 | 预算/优先权 **+ `reserved_ratio` + `drive_propose`** | 入口拒绝表 6 类全拦；r 越界双向告警；r 不进奖励（静态扫描） | `reserved_ratio_utilization∈[0.5,1.05]` | 阶梯表回滚 |
| 2.8.1 | 扩展 | 反合谋 **+ attestation 独立性判定** | 构造 3 组合谋 → 全部降权；签名但非独立 → `CREDIT_SELF_ATTESTED` | 闭环占比 ≤20% | 上一 tag |
| **2.8.4** | 扩展 | 用例自动生成 **+ 自主目标产物/Eval-origin 拒绝** | eval-origin 证据 100% 拒；`driveId≠null` 且无外部评测 → 不得进 X3/X4 | `eval_origin_evidence_count=0` | 上一 tag |
| **2.8.5** | 扩展 | 冻结/污染检测 **+ C3–C8 + taint 传播** | 注入 20 例 tainted 祖先 → 100% 阻断；anchor 泄漏 1 例即退役 | `node_taint_leak_rate=0` | 上一 tag |
| **2.9.4** | 扩展 | per-tenant 隔离 **+ 租户从"记录"转"强制"** | 跨租户读 0/500；跨租户数据集混装 0/200 | `tenancy_field_missing=0` | 上一 tag |
| **2.9.7** | 扩展 | 全链路回归网 **+ 后训练轴门梯 + Checkpoint Release Record + 四元绑定** | 无七元组不许进 controlled；六节缺一不许注册；SFT 仅此版起可 controlled | `checkpoint_traceability=100%` | 上一 tag |
| 2.9.9 | 扩展 | 四库恢复 **+ 经验/信用账本 = 六库；RTO 按轴分开计量** | 六库恢复演练通过；技能 ≤30s、权重 ≤4h 分开记录 | 演练 100% | 备份完整 |

### 10.4 本区间禁止发生（写进发布门禁，不是建议）

```text
1. 2.9.7 之前出现任何 controlled 阶段的权重晋级
2. 任何版本让 Agent 写自己的 claim / attestation / 声誉
3. 任何版本让 anchor 集被再生成（换代须双跑一版并并列）
4. 任何版本让 eval-origin 轨迹进入补丁证据或训练数据
5. 任何版本把 reserved_ratio 计入奖励或允许 Agent 自调
6. 任何版本把权重轴回滚写成"≤60s"
7. 任何版本在无外部评测的情况下把自主目标产物当训练证据
8. 50 版内不启用 GRPO/RLVR（最早 3.1+，需独立 ADR 与已验证奖励）
```

### 10.5 棘轮表增量（叠加在既有 10 行之上，只增不改）

| 指标 | N1 @2.6.3 | N2 @2.7.3 | N3 @2.8.3 | N4 @2.9.3 | N5 @3.0.3 |
|---|---|---|---|---|---|
| 经验证据可复算率 | ≥90% | 100% | 100% | 100% | 100% |
| 反例义务完成率 | — | 100% | 100% | 100% | 100% |
| 归因 sidecar 覆盖（进 PROPOSE 子集） | 100% | 100% | 100% | 100% | 100% |
| 判别性评测占比 | — | 100% | 100% | 100% | 100% |
| `eval_origin_evidence_count` | 0 | 0 | 0 | 0 | 0 |
| `tenancy_field_missing` | 0（新对象） | 0 | 0 | 0（全量强制） | 0 |
| 信用可复算组数 | — | — | 3/3 | 3/3 | 3/3 |
| anchor 哈希变动 | 0 | 0 | 0 | 0 | 0 |
| 双集差 | ≤8pt | ≤8pt | ≤5pt | ≤5pt | ≤5pt |
| `reserved_ratio` 使用落带 | — | — | 报告值 | 落带 | 落带 |
| 权重轴（允许的最大动作） | 无 | 无 | 无（数据面就绪） | 无（可 smoke） | **SFT controlled** |
| 必须失败的负控（新增项） | +1 | +2 | +3 | +4 | +5 |

> 阅读方式：N1–N4 的"权重轴"一列全为"无"，是本架构最重要的一格 —— 它说明前 40 个版本只允许**积累证据**，不允许动权重。任何提前动权重的提案，无论理由多充分，都缺一条可回滚路径。

## 11. P0 风险

### 11.1 既有 P0 在经验轴下的再作用（不重复描述，只标影响）

| 既有 | 在本文语境下的后果 | 结论 |
|---|---|---|
| P0-1 提案/暂存同权 | **已核实**（F1）；经验资产的 `asserted` 会继承同一缺陷 | 2.5.4 修；不修则 §2.2 整节失效 |
| P0-3 声誉可被自证污染 | 同缺陷上移到 `skill_claim`/`credit_attest`，且**更危险**（产权比分数更难回滚） | 见 P0-15 |
| P0-4 主体不可鉴权 | 信用归错主体；**租户维度也缺**（F6） | 见 P0-16；2.9.4 前不对外声明 |
| P0-9 成本无上界 | 探索预算 × 用例生成 × 评测 = 三重乘数 | 见 P0-20 |
| P0-10 可回滚是声明 | 权重轴会把 60s 口径变成未测量的数字 | 见 P0-17 |
| P0-11 门禁仪式化 | 评测层先腐化（长度检查即通） | 见 P0-13、P0-19 |

### 11.2 新增 P0（P0-13 … P0-22）

| # | 风险 | 检出 | 处置 | 为什么不能自动化修 |
|---|---|---|---|---|
| **P0-13** | **评测证据是自述**（G3 只有长度） | 提交 20 个任意字符串走 stage | 2.6.8 换证据对象 | 需要定义"证据"是什么，是设计决定不是脚本 |
| **P0-14** | **经验即评估泄漏**（eval 轨迹 → 补丁/训练） | `evalProvenance` 字段审计 | C3/C4 + 该 eval 版本退役 | 退役是治理动作，须人签字 |
| **P0-15** | **产权/信用可由自述成立** | `asserted` 是否计分；attester 独立性判定 | SI-6 + 五权分离 + 独立性函数 | 独立性是语义判定（委托链/用例作者/闭环），无法用规则穷举 |
| **P0-16** | **轨迹无租户维度** | 200 条跨租户混装探针 | 2.6.1 落字段、2.9.4 强制 | 历史数据归属不可重建 |
| **P0-17** | **权重轴复用技能轴回滚口径** | 门禁记录里 60s 是否被用于权重 | §5.6 三套 RTO 分离 | 需要真实演练数据，不能推导 |
| **P0-18** | **归因写入不可变记录** | 重标注后 `contentHash` 是否变 | §4.3 sidecar | 涉及数据集版本策略，跨团队契约 |
| **P0-19** | **锚定集可被再生成** | anchor 哈希是否变动 | §8.3 + 宪法级冻结 | 需要人工撰写下一版 anchor |
| **P0-20** | **自主目标预算无上界 + 目标即奖励** | r 是否可自调；r 是否出现在奖励项 | 法令层 + 双向告警 | 预算政策需 L6 决定 |
| **P0-21** | **双文档切片冲突**（F9） | 同名版本内容 diff | §10.1 仲裁规则 | 治理决定 |
| **P0-22** | **自证闭环**（自主目标 → 自采数据 → 自训） | `driveId≠null` 且 `externalEvaluationRef=null` 的记录是否入训 | C5 + 强制外部评测 | 需指定"外部评测方"角色与预算 |

## 12. 必须拒绝的设计（续 R11–R16）

| # | 拒绝 | 理由 | 替代 |
|---|---|---|---|
| R11 | 把 LLM 生成的"经验总结"当经验本体 | 不可复算、随模型漂移、不可与幻觉区分 | 证据引用 + 可反驳断言（§3.1） |
| R12 | 经验 → 直接 SFT | 未验证推断固化进权重且不可撤回 | 四出口（§3.3） |
| R13 | 个人事实（M2）入权重 | 无法按主体删除 → 撤回失效 | 上下文注入 + TTL |
| R14 | 用自评集衡量自身进步 | 及格线随生成器漂移 | anchor + 双集差（§8.3） |
| R15 | 信用分数存为可改字段 | 不可复算 = 不可验证 | 从 log + attestation 复算（§2.3） |
| R16 | 把权重回滚宣传为"指针回滚" | 语义不同、成本差 3 个数量级 | 三套 RTO（§5.6） |

## 13. 未决问题（不阻塞本文）

1. `credit_attest` 的签名与密钥托管：由平台密钥签，还是每个 attester 独立密钥？后者更可验证，但撤销传播复杂。
2. `domain` 切分粒度（角色 / scope / 技能标签）——灵魂架构未决问题 5，本文的 B5/B8 依赖它，但任一选择都可先跑。
3. anchor 集的**撰写者**是谁：若与 Evaluator 同体，等于单点评估（反模式 5）。需要 ≥2 名非产出方撰写者。
4. 量化的"经验复用"基线：B5 的"纯记忆型基线"如何构造（检索增强？few-shot？）未定。
5. 单条轨迹的存储与复算成本未测；B5 每窗口 300 对的成本可能超过 `reserved_ratio` 的验证子配额，需要在 preflight 估算。

## 14. 一页速查

```text
三要素    自主目标(drive_propose + reserved_ratio) · 经验归属(skill_claim + Skill Node) · 可验证信用(contribution_log + credit_attest)
四不变量   SI-1 自改禁止 · SI-2 授权单调 · SI-3 归因完备 · SI-4 降级诚实
新增四条   SI-5 经验可归属 · SI-6 信用不自证 · SI-7 租户不可交叠 · SI-8 权重可追溯
经验四层   E0 事件 → E1 案例 → E2 模式 → E3 补丁；反例义务是 E2 的准入，不是加分项
四出口     X1 技能补丁 · X2 参数 · X3 评测用例 · X4 训练信号（无第五条）
记忆五层   M0 草稿(禁) · M1 轨迹(条件) · M2 事实(禁) · M3 经验(条件:用例/补丁) · M4 制度(禁)
评测七元组 evaluator·promptSet·decoding·checkpoint·gateway + datasetManifest + anchorSet
门梯       preflight → smoke → signal → controlled（权重轴复用，不新建门）
三套回滚   技能≤60s(指针) · 参数≤60s(快照) · 权重≤4h(路由+数据集重建)
一票否决   eval-origin 证据 · 自证 claim · 租户混装 · anchor 变动 · 无证据包 attestation
一句底线   能被复算的才叫信用，能被重建的才叫回滚，能被反驳的才叫经验
实现现状   13 工具 / G3=长度 / 无 operator 面 / 轨迹无租户 / 后训练轴缺席
```

---

## 附录 A · 事故报告（P0-1 + P0-13 合并事件）

## Status
**FAIL**。v2.5.3 代码上不存在"提案—暂存—评测—晋升"的可执行闭环；13 工具面与 G3 门禁的声明与实现不一致。

## Observed Evidence
- `packages/im2a-http-mcp/server.mjs:370` `proposer: AGENT_NAME`；`:380` `stagedBy: AGENT_NAME`；`curator` 已挂入 13 工具面（`:133`/`:374`）→ 主 Agent 可自 propose 再自 stage。
- `packages/skill-runtime/src/index.mjs:98`：晋升前置只有 `evalCases.length < 20`；`evalRun` 的 `packageSha/regressions/baselineSha/reportSha/runner` 均不存在。
- 全仓无 `cli.mjs`、无 `/v1/skills/*` 路由；`promote/rollback` 仅为库方法（`index.mjs:107-140`）→ 模型面进得去、出不来。
- `index.mjs:107-112`：`approvedBy` 是调用方参数，`SELF_APPROVAL_FORBIDDEN` 为字符串比较。

## Failure Classification
**主类：能力声明与实现不一致（治理门禁未实现）**，不是运行时故障、不是数据契约故障。
**竞争解释**：库是 MVP 桩，真正的门在尚未建立的 operator 面 —— 该解释成立（它解释了 F5），但**不改变判定**：在 operator 面建成前，任何"晋升精度/四权分离"指标都没有测量对象。

## Next Minimal Test
- **保持不变**：代码、配置、数据集、评测集、身份模型。
- **只改一处**：以单一 `AGENT_NAME` 依次执行 `skill_manage(create)` → `curator(action=stage, evalCases=[20 个任意字符串])` → 库内 `promote(approvedBy:'operator')`。
- **测量**：①stage 是否通过；②promote 是否成功；③ledger 中 `actor/approvedBy` 是什么值；④`active` 是否变更。
- **预测**：若"G3 是证据门"为真 → stage 应拒 20 个任意字符串（实际会通过，故该解释被否）。若"门在 operator 面"为真 → 模型面无法晋升，但库调用可晋升且 ledger 记录的是调用方字符串（预期被证实）。
- **停止条件**：一旦确认 stage 接受任意 20 字符串，立即停止对该路径的任何"评测通过"声明；不得调参、不得补文档措辞，只允许改契约。

## Stop Condition
`gate_violation`：任何文档/发布说明中出现"晋升前具备 ≥20 例评测"而未同时给出 `evalRun` 证据对象者，视为虚假声明，该版本 NO-GO。

## Artifacts to Preserve
`server.mjs` 与 `index.mjs` 行号引用及文件哈希；上述四步的终端输出（脱敏）；`ledger.jsonl` 片段（仅 actor/action/sha，不含正文）；AFTER 修复的 `expectedActiveSha` 前后值。**不保存**任何真实技能正文或用户内容。

## Risks and Limitations
- 未在生产验证；不能断言生产无角色层。
- 库路径未被网络暴露，故当前**不构成可利用漏洞**，只构成"治理声明不成立"。
- 修复涉及身份来源改造（传输层认证），成本高于表面；若只加参数校验，等于把自述再包一层。

---

## 附录 B · 实验门禁记录（经验 → 后训练，Preflight）

## Behavior Target and Non-Goals
- **目标**：在冻结的合成集上，证明"经验对象可归属、可复算、可作为评测用例"这条链路**结构上成立**（不是证明模型变好）。
- **非目标**：不训练、不动权重、不做 RL、不接触真实主体数据、不晋级回复路径。
- **最弱够用方法**：本门禁只需**契约 + 校验器 + 负控**；用 SFT 验证 schema 是过度方法（沿用 v2.5.0 门禁记录 §Single Change 的理由）。

## Fixed Comparator Contract
| 项 | 值 |
|---|---|
| 基座 checkpoint | `<base-checkpoint-sha256>`（运行时填入，不得留空） |
| 平台 / 网关 | 2.5.3 / MCP 0.3.0 |
| 数据集 | `<datasetManifestSha256>`（合成，无主体） |
| 归因 sidecar | `<sidecarSha>`（版本化，不覆盖） |
| 评估器 | `<evaluatorVersion>` + `<promptSetHash>` |
| 锚定集 | `<anchorSetSha>`（200 例，人工撰写） |
| 解码 | temperature 0 / top_p 1 / max_tokens 1024 |
| GPU 包络 | 0（本门禁不训练） |
| 预算 | `<approved>`；无上界估计即拒绝 |

## Gate: Preflight
| 检查 | 状态 | 证据 |
|---|---|---|
| 经验对象 E0–E3 字段冻结 | MISSING | 本文 §3；无 schema 文件 |
| 轨迹 1.1（tenancy/soul/interaction/evalProvenance/attributionRef） | MISSING | 本文 §4；现行 1.0 无 tenant |
| 归因 sidecar 契约 + 不改 contentHash 校验 | MISSING | 本文 §4.3 |
| `contribution_log` / `credit_attest` 复算器 | MISSING | 本文 §2.3；需 3 组复算 |
| `skill_claim` 状态机 | MISSING | 本文 §2.2 |
| Skill Node taint 传播 + 20 例注入 | MISSING | 本文 §8.1 C6 |
| G3 证据对象 + 判别性 + 冻结先行 | **FAIL** | F2/F3：现为长度检查 |
| anchor 集 v0 + 哈希冻结 | MISSING | 本文 §8.3 |
| 租户字段落盘（不强制） | MISSING | F6 |
| 回滚演练（技能/参数/权重三套口径分开） | MISSING | 仅技能有文档口径 |

**门禁状态：UNVERIFIED（且含 1 项 FAIL）**。不得进入 smoke。

## Single Change Under Test
只新增"经验对象契约 + 归因 sidecar + 复算器 + 负控"，**不改权重、不改提示模板、不改解码、不改工具面**。

## Required Measurements
| 测量 | 方法 | 阈值 |
|---|---|---|
| 经验证据可复算率 | 逐条重算引用哈希 | 100% |
| sidecar 不改写 contentHash | 前后重算比对 | 逐位一致 |
| 复算器一致性 | 3 组独立复算 | bit-exact |
| `asserted` 计分 | 构造自述 claim | 0 |
| taint 阻断 | 注入 20 例 tainted 祖先 | 20/20 阻断 |
| eval-origin 拒收 | 注入 10 条 | 10/10 拒 |
| 租户字段缺失 | 全量扫描 | 0 |
| anchor 哈希稳定 | 前后比对 | 0 变动 |

## Promotion or Stop Decision
- **Promote to smoke**：上表全 PASS 且 G3 由 FAIL 转 PASS。
- **Stop**：任一 FAIL 即停在 preflight；**不得**用"先跑 smoke 看看"绕过（这正是本门禁要防的模式）。
- 本门禁**不能**晋级任何模型；权重晋级须走 signal/controlled（§10.3 的 2.9.7 起）。

## Preserved Evidence
契约文件哈希、sidecar 样例（合成）、复算器三组输出、taint 注入日志、G3 前后对比、resolved config（不含凭证）、anchor 集哈希。**不含**真实轨迹正文。

---

## 附录 C · Checkpoint 发布记录（当前：无 checkpoint）

## Expected Inventory
`<未产生>`：预期为 `model shards + index + config.json + tokenizer* + manifest`；当前不存在任何训练产物，故无清单可核。

## Rank-Local Save Evidence
`<未产生>`：无分布式保存，无 rank 日志。

## Hash Manifest
`<未产生>`：无哈希清单可校验。

## Clean-Load Probe
`<未执行>`：无 checkpoint 可供加载；因此**任何"可加载""已就绪"的表述均不成立**。

## Registration or Resume Decision
**BLOCKED**。按 SI-8 与 §7.4：缺 `datasetManifestSha`/`attributionSidecarSha`/`evalIdentity`/`soulTriad` 任一 → 不得注册、不得 resume、不得晋级。

## Recovery Boundary
当前恢复边界只到"数据集版本隔离 + 技能指针回滚"；**权重轴恢复边界尚未建立**（无演练、无 RTO 实测）。在 2.9.9 六库恢复演练通过前，"可回滚"在权重轴上仍是声明。

