#!/usr/bin/env node
/**
 * 模块职责：v0.0.6 验收 CLI —— 先预检，再对真实目标跑 Gate，输出可归档报告
 * 契约：contracts/INTERFACES.md §45（预检）、§46（执行器）
 *
 * 用法：
 *   node scripts/accept.mjs --target http://127.0.0.1:8080 --token "$ADMIN_TOKEN"
 *   node scripts/accept.mjs --target https://im.example.com --token ... --viewer-token ... \
 *        --require-real-providers --report-dir reports
 *
 * 退出码：0 = 预检通过且无 FAIL；1 = 有 FAIL 或预检拦截；2 = 参数错误
 */
import path from 'node:path';
import { writeFileSync, mkdirSync } from 'node:fs';
import { runPreflight } from '../services/acceptance/src/preflight.mjs';
import { AcceptanceRunner, DEFAULT_GATES } from '../services/acceptance/src/runner.mjs';

const argv = process.argv.slice(2);
const arg = (name, fallback = null) => {
  const index = argv.indexOf(`--${name}`);
  return index >= 0 && argv[index + 1] !== undefined ? argv[index + 1] : fallback;
};
const flag = (name) => argv.includes(`--${name}`);

const target = arg('target');
const token = arg('token');
const viewerToken = arg('viewer-token');
const requireRealProviders = flag('require-real-providers');
const allowMemory = flag('allow-memory');
const reportDir = arg('report-dir', 'reports');
const only = arg('only');

if (!target) {
  console.error('用法：node scripts/accept.mjs --target <url> --token <ADMIN_TOKEN> [--viewer-token <VIEWER_TOKEN>] [--require-real-providers] [--allow-memory] [--only G-HEALTH,G-AUTH] [--report-dir reports]');
  process.exitCode = 2;
} else {
  await main();
}

async function main() {
const startedAt = new Date();
console.log(`\n验收目标：${target}`);
console.log('─'.repeat(60));

// ① 预检
const preflight = await runPreflight({
  target,
  token,
  expectDurable: !allowMemory,
  requireRealProviders
});
for (const check of preflight.checks) {
  const mark = check.ok ? (check.severity === 'WARN' ? '△' : '✔') : '✘';
  console.log(`${mark} [${check.id}] ${check.detail ?? ''}`);
}
if (!preflight.ok) {
  console.error('\n✘ 预检未通过：不执行 Gate（避免把环境问题误判成业务失败）');
  // 注意：不要在 fetch 仍持有 socket 时调用 process.exit（Windows 上会触发 libuv 断言）
  process.exitCode = 1;
  return;
}
console.log('✔ 预检通过\n');

// ② 执行器
const runner = new AcceptanceRunner({ target, token, viewerToken });
for (const gate of DEFAULT_GATES) runner.register(gate);
// v0.0.7：Web 客户端 Gate（壳 + manifest + SW 必须可访问）
runner.register({
  id: 'G-APP',
  name: 'Web 客户端可用',
  tier: 'AUTO',
  async run({ target: base }) {
    const html = await fetch(`${base}/app/`);
    if (!html.ok) return { ok: false, detail: `GET /app/ -> ${html.status}` };
    const body = await html.text();
    const manifest = await fetch(`${base}/app/manifest.webmanifest`);
    const sw = await fetch(`${base}/app/sw.js`);
    const details = `shell=${html.status} manifest=${manifest.status} sw=${sw.status}`;
    if (!body.includes('IM2A')) return { ok: false, detail: `shell 不含品牌标识；${details}` };
    if (!manifest.ok || !sw.ok) return { ok: false, detail: details };
    return { ok: true, detail: details };
  }
});
const report = await runner.run({ only: only === null ? null : only.split(',').map((item) => item.trim()).filter(Boolean) });

for (const result of report.results) {
  const mark = result.status === 'PASS' ? '✔' : result.status === 'FAIL' ? '✘' : '○';
  console.log(`${mark} ${result.id.padEnd(14)} ${result.status.padEnd(6)} ${String(result.ms).padStart(5)}ms  ${result.detail ?? ''}`);
}
console.log('─'.repeat(60));
console.log(`结果：PASS ${report.summary.pass} · FAIL ${report.summary.fail} · SKIP ${report.summary.skip} · MANUAL ${report.summary.manual}`);

// ③ 归档报告
mkdirSync(reportDir, { recursive: true });
const stamp = startedAt.toISOString().replace(/[:.]/g, '-');
const jsonFile = path.join(reportDir, `acceptance-${stamp}.json`);
const markdownFile = path.join(reportDir, `acceptance-${stamp}.md`);
const payload = { target, startedAt: startedAt.toISOString(), preflight, report };
writeFileSync(jsonFile, JSON.stringify(payload, null, 2), 'utf8');

const lines = [
  `# 验收报告（${target}）`,
  '',
  `- 开始：${startedAt.toISOString()}`,
  `- 结束：${report.finishedAt ?? new Date().toISOString()}`,
  `- 预检：${preflight.ok ? '通过' : '未通过'}`,
  `- 结果：PASS ${report.summary.pass} · FAIL ${report.summary.fail} · SKIP ${report.summary.skip} · MANUAL ${report.summary.manual}`,
  '',
  '| Gate | 状态 | 耗时 | 说明 |',
  '|---|---|---|---|',
  ...report.results.map((result) => `| ${result.id} | ${result.status} | ${result.ms}ms | ${String(result.detail ?? '').replace(/\|/g, '\\|')} |`),
  '',
  '> 标 SKIP 的 Gate 表示当前凭证/环境不具备（例如未提供 VIEWER_TOKEN）；MANUAL 表示必须人工签核（见 docs/26）。',
  ''
];
writeFileSync(markdownFile, lines.join('\n'), 'utf8');

console.log(`\n报告：${markdownFile}`);
console.log(`原始：${jsonFile}`);
process.exitCode = report.ok ? 0 : 1;
}
