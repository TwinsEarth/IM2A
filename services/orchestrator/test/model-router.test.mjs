// 测试：敏感场景模型路由 R7（契约 §24）。
// 覆盖：家庭/法律/医疗/记忆域钉本地、家庭信任档走本地、普通工作域走云端、显式覆盖可审计、setRule 覆盖默认判断、全部 ROUTE_CODE。
import test from 'node:test';
import assert from 'node:assert/strict';

import { MODEL_TIER, ModelRouter, ModelRouterError, ROUTE_CODE } from '../src/model-router.mjs';
import { TRUST_LEVELS } from '../src/trust-zone.mjs';

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

const makeRouter = (options = {}) => {
  const clock = options.clock ?? makeClock();
  return { clock, router: new ModelRouter({ clock, ...options }) };
};

test('MODEL_TIER / ROUTE_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...MODEL_TIER }, { LOCAL: 'LOCAL', CLOUD: 'CLOUD' });
  assert.deepEqual({ ...ROUTE_CODE }, {
    ROUTED_LOCAL: 'ROUTED_LOCAL',
    ROUTED_CLOUD: 'ROUTED_CLOUD',
    DOMAIN_SENSITIVE: 'DOMAIN_SENSITIVE',
    TRUST_SENSITIVE: 'TRUST_SENSITIVE',
    EXPLICIT_OVERRIDE: 'EXPLICIT_OVERRIDE'
  });
  assert.ok(Object.isFrozen(MODEL_TIER));
  assert.ok(Object.isFrozen(ROUTE_CODE));
});

test('普通工作域 + 陌生人/点头之交信任 => CLOUD / ROUTED_CLOUD', () => {
  const { router } = makeRouter();
  const result = router.route({ domain: 'work', trustLevel: TRUST_LEVELS.STRANGER });
  assert.equal(result.tier, MODEL_TIER.CLOUD);
  assert.equal(result.model, 'cloud-large');
  assert.equal(result.code, 'ROUTED_CLOUD');
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.reasons));
  assert.ok(result.reasons.length > 0);

  assert.equal(router.route({ domain: 'work', trustLevel: TRUST_LEVELS.KNOWN }).code, 'ROUTED_CLOUD');
  assert.equal(router.route({ domain: 'work', trustLevel: TRUST_LEVELS.TRUSTED }).code, 'ROUTED_CLOUD');
});

test('家庭事务（trustLevel=FAMILY）走 LOCAL / TRUST_SENSITIVE（家庭事务不出端侧）', () => {
  const { router } = makeRouter();
  const family = router.route({ domain: 'chores', trustLevel: TRUST_LEVELS.FAMILY });
  assert.equal(family.tier, MODEL_TIER.LOCAL);
  assert.equal(family.model, 'local-small');
  assert.equal(family.code, 'TRUST_SENSITIVE');
  assert.match(family.reasons.join(' '), /trustLevel=1/);
  // SELF 更亲密，同样本地
  assert.equal(router.route({ domain: 'chores', trustLevel: TRUST_LEVELS.SELF }).code, 'TRUST_SENSITIVE');
});

test('敏感域 family/legal/medical/memory 走 LOCAL / DOMAIN_SENSITIVE（与信任档无关）', () => {
  const { router } = makeRouter();
  for (const domain of ['family', 'legal', 'medical', 'memory']) {
    const result = router.route({ domain, trustLevel: TRUST_LEVELS.STRANGER });
    assert.equal(result.tier, MODEL_TIER.LOCAL, `${domain} 必须走本地`);
    assert.equal(result.model, 'local-small');
    assert.equal(result.code, 'DOMAIN_SENSITIVE');
    assert.match(result.reasons.join(' '), new RegExp(`sensitive domain: ${domain}`));
  }
  // 非敏感域（如 legal-advice 只是字符串包含 legal 不算命中，必须精确域）
  assert.equal(router.route({ domain: 'legal-advice', trustLevel: 4 }).code, 'ROUTED_CLOUD');
});

test('显式覆盖：非敏感工作域也能钉 LOCAL，且 reasons 写明是显式覆盖（可审计）', () => {
  const { router } = makeRouter();
  const local = router.route({ domain: 'work', trustLevel: TRUST_LEVELS.STRANGER, explicitTier: MODEL_TIER.LOCAL });
  assert.equal(local.tier, MODEL_TIER.LOCAL);
  assert.equal(local.model, 'local-small');
  assert.equal(local.code, 'EXPLICIT_OVERRIDE');
  assert.match(local.reasons.join(' '), /explicit override/i);
  assert.match(local.reasons.join(' '), /LOCAL/);
  assert.match(local.reasons.join(' '), /domain=work/);

  // 敏感域也能被显式覆盖到云端（覆盖优先级最高）
  const cloud = router.route({ domain: 'family', trustLevel: TRUST_LEVELS.FAMILY, explicitTier: MODEL_TIER.CLOUD });
  assert.equal(cloud.tier, MODEL_TIER.CLOUD);
  assert.equal(cloud.model, 'cloud-large');
  assert.equal(cloud.code, 'EXPLICIT_OVERRIDE');
});

