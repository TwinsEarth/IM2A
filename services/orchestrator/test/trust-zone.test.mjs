// 测试：信任矩阵（契约 §1）。覆盖正常路径 / 每个错误码 / 过期回落 / 覆盖授权 / 审计只追加。
import test from 'node:test';
import assert from 'node:assert/strict';

import { TRUST_LEVELS, TRUST_NAMES, TrustMatrix, TrustZoneError } from '../src/trust-zone.mjs';
import { makeClock } from './_helpers.mjs';

test('TRUST_LEVELS/TRUST_NAMES 常量冻结且顺序与契约一致', () => {
  assert.deepEqual({ ...TRUST_LEVELS }, { SELF: 0, FAMILY: 1, TRUSTED: 2, KNOWN: 3, STRANGER: 4 });
  assert.deepEqual([...TRUST_NAMES], ['SELF', 'FAMILY', 'TRUSTED', 'KNOWN', 'STRANGER']);
  assert.ok(Object.isFrozen(TRUST_LEVELS));
  assert.ok(Object.isFrozen(TRUST_NAMES));
});

test('grant + levelOf 五档往返，返回冻结对象且带 name', () => {
  const matrix = new TrustMatrix({ clock: makeClock() });
  const granted = matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.TRUSTED });
  assert.deepEqual(granted, {
    userId: 'user-a',
    targetId: 'user-b',
    level: 2,
    name: 'TRUSTED',
    grantedAt: '2026-10-21T00:00:00.000Z',
    expiresAt: null
  });
  assert.ok(Object.isFrozen(granted));
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-b' }), 2);

  matrix.grant({ userId: 'user-a', targetId: 'user-a', level: TRUST_LEVELS.SELF });
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-a' }), 0);
  matrix.grant({ userId: 'user-a', targetId: 'user-c', level: TRUST_LEVELS.FAMILY });
  matrix.grant({ userId: 'user-a', targetId: 'user-d', level: TRUST_LEVELS.KNOWN });
  matrix.grant({ userId: 'user-a', targetId: 'user-e', level: TRUST_LEVELS.STRANGER });
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-c' }), 1);
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-d' }), 3);
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-e' }), 4);
});

test('缺省授权回落 STRANGER(4)：未知用户、未知目标、空参数、反向关系', () => {
  const matrix = new TrustMatrix();
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.SELF === 0 ? TRUST_LEVELS.FAMILY : 1 });
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-x' }), TRUST_LEVELS.STRANGER);
  assert.equal(matrix.levelOf({ userId: 'user-x', targetId: 'user-b' }), TRUST_LEVELS.STRANGER);
  assert.equal(matrix.levelOf({ userId: 'user-b', targetId: 'user-a' }), TRUST_LEVELS.STRANGER);
  assert.equal(matrix.levelOf(), TRUST_LEVELS.STRANGER);
  assert.equal(matrix.levelOf({ userId: null, targetId: 'user-b' }), TRUST_LEVELS.STRANGER);
});

test('重复 grant 为覆盖语义，并追加审计（不改写历史）', () => {
  const matrix = new TrustMatrix();
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.KNOWN });
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.FAMILY });
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-b' }), TRUST_LEVELS.FAMILY);
  const history = matrix.history();
  assert.equal(history.length, 2);
  assert.equal(history[1].action, 'GRANT_OVERWRITE');
  assert.equal(history[1].detail.previousLevel, TRUST_LEVELS.KNOWN);
});

test('INVALID_LEVEL：越界/非整数/非数字 level 与非自反的 SELF 授权', () => {
  const matrix = new TrustMatrix();
  const base = { userId: 'user-a', targetId: 'user-b' };
  for (const level of [5, -1, 1.5, '2', null, undefined, NaN]) {
    assert.throws(() => matrix.grant({ ...base, level }), { code: 'INVALID_LEVEL' });
  }
  assert.throws(
    () => matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.SELF }),
    { code: 'INVALID_LEVEL' }
  );
  assert.throws(() => matrix.atLeast({ ...base, level: 9 }), { code: 'INVALID_LEVEL' });
});

test('INVALID_LEVEL：expiresAt 非 ISO 字符串', () => {
  const matrix = new TrustMatrix();
  assert.throws(
    () => matrix.grant({ userId: 'user-a', targetId: 'user-b', level: 1, expiresAt: 'not-a-date' }),
    { code: 'INVALID_LEVEL' }
  );
});

test('NOT_FOUND：grant 端点缺失（user 或 target）', () => {
  const matrix = new TrustMatrix();
  assert.throws(() => matrix.grant({ targetId: 'user-b', level: 1 }), { code: 'NOT_FOUND' });
  assert.throws(() => matrix.grant({ userId: 'user-a', level: 1 }), { code: 'NOT_FOUND' });
  assert.throws(() => matrix.grant({ userId: '  ', targetId: 'user-b', level: 1 }), { code: 'NOT_FOUND' });
});

