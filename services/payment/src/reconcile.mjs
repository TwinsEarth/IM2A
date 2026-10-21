// 模块职责：日切对账——渠道说收了、账本没记（或反之）是最贵的故障；对账必须逐笔说明"差在哪一笔"，且只读不改任何状态 / 契约来源：contracts/INTERFACES.md §50（读 §8 ledger 的 DEPOSIT 流水 + §49 PaymentIntentService 的订单视图）/ 关键不变量：①以订单/入账金额为口径（不按笔数凑）；②missingInLedger 与 mismatched 必须逐笔列出；③ok 仅在 mismatched 与 missingInLedger 均为空时为 true（missingInProvider 不改变 ok，但会列出）；④对账只读；⑤空数据日 checked:0、ok:true。
export const RECONCILE_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  MISMATCH: 'MISMATCH',
  INVALID_ARGUMENT: 'INVALID_ARGUMENT',
});

const CODE = RECONCILE_CODE;
const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
/** 对账只关心"已支付/已退款"的订单（两者都应有渠道入金流水） */
const RECONCILABLE_STATES = new Set(['PAID', 'REFUNDED']);
/** 支付入账在账本里的幂等键前缀（§49） */
const DEPOSIT_KEY_PREFIX = 'deposit:';
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

function dayOf(iso) {
  if (typeof iso !== 'string' || iso.length < 10) return null;
  const day = iso.slice(0, 10);
  return DATE_PATTERN.test(day) ? day : null;
}

export class ReconcileError extends Error {
  constructor(message, code = CODE.INVALID_ARGUMENT) {
    super(message);
    this.name = 'ReconcileError';
    this.code = code;
  }
}

export class Reconciler {
  #clock;
  #ledger;
  #payments;

