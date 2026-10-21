/**
 * 模块职责：im-live 持久化层测试 —— 会话/消息/审计链落 SQLite 后可重放、可校验、重置可复现
 * 契约来源：apps/im2a/store.mjs 头注释（生产为 PostgreSQL，这里是零依赖最小落库）
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { Persistence, createPersistence } from '../apps/im2a/store.mjs';
import { AuditLog } from '../services/audit/src/audit-log.mjs';

function tempDb() {
  const dir = mkdtempSync(join(tmpdir(), 'agentim-store-'));
  return { dbPath: join(dir, 'test.db'), cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

test('首次打开：空库、无种子；写入后可读回，`:memory:` 同样可用', () => {
  const memory = createPersistence({ dbPath: ':memory:' });
  try {
    assert.equal(memory.hasSeed(), false);
    memory.saveThread({ id: 'th-a', kind: 'family', title: '家庭', members: ['u1', 'u2'] });
    assert.equal(memory.hasSeed(), true);
    memory.appendMessage('th-a', { id: 'm-1', at: '2026-10-21T00:00:00.000Z', from: 'u1', text: '你好', kind: 'text' });
    const threads = memory.loadThreads();
    assert.equal(threads.length, 1);
    assert.deepEqual(threads[0].members, ['u1', 'u2']);
    const messages = memory.loadMessages('th-a');
    assert.equal(messages.length, 1);
    assert.equal(messages[0].text, '你好');
    assert.equal(memory.stats().messages, 1);
  } finally {
    memory.close();
  }
});

test('文件库：关闭再打开，会话/消息/序号/审计链完整恢复，审计哈希链可校验', () => {
  const { dbPath, cleanup } = tempDb();
  try {
    const first = new Persistence({ dbPath });
    first.saveThread({ id: 'th-b', kind: 'agent', title: 'Agent', members: ['u1'] });
    first.appendMessage('th-b', { id: 'm-1', at: '2026-10-21T00:00:00.000Z', from: 'u1', text: '第一句', kind: 'text', engine: { source: 'llm', model: 'deepseek-v4.1-flash' } });
    first.appendMessage('th-b', { id: 'm-2', at: '2026-10-21T00:00:01.000Z', from: 'agent', text: '第二句', kind: 'agent', ticketId: 'tkt-1' });
    first.saveSeq(2);
    const log = new AuditLog({ records: [] });
    const a1 = log.append({ source: 'messaging', action: 'CHAT_MESSAGE', actor: 'u1', target: 'th-b' });
    const a2 = log.append({ source: 'agent-runtime', action: 'AGENT_REPLY', actor: 'agent', target: 'th-b' });
    first.appendAudit(a1);
    first.appendAudit(a2);
    first.close();

    const second = new Persistence({ dbPath });
    try {
      assert.equal(second.hasSeed(), true);
      assert.equal(second.loadSeq(), 2);
      const messages = second.loadMessages('th-b');
      assert.equal(messages.length, 2);
      assert.equal(messages[0].engine.model, 'deepseek-v4.1-flash');
      assert.equal(messages[1].ticketId, 'tkt-1');
      const restored = new AuditLog({ records: second.loadAudit() });
      const integrity = restored.verify();
      assert.equal(integrity.ok, true, '跨重启的审计链仍然完整');
      assert.equal(integrity.length, 2);
      assert.equal(restored.replay('trace-x').length, 0);
      const appended = restored.append({ source: 'messaging', action: 'CHAT_MESSAGE' });
      assert.equal(appended.seq, 3, '恢复后序号从链尾继续，不重置');
    } finally {
      second.close();
    }
  } finally {
    cleanup();
  }
});

test('审计链篡改检测：把持久化记录改一个字段后，重放校验必须失败并指出断点', () => {
  const { dbPath, cleanup } = tempDb();
  try {
    const db = new Persistence({ dbPath });
    const log = new AuditLog({ records: [] });
    db.appendAudit(log.append({ source: 'messaging', action: 'A', actor: 'u1' }));
    db.appendAudit(log.append({ source: 'messaging', action: 'B', actor: 'u1' }));
    db.close();

    const tampered = new Persistence({ dbPath });
    const records = tampered.loadAudit();
    records[1] = Object.freeze({ ...records[1], action: 'B-TAMPERED' });
    const log2 = new AuditLog({ records });
    const integrity = log2.verify();
    assert.equal(integrity.ok, false, '被改写的记录必须让链断裂');
    assert.equal(integrity.brokenAt, records[1].seq);
    tampered.close();
  } finally {
    cleanup();
  }
});

test('reset 清空演示数据（保留表结构），随后可重新播种', () => {
  const memory = createPersistence({ dbPath: ':memory:' });
  try {
    memory.saveThread({ id: 'th-c', kind: 'family', title: '家', members: ['u1'] });
    memory.appendMessage('th-c', { id: 'm-1', at: '2026-10-21T00:00:00.000Z', from: 'u1', text: 'x', kind: 'text' });
    memory.appendAudit(new AuditLog({ records: [] }).append({ source: 'messaging', action: 'A' }));
    memory.saveSeq(5);
    assert.equal(memory.hasSeed(), true);

    memory.reset();
    assert.equal(memory.hasSeed(), false);
    assert.equal(memory.loadMessages('th-c').length, 0);
    assert.equal(memory.loadAudit().length, 0);
    assert.equal(memory.loadSeq(), 0);

    memory.saveThread({ id: 'th-c', kind: 'family', title: '家', members: ['u1'] });
    assert.equal(memory.hasSeed(), true);
  } finally {
    memory.close();
  }
});
