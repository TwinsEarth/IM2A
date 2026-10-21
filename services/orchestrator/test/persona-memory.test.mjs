// 测试：人格文件 + 记忆生命周期（契约 §17）。
// 覆盖：SOUL 校验/留痕、隔离、晋升只由「时间+access」决定、衰减/遗忘、portable 迁移签名与冲突、版本/agentId 校验。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  MEMORY_STAGE,
  PERSONA_CODE,
  PersonaError,
  PersonaMemory
} from '../src/persona-memory.mjs';
import { createAgentIdentity } from '../../identity/src/agent-id.mjs';

const AGENT = 'agent-lin-butler';
const USER = 'user-lin';

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

const makeStore = (options = {}) => {
  const clock = options.clock ?? makeClock();
  return { clock, store: new PersonaMemory({ clock, ...options.extra }) };
};

test('MEMORY_STAGE / PERSONA_CODE 常量冻结且与契约一致（含 IMPORT_INVALID）', () => {
  assert.deepEqual({ ...MEMORY_STAGE }, {
    WORKING: 'WORKING',
    EPISODIC: 'EPISODIC',
    SEMANTIC: 'SEMANTIC',
    ARCHIVED: 'ARCHIVED'
  });
  assert.deepEqual({ ...PERSONA_CODE }, {
    SOUL_INVALID: 'SOUL_INVALID',
    MEMORY_ISOLATED: 'MEMORY_ISOLATED',
    STAGE_INVALID: 'STAGE_INVALID',
    NOT_FOUND: 'NOT_FOUND',
    PROMOTION_DENIED: 'PROMOTION_DENIED',
    TOMBSTONED: 'TOMBSTONED',
    EXPORT_INVALID: 'EXPORT_INVALID',
    IMPORT_REJECTED: 'IMPORT_REJECTED',
    IMPORT_INVALID: 'IMPORT_INVALID'
  });
  assert.ok(Object.isFrozen(MEMORY_STAGE));
  assert.ok(Object.isFrozen(PERSONA_CODE));
});

test('setSoul：首次 changed=true，相同内容 changed=false，变更 changed=true 且留审计', () => {
  const { store } = makeStore();
  const first = store.setSoul({
    agentId: AGENT,
    userId: USER,
    values: ['诚实', '保护用户隐私'],
    rules: ['不转账'],
    boundaries: ['不读他人记忆'],
    voice: '温和'
  });
  assert.equal(first.changed, true);
  assert.ok(Object.isFrozen(first));
  assert.ok(Object.isFrozen(first.soul));
  assert.deepEqual([...first.soul.values], ['诚实', '保护用户隐私']);
  assert.equal(first.soul.voice, '温和');
  assert.equal(first.soul.createdAt, '2026-10-21T00:00:00.000Z');
  assert.deepEqual(store.soul({ agentId: AGENT, userId: USER }).values, first.soul.values);

  const again = store.setSoul({ agentId: AGENT, userId: USER, values: ['诚实', '保护用户隐私'], rules: ['不转账'], boundaries: ['不读他人记忆'], voice: '温和' });
  assert.equal(again.changed, false);

  const changed = store.setSoul({ agentId: AGENT, userId: USER, values: ['诚实', '保护用户隐私', '尊重边界'] });
  assert.equal(changed.changed, true);
  assert.equal(changed.soul.createdAt, first.soul.createdAt); // createdAt 不被覆写

  const events = store.history({ agentId: AGENT }).filter((event) => event.action === 'SOUL_SET');
  assert.equal(events.length, 3);
  assert.deepEqual(
    events.map((event) => event.detail.changed),
    [true, false, true]
  );
  assert.ok(Object.isFrozen(store.history()));
  assert.throws(() => store.history().push({}), TypeError);
});

