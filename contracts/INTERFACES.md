# 冻结接口契约 v1.0（FROZEN CONTRACT）

> 本文件是**唯一**的模块级契约来源。所有实现必须与本文签名、错误码、状态机逐字一致。
> 修改本文件需要 Lead 明确授权；实现方**不得**擅自改变签名或状态名。
>
> 契约版本：v1.0（内部修订 R3）· 冻结于 2026-10-21
> 目标运行时：Node.js ≥ 22，ESM（`.mjs`），**零第三方依赖**（只允许 `node:` 内置模块）

### 变更记录（只追加，不改已发布语义）

| 修订 | 时间 | 变更 | 性质 |
|---|---|---|---|
| R1 | 2026-10-21 | 初版冻结：§1–§10 模块签名、错误码、三张状态机 | 冻结 |
| R2 | 2026-10-21 | §9 新增 `refuse()` 与错误码 `PAYER_MISMATCH`（付款人必须等于发送方） | 加法 |
| R3 | 2026-10-21 | ①§3 澄清信任比较方向（`levelOf <= trustZoneMin` 允许）并订正"DENY 优先"概述与 7 步顺序的冲突；②§4 明确"同刻先升级、后过期"的优先级；③§5 补记已实现的加法方法 `fail()/cancel()`；④§9 明确 `MODIFY` 的 patch 语义与非可退状态 | 澄清 + 加法 |
| R4 | 2026-10-21 | **v2.0 面向 Agent 的 IM 修订**：新增 §11 身份（Ed25519 + AgentCard）、§12 Agent Gateway（统一数据平面）、§13 可见域（读侧权限）、§14 频道模型（频道类型隔离未读）、§15 Agent 会话（Agent 作为一等客户端）、§16 签名审计（哈希链 + Ed25519）；§17 禁止事项追加 3 条 v2 铁律 | 加法（v1 语义全部保留） |
| R5 | 2026-10-21 | §11 澄清「平台可读名 vs 自证 id」：`agentId` 允许平台名，自证性由 `derivedId` 表达并在卡片中强制校验；`createAgentIdentity` 返回 `derivedId` | 澄清（不破坏 R4 调用方） |
| R6 | 2026-10-21 | **v3 增补**：新增 §17 人格与记忆生命周期（SOUL + 晋升/衰减/遗忘/跨平台导出）、§18 多 Agent 结构化讨论（DISCUSS/AUDIT/REVIEW）、§19 细粒度工具审批（Agent×Skill 策略）；原 §17/§18 顺延为 §20/§21 | 加法（v1/v2 语义全保留） |
| R7 | 2026-10-21 | **v4 增补「宿主型 IM」机制**：§22 服务端可信上下文注入、§23 旁观与主动沉默、§24 敏感场景模型路由、§25 轮次内投递（mid-turn delivery）；原 §20→§26 禁止事项、§21→§27 验证命令 | 加法（v1–v3 语义全保留） |
| R8 | 2026-10-21 | **v5 增补「可信传达」机制**：§26 签名信封 + 重放保护 + TOFU（Beacon）、§27 第三人称转达（Pisces/Convia，禁止冒充人类）、§28 确认层不可被 planner 预批准（ZLAgent）；原 §26→§29 禁止事项、§21→§30 验证命令 | 加法（v1–v4 语义全保留） |
| R9 | 2026-10-21 | **v6 增补「持久与委托」机制**：§29 Agent 持久邮箱（Greft，修补 TurnInbox 重启丢失的已知缺陷）、§30 带衰减的委托（AGORA：scope 子集 / 时限不长于父 / 信任上限不放宽 / 跳数受限 / 撤销级联）；原 §29→§31 禁止事项、§30→§32 验证命令 | 加法（v1–v5 语义全保留） |
| R10 | 2026-10-21 | **v7 增补「可替换底座」机制**：§31 IM 渠道适配层（AstrBot 统一 MessageEvent + 能力声明，使 PaaS 与自托管内核可切换、能力缺口显式暴露）、§32 陌生 Agent 通信的人类审批挂起（agentcomms/masumi）；**记录架构决策反转**：IM 底座 Phase0–1 回到 PaaS + 适配层（原 v2 自托管内核降为 Phase2 备选，见 docs/02 §19 与 docs/17）；原 §31→§33 禁止事项、§32→§34 验证命令 | 加法 + 一项决策反转（见文档） |
| R15 | 2026-10-21 | **v0.0.4 可观测性增补**：§41 运营指标与告警（付费漏斗/审核时效/机审失败率/退款率；告警规则为纯函数，默认五条阈值）、§42 全链审计追溯（按 messageId 串起审核→扣费→回复→退款；**缺环显式列出而非隐藏**）；原 §41→§43 禁止事项、§42→§44 验证命令 | 加法（v1–v0.0.3 语义全保留） |
| R14 | 2026-10-21 | **v0.0.3 审核链增补**：§40 付费消息审核（机审 PASS/FLAG/BLOCK + 人审队列 + 申诉改判；机审不可用 fail-closed；只有 APPROVED/APPEAL_GRANTED 可投递与扣费；审核状态机与 §4 审批引擎分离）；原 §40→§41 禁止事项、§41→§42 验证命令 | 加法（v1–v0.0.2 语义全保留） |
| R13 | 2026-10-21 | **v0.0.2 内测就绪增补**：§38 短信通道与 OTP 投递（60s 冷却 / 日上限 / 投递失败不静默 / 验证码永不离开 AccountService）、§39 退款争议与时间线（已回复争议走人工，批准后只能经账本 efund-dispute:<id> 幂等退款，不得绕过窗口改余额）；原 §38→§40 禁止事项、§39→§41 验证命令 | 加法（v1–v0.0.1 语义全保留） |
| R12 | 2026-10-21 | **v0.0.1 产品版增补「可付费准入」**：§36 账户/手机号登录/实名（idNumber 只存 hash 与尾号掩码，未实名不得收发付费消息）、§37 钱包（充值/扣费/退款幂等·禁止先扣后退·日发送上限）；原 §36→§38 禁止事项、§37→§39 验证命令 | 加法（v1–v8 语义全保留） |
| R11 | 2026-10-21 | **v8 增补「Context 主权」机制**：§33 Person-scoped Runtime（PERSONAL/TASK/WORKING 三层 + 可迁移 snapshot + Secret 拒绝入层）、§34 确认锁定 Context（Agent 只能 propose，人确认才 LOCKED，冲突上抛不覆写）、§35 PACT 式 scoped 授权 + 签名回执（付费只解锁 scope、不扩权）；原 §33→§36 禁止事项、§34→§37 验证命令 | 加法（v1–v7 语义全保留） |

> 版本号保持 v1.0 的前缀含义是"契约面冻结、语义未破坏"；所有 R2/R3 变更**均为加法或澄清**，无一处改变已有调用者可观察行为。R4 起若出现破坏性变更，须升 v2.0 并保留 v1 并行期。

---

## 0. 通用约定（GLOBAL RULES）

| 约定 | 规则 |
|---|---|
| 金额 | 一律 **整数分（cents）**，字段名以 `Cents` 结尾。不得用浮点。 |
| 时间戳 | ISO-8601 字符串（`new Date().toISOString()`），字段名以 `At` 结尾 |
| 时间源 | 所有模块接受 `{ clock = () => new Date() }` 注入，**禁止**在逻辑里直接调 `Date.now()` / `new Date()` |
| ID | 字符串。用户 `user-*`，Agent `agent-*`，消息 `msg-*`，审批单 `tkt-*`，会话 `neg-*` |
| 错误 | 每个模块导出**一个**自定义 Error 子类，带 `code` 字符串字段；code 值见各模块 |
| 不可变 | 返回的 ticket/session/message 一律 `Object.freeze` 浅冻结；状态变更通过**返回新对象**，不原地改 |
| 审计 | 会产生治理影响的模块接受 `onAudit(event)` 回调；事件形如 `{ at, actor, action, target, decision, detail }` |
| 文件头 | 每个 `.mjs` 首行注释写明「模块职责 / 契约来源 / 关键不变量」 |
| 测试 | `node:test` + `node:assert/strict`。**必须**能用 `node <file>` 直接跑（严禁 `node --test`，本机沙箱禁止 spawn） |

---

## 1. `services/orchestrator/src/trust-zone.mjs`

**职责**：五档信任矩阵，熟人社交的权限数据源。
**不变量**：默认档位永远是 `STRANGER(4)`；过期的授权视同撤销；一切授权变更必须留审计。

```js
export const TRUST_LEVELS = Object.freeze({ SELF: 0, FAMILY: 1, TRUSTED: 2, KNOWN: 3, STRANGER: 4 });
export const TRUST_NAMES = Object.freeze(['SELF', 'FAMILY', 'TRUSTED', 'KNOWN', 'STRANGER']);

export class TrustZoneError extends Error {}   // code: 'INVALID_LEVEL' | 'NOT_FOUND'

export class TrustMatrix {
  constructor({ clock = () => new Date() } = {});
  grant({ userId, targetId, level, expiresAt = null, grantedBy = userId });
  //  -> { userId, targetId, level, name, grantedAt, expiresAt }
  revoke({ userId, targetId });          // -> boolean（不存在返回 false）
  levelOf({ userId, targetId });         // -> number，缺省/过期 => TRUST_LEVELS.STRANGER
  atLeast({ userId, targetId, level });  // -> boolean
  list(userId);                          // -> Array<{ targetId, level, name, grantedAt, expiresAt }>
  history();                             // -> Array<audit event>，只追加
}
```
**规则**：`level` 必须是 0..4 整数否则抛 `INVALID_LEVEL`；`SELF` 只允许 `userId === targetId`；同一对 `(userId,targetId)` 重复 grant 为覆盖语义并追加审计。

---

## 2. `services/orchestrator/src/agent-registry.mjs`

**职责**：总 Agent / 子 Agent / Skill 的注册表，权限门控的配置来源。
**不变量**：Agent 只能属主于一个用户；子 Agent 必须有 `parentId`；每个 Agent 的能力声明是门控的**唯一**依据。

```js
export const AGENT_KIND = Object.freeze({ BUTLER: 'butler', SUBAGENT: 'subagent', SKILL: 'skill' });

export class AgentRegistryError extends Error {}
// code: 'AGENT_EXISTS' | 'AGENT_UNKNOWN' | 'OWNER_REQUIRED' | 'PARENT_REQUIRED'

export class AgentRegistry {
  constructor();
  register({
    agentId, ownerUserId, kind, domain = null, parentId = null,
    allowedSkills = [], destructiveSkills = [],
    trustZoneMin = 4,               // 该 Agent 可作用于的最低信任档（4=只能对陌生人做只读）
    budgetCentsPerDay = 0,          // 0 => 无预算（=不允许花钱）
    approvalThresholdCents = 0,     // 金额 >= 阈值 => 必须人工审批；0 => 任何花钱都要审批
    model = 'cloud-large'
  });                               // -> frozen agent
  get(agentId);                     // -> agent | undefined
  listByOwner(ownerUserId);         // -> agent[]
  children(butlerAgentId);          // -> agent[]
  isSkillAllowed({ agentId, skillId }); // -> boolean
}
```

---

## 3. `services/orchestrator/src/capability-gate.mjs`

**职责**：**代码层**权限门控。任何 Skill 调用在发出前必须过这里；不依赖模型自觉。
**不变量**：`DENY` 类判定（未知 Agent / Skill 未授权 / 信任不足）**先于** `REQUIRE_APPROVAL` 类判定（破坏性动作 / 超金额阈值）；预算检查位于破坏性动作之后、金额阈值之前。**判定顺序以上面 7 步为准**（早期版本"DENY 优先于 REQUIRE_APPROVAL"的概述与第 4/5 步顺序冲突，已按 7 步订正）。每次调用产生一条审计事件（无论放行与否）。

```js
export const DECISION = Object.freeze({ ALLOW: 'ALLOW', REQUIRE_APPROVAL: 'REQUIRE_APPROVAL', DENY: 'DENY' });
export const GATE_CODE = Object.freeze({
  ALLOWED: 'ALLOWED',
  AGENT_UNKNOWN: 'AGENT_UNKNOWN',
  SKILL_NOT_ALLOWED: 'SKILL_NOT_ALLOWED',
  TRUST_TOO_LOW: 'TRUST_TOO_LOW',
  BUDGET_EXCEEDED: 'BUDGET_EXCEEDED',
  MONTHLY_CAP: 'MONTHLY_CAP',
  AMOUNT_THRESHOLD: 'AMOUNT_THRESHOLD',
  DESTRUCTIVE_ACTION: 'DESTRUCTIVE_ACTION'
});

export class GateDeniedError extends Error {} // 携带 .code / .decision / .checks

export class DailyBudget {
  constructor({ clock = () => new Date() } = {});
  remaining(agentId);              // -> cents
  trySpend(agentId, cents);        // -> { ok, remaining }（超限不扣减）
  capOf(agentId);                  // -> cents
  setCap(agentId, cents);
  reset(agentId);
}

export class CapabilityGate {
  constructor({ registry, trustMatrix, budget = null, clock = () => new Date(), onAudit = () => {} });
  evaluate({ agentId, userId, targetId = null, skillId, amountCents = 0, destructive = false });
  //  -> { decision, code, checks: [{ name, ok, detail }], audit }
  assertAllowed(request);          // -> 同上，但 DENY 时抛 GateDeniedError
}
```
**判定顺序（必须按此顺序短路）**：
1. Agent 不存在 → `DENY / AGENT_UNKNOWN`
2. `skillId` 不在 `allowedSkills` → `DENY / SKILL_NOT_ALLOWED`
3. `targetId` 非空且 `trustMatrix.levelOf({userId,targetId})` **数值上大于** `agent.trustZoneMin`（即对方信任度不足）→ `DENY / TRUST_TOO_LOW`
   > 语义澄清（易错点）：`trustZoneMin` 是该 Agent 允许作用的**最低信任要求**；档位数值越大信任越低（SELF=0 最亲，STRANGER=4 最疏）。因此 `trustZoneMin=4` 表示「对任何人开放」，`trustZoneMin=2` 表示「仅限 SELF/FAMILY/TRUSTED」。**允许条件：`levelOf <= trustZoneMin`**；拒绝条件：`levelOf > trustZoneMin`。
