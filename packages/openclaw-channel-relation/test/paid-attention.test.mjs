// SPDX-License-Identifier: MIT
//
// 付费注意力状态机（PaidAttentionMachine）的单元测试。
//
// 语义源：src/paid-attention.ts 顶部注释（core 的 internal/api/paid_flow.go 与
// internal/ledger/ledger.go）。测试覆盖：
//   - 合法状态迁移（billingMode 1 发送即扣费 / 2 打开才扣费 / reject / expire）；
//   - 非法迁移（ILLEGAL_TRANSITION、NOT_FOUND、INVALID_INPUT）；
//   - 幂等（同一 messageId 重复 send/open/reject 不重复扣费、不重复建单）；
//   - 发送前置决策分支（blocked / below_price / auto_rejected）及其优先级。
//
// 运行：node --test test/paid-attention.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  ATTENTION_LEVEL_NAMES,
  PAID_TRANSITIONS,
  PaidAttentionError,
  PaidAttentionMachine,
} from "../src/paid-attention.ts";

function sendInput(overrides = {}) {
  return {
    messageId: "m1",
    senderId: "sender-1",
    receiverId: "receiver-1",
    content: "hello",
    billingMode: 1,
    amountCents: 100,
    ...overrides,
  };
}

/** 断言抛出 PaidAttentionError 且 code 精确匹配。 */
function expectError(fn, code, label) {
  assert.throws(
    fn,
    (err) => {
      assert.ok(err instanceof PaidAttentionError, `${label}: 应抛 PaidAttentionError，实际 ${err}`);
      assert.equal(err.name, "PaidAttentionError", `${label}: name`);
      assert.equal(err.code, code, `${label}: code`);
      return true;
    },
    label,
  );
}

function machine(clock) {
  const state = { now: clock ?? 1_000_000 };
  const m = new PaidAttentionMachine({ now: () => state.now });
  return { m, state };
}

// ---- 合法迁移 ----

test("billingMode=1: send charges immediately, open does not double charge, reject refunds once", () => {
  const { m } = machine();
  const sent = m.send(sendInput({ billingMode: 1 }));
  assert.deepEqual(sent, { status: "charged", messageId: "m1", ledgerStatus: "charged" });
  assert.equal(m.events.length, 2, "send 产生 decision + ledger 两个事件");
  assert.deepEqual(
    m.events.map((e) => [e.type, e.ledgerStatus ?? e.status]),
    [
      ["decision", "charged"],
      ["ledger", "charged"],
    ],
  );

  const rec = m.get("m1");
  assert.equal(rec.status, "charged");
  assert.equal(rec.ledgerStatus, "charged");
  assert.equal(rec.billingMode, 1);
  assert.equal(rec.amountCents, 100);
  assert.equal(rec.refunded, false);
  assert.equal(rec.senderId, "sender-1");
  assert.equal(rec.receiverId, "receiver-1");
  assert.equal(rec.createdAtUnix, 1_000_000);

  // 发送即扣费：打开不重复扣，也不产生新事件。
  const opened = m.open("m1");
  assert.deepEqual(opened, { messageId: "m1", status: "opened", charged: false });
  assert.equal(m.events.length, 2, "mode1 的 open 不应产生扣费事件");
  assert.equal(m.get("m1").ledgerStatus, "charged");

  const rejected = m.reject("m1", "user rejected");
  assert.deepEqual(rejected, { messageId: "m1", status: "rejected", refunded: true });
  assert.equal(m.get("m1").status, "rejected");
  assert.equal(m.get("m1").ledgerStatus, "refunded");
  assert.equal(m.get("m1").refunded, true);
  assert.equal(m.events.length, 4, "reject 追加 paid_message + ledger(refunded)");
  assert.deepEqual(
    m.events.slice(-2).map((e) => [e.type, e.status ?? e.ledgerStatus, e.reason]),
    [
      ["paid_message", "rejected", "user rejected"],
      ["ledger", "refunded", undefined],
    ],
  );
});