test('SOUL_INVALID：values 空/非数组/含非字符串/全空白；rules/voice 非法', () => {
  const { store } = makeStore();
  const base = { agentId: AGENT, userId: USER };
  for (const values of [[], 'x', [1], ['   '], null, undefined]) {
    assert.throws(() => store.setSoul({ ...base, values }), { code: 'SOUL_INVALID' });
  }
  assert.throws(() => store.setSoul({ ...base, values: ['ok'], rules: 'no' }), { code: 'SOUL_INVALID' });
  assert.throws(() => store.setSoul({ ...base, values: ['ok'], boundaries: 42 }), { code: 'SOUL_INVALID' });
  assert.throws(() => store.setSoul({ ...base, values: ['ok'], voice: 42 }), { code: 'SOUL_INVALID' });
});

test('隔离：A 读 B 的人格记忆 => MEMORY_ISOLATED（与 §6 一致）', () => {
  const { store } = makeStore();
  store.remember({ agentId: 'agent-b', userId: USER, content: 'B 的私密记忆' });
  assert.throws(
    () => store.recall({ agentId: 'agent-b', requesterAgentId: AGENT, userId: USER }),
    { code: 'MEMORY_ISOLATED' }
  );
  // 自己读自己可以
  assert.equal(store.recall({ agentId: 'agent-b', requesterAgentId: 'agent-b', userId: USER }).length, 1);
});

test('remember/recall：记录冻结、按 stage/query/limit 过滤、按最近访问排序', () => {
  const { store, clock } = makeStore();
  const first = store.remember({ agentId: AGENT, userId: USER, content: '用户喜欢火锅', tags: ['food', 'food', ''] });
  assert.ok(Object.isFrozen(first));
  assert.match(first.id, /^pmem-/);
  assert.equal(first.stage, MEMORY_STAGE.WORKING);
  assert.equal(first.accesses, 0);
  assert.deepEqual([...first.tags], ['food']);
  assert.equal(first.expiresAt, null);

  clock.advance(1000);
  const second = store.remember({ agentId: AGENT, userId: USER, content: '用户讨厌下雨', stage: MEMORY_STAGE.EPISODIC });
  clock.advance(1000);
  const third = store.remember({ agentId: AGENT, userId: USER, content: '用户是设计师', stage: MEMORY_STAGE.SEMANTIC });

  const all = store.recall({ agentId: AGENT, userId: USER });
  assert.equal(all.length, 3);
  assert.deepEqual(all.map((record) => record.id), [third.id, second.id, first.id]); // 最近创建优先
  assert.ok(Object.isFrozen(all));
  assert.equal(store.recall({ agentId: AGENT, userId: USER, stage: MEMORY_STAGE.EPISODIC }).length, 1);
  assert.equal(store.recall({ agentId: AGENT, userId: USER, query: '火锅' }).length, 1);
  assert.equal(store.recall({ agentId: AGENT, userId: USER, query: '不存在的词' }).length, 0);
  assert.equal(store.recall({ agentId: AGENT, userId: USER, limit: 1 }).length, 1);
  assert.deepEqual(store.recall({ agentId: AGENT, userId: USER, limit: 0 }), []);
});

test('STAGE_INVALID：remember/recall 的 stage 必须属于四阶段', () => {
  const { store } = makeStore();
  assert.throws(() => store.remember({ agentId: AGENT, userId: USER, content: 'x', stage: 'DREAM' }), {
    code: 'STAGE_INVALID'
  });
  assert.throws(() => store.recall({ agentId: AGENT, userId: USER, stage: 'DREAM' }), {
    code: 'STAGE_INVALID'
  });
});

test('NOT_FOUND：缺属主 / 未知 record / 未知晋升目标 / 未知遗忘目标', () => {
  const { store } = makeStore();
  assert.throws(() => store.remember({ agentId: AGENT, content: 'x' }), { code: 'NOT_FOUND' });
  assert.throws(() => store.remember({ userId: USER, content: 'x' }), { code: 'NOT_FOUND' });
  assert.throws(() => store.access({ agentId: AGENT, recordId: 'pmem-missing' }), { code: 'NOT_FOUND' });
  assert.throws(() => store.promote({ agentId: AGENT, userId: USER, recordId: 'pmem-missing' }), {
    code: 'NOT_FOUND'
  });
  assert.throws(() => store.forget({ agentId: AGENT, userId: USER, recordId: 'pmem-missing' }), {
    code: 'NOT_FOUND'
  });
  assert.throws(() => store.stats({ agentId: AGENT }), { code: 'NOT_FOUND' });
});

