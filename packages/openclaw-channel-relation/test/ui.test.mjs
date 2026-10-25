// SPDX-License-Identifier: MIT
//
// 三态 UI 视图模型的单元测试。
//
// 语义源：src/ui.ts（IM2A Flutter 客户端的 approval / overview / conversation 三态）。
//   - 迁移表：合法 7 条，非法 5 条（3 状态 × 4 事件 = 12 组合）；
//   - 三个 build*View 的最小可用性：状态判别、计数、卡片动作、文本拼接、分流。
//
// 运行：node --test test/ui.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  UI_STATES,
  UI_STATE_LABELS,
  UI_TRANSITIONS,
  UiTransitionError,
  buildApprovalView,
  buildConversationView,
  buildOverviewView,
  initialUiState,
  isUiState,
  switchState,
} from "../src/ui.ts";

// 独立期望表：合法迁移（7 条）。
const EXPECTED_LEGAL = {
  approval: {
    approvalsResolved: "overview",
    openConversation: "conversation",
    openOverview: "overview",
  },
  overview: {
    pendingApproval: "approval",
    openConversation: "conversation",
  },
  conversation: {
    pendingApproval: "approval",
    openOverview: "overview",
  },
};

const ALL_EVENTS = ["pendingApproval", "approvalsResolved", "openConversation", "openOverview"];

function expectTransitionError(fn, from, event, label) {
  assert.throws(
    fn,
    (err) => {
      assert.ok(err instanceof UiTransitionError, `${label}: 应抛 UiTransitionError，实际 ${err}`);
      assert.ok(err instanceof Error, `${label}: UiTransitionError 必须是 Error 子类`);
      assert.equal(err.name, "UiTransitionError", `${label}: name`);
      assert.equal(err.from, from, `${label}: from`);
      assert.equal(err.event, event, `${label}: event`);
      assert.match(err.message, /illegal ui transition/, `${label}: message`);
      assert.ok(err.message.includes(String(event)), `${label}: message 应包含事件名`);
      return true;
    },
    label,
  );
}

// ---- 三态迁移 ----

test("three-state transitions: all 12 state/event combinations match the expected table", () => {
  assert.deepEqual(UI_STATES, ["approval", "overview", "conversation"]);
  assert.deepEqual(Object.keys(EXPECTED_LEGAL).sort(), [...UI_STATES].sort());

  let legal = 0;
  let illegal = 0;
  for (const from of UI_STATES) {
    // 表本身只允许这 3 个来源状态
    assert.deepEqual(
      Object.keys(UI_TRANSITIONS[from]).sort(),
      Object.keys(EXPECTED_LEGAL[from]).sort(),
      `${from} 的迁移表与期望不一致`,
    );

    for (const event of ALL_EVENTS) {
      const expectedNext = EXPECTED_LEGAL[from][event];
      const move = () => switchState(from, { type: event });

      if (expectedNext) {
        legal += 1;
        assert.equal(move(), expectedNext, `${from} --${event}--> ${expectedNext}`);
        assert.equal(UI_TRANSITIONS[from][event], expectedNext, `UI_TRANSITIONS[${from}][${event}]`);
      } else {
        illegal += 1;
        expectTransitionError(move, from, event, `非法迁移 ${from} --${event}-->`);
      }
    }
  }
  assert.equal(legal, 7, "合法迁移必须恰好 7 条");
  assert.equal(illegal, 5, "非法组合必须恰好 5 条");
  assert.equal(legal + illegal, 12, "必须覆盖 3×4=12 种组合");
});

test("switchState rejects missing/unknown events and unknown source states explicitly", () => {
  expectTransitionError(() => switchState("approval", undefined), "approval", "<missing>", "undefined event");
  expectTransitionError(() => switchState("approval", {}), "approval", "<missing>", "event without type");
  expectTransitionError(
    () => switchState("approval", { type: "bogusEvent" }),
    "approval",
    "bogusEvent",
    "unknown event",
  );
  expectTransitionError(
    () => switchState("bogus", { type: "pendingApproval" }),
    "bogus",
    "pendingApproval",
    "unknown source state",
  );
});

test("initialUiState: valid values pass through, everything else falls back to approval", () => {
  assert.equal(initialUiState(), "approval", "默认审批态");
  for (const state of UI_STATES) assert.equal(initialUiState(state), state);
  for (const value of ["bogus", "", "Approval", " overview", undefined, null, 42]) {
    assert.equal(initialUiState(value), "approval", `非法初值 ${String(value)} 应回落 approval`);
  }
  assert.equal(isUiState("approval"), true);
  assert.equal(isUiState("overview"), true);
  assert.equal(isUiState("conversation"), true);
  for (const value of ["bogus", "", "Approval", undefined, null]) {
    assert.equal(isUiState(value), false, `isUiState(${String(value)}) 应为 false`);
  }
});

test("UI_STATE_LABELS covers all three states with human labels", () => {
  assert.deepEqual(Object.keys(UI_STATE_LABELS).sort(), [...UI_STATES].sort());
  assert.deepEqual(UI_STATE_LABELS, {
    approval: "审批态",
    overview: "巡视态",
    conversation: "会话态",
  });
  for (const state of UI_STATES) {
    assert.equal(typeof UI_STATE_LABELS[state], "string");
    assert.ok(UI_STATE_LABELS[state].length > 0);
  }
});

// ---- 审批态视图 ----

