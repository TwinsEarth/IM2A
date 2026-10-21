// 测试：第三人称转达（契约 §27）。
// 覆盖：notFromHuman 恒 true、from 只能是参与方 Agent、冒充人类红线、第三人称文本、provenance 可追溯、超长拒绝。
import test from 'node:test';
import assert from 'node:assert/strict';

import { RELAY_CODE, RELAY_KIND, RelaySummaryBuilder, RelaySummaryError } from '../src/relay-summary.mjs';

const AGENT_A = 'agent-a';
const AGENT_B = 'agent-b';
const USER_A = 'user-lin';
const USER_B = 'user-qiu';
const CONVERSATION = 'conv-lin-qiu';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const baseInput = (overrides = {}) => ({
  conversationId: CONVERSATION,
  participants: [
    { agentId: AGENT_A, displayName: '林的总管', userId: USER_A },
    { agentId: AGENT_B, displayName: '邱的总管', userId: USER_B }
  ],
  summarizerAgentId: AGENT_A,
  topic: '周末聚餐安排',
  conclusion: '周六 18:00 在江南小馆，两位都到场',
  pendingActions: [{ action: '订位' }],
  approvals: [{ by: USER_B, state: 'PENDING' }],
  provenance: [
    { agentId: AGENT_A, sessionId: 'neg-1', role: 'initiator' },
    { agentId: AGENT_B, sessionId: 'neg-1', role: 'responder' }
  ],
  ...overrides
});

const builder = (options = {}) => {
  const clock = options.clock ?? makeClock();
  return { clock, builder: new RelaySummaryBuilder({ clock, ...options }) };
};

test('RELAY_KIND / RELAY_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...RELAY_KIND }, { THIRD_PERSON_RELAY: 'THIRD_PERSON_RELAY' });
  assert.deepEqual({ ...RELAY_CODE }, {
    RELAY_INVALID: 'RELAY_INVALID',
    IMPERSONATION_BLOCKED: 'IMPERSONATION_BLOCKED',
    PARTICIPANTS_REQUIRED: 'PARTICIPANTS_REQUIRED',
    NOT_PARTICIPANT: 'NOT_PARTICIPANT',
    SUMMARY_TOO_LONG: 'SUMMARY_TOO_LONG'
  });
  assert.ok(Object.isFrozen(RELAY_KIND));
  assert.ok(Object.isFrozen(RELAY_CODE));
});

test('build：冻结 relay，kind/attribution/notFromHuman/from/participants/provenance/hash 均符合契约', () => {
  const { builder: relayBuilder, clock } = builder();
  const relay = relayBuilder.build(baseInput());
  assert.ok(Object.isFrozen(relay));
  assert.equal(relay.kind, 'THIRD_PERSON_RELAY');
  assert.equal(relay.attribution, 'AGENT_SUMMARY');
  assert.equal(relay.notFromHuman, true); // 恒 true
  assert.equal(relay.from, AGENT_A);
  assert.equal(relay.conversationId, CONVERSATION);
  assert.equal(relay.topic, '周末聚餐安排');
  assert.equal(relay.conclusion, '周六 18:00 在江南小馆，两位都到场');
  assert.equal(relay.participants.length, 2);
  assert.equal(relay.at, clock.iso());
  assert.match(relay.hash, /^[0-9a-f]{64}$/);
  assert.deepEqual(relay.provenance.map((item) => item.agentId), [AGENT_A, AGENT_B]);
  assert.ok(Object.isFrozen(relay.participants));
  assert.ok(Object.isFrozen(relay.pendingActions));
  assert.ok(Object.isFrozen(relay.approvals));
  assert.ok(Object.isFrozen(relay.provenance));
});