4. `destructive === true` 或 skillId ∈ `destructiveSkills` → `REQUIRE_APPROVAL / DESTRUCTIVE_ACTION`
5. `amountCents > budget.remaining()` → `DENY / BUDGET_EXCEEDED`
6. `amountCents >= approvalThresholdCents`（且 `amountCents > 0`）→ `REQUIRE_APPROVAL / AMOUNT_THRESHOLD`
7. 否则 `ALLOW / ALLOWED`

---

## 4. `services/orchestrator/src/approval-engine.mjs`

**职责**：审批单生命周期（人机混合的「人\"这一侧的唯一落点）。
**不变量**：状态机显式且不可跳步；事件溯源只追加；超时升级幂等。

```js
export const TICKET_STATE = Object.freeze({
  PENDING: 'PENDING', REVIEWING: 'REVIEWING', APPROVED: 'APPROVED', REJECTED: 'REJECTED',
  MODIFIED: 'MODIFIED', EXECUTING: 'EXECUTING', COMPLETED: 'COMPLETED', FAILED: 'FAILED', EXPIRED: 'EXPIRED'
});
export const TICKET_TYPE = Object.freeze({ NEGOTIATION: 'NEGOTIATION', PAYMENT: 'PAYMENT', SKILL_CALL: 'SKILL_CALL', EXTERNAL_ACTION: 'EXTERNAL_ACTION' });

export class ApprovalStateError extends Error {} // code: 'NOT_FOUND' | 'ILLEGAL_TRANSITION' | 'FORBIDDEN' | 'EXPIRED_TICKET'

export class ApprovalEngine {
  constructor({ clock = () => new Date(), escalation = { afterMs: [30 * 60e3, 2 * 3600e3], channels: ['watch', 'sms'] }, onEvent = () => {} });
  create({ userId, sourceAgentId, ticketType, payload = {}, deadlineMs = 30 * 60e3, priority = 'normal', traceId = null });
  //  -> frozen ticket { id, userId, sourceAgentId, ticketType, state:'PENDING', payload, createdAt, deadline, priority, traceId, patch:null }
  get(ticketId);
  listPending(userId);             // -> 按 priority(urgent>high>normal>low) 再按 deadline 升序
  decide({ ticketId, actorUserId, action, patch = null, comment = null });
  //  action: 'APPROVE' | 'MODIFY' | 'REJECT'；非本人 => FORBIDDEN
  beginExecution({ ticketId });    // 仅 APPROVED|MODIFIED 可进入 EXECUTING
  complete({ ticketId, result = null });
  fail({ ticketId, error });
  escalateOverdue(nowMs = null);   // -> [{ ticketId, level, channel, at }]，重复调用不重复升级
  beginReview({ ticketId, actorUserId }); // 仅 PENDING -> REVIEWING；其它状态 ILLEGAL_TRANSITION
  events(ticketId = null);         // -> 事件数组（只追加）
}
```
**合法迁移（白名单，其它一律 `ILLEGAL_TRANSITION`）**：
```
PENDING   -> REVIEWING | APPROVED | MODIFIED | REJECTED | EXPIRED
REVIEWING -> APPROVED | MODIFIED | REJECTED | EXPIRED
APPROVED  -> EXECUTING | EXPIRED
MODIFIED  -> EXECUTING | EXPIRED
EXECUTING -> COMPLETED | FAILED
```
**超时**：`deadline` 到期后 `decide()` 抛 `EXPIRED_TICKET`；`escalateOverdue()` 按 `afterMs` 逐级升级并记录 `escalationLevel`。
**同刻优先级**：当 `afterMs[0]` 与 `deadline` 落在同一时刻（默认都是 30 分钟）时，**先升级、后过期**——即该次 `escalateOverdue()` 仍产生一级升级事件，`decide()` 到下一刻才抛 `EXPIRED_TICKET`。这样保证"手表先响、单子后死"，用户不会遇到"刚看到就失效"。

---

## 5. `services/orchestrator/src/negotiation-engine.mjs`

**职责**：Agent↔Agent 结构化协商（8 态 Task 状态机），与人类 IM 通道物理隔离。
**不变量**：协商中间过程**不产生**用户可见通知；终止时才产出 artifact；轮次/Token 硬上限，超限转 `INPUT_REQUIRED` 上抛。

```js
export const TASK_STATE = Object.freeze({
  SUBMITTED: 'SUBMITTED', WORKING: 'WORKING', INPUT_REQUIRED: 'INPUT_REQUIRED', AUTH_REQUIRED: 'AUTH_REQUIRED',
  COMPLETED: 'COMPLETED', FAILED: 'FAILED', CANCELED: 'CANCELED', REJECTED: 'REJECTED'
});
export const INTENT = Object.freeze({ PROPOSE: 'PROPOSE', COUNTER: 'COUNTER', ACCEPT: 'ACCEPT', REJECT: 'REJECT', REQUEST_INFO: 'REQUEST_INFO', REQUEST_AUTH: 'REQUEST_AUTH' });

export class NegotiationError extends Error {} // code: 'NOT_FOUND' | 'ILLEGAL_STATE' | 'WRONG_AGENT' | 'INTENT_INVALID'

export class NegotiationEngine {
  constructor({ clock = () => new Date(), maxRounds = 5, maxTokens = 4000, maxSeconds = 300 });
  open({ initiatorAgentId, initiatorUserId, responderAgentId, responderUserId, topic, payload = {} });
  //  -> frozen session { id, topic, state:'SUBMITTED', round:0, tokens:0, initiator*, responder*, turns:[], terminalReason:null, artifact:null }
  turn({ sessionId, fromAgentId, intent, payload = {}, tokens = 0 }); // -> { session, turn }
  fail({ sessionId, reason = 'FAILED' });   // 任意非终态 -> FAILED（外部调度/异常终止）
  cancel({ sessionId, reason = 'CANCELED' }); // 任意非终态 -> CANCELED（用户或上游撤销）
  get(sessionId);
  list({ state = null } = {});
  artifact(sessionId);            // -> null | { type:'AGREEMENT', summary, payload, at }
}
```
**迁移规则（必须逐条实现）**：
- `fromAgentId` 既非 initiator 也非 responder → `WRONG_AGENT`
- 终态（COMPLETED/FAILED/CANCELED/REJECTED）再收 turn → `ILLEGAL_STATE`
- `PROPOSE`：仅 `SUBMITTED` 合法 → `WORKING`；其它状态 → `ILLEGAL_STATE`
- `COUNTER`：仅 `WORKING`/`INPUT_REQUIRED` 合法；`round += 1`；**若 `round > maxRounds`** → 置 `state='INPUT_REQUIRED'`、`terminalReason='MAX_ROUNDS'`，**不抛异常**，返回该 session
- `ACCEPT`：`WORKING`/`INPUT_REQUIRED`/`AUTH_REQUIRED` → `COMPLETED`，生成 artifact `{ type:'AGREEMENT', summary: payload.summary ?? session.topic }`
- `REJECT`：任意非终态 → `REJECTED`
- `REQUEST_INFO`：→ `INPUT_REQUIRED`
- `REQUEST_AUTH`：→ `AUTH_REQUIRED`（人机混合的协议级中断点）
- Token：累计 `tokens > maxTokens` → 与 `MAX_ROUNDS` 同样处理，`terminalReason='TOKEN_BUDGET'`

---

## 6. `services/orchestrator/src/memory-store.mjs`

**职责**：三层记忆（working/episodic/semantic），**按 Agent 隔离**。
**不变量**：总管 Agent 读不到子 Agent 的原始记忆，只能拿到协调摘要（digest）。

```js
export const MEMORY_LAYER = Object.freeze({ WORKING: 'working', EPISODIC: 'episodic', SEMANTIC: 'semantic' });

export class MemoryIsolationError extends Error {} // code: 'MEMORY_ISOLATED' | 'LAYER_INVALID' | 'NOT_FOUND'

export class MemoryStore {
  constructor({ clock = () => new Date() } = {});
  write({ agentId, userId, layer, content, tags = [] }); // -> frozen record { id, agentId, userId, layer, content, tags, at, expiresAt }
  read({ agentId, requesterAgentId = agentId, userId, layer, limit = 20 }); // -> record[]
  search({ agentId, requesterAgentId = agentId, userId, layer, query, limit = 5 }); // -> [{ record, score }]
  digest({ butlerAgentId, userId });   // -> { coordination: [{ agentId, lastAt, count, summary }], contentExposed: false }
  purge({ userId });                   // -> { deleted, byAgent, tombstones }  用户删除权
}
```
**隔离规则**：`requesterAgentId !== agentId` 时 `read()`/`search()` 抛 `MEMORY_ISOLATED`；`digest()` 只返回统计摘要，绝不含 `content`。
**评分**：`search` 用确定性词频打分（`sum(termHits)/(1+log(len))` 之类），**不引入向量库**——契约只要求确定性可测，向量后端在文档中说明为生产替换点。

---

## 7. `services/orchestrator/src/egress-guard.mjs`

**职责**：出口合规护栏。所有 Agent 外发内容（含发往其它 Agent 的协商内容）统一过审。
**不变量**：`BLOCK` 一定拦截；结果必须可与 `messageId` 绑定写入审计。

```js
export const VERDICT = Object.freeze({ PASS: 'PASS', REVIEW: 'REVIEW', BLOCK: 'BLOCK' });

export class EgressBlockedError extends Error {} // code: 'EGRESS_BLOCKED'

export class EgressGuard {
  constructor({ rules = DEFAULT_RULES, clock = () => new Date(), maxLength = 4000, onAudit = () => {} });
  inspect({ content, fromAgentId, toKind, messageId = null, trustZone = null });
  //  -> { verdict, matched: [{ id, verdict, match }], contentHash, at, audit }
  assertPass(request);             // BLOCK 时抛 EgressBlockedError
}
export const DEFAULT_RULES;        // [{ id, verdict:'BLOCK'|'REVIEW', pattern: RegExp, note }]
```
**默认规则至少覆盖**：身份证号（18 位）、银行卡号（16–19 位数字）、转账/汇款话术、辱骂词表、外部链接（REVIEW）、超长内容。零宽字符/全角绕过要归一化后匹配。

---

## 8. `services/payment/src/ledger.mjs`

**职责**：不可变账本。**恰好一次**扣费是硬要求。
**不变量**：`idempotencyKey` 全局唯一；重复投递返回首次结果且 `duplicate:true`；条目只追加，绝不修改。

```js
export const ENTRY_TYPE = Object.freeze({ CHARGE: 'CHARGE', REFUND: 'REFUND', SETTLE: 'SETTLE', DEPOSIT: 'DEPOSIT' });
// R12 加法：DEPOSIT 为 v0.0.1 钱包充值入金事件；balanceOf 的 CHARGE/REFUND 口径不变（钱包用条目自行推导可用余额）。

export class LedgerError extends Error {} // code: 'INVALID_AMOUNT' | 'INVALID_TYPE' | 'KEY_REQUIRED'

export class Ledger {
  constructor({ clock = () => new Date() } = {});
  post({ idempotencyKey, type, userId, amountCents, ref = null, meta = {} });
  //  -> { entry, duplicate: boolean }   entry = { id, idempotencyKey, type, userId, amountCents, ref, meta, at }
  entries({ userId = null } = {});     // -> entry[]
  balanceOf(userId);                   // -> cents（CHARGE 累加，REFUND 扣减）
  findByIdempotencyKey(key);           // -> entry | undefined
  stats();                             // -> { count, charged, refunded, settled }
  snapshot();                          // -> 可序列化全量（供持久化/测试）
}
```
**并发**：`post` 必须同步完成「查重 + 追加」，在单线程 Node 中即为原子；文档需注明分布式部署时该键是 DB 唯一索引。

---

## 9. `services/payment/src/paid-message.mjs`

**职责**：陌生人付费消息状态机 + 两档计费。
**不变量**：**先 Agent 预审，后付费**（不可颠倒）；付费永不能绕过过滤；每笔扣费经 `ledger` 幂等键 `pay:<messageId>`。

