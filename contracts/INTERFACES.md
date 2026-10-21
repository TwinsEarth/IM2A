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

## 11. 禁止事项（对所有实现者）

1. **零依赖**：不得 `import` 任何非 `node:` 模块；不得 `npm install`（本机 npm 不可用）。
2. **不得使用 `node --test`**：沙箱禁止子进程管道，会导致 `EPERM`。用 `node services/orchestrator/test/xxx.test.mjs` 直跑。
3. **不得改动他人文件**：每个模块的写入范围见任务单；越界会破坏并行协作。
4. **不得 git commit / git push**：由 Lead 统一执行。
5. **不得静默降级**：实现不了契约时，抛明确错误并回报 Lead，不要悄悄改变语义。
6. **不得在契约外增加必需参数**：可选参数可加，签名前 1–N 个位置参数必须一致。

---

## 12. 验证命令

```powershell
# 单模块（最常用，快速反馈）
node E:\GPT\agent-im\services\orchestrator\test\trust-zone.test.mjs

# 按目录过滤全量
node E:\GPT\agent-im\scripts\test.mjs --filter=orchestrator
node E:\GPT\agent-im\scripts\test.mjs --filter=payment
node E:\GPT\agent-im\scripts\test.mjs --filter=messaging

# 全量（Lead 集成阶段）
node E:\GPT\agent-im\scripts\test.mjs
```

**每个测试文件至少覆盖**：正常路径、每个错误码至少一次、状态机非法迁移、幂等/重复调用。用 `assert.throws(fn, { code: 'XXX' })` 断言错误码。
