/**
 * 模块职责：多节点事件广播总线（memory / tcp 同构接口）—— 横向扩展的最小实现
 * 契约来源：contracts/INTERFACES.md §48（v0.0.8）
 *
 * 为什么不是消息队列：总线**不持久化**，没有订阅者就丢弃；它的职责只是"把事件扇出到各节点的在线客户端"。
 * 生产替换为 Redis Pub/Sub 时，只需替换本文件实现，调用方（SSE 层）不变。
 *
 * 不变量：
 *   · 每个事件每个节点最多投递一次（按 id 去重，跨节点回环不得重复触发本地订阅）
 *   · 同一 origin 的事件在订阅端保持发布顺序
 *   · close 后 publish 不抛，返回 { ok:false, code:'BUS_CLOSED' }
 *   · 单条消息 JSON 上限 64KB，超限丢弃并计入 stats.dropped
 */
import { createServer, connect } from 'node:net';
import { randomUUID } from 'node:crypto';

export const BUS_KIND = Object.freeze({ MEMORY: 'memory', TCP: 'tcp' });
export const BUS_CODE = Object.freeze({ BUS_CLOSED: 'BUS_CLOSED', INVALID_CONFIG: 'INVALID_CONFIG', PEER_FAILED: 'PEER_FAILED' });

export class BroadcastError extends Error {
  constructor(message, code = BUS_CODE.INVALID_CONFIG) {
    super(message);
    this.name = 'BroadcastError';
    this.code = code;
  }
}

const MAX_BYTES = 64 * 1024;
const DEDUPE_CAP = 20_000;

function createCore({ kind, nodeId, clock, onDrop }) {
  const subscribers = new Map(); // topic -> Set<handler>
  const seen = new Set();
  const seenOrder = [];
  let counter = 0;
  const stats = { published: 0, received: 0, delivered: 0, deduped: 0, dropped: 0 };
  let closed = false;

  const remember = (id) => {
    if (seen.has(id)) return false;
    seen.add(id);
    seenOrder.push(id);
    if (seenOrder.length > DEDUPE_CAP) {
      const evicted = seenOrder.shift();
      seen.delete(evicted);
    }
    return true;
  };

  const deliverLocal = (topic, payload) => {
    const handlers = subscribers.get(topic);
    if (!handlers || handlers.size === 0) return 0;
    let count = 0;
    for (const handler of handlers) {
      try {
        handler(payload);
        count += 1;
      } catch {
        // 单个订阅者异常不能影响其它订阅者
      }
    }
    stats.delivered += count;
    return count;
  };

  return {
    kind,
    nodeId,
    nextId() {
      counter += 1;
      return `${nodeId}:${counter}`;
    },
    /** 本地订阅（远程事件与本地事件走同一入口） */
    subscribe(topic, handler) {
      if (typeof handler !== 'function') throw new BroadcastError('subscribe: handler 必须是函数');
      const key = String(topic);
      if (!subscribers.has(key)) subscribers.set(key, new Set());
      subscribers.get(key).add(handler);
      return () => subscribers.get(key)?.delete(handler);
    },
    /** 接收"任意来源"的事件（远程节点或本地）；返回是否新鲜 */
    ingest(event) {
      if (closed) return false;
      stats.received += 1;
      if (!event || typeof event.topic !== 'string' || typeof event.id !== 'string') return false;
      if (!remember(event.id)) {
        stats.deduped += 1;
        return false;
      }
      deliverLocal(event.topic, event.payload);
      return true;
    },
    publishLocal(topic, payload, id) {
      if (closed) return { ok: false, code: BUS_CODE.BUS_CLOSED };
      const serialized = JSON.stringify({ topic, payload, id, origin: nodeId });
      if (Buffer.byteLength(serialized) > MAX_BYTES) {
        stats.dropped += 1;
        onDrop?.('TOO_LARGE', topic);
        return { ok: false, code: 'MESSAGE_TOO_LARGE' };
      }
      remember(id);
      stats.published += 1;
      deliverLocal(topic, payload);
      return { ok: true, event: { topic, payload, id, origin: nodeId } };
    },
    stats: () => ({ ...stats, nodeId, kind, topics: subscribers.size }),
    closeAll() {
      closed = true;
      subscribers.clear();
    },
    isClosed: () => closed
  };
}