```js
export const MSG_STATE = Object.freeze({
  INTENT: 'INTENT', AWAITING_PAYMENT: 'AWAITING_PAYMENT', REFUSED: 'REFUSED',
  PAID: 'PAID', VIEWED: 'VIEWED', REPLIED: 'REPLIED', SETTLED: 'SETTLED', REFUNDED: 'REFUNDED', EXPIRED: 'EXPIRED'
});
export const BILLING = Object.freeze({ SEND_PAY: 1, VIEW_REPLY_PAY: 2 });

export class PaidMessageError extends Error {}
// code: 'NOT_FOUND' | 'ILLEGAL_STATE' | 'DAILY_CAP_EXCEEDED' | 'NOT_PAYABLE' | 'NEEDS_SCREENING' | 'SELF_MESSAGE' | 'PAYER_MISMATCH'

export class PaidMessageService {
  constructor({ ledger, clock = () => new Date(), dailySendCap = 3, approveCapCents = 50000 });
  submitIntent({ senderUserId, senderAgentId, receiverUserId, topic, summary, billingMode, amountCents, idempotencyKey });
  screen({ messageId, verdict });   // verdict: 'PASS' | 'BLOCK'（来自目标用户的 Agent 预审）
  pay({ messageId, payerUserId });  // -> { message, charged, entry }；payerUserId 必须 === senderUserId，否则 PAYER_MISMATCH
  refuse({ messageId, reason = null }); // AWAITING_PAYMENT|INTENT -> REFUSED（目标用户/其代理拒绝，不扣费）
  view({ messageId, viewerUserId });
  reply({ messageId, replyText = null });
  settle({ messageId });
  refund({ messageId, reason = null });
  get(messageId);
  list({ receiverUserId = null, state = null } = {});
}
```
**状态机（白名单）**：
```
INTENT          -> AWAITING_PAYMENT (screen PASS) | REFUSED (screen BLOCK)
AWAITING_PAYMENT-> PAID (pay) | REFUSED | EXPIRED
PAID            -> VIEWED (view) | REFUNDED (refund)
VIEWED          -> REPLIED (reply) | REFUNDED (refund)
REPLIED         -> SETTLED (settle)
```
**计费**：`SEND_PAY` 在 `pay()` 扣费；`VIEW_REPLY_PAY` 也在 `pay()` 扣费（进入对方审批/查看动作时触发，见文档）。同一 `messageId` 重复 `pay()` 返回 `charged:false`，**不得**二次扣费。`refund()` 只允许 `PAID|VIEWED`，`REPLIED` 之后不可退（争议走人工）。

---

## 10. `services/messaging/src/{envelope,seq,dedupe}.mjs`

**职责**：人类 IM 通道的业务消息封装、序号、去重。
**不变量**：业务语义恰好一次；序号会话内单调。

```js
// envelope.mjs
export const BIZ_TYPE = Object.freeze({ APPROVAL_CARD: 'APPROVAL_CARD', STRANGER_INTENT: 'STRANGER_INTENT', AGENT_DIGEST: 'AGENT_DIGEST', NORMAL_TEXT: 'NORMAL_TEXT', SYSTEM_NOTICE: 'SYSTEM_NOTICE' });
export class EnvelopeError extends Error {} // code: 'BAD_BIZ_TYPE' | 'MISSING_FIELD'
export function makeEnvelope({ bizType, bizId, payload = {}, traceId = null, trustZone = null, agentContext = null, bizSeq = null });
export function validateEnvelope(envelope);   // -> { ok: boolean, errors: string[] }

// seq.mjs
export function fnv1a32(input);               // -> uint32
export class SeqAllocator {
  constructor({ shards = 64 });
  next({ userId, conversationId });           // -> { seq, shard }
  stats();                                    // -> { shards, allocated }
}

// dedupe.mjs
export class DedupeStore {
  constructor({ ttlMs = 864e5, clock = () => new Date() } = {});
  checkAndMark(key);                          // -> { duplicate: boolean, firstSeenAt }
  size();
  prune();
}
```
**序号语义**：`next()` 对同一 `conversationId` 返回严格递增（从 1 开始）；不同会话互不影响；`shard = fnv1a32(userId) % 64`。

---

## 11. `services/identity/src/agent-id.mjs`（v2 新增）

**职责**：Agent 自持身份（Ed25519）与 AgentCard 签发/验签——Agent 不再是一个内部字符串 id。
**不变量**：私钥不出 `createAgentIdentity` 的返回值以外；验签**只返回判定、不抛异常**（verification is a verdict）；规范化 JSON 与审计链共用同一规则。

```js
export const KEY_ALG = Object.freeze({ ED25519: 'ed25519' });
export const IDENTITY_CODE = Object.freeze({
  INVALID_KEY: 'INVALID_KEY', BAD_CARD: 'BAD_CARD', CARD_EXPIRED: 'CARD_EXPIRED',
  SIGNATURE_INVALID: 'SIGNATURE_INVALID', HASH_MISMATCH: 'HASH_MISMATCH'
});
export class IdentityError extends Error {}   // code 取 IDENTITY_CODE 之一

export function canonicalize(value);          // 稳定序列化（键排序、无空格），与 audit 一致
export function sha256Hex(text);
export function createAgentIdentity({ agentId = null, privateKeyPem = null } = {});
//  -> { agentId, keyAlg:'ed25519', publicKeyPem, privateKeyPem, createdAt }
export function agentIdFromPublicKey(publicKeyPem);   // -> `agent-<22 位 base64url(sha256(pubkey))>`
export function createAgentCard({
  identity, ownerUserId, displayName = null,
  capabilities = [], domains = [], trustCeiling = 4,
  endpoint = null, ttlMs = 7 * 864e5
});   // -> card（未签名）
export function cardHash(card);                        // -> hex（对 canonical(card) 求 sha256）
export function signAgentCard({ card, identity });     // -> { card, cardHash, alg:'ed25519', signature, signerPublicKeyPem }
export function verifyAgentCard({ signedCard, now = null });   // -> { ok, code, card }
export function signPayload({ identity, payload });    // -> { alg, signature, payloadHash }
export function verifyPayload({ publicKeyPem, payload, signature }); // -> { ok, code }
```
**签发主体（易混淆，必须区分）**：`signAgentCard({card, identity})` 中的 `identity` 是**卡片主体自己**——AgentCard 是**自签**的，只证明"该 Agent 持有对应私钥"。**平台背书不在卡片签名里**，而体现在 ①注册表中该 agentId→principal 的绑定、②`AgentGateway.issueToken` 签发的短期 token（含 domains/trustCeiling）。这正是 A2A 的核心教训：**身份可验证 ≠ 已授权**。

**规则**：`agentId` 允许两种形态——
1. **平台名**（如 `agent-lin-butler`）：由平台注册表分配的权威可读名；此时自证性由 `derivedId`（`agentIdFromPublicKey` 重算值）单独表达，`createAgentIdentity` 必须把两者都返回。
2. **自证 id**（不传 `agentId` 或 `agentId === derivedId`）：id 本身即公钥指纹，可直接跨平台验真。

`verifyAgentCard` 依次检查 ①结构 ②`cardHash === cardHash(card)` ③签名验签 ④`expiresAt > now` ⑤**若 `card.derivedId` 存在，必须等于 `agentIdFromPublicKey(card.publicKeyPem)`**；失败返回对应 `code`（`BAD_CARD` / `HASH_MISMATCH` / `SIGNATURE_INVALID` / `CARD_EXPIRED`）。
> 这条规则的目的是让「平台内可读名」与「跨平台自证」共存：内部用可读名便于运维与审计可读性，对外交换时用 `derivedId` 做可验证身份，两者不得混淆。

---

## 12. `services/gateway/src/agent-gateway.mjs`（v2 新增）

**职责**：**所有 Agent 流量的唯一入口**（内部 Agent / 第三方 Agent / 外部 A2A / MCP）。认证、限流、前置门控、出口护栏、签名审计都在这一个数据平面上，不在编排层各处散落。

```js
export const PROTOCOL = Object.freeze({ AGENT_WS: 'AGENT_WS', IM_CUSTOM: 'IM_CUSTOM', A2A_JSONRPC: 'A2A_JSONRPC', MCP_TOOLS: 'MCP_TOOLS' });
export const GATEWAY_CODE = Object.freeze({
  UNAUTHENTICATED: 'UNAUTHENTICATED', CARD_EXPIRED: 'CARD_EXPIRED', SIGNATURE_INVALID: 'SIGNATURE_INVALID',
  TOKEN_MISMATCH: 'TOKEN_MISMATCH', TOKEN_REVOKED: 'TOKEN_REVOKED', PRINCIPAL_MISMATCH: 'PRINCIPAL_MISMATCH',
  RATE_LIMITED: 'RATE_LIMITED', GATE_DENIED: 'GATE_DENIED', EGRESS_BLOCKED: 'EGRESS_BLOCKED',
  UNKNOWN_ROUTE: 'UNKNOWN_ROUTE', NOT_CONFIGURED: 'NOT_CONFIGURED'
});
export class GatewayError extends Error {}     // code 取 GATEWAY_CODE 之一

export class AgentGateway {
  constructor({ capabilityGate, egressGuard, signedAudit = null, clock = () => new Date(),
                rateLimit = { windowMs: 60_000, max: 120 } });
  issueToken({ agentId, principalUserId, domains = [], trustCeiling = 4, ttlMs = 15 * 60e3 });
  //  -> { token, agentId, principalUserId, domains, trustCeiling, issuedAt, expiresAt }
  revokeToken(token);                              // -> boolean
  authenticate({ signedCard, token, protocol });   // -> { agentId, principalUserId, domains, trustCeiling, protocol }
  route({ signedCard, token, protocol, action, payload = {} });
  //  -> { ok:true, agentId, protocol, action, decision:'ALLOW'|'REQUIRE_APPROVAL', auditSeq, normalized }
  events({ limit = null } = {});                   // -> 网关自身事件（认证/限流/拒绝/放行）
}
```
**处理管线（顺序冻结，短路）**：
1. `authenticate`：卡片验签（`verifyAgentCard`）→ 未过期；token 存在且未撤销；`token.agentId === card.agentId`；token 未过期。任一失败抛 `GatewayError`。2. 限流：同一 `agentId` 在 `windowMs` 内超过 `max` 次 → `RATE_LIMITED`。
3. 前置门控：`payload.skillId` 存在时调 `capabilityGate.evaluate({ agentId, userId: principalUserId, targetId: payload.targetId ?? null, skillId, amountCents: payload.amountCents ?? 0 })`；`DENY` → `GATE_DENIED`（携带底层 code）；`REQUIRE_APPROVAL` → **不拒绝**，`decision='REQUIRE_APPROVAL'` 且 `normalized.execute=false`。
4. 出口护栏：`payload.content` 存在时调 `egressGuard.inspect(...)`；`BLOCK` → `EGRESS_BLOCKED`。
5. 协议归一化：`normalized = { action, protocol, execute, skillId, targetId, content, meta }`（A2A/MCP 的适配在此收口，网关只做归一化，不执行具体业务）。
6. 签名审计：向 `signedAudit.append({ source:'agent-gateway', action:'route', actor: agentId, target: action, decision, detail })` 写一条；`auditSeq` 回传。

**token 下发（R5 裁定，防伪造的关键）**：

| Agent 形态 | 谁签发 token | 约束 |
|---|---|---|
| 内置 Agent | 注册中心以服务身份（mTLS/内部调用）调 `issueToken` | Agent 进程内不得自行签发；token 只含 agentId + principal + domains + trustCeiling + exp |
| 第三方托管 Agent | 用户授权（OAuth2 授权码式 consent）→ 平台签发 | 用户必须显式同意 scope；默认 15 分钟有效期；可随时 `revokeToken` |
| 外部 A2A Agent | **不签发本平台 token** | 走边界网关的受限委托令牌：默认仅 `contact` 域只读 + 提交来意，不得读记忆/日程 |

**私钥边界**：平台**不托管**第三方 Agent 私钥；内置 Agent 可使用 KMS 托管，但那只是运维便利，不构成安全边界——生产要求私钥不离开 Agent 运行域（KMS 或 TEE 二选一）。AgentCard 始终自签，平台只签 token。

**错误码到 HTTP 的映射（v2 收敛）**：认证类 → 401；`RATE_LIMITED` → 429；`GATE_DENIED` / `EGRESS_BLOCKED` / `SCOPE_VIOLATION` → 403；`UNKNOWN_ROUTE` → **400 `INVALID_ARGUMENT`**（客户端错误、不可重试；不得用 `UNIMPLEMENTED`，否则会诱导 SDK 重试或降级到别的协议）。

---

## 13. `services/gateway/src/visibility.mjs`（v2 新增）

**职责**：**读侧可见域**。权限不只是"能不能做"，还包括"以谁的视角、能看见哪些字段"。

```js
export const DOMAIN = Object.freeze({ CONTACT: 'contact', GROUP: 'group', TASK: 'task', SCHEDULE: 'schedule', MEMORY: 'memory' });
export const VISIBILITY_CODE = Object.freeze({
  VISIBILITY_DENIED: 'VISIBILITY_DENIED', DOMAIN_NOT_GRANTED: 'DOMAIN_NOT_GRANTED',
  TRUST_CEILING_EXCEEDED: 'TRUST_CEILING_EXCEEDED', SCOPE_EXPIRED: 'SCOPE_EXPIRED',
  RESOURCE_NOT_FOUND: 'RESOURCE_NOT_FOUND'
});
export class VisibilityError extends Error {}

export class VisibilityGate {
  constructor({ trustMatrix, clock = () => new Date() });
  buildScope({ principalUserId, agentId, domains, trustCeiling = 4, ttlMs = 15 * 60e3 });
  //  -> { principalUserId, agentId, domains, trustCeiling, issuedAt, expiresAt }
  evaluate({ scope, resource });
  //  resource = { domain, trustLevel = 4, id = null }
  //  -> { decision:'ALLOW'|'REDACT'|'DENY', code, domain, allowedFields, redactedFields }
  project({ scope, resource, fields = {} });
  //  -> { decision, allowedFields, redactedFields, projected }
}
```
**字段裁剪表（冻结）**：