test("billingMode=2: send is pending/delivered, first open charges exactly once, message status stays delivered", () => {
  const { m } = machine();
  const sent = m.send(sendInput({ billingMode: 2 }));
  assert.deepEqual(sent, { status: "delivered", messageId: "m1", ledgerStatus: "pending" });
  assert.equal(m.get("m1").status, "delivered");
  assert.equal(m.get("m1").ledgerStatus, "pending");
  assert.equal(m.get("m1").createdAtUnix, 1_000_000);

  const opened = m.open("m1");
  assert.deepEqual(opened, { messageId: "m1", status: "charged", charged: true });
  assert.equal(m.get("m1").ledgerStatus, "charged");
  assert.equal(m.get("m1").status, "delivered", "打开只改变账本，不改变 core 的消息状态");
  assert.equal(m.events.filter((e) => e.type === "ledger" && e.ledgerStatus === "charged").length, 1);

  const again = m.open("m1");
  assert.equal(again.status, "charged");
  assert.equal(again.charged, true);
  assert.equal(again.idempotent, true, "重复 open 必须幂等");
  assert.equal(
    m.events.filter((e) => e.type === "ledger" && e.ledgerStatus === "charged").length,
    1,
    "重复 open 不得二次扣费",
  );

  const rejected = m.reject("m1");
  assert.deepEqual(rejected, { messageId: "m1", status: "rejected", refunded: true });
  assert.equal(m.get("m1").ledgerStatus, "refunded");
  assert.equal(m.get("m1").refunded, true);
});

test("reject before open: no refund, ledger stays pending, message becomes rejected", () => {
  const { m } = machine();
  m.send(sendInput({ billingMode: 2 }));
  const rejected = m.reject("m1");
  assert.deepEqual(rejected, { messageId: "m1", status: "rejected", refunded: false });
  assert.equal(m.get("m1").ledgerStatus, "pending");
  assert.equal(m.get("m1").refunded, false);
});

test("expire: only delivered messages, boundary is age >= maxAge, charged ledger is refunded", () => {
  const { m, state } = machine();
  m.send(sendInput({ messageId: "mode1", billingMode: 1 }));
  m.send(sendInput({ messageId: "mode2", billingMode: 2 }));

  state.now += 86_399;
  let expired = m.expire();
  assert.deepEqual(expired, { expiredCount: 0, messageIds: [] }, "未到 maxAge 不得过期");

  state.now += 1; // age == 86400，按 `now - createdAt < maxAge` 的否定分支应过期
  expired = m.expire();
  assert.deepEqual(expired, { expiredCount: 1, messageIds: ["mode2"] });
  assert.equal(m.get("mode2").status, "expired");
  assert.equal(m.get("mode1").status, "charged", "core 只清理 delivered，mode1 的 charged 不清理");
  assert.equal(m.get("mode2").ledgerStatus, "pending");

  // 重复 expire：已经 expired 的不再计数。
  assert.deepEqual(m.expire(), { expiredCount: 0, messageIds: [] });
});

test("expire after open (mode2): refunds the charged ledger and emits ledger→paid_message", () => {
  const { m, state } = machine();
  m.send(sendInput({ billingMode: 2 }));
  m.open("m1");
  state.now += 86_400;
  const expired = m.expire();
  assert.deepEqual(expired, { expiredCount: 1, messageIds: ["m1"] });
  assert.equal(m.get("m1").status, "expired");
  assert.equal(m.get("m1").ledgerStatus, "refunded");
  assert.equal(m.get("m1").refunded, true);
  assert.deepEqual(
    m.events.slice(-2).map((e) => [e.type, e.status ?? e.ledgerStatus]),
    [
      ["ledger", "refunded"],
      ["paid_message", "expired"],
    ],
  );
});

test("expire supports explicit now/maxAge and a configured default age", () => {
  const { m } = machine(10_000);
  m.send(sendInput({ billingMode: 2 }));
  // 显式参数覆盖注入时钟：10_000 + 100，maxAge 50 → 过期。
  assert.deepEqual(m.expire(10_100, 50), { expiredCount: 1, messageIds: ["m1"] });

  const custom = new PaidAttentionMachine({ now: () => 5_000, defaultMaxAgeSec: 10 });
  custom.send(sendInput({ messageId: "m2", billingMode: 2 }));
  assert.deepEqual(custom.expire(5_009), { expiredCount: 0, messageIds: [] });
  assert.deepEqual(custom.expire(5_010), { expiredCount: 1, messageIds: ["m2"] });
});

