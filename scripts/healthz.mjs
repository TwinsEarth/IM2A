import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const gateway = process.env.IM2A_GATEWAY_URL ?? 'http://127.0.0.1:18080';
const dshUrl = process.env.DSH_WEB_URL;
const dshToken = process.env.DSH_WEB_TOKEN;

async function probe(url, headers = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch(url, { headers, signal: controller.signal });
    return { ok: response.ok, status: response.status };
  } catch (error) {
    return { ok: false, status: 0, error: String(error) };
  } finally {
    clearTimeout(timer);
  }
}

const gatewayResult = await probe(`${gateway.replace(/\/+$/, '')}/v1/plugins`);
let dshResult = { ok: false, status: 0, error: 'DSH_WEB_URL/DSH_WEB_TOKEN not configured' };
if (dshUrl && dshToken) {
  dshResult = await probe(`${dshUrl.replace(/\/+$/, '')}/api/health`, {
    authorization: `Bearer ${dshToken}`,
  });
}

const result = gatewayResult.ok && dshResult.ok ? 'passed' : 'blocked';
const evidence = {
  schemaVersion: '1.0',
  checkedAt: new Date().toISOString(),
  gateway: { url: gateway, ...gatewayResult },
  dsh: { url: dshUrl ?? null, ...dshResult },
  result,
};
mkdirSync(join(root, '.tmp'), { recursive: true });
writeFileSync(join(root, '.tmp', 'healthz.json'), JSON.stringify(evidence, null, 2) + '\n');
console.log(JSON.stringify(evidence, null, 2));
if (result !== 'passed') process.exitCode = 1;
