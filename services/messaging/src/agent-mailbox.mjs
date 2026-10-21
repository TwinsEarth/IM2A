// 模块职责：Agent 持久邮箱（append-only JSONL 落盘；会话历史属于 Agent 身份而非某次运行会话，进程崩溃/换机器可恢复）——修补 v4 TurnInbox 内存态重启丢 pending 的已知缺陷 / 契约来源：contracts/INTERFACES.md §29 / 关键不变量：①每 agent 一个 `<dir>/<agentId>.jsonl`，新实例读同目录必须恢复同样的 pending；②同 message.id 重复投递不重复入队（duplicate:true）；③cursor 单调，乱序/回退 → CURSOR_INVALID；④容量超限 → CAPACITY_EXCEEDED（不静默丢）；⑤读取逐行 parse 并跳过坏行，坏行不让实例崩溃。
import {
  appendFileSync,
  closeSync,
  mkdirSync,
  openSync,
  readFileSync,
  readSync,
  statSync,
} from 'node:fs';
import { join, resolve } from 'node:path';

export const MAILBOX_CODE = Object.freeze({
  MAILBOX_UNKNOWN: 'MAILBOX_UNKNOWN',
  MESSAGE_INVALID: 'MESSAGE_INVALID',
  DUPLICATE_MESSAGE: 'DUPLICATE_MESSAGE',
  CAPACITY_EXCEEDED: 'CAPACITY_EXCEEDED',
  CURSOR_INVALID: 'CURSOR_INVALID',
});

const CODE = MAILBOX_CODE;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);
/** 文件名安全：只允许字母数字与 . _ -（agentId 由平台签发，正常形如 agent-xxx） */
const SAFE_AGENT_ID = /^[A-Za-z0-9._-]+$/;

export class AgentMailboxError extends Error {
  constructor(message, code = CODE.MAILBOX_UNKNOWN) {
    super(message);
    this.name = 'AgentMailboxError';
    this.code = code;
  }
}

const freezeMessage = (message) => Object.freeze({ ...message });

export class AgentMailboxStore {
  #dir;
  #clock;
  #capacityPerAgent;

  constructor({ dir, clock = () => new Date(), capacityPerAgent = 1000 } = {}) {
    if (!isNonEmptyString(dir)) throw new TypeError('dir 必须是非空字符串（邮箱存储目录）');
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(capacityPerAgent) || capacityPerAgent <= 0) {
      throw new RangeError('capacityPerAgent 必须是正整数');
    }
    this.#dir = resolve(dir);
    this.#clock = clock;
    this.#capacityPerAgent = capacityPerAgent;
    // 目录只建一次；每个 agent 一个 append-only 文件，首次 deliver 时懒创建
    mkdirSync(this.#dir, { recursive: true });
  }