test("buildApprovalView: pending cards get three actions, terminal cards are read-only, counts add up", () => {
  const tickets = [
    {
      ticketId: "t1",
      title: "支付 500 元",
      summary: "采购审批",
      agentId: "agent-a",
      state: "PENDING",
      deadline: "2026-10-26T00:00:00Z",
    },
    { ticketId: "t2", state: "APPROVED" },
    { ticketId: "t3", title: "已过期", state: "EXPIRED" },
    { ticketId: "t4", state: "REJECTED" },
  ];
  const view = buildApprovalView(tickets);

  assert.equal(view.state, "approval");
  assert.equal(view.cards.length, 4);
  assert.equal(view.pendingCount, 1);
  assert.equal(view.resolvedCount, 3);

  const [pending, approved, expired, rejected] = view.cards;
  assert.deepEqual(
    {
      id: pending.id,
      title: pending.title,
      summary: pending.summary,
      agent: pending.agent,
      state: pending.state,
      pending: pending.pending,
      deadline: pending.deadline,
    },
    {
      id: "t1",
      title: "支付 500 元",
      summary: "采购审批",
      agent: "agent-a",
      state: "PENDING",
      pending: true,
      deadline: "2026-10-26T00:00:00Z",
    },
  );
  assert.deepEqual(pending.actions, ["approve", "modify", "reject"]);

  // 缺省字段：title="待审批"、summary=""、agent="agent"。
  assert.equal(approved.title, "待审批");
  assert.equal(approved.summary, "");
  assert.equal(approved.agent, "agent");
  assert.equal(approved.pending, false);
  assert.deepEqual(approved.actions, []);
  assert.equal(approved.deadline, undefined);

  for (const card of [approved, expired, rejected]) {
    assert.equal(card.pending, false);
    assert.deepEqual(card.actions, [], "终态卡片不得给审批动作");
  }
  assert.equal(expired.title, "已过期");
});

test("buildApprovalView: empty input is a valid empty approval view", () => {
  const view = buildApprovalView([]);
  assert.equal(view.state, "approval");
  assert.deepEqual(view.cards, []);
  assert.equal(view.pendingCount, 0);
  assert.equal(view.resolvedCount, 0);
});

// ---- 巡视态视图 ----

test("buildOverviewView: logs first, negotiations after, text and counts are derived", () => {
  const logs = [
    { at: "10:00:00", agent: "agent-a", action: "calendar.read", detail: "3 events" },
    { at: "10:01:00", agent: "agent-b", action: "purchase.quote" },
  ];
  const negotiations = [{ id: "neg-1", state: "open", round: 2 }];
  const view = buildOverviewView(logs, negotiations);

  assert.equal(view.state, "overview");
  assert.equal(view.logCount, 2);
  assert.equal(view.negotiationCount, 1);
  assert.equal(view.timeline.length, 3);
  assert.deepEqual(view.timeline[0], {
    kind: "log",
    at: "10:00:00",
    text: "agent-a calendar.read: 3 events",
  });
  assert.deepEqual(view.timeline[1], {
    kind: "log",
    at: "10:01:00",
    text: "agent-b purchase.quote",
  });
  assert.deepEqual(view.timeline[2], {
    kind: "negotiation",
    text: "negotiation neg-1 open round=2",
  });
  assert.equal(view.timeline[2].at, undefined, "协商条目没有时间戳字段");
  assert.ok(view.timeline.slice(0, 2).every((item) => item.kind === "log"));
});

test("buildOverviewView: negotiations default to empty", () => {
  const view = buildOverviewView([{ at: "11:00", agent: "a", action: "noop" }]);
  assert.equal(view.negotiationCount, 0);
  assert.equal(view.logCount, 1);
  assert.equal(view.timeline.length, 1);

  const empty = buildOverviewView([]);
  assert.deepEqual(empty.timeline, []);
  assert.equal(empty.logCount, 0);
  assert.equal(empty.negotiationCount, 0);
});

// ---- 会话态视图 ----

test("buildConversationView: human/agent/card messages are counted separately and preserved", () => {
  const messages = [
    { id: "m1", from: "user-1", text: "你好", kind: 0, mine: true },
    { id: "m2", from: "agent-a", text: "摘要：3 条", kind: 1 },
    { id: "m3", from: "agent-a", text: "审批卡片", kind: 2 },
    { id: "m4", from: "user-2", text: "第二条", kind: 0 },
  ];
  const view = buildConversationView(messages);

  assert.equal(view.state, "conversation");
  assert.equal(view.humanCount, 2);
  assert.equal(view.agentSummaryCount, 1);
  assert.equal(view.approvalCardCount, 1);
  assert.equal(view.messages.length, 4);
  assert.deepEqual(view.messages, messages, "视图保留原始消息（不合并正文）");
});

test("buildConversationView: rejects out-of-range message kinds", () => {
  assert.throws(() => buildConversationView([{ id: "m", from: "a", text: "x", kind: 3 }]), /invalid conversation message kind: 3/);
  assert.throws(() => buildConversationView([{ id: "m", from: "a", text: "x", kind: -1 }]), /invalid conversation message kind: -1/);
  assert.throws(() => buildConversationView([{ id: "m", from: "a", text: "x", kind: "0" }]), /invalid conversation message kind: 0/);
  assert.equal(buildConversationView([]).humanCount, 0);
  assert.equal(buildConversationView([]).agentSummaryCount, 0);
  assert.equal(buildConversationView([]).approvalCardCount, 0);
});