test('access：访问计数 +1、lastAccessedAt 推进，返回新冻结对象', () => {
  const { store, clock } = makeStore();
  const record = store.remember({ agentId: AGENT, userId: USER, content: '可访问', salience: 0.9 });
  clock.advance(5000);
  const after = store.access({ agentId: AGENT, recordId: record.id });
  assert.notEqual(after, record);
  assert.equal(after.accesses, 1);
  assert.equal(after.lastAccessedAt, '2026-10-21T00:00:05.000Z');
  assert.equal(record.accesses, 0); // 原对象不变
  assert.ok(Object.isFrozen(after));
});

test('晋升规则①：WORKING ≥24h 且 accesses≥2 => EPISODIC；只靠时间不能晋升', () => {
  const { store, clock } = makeStore();
  const promoted = store.remember({ agentId: AGENT, userId: USER, content: '重要偏好' });
  store.access({ agentId: AGENT, recordId: promoted.id });
  store.access({ agentId: AGENT, recordId: promoted.id });
  clock.advance(24 * 60 * 60e3 - 1);
  assert.deepEqual([...store.promoteEligible({ agentId: AGENT, userId: USER })], []); // 差 1ms
  clock.advance(1);
  const eligible = store.promoteEligible({ agentId: AGENT, userId: USER });
  assert.equal(eligible.length, 1);
  assert.deepEqual(
    { ...eligible[0] },
    { recordId: promoted.id, from: 'WORKING', to: 'EPISODIC', reason: 'WORKING_AGE_24H_ACCESS_2' }
  );
  const updated = store.promote({ agentId: AGENT, userId: USER, recordId: promoted.id });
  assert.equal(updated.stage, MEMORY_STAGE.EPISODIC);
  assert.equal(updated.promotedAt, '2026-10-22T00:00:00.000Z');
  assert.equal(store.stats({ agentId: AGENT, userId: USER }).byStage.EPISODIC, 1);

  // 仅时间流逝但从未 access => 不晋升，强行 promote 被拒
  const timeOnly = store.remember({ agentId: AGENT, userId: USER, content: '只是时间久了' });
  clock.advance(25 * 60 * 60e3);
  assert.deepEqual([...store.promoteEligible({ agentId: AGENT, userId: USER })], []);
  assert.throws(() => store.promote({ agentId: AGENT, userId: USER, recordId: timeOnly.id }), {
    code: 'PROMOTION_DENIED'
  });
});

test('晋升规则②：EPISODIC ≥30d 且 accesses≥5 => SEMANTIC；访问次数不够不晋升', () => {
  const { store, clock } = makeStore();
  const record = store.remember({ agentId: AGENT, userId: USER, content: '长期事实', stage: MEMORY_STAGE.EPISODIC });
  for (let index = 0; index < 4; index += 1) store.access({ agentId: AGENT, recordId: record.id });
  clock.advance(30 * 24 * 60 * 60e3);
  assert.deepEqual([...store.promoteEligible({ agentId: AGENT, userId: USER })], []); // 4 < 5
  store.access({ agentId: AGENT, recordId: record.id });
  const eligible = store.promoteEligible({ agentId: AGENT, userId: USER });
  assert.equal(eligible.length, 1);
  assert.deepEqual(
    { ...eligible[0] },
    { recordId: record.id, from: 'EPISODIC', to: 'SEMANTIC', reason: 'EPISODIC_AGE_30D_ACCESS_5' }
  );
  assert.equal(store.promote({ agentId: AGENT, userId: USER, recordId: record.id }).stage, MEMORY_STAGE.SEMANTIC);
  // SEMANTIC 已是最高层，不可再晋升
  assert.throws(() => store.promote({ agentId: AGENT, userId: USER, recordId: record.id }), {
    code: 'PROMOTION_DENIED'
  });
});

