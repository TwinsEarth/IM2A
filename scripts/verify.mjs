#!/usr/bin/env node
/**
 * 模块职责：交付物一致性验收脚本（Lead 的验收门禁）——机械校验「文档 / 契约 / 代码」三者不漂移
 * 契约来源：contracts/INTERFACES.md v1.0
 *
 * 校验项：
 *   1. 依赖洁净性：services/ 与 apps/ 下只允许 `node:` 内置模块与相对路径 import（零第三方依赖）
 *   2. 测试纪律：不允许 `node --test`（本机 Windows 沙箱禁止子进程管道，会 EPERM）
 *   3. 实现模块首行声明「模块职责」
 *   4. 契约里出现的模块路径都真实存在
 *   5. 三张状态机的枚举值在实现与 proto 中都齐全
 *   6. 文档内部链接可解析（相对路径的 md/mjs/proto/yaml/dart 文件存在）
 *   7. openapi 声明的路径在演示服务里确实有实现
 *   8. 文档术语不出现契约外的状态名（防手写漂移）
 *
 * 用法：node scripts/verify.mjs        # exit 0 = 全部通过
 */
import { readdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..');
const results = [];
const record = (name, ok, detail = '') => results.push({ name, ok, detail });

async function walk(dir, out = [], filter = () => true) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries) {
    if (entry.name.startsWith('.') || entry.name === 'node_modules') continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) await walk(full, out, filter);
    else if (filter(full)) out.push(full);
  }
  return out;
}

const read = (p) => readFile(p, 'utf8');
const rel = (p) => path.relative(ROOT, p).replaceAll('\\', '/');
const exists = async (p) => !!(await stat(p).catch(() => null));

// ───────────────────────── 1. 依赖洁净性 ─────────────────────────
{
  const files = await walk(ROOT, [], (f) => f.endsWith('.mjs') && !rel(f).startsWith('scripts/'));
  const offenders = [];
  for (const file of files) {
    const text = await read(file);
    for (const match of text.matchAll(/from\s+['"]([^'"]+)['"]/g)) {
      const spec = match[1];
      const ok = spec.startsWith('node:') || spec.startsWith('./') || spec.startsWith('../');
      if (!ok) offenders.push(`${rel(file)} → ${spec}`);
    }
  }
  record('依赖洁净性：仅 node: 内置 + 相对路径', offenders.length === 0, offenders.join('; '));
}

// ───────────────────────── 2. 测试纪律 ─────────────────────────
{
  // 排除本文件自身（它包含用于检测的字符串字面量）
  const files = await walk(ROOT, [], (f) => f.endsWith('.mjs') && rel(f) !== 'scripts/verify.mjs');
  const offenders = [];
  for (const file of files) {
    const text = await read(file);
    // 允许出现在注释/字符串说明里，禁止作为实际命令调用
    if (/"node --test |node --test ['"]|spawn\(\s*['"]node['"],\s*\[\s*['"]--test/.test(text)) {
      offenders.push(rel(file));
    }
  }
  record('测试纪律：未使用 node --test / 子进程 spawn', offenders.length === 0, offenders.join('; '));
}

// ───────────────────────── 3. 实现模块首行职责声明 ─────────────────────────
{
  const files = (
    await Promise.all([
      walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && rel(f).includes('/src/')),
      walk(path.join(ROOT, 'apps'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('clients-flutter'))
    ])
  ).flat();
  const missing = [];
  for (const file of files) {
    const head = (await read(file)).slice(0, 400);
    if (!head.includes('模块职责')) missing.push(rel(file));
  }
  record(`实现模块首行含「模块职责」声明（${files.length} 个）`, missing.length === 0, missing.join('; '));
}

// ───────────────────────── 4. 契约模块路径存在 ─────────────────────────
{
  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const paths = [
    ...new Set([...contract.matchAll(/`?((?:services|apps)\/[\w./-]+\.mjs)`?/g)].map((m) => m[1]))
  ].filter((p) => p.includes('/src/') || !p.includes('/test/'));
  const missing = [];
  for (const p of paths) if (!(await exists(path.join(ROOT, p)))) missing.push(p);
  record(`契约引用的模块文件都存在（${paths.length} 个）`, missing.length === 0, missing.join('; '));
}

