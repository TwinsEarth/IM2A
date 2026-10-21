// 模块职责：v5 签名信封（Ed25519 消息级签名 + 时间窗 + nonce 重放保护 + TOFU 密钥学习），陌生人付费路径的密码学层 / 契约来源：contracts/INTERFACES.md §26（复用 §11 identity 的 canonicalize/sha256Hex/signPayload/verifyPayload，只调用不改）/ 关键不变量：①签名只证明「这条消息确实由该 Agent 私钥签发」，**不证明它有权发**——授权仍在 §12 AgentGateway 与 §9 付费门控；②verify 只返回判定绝不抛；③判定顺序冻结：结构→签名→时间窗→expectedTo→nonce→发送方公钥；④TOFU 首学成功、不同公钥冲突即 TOFU_CONFLICT。
import { randomUUID } from 'node:crypto';
import { agentIdFromPublicKey, signPayload, verifyPayload } from './agent-id.mjs';

export const ENVELOPE_CODE = Object.freeze({
  ENVELOPE_INVALID: 'ENVELOPE_INVALID',
  SIGNATURE_INVALID: 'SIGNATURE_INVALID',
  REPLAY_DETECTED: 'REPLAY_DETECTED',
  CLOCK_SKEW: 'CLOCK_SKEW',
  KEY_UNKNOWN: 'KEY_UNKNOWN',
  TOFU_CONFLICT: 'TOFU_CONFLICT',
});

const CODE = ENVELOPE_CODE;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);

export class SignedEnvelopeError extends Error {
  constructor(message, code = CODE.ENVELOPE_INVALID) {
    super(message);
    this.name = 'SignedEnvelopeError';
    this.code = code;
  }
}

/** 冻结结果的统一出口：失败也只返回判定，绝不抛异常 */
const verdict = (ok, code, from = null, payload = null) =>
  Object.freeze({ ok, code, from, payload });

export class SignedEnvelopeService {
  #identity;
  #clock;
  #windowMs;
  #nonceTtlMs;
  /** @type {Map<string, string>} agentId -> 已学习（TOFU/白名单）的 Ed25519 公钥 PEM */
  #knownKeys = new Map();
  /** @type {Map<string, string>} agentId -> 首次学习时间 */
  #learnedAt = new Map();
  /** @type {Map<string, number>} nonce -> 首次验签通过的毫秒时间戳（TTL 到期后可复用） */
  #nonces = new Map();