test('衰减：salience<0.2 且距最后访问≥90d => ARCHIVED（不删除，recall 不再返回，stats/审计可见）', () => {
  const { store, clock } = makeStore();
  const low = store.remember({ agentId: AGENT, userId: USER, content: '低价值记忆', salience: 0.1 });
  const high = store.remember({ agentId: AGENT, userId: USER, content: '高价值记忆', salience: 0.9 });
  const recent = store.remember({ agentId: AGENT, userId: USER, content: '最近访问过', salience: 0.1 });
  clock.advance(89 * 24 * 60 * 60e3);
  store.access({ agentId: AGENT, recordId: recent.id }); // 最后访问被刷新
  clock.advance(24 * 60 * 60e3 - 1); // 距 low 创建 90d-1ms
  assert.deepEqual([...store.decay({ agentId: AGENT, userId: USER })], []);
  clock.advance(1); // low 达到 90d；recent 只 idle 1d
  const decayed = store.decay({ agentId: AGENT, userId: USER });
  assert.equal(decayed.length, 1);
  assert.deepEqual(
    { ...decayed[0] },
    { recordId: low.id, from: 'WORKING', to: 'ARCHIVED', reason: 'LOW_SALIENCE_IDLE_90D' }
  );
  const recalled = store.recall({ agentId: AGENT, userId: USER });
  assert.deepEqual(recalled.map((record) => record.id).includes(low.id), false);
  assert.deepEqual(recalled.map((record) => record.id).includes(high.id), true);
  const stats = store.stats({ agentId: AGENT, userId: USER });
  assert.equal(stats.byStage.ARCHIVED, 1); // 归档仍计入统计
  assert.equal(stats.memories, 3);
  assert.ok(store.history({ agentId: AGENT }).some((event) => event.action === 'DECAY'));
  assert.throws(() => store.access({ agentId: AGENT, recordId: low.id }), { code: 'TOMBSTONED' });
});

test('遗忘：软删 + tombstone；recall 为空；重复 forget/access => TOMBSTONED', () => {
  const { store } = makeStore();
  const record = store.remember({ agentId: AGENT, userId: USER, content: '要忘记的秘密' });
  const tombstone = store.forget({ agentId: AGENT, userId: USER, recordId: record.id });
  assert.ok(Object.isFrozen(tombstone));
  assert.equal(tombstone.recordId, record.id);
  assert.equal('content' in tombstone, false); // tombstone 不含内容
  assert.deepEqual(store.recall({ agentId: AGENT, userId: USER }), []);
  assert.throws(() => store.forget({ agentId: AGENT, userId: USER, recordId: record.id }), {
    code: 'TOMBSTONED'
  });
  assert.throws(() => store.access({ agentId: AGENT, recordId: record.id }), { code: 'TOMBSTONED' });
  const stats = store.stats({ agentId: AGENT, userId: USER });
  assert.equal(stats.memories, 0);
  assert.equal(stats.tombstoned, 1);
  assert.ok(store.history({ agentId: AGENT }).some((event) => event.action === 'FORGET'));
});

test('TTL：过期记忆 recall 不返回（软过期，stats 仍记录）', () => {
  const { store, clock } = makeStore();
  const record = store.remember({ agentId: AGENT, userId: USER, content: '短期记忆', ttlMs: 1000 });
  assert.equal(record.expiresAt, '2026-10-21T00:00:01.000Z');
  clock.advance(1001);
  assert.deepEqual(store.recall({ agentId: AGENT, userId: USER }), []);
  assert.equal(store.stats({ agentId: AGENT, userId: USER }).memories, 1);
});

