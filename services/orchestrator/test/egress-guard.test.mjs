// 测试：出口合规护栏（契约 §7）。覆盖身份证/银行卡/转账话术/辱骂/外链/超长、零宽与全角绕过、BLOCK 拦截与审计绑定。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  DEFAULT_RULES,
  EgressBlockedError,
  EgressGuard,
  VERDICT
} from '../src/egress-guard.mjs';
import { makeClock } from './_helpers.mjs';

function makeGuard(options = {}) {
  const clock = options.clock ?? makeClock();
  const audit = [];
  const guard = new EgressGuard({
    clock,
    ...(options.rules ? { rules: options.rules } : {}),
    ...(options.maxLength ? { maxLength: options.maxLength } : {}),
    onAudit: (event) => audit.push(event)
  });
  return { clock, guard, audit };
}

const inspect = (guard, content, extra = {}) =>
  guard.inspect({ content, fromAgentId: 'agent-a', toKind: 'user', ...extra });

test('VERDICT/DEFAULT_RULES 冻结，默认规则覆盖契约要求的安全类别', () => {
  assert.deepEqual({ ...VERDICT }, { PASS: 'PASS', REVIEW: 'REVIEW', BLOCK: 'BLOCK' });
  assert.ok(Object.isFrozen(VERDICT));
  assert.ok(Object.isFrozen(DEFAULT_RULES));
  const ids = DEFAULT_RULES.map((rule) => rule.id);
  for (const required of ['id-card-cn', 'bank-card', 'transfer-talk', 'abuse', 'external-link', 'max-length']) {
    assert.ok(ids.includes(required), `默认规则缺少 ${required}`);
  }
  for (const rule of DEFAULT_RULES) {
    assert.ok(rule.pattern instanceof RegExp);
    assert.ok([VERDICT.BLOCK, VERDICT.REVIEW].includes(rule.verdict));
  }
});

test('PASS：正常内容放行，matched 为空，contentHash 为 sha256 hex，audit 绑定 messageId', () => {
  const { guard, audit } = makeGuard();
  const result = inspect(guard, '今天一起吃火锅吗？', { messageId: 'msg-1' });
  assert.equal(result.verdict, VERDICT.PASS);
  assert.deepEqual([...result.matched], []);
  assert.match(result.contentHash, /^[0-9a-f]{64}$/);
  assert.equal(result.at, '2026-10-21T00:00:00.000Z');
  assert.ok(Object.isFrozen(result));
  assert.equal(result.audit.target, 'msg-1');
  assert.equal(result.audit.decision, 'PASS');
  assert.equal(result.audit.action, 'EGRESS_INSPECT');
  assert.equal(audit.length, 1);
  assert.equal(audit[0], result.audit);
  assert.doesNotThrow(() => guard.assertPass({ content: '普通内容', fromAgentId: 'agent-a' }));
});

test('BLOCK：18 位身份证号；assertPass 抛 EGRESS_BLOCKED 并携带命中信息', () => {
  const { guard, audit } = makeGuard();
  const result = inspect(guard, '我的身份证是 110101199003071234，帮我登记', { messageId: 'msg-2' });
  assert.equal(result.verdict, VERDICT.BLOCK);
  assert.ok(result.matched.some((item) => item.id === 'id-card-cn'));
  assert.ok(result.matched.every((item) => typeof item.id === 'string' && 'match' in item));

  let caught = null;
  try {
    guard.assertPass({ content: '110101199003071234', fromAgentId: 'agent-a', messageId: 'msg-2' });
  } catch (error) {
    caught = error;
  }
  assert.ok(caught instanceof EgressBlockedError);
  assert.ok(caught instanceof Error);
  assert.equal(caught.code, 'EGRESS_BLOCKED');
  assert.equal(caught.messageId, 'msg-2');
  assert.match(caught.contentHash, /^[0-9a-f]{64}$/);
  assert.ok(audit.some((event) => event.decision === 'BLOCK'));
});

test('BLOCK：16–19 位银行卡号（含空格分隔）', () => {
  const { guard } = makeGuard();
  assert.equal(inspect(guard, '卡号 6222021234567890').verdict, VERDICT.BLOCK);
  assert.equal(inspect(guard, '卡号 6222 0212 3456 7890').verdict, VERDICT.BLOCK);
});

