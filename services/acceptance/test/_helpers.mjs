// 测试辅助（非 *.test.mjs，不参与聚合器）：进程内起 im2agent、临时目录、备份副作用清理。
import { mkdtemp, readdir, rm, unlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { startServer } from '../../../apps/im2agent/server.mjs';

export const ADMIN_TOKEN = 'test-admin-token-123';
export const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
export const BACKUP_DIR = path.join(REPO_ROOT, 'apps', 'im2agent', '.data', 'backups');

export async function startDemo({ env = {} } = {}) {
  const started = await startServer({
    env: {
      PROFILE: 'demo',
      PORT: '0',
      STORE: 'memory',
      RATE_LIMIT_PER_MIN: '0',
      ADMIN_TOKEN,
      ...env
    }
  });
  return { started, target: `http://127.0.0.1:${started.port}` };
}

/** 起服务 → 执行 → 必定关闭服务 */
export async function withServer(env, fn) {
  const { started, target } = await startDemo({ env });
  try {
    return await fn({ started, target });
  } finally {
    await started.close();
  }
}

export async function makeTempDir(prefix = 'acceptance-') {
  return mkdtemp(path.join(tmpdir(), prefix));
}

export async function removeDirQuiet(dir) {
  try {
    await rm(dir, { recursive: true, force: true });
  } catch {
    // 清理失败不掩盖测试结果
  }
}

export async function listDirQuiet(dir) {
  try {
    return new Set(await readdir(dir));
  } catch {
    return new Set();
  }
}

/** G-BACKUP 会在目标上真实创建备份文件；测试结束只删除本轮新增的 */
export async function cleanupNewFiles(dir, before) {
  try {
    for (const name of await readdir(dir)) {
      if (before.has(name)) continue;
      await unlink(path.join(dir, name)).catch(() => {});
    }
  } catch {
    // 目录不存在等忽略
  }
}
