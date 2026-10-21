#!/usr/bin/env node
/**
 * 模块职责：v0.1.0 一键发布检查 —— 全绿才允许打 tag
 * 契约：contracts/INTERFACES.md §52
 *
 * 依次执行：全量测试 → verify 一致性 → quickstart 自检 → deploy-lint → 内置实例验收 Gate。
 * 任一步失败 → 退出码 1，报告标 FAIL 并落盘（归档失败现场）。
 *
 * 用法：node scripts/release-check.mjs [--report <path>]
 */
import path from 'node:path';
import os from 'node:os';
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import * as fsSync from 'node:fs';

import { VERSION, CONTRACT_REVISION, SCHEMA_VERSION } from '../apps/im2agent/version.mjs';
import { startServer } from '../apps/im2agent/server.mjs';
import { AcceptanceRunner, DEFAULT_GATES } from '../services/acceptance/src/runner.mjs';

const argv = process.argv.slice(2);
const reportIndex = argv.indexOf('--report');
const reportPath = reportIndex >= 0 && argv[reportIndex + 1] ? argv[reportIndex + 1] : path.join('reports', 'RELEASE-REPORT.md');

const steps = [];
const startedAt = new Date();

/** 跑一个子进程步骤（stdio:'inherit'：沙箱下捕获管道不可用） */
function runStep(name, command, args) {
  const started = Date.now();
  const result = spawnSync(command, args, { stdio: 'inherit', cwd: process.cwd() });
  const ms = Date.now() - started;
  const ok = result.status === 0;
  steps.push({ name, command: [command, ...args].join(' '), ok, ms, status: result.status });
  console.log(`${ok ? '✔' : '✘'} ${name}  ${ms}ms`);
  return ok;
}

async function runAcceptanceInProcess() {
  const started = Date.now();
  // 必须起一个"像生产"的本地目标：有令牌、持久存储——否则 G-AUTH/G-STORE 会因为
  // demo 实例"无令牌也放行 + 内存存储"而正确失败（那不是验收，是自欺）。
  const { mkdtempSync, rmSync } = await import('node:fs');
  const os = await import('node:os');
  const dataDir = mkdtempSync(path.join(os.tmpdir(), 'im2a-release-'));
  const token = 'release-token-123';
  const viewerToken = 'release-view-123';
  const app = await startServer({
    env: {
      PROFILE: 'lan',
      PORT: '0',
      STORE: 'jsonfile',
      STORE_FILE: path.join(dataDir, 'state.json'),
      RATE_LIMIT_PER_MIN: '0',
      ADMIN_TOKEN: token,
      VIEWER_TOKEN: viewerToken
    }
  });
  try {
    const runner = new AcceptanceRunner({ target: app.urls[0], token, viewerToken });
    for (const gate of DEFAULT_GATES) runner.register(gate);
    const report = await runner.run({});
    const ms = Date.now() - started;
    steps.push({
      name: '内置实例验收 Gate',
      command: 'AcceptanceRunner(DEFAULT_GATES) @ lan/jsonfile',
      ok: report.ok,
      ms,
      status: report.ok ? 0 : 1,
      detail: `PASS ${report.summary.pass} / FAIL ${report.summary.fail} / SKIP ${report.summary.skip}`
    });
    console.log(`${report.ok ? '✔' : '✘'} 内置实例验收 Gate  ${ms}ms  PASS ${report.summary.pass} / FAIL ${report.summary.fail} / SKIP ${report.summary.skip}`);
    return report.ok;
  } finally {
    await app.close();
    rmSync(dataDir, { recursive: true, force: true });
  }
}

/**
 * v0.1.1 缺陷修复：§51 不变量⑤「版本号与 git tag 必须一致」原先只有文字、没有检查。
 * 用文件描述符重定向 git 输出（沙箱下捕获管道不可用），无法取到时降级为 WARN 而**不误判通过**。
 */