| 资源信任档 | 允许返回的字段 |
|---|---|
| `SELF(0)` / `FAMILY(1)` | 全字段 |
| `TRUSTED(2)` | 除敏感凭据外的业务字段（默认 `id, displayName, tags, detail, endpoint`） |
| `KNOWN(3)` | `id, displayName, tags` |
| `STRANGER(4)` | 仅 `id, exists`（存在性，不含内容） |
| `MEMORY` 域（任何档） | 仅 `agentId, lastAt, count, summary`（**永不返回原始内容**） |

**判定顺序（短路）**：scope 过期 → `DENY/SCOPE_EXPIRED`；`resource.domain ∉ scope.domains` → `DENY/DOMAIN_NOT_GRANTED`；`resource.trustLevel > scope.trustCeiling` → `DENY/TRUST_CEILING_EXCEEDED`；否则按表裁剪，有字段被裁掉则 `REDACT`，否则 `ALLOW`。

---

## 14. `services/messaging/src/channel.mjs`（v2 新增）

**职责**：频道模型（WuKongIM/WKongAI 的频道类型思想）：人类与 Agent 共享消息内核，但靠**频道类型**隔离未读与投递——Agent 协商不再另开物理通道，而是独立频道类型。

```js
export const CHANNEL_TYPE = Object.freeze({ DIRECT: 'DIRECT', GROUP: 'GROUP', TASK: 'TASK', AGENT_NEGOTIATION: 'AGENT_NEGOTIATION', STRANGER_QUEUE: 'STRANGER_QUEUE' });
export const MEMBER_KIND = Object.freeze({ HUMAN: 'human', AGENT: 'agent' });
export const MESSAGE_KIND = Object.freeze({ TEXT: 'TEXT', CMD: 'CMD', AGENT_TURN: 'AGENT_TURN', APPROVAL_CARD: 'APPROVAL_CARD', INTENT: 'INTENT' });
export const CHANNEL_CODE = Object.freeze({
  CHANNEL_EXISTS: 'CHANNEL_EXISTS', CHANNEL_UNKNOWN: 'CHANNEL_UNKNOWN', TYPE_INVALID: 'TYPE_INVALID',
  MEMBER_EXISTS: 'MEMBER_EXISTS', NOT_MEMBER: 'NOT_MEMBER', POST_DENIED: 'POST_DENIED',
  AGENT_NOT_SUBSCRIBED: 'AGENT_NOT_SUBSCRIBED', CMD_NOT_PERSISTED: 'CMD_NOT_PERSISTED'
});
export class ChannelError extends Error {}

export class ChannelRegistry {
  constructor({ clock = () => new Date() });
  create({ channelId, type, ownerUserId, title = null });
  join({ channelId, memberId, kind, agentId = null, principalUserId = null, trustLevel = 4 });
  leave({ channelId, memberId });
  get(channelId);                       // -> channel | undefined
  members(channelId);                   // -> member[]
  channelsOf(memberId);                 // -> channelId[]
  post({ channelId, memberId, kind, payload = {}, seq = null });
  //  -> { stored, message, deliveredTo, unreadDelta }
  //     unreadDelta 是**整数**：本次 post 使多少人类成员各 +1（通常 0 或 1；群聊可能 >1）。
  //     message 最小集：{ id, channelId, seq, kind, fromMemberId, at, payload, stored }
  unreadFor({ memberId });              // -> { total, byChannel }
  markRead({ memberId, channelId });
  assertCanPost({ channelId, memberId, kind });   // 不合规抛 ChannelError
}
```
**语义（冻结）**：
- `CMD` 类消息：`stored=false`、**不计未读**、只投递给已订阅的 Agent 成员（用于唤醒/心跳/指令）。
- `AGENT_NEGOTIATION` 频道：**对人类成员的未读恒为 0**（协商中间态不打扰人，只在巡视态可看）；人类成员 POST 该类型频道 → `POST_DENIED`（协商只能由 Agent 发起）。
- `STRANGER_QUEUE` 频道：陌生人入站先入队，不产生对方未读，直到转为审批卡片。
- 非成员 POST → `NOT_MEMBER`；Agent 成员未订阅频道时投递被跳过并在 `deliveredTo` 中不出现。

---

## 15. `services/messaging/src/session.mjs`（v2 新增）

**职责**：**Agent 作为消息内核的一等客户端**——独立长连接、独立在线状态、独立订阅，可独立部署/第三方托管（MagicChat/WKongAI/Buzz 的共同形态）。传输层抽象，内存与 SSE 两种适配器都实现同一接口。

```js
export const SESSION_STATE = Object.freeze({ CONNECTING: 'CONNECTING', ONLINE: 'ONLINE', OFFLINE: 'OFFLINE', REVOKED: 'REVOKED' });
export const SESSION_CODE = Object.freeze({
  UNKNOWN_AGENT: 'UNKNOWN_AGENT', ALREADY_ONLINE: 'ALREADY_ONLINE', NOT_ONLINE: 'NOT_ONLINE',
  TRANSPORT_REQUIRED: 'TRANSPORT_REQUIRED', SEND_FAILED: 'SEND_FAILED'
});
export class SessionError extends Error {}

export class AgentSessionRegistry {
  constructor({ clock = () => new Date(), heartbeatTimeoutMs = 90_000, channelRegistry = null });
  connect({ agentId, principalUserId, transport, subscriptions = [], protocol = 'AGENT_WS' });
  //  transport 只需实现 { send(obj), close() }；返回 session
  heartbeat({ agentId });
  disconnect({ agentId, reason = 'client_closed' });
  subscribe({ agentId, channelId }); unsubscribe({ agentId, channelId });
  presence();                            // -> [{ agentId, state, protocol, principalUserId, subscriptions, lastSeenAt }]
  sweepStale(nowMs = null);              // 心跳超时 -> OFFLINE，返回被置离线的 agentId[]
  push({ channelId, message });           // -> { delivered:[agentId], failed:[agentId] }
}
```
**规则**：重复 `connect` 同一在线 agent → `ALREADY_ONLINE`；`transport` 缺失/不合法 → `TRANSPORT_REQUIRED`；`push` 只发给「在线 + 已订阅该频道」的 agent，`transport.send` 抛错则该 agent 置 `OFFLINE` 并计入 `failed`。

---

## 16. 审计：`services/audit/src/{audit-log,signed-audit}.mjs`（契约追认 + v2 签名扩展）

**职责**：不可变审计 = **哈希链**（防单条篡改、防截断）+ **Ed25519 签名**（防"有权改库者整链重算"、可证明"是谁写的"）。

`audit-log.mjs`（已实现，本契约追认其 API）：`AuditLog({ clock, limit, records })`、`append(entry)`、`verify()`、`records(filter)`、`replay(traceId)`、`snapshot()`、`head()`、`size()`、`trimmed()`；创世前驱 `GENESIS_HASH='GENESIS'`。

```js
// services/audit/src/signed-audit.mjs
export const SIGNED_AUDIT_CODE = Object.freeze({ SIGNER_REQUIRED: 'SIGNER_REQUIRED', CHAIN_BROKEN: 'CHAIN_BROKEN', SIGNATURE_INVALID: 'SIGNATURE_INVALID', UNKNOWN_SEQ: 'UNKNOWN_SEQ' });
export class SignedAuditError extends Error {}

export class SignedAuditLog {
  constructor({ clock = () => new Date(), identity, log = null });
  append({ source, action, actor = null, target = null, decision = null, detail = null, traceId = null });
  //  -> 冻结 entry：{ ...审计字段, signer, signature }
  verify();          // -> { ok, length, headSeq, chain:{ ok, brokenAt }, signatures:{ ok, invalidAt }, head }
  //  head = 链尾的 **hash 字符串**（hex）；headSeq 为对应序号。签名编码统一 **base64**（标准、非 url-safe）。
  checkpoint();      // -> { seq, hash, alg:'ed25519', signature, at }
  entries({ limit = null } = {});   // -> entry[]
  head();
}
```
**规则**：`signature = ed25519(identity.privateKey, entry.hash)`；`verify()` 必须同时校验链与每条签名；`chain.ok === false` 时不可只报签名通过就返回 ok。`identity` 缺失 → `SIGNER_REQUIRED`（签名审计不允许无签名模式）。

---

## 17. `services/orchestrator/src/persona-memory.mjs`（v3 新增）

**职责**：Agent 的**人格文件 + 记忆生命周期**。借鉴 nekoclaw 的 `SOUL.md + MEMORY.md + 分层记忆生命周期` 与 MindRoom 的「跨平台持久记忆」：人格是稳定的，记忆是会被晋升/衰减/遗忘的，两者必须分开存、分开授权。

**不变量**：
- **SOUL 与会话记忆分离**：SOUL（价值观/规则/边界/语气）是低频变更的人格基座，写入必须留审计；记忆是高频写入且会衰老的数据。
- **晋升不可由模型自称**：记录从 WORKING→EPISODIC→SEMANTIC 只由「时间 + 访问次数」规则决定，模型无权宣布"这条很重要"。
- **遗忘是软删除**：`ARCHIVED`/tombstone 保留，审计可证"曾经存在过"，但 `recall` 不再返回内容。

```js
export const MEMORY_STAGE = Object.freeze({ WORKING: 'WORKING', EPISODIC: 'EPISODIC', SEMANTIC: 'SEMANTIC', ARCHIVED: 'ARCHIVED' });
export const PERSONA_CODE = Object.freeze({
  SOUL_INVALID: 'SOUL_INVALID', MEMORY_ISOLATED: 'MEMORY_ISOLATED', STAGE_INVALID: 'STAGE_INVALID',
  NOT_FOUND: 'NOT_FOUND', PROMOTION_DENIED: 'PROMOTION_DENIED', TOMBSTONED: 'TOMBSTONED',
  EXPORT_INVALID: 'EXPORT_INVALID', IMPORT_INVALID: 'IMPORT_INVALID', IMPORT_REJECTED: 'IMPORT_REJECTED'
});
export class PersonaError extends Error {}

export class PersonaMemory {
  constructor({ clock = () => new Date(), identity = null });   // identity 注入后 exportPortable 带签名
  setSoul({ agentId, userId, values, rules = [], boundaries = [], voice = null });
  //  -> { soul, changed }   values 必须是非空字符串数组，否则 SOUL_INVALID
  soul({ agentId, userId });                                   // -> 冻结 soul | null
  remember({ agentId, userId, content, stage = 'WORKING', tags = [], salience = 0.5, ttlMs = null });
  recall({ agentId, requesterAgentId = agentId, userId, stage = null, query = null, limit = 10 });
  access({ agentId, recordId });                               // 访问计数 +1（晋升唯一依据）
  promoteEligible({ agentId, userId, nowMs = null });           // -> [{ recordId, from, to, reason }]
  promote({ agentId, userId, recordId });                       // 仅 WORKING→EPISODIC→SEMANTIC
  decay({ agentId, userId, nowMs = null });                     // 低 salience + 久未访问 → ARCHIVED，不删除
  forget({ agentId, userId, recordId });                        // 软删 + tombstone
  exportPortable({ agentId, userId });   // -> { version:'agentim-persona/1', agentId, soul, memories, tombstones, exportedAt, signature? }
  importPortable({ bundle, expectAgentId = null });             // -> { imported, skipped, conflicts }
  stats({ agentId, userId });
}
```
**晋升/衰减规则（冻结常量，实现不得改）**：

| 迁移 | 条件 |
|---|---|
| WORKING → EPISODIC | 创建后 ≥ 24h **且** `accesses ≥ 2` |
| EPISODIC → SEMANTIC | 创建后 ≥ 30d **且** `accesses ≥ 5` |
| → ARCHIVED（decay） | `salience < 0.2` **且** 距最后访问 ≥ 90d |

**隔离**：`requesterAgentId !== agentId` → `MEMORY_ISOLATED`（与 §6 一致）。**跨平台迁移**：`exportPortable` 产出可携带包并（在有 `identity` 时）签名；`importPortable` 校验 version 与 agentId——**version/schema 不符 → `IMPORT_INVALID`**（拒绝导入）、**agentId 不符或签名无效 → `IMPORT_REJECTED`**（拒绝认领）、tombstone 冲突记入 `conflicts` 而不是静默覆盖。`EXPORT_INVALID` 仅用于导出侧（bundle 结构非法/无法序列化）。

---

## 18. `services/orchestrator/src/discussion-engine.mjs`（v3 新增）

**职责**：**多 Agent 结构化讨论**（≥2 方，非两方协商）。借鉴 imcodes 的 `discuss / audit / review` 三种模式：协商是"达成一致"，讨论是"形成结论/发现/评审意见"，后者必须保留异议。

**不变量**：
- 参与者 ≥ 2 且唯一；非参与者不得发言。
- 每人每轮发言次数有上限（默认 1），轮次有硬上限（默认 3），超限转 `ESCALATED` **而不是静默丢弃**。
- **异议不可被"综合"掉**：`synthesize` 的 `dissent` 原样进入会议纪要；AUDIT/REVIEW 必须至少一条 `findings`，DISCUSS 必须有 `conclusion`。