// ---- 非法迁移 ----

test("illegal transitions: open/reject after terminal states and missing messages", () => {
  const { m } = machine();

  // 未创建 → NOT_FOUND
  expectError(() => m.open("nope"), "NOT_FOUND", "open unknown");
  expectError(() => m.reject("nope"), "NOT_FOUND", "reject unknown");
  assert.equal(m.get("nope"), undefined);

  // rejected 后 open → ILLEGAL_TRANSITION
  m.send(sendInput({ messageId: "rejected-1", billingMode: 2 }));
  m.reject("rejected-1");
  expectError(() => m.open("rejected-1"), "ILLEGAL_TRANSITION", "open rejected");

  // expired 后 open/reject → ILLEGAL_TRANSITION
  const second = machine();
  second.m.send(sendInput({ messageId: "expired-1", billingMode: 2 }));
  second.state.now += 86_400;
  second.m.expire();
  expectError(() => second.m.open("expired-1"), "ILLEGAL_TRANSITION", "open expired");
  expectError(() => second.m.reject("expired-1"), "ILLEGAL_TRANSITION", "reject expired");

  // 已知差异（文件头注释 1）：同一 messageId 重复 reject 是幂等重放，不是错误。
  assert.equal(m.reject("rejected-1").idempotent, true);
});

test("invalid input: INVALID_INPUT for malformed send/expire, and nothing is recorded", () => {
  const { m } = machine();

  expectError(() => m.send(null), "INVALID_INPUT", "null input");
  expectError(() => m.send(undefined), "INVALID_INPUT", "undefined input");
  for (const [label, input] of [
    ["missing messageId", sendInput({ messageId: "" })],
    ["non-string messageId", sendInput({ messageId: 123 })],
    ["billingMode 0", sendInput({ billingMode: 0 })],
    ["billingMode 3", sendInput({ billingMode: 3 })],
    ["amount 0", sendInput({ amountCents: 0 })],
    ["amount negative", sendInput({ amountCents: -1 })],
    ["amount NaN", sendInput({ amountCents: Number.NaN })],
    ["amount Infinity", sendInput({ amountCents: Number.POSITIVE_INFINITY })],
    ["amount string", sendInput({ amountCents: "100" })],
    ["attentionLevel 4", sendInput({ attentionLevel: 4 })],
    ["attentionLevel -1", sendInput({ attentionLevel: -1 })],
    ["attentionLevel 1.5", sendInput({ attentionLevel: 1.5 })],
    ["attentionLevel string", sendInput({ attentionLevel: "2" })],
  ]) {
    expectError(() => m.send(input), "INVALID_INPUT", label);
  }

  assert.equal(m.events.length, 0, "非法 send 不得产生事件");
  assert.equal(m.get("m1"), undefined, "非法 send 不得建单");

  expectError(() => m.expire(1_000_000, 0), "INVALID_INPUT", "maxAge 0");
  expectError(() => m.expire(1_000_000, -1), "INVALID_INPUT", "maxAge negative");
});

test("validation runs before replay: a malformed retry of the same messageId still throws", () => {
  const { m } = machine();
  m.send(sendInput());
  // 幂等键已存在，但第二次输入本身非法 → INVALID_INPUT（不返回首次结果）。
  expectError(() => m.send(sendInput({ amountCents: 0 })), "INVALID_INPUT", "replay with invalid amount");
  assert.equal(m.get("m1").amountCents, 100);
  assert.equal(m.events.length, 2);
});

// ---- 幂等 ----

