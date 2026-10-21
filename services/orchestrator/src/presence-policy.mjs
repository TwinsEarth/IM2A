// 模块职责：旁观与主动沉默（Presence Policy）——Agent 在家庭群/熟人群里可以默默记录、只在被 @ 时说话、或完全不参与。
// 契约来源：contracts/INTERFACES.md §23。
// 关键不变量：①判定顺序冻结：无策略→ALWAYS/POLICY_NOT_FOUND → OFF→SILENCED（不说不记）→ OBSERVE→speak=false/recordSilently=true
//             → MENTION_ONLY→isDirect 或 mentioned 才说话（未提及 MENTION_REQUIRED）→ ALWAYS→说话；
//             ②DIRECT 频道除 OFF 外一律说话（1:1 里"沉默"等于失联）；③判定只返回结论对象，不抛业务异常。
export const PRESENCE_MODE = Object.freeze({
  ALWAYS: 'ALWAYS',
  MENTION_ONLY: 'MENTION_ONLY',
  OBSERVE: 'OBSERVE',
  OFF: 'OFF'
});

export const PRESENCE_CODE = Object.freeze({
  POLICY_MATCHED: 'POLICY_MATCHED',
  POLICY_NOT_FOUND: 'POLICY_NOT_FOUND',
  MODE_INVALID: 'MODE_INVALID',
  SILENCED: 'SILENCED',
  MENTION_REQUIRED: 'MENTION_REQUIRED'
});

const CODE = PRESENCE_CODE;
const MODE_VALUES = new Set(Object.values(PRESENCE_MODE));
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class PresencePolicyError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'PresencePolicyError';
    this.code = code;
  }
}

export class PresencePolicy {
  #clock;
  /** @type {Map<string, object>} `${agentId}\0${channelId}` -> frozen policy */
  #policies;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#policies = new Map();
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  #key(agentId, channelId) {
    return `${agentId}\u0000${channelId}`;
  }

  /** 写入/覆盖某 Agent 在某频道的在场策略；mode 非法 => MODE_INVALID */
  setPolicy({ agentId, channelId, mode, note = null } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('setPolicy: agentId 必填');
    if (!isNonEmptyString(channelId)) throw new TypeError('setPolicy: channelId 必填');
    if (!MODE_VALUES.has(mode)) {
      throw new PresencePolicyError(
        `非法在场模式: ${String(mode)}（允许 ${[...MODE_VALUES].join('/')}）`,
        CODE.MODE_INVALID
      );
    }
    const policy = Object.freeze({
      agentId,
      channelId,
      mode,
      note: note ?? null,
      updatedAt: this.#nowIso()
    });
    this.#policies.set(this.#key(agentId, channelId), policy);
    return policy;
  }

  /** @returns {ReadonlyArray<object>} 按 channelId 升序；未知 Agent 返回空数组 */
  policiesOf(agentId) {
    const out = [];
    for (const policy of this.#policies.values()) {
      if (policy.agentId === agentId) out.push(policy);
    }
    out.sort((a, b) => (a.channelId < b.channelId ? -1 : a.channelId > b.channelId ? 1 : 0));
    return Object.freeze(out);
  }

  /**
   * 判定是否说话 / 是否静默记录。
   * @returns {Readonly<{speak:boolean, mode:string, code:string, recordSilently:boolean, reasons:ReadonlyArray<string>}>}
   */
  decide({ agentId, channelId, mentioned = false, isDirect = false } = {}) {
    const policy = this.#policies.get(this.#key(agentId, channelId)) ?? null;
    const result = (speak, mode, code, recordSilently, reasons) =>
      Object.freeze({
        speak,
        mode,
        code,
        recordSilently,
        reasons: Object.freeze(reasons)
      });

    // 无策略 => 默认活跃（POLICY_NOT_FOUND 是"没有配置"而不是错误）
    if (!policy) {
      return result(true, PRESENCE_MODE.ALWAYS, CODE.POLICY_NOT_FOUND, false, [
        'no policy configured: default ALWAYS'
      ]);
    }

    const mode = policy.mode;

    // DIRECT 例外（契约 §23 冻结）：1:1 频道除 OFF 外一律说话，防止 Agent 在私聊里"失联"
    if (isDirect === true && mode !== PRESENCE_MODE.OFF) {
      return result(true, mode, CODE.POLICY_MATCHED, false, [
        `direct channel: mode=${mode} 仍必须回应（1:1 沉默等于失联）`
      ]);
    }

    switch (mode) {
      case PRESENCE_MODE.OFF:
        return result(false, mode, CODE.SILENCED, false, ['mode=OFF: 不说话也不记录']);
      case PRESENCE_MODE.OBSERVE:
        return result(false, mode, CODE.SILENCED, true, ['mode=OBSERVE: 记录但不打扰']);
      case PRESENCE_MODE.MENTION_ONLY:
        if (mentioned === true) {
          return result(true, mode, CODE.POLICY_MATCHED, false, ['mode=MENTION_ONLY: 已被 @，可以说话']);
        }
        return result(false, mode, CODE.MENTION_REQUIRED, false, [
          'mode=MENTION_ONLY: 未被 @，保持沉默（非 DIRECT 频道）'
        ]);
      case PRESENCE_MODE.ALWAYS:
      default:
        return result(true, mode, CODE.POLICY_MATCHED, false, ['mode=ALWAYS: 正常参与']);
    }
  }
}
