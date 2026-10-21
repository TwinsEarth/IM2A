/**
 * 模块职责：v0.0.8 多节点广播测试 —— memory/tcp 总线语义 + 两实例跨节点 SSE
 * 契约来源：contracts/INTERFACES.md §48
 *
 * 运行：node tests/broadcast.test.mjs
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createBroadcastBus, BUS_KIND, BUS_CODE } from '../services/messaging/src/broadcast.mjs';
import { startServer } from '../apps/im2agent/server.mjs';

test('memory 总线：订阅收到、退订生效、无订阅者丢弃、close 后 publish 不抛', async () => {
  const bus = createBroadcastBus({ kind: BUS_KIND.MEMORY, nodeId: 'n1' });
  await bus.start();
  const received = [];
  const off = bus.subscribe('t', (payload) => received.push(payload));
  bus.publish('t', { a: 1 });
  assert.equal(received.length, 1);
  off();
  bus.publish('t', { a: 2 });
  assert.equal(received.length, 1, '退订后不应再收到');
  const dropped = bus.publish('nobody', { x: 1 });
  assert.equal(dropped.ok, true, '无订阅者不是错误（总线不持久化）');
  await bus.close();
  const afterClose = bus.publish('t', { a: 3 });
  assert.equal(afterClose.ok, false);
  assert.equal(afterClose.code, BUS_CODE.BUS_CLOSED);
});

test('tcp 总线：两节点互收、同源保序、回环去重', async () => {
  const a = createBroadcastBus({ kind: BUS_KIND.TCP, nodeId: 'A', listenPort: 0 });
  await a.start();
  const b = createBroadcastBus({ kind: BUS_KIND.TCP, nodeId: 'B', listenPort: 0, peers: [`127.0.0.1:${a.port}`] });
  await b.start();
  // 双向连接：再让 A 连 B
  const a2 = createBroadcastBus({ kind: BUS_KIND.TCP, nodeId: 'A2', listenPort: 0, peers: [`127.0.0.1:${b.port}`] });
  await a2.start();

  await new Promise((resolve) => setTimeout(resolve, 100)); // 等连接建立

  const atB = [];
  const atA = [];
  b.subscribe('orders', (payload) => atB.push(payload.seq));
  a.subscribe('orders', (payload) => atA.push(payload.seq));

  for (let seq = 1; seq <= 10; seq += 1) a.publish('orders', { seq });
  await new Promise((resolve) => setTimeout(resolve, 200));

  assert.deepEqual(atB, [1, 2, 3, 4, 5, 6, 7, 8, 9, 10], '同源事件在 B 端保序且不重复');
  assert.equal(atA.length, 10, 'A 本地也收到自己的事件（且不因回环重复）');
  const deduped = b.stats().deduped + a.stats().deduped;
  assert.ok(deduped >= 0);

  // 反向：B 发布，A 收到
  const reverse = [];
  a.subscribe('notify', (payload) => reverse.push(payload.seq));
  b.publish('notify', { seq: 99 });
  await new Promise((resolve) => setTimeout(resolve, 200));
  assert.deepEqual(reverse, [99]);

  await a2.close();
  await b.close();
  await a.close();
});

test('缺陷与边界：超大消息被丢弃计数；非法配置抛 INVALID_CONFIG', async () => {
  const bus = createBroadcastBus({ kind: BUS_KIND.MEMORY, nodeId: 'n' });
  await bus.start();
  const big = { text: 'x'.repeat(70 * 1024) };
  const result = bus.publish('t', big);
  assert.equal(result.ok, false);
  assert.equal(result.code, 'MESSAGE_TOO_LARGE');
  assert.equal(bus.stats().dropped, 1);
  await bus.close();

  assert.throws(() => createBroadcastBus({ kind: 'kafka' }), { code: BUS_CODE.INVALID_CONFIG });
});

test('跨节点 SSE：A 节点的决策事件能推送到 B 节点的客户端', async () => {
  // 两个 im2agent 实例，B 通过 TCP 总线连 A
  const appA = await startServer({ env: { PROFILE: 'demo', PORT: '0', STORE: 'memory', RATE_LIMIT_PER_MIN: '0', IM2AGENT_BUS: 'tcp', IM2AGENT_BUS_PORT: '0' } });
  const appB = await startServer({
    env: {
      PROFILE: 'demo',
      PORT: '0',
      STORE: 'memory',
      RATE_LIMIT_PER_MIN: '0',
      IM2AGENT_BUS: 'tcp',
      IM2AGENT_BUS_PORT: '0',
      IM2AGENT_BUS_PEERS: `127.0.0.1:${appA.holder.bus.port}`
    }
  });
  try {
    await new Promise((resolve) => setTimeout(resolve, 150));

    // 在 B 上订阅 SSE
    const controller = new AbortController();
    const response = await fetch(`${appB.urls[0]}/api/events`, { headers: { accept: 'text/event-stream' }, signal: controller.signal });
    assert.equal(response.status, 200);
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    const readUntil = async (needle, timeoutMs = 4000) => {
      const deadline = Date.now() + timeoutMs;
      while (Date.now() < deadline) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        if (buffer.includes(needle)) return buffer;
      }
      throw new Error(`SSE 未在 ${timeoutMs}ms 内收到 ${needle}：${buffer.slice(0, 200)}`);
    };

    try {
      await readUntil('hello');
      // 在 A 上做一次决策 → B 的 SSE 应该收到
      const decide = await fetch(`${appA.urls[0]}/api/approval/decide`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ ticket_id: 'tkt-procurement-1', action: 'APPROVE' })
      });
      assert.equal(decide.status, 200);
      buffer = '';
      const text = await readUntil('decide');
      assert.match(text, /tkt-procurement-1/);
      assert.equal(appA.holder.bus.kind, 'tcp');
      assert.equal(appB.holder.bus.kind, 'tcp');
    } finally {
      controller.abort();
    }
  } finally {
    await appB.close();
    await appA.close();
  }
});
