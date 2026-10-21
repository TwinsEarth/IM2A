// 测试：旁观与主动沉默（契约 §23）。
// 覆盖：家庭群默认旁观（记录但不打扰）、OFF 不说不记、@ 才说话、DIRECT 除 OFF 外一律说话、MODE_INVALID/POLICY_NOT_FOUND 等全部判定码。
import test from 'node:test';
import assert from 'node:assert/strict';

import { PRESENCE_CODE, PRESENCE_MODE, PresencePolicy, PresencePolicyError } from '../src/presence-policy.mjs';

const AGENT = 'agent-lin-butler';

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

const makePolicy = (clock = makeClock()) => ({ clock, policy: new PresencePolicy({ clock }) });

test('PRESENCE_MODE / PRESENCE_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...PRESENCE_MODE }, {
    ALWAYS: 'ALWAYS',
    MENTION_ONLY: 'MENTION_ONLY',
    OBSERVE: 'OBSERVE',
    OFF: 'OFF'
  });
  assert.deepEqual({ ...PRESENCE_CODE }, {
    POLICY_MATCHED: 'POLICY_MATCHED',
    POLICY_NOT_FOUND: 'POLICY_NOT_FOUND',
    MODE_INVALID: 'MODE_INVALID',
    SILENCED: 'SILENCED',
    MENTION_REQUIRED: 'MENTION_REQUIRED'
  });
  assert.ok(Object.isFrozen(PRESENCE_MODE));
  assert.ok(Object.isFrozen(PRESENCE_CODE));
});

test('无策略 => ALWAYS / POLICY_NOT_FOUND（默认活跃；"没配置"不是错误）', () => {
  const { policy } = makePolicy();
  const result = policy.decide({ agentId: AGENT, channelId: 'family' });
  assert.equal(result.speak, true);
  assert.equal(result.mode, PRESENCE_MODE.ALWAYS);
  assert.equal(result.code, 'POLICY_NOT_FOUND');
  assert.equal(result.recordSilently, false);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.reasons));
});

test('家庭群默认旁观：OBSERVE => speak=false 且 recordSilently=true（记录但不打扰）', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, channelId: 'family', mode: PRESENCE_MODE.OBSERVE, note: '家庭群少说话' });
  const result = policy.decide({ agentId: AGENT, channelId: 'family', mentioned: true });
  assert.equal(result.speak, false);
  assert.equal(result.recordSilently, true);
  assert.equal(result.mode, PRESENCE_MODE.OBSERVE);
  assert.equal(result.code, 'SILENCED');
  assert.match(result.reasons.join(' '), /记录但不打扰/);
  // 即使被 @，OBSERVE 也不说话（只有 DIRECT 例外）
  assert.equal(policy.decide({ agentId: AGENT, channelId: 'family', mentioned: true }).speak, false);
});

test('OFF：不说话也不记录（连 @ 到也不回应；DIRECT 也沉默）', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, channelId: 'family', mode: PRESENCE_MODE.OFF });
  const result = policy.decide({ agentId: AGENT, channelId: 'family', mentioned: true, isDirect: false });
  assert.equal(result.speak, false);
  assert.equal(result.recordSilently, false);
  assert.equal(result.code, 'SILENCED');
  assert.equal(result.mode, PRESENCE_MODE.OFF);

  const direct = policy.decide({ agentId: AGENT, channelId: 'family', mentioned: true, isDirect: true });
  assert.equal(direct.speak, false); // DIRECT 例外不覆盖 OFF
  assert.equal(direct.recordSilently, false);
  assert.equal(direct.code, 'SILENCED');
});

test('MENTION_ONLY：被 @ 才说话，未提及 => MENTION_REQUIRED', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, channelId: 'group-1', mode: PRESENCE_MODE.MENTION_ONLY });

  const notMentioned = policy.decide({ agentId: AGENT, channelId: 'group-1' });
  assert.equal(notMentioned.speak, false);
  assert.equal(notMentioned.mode, PRESENCE_MODE.MENTION_ONLY);
  assert.equal(notMentioned.code, 'MENTION_REQUIRED');
  assert.equal(notMentioned.recordSilently, false);

  const mentioned = policy.decide({ agentId: AGENT, channelId: 'group-1', mentioned: true });
  assert.equal(mentioned.speak, true);
  assert.equal(mentioned.code, 'POLICY_MATCHED');
  assert.equal(mentioned.recordSilently, false);
});