```js
export const DISCUSSION_MODE = Object.freeze({ DISCUSS: 'DISCUSS', AUDIT: 'AUDIT', REVIEW: 'REVIEW' });
export const DISCUSSION_STATE = Object.freeze({ OPEN: 'OPEN', IN_ROUND: 'IN_ROUND', SYNTHESIZING: 'SYNTHESIZING', CONCLUDED: 'CONCLUDED', ESCALATED: 'ESCALATED', ABORTED: 'ABORTED' });
export const STANCE = Object.freeze({ SUPPORT: 'SUPPORT', OPPOSE: 'OPPOSE', QUESTION: 'QUESTION', EVIDENCE: 'EVIDENCE' });
export const DISCUSSION_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ILLEGAL_STATE: 'ILLEGAL_STATE', NOT_PARTICIPANT: 'NOT_PARTICIPANT',
  MODE_INVALID: 'MODE_INVALID', PARTICIPANTS_INVALID: 'PARTICIPANTS_INVALID', ROUND_LIMIT: 'ROUND_LIMIT',
  TURN_LIMIT: 'TURN_LIMIT', FINDINGS_REQUIRED: 'FINDINGS_REQUIRED', CONCLUSION_REQUIRED: 'CONCLUSION_REQUIRED'
});
export class DiscussionError extends Error {}

export class DiscussionEngine {
  constructor({ clock = () => new Date(), maxRounds = 3, maxTurnsPerParticipantPerRound = 1 });
  open({ mode, topic, participants, ownerUserId, agenda = [] });
  //  participants: [{ agentId, role }]，≥2 且 agentId 唯一
  speak({ discussionId, agentId, stance, content, evidence = [], round = null });
  nextRound({ discussionId });                                  // 开新一轮；超 maxRounds → ESCALATED
  synthesize({ discussionId, byAgentId, conclusion, findings = [], dissent = [] });
  escalate({ discussionId, reason }); abort({ discussionId, reason });
  get(discussionId); list({ mode = null, state = null } = {});
  minutes(discussionId);   // -> { topic, mode, participants, rounds:[{round, speeches}], conclusion, findings, dissent, state, openedAt, concludedAt }
}
```

---

## 19. `services/orchestrator/src/tool-policy.mjs`（v3 新增）

**职责**：**细粒度工具审批策略**。借鉴 MindRoom：审批粒度不是"一个全局金额阈值"，而是"每个 Agent × 每个 Skill 一条策略"。

**与 §3 的分工（不得混淆）**：`CapabilityGate` 判**权限**（这个 Agent 能不能调这个 Skill、信任够不够、预算够不够）；`ToolPolicy` 判**审批形态**（自动执行 / 必须人工批 / 直接拒 / 需双人批）。顺序：Gate → ToolPolicy → ApprovalEngine。

```js
export const POLICY_DECISION = Object.freeze({ AUTO: 'AUTO', APPROVE: 'APPROVE', DENY: 'DENY' });
export const TOOL_POLICY_CODE = Object.freeze({
  POLICY_MATCHED: 'POLICY_MATCHED', SKILL_NOT_COVERED: 'SKILL_NOT_COVERED', POLICY_NOT_FOUND: 'POLICY_NOT_FOUND',
  TRUST_TOO_LOW: 'TRUST_TOO_LOW', AMOUNT_EXCEEDED: 'AMOUNT_EXCEEDED', NEEDS_SECOND_APPROVER: 'NEEDS_SECOND_APPROVER'
});
export class ToolPolicyError extends Error {}

export class ToolPolicy {
  constructor({ clock = () => new Date() });
  setPolicy({ agentId, skillId, decision, maxAmountCents = null, trustLevelMax = 4, requiresDoubleApproval = false, note = null });
  removePolicy({ agentId, skillId });         // -> boolean
  policiesOf(agentId);                        // -> policy[]
  evaluate({ agentId, skillId, amountCents = 0, trustLevel = 4, hasSecondApprover = false });
  //  -> { decision, code, policy, reasons: [] }
}
```
**优先级与判定（冻结）**：`skillId` 精确策略 > agent 通配策略（`skillId='*'`）> 无策略。命中后按序：`trustLevel > trustLevelMax` → `DENY/TRUST_TOO_LOW`；`maxAmountCents !== null && amountCents > maxAmountCents` → `APPROVE/AMOUNT_EXCEEDED`；`requiresDoubleApproval && !hasSecondApprover` → `APPROVE/NEEDS_SECOND_APPROVER`；否则 `policy.decision/POLICY_MATCHED`。无任何策略 → `AUTO/SKILL_NOT_COVERED`。

---

## 22. `services/gateway/src/trusted-context.mjs`（v4 新增）

**职责**：**服务端可信上下文注入**。借鉴 MagicChat：Agent 处理一个请求时，不该自己去查关系链/群成员/授权候选——服务端把「当前会话、发送者、信任档位、能力声明、候选对象」作为**签名上下文**注入，Agent 只读不可改。

**不变量**：
- 上下文由网关签名；Agent 自造或改写上下文 → `CONTEXT_FORGED`。
- 候选对象（candidates）必须已经过 §13 可见域裁剪，**注入不等于越权**。
- 上下文有 TTL；跨 Agent 复用 → `CONTEXT_SCOPE_MISMATCH`。

```js
export const CONTEXT_CODE = Object.freeze({
  CONTEXT_INVALID: 'CONTEXT_INVALID', CONTEXT_EXPIRED: 'CONTEXT_EXPIRED',
  CONTEXT_FORGED: 'CONTEXT_FORGED', CONTEXT_SCOPE_MISMATCH: 'CONTEXT_SCOPE_MISMATCH'
});
export class TrustedContextError extends Error {}

export class TrustedContextProvider {
  constructor({ trustMatrix, registry, visibility = null, identity, clock = () => new Date(), ttlMs = 120_000 });
  build({ agentId, principalUserId, conversationId, channelType = null, members = [], senderId = null, candidates = {} });
  //  -> 冻结 context：{ contextId, issuedAt, expiresAt, agentId, principalUserId,
  //       conversation:{ id, channelType, members }, sender:{ id, trustLevel }|null,
  //       capabilities:[], trustCeiling, candidates:{ contacts?, groups?, approvals? }, signature }
  verify({ context, agentId = null });   // -> { ok, code, context }  只返回判定，不抛
}
```

---

## 23. `services/orchestrator/src/presence-policy.mjs`（v4 新增）

**职责**：**旁观与主动沉默**。借鉴 nekoclaw：Agent 在家庭群/熟人群里不该过度活跃——可以默默记录、可以只在被 @ 时说话、也可以完全不参与。

```js
export const PRESENCE_MODE = Object.freeze({ ALWAYS: 'ALWAYS', MENTION_ONLY: 'MENTION_ONLY', OBSERVE: 'OBSERVE', OFF: 'OFF' });
export const PRESENCE_CODE = Object.freeze({
  POLICY_MATCHED: 'POLICY_MATCHED', POLICY_NOT_FOUND: 'POLICY_NOT_FOUND', MODE_INVALID: 'MODE_INVALID',
  SILENCED: 'SILENCED', MENTION_REQUIRED: 'MENTION_REQUIRED'
});
export class PresencePolicyError extends Error {}

export class PresencePolicy {
  constructor({ clock = () => new Date() });
  setPolicy({ agentId, channelId, mode, note = null });
  policiesOf(agentId);
  decide({ agentId, channelId, mentioned = false, isDirect = false });
  //  -> { speak: boolean, mode, code, recordSilently: boolean, reasons: [] }
}
```
**判定（冻结）**：无策略 → `ALWAYS/POLICY_NOT_FOUND`。命中后：`OFF` → 不说话也不记录（`SILENCED`）；`OBSERVE` → `speak=false, recordSilently=true`（`SILENCED`）；`MENTION_ONLY` → `isDirect ? 说话 : speak=mentioned`（未提及 → `MENTION_REQUIRED`）；`ALWAYS` → 说话。**DIRECT 频道除 `OFF` 外一律说话**（1:1 对话里"沉默"等于失联）。

**投递集成铁律（接频道/轮次时必守）**：
1. `OBSERVE` 的消息必须进记忆/审计，但**不得产生任何用户可见信号**（正在输入/已读/回复中）——否则"记录但不打扰"立刻破功。
2. `OFF` **不得产生任何痕迹**：不进 inbox、不写审计、不加未读；"先入队再丢弃"会留下本不该存在的审计痕迹，并与 §25 的 MID_TURN 投递竞态。
3. `isDirect` 与 `mentioned` 必须来自频道层（频道类型 + @ 解析），**不能由模型自报**，否则 DIRECT 例外会被模型用来"给自己开麦"。

---

## 24. `services/orchestrator/src/model-router.mjs`（v4 新增）

**职责**：**按敏感度路由模型**。借鉴 MindRoom：敏感会话（家庭/法律/医疗/记忆）钉在本地或端侧模型，其余走云端；路由决策必须可审计。

```js
export const MODEL_TIER = Object.freeze({ LOCAL: 'LOCAL', CLOUD: 'CLOUD' });
export const ROUTE_CODE = Object.freeze({
  ROUTED_LOCAL: 'ROUTED_LOCAL', ROUTED_CLOUD: 'ROUTED_CLOUD',
  DOMAIN_SENSITIVE: 'DOMAIN_SENSITIVE', TRUST_SENSITIVE: 'TRUST_SENSITIVE', EXPLICIT_OVERRIDE: 'EXPLICIT_OVERRIDE'
});
export class ModelRouterError extends Error {}

export class ModelRouter {
  constructor({ clock = () => new Date(), sensitiveDomains = ['family', 'legal', 'medical', 'memory'], localModel = 'local-small', cloudModel = 'cloud-large' });
  setRule({ domain, tier, note = null });          // 覆盖默认敏感域判断
  route({ domain, trustLevel = 4, explicitTier = null });
  //  -> { tier, model, code, reasons: [] }
}
```
**判定（冻结）**：显式覆盖 → 该 tier（`EXPLICIT_OVERRIDE`，同时给出 reasons 让人可审）；`domain ∈ sensitiveDomains` → `LOCAL/DOMAIN_SENSITIVE`；`trustLevel <= FAMILY(1)` → `LOCAL/TRUST_SENSITIVE`（家庭事务不出端侧）；否则 `CLOUD/ROUTED_CLOUD`。

---

## 25. `services/messaging/src/turn-inbox.mjs`（v4 新增）

**职责**：**轮次内投递（mid-turn delivery）**。借鉴 Flopwire：Agent 正在执行一轮任务时，对方的消息/协商提案应当**在该轮次内到达**，而不是等到下一次用户提示。这是"协商中间态可即时注入"的落点。

```js
export const TURN_STATE = Object.freeze({ IDLE: 'IDLE', RUNNING: 'RUNNING', CLOSED: 'CLOSED' });
export const INBOX_CODE = Object.freeze({
  TURN_NOT_FOUND: 'TURN_NOT_FOUND', TURN_CLOSED: 'TURN_CLOSED', AGENT_MISMATCH: 'AGENT_MISMATCH',
  CAPACITY_EXCEEDED: 'CAPACITY_EXCEEDED', NO_ACTIVE_TURN: 'NO_ACTIVE_TURN'
});
export class TurnInboxError extends Error {}

export class TurnInbox {
  constructor({ clock = () => new Date(), capacity = 50 });
  beginTurn({ agentId, turnId = null });                  // -> turn { id, agentId, state:'RUNNING', startedAt }
  inject({ agentId, message, turnId = null });            // -> { delivered:'MID_TURN'|'QUEUED', pending }
  drain({ agentId, turnId = null });                      // -> { consumed: message[], pending }
  endTurn({ agentId, turnId = null, outcome = null });    // -> { injected, consumed, dropped, state:'CLOSED' }
  status({ agentId });                                    // -> { state, pending, turns }
}
```
**不变量**：活动轮次内的注入标记为 `MID_TURN` 并立即可被 `drain` 消费；无活动轮次时入队（`QUEUED`）等下一轮；轮次结束仍未消费 → 计入 `dropped` **并保留审计**（不静默丢失）；容量超限 → `CAPACITY_EXCEEDED`。

---

## 26. `services/identity/src/signed-envelope.mjs`（v5 新增）

**职责**：**带签名的消息信封 + 重放保护 + TOFU 密钥学习**。借鉴 Beacon 的"第三协议层（Agent 间的社交+经济胶水）"：Ed25519 签名只证明"这条消息确实由该 Agent 发出"，**不证明它有权发**——授权仍在 §12 网关与 §9 付费门控里。

```js
export const ENVELOPE_CODE = Object.freeze({
  ENVELOPE_INVALID: 'ENVELOPE_INVALID', SIGNATURE_INVALID: 'SIGNATURE_INVALID', REPLAY_DETECTED: 'REPLAY_DETECTED',
  CLOCK_SKEW: 'CLOCK_SKEW', KEY_UNKNOWN: 'KEY_UNKNOWN', TOFU_CONFLICT: 'TOFU_CONFLICT'
});
export class SignedEnvelopeError extends Error {}

export class SignedEnvelopeService {
  constructor({ identity, clock = () => new Date(), windowMs = 300_000, nonceTtlMs = 600_000 });
  sign({ toAgentId, kind, payload = {}, meta = {} });
  //  -> { v:1, from, to, nonce, at, kind, payload, meta, alg:'ed25519', publicKeyPem, signature }
  verify({ envelope, expectedTo = null });   // -> { ok, code, from, payload }；只返回判定，不抛
  learnKey({ agentId, publicKeyPem });       // TOFU：首次学习成功；已存在且不同 → throw TOFU_CONFLICT
  knownKey(agentId);                          // -> publicKeyPem | null
}
```
**判定顺序（冻结）**：结构 → 签名 → 时间窗（`|now-at| > windowMs` → `CLOCK_SKEW`）→ `expectedTo` 不符 → `ENVELOPE_INVALID` → nonce 重放（→ `REPLAY_DETECTED`）→ 发送方公钥：已知则用已知钥验签（`SIGNATURE_INVALID`），未知则 `KEY_UNKNOWN`（调用方须先 `learnKey` 或预置白名单）。
**与付费的边界**：信封通过 ≠ 可以收费/放行；付费仍走 §9 状态机与 §3 门控。**不要**把"验签通过"当作"陌生人可信"的替代品。