// ───────────────────────── 5. 状态机枚举三方一致 ─────────────────────────
{
  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const protoText = (
    await Promise.all((await walk(path.join(ROOT, 'contracts/proto'), [], (f) => f.endsWith('.proto'))).map(read))
  ).join('\n');

  const machines = {
    TICKET_STATE: ['PENDING', 'REVIEWING', 'APPROVED', 'REJECTED', 'MODIFIED', 'EXECUTING', 'COMPLETED', 'FAILED', 'EXPIRED'],
    TASK_STATE: ['SUBMITTED', 'WORKING', 'INPUT_REQUIRED', 'AUTH_REQUIRED', 'COMPLETED', 'FAILED', 'CANCELED', 'REJECTED'],
    MSG_STATE: ['INTENT', 'AWAITING_PAYMENT', 'REFUSED', 'PAID', 'VIEWED', 'REPLIED', 'SETTLED', 'REFUNDED', 'EXPIRED'],
    TRUST_LEVELS: ['SELF', 'FAMILY', 'TRUSTED', 'KNOWN', 'STRANGER'],
    // v2 新增：频道 / 会话 / 协议 / 可见域（proto 侧首值与大小写不同，用 i 标志兼容 Domain 的 CONTACT vs 'contact'）
    CHANNEL_TYPE: ['DIRECT', 'GROUP', 'TASK', 'AGENT_NEGOTIATION', 'STRANGER_QUEUE'],
    SESSION_STATE: ['CONNECTING', 'ONLINE', 'OFFLINE', 'REVOKED'],
    PROTOCOL: ['AGENT_WS', 'IM_CUSTOM', 'A2A_JSONRPC', 'MCP_TOOLS'],
    DOMAIN: ['contact', 'group', 'task', 'schedule', 'memory']
  };

  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');

  for (const [machine, values] of Object.entries(machines)) {
    const inContract = values.every((v) => contract.includes(v));
    const inSrc = values.every((v) => srcText.includes(`'${v}'`) || srcText.includes(`"${v}"`));
    const inProto = values.every((v) => new RegExp(`\\b${v}\\s*=\\s*\\d+`, 'i').test(protoText));
    record(
      `状态机 ${machine} 三方差值一致（${values.length} 态）`,
      inContract && inSrc && inProto,
      `contract=${inContract} impl=${inSrc} proto=${inProto}`
    );
  }
}

