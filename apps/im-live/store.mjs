/**
 * 模块职责：im-live 的持久化层 —— 用 Node 24 内置 node:sqlite 把「会话 / 消息 / 审计链」落到单文件数据库
 * 契约来源：docs/05-数据模型与存储.md（生产为 PostgreSQL；这里是零依赖的最小可运行落库）
 *
 * 边界（诚实）：
 *   · 持久化范围 = 会话元数据 + 消息（含 engine 元数据）+ 审计哈希链 + 消息序号；
 *   · 治理状态（审批单 / 账本 / 信任矩阵）仍由 flow.mjs 在内存重建（重启回种子），不在本轮落库；
 *   · DB 路径来自 IM_DB_PATH，默认 <repo>/.data/agentim.db；`:memory:` 用于测试；
 *   · 只允许 CREATE/INSERT/SELECT/UPDATE(seq)，不做删除接口——审计与消息是只追加语义。
 */
import { DatabaseSync } from 'node:sqlite';
import { existsSync, mkdirSync } from 'node:fs';
import path from 'node:path';

const SCHEMA = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS threads (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  members_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  at TEXT NOT NULL,
  sender TEXT NOT NULL,
  text TEXT NOT NULL,
  kind TEXT NOT NULL,
  engine_json TEXT,
  ticket_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id, at);
CREATE TABLE IF NOT EXISTS audit (
  seq INTEGER PRIMARY KEY,
  at TEXT NOT NULL,
  source TEXT NOT NULL,
  action TEXT NOT NULL,
  actor TEXT,
  trace_id TEXT,
  target TEXT,
  decision TEXT,
  detail_json TEXT,
  prev_hash TEXT NOT NULL,
  hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_trace ON audit(trace_id);
`;

export function defaultDbPath() {
  return process.env.IM_DB_PATH || path.join(process.cwd(), '.data', 'agentim.db');
}

export class Persistence {
  #dbPath;
  #db;
  #closed = false;

  constructor({ dbPath = defaultDbPath() } = {}) {
    this.#dbPath = dbPath;
    if (dbPath !== ':memory:') {
      const dir = path.dirname(dbPath);
      if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
    }
    this.#db = new DatabaseSync(dbPath);
    this.#db.exec('PRAGMA journal_mode = WAL;');
    this.#db.exec(SCHEMA);
    this.#readyStatements();
  }

  #stmts = null;
  #readyStatements() {
    const db = this.#db;
    this.#stmts = {
      seedCount: db.prepare('SELECT COUNT(*) AS n FROM threads'),
      insertThread: db.prepare('INSERT OR REPLACE INTO threads(id, kind, title, members_json) VALUES (?, ?, ?, ?)'),
      listThreads: db.prepare('SELECT id, kind, title, members_json FROM threads ORDER BY rowid'),
      insertMessage: db.prepare('INSERT OR IGNORE INTO messages(id, thread_id, at, sender, text, kind, engine_json, ticket_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)'),
      listMessages: db.prepare('SELECT id, at, sender, text, kind, engine_json, ticket_id FROM messages WHERE thread_id = ? ORDER BY rowid'),
      insertAudit: db.prepare('INSERT OR IGNORE INTO audit(seq, at, source, action, actor, trace_id, target, decision, detail_json, prev_hash, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)'),
      listAudit: db.prepare('SELECT seq, at, source, action, actor, trace_id, target, decision, detail_json, prev_hash, hash FROM audit ORDER BY seq'),
      getMeta: db.prepare('SELECT value FROM meta WHERE key = ?'),
      setMeta: db.prepare('INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value')
    };
  }

  get dbPath() {
    return this.#dbPath;
  }

  /** 是否已有种子（用于「首次启动播种，之后加载」判定）。 */
  hasSeed() {
    return Number(this.#stmts.seedCount.get()?.n ?? 0) > 0;
  }

  saveThread(thread) {
    this.#stmts.insertThread.run(thread.id, thread.kind, thread.title, JSON.stringify(thread.members));
  }

  appendMessage(threadId, message) {
    this.#stmts.insertMessage.run(
      message.id,
      threadId,
      message.at,
      message.from,
      message.text,
      message.kind,
      message.engine ? JSON.stringify(message.engine) : null,
      message.ticketId ?? null
    );
  }

  /** 按线程读消息；engine/ticket 字段还原为对象。 */
  loadMessages(threadId) {
    return this.#stmts.listMessages.all(threadId).map((row) => {
      const message = {
        id: row.id,
        at: row.at,
        from: row.sender,
        text: row.text,
        kind: row.kind
      };
      if (row.engine_json) message.engine = JSON.parse(row.engine_json);
      if (row.ticket_id) message.ticketId = row.ticket_id;
      return Object.freeze(message);
    });
  }

  loadThreads() {
    return this.#stmts.listThreads.all().map((row) => ({
      id: row.id,
      kind: row.kind,
      title: row.title,
      members: JSON.parse(row.members_json),
      messages: []
    }));
  }

  appendAudit(record) {
    this.#stmts.insertAudit.run(
      record.seq,
      record.at,
      record.source,
      record.action,
      record.actor ?? null,
      record.traceId ?? null,
      record.target ?? null,
      record.decision ?? null,
      record.detail === null || record.detail === undefined ? null : JSON.stringify(record.detail),
      record.prevHash,
      record.hash
    );
  }

  /** 审计链重放（恢复 AuditLog 用）；detail 还原为对象。 */
  loadAudit() {
    return this.#stmts.listAudit.all().map((row) => Object.freeze({
      seq: row.seq,
      at: row.at,
      source: row.source,
      action: row.action,
      actor: row.actor,
      traceId: row.trace_id,
      target: row.target,
      decision: row.decision,
      detail: row.detail_json ? JSON.parse(row.detail_json) : null,
      prevHash: row.prev_hash,
      hash: row.hash
    }));
  }

  /** 重置演示数据（保留 DB 文件与表结构）；用于「重置」按钮与测试。 */
  reset() {
    this.#db.exec('DELETE FROM messages; DELETE FROM audit; DELETE FROM threads; DELETE FROM meta;');
  }

  saveSeq(value) {
    this.#stmts.setMeta.run('seq', String(value));
  }

  loadSeq() {
    const row = this.#stmts.getMeta.get('seq');
    return row ? Number(row.value) : 0;
  }

  stats() {
    return Object.freeze({
      dbPath: this.#dbPath,
      threads: Number(this.#stmts.seedCount.get()?.n ?? 0),
      messages: Number(this.#db.prepare('SELECT COUNT(*) AS n FROM messages').get()?.n ?? 0),
      audit: Number(this.#db.prepare('SELECT COUNT(*) AS n FROM audit').get()?.n ?? 0)
    });
  }

  close() {
    if (this.#closed) return;
    this.#closed = true;
    this.#db.close();
  }
}

/** 创建持久化层；`:memory:` 或路径均可。 */
export function createPersistence(options = {}) {
  return new Persistence(options);
}
