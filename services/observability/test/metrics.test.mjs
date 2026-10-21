// 测试：运营指标与告警（契约 §41）。
// 覆盖：计数器/直方图分位、漏斗转化率（0 分母安全）、默认五条告警的越线/未越线边界、纯函数告警。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ALERT_SEVERITY,
  DEFAULT_ALERT_RULES,
  METRIC,
  Metrics,
  evaluateAlerts
} from '../src/metrics.mjs';

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

const buildMetrics = (increments = {}, observations = {}) => {
  const clock = makeClock();
  const metrics = new Metrics({ clock });
  for (const [name, value] of Object.entries(increments)) metrics.inc(name, value);
  for (const [name, samples] of Object.entries(observations)) {
    for (const sample of samples) metrics.observe(name, sample);
  }
  return { clock, metrics };
};

const closeTo = (actual, expected, epsilon = 1e-9) => {
  assert.ok(Math.abs(actual - expected) <= epsilon, `期望 ${expected}，实际 ${actual}`);
};

test('METRIC / ALERT_SEVERITY 常量冻结且与契约一致', () => {
  const expected = [
    'PAID_SENT',
    'MODERATION_PASS',
    'MODERATION_FLAG',
    'MODERATION_BLOCK',
    'MODERATION_APPROVE',
    'MODERATION_REJECT',
    'CHARGE',
    'VIEWED',
    'REPLIED',
    'REFUND',
    'DISPUTE_OPEN',
    'DISPUTE_APPROVE',
    'DISPUTE_REJECT',
    'OTP_SENT',
    'OTP_FAILED'
  ];
  assert.deepEqual(Object.keys(METRIC), expected);
  assert.equal(Object.keys(METRIC).length, 15);
  assert.deepEqual({ ...ALERT_SEVERITY }, { INFO: 'INFO', WARN: 'WARN', CRITICAL: 'CRITICAL' });
  assert.ok(Object.isFrozen(METRIC));
  assert.ok(Object.isFrozen(ALERT_SEVERITY));
});

test('inc 累加；snapshot 含全部计数器/直方图/rates/funnel/generatedAt 且冻结', () => {
  const { clock, metrics } = buildMetrics({ [METRIC.PAID_SENT]: 3, [METRIC.CHARGE]: 2 });
  clock.advance(5000);
  metrics.inc(METRIC.PAID_SENT); // 默认 1
  metrics.inc(METRIC.PAID_SENT, 2, { channel: 'DIRECT' });

  const snapshot = metrics.snapshot();
  assert.equal(snapshot.counters[METRIC.PAID_SENT], 6);
  assert.equal(snapshot.counters[METRIC.CHARGE], 2);
  assert.equal(snapshot.counters[METRIC.REFUND], 0); // 未打点也有 0
  assert.equal(snapshot.generatedAt, '2026-10-21T00:00:05.000Z');
  assert.deepEqual({ ...snapshot.counters, ...{} }, { ...snapshot.counters }); // 可枚举
  assert.ok(Object.isFrozen(snapshot));
  assert.ok(Object.isFrozen(snapshot.counters));
  assert.ok(Object.isFrozen(snapshot.histograms));
  assert.ok(Object.isFrozen(snapshot.rates));
  assert.ok(Object.isFrozen(snapshot.funnel));
  // 维度计数器
  assert.equal(snapshot.labeledCounters[METRIC.PAID_SENT]['channel=DIRECT'], 2);
});

test('observe：count/min/p50/p95/max 正确（最近秩），空样本返回 0', () => {
  const { metrics } = buildMetrics({}, { PLACED_ORDER: Array.from({ length: 100 }, (_, index) => index + 1) });
  metrics.observe('LATENCY_MS', 5);
  metrics.observe('LATENCY_MS', 9);

  const latency = metrics.snapshot().histograms.LATENCY_MS;
  assert.deepEqual({ ...latency }, { count: 2, min: 5, p50: 5, p95: 9, max: 9 });

  const hundred = metrics.snapshot().histograms.PLACED_ORDER;
  assert.equal(hundred.count, 100);
  assert.equal(hundred.min, 1);
  assert.equal(hundred.p50, 50);
  assert.equal(hundred.p95, 95);
  assert.equal(hundred.max, 100);

  // 从未 observe 的指标：样本 <1，各分位返回 0
  const untouched = metrics.snapshot().histograms[METRIC.REFUND];
  assert.deepEqual({ ...untouched }, { count: 0, min: 0, p50: 0, p95: 0, max: 0 });
});