---

## 27. `services/orchestrator/src/relay-summary.mjs`（v5 新增）

**职责**：**第三人称转达**。借鉴 Pisces/Convia：AI 作为人际会话里的**合法第三方节点**注入摘要，**绝不冒充任何一方**。这解决的是：A 的日程 Agent 与 B 的日程 Agent 协商完成后，结果如何进入两人的 IM 会话。

```js
export const RELAY_KIND = Object.freeze({ THIRD_PERSON_RELAY: 'THIRD_PERSON_RELAY' });
export const RELAY_CODE = Object.freeze({
  RELAY_INVALID: 'RELAY_INVALID', IMPERSONATION_BLOCKED: 'IMPERSONATION_BLOCKED',
  PARTICIPANTS_REQUIRED: 'PARTICIPANTS_REQUIRED', NOT_PARTICIPANT: 'NOT_PARTICIPANT', SUMMARY_TOO_LONG: 'SUMMARY_TOO_LONG'
});
export class RelaySummaryError extends Error {}

export class RelaySummaryBuilder {
  constructor({ clock = () => new Date(), maxLength = 600 });
  build({ conversationId, participants, summarizerAgentId, topic, conclusion, pendingActions = [], approvals = [], provenance = [] });
  //  -> 冻结 relay：{ kind, attribution:'AGENT_SUMMARY', notFromHuman:true, from:summarizerAgentId,
  //        participants, topic, conclusion, pendingActions, approvals, provenance, text, hash, at }
  assertNotImpersonation({ relay, humanUserIds = [] });   // from ∈ humanUserIds → throw IMPERSONATION_BLOCKED
  render({ relay });                                       // -> 第三人称文本
}
```
**不变量**：`notFromHuman` 恒为 `true`、`attribution` 恒为 `AGENT_SUMMARY`（**不允许生成"像是人发的"消息**）；`summarizerAgentId` 必须是参与方；`provenance` 必须能追溯到"哪两个/哪几个 Agent 协商出的这个结论"；超过 `maxLength` → `SUMMARY_TOO_LONG`（摘要不是聊天记录）。

---

## 28. `services/orchestrator/src/confirmation-tier.mjs`（v5 新增）

**职责**：**确认层工具不可被 planner 预批准**。借鉴 ZLAgent：把工具分成 `PLANNER`（可自动执行）与 `CONFIRMATION`（必须人确认，**模型/规划器的"我已批准"一律无效**）。扣费、审批、对外承诺必须落在确认层。

```js
export const TOOL_TIER = Object.freeze({ PLANNER: 'PLANNER', CONFIRMATION: 'CONFIRMATION' });
export const TIER_CODE = Object.freeze({
  PLANNER_ALLOWED: 'PLANNER_ALLOWED', CONFIRMATION_REQUIRED: 'CONFIRMATION_REQUIRED',
  PLANNER_PREAPPROVAL_REJECTED: 'PLANNER_PREAPPROVAL_REJECTED', TIER_SET: 'TIER_SET'
});
export class ConfirmationTierError extends Error {}

export class ConfirmationTier {
  constructor({ clock = () => new Date() });
  setTier({ agentId, skillId, tier });     // -> policy；未登记技能默认 CONFIRMATION（fail-closed）
  tierOf({ agentId, skillId });            // -> TOOL_TIER
  evaluate({ agentId, skillId, plannerClaimsApproved = false, humanApproved = false });
  //  -> { decision:'AUTO'|'REQUIRE_APPROVAL', code, tier, reasons: [] }
}
```
**判定（冻结）**：
- 未登记技能 → `tier=CONFIRMATION`（**fail-closed**，不假设它安全）。
- `tier=PLANNER` → `AUTO/PLANNER_ALLOWED`。
- `tier=CONFIRMATION` 且 `humanApproved=true` → `AUTO/CONFIRMATION_REQUIRED`（人已批准，可执行；reasons 注明"人确认"）。
- `tier=CONFIRMATION` 且未获人批准：`REQUIRE_APPROVAL`；若 `plannerClaimsApproved=true` → code `PLANNER_PREAPPROVAL_REJECTED`（**记录这次越权主张，进审计**），否则 `CONFIRMATION_REQUIRED`。

---

## 29. `services/messaging/src/agent-mailbox.mjs`（v6 新增）

**职责**：**Agent 持久邮箱**。借鉴 Greft：会话历史归属于 **Agent 身份**而不是某次运行会话——接收方离线时消息安全暂存，进程崩溃/换机器/换框架都能靠同一邮箱恢复。

**这直接修补的已知缺陷**：v4 的 `TurnInbox` 是单进程内存态，重启即丢 pending（当时已如实登记）。本模块提供落盘版本。

```js
export const MAILBOX_CODE = Object.freeze({
  MAILBOX_UNKNOWN: 'MAILBOX_UNKNOWN', MESSAGE_INVALID: 'MESSAGE_INVALID', DUPLICATE_MESSAGE: 'DUPLICATE_MESSAGE',
  CAPACITY_EXCEEDED: 'CAPACITY_EXCEEDED', CURSOR_INVALID: 'CURSOR_INVALID'
});
export class AgentMailboxError extends Error {}

export class AgentMailboxStore {
  constructor({ dir, clock = () => new Date(), capacityPerAgent = 1000 });
  deliver({ toAgentId, message });          // -> { stored, duplicate, seq }；message 必须有非空 id
  fetch({ agentId, cursor = null, limit = 50 });   // -> { messages, nextCursor, pending }
  ack({ agentId, cursor });                 // -> { acked }  提交消费位移
  history(agentId);                         // -> 全量只读（审计用）
  stats(agentId);                           // -> { delivered, acked, pending, sizeBytes }
}
```
**不变量**：①每个 agent 一个 append-only 文件（`<dir>/<agentId>.jsonl`），新实例读取同一目录必须恢复同样的 pending；②同一 `message.id` 重复投递**不重复入队**（返回 `duplicate:true`）；③`cursor` 单调，乱序/回退 → `CURSOR_INVALID`；④容量超限 → `CAPACITY_EXCEEDED`（不静默丢弃）。

---

## 30. `services/gateway/src/delegation.mjs`（v6 新增）

**职责**：**带衰减的委托**。借鉴 AGORA 的治理层原语：总 Agent 不能把"自己的全部权限"直接交给子 Agent，委托必须**收窄**（scope 子集、时限不长于父、信任上限不放宽、跳数受限），且父级撤销要级联。

```js
export const DELEGATION_CODE = Object.freeze({
  DELEGATION_DENIED: 'DELEGATION_DENIED', SCOPE_ESCALATION: 'SCOPE_ESCALATION', EXPIRY_EXCEEDED: 'EXPIRY_EXCEEDED',
  HOP_LIMIT: 'HOP_LIMIT', PARENT_REVOKED: 'PARENT_REVOKED', TOKEN_INVALID: 'TOKEN_INVALID'
});
export class DelegationError extends Error {}

export class DelegationService {
  constructor({ clock = () => new Date() });
  delegate({ parentToken, childAgentId, principalUserId, domains, trustCeiling, ttlMs, hopLimit = 1 });
  //  -> 冻结 token：{ id, agentId, principalUserId, domains, trustCeiling, issuedAt, expiresAt, hopLimit, parentId, signature? }
  verify({ token, agentId });   // -> { ok, code, effectiveScope, chain }
  revoke({ tokenId, cascade = true });   // -> { revoked }
  chainOf(tokenId);                      // -> token[]
}
```
**衰减规则（冻结，违反即拒绝）**：`child.domains ⊆ parent.domains`（否则 `SCOPE_ESCALATION`）；`child.trustCeiling <= parent.trustCeiling`（数值越大越宽松，**不得放宽**，否则 `SCOPE_ESCALATION`）；`child.expiresAt <= parent.expiresAt`（否则 `EXPIRY_EXCEEDED`）；`hopLimit` 必须 ≥1 且每层 -1（0 → `HOP_LIMIT`）；父级被撤销 → 子级 `PARENT_REVOKED`（`cascade` 默认 true）。

---

## 31. `services/messaging/src/im-adapter.mjs`（v7 新增）

**职责**：**IM 渠道适配层**。借鉴 AstrBot / MalikClaw：业务代码（编排层）只面向统一 `MessageEvent` 编程，**不直接依赖任何平台 SDK**。这样"用腾讯云 IM PaaS 还是自托管内核"变成部署选择，而不是代码重写。

**为什么这层必须有**：v2 曾选"自托管内核"，v7 决策改为 **Phase0–1 用 PaaS + 适配层**。两者能共存的前提就是这层抽象；而**平台能力的缺口必须在适配器里显式声明**（缺失就拒绝，不静默降级）。

```js
export const ADAPTER_CODE = Object.freeze({
  ADAPTER_UNKNOWN: 'ADAPTER_UNKNOWN', ADAPTER_EXISTS: 'ADAPTER_EXISTS',
  CAPABILITY_MISSING: 'CAPABILITY_MISSING', EVENT_INVALID: 'EVENT_INVALID', PLATFORM_ERROR: 'PLATFORM_ERROR'
});
export const ADAPTER_CAPABILITY = Object.freeze({
  UNREAD_SHAPING: 'UNREAD_SHAPING',       // 能否按频道类型整形未读（AGENT_NEGOTIATION 恒 0 依赖它）
  CUSTOM_MESSAGE: 'CUSTOM_MESSAGE',       // 自定义消息类型（Agent 轮次/CMD）
  AGENT_MEMBERSHIP: 'AGENT_MEMBERSHIP',   // Agent 能否作为合法成员参与投递
  AUDIT_CHAIN: 'AUDIT_CHAIN',             // 能否把平台事件纳入自有审计链
  STREAMING: 'STREAMING',                 // 流式回复
  CARD_CALLBACK: 'CARD_CALLBACK'          // 卡片点击回调（审批输入源）
});
export class ImAdapterError extends Error {}

export function normalizeEvent({ platform, adapterId, raw });   // -> MessageEvent
export class ImAdapterRegistry {
  constructor();
  register({ adapterId, platform, capabilities = [], send, verifyEvent = null });
  capabilitiesOf(adapterId); adaptersOf(platform);
  hasCapability({ adapterId, capability });
  requireCapability({ adapterId, capability });   // 缺失 → throw CAPABILITY_MISSING
  dispatch({ adapterId, raw });                   // -> { event, platformMessageId }
}
```
**统一 MessageEvent（冻结字段）**：
```js
{ eventId, platform, adapterId, channelId, senderId, senderKind: 'human'|'agent',
  kind: 'TEXT'|'CARD'|'AGENT_TURN'|'CMD', text, card, at, replyTo, metadata, trustZone }
```
**不变量**：①`dispatch` 先 `verifyEvent`（可选）再归一化，非法 → `EVENT_INVALID`；②`send` 抛错 → `PLATFORM_ERROR`（不吞异常）；③**能力缺失必须显式报错**：例如某 PaaS 适配器未声明 `UNREAD_SHAPING`，调用方要求"协商频道人类未读恒 0"时必须 `CAPABILITY_MISSING`，**不允许假装支持**。

---

## 32. `services/orchestrator/src/agent-correspondence.mjs`（v7 新增）

**职责**：**陌生 Agent 通信的人类审批挂起**。借鉴 agentcomms / masumi-agent-messenger：Agent 之间的通信可以在**需要人决策的节点挂起**，而不是让 Agent 自行跑完全部回合；语义层（协商内容）仍由平台自己的 `negotiation-engine` 控制，中转层只做投递与挂起。

```js
export const CORRESPONDENCE_STATE = Object.freeze({
  OPEN: 'OPEN', SUSPENDED: 'SUSPENDED', APPROVED: 'APPROVED', REFUSED: 'REFUSED', EXPIRED: 'EXPIRED'
});
export const CORRESPONDENCE_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ILLEGAL_STATE: 'ILLEGAL_STATE', APPROVAL_REQUIRED: 'APPROVAL_REQUIRED',
  ALREADY_DECIDED: 'ALREADY_DECIDED', EXPIRED: 'EXPIRED'
});
export class CorrespondenceError extends Error {}

export class AgentCorrespondence {
  constructor({ clock = () => new Date(), ttlMs = 24 * 3600e3, onAudit = () => {} });
  open({ correspondenceId = null, fromAgentId, toAgentId, principalUserId, topic, payload = {}, requiresHumanApproval = true });
  //  -> 冻结 conversation { id, state, ... }：requiresHumanApproval ? SUSPENDED : APPROVED
  decide({ correspondenceId, actorUserId, action, comment = null });   // action: 'APPROVE' | 'REFUSE'
  expire({ correspondenceId, nowMs = null });
  deliverable({ correspondenceId });   // -> { ok, code }：只有 APPROVED 才能投递
  get(correspondenceId); list({ state = null } = {}); events(correspondenceId = null);
}
```
**判定（冻结）**：只有属主 `principalUserId` 能 `decide`（否则 `APPROVAL_REQUIRED` 语义不成立，实现用 `NOT_FOUND` 亦可，但**不得**让别人批）；终态再 decide → `ALREADY_DECIDED`；`SUSPENDED` 过期 → `EXPIRED`；`deliverable` 仅对 `APPROVED` 返回 ok（**挂起中的来意不得进入会话**）。

---

## 33. `services/orchestrator/src/person-runtime.mjs`（v8 新增）