test('setRule 覆盖默认敏感域判断：工作域钉本地 => ROUTED_LOCAL；敏感域改云端 => ROUTED_CLOUD', () => {
  const { router, clock } = makeRouter();
  const rule = router.setRule({ domain: 'work', tier: MODEL_TIER.LOCAL, note: '工作域也涉密' });
  assert.ok(Object.isFrozen(rule));
  assert.equal(rule.domain, 'work');
  assert.equal(rule.tier, MODEL_TIER.LOCAL);
  assert.equal(rule.note, '工作域也涉密');
  assert.equal(rule.updatedAt, '2026-10-21T00:00:00.000Z');

  const local = router.route({ domain: 'work', trustLevel: TRUST_LEVELS.STRANGER });
  assert.equal(local.tier, MODEL_TIER.LOCAL);
  assert.equal(local.code, 'ROUTED_LOCAL');
  assert.match(local.reasons.join(' '), /rule override/);

  clock.advance(1000);
  router.setRule({ domain: 'family', tier: MODEL_TIER.CLOUD, note: '家庭域允许云端（用户明确同意）' });
  const cloud = router.route({ domain: 'family', trustLevel: TRUST_LEVELS.FAMILY });
  assert.equal(cloud.tier, MODEL_TIER.CLOUD);
  assert.equal(cloud.code, 'ROUTED_CLOUD');
  assert.match(cloud.reasons.join(' '), /rule override/);

  // 覆盖规则可再次覆盖
  clock.advance(1000);
  router.setRule({ domain: 'work', tier: MODEL_TIER.CLOUD });
  assert.equal(router.route({ domain: 'work', trustLevel: 4 }).code, 'ROUTED_CLOUD');
});

test('优先级：显式覆盖 > setRule > 敏感域 > 信任档 > 默认云端', () => {
  const { router } = makeRouter();
  router.setRule({ domain: 'work', tier: MODEL_TIER.LOCAL });
  const explicit = router.route({ domain: 'work', trustLevel: 4, explicitTier: MODEL_TIER.CLOUD });
  assert.equal(explicit.tier, MODEL_TIER.CLOUD);
  assert.equal(explicit.code, 'EXPLICIT_OVERRIDE');

  // setRule 也克制敏感域：legal 被规则改成 CLOUD 后不再 DOMAIN_SENSITIVE
  router.setRule({ domain: 'legal', tier: MODEL_TIER.CLOUD });
  assert.equal(router.route({ domain: 'legal', trustLevel: 4 }).code, 'ROUTED_CLOUD');
});

test('自定义构造：sensitiveDomains / localModel / cloudModel 生效', () => {
  const { router } = makeRouter({
    sensitiveDomains: ['finance'],
    localModel: 'edge-tiny',
    cloudModel: 'cloud-xl'
  });
  const finance = router.route({ domain: 'finance', trustLevel: 4 });
  assert.equal(finance.tier, MODEL_TIER.LOCAL);
  assert.equal(finance.model, 'edge-tiny');
  assert.equal(finance.code, 'DOMAIN_SENSITIVE');

  const work = router.route({ domain: 'work', trustLevel: 4 });
  assert.equal(work.tier, MODEL_TIER.CLOUD);
  assert.equal(work.model, 'cloud-xl');
  assert.equal(work.code, 'ROUTED_CLOUD');
});

test('入参归一：trustLevel 越界钳制；domain 缺失走默认云端；显式 tier 非法 => TypeError', () => {
  const { router } = makeRouter();
  assert.equal(router.route({ domain: 'work', trustLevel: 99 }).code, 'ROUTED_CLOUD');
  assert.equal(router.route({ domain: 'work', trustLevel: -1 }).code, 'TRUST_SENSITIVE');
  assert.equal(router.route({ domain: 'work', trustLevel: Number.NaN }).code, 'ROUTED_CLOUD');
  assert.equal(router.route({}).code, 'ROUTED_CLOUD');

  assert.throws(() => router.route({ domain: 'work', explicitTier: 'EDGE' }), TypeError);
  assert.throws(() => router.setRule({ domain: 'work', tier: 'EDGE' }), TypeError);
  assert.throws(() => router.setRule({ tier: MODEL_TIER.LOCAL }), TypeError);
  assert.throws(() => router.setRule({ domain: '', tier: MODEL_TIER.LOCAL }), TypeError);
});

test('ModelRouterError 是 Error 子类（契约导出；路由判定本身只返回结论不抛业务异常）', () => {
  const error = new ModelRouterError('x', 'SOME_CODE');
  assert.ok(error instanceof Error);
  assert.equal(error.name, 'ModelRouterError');
  assert.equal(error.code, 'SOME_CODE');
});