// ───────────────────────── 6. 文档内部链接可解析 ─────────────────────────
{
  const docs = await walk(path.join(ROOT, 'docs'), [], (f) => f.endsWith('.md'));
  docs.push(path.join(ROOT, 'README.md'));
  const broken = [];
  let total = 0;
  for (const doc of docs) {
    const text = await read(doc);
    for (const match of text.matchAll(/\]\(([^)#\s]+)(?:#[^)]*)?\)/g)) {
      const target = match[1];
      if (/^(https?:|mailto:)/.test(target)) continue;
      total += 1;
      const resolved = path.resolve(path.dirname(doc), decodeURIComponent(target));
      if (!(await exists(resolved))) broken.push(`${rel(doc)} → ${target}`);
    }
  }
  record(`文档内部链接可解析（${total} 条）`, broken.length === 0, broken.join('; '));
}

// ───────────────────────── 7. openapi 路径 ↔ 演示服务实现 ─────────────────────────
{
  const specs = await walk(path.join(ROOT, 'contracts/openapi'), [], (f) => f.endsWith('.yaml'));
  const server = (
    await Promise.all([
      read(path.join(ROOT, 'apps/demo-server/server.mjs')),
      read(path.join(ROOT, 'apps/im-live/server.mjs'))
    ])
  ).join('\n');
  const declared = [];
  for (const specFile of specs) {
    const text = await read(specFile);
    for (const match of text.matchAll(/^\s{2}(\/api\/[\w{}/-]+):/gm)) declared.push(match[1]);
  }
  const missing = declared.filter((p) => {
    // /api/intent/{id}/screen → 在 server 中应以正则或字面量出现
    const base = p.split('/{')[0];
    if (!server.includes(base)) return true;
    const tail = p.includes('/{') ? p.slice(p.lastIndexOf('}/') + 2) : null;
    return tail ? !server.includes(tail) : false;
  });
  record(
    `openapi 声明的 ${declared.length} 条路径在演示服务中有实现（${specs.length} 个 spec）`,
    missing.length === 0,
    missing.join('; ')
  );
}

// ───────────────────────── 8. 术语防漂移 ─────────────────────────
{
  const docs = await walk(path.join(ROOT, 'docs'), [], (f) => f.endsWith('.md'));
  // 常见的手写错误：把契约里的名字写错（契约中不存在这些 token）
  const forbidden = ['PENDING_PAYMENT', 'AWAITING_PAY', 'PAYMENT_PENDING', 'TRUST_LEVEL_', 'TICKET_STATES', 'MSG_STATUS', 'TASK_STATUS'];
  const hits = [];
  for (const doc of docs) {
    const text = await read(doc);
    for (const token of forbidden) {
      // 用「前后不是大写字母/下划线」做边界，避免 AWAITING_PAY 误伤 AWAITING_PAYMENT
      if (new RegExp(`(^|[^A-Z_])${token}($|[^A-Z_])`).test(text)) hits.push(`${rel(doc)}: ${token}`);
    }
  }
  record('文档未出现契约外状态名', hits.length === 0, hits.join('; '));
}

// ───────────────────────── 9. v2 交付完整性 ─────────────────────────
{
  const v2Modules = [
    'services/identity/src/agent-id.mjs',
    'services/identity/src/index.mjs',
    'services/gateway/src/agent-gateway.mjs',
    'services/gateway/src/visibility.mjs',
    'services/gateway/src/index.mjs',
    'services/messaging/src/channel.mjs',
    'services/messaging/src/session.mjs',
    'services/audit/src/signed-audit.mjs',
    'services/audit/src/index.mjs'
  ];
  const missingModules = [];
  for (const modulePath of v2Modules) if (!(await exists(path.join(ROOT, modulePath)))) missingModules.push(modulePath);
  record(`v2 模块文件齐备（${v2Modules.length} 个）`, missingModules.length === 0, missingModules.join('; '));

  const v2Protos = ['agent_identity.proto', 'gateway.proto', 'channel.proto', 'signed_audit.proto'];
  const missingProtos = [];
  for (const proto of v2Protos) if (!(await exists(path.join(ROOT, 'contracts/proto', proto)))) missingProtos.push(proto);
  record(`v2 proto 契约齐备（${v2Protos.length} 个）`, missingProtos.length === 0, missingProtos.join('; '));

  record(
    'v2 REST 契约存在（contracts/openapi/v2-agent-network.openapi.yaml）',
    await exists(path.join(ROOT, 'contracts/openapi/v2-agent-network.openapi.yaml')),
    '缺失'
  );
}

// ───────────────────────── 10. v2 文档口径完整 ─────────────────────────
{
  const doc11 = path.join(ROOT, 'docs/11-AI原生IM架构-v2.md');
  const doc12 = path.join(ROOT, 'docs/12-开源IM项目对标与取舍.md');
  if (!(await exists(doc11)) || !(await exists(doc12))) {
    record('v2 文档存在（docs/11 架构修订 + docs/12 开源对标）', false, '缺文件');
  } else {
    const t11 = await read(doc11);
    const t12 = await read(doc12);
    const hasAllSix = ['V2-1', 'V2-2', 'V2-3', 'V2-4', 'V2-5', 'V2-6'].every((tag) => t11.includes(tag));
    record('docs/11 覆盖 V2-1…V2-6 六项变更', hasAllSix, '缺某一项');
    const projects = ['WuKongIM', 'WKongAI', 'MagicChat', 'Sigil', 'SKChat', 'Buzz', 'AICQ', 'A2A', 'Agentgateway'];
    const missingProjects = projects.filter((name) => !t12.includes(name));
    record(`docs/12 覆盖 ${projects.length} 个对标项目`, missingProjects.length === 0, missingProjects.join(', '));
    const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
    const sections = ['11', '12', '13', '14', '15', '16'];
    const missingSections = sections.filter((section) => !contract.includes(`## ${section}. `));
    record('契约含 v2 §11–§16 六节', missingSections.length === 0, missingSections.join(', '));
  }
}

// ───────────────────────── 汇总 ─────────────────────────
const passed = results.filter((r) => r.ok).length;
console.log(`\n交付物一致性验收：${passed}/${results.length} 通过\n`);
for (const r of results) {
  console.log(`${r.ok ? '✔' : '✘'} ${r.name}${r.ok || !r.detail ? '' : `\n    ↳ ${r.detail}`}`);
}

if (passed !== results.length) {
  console.error('\n验收未通过：请修正上面标 ✘ 的项。');
  process.exit(1);
}
console.log('\n全部通过。');