test("idempotent send: same messageId replays first result, never re-charges or re-creates", () => {
  const { m } = machine();
  const first = m.send(sendInput({ billingMode: 1, amountCents: 100 }));
  const eventsAfterFirst = m.events.length;

  const second = m.send(sendInput({ billingMode: 1, amountCents: 999, content: "changed" }));
  assert.deepEqual(
    { status: second.status, messageId: second.messageId, ledgerStatus: second.ledgerStatus },
    { status: first.status, messageId: first.messageId, ledgerStatus: first.ledgerStatus },
    "重放必须返回首次结果",
  );
  assert.equal(second.idempotent, true);
  assert.equal(m.events.length, eventsAfterFirst, "幂等重放不得追加事件");
  assert.equal(m.get("m1").amountCents, 100, "重放不得改变已建单金额");
  assert.equal(
    m.events.filter((e) => e.type === "ledger" && e.ledgerStatus === "charged").length,
    1,
    "同一 messageId 只扣一次",
  );
});

test("idempotent rejected decision: first decision is replayed for the same messageId", () => {
  const { m } = machine();
  const first = m.send(sendInput({ amountCents: 100 }), { minPriceCents: 500 });
  assert.equal(first.status, "below_price");
  assert.equal(first.messageId, "");
  assert.equal(m.get("m1"), undefined);

  const second = m.send(sendInput({ amountCents: 900 }), { minPriceCents: 500 });
  assert.equal(second.status, "below_price", "同一 messageId 的首次拒绝决策被重放");
  assert.equal(second.idempotent, true);
  assert.equal(m.get("m1"), undefined, "被拒消息不建单");
  assert.equal(m.events.length, 1, "只保留首次 decision 事件");
});

test("idempotent reject/open: repeated calls do not duplicate refunds or ledger entries", () => {
  const { m } = machine();
  m.send(sendInput({ billingMode: 2 }));
  m.open("m1");
  const before = m.events.length;

  const r1 = m.reject("m1");
  const afterReject = m.events.length;
  const r2 = m.reject("m1");
  assert.deepEqual(r2, { ...r1, idempotent: true });
  assert.equal(r1.refunded, true);
  assert.equal(r2.refunded, true, "幂等重放返回首次的 refunded 值");
  assert.equal(m.events.length, afterReject, "重复 reject 不得二次退款/追加事件");
  assert.ok(afterReject > before);
  assert.equal(m.get("m1").ledgerStatus, "refunded");
});

test("different messageIds are independent (no cross-talk)", () => {
  const { m } = machine();
  m.send(sendInput({ messageId: "a", billingMode: 1 }));
  m.send(sendInput({ messageId: "b", billingMode: 2 }));
  m.reject("a");
  assert.equal(m.get("a").status, "rejected");
  assert.equal(m.get("a").ledgerStatus, "refunded");
  assert.equal(m.get("b").status, "delivered");
  assert.equal(m.get("b").ledgerStatus, "pending");
  assert.equal(m.open("b").charged, true);
});

// ---- 发送前置决策分支 ----

test("decision branches: compliance → min price → blacklist → auto-reject, first hit wins", () => {
  const policy = {
    minPriceCents: 500,
    autoRejectAboveCents: 10_000,
    blockedSenders: ["blocked-sender"],
    senderBlacklisted: false,
  };

  // 1) compliance 预审最优先
  {
    const { m } = machine();
    const r = m.send(
      sendInput({ amountCents: 1, compliancePassed: false, senderId: "blocked-sender" }),
      policy,
    );
    assert.equal(r.status, "blocked");
    assert.match(r.reason, /compliance/i);
    assert.equal(m.events.length, 1, "仅 decision 事件");
    assert.equal(m.events[0].type, "decision");
    assert.equal(m.events[0].messageId, "");
  }

  // 2) 低于接收方定价（严格小于才拒；等于放行）
  {
    const { m } = machine();
    const r = m.send(sendInput({ amountCents: 499, senderId: "blocked-sender" }), policy);
    assert.equal(r.status, "below_price", "定价检查先于黑名单");
    assert.match(r.reason, /500/);
    assert.equal(m.get("m1"), undefined);
    assert.equal(m.send(sendInput({ messageId: "ok", amountCents: 500 }), policy).status, "charged");
  }

  // 3) 黑名单（内存列表 + core PG 结果），先于自动拒收
  {
    const { m } = machine();
    const r = m.send(sendInput({ senderId: "blocked-sender", amountCents: 50_000 }), policy);
    assert.equal(r.status, "blocked");
    assert.match(r.reason, /blocked/i);

    const numeric = m.send(
      sendInput({ messageId: "numeric", senderId: 42, amountCents: 600 }),
      { blockedSenders: ["42"] },
    );
    assert.equal(numeric.status, "blocked", "senderId 以字符串形式比对");

    const pg = m.send(
      sendInput({ messageId: "pg", amountCents: 600 }),
      { senderBlacklisted: true },
    );
    assert.equal(pg.status, "blocked", "core PG blacklist 结果直接拦截");
  }

  // 4) 自动拒收阈值（严格 >=）
  {
    const { m } = machine();
    assert.equal(m.send(sendInput({ amountCents: 10_000 }), policy).status, "auto_rejected");
    assert.equal(m.send(sendInput({ messageId: "below", amountCents: 9_999 }), policy).status, "charged");
    assert.equal(
      m.send(sendInput({ messageId: "disabled", amountCents: 10 ** 9 }), { autoRejectAboveCents: 0 }).status,
      "charged",
      "0 = 不启用",
    );
  }

  // 5) 全部通过 → 建单
  {
    const { m } = machine();
    const r = m.send(sendInput({ amountCents: 1_000, attentionLevel: 3 }), policy);
    assert.deepEqual(r, { status: "charged", messageId: "m1", ledgerStatus: "charged" });
    assert.equal(m.get("m1").attentionLevel, 3);
  }
});