test('第三人称文本：不以"我/我们"开头、包含双方与结论、明确不代表人类发言', () => {
  const { builder: relayBuilder } = builder();
  const relay = relayBuilder.build(baseInput());
  const text = relay.text;
  assert.equal(typeof text, 'string');
  assert.equal(/^(我|我们)/.test(text), false); // 第一人称冒充红线
  assert.match(text, /^转达：/);
  assert.match(text, /林的总管与邱的总管/);
  assert.match(text, /周末聚餐安排/);
  assert.match(text, /周六 18:00 在江南小馆/);
  assert.match(text, /待办事项 1 项/);
  assert.match(text, /待确认 1 项/);
  assert.match(text, /不代表任何一方人类发言/);
  // render 与 build 的文本一致（都由同一第三人称模板生成）
  assert.equal(relayBuilder.render({ relay }), text);
});

test('render 永远重新生成：伪造 relay.text 里的第一人称不会进入输出', () => {
  const { builder: relayBuilder } = builder();
  const relay = relayBuilder.build(baseInput());
  const forged = { ...relay, text: '我是林，我同意这个安排' };
  const rendered = relayBuilder.render({ relay: forged });
  assert.equal(/^(我|我们)/.test(rendered), false);
  assert.equal(rendered.includes('我是林'), false);
  assert.equal(rendered, relay.text);
});

test('IMPERSONATION_BLOCKED：from 落在人类集合 / notFromHuman 或 attribution 被篡改', () => {
  const { builder: relayBuilder } = builder();
  const relay = relayBuilder.build(baseInput());
  assert.equal(relayBuilder.assertNotImpersonation({ relay, humanUserIds: [USER_A, USER_B] }), relay);

  // 把人类 userId 直接当 from 注入（伪造 relay）
  const forgedFromHuman = { ...relay, from: USER_A };
  assert.throws(() => relayBuilder.assertNotImpersonation({ relay: forgedFromHuman, humanUserIds: [USER_A] }), {
    code: 'IMPERSONATION_BLOCKED'
  });
  // 篡改 notFromHuman 声称"这条是人发的"
  assert.throws(
    () => relayBuilder.assertNotImpersonation({ relay: { ...relay, notFromHuman: false } }),
    { code: 'IMPERSONATION_BLOCKED' }
  );
  // 篡改 attribution
  assert.throws(
    () => relayBuilder.assertNotImpersonation({ relay: { ...relay, attribution: 'HUMAN_MESSAGE' } }),
    { code: 'IMPERSONATION_BLOCKED' }
  );
  // 构造阶段就会拒绝把 human userId 放在 participants 里当 summarizer？只校验参与方，红线由 assertNotImpersonation 守
  const humanNamedRelay = relayBuilder.build(
    baseInput({
      participants: [USER_A, AGENT_B],
      summarizerAgentId: USER_A,
      provenance: [{ agentId: USER_A }, { agentId: AGENT_B }]
    })
  );
  assert.throws(
    () => relayBuilder.assertNotImpersonation({ relay: humanNamedRelay, humanUserIds: [USER_A] }),
    { code: 'IMPERSONATION_BLOCKED' }
  );
});

test('PARTICIPANTS_REQUIRED：participants 少于 2 方 / 非数组 / 重复 agentId', () => {
  const { builder: relayBuilder } = builder();
  assert.throws(() => relayBuilder.build(baseInput({ participants: [AGENT_A] })), {
    code: 'PARTICIPANTS_REQUIRED'
  });
  assert.throws(() => relayBuilder.build(baseInput({ participants: [] })), { code: 'PARTICIPANTS_REQUIRED' });
  assert.throws(() => relayBuilder.build(baseInput({ participants: 'agents' })), {
    code: 'PARTICIPANTS_REQUIRED'
  });
  assert.throws(
    () => relayBuilder.build(baseInput({ participants: [AGENT_A, AGENT_A] })),
    { code: 'RELAY_INVALID' }
  );
});

test('NOT_PARTICIPANT：summarizerAgentId 不在参与方里', () => {
  const { builder: relayBuilder } = builder();
  assert.throws(
    () => relayBuilder.build(baseInput({ summarizerAgentId: 'agent-outsider' })),
    { code: 'NOT_PARTICIPANT' }
  );
});

