/**
 * 模块职责：im2agent 极轻量原型的存储适配层 —— memory / jsonfile / postgres(可选，未实现即显式拒绝)
 * 契约来源：本原型自述（apps/im2agent/README.md）；不引入任何第三方依赖
 *
 * 设计原则：
 *   · 默认 memory：`node server.mjs` 零依赖起闭环，进程退出即清空（Demo 形态）
 *   · jsonfile：单文件落盘，重启可恢复（局域网形态够用）
 *   · postgres：生产形态的适配点。本机无驱动（零依赖 + 无 pg 客户端），
 *     因此**显式抛 STORE_NOT_AVAILABLE**，绝不静默降级成文件存储——降级必须由运维显式选择。
 */
import { readFileSync, writeFileSync, mkdirSync, renameSync } from 'node:fs';
import path from 'node:path';

export const STORE_KIND = Object.freeze({ MEMORY: 'memory', JSONFILE: 'jsonfile', POSTGRES: 'postgres' });

export class StoreError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'StoreError';
    this.code = code;
  }
}

/** 统一接口：read()/write(snapshot)/kind/info()。所有实现都必须同步或可 await。 */
export function createStore({ kind = STORE_KIND.MEMORY, file = null, dsn = null } = {}) {
  if (kind === STORE_KIND.MEMORY) {
    let snapshot = null;
    return {
      kind,
      info: () => ({ kind, durable: false, note: '进程退出即清空（Demo 形态）' }),
      read: () => snapshot,
      write: (next) => {
        snapshot = next;
      }
    };
  }

  if (kind === STORE_KIND.JSONFILE) {
    if (typeof file !== 'string' || file === '') throw new StoreError('jsonfile 需要 file 路径', 'STORE_INVALID');
    const dir = path.dirname(file);
    let cached = null;
    return {
      kind,
      info: () => ({ kind, durable: true, file }),
      read: () => {
        if (cached !== null) return cached;
        try {
          cached = JSON.parse(readFileSync(file, 'utf8'));
          return cached;
        } catch (error) {
          if (error?.code === 'ENOENT') return null;
          // 坏文件不静默清空：交给调用方决定（备份/重建），避免"数据看起来还在其实已丢"
          throw new StoreError(`状态文件不可读或不是合法 JSON: ${file}`, 'STORE_CORRUPT');
        }
      },
      write: (next) => {
        cached = next;
        mkdirSync(dir, { recursive: true });
        const tmp = `${file}.tmp`;
        writeFileSync(tmp, JSON.stringify(next, null, 2), 'utf8');
        renameSync(tmp, file); // 原子替换，避免半写
      }
    };
  }

  if (kind === STORE_KIND.POSTGRES) {
    // 生产形态：本机没有 pg 客户端，也不允许偷偷退回文件存储。
    return {
      kind,
      info: () => ({ kind, durable: true, dsn: dsn === null ? null : '***', available: false }),
      read: () => {
        throw new StoreError('postgres 存储在本原型中未实现（缺驱动）；请显式改用 STORE=jsonfile 或接入 pg', 'STORE_NOT_AVAILABLE');
      },
      write: () => {
        throw new StoreError('postgres 存储在本原型中未实现（缺驱动）；请显式改用 STORE=jsonfile 或接入 pg', 'STORE_NOT_AVAILABLE');
      }
    };
  }

  throw new StoreError(`未知 STORE 类型: ${String(kind)}（允许 memory/jsonfile/postgres）`, 'STORE_INVALID');
}