test('revoke 返回 boolean：存在 true 且回落 STRANGER；不存在 false', () => {
  const matrix = new TrustMatrix();
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.TRUSTED });
  assert.equal(matrix.revoke({ userId: 'user-a', targetId: 'user-b' }), true);
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-b' }), TRUST_LEVELS.STRANGER);
  assert.equal(matrix.revoke({ userId: 'user-a', targetId: 'user-b' }), false);
  const last = matrix.history().at(-1);
  assert.equal(last.action, 'REVOKE');
  assert.equal(last.decision, 'NOT_FOUND');
});

test('边界：授权过期后 levelOf 回落 STRANGER，过期即视同撤销并留审计（只一次）', () => {
  const clock = makeClock();
  const matrix = new TrustMatrix({ clock });
  matrix.grant({
    userId: 'user-a',
    targetId: 'user-b',
    level: TRUST_LEVELS.FAMILY,
    expiresAt: '2026-10-21T01:00:00.000Z'
  });

  clock.advance(60 * 60e3 - 1);
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-b' }), TRUST_LEVELS.FAMILY);

  // 恰好到期即视同撤销（<= 判定）
  clock.advance(1);
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-b' }), TRUST_LEVELS.STRANGER);

  const afterExpire = matrix.history();
  const expireEvents = afterExpire.filter((event) => event.action === 'EXPIRE');
  assert.equal(expireEvents.length, 1);
  assert.equal(expireEvents[0].decision, 'REVOKED');
  assert.equal(expireEvents[0].detail.level, TRUST_LEVELS.FAMILY);

  // 再次访问不再重复产生 EXPIRE 审计
  assert.equal(matrix.levelOf({ userId: 'user-a', targetId: 'user-b' }), TRUST_LEVELS.STRANGER);
  assert.equal(matrix.history().filter((event) => event.action === 'EXPIRE').length, 1);
});

test('边界：list() 过滤过期授权，并按 targetId 确定性排序', () => {
  const clock = makeClock();
  const matrix = new TrustMatrix({ clock });
  matrix.grant({ userId: 'user-a', targetId: 'user-c', level: 2 });
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: 1 });
  matrix.grant({
    userId: 'user-a',
    targetId: 'user-d',
    level: 3,
    expiresAt: '2026-10-21T00:10:00.000Z'
  });
  assert.deepEqual(
    matrix.list('user-a').map((item) => item.targetId),
    ['user-b', 'user-c', 'user-d']
  );
  clock.advance(10 * 60e3);
  assert.deepEqual(
    matrix.list('user-a').map((item) => item.targetId),
    ['user-b', 'user-c']
  );
  assert.ok(Object.isFrozen(matrix.list('user-a')));
  assert.deepEqual(matrix.list('nobody'), []);
});

test('atLeast 数值方向：数值越小信任越高（SELF=0 最亲）', () => {
  const matrix = new TrustMatrix();
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.FAMILY });
  assert.equal(matrix.atLeast({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.FAMILY }), true);
  assert.equal(matrix.atLeast({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.SELF }), false);
  assert.equal(matrix.atLeast({ userId: 'user-a', targetId: 'user-b', level: TRUST_LEVELS.STRANGER }), true);
  assert.equal(matrix.atLeast({ userId: 'user-a', targetId: 'user-x', level: TRUST_LEVELS.STRANGER }), true);
});

test('审计事件形状为 {at,actor,action,target,decision,detail} 且 history 只追加/冻结', () => {
  const matrix = new TrustMatrix({ clock: makeClock() });
  matrix.grant({ userId: 'user-a', targetId: 'user-b', level: 1, grantedBy: 'user-a' });
  const snapshot = matrix.history();
  assert.ok(Object.isFrozen(snapshot));
  assert.throws(() => snapshot.push({}), TypeError);
  const event = snapshot[0];
  assert.deepEqual(Object.keys(event).sort(), ['action', 'actor', 'at', 'decision', 'detail', 'target']);
  assert.equal(event.actor, 'user-a');
  assert.equal(event.target, 'user-b');
  assert.equal(event.at, '2026-10-21T00:00:00.000Z');

  matrix.revoke({ userId: 'user-a', targetId: 'user-b' });
  assert.equal(snapshot.length, 1); // 旧快照不被改写
  assert.equal(matrix.history().length, 2);
});

test('TrustZoneError 带 code 且是 Error 子类', () => {
  const matrix = new TrustMatrix();
  try {
    matrix.grant({ userId: 'user-a', targetId: 'user-b', level: 7 });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof TrustZoneError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'INVALID_LEVEL');
  }
});