test('exportPortable -> importPortable 往返一致（soul + 记忆），重复导入幂等跳过', () => {
  const clock = makeClock();
  const source = new PersonaMemory({ clock });
  source.setSoul({ agentId: AGENT, userId: USER, values: ['诚实'] });
  const first = source.remember({ agentId: AGENT, userId: USER, content: '喜欢火锅' });
  const second = source.remember({ agentId: AGENT, userId: USER, content: '住在杭州', stage: MEMORY_STAGE.EPISODIC });
  source.access({ agentId: AGENT, recordId: second.id });

  const bundle = source.exportPortable({ agentId: AGENT, userId: USER });
  assert.ok(Object.isFrozen(bundle));
  assert.equal(bundle.version, 'agentim-persona/1');
  assert.equal(bundle.agentId, AGENT);
  assert.equal(bundle.memories.length, 2);
  assert.deepEqual([...bundle.tombstones], []);
  assert.equal('signature' in bundle, false); // 未注入 identity => 无签名
  assert.equal(bundle.exportedAt, '2026-10-21T00:00:00.000Z');

  const target = new PersonaMemory({ clock });
  const result = target.importPortable({ bundle, expectAgentId: AGENT });
  assert.equal(result.imported.length, 3); // 2 条记忆 + soul
  assert.deepEqual([...result.skipped], []);
  assert.deepEqual([...result.conflicts], []);
  assert.ok(Object.isFrozen(result));
  assert.deepEqual(
    target.recall({ agentId: AGENT, userId: USER }).map((record) => record.content).sort(),
    ['住在杭州', '喜欢火锅'].sort()
  );
  assert.equal(target.soul({ agentId: AGENT, userId: USER }).values[0], '诚实');
  assert.equal(target.stats({ agentId: AGENT, userId: USER }).accesses, 1);

  // 幂等：重复导入 => 全部 skipped duplicate，不重复写入
  const secondImport = target.importPortable({ bundle });
  assert.equal(secondImport.imported.length, 0);
  assert.equal(secondImport.skipped.length, 3);
  assert.ok(secondImport.skipped.every((item) => item.reason === 'DUPLICATE'));
  assert.equal(target.stats({ agentId: AGENT, userId: USER }).memories, 2);
});

test('IMPORT_INVALID：version/schema 不符（bundle 非对象、memories/soul 结构非法）', () => {
  const { store } = makeStore();
  assert.throws(() => store.importPortable({ bundle: null }), { code: 'IMPORT_INVALID' });
  assert.throws(
    () => store.importPortable({ bundle: { version: 'agentim-persona/0', agentId: AGENT, memories: [] } }),
    { code: 'IMPORT_INVALID' }
  );
  assert.throws(
    () => store.importPortable({ bundle: { version: 'agentim-persona/1', agentId: AGENT, memories: 'nope' } }),
    { code: 'IMPORT_INVALID' }
  );
  assert.throws(
    () =>
      store.importPortable({
        bundle: {
          version: 'agentim-persona/1',
          agentId: AGENT,
          soul: { userId: USER, values: [] },
          memories: [],
          tombstones: []
        }
      }),
    { code: 'IMPORT_INVALID' }
  );
  assert.throws(
    () =>
      store.importPortable({
        bundle: {
          version: 'agentim-persona/1',
          agentId: AGENT,
          memories: [{ id: 'pmem-bad', userId: USER, content: 'x', stage: 'DREAM' }],
          tombstones: []
        }
      }),
    { code: 'IMPORT_INVALID' }
  );
});