test('observe 窗口：只保留最近 1000 个样本（p50/p95 用窗口样本）', () => {
  const { metrics } = buildMetrics();
  for (let value = 1; value <= 1005; value += 1) metrics.observe('WINDOWED', value);
  const histogram = metrics.snapshot().histograms.WINDOWED;
  assert.equal(histogram.count, 1000);
  assert.equal(histogram.min, 6);
  assert.equal(histogram.max, 1005);
  assert.equal(histogram.p50, 505);
});

test('漏斗：PAID_SENT→审核→CHARGE→VIEWED→REPLIED→SETTLED 计数与转化率；0 分母返回 0（不是 NaN）', () => {
  const { metrics } = buildMetrics({
    [METRIC.PAID_SENT]: 100,
    [METRIC.MODERATION_PASS]: 60,
    [METRIC.MODERATION_FLAG]: 20,
    [METRIC.CHARGE]: 40,
    [METRIC.VIEWED]: 30,
    [METRIC.REPLIED]: 20,
    SETTLED: 10
  });
  const funnel = metrics.snapshot().funnel;
  assert.deepEqual({ ...funnel.counts }, {
    PAID_SENT: 100,
    MODERATION: 80,
    CHARGE: 40,
    VIEWED: 30,
    REPLIED: 20,
    SETTLED: 10
  });
  closeTo(funnel.conversionRates.MODERATION, 0.8);
  closeTo(funnel.conversionRates.CHARGE, 0.5);
  closeTo(funnel.conversionRates.VIEWED, 0.75);
  closeTo(funnel.conversionRates.REPLIED, 20 / 30);
  closeTo(funnel.conversionRates.SETTLED, 0.5);
  assert.equal(funnel.stages.length, 6);
  assert.equal(funnel.stages[0].stage, 'PAID_SENT');
  assert.equal(funnel.stages.at(-1).stage, 'SETTLED');

  // 全新快照：所有比率都是 0，且不是 NaN
  const empty = buildMetrics().metrics.snapshot().funnel;
  for (const value of Object.values(empty.conversionRates)) {
    assert.equal(Number.isNaN(value), false);
    assert.equal(value, 0);
  }
});

test('rates：机审失败率/退款率/短信失败率/人审通过率；分母为 0 时 0', () => {
  const { metrics } = buildMetrics({
    [METRIC.MODERATION_PASS]: 80,
    [METRIC.MODERATION_FLAG]: 10,
    [METRIC.MODERATION_BLOCK]: 10,
    [METRIC.CHARGE]: 10,
    [METRIC.REFUND]: 4,
    [METRIC.OTP_SENT]: 8,
    [METRIC.OTP_FAILED]: 2,
    [METRIC.MODERATION_APPROVE]: 3,
    [METRIC.MODERATION_REJECT]: 1
  });
  const rates = metrics.snapshot().rates;
  closeTo(rates.moderationFailureRate, 0.2);
  closeTo(rates.moderationPassRate, 0.8);
  closeTo(rates.refundRate, 0.4);
  closeTo(rates.smsFailureRate, 0.2);
  closeTo(rates.humanApprovalRate, 0.75);

  const empty = buildMetrics().metrics.snapshot().rates;
  for (const value of Object.values(empty)) {
    assert.equal(Number.isNaN(value), false);
    assert.equal(value, 0);
  }
});

test('DEFAULT_ALERT_RULES：恰好五条、冻结、阈值与严重级别符合契约', () => {
  assert.equal(DEFAULT_ALERT_RULES.length, 5);
  assert.ok(Object.isFrozen(DEFAULT_ALERT_RULES));
  assert.ok(DEFAULT_ALERT_RULES.every((rule) => Object.isFrozen(rule)));
  assert.deepEqual(
    DEFAULT_ALERT_RULES.map((rule) => `${rule.id}:${rule.severity}`),
    [
      'machine-failure-rate:WARN',
      'human-review-backlog:WARN',
      'refund-rate:CRITICAL',
      'sms-failure-rate:WARN',
      'reconciliation-diff:CRITICAL'
    ]
  );
  assert.deepEqual(DEFAULT_ALERT_RULES.map((rule) => rule.threshold), [0.1, 20, 0.3, 0.1, 0]);
});

test('告警① 机审失败率：刚好 10% 不告警；>10% → WARN', () => {
  const atThreshold = buildMetrics({
    [METRIC.MODERATION_PASS]: 90,
    [METRIC.MODERATION_FLAG]: 10
  }).metrics.snapshot();
  assert.deepEqual([...evaluateAlerts({ snapshot: atThreshold })], []);

  const over = buildMetrics({
    [METRIC.MODERATION_PASS]: 89,
    [METRIC.MODERATION_FLAG]: 11
  }).metrics.snapshot();
  const alerts = evaluateAlerts({ snapshot: over });
  assert.equal(alerts.length, 1);
  assert.equal(alerts[0].id, 'machine-failure-rate');
  assert.equal(alerts[0].severity, ALERT_SEVERITY.WARN);
  closeTo(alerts[0].value, 0.11);
  assert.equal(alerts[0].threshold, 0.1);
});