test('ALWAYS：正常参与，speak=true / POLICY_MATCHED', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, channelId: 'group-1', mode: PRESENCE_MODE.ALWAYS });
  const result = policy.decide({ agentId: AGENT, channelId: 'group-1' });
  assert.equal(result.speak, true);
  assert.equal(result.mode, PRESENCE_MODE.ALWAYS);
  assert.equal(result.code, 'POLICY_MATCHED');
  assert.equal(result.recordSilently, false);
});

test('DIRECT 例外：DIRECT 频道除 OFF 外一律说话（防 1:1 里 Agent 失联）', () => {
  const { policy } = makePolicy();
  const modes = [PRESENCE_MODE.ALWAYS, PRESENCE_MODE.MENTION_ONLY, PRESENCE_MODE.OBSERVE];
  for (const mode of modes) {
    policy.setPolicy({ agentId: AGENT, channelId: `direct-${mode}`, mode, note: null });
    const result = policy.decide({
      agentId: AGENT,
      channelId: `direct-${mode}`,
      mentioned: false,
      isDirect: true
    });
    assert.equal(result.speak, true, `DIRECT + ${mode} 必须说话`);
    assert.equal(result.recordSilently, false);
    assert.equal(result.code, 'POLICY_MATCHED');
    assert.match(result.reasons.join(' '), /direct channel/);
  }

  policy.setPolicy({ agentId: AGENT, channelId: 'direct-off', mode: PRESENCE_MODE.OFF });
  const off = policy.decide({ agentId: AGENT, channelId: 'direct-off', mentioned: true, isDirect: true });
  assert.equal(off.speak, false, 'DIRECT 例外不覆盖 OFF');
});

test('MODE_INVALID：setPolicy 的 mode 必须属于四模式；入参缺失是 TypeError', () => {
  const { policy } = makePolicy();
  for (const mode of ['SILENT', 'always', null, undefined, 1]) {
    assert.throws(() => policy.setPolicy({ agentId: AGENT, channelId: 'c1', mode }), { code: 'MODE_INVALID' });
  }
  assert.throws(() => policy.setPolicy({ channelId: 'c1', mode: PRESENCE_MODE.ALWAYS }), TypeError);
  assert.throws(() => policy.setPolicy({ agentId: AGENT, mode: PRESENCE_MODE.ALWAYS }), TypeError);
  assert.throws(() => policy.setPolicy({ agentId: AGENT, channelId: '', mode: PRESENCE_MODE.ALWAYS }), TypeError);
});

test('setPolicy 冻结、可覆盖；policiesOf 按 channelId 升序且冻结', () => {
  const { policy, clock } = makePolicy();
  const stored = policy.setPolicy({ agentId: AGENT, channelId: 'family', mode: PRESENCE_MODE.OBSERVE, note: 'n1' });
  assert.ok(Object.isFrozen(stored));
  assert.equal(stored.note, 'n1');
  assert.equal(stored.updatedAt, '2026-10-21T00:00:00.000Z');

  clock.advance(1000);
  policy.setPolicy({ agentId: AGENT, channelId: 'work', mode: PRESENCE_MODE.MENTION_ONLY });
  policy.setPolicy({ agentId: AGENT, channelId: 'family', mode: PRESENCE_MODE.ALWAYS });
  const list = policy.policiesOf(AGENT);
  assert.deepEqual(list.map((item) => item.channelId), ['family', 'work']);
  assert.equal(list[0].mode, PRESENCE_MODE.ALWAYS); // 覆盖生效
  assert.equal(list[0].updatedAt, '2026-10-21T00:00:01.000Z');
  assert.equal(list.length, 2); // 同一 agent+channel 只有一条
  assert.ok(Object.isFrozen(list));
  assert.deepEqual(policy.policiesOf('agent-other'), []);
});

test('策略按 agent × channel 隔离：同一 Agent 不同频道互不影响', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, channelId: 'family', mode: PRESENCE_MODE.OBSERVE });
  policy.setPolicy({ agentId: AGENT, channelId: 'work', mode: PRESENCE_MODE.ALWAYS });
  assert.equal(policy.decide({ agentId: AGENT, channelId: 'family' }).speak, false);
  assert.equal(policy.decide({ agentId: AGENT, channelId: 'work' }).speak, true);
  // 其他 Agent 在 family 频道没有策略 => 默认 ALWAYS
  assert.equal(policy.decide({ agentId: 'agent-other', channelId: 'family' }).code, 'POLICY_NOT_FOUND');
});

test('PresencePolicyError 带 code 且是 Error 子类', () => {
  const { policy } = makePolicy();
  try {
    policy.setPolicy({ agentId: AGENT, channelId: 'c1', mode: 'DREAM' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof PresencePolicyError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'MODE_INVALID');
  }
});