**职责**：**Person-scoped Runtime**。Personal Agent 不是"一次会话里的 Agent"，而是跨天/跨周持续存在的运行时：它的执行状态必须由 Runtime 显式管理，而不是散落在对话历史里。

**三层状态（迁移口径在这一层就钉死）**：

| 层 | 生命周期 | 内容 | 是否进可迁移包 |
|---|---|---|---|
| `PERSONAL` | 年 | 用户画像、偏好、关系与边界 | ✅ 进（Context 主权归用户） |
| `TASK` | 天/小时 | 任务检查点、等待条件（外部事件/审批/日程） | ✅ 进（换实例要能续跑） |
| `WORKING` | 单次执行周期 | 当前推理工作集 | ❌ 不进（执行期状态不迁移） |

```js
export const STATE_LAYER = Object.freeze({ PERSONAL: 'PERSONAL', TASK: 'TASK', WORKING: 'WORKING' });
export const RUNTIME_TASK_STATE = Object.freeze({ CREATED: 'CREATED', RUNNING: 'RUNNING', WAITING: 'WAITING', COMPLETED: 'COMPLETED', FAILED: 'FAILED', CANCELED: 'CANCELED' });
export const RUNTIME_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ILLEGAL_STATE: 'ILLEGAL_STATE', LAYER_INVALID: 'LAYER_INVALID',
  SECRET_IN_LAYER: 'SECRET_IN_LAYER', BUNDLE_INVALID: 'BUNDLE_INVALID', VERSION_MISMATCH: 'VERSION_MISMATCH'
});
export class PersonRuntimeError extends Error {}

export class PersonRuntime {
  constructor({ userId, clock = () => new Date(), version = 'im2a-runtime/1' });
  putPersonal({ key, value, evidence = [] });        // key 以 `secret:` 开头 → SECRET_IN_LAYER（凭证归 Vault）
  putTask({ taskId, title, state = 'CREATED', checkpoint = {}, waitingFor = null });
  setWorking({ turnId, context });                   // 单次执行周期的工作集
  clearWorking();                                    // 执行结束立即失效
  transition({ taskId, to, reason = null });         // 白名单：CREATED→RUNNING→WAITING→RUNNING→COMPLETED/FAILED；任意非终态→CANCELED
  wait({ taskId, condition });                        // condition: { kind:'external_event'|'approval'|'schedule', ref, until? }
  resume({ taskId, reason = null });
  personal({ key }); task(taskId); tasks({ state = null } = {});
  snapshot();                                        // -> { version, userId, personal, tasks, exportedAt, hash }（**不含 WORKING、不含凭证**）
  restore(bundle);                                   // -> { personal, tasks }；version 不符 → VERSION_MISMATCH，hash 不符 → BUNDLE_INVALID
  metrics();                                         // Context 飞轮指标：{ personalKeys, tasksTotal, tasksCompleted, tasksWaiting }
}
```
**不变量**：①`WORKING` 永不进入 `snapshot`；②任何 `secret:*` key 写入 → `SECRET_IN_LAYER`；③终态不可回退；④`snapshot` 带 `hash`，`restore` 必须校验，损坏即拒绝（不"尽力恢复"）。

---

## 34. `services/orchestrator/src/context-lock.mjs`（v8 新增）

**职责**：**确认锁定的 Context**。借鉴 MyContext：Agent 可以自动提取与整理 Context，但**关键结论的写入权在用户**；用户确认后永久锁定，任何子 Agent 不得覆写。

```js
export const LOCK_STATE = Object.freeze({ PENDING: 'PENDING', LOCKED: 'LOCKED', CONFLICT: 'CONFLICT' });
export const CONTEXT_LOCK_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ILLEGAL_STATE: 'ILLEGAL_STATE', LOCK_IMMUTABLE: 'LOCK_IMMUTABLE',
  CONFLICT_PENDING: 'CONFLICT_PENDING', FORBIDDEN: 'FORBIDDEN', INVALID_VALUE: 'INVALID_VALUE'
});
export class ContextLockError extends Error {}

export class ContextLock {
  constructor({ ownerUserId, clock = () => new Date(), onAudit = () => {} });
  propose({ key, value, sourceAgentId, evidence = [] });
  //  未存在 → PENDING；已 LOCKED 且值相同 → 幂等返回 LOCKED；已 LOCKED 且值不同 → CONFLICT（**原值不变**）
  confirm({ key, byUserId, decision = 'LOCK' });        // 只有 ownerUserId 能确认 → LOCKED；reject → 回到未锁定
  read({ key });                                        // -> { state, value, lockedBy, lockedAt, candidate? }
  unlock({ key, byUserId, reason });                    // 必须给 reason 且留审计；解后回到未锁定
  history({ key }); stats();                            // 飞轮指标：{ pending, locked, conflicts }
}
```
**不变量**：①子 Agent 只有 `propose` 权，`confirm` 必须是属主（否则 `FORBIDDEN`）；②`LOCKED` 的 key 值**不可被任何 propose 覆写**；③冲突不自动合并、必须上抛到人；④`unlock` 是唯一的解锁路径，且必须留审计。

---

## 35. `services/gateway/src/scoped-grant.mjs`（v8 新增）

**职责**：**PACT 式 scoped 授权 + 签名回执**。付费解锁的不是"通路"，而是"被允许查看本条消息原文"这一特定 scope；每次动作生成绑定 `messageId + scope + action` 的回执，进不可变审计。

```js
export const GRANT_SCOPE = Object.freeze({ VIEW_SUMMARY: 'view_summary', VIEW_ORIGINAL: 'view_original', REPLY: 'reply' });
export const GRANT_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', SCOPE_INVALID: 'SCOPE_INVALID', SCOPE_NOT_GRANTED: 'SCOPE_NOT_GRANTED',
  EXPIRED: 'EXPIRED', ALREADY_USED: 'ALREADY_USED', RECEIPT_MISMATCH: 'RECEIPT_MISMATCH', REVOKED: 'REVOKED'
});
export class ScopedGrantError extends Error {}

export class ScopedGrant {
  constructor({ clock = () => new Date(), identity = null, maxTtlMs = 10 * 60e3 });
  grant({ messageId, granteeAgentId, scopes, ttlMs = 5 * 60e3, priceCents = 0 });
  //  -> 冻结 grant：{ id, messageId, granteeAgentId, scopes, priceCents, issuedAt, expiresAt }
  consume({ grantId, action });            // action ∈ scopes；**每个 scope 单次消耗**；返回 receipt
  //  -> { receipt: { messageId, scope, action, at, hash, signature? } }
  verifyReceipt({ receipt });              // -> { ok, code }
  revoke({ grantId }); list({ messageId = null } = {});
}
```
**不变量**：①`scopes` 必须 ⊆ 白名单，否则 `SCOPE_INVALID`；②每个 scope **单次**消耗，重复 → `ALREADY_USED`；③过期 → `EXPIRED`，撤销 → `REVOKED`；④回执 hash 绑定 `messageId+scope+action+at`，注入 `identity` 时同时签名；⑤**付费不扩大 scope**——`priceCents` 只是记录，不参与授权判定。

---

## 36. `services/account/src/account.mjs`（v0.0.1 产品版新增）

**职责**：**账户、手机号登录、实名与付费准入**。这是"能对真实用户开付费"的前置：没有实名就没有可追责主体，付费争议也立不住。

```js
export const ACCOUNT_STATE = Object.freeze({ UNVERIFIED: 'UNVERIFIED', VERIFIED: 'VERIFIED', REJECTED: 'REJECTED', SUSPENDED: 'SUSPENDED' });
export const ACCOUNT_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ALREADY_REGISTERED: 'ALREADY_REGISTERED', OTP_INVALID: 'OTP_INVALID',
  OTP_EXPIRED: 'OTP_EXPIRED', OTP_ATTEMPTS_EXCEEDED: 'OTP_ATTEMPTS_EXCEEDED',
  REALNAME_REQUIRED: 'REALNAME_REQUIRED', REALNAME_REJECTED: 'REALNAME_REJECTED',
  SESSION_INVALID: 'SESSION_INVALID', SESSION_EXPIRED: 'SESSION_EXPIRED', FORBIDDEN: 'FORBIDDEN'
});
export class AccountError extends Error {}

export class AccountService {
  constructor({ clock = () => new Date(), otp = {}, realName = {} });
  requestOtp({ phone });                       // -> { challengeId, expiresAt, devCodeOnlyInDev? }
  register({ phone, code, displayName });      // -> { account: UNVERIFIED, session }
  verifyRealName({ accountId, realName, idNumber });  // -> { state: 'VERIFIED', idNumberHash, maskedTail }
  login({ phone, code });                      // -> { account, session }
  authenticate({ accessToken });               // -> { account, session }；过期 SESSION_EXPIRED
  canUsePaidMessaging({ accountId });          // -> { ok, code }：必须 VERIFIED 且非 SUSPENDED
  suspend({ accountId, reason }); get(accountId);
}
```
**不变量**：①`idNumber` **只存 `sha256(salt + idNumber)` 与尾号掩码，绝不存明文**；②OTP 单次有效、有 TTL、有尝试次数上限；③未实名账号 `canUsePaidMessaging` 恒 `{ok:false, code:'REALNAME_REQUIRED'}`；④`SUSPENDED` 阻断登录以外的所有写操作；⑤access token 有 TTL，过期是 `SESSION_EXPIRED` 而不是"静默续期"。

---

## 37. `services/payment/src/wallet.mjs`（v0.0.1 产品版新增）

**职责**：**余额、充值、扣费与退款**。账本（§8）只记不可变流水；钱包回答"这个人现在能花多少、能不能退"。

```js
export const WALLET_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', INVALID_AMOUNT: 'INVALID_AMOUNT', INSUFFICIENT_FUNDS: 'INSUFFICIENT_FUNDS',
  REFUND_WINDOW_CLOSED: 'REFUND_WINDOW_CLOSED', DAILY_CAP_EXCEEDED: 'DAILY_CAP_EXCEEDED', SUSPENDED: 'SUSPENDED'
});
export class WalletError extends Error {}

export class Wallet {
  constructor({ clock = () => new Date(), ledger = null, dailySendCapCents = 50000, messageState = () => null });
  topUp({ userId, amountCents, idempotencyKey, channel = 'manual' });   // 充值（幂等键）
  balance({ userId });                                                  // -> { availableCents, frozenCents }
  chargeForMessage({ userId, messageId, amountCents });                 // 幂等键 pay:<messageId>
  refund({ userId, messageId, reason });                                // 仅未回复态可退
  summary({ userId });
}
```
**不变量**：①**禁止"先扣后退"**：扣费走余额扣减，退款仅限"对方尚未回复"（按注入的 `messageState` 判定）；②充值/扣费都幂等，同一幂等键重放返回首次结果；③日发送上限按自然日累计，超限 `DAILY_CAP_EXCEEDED`；④余额不允许为负；⑤每笔变动都写账本（`DEPOSIT/CHARGE/REFUND`），钱包自身不持有"影子余额"以外的真相。

---

## 38. `services/account/src/sms-provider.mjs` + `otp-delivery.mjs`（v0.0.2 新增）

**职责**：**真实短信通道与 OTP 投递策略**。v0.0.1 的 dev 验证码只能内部人用；v0.0.2 起验证码必须经可插拔通道下发，且**验证码永不离开 AccountService**（由 `otp.deliver` 回调注入）。

```js
// sms-provider.mjs
export const SMS_CODE = Object.freeze({
  SMS_FAILED: 'SMS_FAILED', SMS_TIMEOUT: 'SMS_TIMEOUT', SMS_REJECTED: 'SMS_REJECTED',
  INVALID_PHONE: 'INVALID_PHONE', PROVIDER_UNAVAILABLE: 'PROVIDER_UNAVAILABLE'
});
export class SmsError extends Error {}
export function createSmsProvider({ kind = 'dev', transport = null, timeoutMs = 3000, clock = () => new Date() });
// kind: 'dev'（记录不外发，测试/演示）| 'log'（写日志，等价验证）| 'vendor'（注入 transport({phone,code,ttlSeconds}) -> {providerMessageId}）
// 返回：{ name, kind, info(), async sendOtp({ phone, code, ttlSeconds }) -> { providerMessageId, sentAt } }
// 失败一律抛 SmsError（不吞），且不得把 code 写进错误消息
```
```js
// otp-delivery.mjs
export const OTP_DELIVERY_CODE = Object.freeze({
  OTP_COOLDOWN: 'OTP_COOLDOWN', OTP_DAILY_CAP: 'OTP_DAILY_CAP', OTP_DELIVERY_FAILED: 'OTP_DELIVERY_FAILED'
});
export class OtpDeliveryError extends Error {}
export class OtpDelivery {
  constructor({ provider, clock = () => new Date(), cooldownMs = 60_000, dailyCap = 5, onAudit = () => {} });
  deliver({ phone, code, challengeId, ttlSeconds });   // 作为 AccountService 的 otp.deliver 注入
  stats();                                             // { sent, failed, cooldownHits, dailyCapHits }
}
```
**不变量**：①同一手机号 60s 内二次请求 → `OTP_COOLDOWN`（不重发）；②同一自然日超过 `dailyCap` 次 → `OTP_DAILY_CAP`；③投递失败 → `OTP_DELIVERY_FAILED`（**不得静默成功**），且失败计数可见；④**日志/错误/审计中都不得出现验证码明文**（dev provider 的记录只能在内存里给测试读）。

---

## 39. `services/payment/src/dispute.mjs`（v0.0.2 新增）

