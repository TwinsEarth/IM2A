/**
 * 模块职责：HTTPS/TLS 配置测试 —— 缺证书必须抛具名错误（不静默降级为 http）
 * 契约来源：apps/im2a/server.mjs loadTlsOptions / startServer；部署文档见 apps/im2a/README.md
 *
 * 说明：本机不依赖 openssl 生成证书，因此仅验证配置边界；真实握手在部署环境用 `curl -k https://…` 验证。
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { loadTlsOptions, startServer } from '../apps/im2a/server.mjs';

test('loadTlsOptions：缺 cert/key 参数时抛 TLS_CONFIG_MISSING', () => {
  assert.throws(() => loadTlsOptions({}), /TLS_CONFIG_MISSING/);
  assert.throws(() => loadTlsOptions({ certPath: 'a.pem' }), /TLS_CONFIG_MISSING/);
  assert.throws(() => loadTlsOptions({ keyPath: 'b.pem' }), /TLS_CONFIG_MISSING/);
});

test('loadTlsOptions：路径不存在时抛 TLS_CONFIG_MISSING 并带上原因', () => {
  assert.throws(
    () => loadTlsOptions({ certPath: join(tmpdir(), 'no-such-cert-xyz.pem'), keyPath: join(tmpdir(), 'no-such-key-xyz.pem') }),
    /TLS_CONFIG_MISSING: 无法读取证书\/私钥/
  );
});

test('startServer(tls)：证书无效时启动失败，绝不静默降级为 http', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'agentim-tls-'));
  const certPath = join(dir, 'cert.pem');
  const keyPath = join(dir, 'key.pem');
  writeFileSync(certPath, 'not-a-certificate');
  writeFileSync(keyPath, 'not-a-key');
  try {
    await assert.rejects(() => startServer({ port: 0, db: ':memory:', tls: { certPath, keyPath } }), /(TLS_CONFIG_MISSING|error:)/i);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
