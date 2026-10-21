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
export const ENTRY_TYPE = Object.freeze({ CHARGE: 'CHARGE', REFUND: 'REFUND', SETTLE: 'SETTLE' });

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

## 20. 禁止事项（对所有实现者）

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

## 21. 验证命令

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

# 一条命令验证（起服务 → 自检 → 打印入口）
node E:\GPT\agent-im\scripts\quickstart.mjs
node E:\GPT\agent-im\scripts\quickstart.mjs --check   # CI 用，exit 0/1

# 全量（Lead 集成阶段）
node E:\GPT\agent-im\scripts\test.mjs
```

**每个测试文件至少覆盖**：正常路径、每个错误码至少一次、状态机非法迁移、幂等/重复调用。用 `assert.throws(fn, { code: 'XXX' })` 断言错误码。
