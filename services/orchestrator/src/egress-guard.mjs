// 模块职责：出口合规护栏。所有 Agent 外发内容（含发往其它 Agent 的协商内容）统一过审。
// 契约来源：contracts/INTERFACES.md §7。
// 关键不变量：①BLOCK 一定拦截（assertPass 抛 EgressBlockedError）；②结果可与 messageId 绑定写入审计；
//             ③先做 Unicode NFKC + 去零宽字符归一化，再匹配，防止全角/零宽绕过。
import { createHash } from 'node:crypto';

export const VERDICT = Object.freeze({ PASS: 'PASS', REVIEW: 'REVIEW', BLOCK: 'BLOCK' });

const VERDICT_RANK = Object.freeze({ PASS: 0, REVIEW: 1, BLOCK: 2 });
const DEFAULT_MAX_LENGTH = 4000;

/** 默认规则：身份证号 / 银行卡号 / 转账话术 / 辱骂词表 => BLOCK；外部链接 => REVIEW；超长 => BLOCK */
export const DEFAULT_RULES = Object.freeze([
  Object.freeze({
    id: 'id-card-cn',
    verdict: VERDICT.BLOCK,
    pattern: /(?<!\d)\d{17}[\dXx](?!\d)/u,
    note: '18 位中国大陆身份证号'
  }),
  Object.freeze({
    id: 'bank-card',
    verdict: VERDICT.BLOCK,
    pattern: /(?<!\d)\d{16,19}(?!\d)/u,
    note: '16–19 位银行卡号'
  }),
  Object.freeze({
    id: 'transfer-talk',
    verdict: VERDICT.BLOCK,
    pattern: /(转账|汇款|打款|收款码|付款码|扫码支付|验证码|退款到|垫付|先付款)/u,
    note: '转账/汇款/索要资金话术'
  }),
  Object.freeze({
    id: 'abuse',
    verdict: VERDICT.BLOCK,
    pattern: /(傻逼|傻B|去死|滚蛋|废物|白痴|蠢货|妈的|操你|弄死你|狗东西)/iu,
    note: '辱骂/威胁词表'
  }),
  Object.freeze({
    id: 'external-link',
    verdict: VERDICT.REVIEW,
    pattern: /(https?:\/\/|www\.|t\.me\/|\.com\/|\.cn\/|\.net\/|\.io\/)/iu,
    note: '外部链接'
  }),
  Object.freeze({
    id: 'max-length',
    verdict: VERDICT.BLOCK,
    pattern: /[\s\S]{4001,}/u,
    note: '超长内容（默认上限 4000 字符，可由 maxLength 覆盖）'
  })
]);

export class EgressBlockedError extends Error {
  constructor(message, { verdict = VERDICT.BLOCK, matched = [], contentHash = null, messageId = null, at = null } = {}) {
    super(message);
    this.name = 'EgressBlockedError';
    this.code = 'EGRESS_BLOCKED';
    this.verdict = verdict;
    this.matched = matched;
    this.contentHash = contentHash;
    this.messageId = messageId;
    this.at = at;
  }
}

/** 归一化：NFKC（全角转半角）+ 移除零宽/软连字符等不可见字符 */
const normalizeContent = (content) =>
  String(content ?? '')
    .normalize('NFKC')
    .replace(/[\u200B-\u200D\u2060\uFEFF\u00AD\u180E]/gu, '');

/** 数字压缩视图：把数字之间的空格/分隔符去掉，防止 “6222 0212 …” 绕过 */
const compactDigits = (text) => text.replace(/(?<=\d)[\s\u00A0_.\-·]{1,3}(?=\d)/gu, '');

const truncate = (text, max = 120) => (text.length <= max ? text : `${text.slice(0, max)}…`);

export class EgressGuard {
  #rules;
  #clock;
  #maxLength;
  #onAudit;

  constructor({ rules = DEFAULT_RULES, clock = () => new Date(), maxLength = DEFAULT_MAX_LENGTH, onAudit = () => {} } = {}) {
    if (!Array.isArray(rules)) throw new TypeError('EgressGuard: rules 必须是数组');
    if (!Number.isInteger(maxLength) || maxLength <= 0) {
      throw new TypeError('EgressGuard: maxLength 必须是正整数');
    }
    this.#rules = Object.freeze([...rules]);
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#maxLength = maxLength;
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
  }

  #nowIso() {
    const sampled = this.#clock();
    const now = sampled instanceof Date ? sampled : new Date(sampled);
    return now.toISOString();
  }

  /**
   * @returns {Readonly<{verdict:string, matched:ReadonlyArray<{id:string,verdict:string,match:string}>, contentHash:string, at:string, audit:object}>}
   */
  inspect({ content, fromAgentId, toKind, messageId = null, trustZone = null } = {}) {
    const normalized = normalizeContent(content);
    const compact = compactDigits(normalized);
    const matched = [];
    const seen = new Set();

    // 超长内容：长度上限由构造参数决定（默认规则中另有 4001 字符的 pattern 兜底默认值）
    if (normalized.length > this.#maxLength) {
      seen.add('max-length');
      matched.push(
        Object.freeze({
          id: 'max-length',
          verdict: VERDICT.BLOCK,
          match: `length=${normalized.length} > maxLength=${this.#maxLength}`
        })
      );
    }

    for (const rule of this.#rules) {
      if (!rule || !(rule.pattern instanceof RegExp)) continue;
      if (seen.has(rule.id)) continue;
      let hit = null;
      for (const candidate of [normalized, compact]) {
        rule.pattern.lastIndex = 0;
        const result = rule.pattern.exec(candidate);
        if (result) {
          hit = result[0];
          break;
        }
      }
      if (!hit) continue;
      seen.add(rule.id);
      matched.push(
        Object.freeze({
          id: rule.id,
          verdict: rule.verdict === VERDICT.REVIEW ? VERDICT.REVIEW : VERDICT.BLOCK,
          match: truncate(hit)
        })
      );
    }

    const verdict = matched.reduce(
      (worst, item) => (VERDICT_RANK[item.verdict] > VERDICT_RANK[worst] ? item.verdict : worst),
      VERDICT.PASS
    );
    const contentHash = createHash('sha256').update(normalized, 'utf8').digest('hex');
    const at = this.#nowIso();
    const audit = Object.freeze({
      at,
      actor: fromAgentId ?? null,
      action: 'EGRESS_INSPECT',
      target: messageId ?? null,
      decision: verdict,
      detail: Object.freeze({
        toKind: toKind ?? null,
        trustZone: trustZone ?? null,
        matched: Object.freeze(matched.map((item) => item.id)),
        contentHash,
        messageId: messageId ?? null
      })
    });
    this.#onAudit(audit);

    return Object.freeze({
      verdict,
      matched: Object.freeze(matched),
      contentHash,
      at,
      audit
    });
  }

  /** BLOCK 时抛 EgressBlockedError，其余返回 inspect 结果 */
  assertPass(request = {}) {
    const result = this.inspect(request);
    if (result.verdict === VERDICT.BLOCK) {
      throw new EgressBlockedError(`外发内容被出口护栏拦截: ${result.matched.map((item) => item.id).join(', ')}`, {
        verdict: result.verdict,
        matched: result.matched,
        contentHash: result.contentHash,
        messageId: request?.messageId ?? null,
        at: result.at
      });
    }
    return result;
  }
}