**职责**：**退款争议与时间线举证**。§37 的钱包退款只覆盖"未回复"；已回复的争议必须走人工，且**退款只能由"已批准争议"触发**（不允许绕过窗口直接改余额）。

```js
export const DISPUTE_STATE = Object.freeze({ OPEN: 'OPEN', APPROVED: 'APPROVED', REJECTED: 'REJECTED' });
export const DISPUTE_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ILLEGAL_STATE: 'ILLEGAL_STATE', NOT_ELIGIBLE: 'NOT_ELIGIBLE',
  FORBIDDEN: 'FORBIDDEN', DUPLICATE_OPEN: 'DUPLICATE_OPEN'
});
export class DisputeError extends Error {}
export class DisputeService {
  constructor({ clock = () => new Date(), ledger, messageState = () => null, onAudit = () => {} });
  open({ messageId, userId, reason, evidence = [] });   // 仅当消息已 REPLIED/SETTLED（未回复走 §37 自助退款）→ NOT_ELIGIBLE
  resolve({ disputeId, actorUserId, decision, note = null });  // decision: 'APPROVE' | 'REJECT'
  timeline({ messageId });                              // -> [{ at, type, ref, amountCents? }]：CHARGE→…→REPLIED→REFUND
  get(disputeId); list({ state = null } = {}); stats();
}
```
**不变量**：①争议只能对**已回复/已结算**的消息开（否则引导去 §37 自助退款，返回 `NOT_ELIGIBLE`）；②同一消息只能有一个未决争议（`DUPLICATE_OPEN`）；③`APPROVE` 通过账本记 **`refund-dispute:<disputeId>`** 幂等退款（唯一真相仍是 §8），金额取该消息 `pay:<messageId>` 的扣费额；④`REJECT` 不动余额；⑤每个状态迁移写审计，`timeline` 必须能还原完整时间线。

---

## 40. `services/moderation/src/moderation.mjs`（v0.0.3 新增）

**职责**：**付费消息的审核链**。付费消息在"扣费/投递"之前必须先过审核；机器可自动拦，拿不准的转人审，被拒的可申诉。审核结论与规则/模型版本一起进审计，**审核状态机与 `§4 审批引擎` 分属两套，不复用 ticket 状态**。

```js
export const MODERATION_STATE = Object.freeze({
  RECEIVED: 'RECEIVED', MACHINE_PASSED: 'MACHINE_PASSED', HUMAN_PENDING: 'HUMAN_PENDING',
  APPROVED: 'APPROVED', REJECTED: 'REJECTED', APPEALED: 'APPEALED',
  APPEAL_GRANTED: 'APPEAL_GRANTED', APPEAL_UPHELD: 'APPEAL_UPHELD'
});
export const VERDICT = Object.freeze({ PASS: 'PASS', FLAG: 'FLAG', BLOCK: 'BLOCK' });
export const MODERATION_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND', ILLEGAL_STATE: 'ILLEGAL_STATE', ALREADY_DECIDED: 'ALREADY_DECIDED',
  FORBIDDEN: 'FORBIDDEN', SELF_REVIEW_FORBIDDEN: 'SELF_REVIEW_FORBIDDEN',
  MACHINE_UNAVAILABLE: 'MACHINE_UNAVAILABLE', APPEAL_WINDOW_CLOSED: 'APPEAL_WINDOW_CLOSED', CONTENT_REQUIRED: 'CONTENT_REQUIRED'
});
export class ModerationError extends Error {}

export class ModerationService {
  constructor({ clock = () => new Date(), machine = null, onAudit = () => {}, appealWindowMs = 7 * 24 * 3600e3 });
  submit({ messageId, senderUserId, content, channel = 'PAID_MESSAGE' });   // -> 冻结 case
  review({ caseId, actorUserId, decision, note = null });                     // 'APPROVE' | 'REJECT'
  appeal({ caseId, userId, reason });                                          // 仅 senderUserId，且状态 REJECTED/APPEAL_UPHELD
  resolveAppeal({ caseId, actorUserId, decision, note = null });               // 'GRANT' | 'UPHOLD'；不得与原审核人相同
  canDeliver({ caseId });                                                      // -> { ok, code }
  get(caseId); list({ state = null } = {}); stats();
}
```
**判定（冻结）**：`machine({messageId, content, senderUserId})` 返回 `{ verdict, ruleVersion, modelVersion, hits[] }`。
①`PASS` → `APPROVED`（自动通过也必须记录 rule/model 版本）；②`FLAG` → `HUMAN_PENDING`；③`BLOCK` → `REJECTED`（可申诉）；
④**机审不可用/抛错 → `HUMAN_PENDING`（fail-closed），绝不自动放行**；⑤`review` 仅对 `HUMAN_PENDING`，重复 → `ILLEGAL_STATE`；
⑥`appeal` 只能由发送者本人、仅对 `REJECTED`/`APPEAL_UPHELD` 且在窗口内；⑦`resolveAppeal` 的审核人不得与原审核人相同（`SELF_REVIEW_FORBIDDEN`）；
⑧**只有 `APPROVED`/`APPEAL_GRANTED` 允许投递与扣费**（`canDeliver` 为唯一判定）；⑨每次迁移写审计（含 `caseId/messageId/ruleVersion/modelVersion/actor/decision`）。

---

## 41. `services/observability/src/metrics.mjs`（v0.0.4 新增）

**职责**：**运营指标与告警**。内测阶段最怕"出了问题只能猜"：必须能看到付费漏斗、审核时效、机审失败率、退款率，并在越线时告警。

```js
export const METRIC = Object.freeze({
  PAID_SENT: 'PAID_SENT', MODERATION_PASS: 'MODERATION_PASS', MODERATION_FLAG: 'MODERATION_FLAG',
  MODERATION_BLOCK: 'MODERATION_BLOCK', MODERATION_APPROVE: 'MODERATION_APPROVE', MODERATION_REJECT: 'MODERATION_REJECT',
  CHARGE: 'CHARGE', VIEWED: 'VIEWED', REPLIED: 'REPLIED', REFUND: 'REFUND',
  DISPUTE_OPEN: 'DISPUTE_OPEN', DISPUTE_APPROVE: 'DISPUTE_APPROVE', DISPUTE_REJECT: 'DISPUTE_REJECT',
  OTP_SENT: 'OTP_SENT', OTP_FAILED: 'OTP_FAILED'
});
export class Metrics {
  constructor({ clock = () => new Date() });
  inc(name, value = 1, labels = {});
  observe(name, ms, labels = {});         // 直方图：count/min/p50/p95/max
  snapshot();                             // { counters, histograms, rates, funnel, generatedAt }
}
export const ALERT_SEVERITY = Object.freeze({ INFO: 'INFO', WARN: 'WARN', CRITICAL: 'CRITICAL' });
export function evaluateAlerts({ snapshot, gauges = {} }, rules = []);   // -> [{ id, severity, message, value, threshold }]
export const DEFAULT_ALERT_RULES;        // 见下：机审失败率/人审积压/退款率/短信失败率/对账差异
```
**判定（冻结）**：①漏斗固定为 `PAID_SENT → MODERATION_PASS|HUMAN_PENDING → CHARGE → VIEWED → REPLIED → SETTLED`，用计数器推导**转化率**（分母为 0 时返回 0，不得 NaN）；②`observe` 的 p50/p95 用最近窗口样本（窗口内样本 <1 时返回 0）；③告警规则是**纯函数**（输入快照/gauges，输出告警数组），不读环境、不发网络；④默认规则阈值：机审失败率 >10% → WARN、人审积压 >20 → WARN、退款率 >30% → CRITICAL、短信失败率 >10% → WARN、对账差异 ≠0 → CRITICAL。

---

## 42. `services/observability/src/trace.mjs`（v0.0.4 新增）

**职责**：**全链审计追溯**。客户申诉、监管调取、内部复盘都需要"从一条消息串起审核→扣费→投递→回复→退款"的证据链，而不是在四个系统里手工拼。

```js
export class AuditTrace {
  constructor({ ledger, moderation, disputes, audit, clock = () => new Date() });
  trace({ messageId });                   // -> { messageId, steps: [{ at, source, type, actor, amountCents?, ref }], complete, missing }
  byAccount({ accountId, limit = 20 });   // -> 该账号相关消息的追踪列表
}
```
**判定（冻结）**：①`steps` 按 `at` 升序，同刻按固定优先级（`moderation < ledger < reply < dispute`）；②每条 step 必须带 `source`（`'moderation'|'ledger'|'dispute'|'audit'`）与 `type`；③**缺环不是隐藏而是显式**：`complete=false` 时 `missing` 列出缺失环节（例如"有 CHARGE 无审核记录"），这对复盘比"看起来完整"更重要；④追溯是**只读**，不触发任何状态迁移。

---

## 43. 禁止事项（对所有实现者）

1. **零依赖**：不得 `import` 任何非 `node:` 模块；不得 `npm install`（本机 npm 不可用）。
2. **不得使用 `node --test`**：沙箱禁止子进程管道，会导致 `EPERM`。用 `node services/orchestrator/test/xxx.test.mjs` 直跑。
3. **不得改动他人文件**：每个模块的写入范围见任务单；越界会破坏并行协作。
4. **不得 git commit / git push**：由 Lead 统一执行。
5. **不得静默降级**：实现不了契约时，抛明确错误并回报 Lead，不要悄悄改变语义。
6. **不得在契约外增加必需参数**：可选参数可加，签名前 1–N 个位置参数必须一致。
7. **v2 起：Agent 的外部动作必须经 `AgentGateway`**：不得在网关之外直接调用 Skill、直接投递跨 principal 消息，否则限流/门控/审计三处都会漏。
8. **v2 起：Agent 代用户读数据必须带 `VisibilityScope`**：不得把「没有 scope 检查」的列表/搜索接口交给 Agent；`MEMORY` 域永不返回原始内容。
9. **v2 起：审计必须签名**：新增审计写入点一律走 `SignedAuditLog`；`AuditLog` 只作为底层链实现，不得在新代码里绕过签名。

---

## 44. 验证命令

```powershell
# 单模块（最常用，快速反馈）
node E:\GPT\agent-im\services\orchestrator\test\trust-zone.test.mjs

# 按目录过滤全量
node E:\GPT\agent-im\scripts\test.mjs --filter=orchestrator
node E:\GPT\agent-im\scripts\test.mjs --filter=payment
node E:\GPT\agent-im\scripts\test.mjs --filter=messaging

# v2 新增模块
node E:\GPT\agent-im\scripts\test.mjs --filter=identity
node E:\GPT\agent-im\scripts\test.mjs --filter=gateway
node E:\GPT\agent-im\scripts\test.mjs --filter=audit

# v3 新增模块（§17–§19）
node E:\GPT\agent-im\scripts\test.mjs --filter=persona
node E:\GPT\agent-im\scripts\test.mjs --filter=tool-policy
node E:\GPT\agent-im\scripts\test.mjs --filter=discussion

# v4 新增模块（§22–§25）
node E:\GPT\agent-im\scripts\test.mjs --filter=trusted-context
node E:\GPT\agent-im\scripts\test.mjs --filter=presence
node E:\GPT\agent-im\scripts\test.mjs --filter=model-router
node E:\GPT\agent-im\scripts\test.mjs --filter=turn-inbox

# v5 新增模块（§26–§28）
node E:\GPT\agent-im\scripts\test.mjs --filter=signed-envelope
node E:\GPT\agent-im\scripts\test.mjs --filter=relay-summary
node E:\GPT\agent-im\scripts\test.mjs --filter=confirmation-tier

# v6 新增模块（§29–§30）
node E:\GPT\agent-im\scripts\test.mjs --filter=agent-mailbox
node E:\GPT\agent-im\scripts\test.mjs --filter=delegation

# v7 新增模块（§31–§32）
node E:\GPT\agent-im\scripts\test.mjs --filter=im-adapter
node E:\GPT\agent-im\scripts\test.mjs --filter=agent-correspondence

# v0.0.4 可观测性新增模块（§41–§42）
node E:\GPT\agent-im\scripts\test.mjs --filter=metrics
node E:\GPT\agent-im\scripts\test.mjs --filter=trace

# v0.0.3 审核链新增模块（§40）
node E:\GPT\agent-im\scripts\test.mjs --filter=moderation

# v0.0.2 内测就绪新增模块（§38–§39）
node E:\GPT\agent-im\scripts\test.mjs --filter=sms-provider
node E:\GPT\agent-im\scripts\test.mjs --filter=otp-delivery
node E:\GPT\agent-im\scripts\test.mjs --filter=dispute

# v0.0.1 产品版新增模块（§36–§37）
node E:\GPT\agent-im\scripts\test.mjs --filter=account
node E:\GPT\agent-im\scripts\test.mjs --filter=wallet

# v8 新增模块（§33–§35）
node E:\GPT\agent-im\scripts\test.mjs --filter=person-runtime
node E:\GPT\agent-im\scripts\test.mjs --filter=context-lock
node E:\GPT\agent-im\scripts\test.mjs --filter=scoped-grant

# 一条命令验证（起服务 → 自检 → 打印入口）
node E:\GPT\agent-im\scripts\quickstart.mjs
node E:\GPT\agent-im\scripts\quickstart.mjs --check   # CI 用，exit 0/1

# 全量（Lead 集成阶段）
node E:\GPT\agent-im\scripts\test.mjs
```

**每个测试文件至少覆盖**：正常路径、每个错误码至少一次、状态机非法迁移、幂等/重复调用。用 `assert.throws(fn, { code: 'XXX' })` 断言错误码。
