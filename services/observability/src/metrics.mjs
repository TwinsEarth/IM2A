// 模块职责：运营指标与告警——付费漏斗、审核时效、机审失败率、退款率可见，并在越线时给出可行动的告警。
// 契约来源：contracts/INTERFACES.md §41。
// 关键不变量：①漏斗转化率由计数器推导，分母为 0 时返回 0（绝不 NaN）；②observe 的 p50/p95 用最近窗口样本，样本 <1 返回 0；
//             ③告警规则是纯函数（只读快照/gauges，不读环境、不发网络）；④默认五条规则阈值冻结。
export const METRIC = Object.freeze({
  PAID_SENT: 'PAID_SENT',
  MODERATION_PASS: 'MODERATION_PASS',
  MODERATION_FLAG: 'MODERATION_FLAG',
  MODERATION_BLOCK: 'MODERATION_BLOCK',
  MODERATION_APPROVE: 'MODERATION_APPROVE',
  MODERATION_REJECT: 'MODERATION_REJECT',
  CHARGE: 'CHARGE',
  VIEWED: 'VIEWED',
  REPLIED: 'REPLIED',
  REFUND: 'REFUND',
  DISPUTE_OPEN: 'DISPUTE_OPEN',
  DISPUTE_APPROVE: 'DISPUTE_APPROVE',
  DISPUTE_REJECT: 'DISPUTE_REJECT',
  OTP_SENT: 'OTP_SENT',
  OTP_FAILED: 'OTP_FAILED'
});

export const ALERT_SEVERITY = Object.freeze({ INFO: 'INFO', WARN: 'WARN', CRITICAL: 'CRITICAL' });

const METRIC_NAMES = Object.freeze(Object.values(METRIC));
/** observe 的样本窗口：只保留最近 N 个样本用于 p50/p95（契约要求"最近窗口样本"） */
const HISTOGRAM_WINDOW = 1000;
const isFiniteNumber = (value) => typeof value === 'number' && Number.isFinite(value);

const safeRatio = (numerator, denominator) => {
  if (!isFiniteNumber(numerator) || !isFiniteNumber(denominator) || denominator <= 0) return 0;
  return numerator / denominator;
};

/** 最近秩百分位（nearest-rank）：p50 of [1..100] = 50，p95 = 95 */
function percentile(sortedSamples, p) {
  if (sortedSamples.length < 1) return 0;
  const index = Math.min(sortedSamples.length - 1, Math.max(0, Math.ceil((p / 100) * sortedSamples.length) - 1));
  return sortedSamples[index];
}

const labelsKey = (labels) => {
  if (!labels || typeof labels !== 'object') return '';
  return Object.keys(labels)
    .sort()
    .map((key) => `${key}=${String(labels[key])}`)
    .join('&');
};