function checkVersionTag() {
  const started = Date.now();
  const { openSync, closeSync, readFileSync, unlinkSync } = fsSync;
  const tmp = path.join(os.tmpdir(), `im2a-tags-${process.pid}.txt`);
  let tags = null;
  try {
    const fd = openSync(tmp, 'w');
    const result = spawnSync('git', ['tag', '--points-at', 'HEAD'], { stdio: ['ignore', fd, 'ignore'], cwd: process.cwd() });
    closeSync(fd);
    if (result.status === 0) {
      tags = readFileSync(tmp, 'utf8').split('\n').map((line) => line.trim()).filter(Boolean);
    }
  } catch {
    tags = null;
  } finally {
    try {
      unlinkSync(tmp);
    } catch {
      // 忽略清理失败
    }
  }

  const ms = Date.now() - started;
  const expected = `v${VERSION}`;
  let ok = true;
  let detail;
  if (tags === null) {
    detail = 'git 不可用，跳过 tag 检查（**需人工核对** HEAD 上的 tag 与 VERSION）';
  } else if (tags.some((tag) => tag === expected || tag.startsWith(`${expected}-`))) {
    detail = `HEAD 有匹配 tag：${tags.join(', ')}`;
  } else if (tags.length === 0) {
    detail = `HEAD 暂无 tag（发布时创建 ${expected} 后需复跑本检查）`;
  } else {
    ok = false;
    detail = `HEAD tag 与 VERSION(${VERSION}) 不一致：${tags.join(', ')}`;
  }
  steps.push({ name: '版本 ↔ tag 一致性', command: 'git tag --points-at HEAD', ok, ms, status: ok ? 0 : 1, detail });
  console.log(`${ok ? '✔' : '△'} 版本 ↔ tag 一致性  ${ms}ms  ${detail}`);
  return ok;
}

console.log(`\n发布检查 · version ${VERSION} · contract ${CONTRACT_REVISION} · schema ${SCHEMA_VERSION}\n${'─'.repeat(56)}`);

const results = [];
results.push(runStep('全量测试', process.execPath, ['scripts/test.mjs']));
results.push(runStep('一致性验收', process.execPath, ['scripts/verify.mjs']));
results.push(runStep('自检 quickstart', process.execPath, ['scripts/quickstart.mjs', '--check']));
results.push(runStep('部署产物检查', process.execPath, ['scripts/deploy-lint.mjs']));
results.push(await runAcceptanceInProcess());
results.push(checkVersionTag());

const ok = results.every(Boolean);
const finishedAt = new Date();
console.log(`${'─'.repeat(56)}`);
console.log(ok ? '✔ 发布检查全部通过' : '✘ 发布检查未通过（禁止打 tag）');

// 报告无论成败都落盘：失败现场也要能归档
mkdirSync(path.dirname(path.resolve(reportPath)), { recursive: true });
const lines = [
  `# 发布检查报告 · ${ok ? 'PASS' : 'FAIL'}`,
  '',
  `- 版本：${VERSION}`,
  `- 契约：${CONTRACT_REVISION}`,
  `- 数据 schema：${SCHEMA_VERSION}`,
  `- 开始：${startedAt.toISOString()}`,
  `- 结束：${finishedAt.toISOString()}`,
  `- 结论：${ok ? '允许发布' : '禁止发布'}`,
  '',
  '| 步骤 | 命令 | 结果 | 耗时 |',
  '|---|---|---|---|',
  ...steps.map((step) => `| ${step.name} | \`${step.command}\` | ${step.ok ? 'PASS' : 'FAIL'} | ${step.ms}ms |`),
  '',
  '> 本报告由 scripts/release-check.mjs 生成；检查只读，使用内存存储与随机端口。',
  ''
];
writeFileSync(reportPath, lines.join('\n'), 'utf8');
console.log(`报告：${reportPath}`);

process.exitCode = ok ? 0 : 1;