  constructor({ identity, clock = () => new Date(), windowMs = 300_000, nonceTtlMs = 600_000 } = {}) {
    if (
      !identity ||
      !isNonEmptyString(identity.agentId) ||
      !isNonEmptyString(identity.publicKeyPem) ||
      !isNonEmptyString(identity.privateKeyPem)
    ) {
      throw new TypeError('identity 必填（需含 agentId / Ed25519 publicKeyPem / privateKeyPem）');
    }
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(windowMs) || windowMs <= 0) throw new RangeError('windowMs 必须是正整数（毫秒）');
    if (!Number.isInteger(nonceTtlMs) || nonceTtlMs <= 0) throw new RangeError('nonceTtlMs 必须是正整数（毫秒）');
    this.#identity = identity;
    this.#clock = clock;
    this.#windowMs = windowMs;
    this.#nonceTtlMs = nonceTtlMs;
  }

  #now() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled;
  }

  /**
   * 签出消息信封。签名覆盖 canonical(envelope 去掉 signature)。
   * ⚠️ 边界：签名只是「发送方对此内容的不可否认声明」，不是发送权限；
   * 收件方仍须走 §12 网关门控与 §9 付费状态机决定是否受理/收费。
   */
  sign({ toAgentId, kind, payload = {}, meta = {} } = {}) {
    if (!isNonEmptyString(toAgentId)) throw new TypeError('toAgentId 必须是非空字符串');
    if (!isNonEmptyString(kind)) throw new TypeError('kind 必须是非空字符串');
    if (!isPlainObject(payload)) throw new TypeError('payload 必须是普通对象');
    if (!isPlainObject(meta)) throw new TypeError('meta 必须是普通对象');

    const unsigned = {
      v: 1,
      from: this.#identity.agentId,
      to: toAgentId,
      nonce: randomUUID(),
      at: this.#now().toISOString(),
      kind,
      payload: Object.freeze({ ...payload }),
      meta: Object.freeze({ ...meta }),
      alg: 'ed25519',
      publicKeyPem: this.#identity.publicKeyPem,
    };
    const { signature } = signPayload({ identity: this.#identity, payload: unsigned });
    return Object.freeze({ ...unsigned, signature });
  }

  /**
   * 验签（只返回判定，绝不抛异常）。判定顺序冻结（契约 §26）：
   * ① 结构 → ENVELOPE_INVALID
   * ② 用信封自带公钥验签 → SIGNATURE_INVALID（先证完整性，任何字段被改都会在这里失败）
   * ③ 时间窗 |now-at| > windowMs → CLOCK_SKEW
   * ④ expectedTo 不符 → ENVELOPE_INVALID
   * ⑤ nonce 重放（未过期）→ REPLAY_DETECTED
   * ⑥ 发送方公钥：未知 → KEY_UNKNOWN；已知且与信封携带钥不符或验签失败 → SIGNATURE_INVALID
   *
   * ⚠️ 通过 ≠ 有权：`ok:true` 只说明「from 私钥签发过这段内容」，**不说明**发送方有权限发、
   * 有权限收费、或陌生人因此变得可信。授权请继续走 §12 网关 / §9 付费 / §1 信任档 / §13 可见域。
   */
  verify({ envelope, expectedTo = null } = {}) {
    let from = null;
    try {
      if (isPlainObject(envelope) && isNonEmptyString(envelope.from)) from = envelope.from;

      // ① 结构
      if (!isPlainObject(envelope)) return verdict(false, CODE.ENVELOPE_INVALID, from);
      if (
        envelope.v !== 1 ||
        !isNonEmptyString(envelope.from) ||
        !isNonEmptyString(envelope.to) ||
        !isNonEmptyString(envelope.nonce) ||
        !isNonEmptyString(envelope.kind) ||
        !isNonEmptyString(envelope.at) ||
        !isNonEmptyString(envelope.publicKeyPem) ||
        !isNonEmptyString(envelope.signature) ||
        envelope.alg !== 'ed25519' ||
        !isPlainObject(envelope.payload) ||
        !isPlainObject(envelope.meta)
      ) {
        return verdict(false, CODE.ENVELOPE_INVALID, from);
      }
      const atMs = Date.parse(envelope.at);
      if (!Number.isFinite(atMs)) return verdict(false, CODE.ENVELOPE_INVALID, from);
      if (expectedTo !== null && !isNonEmptyString(expectedTo)) {
        return verdict(false, CODE.ENVELOPE_INVALID, from);
      }

      const { signature, ...signedPayload } = envelope;

      // ② 完整性/真实性：先用信封携带的公钥验签（防止任何字段被改后继续走后面的判定）
      if (!verifyPayload({ publicKeyPem: envelope.publicKeyPem, payload: signedPayload, signature }).ok) {
        return verdict(false, CODE.SIGNATURE_INVALID, from);
      }

      // ③ 时间窗（重放防护的时间面）
      const nowMs = this.#now().getTime();
      if (Math.abs(nowMs - atMs) > this.#windowMs) {
        return verdict(false, CODE.CLOCK_SKEW, from);
      }

      // ④ 收件人校验
      if (expectedTo !== null && envelope.to !== expectedTo) {
        return verdict(false, CODE.ENVELOPE_INVALID, from);
      }

      // ⑤ nonce 重放（TTL 到期即可复用）
      this.#pruneNonces(nowMs);
      if (this.#nonces.has(envelope.nonce)) {
        return verdict(false, CODE.REPLAY_DETECTED, from);
      }

      // ⑥ 发送方公钥（TOFU / 白名单）：未知则必须由调用方先 learnKey；已知则必须与信封一致并验签
      const known = this.#knownKeys.get(envelope.from);
      if (known === undefined) return verdict(false, CODE.KEY_UNKNOWN, from);
      if (known !== envelope.publicKeyPem) return verdict(false, CODE.SIGNATURE_INVALID, from);
      if (!verifyPayload({ publicKeyPem: known, payload: signedPayload, signature }).ok) {
        return verdict(false, CODE.SIGNATURE_INVALID, from);
      }

      // 只有完整通过才登记 nonce（失败的判定不会"烧掉"合法 nonce）
      this.#nonces.set(envelope.nonce, nowMs);
      return verdict(true, null, envelope.from, envelope.payload);
    } catch {
      return verdict(false, CODE.ENVELOPE_INVALID, from);
    }
  }

  /**
   * TOFU 学习发送方公钥：首次学习成功；同一 agentId 再次学习相同公钥幂等，
   * 不同公钥 → throw TOFU_CONFLICT（绝不静默覆盖，否则 TOFU 失去意义）。
   * @returns {Readonly<{agentId, publicKeyPem, learnedAt, changed:boolean}>}
   */
  learnKey({ agentId, publicKeyPem } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    if (!isNonEmptyString(publicKeyPem)) throw new TypeError('publicKeyPem 必须是非空字符串');
    try {
      agentIdFromPublicKey(publicKeyPem); // 仅校验 PEM 是合法 Ed25519 公钥（不要求与 agentId 同名）
    } catch {
      throw new SignedEnvelopeError('publicKeyPem 不是合法的 Ed25519 公钥 PEM', CODE.ENVELOPE_INVALID);
    }

    const existing = this.#knownKeys.get(agentId);
    if (existing !== undefined) {
      if (existing !== publicKeyPem) {
        throw new SignedEnvelopeError(
          `TOFU 冲突：agent ${agentId} 已学习过不同公钥（拒绝静默覆盖）`,
          CODE.TOFU_CONFLICT
        );
      }
      return Object.freeze({
        agentId,
        publicKeyPem: existing,
        learnedAt: this.#learnedAt.get(agentId),
        changed: false,
      });
    }
    const learnedAt = this.#now().toISOString();
    this.#knownKeys.set(agentId, publicKeyPem);
    this.#learnedAt.set(agentId, learnedAt);
    return Object.freeze({ agentId, publicKeyPem, learnedAt, changed: true });
  }

  /** @returns {string|null} 已学习公钥；未知返回 null */
  knownKey(agentId) {
    if (!isNonEmptyString(agentId)) return null;
    return this.#knownKeys.get(agentId) ?? null;
  }

  #pruneNonces(nowMs) {
    for (const [nonce, seenAt] of this.#nonces) {
      if (nowMs - seenAt >= this.#nonceTtlMs) this.#nonces.delete(nonce);
    }
  }
}