export class Metrics {
  #clock;
  /** @type {Map<string, number>} name -> flat counter */
  #counters;
  /** @type {Map<string, Map<string, number>>} name -> labelKey -> counter（维度观测，便于下钻） */
  #labeledCounters;
  /** @type {Map<string, number[]>} name -> 最近窗口样本（ms） */
  #samples;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#counters = new Map(METRIC_NAMES.map((name) => [name, 0]));
    this.#labeledCounters = new Map();
    this.#samples = new Map();
  }

  #assertName(name, method) {
    if (typeof name !== 'string' || name.trim() === '') {
      throw new TypeError(`${method}: name 必须是非空字符串`);
    }
  }

  /** 计数器累加（labels 可选，用于按维度下钻；扁平计数器始终可用） */
  inc(name, value = 1, labels = {}) {
    this.#assertName(name, 'inc');
    if (!isFiniteNumber(value)) throw new TypeError('inc: value 必须是有限数字');
    this.#counters.set(name, (this.#counters.get(name) ?? 0) + value);
    const key = labelsKey(labels);
    if (key !== '') {
      let bucket = this.#labeledCounters.get(name);
      if (!bucket) {
        bucket = new Map();
        this.#labeledCounters.set(name, bucket);
      }
      bucket.set(key, (bucket.get(key) ?? 0) + value);
    }
    return this.#counters.get(name);
  }

  /** 直方图观测（ms）：count/min/p50/p95/max，样本窗口为最近 1000 个 */
  observe(name, ms, labels = {}) {
    this.#assertName(name, 'observe');
    if (!isFiniteNumber(ms) || ms < 0) throw new TypeError('observe: ms 必须是非负有限数字');
    void labels; // 直方图窗口按 name 聚合；labels 维度下钻由调用方自建（保持快照形状稳定）
    let samples = this.#samples.get(name);
    if (!samples) {
      samples = [];
      this.#samples.set(name, samples);
    }
    samples.push(ms);
    if (samples.length > HISTOGRAM_WINDOW) samples.splice(0, samples.length - HISTOGRAM_WINDOW);
    return this.#histogramOf(name);
  }

  #histogramOf(name) {
    const samples = this.#samples.get(name) ?? [];
    if (samples.length < 1) {
      return Object.freeze({ count: 0, min: 0, p50: 0, p95: 0, max: 0 });
    }
    const sorted = [...samples].sort((a, b) => a - b);
    return Object.freeze({
      count: sorted.length,
      min: sorted[0],
      p50: percentile(sorted, 50),
      p95: percentile(sorted, 95),
      max: sorted.at(-1)
    });
  }

  #counter(name) {
    return this.#counters.get(name) ?? 0;
  }

  /** 冻结快照：{ counters, histograms, rates, funnel, generatedAt }（含维度计数器 labeledCounters 便于下钻） */
  snapshot() {
    const counters = {};
    for (const [name, value] of this.#counters) counters[name] = value;

    const histograms = {};
    for (const name of METRIC_NAMES) histograms[name] = this.#histogramOf(name);
    for (const name of this.#samples.keys()) histograms[name] = this.#histogramOf(name);

    const labeledCounters = {};
    for (const [name, bucket] of this.#labeledCounters) {
      const labels = {};
      for (const [key, value] of bucket) labels[key] = value;
      labeledCounters[name] = Object.freeze(labels);
    }

    const paidSent = this.#counter(METRIC.PAID_SENT);
    const machinePassed = this.#counter(METRIC.MODERATION_PASS);
    const machineFlagged = this.#counter(METRIC.MODERATION_FLAG);
    const machineBlocked = this.#counter(METRIC.MODERATION_BLOCK);
    const moderationEntered = machinePassed + machineFlagged; // PASS + HUMAN_PENDING(FLAG)
    const machineEvaluated = machinePassed + machineFlagged + machineBlocked;
    const charged = this.#counter(METRIC.CHARGE);
    const viewed = this.#counter(METRIC.VIEWED);
    const replied = this.#counter(METRIC.REPLIED);
    const refunded = this.#counter(METRIC.REFUND);
    const settled = this.#counter('SETTLED'); // 契约漏斗末段；METRIC 常量未单列，由调用方以计数器名 'SETTLED' 累加
    const otpSent = this.#counter(METRIC.OTP_SENT);
    const otpFailed = this.#counter(METRIC.OTP_FAILED);

    const rates = Object.freeze({
      // 机审失败率 = 未自动通过（FLAG+BLOCK）/ 机审总量
      moderationFailureRate: safeRatio(machineFlagged + machineBlocked, machineEvaluated),
      moderationPassRate: safeRatio(machinePassed, machineEvaluated),
      // 退款率 = REFUND / CHARGE
      refundRate: safeRatio(refunded, charged),
      // 短信失败率 = OTP_FAILED / (OTP_SENT + OTP_FAILED)
      smsFailureRate: safeRatio(otpFailed, otpSent + otpFailed),
      // 人审通过率
      humanApprovalRate: safeRatio(
        this.#counter(METRIC.MODERATION_APPROVE),
        this.#counter(METRIC.MODERATION_APPROVE) + this.#counter(METRIC.MODERATION_REJECT)
      )
    });

    const counts = Object.freeze({
      PAID_SENT: paidSent,
      MODERATION: moderationEntered,
      CHARGE: charged,
      VIEWED: viewed,
      REPLIED: replied,
      SETTLED: settled
    });
    const conversionRates = Object.freeze({
      MODERATION: safeRatio(moderationEntered, paidSent),
      CHARGE: safeRatio(charged, moderationEntered),
      VIEWED: safeRatio(viewed, charged),
      REPLIED: safeRatio(replied, viewed),
      SETTLED: safeRatio(settled, replied)
    });
    const funnel = Object.freeze({
      counts,
      conversionRates,
      stages: Object.freeze(
        ['PAID_SENT', 'MODERATION', 'CHARGE', 'VIEWED', 'REPLIED', 'SETTLED'].map((stage, index, list) => {
          const previous = index === 0 ? null : counts[list[index - 1]];
          return Object.freeze({
            stage,
            count: counts[stage],
            conversionFromPrevious: index === 0 ? (counts[stage] > 0 ? 1 : 0) : safeRatio(counts[stage], previous)
          });
        })
      )
    });

    const sampled = this.#clock();
    const generatedAt = (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
    return Object.freeze({
      counters: Object.freeze(counters),
      histograms: Object.freeze(histograms),
      labeledCounters: Object.freeze(labeledCounters),
      rates,
      funnel,
      generatedAt
    });
  }
}

// ── 告警：纯函数 + 默认规则 ──────────────────────────────────────────────────

const freezeRule = (rule) => Object.freeze({ ...rule });

/** 默认五条规则（契约 §41 冻结阈值） */
export const DEFAULT_ALERT_RULES = Object.freeze([
  freezeRule({
    id: 'machine-failure-rate',
    severity: ALERT_SEVERITY.WARN,
    metric: 'moderationFailureRate',
    comparator: 'gt',
    threshold: 0.1,
    description: '机审失败率过高'
  }),
  freezeRule({
    id: 'human-review-backlog',
    severity: ALERT_SEVERITY.WARN,
    metric: 'humanReviewBacklog',
    comparator: 'gt',
    threshold: 20,
    description: '人工审核积压'
  }),
  freezeRule({
    id: 'refund-rate',
    severity: ALERT_SEVERITY.CRITICAL,
    metric: 'refundRate',
    comparator: 'gt',
    threshold: 0.3,
    description: '退款率过高'
  }),
  freezeRule({
    id: 'sms-failure-rate',
    severity: ALERT_SEVERITY.WARN,
    metric: 'smsFailureRate',
    comparator: 'gt',
    threshold: 0.1,
    description: '短信失败率过高'
  }),
  freezeRule({
    id: 'reconciliation-diff',
    severity: ALERT_SEVERITY.CRITICAL,
    metric: 'reconciliationDiffCents',
    comparator: 'ne',
    threshold: 0,
    description: '对账存在差异'
  })
]);

const gaugeNumber = (value) => (isFiniteNumber(value) ? value : 0);

/** 从快照/gauges 解析规则指标值（纯函数） */
function resolveMetric(metric, snapshot, gauges) {
  if (metric === 'humanReviewBacklog') {
    return gaugeNumber(gauges.humanReviewBacklog ?? gauges.pendingHumanReview ?? 0);
  }
  if (metric === 'reconciliationDiffCents' || metric === 'reconciliationDiff') {
    return gaugeNumber(gauges.reconciliationDiffCents ?? gauges.reconciliationDiff ?? 0);
  }
  return isFiniteNumber(snapshot?.rates?.[metric]) ? snapshot.rates[metric] : 0;
}

/**
 * 告警求值（纯函数：同样输入两次结果一致，不读环境/不发网络）。
 * @returns {ReadonlyArray<{id:string, severity:string, message:string, value:number, threshold:number}>}
 */
export function evaluateAlerts({ snapshot, gauges = {} } = {}, rules = DEFAULT_ALERT_RULES) {
  const safeSnapshot = snapshot ?? { rates: {}, counters: {}, funnel: {} };
  const safeGauges = gauges ?? {};
  const alerts = [];
  for (const rule of Array.isArray(rules) ? rules : []) {
    if (!rule || typeof rule !== 'object' || typeof rule.id !== 'string') continue;
    const value = resolveMetric(rule.metric, safeSnapshot, safeGauges);
    const threshold = isFiniteNumber(rule.threshold) ? rule.threshold : 0;
    const triggered = rule.comparator === 'ne' ? value !== threshold : value > threshold;
    if (!triggered) continue;
    alerts.push(
      Object.freeze({
        id: rule.id,
        severity: rule.severity ?? ALERT_SEVERITY.WARN,
        message: `${rule.description ?? rule.id}: value=${value}, threshold=${threshold}`,
        value,
        threshold
      })
    );
  }
  return Object.freeze(alerts);
}