  /**
   * 投递消息到 agent 邮箱（持久化 append）。
   * 同一 message.id 重复投递 → { stored:false, duplicate:true, seq:首次入队序号 }（不重复入队）。
   * 容量上限按**未 ack 的 pending 条数**计（接收方长期离线时给发送方背压，但不静默丢弃）。
   * @returns {Readonly<{stored:boolean, duplicate:boolean, seq:number}>}
   */
  deliver({ toAgentId, message, rejectDuplicate = false } = {}) {
    this.#assertAgentId(toAgentId);
    if (typeof rejectDuplicate !== 'boolean') throw new TypeError('rejectDuplicate 必须是布尔值');
    const state = this.#load(toAgentId);
    const messages = state === null ? [] : state.messages;
    const existing = messages.find((entry) => entry.message.id === message?.id);
    if (existing) {
      if (rejectDuplicate) {
        // 可选严格模式（加法参数）：调用方要求"重复即报错"时用，默认仍按契约返回 duplicate:true
        throw new AgentMailboxError(
          `消息重复投递: ${existing.message.id}（seq=${existing.seq}）`,
          CODE.DUPLICATE_MESSAGE
        );
      }
      return Object.freeze({ stored: false, duplicate: true, seq: existing.seq });
    }
    this.#validateMessage(message);

    const pending = state === null ? 0 : messages.filter((entry) => entry.seq > state.ackedCursor).length;
    if (pending >= this.#capacityPerAgent) {
      throw new AgentMailboxError(
        `邮箱已达容量上限 capacityPerAgent=${this.#capacityPerAgent}（pending=${pending}），拒绝静默丢弃`,
        CODE.CAPACITY_EXCEEDED
      );
    }

    const maxSeq = messages.length === 0 ? 0 : messages[messages.length - 1].seq;
    const seq = maxSeq + 1;
    this.#appendLine(this.#fileOf(toAgentId), {
      type: 'deliver',
      seq,
      message,
      at: this.#nowIso(),
    });
    return Object.freeze({ stored: true, duplicate: false, seq });
  }

  /**
   * 拉取消息（不消费；消费用 ack）。cursor 单调：默认从已 ack 位置开始；
   * 回退（< 已 ack 位置）/ 乱序（> 当前最大 seq）/ 负数 / 非整数 → CURSOR_INVALID。
   * @returns {Readonly<{messages:object[], nextCursor:number, pending:number}>}
   */
  fetch({ agentId, cursor = null, limit = 50 } = {}) {
    this.#assertAgentId(agentId);
    if (!Number.isInteger(limit) || limit <= 0) throw new TypeError('limit 必须是正整数');
    const state = this.#load(agentId);
    if (state === null) throw new AgentMailboxError(`邮箱不存在: ${agentId}`, CODE.MAILBOX_UNKNOWN);

    const maxSeq = state.messages.length === 0 ? 0 : state.messages[state.messages.length - 1].seq;
    let startCursor;
    if (cursor === null) {
      startCursor = state.ackedCursor;
    } else {
      this.#assertCursor({ cursor, maxSeq, floor: state.ackedCursor });
      startCursor = cursor;
    }

    const page = state.messages.filter((entry) => entry.seq > startCursor).slice(0, limit);
    const nextCursor = page.length > 0 ? page[page.length - 1].seq : startCursor;
    const pending = state.messages.filter((entry) => entry.seq > nextCursor).length;
    return Object.freeze({
      messages: Object.freeze(page.map((entry) => freezeMessage(entry.message))),
      nextCursor,
      pending,
    });
  }

  /**
   * 提交消费位移（append 一条 ack 事件，不重写历史）。cursor 只能前进（等于当前值幂等）。
   * @returns {Readonly<{acked:number}>}
   */
  ack({ agentId, cursor } = {}) {
    this.#assertAgentId(agentId);
    const state = this.#load(agentId);
    if (state === null) throw new AgentMailboxError(`邮箱不存在: ${agentId}`, CODE.MAILBOX_UNKNOWN);
    const maxSeq = state.messages.length === 0 ? 0 : state.messages[state.messages.length - 1].seq;
    this.#assertCursor({ cursor, maxSeq, floor: state.ackedCursor });
    if (cursor === state.ackedCursor) return Object.freeze({ acked: cursor });
    this.#appendLine(this.#fileOf(agentId), { type: 'ack', cursor, at: this.#nowIso() });
    return Object.freeze({ acked: cursor });
  }

  /** 全量只读历史（审计用；含已 ack 与 pending），按投递顺序。 */
  history(agentId) {
    this.#assertAgentId(agentId);
    const state = this.#load(agentId);
    if (state === null) throw new AgentMailboxError(`邮箱不存在: ${agentId}`, CODE.MAILBOX_UNKNOWN);
    return Object.freeze(state.messages.map((entry) => freezeMessage(entry.message)));
  }

  /** 邮箱统计。sizeBytes = JSONL 文件真实字节数（含 ack 事件）。 */
  stats(agentId) {
    this.#assertAgentId(agentId);
    const state = this.#load(agentId);
    if (state === null) throw new AgentMailboxError(`邮箱不存在: ${agentId}`, CODE.MAILBOX_UNKNOWN);
    const delivered = state.messages.length;
    const acked = state.messages.filter((entry) => entry.seq <= state.ackedCursor).length;
    const { size } = statSync(this.#fileOf(agentId));
    return Object.freeze({ delivered, acked, pending: delivered - acked, sizeBytes: size });
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #assertAgentId(agentId) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    if (!SAFE_AGENT_ID.test(agentId) || agentId === '.' || agentId === '..') {
      throw new TypeError(`agentId 含文件名非法字符（仅允许字母/数字/. _ -）: ${agentId}`);
    }
  }

  #validateMessage(message) {
    if (!isPlainObject(message) || !isNonEmptyString(message.id)) {
      throw new AgentMailboxError('message 必须是含非空 id 的普通对象', CODE.MESSAGE_INVALID);
    }
    try {
      JSON.stringify(message); // 必须可持久化（循环引用/BigInt 等 → MESSAGE_INVALID）
    } catch {
      throw new AgentMailboxError('message 必须可 JSON 序列化（含循环引用/BigInt 非法）', CODE.MESSAGE_INVALID);
    }
  }

  #assertCursor({ cursor, maxSeq, floor }) {
    if (!Number.isInteger(cursor) || cursor < 0) {
      throw new AgentMailboxError(`cursor 必须是非负整数，收到 ${String(cursor)}`, CODE.CURSOR_INVALID);
    }
    if (cursor < floor) {
      throw new AgentMailboxError(`cursor 回退：${cursor} < 已确认位移 ${floor}`, CODE.CURSOR_INVALID);
    }
    if (cursor > maxSeq) {
      throw new AgentMailboxError(`cursor 乱序：${cursor} > 当前最大 seq ${maxSeq}`, CODE.CURSOR_INVALID);
    }
  }

  #fileOf(agentId) {
    return join(this.#dir, `${agentId}.jsonl`);
  }

  /**
   * 从磁盘重建邮箱状态（每次操作都读盘，保证"同目录新实例恢复同样的 pending"，不依赖进程内缓存）。
   * 逐行 JSON.parse，坏行/半行/未知事件一律跳过；文件内的重复 message.id 只保留首条。
   */
  #load(agentId) {
    let text;
    try {
      text = readFileSync(this.#fileOf(agentId), 'utf8');
    } catch (error) {
      if (error && error.code === 'ENOENT') return null;
      throw error;
    }
    const messages = [];
    const seenIds = new Set();
    let ackedCursor = 0;
    for (const line of text.split(/\r?\n/)) {
      if (line.trim() === '') continue;
      let record;
      try {
        record = JSON.parse(line);
      } catch {
        continue; // 坏行：跳过但不崩（例如进程崩溃留下的半行 / 手工追加的垃圾）
      }
      if (!isPlainObject(record)) continue;
      if (record.type === 'deliver') {
        if (!Number.isInteger(record.seq) || record.seq <= 0) continue;
        if (!isPlainObject(record.message) || !isNonEmptyString(record.message.id)) continue;
        if (seenIds.has(record.message.id)) continue;
        seenIds.add(record.message.id);
        messages.push({ seq: record.seq, message: record.message, at: record.at ?? null });
      } else if (record.type === 'ack') {
        if (!Number.isInteger(record.cursor) || record.cursor < 0) continue;
        if (record.cursor > ackedCursor) ackedCursor = record.cursor;
      }
    }
    return { messages, ackedCursor };
  }

  /**
   * append 一行 JSONL。若文件末尾不是换行（崩溃留下的半行），先补一个换行再写，
   * 避免新记录被粘到半行上变成"一起坏掉"。
   */
  #appendLine(file, record) {
    const line = `${JSON.stringify(record)}\n`;
    let prefix = '';
    try {
      const { size } = statSync(file);
      if (size > 0) {
        const fd = openSync(file, 'r');
        try {
          const tail = Buffer.alloc(1);
          readSync(fd, tail, 0, 1, size - 1);
          if (tail.toString('utf8') !== '\n') prefix = '\n';
        } finally {
          closeSync(fd);
        }
      }
    } catch (error) {
      if (!error || error.code !== 'ENOENT') throw error;
    }
    appendFileSync(file, `${prefix}${line}`, 'utf8');
  }

  #nowIso() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled.toISOString();
  }
}