test('IMPORT_REJECTED：agentId 不符；签名被篡改；EXPORT_INVALID：导出侧 identity 非法', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ clock });
  const signedStore = new PersonaMemory({ clock, identity });
  signedStore.remember({ agentId: AGENT, userId: USER, content: '签名内容' });
  const signedBundle = signedStore.exportPortable({ agentId: AGENT, userId: USER });
  assert.equal(signedBundle.signature.alg, 'ed25519');
  assert.ok(signedBundle.signature.signature.length > 0);
  assert.equal(signedBundle.signature.signerPublicKeyPem, identity.publicKeyPem);

  const target = new PersonaMemory({ clock });
  assert.equal(target.importPortable({ bundle: signedBundle }).imported.length, 1);
  assert.throws(() => target.importPortable({ bundle: signedBundle, expectAgentId: 'agent-other' }), {
    code: 'IMPORT_REJECTED'
  });

  const tampered = {
    ...signedBundle,
    memories: signedBundle.memories.map((memory) => ({ ...memory, content: '被篡改' }))
  };
  assert.throws(() => new PersonaMemory({ clock }).importPortable({ bundle: tampered }), {
    code: 'IMPORT_REJECTED'
  });

  const brokenIdentity = new PersonaMemory({ clock, identity: {} });
  assert.throws(() => brokenIdentity.exportPortable({ agentId: AGENT, userId: USER }), {
    code: 'EXPORT_INVALID'
  });
});

test('tombstone 拦截复活：导出后本地遗忘，再导入旧包 => conflicts(TOMBSTONED)，不复活', () => {
  const clock = makeClock();
  const store = new PersonaMemory({ clock });
  const record = store.remember({ agentId: AGENT, userId: USER, content: '会被遗忘' });
  const bundle = store.exportPortable({ agentId: AGENT, userId: USER });
  store.forget({ agentId: AGENT, userId: USER, recordId: record.id });

  const result = store.importPortable({ bundle });
  assert.equal(result.imported.length, 0);
  assert.equal(result.conflicts.length, 1);
  assert.equal(result.conflicts[0].recordId, record.id);
  assert.equal(result.conflicts[0].reason, 'TOMBSTONED');
  assert.deepEqual(store.recall({ agentId: AGENT, userId: USER }), []);
});

test('tombstone 与本地活跃记录冲突 => conflicts(TOMBSTONE_CONFLICT)，不静默删除本地记忆', () => {
  const clock = makeClock();
  const source = new PersonaMemory({ clock });
  const record = source.remember({ agentId: AGENT, userId: USER, content: '源端记忆' });
  const exportBundle = source.exportPortable({ agentId: AGENT, userId: USER });

  const target = new PersonaMemory({ clock });
  target.importPortable({ bundle: exportBundle }); // 目标端先拥有活跃记录
  const tombstoneBundle = {
    version: 'agentim-persona/1',
    agentId: AGENT,
    soul: null,
    memories: [],
    tombstones: [
      { recordId: record.id, agentId: AGENT, userId: USER, stage: 'WORKING', forgottenAt: clock.iso() }
    ],
    exportedAt: clock.iso()
  };
  const result = target.importPortable({ bundle: tombstoneBundle });
  assert.equal(result.conflicts.length, 1);
  assert.equal(result.conflicts[0].reason, 'TOMBSTONE_CONFLICT');
  // 本地内容仍在（人工裁决前不静默删）
  assert.equal(target.recall({ agentId: AGENT, userId: USER }).length, 1);
});

test('内容冲突不覆盖：本地记录与传入包内容不同 => conflicts(CONTENT_CONFLICT)，本地不变', () => {
  const clock = makeClock();
  const source = new PersonaMemory({ clock });
  source.remember({ agentId: AGENT, userId: USER, content: '原始内容' });
  const bundle = source.exportPortable({ agentId: AGENT, userId: USER });

  const target = new PersonaMemory({ clock });
  target.importPortable({ bundle });
  const conflictBundle = { ...bundle, memories: bundle.memories.map((memory) => ({ ...memory, content: '不同内容' })) };
  const result = target.importPortable({ bundle: conflictBundle });
  assert.equal(result.conflicts.length, 1);
  assert.equal(result.conflicts[0].reason, 'CONTENT_CONFLICT');
  assert.equal(target.recall({ agentId: AGENT, userId: USER })[0].content, '原始内容');
});

test('PersonaError 带 code 且是 Error 子类', () => {
  const { store } = makeStore();
  try {
    store.setSoul({ agentId: AGENT, userId: USER, values: [] });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof PersonaError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'SOUL_INVALID');
  }
});