test('RELAY_INVALID：conversationId/topic/conclusion 缺失、pendingActions 非数组、provenance 不可追溯', () => {
  const { builder: relayBuilder } = builder();
  assert.throws(() => relayBuilder.build(baseInput({ conversationId: '' })), { code: 'RELAY_INVALID' });
  assert.throws(() => relayBuilder.build(baseInput({ topic: '  ' })), { code: 'RELAY_INVALID' });
  assert.throws(() => relayBuilder.build(baseInput({ conclusion: undefined })), { code: 'RELAY_INVALID' });
  assert.throws(() => relayBuilder.build(baseInput({ pendingActions: 'x' })), { code: 'RELAY_INVALID' });
  assert.throws(() => relayBuilder.build(baseInput({ provenance: [{ agentId: AGENT_A }] })), {
    code: 'RELAY_INVALID'
  });
  assert.throws(() => relayBuilder.assertNotImpersonation({ relay: null }), { code: 'RELAY_INVALID' });
  assert.throws(() => relayBuilder.render({ relay: { kind: 'OTHER' } }), { code: 'RELAY_INVALID' });
  assert.throws(() => new RelaySummaryBuilder({ maxLength: 0 }), TypeError);
});

test('SUMMARY_TOO_LONG：超过 maxLength 拒绝（摘要不是聊天记录），边界长度可通过', () => {
  const { builder: relayBuilder } = builder({ maxLength: 1000 });
  const relay = relayBuilder.build(baseInput());
  const exactLength = relay.text.length;

  const exact = new RelaySummaryBuilder({ clock: makeClock(), maxLength: exactLength });
  assert.doesNotThrow(() => exact.build(baseInput()));

  const tooShort = new RelaySummaryBuilder({ clock: makeClock(), maxLength: exactLength - 1 });
  assert.throws(() => tooShort.build(baseInput()), { code: 'SUMMARY_TOO_LONG' });

  // 默认 600：超长 conclusion 被拒绝
  const defaultBuilder = new RelaySummaryBuilder({ clock: makeClock() });
  assert.throws(() => defaultBuilder.build(baseInput({ conclusion: '很长的结论'.repeat(200) })), {
    code: 'SUMMARY_TOO_LONG'
  });
});

test('provenance 默认可追溯到全部参与方；显式 provenance 原样保留且冻结', () => {
  const { builder: relayBuilder } = builder();
  const auto = relayBuilder.build(baseInput({ provenance: [] }));
  assert.deepEqual(auto.provenance.map((item) => item.agentId), [AGENT_A, AGENT_B]);
  assert.deepEqual(auto.provenance.map((item) => item.role), ['participant', 'participant']);

  const explicit = relayBuilder.build(baseInput());
  assert.deepEqual({ ...explicit.provenance[0] }, { agentId: AGENT_A, sessionId: 'neg-1', role: 'initiator' });
  assert.ok(Object.isFrozen(explicit.provenance[0]));
});

test('participants 支持字符串与对象两种写法；对象 displayName 用于渲染', () => {
  const { builder: relayBuilder } = builder();
  const relay = relayBuilder.build(
    baseInput({
      participants: ['agent-x', { agentId: 'agent-y', displayName: 'Y 的总管' }],
      summarizerAgentId: 'agent-x',
      provenance: [{ agentId: 'agent-x' }, { agentId: 'agent-y' }]
    })
  );
  assert.equal(relay.participants[0].agentId, 'agent-x');
  assert.equal(relay.participants[0].displayName, null);
  assert.match(relay.text, /agent-x与Y 的总管/);
});

test('RelaySummaryError 带 code 且是 Error 子类', () => {
  const { builder: relayBuilder } = builder();
  try {
    relayBuilder.build(baseInput({ summarizerAgentId: 'agent-outsider' }));
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof RelaySummaryError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_PARTICIPANT');
  }
});