test('告警② 人审积压：20 不告警；21 → WARN', () => {
  const empty = buildMetrics().metrics.snapshot();
  assert.deepEqual([...evaluateAlerts({ snapshot: empty, gauges: { humanReviewBacklog: 20 } })], []);
  const alerts = evaluateAlerts({ snapshot: empty, gauges: { humanReviewBacklog: 21 } });
  assert.equal(alerts.length, 1);
  assert.equal(alerts[0].id, 'human-review-backlog');
  assert.equal(alerts[0].severity, ALERT_SEVERITY.WARN);
  assert.equal(alerts[0].value, 21);
});

test('告警③ 退款率：刚好 30% 不告警；>30% → CRITICAL', () => {
  const atThreshold = buildMetrics({ [METRIC.CHARGE]: 10, [METRIC.REFUND]: 3 }).metrics.snapshot();
  assert.deepEqual([...evaluateAlerts({ snapshot: atThreshold })], []);
  const over = buildMetrics({ [METRIC.CHARGE]: 10, [METRIC.REFUND]: 4 }).metrics.snapshot();
  const alerts = evaluateAlerts({ snapshot: over });
  assert.equal(alerts.length, 1);
  assert.equal(alerts[0].id, 'refund-rate');
  assert.equal(alerts[0].severity, ALERT_SEVERITY.CRITICAL);
  closeTo(alerts[0].value, 0.4);
});

test('告警④ 短信失败率：刚好 10% 不告警；>10% → WARN', () => {
  const atThreshold = buildMetrics({ [METRIC.OTP_SENT]: 9, [METRIC.OTP_FAILED]: 1 }).metrics.snapshot();
  assert.deepEqual([...evaluateAlerts({ snapshot: atThreshold })], []);
  const over = buildMetrics({ [METRIC.OTP_SENT]: 9, [METRIC.OTP_FAILED]: 2 }).metrics.snapshot();
  const alerts = evaluateAlerts({ snapshot: over });
  assert.equal(alerts.length, 1);
  assert.equal(alerts[0].id, 'sms-failure-rate');
  assert.equal(alerts[0].severity, ALERT_SEVERITY.WARN);
});

test('告警⑤ 对账差异：0 不告警；≠0 → CRITICAL', () => {
  const empty = buildMetrics().metrics.snapshot();
  assert.deepEqual([...evaluateAlerts({ snapshot: empty, gauges: { reconciliationDiffCents: 0 } })], []);
  const alerts = evaluateAlerts({ snapshot: empty, gauges: { reconciliationDiffCents: -1 } });
  assert.equal(alerts.length, 1);
  assert.equal(alerts[0].id, 'reconciliation-diff');
  assert.equal(alerts[0].severity, ALERT_SEVERITY.CRITICAL);
  assert.equal(alerts[0].value, -1);
});

test('纯函数与边界：空快照不告警、同输入两次结果一致、显式空 rules 不告警、自定义规则生效', () => {
  const emptySnapshot = buildMetrics().metrics.snapshot();
  assert.deepEqual([...evaluateAlerts({ snapshot: emptySnapshot })], []);
  assert.deepEqual([...evaluateAlerts({ snapshot: emptySnapshot, gauges: {} })], []);

  const busySnapshot = buildMetrics({ [METRIC.CHARGE]: 10, [METRIC.REFUND]: 5 }).metrics.snapshot();
  const first = evaluateAlerts({ snapshot: busySnapshot, gauges: { humanReviewBacklog: 100 } });
  const second = evaluateAlerts({ snapshot: busySnapshot, gauges: { humanReviewBacklog: 100 } });
  assert.deepEqual(first, second);
  assert.ok(Object.isFrozen(first));
  assert.ok(first.every((alert) => Object.isFrozen(alert)));
  assert.equal(first.length, 2); // refund-rate + human-review-backlog

  // 规则显式传空数组 => 不做任何告警
  assert.deepEqual([...evaluateAlerts({ snapshot: busySnapshot }, [])], []);

  // 自定义规则
  const custom = [
    { id: 'custom-refund', severity: ALERT_SEVERITY.INFO, metric: 'refundRate', comparator: 'gt', threshold: 0.2, description: '自定义退款阈值' }
  ];
  const customAlerts = evaluateAlerts({ snapshot: busySnapshot }, custom);
  assert.equal(customAlerts.length, 1);
  assert.equal(customAlerts[0].id, 'custom-refund');
  assert.equal(customAlerts[0].severity, ALERT_SEVERITY.INFO);
  assert.match(customAlerts[0].message, /value=0.5/);
});
