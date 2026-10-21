// 模块职责：签名审计 —— 在既有哈希链 AuditLog 之上加 Ed25519 签名，防"有权改库者整链重算"，并证明"是谁写的"。
// 契约来源：contracts/INTERFACES.md §16（AuditLog 为已实现底层链，本模块只包装、不修改它）。
// 关键不变量：①每条 entry 的 signature = ed25519(identity.privateKey, entry.hash)；②verify() 必须同时校验链与每条签名，链断时绝不因"签名通过"而返回 ok；
//             ③identity 缺失 => SIGNER_REQUIRED（签名审计不允许无签名模式）；④checkpoint 可被独立持公钥者验签。
import { sign, verify } from 'node:crypto';

import { AuditLog } from './audit-log.mjs';

export const SIGNED_AUDIT_CODE = Object.freeze({
  SIGNER_REQUIRED: 'SIGNER_REQUIRED',
  CHAIN_BROKEN: 'CHAIN_BROKEN',
  SIGNATURE_INVALID: 'SIGNATURE_INVALID',
  UNKNOWN_SEQ: 'UNKNOWN_SEQ'
});

const CODE = SIGNED_AUDIT_CODE;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class SignedAuditError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'SignedAuditError';
    this.code = code;
  }
}

function signHash(privateKeyPem, hash) {
  return sign(null, Buffer.from(hash, 'utf8'), privateKeyPem).toString('base64');
}

function verifyHash(publicKeyPem, hash, signature) {
  try {
    return verify(null, Buffer.from(hash, 'utf8'), publicKeyPem, Buffer.from(signature, 'base64'));
  } catch {
    return false;
  }
}

export class SignedAuditLog {
  #clock;
  #identity;
  #log;
  /** @type {Map<number, {signer:string, signature:string}>} 本进程内签名（seq -> 签名材料） */
  #signatures;

  constructor({ clock = () => new Date(), identity, log = null } = {}) {
    if (!identity || !isNonEmptyString(identity.privateKeyPem) || !isNonEmptyString(identity.publicKeyPem)) {
      throw new SignedAuditError('签名审计必须注入 Ed25519 identity（privateKeyPem/publicKeyPem）', CODE.SIGNER_REQUIRED);
    }
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#identity = identity;
    this.#signatures = new Map();
    if (log !== null && log !== undefined) {
      if (
        typeof log.append !== 'function' ||
        typeof log.verify !== 'function' ||
        typeof log.records !== 'function' ||
        typeof log.head !== 'function'
      ) {
        throw new TypeError('SignedAuditLog: log 必须是 AuditLog 兼容实例');
      }
      this.#log = log;
    } else {
      this.#log = new AuditLog({ clock: this.#clock });
    }
  }

  #nowIso() {
    const sampled = this.#clock();
    const date = sampled instanceof Date ? sampled : new Date(sampled);
    return date.toISOString();
  }

  #signatureOf(record) {
    if (isNonEmptyString(record.signature)) {
      return { signer: record.signer ?? null, signature: record.signature };
    }
    const cached = this.#signatures.get(record.seq);
    return cached ?? null;
  }

  #merge(record) {
    const signed = this.#signatureOf(record);
    return Object.freeze({
      ...record,
      signer: signed?.signer ?? null,
      signature: signed?.signature ?? null
    });
  }

  /** 追加一条审计并签名；返回冻结 entry（含 signer/signature） */
  append({ source, action, actor = null, target = null, decision = null, detail = null, traceId = null } = {}) {
    const record = this.#log.append({
      source,
      action,
      actor,
      target,
      decision,
      detail,
      traceId,
      at: this.#nowIso()
    });
    let signature;
    try {
      signature = signHash(this.#identity.privateKeyPem, record.hash);
    } catch {
      throw new SignedAuditError('Ed25519 签名失败（identity.privateKeyPem 非法）', CODE.SIGNER_REQUIRED);
    }
    this.#signatures.set(record.seq, { signer: this.#identity.agentId, signature });
    return Object.freeze({ ...record, signer: this.#identity.agentId, signature });
  }

  /**
   * 全量校验：链（防单条篡改/截断）+ 每条 Ed25519 签名（防整链重算）。
   * @returns {Readonly<{ok:boolean, length:number, chain:{ok:boolean,brokenAt:number|null}, signatures:{ok:boolean,invalidAt:number|null}, head:string}>}
   */
  verify() {
    const chainResult = this.#log.verify();
    const records = this.#log.snapshot();
    let invalidAt = null;
    for (const record of records) {
      const signed = this.#signatureOf(record);
      const signatureOk = Boolean(signed) && verifyHash(this.#identity.publicKeyPem, record.hash, signed.signature);
      if (!signatureOk) {
        invalidAt = invalidAt ?? record.seq;
      }
    }
    const signaturesOk = invalidAt === null;
    return Object.freeze({
      ok: chainResult.ok && signaturesOk,
      length: records.length,
      chain: Object.freeze({ ok: chainResult.ok, brokenAt: chainResult.brokenAt ?? null }),
      signatures: Object.freeze({ ok: signaturesOk, invalidAt }),
      head: this.#log.head()
    });
  }

  /** 独立可验签的检查点：签名对象就是 checkpoint.hash（与 entry 签名同一约定） */
  checkpoint() {
    const head = this.#log.head();
    const records = this.#log.records();
    const seq = records.length > 0 ? records.at(-1).seq : 0;
    let signature;
    try {
      signature = signHash(this.#identity.privateKeyPem, head);
    } catch {
      throw new SignedAuditError('Ed25519 签名失败（identity.privateKeyPem 非法）', CODE.SIGNER_REQUIRED);
    }
    return Object.freeze({
      seq,
      hash: head,
      alg: 'ed25519',
      signature,
      at: this.#nowIso()
    });
  }

  /** @returns {ReadonlyArray<object>} 最近 N 条（仍按时间正序），含 signer/signature */
  entries({ limit = null } = {}) {
    const records = this.#log.records({ limit });
    return Object.freeze(records.map((record) => this.#merge(record)));
  }

  head() {
    return this.#log.head();
  }

  /** 校验失败即抛错（供"未通过审计不得对外宣称成功"的写入点使用） */
  assertVerified() {
    const result = this.verify();
    if (!result.chain.ok) {
      throw new SignedAuditError(`审计链断裂，brokenAt=${String(result.chain.brokenAt)}`, CODE.CHAIN_BROKEN);
    }
    if (!result.signatures.ok) {
      throw new SignedAuditError(`审计签名非法，invalidAt=${String(result.signatures.invalidAt)}`, CODE.SIGNATURE_INVALID);
    }
    return result;
  }

  /** 按 seq 取单条合并后的 entry；不存在 => UNKNOWN_SEQ */
  entry(seq) {
    const record = this.#log.snapshot().find((item) => item.seq === seq);
    if (!record) throw new SignedAuditError(`未知审计序号: ${String(seq)}`, CODE.UNKNOWN_SEQ);
    return this.#merge(record);
  }
}