test("decision rejection emits a reason and leaves no message record", () => {
  for (const [label, input, policy, expected] of [
    ["compliance", sendInput({ compliancePassed: false }), {}, "blocked"],
    ["price", sendInput({ amountCents: 1 }), { minPriceCents: 2 }, "below_price"],
    ["blacklist", sendInput({ senderId: "x" }), { blockedSenders: ["x"] }, "blocked"],
    ["auto-reject", sendInput({ amountCents: 5 }), { autoRejectAboveCents: 5 }, "auto_rejected"],
  ]) {
    const { m } = machine(); // 每个分支独立状态机，避免同一 messageId 的幂等重放干扰
    const r = m.send(input, policy);
    assert.equal(r.status, expected, label);
    assert.equal(r.messageId, "", `${label}: 拒绝结果不带 messageId`);
    assert.equal(typeof r.reason, "string", `${label}: reason 必填`);
    assert.ok(r.reason.length > 0, `${label}: reason 非空`);
    assert.equal(m.get("m1"), undefined, `${label}: 拒绝不得建单`);
    assert.equal(m.events.length, 1, `${label}: 仅一条 decision 事件`);
  }
});

// ---- 状态机表与常量 ----

test("PAID_TRANSITIONS table and attention level names are stable", () => {
  assert.deepEqual(Object.keys(PAID_TRANSITIONS).sort(), ["", "charged", "delivered", "expired", "rejected"]);
  assert.equal(PAID_TRANSITIONS[""].send_mode1, "charged");
  assert.equal(PAID_TRANSITIONS[""].send_mode2, "delivered");
  assert.equal(PAID_TRANSITIONS.charged.open, null, "mode1 打开不改变消息状态");
  assert.equal(PAID_TRANSITIONS.charged.reject, "rejected");
  assert.equal(PAID_TRANSITIONS.charged.expire, null, "core 不清理 mode1 的 charged");
  assert.equal(PAID_TRANSITIONS.delivered.open, null, "mode2 打开只改账本");
  assert.equal(PAID_TRANSITIONS.delivered.expire, "expired");
  assert.equal(PAID_TRANSITIONS.rejected.reject, "rejected");
  assert.deepEqual(PAID_TRANSITIONS.expired, {});

  assert.deepEqual(ATTENTION_LEVEL_NAMES, ["low", "medium", "high", "urgent"]);

  // 观测到的迁移与表一致。
  const { m, state } = machine();
  m.send(sendInput({ messageId: "mode2", billingMode: 2 }));
  assert.equal(m.get("mode2").status, PAID_TRANSITIONS[""].send_mode2);
  m.open("mode2");
  assert.equal(m.get("mode2").status, "delivered", "open 在表中是 null（状态不变）");
  state.now += 86_400;
  m.expire();
  assert.equal(m.get("mode2").status, PAID_TRANSITIONS.delivered.expire);
});
