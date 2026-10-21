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

// ───────────────────────── 11. v3 交付完整性（人格记忆 / 讨论模式 / 工具审批 / 一条命令验证）─────────────────────────
{
  const v3Modules = [
    ['services/orchestrator/src/persona-memory.mjs', 'services/orchestrator/test/persona-memory.test.mjs'],
    ['services/orchestrator/src/discussion-engine.mjs', 'services/orchestrator/test/discussion-engine.test.mjs'],
    ['services/orchestrator/src/tool-policy.mjs', 'services/orchestrator/test/tool-policy.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v3Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v3 模块与测试齐备（${v3Modules.length} 组）`, missing.length === 0, missing.join('; '));

  // v3 状态机：契约 + 实现两方一致（proto 尚未补，已在 docs/13 登记为待补）
  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v3Machines = {
    MEMORY_STAGE: ['WORKING', 'EPISODIC', 'SEMANTIC', 'ARCHIVED'],
    DISCUSSION_MODE: ['DISCUSS', 'AUDIT', 'REVIEW'],
    DISCUSSION_STATE: ['OPEN', 'IN_ROUND', 'SYNTHESIZING', 'CONCLUDED', 'ESCALATED', 'ABORTED'],
    STANCE: ['SUPPORT', 'OPPOSE', 'QUESTION', 'EVIDENCE'],
    POLICY_DECISION: ['AUTO', 'APPROVE', 'DENY']
  };
  for (const [machine, values] of Object.entries(v3Machines)) {
    const inContract = values.every((v) => contract.includes(v));
    const inSrc = values.every((v) => srcText.includes(`'${v}'`));
    record(`v3 状态机 ${machine} 契约↔实现一致（${values.length} 态）`, inContract && inSrc, `contract=${inContract} impl=${inSrc}`);
  }

  const quickstart = path.join(ROOT, 'scripts/quickstart.mjs');
  const pkg = JSON.parse(await read(path.join(ROOT, 'package.json')));
  record(
    '一条命令验证可用（scripts/quickstart.mjs + npm start）',
    (await exists(quickstart)) && typeof pkg.scripts?.start === 'string',
    '缺 quickstart 或 package.json start'
  );

  const doc13 = path.join(ROOT, 'docs/13-分层选型与落地映射.md');
  if (await exists(doc13)) {
    const t13 = await read(doc13);
    const projects = ['MagicChat', 'MindRoom', 'nekoclaw', 'imcodes', 'Xiajiao', 'AICQ'];
    const missingProjects = projects.filter((name) => !t13.includes(name));
    record(`docs/13 覆盖分层选型 ${projects.length} 个项目`, missingProjects.length === 0, missingProjects.join(', '));
  } else {
    record('docs/13 分层选型文档存在', false, '缺文件');
  }

  const v3Sections = ['17', '18', '19'];
  const missingSections = v3Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v3 §17–§19 三节', missingSections.length === 0, missingSections.join(', '));
}

// ───────────────────────── 12. v4「宿主型 IM」交付完整性 ─────────────────────────
{
  const v4Modules = [
    ['services/gateway/src/trusted-context.mjs', 'services/gateway/test/trusted-context.test.mjs'],
    ['services/orchestrator/src/presence-policy.mjs', 'services/orchestrator/test/presence-policy.test.mjs'],
    ['services/orchestrator/src/model-router.mjs', 'services/orchestrator/test/model-router.test.mjs'],
    ['services/messaging/src/turn-inbox.mjs', 'services/messaging/test/turn-inbox.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v4Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v4 模块与测试齐备（${v4Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v4Machines = {
    PRESENCE_MODE: ['ALWAYS', 'MENTION_ONLY', 'OBSERVE', 'OFF'],
    MODEL_TIER: ['LOCAL', 'CLOUD'],
    TURN_STATE: ['IDLE', 'RUNNING', 'CLOSED']
  };
  for (const [machine, values] of Object.entries(v4Machines)) {
    const inContract = values.every((v) => contract.includes(v));
    const inSrc = values.every((v) => srcText.includes(`'${v}'`));
    record(`v4 状态机 ${machine} 契约↔实现一致（${values.length} 态）`, inContract && inSrc, `contract=${inContract} impl=${inSrc}`);
  }

  const v4Sections = ['22', '23', '24', '25'];
  const missingSections = v4Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v4 §22–§25 四节', missingSections.length === 0, missingSections.join(', '));

  const doc14 = path.join(ROOT, 'docs/14-宿主型IM判据与机制落点.md');
  if (await exists(doc14)) {
    const t14 = await read(doc14);
    const projects = ['MagicChat', 'MindRoom', 'nekoclaw', 'imcodes', 'AICQ', 'Flopwire'];
    const missingProjects = projects.filter((name) => !t14.includes(name));
    record(`docs/14 覆盖宿主机制 ${projects.length} 个项目`, missingProjects.length === 0, missingProjects.join(', '));
    record('docs/14 写明「宿主 vs 机器人」判据', t14.includes('宿主') && t14.includes('机器人'), '缺判据表述');
  } else {
    record('docs/14 宿主型 IM 判据文档存在', false, '缺文件');
  }
}

// ───────────────────────── 13. v5「可信传达」交付完整性 ─────────────────────────
{
  const v5Modules = [
    ['services/identity/src/signed-envelope.mjs', 'services/identity/test/signed-envelope.test.mjs'],
    ['services/orchestrator/src/relay-summary.mjs', 'services/orchestrator/test/relay-summary.test.mjs'],
    ['services/orchestrator/src/confirmation-tier.mjs', 'services/orchestrator/test/confirmation-tier.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v5Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v5 模块与测试齐备（${v5Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v5Machines = {
    TOOL_TIER: ['PLANNER', 'CONFIRMATION'],
    RELAY_KIND: ['THIRD_PERSON_RELAY']
  };
  for (const [machine, values] of Object.entries(v5Machines)) {
    const inContract = values.every((v) => contract.includes(v));
    const inSrc = values.every((v) => srcText.includes(`'${v}'`));
    record(`v5 枚举 ${machine} 契约↔实现一致（${values.length} 值）`, inContract && inSrc, `contract=${inContract} impl=${inSrc}`);
  }

  const v5Sections = ['26', '27', '28'];
  const missingSections = v5Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v5 §26–§28 三节', missingSections.length === 0, missingSections.join(', '));

  const doc15 = path.join(ROOT, 'docs/15-四层技术栈映射与自研边界.md');
  if (await exists(doc15)) {
    const t15 = await read(doc15);
    const projects = ['Pisces', 'dsh-meeting', 'a2a-dm', 'Beacon', 'MalikClaw', 'ZLAgent', 'openvela'];
    const missingProjects = projects.filter((name) => !t15.includes(name));
    record(`docs/15 覆盖四层 ${projects.length} 个项目`, missingProjects.length === 0, missingProjects.join(', '));
    record('docs/15 明确「开源解决连接与执行，自研解决信任与治理」', t15.includes('信任') && t15.includes('治理'), '缺结论表述');
  } else {
    record('docs/15 四层技术栈映射文档存在', false, '缺文件');
  }
}

// ───────────────────────── 14. v6「持久与委托」交付完整性 ─────────────────────────
{
  const v6Modules = [
    ['services/messaging/src/agent-mailbox.mjs', 'services/messaging/test/agent-mailbox.test.mjs'],
    ['services/gateway/src/delegation.mjs', 'services/gateway/test/delegation.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v6Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v6 模块与测试齐备（${v6Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v6Codes = ['CAPACITY_EXCEEDED', 'CURSOR_INVALID', 'DUPLICATE_MESSAGE', 'SCOPE_ESCALATION', 'EXPIRY_EXCEEDED', 'HOP_LIMIT', 'PARENT_REVOKED'];
  const missingCodes = v6Codes.filter((code) => !contract.includes(code) || !srcText.includes(`'${code}'`));
  record(`v6 关键错误码契约↔实现一致（${v6Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  const v6Sections = ['29', '30'];
  const missingSections = v6Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v6 §29–§30 两节', missingSections.length === 0, missingSections.join(', '));

  const doc16 = path.join(ROOT, 'docs/16-三层技术栈与平台接入边界.md');
  if (await exists(doc16)) {
    const t16 = await read(doc16);
    const projects = ['Sigil', 'Agent-IM', 'im-for-agents', 'WKongAI', 'Greft', 'AgentHub', 'AGORA'];
    const platforms = ['微信', '飞书', '钉钉', 'WhatsApp'];
    const missingProjects = projects.filter((name) => !t16.includes(name));
    const missingPlatforms = platforms.filter((name) => !t16.includes(name));
    record(`docs/16 覆盖协议/编排层 ${projects.length} 个项目`, missingProjects.length === 0, missingProjects.join(', '));
    record(`docs/16 覆盖主流平台边界（${platforms.length} 个）`, missingPlatforms.length === 0, missingPlatforms.join(', '));
  } else {
    record('docs/16 三层技术栈与平台边界文档存在', false, '缺文件');
  }
}

// ───────────────────────── 15. v7「可替换底座」交付完整性 ─────────────────────────
{
  const v7Modules = [
    ['services/messaging/src/im-adapter.mjs', 'services/messaging/test/im-adapter.test.mjs'],
    ['services/orchestrator/src/agent-correspondence.mjs', 'services/orchestrator/test/agent-correspondence.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v7Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v7 模块与测试齐备（${v7Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v7Codes = ['CAPABILITY_MISSING', 'ADAPTER_EXISTS', 'PLATFORM_ERROR', 'EVENT_INVALID', 'ALREADY_DECIDED'];
  const missingCodes = v7Codes.filter((code) => !contract.includes(code) || !srcText.includes(`'${code}'`));
  record(`v7 关键错误码契约↔实现一致（${v7Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));
  const v7Caps = ['UNREAD_SHAPING', 'CUSTOM_MESSAGE', 'AGENT_MEMBERSHIP', 'CARD_CALLBACK'];
  const missingCaps = v7Caps.filter((cap) => !contract.includes(cap) || !srcText.includes(`'${cap}'`));
  record(`v7 适配器能力枚举契约↔实现一致（${v7Caps.length} 个）`, missingCaps.length === 0, missingCaps.join(', '));

  const v7Sections = ['31', '32'];
  const missingSections = v7Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v7 §31–§32 两节', missingSections.length === 0, missingSections.join(', '));

  const doc17 = path.join(ROOT, 'docs/17-三条路线与分层决策.md');
  if (await exists(doc17)) {
    const t17 = await read(doc17);
    const projects = ['agentcomms', 'masumi', 'AgentHub', 'Xiajiao', 'AstrBot', 'WKongAI'];
    const missingProjects = projects.filter((name) => !t17.includes(name));
    record(`docs/17 覆盖三条路线 ${projects.length} 个项目`, missingProjects.length === 0, missingProjects.join(', '));
    record('docs/17 写明底座反转与补偿', t17.includes('反转') && (t17.includes('补偿') || t17.includes('适配')), '缺反转/补偿表述');
  } else {
    record('docs/17 三条路线与分层决策文档存在', false, '缺文件');
  }

  const doc02 = await read(path.join(ROOT, 'docs/02-技术选型与框架.md'));
  record('docs/02 记录 v7 底座决策反转（§19）', doc02.includes('## 19.') && doc02.includes('v7'), '缺少 §19 反转小节');
}

// ───────────────────────── 16. v8「Context 主权」交付完整性 ─────────────────────────
{
  const v8Modules = [
    ['services/orchestrator/src/person-runtime.mjs', 'services/orchestrator/test/person-runtime.test.mjs'],
    ['services/orchestrator/src/context-lock.mjs', 'services/orchestrator/test/context-lock.test.mjs'],
    ['services/gateway/src/scoped-grant.mjs', 'services/gateway/test/scoped-grant.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v8Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v8 模块与测试齐备（${v8Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v8Codes = ['SECRET_IN_LAYER', 'VERSION_MISMATCH', 'BUNDLE_INVALID', 'LOCK_IMMUTABLE', 'CONFLICT_PENDING', 'SCOPE_NOT_GRANTED', 'ALREADY_USED'];
  const missingCodes = v8Codes.filter((code) => !contract.includes(code) || !srcText.includes(`'${code}'`));
  record(`v8 关键错误码契约↔实现一致（${v8Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));
  const v8Enums = {
    STATE_LAYER: ['PERSONAL', 'TASK', 'WORKING'],
    GRANT_SCOPE: ['view_summary', 'view_original', 'reply'],
    LOCK_STATE: ['PENDING', 'LOCKED', 'CONFLICT']
  };
  for (const [name, values] of Object.entries(v8Enums)) {
    const inContract = values.every((v) => contract.includes(v));
    const inSrc = values.every((v) => srcText.includes(`'${v}'`));
    record(`v8 枚举 ${name} 契约↔实现一致（${values.length} 值）`, inContract && inSrc, `contract=${inContract} impl=${inSrc}`);
  }

  const v8Sections = ['33', '34', '35'];
  const missingSections = v8Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v8 §33–§35 三节', missingSections.length === 0, missingSections.join(', '));

  const doc20 = path.join(ROOT, 'docs/20-Context飞轮与Personal-Runtime.md');
  if (await exists(doc20)) {
    const t20 = await read(doc20);
    const required = ['Context', '飞轮', 'PACT', 'PAP', 'Runtime'];
    const missingKeywords = required.filter((word) => !t20.includes(word));
    record('docs/20 覆盖 Context 飞轮与授权协议', missingKeywords.length === 0, missingKeywords.join(', '));
  } else {
    record('docs/20 Context 飞轮文档存在', false, '缺文件');
  }
}

// ───────────────────────── 17. v0.0.1 产品版「可付费准入」交付完整性 ─────────────────────────
{
  const v001Modules = [
    ['services/account/src/account.mjs', 'services/account/test/account.test.mjs'],
    ['services/payment/src/wallet.mjs', 'services/payment/test/wallet.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v001Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v0.0.1 账户/钱包模块与测试齐备（${v001Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v001Codes = ['REALNAME_REQUIRED', 'OTP_EXPIRED', 'OTP_ATTEMPTS_EXCEEDED', 'SESSION_EXPIRED', 'INSUFFICIENT_FUNDS', 'REFUND_WINDOW_CLOSED', 'DAILY_CAP_EXCEEDED'];
  const missingCodes = v001Codes.filter((code) => !contract.includes(code) || !srcText.includes(`'${code}'`));
  record(`v0.0.1 关键错误码契约↔实现一致（${v001Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  const v001Sections = ['36', '37'];
  const missingSections = v001Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v0.0.1 §36–§37 两节', missingSections.length === 0, missingSections.join(', '));

  // 合规硬要求：实名证件号只能以 hash 形式出现
  const accountSrc = await exists(path.join(ROOT, 'services/account/src/account.mjs'))
    ? await read(path.join(ROOT, 'services/account/src/account.mjs'))
    : '';
  record(
    '账户模块做了证件号哈希（不存明文）',
    /sha256|createHash/.test(accountSrc) && /idNumberHash|maskedTail/.test(accountSrc),
    '未发现 hash/掩码实现'
  );

  const doc21 = path.join(ROOT, 'docs/21-v0.0.1-产品版范围与验收.md');
  if (await exists(doc21)) {
    const t21 = await read(doc21);
    const required = ['实名', '退款', '验收', '内测'];
    const missingKeywords = required.filter((word) => !t21.includes(word));
    record('docs/21 覆盖 v0.0.1 范围与验收', missingKeywords.length === 0, missingKeywords.join(', '));
  } else {
    record('docs/21 v0.0.1 范围文档存在', false, '缺文件');
  }
}

// ───────────────────────── 18. v0.0.2 内测就绪（短信 + 争议）交付完整性 ─────────────────────────
{
  const v002Modules = [
    ['services/account/src/sms-provider.mjs', 'services/account/test/sms-provider.test.mjs'],
    ['services/account/src/otp-delivery.mjs', 'services/account/test/otp-delivery.test.mjs'],
    ['services/payment/src/dispute.mjs', 'services/payment/test/dispute.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v002Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v0.0.2 短信/争议模块与测试齐备（${v002Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v002Codes = ['OTP_COOLDOWN', 'OTP_DAILY_CAP', 'OTP_DELIVERY_FAILED', 'NOT_ELIGIBLE', 'DUPLICATE_OPEN', 'SMS_FAILED'];
  const missingCodes = v002Codes.filter((code) => !contract.includes(code) || !srcText.includes(`'${code}'`));
  record(`v0.0.2 关键错误码契约↔实现一致（${v002Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  const v002Sections = ['38', '39'];
  const missingSections = v002Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v0.0.2 §38–§39 两节', missingSections.length === 0, missingSections.join(', '));

  // 合规硬要求：争议退款必须经账本幂等键，而不是直接改余额
  const disputeSrc = await exists(path.join(ROOT, 'services/payment/src/dispute.mjs'))
    ? await read(path.join(ROOT, 'services/payment/src/dispute.mjs'))
    : '';
  record('争议退款走账本幂等键（refund-dispute:）', disputeSrc.includes('refund-dispute:'), '未发现幂等键实现');

  const doc22 = path.join(ROOT, 'docs/22-v0.0.2-内测就绪-短信与争议.md');
  if (await exists(doc22)) {
    const t22 = await read(doc22);
    const required = ['短信', '争议', '冷却', '验收'];
    const missingKeywords = required.filter((word) => !t22.includes(word));
    record('docs/22 覆盖短信通道与退款争议', missingKeywords.length === 0, missingKeywords.join(', '));
  } else {
    record('docs/22 v0.0.2 范围文档存在', false, '缺文件');
  }
}

// ───────────────────────── 19. v0.0.3 审核链交付完整性 ─────────────────────────
{
  const v003Modules = [['services/moderation/src/moderation.mjs', 'services/moderation/test/moderation.test.mjs']];
  const missing = [];
  for (const [impl, test] of v003Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v0.0.3 审核模块与测试齐备（${v003Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v003Codes = ['HUMAN_PENDING', 'SELF_REVIEW_FORBIDDEN', 'APPEAL_WINDOW_CLOSED', 'CONTENT_REQUIRED', 'APPEAL_GRANTED'];
  const missingCodes = v003Codes.filter((code) => !contract.includes(code) || !srcText.includes(`'${code}'`));
  record(`v0.0.3 关键错误码/状态契约↔实现一致（${v003Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  record('契约含 v0.0.3 §40 审核链', contract.includes('## 40. `services/moderation/src/moderation.mjs`'), '缺 §40');
  record('v0.0.3 明确 fail-closed 原则', contract.includes('fail-closed'), '缺 fail-closed 表述');

  const moderationSrc = await exists(path.join(ROOT, 'services/moderation/src/moderation.mjs'))
    ? await read(path.join(ROOT, 'services/moderation/src/moderation.mjs'))
    : '';
  record('审核模块对机审异常做了 fail-closed 处理', /HUMAN_PENDING/.test(moderationSrc) && /catch|machineUnavailable/.test(moderationSrc), '未发现 fail-closed 分支');

  const doc23 = path.join(ROOT, 'docs/23-v0.0.3-审核链.md');
  if (await exists(doc23)) {
    const t23 = await read(doc23);
    const required = ['机审', '人审', '申诉'];
    const missingKeywords = required.filter((word) => !t23.includes(word));
    const failClosed = t23.includes('fail-closed') || t23.includes('fail closed') || t23.includes('fail–closed');
    record('docs/23 覆盖审核链与申诉', missingKeywords.length === 0, missingKeywords.join(', '));
    record('docs/23 明确 fail-closed 原则', failClosed, '缺 fail-closed 表述');
  } else {
    record('docs/23 v0.0.3 审核链文档存在', false, '缺文件');
  }
}

// ───────────────────────── 20. v0.0.4 可观测性交付完整性 ─────────────────────────
{
  const v004Modules = [
    ['services/observability/src/metrics.mjs', 'services/observability/test/metrics.test.mjs'],
    ['services/observability/src/trace.mjs', 'services/observability/test/trace.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v004Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v0.0.4 指标/追溯模块与测试齐备（${v004Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v004Codes = ['PAID_SENT', 'MODERATION_PASS', 'REFUND', 'OTP_FAILED', 'complete', 'missing'];
  const missingCodes = v004Codes.filter((code) => !contract.includes(code) && !srcText.includes(code));
  record(`v0.0.4 关键指标/字段契约↔实现存在（${v004Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  const v004Sections = ['41', '42'];
  const missingSections = v004Sections.filter((section) => !contract.includes(`## ${section}. `));
  record('契约含 v0.0.4 §41–§42 两节', missingSections.length === 0, missingSections.join(', '));
  record('v0.0.4 明确"缺环显式"原则', contract.includes('complete=false') || contract.includes('缺环'), '缺缺环表述');

  const doc24 = path.join(ROOT, 'docs/24-v0.0.4-可观测与运营.md');
  if (await exists(doc24)) {
    const t24 = await read(doc24);
    const required = ['漏斗', '告警', '追溯'];
    const missingKeywords = required.filter((word) => !t24.includes(word));
    record('docs/24 覆盖漏斗/告警/追溯', missingKeywords.length === 0, missingKeywords.join(', '));
  } else {
    record('docs/24 v0.0.4 可观测文档存在', false, '缺文件');
  }
}

// ───────────────────────── 21. v0.0.5 内测保障交付完整性 ─────────────────────────
{
  const v005Modules = [
    ['services/resilience/src/provider-guard.mjs', 'services/resilience/test/provider-guard.test.mjs'],
    ['services/persistence/src/backup.mjs', 'services/persistence/test/backup.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v005Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v0.0.5 熔断/备份模块与测试齐备（${v005Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v005Codes = ['CIRCUIT_OPEN', 'HALF_OPEN', 'FALLBACK_FAILED', 'usedFallback', 'HASH_MISMATCH', 'sha256'];
  const missingCodes = v005Codes.filter((code) => !contract.includes(code) && !srcText.includes(code));
  record(`v0.0.5 关键错误码/字段契约↔实现存在（${v005Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  record('契约含 v0.0.5 §43–§44 两节', contract.includes('## 43. `services/resilience') && contract.includes('## 44. `services/persistence'), '缺 §43/§44');
  record('v0.0.5 明确 restore 不自动应用', contract.includes('不自动应用'), '缺"不自动应用"约束');

  const doc25 = path.join(ROOT, 'docs/25-v0.0.5-内测保障.md');
  if (await exists(doc25)) {
    const t25 = await read(doc25);
    const required = ['降级', '备份', '恢复', '演练'];
    const missingKeywords = required.filter((word) => !t25.includes(word));
    record('docs/25 覆盖降级与备份恢复', missingKeywords.length === 0, missingKeywords.join(', '));
  } else {
    record('docs/25 v0.0.5 内测保障文档存在', false, '缺文件');
  }
}

// ───────────────────────── 22. v0.0.6 验收工具化交付完整性 ─────────────────────────
{
  const v006Modules = [
    ['services/acceptance/src/preflight.mjs', 'services/acceptance/test/preflight.test.mjs'],
    ['services/acceptance/src/runner.mjs', 'services/acceptance/test/runner.test.mjs']
  ];
  const missing = [];
  for (const [impl, test] of v006Modules) {
    if (!(await exists(path.join(ROOT, impl)))) missing.push(impl);
    if (!(await exists(path.join(ROOT, test)))) missing.push(test);
  }
  record(`v0.0.6 预检/执行器模块与测试齐备（${v006Modules.length} 组）`, missing.length === 0, missing.join('; '));

  const contract = await read(path.join(ROOT, 'contracts/INTERFACES.md'));
  const srcText = (
    await Promise.all(
      (await walk(path.join(ROOT, 'services'), [], (f) => f.endsWith('.mjs') && !rel(f).includes('/test/'))).map(read)
    )
  ).join('\n');
  const v006Codes = ['TARGET_UNREACHABLE', 'AUTH_FAILED', 'STORE_NOT_DURABLE', 'PROVIDER_NOT_CONFIGURED', 'MANUAL', 'SKIP'];
  const missingCodes = v006Codes.filter((code) => !contract.includes(code) && !srcText.includes(code));
  record(`v0.0.6 关键状态/错误码契约↔实现存在（${v006Codes.length} 个）`, missingCodes.length === 0, missingCodes.join(', '));

  record('契约含 v0.0.6 §45–§46 两节', contract.includes('## 45. `services/acceptance/src/preflight.mjs`') && contract.includes('## 46. `services/acceptance/src/runner.mjs`'), '缺 §45/§46');
  record('v0.0.6 明确不可自动化标 MANUAL', contract.includes('MANUAL'), '缺 MANUAL 表述');

  const doc26 = path.join(ROOT, 'docs/26-v0.0.6-真实环境验收Runbook.md');
  if (await exists(doc26)) {
    const t26 = await read(doc26);
    const required = ['预检', '验收', 'Gate', '人工'];
    const missingKeywords = required.filter((word) => !t26.includes(word));
    record('docs/26 覆盖预检/验收/人工 Gate', missingKeywords.length === 0, missingKeywords.join(', '));
  } else {
    record('docs/26 v0.0.6 验收 Runbook 存在', false, '缺文件');
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