  constructor({ clock = () => new Date(), ledger, payments } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!ledger || typeof ledger !== 'object' || typeof ledger.entries !== 'function') {
      throw new TypeError('ledger 必填（§8 Ledger 实例：账本是对账的唯一真相）');
    }
    if (!payments || typeof payments !== 'object' || typeof payments.list !== 'function') {
      throw new TypeError('payments 必填（需提供 list(): order[]，如 §49 PaymentIntentService）');
    }
    this.#clock = clock;
    this.#ledger = ledger;
    this.#payments = payments;
  }

  /**
   * 跑一次日切对账（只读）。date 为 'YYYY-MM-DD'，缺省取注入 clock 的当天。
   * @returns 冻结 { date, checked, matched, mismatched, missingInLedger, missingInProvider, ok }
   */
  run({ date = null } = {}) {
    const resolvedDate = this.#resolveDate(date);
    let orders;
    try {
      orders = this.#payments.list();
    } catch {
      throw new ReconcileError('读取支付订单失败，无法对账', CODE.NOT_FOUND);
    }
    if (!Array.isArray(orders)) orders = [];

    const allOrderIds = new Set(orders.filter((order) => order && isNonEmptyString(order.orderId)).map((order) => order.orderId));
    // 日期口径：paidAt 优先，其次 createdAt；缺日期的订单（测试替身）视为属于对账日
    const scopedOrders = orders
      .filter((order) => order && isNonEmptyString(order.orderId) && RECONCILABLE_STATES.has(order.state))
      .filter((order) => {
        const orderDay = dayOf(order.paidAt ?? order.createdAt);
        return orderDay === null || orderDay === resolvedDate;
      })
      .sort((a, b) => (a.orderId < b.orderId ? -1 : a.orderId > b.orderId ? 1 : 0));

    // 账本侧：只看当日的 DEPOSIT，且只认"支付订单入金"（key=deposit:<orderId> 或 ref 命中订单号）
    const depositByOrder = new Map();
    for (const entry of this.#ledger.entries()) {
      if (entry.type !== 'DEPOSIT') continue;
      if (dayOf(entry.at) !== resolvedDate) continue;
      let orderId = null;
      if (isNonEmptyString(entry.idempotencyKey) && entry.idempotencyKey.startsWith(DEPOSIT_KEY_PREFIX)) {
        orderId = entry.idempotencyKey.slice(DEPOSIT_KEY_PREFIX.length);
      } else if (isNonEmptyString(entry.ref) && allOrderIds.has(entry.ref)) {
        orderId = entry.ref;
      }
      if (!orderId) continue; // 非支付入金（如 §37 钱包充值）不参与渠道对账
      depositByOrder.set(orderId, (depositByOrder.get(orderId) ?? 0) + entry.amountCents);
    }

    const mismatched = [];
    const missingInLedger = [];
    let matched = 0;
    for (const order of scopedOrders) {
      if (!depositByOrder.has(order.orderId)) {
        missingInLedger.push(order.orderId);
        continue;
      }
      const ledgerAmountCents = depositByOrder.get(order.orderId);
      if (ledgerAmountCents !== order.amountCents) {
        mismatched.push(
          Object.freeze({
            orderId: order.orderId,
            providerAmountCents: order.amountCents,
            ledgerAmountCents,
            reason: 'AMOUNT_MISMATCH',
          })
        );
      } else {
        matched += 1;
      }
    }
    // 渠道缺失：账本有支付入金、但订单侧根本不存在（按全量订单判断，避免跨日误报）
    const missingInProvider = [...depositByOrder.keys()].filter((orderId) => !allOrderIds.has(orderId)).sort();

    return Object.freeze({
      date: resolvedDate,
      checked: scopedOrders.length,
      matched,
      mismatched: Object.freeze(mismatched),
      missingInLedger: Object.freeze(missingInLedger),
      missingInProvider: Object.freeze(missingInProvider),
      ok: mismatched.length === 0 && missingInLedger.length === 0,
    });
  }

  /** 可归档的 Markdown 对账报告（含逐笔差异，便于工单/审计归档）。 */
  report({ date = null } = {}) {
    const result = this.run({ date });
    const lines = [
      `# 支付日切对账报告 ${result.date}`,
      '',
      `- 生成时间：${this.#nowIso()}`,
      `- 对账结果：${result.ok ? 'PASS' : 'FAIL'}`,
      `- 核对订单数（checked）：${result.checked}`,
      `- 金额匹配（matched）：${result.matched}`,
      `- 金额不一致（mismatched）：${result.mismatched.length}`,
      `- 账本缺失（missingInLedger）：${result.missingInLedger.length}`,
      `- 渠道缺失（missingInProvider）：${result.missingInProvider.length}`,
      '',
      '## 金额不一致',
    ];
    if (result.mismatched.length === 0) {
      lines.push('（无）');
    } else {
      lines.push('| orderId | 渠道金额(分) | 账本金额(分) | 原因 |', '| --- | --- | --- | --- |');
      for (const item of result.mismatched) {
        lines.push(`| ${item.orderId} | ${item.providerAmountCents} | ${item.ledgerAmountCents} | ${item.reason} |`);
      }
    }
    lines.push('', '## 账本缺失（渠道已收、账本未记）');
    if (result.missingInLedger.length === 0) lines.push('（无）');
    else for (const orderId of result.missingInLedger) lines.push(`- ${orderId}`);
    lines.push('', '## 渠道缺失（账本已记、订单不存在）');
    if (result.missingInProvider.length === 0) lines.push('（无）');
    else for (const orderId of result.missingInProvider) lines.push(`- ${orderId}`);
    lines.push('');
    return lines.join('\n');
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #resolveDate(date) {
    if (date === null || date === undefined) return this.#nowIso().slice(0, 10);
    if (typeof date !== 'string' || !DATE_PATTERN.test(date)) {
      throw new ReconcileError(`date 必须是 YYYY-MM-DD 格式，收到 ${String(date)}`, CODE.INVALID_ARGUMENT);
    }
    const parsed = Date.parse(`${date}T00:00:00.000Z`);
    if (!Number.isFinite(parsed) || new Date(parsed).toISOString().slice(0, 10) !== date) {
      throw new ReconcileError(`date 不是合法日期: ${date}`, CODE.INVALID_ARGUMENT);
    }
    return date;
  }

  #nowIso() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled.toISOString();
  }
}