function createMemoryBus({ nodeId = 'node-local', clock = () => Date.now() } = {}) {
  const core = createCore({ kind: BUS_KIND.MEMORY, nodeId, clock });
  return {
    kind: BUS_KIND.MEMORY,
    get nodeId() {
      return core.nodeId;
    },
    async start() {
      return this;
    },
    async close() {
      core.closeAll();
    },
    publish(topic, payload, { id = null } = {}) {
      return core.publishLocal(String(topic), payload, id ?? core.nextId());
    },
    subscribe: core.subscribe,
    stats: core.stats
  };
}

function createTcpBus({ nodeId = null, listenPort = 0, peers = [], clock = () => Date.now() } = {}) {
  const id = nodeId ?? `node-${randomUUID().slice(0, 8)}`;
  const core = createCore({ kind: BUS_KIND.TCP, nodeId: id, clock, onDrop: () => {} });
  const sockets = new Set();
  let server = null;
  let actualPort = null;
  const peerStats = [];

  const wire = (socket, label) => {
    sockets.add(socket);
    let buffer = '';
    socket.setEncoding('utf8');
    socket.on('data', (chunk) => {
      buffer += chunk;
      let newline = buffer.indexOf('\n');
      while (newline >= 0) {
        const line = buffer.slice(0, newline);
        buffer = buffer.slice(newline + 1);
        if (line.trim() !== '') {
          try {
            core.ingest(JSON.parse(line));
          } catch {
            // 坏帧直接丢弃，不影响连接
          }
        }
        newline = buffer.indexOf('\n');
      }
    });
    socket.on('error', () => {});
    socket.on('close', () => sockets.delete(socket));
    // 首次连接时向对端宣告自身身份（便于对端日志和调试；不影响投递语义）
    socket.write(`${JSON.stringify({ type: 'hello', nodeId: id })}\n`);
    return socket;
  };

  const forward = (event) => {
    const frame = `${JSON.stringify(event)}\n`;
    for (const socket of sockets) {
      try {
        socket.write(frame);
      } catch {
        sockets.delete(socket);
      }
    }
  };

  return {
    kind: BUS_KIND.TCP,
    get nodeId() {
      return id;
    },
    get port() {
      return actualPort;
    },
    async start() {
      server = createServer((socket) => wire(socket, 'inbound'));
      await new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(listenPort, '127.0.0.1', () => {
          actualPort = server.address().port;
          resolve();
        });
      });
      for (const peer of peers) {
        const [host, portText] = String(peer).split(':');
        const port = Number(portText);
        if (!Number.isInteger(port) || port <= 0) {
          peerStats.push({ peer, status: 'INVALID' });
          continue;
        }
        const socket = connect({ host: host || '127.0.0.1', port }, () => wire(socket, 'outbound'));
        socket.on('error', () => {
          socket.destroy();
          sockets.delete(socket);
          peerStats.push({ peer, status: 'FAILED' });
        });
      }
      return this;
    },
    async close() {
      core.closeAll();
      for (const socket of sockets) socket.destroy();
      sockets.clear();
      if (server) await new Promise((resolve) => server.close(() => resolve()));
    },
    publish(topic, payload, { id = null } = {}) {
      if (core.isClosed()) return { ok: false, code: BUS_CODE.BUS_CLOSED };
      const eventId = id ?? core.nextId();
      const result = core.publishLocal(String(topic), payload, eventId);
      if (result.ok) forward({ ...result.event });
      return result;
    },
    subscribe: core.subscribe,
    stats: () => ({ ...core.stats(), port: actualPort, peers: sockets.size, peerStats: peerStats.slice(-10) })
  };
}

export function createBroadcastBus({ kind = BUS_KIND.MEMORY, nodeId = null, listenPort = 0, peers = [], clock = () => Date.now() } = {}) {
  if (kind === BUS_KIND.MEMORY) return createMemoryBus({ nodeId: nodeId ?? 'node-local', clock });
  if (kind === BUS_KIND.TCP) return createTcpBus({ nodeId, listenPort, peers, clock });
  throw new BroadcastError(`未知 BUS_KIND: ${String(kind)}（允许 memory / tcp）`, BUS_CODE.INVALID_CONFIG);
}