test('BLOCK：全角数字与零宽字符绕过被归一化后拦截', () => {
  const { guard } = makeGuard();
  assert.equal(inspect(guard, '６２２２０２１２３４５６７８９０').verdict, VERDICT.BLOCK);
  assert.equal(inspect(guard, '110101\u200B199003071234').verdict, VERDICT.BLOCK);
  assert.equal(inspect(guard, '110101\uFEFF1990030712\u00AD34').verdict, VERDICT.BLOCK);
});

test('BLOCK：转账/汇款话术与辱骂词表', () => {
  const { guard } = makeGuard();
  assert.equal(inspect(guard, '你先把钱转账给我，我马上还你').verdict, VERDICT.BLOCK);
  assert.equal(inspect(guard, '帮我汇款到这个账户').verdict, VERDICT.BLOCK);
  assert.equal(inspect(guard, '你个废物，滚蛋').verdict, VERDICT.BLOCK);
});

test('REVIEW：外部链接需人工复核；assertPass 对 REVIEW 放行', () => {
  const { guard } = makeGuard();
  const result = inspect(guard, '详情见 https://example.com/a');
  assert.equal(result.verdict, VERDICT.REVIEW);
  assert.ok(result.matched.some((item) => item.id === 'external-link'));
  assert.doesNotThrow(() => guard.assertPass({ content: '详情见 https://example.com/a', fromAgentId: 'agent-a' }));
});

test('优先级：同时命中 BLOCK 与 REVIEW 时判定 BLOCK', () => {
  const { guard } = makeGuard();
  const result = inspect(guard, '点这里 https://evil.com 然后转账给我');
  assert.equal(result.verdict, VERDICT.BLOCK);
  assert.ok(result.matched.some((item) => item.verdict === 'REVIEW'));
  assert.ok(result.matched.some((item) => item.verdict === 'BLOCK'));
});

test('边界：超长内容 BLOCK（maxLength 可配置）', () => {
  const { guard } = makeGuard({ maxLength: 20 });
  const result = inspect(guard, '正'.repeat(21));
  assert.equal(result.verdict, VERDICT.BLOCK);
  assert.ok(result.matched.some((item) => item.id === 'max-length'));
  assert.equal(inspect(guard, '正'.repeat(20)).verdict, VERDICT.PASS);
  // 默认规则对 4001+ 字符自带兜底
  const { guard: defaultGuard } = makeGuard();
  assert.equal(inspect(defaultGuard, 'a'.repeat(4001)).verdict, VERDICT.BLOCK);
});

test('自定义规则替换默认规则', () => {
  const { guard } = makeGuard({ rules: [{ id: 'secret-word', verdict: 'REVIEW', pattern: /机密/u, note: 'x' }] });
  const matched = inspect(guard, '这是机密文件');
  assert.equal(matched.verdict, VERDICT.REVIEW);
  assert.deepEqual(matched.matched.map((item) => item.id), ['secret-word']);
  assert.equal(inspect(guard, '普通内容').verdict, VERDICT.PASS);
});

test('contentHash 确定性：同内容同 hash（与 messageId 无关），不同内容不同 hash', () => {
  const { guard } = makeGuard();
  const first = inspect(guard, '同样内容', { messageId: 'msg-a' });
  const second = inspect(guard, '同样内容', { messageId: 'msg-b' });
  const different = inspect(guard, '不同内容');
  assert.equal(first.contentHash, second.contentHash);
  assert.notEqual(first.contentHash, different.contentHash);
  assert.equal(first.audit.target, 'msg-a');
  assert.equal(second.audit.target, 'msg-b');
});

test('空内容 PASS；返回对象与 matched 项均冻结', () => {
  const { guard } = makeGuard();
  const empty = inspect(guard, '');
  assert.equal(empty.verdict, VERDICT.PASS);
  assert.ok(Object.isFrozen(empty));
  assert.ok(Object.isFrozen(empty.matched));
  const blocked = inspect(guard, '转账 110101199003071234');
  assert.ok(Object.isFrozen(blocked.matched[0]));
});

test('EgressBlockedError 带 EGRESS_BLOCKED 且是 Error 子类', () => {
  const { guard } = makeGuard();
  try {
    guard.assertPass({ content: '转账给我', fromAgentId: 'agent-a' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof EgressBlockedError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'EGRESS_BLOCKED');
  }
});
